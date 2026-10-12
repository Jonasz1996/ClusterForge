// Package kubenodes haalt een worker weg uit een Kubernetes-cluster, in
// vaste stappen: leegmaken, de VM uitzetten, de node uit Kubernetes halen,
// de VM verwijderen, en de node uit ClusterForge met een nieuwe revisie van
// de spec met één worker minder. Een server weghalen kan niet: etcd moet
// zijn meerderheid houden.
package kubenodes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// Kind is de soort taak die een worker weghaalt.
const Kind = "kube.remove_node"

// ConflictError betekent dat de node nu niet weg kan, met de reden.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	jobs *jobs.Runner
	kube *kube.Service
	pve  *proxmox.Service
	inv  *inventory.Service
	dep  *deploy.Service

	// Poll is de pauze tussen twee keer kijken of de VM uit staat.
	Poll time.Duration
	// ShutdownTimeout is hoe lang de VM over netjes afsluiten mag doen;
	// daarna gaat hij hard uit.
	ShutdownTimeout time.Duration
	// Changed wordt aangeroepen als een node uit het cluster is. Mag nil zijn.
	Changed func()
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, runner *jobs.Runner, kb *kube.Service, pve *proxmox.Service,
	inv *inventory.Service, dep *deploy.Service) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, jobs: runner, kube: kb, pve: pve, inv: inv, dep: dep,
		Poll: 3 * time.Second, ShutdownTimeout: 2 * time.Minute,
	}
	runner.Register(Kind, s.run)
	// Elke stap kan opnieuw: een node die al leeg, uit of weg is, slaat hij
	// over.
	runner.Retryable(Kind)
	return s
}

// Plan is wat weghalen zou doen.
type Plan struct {
	// Node is de naam in Kubernetes, Hostname die in ClusterForge.
	Node     string
	NodeID   uuid.UUID
	Hostname string
	Ready    bool
	// VMID en Host zijn de VM in Proxmox.
	VMID int
	Host string
	// Workers is het aantal workers daarna.
	Workers int
	Drain   kube.DrainPlan
}

// params gaan mee met de taak.
type params struct {
	Node      string    `json:"node"`
	NodeID    uuid.UUID `json:"node_id"`
	Hostname  string    `json:"hostname"`
	ProxmoxID uuid.UUID `json:"proxmox_id"`
	VMID      int       `json:"vmid"`
	Force     bool      `json:"force,omitempty"`
}

// worker zoekt de ClusterForge-node van een node in Kubernetes en
// controleert dat hij weg kan.
func (s *Service) worker(ctx context.Context, clusterID uuid.UUID, node string) (params, deploy.Spec, error) {
	c, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		return params{}, deploy.Spec{}, err
	}
	if c.GitRepoID != nil {
		return params{}, deploy.Spec{}, &ConflictError{"dit cluster volgt Git, en welke nodes erin zitten komt uit cluster.yaml. " +
			"Ontkoppel het cluster eerst bij GitOps, haal de worker weg, en zet workers daarna lager in cluster.yaml"}
	}
	if !kube.ValidName(node) {
		return params{}, deploy.Spec{}, fmt.Errorf("%w: node %q", kube.ErrInvalid, node)
	}
	id, err := s.kube.NodeIDOf(ctx, clusterID, node)
	if err != nil {
		return params{}, deploy.Spec{}, err
	}
	if id == uuid.Nil {
		return params{}, deploy.Spec{}, &ConflictError{node + " is geen node in ClusterForge, dus ClusterForge weet niet welke VM erbij hoort"}
	}
	inv, _, err := s.dep.Nodes(ctx, clusterID)
	if err != nil {
		return params{}, deploy.Spec{}, err
	}
	spec, err := deploy.ParseSpec(c.Spec, templates.ClusterInfo{Name: c.Name, Slug: c.Slug, Environment: string(c.Environment)}, inv)
	if errors.Is(err, deploy.ErrNoSpec) {
		return params{}, deploy.Spec{}, &ConflictError{"dit cluster komt niet uit een template; haal de node weg met kubectl delete node nadat zijn VM uit staat"}
	}
	if err != nil {
		return params{}, deploy.Spec{}, err
	}
	i := slices.IndexFunc(spec.Nodes, func(n deploy.SpecNode) bool { return n.NodeID == id })
	switch {
	case i < 0:
		return params{}, deploy.Spec{}, &ConflictError{node + " staat niet in de specificatie van het cluster"}
	case spec.Nodes[i].Role == "server":
		return params{}, deploy.Spec{}, &ConflictError{"een server weghalen kan niet: etcd, de database van Kubernetes, moet zijn meerderheid houden. " +
			"Alleen een worker kan weg"}
	case spec.Nodes[i].Role != "worker":
		return params{}, deploy.Spec{}, &ConflictError{fmt.Sprintf("%s heeft rol %s; alleen een worker kan weg", node, spec.Nodes[i].Role)}
	}
	row, err := s.q.GetNode(ctx, id)
	if err != nil {
		return params{}, deploy.Spec{}, err
	}
	if row.Node.ProxmoxID == nil || row.Node.PveVmid == nil {
		return params{}, deploy.Spec{}, &ConflictError{row.Node.Hostname + " heeft geen VM in Proxmox die ClusterForge kent"}
	}
	return params{
		Node: node, NodeID: id, Hostname: row.Node.Hostname, ProxmoxID: *row.Node.ProxmoxID, VMID: int(*row.Node.PveVmid),
	}, spec, nil
}

