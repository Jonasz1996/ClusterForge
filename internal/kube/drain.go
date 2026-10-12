package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// KindDrain is de soort taak die een node in Kubernetes leegmaakt.
const KindDrain = "kube.drain"

// DrainOptions zegt hoe een node leeg moet.
type DrainOptions struct {
	// Force zet ook pods zonder eigenaar en pods met emptyDir uit, zoals
	// kubectl drain --force --delete-emptydir-data. Die zijn daarna weg.
	Force bool
	// Maintenance is leegmaken voor onderhoud of een herstart van de node:
	// de cordon krijgt de annotatie van ClusterForge, pods zonder eigenaar
	// blijven staan, want ze starten na een herstart vanzelf opnieuw, en
	// pods met emptyDir gaan wel weg.
	Maintenance bool
}

// DrainPod is een pod op een node die leeg moet.
type DrainPod struct {
	Namespace string
	Name      string
	UID       string
	OwnerKind string
	OwnerName string
	// Reason zegt waarom de pod blijft of tegenhoudt, of waarop je moet
	// letten als hij uitgezet wordt.
	Reason string
}

func (p DrainPod) String() string { return p.Namespace + "/" + p.Name }

// DrainPlan is wat leegmaken zou doen.
type DrainPlan struct {
	Node string
	// Cordoned is true als de node al op cordon stond.
	Cordoned bool
	// Evict worden uitgezet, Skip blijven staan, en Blocked houden het
	// leegmaken tegen zonder Force.
	Evict, Skip, Blocked []DrainPod
}

// PlanDrain bekijkt de pods op een node: welke uitgezet worden, welke
// blijven en welke het leegmaken tegenhouden.
func (s *Service) PlanDrain(ctx context.Context, clusterID uuid.UUID, node string, opts DrainOptions) (DrainPlan, error) {
	plan := DrainPlan{Node: node}
	if !ValidName(node) {
		return plan, fmt.Errorf("%w: node %q", ErrInvalid, node)
	}
	s.forget(clusterID)
	n, err := GetInto[Node](ctx, s, clusterID, "/api/v1/nodes/"+node)
	if err != nil {
		return plan, err
	}
	plan.Cordoned = n.Spec.Unschedulable
	pods, err := s.podsOnNode(ctx, clusterID, node)
	if err != nil {
		return plan, err
	}
	for _, p := range pods {
		dp := DrainPod{Namespace: p.Metadata.Namespace, Name: p.Metadata.Name, UID: p.Metadata.UID}
		ow := p.Metadata.Owner()
		if ow != nil {
			dp.OwnerKind, dp.OwnerName = ow.Kind, ow.Name
		}
		skip := func(why string) { dp.Reason = why; plan.Skip = append(plan.Skip, dp) }
		block := func(why string) { dp.Reason = why; plan.Blocked = append(plan.Blocked, dp) }
		switch {
		case p.Metadata.DeletionTimestamp != nil:
			skip("wordt al gestopt")
		case p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed":
			skip("is klaar en draait niet meer")
		case p.Metadata.Annotations["kubernetes.io/config.mirror"] != "":
			skip("statische pod van de kubelet op deze node")
		case ow != nil && ow.Kind == "DaemonSet":
			skip("hoort bij DaemonSet " + ow.Name + ", die op elke node een pod draait")
		case ow == nil && opts.Maintenance:
			skip("heeft geen eigenaar; hij blijft staan en start na een herstart vanzelf opnieuw")
		case ow == nil && !opts.Force:
			block("heeft geen eigenaar: uitgezet komt hij nergens terug")
		case emptyDir(p) && !opts.Force && !opts.Maintenance:
			block("bewaart gegevens in emptyDir; die gaan verloren")
		default:
			switch {
			case ow == nil:
				dp.Reason = "heeft geen eigenaar en komt niet terug"
			case emptyDir(p):
				dp.Reason = "de gegevens in emptyDir gaan verloren"
			}
			plan.Evict = append(plan.Evict, dp)
		}
	}
	return plan, nil
}

func emptyDir(p Pod) bool {
	return slices.ContainsFunc(p.Spec.Volumes, func(v PodVolume) bool { return v.EmptyDir != nil })
}

// BlockedError betekent dat pods het leegmaken tegenhouden; met Force gaat
// het toch.
type BlockedError struct{ Pods []DrainPod }

func (e *BlockedError) Error() string {
	var parts []string
	for _, p := range e.Pods {
		parts = append(parts, p.String()+" "+p.Reason)
	}
	return "leegmaken stopt hier: " + strings.Join(parts, "; ") + ". Met Toch doorgaan gaan ze weg"
}

