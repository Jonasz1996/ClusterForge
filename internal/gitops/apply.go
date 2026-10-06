package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/rollout"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// Decision is de uitkomst van een goedkeuring: de wijziging en, als er op
// de nodes iets verandert, de taak die haar toepast.
type Decision struct {
	Change store.GitChange
	Job    *store.Job
}

var statusText = map[string]string{
	"pending": "wacht op goedkeuring", "applying": "goedgekeurd en wordt toegepast", "applied": "toegepast", "failed": "mislukt",
	"rejected": "afgewezen", "superseded": "vervangen door een nieuwere",
}

// Blocked zegt waarom een wachtende wijziging (nog) niet goed te keuren
// is, met een vaste code, of twee lege teksten als het kan. serverURL is het
// adres van ClusterForge bij de koppeling: nieuwe nodes melden zich daar
// aan.
func Blocked(kind string, p Plan, serverURL string) (code, msg string) {
	grows := kind == "create" || len(p.NewNodes) > 0
	switch {
	case grows && serverURL == "":
		return "no_server_url", "Nieuwe nodes melden zich aan bij het adres van ClusterForge, en dat staat nog niet bij de Git-koppeling. Vul het in bij GitOps, onder Koppeling."
	case kind == "update" && slices.ContainsFunc(p.NewNodes, func(n PlanNewNode) bool { return n.Address == "" }):
		return "dhcp_growth", "Omhoog schalen uit Git kan alleen bij een cluster met vaste adressen. Met DHCP is het adres van een nieuwe node pas bekend als zijn VM draait, " +
			"en daarmee wat er in de configuratie van de andere nodes verandert; dat moet je zien voor je goedkeurt. Zet node_count in Git terug."
	}
	return "", ""
}

// Approval is wat goedkeuren van een wachtende wijziging zou doen; de
// detailpagina en het bevestigingsvenster tonen het.
type Approval struct {
	// Prod: het cluster is of wordt prod, dus de beheerder tikt de slug in.
	Prod bool
	Slug string
	// All: een eerdere revisie is niet toegepast, dus de taak past alle
	// stappen opnieuw toe.
	All bool
}

