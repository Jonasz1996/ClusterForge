package rollout

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// KubeServerRole is de rol van de servers in de template kubernetes.
const KubeServerRole = "server"

// k3sUnits zijn de services van k3s: k3s op een server, k3s-agent op een
// worker.
var k3sUnits = []string{"k3s", "k3s-agent"}

// restartsK3s zegt of het toepassen van deze stappen k3s kan herstarten.
func restartsK3s(steps []templates.Step) bool {
	for _, ts := range steps {
		if slices.ContainsFunc(touched(ts), func(svc string) bool { return slices.Contains(k3sUnits, svc) }) {
			return true
		}
	}
	return false
}

// kubeNotes zeggen bij een Kubernetes-cluster wat er met de nodes in
// Kubernetes gebeurt. servers zijn de servers van het cluster.
func kubeNotes(p Plan, servers []string) []string {
	if !p.Kubernetes || !slices.ContainsFunc(p.Nodes, func(n PlanNode) bool {
		return slices.ContainsFunc(n.Services, func(svc string) bool { return slices.Contains(k3sUnits, svc) })
	}) {
		return nil
	}
	out := []string{"Dit is een Kubernetes-cluster: eerst de servers, dan de workers, want de kubelet op een node mag niet nieuwer zijn dan de API-server. " +
		"Krijgt een node een nieuwe k3s, dan maakt de taak hem eerst leeg: hij gaat op cordon en zijn pods gaan naar de andere nodes. " +
		"Pas als hij Ready is met de nieuwe versie, krijgt hij weer pods. Komt een node niet terug, dan stopt de taak en blijft die node op cordon."}
	if len(servers) == 1 {
		out = append(out, fmt.Sprintf("Er is maar één server, %s: terwijl k3s daar herstart, antwoordt de API van het cluster even niet en is het VIP even weg. "+
			"Apps die al draaien, blijven draaien. Met drie servers merk je dit niet.", servers[0]))
	}
	return out
}

// kubeNode is een node van een Kubernetes-cluster waarop het toepassen k3s
// kan herstarten.
type kubeNode struct {
	// name is de naam in Kubernetes, want de k3s-versie van de template.
	name, want string
	// drained: de taak zette hem op cordon voor een nieuwe k3s.
	drained bool
}

// stays zegt bij een fout na het leegmaken dat de node op cordon blijft.
func (kn *kubeNode) stays(err error) error {
	if kn == nil || !kn.drained || err == nil {
		return err
	}
	return fmt.Errorf("%w. %s blijft op cordon; los het op en kies Opnieuw toepassen, of haal de cordon weg met Uncordon op het tabblad Nodes", err, kn.name)
}

// kubePrepare zoekt de node in Kubernetes en maakt hem leeg als hij een
// andere k3s krijgt. nil betekent dat er in Kubernetes niets te doen is.
// Een fout bij het leegmaken geeft de node weer pods; er is dan niets
// toegepast.
func (s *Service) kubePrepare(ctx context.Context, st *jobs.Step, p Plan, n *PlanNode, d *deploy.Desired, steps []templates.Step) (*kubeNode, error) {
	if !p.Kubernetes || s.Kube == nil || !restartsK3s(steps) {
		return nil, nil
	}
	flush := func() { _ = st.Flush(ctx) }
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	name, err := s.Kube.NodeNameOf(rctx, p.ClusterID, n.NodeID)
	var node kube.Node
	var nodes []kube.Node
	if err == nil && name != "" {
		node, err = s.Kube.ReadNode(rctx, p.ClusterID, name)
	}
	if err == nil && name != "" {
		nodes, err = s.Kube.Nodes(rctx, p.ClusterID)
	}
	switch {
	case err != nil:
		st.Logf("Kubernetes antwoordt niet (%v); %s wordt niet eerst leeggemaakt", err, n.Hostname)
		return nil, nil
	case name == "":
		st.Logf("Kubernetes kent %s niet; er is niets leeg te maken", n.Hostname)
		return nil, nil
	}
	kn := &kubeNode{name: name, want: d.Template.KubeVersion()}
	have := node.Status.NodeInfo.KubeletVersion
	if kn.want == "" || have == kn.want {
		return kn, nil
	}
	st.Logf("%s draait k3s %s en krijgt %s", name, have, kn.want)
	others := slices.ContainsFunc(nodes, func(o kube.Node) bool { return o.Metadata.Name != name && o.Ready() && !o.Spec.Unschedulable })
	if !others && !node.Spec.Unschedulable {
		st.Logf("%s is de enige node die pods kan krijgen; leegmaken zou elke app stoppen. k3s herstart zonder de pods te stoppen, dus ze blijven staan", name)
		return kn, nil
	}
	kn.drained = true
	_, err = s.Kube.Drain(ctx, p.ClusterID, name, kube.DrainOptions{Maintenance: true}, func(line string) {
		st.Logf("%s", line)
		flush()
	})
	if err == nil {
		return kn, nil
	}
	if !jobs.Interrupted(ctx) {
		s.kubeRollback(ctx, st, p, name)
	}
	var blocked *kube.BlockedError
	if errors.As(err, &blocked) {
		pods := make([]string, len(blocked.Pods))
		for i, bp := range blocked.Pods {
			pods[i] = bp.String() + " " + bp.Reason
		}
		err = errors.New(strings.Join(pods, "; "))
	}
	return nil, fmt.Errorf("%s leegmaken lukt niet: %w; er is niets toegepast", n.Hostname, err)
}

