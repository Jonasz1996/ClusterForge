package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
)

// GetComponentMetrics geeft de grafieken van de database van een cluster,
// met een lijn per datanode.
func (s *Server) GetComponentMetrics(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID, params gen.GetComponentMetricsParams) {
	ctx := r.Context()
	if _, err := s.q.GetCluster(ctx, clusterID); err != nil {
		s.componentError(w, r, err)
		return
	}
	list, err := s.components.List(ctx, clusterID)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	for _, c := range list {
		if c.ID != componentID {
			continue
		}
		var nodes []uuid.UUID
		for _, n := range c.Nodes {
			if n.Role == components.RoleData {
				nodes = append(nodes, n.NodeID)
			}
		}
		s.writeMetrics(w, r, metrics.DatabasePanels(c.Engine), metrics.DatabaseSelector(clusterID, c.Engine, nodes), params.Range, params.From, params.To)
		return
	}
	s.componentError(w, r, components.ErrNotFound)
}
