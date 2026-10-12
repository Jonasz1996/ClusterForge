package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/kubenodes"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// RemoveKubeNode haalt een worker weg: leeg, VM uit, uit Kubernetes, VM
// weg en uit ClusterForge, als taak. Met preview alleen het plan.
func (s *Server) RemoveKubeNode(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.KubeRemoveNodeInput
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
		plan, err := s.kubeNodes.Plan(ctx, clusterID, in.Node, deref(in.Force))
		if err != nil {
			s.kubeNodesError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, gen.KubeRemoveNodePlan{
			Node: plan.Node, NodeId: plan.NodeID, Hostname: plan.Hostname, Ready: plan.Ready, Vmid: plan.VMID, Host: plan.Host,
			Workers: plan.Workers,
			Drain: gen.KubeDrainPlan{
				Node: plan.Node, Cordoned: plan.Drain.Cordoned, Evict: toAPIDrainPods(plan.Drain.Evict), Skip: toAPIDrainPods(plan.Drain.Skip),
				Blocked: toAPIDrainPods(plan.Drain.Blocked),
			},
		})
		return
	}
	if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, "een worker weghalen") {
		return
	}
	j, err := s.kubeNodes.Request(ctx, events.User(p.User.ID), clusterID, in.Node, deref(in.Force))
	if err != nil {
		s.kubeNodesError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) kubeNodesError(w http.ResponseWriter, r *http.Request, err error) {
	var ce *kubenodes.ConflictError
	if errors.As(err, &ce) {
		writeError(w, http.StatusConflict, "conflict", ce.Error())
		return
	}
	s.kubeError(w, r, err)
}
