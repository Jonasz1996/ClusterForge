package proxmox

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// ProposeLinks stelt voor om een node aan een VM te koppelen als de VM geen
// node is en een netwerkkaart heeft met het MAC-adres van een interface van
// die node. Alleen nodes met een agent en zonder VM tellen mee, en een
// voorstel dat al bestaat, ook een genegeerd, komt niet opnieuw. Voorstellen
// die niet meer kunnen, verdwijnen.
func (s *Service) ProposeLinks(ctx context.Context) {
	if err := s.proposeLinks(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("koppelvoorstellen maken mislukt", "err", err)
	}
}

func (s *Service) proposeLinks(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.DeleteInvalidLinkProposals(ctx); err != nil {
			return err
		}
		matches, err := q.ListLinkMatches(ctx, SandboxPool)
		if err != nil {
			return err
		}
		for _, m := range matches {
			id, err := q.InsertLinkProposal(ctx, store.InsertLinkProposalParams(m))
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			vmid := m.Vmid
			res, err := q.GetProxmoxGuest(ctx, store.GetProxmoxGuestParams{ConnectionID: m.ConnectionID, Vmid: &vmid})
			if err != nil {
				return err
			}
			node, err := q.LockNode(ctx, m.NodeID)
			if err != nil {
				return err
			}
			err = s.ev.Write(ctx, q, events.Event{
				Actor: events.System(), SubjectType: "node", SubjectID: m.NodeID.String(), ClusterID: node.ClusterID,
				Action: "node.link_proposed",
				Payload: map[string]any{
					"proposal_id": id.String(), "hostname": node.Hostname, "connection_id": m.ConnectionID.String(),
					"vmid": m.Vmid, "guest": res.Name, "host": res.PveNode, "mac": m.Mac,
				},
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}