// ApprovalFor leest wat goedkeuren zou vragen.
func (s *Service) ApprovalFor(ctx context.Context, ch store.GitChange) (Approval, error) {
	var meta Metadata
	_ = json.Unmarshal(ch.Metadata, &meta)
	a := Approval{Slug: ch.Slug, Prod: meta.Environment == string(store.EnvironmentProd)}
	if ch.ClusterID == nil {
		return a, nil
	}
	c, err := s.q.GetCluster(ctx, *ch.ClusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	a.Prod = a.Prod || c.Environment == store.EnvironmentProd
	a.All = c.AppliedRevision < c.SpecRevision
	return a, nil
}

// Approve keurt een wachtende wijziging goed. In één transactie schrijft
// het een spec-revisie met bron git en de commit, werkt het de metadata bij
// en zet het via het clusterslot cluster.apply in de wachtrij. Verandert er
// op geen node een stap en was alles al toegepast, dan schuift de revisie
// op zonder taak. De bevestiging bij prod controleert de aanroeper.
func (s *Service) Approve(ctx context.Context, actor events.Actor, id uuid.UUID) (Decision, error) {
	// Eerst de kop van de branch lezen: staat er een nieuwere commit, dan
	// vervalt deze wijziging. Is GitHub onbereikbaar, dan geldt de laatste
	// stand; de fout staat bij de koppeling.
	if err := s.Sync(ctx); err != nil {
		s.log.Debug("gitops: synchroniseren voor goedkeuren mislukt", "err", err)
	}
	row, err := s.q.GetGitChange(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, ErrNotFound
	}
	if err != nil {
		return Decision{}, err
	}
	if row.Status != "pending" {
		return Decision{}, &ConflictError{Code: "not_pending", Msg: "deze wijziging is " + statusText[row.Status]}
	}
	var plan Plan
	if err := json.Unmarshal(row.Plan, &plan); err != nil {
		return Decision{}, err
	}
	repo, err := s.current(ctx)
	if err != nil {
		return Decision{}, err
	}
	if repo == nil || repo.ID != row.RepoID {
		return Decision{}, &ConflictError{Code: "not_linked", Msg: "de repository van deze wijziging is niet meer gekoppeld"}
	}
	if code, msg := Blocked(row.Kind, plan, repo.ServerUrl); msg != "" {
		return Decision{}, &ConflictError{Code: code, Msg: msg}
	}
	var meta Metadata
	if err := json.Unmarshal(row.Metadata, &meta); err != nil {
		return Decision{}, err
	}
	if row.Kind == "create" {
		return s.approveCreate(ctx, actor, row, plan, meta, *repo)
	}
	var next deploy.Spec
	if err := json.Unmarshal(row.Spec, &next); err != nil {
		return Decision{}, err
	}
	if row.ClusterID == nil {
		return Decision{}, &ConflictError{Code: "not_linked", Msg: "het cluster van deze wijziging bestaat niet meer"}
	}
	c, err := s.q.GetCluster(ctx, *row.ClusterID)
	if err != nil {
		return Decision{}, err
	}
	if c.GitRepoID == nil || *c.GitRepoID != row.RepoID {
		return Decision{}, &ConflictError{Code: "not_linked", Msg: "het cluster is niet meer aan deze repository gekoppeld"}
	}
	if c.SpecRevision != row.BaseRevision {
		reason := fmt.Sprintf("het cluster staat nu op revisie %d", c.SpecRevision)
		return Decision{}, s.stale(ctx, actor, row.ID, reason, "deze wijziging is gepland tegen revisie "+fmt.Sprint(row.BaseRevision)+" en "+reason+"; er komt een nieuw plan")
	}

	// Het plan moet nog kloppen: dezelfde stappen met dezelfde diffs, met
	// de facts van nu.
	cl := s.clusterOf(ctx, store.ListGitClustersRow{
		ID: c.ID, Slug: c.Slug, Name: c.Name, Description: c.Description, Environment: c.Environment, Tags: c.Tags, Type: c.Type,
		Spec: c.Spec, SpecRevision: c.SpecRevision, AppliedRevision: c.AppliedRevision, TemplateName: c.TemplateName, TemplateVersion: c.TemplateVersion,
		GitRepoID: c.GitRepoID,
	})
	if cl.Spec == nil {
		return Decision{}, &ConflictError{Code: "not_template", Msg: "het cluster heeft geen gewenste staat uit een template"}
	}
	old := *cl.Spec
	newTpl, ok := s.reg().Get(next.Template.Name, next.Template.Version)
	if !ok {
		return Decision{}, &ConflictError{Code: "not_template", Msg: fmt.Sprintf("%s %s zit niet in deze server", next.Template.Name, next.Template.Version)}
	}
	oldTpl, ok := s.reg().Get(old.Template.Name, old.Template.Version)
	if !ok {
		oldTpl = nil
	}
	inv, states, err := s.dep.Nodes(ctx, c.ID)
	if err != nil {
		return Decision{}, err
	}
	if m := old.Membership(inv); len(m) > 0 {
		return Decision{}, &ConflictError{Code: "membership", Msg: "het lidmaatschap wijkt af van de specificatie (" + strings.Join(m, "; ") +
			"); toepassen zou de lijst met peers herschrijven en een node buitensluiten"}
	}
	// Nodes die erbij komen, opnieuw gepland met de stand van nu: dezelfde
	// hostnames, indexen en adressen als in het plan.
	inOld := func(id uuid.UUID) bool {
		return slices.ContainsFunc(old.Nodes, func(n deploy.SpecNode) bool { return n.NodeID == id })
	}
	base := next
	base.Nodes = slices.DeleteFunc(slices.Clone(next.Nodes), func(n deploy.SpecNode) bool { return !inOld(n.NodeID) })
	values, err := newTpl.MaskedValues(next.Params)
	if err != nil {
		return Decision{}, &ConflictError{Code: "render_failed", Msg: err.Error()}
	}
	growth, err := s.dep.PlanGrowth(ctx, base, newTpl, values, states)
	if err != nil {
		var ve deploy.ValidationError
		if !errors.As(err, &ve) {
			return Decision{}, err
		}
		return Decision{}, s.stale(ctx, actor, row.ID, ve.Msg, ve.Msg+"; er komt een nieuw plan, bekijk het opnieuw")
	}
	if !sameGrowth(slices.DeleteFunc(slices.Clone(next.Nodes), func(n deploy.SpecNode) bool { return inOld(n.NodeID) }), growth.Nodes) {
		const reason = "de nodes die erbij komen, krijgen nu een andere naam of een ander adres"
		return Decision{}, s.stale(ctx, actor, row.ID, reason, reason+"; er komt een nieuw plan, bekijk het opnieuw")
	}
	all := maps.Clone(states)
	maps.Copy(all, growth.States)
	isNew := func(id uuid.UUID) bool { return !inOld(id) }
	diffs, warnings := nodeDiffs(oldTpl, newTpl, old, next, states, all, isNew, int(row.BaseRevision), row.CommitSha)
	if len(warnings) > 0 {
		return Decision{}, &ConflictError{Code: "render_failed", Msg: strings.Join(warnings, " ")}
	}
	if !samePlan(plan, next, diffs) {
		const reason = "het plan klopt niet meer met de nodes, bijvoorbeeld door andere netwerkgegevens"
		return Decision{}, s.stale(ctx, actor, row.ID, reason, reason+"; er komt een nieuw plan, bekijk het opnieuw")
	}

	full := c.AppliedRevision < c.SpecRevision || oldTpl == nil
	var rp *rollout.Plan
	if !plan.NoSteps || full {
		nodes := make([]rollout.ChangeNode, 0, len(next.Nodes))
		var grow []deploy.NewNode
		for _, n := range next.Nodes {
			if isNew(n.NodeID) {
				grow = append(grow, deploy.NewNodeOf(n))
				continue
			}
			nodes = append(nodes, changeNode(n, diffs[n.NodeID]))
		}
		p, err := s.ro.PlanChange(ctx, rollout.ChangeInput{
			ClusterID: c.ID, Revision: int(c.SpecRevision) + 1, Template: next.Template.Name, Version: next.Template.Version,
			Nodes: nodes, All: full, ChangeID: &row.ID, Commit: row.CommitSha, New: grow, ServerURL: repo.ServerUrl,
		})
		if err := rolloutError(err); err != nil {
			return Decision{}, err
		}
		rp = &p
	}

	var by *uuid.UUID
	if uid, err := uuid.Parse(actor.ID); err == nil && actor.Type == store.ActorTypeUser {
		by = &uid
	}
	var out Decision
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockCluster(ctx, c.ID)
		if err != nil {
			return err
		}
		if cur.SpecRevision != row.BaseRevision || cur.GitRepoID == nil {
			return &ConflictError{Code: "stale", Msg: "het cluster veranderde intussen; wacht op het nieuwe plan"}
		}
		ch, err := q.LockGitChange(ctx, row.ID)
		if err != nil {
			return err
		}
		if ch.Status != "pending" {
			return &ConflictError{Code: "not_pending", Msg: "deze wijziging is intussen " + statusText[ch.Status]}
		}
		// Nieuwe nodes komen in de inventory, in provisioning; de spec krijgt
		// hun echte id in plaats van het plan-id.
		spec := row.Spec
		if rp != nil && len(rp.NewNodes) > 0 {
			planned := make([]uuid.UUID, len(rp.NewNodes))
			for i, n := range rp.NewNodes {
				planned[i] = n.NodeID
			}
			if err := s.dep.AddNodesTx(ctx, q, actor, c.ID, newTpl, rp.NewNodes); err != nil {
				var ve deploy.ValidationError
				if errors.As(err, &ve) {
					return &ConflictError{Code: "stale", Msg: ve.Msg}
				}
				return err
			}
			withIDs := next
			withIDs.Nodes = slices.Clone(next.Nodes)
			for i := range withIDs.Nodes {
				if k := slices.Index(planned, withIDs.Nodes[i].NodeID); k >= 0 {
					withIDs.Nodes[i].NodeID = rp.NewNodes[k].NodeID
				}
			}
			if spec, err = json.Marshal(withIDs); err != nil {
				return err
			}
		}
		// Een geheim dat de nieuwe templateversie erbij heeft, maakt
		// ClusterForge zelf aan.
		for _, name := range next.Secrets {
			if _, err := q.GetSecret(ctx, store.GetSecretParams{ClusterID: c.ID, Name: name}); err == nil {
				continue
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			v, err := newTpl.NewSecret(name)
			if err != nil {
				return err
			}
			if err := s.dep.AddSecretTx(ctx, q, actor, c.ID, meta.Name, name, v); err != nil {
				return err
			}
		}
		rev, err := q.SetClusterSpec(ctx, store.SetClusterSpecParams{
			ID: c.ID, Spec: spec, TemplateName: &next.Template.Name, TemplateVersion: &next.Template.Version,
		})
		if err != nil {
			return err
		}
		if err := q.InsertSpecRevision(ctx, store.InsertSpecRevisionParams{
			ClusterID: c.ID, Revision: rev, Spec: spec, Source: "git", CreatedBy: by, CommitSha: &row.CommitSha,
		}); err != nil {
			return err
		}
		if err := s.setMetadata(ctx, q, actor, cur, meta); err != nil {
			return err
		}
		common := map[string]any{"slug": row.Slug, "path": row.Path, "commit": row.CommitSha, "author": row.CommitAuthor, "revision": rev}
		err = s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: "cluster.spec_changed",
			Payload: map[string]any{
				"name": meta.Name, "revision": rev, "previous_revision": row.BaseRevision, "source": "git",
				"template": next.Template.Name, "template_version": next.Template.Version, "commit": row.CommitSha, "change_id": row.ID,
			},
		})
		if err != nil {
			return err
		}
		if rp == nil {
			if err := q.SetAppliedRevision(ctx, store.SetAppliedRevisionParams{ID: c.ID, Revision: rev}); err != nil {
				return err
			}
			out.Change, err = q.DecideGitChange(ctx, store.DecideGitChangeParams{ID: row.ID, Status: "applied", Revision: &rev, DecidedBy: by})
			if err != nil {
				return err
			}
			if err := s.writeChange(ctx, q, actor, out.Change, "gitops.change_approved", with(common, "name", meta.Name, "job_id", nil)); err != nil {
				return err
			}
			return s.writeChange(ctx, q, actor, out.Change, "gitops.change_applied", with(common, "job_id", nil, "no_steps", true))
		}
		rp.Revision = int(rev)
		title := fmt.Sprintf("Wijziging uit Git toepassen in %s (commit %s)", meta.Name, short(row.CommitSha))
		j, err := s.ro.EnqueueTx(ctx, q, actor, title, *rp)
		if err := rolloutError(err); err != nil {
			return err
		}
		out.Job = &j
		out.Change, err = q.DecideGitChange(ctx, store.DecideGitChangeParams{ID: row.ID, Status: "applying", Revision: &rev, JobID: &j.ID, DecidedBy: by})
		if isUnique(err) {
			return &ConflictError{Code: "busy", Msg: "een vorige wijziging van dit cluster wordt nog afgerond; probeer het zo opnieuw"}
		}
		if err != nil {
			return err
		}
		return s.writeChange(ctx, q, actor, out.Change, "gitops.change_approved", with(common, "name", meta.Name, "job_id", j.ID, "all", rp.All,
			"new_nodes", len(rp.NewNodes)))
	})
	if err != nil {
		return Decision{}, err
	}
	if out.Job != nil {
		s.ro.Kick()
	}
	s.Kick(false)
	return out, nil
}