// Drain zet een node op cordon en zet zijn pods uit via de Eviction-API,
// zodat een PodDisruptionBudget geldt. Houdt een budget een pod tegen, dan
// probeert Drain het elke DrainRetry opnieuw tot DrainTimeout. Daarna
// wacht hij tot de pods echt weg zijn. log krijgt de voortgang.
func (s *Service) Drain(ctx context.Context, clusterID uuid.UUID, node string, opts DrainOptions, log func(string)) (DrainPlan, error) {
	plan, err := s.PlanDrain(ctx, clusterID, node, opts)
	if err != nil {
		return plan, err
	}
	if len(plan.Blocked) > 0 {
		return plan, &BlockedError{Pods: plan.Blocked}
	}
	if plan.Cordoned {
		log(node + " stond al op cordon")
	} else {
		if err := s.setUnschedulable(ctx, clusterID, node, true, opts.Maintenance); err != nil {
			return plan, err
		}
		log(node + " staat op cordon: er komen geen nieuwe pods op")
	}
	for _, p := range plan.Skip {
		log(p.String() + " blijft: " + p.Reason)
	}
	if len(plan.Evict) == 0 {
		log("er staan geen pods op " + node + " die weg moeten")
		return plan, nil
	}
	ctx, cancel := context.WithTimeoutCause(ctx, s.DrainTimeout, errDrainTimeout)
	defer cancel()
	remaining := plan.Evict
	said := map[string]string{}
	for len(remaining) > 0 {
		var next []DrainPod
		for _, p := range remaining {
			why, err := s.evict(ctx, clusterID, p.Namespace, p.Name)
			if err != nil {
				return plan, s.drainStopped(ctx, err, remaining)
			}
			if why != "" {
				next = append(next, p)
				if said[p.UID] != why {
					said[p.UID] = why
					log(fmt.Sprintf("%s mag nog niet weg (%s); opnieuw over %s", p, why, seconds(s.DrainRetry)))
				}
				continue
			}
			text := p.String() + " uitgezet"
			if p.Reason != "" {
				text += "; " + p.Reason
			}
			log(text)
		}
		remaining = next
		if len(remaining) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return plan, s.drainStopped(ctx, context.Cause(ctx), remaining)
		case <-time.After(s.DrainRetry):
		}
	}
	// Een pod stopt pas na zijn grace period; wacht tot ze weg zijn.
	evicted := map[string]DrainPod{}
	for _, p := range plan.Evict {
		evicted[p.UID] = p
	}
	t := time.NewTicker(s.DrainPoll)
	defer t.Stop()
	for {
		s.forget(clusterID)
		pods, err := s.podsOnNode(ctx, clusterID, node)
		if err != nil {
			return plan, s.drainStopped(ctx, err, nil)
		}
		var left []DrainPod
		for _, p := range pods {
			if dp, ok := evicted[p.Metadata.UID]; ok {
				left = append(left, dp)
			}
		}
		if len(left) == 0 {
			log(fmt.Sprintf("%s is leeg: %s uitgezet", node, count(len(plan.Evict), "pod", "pods")))
			return plan, nil
		}
		select {
		case <-ctx.Done():
			return plan, s.drainStopped(ctx, context.Cause(ctx), left)
		case <-t.C:
		}
	}
}

var errDrainTimeout = errors.New("leegmaken duurt te lang")

// drainStopped maakt de fout als leegmaken stopt: bij een time-out met de
// pods die nog staan.
func (s *Service) drainStopped(ctx context.Context, err error, left []DrainPod) error {
	if !errors.Is(context.Cause(ctx), errDrainTimeout) {
		return err
	}
	names := make([]string, 0, len(left))
	for _, p := range left {
		names = append(names, p.String())
	}
	return fmt.Errorf("na %s staan er nog pods: %s. Een PodDisruptionBudget laat ze misschien niet gaan; geef de app meer pods of verlaag minAvailable",
		seconds(s.DrainTimeout), strings.Join(names, ", "))
}

// Release haalt de cordon weg die ClusterForge voor onderhoud zette. Een
// cordon zonder de annotatie van ClusterForge, die iemand zelf zette,
// blijft. released is true als de node weer pods krijgt.
func (s *Service) Release(ctx context.Context, clusterID uuid.UUID, node string) (released bool, err error) {
	if !ValidName(node) {
		return false, fmt.Errorf("%w: node %q", ErrInvalid, node)
	}
	s.forget(clusterID)
	n, err := GetInto[Node](ctx, s, clusterID, "/api/v1/nodes/"+node)
	if err != nil {
		return false, err
	}
	if _, ours := n.Metadata.Annotations[MarkAnnotation]; !ours {
		return !n.Spec.Unschedulable, nil
	}
	return true, s.setUnschedulable(ctx, clusterID, node, false, false)
}

