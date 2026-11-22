package kube

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var (
	dnsLabel     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	dnsSubdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

// ValidName is true voor een geldige naam van een node of object.
func ValidName(s string) bool { return len(s) <= 253 && dnsSubdomain.MatchString(s) }

// nsPath is het pad van een soort objecten, in één namespace of in alle.
func nsPath(prefix, ns, resource string) (string, error) {
	if ns == "" {
		return prefix + "/" + resource, nil
	}
	if !dnsLabel.MatchString(ns) {
		return "", fmt.Errorf("%w: namespace %q", ErrInvalid, ns)
	}
	return prefix + "/namespaces/" + ns + "/" + resource, nil
}

// Version geeft de versie van Kubernetes, zoals v1.35.9+k3s1.
func (s *Service) Version(ctx context.Context, clusterID uuid.UUID) (string, error) {
	v, err := GetInto[struct {
		GitVersion string `json:"gitVersion"`
	}](ctx, s, clusterID, "/version")
	return v.GitVersion, err
}

// Nodes geeft de Kubernetes-nodes, op naam.
func (s *Service) Nodes(ctx context.Context, clusterID uuid.UUID) ([]Node, error) {
	nodes, err := List[Node](ctx, s, clusterID, "/api/v1/nodes", nil)
	slices.SortFunc(nodes, func(a, b Node) int { return cmp.Compare(a.Metadata.Name, b.Metadata.Name) })
	return nodes, err
}

// Pods geeft de pods in een namespace, of in alle als ns leeg is. Is node
// gezet, dan alleen de pods op die node.
func (s *Service) Pods(ctx context.Context, clusterID uuid.UUID, ns, node string) ([]Pod, error) {
	p, err := nsPath("/api/v1", ns, "pods")
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if node != "" {
		if !ValidName(node) {
			return nil, fmt.Errorf("%w: node %q", ErrInvalid, node)
		}
		q.Set("fieldSelector", "spec.nodeName="+node)
	}
	pods, err := List[Pod](ctx, s, clusterID, p, q)
	slices.SortFunc(pods, func(a, b Pod) int {
		return cmp.Or(cmp.Compare(a.Metadata.Namespace, b.Metadata.Namespace), cmp.Compare(a.Metadata.Name, b.Metadata.Name))
	})
	return pods, err
}

// workloadKinds zijn de soorten op het tabblad Apps, met hun pad.
var workloadKinds = []struct{ kind, prefix, resource string }{
	{"Deployment", "/apis/apps/v1", "deployments"},
	{"StatefulSet", "/apis/apps/v1", "statefulsets"},
	{"DaemonSet", "/apis/apps/v1", "daemonsets"},
	{"CronJob", "/apis/batch/v1", "cronjobs"},
	{"Job", "/apis/batch/v1", "jobs"},
}

// Workloads geeft de Deployments, StatefulSets, DaemonSets, CronJobs en
// Jobs. Een Job van een CronJob staat er niet los bij.
func (s *Service) Workloads(ctx context.Context, clusterID uuid.UUID, ns string) ([]Workload, error) {
	lists := make([][]Workload, len(workloadKinds))
	errs := make([]error, len(workloadKinds))
	var wg sync.WaitGroup
	for i, k := range workloadKinds {
		p, err := nsPath(k.prefix, ns, k.resource)
		if err != nil {
			return nil, err
		}
		wg.Go(func() {
			lists[i], errs[i] = List[Workload](ctx, s, clusterID, p, nil)
			for j := range lists[i] {
				lists[i][j].Kind = k.kind
			}
		})
	}
	wg.Wait()
	var out []Workload
	for i := range lists {
		if errs[i] != nil {
			return nil, errs[i]
		}
		for _, w := range lists[i] {
			if o := w.Metadata.Owner(); w.Kind == "Job" && o != nil && o.Kind == "CronJob" {
				continue
			}
			out = append(out, w)
		}
	}
	slices.SortStableFunc(out, func(a, b Workload) int {
		return cmp.Or(cmp.Compare(a.Metadata.Namespace, b.Metadata.Namespace), cmp.Compare(a.Metadata.Name, b.Metadata.Name))
	})
	return out, nil
}

// Namespaces geeft de namen van de namespaces.
func (s *Service) Namespaces(ctx context.Context, clusterID uuid.UUID) ([]string, error) {
	items, err := List[struct {
		Metadata Meta `json:"metadata"`
	}](ctx, s, clusterID, "/api/v1/namespaces", nil)
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Metadata.Name)
	}
	slices.Sort(out)
	return out, err
}

