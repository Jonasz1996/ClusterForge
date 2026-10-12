package status

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	// KubeAPIDown is hoe lang de API van een Kubernetes-cluster niet mag
	// antwoorden voor het cluster down is: drie rondes van de sync.
	KubeAPIDown = 90 * time.Second
	// AppGrace is hoe lang een app niet mag doen wat hij moet voor hij
	// verminderd of down is. Een gewone uitrol of een pod die herstart, is
	// binnen die tijd klaar.
	AppGrace = 2 * time.Minute
	// maxKubeProblems is hoeveel problemen de reden van een cluster noemt.
	maxKubeProblems = 3
)

// KubeInput is wat de sync laatst zag van een Kubernetes-cluster.
type KubeInput struct {
	// Synced is false zolang de sync het cluster nog nooit las.
	Synced bool
	APIOk  bool
	// ErrorKind is api als een agent op een server de API vroeg en geen
	// antwoord kreeg; old_agent en no_agent zeggen niets over de API.
	ErrorKind string
	Error     string
	// OKAt is wanneer de API het laatst antwoordde.
	OKAt  *time.Time
	Nodes []KubeNode
	// Apps zijn de apps buiten de systeemnamespaces, met hun status van
	// deze ronde.
	Apps []AppResult
}

// KubeNode is een node zoals Kubernetes hem ziet.
type KubeNode struct {
	Name  string
	Roles []string
	Ready bool
}

// App is een Deployment, StatefulSet of DaemonSet zoals de sync hem zag.
type App struct {
	Namespace, Kind, Name string
	Desired, Ready        int
	// Trouble is wat de pods hebben, zoals ImagePullBackOff.
	Trouble string
	// ShortSince is sinds wanneer de app niet doet wat hij moet; nil als
	// hij in orde is.
	ShortSince *time.Time
}

// AppResult is een app met zijn status.
type AppResult struct {
	App
	Result
}

// AppStatus geeft de status van een app. Doet hij al AppGrace niet wat hij
// moet, dan is hij down zonder één klare pod, en anders verminderd.
func AppStatus(a App, now time.Time) Result {
	if a.ShortSince == nil || now.Sub(*a.ShortSince) < AppGrace {
		return Result{Healthy, ""}
	}
	st := Degraded
	if a.Ready == 0 {
		st = Down
	}
	return Result{st, AppReason(a.Desired, a.Ready, a.Trouble)}
}

// AppReason is de reden bij de status van een app, zoals '0 van 2 pods
// klaar (ImagePullBackOff)' of 'de uitrol hangt (CrashLoopBackOff)': de oude
// pods draaien nog, maar de nieuwe komen er niet.
func AppReason(desired, ready int, trouble string) string {
	s := "de uitrol hangt"
	if ready < desired {
		s = fmt.Sprintf("%d van %d pods klaar", ready, desired)
	}
	if trouble != "" {
		s += " (" + trouble + ")"
	}
	return s
}

// Kube past de regels voor Kubernetes toe op de uitkomst van de nodes en
// VIP's. Down als de API via geen enkele server antwoordt, en dat al
// KubeAPIDown. Verminderd als een node NotReady is (bij een server mist
// etcd dan een lid) of als een app verminderd of down is. Is het cluster
// door zijn nodes al down, in split-brain of onbekend, dan blijft dat zo.
func Kube(base Result, in KubeInput, now time.Time) Result {
	if base.Status == Down || base.Status == SplitBrain || base.Status == Unknown || !in.Synced {
		return base
	}
	if !in.APIOk {
		if in.ErrorKind == "api" && (in.OKAt == nil || now.Sub(*in.OKAt) >= KubeAPIDown) {
			return Result{Down, "de Kubernetes-API antwoordt via geen enkele server: " + in.Error}
		}
		// Wat de sync laatst zag, is niet meer actueel.
		return base
	}
	var problems []string
	for _, n := range in.Nodes {
		switch {
		case n.Ready:
		case slices.Contains(n.Roles, "etcd"):
			problems = append(problems, "server "+n.Name+" is NotReady, etcd mist een lid")
		default:
			problems = append(problems, "node "+n.Name+" is NotReady")
		}
	}
	for _, a := range in.Apps {
		if a.Status == Degraded || a.Status == Down {
			problems = append(problems, "app "+a.Name+" in "+a.Namespace+": "+a.Reason)
		}
	}
	if len(problems) > maxKubeProblems {
		problems = append(problems[:maxKubeProblems], fmt.Sprintf("en nog %d", len(problems)-maxKubeProblems))
	}
	if base.Status == Degraded {
		problems = append([]string{base.Reason}, problems...)
	}
	if len(problems) == 0 {
		return base
	}
	return Result{Degraded, strings.Join(problems, "; ")}
}
