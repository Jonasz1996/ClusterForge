package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// approveCreate keurt een nieuw cluster uit Git goed. De uitrol maakt in
// één transactie het cluster, zijn nodes en spec-revisie 1 met bron git en
// de commit, en zet cluster.deploy in de wachtrij. In dezelfde transactie
// wordt het cluster aan zijn bestand gekoppeld en wijst de wijziging naar
// het nieuwe cluster en de taak.
func (s *Service) approveCreate(ctx context.Context, actor events.Actor, row store.GetGitChangeRow, plan Plan, meta Metadata, repo store.GitRepo) (Decision, error) {
	var spec deploy.Spec
	if err := json.Unmarshal(row.Spec, &spec); err != nil {
		return Decision{}, err
	}
	if spec.Target.ProxmoxID == uuid.Nil {
		const reason = "het plan is gemaakt door een oudere versie van ClusterForge, zonder het Proxmox-doel"
		return Decision{}, s.stale(ctx, actor, row.ID, reason, reason+"; er komt een nieuw plan, bekijk het opnieuw")
	}
	params := maps.Clone(spec.Params)
	if params == nil {
		params = map[string]any{}
	}
	// Zonder vrid in het bestand de VRRP-id uit het plan, zodat de uitrol
	// doet wat de beheerder zag.
	if v, ok := params["vrid"]; (!ok || v == nil) && plan.VRID > 0 {
		params["vrid"] = plan.VRID
	}
	req := deploy.Request{
		Template: spec.Template.Name,
		Cluster:  deploy.ClusterInput{Name: meta.Name, Slug: row.Slug, Environment: meta.Environment, Description: meta.Description},
		Params:   params, Target: spec.Target, ServerURL: repo.ServerUrl,
	}
	// Dezelfde controles als bij het plannen, met de stand van nu.
	now, err := s.dep.CheckNew(ctx, req, spec.Template.Version)
	if err != nil {
		var ve deploy.ValidationError
		if !errors.As(err, &ve) {
			return Decision{}, err
		}
		return Decision{}, s.stale(ctx, actor, row.ID, ve.Msg, ve.Msg+"; er komt een nieuw plan, bekijk het opnieuw")
	}
	if !sameNew(plan, now) {
		const reason = "de nodes of het VIP van het nieuwe cluster zijn niet meer zoals in het plan, bijvoorbeeld door een adres dat intussen in gebruik is"
		return Decision{}, s.stale(ctx, actor, row.ID, reason, reason+"; er komt een nieuw plan, bekijk het opnieuw")
	}

	var by *uuid.UUID
	if uid, err := uuid.Parse(actor.ID); err == nil && actor.Type == store.ActorTypeUser {
		by = &uid
	}
	url := FileURL(repo.ApiUrl, repo.Owner, repo.Name, repo.Branch, row.Path)
	var out Decision
	j, _, err := s.dep.RequestWith(ctx, actor, req, deploy.RequestOptions{
		Version: spec.Template.Version, Source: "git", CommitSha: &row.CommitSha, Tags: meta.Tags,
		Payload: map[string]any{"commit": row.CommitSha, "change_id": row.ID},
		InTx: func(ctx context.Context, q *store.Queries, c store.Cluster, rev int32, j store.Job) error {
			ch, err := q.LockGitChange(ctx, row.ID)
			if err != nil {
				return err
			}
			if ch.Status != "pending" {
				return &ConflictError{Code: "not_pending", Msg: "deze wijziging is intussen " + statusText[ch.Status]}
			}
			if err := q.SetClusterGit(ctx, store.SetClusterGitParams{ID: c.ID, GitRepoID: &repo.ID, GitRepoUrl: url}); err != nil {
				return err
			}
			err = s.ev.Write(ctx, q, events.Event{
				Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: "cluster.git_linked",
				Payload: map[string]any{"name": c.Name, "path": row.Path, "repo": repo.Owner + "/" + repo.Name, "url": url, "commit": row.CommitSha},
			})
			if err != nil {
				return err
			}
			out.Change, err = q.DecideGitChange(ctx, store.DecideGitChangeParams{
				ID: row.ID, Status: "applying", Revision: &rev, JobID: &j.ID, ClusterID: &c.ID, DecidedBy: by,
			})
			if isUnique(err) {
				return &ConflictError{Code: "busy", Msg: "een vorige wijziging van dit cluster wordt nog afgerond; probeer het zo opnieuw"}
			}
			if err != nil {
				return err
			}
			return s.writeChange(ctx, q, actor, out.Change, "gitops.change_approved", map[string]any{
				"slug": row.Slug, "path": row.Path, "commit": row.CommitSha, "author": row.CommitAuthor, "revision": rev,
				"name": meta.Name, "job_id": j.ID, "kind": "create", "new_nodes": len(plan.NewNodes),
			})
		},
	})
	if err != nil {
		var ve deploy.ValidationError
		if errors.As(err, &ve) {
			return Decision{}, &ConflictError{Code: "invalid", Msg: ve.Msg}
		}
		return Decision{}, err
	}
	out.Job = &j
	s.Kick(false)
	return out, nil
}

// sameNew zegt of een nieuw cluster nu dezelfde nodes, adressen, VM's en
// hetzelfde VIP krijgt als in het plan. Op welke host een VM komt, kiest de
// uitrol met het vrije geheugen van dat moment.
func sameNew(p Plan, now deploy.Plan) bool {
	if p.VIP != now.VIP || p.VRID != now.VRID {
		return false
	}
	return slices.EqualFunc(p.NewNodes, now.Nodes, func(a PlanNewNode, b deploy.PlannedNode) bool {
		return a.Hostname == b.Hostname && a.Role == b.Role && a.Address == b.Address && a.Prefix == b.Prefix && a.VM == b.VM
	})
}
