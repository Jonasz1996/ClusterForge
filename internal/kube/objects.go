package kube

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// De objecten van de API, met alleen de velden die ClusterForge leest.

type Meta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid"`
	CreationTimestamp time.Time         `json:"creationTimestamp"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp"`
	Generation        int64             `json:"generation"`
	Labels            map[string]string `json:"labels"`
	Annotations       map[string]string `json:"annotations"`
	OwnerReferences   []OwnerRef        `json:"ownerReferences"`
}

type OwnerRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Controller *bool  `json:"controller"`
}

// Owner is de eigenaar die het object beheert, zoals de ReplicaSet van een
// pod.
func (m Meta) Owner() *OwnerRef {
	for i, o := range m.OwnerReferences {
		if o.Controller != nil && *o.Controller {
			return &m.OwnerReferences[i]
		}
	}
	return nil
}

type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

func condition(cs []Condition, typ string) (Condition, bool) {
	for _, c := range cs {
		if c.Type == typ {
			return c, true
		}
	}
	return Condition{}, false
}

type Node struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		Unschedulable bool `json:"unschedulable"`
	} `json:"spec"`
	Status struct {
		Conditions []Condition `json:"conditions"`
		Addresses  []struct {
			Type    string `json:"type"`
			Address string `json:"address"`
		} `json:"addresses"`
		Capacity    map[string]string `json:"capacity"`
		Allocatable map[string]string `json:"allocatable"`
		NodeInfo    struct {
			KubeletVersion          string `json:"kubeletVersion"`
			OSImage                 string `json:"osImage"`
			KernelVersion           string `json:"kernelVersion"`
			ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
			Architecture            string `json:"architecture"`
		} `json:"nodeInfo"`
	} `json:"status"`
}

// Ready is true als de conditie Ready True is.
func (n Node) Ready() bool {
	c, ok := condition(n.Status.Conditions, "Ready")
	return ok && c.Status == "True"
}

// Address geeft het adres van een soort, zoals InternalIP.
func (n Node) Address(typ string) string {
	for _, a := range n.Status.Addresses {
		if a.Type == typ {
			return a.Address
		}
	}
	return ""
}

