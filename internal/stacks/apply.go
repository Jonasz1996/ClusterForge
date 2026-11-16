package stacks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/cronjobs"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Acties van een stacktaak.
const (
	// ActionApply brengt de stacks in hun gewenste stand.
	ActionApply = "apply"
	// ActionStart en ActionStop zetten de gewenste stand en passen toe.
	ActionStart = "start"
	ActionStop  = "stop"
	// ActionRestart herstart waar de stack draait.
	ActionRestart = "restart"
	// ActionUpdate haalt nieuwe images en maakt de containers opnieuw
	// waar nodig; de reservenodes halen alleen de images.
	ActionUpdate = "update"
	// ActionDown haalt de containers weg (docker compose down) op elke
	// node; data en bestanden blijven, de gewenste stand wordt stopped.
	ActionDown = "down"
	// ActionLists zet alleen de lijst voor keepalived en de syncs.
	ActionLists = "lists"
)

// Params zijn de parameters van een stacktaak.
type Params struct {
	Action string `json:"action"`
	// StackIDs beperkt de taak; leeg is elke stack van het cluster.
	StackIDs []uuid.UUID `json:"stack_ids,omitempty"`
	// NodeID beperkt de taak tot één node.
	NodeID   *uuid.UUID `json:"node_id,omitempty"`
	Services []string   `json:"services,omitempty"`
	Title    string     `json:"title,omitempty"`
}

// Enqueue zet een stacktaak in de wachtrij; per cluster loopt er één taak
// tegelijk.
func (s *Service) Enqueue(ctx context.Context, actor events.Actor, clusterID uuid.UUID, p Params) (uuid.UUID, error) {
	c, err := s.cluster(ctx, clusterID)
	if err != nil {
		return uuid.Nil, err
	}
	switch p.Action {
	case ActionApply, ActionStart, ActionStop, ActionRestart, ActionUpdate, ActionDown, ActionLists:
	default:
		return uuid.Nil, &FieldError{"onbekende actie " + p.Action}
	}
	for _, sv := range p.Services {
		if !protocol.ValidContainerName(sv) {
			return uuid.Nil, &FieldError{"ongeldige dienst " + sv}
		}
	}
	var names []string
	for _, id := range p.StackIDs {
		st, err := s.Get(ctx, id)
		if err != nil {
			return uuid.Nil, err
		}
		if st.ClusterID != clusterID {
			return uuid.Nil, ErrNotFound
		}
		names = append(names, st.Name)
	}
	if p.Action == ActionStart || p.Action == ActionStop || p.Action == ActionDown {
		if len(p.StackIDs) == 0 {
			return uuid.Nil, &FieldError{"kies de stacks om te starten of te stoppen"}
		}
		want := "running"
		if p.Action != ActionStart {
			want = "stopped"
		}
		if _, err := s.pool.Exec(ctx, `UPDATE docker_stacks SET desired = $2, updated_at = now() WHERE id = ANY($1)`, p.StackIDs, want); err != nil {
			return uuid.Nil, err
		}
	}
	if p.Title == "" {
		what := "alle stacks"
		if len(names) > 0 {
			what = strings.Join(names, ", ")
		}
		p.Title = fmt.Sprintf("Containers %s: %s (%s)", actionText(p.Action), what, c.Name)
	}
	cid := clusterID
	j, err := s.runner.EnqueueForCluster(ctx, jobs.Spec{Kind: Kind, Title: p.Title, ClusterID: &cid, NodeID: p.NodeID, Actor: actor, Params: p})
	if err != nil {
		return uuid.Nil, err
	}
	s.event(ctx, actor, clusterID, clusterID.String(), "stack.action", map[string]any{"action": p.Action, "stacks": names, "job_id": j.ID.String()})
	s.runner.Kick()
	return j.ID, nil
}

func actionText(a string) string {
	switch a {
	case ActionStart:
		return "starten"
	case ActionStop:
		return "stoppen"
	case ActionRestart:
		return "herstarten"
	case ActionUpdate:
		return "bijwerken"
	case ActionDown:
		return "weghalen"
	case ActionLists:
		return "lijst bijwerken"
	}
	return "toepassen"
}

// plan is wat de taak op één node doet.
type plan struct {
	node Node
	run  protocol.StackRun
	// stacks: naam → id, om het resultaat te bewaren.
	stacks map[string]Stack
}