// NodeReady zegt of een node Ready is in Kubernetes.
func (s *Service) NodeReady(ctx context.Context, clusterID uuid.UUID, node string) (bool, error) {
	if !ValidName(node) {
		return false, fmt.Errorf("%w: node %q", ErrInvalid, node)
	}
	s.forget(clusterID)
	n, err := GetInto[Node](ctx, s, clusterID, "/api/v1/nodes/"+node)
	return n.Ready(), err
}

// NodeNameOf geeft de naam in Kubernetes van een ClusterForge-node, of ""
// als het cluster hem niet kent.
func (s *Service) NodeNameOf(ctx context.Context, clusterID, nodeID uuid.UUID) (string, error) {
	nodes, err := s.Nodes(ctx, clusterID)
	if err != nil {
		return "", err
	}
	links, err := s.Links(ctx, clusterID, nodes)
	if err != nil {
		return "", err
	}
	for name, l := range links {
		if l.NodeID == nodeID {
			return name, nil
		}
	}
	return "", nil
}

// drainParams gaan mee met de taak.
type drainParams struct {
	Node  string `json:"node"`
	Force bool   `json:"force,omitempty"`
}

// EnableDrain registreert de taak die een node leegmaakt.
func (s *Service) EnableDrain(runner *jobs.Runner, ev *events.Writer) {
	s.jobs, s.ev = runner, ev
	runner.Register(KindDrain, s.runDrain)
}

// RequestDrain plant het leegmaken van een node als taak. Houden pods het
// tegen en is Force uit, dan is de fout een BlockedError.
func (s *Service) RequestDrain(ctx context.Context, actor events.Actor, clusterID uuid.UUID, node string, force bool) (store.Job, error) {
	plan, err := s.PlanDrain(ctx, clusterID, node, DrainOptions{Force: force})
	if err != nil {
		return store.Job{}, err
	}
	if len(plan.Blocked) > 0 {
		return store.Job{}, &BlockedError{Pods: plan.Blocked}
	}
	spec := jobs.Spec{
		Kind: KindDrain, Title: "Node leegmaken: " + node, Params: drainParams{Node: node, Force: force},
		ClusterID: &clusterID, Actor: actor,
	}
	if id, err := s.linkedNode(ctx, clusterID, node); err == nil && id != nil {
		spec.NodeID = id
	}
	j, err := s.jobs.EnqueueForCluster(ctx, spec)
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{busy.Error()}
	}
	return j, err
}

// linkedNode is de ClusterForge-node van een node in Kubernetes.
func (s *Service) linkedNode(ctx context.Context, clusterID uuid.UUID, node string) (*uuid.UUID, error) {
	nodes, err := s.Nodes(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	links, err := s.Links(ctx, clusterID, nodes)
	if err != nil {
		return nil, err
	}
	if l, ok := links[node]; ok {
		return &l.NodeID, nil
	}
	return nil, nil
}

func (s *Service) runDrain(ctx context.Context, j *jobs.Job) error {
	var p drainParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	if j.ClusterID == nil {
		return errors.New("taak zonder cluster")
	}
	clusterID := *j.ClusterID
	var plan DrainPlan
	err := j.Step(ctx, "Cordon en pods uitzetten", func(ctx context.Context, st *jobs.Step) error {
		var err error
		plan, err = s.Drain(ctx, clusterID, p.Node, DrainOptions{Force: p.Force}, func(line string) {
			st.Logf("%s", line)
			_ = st.Flush(ctx)
		})
		var be *BlockedError
		if errors.As(err, &be) {
			// Sinds het plan kwam er een pod bij die tegenhoudt.
			st.Logf("%s", be.Error())
		}
		if err != nil {
			st.Logf("%s blijft op cordon; met Uncordon krijgt hij weer pods", p.Node)
		}
		return err
	})
	if err != nil {
		return err
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	return s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "kube.drained",
		Payload: map[string]any{
			"kind": "Node", "name": p.Node, "node": p.Node, "evicted": len(plan.Evict), "skipped": len(plan.Skip),
			"force": p.Force, "job_id": j.ID, "title": j.Title,
		},
	})
}

// seconds schrijft een duur als "5 s" of "5 min".
func seconds(d time.Duration) string {
	if d >= 2*time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d s", int(d.Round(time.Second).Seconds()))
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
