package gitops

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// Edit is een wijziging van de gewenste staat in de webinterface, voor een
// cluster uit een template dat geen Git volgt. Ze krijgt hetzelfde plan,
// dezelfde revisies en dezelfde uitrol als een wijziging uit Git.
type Edit struct {
	// Version is de templateversie; leeg houdt de huidige.
	Version string
	// Params zijn de parameters die veranderen; de andere houden hun waarde.
	Params map[string]any
}

// EditForm is wat de webinterface toont om de spec te wijzigen.
type EditForm struct {
	Template string
	Version  string
	// Versions zijn de versies van de template in deze server.
	Versions []string
	Params   []EditParam
	// Blocked zegt waarom de spec hier niet te wijzigen is, zoals bij een
	// cluster dat Git volgt; leeg als het kan.
	Blocked string
}

// EditParam is een parameter met zijn waarde nu.
type EditParam struct {
	Param templates.Param
	// Value is nil bij een geheim of een lege optionele waarde.
	Value any
	// Locked zegt waarom de parameter niet te wijzigen is; leeg als het kan.
	Locked string
}

// EditPlan is het plan van een wijziging uit de webinterface.
type EditPlan struct {
	Plan Plan
	// Hash is de vingerafdruk van het plan. Toepassen vraagt hem terug: zo
	// gebeurt er wat de beheerder zag, of hij krijgt een nieuw plan.
	Hash string
	// Prod: het cluster is prod, dus de beheerder tikt de slug in.
	Prod bool
	// All: een eerdere revisie is niet toegepast, dus de taak past alle
	// stappen opnieuw toe.
	All bool
	// Blocked zegt waarom het plan niet toe te passen is, met een vaste code.
	BlockedCode, Blocked string
}

// Applied is de uitkomst van een wijziging: de nieuwe revisie en, als er op
// de nodes iets verandert, de taak die haar toepast.
type Applied struct {
	Revision int32
	Job      *store.Job
}

const gitManagedMsg = "dit cluster volgt Git: cluster.yaml is de bron van de specificatie. Wijzig het bestand in Git, of ontkoppel het cluster eerst"

// editable leest een cluster waarvan de spec in de webinterface te wijzigen
// is.
func (s *Service) editable(ctx context.Context, clusterID uuid.UUID) (store.Cluster, *Cluster, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil, ErrNotFound
	}
	if err != nil {
		return c, nil, err
	}
	if c.GitRepoID != nil {
		return c, nil, &ConflictError{Code: "git_managed", Msg: gitManagedMsg}
	}
	cl := s.clusterFrom(ctx, c)
	if cl.Spec == nil {
		return c, nil, &ConflictError{Code: "not_template", Msg: "dit cluster komt niet uit een template en heeft geen gewenste staat"}
	}
	return c, cl, nil
}

// EditForm geeft de parameters van een cluster om te wijzigen, met de
// waarden van nu, voor de huidige templateversie of voor version.
func (s *Service) EditForm(ctx context.Context, clusterID uuid.UUID, version string) (EditForm, error) {
	c, cl, err := s.editable(ctx, clusterID)
	var ce *ConflictError
	if errors.As(err, &ce) && ce.Code == "git_managed" {
		cl = s.clusterFrom(ctx, c)
	} else if err != nil {
		return EditForm{}, err
	}
	if cl.Spec == nil {
		return EditForm{}, &ConflictError{Code: "not_template", Msg: "dit cluster komt niet uit een template en heeft geen gewenste staat"}
	}
	old := cl.Spec
	version = cmp.Or(version, old.Template.Version)
	tpl, ok := s.reg().Get(old.Template.Name, version)
	if !ok {
		return EditForm{}, ValidationError{Field: "version", Msg: fmt.Sprintf("onbekende versie %s van %s", version, old.Template.Name)}
	}
	f := EditForm{Template: tpl.Name, Version: tpl.Version, Versions: s.reg().Versions(tpl.Name), Params: []EditParam{}}
	if ce != nil {
		f.Blocked = upperFirst(gitManagedMsg) + "."
	}
	vm := tpl.VMParams()
	for _, p := range tpl.Params {
		ep := EditParam{Param: p}
		v, had := old.Params[p.Name]
		switch {
		case p.Type == "secret":
			ep.Locked = "Een geheim: ClusterForge bewaart het versleuteld en wijzigt het hier niet."
		case !had:
			ep.Value = p.Default
		case p.Immutable:
			ep.Value, ep.Locked = v, "Ligt vast na de uitrol."
		case slices.Contains(vm, p.Name):
			ep.Value, ep.Locked = v, "Ligt vast na de uitrol: een bestaande VM wordt niet groter of kleiner gemaakt."
		default:
			ep.Value = v
		}
		f.Params = append(f.Params, ep)
	}
	return f, nil
}

