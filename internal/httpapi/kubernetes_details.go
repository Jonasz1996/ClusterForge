package httpapi

import (
	"context"
	"encoding/base64"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// maxKubeEvents begrenst de lijst op het tabblad Gebeurtenissen.
const maxKubeEvents = 300

func toAPIKubeRef(r kube.Ref) gen.KubeRef {
	return gen.KubeRef{Group: r.Group, Version: r.Version, Resource: r.Resource, Namespace: r.Namespace, Name: r.Name}
}

func nullableTime(t time.Time) nullable.Nullable[time.Time] {
	if t.IsZero() {
		return nullable.NewNullNullable[time.Time]()
	}
	return nullable.NewNullableWithValue(t)
}

// kubeEvents zet events om, met een verwijzing naar het object als
// ClusterForge de soort kent.
func (s *Server) kubeEvents(ctx context.Context, clusterID uuid.UUID, events []kube.Event) []gen.KubeEvent {
	out := make([]gen.KubeEvent, 0, len(events))
	for _, e := range events {
		io := e.InvolvedObject
		ev := gen.KubeEvent{
			Namespace: e.Metadata.Namespace, Kind: io.Kind, Name: io.Name, Type: e.Type, Reason: e.Reason, Message: e.Message,
			Count: max(e.Count, 1), FirstSeen: nullable.NewNullNullable[time.Time](), LastSeen: e.When(), Source: e.From(),
			Object: nullable.NewNullNullable[gen.KubeRef](),
		}
		if e.FirstTimestamp != nil {
			ev.FirstSeen = nullableTime(*e.FirstTimestamp)
		}
		if t, ok := s.kube.ResourceFor(ctx, clusterID, io.APIVersion, io.Kind); ok && io.Name != "" {
			ref := gen.KubeRef{Group: t.Group, Version: t.Version, Resource: t.Resource, Name: io.Name}
			if t.Namespaced {
				ref.Namespace = io.Namespace
			}
			ev.Object = nullable.NewNullableWithValue(ref)
		}
		out = append(out, ev)
	}
	return out
}

func (s *Server) GetKubeNetwork(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.GetKubeNetworkParams) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	ns, keep := kubeScope(params.Namespace, params.System)
	services, ingresses, err := s.kube.Network(ctx, clusterID, ns)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	// Het aantal pods per Service: een Service zonder pods is een
	// veelgemaakte fout, zoals een label dat niet klopt.
	pods, err := s.kube.Pods(ctx, clusterID, ns, "")
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	out := gen.KubeNetwork{Services: []gen.KubeService{}, Ingresses: []gen.KubeIngress{}}
	for _, sv := range services {
		if !keep(sv.Metadata.Namespace) {
			continue
		}
		as := gen.KubeService{
			Namespace: sv.Metadata.Namespace, Name: sv.Metadata.Name, Type: sv.Spec.Type, ClusterIp: sv.Spec.ClusterIP,
			External: append([]string{}, sv.External()...), Ports: []gen.KubeServicePort{}, Selector: map[string]string{},
			Url: sv.URL(), System: kube.IsSystem(sv.Metadata.Namespace),
		}
		for _, p := range sv.Spec.Ports {
			as.Ports = append(as.Ports, gen.KubeServicePort{Name: p.Name, Protocol: p.Protocol, Port: p.Port, TargetPort: p.Target(), NodePort: p.NodePort})
		}
		for k, v := range sv.Spec.Selector {
			as.Selector[k] = v
		}
		for _, p := range pods {
			if p.Metadata.Namespace == sv.Metadata.Namespace && sv.Selects(p.Metadata.Labels) {
				as.Pods++
			}
		}
		out.Services = append(out.Services, as)
	}
	for _, in := range ingresses {
		if !keep(in.Metadata.Namespace) {
			continue
		}
		ai := gen.KubeIngress{
			Namespace: in.Metadata.Namespace, Name: in.Metadata.Name, Class: in.Class(), Address: append([]string{}, in.Address()...),
			Rules: []gen.KubeIngressRule{}, System: kube.IsSystem(in.Metadata.Namespace),
		}
		for _, rule := range in.Rules() {
			ai.Rules = append(ai.Rules, gen.KubeIngressRule{Host: rule.Host, Path: rule.Path, Service: rule.Service, Port: rule.Port, Tls: rule.TLS, Url: rule.URL})
		}
		out.Ingresses = append(out.Ingresses, ai)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetKubeStorage(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.GetKubeStorageParams) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	ns, keep := kubeScope(params.Namespace, params.System)
	st, err := s.kube.Storage(ctx, clusterID, ns)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	out := gen.KubeStorage{Claims: []gen.KubeClaim{}, Volumes: []gen.KubeVolume{}, Classes: []gen.KubeStorageClass{}}
	for _, c := range st.Claims {
		if !keep(c.Metadata.Namespace) {
			continue
		}
		size := c.Status.Capacity["storage"]
		if size == "" {
			size = c.Spec.Resources.Requests["storage"]
		}
		out.Claims = append(out.Claims, gen.KubeClaim{
			Namespace: c.Metadata.Namespace, Name: c.Metadata.Name, Phase: c.Status.Phase, Class: c.Class(), Volume: c.Spec.VolumeName,
			Bytes: kube.Bytes(size), AccessModes: append([]string{}, c.Spec.AccessModes...),
			UsedBy: append([]string{}, st.UsedBy[c.Metadata.Namespace+"/"+c.Metadata.Name]...), System: kube.IsSystem(c.Metadata.Namespace),
		})
	}
	for _, v := range st.Volumes {
		claim := ""
		if v.Spec.ClaimRef != nil {
			if !keep(v.Spec.ClaimRef.Namespace) {
				continue
			}
			claim = v.Spec.ClaimRef.Namespace + "/" + v.Spec.ClaimRef.Name
		}
		out.Volumes = append(out.Volumes, gen.KubeVolume{
			Name: v.Metadata.Name, Phase: v.Status.Phase, Class: v.Spec.StorageClassName, Bytes: kube.Bytes(v.Spec.Capacity["storage"]),
			Reclaim: v.Spec.PersistentVolumeReclaimPolicy, Claim: claim, Node: v.Node(), Path: v.Path(),
		})
	}
	for _, c := range st.Classes {
		sc := gen.KubeStorageClass{Name: c.Metadata.Name, Provisioner: c.Provisioner, Default: c.Default(), Reclaim: "Delete", Binding: "Immediate"}
		if c.ReclaimPolicy != nil {
			sc.Reclaim = *c.ReclaimPolicy
		}
		if c.VolumeBindingMode != nil {
			sc.Binding = *c.VolumeBindingMode
		}
		if c.AllowVolumeExpansion != nil {
			sc.Expansion = *c.AllowVolumeExpansion
		}
		out.Classes = append(out.Classes, sc)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetKubeConfig(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.GetKubeConfigParams) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	ns, keep := kubeScope(params.Namespace, params.System)
	maps, secrets, err := s.kube.Config(ctx, clusterID, ns)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	out := gen.KubeConfig{ConfigMaps: []gen.KubeConfigMap{}, Secrets: []gen.KubeSecret{}}
	for _, m := range maps {
		if !keep(m.Metadata.Namespace) {
			continue
		}
		cm := gen.KubeConfigMap{
			Namespace: m.Metadata.Namespace, Name: m.Metadata.Name, Keys: []string{}, CreatedAt: m.Metadata.CreationTimestamp,
			System: kube.IsSystem(m.Metadata.Namespace),
		}
		for k, v := range m.Data {
			cm.Keys = append(cm.Keys, k)
			cm.Bytes += len(v)
		}
		for k, v := range m.BinaryData {
			cm.Keys = append(cm.Keys, k)
			cm.Bytes += base64.StdEncoding.DecodedLen(len(v))
		}
		slices.Sort(cm.Keys)
		out.ConfigMaps = append(out.ConfigMaps, cm)
	}
	for _, sec := range secrets {
		if !keep(sec.Metadata.Namespace) {
			continue
		}
		as := gen.KubeSecret{
			Namespace: sec.Metadata.Namespace, Name: sec.Metadata.Name, Type: sec.Type, Keys: []gen.KubeSecretKey{},
			CreatedAt: sec.Metadata.CreationTimestamp, System: kube.IsSystem(sec.Metadata.Namespace),
		}
		for _, k := range sec.Keys() {
			as.Keys = append(as.Keys, gen.KubeSecretKey{Name: k.Name, Bytes: k.Bytes})
		}
		out.Secrets = append(out.Secrets, as)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ListKubeEvents(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.ListKubeEventsParams) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	ns, keep := kubeScope(params.Namespace, params.System)
	events, err := s.kube.Events(ctx, clusterID, ns, math.MaxInt)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	warnings := params.Warnings != nil && *params.Warnings
	kept := make([]kube.Event, 0, min(len(events), maxKubeEvents))
	for _, e := range events {
		if len(kept) == maxKubeEvents {
			break
		}
		if keep(e.Metadata.Namespace) && (!warnings || e.Type == "Warning") {
			kept = append(kept, e)
		}
	}
	writeJSON(w, http.StatusOK, list[gen.KubeEvent]{s.kubeEvents(ctx, clusterID, kept)})
}

func (s *Server) ListKubeResources(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	types, err := s.kube.Resources(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	items := make([]gen.KubeResourceType, 0, len(types))
	for _, t := range types {
		items = append(items, gen.KubeResourceType{Group: t.Group, Version: t.Version, Resource: t.Resource, Kind: t.Kind, Namespaced: t.Namespaced})
	}
	writeJSON(w, http.StatusOK, list[gen.KubeResourceType]{items})
}

func (s *Server) ListKubeObjects(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.ListKubeObjectsParams) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	ns, keep := kubeScope(params.Namespace, params.System)
	objs, err := s.kube.Objects(ctx, clusterID, kube.Ref{Group: deref(params.Group), Version: params.Version, Resource: params.Resource, Namespace: ns})
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	items := make([]gen.KubeObjectSummary, 0, len(objs))
	for _, o := range objs {
		if keep(o.Namespace) {
			items = append(items, gen.KubeObjectSummary{Namespace: o.Namespace, Name: o.Name, CreatedAt: o.Created, Status: o.Status})
		}
	}
	writeJSON(w, http.StatusOK, list[gen.KubeObjectSummary]{items})
}

// toAPIContainer zet een container met zijn status om: wat hij nu doet
// en, als hij herstartte, waarom de vorige stopte.
func toAPIContainer(c kube.Container, st *kube.ContainerStatus, init bool) gen.KubeContainer {
	out := gen.KubeContainer{
		Name: c.Name, Image: c.Image, Init: init, State: gen.KubeContainerStateUnknown, Since: nullable.NewNullNullable[time.Time](),
		LastExitCode: nullable.NewNullNullable[int](), LastFinished: nullable.NewNullNullable[time.Time](),
	}
	if st == nil {
		return out
	}
	out.Ready, out.Restarts = st.Ready, st.RestartCount
	switch s := st.State; {
	case s.Running != nil:
		out.State, out.Since = gen.KubeContainerStateRunning, nullableTime(s.Running.StartedAt)
	case s.Waiting != nil:
		out.State, out.Reason, out.Message = gen.KubeContainerStateWaiting, s.Waiting.Reason, s.Waiting.Message
	case s.Terminated != nil:
		out.State, out.Reason, out.Message = gen.KubeContainerStateTerminated, s.Terminated.Reason, s.Terminated.Message
		out.Since = nullableTime(s.Terminated.FinishedAt)
	}
	if t := st.LastState.Terminated; t != nil {
		out.LastReason = t.Reason
		out.LastExitCode = nullable.NewNullableWithValue(t.ExitCode)
		out.LastFinished = nullableTime(t.FinishedAt)
	}
	return out
}

func toAPIPodDetail(p kube.Pod) gen.KubePodDetail {
	out := gen.KubePodDetail{Node: p.Spec.NodeName, Ip: p.Status.PodIP, HostIp: p.Status.HostIP, Qos: p.Status.QOSClass, Containers: []gen.KubeContainer{}}
	find := func(statuses []kube.ContainerStatus, name string) *kube.ContainerStatus {
		for i := range statuses {
			if statuses[i].Name == name {
				return &statuses[i]
			}
		}
		return nil
	}
	for _, c := range p.Spec.InitContainers {
		out.Containers = append(out.Containers, toAPIContainer(c, find(p.Status.InitContainerStatuses, c.Name), true))
	}
	for _, c := range p.Spec.Containers {
		out.Containers = append(out.Containers, toAPIContainer(c, find(p.Status.ContainerStatuses, c.Name), false))
	}
	return out
}

func (s *Server) GetKubeObject(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.GetKubeObjectParams) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	ref := kube.Ref{Group: deref(params.Group), Version: params.Version, Resource: params.Resource, Namespace: deref(params.Namespace), Name: params.Name}
	d, err := s.kube.Object(ctx, clusterID, ref)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	out := gen.KubeObject{
		Ref: toAPIKubeRef(d.Ref), ApiVersion: d.APIVersion, Kind: d.Kind, Uid: d.Meta.UID, CreatedAt: d.Meta.CreationTimestamp,
		Labels: map[string]string{}, Annotations: map[string]string{}, Summary: d.Summary, Problem: d.Problem,
		Conditions: []gen.KubeCondition{}, Chain: []gen.KubeChainLink{}, Events: s.kubeEvents(ctx, clusterID, d.Events), Yaml: d.YAML,
		Pod: nullable.NewNullNullable[gen.KubePodDetail](), Actions: []gen.KubeActionName{}, Replicas: nullable.NewNullNullable[int](),
	}
	for _, a := range d.Actions {
		out.Actions = append(out.Actions, gen.KubeActionName(a))
	}
	if d.Replicas != nil {
		out.Replicas = nullable.NewNullableWithValue(*d.Replicas)
	}
	for k, v := range d.Meta.Labels {
		out.Labels[k] = v
	}
	for k, v := range d.Meta.Annotations {
		out.Annotations[k] = v
	}
	for _, c := range d.Conditions {
		out.Conditions = append(out.Conditions, gen.KubeCondition{
			Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message, LastTransition: nullableTime(c.LastTransitionTime),
		})
	}
	for _, l := range d.Chain {
		al := gen.KubeChainLink{
			Role: gen.KubeChainLinkRole(l.Role), Kind: l.Kind, Name: l.Name, Text: l.Text, Url: l.URL,
			Ref: nullable.NewNullNullable[gen.KubeRef](), NodeId: nullable.NewNullNullable[uuid.UUID](),
		}
		if l.Ref != nil {
			al.Ref = nullable.NewNullableWithValue(toAPIKubeRef(*l.Ref))
		}
		if l.NodeID != nil {
			al.NodeId = nullable.NewNullableWithValue(*l.NodeID)
		}
		out.Chain = append(out.Chain, al)
	}
	if d.Pod != nil {
		out.Pod = nullable.NewNullableWithValue(toAPIPodDetail(*d.Pod))
	}
	writeJSON(w, http.StatusOK, out)
}

// defaultLogLines is het aantal regels log als de vraag er geen noemt.
const defaultLogLines = 500

func (s *Server) GetKubeLogs(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.GetKubeLogsParams) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	o := kube.LogOptions{
		Container: deref(params.Container), Previous: deref(params.Previous), Lines: defaultLogLines, Timestamps: deref(params.Timestamps),
	}
	if params.Lines != nil {
		if *params.Lines < 1 || *params.Lines > protocol.KubeMaxLogLines {
			writeError(w, http.StatusBadRequest, "validation", "lines moet tussen 1 en 5000 liggen")
			return
		}
		o.Lines = *params.Lines
	}
	text, trimmed, err := s.kube.Logs(ctx, clusterID, params.Namespace, params.Pod, o)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.KubeLogs{Text: text, Truncated: trimmed})
}