// Roles zijn de rollen uit de labels node-role.kubernetes.io/<rol>.
func (n Node) Roles() []string {
	var out []string
	for k := range n.Metadata.Labels {
		if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && r != "" {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return out
}

// Pressure zijn de condities die een probleem melden, zoals DiskPressure.
func (n Node) Pressure() []string {
	var out []string
	for _, c := range n.Status.Conditions {
		if c.Type != "Ready" && c.Status == "True" {
			out = append(out, c.Type)
		}
	}
	return out
}

type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type ContainerState struct {
	Waiting *struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"waiting"`
	Running *struct {
		StartedAt time.Time `json:"startedAt"`
	} `json:"running"`
	Terminated *struct {
		Reason   string `json:"reason"`
		Message  string `json:"message"`
		ExitCode int    `json:"exitCode"`
		Signal   int    `json:"signal"`
	} `json:"terminated"`
}

type ContainerStatus struct {
	Name         string         `json:"name"`
	Image        string         `json:"image"`
	Ready        bool           `json:"ready"`
	RestartCount int            `json:"restartCount"`
	State        ContainerState `json:"state"`
}

type Pod struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		NodeName       string      `json:"nodeName"`
		Containers     []Container `json:"containers"`
		InitContainers []Container `json:"initContainers"`
	} `json:"spec"`
	Status struct {
		Phase                 string            `json:"phase"`
		Reason                string            `json:"reason"`
		Message               string            `json:"message"`
		PodIP                 string            `json:"podIP"`
		StartTime             *time.Time        `json:"startTime"`
		Conditions            []Condition       `json:"conditions"`
		ContainerStatuses     []ContainerStatus `json:"containerStatuses"`
		InitContainerStatuses []ContainerStatus `json:"initContainerStatuses"`
	} `json:"status"`
}

// Restarts telt de herstarts van alle containers.
func (p Pod) Restarts() int {
	n := 0
	for _, c := range p.Status.ContainerStatuses {
		n += c.RestartCount
	}
	return n
}

// ReadyCount geeft hoeveel containers klaar zijn, van hoeveel.
func (p Pod) ReadyCount() (ready, total int) {
	for _, c := range p.Status.ContainerStatuses {
		if c.Ready {
			ready++
		}
	}
	return ready, max(len(p.Spec.Containers), len(p.Status.ContainerStatuses))
}

// StatusText is de status zoals kubectl get pods hem toont: Running,
// Completed, CrashLoopBackOff, ImagePullBackOff, Init:..., Terminating.
func (p Pod) StatusText() string {
	reason := cmpOr(p.Status.Reason, p.Status.Phase, "Unknown")
	for i, c := range p.Status.InitContainerStatuses {
		switch {
		case c.State.Terminated != nil && c.State.Terminated.ExitCode == 0:
			continue
		case c.State.Terminated != nil:
			reason = "Init:" + cmpOr(c.State.Terminated.Reason, "Error")
		case c.State.Waiting != nil && c.State.Waiting.Reason != "" && c.State.Waiting.Reason != "PodInitializing":
			reason = "Init:" + c.State.Waiting.Reason
		default:
			reason = fmt.Sprintf("Init:%d/%d", i, len(p.Spec.InitContainers))
		}
		return reason
	}
	running := false
	for i := len(p.Status.ContainerStatuses) - 1; i >= 0; i-- {
		c := p.Status.ContainerStatuses[i]
		switch {
		case c.State.Waiting != nil && c.State.Waiting.Reason != "":
			reason = c.State.Waiting.Reason
		case c.State.Terminated != nil && c.State.Terminated.Reason != "":
			reason = c.State.Terminated.Reason
		case c.State.Terminated != nil && c.State.Terminated.Signal != 0:
			reason = fmt.Sprintf("Signal:%d", c.State.Terminated.Signal)
		case c.State.Terminated != nil:
			reason = fmt.Sprintf("ExitCode:%d", c.State.Terminated.ExitCode)
		case c.Ready && c.State.Running != nil:
			running = true
		}
	}
	if reason == "Completed" && running {
		reason = "Running"
	}
	if p.Metadata.DeletionTimestamp != nil {
		reason = "Terminating"
	}
	return reason
}

// Problem is true als de pod aandacht vraagt: hij wacht met een fout,
// faalde, of draait zonder klaar te zijn.
func (p Pod) Problem() bool {
	s := p.StatusText()
	switch s {
	case "Running":
		ready, total := p.ReadyCount()
		return ready < total
	case "Completed", "Succeeded", "Pending", "ContainerCreating", "PodInitializing", "Terminating":
		return false
	}
	// Init:0/1 is wachten op een init-container, Init:CrashLoopBackOff
	// een probleem.
	if rest, ok := strings.CutPrefix(s, "Init:"); ok {
		return !strings.Contains(rest, "/")
	}
	return true
}

func cmpOr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Workload is een Deployment, StatefulSet, DaemonSet, Job of CronJob, met
// de velden van elk.
type Workload struct {
	Kind     string `json:"kind"`
	Metadata Meta   `json:"metadata"`
	Spec     struct {
		Replicas    *int   `json:"replicas"`
		Completions *int   `json:"completions"`
		Schedule    string `json:"schedule"`
		Suspend     *bool  `json:"suspend"`
		Template    struct {
			Spec struct {
				Containers []Container `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
		JobTemplate struct {
			Spec struct {
				Template struct {
					Spec struct {
						Containers []Container `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"jobTemplate"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration     int64       `json:"observedGeneration"`
		Replicas               int         `json:"replicas"`
		ReadyReplicas          int         `json:"readyReplicas"`
		UpdatedReplicas        int         `json:"updatedReplicas"`
		AvailableReplicas      int         `json:"availableReplicas"`
		DesiredNumberScheduled int         `json:"desiredNumberScheduled"`
		NumberReady            int         `json:"numberReady"`
		UpdatedNumberScheduled int         `json:"updatedNumberScheduled"`
		Succeeded              int         `json:"succeeded"`
		Failed                 int         `json:"failed"`
		Active                 any         `json:"active"`
		LastScheduleTime       *time.Time  `json:"lastScheduleTime"`
		Conditions             []Condition `json:"conditions"`
	} `json:"status"`
}

// Desired is het gewenste aantal pods; bij een DaemonSet het aantal nodes
// waarop hij hoort, bij een Job het aantal voltooiingen.
func (w Workload) Desired() int {
	switch w.Kind {
	case "DaemonSet":
		return w.Status.DesiredNumberScheduled
	case "Job":
		if w.Spec.Completions != nil {
			return *w.Spec.Completions
		}
		return 1
	case "CronJob":
		return 0
	}
	if w.Spec.Replicas != nil {
		return *w.Spec.Replicas
	}
	return 1
}

// ReadyPods is het aantal klare pods, bij een Job het aantal geslaagde.
func (w Workload) ReadyPods() int {
	switch w.Kind {
	case "DaemonSet":
		return w.Status.NumberReady
	case "Job":
		return w.Status.Succeeded
	case "CronJob":
		return 0
	}
	return w.Status.ReadyReplicas
}

// UpToDate is het aantal pods met de nieuwste versie.
func (w Workload) UpToDate() int {
	if w.Kind == "DaemonSet" {
		return w.Status.UpdatedNumberScheduled
	}
	return w.Status.UpdatedReplicas
}

// Images zijn de images van de containers.
func (w Workload) Images() []string {
	cs := w.Spec.Template.Spec.Containers
	if w.Kind == "CronJob" {
		cs = w.Spec.JobTemplate.Spec.Template.Spec.Containers
	}
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Image)
	}
	return out
}

// Workload-statussen.
const (
	WorkloadReady     = "ready"
	WorkloadRolling   = "rolling"
	WorkloadDegraded  = "degraded"
	WorkloadStopped   = "stopped"
	WorkloadFailed    = "failed"
	WorkloadRunning   = "running"
	WorkloadSuspended = "suspended"
	WorkloadScheduled = "scheduled"
)

// State zegt hoe het ervoor staat, met een korte uitleg.
func (w Workload) State() (string, string) {
	switch w.Kind {
	case "Job":
		if c, ok := condition(w.Status.Conditions, "Failed"); ok && c.Status == "True" {
			return WorkloadFailed, cmpOr(c.Message, c.Reason, "de job is mislukt")
		}
		if c, ok := condition(w.Status.Conditions, "Complete"); ok && c.Status == "True" {
			return WorkloadReady, "voltooid"
		}
		return WorkloadRunning, "bezig"
	case "CronJob":
		if w.Spec.Suspend != nil && *w.Spec.Suspend {
			return WorkloadSuspended, "gepauzeerd"
		}
		return WorkloadScheduled, "volgens schema " + w.Spec.Schedule
	}
	desired, ready := w.Desired(), w.ReadyPods()
	switch {
	case w.Metadata.Generation > w.Status.ObservedGeneration:
		return WorkloadRolling, "de wijziging is nog niet opgepikt"
	case desired == 0:
		return WorkloadStopped, "geschaald naar 0"
	case w.UpToDate() < desired:
		return WorkloadRolling, fmt.Sprintf("%d van %d pods bijgewerkt", w.UpToDate(), desired)
	case ready < desired:
		return WorkloadDegraded, fmt.Sprintf("%d van %d pods klaar", ready, desired)
	}
	return WorkloadReady, fmt.Sprintf("%d van %d pods klaar", ready, desired)
}

// Healthy is true als een workload doet wat hij moet.
func (w Workload) Healthy() bool {
	st, _ := w.State()
	return st == WorkloadReady || st == WorkloadStopped || st == WorkloadRunning || st == WorkloadScheduled || st == WorkloadSuspended
}

type Event struct {
	Metadata       Meta       `json:"metadata"`
	Type           string     `json:"type"`
	Reason         string     `json:"reason"`
	Message        string     `json:"message"`
	Count          int        `json:"count"`
	LastTimestamp  *time.Time `json:"lastTimestamp"`
	EventTime      *time.Time `json:"eventTime"`
	InvolvedObject struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"involvedObject"`
}

// When is het laatste tijdstip van het event.
func (e Event) When() time.Time {
	switch {
	case e.LastTimestamp != nil:
		return *e.LastTimestamp
	case e.EventTime != nil:
		return *e.EventTime
	}
	return e.Metadata.CreationTimestamp
}

// MilliCPU leest een CPU-hoeveelheid zoals 2, 1500m of 250000000n.
func MilliCPU(q string) int64 {
	switch {
	case q == "":
		return 0
	case strings.HasSuffix(q, "m"):
		v, _ := strconv.ParseFloat(strings.TrimSuffix(q, "m"), 64)
		return int64(math.Round(v))
	case strings.HasSuffix(q, "u"):
		v, _ := strconv.ParseFloat(strings.TrimSuffix(q, "u"), 64)
		return int64(math.Round(v / 1e3))
	case strings.HasSuffix(q, "n"):
		v, _ := strconv.ParseFloat(strings.TrimSuffix(q, "n"), 64)
		return int64(math.Round(v / 1e6))
	}
	v, _ := strconv.ParseFloat(q, 64)
	return int64(math.Round(v * 1000))
}

// Bytes leest een hoeveelheid geheugen zoals 4015180Ki, 2Gi of 512M.
func Bytes(q string) int64 {
	for _, s := range []struct {
		suffix string
		mul    float64
	}{
		{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"Pi", 1 << 50},
		{"k", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}, {"P", 1e15}, {"m", 1e-3},
	} {
		if v, ok := strings.CutSuffix(q, s.suffix); ok {
			f, _ := strconv.ParseFloat(v, 64)
			return int64(math.Round(f * s.mul))
		}
	}
	f, _ := strconv.ParseFloat(q, 64)
	return int64(math.Round(f))
}

// SystemNamespaces zijn de namespaces van Kubernetes en k3s zelf; het
// dashboard verbergt ze standaard.
var SystemNamespaces = []string{"kube-system", "kube-public", "kube-node-lease"}

// IsSystem is true voor een systeemnamespace.
func IsSystem(ns string) bool { return slices.Contains(SystemNamespaces, ns) }
