package kube

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ObjectDetail is één object met alles wat het dashboard erbij toont.
type ObjectDetail struct {
	Ref        Ref
	APIVersion string
	Kind       string
	Meta       Meta
	// Summary is de toestand in gewone woorden; Problem zegt of hij
	// aandacht vraagt.
	Summary    string
	Problem    bool
	Conditions []Condition
	Events     []Event
	YAML       string
	Chain      []ChainLink
	// Pod is het object als pod, als het een pod is.
	Pod *Pod
	// Actions zijn de acties die bij het object kunnen, en Replicas hoeveel
	// pods het vraagt als je het kunt schalen.
	Actions  []string
	Replicas *int
}

// ChainLink is een schakel in de keten van een object: zijn eigenaar, de
// node waarop hij draait met de VM en de Proxmox-host, of de Service en
// Ingress waarlangs hij bereikbaar is.
type ChainLink struct {
	// Role is owner, node, clusterforge, vm, service, ingress, pods,
	// volume, claim of backend.
	Role string
	Kind string
	Name string
	// Ref is het object in Kubernetes, als het er een is.
	Ref *Ref
	// NodeID is de ClusterForge-node bij Role clusterforge en vm.
	NodeID *uuid.UUID
	Text   string
	// URL is een webadres, bij een Ingress of een LoadBalancer.
	URL string
}

// maxOwners begrenst de keten naar boven: pod, ReplicaSet, Deployment.
const maxOwners = 4

// Object haalt één object met zijn events, YAML en keten.
func (s *Service) Object(ctx context.Context, clusterID uuid.UUID, ref Ref) (ObjectDetail, error) {
	if ref.Name == "" {
		return ObjectDetail{}, fmt.Errorf("%w: geen naam", ErrInvalid)
	}
	p, err := ref.Path()
	if err != nil {
		return ObjectDetail{}, err
	}
	body, err := s.Get(ctx, clusterID, p, nil)
	if err != nil {
		return ObjectDetail{}, err
	}
	var o genericObject
	if err := json.Unmarshal(body, &o); err != nil {
		return ObjectDetail{}, fmt.Errorf("%s: %w", p, err)
	}
	d := ObjectDetail{Ref: ref, APIVersion: o.APIVersion, Kind: o.Kind, Meta: o.Metadata, Conditions: o.conditions()}
	if d.YAML, err = ToYAML(body); err != nil {
		return d, err
	}
	d.Summary, d.Problem = summary(o, body)
	d.Actions, d.Replicas = ActionsFor(o.Kind, ref, body)

	var wg sync.WaitGroup
	var events []Event
	var evErr error
	wg.Go(func() {
		if d.Meta.UID != "" {
			events, evErr = List[Event](ctx, s, clusterID, "/api/v1/events", url.Values{"fieldSelector": {"involvedObject.uid=" + d.Meta.UID}})
		}
	})
	chain, chainErr := s.chain(ctx, clusterID, d.Meta, o, body)
	wg.Wait()
	if err := errors.Join(evErr, chainErr); err != nil {
		return d, err
	}
	slices.SortFunc(events, func(a, b Event) int { return b.When().Compare(a.When()) })
	if len(events) > 50 {
		events = events[:50]
	}
	d.Events, d.Chain = events, chain
	if o.Kind == "Pod" {
		var pod Pod
		if err := json.Unmarshal(body, &pod); err == nil {
			d.Pod = &pod
		}
	}
	return d, nil
}

