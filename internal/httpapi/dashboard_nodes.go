package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/updates"
)

// dashboardNode is één node voor /api/dashboard/v1/nodes (fase 31): platte
// velden, zodat een eigen dashboard per cluster een groep en per node een
// tegel kan maken en bijhouden.
type dashboardNode struct {
	ID          uuid.UUID  `json:"id"`
	Hostname    string     `json:"hostname"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterSlug *string    `json:"cluster_slug"`
	ClusterName *string    `json:"cluster_name"`
	Role        string     `json:"role"`
	Description string     `json:"description"`
	Lifecycle   string     `json:"lifecycle"`
	PrimaryIP   *string    `json:"primary_ip"`
	Tags        []string   `json:"tags"`
	// Status zoals ClusterForge de node beoordeelt (healthy, degraded,
	// down, unknown), met een label en de reden.
	Status       string `json:"status"`
	StatusLabel  string `json:"status_label"`
	StatusReason string `json:"status_reason"`
	// Online: een heartbeat van de agent binnen status.HeartbeatDown.
	Online bool `json:"online"`
	// Maintenance: lifecycle is maintenance.
	Maintenance     bool                  `json:"maintenance"`
	Updates         int                   `json:"updates"`
	UpdatesSecurity int                   `json:"updates_security"`
	RebootRequired  bool                  `json:"reboot_required"`
	Proxmox         *dashboardNodeProxmox `json:"proxmox"`
	URL             string                `json:"url"`
}

// dashboardNodeProxmox is de VM of container in Proxmox achter een node.
// Host, type en naam zijn leeg zolang de Proxmox-sync de gast niet kent.
type dashboardNodeProxmox struct {
	Host string `json:"host"`
	VMID int32  `json:"vmid"`
	// Type zoals Proxmox het noemt: qemu of lxc.
	Type string `json:"type"`
	Name string `json:"name"`
}

func (s *Server) buildDashboardNodes(ctx context.Context, now time.Time, upd []updates.Node) ([]dashboardNode, error) {
	rows, err := s.q.DashboardNodeList(ctx)
	if err != nil {
		return nil, err
	}
	byNode := make(map[uuid.UUID]updates.Node, len(upd))
	for _, n := range upd {
		byNode[n.NodeID] = n
	}
	out := make([]dashboardNode, 0, len(rows))
	for _, r := range rows {
		n := dashboardNode{
			ID: r.ID, Hostname: r.Hostname, ClusterID: r.ClusterID, ClusterSlug: r.ClusterSlug, ClusterName: r.ClusterName,
			Role: r.Role, Description: r.Description, Lifecycle: r.Lifecycle, Tags: nonNil(r.Tags),
			Status: r.Status, StatusLabel: statusLabels[r.Status], StatusReason: r.StatusReason,
			Online:      r.HeartbeatAt != nil && now.Sub(*r.HeartbeatAt) <= status.HeartbeatDown,
			Maintenance: r.Lifecycle == "maintenance",
			URL:         "/nodes/detail?id=" + r.ID.String(),
		}
		if n.StatusLabel == "" {
			n.StatusLabel = r.Status
		}
		if r.PrimaryIp != nil {
			ip := r.PrimaryIp.String()
			n.PrimaryIP = &ip
		}
		if u, ok := byNode[r.ID]; ok {
			if u.Upgrades != nil {
				n.Updates, n.UpdatesSecurity = u.Upgrades.Total, u.Upgrades.Security
			}
			n.RebootRequired = u.RebootRequired
		}
		if r.ProxmoxID != nil && r.PveVmid != nil {
			n.Proxmox = &dashboardNodeProxmox{VMID: *r.PveVmid, Host: deref(r.PveNode), Type: deref(r.PveType), Name: deref(r.PveName)}
		}
		out = append(out, n)
	}
	return out, nil
}

// nodesFor geeft de nodes die een token mag zien: met clusters alleen de
// nodes van die clusters, zonder (alle clusters) ook de nodes zonder cluster.
// Een kopie, zodat de URL's de cache niet raken.
func (d dashboardSet) nodesFor(ids []uuid.UUID, base string) []dashboardNode {
	out := make([]dashboardNode, 0, len(d.nodes))
	for _, n := range d.nodes {
		if len(ids) > 0 && (n.ClusterID == nil || !slices.Contains(ids, *n.ClusterID)) {
			continue
		}
		n.Tags = slices.Clone(n.Tags)
		n.URL = base + n.URL
		out = append(out, n)
	}
	return out
}

// dashboardNodes: GET /api/dashboard/v1/nodes, gesorteerd op cluster_id
// (nodes zonder cluster achteraan) en hostname. ?cluster=<slug, id of naam>
// geeft alleen de nodes van dat cluster.
func (s *Server) dashboardNodes(w http.ResponseWriter, r *http.Request) {
	t, _ := r.Context().Value(dashboardTokenKey{}).(store.ApiToken)
	d, err := s.dashboardData(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	nodes := d.nodesFor(t.ClusterIds, baseURL(r, s.cfg.TrustProxyHeaders))
	key := strings.TrimSpace(r.URL.Query().Get("cluster"))
	if key == "" {
		writeJSON(w, http.StatusOK, nodes)
		return
	}
	for _, c := range d.filter(t.ClusterIds).Clusters {
		if strings.EqualFold(c.Slug, key) || c.Id.String() == strings.ToLower(key) || strings.EqualFold(c.Name, key) {
			out := make([]dashboardNode, 0)
			for _, n := range nodes {
				if n.ClusterID != nil && *n.ClusterID == c.Id {
					out = append(out, n)
				}
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden (of niet zichtbaar met dit token)")
}
