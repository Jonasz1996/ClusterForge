package cronjobs

// Beheerde cronjobs (fase 6, mijlpaal 3). Een job hoort bij een cluster; de
// server stuurt elke node de set jobs die op hem horen (CronApply), en de
// agent stuurt de runs terug (TypeCronRuns). Zie internal/agent/cronmanaged.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/cronexpr"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Grenzen van een job.
const (
	MaxJobCommand  = 8000
	MinJobTimeout  = 60
	MaxJobTimeout  = 24 * 3600
	maxJobName     = 80
	defaultTimeout = 3600
)

var jobUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// JobInput is een job zoals een beheerder hem invult.
type JobInput struct {
	Name           string
	Schedule       string
	RunOn          string
	NodeID         *uuid.UUID
	User           string
	Command        string
	TimeoutSeconds int
	Enabled        bool
	// Retries is hoe vaak een mislukte run herhaalt, 0 tot 3.
	Retries int
	// Alert: een mislukte run opent een incident.
	Alert bool
	// Kind is command (leeg) of sync; een sync heeft Sync in plaats van
	// Command.
	Kind string
	Sync *SyncInput
}

// InvalidError is een fout in wat de beheerder invulde, bij een veld.
type InvalidError struct {
	Field string
	Msg   string
}

func (e *InvalidError) Error() string { return e.Msg }

// ErrJobNotFound: de job bestaat niet.
var ErrJobNotFound = errors.New("cronjob niet gevonden")

// ScheduleInfo is een schema in gewone taal met de volgende runs.
type ScheduleInfo struct {
	Describe   string
	OnCalendar string
	Next       []time.Time
}

// CheckSchedule leest een schema voor een beheerde job.
func (s *Service) CheckSchedule(schedule string) (ScheduleInfo, error) {
	sc, err := cronexpr.Parse(schedule)
	if err != nil {
		return ScheduleInfo{}, &InvalidError{"schedule", err.Error()}
	}
	cal, err := sc.OnCalendar()
	if err != nil {
		return ScheduleInfo{}, &InvalidError{"schedule", err.Error()}
	}
	info := ScheduleInfo{Describe: sc.Describe(), OnCalendar: cal}
	t := s.Now().In(s.loc())
	for range 5 {
		t = sc.Next(t)
		if t.IsZero() {
			break
		}
		info.Next = append(info.Next, t)
	}
	return info, nil
}

func (s *Service) loc() *time.Location {
	if s.Location != nil {
		return s.Location
	}
	return time.Local
}

