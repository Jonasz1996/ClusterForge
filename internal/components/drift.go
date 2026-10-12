package components

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// NodeSteps geeft de stappen van de toegepaste onderdelen op een node,
// gerenderd met de revisie die het laatst helemaal lukte, voor de
// driftcontrole. Zo valt een met de hand aangepaste 99-clusterforge-galera.cnf op, ook
// op een cluster zonder template, en ook het bestand met cf_db voor het
// VIP.
func (s *Service) NodeSteps(ctx context.Context, nodeID uuid.UUID) ([]templates.Step, int, error) {
	rows, err := s.q.ListNodeComponents(ctx, nodeID)
	if err != nil {
		return nil, 0, err
	}
	var out []templates.Step
	revision := 0
	for _, comp := range rows {
		rev := int(*comp.AppliedRevision)
		r, err := s.load(ctx, jobParams{ComponentID: comp.ID, Revision: rev})
		if err != nil {
			return nil, 0, err
		}
		steps, err := deploy.RenderNode(r.tpl, r.spec.deploySpec(), r.secrets, r.states, nodeID)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", r.tpl.Title, err)
		}
		out = append(out, steps...)
		if v := r.spec.VIP; v != nil && slices.Contains(v.Nodes, nodeID) {
			if v.Own {
				i := slices.IndexFunc(r.spec.Nodes, func(n Node) bool { return n.NodeID == nodeID })
				vs, err := ownVIPSteps(r, comp.Engine, r.spec.Nodes[i])
				if err != nil {
					return nil, 0, fmt.Errorf("%s: VIP %s: %w", r.tpl.Title, v.Address, err)
				}
				out = append(out, vs...)
			} else {
				out = append(out, vipSteps(r.spec.Cluster.Slug, v.Address)...)
			}
		}
		revision = max(revision, rev)
	}
	return out, revision, nil
}
