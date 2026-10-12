package kube

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De acties op een object, zoals de API ze noemt.
const (
	ActScale    = "scale"
	ActRestart  = "restart"
	ActRollback = "rollback"
	ActDelete   = "delete"
	ActCordon   = "cordon"
	ActUncordon = "uncordon"
	ActDrain    = "drain"
)

// MaxReplicas is het hoogste aantal pods waarnaar ClusterForge schaalt.
const MaxReplicas = 100

// MarkAnnotation staat op een node die ClusterForge voor onderhoud op
// cordon zette. Na het onderhoud haalt ClusterForge de cordon alleen weg
// als deze annotatie er staat: een cordon die iemand zelf zette, blijft.
const MarkAnnotation = "clusterforge.io/onderhoud"

// restartAnnotation is wat kubectl rollout restart zet.
const restartAnnotation = "kubectl.kubernetes.io/restartedAt"

// revisionAnnotation is het versienummer van een Deployment en zijn
// ReplicaSets.
const revisionAnnotation = "deployment.kubernetes.io/revision"

// ConflictError betekent dat de actie nu niet kan, met de reden.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// Action is één actie op een object.
type Action struct {
	Name string
	Ref  Ref
	// Replicas bij schalen.
	Replicas int
	// Revision bij terugdraaien; nul is de vorige versie.
	Revision int64
}

// Outcome zegt wat een actie deed. Event is leeg als er niets veranderde;
// anders gaat het met Payload naar het logboek.
type Outcome struct {
	Message string
	Kind    string
	Event   string
	Payload map[string]any
}