// PlanEdit plant een wijziging uit de webinterface. Er verandert nog niets.
// Een fout in de invoer is een ValidationError met het veld, zoals
// params.workers.
func (s *Service) PlanEdit(ctx context.Context, clusterID uuid.UUID, e Edit) (EditPlan, error) {
	c, cl, err := s.editable(ctx, clusterID)
	if err != nil {
		return EditPlan{}, err
	}
	pd, err := s.planEdit(ctx, cl, e)
	if err != nil {
		return EditPlan{}, err
	}
	return s.editPlanOf(ctx, c, cl, pd)
}

func (s *Service) editPlanOf(ctx context.Context, c store.Cluster, cl *Cluster, pd *planned) (EditPlan, error) {
	ep := EditPlan{
		Plan: *pd.plan, Hash: planHash(pd.plan), Prod: c.Environment == store.EnvironmentProd,
		All: c.AppliedRevision < c.SpecRevision || pd.oldTpl == nil,
	}
	inv, _, err := s.dep.Nodes(ctx, c.ID)
	if err != nil {
		return EditPlan{}, err
	}
	switch m := cl.Spec.Membership(inv); {
	case len(pd.unrendered) > 0:
		ep.BlockedCode, ep.Blocked = "render_failed", strings.Join(pd.unrendered, " ")
	case len(m) > 0:
		ep.BlockedCode = "membership"
		ep.Blocked = "De nodes van het cluster kloppen niet met de specificatie (" + strings.Join(m, "; ") +
			"). Toepassen zou de lijst met peers herschrijven en een node buitensluiten."
	case slices.ContainsFunc(pd.plan.NewNodes, func(n PlanNewNode) bool { return n.Address == "" }):
		ep.BlockedCode = "dhcp_growth"
		ep.Blocked = "Nodes toevoegen kan alleen bij een cluster met vaste adressen. Met DHCP is het adres van een nieuwe node pas bekend als zijn VM draait, " +
			"en daarmee wat er op de andere nodes verandert; dat moet je zien voor je toepast."
	}
	return ep, nil
}