// kubeRollback geeft een node weer pods als het leegmaken mislukte.
func (s *Service) kubeRollback(ctx context.Context, st *jobs.Step, p Plan, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	switch released, err := s.Kube.Release(ctx, p.ClusterID, name); {
	case err != nil:
		st.Logf("terugdraaien mislukt: %v; haal de cordon weg met Uncordon op het tabblad Nodes", err)
	case released:
		st.Logf("terugdraaien: %s krijgt weer pods", name)
	}
	_ = st.Flush(ctx)
}

// kubeFinish wacht na het toepassen tot de node Ready is met de k3s van de
// template, en haalt dan de cordon van ClusterForge weg. Een cordon die
// iemand zelf zette, blijft.
func (s *Service) kubeFinish(ctx context.Context, st *jobs.Step, p Plan, kn *kubeNode) error {
	if kn == nil {
		return nil
	}
	flush := func() { _ = st.Flush(ctx) }
	want := kn.want
	if want == "" {
		want = "de versie die hij had"
	}
	st.Logf("wachten tot %s Ready is in Kubernetes met k3s %s", kn.name, want)
	flush()
	wctx, cancel := context.WithTimeoutCause(ctx, s.KubeReadyTimeout, fmt.Errorf(
		"%s is na %s niet Ready met k3s %s; kijk op de node met journalctl -u k3s of journalctl -u k3s-agent", kn.name, s.KubeReadyTimeout, want))
	defer cancel()
	said := ""
	var node kube.Node
	for {
		var err error
		node, err = s.Kube.ReadNode(wctx, p.ClusterID, kn.name)
		var msg string
		switch {
		case err != nil:
			// Na een herstart van de enige server is de API er even niet.
			msg = "wachten op Kubernetes: " + err.Error()
		case !node.Ready():
			msg = kn.name + " is nog niet Ready"
		case kn.want != "" && node.Status.NodeInfo.KubeletVersion != kn.want:
			msg = kn.name + " meldt nog k3s " + node.Status.NodeInfo.KubeletVersion
		}
		if msg == "" {
			break
		}
		if msg != said {
			said = msg
			st.Logf("%s", msg)
			flush()
		}
		select {
		case <-wctx.Done():
			return kn.stays(fmt.Errorf("%w (laatst: %s)", context.Cause(wctx), said))
		case <-time.After(s.Retry):
		}
	}
	st.Logf("%s is Ready met k3s %s", kn.name, node.Status.NodeInfo.KubeletVersion)
	if !node.Spec.Unschedulable {
		return nil
	}
	if _, ours := node.Metadata.Annotations[kube.MarkAnnotation]; !ours {
		st.Logf("%s blijft op cordon: die zette iemand zelf. Haal hem weg met Uncordon op het tabblad Nodes", kn.name)
		return nil
	}
	if _, err := s.Kube.Release(ctx, p.ClusterID, kn.name); err != nil {
		return kn.stays(fmt.Errorf("de cordon van %s weghalen: %w", kn.name, err))
	}
	st.Logf("%s krijgt weer pods", kn.name)
	return nil
}

// servers geeft de hostnames met de rol server.
func servers[T any](nodes []T, of func(T) (hostname, role string)) []string {
	var out []string
	for _, n := range nodes {
		if h, r := of(n); r == KubeServerRole {
			out = append(out, h)
		}
	}
	return out
}