// changeNode zet wat er op een node verandert om in de stappen van de
// taak, met wat ze doen en welke services ze raken.
func changeNode(n deploy.SpecNode, d nodeDiff) rollout.ChangeNode {
	cn := rollout.ChangeNode{NodeID: n.NodeID, Hostname: n.Hostname}
	for _, sc := range d.Steps {
		if sc.Change == "removed" {
			continue
		}
		i := slices.IndexFunc(d.After, func(ts templates.Step) bool { return slices.Equal(drift.StepIDs(ts), sc.IDs) })
		if i < 0 {
			continue
		}
		ts := d.After[i]
		for _, id := range sc.IDs {
			cn.Steps = append(cn.Steps, rollout.PlanStep{Step: id, Title: sc.Title, Action: rollout.Action(ts, id)})
		}
		for _, svc := range rollout.Touched(ts) {
			if !slices.Contains(cn.Services, svc) {
				cn.Services = append(cn.Services, svc)
			}
		}
	}
	return cn
}

// samePlan zegt of de nodes nu dezelfde stappen met dezelfde diffs krijgen
// als in het plan. Een nieuwe node staat in het plan zonder id, dus de
// nodes worden op hostname vergeleken.
func samePlan(p Plan, next deploy.Spec, diffs map[uuid.UUID]nodeDiff) bool {
	key := func(steps []StepChange) string {
		b, _ := json.Marshal(steps)
		return string(b)
	}
	planned := map[string]string{}
	for _, n := range p.Nodes {
		planned[n.Hostname] = key(n.Steps)
	}
	for _, n := range next.Nodes {
		d, rendered := diffs[n.NodeID]
		if !rendered {
			continue
		}
		want, ok := planned[n.Hostname]
		if len(d.Steps) == 0 {
			if ok {
				return false
			}
			continue
		}
		if !ok || want != key(d.Steps) {
			return false
		}
		delete(planned, n.Hostname)
	}
	return len(planned) == 0
}