// summary zegt in gewone woorden hoe het met een object gaat.
func summary(o genericObject, body []byte) (string, bool) {
	switch o.Kind {
	case "Pod":
		var p Pod
		if json.Unmarshal(body, &p) != nil {
			break
		}
		ready, total := p.ReadyCount()
		st := p.StatusText()
		where := ""
		if p.Spec.NodeName != "" {
			where = " op " + p.Spec.NodeName
		}
		switch {
		case p.Problem():
			txt := fmt.Sprintf("%s%s: %d van %d containers klaar", st, where, ready, total)
			if n := p.Restarts(); n > 0 {
				txt += fmt.Sprintf(", %d herstarts", n)
			}
			return txt, true
		case st == "Running":
			return fmt.Sprintf("Draait%s, %d van %d containers klaar", where, ready, total), false
		case st == "Completed":
			return "Klaar: de containers stopten zonder fout", false
		case st == "Pending":
			return "Wacht op een node of op het downloaden van het image", false
		}
		return st + where, false
	case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
		var w Workload
		if json.Unmarshal(body, &w) != nil {
			break
		}
		w.Kind = o.Kind
		state, text := w.State()
		label := map[string]string{
			WorkloadReady: "Klaar", WorkloadRolling: "Wordt bijgewerkt", WorkloadDegraded: "Niet alles draait", WorkloadStopped: "Gestopt",
			WorkloadFailed: "Mislukt", WorkloadRunning: "Bezig", WorkloadSuspended: "Gepauzeerd", WorkloadScheduled: "Ingepland",
		}[state]
		if text != "" {
			label += ": " + text
		}
		return label, !w.Healthy()
	case "Node":
		var n Node
		if json.Unmarshal(body, &n) != nil {
			break
		}
		txt := "Ready"
		if !n.Ready() {
			c, _ := condition(n.Status.Conditions, "Ready")
			txt = "NotReady: " + cmpOr(c.Message, c.Reason, "de kubelet meldt zich niet")
		}
		if n.Spec.Unschedulable {
			txt += "; cordon, er komen geen nieuwe pods op"
		}
		return txt, !n.Ready()
	case "PersistentVolumeClaim", "PersistentVolume":
		switch o.Status.Phase {
		case "Bound":
			return "Gebonden aan een volume", false
		case "Pending":
			return "Wacht op een volume; local-path maakt het pas als een pod de claim gebruikt", false
		case "Lost", "Failed":
			return o.Status.Phase, true
		}
	case "Namespace":
		return o.Status.Phase, o.Status.Phase == "Terminating"
	}
	st := o.statusText()
	return st, strings.HasPrefix(st, "Niet ") || st == "Failed"
}

// chain bouwt de keten van een object.
func (s *Service) chain(ctx context.Context, clusterID uuid.UUID, md Meta, o genericObject, body []byte) ([]ChainLink, error) {
	var out []ChainLink
	// Naar boven: de eigenaar van de eigenaar, tot de Deployment.
	cur := md
	for range maxOwners {
		own := cur.Owner()
		if own == nil {
			break
		}
		rt, ok := s.ResourceFor(ctx, clusterID, own.APIVersion, own.Kind)
		if !ok {
			out = append(out, ChainLink{Role: "owner", Kind: own.Kind, Name: own.Name})
			break
		}
		ref := Ref{Group: rt.Group, Version: rt.Version, Resource: rt.Resource, Name: own.Name}
		if rt.Namespaced {
			ref.Namespace = cur.Namespace
		}
		out = append(out, ChainLink{Role: "owner", Kind: own.Kind, Name: own.Name, Ref: &ref})
		p, err := ref.Path()
		if err != nil {
			break
		}
		next, err := GetInto[genericObject](ctx, s, clusterID, p)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		cur = next.Metadata
	}

	var labels map[string]string
	switch o.Kind {
	case "Pod":
		var p Pod
		_ = json.Unmarshal(body, &p)
		labels = md.Labels
		if p.Spec.NodeName != "" {
			links, err := s.nodeChain(ctx, clusterID, p.Spec.NodeName)
			if err != nil {
				return nil, err
			}
			out = append(out, links...)
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && objectName.MatchString(v.PersistentVolumeClaim.ClaimName) {
				ref := Ref{Version: "v1", Resource: "persistentvolumeclaims", Namespace: md.Namespace, Name: v.PersistentVolumeClaim.ClaimName}
				out = append(out, ChainLink{Role: "volume", Kind: "PersistentVolumeClaim", Name: ref.Name, Ref: &ref})
			}
		}
	case "Node":
		links, err := s.nodeChain(ctx, clusterID, md.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, links[1:]...)
	case "Deployment", "StatefulSet", "DaemonSet":
		var w struct {
			Spec struct {
				Template struct {
					Metadata Meta `json:"metadata"`
				} `json:"template"`
			} `json:"spec"`
		}
		_ = json.Unmarshal(body, &w)
		labels = w.Spec.Template.Metadata.Labels
	case "Service":
		links, err := s.serviceChain(ctx, clusterID, md, body)
		if err != nil {
			return nil, err
		}
		out = append(out, links...)
	case "Ingress":
		var in Ingress
		_ = json.Unmarshal(body, &in)
		seen := map[string]bool{}
		for _, r := range in.Rules() {
			if r.Service != "" && !seen[r.Service] && objectName.MatchString(r.Service) {
				seen[r.Service] = true
				ref := Ref{Version: "v1", Resource: "services", Namespace: md.Namespace, Name: r.Service}
				out = append(out, ChainLink{Role: "backend", Kind: "Service", Name: r.Service, Ref: &ref})
			}
		}
	case "PersistentVolumeClaim":
		var c Claim
		_ = json.Unmarshal(body, &c)
		if c.Spec.VolumeName != "" && objectName.MatchString(c.Spec.VolumeName) {
			ref := Ref{Version: "v1", Resource: "persistentvolumes", Name: c.Spec.VolumeName}
			out = append(out, ChainLink{Role: "volume", Kind: "PersistentVolume", Name: c.Spec.VolumeName, Ref: &ref})
		}
	case "PersistentVolume":
		var v Volume
		_ = json.Unmarshal(body, &v)
		if cr := v.Spec.ClaimRef; cr != nil && dnsLabel.MatchString(cr.Namespace) && objectName.MatchString(cr.Name) {
			ref := Ref{Version: "v1", Resource: "persistentvolumeclaims", Namespace: cr.Namespace, Name: cr.Name}
			out = append(out, ChainLink{Role: "claim", Kind: "PersistentVolumeClaim", Name: cr.Namespace + "/" + cr.Name, Ref: &ref})
		}
		if n := v.Node(); n != "" {
			ref := Ref{Version: "v1", Resource: "nodes", Name: n}
			out = append(out, ChainLink{Role: "node", Kind: "Node", Name: n, Ref: &ref})
		}
	}
	if len(labels) > 0 && md.Namespace != "" {
		links, err := s.reachable(ctx, clusterID, md.Namespace, labels)
		if err != nil {
			return nil, err
		}
		out = append(out, links...)
	}
	return out, nil
}

