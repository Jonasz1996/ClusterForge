package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func (s *Server) SetPreferences(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	var in gen.Preferences
	if !decode(w, r, &in) {
		return
	}
	if err := s.q.SetUserKubeLearning(r.Context(), store.SetUserKubeLearningParams{ID: p.User.ID, KubeLearning: in.KubeLearning}); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// kubeError schrijft de fout van een Kubernetes-verzoek, met een code
// waaraan de webinterface ziet wat er te doen is.
func (s *Server) kubeError(w http.ResponseWriter, r *http.Request, err error) {
	var ue *kube.UnavailableError
	var ae *kube.APIError
	switch {
	case errors.Is(err, kube.ErrNotKubernetes):
		writeError(w, http.StatusNotFound, "not_found", "geen Kubernetes-cluster met dit id")
	case errors.Is(err, kube.ErrInvalid):
		writeError(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, kube.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.As(err, &ue) && ue.TooOld:
		writeError(w, http.StatusServiceUnavailable, "agent_too_old", ue.Error())
	case errors.As(err, &ue):
		writeError(w, http.StatusServiceUnavailable, "kube_unavailable", ue.Error())
	case errors.As(err, &ae):
		writeError(w, http.StatusBadGateway, "kube_api", ae.Error())
	default:
		s.internalError(w, r, err)
	}
}

// clusterVIPs geeft het eerste VIP per cluster: dat van de API en de apps.
func (s *Server) clusterVIPs(ctx context.Context) (map[uuid.UUID]string, error) {
	vips, err := s.q.ListAllVIPs(ctx)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]string{}
	for _, v := range vips {
		if _, ok := out[v.ClusterID]; !ok {
			out[v.ClusterID] = v.Address.String()
		}
	}
	return out, nil
}

func nullableString(m map[uuid.UUID]string, id uuid.UUID) nullable.Nullable[string] {
	if v, ok := m[id]; ok {
		return nullable.NewNullableWithValue(v)
	}
	return nullable.NewNullNullable[string]()
}

func (s *Server) ListKubeClusters(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListKubeClusters(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	vips, err := s.clusterVIPs(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.KubeClusterSummary, 0, len(rows))
	for _, c := range rows {
		items = append(items, gen.KubeClusterSummary{
			Id: c.ID, Name: c.Name, Slug: c.Slug, Environment: gen.Environment(c.Environment), Status: gen.Status(c.Status), StatusReason: c.StatusReason,
			Vip: nullableString(vips, c.ID), Synced: c.Synced, ApiOk: c.ApiOk, Error: c.Error, Version: c.Version,
			NodesTotal: int(c.NodesTotal), NodesReady: int(c.NodesReady), PodsTotal: int(c.PodsTotal), PodsRunning: int(c.PodsRunning),
			PodsPending: int(c.PodsPending), PodsProblem: int(c.PodsProblem), WorkloadsTotal: int(c.WorkloadsTotal),
			WorkloadsReady: int(c.WorkloadsReady), SyncedAt: nullableOf(c.SyncedAt), OkAt: nullableOf(c.OkAt),
		})
	}
	writeJSON(w, http.StatusOK, list[gen.KubeClusterSummary]{items})
}

func kubeUsage(u kube.Usage) gen.KubeUsage {
	return gen.KubeUsage{CpuMillis: u.CPUMillis, MemoryBytes: u.MemoryBytes}
}

func toAPIKubePod(p kube.Pod) gen.KubePod {
	ready, total := p.ReadyCount()
	images := make([]string, 0, len(p.Spec.Containers))
	for _, c := range p.Spec.Containers {
		images = append(images, c.Image)
	}
	out := gen.KubePod{
		Namespace: p.Metadata.Namespace, Name: p.Metadata.Name, Node: p.Spec.NodeName, Status: p.StatusText(), Problem: p.Problem(),
		Ready: ready, Containers: total, Restarts: p.Restarts(), Ip: p.Status.PodIP, Images: images,
		CreatedAt: p.Metadata.CreationTimestamp, System: kube.IsSystem(p.Metadata.Namespace),
	}
	if o := p.Metadata.Owner(); o != nil {
		out.OwnerKind, out.OwnerName = o.Kind, o.Name
	}
	return out
}

func (s *Server) GetKubeOverview(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	o, err := s.kube.Overview(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	vips, err := s.clusterVIPs(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.KubeOverview{
		ClusterId: clusterID, Version: o.Version, Via: o.Via, Vip: nullableString(vips, clusterID), NodesTotal: len(o.Nodes),
		Pods: gen.KubePodCounts{
			Total: o.Pods.Total, Running: o.Pods.Running, Pending: o.Pods.Pending, Succeeded: o.Pods.Succeeded, Failed: o.Pods.Failed,
			Problem: o.Pods.Problem,
		},
		WorkloadsTotal: o.Workloads, WorkloadsReady: o.WorkloadsReady, Namespaces: append([]string{}, o.Namespaces...),
		Allocatable: kubeUsage(o.Allocatable), Used: nullable.NewNullNullable[gen.KubeUsage](),
		Warnings: []gen.KubeEvent{}, Problems: []gen.KubePod{},
	}
	for _, n := range o.Nodes {
		if n.Ready() {
			out.NodesReady++
		}
	}
	if o.Used != nil {
		out.Used = nullable.NewNullableWithValue(kubeUsage(*o.Used))
	}
	for _, e := range o.Warnings {
		out.Warnings = append(out.Warnings, gen.KubeEvent{
			Namespace: e.Metadata.Namespace, Kind: e.InvolvedObject.Kind, Name: e.InvolvedObject.Name, Reason: e.Reason,
			Message: e.Message, Count: max(e.Count, 1), LastSeen: e.When(),
		})
	}
	for _, p := range o.Problems {
		out.Problems = append(out.Problems, toAPIKubePod(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ListKubeNodes(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	var (
		nodes []kube.Node
		pods  []kube.Pod
		usage map[string]kube.Usage
		errs  [3]error
		wg    sync.WaitGroup
	)
	wg.Go(func() { nodes, errs[0] = s.kube.Nodes(ctx, clusterID) })
	wg.Go(func() { pods, errs[1] = s.kube.Pods(ctx, clusterID, "", "") })
	// Zonder metrics-server staat er geen gebruik bij.
	wg.Go(func() { usage, errs[2] = s.kube.NodeUsage(ctx, clusterID) })
	wg.Wait()
	for _, err := range errs[:2] {
		if err != nil {
			s.kubeError(w, r, err)
			return
		}
	}
	links, err := s.kube.Links(ctx, clusterID, nodes)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	perNode := kube.PodsPerNode(pods)
	items := make([]gen.KubeNode, 0, len(nodes))
	for _, n := range nodes {
		alloc := kube.Usage{CPUMillis: kube.MilliCPU(n.Status.Allocatable["cpu"]), MemoryBytes: kube.Bytes(n.Status.Allocatable["memory"])}
		kn := gen.KubeNode{
			Name: n.Metadata.Name, Roles: append([]string{}, n.Roles()...), Ready: n.Ready(), Unschedulable: n.Spec.Unschedulable,
			Pressure: append([]string{}, n.Pressure()...), InternalIp: n.Address("InternalIP"),
			KubeletVersion: n.Status.NodeInfo.KubeletVersion, OsImage: n.Status.NodeInfo.OSImage, Kernel: n.Status.NodeInfo.KernelVersion,
			ContainerRuntime: n.Status.NodeInfo.ContainerRuntimeVersion, Arch: n.Status.NodeInfo.Architecture,
			Allocatable: kubeUsage(alloc), Used: nullable.NewNullNullable[gen.KubeUsage](), Node: nullable.NewNullNullable[gen.KubeNodeLink](),
			Pods: perNode[n.Metadata.Name], CreatedAt: n.Metadata.CreationTimestamp,
		}
		if !kn.Ready {
			for _, c := range n.Status.Conditions {
				if c.Type == "Ready" {
					kn.ReadyReason = c.Message
					if kn.ReadyReason == "" {
						kn.ReadyReason = c.Reason
					}
				}
			}
		}
		if u, ok := usage[n.Metadata.Name]; ok && errs[2] == nil {
			kn.Used = nullable.NewNullableWithValue(kubeUsage(u))
		}
		if l, ok := links[n.Metadata.Name]; ok {
			kn.Node = nullable.NewNullableWithValue(gen.KubeNodeLink{Id: l.NodeID, Hostname: l.Hostname, Agent: l.Agent, AgentProtocol: l.Protocol})
		}
		items = append(items, kn)
	}
	writeJSON(w, http.StatusOK, list[gen.KubeNode]{items})
}

// kubeScope is de namespace uit de query; zonder namespace telt system of
// de systeemnamespaces meedoen.
func kubeScope(ns *string, system *bool) (string, func(string) bool) {
	if ns != nil && *ns != "" {
		return *ns, func(string) bool { return true }
	}
	all := system != nil && *system
	return "", func(n string) bool { return all || !kube.IsSystem(n) }
}

func (s *Server) ListKubeWorkloads(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.ListKubeWorkloadsParams) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	ns, keep := kubeScope(params.Namespace, params.System)
	ws, err := s.kube.Workloads(ctx, clusterID, ns)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	items := make([]gen.KubeWorkload, 0, len(ws))
	for _, wl := range ws {
		if !keep(wl.Metadata.Namespace) {
			continue
		}
		state, text := wl.State()
		items = append(items, gen.KubeWorkload{
			Kind: gen.KubeWorkloadKind(wl.Kind), Namespace: wl.Metadata.Namespace, Name: wl.Metadata.Name, Desired: wl.Desired(),
			Ready: wl.ReadyPods(), UpToDate: wl.UpToDate(), Images: append([]string{}, wl.Images()...), State: gen.KubeWorkloadState(state),
			StateText: text, Schedule: wl.Spec.Schedule, CreatedAt: wl.Metadata.CreationTimestamp, System: kube.IsSystem(wl.Metadata.Namespace),
		})
	}
	writeJSON(w, http.StatusOK, list[gen.KubeWorkload]{items})
}

func (s *Server) ListKubePods(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.ListKubePodsParams) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	ns, keep := kubeScope(params.Namespace, params.System)
	node := ""
	if params.Node != nil {
		node = *params.Node
	}
	pods, err := s.kube.Pods(ctx, clusterID, ns, node)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	items := make([]gen.KubePod, 0, len(pods))
	for _, p := range pods {
		if keep(p.Metadata.Namespace) {
			items = append(items, toAPIKubePod(p))
		}
	}
	writeJSON(w, http.StatusOK, list[gen.KubePod]{items})
}
