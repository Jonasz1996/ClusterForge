package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/topo"
)

// mapLayer bouwt een laag van de kaart.
func (s *Server) mapLayer(r *http.Request, layer string, f topo.MapFilter) (topo.Map, error) {
	m, err := s.topo.Load(r.Context())
	if err != nil {
		return topo.Map{}, err
	}
	inv, err := s.topo.LoadInventory(r.Context())
	if err != nil {
		return topo.Map{}, err
	}
	if layer == "network" {
		return topo.Network(m, inv, f), nil
	}
	return topo.Physical(m, inv, f), nil
}

func (s *Server) GetMap(w http.ResponseWriter, r *http.Request, params gen.GetMapParams) {
	if params.Layer != gen.GetMapParamsLayerPhysical && params.Layer != gen.GetMapParamsLayerNetwork {
		writeError(w, http.StatusBadRequest, "bad_request", "laag is physical of network")
		return
	}
	var f topo.MapFilter
	if params.Environment != nil {
		f.Environment = string(*params.Environment)
	}
	if params.ClusterId != nil {
		id := *params.ClusterId
		f.ClusterID = &id
	}
	m, err := s.mapLayer(r, string(params.Layer), f)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// hostInterface is gen.HostInterface.
type hostInterface struct {
	Iface         string   `json:"iface"`
	Type          string   `json:"type"`
	Active        bool     `json:"active"`
	Ports         []string `json:"ports"`
	VLANID        *int32   `json:"vlan_id"`
	VLANRawDevice string   `json:"vlan_raw_device"`
	CIDR          string   `json:"cidr"`
	CIDR6         string   `json:"cidr6"`
	Gateway       string   `json:"gateway"`
	Gateway6      string   `json:"gateway6"`
	VLANAware     bool     `json:"vlan_aware"`
	MTU           *int32   `json:"mtu"`
	Comments      string   `json:"comments"`
}

// hostNetwork is gen.HostNetwork.
type hostNetwork struct {
	Host       string          `json:"host"`
	ReadAt     time.Time       `json:"read_at"`
	Error      string          `json:"error"`
	Interfaces []hostInterface `json:"interfaces"`
}

func (s *Server) GetProxmoxNetwork(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if _, err := s.q.GetProxmoxConnection(r.Context(), id); s.proxmoxError(w, r, err) {
		return
	}
	states, err := s.q.ListHostNetworkStates(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.q.ListHostNetworksByConnection(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := []hostNetwork{}
	for _, st := range states {
		h := hostNetwork{Host: st.PveNode, ReadAt: st.ReadAt, Error: st.Error, Interfaces: []hostInterface{}}
		for _, n := range rows {
			if n.PveNode != st.PveNode {
				continue
			}
			h.Interfaces = append(h.Interfaces, hostInterface{
				Iface: n.Iface, Type: n.Type, Active: n.Active, Ports: n.Ports, VLANID: n.VlanID, VLANRawDevice: n.VlanRawDevice,
				CIDR: n.Cidr, CIDR6: n.Cidr6, Gateway: n.Gateway, Gateway6: n.Gateway6, VLANAware: n.VlanAware, MTU: n.Mtu, Comments: n.Comments,
			})
		}
		out = append(out, h)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

// nodeLink is gen.NodeLink.
type nodeLink struct {
	ID           uuid.UUID  `json:"id"`
	ConnectionID uuid.UUID  `json:"connection_id"`
	Connection   string     `json:"connection"`
	VMID         int32      `json:"vmid"`
	Guest        string     `json:"guest"`
	GuestType    string     `json:"guest_type"`
	Host         string     `json:"host"`
	NodeID       uuid.UUID  `json:"node_id"`
	Hostname     string     `json:"hostname"`
	ClusterID    *uuid.UUID `json:"cluster_id"`
	Cluster      string     `json:"cluster"`
	MAC          string     `json:"mac"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (s *Server) ListNodeLinks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListLinkProposals(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]nodeLink, 0, len(rows))
	for _, l := range rows {
		out = append(out, nodeLink{
			ID: l.ID, ConnectionID: l.ConnectionID, Connection: l.Connection, VMID: l.Vmid, Guest: l.Guest, GuestType: l.GuestType,
			Host: l.Host, NodeID: l.NodeID, Hostname: l.Hostname, ClusterID: l.ClusterID, Cluster: l.Cluster, MAC: l.Mac, CreatedAt: l.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

var errLinkDecided = inventory.ConflictError{Msg: "over dit voorstel is al beslist", Code: "decided"}

// decideLink bevestigt of negeert een koppelvoorstel. Bevestigen koppelt de
// node aan de VM, zoals Koppelen bij de node, in dezelfde transactie.
func (s *Server) decideLink(w http.ResponseWriter, r *http.Request, id uuid.UUID, confirm bool) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	actor := events.User(p.User.ID)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		l, err := q.LockLinkProposal(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return inventory.ErrNotFound
		}
		if err != nil {
			return err
		}
		if l.State != "suggested" {
			return errLinkDecided
		}
		vmid := l.Vmid
		guest, err := q.GetProxmoxGuest(ctx, store.GetProxmoxGuestParams{ConnectionID: l.ConnectionID, Vmid: &vmid})
		if errors.Is(err, pgx.ErrNoRows) {
			return inventory.ConflictError{Msg: "die VM staat niet meer in Proxmox"}
		}
		if err != nil {
			return err
		}
		state, action := "ignored", "node.link_ignored"
		node, err := q.LockNode(ctx, l.NodeID)
		if err != nil {
			return err
		}
		if confirm {
			state, action = "confirmed", "node.link_confirmed"
			if node.ProxmoxID != nil {
				return inventory.ConflictError{Msg: node.Hostname + " hangt al aan een VM"}
			}
			_, err := s.inv.UpdateNodeTx(ctx, q, actor, l.NodeID, func(f *inventory.NodeFields) {
				f.Proxmox = &inventory.ProxmoxLink{ConnectionID: l.ConnectionID, VMID: int(l.Vmid)}
			})
			if err != nil {
				return err
			}
		}
		if err := q.DecideLinkProposal(ctx, store.DecideLinkProposalParams{ID: id, State: state, DecidedBy: &p.User.ID}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "node", SubjectID: l.NodeID.String(), ClusterID: node.ClusterID, Action: action,
			Payload: map[string]any{
				"proposal_id": id.String(), "hostname": node.Hostname, "connection_id": l.ConnectionID.String(),
				"vmid": l.Vmid, "guest": guest.Name, "host": guest.PveNode, "mac": l.Mac,
			},
		})
	})
	if s.inventoryError(w, r, inventory.Translate(err)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ConfirmNodeLink(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.decideLink(w, r, id, true)
}

func (s *Server) IgnoreNodeLink(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.decideLink(w, r, id, false)
}
