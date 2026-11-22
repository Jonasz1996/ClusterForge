package kube

import (
	"slices"
	"strings"
	"time"
)

// IsApp is true voor de soorten die een status krijgen: een app draait
// altijd, een Job of CronJob is na een tijd klaar.
func IsApp(kind string) bool {
	return kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet"
}

// WorkloadOf geeft de app van een pod: de eigenaar, en bij een ReplicaSet de
// Deployment erboven. De ReplicaSet van een Deployment heet zoals de
// Deployment met de pod-template-hash erachter. Leeg als de pod geen app
// heeft.
func WorkloadOf(m Meta) (kind, name string) {
	o := m.Owner()
	if o == nil {
		return "", ""
	}
	switch o.Kind {
	case "StatefulSet", "DaemonSet", "Job":
		return o.Kind, o.Name
	case "ReplicaSet":
		if h := m.Labels["pod-template-hash"]; h != "" {
			if d, ok := strings.CutSuffix(o.Name, "-"+h); ok && d != "" {
				return "Deployment", d
			}
		}
		// Zonder label: de laatste streep scheidt de hash af.
		if i := strings.LastIndexByte(o.Name, '-'); i > 0 {
			return "Deployment", o.Name[:i]
		}
	}
	return "", ""
}

// rollingOver is true als een Deployment nog oude pods heeft naast de
// nieuwe: de uitrol is pas klaar als die weg zijn.
func (w Workload) rollingOver() bool {
	return w.Kind == "Deployment" && w.Status.Replicas > w.Status.UpdatedReplicas
}

// Health zegt of een app niet doet wat hij moet, en wat zijn pods hebben.
// Een app doet niet wat hij moet als hij minder pods klaar heeft dan
// gewenst, of als een uitrol niet opschiet omdat de nieuwe pods een
// probleem hebben, zoals een image die niet bestaat: de oude pods draaien
// dan nog, maar de nieuwe versie komt er nooit.
func (w Workload) Health(pods []Pod) (short bool, trouble string) {
	var problems []string
	for _, p := range pods {
		if p.Metadata.Namespace != w.Metadata.Namespace || !p.Problem() {
			continue
		}
		if k, n := WorkloadOf(p.Metadata); k == w.Kind && n == w.Metadata.Name {
			if s := p.StatusText(); !slices.Contains(problems, s) {
				problems = append(problems, s)
			}
		}
	}
	slices.Sort(problems)
	trouble = strings.Join(problems, ", ")
	desired := w.Desired()
	if desired == 0 {
		return false, trouble
	}
	st, _ := w.State()
	return w.ReadyPods() < desired || (st == WorkloadRolling && len(problems) > 0), trouble
}

// pods zijn de pods van de app.
func (w Workload) pods(pods []Pod) []Pod {
	var out []Pod
	for _, p := range pods {
		if p.Metadata.Namespace != w.Metadata.Namespace {
			continue
		}
		if k, n := WorkloadOf(p.Metadata); k == w.Kind && n == w.Metadata.Name {
			out = append(out, p)
		}
	}
	return out
}

// Restarts telt hoe vaak de containers van de pods van de app herstartten,
// met wanneer een container het laatst stopte. Een nieuwe pod begint weer
// bij nul, dus dit zegt iets over de pods die er nu zijn.
func (w Workload) Restarts(pods []Pod) (n int, last *time.Time) {
	for _, p := range w.pods(pods) {
		for _, c := range p.Status.ContainerStatuses {
			n += c.RestartCount
			if t := c.LastState.Terminated; c.RestartCount > 0 && t != nil && !t.FinishedAt.IsZero() && (last == nil || t.FinishedAt.After(*last)) {
				at := t.FinishedAt
				last = &at
			}
		}
	}
	return n, last
}

// NoMemoryLimit zijn de containers zonder geheugenlimiet. Zijn er pods, dan
// tellen die, want een LimitRange in de namespace kan er een invullen;
// anders het sjabloon van de app.
func (w Workload) NoMemoryLimit(pods []Pod) []string {
	containers := w.Spec.Template.Spec.Containers
	if ps := w.pods(pods); len(ps) > 0 {
		containers = ps[0].Spec.Containers
	}
	out := []string{}
	for _, c := range containers {
		if !c.MemoryLimit() && !slices.Contains(out, c.Name) {
			out = append(out, c.Name)
		}
	}
	return out
}

// imagesChanged geeft de images die anders zijn dan voorheen, als 'oud' en
// 'nieuw'; ok is false als er niets veranderde.
func imagesChanged(before, after []string) (from, to []string, ok bool) {
	for i := range max(len(before), len(after)) {
		var b, a string
		if i < len(before) {
			b = before[i]
		}
		if i < len(after) {
			a = after[i]
		}
		if a != b {
			from, to = append(from, b), append(to, a)
		}
	}
	return from, to, len(to) > 0
}

// rolloutStart is wanneer de eerste pod van een app met images ontstond:
// het begin van een uitrol naar die images.
func rolloutStart(w Workload, pods []Pod, images []string) (time.Time, bool) {
	var first time.Time
	for _, p := range pods {
		if p.Metadata.Namespace != w.Metadata.Namespace || p.Metadata.CreationTimestamp.IsZero() {
			continue
		}
		if k, n := WorkloadOf(p.Metadata); k != w.Kind || n != w.Metadata.Name {
			continue
		}
		have := make([]string, len(p.Spec.Containers))
		for i, c := range p.Spec.Containers {
			have[i] = c.Image
		}
		if slices.Equal(have, images) && (first.IsZero() || p.Metadata.CreationTimestamp.Before(first)) {
			first = p.Metadata.CreationTimestamp
		}
	}
	return first, !first.IsZero()
}

// warningStart is wanneer een event voor het eerst gebeurde.
func (e Event) warningStart() time.Time {
	switch {
	case e.FirstTimestamp != nil:
		return *e.FirstTimestamp
	case e.EventTime != nil:
		return *e.EventTime
	}
	return e.Metadata.CreationTimestamp
}
