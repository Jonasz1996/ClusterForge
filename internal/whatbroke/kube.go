package whatbroke

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// SourceKube is een waarschuwing uit Kubernetes.
const SourceKube = "kube"

// kubeSyncDelay is de interval van de sync van Kubernetes: zo lang na een
// uitrol ziet ClusterForge hem.
const kubeSyncDelay = 30 * time.Second

// kubeSymptoms zijn de waarschuwingen die op een app of node wijzen die
// misgaat; de rest is informatie.
var kubeSymptoms = map[string]bool{
	"BackOff": true, "Failed": true, "OOMKilled": true, "OOMKilling": true, "FailedScheduling": true, "Unhealthy": true,
	"Evicted": true, "FailedMount": true, "FailedAttachVolume": true, "NodeNotReady": true, "FailedCreatePodSandBox": true,
}

// FromKubeEvent maakt een regel van een waarschuwing uit Kubernetes.
func FromKubeEvent(r store.ListKubeEventsInScopeRow) Entry {
	cid := r.ClusterID
	class := events.ClassInfo
	if kubeSymptoms[r.Reason] {
		class = events.ClassSymptom
	}
	en := Entry{
		ID: "k" + strconv.FormatInt(r.ID, 10), Ts: r.Ts, From: r.Ts, To: r.Ts, Class: class, Source: SourceKube, Kind: "kube",
		Action: r.Reason, Summary: kubeSummary(r), NodeID: r.NodeID, Hostname: r.Hostname, ClusterID: &cid, Cluster: r.ClusterName,
		Subject: r.ObjectName, Detail: map[string]string{"namespace": r.Namespace, "object": r.ObjectKind + " " + r.ObjectName},
	}
	if r.AppName != "" {
		en.Detail["app"], en.Detail["app_kind"] = r.AppName, r.AppKind
		en.Link = kubeLink(r.ClusterID, r.AppKind, r.Namespace, r.AppName)
	} else {
		en.Link = kubeLink(r.ClusterID, r.ObjectKind, r.Namespace, r.ObjectName)
	}
	if r.NodeName != "" {
		en.Detail["node"] = r.NodeName
	}
	if r.Count > 1 {
		en.Detail["count"] = strconv.Itoa(int(r.Count))
	}
	return en
}

// kubeSummary is een waarschuwing in één regel, zoals 'BackOff bij pod
// web-7c9-c in demo: Back-off restarting failed container (12 keer)'. De
// melding zelf is van Kubernetes, en dus Engels.
func kubeSummary(r store.ListKubeEventsInScopeRow) string {
	what := strings.ToLower(r.ObjectKind) + " " + r.ObjectName
	if r.Namespace != "" {
		what += " in " + r.Namespace
	}
	s := r.Reason + " bij " + what
	if r.Message != "" {
		s += ": " + r.Message
	}
	if r.Count > 1 {
		s += fmt.Sprintf(" (%d keer)", r.Count)
	}
	return s
}

// kubeResources zijn de soorten met een eigen pagina in het dashboard.
var kubeResources = map[string][3]string{
	"Pod": {"", "v1", "pods"}, "Node": {"", "v1", "nodes"}, "Deployment": {"apps", "v1", "deployments"},
	"StatefulSet": {"apps", "v1", "statefulsets"}, "DaemonSet": {"apps", "v1", "daemonsets"},
	"ReplicaSet": {"apps", "v1", "replicasets"}, "Job": {"batch", "v1", "jobs"},
}

// kubeLink is de pagina van een object, of het tabblad Gebeurtenissen van
// het cluster voor een soort zonder eigen pagina.
func kubeLink(clusterID uuid.UUID, kind, namespace, name string) string {
	res, ok := kubeResources[kind]
	if !ok || name == "" {
		return "/kubernetes/cluster?id=" + clusterID.String() + "&tab=gebeurtenissen"
	}
	q := "id=" + clusterID.String()
	if res[0] != "" {
		q += "&g=" + res[0]
	}
	q += "&v=" + res[1] + "&r=" + res[2]
	if namespace != "" {
		q += "&ns=" + url.QueryEscape(namespace)
	}
	return "/kubernetes/object?" + q + "&name=" + url.QueryEscape(name)
}

