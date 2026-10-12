package kubelessons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/kube"
)

// run is één controle van een les: het cluster, en de data die de
// opdrachten van de les vastlegden.
type run struct {
	ctx  context.Context
	kube Reader
	env  Env
	data map[string]Data
	vars map[string]string
	// started: deze controle begon een opdracht.
	started bool
}

// fillVars zet de plaatshouders: het VIP, het webadres van de app, en de
// tekstwaarden uit de data, zoals de node die op cordon ging.
func (x *run) fillVars() {
	x.vars = map[string]string{"vip": x.env.VIP, "host": webHost(x.env.VIP)}
	for _, d := range x.data {
		for k, v := range d {
			if s, ok := v.(string); ok && k != detailKey && s != "" {
				x.vars[k] = s
			}
		}
	}
}

// webHost is het webadres van web: onder nip.io wijst het zonder eigen DNS
// naar het VIP.
func webHost(vip string) string {
	if vip == "" {
		return "web.example.com"
	}
	return "web." + vip + ".nip.io"
}

// deployment leest een Deployment in leren, met de ruwe JSON; nil als hij
// er niet is.
func (x *run) deployment(name string) (*kube.Workload, []byte, error) {
	body, err := x.kube.Get(x.ctx, x.env.ClusterID, "/apis/apps/v1/namespaces/"+Namespace+"/deployments/"+name, nil)
	if errors.Is(err, kube.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var w kube.Workload
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, nil, err
	}
	w.Kind = "Deployment"
	if w.Metadata.DeletionTimestamp != nil {
		return nil, nil, nil
	}
	return &w, body, nil
}

// pods zijn de pods in leren met het label app, zonder die al weggaan.
func (x *run) pods(app string) ([]kube.Pod, error) {
	all, err := x.kube.Pods(x.ctx, x.env.ClusterID, Namespace, "")
	if err != nil {
		return nil, err
	}
	var out []kube.Pod
	for _, p := range all {
		if p.Metadata.Labels["app"] == app && p.Metadata.DeletionTimestamp == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// systemPods zijn de pods in kube-system waarvan de naam zo begint.
func (x *run) systemPods(prefix string) ([]kube.Pod, error) {
	all, err := x.kube.Pods(x.ctx, x.env.ClusterID, "kube-system", "")
	if err != nil {
		return nil, err
	}
	var out []kube.Pod
	for _, p := range all {
		if strings.HasPrefix(p.Metadata.Name, prefix) {
			out = append(out, p)
		}
	}
	return out, nil
}

// namespaceExists is true als leren er is en niet weggaat.
func (x *run) namespaceExists() (bool, string, error) {
	body, err := x.kube.Get(x.ctx, x.env.ClusterID, "/api/v1/namespaces/"+Namespace, nil)
	if errors.Is(err, kube.ErrNotFound) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	var ns struct {
		Metadata kube.Meta `json:"metadata"`
	}
	if err := json.Unmarshal(body, &ns); err != nil {
		return false, "", err
	}
	if ns.Metadata.DeletionTimestamp != nil {
		return false, "De namespace leren wordt nog weggehaald; wacht tot hij weg is en maak hem dan opnieuw.", nil
	}
	return true, "", nil
}

// noWeb is de uitleg als web er niet is.
const noWeb = "Er is geen Deployment web in leren. Maak hem eerst, in les 2."

// image is het image van de eerste container.
func image(w *kube.Workload) string {
	if imgs := w.Images(); len(imgs) > 0 {
		return imgs[0]
	}
	return ""
}

// shortImage laat docker.io/library/ weg, zoals je het zelf schrijft.
func shortImage(img string) string {
	return strings.TrimPrefix(strings.TrimPrefix(img, "docker.io/"), "library/")
}

// rolledOut is true als een uitrol klaar is: alle pods zijn nieuw en klaar,
// en de oude zijn weg.
func rolledOut(w *kube.Workload) (bool, string) {
	st, why := w.State()
	switch {
	case st != kube.WorkloadReady:
		return false, why
	case w.Status.Replicas > w.Desired():
		return false, fmt.Sprintf("de oude pods stoppen nog: %d pods voor %d", w.Status.Replicas, w.Desired())
	}
	return true, why
}

// podStates vat de pods samen, zoals 'web-5d9c-x2q: ImagePullBackOff'.
func podStates(pods []kube.Pod) string {
	parts := make([]string, 0, len(pods))
	for i, p := range pods {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("en nog %d", len(pods)-3))
			break
		}
		parts = append(parts, p.Metadata.Name+": "+p.StatusText())
	}
	return strings.Join(parts, ", ")
}

