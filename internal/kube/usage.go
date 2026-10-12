package kube

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/metrics"
)

// MetricsSink neemt de metingen uit metrics-server aan; de Ingester van
// VictoriaMetrics doet dat.
type MetricsSink interface {
	IngestKube(ctx context.Context, clusterID uuid.UUID, samples []metrics.KubeSample, now time.Time) error
}

// MetricsInterval is hoe vaak de sync CPU en geheugen uit metrics-server
// leest.
const MetricsInterval = time.Minute

type podMetrics struct {
	Metadata   Meta `json:"metadata"`
	Containers []struct {
		Name  string            `json:"name"`
		Usage map[string]string `json:"usage"`
	} `json:"containers"`
}

type nodeMetrics struct {
	Metadata Meta              `json:"metadata"`
	Usage    map[string]string `json:"usage"`
}

// usageSamples leest CPU en geheugen per pod en per node uit metrics-server.
// Een pod krijgt de node waarop hij draait, met de ClusterForge-node als
// die gekoppeld is, en de app waar hij bij hoort.
func (s *Service) usageSamples(ctx context.Context, clusterID uuid.UUID, pods []Pod, links map[string]NodeLink) ([]metrics.KubeSample, error) {
	pm, err := List[podMetrics](ctx, s, clusterID, "/apis/metrics.k8s.io/v1beta1/pods", nil)
	if err != nil {
		return nil, err
	}
	nm, err := List[nodeMetrics](ctx, s, clusterID, "/apis/metrics.k8s.io/v1beta1/nodes", nil)
	if err != nil {
		return nil, err
	}
	byPod := map[[2]string]Pod{}
	for _, p := range pods {
		byPod[[2]string{p.Metadata.Namespace, p.Metadata.Name}] = p
	}
	node := func(name string) (string, uuid.UUID) {
		if l, ok := links[name]; ok {
			return l.Hostname, l.NodeID
		}
		return name, uuid.Nil
	}
	out := make([]metrics.KubeSample, 0, 2*(len(pm)+len(nm)))
	for _, m := range pm {
		var cpu float64
		var mem int64
		for _, c := range m.Containers {
			cpu += Cores(c.Usage["cpu"])
			mem += Bytes(c.Usage["memory"])
		}
		labels := map[string]string{"namespace": m.Metadata.Namespace, "pod": m.Metadata.Name}
		var host string
		var hostID uuid.UUID
		if p, ok := byPod[[2]string{m.Metadata.Namespace, m.Metadata.Name}]; ok {
			host, hostID = node(p.Spec.NodeName)
			if kind, name := WorkloadOf(p.Metadata); kind != "" {
				labels["workload_kind"], labels["workload"] = kind, name
			}
		}
		out = append(out,
			metrics.KubeSample{Name: metrics.KubePodCPU, Node: host, NodeID: hostID, Labels: labels, Value: cpu},
			metrics.KubeSample{Name: metrics.KubePodMemory, Node: host, NodeID: hostID, Labels: labels, Value: float64(mem)},
		)
	}
	for _, m := range nm {
		host, hostID := node(m.Metadata.Name)
		out = append(out,
			metrics.KubeSample{Name: metrics.KubeNodeCPU, Node: host, NodeID: hostID, Value: Cores(m.Usage["cpu"])},
			metrics.KubeSample{Name: metrics.KubeNodeMemory, Node: host, NodeID: hostID, Value: float64(Bytes(m.Usage["memory"]))},
		)
	}
	return out, nil
}
