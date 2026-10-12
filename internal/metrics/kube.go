package metrics

import (
	"bytes"
	"context"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Namen van de metingen uit metrics-server. CPU is in cores, geheugen de
// working set in bytes, zoals kubectl top ze toont.
const (
	KubePodCPU     = "kube_pod_cpu_usage_cores"
	KubePodMemory  = "kube_pod_memory_working_set_bytes"
	KubeNodeCPU    = "kube_node_cpu_usage_cores"
	KubeNodeMemory = "kube_node_memory_working_set_bytes"
)

var kubeMetricName = regexp.MustCompile(`^kube_[a-z0-9_]{1,100}$`)

// KubeSample is een meting van een pod of node uit metrics-server.
type KubeSample struct {
	Name string
	// Node is de hostname van de node in ClusterForge, of de naam in
	// Kubernetes als hij niet gekoppeld is; dan is NodeID leeg.
	Node   string
	NodeID uuid.UUID
	// Labels zijn namespace, pod, workload en workload_kind.
	Labels map[string]string
	Value  float64
}

// IngestKube schrijft de metingen van een Kubernetes-cluster, met dezelfde
// labels als de metrics van een node plus namespace en pod.
func (i *Ingester) IngestKube(ctx context.Context, clusterID uuid.UUID, samples []KubeSample, now time.Time) error {
	if !i.Enabled() || len(samples) == 0 {
		return nil
	}
	c, err := i.q.GetCluster(ctx, clusterID)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	writeKubeLines(&buf, c.Slug, clusterID.String(), string(c.Environment), samples, now)
	i.queue(buf.Bytes())
	return nil
}

func writeKubeLines(buf *bytes.Buffer, cluster, clusterID, env string, samples []KubeSample, now time.Time) {
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	for _, s := range samples {
		if !kubeMetricName.MatchString(s.Name) || math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			continue
		}
		nodeID := ""
		if s.NodeID != uuid.Nil {
			nodeID = s.NodeID.String()
		}
		buf.WriteString(s.Name)
		buf.WriteByte('{')
		buf.WriteString(labelPrefix(s.Node, nodeID, cluster, clusterID, env))
		keys := make([]string, 0, len(s.Labels))
		for k, v := range s.Labels {
			if v != "" && labelName.MatchString(k) && !slices.Contains(serverLabels, k) && !strings.HasPrefix(k, "__") {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			buf.WriteByte(',')
			writeLabel(buf, k, s.Labels[k])
		}
		buf.WriteString("} ")
		buf.WriteString(strconv.FormatFloat(s.Value, 'g', -1, 64))
		buf.WriteByte(' ')
		buf.WriteString(ts)
		buf.WriteByte('\n')
	}
}

// Eenheden van de grafieken van Kubernetes.
const (
	UnitMillicores Unit = "millicores"
	UnitBytes      Unit = "bytes"
)

// KubeClusterPanels zijn CPU en geheugen van een Kubernetes-cluster, met
// een lijn per namespace.
var KubeClusterPanels = []Panel{
	{ID: "cpu", Title: "CPU per namespace", Unit: UnitMillicores, Queries: []PanelQuery{
		{`1000 * sum by (namespace) (` + KubePodCPU + `{$sel})`, "{namespace}"},
	}},
	{ID: "memory", Title: "Geheugen per namespace", Unit: UnitBytes, Queries: []PanelQuery{
		{`sum by (namespace) (` + KubePodMemory + `{$sel})`, "{namespace}"},
	}},
}

// KubePodPanels zijn CPU en geheugen met een lijn per pod, voor een app of
// één pod.
var KubePodPanels = []Panel{
	{ID: "cpu", Title: "CPU per pod", Unit: UnitMillicores, Queries: []PanelQuery{
		{`1000 * sum by (pod) (` + KubePodCPU + `{$sel})`, "{pod}"},
	}},
	{ID: "memory", Title: "Geheugen per pod", Unit: UnitBytes, Queries: []PanelQuery{
		{`sum by (pod) (` + KubePodMemory + `{$sel})`, "{pod}"},
	}},
}

// KubeSelector maakt de labelselector voor een cluster, en eventueel een
// namespace, een app of een pod. De namen moeten geldige
// Kubernetes-namen zijn; die bevatten geen tekens die in PromQL iets
// betekenen.
func KubeSelector(clusterID uuid.UUID, namespace, workloadKind, workload, pod string) string {
	s := ClusterSelector(clusterID)
	for _, kv := range [][2]string{{"namespace", namespace}, {"workload_kind", workloadKind}, {"workload", workload}, {"pod", pod}} {
		if kv[1] != "" {
			s += `,` + kv[0] + `="` + kv[1] + `"`
		}
	}
	return s
}