// Plan bekijkt wat weghalen zou doen: welke pods weg moeten en welke VM
// verdwijnt. Er verandert niets.
func (s *Service) Plan(ctx context.Context, clusterID uuid.UUID, node string, force bool) (Plan, error) {
	p, spec, err := s.worker(ctx, clusterID, node)
	if err != nil {
		return Plan{}, err
	}
	out := Plan{Node: node, NodeID: p.NodeID, Hostname: p.Hostname, VMID: p.VMID, Workers: -1}
	for _, n := range spec.Nodes {
		if n.Role == "worker" {
			out.Workers++
		}
	}
	// Dezelfde controles als vlak voor het uitzetten en verwijderen, zodat
	// een VM die niet klopt de taak niet halverwege laat stoppen.
	vm, err := s.check(ctx, clusterID, p, false)
	var ge *GuardError
	switch {
	case errors.Is(err, errVMGone):
	case errors.As(err, &ge):
		return Plan{}, &ConflictError{"ClusterForge haalt deze worker niet weg: " + ge.Msg}
	case err != nil:
		return Plan{}, &ConflictError{"Proxmox is niet te lezen: " + err.Error()}
	default:
		out.Host = vm.res.Node
		if err := protected(ctx, vm, p.VMID); err != nil {
			return Plan{}, &ConflictError{err.Error()}
		}
	}
	if out.Ready, err = s.kube.NodeReady(ctx, clusterID, node); err != nil {
		return Plan{}, err
	}
	if out.Ready {
		out.Drain, err = s.kube.PlanDrain(ctx, clusterID, node, kube.DrainOptions{Force: force})
	}
	return out, err
}

// Request plant het weghalen als taak. Houden pods het leegmaken tegen en
// is force uit, dan is de fout een kube.BlockedError.
func (s *Service) Request(ctx context.Context, actor events.Actor, clusterID uuid.UUID, node string, force bool) (store.Job, error) {
	pl, err := s.Plan(ctx, clusterID, node, force)
	if err != nil {
		return store.Job{}, err
	}
	if len(pl.Drain.Blocked) > 0 {
		return store.Job{}, &kube.BlockedError{Pods: pl.Drain.Blocked}
	}
	p, _, err := s.worker(ctx, clusterID, node)
	if err != nil {
		return store.Job{}, err
	}
	p.Force = force
	j, err := s.jobs.EnqueueForCluster(ctx, jobs.Spec{
		Kind: Kind, Title: "Worker weghalen: " + p.Hostname, Params: p, ClusterID: &clusterID, NodeID: &p.NodeID, Actor: actor,
	})
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{busy.Error()}
	}
	return j, err
}