// validate controleert een job in een cluster.
func (s *Service) validate(ctx context.Context, q *store.Queries, clusterID uuid.UUID, in *JobInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Schedule = strings.TrimSpace(in.Schedule)
	in.User = strings.TrimSpace(in.User)
	in.Command = strings.TrimRight(in.Command, " \t\r\n")
	if in.User == "" {
		in.User = "root"
	}
	switch in.Kind {
	case "", protocol.CronKindCommand:
		in.Kind, in.Sync = protocol.CronKindCommand, nil
	case protocol.CronKindSync:
		if err := validateSync(ctx, q, clusterID, in); err != nil {
			return err
		}
	default:
		return &InvalidError{"kind", "kies command of sync"}
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = defaultTimeout
	}
	switch {
	case in.Name == "" || utf8.RuneCountInString(in.Name) > maxJobName || strings.ContainsAny(in.Name, "\n\r\t"):
		return &InvalidError{"name", fmt.Sprintf("een naam van 1 tot %d tekens, op één regel", maxJobName)}
	case !jobUser.MatchString(in.User):
		return &InvalidError{"user", "een gebruikersnaam zoals root of www-data"}
	case in.Kind == protocol.CronKindCommand && strings.TrimSpace(in.Command) == "":
		return &InvalidError{"command", "geen commando"}
	case len(in.Command) > MaxJobCommand:
		return &InvalidError{"command", fmt.Sprintf("het commando is langer dan %d tekens; zet het in een script op de node", MaxJobCommand)}
	case in.TimeoutSeconds < MinJobTimeout || in.TimeoutSeconds > MaxJobTimeout:
		return &InvalidError{"timeout_seconds", "een tijdslimiet tussen 1 minuut en 24 uur"}
	case in.Retries < 0 || in.Retries > 3:
		return &InvalidError{"retries", "herhalen kan 0 tot 3 keer"}
	}
	if _, err := s.CheckSchedule(in.Schedule); err != nil {
		return err
	}
	switch in.RunOn {
	case protocol.CronOnVIP:
		in.NodeID = nil
		vips, err := q.ListCronVIPs(ctx)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(vips, func(v store.ListCronVIPsRow) bool { return v.ClusterID == clusterID }) {
			return &InvalidError{"run_on", "dit cluster heeft geen VIP; kies een vaste node of alle nodes"}
		}
	case protocol.CronOnNode:
		if in.NodeID == nil {
			return &InvalidError{"node_id", "kies de node"}
		}
		nodes, err := q.ListCronCandidateNodes(ctx)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(nodes, func(n store.ListCronCandidateNodesRow) bool {
			return n.ID == *in.NodeID && n.ClusterID != nil && *n.ClusterID == clusterID
		}) {
			return &InvalidError{"node_id", "die node hoort niet bij dit cluster of heeft geen agent"}
		}
	case protocol.CronOnAll:
		in.NodeID = nil
	default:
		return &InvalidError{"run_on", "kies vip, node of all"}
	}
	return nil
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func jobPayload(j store.CronJob, cluster string) map[string]any {
	p := map[string]any{
		"name": j.Name, "cluster": cluster, "schedule": j.Schedule, "run_on": j.RunOn, "user": j.UserName,
		"command": j.Command, "timeout_seconds": j.TimeoutSeconds, "enabled": j.Enabled, "revision": j.Revision,
		"retries": j.Retries, "alert": j.Alert, "kind": j.Kind,
	}
	if j.NodeID != nil {
		p["node_id"] = j.NodeID.String()
	}
	if j.Kind == protocol.CronKindSync {
		delete(p, "command")
		p["source"], p["target"], p["delete"], p["exclude"], p["bwlimit_mb"] = j.SyncSource, j.SyncTarget, j.SyncDelete, j.SyncExclude, j.SyncBwlimitMb
		if j.SyncToCluster != nil {
			p["to_cluster_id"] = j.SyncToCluster.String()
		}
	}
	return p
}

// CreateJob maakt een job en zet hem meteen op de nodes.
func (s *Service) CreateJob(ctx context.Context, actor events.Actor, createdBy *uuid.UUID, clusterID uuid.UUID, clusterName string, in JobInput) (store.CronJob, error) {
	var job store.CronJob
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := s.validate(ctx, q, clusterID, &in); err != nil {
			return err
		}
		var err error
		src, dst, to, del, ex, bw := syncParams(in)
		job, err = q.InsertCronJob(ctx, store.InsertCronJobParams{
			ClusterID: clusterID, Name: in.Name, Schedule: in.Schedule, RunOn: in.RunOn, NodeID: in.NodeID, UserName: in.User,
			Command: in.Command, TimeoutSeconds: int32(in.TimeoutSeconds), Enabled: in.Enabled, CreatedBy: createdBy,
			Retries: int32(in.Retries), Alert: in.Alert, Kind: in.Kind, SyncSource: src, SyncTarget: dst, SyncToCluster: to,
			SyncDelete: del, SyncExclude: ex, SyncBwlimitMb: bw,
		})
		if isUnique(err) {
			return &InvalidError{"name", "er is al een cronjob met die naam in dit cluster"}
		}
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cron_job", SubjectID: job.ID.String(), ClusterID: &clusterID, Action: "cron_job.created",
			Payload: jobPayload(job, clusterName),
		})
	})
	if err != nil {
		return job, err
	}
	s.applyClusters(ctx, clusterID, derefID(job.SyncToCluster))
	return job, nil
}

