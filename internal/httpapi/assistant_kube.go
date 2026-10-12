package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/kubelessons"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// De tools voor Kubernetes lezen via dezelfde GET-handlers als het
// dashboard: via de agent op een server, alleen lezend, en zonder de
// waarden van Secrets, want die verlaten de node niet. Logs, YAML en
// berichten gaan bovendien door assistant.Mask.

// kubeLogLines is hoeveel regels log de assistent leest.
const kubeLogLines = 200

// kubeYAMLBudget is hoeveel YAML van een object naar het model gaat.
const kubeYAMLBudget = 8 * 1024

func (s *Server) kubeTools() []assistant.Tool {
	return []assistant.Tool{
		{
			Name: "kubernetes",
			Description: "Een Kubernetes-cluster nu, live uit de API: versie, nodes met Ready en de reden, apps met hoeveel pods klaar zijn " +
				"en waarom niet, pods met een probleem en de waarschuwingen van Kubernetes. Met een namespace: alle apps en pods " +
				"daarin, ook van k3s zelf in kube-system.",
			Properties: map[string]any{
				"cluster":   strProp("Naam of id van het Kubernetes-cluster"),
				"namespace": strProp("Een namespace, of leeg voor de eigen apps in alle namespaces"),
			},
			Required: []string{"cluster", "namespace"},
			Label: func(in map[string]any) string {
				if ns := arg(in, "namespace"); ns != "" {
					return "Namespace " + ns + " in " + arg(in, "cluster") + " bekijken"
				}
				return "Kubernetes in " + arg(in, "cluster") + " bekijken"
			},
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Cluster   string `json:"cluster"`
					Namespace string `json:"namespace"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				id, err := s.findCluster(ctx, c, in.Cluster)
				if err != nil {
					return nil, err
				}
				var ov gen.KubeOverview
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetKubeOverview(w, r, id) }, &ov); err != nil {
					return nil, err
				}
				var nodes list[gen.KubeNode]
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListKubeNodes(w, r, id) }, &nodes); err != nil {
					return nil, err
				}
				wp := gen.ListKubeWorkloadsParams{}
				if in.Namespace != "" {
					all := true
					wp.Namespace, wp.System = &in.Namespace, &all
				}
				var apps list[gen.KubeWorkload]
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListKubeWorkloads(w, r, id, wp) }, &apps); err != nil {
					return nil, err
				}
				pods := ov.Problems
				if in.Namespace != "" {
					all := true
					var pl list[gen.KubePod]
					if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) {
						s.ListKubePods(w, r, id, gen.ListKubePodsParams{Namespace: &in.Namespace, System: &all})
					}, &pl); err != nil {
						return nil, err
					}
					pods = pl.Items
				}
				for i := range ov.Warnings {
					ov.Warnings[i].Message = assistant.Mask(ov.Warnings[i].Message)
				}
				ns := make([]any, len(nodes.Items))
				for i, n := range nodes.Items {
					ns[i] = compact(n, "os_image", "kernel", "container_runtime", "arch", "created_at", "pods")
				}
				out := map[string]any{
					"versie": ov.Version, "via": ov.Via, "vip": ov.Vip, "nodes": ns, "namespaces": ov.Namespaces,
					"pods_telling": compact(ov.Pods), "apps_klaar": fmt.Sprintf("%d van %d", ov.WorkloadsReady, ov.WorkloadsTotal),
					"waarschuwingen": compactAll(ov.Warnings, "object", "namespace"),
					"apps":           compactAll(apps.Items, "created_at", "up_to_date"),
				}
				res, _ := compact(out).(map[string]any)
				if res == nil {
					res = map[string]any{}
				}
				if in.Namespace == "" {
					res["opmerking_pods"] = "alleen de pods met een probleem; vraag een namespace voor alle pods"
				}
				return withList(res, "pods", compactAll(pods, "created_at", "containers", "owner_kind"), false, false), nil
			},
		},
		{
			Name: "kube_object",
			Description: "Eén object in een Kubernetes-cluster: zijn toestand in gewone woorden, condities, de keten van Deployment " +
				"via pod en node tot de VM, de events, bij een pod elke container met zijn toestand, herstarts en laatste exitcode, " +
				"en de YAML. Van een Secret alleen de namen van de sleutels.",
			Properties: map[string]any{
				"cluster":   strProp("Naam of id van het Kubernetes-cluster"),
				"soort":     strProp("De soort, zoals pods, deployments, statefulsets, services, ingresses, nodes of persistentvolumeclaims; ook Pod of Deployment mag"),
				"namespace": strProp("De namespace; leeg bij een node of een ander object zonder namespace"),
				"naam":      strProp("De naam van het object"),
			},
			Required: []string{"cluster", "soort", "namespace", "naam"},
			Label: func(in map[string]any) string {
				name := arg(in, "naam")
				if ns := arg(in, "namespace"); ns != "" {
					name = ns + "/" + name
				}
				// pods wordt pod, ingresses ingress.
				soort := strings.ToLower(arg(in, "soort"))
				if strings.HasSuffix(soort, "sses") {
					soort = strings.TrimSuffix(soort, "es")
				}
				return strings.TrimSuffix(soort, "s") + " " + name + " bekijken"
			},
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Cluster   string `json:"cluster"`
					Soort     string `json:"soort"`
					Namespace string `json:"namespace"`
					Naam      string `json:"naam"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				id, err := s.findCluster(ctx, c, in.Cluster)
				if err != nil {
					return nil, err
				}
				rt, err := s.kubeResource(ctx, c, id, in.Soort)
				if err != nil {
					return nil, err
				}
				p := gen.GetKubeObjectParams{Group: &rt.Group, Version: rt.Version, Resource: rt.Resource, Name: in.Naam}
				if rt.Namespaced {
					if in.Namespace == "" {
						return nil, assistant.ToolError{Msg: rt.Resource + " hebben een namespace; geef die mee"}
					}
					p.Namespace = &in.Namespace
				}
				var o gen.KubeObject
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetKubeObject(w, r, id, p) }, &o); err != nil {
					return nil, err
				}
				return kubeObjectForModel(o), nil
			},
		},
		{
			Name: "kube_logs",
			Description: fmt.Sprintf("De laatste %d regels log van een container in een pod, met tijdstippen. Met vorige de log van "+
				"de container voor zijn laatste herstart: daar staat meestal waarom hij stopte. Wachtwoorden en tokens zijn vervangen door %s.",
				kubeLogLines, assistant.Masked),
			Properties: map[string]any{
				"cluster":   strProp("Naam of id van het Kubernetes-cluster"),
				"namespace": strProp("De namespace van de pod"),
				"pod":       strProp("De naam van de pod"),
				"container": strProp("De container; leeg als de pod er maar één heeft"),
				"vorige":    boolean("De log van de vorige container, van voor de laatste herstart"),
			},
			Required: []string{"cluster", "namespace", "pod", "container", "vorige"},
			Label: func(in map[string]any) string {
				what := "Logs van "
				if v, _ := in["vorige"].(bool); v {
					what = "Vorige logs van "
				}
				return what + arg(in, "namespace") + "/" + arg(in, "pod") + " lezen"
			},
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Cluster   string `json:"cluster"`
					Namespace string `json:"namespace"`
					Pod       string `json:"pod"`
					Container string `json:"container"`
					Vorige    bool   `json:"vorige"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				id, err := s.findCluster(ctx, c, in.Cluster)
				if err != nil {
					return nil, err
				}
				lines, stamps := kubeLogLines, true
				p := gen.GetKubeLogsParams{Namespace: in.Namespace, Pod: in.Pod, Previous: &in.Vorige, Lines: &lines, Timestamps: &stamps}
				if in.Container != "" {
					p.Container = &in.Container
				}
				var l gen.KubeLogs
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetKubeLogs(w, r, id, p) }, &l); err != nil {
					return nil, err
				}
				out := map[string]any{"pod": in.Namespace + "/" + in.Pod, "vorige": in.Vorige}
				if in.Container != "" {
					out["container"] = in.Container
				}
				text := assistant.Mask(l.Text)
				if strings.TrimSpace(text) == "" {
					out["opmerking"] = "de log is leeg"
					return out, nil
				}
				// Bij te veel tekst tellen de laatste regels.
				if len(text) > listBudget {
					cut := len(text) - listBudget
					if i := strings.IndexByte(text[cut:], '\n'); i >= 0 {
						cut += i + 1
					}
					text = text[cut:]
					out["let_op"] = "alleen de laatste regels; de eerdere waren te lang"
				}
				out["log"] = text
				return out, nil
			},
		},
	}
}

// kubeResource zoekt de soort zoals het model hem noemt (pods, Pod, pod)
// in de soorten die het cluster kent.
func (s *Server) kubeResource(ctx context.Context, c assistant.Caller, clusterID uuid.UUID, soort string) (gen.KubeResourceType, error) {
	var l list[gen.KubeResourceType]
	if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListKubeResources(w, r, clusterID) }, &l); err != nil {
		return gen.KubeResourceType{}, err
	}
	want := strings.ToLower(strings.TrimSpace(soort))
	var best *gen.KubeResourceType
	for i, t := range l.Items {
		if t.Resource != want && strings.ToLower(t.Kind) != want && strings.TrimSuffix(t.Resource, "s") != want {
			continue
		}
		// De kern en apps gaan voor, zoals Event voor events.k8s.io.
		if best == nil || (best.Group != "" && (t.Group == "" || t.Group == "apps")) {
			best = &l.Items[i]
		}
	}
	if best == nil {
		return gen.KubeResourceType{}, assistant.ToolError{Msg: fmt.Sprintf("het cluster kent geen soort %q; gebruik bijvoorbeeld pods, deployments of nodes", soort)}
	}
	return *best, nil
}

// kubeObjectForModel houdt van een object wat helpt om het te begrijpen.
// Annotaties vallen weg; daar staat soms de hele vorige versie in, met
// waarden.
func kubeObjectForModel(o gen.KubeObject) map[string]any {
	chain := make([]any, 0, len(o.Chain))
	for _, l := range o.Chain {
		chain = append(chain, compact(map[string]any{"rol": l.Role, "soort": l.Kind, "naam": l.Name, "tekst": l.Text}))
	}
	for i := range o.Events {
		o.Events[i].Message = assistant.Mask(o.Events[i].Message)
	}
	for i := range o.Conditions {
		o.Conditions[i].Message = assistant.Mask(o.Conditions[i].Message)
	}
	out := map[string]any{
		"soort": o.Kind, "naam": o.Ref.Name, "namespace": o.Ref.Namespace, "toestand": o.Summary, "probleem": o.Problem,
		"labels": o.Labels, "aangemaakt": o.CreatedAt, "condities": compactAll(o.Conditions), "keten": chain,
		"events": compactAll(o.Events, "object", "namespace", "kind", "name"),
	}
	if r, err := o.Replicas.Get(); err == nil {
		out["replicas"] = r
	}
	if p, err := o.Pod.Get(); err == nil {
		for i := range p.Containers {
			p.Containers[i].Message = assistant.Mask(p.Containers[i].Message)
		}
		out["pod"] = compact(p, "host_ip")
	}
	yaml := assistant.Mask(o.Yaml)
	if len(yaml) > kubeYAMLBudget {
		cut := kubeYAMLBudget
		for cut > 0 && yaml[cut]&0xC0 == 0x80 {
			cut--
		}
		yaml = yaml[:cut] + "\n# [afgekapt]"
	}
	out["yaml"] = yaml
	m, _ := compact(out).(map[string]any)
	return m
}

// kubeObjectLink is de pagina van een object in het dashboard.
func kubeObjectLink(clusterID uuid.UUID, ref gen.KubeRef) string {
	q := url.Values{"id": {clusterID.String()}, "g": {ref.Group}, "v": {ref.Version}, "r": {ref.Resource}, "name": {ref.Name}}
	if ref.Namespace != "" {
		q.Set("ns", ref.Namespace)
	}
	return "/kubernetes/object?" + q.Encode()
}

// canAssist schrijft 403 als de assistent niet voor deze gebruiker is.
func (s *Server) canAssist(w http.ResponseWriter, r *http.Request) bool {
	ok, reason, err := s.assistant.CanUse(r.Context(), caller(r))
	switch {
	case err != nil:
		s.internalError(w, r, err)
		return false
	case !ok:
		writeError(w, http.StatusForbidden, "forbidden", reason)
		return false
	}
	return true
}

// ExplainKubeObject laat de assistent uitleggen wat er met een object is,
// zoals 'Waarom start deze pod niet?'.
func (s *Server) ExplainKubeObject(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	var ref gen.KubeRef
	if !decode(w, r, &ref) {
		return
	}
	if !s.canAssist(w, r) {
		return
	}
	ctx := r.Context()
	cl, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	// Het object zoals het dashboard het nu toont; een fout gaat zo naar
	// de gebruiker.
	p := gen.GetKubeObjectParams{Group: &ref.Group, Version: ref.Version, Resource: ref.Resource, Name: ref.Name}
	if ref.Namespace != "" {
		p.Namespace = &ref.Namespace
	}
	rec := &viewRecorder{header: http.Header{}}
	s.GetKubeObject(rec, r, clusterID, p)
	if rec.status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body.Bytes())
		return
	}
	var o gen.KubeObject
	if err := json.Unmarshal(rec.body.Bytes(), &o); err != nil {
		s.internalError(w, r, err)
		return
	}
	turn, err := s.assistant.Start(ctx, caller(r), kubeExplainTask(cl, o))
	if s.assistantError(w, r, err) {
		return
	}
	s.streamTurn(w, r, turn)
}

func kubeExplainTask(cl store.Cluster, o gen.KubeObject) assistant.Task {
	name := o.Ref.Name
	if o.Ref.Namespace != "" {
		name = o.Ref.Namespace + "/" + name
	}
	question := "Leg " + o.Kind + " " + name + " uit."
	switch {
	case o.Kind == "Pod" && o.Problem:
		question = "Waarom start pod " + name + " niet?"
	case o.Kind == "Pod":
		question = "Hoe gaat het met pod " + name + "?"
	case o.Problem:
		question = "Wat is er mis met " + o.Kind + " " + name + "?"
	}
	data, _ := json.Marshal(kubeObjectForModel(o))
	id := cl.ID
	return assistant.Task{
		Kind: assistant.KindExplain, Title: question, Question: question, SubjectType: assistant.SubjectCluster, SubjectID: &id,
		Prompt: fmt.Sprintf(`%s Het object staat in het Kubernetes-cluster %s (%s). Wat ClusterForge er nu over weet, staat hieronder; de pagina in ClusterForge is %s.

Zoek de oorzaak in de events, de condities, de toestand van de containers en de logs:
- bij een pod die herstart of niet klaar is: lees de log met kube_logs, en met vorige=true de log van voor de laatste herstart; daar staat meestal waarom de container stopte;
- bij een app (Deployment, StatefulSet, DaemonSet): zoek met de tool kubernetes en de namespace de pods met een probleem, en bekijk er een met kube_object;
- bij een node: de condities en de pods erop.

Antwoord in gewone taal, voor iemand die Kubernetes aan het leren is:
- eerst de oorzaak in een of twee zinnen, met het bewijs: de regel uit de log of het event, letterlijk;
- dan wat de gebruiker kan nakijken of doen, in ClusterForge (met een link) of met kubectl op een server;
- weet je het niet zeker, zeg dat dan, en wat je nog zou willen zien.
Houd het kort. De waarden van Secrets ziet ClusterForge niet, en %s in een log of YAML is een weggelaten wachtwoord of token.

Het object:
%s`, question, cl.Name, cl.Environment, kubeObjectLink(cl.ID, o.Ref), assistant.Masked, data),
	}
}

// HintKubeLesson is 'Ik zit vast': de assistent kijkt op het cluster en
// geeft een hint bij de opdracht die aan de beurt is.
func (s *Server) HintKubeLesson(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, lesson string) {
	l, ok := kubelessons.Find(lesson)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "onbekende les")
		return
	}
	if !s.canAssist(w, r) {
		return
	}
	ctx := r.Context()
	cl, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	env, err := s.lessonEnv(r, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	res, err := s.lessons.Check(ctx, env, l, nil)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	switch {
	case res.Unavailable != "":
		writeError(w, http.StatusConflict, "unavailable", res.Unavailable)
		return
	case res.Problem != "":
		writeError(w, http.StatusServiceUnavailable, "kube_unavailable", res.Problem)
		return
	}
	i := slices.IndexFunc(res.Steps, func(st kubelessons.StepResult) bool { return st.State == kubelessons.StateCurrent })
	if i < 0 {
		writeError(w, http.StatusConflict, "done", "deze les is af; er is geen opdracht aan de beurt")
		return
	}
	turn, err := s.assistant.Start(ctx, caller(r), hintTask(cl, l, res.Steps[i]))
	if s.assistantError(w, r, err) {
		return
	}
	s.streamTurn(w, r, turn)
}

func hintTask(cl store.Cluster, l *kubelessons.Lesson, st kubelessons.StepResult) assistant.Task {
	// Het goede antwoord op een vraag gaat niet mee: dan kan de assistent
	// het ook niet verklappen.
	step := map[string]any{
		"titel": st.Title, "uitleg": st.Text, "in_clusterforge": st.Web, "met_kubectl": st.Kubectl, "yaml": st.YAML,
		"vraag": st.Question, "wat_clusterforge_zag": st.Detail,
	}
	if st.Question != "" && st.Step.Hint != "" {
		step["hint_van_de_les"] = st.Step.Hint
	}
	data, _ := json.Marshal(compact(step))
	id := cl.ID
	question := fmt.Sprintf("Ik zit vast bij les %d, %s: %s", l.Number, l.Title, st.Title)
	return assistant.Task{
		Kind: assistant.KindExplain, Title: question, Question: question, SubjectType: assistant.SubjectCluster, SubjectID: &id,
		Prompt: fmt.Sprintf(`De gebruiker volgt in ClusterForge les %d, %q, op het Kubernetes-cluster %s (%s), en zit vast bij de opdracht %q. ClusterForge controleert de opdracht op het cluster; wat het zag, staat bij wat_clusterforge_zag.

Geef een hint die verder helpt, zonder het antwoord voor te zeggen:
- kijk met de tools wat er nu op het cluster staat (de lessen gebruiken de namespace leren, tenzij de opdracht een andere noemt) en vergelijk dat met wat de opdracht vraagt;
- zeg wat er nog niet klopt of waar de gebruiker moet kijken, als richting of als vraag terug; geef niet het hele commando of de hele YAML;
- is de opdracht een vraag, geef dan nooit het antwoord, ook niet half: help de gebruiker het zelf te vinden in het dashboard of met kubectl;
- vriendelijk en kort: een paar zinnen.

De opdracht:
%s`, l.Number, l.Title, cl.Name, cl.Environment, st.Title, data),
	}
}