// plans maakt per node de run. De VIP-houder komt als laatste, zodat een
// fout eerst op een reservenode blijkt.
func (s *Service) plans(ctx context.Context, clusterID uuid.UUID, p Params) (Cluster, []plan, []Stack, error) {
	c, err := s.cluster(ctx, clusterID)
	if err != nil {
		return c, nil, nil, err
	}
	nodes, err := s.nodes(ctx, c)
	if err != nil {
		return c, nil, nil, err
	}
	all, err := s.listStacks(ctx, clusterID)
	if err != nil {
		return c, nil, nil, err
	}
	var plans []plan
	for _, n := range nodes {
		if p.NodeID != nil && *p.NodeID != n.ID {
			continue
		}
		pl := plan{node: n, stacks: map[string]Stack{}}
		var lines []protocol.StackListLine
		for _, st := range all {
			if !present(st, n) {
				continue
			}
			lines = append(lines, protocol.StackListLine{Name: st.Name, Dir: st.Dir, ComposeFiles: st.ComposeFiles, Placement: placementFor(st, c, len(nodes)), Critical: st.Critical})
			if p.Action == ActionLists || (len(p.StackIDs) > 0 && !slices.Contains(p.StackIDs, st.ID)) {
				continue
			}
			want, pull := wantFor(st, should(st, n, c, len(nodes)), p.Action)
			if want == "" {
				continue
			}
			files, err := s.openFiles(st)
			if err != nil {
				return c, nil, nil, fmt.Errorf("%s: bestanden openen: %w", st.Name, err)
			}
			pl.run.Stacks = append(pl.run.Stacks, protocol.StackApply{
				Name: st.Name, Dir: st.Dir, ComposeFiles: st.ComposeFiles, Files: files, Want: want, Pull: pull, Services: p.Services,
			})
			pl.stacks[st.Name] = st
		}
		list := protocol.FormatStackList(lines)
		pl.run.List = &list
		plans = append(plans, pl)
	}
	slices.SortStableFunc(plans, func(a, b plan) int {
		switch {
		case a.node.HoldsVIP == b.node.HoldsVIP:
			return 0
		case a.node.HoldsVIP:
			return 1
		}
		return -1
	})
	return c, plans, all, nil
}

// placementFor is de plaatsing in de lijst van keepalived: zonder VIP is vip
// hetzelfde als alle.
func placementFor(st Stack, c Cluster, nodeCount int) string {
	if st.Placement == protocol.PlaceVIP && (c.VIP == "" || nodeCount <= 1) {
		return protocol.PlaceAll
	}
	return st.Placement
}

// wantFor zegt wat een node met een stack doet.
func wantFor(st Stack, runsHere bool, action string) (string, bool) {
	running := st.Desired == "running"
	switch action {
	case ActionApply, ActionStart, ActionStop:
		if running && runsHere {
			return protocol.StackUp, false
		}
		return protocol.StackStop, false
	case ActionRestart:
		if running && runsHere {
			return protocol.StackRestart, false
		}
		return "", false
	case ActionUpdate:
		if running && runsHere {
			return protocol.StackUp, true
		}
		return protocol.StackFiles, true
	case ActionDown:
		return protocol.StackDown, false
	}
	return "", false
}

func (s *Service) apply(ctx context.Context, j *jobs.Job) error {
	var p Params
	if err := j.Decode(&p); err != nil {
		return err
	}
	if j.ClusterID == nil {
		return errors.New("stacktaak zonder cluster")
	}
	c, plans, all, err := s.plans(ctx, *j.ClusterID, p)
	if err != nil {
		return err
	}
	if err := j.Step(ctx, "plan", func(ctx context.Context, st *jobs.Step) error {
		if c.VIP != "" {
			owner := "niemand"
			for _, pl := range plans {
				if pl.node.HoldsVIP {
					owner = pl.node.Hostname
				}
			}
			st.Logf("VIP %s staat op %s; die node komt als laatste.", c.VIP, owner)
		}
		for _, pl := range plans {
			var parts []string
			for _, a := range pl.run.Stacks {
				parts = append(parts, a.Name+": "+a.Want)
			}
			if len(parts) == 0 {
				parts = []string{"alleen de lijst voor keepalived"}
			}
			st.Logf("%s: %s", pl.node.Hostname, strings.Join(parts, ", "))
		}
		if len(plans) == 0 {
			return errors.New("dit cluster heeft geen nodes")
		}
		return nil
	}); err != nil {
		return err
	}
	var failed []string
	for _, pl := range plans {
		pl := pl
		name := pl.node.Hostname + ": stacks"
		err := j.Step(ctx, name, func(ctx context.Context, st *jobs.Step) error {
			switch {
			case !pl.node.Supported:
				return fmt.Errorf("agent te oud (protocol %d, %d nodig); Nodes → Agents bijwerken", pl.node.Protocol, protocol.StacksSince)
			case !pl.node.Online:
				return errors.New("de node is offline")
			}
			spec := protocol.RunSpec{ID: fmt.Sprintf("stack-%s-%s", j.ID.String()[:8], pl.node.ID.String()[:8]), Kind: protocol.RunStack,
				Title: j.Title, TimeoutSeconds: 3 * 3600, Stack: &pl.run}
			res, err := s.Run.Do(ctx, st, pl.node.ID, spec)
			if err != nil {
				return err
			}
			s.saveReports(context.WithoutCancel(ctx), pl, res)
			if res.State != protocol.RunOK {
				return errors.New(noderun.Describe(res))
			}
			return nil
		})
		if err != nil {
			failed = append(failed, pl.node.Hostname)
			// Mislukt een reservenode, dan de VIP-node niet aanraken.
			if !pl.node.HoldsVIP {
				break
			}
		}
	}
	syncErr := j.Step(ctx, "syncs", func(ctx context.Context, st *jobs.Step) error {
		return s.ensureSyncs(ctx, st, c, plans, all)
	})
	if len(failed) > 0 {
		return fmt.Errorf("niet gelukt op %s", strings.Join(failed, ", "))
	}
	return syncErr
}