// UpdateJob wijzigt een job; het event heeft de oude en nieuwe waarden.
func (s *Service) UpdateJob(ctx context.Context, actor events.Actor, id uuid.UUID, in JobInput) (store.CronJob, error) {
	var job store.CronJob
	var clusterID, oldTo uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		old, err := q.GetCronJob(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrJobNotFound
		}
		if err != nil {
			return err
		}
		clusterID, oldTo = old.ClusterID, derefID(old.SyncToCluster)
		// De soort van een job verandert niet.
		in.Kind = old.Kind
		if err := s.validate(ctx, q, old.ClusterID, &in); err != nil {
			return err
		}
		src, dst, to, del, ex, bw := syncParams(in)
		job, err = q.UpdateCronJob(ctx, store.UpdateCronJobParams{
			ID: id, Name: in.Name, Schedule: in.Schedule, RunOn: in.RunOn, NodeID: in.NodeID, UserName: in.User,
			Command: in.Command, TimeoutSeconds: int32(in.TimeoutSeconds), Enabled: in.Enabled, Retries: int32(in.Retries), Alert: in.Alert,
			SyncSource: src, SyncTarget: dst, SyncToCluster: to, SyncDelete: del, SyncExclude: ex, SyncBwlimitMb: bw,
		})
		if isUnique(err) {
			return &InvalidError{"name", "er is al een cronjob met die naam in dit cluster"}
		}
		if err != nil {
			return err
		}
		before := jobPayload(store.CronJob{
			Name: old.Name, Schedule: old.Schedule, RunOn: old.RunOn, NodeID: old.NodeID, UserName: old.UserName, Command: old.Command,
			TimeoutSeconds: old.TimeoutSeconds, Enabled: old.Enabled, Revision: old.Revision, Retries: old.Retries, Alert: old.Alert,
			Kind: old.Kind, SyncSource: old.SyncSource, SyncTarget: old.SyncTarget, SyncToCluster: old.SyncToCluster, SyncDelete: old.SyncDelete,
			SyncExclude: old.SyncExclude, SyncBwlimitMb: old.SyncBwlimitMb,
		}, old.ClusterName)
		after := jobPayload(job, old.ClusterName)
		changes := map[string]any{}
		for k, v := range after {
			if k != "revision" && fmt.Sprint(before[k]) != fmt.Sprint(v) {
				changes[k] = map[string]any{"from": before[k], "to": v}
			}
		}
		for _, k := range []string{"node_id", "to_cluster_id"} {
			if _, ok := before[k]; ok {
				if _, still := after[k]; !still {
					changes[k] = map[string]any{"from": before[k], "to": nil}
				}
			}
		}
		action := "cron_job.updated"
		if len(changes) == 1 && changes["enabled"] != nil {
			action = map[bool]string{true: "cron_job.enabled", false: "cron_job.disabled"}[job.Enabled]
		}
		after["changes"] = changes
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cron_job", SubjectID: job.ID.String(), ClusterID: &clusterID, Action: action, Payload: after,
		})
	})
	if err != nil {
		return job, err
	}
	s.applyClusters(ctx, clusterID, oldTo, derefID(job.SyncToCluster))
	return job, nil
}

// DeleteJob haalt een job weg, ook van de nodes.
func (s *Service) DeleteJob(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	var clusterID, oldTo uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		old, err := q.GetCronJob(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrJobNotFound
		}
		if err != nil {
			return err
		}
		clusterID, oldTo = old.ClusterID, derefID(old.SyncToCluster)
		if err := q.DeleteCronJob(ctx, id); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cron_job", SubjectID: id.String(), ClusterID: &clusterID, Action: "cron_job.deleted",
			Payload: map[string]any{"name": old.Name, "cluster": old.ClusterName, "schedule": old.Schedule, "command": old.Command},
		})
	})
	if err != nil {
		return err
	}
	s.applyClusters(ctx, clusterID, oldTo)
	return nil
}

// world is wat ApplyAll en het overzicht nodig hebben.
type world struct {
	// retryDelay is de wachttijd in seconden voor een herhaling.
	retryDelay int
	jobs       []store.ListCronJobsRow
	nodes      []store.ListCronCandidateNodesRow
	vips       map[uuid.UUID][]store.ListCronVIPsRow
	sets       map[uuid.UUID]store.CronNodeSet
	// syncNodes zijn de sleutels van de nodes in een sync.
	syncNodes map[uuid.UUID]store.CronSyncNode
}