// sameGrowth zegt of de nodes die nu bij het cluster zouden komen dezelfde
// zijn als in het plan.
func sameGrowth(planned, now []deploy.SpecNode) bool {
	return slices.EqualFunc(planned, now, func(a, b deploy.SpecNode) bool {
		return a.Hostname == b.Hostname && a.Role == b.Role && a.Index == b.Index && a.Address == b.Address && a.Prefix == b.Prefix && a.VM == b.VM
	})
}

// setMetadata zet naam, beschrijving, omgeving en tags uit Git, met het
// event cluster.updated als er iets verandert.
func (s *Service) setMetadata(ctx context.Context, q *store.Queries, actor events.Actor, c store.Cluster, m Metadata) error {
	tags := m.Tags
	if tags == nil {
		tags = []string{}
	}
	diff := map[string]any{}
	add := func(field string, from, to any) {
		diff[field] = map[string]any{"from": from, "to": to}
	}
	if c.Name != m.Name {
		add("name", c.Name, m.Name)
	}
	if c.Description != m.Description {
		add("description", c.Description, m.Description)
	}
	if string(c.Environment) != m.Environment {
		add("environment", c.Environment, m.Environment)
	}
	if !slices.Equal(c.Tags, tags) && (len(c.Tags) > 0 || len(tags) > 0) {
		add("tags", c.Tags, tags)
	}
	if len(diff) == 0 {
		return nil
	}
	if _, err := q.UpdateCluster(ctx, store.UpdateClusterParams{
		ID: c.ID, Slug: c.Slug, Name: m.Name, Description: m.Description, Type: c.Type,
		Environment: store.Environment(m.Environment), GitRepoUrl: c.GitRepoUrl, Tags: tags,
	}); err != nil {
		return err
	}
	return s.ev.Write(ctx, q, events.Event{
		Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: "cluster.updated", Payload: diff,
	})
}