// nodeChain is de node, de ClusterForge-node en de VM op de Proxmox-host.
func (s *Service) nodeChain(ctx context.Context, clusterID uuid.UUID, name string) ([]ChainLink, error) {
	ref := Ref{Version: "v1", Resource: "nodes", Name: name}
	out := []ChainLink{{Role: "node", Kind: "Node", Name: name, Ref: &ref}}
	p, err := ref.Path()
	if err != nil {
		return out, nil
	}
	n, err := GetInto[Node](ctx, s, clusterID, p)
	if errors.Is(err, ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	links, err := s.Links(ctx, clusterID, []Node{n})
	if err != nil {
		return nil, err
	}
	l, ok := links[name]
	if !ok {
		return out, nil
	}
	id := l.NodeID
	out = append(out, ChainLink{Role: "clusterforge", Name: l.Hostname, NodeID: &id})
	row, err := s.q.GetNode(ctx, l.NodeID)
	node := row.Node
	if err != nil || node.ProxmoxID == nil || node.PveVmid == nil {
		return out, nil
	}
	text := fmt.Sprintf("VM %d", *node.PveVmid)
	if g, err := s.q.GetProxmoxGuest(ctx, store.GetProxmoxGuestParams{ConnectionID: *node.ProxmoxID, Vmid: node.PveVmid}); err == nil && g.PveNode != "" {
		text += " op " + g.PveNode
	}
	out = append(out, ChainLink{Role: "vm", Name: strconv.Itoa(int(*node.PveVmid)), NodeID: &id, Text: text})
	return out, nil
}

// reachable zijn de Services die pods met deze labels kiezen, en de
// Ingresses naar die Services.
func (s *Service) reachable(ctx context.Context, clusterID uuid.UUID, ns string, labels map[string]string) ([]ChainLink, error) {
	services, ingresses, err := s.Network(ctx, clusterID, ns)
	if err != nil {
		return nil, err
	}
	var out []ChainLink
	picked := map[string]bool{}
	for _, sv := range services {
		if !sv.Selects(labels) {
			continue
		}
		picked[sv.Metadata.Name] = true
		ref := Ref{Version: "v1", Resource: "services", Namespace: ns, Name: sv.Metadata.Name}
		out = append(out, ChainLink{Role: "service", Kind: "Service", Name: sv.Metadata.Name, Ref: &ref, Text: sv.Describe(), URL: sv.URL()})
	}
	out = append(out, ingressLinks(ns, ingresses, picked)...)
	return out, nil
}

func ingressLinks(ns string, ingresses []Ingress, services map[string]bool) []ChainLink {
	var out []ChainLink
	for _, in := range ingresses {
		for _, r := range in.Rules() {
			if !services[r.Service] {
				continue
			}
			ref := Ref{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses", Namespace: ns, Name: in.Metadata.Name}
			out = append(out, ChainLink{Role: "ingress", Kind: "Ingress", Name: in.Metadata.Name, Ref: &ref, Text: r.Host + r.Path, URL: r.URL})
			break
		}
	}
	return out
}

// serviceChain zijn de pods die een Service kiest en de Ingresses ernaar.
func (s *Service) serviceChain(ctx context.Context, clusterID uuid.UUID, md Meta, body []byte) ([]ChainLink, error) {
	var sv KService
	_ = json.Unmarshal(body, &sv)
	var out []ChainLink
	if len(sv.Spec.Selector) > 0 {
		pods, err := s.Pods(ctx, clusterID, md.Namespace, "")
		if err != nil {
			return nil, err
		}
		n, ready := 0, 0
		for _, p := range pods {
			if sv.Selects(p.Metadata.Labels) {
				n++
				if r, t := p.ReadyCount(); r == t && t > 0 {
					ready++
				}
			}
		}
		out = append(out, ChainLink{Role: "pods", Kind: "Pod", Name: labelSelector(sv.Spec.Selector), Text: fmt.Sprintf("%d pods, %d klaar", n, ready)})
	}
	_, ingresses, err := s.Network(ctx, clusterID, md.Namespace)
	if err != nil {
		return nil, err
	}
	return append(out, ingressLinks(md.Namespace, ingresses, map[string]bool{md.Name: true})...), nil
}

func labelSelector(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

// LogOptions kiest welke log.
type LogOptions struct {
	Container string
	// Previous is de log van de container voor de laatste herstart.
	Previous bool
	// Lines is het aantal laatste regels, hoogstens KubeMaxLogLines.
	Lines      int
	Timestamps bool
}

// Logs geeft de laatste regels log van een container. trimmed zegt dat de
// agent de oudste regels wegliet omdat de log te groot was.
func (s *Service) Logs(ctx context.Context, clusterID uuid.UUID, ns, pod string, o LogOptions) (text string, trimmed bool, err error) {
	if !dnsLabel.MatchString(ns) || !ValidName(pod) || (o.Container != "" && !dnsLabel.MatchString(o.Container)) {
		return "", false, fmt.Errorf("%w: pod of container", ErrInvalid)
	}
	q := url.Values{"tailLines": {strconv.Itoa(min(max(o.Lines, 1), protocol.KubeMaxLogLines))}}
	if o.Container != "" {
		q.Set("container", o.Container)
	}
	if o.Previous {
		q.Set("previous", "true")
	}
	if o.Timestamps {
		q.Set("timestamps", "true")
	}
	body, _, err := s.request(ctx, clusterID, protocol.KubeLogs, "/api/v1/namespaces/"+ns+"/pods/"+pod+"/log", q)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 400 {
		// Zoals een container die de pod niet heeft.
		if strings.Contains(ae.Message, "previous terminated container") {
			return "", false, ErrNoPrevious
		}
		return "", false, fmt.Errorf("%w: %s", ErrInvalid, ae.Message)
	}
	if err != nil {
		return "", false, err
	}
	if err := json.Unmarshal(body, &text); err != nil {
		return "", false, fmt.Errorf("log: %w", err)
	}
	text, trimmed = strings.CutPrefix(text, protocol.KubeLogTrimmed)
	return text, trimmed, nil
}

// Events geeft de events, waarschuwingen eerst en dan de nieuwste, hoogstens
// limit.
func (s *Service) Events(ctx context.Context, clusterID uuid.UUID, ns string, limit int) ([]Event, error) {
	p, err := nsPath("/api/v1", ns, "events")
	if err != nil {
		return nil, err
	}
	events, err := List[Event](ctx, s, clusterID, p, nil)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(events, func(a, b Event) int {
		if (a.Type == "Warning") != (b.Type == "Warning") {
			if a.Type == "Warning" {
				return -1
			}
			return 1
		}
		return cmp.Or(b.When().Compare(a.When()), cmp.Compare(a.Metadata.Name, b.Metadata.Name))
	})
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}