// ApplyEdit past een wijziging uit de webinterface toe als het plan nog
// hetzelfde is als dat met hash: een nieuwe revisie met bron ui en, als er
// op de nodes iets verandert, cluster.apply in de wachtrij. serverURL is het
// adres waarmee nieuwe nodes zich aanmelden. De bevestiging bij prod
// controleert de aanroeper.
func (s *Service) ApplyEdit(ctx context.Context, actor events.Actor, clusterID uuid.UUID, e Edit, hash, serverURL string) (Applied, error) {
	c, cl, err := s.editable(ctx, clusterID)
	if err != nil {
		return Applied{}, err
	}
	pd, err := s.planEdit(ctx, cl, e)
	if err != nil {
		return Applied{}, err
	}
	ep, err := s.editPlanOf(ctx, c, cl, pd)
	switch {
	case err != nil:
		return Applied{}, err
	case ep.Hash != hash:
		return Applied{}, &ConflictError{Code: "plan_changed",
			Msg: "het plan is veranderd sinds je het bekeek, bijvoorbeeld door een andere wijziging of een VIP dat verhuisde; bekijk het nieuwe plan"}
	case ep.Blocked != "":
		return Applied{}, &ConflictError{Code: ep.BlockedCode, Msg: ep.Blocked}
	}
	serverURL = strings.TrimSpace(serverURL)
	if len(pd.plan.NewNodes) > 0 && serverURL == "" {
		return Applied{}, ValidationError{Field: "server_url", Msg: "vul het adres van ClusterForge in; daar melden de nieuwe nodes zich aan"}
	}
	rp, err := s.rolloutPlan(ctx, c, *pd.plan, pd, nil, "", serverURL)
	if err != nil {
		return Applied{}, err
	}
	var by *uuid.UUID
	if uid, err := uuid.Parse(actor.ID); err == nil && actor.Type == store.ActorTypeUser {
		by = &uid
	}
	var out Applied
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockCluster(ctx, c.ID)
		if err != nil {
			return err
		}
		if cur.SpecRevision != c.SpecRevision || cur.GitRepoID != nil {
			return &ConflictError{Code: "plan_changed", Msg: "het cluster veranderde intussen; bekijk het nieuwe plan"}
		}
		src := specSource{Source: "ui", By: by}
		if out.Revision, err = s.writeSpec(ctx, q, actor, c.ID, c.Name, pd.next, pd.newTpl, rp, src); err != nil {
			return err
		}
		if err := s.specChanged(ctx, q, actor, c.ID, c.Name, out.Revision, c.SpecRevision, pd.next, src); err != nil {
			return err
		}
		if rp == nil {
			return q.SetAppliedRevision(ctx, store.SetAppliedRevisionParams{ID: c.ID, Revision: out.Revision})
		}
		rp.Revision = int(out.Revision)
		title := fmt.Sprintf("Revisie %d toepassen in %s", out.Revision, c.Name)
		if n := len(rp.NewNodes); n > 0 {
			title = fmt.Sprintf("%s toevoegen aan %s", count(n, "node", "nodes"), c.Name)
		}
		j, err := s.ro.EnqueueTx(ctx, q, actor, title, *rp)
		if err := rolloutError(err); err != nil {
			return err
		}
		out.Job = &j
		return nil
	})
	if err != nil {
		return Applied{}, err
	}
	if out.Job != nil {
		s.ro.Kick()
	}
	return out, nil
}

// planEdit controleert de wijziging en plant haar zoals een bestand uit Git
// met dezelfde inhoud.
func (s *Service) planEdit(ctx context.Context, cl *Cluster, e Edit) (*planned, error) {
	ch, err := s.checkEdit(cl, e)
	if err != nil {
		return nil, err
	}
	if ch.Same(cl) {
		return nil, &ConflictError{Code: "no_change", Msg: "er verandert niets: de specificatie is al zo"}
	}
	pd, errs, err := s.planChange(ctx, cl, ch, "")
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, ValidationError{Field: errs[0].Field, Msg: errs[0].Message}
	}
	return pd, nil
}