// waitHint zegt wat een wachtende pod tegenhoudt.
func waitHint(pods []kube.Pod) string {
	for _, p := range pods {
		switch p.StatusText() {
		case "ErrImagePull", "ImagePullBackOff":
			return " Het image komt niet binnen: klopt de naam, en kan de node bij internet?"
		case "CrashLoopBackOff":
			return " De container stopt steeds: kijk in de log van de pod."
		case "Pending":
			return " De pod wacht op een plek: kijk bij de gebeurtenissen van de pod waarom."
		}
	}
	return ""
}

// ready is true als alle containers van een draaiende pod klaar zijn.
func ready(p kube.Pod) bool {
	r, t := p.ReadyCount()
	return p.Status.Phase == "Running" && t > 0 && r == t
}

// names geeft de namen van pods.
func names(pods []kube.Pod) []string {
	out := make([]string, 0, len(pods))
	for _, p := range pods {
		out = append(out, p.Metadata.Name)
	}
	return out
}

// nodesOf zijn de nodes waarop pods draaien, op naam.
func nodesOf(pods []kube.Pod) []string {
	var out []string
	for _, p := range pods {
		if p.Spec.NodeName != "" && !slices.Contains(out, p.Spec.NodeName) {
			out = append(out, p.Spec.NodeName)
		}
	}
	slices.Sort(out)
	return out
}

// and schrijft een opsomming: 'a, b en c'.
func and(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " en " + items[len(items)-1]
}

// plural kiest enkelvoud of meervoud: plural(2, "node", "nodes").
func plural(n int, one, more string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, more)
}

// podSpec is het deel van een Deployment dat zegt waar de configuratie
// vandaan komt.
type podSpec struct {
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					EnvFrom []struct {
						ConfigMapRef *struct {
							Name string `json:"name"`
						} `json:"configMapRef"`
						SecretRef *struct {
							Name string `json:"name"`
						} `json:"secretRef"`
					} `json:"envFrom"`
					Env []struct {
						ValueFrom *struct {
							ConfigMapKeyRef *struct {
								Name string `json:"name"`
							} `json:"configMapKeyRef"`
							SecretKeyRef *struct {
								Name string `json:"name"`
							} `json:"secretKeyRef"`
						} `json:"valueFrom"`
					} `json:"env"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// configRefs geeft de ConfigMaps en Secrets die een Deployment als
// omgevingsvariabelen leest, met envFrom of per sleutel.
func configRefs(body []byte) (maps, secrets []string) {
	var d podSpec
	_ = json.Unmarshal(body, &d)
	add := func(list *[]string, name string) {
		if !slices.Contains(*list, name) {
			*list = append(*list, name)
		}
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		for _, e := range c.EnvFrom {
			if e.ConfigMapRef != nil {
				add(&maps, e.ConfigMapRef.Name)
			}
			if e.SecretRef != nil {
				add(&secrets, e.SecretRef.Name)
			}
		}
		for _, e := range c.Env {
			if e.ValueFrom == nil {
				continue
			}
			if e.ValueFrom.ConfigMapKeyRef != nil {
				add(&maps, e.ValueFrom.ConfigMapKeyRef.Name)
			}
			if e.ValueFrom.SecretKeyRef != nil {
				add(&secrets, e.ValueFrom.SecretKeyRef.Name)
			}
		}
	}
	return maps, secrets
}

// usesClaim is true als een pod de claim als volume heeft.
func usesClaim(p kube.Pod, claim string) bool {
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == claim {
			return true
		}
	}
	return false
}

// logLines zijn de regels van een log die niet leeg zijn.
func logLines(text string) []string {
	var out []string
	for l := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

// clip kort een tekst in tot n tekens.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