func (s *Service) saveReports(ctx context.Context, pl plan, res protocol.RunStatus) {
	now := time.Now()
	for _, r := range res.Stacks {
		st, ok := pl.stacks[r.Name]
		if !ok {
			continue
		}
		if r.Result == protocol.StackResultFailed {
			_, _ = s.pool.Exec(ctx, `INSERT INTO docker_stack_nodes (stack_id, node_id, error, failed_at) VALUES ($1, $2, $3, $4)
				ON CONFLICT (stack_id, node_id) DO UPDATE SET error = $3, failed_at = $4`, st.ID, pl.node.ID, r.Problem, now)
			continue
		}
		_, _ = s.pool.Exec(ctx, `INSERT INTO docker_stack_nodes (stack_id, node_id, revision, applied_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (stack_id, node_id) DO UPDATE SET revision = $3, applied_at = $4, error = '', failed_at = NULL`, st.ID, pl.node.ID, st.Revision, now)
	}
}

// ensureSyncs zet de syncs van de stacks met plaatsing vip (van de VIP-node
// naar de andere nodes, met de containers kort gepauzeerd) en haalt syncs
// weg van stacks die er geen meer nodig hebben.
func (s *Service) ensureSyncs(ctx context.Context, st *jobs.Step, c Cluster, plans []plan, all []Stack) error {
	if s.Syncs == nil {
		return nil
	}
	nodeCount := len(plans)
	want := map[string]bool{}
	var syncs []cronjobs.TemplateSync
	if c.VIP != "" && nodeCount > 1 {
		for _, sk := range all {
			if sk.Placement != protocol.PlaceVIP {
				continue
			}
			for i, p := range sk.SyncPaths {
				name := SyncName(sk.Name, i)
				want[name] = true
				syncs = append(syncs, cronjobs.TemplateSync{
					Name: name, Schedule: sk.SyncSchedule, Source: p + "/", Target: p + "/",
					Before: protocol.SyncPausePrefix + sk.Name, Alert: true, TimeoutSeconds: 3600,
				})
			}
		}
	}
	if len(syncs) > 0 {
		s.Syncs.EnsureTemplateSyncs(ctx, c.ID, syncs)
		st.Logf("%d syncs van de VIP-node naar de andere nodes staan in Cronjobs (namen %s…)", len(syncs), SyncPrefix)
	}
	rows, err := s.pool.Query(ctx, `SELECT id, name FROM cron_jobs WHERE cluster_id = $1 AND name LIKE 'cf-stack-%'`, c.ID)
	if err != nil {
		return err
	}
	type job struct {
		id   uuid.UUID
		name string
	}
	var stale []job
	for rows.Next() {
		var jb job
		if err := rows.Scan(&jb.id, &jb.name); err != nil {
			rows.Close()
			return err
		}
		if !want[jb.name] {
			stale = append(stale, jb)
		}
	}
	rows.Close()
	for _, jb := range stale {
		if err := s.Syncs.DeleteJob(ctx, events.System(), jb.id); err != nil {
			st.Logf("sync %s weghalen mislukte: %v", jb.name, err)
			continue
		}
		st.Logf("sync %s weggehaald (niet meer nodig)", jb.name)
	}
	return nil
}

// SyncName is de naam van de i-de sync van een stack.
func SyncName(stack string, i int) string {
	if i == 0 {
		return SyncPrefix + stack
	}
	return fmt.Sprintf("%s%s-%d", SyncPrefix, stack, i+1)
}