// rolledOut legt een uitrol op het moment dat de eerste pod met de nieuwe
// image ontstond, als de sync dat kon zien; anders ligt hij in de 30 s voor
// de sync hem zag.
func rolledOut(en *Entry, at string) {
	if t, err := time.Parse(time.RFC3339, at); err == nil && !t.After(en.Ts) && en.Ts.Sub(t) <= 10*time.Minute {
		en.Ts, en.From, en.To = t, t, t
		return
	}
	en.From, en.Delay = en.Ts.Add(-kubeSyncDelay), "Kubernetes wordt elke 30 s gelezen"
}

// appTrouble is een symptoom van een app: een waarschuwing over zijn pods,
// of een status die verminderd of down werd.
type appTrouble struct {
	ts   time.Time
	verb string
	// status is true voor een overgang van de status, false voor een
	// waarschuwing.
	status bool
}

// appKey is een app in een cluster.
func appKey(cluster *uuid.UUID, namespace, name string) string {
	c := ""
	if cluster != nil {
		c = cluster.String()
	}
	return c + "/" + namespace + "/" + name
}

// troubleVerb zegt in een paar woorden wat een app heeft.
func troubleVerb(e Entry) string {
	if e.Source == SourceLog {
		if e.Detail["to"] == "down" {
			return "is down"
		}
		return "is verminderd"
	}
	msg := strings.ToLower(e.Summary)
	switch e.Action {
	case "BackOff":
		if strings.Contains(msg, "pulling image") {
			return "kan zijn image niet ophalen"
		}
		return "crasht"
	case "OOMKilled", "OOMKilling":
		return "crasht door gebrek aan geheugen"
	case "Failed":
		if strings.Contains(msg, "image") {
			return "kan zijn image niet ophalen"
		}
		return "start niet"
	case "FailedScheduling":
		return "krijgt geen plek op een node"
	case "Unhealthy":
		return "is niet gezond"
	}
	return "heeft problemen"
}

// kubeFacts zijn per app de symptomen, oudste eerst.
func kubeFacts(entries []Entry, from time.Time) map[string][]appTrouble {
	out := map[string][]appTrouble{}
	for _, e := range entries {
		if e.Class != events.ClassSymptom || e.Ts.Before(from) {
			continue
		}
		var name string
		switch {
		case e.Source == SourceKube:
			name = e.Detail["app"]
		case e.Source == SourceLog && e.Action == "kube.app_status_changed":
			name = e.Detail["name"]
		}
		if name == "" {
			continue
		}
		k := appKey(e.ClusterID, e.Detail["namespace"], name)
		out[k] = append(out[k], appTrouble{ts: e.Ts, verb: troubleVerb(e), status: e.Source == SourceLog})
	}
	return out
}

// rolloutLink is het verband tussen een uitrol en een app die erna
// misging: 'web in demo crasht sinds de uitrol van image nginx:1.29 om
// 14:02'. Een waarschuwing van Kubernetes zegt meer dan de status, dus de
// eerste waarschuwing gaat voor.
func rolloutLink(e Entry, f facts) (string, bool) {
	ns, name := e.Detail["namespace"], e.Detail["name"]
	var first *appTrouble
	for _, t := range f.apps[appKey(e.ClusterID, ns, name)] {
		if t.ts.Before(e.From) {
			continue
		}
		if first == nil || (first.status && !t.status) {
			first = &t
		}
		if !first.status {
			break
		}
	}
	if first == nil {
		return "", false
	}
	return fmt.Sprintf("%s in %s %s sinds de uitrol van image %s om %s.", name, ns, first.verb, e.Detail["to"],
		e.Ts.Local().Format("15:04")), true
}