// stale laat een verouderde wijziging vervallen en vraagt een nieuw plan.
func (s *Service) stale(ctx context.Context, actor events.Actor, id uuid.UUID, reason, msg string) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		old, err := q.SupersedeGitChange(ctx, store.SupersedeGitChangeParams{ID: id, Reason: reason})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "git_change", SubjectID: old.ID.String(), ClusterID: old.ClusterID, Action: "gitops.change_superseded",
			Payload: map[string]any{"slug": old.Slug, "path": old.Path, "commit": old.CommitSha, "author": old.CommitAuthor, "reason": reason},
		})
	})
	if err != nil {
		return err
	}
	s.Kick(true)
	return &ConflictError{Code: "plan_changed", Msg: msg}
}

// Reject wijst een wachtende wijziging af. Het bestand blijft afgewezen tot
// een nieuwe commit het wijzigt.
func (s *Service) Reject(ctx context.Context, actor events.Actor, id uuid.UUID, reason string) (store.GitChange, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 500 {
		return store.GitChange{}, ValidationError{"reason", "geef een reden van hoogstens 500 tekens"}
	}
	var by *uuid.UUID
	if uid, err := uuid.Parse(actor.ID); err == nil && actor.Type == store.ActorTypeUser {
		by = &uid
	}
	var out store.GitChange
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		ch, err := q.LockGitChange(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if ch.Status != "pending" {
			return &ConflictError{Code: "not_pending", Msg: "deze wijziging is " + statusText[ch.Status]}
		}
		out, err = q.DecideGitChange(ctx, store.DecideGitChangeParams{ID: id, Status: "rejected", Reason: reason, DecidedBy: by})
		if err != nil {
			return err
		}
		var meta Metadata
		_ = json.Unmarshal(ch.Metadata, &meta)
		return s.writeChange(ctx, q, actor, out, "gitops.change_rejected", map[string]any{
			"slug": ch.Slug, "path": ch.Path, "name": meta.Name, "commit": ch.CommitSha, "author": ch.CommitAuthor, "reason": reason,
		})
	})
	if err != nil {
		return store.GitChange{}, err
	}
	s.Kick(false)
	return out, nil
}