func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	if j.ClusterID == nil {
		return errors.New("taak zonder cluster")
	}
	clusterID := *j.ClusterID
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	logger := func(st *jobs.Step) func(string) {
		return func(line string) {
			st.Logf("%s", line)
			_ = st.Flush(ctx)
		}
	}

	if err := j.Step(ctx, "Leegmaken", func(ctx context.Context, st *jobs.Step) error {
		if gone, err := s.kube.NodeGone(ctx, clusterID, p.Node); err != nil || gone {
			if gone {
				st.Logf("%s staat al niet meer in Kubernetes", p.Node)
			}
			return err
		}
		ready, err := s.kube.NodeReady(ctx, clusterID, p.Node)
		if err != nil {
			return err
		}
		if !ready {
			// De kubelet antwoordt niet, dus een pod stopt daar nooit netjes.
			if err := s.kube.Cordon(ctx, clusterID, p.Node); err != nil {
				return err
			}
			st.Logf("%s is niet Ready; hij staat op cordon, en zijn pods komen op andere nodes zodra hij uit Kubernetes weg is", p.Node)
			return nil
		}
		_, err = s.kube.Drain(ctx, clusterID, p.Node, kube.DrainOptions{Force: p.Force}, logger(st))
		var be *kube.BlockedError
		if errors.As(err, &be) {
			st.Logf("%s", be.Error())
		}
		return err
	}); err != nil {
		return err
	}

	if err := j.Step(ctx, "VM uitzetten", func(ctx context.Context, st *jobs.Step) error {
		if err := s.decommission(ctx, actor, j, p); err != nil {
			return err
		}
		return s.powerOff(ctx, st, clusterID, p)
	}); err != nil {
		return err
	}

	if err := j.Step(ctx, "Node uit Kubernetes halen", func(ctx context.Context, st *jobs.Step) error {
		if err := s.kube.DeleteNode(ctx, clusterID, p.Node); err != nil {
			return err
		}
		gone, err := s.kube.NodeGone(ctx, clusterID, p.Node)
		if err != nil {
			return err
		}
		if !gone {
			return fmt.Errorf("%s staat er na het weghalen nog; draait k3s-agent nog op een andere machine met die naam?", p.Node)
		}
		st.Logf("%s staat niet meer in Kubernetes; zijn pods draaien op de andere nodes", p.Node)
		return nil
	}); err != nil {
		return err
	}

	if err := j.Step(ctx, "VM verwijderen", func(ctx context.Context, st *jobs.Step) error {
		return s.destroyWorker(ctx, st, clusterID, p)
	}); err != nil {
		return err
	}

	return j.Step(ctx, "Uit ClusterForge halen", func(ctx context.Context, st *jobs.Step) error {
		var sh deploy.Shrink
		var agentKey string
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			q := store.New(tx)
			var err error
			if sh, err = s.dep.ShrinkTx(ctx, q, actor, clusterID, p.NodeID); err != nil {
				return err
			}
			agentKey, err = s.inv.DeleteNodeTx(ctx, q, actor, p.NodeID, map[string]any{"job_id": j.ID, "reason": "worker weggehaald"})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		})
		s.inv.Disconnected(agentKey, err)
		if err != nil {
			return err
		}
		if s.Changed != nil {
			s.Changed()
		}
		if sh.Revision > 0 {
			st.Logf("%s is uit ClusterForge; revisie %d van de specificatie heeft een worker minder", p.Hostname, sh.Revision)
			if !sh.Applied && len(sh.Changed) > 0 {
				st.Logf("op %s verandert daardoor een stap; pas de revisie toe met Opnieuw toepassen bij de specificatie", joinHosts(sh.Changed))
			}
		}
		return s.ev.Write(ctx, nil, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "kube.node_removed",
			Payload: map[string]any{"node": p.Node, "hostname": p.Hostname, "vmid": p.VMID, "revision": sh.Revision, "job_id": j.ID, "title": j.Title},
		})
	})
}

// decommission zet de node uit dienst, zodat zijn status niet meetelt
// zodra de VM uit staat.
func (s *Service) decommission(ctx context.Context, actor events.Actor, j *jobs.Job, p params) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.SetNodeLifecycle(ctx, store.SetNodeLifecycleParams{NodeID: p.NodeID, Lifecycle: store.NodeLifecycleDecommissioned})
		if err != nil || row.Previous == store.NodeLifecycleDecommissioned {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "node", SubjectID: p.NodeID.String(), ClusterID: j.ClusterID, Action: "node.lifecycle_changed",
			Payload: map[string]any{"hostname": p.Hostname, "from": row.Previous, "to": store.NodeLifecycleDecommissioned, "job_id": j.ID, "title": j.Title},
		})
	})
}

func joinHosts(h []string) string {
	switch len(h) {
	case 0:
		return ""
	case 1:
		return h[0]
	}
	return fmt.Sprintf("%s en %s", joinComma(h[:len(h)-1]), h[len(h)-1])
}

func joinComma(h []string) string {
	out := h[0]
	for _, x := range h[1:] {
		out += ", " + x
	}
	return out
}
