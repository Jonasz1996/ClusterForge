package publish

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// nodeRow is een node zoals de publicatie hem nodig heeft.
type nodeRow struct {
	ID          uuid.UUID
	Hostname    string
	ClusterID   *uuid.UUID
	ClusterName string
	ClusterSlug string
	Environment string
	Template    string
	PrimaryIP   string
	Protocol    int
	Online      bool
	// VIPs zijn de VIP's die deze node nu heeft.
	VIPs []string
}

// Ready zegt waarom de node geen webverzoek kan doen; leeg als het kan.
func (n nodeRow) Ready() string {
	switch {
	case !n.Online:
		return fmt.Sprintf("de agent op %s is offline", n.Hostname)
	case n.Protocol < protocol.WebSince:
		return fmt.Sprintf("de agent op %s is te oud (protocol %d, %d nodig); werk hem bij via Nodes → Agents bijwerken", n.Hostname, n.Protocol, protocol.WebSince)
	}
	return ""
}

// clusterRow is een cluster met zijn VIP's.
type clusterRow struct {
	ID          uuid.UUID
	Name        string
	Slug        string
	Environment string
	Template    string
	VIPs        []vipRow
}

type vipRow struct {
	Address string
	Owner   *uuid.UUID
	// DB: het eigen VIP van een database (Patroni), niet van de app.
	DB bool
}

// isDBVIP herkent het eigen VIP van een database aan zijn omschrijving.
func isDBVIP(desc string) bool {
	d := strings.ToLower(desc)
	return strings.Contains(d, "database") || strings.Contains(d, "patroni") || strings.Contains(d, "postgres")
}

// primaryVIP is het VIP waar een dienst van het cluster naar moet wijzen:
// nooit het eigen VIP van de database, liefst het VIP dat een node met de
// dienst nu heeft, anders het eerste (scan 28/11: een cluster met VIP wijst
// altijd naar zijn VIP, en .240 van Patroni is niet de outpost .244).
func primaryVIP(c clusterRow, on []nodeRow) string {
	var app []vipRow
	for _, v := range c.VIPs {
		if !v.DB {
			app = append(app, v)
		}
	}
	if len(app) == 0 {
		return ""
	}
	for _, v := range app {
		for _, n := range on {
			if v.Owner != nil && *v.Owner == n.ID {
				return v.Address
			}
			for _, a := range n.VIPs {
				if a == v.Address {
					return v.Address
				}
			}
		}
	}
	return app[0].Address
}

func (s *Service) nodes(ctx context.Context) ([]nodeRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT n.id, n.hostname, n.cluster_id, coalesce(c.name, ''), coalesce(c.slug, ''),
		coalesce(c.environment::text, ''), coalesce(c.template_name, ''), coalesce(host(n.primary_ip), ''),
		coalesce(a.protocol_version, 0), st.heartbeat_at,
		coalesce((SELECT array_agg(host(v.address) ORDER BY v.address) FROM vips v WHERE v.owner_node_id = n.id), '{}')
		FROM nodes n
		LEFT JOIN clusters c ON c.id = n.cluster_id
		LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
		LEFT JOIN node_status st ON st.node_id = n.id
		ORDER BY coalesce(c.name, ''), n.hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []nodeRow
	for rows.Next() {
		var n nodeRow
		var proto int32
		var hb *time.Time
		if err := rows.Scan(&n.ID, &n.Hostname, &n.ClusterID, &n.ClusterName, &n.ClusterSlug, &n.Environment, &n.Template, &n.PrimaryIP,
			&proto, &hb, &n.VIPs); err != nil {
			return nil, err
		}
		n.Protocol = int(proto)
		n.Online = hb != nil && time.Since(*hb) < status.HeartbeatLate
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Service) clusters(ctx context.Context) ([]clusterRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.name, c.slug, c.environment::text, coalesce(c.template_name, ''),
		coalesce((SELECT array_agg(host(v.address) ORDER BY v.address) FROM vips v WHERE v.cluster_id = c.id), '{}'),
		coalesce((SELECT array_agg(coalesce(v.owner_node_id::text, '') ORDER BY v.address) FROM vips v WHERE v.cluster_id = c.id), '{}'),
		coalesce((SELECT array_agg(v.description ORDER BY v.address) FROM vips v WHERE v.cluster_id = c.id), '{}')
		FROM clusters c ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []clusterRow
	for rows.Next() {
		var c clusterRow
		var addrs, owners, descs []string
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.Environment, &c.Template, &addrs, &owners, &descs); err != nil {
			return nil, err
		}
		for i, a := range addrs {
			v := vipRow{Address: a}
			if i < len(descs) {
				v.DB = isDBVIP(descs[i])
			}
			if i < len(owners) && owners[i] != "" {
				if id, err := uuid.Parse(owners[i]); err == nil {
					v.Owner = &id
				}
			}
			c.VIPs = append(c.VIPs, v)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// pickNode kiest de node waarop de agent NPM of Authentik aanspreekt: de
// gekozen losse node, of in het cluster de node met het VIP (daar draait
// NPM), anders een andere node die online is.
func (s *Service) pickNode(ctx context.Context, clusterID, nodeID *uuid.UUID, what string) (nodeRow, error) {
	nodes, err := s.nodes(ctx)
	if err != nil {
		return nodeRow{}, err
	}
	if nodeID != nil {
		for _, n := range nodes {
			if n.ID == *nodeID {
				if why := n.Ready(); why != "" {
					return nodeRow{}, invalid("%s: %s", what, why)
				}
				return n, nil
			}
		}
		return nodeRow{}, invalid("%s: de gekozen node bestaat niet meer", what)
	}
	if clusterID == nil {
		return nodeRow{}, invalid("%s is niet gekoppeld", what)
	}
	var first *nodeRow
	var why string
	for i, n := range nodes {
		if n.ClusterID == nil || *n.ClusterID != *clusterID {
			continue
		}
		if w := n.Ready(); w != "" {
			why = w
			continue
		}
		if len(n.VIPs) > 0 {
			return n, nil
		}
		if first == nil {
			first = &nodes[i]
		}
	}
	if first != nil {
		return *first, nil
	}
	if why == "" {
		why = "het cluster heeft geen nodes"
	}
	return nodeRow{}, invalid("%s: %s", what, why)
}

// npmVIPNode kiest voor NPM alleen de node met het VIP: NPM draait daar
// alleen (fase 11). Zonder VIP-eigenaar (losse NPM) de gekozen node.
func (s *Service) npmNode(ctx context.Context, st Settings) (nodeRow, error) {
	n, err := s.pickNode(ctx, st.NPMClusterID, st.NPMNodeID, "NPM")
	if err != nil {
		return nodeRow{}, err
	}
	if st.NPMClusterID != nil && len(n.VIPs) == 0 {
		return nodeRow{}, invalid("NPM: geen enkele node van het cluster heeft nu het VIP; NPM draait alleen op de VIP-node")
	}
	return n, nil
}
