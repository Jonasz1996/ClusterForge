package kube

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// EventRetention is hoe lang de waarschuwingen uit Kubernetes bewaard
// blijven.
const EventRetention = 30 * 24 * time.Hour

// Run vat de Kubernetes-clusters elke Interval samen, tot ctx afloopt.
func (s *Service) Run(ctx context.Context) {
	if s.Interval <= 0 {
		return
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.SyncAll(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("Kubernetes-clusters samenvatten mislukt", "err", err)
		}
		s.prune(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// prune ruimt eens per uur de waarschuwingen van meer dan 30 dagen op.
func (s *Service) prune(ctx context.Context) {
	now := s.Now()
	if now.Sub(s.prunedAt) < time.Hour {
		return
	}
	s.prunedAt = now
	if _, err := s.q.PruneKubeEvents(ctx, now.Add(-EventRetention)); err != nil && ctx.Err() == nil {
		s.log.Warn("oude Kubernetes-waarschuwingen opruimen mislukt", "err", err)
	}
}

// SyncAll vat elk Kubernetes-cluster samen. Een cluster waarvan de API niet
// antwoordt, krijgt de fout in kube_clusters; de andere gaan door.
func (s *Service) SyncAll(ctx context.Context) error {
	ids, err := s.q.ListKubeClusterIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.Sync(ctx, id); err != nil && ctx.Err() != nil {
			return err
		}
	}
	return nil
}

// ErrorKind deelt een fout van de sync in: api als een agent op een server
// de API vroeg en geen antwoord kreeg, old_agent of no_agent als er niemand
// was om het te vragen.
func ErrorKind(err error) string {
	var ue *UnavailableError
	switch {
	case errors.As(err, &ue) && ue.TooOld:
		return "old_agent"
	case errors.As(err, &ue) && ue.NoAgent:
		return "no_agent"
	}
	return "api"
}

// Sync vat één cluster samen: de samenvatting in kube_clusters, de nodes in
// kube_nodes, de apps in kube_workloads en de waarschuwingen in
// kube_events. Een app met een andere image dan bij de vorige sync is een
// uitrol, en komt in het logboek. Elke MetricsInterval gaan CPU en geheugen
// uit metrics-server naar Metrics. De ServiceAccounts van verlopen en
// ingetrokken kubeconfigs gaan weg. Daarna rekent de status opnieuw.
func (s *Service) Sync(ctx context.Context, clusterID uuid.UUID) error {
	err := s.sync(ctx, clusterID)
	if s.Changed != nil {
		s.Changed()
	}
	return err
}

func (s *Service) sync(ctx context.Context, clusterID uuid.UUID) error {
	var (
		version   string
		via       uuid.UUID
		nodes     []Node
		pods      []Pod
		workloads []Workload
		warnings  []Event
		errs      [3]error
		werr      error
	)
	body, via, err := s.get(ctx, clusterID, "/version", nil)
	if err == nil {
		version, err = parseVersion(body)
	}
	if err == nil {
		var wg sync.WaitGroup
		wg.Go(func() { nodes, errs[0] = s.Nodes(ctx, clusterID) })
		wg.Go(func() { pods, errs[1] = s.Pods(ctx, clusterID, "", "") })
		wg.Go(func() { workloads, errs[2] = s.Workloads(ctx, clusterID, "") })
		// Zonder de waarschuwingen gaat de rest gewoon door.
		wg.Go(func() {
			warnings, werr = List[Event](ctx, s, clusterID, "/api/v1/events", url.Values{"fieldSelector": {"type=Warning"}})
		})
		wg.Wait()
		err = errors.Join(errs[:]...)
	}
	if err != nil {
		if serr := s.q.SaveKubeClusterError(ctx, store.SaveKubeClusterErrorParams{
			ClusterID: clusterID, Error: err.Error(), ErrorKind: ErrorKind(err),
		}); serr != nil {
			return serr
		}
		return err
	}
	if werr != nil {
		s.log.Warn("Kubernetes-waarschuwingen lezen mislukt", "cluster", clusterID, "err", werr)
	}
	links, err := s.Links(ctx, clusterID, nodes)
	if err != nil {
		return err
	}
	usage := s.usage(ctx, clusterID, pods, links)
	perNode := PodsPerNode(pods)
	pc := CountPods(pods)
	ready, wready := 0, 0
	for _, n := range nodes {
		if n.Ready() {
			ready++
		}
	}
	for _, w := range workloads {
		if w.Healthy() {
			wready++
		}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		viaID := &via
		if via == uuid.Nil {
			viaID = nil
		}
		if err := q.SaveKubeCluster(ctx, store.SaveKubeClusterParams{
			ClusterID: clusterID, Version: version, NodesTotal: int32(len(nodes)), NodesReady: int32(ready),
			PodsTotal: int32(pc.Total), PodsRunning: int32(pc.Running), PodsPending: int32(pc.Pending), PodsProblem: int32(pc.Problem),
			WorkloadsTotal: int32(len(workloads)), WorkloadsReady: int32(wready), ViaNodeID: viaID,
		}); err != nil {
			return err
		}
		if err := q.DeleteKubeNodes(ctx, clusterID); err != nil {
			return err
		}
		for _, n := range nodes {
			var nodeID *uuid.UUID
			if l, ok := links[n.Metadata.Name]; ok {
				nodeID = &l.NodeID
			}
			created := n.Metadata.CreationTimestamp
			if err := q.InsertKubeNode(ctx, store.InsertKubeNodeParams{
				ClusterID: clusterID, Name: n.Metadata.Name, NodeID: nodeID, Roles: nonNil(n.Roles()), Ready: n.Ready(),
				Unschedulable: n.Spec.Unschedulable, Kubelet: n.Status.NodeInfo.KubeletVersion, InternalIp: n.Address("InternalIP"),
				CpuMillis: MilliCPU(n.Status.Allocatable["cpu"]), MemoryBytes: Bytes(n.Status.Allocatable["memory"]),
				Pods: int32(perNode[n.Metadata.Name]), CreatedAt: &created,
			}); err != nil {
				return err
			}
		}
		if err := s.saveApps(ctx, q, clusterID, workloads, pods); err != nil {
			return err
		}
		return saveWarnings(ctx, q, clusterID, warnings, pods, workloads, links)
	})
	if err != nil {
		return err
	}
	if len(usage) > 0 {
		if err := s.Metrics.IngestKube(ctx, clusterID, usage, s.Now()); err != nil {
			s.log.Warn("Kubernetes-metrics wegschrijven mislukt", "cluster", clusterID, "err", err)
		}
	}
	s.cleanAccess(ctx, clusterID)
	return nil
}

// usage leest de metrics als het tijd is. Zonder metrics-server gaat de
// sync gewoon door.
func (s *Service) usage(ctx context.Context, clusterID uuid.UUID, pods []Pod, links map[string]NodeLink) []metrics.KubeSample {
	if s.Metrics == nil {
		return nil
	}
	now := s.Now()
	s.mu.Lock()
	// Een sync elke 30 s mag net iets te vroeg zijn.
	due := now.Sub(s.usageAt[clusterID]) >= MetricsInterval-5*time.Second
	if due {
		s.usageAt[clusterID] = now
	}
	s.mu.Unlock()
	if !due {
		return nil
	}
	samples, err := s.usageSamples(ctx, clusterID, pods, links)
	if err != nil {
		s.log.Warn("CPU en geheugen uit metrics-server lezen mislukt", "cluster", clusterID, "err", err)
		return nil
	}
	return samples
}

type appKey struct{ namespace, kind, name string }

// saveApps bewaart de apps, haalt weg wat er niet meer is, en schrijft een
// kube.rolled_out bij een app met een andere image dan bij de vorige sync.
func (s *Service) saveApps(ctx context.Context, q *store.Queries, clusterID uuid.UUID, workloads []Workload, pods []Pod) error {
	rows, err := q.ListKubeWorkloadImages(ctx, clusterID)
	if err != nil {
		return err
	}
	before := map[appKey][]string{}
	for _, r := range rows {
		before[appKey{r.Namespace, r.Kind, r.Name}] = r.Images
	}
	// Op de microseconde, zoals PostgreSQL hem bewaart.
	seen := s.Now().UTC().Truncate(time.Microsecond)
	for _, w := range workloads {
		if !IsApp(w.Kind) {
			continue
		}
		short, trouble := w.Health(pods)
		images := nonNil(w.Images())
		if err := q.UpsertKubeWorkload(ctx, store.UpsertKubeWorkloadParams{
			ClusterID: clusterID, Namespace: w.Metadata.Namespace, Kind: w.Kind, Name: w.Metadata.Name, System: IsSystem(w.Metadata.Namespace),
			Desired: int32(w.Desired()), Ready: int32(w.ReadyPods()), UpToDate: int32(w.UpToDate()), Images: images,
			Trouble: clip(trouble, 300), Short: short, SeenAt: seen,
		}); err != nil {
			return err
		}
		old, ok := before[appKey{w.Metadata.Namespace, w.Kind, w.Metadata.Name}]
		if !ok || s.ev == nil {
			continue
		}
		if from, to, changed := imagesChanged(old, images); changed {
			payload := map[string]any{
				"kind": w.Kind, "namespace": w.Metadata.Namespace, "name": w.Metadata.Name,
				"from": strings.Join(from, ", "), "to": strings.Join(to, ", "), "images": images,
			}
			// Het echte begin: de sync ziet een uitrol tot 30 s later.
			if at, ok := rolloutStart(w, pods, images); ok {
				payload["at"] = at.UTC().Format(time.RFC3339)
			}
			if err := s.ev.Write(ctx, q, events.Event{
				Actor: events.System(), SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
				Action: "kube.rolled_out", Payload: payload,
			}); err != nil {
				return err
			}
		}
	}
	return q.DeleteGoneKubeWorkloads(ctx, store.DeleteGoneKubeWorkloadsParams{ClusterID: clusterID, SeenAt: seen})
}

// saveWarnings bewaart de waarschuwingen, en de containers die door gebrek
// aan geheugen stopten: dat laatste is geen event in Kubernetes, alleen een
// status van de pod.
func saveWarnings(ctx context.Context, q *store.Queries, clusterID uuid.UUID, warnings []Event, pods []Pod, workloads []Workload,
	links map[string]NodeLink) error {
	byPod := map[[2]string]Pod{}
	for _, p := range pods {
		byPod[[2]string{p.Metadata.Namespace, p.Metadata.Name}] = p
	}
	apps := map[appKey]bool{}
	for _, w := range workloads {
		apps[appKey{w.Metadata.Namespace, w.Kind, w.Metadata.Name}] = true
	}
	nodeID := func(name string) *uuid.UUID {
		if l, ok := links[name]; ok {
			id := l.NodeID
			return &id
		}
		return nil
	}
	for _, e := range warnings {
		o := e.InvolvedObject
		ns := cmpOr(o.Namespace, e.Metadata.Namespace)
		row := store.UpsertKubeEventParams{
			ClusterID: clusterID, Key: e.Metadata.Namespace + "/" + e.Metadata.Name, Ts: e.warningStart(), LastTs: e.When(),
			Count: int32(max(e.Count, 1)), Reason: clip(e.Reason, 100), Namespace: ns, ObjectKind: o.Kind, ObjectName: o.Name,
			NodeName: e.Source.Host, Message: clip(e.Message, 500),
		}
		switch o.Kind {
		case "Pod":
			if p, ok := byPod[[2]string{ns, o.Name}]; ok {
				row.AppKind, row.AppName = WorkloadOf(p.Metadata)
				row.NodeName = cmpOr(p.Spec.NodeName, row.NodeName)
			}
		case "Deployment", "StatefulSet", "DaemonSet":
			row.AppKind, row.AppName = o.Kind, o.Name
		case "ReplicaSet":
			if i := strings.LastIndexByte(o.Name, '-'); i > 0 && apps[appKey{ns, "Deployment", o.Name[:i]}] {
				row.AppKind, row.AppName = "Deployment", o.Name[:i]
			}
		case "Node":
			row.NodeName = o.Name
		}
		row.NodeID = nodeID(row.NodeName)
		if err := q.UpsertKubeEvent(ctx, row); err != nil {
			return err
		}
	}
	for _, p := range pods {
		for _, c := range p.Status.ContainerStatuses {
			for _, st := range []ContainerState{c.State, c.LastState} {
				t := st.Terminated
				if t == nil || t.Reason != "OOMKilled" || t.FinishedAt.IsZero() {
					continue
				}
				kind, name := WorkloadOf(p.Metadata)
				if err := q.UpsertKubeEvent(ctx, store.UpsertKubeEventParams{
					ClusterID: clusterID,
					Key:       "oom/" + p.Metadata.Namespace + "/" + p.Metadata.Name + "/" + c.Name + "/" + strconv.FormatInt(t.FinishedAt.Unix(), 10),
					Ts:        t.FinishedAt, LastTs: t.FinishedAt, Count: 1, Reason: "OOMKilled", Namespace: p.Metadata.Namespace,
					ObjectKind: "Pod", ObjectName: p.Metadata.Name, AppKind: kind, AppName: name, NodeName: p.Spec.NodeName,
					NodeID:  nodeID(p.Spec.NodeName),
					Message: fmt.Sprintf("Container %s stopte omdat hij meer geheugen wilde dan zijn limiet", c.Name),
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