// Usage is het gebruik van CPU en geheugen volgens metrics-server.
type Usage struct {
	CPUMillis   int64
	MemoryBytes int64
}

// NodeUsage geeft het gebruik per node uit metrics-server, of een fout als
// die er niet is.
func (s *Service) NodeUsage(ctx context.Context, clusterID uuid.UUID) (map[string]Usage, error) {
	items, err := List[struct {
		Metadata Meta              `json:"metadata"`
		Usage    map[string]string `json:"usage"`
	}](ctx, s, clusterID, "/apis/metrics.k8s.io/v1beta1/nodes", nil)
	if err != nil {
		return nil, err
	}
	out := map[string]Usage{}
	for _, it := range items {
		out[it.Metadata.Name] = Usage{CPUMillis: MilliCPU(it.Usage["cpu"]), MemoryBytes: Bytes(it.Usage["memory"])}
	}
	return out, nil
}

// Warnings geeft de laatste waarschuwingen, nieuwste eerst.
func (s *Service) Warnings(ctx context.Context, clusterID uuid.UUID, limit int) ([]Event, error) {
	q := url.Values{"fieldSelector": {"type=Warning"}}
	events, err := List[Event](ctx, s, clusterID, "/api/v1/events", q)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(events, func(a, b Event) int { return b.When().Compare(a.When()) })
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

// PodCounts telt pods per status.
type PodCounts struct {
	Total, Running, Pending, Succeeded, Failed, Problem int
}

// CountPods telt de pods.
func CountPods(pods []Pod) PodCounts {
	var c PodCounts
	for _, p := range pods {
		c.Total++
		switch p.Status.Phase {
		case "Running":
			c.Running++
		case "Pending":
			c.Pending++
		case "Succeeded":
			c.Succeeded++
		case "Failed":
			c.Failed++
		}
		if p.Problem() {
			c.Problem++
		}
	}
	return c
}

// Overview is wat er op het tabblad Overzicht staat.
type Overview struct {
	Version   string
	Via       string
	Nodes     []Node
	Pods      PodCounts
	Workloads int
	// WorkloadsReady telt de workloads die doen wat ze moeten.
	WorkloadsReady int
	Namespaces     []string
	// Allocatable is wat de nodes samen aan pods kunnen geven.
	Allocatable Usage
	// Used is nil zonder metrics-server.
	Used     *Usage
	Warnings []Event
	// Problems zijn de pods die aandacht vragen, hoogstens tien.
	Problems []Pod
}

// Overview leest alles voor het tabblad Overzicht tegelijk.
func (s *Service) Overview(ctx context.Context, clusterID uuid.UUID) (Overview, error) {
	var o Overview
	var (
		pods      []Pod
		workloads []Workload
		usage     map[string]Usage
		errs      [6]error
		via       uuid.UUID
	)
	// Eerst /version: dat kiest de node, en de andere verzoeken gaan naar
	// dezelfde.
	body, via, err := s.get(ctx, clusterID, "/version", nil)
	if err != nil {
		return o, err
	}
	o.Version, _ = parseVersion(body)
	if r, err := s.q.GetNodeRef(ctx, via); err == nil {
		o.Via = r.Hostname
	}
	var wg sync.WaitGroup
	wg.Go(func() { o.Nodes, errs[0] = s.Nodes(ctx, clusterID) })
	wg.Go(func() { pods, errs[1] = s.Pods(ctx, clusterID, "", "") })
	wg.Go(func() { workloads, errs[2] = s.Workloads(ctx, clusterID, "") })
	wg.Go(func() { o.Namespaces, errs[3] = s.Namespaces(ctx, clusterID) })
	// metrics-server en de events zijn mooi meegenomen; zonder gaat het ook.
	wg.Go(func() { usage, errs[4] = s.NodeUsage(ctx, clusterID) })
	wg.Go(func() { o.Warnings, errs[5] = s.Warnings(ctx, clusterID, 5) })
	wg.Wait()
	for _, err := range errs[:4] {
		if err != nil {
			return o, err
		}
	}
	o.Pods = CountPods(pods)
	o.Workloads = len(workloads)
	for _, w := range workloads {
		if w.Healthy() {
			o.WorkloadsReady++
		}
	}
	for _, n := range o.Nodes {
		o.Allocatable.CPUMillis += MilliCPU(n.Status.Allocatable["cpu"])
		o.Allocatable.MemoryBytes += Bytes(n.Status.Allocatable["memory"])
	}
	if errs[4] == nil && len(usage) > 0 {
		var u Usage
		for _, x := range usage {
			u.CPUMillis += x.CPUMillis
			u.MemoryBytes += x.MemoryBytes
		}
		o.Used = &u
	}
	for _, p := range pods {
		if p.Problem() && len(o.Problems) < 10 {
			o.Problems = append(o.Problems, p)
		}
	}
	return o, nil
}

// NodeLink is de ClusterForge-node bij een Kubernetes-node.
type NodeLink struct {
	NodeID   uuid.UUID
	Hostname string
	// Agent is true als de node een agent heeft; Protocol is zijn versie.
	Agent    bool
	Protocol int
}

// Links koppelt de Kubernetes-nodes aan de ClusterForge-nodes van het
// cluster: op de nodenaam uit de facts, op adres, en op hostname.
func (s *Service) Links(ctx context.Context, clusterID uuid.UUID, nodes []Node) (map[string]NodeLink, error) {
	rows, err := s.q.ListNodesByCluster(ctx, &clusterID)
	if err != nil {
		return nil, err
	}
	bridge, err := s.q.ListKubeBridgeNodes(ctx, &clusterID)
	if err != nil {
		return nil, err
	}
	factName := map[uuid.UUID]string{}
	proto := map[uuid.UUID]int{}
	for _, b := range bridge {
		proto[b.ID] = int(b.AgentProtocol)
		var f protocol.KubeFacts
		if len(b.Kube) > 0 && json.Unmarshal(b.Kube, &f) == nil && f.NodeName != "" {
			factName[b.ID] = f.NodeName
		}
	}
	out := map[string]NodeLink{}
	for _, n := range nodes {
		name, ip := n.Metadata.Name, n.Address("InternalIP")
		match := func(pred func(r int) bool) bool {
			for i, r := range rows {
				if pred(i) {
					p, ok := proto[r.Node.ID]
					out[name] = NodeLink{NodeID: r.Node.ID, Hostname: r.Node.Hostname, Agent: ok, Protocol: p}
					return true
				}
			}
			return false
		}
		_ = match(func(i int) bool { return factName[rows[i].Node.ID] == name }) ||
			match(func(i int) bool {
				return ip != "" && rows[i].Node.PrimaryIp != nil && rows[i].Node.PrimaryIp.String() == ip
			}) ||
			match(func(i int) bool { return rows[i].Node.Hostname == name })
	}
	return out, nil
}

// PodsPerNode telt de pods per node, zonder de voltooide.
func PodsPerNode(pods []Pod) map[string]int {
	out := map[string]int{}
	for _, p := range pods {
		if p.Spec.NodeName != "" && p.Status.Phase != "Succeeded" && p.Status.Phase != "Failed" {
			out[p.Spec.NodeName]++
		}
	}
	return out
}

// NotReady geeft de namen uit want die geen Ready node in Kubernetes zijn.
func (s *Service) NotReady(ctx context.Context, clusterID uuid.UUID, want []string) ([]string, error) {
	nodes, err := s.Nodes(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	ready := map[string]bool{}
	for _, n := range nodes {
		ready[n.Metadata.Name] = n.Ready()
	}
	var out []string
	for _, w := range want {
		if !ready[w] {
			out = append(out, w)
		}
	}
	return out, nil
}