// checkEdit zet de wijziging om in een gecontroleerd bestand: de huidige
// parameters met de nieuwe waarden erover, genormaliseerd zoals in een spec.
func (s *Service) checkEdit(cl *Cluster, e Edit) (*Checked, error) {
	old := cl.Spec
	version := cmp.Or(e.Version, old.Template.Version)
	tpl, ok := s.reg().Get(old.Template.Name, version)
	if !ok {
		return nil, ValidationError{Field: "version", Msg: fmt.Sprintf("onbekende versie %s; deze server kent %s",
			version, strings.Join(s.reg().Versions(old.Template.Name), ", "))}
	}
	params := map[string]any{}
	for _, p := range tpl.Params {
		if p.Type == "secret" {
			continue
		}
		if v, had := old.Params[p.Name]; had {
			params[p.Name] = v
			continue
		}
		// Nieuw in deze versie: de standaardwaarde, tenzij de wijziging er
		// een geeft.
		if _, given := e.Params[p.Name]; given {
			continue
		}
		switch {
		case p.Default != nil:
			v, err := tpl.Normalize(p.Name, p.Default)
			if err != nil {
				return nil, err
			}
			params[p.Name] = v
		case p.Optional:
			params[p.Name] = nil
		default:
			return nil, ValidationError{Field: "params." + p.Name, Msg: fmt.Sprintf("%s is nieuw in versie %s; kies een waarde", p.Label, tpl.Version)}
		}
	}
	vm := tpl.VMParams()
	for _, name := range slices.Sorted(maps.Keys(e.Params)) {
		field := "params." + name
		p, ok := tpl.Param(name)
		switch {
		case !ok:
			return nil, ValidationError{Field: field, Msg: fmt.Sprintf("onbekende parameter voor %s %s", tpl.Name, tpl.Version)}
		case p.Type == "secret":
			return nil, ValidationError{Field: field, Msg: "een geheim wijzig je hier niet; ClusterForge maakt het zelf aan en bewaart het versleuteld"}
		}
		var v any
		raw := e.Params[name]
		if strings.TrimSpace(text(raw)) == "" {
			if !p.Optional {
				return nil, ValidationError{Field: field, Msg: p.Label + " mag niet leeg zijn"}
			}
		} else {
			n, err := tpl.Normalize(name, raw)
			if err != nil {
				return nil, ValidationError{Field: field, Msg: p.Label + ": " + err.Error()}
			}
			v = n
		}
		if cur, had := old.Params[name]; had && text(v) != text(cur) {
			switch {
			case p.Immutable:
				return nil, ValidationError{Field: field, Msg: fmt.Sprintf("%s ligt vast na de uitrol (nu %s)", p.Label, orNone(text(cur)))}
			case slices.Contains(vm, name):
				return nil, ValidationError{Field: field, Msg: fmt.Sprintf("%s ligt vast na de uitrol (nu %s): een bestaande VM wordt niet groter of kleiner gemaakt",
					p.Label, orNone(text(cur)))}
			}
		}
		params[name] = v
	}
	if err := fewerNodes(cl, tpl, params); err != nil {
		return nil, err
	}
	lines := map[string]int{}
	for name := range params {
		lines["params."+name] = 1
	}
	f := &File{
		Format: Format, Template: FileTemplate{Name: tpl.Name, Version: tpl.Version}, Params: params, lines: lines,
		Cluster: FileCluster{Name: cl.Name, Slug: cl.Slug, Environment: cl.Environment, Description: cl.Description, Tags: cl.Tags},
	}
	return &Checked{File: f, Template: tpl, Params: params, Metadata: cl.metadata()}, nil
}

// fewerNodes weigert minder nodes dan de spec heeft. Een worker van een
// Kubernetes-cluster haal je weg op het tabblad Nodes; die taak maakt hem
// eerst leeg.
func fewerNodes(cl *Cluster, tpl *templates.Template, params map[string]any) error {
	values, err := tpl.MaskedValues(params)
	if err != nil {
		var fe templates.FieldError
		if errors.As(err, &fe) {
			return ValidationError{Field: "params." + fe.Field, Msg: fe.Msg}
		}
		return ValidationError{Field: "params", Msg: err.Error()}
	}
	counts, err := tpl.Counts(templates.Context{Params: values, Cluster: cl.Spec.Cluster})
	if err != nil {
		return ValidationError{Field: "params", Msg: err.Error()}
	}
	for i, r := range tpl.Roles {
		have := 0
		for _, n := range cl.Spec.Nodes {
			if n.Role == r.Name {
				have++
			}
		}
		if counts[i] >= have {
			continue
		}
		field := "params"
		if p := tpl.CountParam(r.Name); p != "" {
			field += "." + p
		}
		msg := fmt.Sprintf("het cluster heeft nu %d nodes in rol %s; minder gaat niet via de specificatie, maar door een node uit dienst te zetten en uit het cluster te halen",
			have, r.Name)
		if tpl.ClusterType == "kubernetes" {
			msg = fmt.Sprintf("het cluster heeft nu %s; een worker haal je weg op het tabblad Nodes van Kubernetes, zodat hij eerst leeg gaat", count(have, r.Name, r.Name+"s"))
			if r.Name == "server" {
				msg = fmt.Sprintf("het cluster heeft nu %s; minder servers kan niet, want etcd moet zijn meerderheid houden", count(have, "server", "servers"))
			}
		}
		return ValidationError{Field: field, Msg: msg}
	}
	return nil
}

// planHash is de vingerafdruk van wat een plan op de nodes doet. De
// waarschuwingen tellen niet mee: die gaan ook over het vrije geheugen in
// Proxmox, dat elke minuut anders is.
func planHash(p *Plan) string {
	b, _ := json.Marshal(struct {
		Template  PlanTemplate
		Params    []FieldChange
		NewNodes  []PlanNewNode
		Nodes     []PlanNode
		Unchanged []string
	}{p.Template, p.Params, p.NewNodes, p.Nodes, p.Unchanged})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