func (s *Service) world(ctx context.Context) (world, error) {
	w := world{retryDelay: max(1, int(s.RetryDelay/time.Second))}
	var err error
	if w.jobs, err = s.q.ListCronJobs(ctx); err != nil {
		return w, err
	}
	if w.nodes, err = s.q.ListCronCandidateNodes(ctx); err != nil {
		return w, err
	}
	vips, err := s.q.ListCronVIPs(ctx)
	if err != nil {
		return w, err
	}
	w.vips = map[uuid.UUID][]store.ListCronVIPsRow{}
	for _, v := range vips {
		w.vips[v.ClusterID] = append(w.vips[v.ClusterID], v)
	}
	sets, err := s.q.ListCronNodeSets(ctx)
	if err != nil {
		return w, err
	}
	w.sets = map[uuid.UUID]store.CronNodeSet{}
	for _, st := range sets {
		w.sets[st.NodeID] = st
	}
	keys, err := s.q.ListCronSyncNodes(ctx)
	if err != nil {
		return w, err
	}
	w.syncNodes = map[uuid.UUID]store.CronSyncNode{}
	for _, k := range keys {
		w.syncNodes[k.NodeID] = k
	}
	return w, nil
}

// onNode zegt of job op node hoort.
func onNode(j store.ListCronJobsRow, n store.ListCronCandidateNodesRow) bool {
	if n.ClusterID == nil || *n.ClusterID != j.ClusterID {
		return false
	}
	return j.RunOn != protocol.CronOnNode || (j.NodeID != nil && *j.NodeID == n.ID)
}

