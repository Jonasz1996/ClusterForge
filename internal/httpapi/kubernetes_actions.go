package httpapi

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// actionWhat is wat een actie doet, voor de melding over tweestapsverificatie.
var actionWhat = map[gen.KubeActionInputAction]string{
	gen.KubeActionInputActionScale: "schalen", gen.KubeActionInputActionRestart: "herstarten",
	gen.KubeActionInputActionRollback: "terugdraaien", gen.KubeActionInputActionDelete: "iets weghalen",
	gen.KubeActionInputActionCordon: "een node op cordon zetten", gen.KubeActionInputActionUncordon: "een node weer pods geven",
}

func (s *Server) KubeAction(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.KubeActionInput
	if !decode(w, r, &in) {
		return
	}
	what, known := actionWhat[in.Action]
	if !known {
		writeError(w, http.StatusBadRequest, "validation", "onbekende actie "+string(in.Action))
		return
	}
	ctx := r.Context()
	c, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	if in.Action == gen.KubeActionInputActionScale && in.Replicas == nil {
		writeError(w, http.StatusBadRequest, "validation", "geef het aantal pods in replicas")
		return
	}
	if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, what) {
		return
	}
	a := kube.Action{
		Name: string(in.Action), Ref: kube.Ref{Group: in.Ref.Group, Version: in.Ref.Version, Resource: in.Ref.Resource, Namespace: in.Ref.Namespace, Name: in.Ref.Name},
		Replicas: deref(in.Replicas), Revision: deref(in.Revision),
	}
	out, err := s.kube.Do(ctx, clusterID, a)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	if out.Event != "" {
		// De actie is gebeurd; ook als de browser al weg is, hoort ze in
		// het logboek.
		_ = s.ev.Write(context.WithoutCancel(ctx), nil, events.Event{
			Actor: events.User(p.User.ID), SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action: out.Event, Payload: out.Payload,
		})
	}
	writeJSON(w, http.StatusOK, gen.KubeActionResult{Message: out.Message, Changed: out.Event != ""})
}

func (s *Server) ListKubeRevisions(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.ListKubeRevisionsParams) {
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	revs, err := s.kube.Revisions(ctx, clusterID, params.Namespace, params.Name)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	items := make([]gen.KubeRevision, 0, len(revs))
	for _, rv := range revs {
		items = append(items, gen.KubeRevision{
			Revision: rv.Revision, Replicaset: rv.ReplicaSet, Images: nonNil(rv.Images), CreatedAt: rv.CreatedAt,
			Replicas: rv.Replicas, Current: rv.Current, ChangeCause: rv.ChangeCause,
		})
	}
	writeJSON(w, http.StatusOK, list[gen.KubeRevision]{items})
}

func toAPIDrainPods(ps []kube.DrainPod) []gen.KubeDrainPod {
	out := make([]gen.KubeDrainPod, 0, len(ps))
	for _, p := range ps {
		out = append(out, gen.KubeDrainPod{Namespace: p.Namespace, Name: p.Name, OwnerKind: p.OwnerKind, OwnerName: p.OwnerName, Reason: p.Reason})
	}
	return out
}

func (s *Server) DrainKubeNode(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.KubeDrainInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	c, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	if in.Preview != nil && *in.Preview {
		plan, err := s.kube.PlanDrain(ctx, clusterID, in.Node, kube.DrainOptions{Force: deref(in.Force)})
		if err != nil {
			s.kubeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, gen.KubeDrainPlan{
			Node: plan.Node, Cordoned: plan.Cordoned, Evict: toAPIDrainPods(plan.Evict), Skip: toAPIDrainPods(plan.Skip),
			Blocked: toAPIDrainPods(plan.Blocked),
		})
		return
	}
	if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, "een node leegmaken") {
		return
	}
	j, err := s.kube.RequestDrain(ctx, events.User(p.User.ID), clusterID, in.Node, deref(in.Force))
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}