// Reapply past de huidige revisie opnieuw toe als een eerdere toepassing
// mislukte: een nieuwe cluster.apply met alle stappen. Staan er nodes uit
// een mislukte schaalstap nog in provisioning, dan maakt de taak hun VM als
// die er nog niet is, meldt ze aan en neemt ze als laatste mee. Een
// mislukte wijziging blijft mislukt.
func (s *Service) Reapply(ctx context.Context, actor events.Actor, clusterID uuid.UUID) (store.Job, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, ErrNotFound
	}
	if err != nil {
		return store.Job{}, err
	}
	if c.TemplateName == nil || c.TemplateVersion == nil {
		return store.Job{}, &ConflictError{Code: "not_template", Msg: "alleen een cluster uit een template kan opnieuw toegepast worden"}
	}
	if c.AppliedRevision >= c.SpecRevision {
		return store.Job{}, &ConflictError{Code: "up_to_date", Msg: fmt.Sprintf("revisie %d is al toegepast", c.SpecRevision)}
	}
	inv, _, err := s.dep.Nodes(ctx, clusterID)
	if err != nil {
		return store.Job{}, err
	}
	spec, err := deploy.ParseSpec(c.Spec, templates.ClusterInfo{Name: c.Name, Slug: c.Slug, Environment: string(c.Environment)}, inv)
	if err != nil {
		return store.Job{}, &ConflictError{Code: "not_template", Msg: "de gewenste staat is niet te lezen: " + err.Error()}
	}
	if m := spec.Membership(inv); len(m) > 0 {
		return store.Job{}, &ConflictError{Code: "membership", Msg: "het lidmaatschap wijkt af van de specificatie (" + strings.Join(m, "; ") + ")"}
	}
	lifecycle := map[uuid.UUID]string{}
	for _, n := range inv {
		lifecycle[n.ID] = n.Lifecycle
	}
	nodes := make([]rollout.ChangeNode, 0, len(spec.Nodes))
	var grow []deploy.NewNode
	for _, n := range spec.Nodes {
		if lifecycle[n.NodeID] == string(store.NodeLifecycleProvisioning) {
			grow = append(grow, deploy.NewNodeOf(n))
			continue
		}
		nodes = append(nodes, rollout.ChangeNode{NodeID: n.NodeID, Hostname: n.Hostname})
	}
	if len(nodes) == 0 {
		return store.Job{}, &ConflictError{Code: "not_deployed",
			Msg: "de uitrol van dit cluster is niet afgerond; open de uitroltaak en kies daar Opnieuw proberen"}
	}
	serverURL := ""
	if len(grow) > 0 {
		repo, err := s.current(ctx)
		if err != nil {
			return store.Job{}, err
		}
		if repo != nil {
			serverURL = repo.ServerUrl
		}
		if serverURL == "" {
			return store.Job{}, &ConflictError{Code: "no_server_url",
				Msg: "er staan nieuwe nodes klaar, en die melden zich aan bij het adres van ClusterForge; vul dat in bij GitOps, onder Koppeling"}
		}
	}
	p, err := s.ro.PlanChange(ctx, rollout.ChangeInput{
		ClusterID: clusterID, Revision: int(c.SpecRevision), Template: spec.Template.Name, Version: spec.Template.Version, Nodes: nodes, All: true,
		New: grow, ServerURL: serverURL,
	})
	if err := rolloutError(err); err != nil {
		return store.Job{}, err
	}
	var j store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockCluster(ctx, clusterID)
		if err != nil {
			return err
		}
		if cur.SpecRevision != c.SpecRevision {
			return &ConflictError{Code: "stale", Msg: "het cluster veranderde intussen; probeer het opnieuw"}
		}
		j, err = s.ro.EnqueueTx(ctx, q, actor, fmt.Sprintf("Revisie %d opnieuw toepassen in %s", c.SpecRevision, c.Name), p)
		if err := rolloutError(err); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "gitops.reapply_requested",
			Payload: map[string]any{"name": c.Name, "revision": c.SpecRevision, "applied_revision": c.AppliedRevision, "job_id": j.ID, "new_nodes": len(grow)},
		})
	})
	if err != nil {
		return store.Job{}, err
	}
	s.ro.Kick()
	return j, nil
}