// write stuurt een schrijvend verzoek naar de beste server-node. Anders dan
// bij lezen gaat het niet naar de volgende als de agent niet op tijd
// antwoordt: dan is niet zeker of het verzoek al uitgevoerd is. Na een
// geslaagd verzoek is de cache van het cluster leeg, zodat het dashboard
// meteen de nieuwe toestand ziet.
func (s *Service) write(ctx context.Context, clusterID uuid.UUID, req protocol.KubeRequest) ([]byte, error) {
	nodes, err := s.candidates(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	var last error
	for _, n := range nodes {
		req.ID = uuid.NewString()
		rctx, cancel := context.WithTimeout(ctx, s.Timeout)
		res, err := s.bus.Kube(rctx, n.ID, req)
		cancel()
		switch {
		case errors.Is(err, agentbus.ErrAgentOffline):
			last = &UnavailableError{Msg: "de agent op " + n.Hostname + " is niet verbonden"}
			continue
		case errors.Is(err, agentbus.ErrNoAnswer):
			return nil, &UnavailableError{Msg: "de agent op " + n.Hostname + " antwoordde niet op tijd; kijk of de actie toch gelukt is"}
		case err != nil:
			return nil, err
		case res.Status == 0 && strings.HasPrefix(res.Error, "onbekend kube-verzoek"):
			last = &UnavailableError{TooOld: true, Msg: "de agent op " + n.Hostname +
				" kent deze actie nog niet; werk hem bij met het installatiescript en --upgrade"}
			continue
		case res.Status == 0:
			last = &UnavailableError{Msg: n.Hostname + ": " + res.Error}
			continue
		}
		s.forget(clusterID)
		if res.Status < 200 || res.Status > 299 {
			return nil, apiError(res)
		}
		if res.Error != "" {
			return nil, &APIError{Status: res.Status, Message: res.Error}
		}
		return res.Body, nil
	}
	return nil, last
}

// forget leegt de cache van een cluster.
func (s *Service) forget(clusterID uuid.UUID) {
	prefix := clusterID.String() + " "
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.cache {
		if strings.HasPrefix(k, prefix) {
			delete(s.cache, k)
		}
	}
}

func (s *Service) patch(ctx context.Context, clusterID uuid.UUID, path, patchType string, patch any) error {
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = s.write(ctx, clusterID, protocol.KubeRequest{Op: protocol.KubePatch, Path: path, Body: body, PatchType: patchType})
	return err
}

// typeOf zoekt de soort van een object in de discovery.
func (s *Service) typeOf(ctx context.Context, clusterID uuid.UUID, ref Ref) (ResourceType, error) {
	types, err := s.Resources(ctx, clusterID)
	if err != nil {
		return ResourceType{}, err
	}
	for _, t := range types {
		if t.Group == ref.Group && t.Version == ref.Version && t.Resource == ref.Resource {
			if t.Namespaced != (ref.Namespace != "") {
				if t.Namespaced {
					return t, fmt.Errorf("%w: %s horen in een namespace", ErrInvalid, ref.Resource)
				}
				return t, fmt.Errorf("%w: %s hebben geen namespace", ErrInvalid, ref.Resource)
			}
			return t, nil
		}
	}
	return ResourceType{}, fmt.Errorf("%w: het cluster kent de soort %s niet", ErrNotFound, ref.Resource)
}

// scalable zijn de soorten met een aantal pods.
var scalable = []string{"Deployment", "StatefulSet", "ReplicaSet"}

// restartable zijn de soorten die herstarten met een nieuw podtemplate.
var restartable = []string{"Deployment", "StatefulSet", "DaemonSet"}

// ActionsFor zegt welke acties bij een object horen, en hoeveel pods het
// vraagt als je het kunt schalen.
func ActionsFor(kind string, ref Ref, body []byte) (actions []string, replicas *int) {
	var o struct {
		Metadata Meta `json:"metadata"`
		Spec     struct {
			Replicas      *int `json:"replicas"`
			Unschedulable bool `json:"unschedulable"`
		} `json:"spec"`
	}
	_ = json.Unmarshal(body, &o)
	builtin := ref.Group == "" || ref.Group == "apps"
	switch {
	case kind == "Node" && ref.Group == "":
		if o.Spec.Unschedulable {
			return []string{ActUncordon, ActDrain}, nil
		}
		return []string{ActCordon, ActDrain}, nil
	case kind == "Namespace" && ref.Group == "" && slices.Contains(protectedNamespaces, ref.Name):
		return []string{}, nil
	case builtin && slices.Contains(scalable, kind):
		r := 1
		if o.Spec.Replicas != nil {
			r = *o.Spec.Replicas
		}
		replicas = &r
		if kind == "ReplicaSet" {
			if ow := o.Metadata.Owner(); ow != nil && ow.Kind == "Deployment" {
				return []string{ActDelete}, nil
			}
			return []string{ActScale, ActDelete}, replicas
		}
		actions = []string{ActScale, ActRestart}
		if kind == "Deployment" {
			actions = append(actions, ActRollback)
		}
		return append(actions, ActDelete), replicas
	case builtin && kind == "DaemonSet":
		return []string{ActRestart, ActDelete}, nil
	}
	return []string{ActDelete}, nil
}

// protectedNamespaces zijn de namespaces van Kubernetes zelf; ook de agent
// weigert ze weg te halen.
var protectedNamespaces = []string{"default", "kube-system", "kube-public", "kube-node-lease"}

// Do voert een actie uit op één object.
func (s *Service) Do(ctx context.Context, clusterID uuid.UUID, a Action) (Outcome, error) {
	if a.Ref.Name == "" {
		return Outcome{}, fmt.Errorf("%w: geen naam", ErrInvalid)
	}
	p, err := a.Ref.Path()
	if err != nil {
		return Outcome{}, err
	}
	t, err := s.typeOf(ctx, clusterID, a.Ref)
	if err != nil {
		return Outcome{}, err
	}
	// Vers ophalen: een antwoord uit de cache kan net verouderd zijn.
	s.forget(clusterID)
	body, err := s.Get(ctx, clusterID, p, nil)
	if err != nil {
		return Outcome{}, err
	}
	allowed, _ := ActionsFor(t.Kind, a.Ref, body)
	if !slices.Contains(allowed, a.Name) && a.Name != ActCordon && a.Name != ActUncordon {
		return Outcome{}, notFor(a.Name, t.Kind, a.Ref, body)
	}
	var o struct {
		Metadata Meta `json:"metadata"`
		Spec     struct {
			Replicas      *int `json:"replicas"`
			Paused        bool `json:"paused"`
			Unschedulable bool `json:"unschedulable"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &o); err != nil {
		return Outcome{}, fmt.Errorf("%s: %w", p, err)
	}
	name := a.Ref.Name
	payload := map[string]any{"kind": t.Kind, "namespace": a.Ref.Namespace, "name": name}
	out := Outcome{Kind: t.Kind, Payload: payload}
	switch a.Name {
	case ActScale:
		if a.Replicas < 0 || a.Replicas > MaxReplicas {
			return Outcome{}, fmt.Errorf("%w: het aantal pods moet tussen 0 en %d liggen", ErrInvalid, MaxReplicas)
		}
		from := 1
		if o.Spec.Replicas != nil {
			from = *o.Spec.Replicas
		}
		if from == a.Replicas {
			out.Message = fmt.Sprintf("%s vraagt al %s; niets veranderd", name, pods(from))
			return out, nil
		}
		if err := s.patch(ctx, clusterID, p+"/scale", protocol.KubeMergePatch,
			map[string]any{"spec": map[string]any{"replicas": a.Replicas}}); err != nil {
			return Outcome{}, err
		}
		payload["from"], payload["to"] = from, a.Replicas
		out.Event, out.Message = "kube.scaled", fmt.Sprintf("%s geschaald van %d naar %s", name, from, pods(a.Replicas))
		if a.Replicas == 0 {
			out.Message = fmt.Sprintf("%s geschaald naar 0: alle pods stoppen, de app blijft bestaan", name)
		}
	case ActRestart:
		if o.Spec.Paused {
			return Outcome{}, &ConflictError{name + " staat op pauze; zet hem eerst verder met kubectl rollout resume"}
		}
		patch := map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
			"annotations": map[string]any{restartAnnotation: s.Now().UTC().Format(time.RFC3339)},
		}}}}
		if err := s.patch(ctx, clusterID, p, protocol.KubeMergePatch, patch); err != nil {
			return Outcome{}, err
		}
		out.Event = "kube.restarted"
		out.Message = name + " wordt herstart: Kubernetes vervangt de pods een voor een"
		if t.Kind == "StatefulSet" {
			out.Message = name + " wordt herstart: Kubernetes vervangt de pods een voor een, van de hoogste naar de laagste"
		}
	case ActRollback:
		if o.Spec.Paused {
			return Outcome{}, &ConflictError{name + " staat op pauze; zet hem eerst verder met kubectl rollout resume"}
		}
		revs, tpls, err := s.revisions(ctx, clusterID, a.Ref.Namespace, name, body)
		if err != nil {
			return Outcome{}, err
		}
		var cur, target *Revision
		for i := range revs {
			r := &revs[i]
			if r.Current {
				cur = r
			}
			switch {
			case a.Revision != 0 && r.Revision == a.Revision:
				target = r
			case a.Revision == 0 && !r.Current && (target == nil || r.Revision > target.Revision):
				target = r
			}
		}
		switch {
		case target == nil && a.Revision != 0:
			return Outcome{}, &ConflictError{fmt.Sprintf("%s heeft geen versie %d meer", name, a.Revision)}
		case target == nil:
			return Outcome{}, &ConflictError{name + " heeft geen vorige versie om naar terug te gaan"}
		case target.Current:
			return Outcome{}, &ConflictError{fmt.Sprintf("versie %d is de huidige versie van %s", target.Revision, name)}
		}
		patch := []any{map[string]any{"op": "replace", "path": "/spec/template", "value": tpls[target.ReplicaSet]}}
		if err := s.patch(ctx, clusterID, p, protocol.KubeJSONPatch, patch); err != nil {
			return Outcome{}, err
		}
		payload["to_revision"], payload["images"] = target.Revision, target.Images
		if cur != nil {
			payload["from_revision"] = cur.Revision
		}
		out.Event = "kube.rolled_back"
		out.Message = fmt.Sprintf("%s gaat terug naar versie %d (%s); Kubernetes vervangt de pods een voor een",
			name, target.Revision, strings.Join(target.Images, ", "))
	case ActDelete:
		if _, err := s.write(ctx, clusterID, protocol.KubeRequest{
			Op: protocol.KubeDelete, Path: p, Query: map[string]string{"propagationPolicy": "Background"},
		}); err != nil {
			return Outcome{}, err
		}
		out.Event, out.Message = "kube.deleted", deletedText(t.Kind, a.Ref, o.Metadata)
		if ow := o.Metadata.Owner(); ow != nil {
			payload["owner_kind"], payload["owner_name"] = ow.Kind, ow.Name
		}
	case ActCordon, ActUncordon:
		if t.Kind != "Node" || a.Ref.Group != "" {
			return Outcome{}, notFor(a.Name, t.Kind, a.Ref, body)
		}
		want := a.Name == ActCordon
		if o.Spec.Unschedulable == want {
			if want {
				out.Message = name + " staat al op cordon; niets veranderd"
			} else {
				out.Message = name + " krijgt al nieuwe pods; niets veranderd"
			}
			return out, nil
		}
		if want {
			if err := s.setUnschedulable(ctx, clusterID, name, true, false); err != nil {
				return Outcome{}, err
			}
			out.Event = "kube.cordoned"
			out.Message = name + " staat op cordon: er komen geen nieuwe pods op; wat er draait, blijft draaien"
		} else {
			if err := s.setUnschedulable(ctx, clusterID, name, false, false); err != nil {
				return Outcome{}, err
			}
			out.Event = "kube.uncordoned"
			out.Message = name + " krijgt weer nieuwe pods; wat al elders draait, verhuist niet vanzelf terug"
		}
		payload["node"] = name
	default:
		return Outcome{}, fmt.Errorf("%w: onbekende actie %s", ErrInvalid, a.Name)
	}
	return out, nil
}

// notFor zegt waarom een actie niet bij een object hoort.
func notFor(action, kind string, ref Ref, body []byte) error {
	var o struct {
		Metadata Meta `json:"metadata"`
	}
	_ = json.Unmarshal(body, &o)
	switch {
	case action == ActScale && kind == "ReplicaSet":
		if ow := o.Metadata.Owner(); ow != nil {
			return &ConflictError{fmt.Sprintf("ReplicaSet %s hoort bij %s %s: schaal die, anders zet hij het aantal meteen terug",
				ref.Name, ow.Kind, ow.Name)}
		}
	case action == ActDelete && kind == "Node":
		return &ConflictError{"een node haal je zo niet weg: k3s meldt hem meteen opnieuw aan. Maak hem leeg en zet de VM uit"}
	case action == ActDelete && kind == "Namespace":
		return &ConflictError{"namespace " + ref.Name + " is van Kubernetes zelf; zonder werkt het cluster niet"}
	case action == ActRollback:
		return &ConflictError{"terugdraaien kan alleen bij een Deployment; een " + kind + " zet je terug door de oude YAML toe te passen"}
	}
	return &ConflictError{fmt.Sprintf("%s kan niet bij een %s", actionNames[action], kind)}
}

var actionNames = map[string]string{
	ActScale: "schalen", ActRestart: "herstarten", ActRollback: "terugdraaien", ActDelete: "weghalen",
	ActCordon: "cordon", ActUncordon: "uncordon", ActDrain: "leegmaken",
}

func pods(n int) string {
	if n == 1 {
		return "1 pod"
	}
	return strconv.Itoa(n) + " pods"
}

// deletedText zegt wat er na het weghalen gebeurt.
func deletedText(kind string, ref Ref, m Meta) string {
	what := kind + " " + ref.Name
	if ref.Namespace != "" {
		what = kind + " " + ref.Namespace + "/" + ref.Name
	}
	ow := m.Owner()
	switch {
	case kind == "Pod" && ow != nil && ow.Kind != "Node":
		return what + " weggehaald; " + ow.Kind + " " + ow.Name + " maakt meteen een nieuwe"
	case kind == "Pod":
		return what + " weggehaald; hij had geen eigenaar en komt niet terug"
	case kind == "Namespace":
		return what + " wordt weggehaald, met alles erin"
	case slices.Contains(scalable, kind) || slices.Contains(restartable, kind) || kind == "Job" || kind == "CronJob":
		return what + " weggehaald, met zijn pods"
	}
	return what + " weggehaald"
}

// setUnschedulable zet een node op cordon of haalt de cordon weg. mark
// zet bij cordon de annotatie van ClusterForge; bij uncordon gaat ze er
// altijd af.
func (s *Service) setUnschedulable(ctx context.Context, clusterID uuid.UUID, node string, on, mark bool) error {
	if !ValidName(node) {
		return fmt.Errorf("%w: node %q", ErrInvalid, node)
	}
	md := map[string]any{}
	spec := map[string]any{"unschedulable": nil}
	switch {
	case on && mark:
		spec["unschedulable"] = true
		md["annotations"] = map[string]any{MarkAnnotation: s.Now().UTC().Format(time.RFC3339)}
	case on:
		spec["unschedulable"] = true
	default:
		md["annotations"] = map[string]any{MarkAnnotation: nil}
	}
	patch := map[string]any{"spec": spec}
	if len(md) > 0 {
		patch["metadata"] = md
	}
	return s.patch(ctx, clusterID, "/api/v1/nodes/"+node, protocol.KubeMergePatch, patch)
}

// Revision is een versie van een Deployment: een ReplicaSet met zijn
// podtemplate.
type Revision struct {
	Revision   int64
	ReplicaSet string
	Images     []string
	CreatedAt  time.Time
	// Replicas is hoeveel pods de ReplicaSet nu vraagt.
	Replicas int
	Current  bool
	// ChangeCause is de annotatie kubernetes.io/change-cause, als iemand
	// die zette.
	ChangeCause string
}

// Revisions geeft de versies van een Deployment, de nieuwste eerst.
func (s *Service) Revisions(ctx context.Context, clusterID uuid.UUID, ns, name string) ([]Revision, error) {
	ref := Ref{Group: "apps", Version: "v1", Resource: "deployments", Namespace: ns, Name: name}
	p, err := ref.Path()
	if err != nil {
		return nil, err
	}
	body, err := s.Get(ctx, clusterID, p, nil)
	if err != nil {
		return nil, err
	}
	revs, _, err := s.revisions(ctx, clusterID, ns, name, body)
	return revs, err
}

type replicaSet struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		Replicas *int            `json:"replicas"`
		Template json.RawMessage `json:"template"`
	} `json:"spec"`
}

// revisions geeft de versies van een Deployment en per ReplicaSet het
// podtemplate zoals terugdraaien het terugzet: zonder pod-template-hash.
func (s *Service) revisions(ctx context.Context, clusterID uuid.UUID, ns, name string, deployment []byte) ([]Revision, map[string]any, error) {
	var d struct {
		Metadata Meta `json:"metadata"`
	}
	if err := json.Unmarshal(deployment, &d); err != nil {
		return nil, nil, err
	}
	p, err := nsPath("/apis/apps/v1", ns, "replicasets")
	if err != nil {
		return nil, nil, err
	}
	sets, err := List[replicaSet](ctx, s, clusterID, p, nil)
	if err != nil {
		return nil, nil, err
	}
	current, _ := strconv.ParseInt(d.Metadata.Annotations[revisionAnnotation], 10, 64)
	var out []Revision
	tpls := map[string]any{}
	for _, rs := range sets {
		ow := rs.Metadata.Owner()
		if ow == nil || ow.Kind != "Deployment" || ow.Name != name {
			continue
		}
		rev, err := strconv.ParseInt(rs.Metadata.Annotations[revisionAnnotation], 10, 64)
		if err != nil {
			continue
		}
		var tpl map[string]any
		if err := json.Unmarshal(rs.Spec.Template, &tpl); err != nil {
			continue
		}
		var images []string
		if spec, ok := tpl["spec"].(map[string]any); ok {
			cs, _ := spec["containers"].([]any)
			for _, c := range cs {
				if m, ok := c.(map[string]any); ok {
					img, _ := m["image"].(string)
					images = append(images, img)
				}
			}
		}
		if md, ok := tpl["metadata"].(map[string]any); ok {
			if labels, ok := md["labels"].(map[string]any); ok {
				delete(labels, "pod-template-hash")
			}
		}
		tpls[rs.Metadata.Name] = tpl
		r := Revision{
			Revision: rev, ReplicaSet: rs.Metadata.Name, Images: images, CreatedAt: rs.Metadata.CreationTimestamp,
			ChangeCause: rs.Metadata.Annotations["kubernetes.io/change-cause"],
		}
		if rs.Spec.Replicas != nil {
			r.Replicas = *rs.Spec.Replicas
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Revision) int { return cmp.Compare(b.Revision, a.Revision) })
	if current == 0 && len(out) > 0 {
		current = out[0].Revision
	}
	for i := range out {
		out[i].Current = out[i].Revision == current
	}
	return out, tpls, nil
}

// evict zet één pod uit; een pod die al weg is, is geen fout. blocked is de
// reden als een PodDisruptionBudget het nu niet toestaat.
func (s *Service) evict(ctx context.Context, clusterID uuid.UUID, ns, pod string) (blocked string, err error) {
	p, err := nsPath("/api/v1", ns, "pods")
	if err != nil {
		return "", err
	}
	if !ValidName(pod) {
		return "", fmt.Errorf("%w: pod %q", ErrInvalid, pod)
	}
	_, err = s.write(ctx, clusterID, protocol.KubeRequest{Op: protocol.KubeEvict, Path: p + "/" + pod + "/eviction"})
	var ae *APIError
	switch {
	case errors.Is(err, ErrNotFound):
		return "", nil
	case errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests:
		return ae.Message, nil
	}
	return "", err
}

// podsOnNode zijn de pods op een node.
func (s *Service) podsOnNode(ctx context.Context, clusterID uuid.UUID, node string) ([]Pod, error) {
	return List[Pod](ctx, s, clusterID, "/api/v1/pods", url.Values{"fieldSelector": {"spec.nodeName=" + node}})
}
