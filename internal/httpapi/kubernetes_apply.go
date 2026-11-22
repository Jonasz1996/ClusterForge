package httpapi

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func (s *Server) ApplyKube(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.KubeApplyInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	c, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	ms, err := kube.ParseManifests(in.Yaml)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	preview := deref(in.Preview)
	if !preview && !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, "YAML toepassen") {
		return
	}
	force := deref(in.Force)
	res, err := s.kube.Apply(ctx, clusterID, ms, kube.ApplyOptions{Namespace: deref(in.Namespace), DryRun: preview, Force: force})
	if err != nil && len(res.Objects) == 0 {
		s.kubeError(w, r, err)
		return
	}
	if !preview && res.Changed > 0 {
		created, updated := []string{}, []string{}
		for _, o := range res.Objects {
			what := o.Kind + " " + o.Name
			if o.Namespace != "" {
				what = o.Kind + " " + o.Namespace + "/" + o.Name
			}
			switch o.Action {
			case kube.ApplyCreate:
				created = append(created, what)
			case kube.ApplyUpdate:
				updated = append(updated, what)
			}
		}
		// Wat toegepast is, hoort in het logboek, ook als de browser al
		// weg is. Alleen soort en naam: een Secret gaat er nooit in.
		_ = s.ev.Write(context.WithoutCancel(ctx), nil, events.Event{
			Actor: events.User(p.User.ID), SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action: "kube.applied", Payload: map[string]any{"created": created, "updated": updated, "force": force},
		})
	}
	if err != nil {
		// Een deel is toegepast en toen viel de verbinding weg: dat deel
		// staat al in het logboek, de fout zegt de rest.
		s.kubeError(w, r, err)
		return
	}
	out := gen.KubeApplyResult{Ok: res.OK, Changed: res.Changed, Applied: !preview, Objects: make([]gen.KubeApplyObject, 0, len(res.Objects))}
	for _, o := range res.Objects {
		ao := gen.KubeApplyObject{
			Doc: o.Doc, ApiVersion: o.APIVersion, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name,
			Ref: nullable.NewNullNullable[gen.KubeRef](), Action: gen.KubeApplyObjectAction(o.Action), Diff: o.Diff, Note: o.Note,
			Errors: make([]gen.KubeFieldError, 0, len(o.Errors)), Conflicts: make([]gen.KubeFieldConflict, 0, len(o.Conflicts)),
		}
		if o.Ref != nil {
			ao.Ref = nullable.NewNullableWithValue(toAPIKubeRef(*o.Ref))
		}
		for _, e := range o.Errors {
			ao.Errors = append(ao.Errors, gen.KubeFieldError{Field: e.Field, Message: e.Message})
		}
		for _, cf := range o.Conflicts {
			ao.Conflicts = append(ao.Conflicts, gen.KubeFieldConflict{Field: cf.Field, Manager: cf.Manager})
		}
		out.Objects = append(out.Objects, ao)
	}
	writeJSON(w, http.StatusOK, out)
}