// finished zet een toegepaste wijziging op applied of failed, na de
// afronding van rollout, die de toegepaste revisie al bijwerkte.
func (s *Service) finished(ctx context.Context, j store.Job) {
	status, reason := "failed", j.Error
	switch j.Status {
	case store.JobStatusSucceeded:
		status, reason = "applied", ""
	case store.JobStatusCanceled:
		reason = "de taak is geannuleerd"
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		ch, err := q.FinishGitChange(ctx, store.FinishGitChangeParams{JobID: &j.ID, Status: status, Reason: reason})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		payload := map[string]any{"slug": ch.Slug, "path": ch.Path, "commit": ch.CommitSha, "author": ch.CommitAuthor, "revision": ch.Revision, "job_id": j.ID}
		if status == "failed" {
			payload["error"] = reason
		}
		return s.writeChange(ctx, q, events.System(), ch, "gitops.change_"+status, payload)
	})
	if err != nil {
		s.log.Warn("gitops: wijziging afronden mislukt", "job", j.ID, "err", err)
	}
	s.Kick(false)
}

func (s *Service) writeChange(ctx context.Context, q *store.Queries, actor events.Actor, ch store.GitChange, action string, payload map[string]any) error {
	return s.ev.Write(ctx, q, events.Event{
		Actor: actor, SubjectType: "git_change", SubjectID: ch.ID.String(), ClusterID: ch.ClusterID, Action: action, Payload: payload,
	})
}

// with geeft een kopie van m met de extra sleutels en waarden.
func with(m map[string]any, kv ...any) map[string]any {
	out := make(map[string]any, len(m)+len(kv)/2)
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

// rolloutError zet een fout van rollout om naar die van deze service.
func rolloutError(err error) error {
	var ce *rollout.ConflictError
	var fe *rollout.FieldError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return &ConflictError{Code: ce.Code, Msg: ce.Msg}
	case errors.As(err, &fe):
		return &ConflictError{Code: "invalid", Msg: fe.Message}
	case errors.Is(err, rollout.ErrNotFound):
		return ErrNotFound
	}
	return err
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}