// desired is de set jobs van een node.
func (w world) desired(n store.ListCronCandidateNodesRow) []protocol.ManagedCron {
	out := []protocol.ManagedCron{}
	for _, j := range w.jobs {
		if !onNode(j, n) {
			continue
		}
		sc, err := cronexpr.Parse(j.Schedule)
		if err != nil {
			continue
		}
		cal, err := sc.OnCalendar()
		if err != nil {
			continue
		}
		m := protocol.ManagedCron{
			ID: j.ID.String(), Name: j.Name, Revision: int(j.Revision), Schedule: j.Schedule, OnCalendar: cal, RunOn: j.RunOn,
			User: j.UserName, Command: j.Command, TimeoutSeconds: int(j.TimeoutSeconds), Enabled: j.Enabled,
			Retries: int(j.Retries), RetryDelaySeconds: w.retryDelay,
		}
		if isSync(j) {
			m.Kind, m.Sync = protocol.CronKindSync, w.syncSpec(j, n)
		}
		if j.RunOn == protocol.CronOnVIP {
			for _, v := range w.vips[j.ClusterID] {
				m.VIPs = append(m.VIPs, v.Address.String())
			}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

var emptySetHash = protocol.CronSetHash(nil)

// wantHash is de hash die node n moet hebben.
func (w world) wantHash(n store.ListCronCandidateNodesRow) string {
	return protocol.CronApplyHash(w.desired(n), w.receives(n))
}

// ApplyAll zet op elke node de jobs die er horen. Een node die al klopt,
// krijgt de set ook: zo komt een job terug na een herinstallatie; de agent
// schrijft alleen wat verschilt.
func (s *Service) ApplyAll(ctx context.Context) error {
	return s.apply(ctx, nil)
}

// derefID geeft de uuid, of uuid.Nil zonder.
func derefID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

// applyClusters past de nodes van de clusters toe, na een wijziging (een
// sync raakt ook het cluster van zijn doelen). Een fout staat bij de node;
// de lus probeert het later opnieuw.
func (s *Service) applyClusters(ctx context.Context, ids ...uuid.UUID) {
	set := map[uuid.UUID]bool{}
	for _, id := range ids {
		if id != uuid.Nil {
			set[id] = true
		}
	}
	if err := s.apply(ctx, set); err != nil && ctx.Err() == nil {
		s.log.Warn("cronjobs op de nodes zetten mislukt", "err", err)
	}
}

func (s *Service) apply(ctx context.Context, clusters map[uuid.UUID]bool) error {
	w, err := s.world(ctx)
	if err != nil {
		return err
	}
	s.ensureKeys(ctx, &w)
	now := s.Now()
	var errs []error
	for _, n := range w.nodes {
		if clusters != nil && (n.ClusterID == nil || !clusters[*n.ClusterID]) {
			// Een node die uit het cluster ging, heeft misschien nog jobs.
			if st, ok := w.sets[n.ID]; !ok || st.Hash == "" || st.Hash == emptySetHash {
				continue
			}
		}
		set, receives := w.desired(n), w.receives(n)
		st, known := w.sets[n.ID]
		if len(set) == 0 && len(receives) == 0 && (!known || st.Hash == "" || st.Hash == emptySetHash) {
			continue
		}
		hasSync := len(receives) > 0 || slices.ContainsFunc(set, func(m protocol.ManagedCron) bool { return m.Kind == protocol.CronKindSync })
		mark := func(kind, msg string) {
			if err := s.q.MarkCronNodeSet(ctx, store.MarkCronNodeSetParams{NodeID: n.ID, Error: msg, ErrorKind: kind, SyncedAt: now}); err != nil {
				errs = append(errs, err)
			}
		}
		switch {
		case n.AgentProtocol < protocol.CronManagedSince:
			mark(ErrOldAgent, fmt.Sprintf("de agent op %s heeft protocol %d; beheerde cronjobs vragen protocol %d", n.Hostname, n.AgentProtocol, protocol.CronManagedSince))
			continue
		case hasSync && n.AgentProtocol < protocol.CronSyncSince:
			mark(ErrOldAgent, fmt.Sprintf("de agent op %s heeft protocol %d; een bestandssync vraagt protocol %d", n.Hostname, n.AgentProtocol, protocol.CronSyncSince))
			continue
		case n.HeartbeatAt == nil || now.Sub(*n.HeartbeatAt) > status.HeartbeatLate:
			mark(ErrOffline, "de agent op "+n.Hostname+" is niet verbonden")
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, s.Timeout)
		res, err := s.bus.Cron(rctx, n.ID, protocol.CronRequest{Op: protocol.CronApply, Jobs: set, Receives: receives})
		cancel()
		switch {
		case errors.Is(err, agentbus.ErrAgentOffline):
			mark(ErrOffline, "de agent op "+n.Hostname+" is niet verbonden")
		case errors.Is(err, agentbus.ErrNoAnswer):
			mark(ErrNoAnswer, "de agent op "+n.Hostname+" antwoordde niet op tijd")
		case err != nil:
			mark(ErrAgent, err.Error())
		case res.Error != "":
			mark(ErrAgent, n.Hostname+": "+res.Error)
		default:
			if err := s.q.UpsertCronNodeSet(ctx, store.UpsertCronNodeSetParams{NodeID: n.ID, Hash: res.Hash, AppliedAt: &now}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// RunJob start een job nu: op de node met het VIP, op zijn node, of op alle
// nodes. Het geeft de nodes waarop hij startte.
func (s *Service) RunJob(ctx context.Context, actor events.Actor, id uuid.UUID) ([]string, error) {
	w, err := s.world(ctx)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(w.jobs, func(j store.ListCronJobsRow) bool { return j.ID == id })
	if i < 0 {
		return nil, ErrJobNotFound
	}
	j := w.jobs[i]
	var targets []store.ListCronCandidateNodesRow
	for _, n := range w.nodes {
		if !onNode(j, n) {
			continue
		}
		if j.RunOn == protocol.CronOnVIP {
			owner := slices.IndexFunc(w.vips[j.ClusterID], func(v store.ListCronVIPsRow) bool { return v.OwnerNodeID != nil && *v.OwnerNodeID == n.ID })
			if owner < 0 {
				continue
			}
		}
		targets = append(targets, n)
	}
	if len(targets) == 0 {
		if j.RunOn == protocol.CronOnVIP {
			return nil, &AgentError{"geen node heeft nu het VIP van " + j.ClusterName}
		}
		return nil, &AgentError{"geen node met een agent om deze job op te draaien"}
	}
	var started, failed []string
	for _, n := range targets {
		if st, ok := w.sets[n.ID]; !ok || st.Hash != w.wantHash(n) {
			s.applyClusters(ctx, j.ClusterID, derefID(j.SyncToCluster))
			break
		}
	}
	for _, n := range targets {
		if n.AgentProtocol < protocol.CronManagedSince {
			failed = append(failed, fmt.Sprintf("%s: agent te oud (protocol %d, nodig %d)", n.Hostname, n.AgentProtocol, protocol.CronManagedSince))
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, s.Timeout)
		res, err := s.bus.Cron(rctx, n.ID, protocol.CronRequest{Op: protocol.CronRun, JobID: id.String()})
		cancel()
		switch {
		case err != nil:
			failed = append(failed, n.Hostname+": "+agentMessage(n.Hostname, err))
		case res.Error != "":
			failed = append(failed, n.Hostname+": "+res.Error)
		default:
			started = append(started, n.Hostname)
		}
	}
	if len(started) > 0 {
		if err := s.ev.Write(ctx, nil, events.Event{
			Actor: actor, SubjectType: "cron_job", SubjectID: id.String(), ClusterID: &j.ClusterID, Action: "cron_job.run_requested",
			Payload: map[string]any{"name": j.Name, "cluster": j.ClusterName, "nodes": started},
		}); err != nil {
			return started, err
		}
	}
	if len(failed) > 0 {
		return started, &AgentError{strings.Join(failed, "; ")}
	}
	return started, nil
}

func agentMessage(host string, err error) string {
	switch {
	case errors.Is(err, agentbus.ErrAgentOffline):
		return "de agent op " + host + " is niet verbonden"
	case errors.Is(err, agentbus.ErrNoAnswer):
		return "de agent op " + host + " antwoordde niet op tijd"
	}
	return err.Error()
}

// HandleRuns bewaart de runs die een agent stuurt. Een run van een job die
// niet meer bestaat, of van een andere node, valt weg.
func (s *Service) HandleRuns(ctx context.Context, nodeID uuid.UUID, msg protocol.CronRuns) error {
	runs := msg.Runs
	if len(runs) > protocol.MaxCronRuns {
		runs = runs[:protocol.MaxCronRuns]
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		for _, r := range runs {
			id, err1 := uuid.Parse(r.ID)
			jobID, err2 := uuid.Parse(r.JobID)
			if err1 != nil || err2 != nil || r.StartedAt.IsZero() {
				continue
			}
			switch r.Result {
			case protocol.CronRunning, protocol.CronOK, protocol.CronFailed, protocol.CronSkipped, protocol.CronTimedOut, protocol.CronStopped:
			default:
				continue
			}
			job, err := q.GetCronJob(ctx, jobID)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			prev, err := q.GetCronRunState(ctx, id)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			known := err == nil
			if known && prev != protocol.CronRunning {
				continue
			}
			if len(r.Output) > protocol.CronOutputMax+1024 {
				r.Output, r.OutputTruncated = r.Output[len(r.Output)-protocol.CronOutputMax:], true
			}
			r.Output = strings.ToValidUTF8(strings.ReplaceAll(r.Output, "\x00", ""), "?")
			if len(r.Sync) > 100 {
				r.Sync = r.Sync[:100]
			}
			syncJSON := []byte("[]")
			if len(r.Sync) > 0 {
				syncJSON, _ = json.Marshal(r.Sync)
			}
			if err := q.UpsertCronRun(ctx, store.UpsertCronRunParams{
				ID: id, JobID: jobID, NodeID: nodeID, Revision: int32(r.Revision), Reason: r.Reason, Result: r.Result,
				StartedAt: r.StartedAt, EndedAt: r.EndedAt, ExitCode: int32Ptr(r.Exit), Output: r.Output, OutputTruncated: r.OutputTruncated,
				Sync: syncJSON,
			}); err != nil {
				return err
			}
			if r.Result != protocol.CronFailed && r.Result != protocol.CronTimedOut {
				continue
			}
			host, _ := q.GetNodeRef(ctx, nodeID)
			p := map[string]any{"name": job.Name, "cluster": job.ClusterName, "hostname": host.Hostname, "result": r.Result, "run_id": r.ID}
			if r.Exit != nil {
				p["exit"] = *r.Exit
			}
			if err := s.ev.Write(ctx, q, events.Event{
				Actor: events.System(), SubjectType: "cron_job", SubjectID: jobID.String(), ClusterID: &job.ClusterID,
				Action: "cron_job.run_failed", Payload: p,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Prune haalt runs ouder dan negentig dagen weg, behalve de laatste per job
// en node.
func (s *Service) Prune(ctx context.Context) error {
	_, err := s.q.DeleteOldCronRuns(ctx)
	return err
}

// ErrRunNotRunning: de run loopt niet meer.
var ErrRunNotRunning = errors.New("deze run loopt niet meer")

// StopRun stopt een lopende run op zijn node.
func (s *Service) StopRun(ctx context.Context, actor events.Actor, runID uuid.UUID) error {
	run, err := s.RunRecord(ctx, runID)
	if err != nil {
		return err
	}
	if run.Result != protocol.CronRunning {
		return ErrRunNotRunning
	}
	n, err := s.agentNode(ctx, run.NodeID, protocol.CronFollowSince)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	res, err := s.bus.Cron(rctx, run.NodeID, protocol.CronRequest{Op: protocol.CronStop, JobID: run.JobID.String()})
	switch {
	case err != nil:
		return &AgentError{agentMessage(n.Hostname, err)}
	case res.Error != "":
		return &AgentError{n.Hostname + ": " + res.Error}
	}
	return s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "cron_job", SubjectID: run.JobID.String(), ClusterID: &run.ClusterID, Action: "cron_job.run_stopped",
		Payload: map[string]any{"name": run.JobName, "hostname": run.Hostname, "run_id": runID.String()},
	})
}

// LiveOutput haalt de uitvoer tot nu toe van een lopende run bij de agent.
func (s *Service) LiveOutput(ctx context.Context, run store.GetCronRunRow) (string, bool, error) {
	n, err := s.agentNode(ctx, run.NodeID, protocol.CronFollowSince)
	if err != nil {
		return "", false, err
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := s.bus.Cron(rctx, run.NodeID, protocol.CronRequest{Op: protocol.CronOutput, JobID: run.JobID.String(), RunID: run.ID.String()})
	switch {
	case err != nil:
		return "", false, &AgentError{agentMessage(n.Hostname, err)}
	case res.Error != "":
		return "", false, &AgentError{n.Hostname + ": " + res.Error}
	}
	return res.RunOutput, res.RunOutputTruncated, nil
}

// agentNode geeft de node als zijn agent minstens protocol since heeft.
func (s *Service) agentNode(ctx context.Context, nodeID uuid.UUID, since int32) (store.ListCronCandidateNodesRow, error) {
	nodes, err := s.q.ListCronCandidateNodes(ctx)
	if err != nil {
		return store.ListCronCandidateNodesRow{}, err
	}
	i := slices.IndexFunc(nodes, func(n store.ListCronCandidateNodesRow) bool { return n.ID == nodeID })
	if i < 0 {
		return store.ListCronCandidateNodesRow{}, &AgentError{"de node heeft geen agent meer"}
	}
	if nodes[i].AgentProtocol < since {
		return nodes[i], fmt.Errorf("%w: de agent op %s heeft protocol %d; dit vraagt protocol %d", ErrAgentTooOld, nodes[i].Hostname, nodes[i].AgentProtocol, since)
	}
	return nodes[i], nil
}

// missedGrace is hoe lang na het geplande moment een run mag beginnen voor
// hij als gemist telt.
const missedGrace = 10 * time.Minute

// lastScheduled is het laatste geplande moment in [from, until], of nul.
func lastScheduled(sc cronexpr.Schedule, from, until time.Time) time.Time {
	var last time.Time
	t := from
	for range 20000 {
		next := sc.Next(t)
		if next.IsZero() || next.After(until) {
			return last
		}
		last, t = next, next
	}
	return last
}
