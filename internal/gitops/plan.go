package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/rollout"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Plan is wat een wijziging zou doen. Het staat in git_changes.plan en
// bevat geen geheimen: die zijn met een plaatshouder gerenderd.
type Plan struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	// BaseRevision is de spec-revisie waartegen gepland is; 0 bij een nieuw
	// cluster.
	BaseRevision int           `json:"base_revision"`
	Template     PlanTemplate  `json:"template"`
	Metadata     []FieldChange `json:"metadata"`
	Params       []FieldChange `json:"params"`
	// NewNodes zijn de nodes die erbij komen, met hun geplande adres en
	// VM-vorm.
	NewNodes []PlanNewNode `json:"new_nodes"`
	// Nodes zijn de nodes met een wijziging, in de volgorde van het
	// toepassen: de bestaande nodes met de VIP-eigenaar als laatste, en
	// daarna de nieuwe.
	Nodes []PlanNode `json:"nodes"`
	// Unchanged zijn de nodes waar niets verandert.
	Unchanged []string `json:"unchanged"`
	Warnings  []string `json:"warnings"`
	// NoSteps: geen gerenderde stap verandert, zoals bij alleen andere tags.
	NoSteps bool `json:"no_steps"`
	// Voor een nieuw cluster.
	VIP     string `json:"vip,omitempty"`
	VRID    int    `json:"vrid,omitempty"`
	Proxmox string `json:"proxmox,omitempty"`
}

type PlanTemplate struct {
	Name string `json:"name"`
	// From is leeg bij een nieuw cluster.
	From string `json:"from"`
	To   string `json:"to"`
}

type FieldChange struct {
	Field string `json:"field"`
	Label string `json:"label"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type PlanNewNode struct {
	Hostname string            `json:"hostname"`
	Role     string            `json:"role"`
	Address  string            `json:"address"`
	Prefix   int               `json:"prefix"`
	Host     string            `json:"host"`
	VM       templates.VMShape `json:"vm"`
}

type PlanNode struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	// Role is de rol uit de template, zoals server of worker.
	Role  string       `json:"role,omitempty"`
	New   bool         `json:"new"`
	VIPs  []string     `json:"vips"`
	Steps []StepChange `json:"steps"`
}

// StepChange is één stap die verandert.
type StepChange struct {
	// Step is de stap zoals drift hem noemt, zoals file:/etc/nginx/nginx.conf;
	// IDs zijn de afwijkingen die hij dekt (per pakket één).
	Step  string   `json:"step"`
	IDs   []string `json:"ids"`
	Title string   `json:"title"`
	// Change is added, changed of removed.
	Change  string `json:"change"`
	Summary string `json:"summary"`
	Diff    string `json:"diff,omitempty"`
}

// planUpdate plant een wijziging van een bestaand cluster: het rendert de
// huidige revisie met haar eigen templateversie en de nieuwe met de hare,
// node voor node, met plaatshouders voor de geheimen.
func (s *Service) planUpdate(ctx context.Context, c *Cluster, ch *Checked, commit string) (*Plan, deploy.Spec, []FieldError, error) {
	pd, errs, err := s.planChange(ctx, c, ch, commit)
	if pd == nil {
		return nil, deploy.Spec{}, errs, err
	}
	return pd.plan, pd.next, nil, nil
}

// planned is een plan met wat het toepassen ervan nodig heeft.
type planned struct {
	plan  *Plan
	next  deploy.Spec
	diffs map[uuid.UUID]nodeDiff
	// isNew zegt of een node uit next erbij komt; zijn id is dan nog een
	// plan-id.
	isNew  func(uuid.UUID) bool
	newTpl *templates.Template
	// oldTpl is nil als de huidige templateversie niet in deze server zit.
	oldTpl *templates.Template
	// unrendered zegt welke nodes niet te renderen waren; zo'n plan is niet
	// toe te passen.
	unrendered []string
}

// planChange is planUpdate met de diffs per node erbij. commit is leeg bij
// een wijziging uit de webinterface.
func (s *Service) planChange(ctx context.Context, c *Cluster, ch *Checked, commit string) (*planned, []FieldError, error) {
	old := *c.Spec
	newTpl := ch.Template
	p := &Plan{
		Kind: "update", BaseRevision: c.Revision, Metadata: []FieldChange{}, Params: []FieldChange{},
		Template: PlanTemplate{Name: newTpl.Name, From: old.Template.Version, To: newTpl.Version},
		NewNodes: []PlanNewNode{}, Nodes: []PlanNode{}, Unchanged: []string{}, Warnings: []string{},
	}
	inv, states, err := s.dep.Nodes(ctx, c.ID)
	if err != nil {
		return nil, nil, err
	}
	next := old
	next.Template = deploy.SpecTemplate{Name: newTpl.Name, Version: newTpl.Version}
	next.Cluster = templates.ClusterInfo{Name: ch.Metadata.Name, Slug: c.Slug, Environment: ch.Metadata.Environment}
	next.Params = maps.Clone(ch.Params)
	next.Secrets = newTpl.Secrets()
	if next.Secrets == nil {
		next.Secrets = []string{}
	}
	next.Nodes = slices.Clone(old.Nodes)
	for _, name := range next.Secrets {
		if !slices.Contains(old.Secrets, name) {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s %s heeft een nieuw geheim %s; ClusterForge maakt het bij het toepassen aan.", newTpl.Name, newTpl.Version, name))
		}
	}
	p.Warnings = append(p.Warnings, old.Membership(inv)...)

	values, err := newTpl.MaskedValues(next.Params)
	if err != nil {
		return nil, []FieldError{{Field: "params", Line: ch.File.Line("params"), Message: err.Error()}}, nil
	}
	growth, err := s.dep.PlanGrowth(ctx, next, newTpl, values, states)
	if err != nil {
		var ve deploy.ValidationError
		if errors.As(err, &ve) {
			return nil, []FieldError{fileError(ch.File, ve)}, nil
		}
		return nil, nil, err
	}
	all := maps.Clone(states)
	maps.Copy(all, growth.States)
	next.Nodes = append(next.Nodes, growth.Nodes...)
	p.Warnings = append(p.Warnings, growth.Warnings...)
	for _, n := range growth.Nodes {
		p.NewNodes = append(p.NewNodes, PlanNewNode{Hostname: n.Hostname, Role: n.Role, Address: n.Address, Prefix: n.Prefix, VM: n.VM})
	}

	p.Metadata = metadataChanges(c.metadata(), ch.Metadata)
	oldTpl, ok := s.reg().Get(old.Template.Name, old.Template.Version)
	if !ok {
		oldTpl = nil
	}
	if old.Template.Version != newTpl.Version {
		p.Params = append(p.Params, FieldChange{Field: "template.version", Label: "Templateversie", From: old.Template.Version, To: newTpl.Version})
		if oldTpl != nil && newTpl.KubeVersion() != "" {
			if err := templates.KubeStep(oldTpl, newTpl); err != nil {
				return nil, []FieldError{{Field: "template.version", Line: ch.File.Line("template.version"), Message: err.Error()}}, nil
			}
			if from, to := oldTpl.KubeVersion(), newTpl.KubeVersion(); from != to {
				p.Params = append(p.Params, FieldChange{Field: "kubernetes.version", Label: "Kubernetes (k3s)", From: from, To: to})
				p.Warnings = append(p.Warnings, kubeWarnings(old, to)...)
			}
		}
	}
	p.Params = append(p.Params, paramChanges(newTpl, old.Params, next.Params)...)
	if old.Cluster.Environment != next.Cluster.Environment && next.Cluster.Environment == "prod" {
		p.Warnings = append(p.Warnings, "Het cluster gaat naar prod: daarna vraagt elke ingreep de bevestiging bij prod.")
	}

	if oldTpl == nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s %s zit niet in deze server; de huidige stappen zijn niet te renderen, dus het plan toont geen diffs.",
			old.Template.Name, old.Template.Version))
	}
	owned, err := s.ownedVIPs(ctx, c.ID)
	if err != nil {
		return nil, nil, err
	}
	isNew := func(id uuid.UUID) bool {
		return slices.ContainsFunc(growth.Nodes, func(g deploy.SpecNode) bool { return g.NodeID == id })
	}
	diffs, warnings := nodeDiffs(oldTpl, newTpl, old, next, states, all, isNew, c.Revision, commit)
	p.Warnings = append(p.Warnings, warnings...)
	byNode := map[uuid.UUID]PlanNode{}
	var order []uuid.UUID
	for _, n := range next.Nodes {
		d, rendered := diffs[n.NodeID]
		if !rendered {
			continue
		}
		if len(d.Steps) == 0 {
			p.Unchanged = append(p.Unchanged, n.Hostname)
			continue
		}
		pn := PlanNode{NodeID: n.NodeID, Hostname: n.Hostname, Role: n.Role, New: isNew(n.NodeID), VIPs: owned[n.NodeID], Steps: d.Steps}
		if pn.VIPs == nil {
			pn.VIPs = []string{}
		}
		if pn.New {
			pn.NodeID = uuid.Nil
		}
		byNode[n.NodeID] = pn
		order = append(order, n.NodeID)
	}
	p.Nodes = orderNodes(order, byNode, newTpl.ClusterType == "kubernetes")
	for _, n := range p.Nodes {
		for _, st := range n.Steps {
			if st.Change == "removed" {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s: %s staat niet meer in de template; ClusterForge laat wat er op de node staat ongemoeid.", n.Hostname, st.Title))
			}
		}
	}
	p.NoSteps = len(p.Nodes) == 0
	p.Summary = summary(p)
	return &planned{plan: p, next: next, diffs: diffs, isNew: isNew, newTpl: newTpl, oldTpl: oldTpl, unrendered: warnings}, nil, nil
}

// kubeWarnings zeggen wat een nieuwe k3s met de nodes doet.
func kubeWarnings(spec deploy.Spec, to string) []string {
	out := []string{fmt.Sprintf("Kubernetes gaat naar %s, node voor node: eerst de servers, dan de workers. Elke node gaat eerst leeg en krijgt pas weer pods "+
		"als hij Ready is met de nieuwe versie. Komt een node niet terug, dan stopt de uitrol en blijft die node op cordon.", templates.ShortKube(to))}
	var servers []string
	for _, n := range spec.Nodes {
		if n.Role == rollout.KubeServerRole {
			servers = append(servers, n.Hostname)
		}
	}
	if len(servers) == 1 {
		out = append(out, fmt.Sprintf("Er is maar één server, %s: terwijl k3s daar herstart, antwoordt de API van het cluster even niet en is het VIP even weg. "+
			"Apps die al draaien, blijven draaien.", servers[0]))
	}
	return out
}

// nodeDiff is wat er op één node verandert, met de nieuwe stappen.
type nodeDiff struct {
	Steps []StepChange
	After []templates.Step
}

// nodeDiffs rendert elke node van next met de stappen ervoor (uit old) en
// erna, met plaatshouders voor de geheimen, en geeft per node wat er
// verandert. Zonder oldTpl, of voor een node die niet te renderen is, staat
// de node er niet in; dat laatste komt in de waarschuwingen. Een nieuwe node
// heeft geen stappen ervoor.
func nodeDiffs(oldTpl, newTpl *templates.Template, old, next deploy.Spec, states, all map[uuid.UUID]deploy.NodeState,
	isNew func(uuid.UUID) bool, revision int, commit string) (map[uuid.UUID]nodeDiff, []string) {
	out := map[uuid.UUID]nodeDiff{}
	var warnings []string
	for _, n := range next.Nodes {
		var before []templates.Step
		if !isNew(n.NodeID) {
			if oldTpl == nil || n.NodeID == uuid.Nil {
				continue
			}
			var err error
			before, err = deploy.RenderMasked(oldTpl, old, states, n.NodeID)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("De huidige stappen van %s zijn niet te renderen: %v.", n.Hostname, err))
				continue
			}
		}
		after, err := deploy.RenderMasked(newTpl, next, all, n.NodeID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("De nieuwe stappen van %s zijn niet te renderen: %v.", n.Hostname, err))
			continue
		}
		out[n.NodeID] = nodeDiff{Steps: compareSteps(n.Hostname, before, after, revision, commit), After: after}
	}
	return out, warnings
}

// fileError zet een fout van de uitrolcontroles om naar het veld in het
// bestand.
func fileError(f *File, ve deploy.ValidationError) FieldError {
	field := ve.Field
	switch field {
	case "target.proxmox_id":
		field = "target.proxmox"
	case "template":
		field = "template.version"
	case "", "server_url":
		field = "target"
	}
	return FieldError{Field: field, Line: f.Line(field), Message: ve.Msg}
}

func (s *Service) ownedVIPs(ctx context.Context, clusterID uuid.UUID) (map[uuid.UUID][]string, error) {
	vips, err := s.q.ListVIPsByCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]string{}
	for _, v := range vips {
		if v.Vip.OwnerNodeID != nil {
			out[*v.Vip.OwnerNodeID] = append(out[*v.Vip.OwnerNodeID], v.Vip.Address.String())
		}
	}
	return out, nil
}

// orderNodes zet de nodes in de volgorde van het toepassen: de bestaande
// zonder VIP, dan de eigenaars, en de nieuwe nodes als laatste, elk in de
// volgorde van de spec. Zo kennen de bestaande nodes een nieuwe peer al
// voor hij start; andersom zou de nieuwe node de VIP-eigenaar niet horen
// en het VIP ook nemen. Bij Kubernetes gaan de bestaande servers voor de
// bestaande workers, want een kubelet mag niet nieuwer zijn dan de
// API-server.
func orderNodes(order []uuid.UUID, nodes map[uuid.UUID]PlanNode, kubernetes bool) []PlanNode {
	server := func(n PlanNode) bool { return kubernetes && n.Role == rollout.KubeServerRole }
	out := []PlanNode{}
	for _, group := range []func(PlanNode) bool{
		func(n PlanNode) bool { return !n.New && server(n) && len(n.VIPs) == 0 },
		func(n PlanNode) bool { return !n.New && server(n) && len(n.VIPs) > 0 },
		func(n PlanNode) bool { return !n.New && !server(n) && len(n.VIPs) == 0 },
		func(n PlanNode) bool { return !n.New && !server(n) && len(n.VIPs) > 0 },
		func(n PlanNode) bool { return n.New },
	} {
		for _, id := range order {
			if n := nodes[id]; group(n) {
				out = append(out, n)
			}
		}
	}
	return out
}

// stepKey noemt een stap zoals drift hem noemt; een pakketstap met al zijn
// namen.
func stepKey(s templates.Step) string {
	if s.Package != nil {
		return "package:" + strings.Join(s.Package.Names, ",")
	}
	return drift.StepIDs(s)[0]
}

// compareSteps vergelijkt de stappen van een node voor en na, op hun
// sleutel. Een stap die twee keer voorkomt, krijgt een volgnummer.
func compareSteps(host string, before, after []templates.Step, revision int, commit string) []StepChange {
	keyed := func(steps []templates.Step) ([]string, map[string]templates.Step) {
		var keys []string
		m := map[string]templates.Step{}
		for _, st := range steps {
			k := stepKey(st)
			for n := 2; ; n++ {
				if _, dup := m[k]; !dup {
					break
				}
				k = fmt.Sprintf("%s#%d", stepKey(st), n)
			}
			keys = append(keys, k)
			m[k] = st
		}
		return keys, m
	}
	oldKeys, oldSteps := keyed(before)
	newKeys, newSteps := keyed(after)
	// Een commando dat een ander bestand maakt, heeft een andere sleutel,
	// maar is hetzelfde commando: dat is een wijziging, geen nieuwe stap en
	// een die weg is.
	moved, gone := map[string]string{}, map[string]bool{}
	for _, nk := range newKeys {
		ns := newSteps[nk]
		if _, existed := oldSteps[nk]; existed || ns.Command == nil {
			continue
		}
		for _, ok := range oldKeys {
			os := oldSteps[ok]
			_, still := newSteps[ok]
			if !still && !gone[ok] && os.Command != nil && os.Command.Run == ns.Command.Run {
				moved[nk], gone[ok] = ok, true
				break
			}
		}
	}
	from := fmt.Sprintf("%s (revisie %d)", host, revision)
	to := fmt.Sprintf("%s (commit %s)", host, short(commit))
	if commit == "" {
		to = fmt.Sprintf("%s (revisie %d)", host, revision+1)
	}
	out := []StepChange{}
	for _, k := range newKeys {
		ns := newSteps[k]
		sc := StepChange{Step: strings.SplitN(k, "#", 2)[0], IDs: drift.StepIDs(ns), Title: titleOf(ns)}
		os, existed := oldSteps[k]
		if ok, isMoved := moved[k]; isMoved {
			os, existed = oldSteps[ok], true
		}
		if !existed {
			sc.Change, sc.Summary = "added", "nieuwe stap"
			if ns.File != nil {
				sc.Diff, _ = Diff("", ns.File.Content, "/dev/null", to)
				sc.Summary = fmt.Sprintf("nieuw bestand, %s", lineCount(ns.File.Content))
			}
			out = append(out, sc)
			continue
		}
		if sameStep(os, ns) {
			continue
		}
		sc.Change = "changed"
		sc.Summary, sc.Diff = stepDiff(os, ns, from, to)
		out = append(out, sc)
	}
	for _, k := range oldKeys {
		if _, still := newSteps[k]; !still && !gone[k] {
			os := oldSteps[k]
			out = append(out, StepChange{Step: strings.SplitN(k, "#", 2)[0], IDs: drift.StepIDs(os), Title: titleOf(os),
				Change: "removed", Summary: "staat niet meer in de template"})
		}
	}
	return out
}

func titleOf(s templates.Step) string {
	switch {
	case s.Title == "":
		return stepKey(s)
	case s.Title == "commando" && s.Command != nil:
		return "Commando " + s.Command.Run
	}
	return strings.ToUpper(s.Title[:1]) + s.Title[1:]
}

func sameStep(a, b templates.Step) bool {
	ja, _ := json.Marshal(struct {
		S protocol.Step
		N []protocol.ServiceStep
	}{a.Step, a.Notify})
	jb, _ := json.Marshal(struct {
		S protocol.Step
		N []protocol.ServiceStep
	}{b.Step, b.Notify})
	return string(ja) == string(jb)
}

// stepDiff zegt in een paar woorden wat er aan een stap verandert, met een
// unified diff voor de inhoud van een bestand.
func stepDiff(a, b templates.Step, from, to string) (string, string) {
	var parts []string
	diff := ""
	switch {
	case a.File != nil && b.File != nil:
		if a.File.Content != b.File.Content {
			d, n := Diff(a.File.Content, b.File.Content, from, to)
			diff = d
			switch n {
			case -1:
				parts = append(parts, "inhoud anders (te groot om te vergelijken)")
			case 1:
				parts = append(parts, "1 regel anders")
			default:
				parts = append(parts, fmt.Sprintf("%d regels anders", n))
			}
		}
		parts = append(parts, perm(a.File.Mode, b.File.Mode, a.File.Owner, b.File.Owner, a.File.Group, b.File.Group)...)
	case a.Directory != nil && b.Directory != nil:
		parts = append(parts, perm(a.Directory.Mode, b.Directory.Mode, a.Directory.Owner, b.Directory.Owner, a.Directory.Group, b.Directory.Group)...)
	case a.Package != nil && b.Package != nil:
		if a.Package.State != b.Package.State {
			parts = append(parts, fmt.Sprintf("van %s naar %s", or(a.Package.State, "present"), or(b.Package.State, "present")))
		}
	case a.Service != nil && b.Service != nil:
		if fmt.Sprint(a.Service.Enabled != nil && *a.Service.Enabled) != fmt.Sprint(b.Service.Enabled != nil && *b.Service.Enabled) {
			parts = append(parts, "enabled anders")
		}
		if a.Service.State != b.Service.State {
			parts = append(parts, fmt.Sprintf("toestand van %s naar %s", or(a.Service.State, "ongemoeid"), or(b.Service.State, "ongemoeid")))
		}
	case a.Command != nil && b.Command != nil:
		if a.Command.Run != b.Command.Run {
			parts = append(parts, "ander commando")
		}
		if a.Command.Creates != b.Command.Creates {
			parts = append(parts, "draait opnieuw en maakt nu "+b.Command.Creates)
		}
	}
	if len(a.Notify) != len(b.Notify) || !slices.Equal(notifyNames(a), notifyNames(b)) {
		parts = append(parts, notifyChange(b))
	}
	if len(parts) == 0 {
		parts = append(parts, "anders")
	}
	return strings.Join(parts, ", "), diff
}

// notifyChange zegt welke services een stap na een wijziging nu raakt.
func notifyChange(s templates.Step) string {
	if len(s.Notify) == 0 {
		return "raakt na een wijziging geen services meer"
	}
	verbs := map[string]string{"restarted": "herstarten", "reloaded": "herladen", "started": "starten", "stopped": "stoppen"}
	var out []string
	for _, n := range s.Notify {
		out = append(out, strings.TrimSpace(n.Name+" "+verbs[n.State]))
	}
	return "daarna " + strings.Join(out, ", ")
}

func notifyNames(s templates.Step) []string {
	var out []string
	for _, n := range s.Notify {
		out = append(out, n.Name+":"+n.State)
	}
	return out
}

func perm(m1, m2, o1, o2, g1, g2 string) []string {
	var out []string
	if or(m1, "0644") != or(m2, "0644") {
		out = append(out, fmt.Sprintf("rechten %s naar %s", or(m1, "0644"), or(m2, "0644")))
	}
	if or(o1, "root") != or(o2, "root") || or(g1, "root") != or(g2, "root") {
		out = append(out, fmt.Sprintf("eigenaar %s:%s naar %s:%s", or(o1, "root"), or(g1, "root"), or(o2, "root"), or(g2, "root")))
	}
	return out
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func lineCount(s string) string {
	n := len(lines(s))
	if n == 1 {
		return "1 regel"
	}
	return fmt.Sprintf("%d regels", n)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

var metaLabels = []struct{ field, label string }{
	{"name", "Naam"}, {"description", "Beschrijving"}, {"environment", "Omgeving"}, {"tags", "Tags"},
}

func metadataChanges(old, cur Metadata) []FieldChange {
	out := []FieldChange{}
	vals := func(m Metadata) map[string]string {
		return map[string]string{"name": m.Name, "description": m.Description, "environment": m.Environment, "tags": strings.Join(m.Tags, ", ")}
	}
	a, b := vals(old), vals(cur)
	for _, f := range metaLabels {
		if a[f.field] != b[f.field] {
			out = append(out, FieldChange{Field: "cluster." + f.field, Label: f.label, From: a[f.field], To: b[f.field]})
		}
	}
	return out
}

func paramChanges(tpl *templates.Template, old, cur map[string]any) []FieldChange {
	out := []FieldChange{}
	names := map[string]bool{}
	for k := range old {
		names[k] = true
	}
	for k := range cur {
		names[k] = true
	}
	label := func(name string) string {
		if p, ok := tpl.Param(name); ok {
			return p.Label
		}
		return name
	}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if a, b := text(old[name]), text(cur[name]); a != b {
			out = append(out, FieldChange{Field: "params." + name, Label: label(name), From: a, To: b})
		}
	}
	return out
}

// summary is de samenvatting in de lijst Wijzigingen, zoals "node_count van
// 2 naar 3; keepalived.conf op 2 nodes, 1 nieuwe node".
func summary(p *Plan) string {
	var fields []string
	for _, f := range append(slices.Clone(p.Metadata), p.Params...) {
		name := strings.TrimPrefix(strings.TrimPrefix(f.Field, "cluster."), "params.")
		switch f.Field {
		case "template.version":
			name = "templateversie"
		case "kubernetes.version":
			name = "k3s"
		}
		fields = append(fields, fmt.Sprintf("%s van %s naar %s", name, orNone(f.From), orNone(f.To)))
	}
	if len(fields) > 3 {
		fields = append(fields[:2], fmt.Sprintf("en %d meer", len(fields)-2))
	}
	var parts []string
	if len(fields) > 0 {
		parts = append(parts, strings.Join(fields, ", "))
	}
	perStep := map[string]int{}
	var stepOrder []string
	for _, n := range p.Nodes {
		if n.New {
			continue
		}
		for _, st := range n.Steps {
			name := st.Title
			if strings.HasPrefix(st.Step, "file:") {
				name = path.Base(strings.TrimPrefix(st.Step, "file:"))
			}
			if run, isCommand := strings.CutPrefix(name, "Commando "); isCommand && strings.HasPrefix(st.Step, "command:") && strings.TrimSpace(run) != "" {
				name = "commando " + path.Base(strings.Fields(run)[0])
			}
			if perStep[name] == 0 {
				stepOrder = append(stepOrder, name)
			}
			perStep[name]++
		}
	}
	var steps []string
	for _, name := range stepOrder {
		steps = append(steps, fmt.Sprintf("%s op %s", name, count(perStep[name], "node", "nodes")))
	}
	if len(steps) > 3 {
		steps = append(steps[:2], fmt.Sprintf("en %d andere stappen", len(steps)-2))
	}
	if n := len(p.NewNodes); n > 0 {
		steps = append(steps, count(n, "nieuwe node", "nieuwe nodes"))
	}
	switch {
	case len(steps) > 0:
		parts = append(parts, strings.Join(steps, ", "))
	case p.Kind == "update":
		parts = append(parts, "geen stap op de nodes verandert")
	}
	return strings.Join(parts, "; ")
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// planCreate plant een nieuw cluster met de controles van de uitrol: de
// Proxmox-koppeling op naam, vrije adressen en VIP, en genoeg capaciteit.
// Het geeft ook het doel zoals de uitrol het krijgt.
func (s *Service) planCreate(ctx context.Context, ch *Checked) (*Plan, deploy.Target, []FieldError, error) {
	f := ch.File
	t := f.Target
	conns, err := s.q.ListProxmoxConnections(ctx)
	if err != nil {
		return nil, deploy.Target{}, nil, err
	}
	i := slices.IndexFunc(conns, func(c store.ProxmoxConnection) bool { return c.Name == t.Proxmox })
	if i < 0 {
		var names []string
		for _, c := range conns {
			names = append(names, c.Name)
		}
		msg := "er is geen Proxmox-koppeling " + quote(t.Proxmox)
		if len(names) > 0 {
			msg += "; er zijn: " + strings.Join(names, ", ")
		}
		return nil, deploy.Target{}, []FieldError{{Field: "target.proxmox", Line: f.Line("target.proxmox"), Message: msg}}, nil
	}
	req := deploy.Request{
		Template: ch.Template.Name,
		Cluster: deploy.ClusterInput{Name: ch.Metadata.Name, Slug: f.Cluster.Slug, Environment: ch.Metadata.Environment,
			Description: ch.Metadata.Description},
		Params: maps.Clone(ch.Params),
		Target: deploy.Target{
			ProxmoxID: conns[i].ID, ImageVMID: t.ImageVMID, Storage: t.Storage, Bridge: t.Bridge, VLAN: t.VLAN,
			Network: "dhcp", FirstIP: t.FirstIP, Gateway: t.Gateway, DNS: t.DNS, SSHKeys: strings.Join(t.SSHKeys, "\n"),
		},
	}
	if t.FirstIP != "" {
		req.Target.Network = "static"
	}
	plan, err := s.dep.CheckNew(ctx, req, ch.Template.Version)
	if err != nil {
		var ve deploy.ValidationError
		if errors.As(err, &ve) {
			return nil, deploy.Target{}, []FieldError{fileError(f, ve)}, nil
		}
		return nil, deploy.Target{}, nil, err
	}
	p := &Plan{
		Kind: "create", Template: PlanTemplate{Name: ch.Template.Name, To: ch.Template.Version},
		Metadata: metadataChanges(Metadata{}, ch.Metadata), Params: paramChanges(ch.Template, nil, ch.Params),
		NewNodes: []PlanNewNode{}, Nodes: []PlanNode{}, Unchanged: []string{}, Warnings: []string{},
		VIP: plan.VIP, VRID: plan.VRID, Proxmox: conns[i].Name,
	}
	for _, n := range plan.Nodes {
		p.NewNodes = append(p.NewNodes, PlanNewNode{Hostname: n.Hostname, Role: n.Role, Address: n.Address, Prefix: n.Prefix, Host: n.Host, VM: n.VM})
	}
	if _, given := ch.Params["vrid"]; given && ch.Params["vrid"] == nil && plan.VRID > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Zonder vrid kiest ClusterForge VRRP-id %d; zet hem daarna in het bestand, want na de uitrol ligt hij vast.", plan.VRID))
	}
	p.Summary = fmt.Sprintf("nieuw cluster met %s uit %s %s", count(len(p.NewNodes), "node", "nodes"), ch.Template.Name, ch.Template.Version)
	return p, req.Target, nil, nil
}

func quote(s string) string { return "“" + s + "”" }
