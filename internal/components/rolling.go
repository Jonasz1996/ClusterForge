package components

// Een rollende wijziging gaat node voor node over een database die draait,
// zoals nieuwe instellingen of bijwerken: de datanodes op volgnummer met de
// schrijver als laatste, dan de scheidsrechter. De schrijver is bij Galera
// de node met het VIP, bij Patroni de primary. Vooraf moet elke datanode
// meedoen; anders zou ClusterForge een tweede node stilleggen terwijl er
// al een weg is.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// rollNode is een node zoals een rollende wijziging hem ziet.
type rollNode struct {
	node Node
	// state zegt in woorden hoe het met de database op de node staat;
	// healthy is true als hij meedoet. Een scheidsrechter doet mee als zijn
	// agent er is.
	state   string
	healthy bool
	// writer is de node met het VIP bij Galera, de primary bij Patroni.
	writer bool
	// protocol is dat van de agent; 0 zonder agent.
	protocol int
	server   *protocol.DBServer
	err      error
	cand     store.ListComponentCandidatesRow
	facts    *protocol.Facts
}

// rolling leest de nodes van een database en zet ze in de volgorde van een
// rollende wijziging.
func (s *Service) rolling(ctx context.Context, comp store.ClusterComponent, spec Spec) ([]rollNode, error) {
	slug := spec.Cluster.Slug
	cands, err := s.q.ListComponentCandidates(ctx, &comp.ClusterID)
	if err != nil {
		return nil, err
	}
	holders := map[uuid.UUID]bool{}
	if spec.VIP != nil && comp.Engine != protocol.DBPostgreSQL {
		hs, err := s.vipHolder(ctx, spec, s.Now().Add(-s.Fresh))
		if err != nil {
			return nil, err
		}
		for _, h := range hs {
			holders[h.NodeID] = true
		}
	}
	var out []rollNode
	for _, n := range spec.order() {
		r := rollNode{node: n}
		if i := slices.IndexFunc(cands, func(c store.ListComponentCandidatesRow) bool { return c.ID == n.NodeID }); i >= 0 {
			r.cand = cands[i]
			if r.cand.HasAgent {
				r.protocol = int(r.cand.AgentProtocol)
			}
			if len(r.cand.Facts) > 0 {
				var f protocol.Facts
				if json.Unmarshal(r.cand.Facts, &f) == nil {
					r.facts = &f
				}
			}
		}
		if n.Role != RoleData {
			r.healthy = r.cand.HeartbeatAt != nil && s.Now().Sub(*r.cand.HeartbeatAt) <= s.Fresh
			r.state = "scheidsrechter"
			if !r.healthy {
				r.state = "de agent van de scheidsrechter is offline"
			}
			out = append(out, r)
			continue
		}
		r.server, r.err = s.server(ctx, n.NodeID, comp.Engine)
		switch {
		case r.err != nil:
			r.state = "antwoordt niet: " + r.err.Error()
		case comp.Engine == protocol.DBPostgreSQL:
			r.state, r.healthy = patroniHealthy(r.server, slug)
			r.writer = isPrimary(r.server, slug)
		default:
			r.state, r.healthy = galeraState(r.server, slug), r.server != nil && healthy(r.server.Galera, slug, 1)
			r.writer = holders[n.NodeID]
		}
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b rollNode) int {
		switch {
		case a.node.Role != b.node.Role:
			if a.node.Role == RoleData {
				return -1
			}
			return 1
		case a.writer != b.writer:
			if b.writer {
				return -1
			}
			return 1
		}
		return a.node.Index - b.node.Index
	})
	return out, nil
}

// galeraState zegt in woorden hoe het met MariaDB op een node staat.
func galeraState(srv *protocol.DBServer, slug string) string {
	switch {
	case srv == nil:
		return "MariaDB draait niet"
	case !srv.Running:
		return firstNonEmpty(srv.Error, "MariaDB draait niet ("+srv.Active+")")
	case srv.Galera == nil:
		return "MariaDB draait zonder Galera"
	case srv.Galera.ClusterName != slug:
		return "doet mee in cluster " + srv.Galera.ClusterName
	case srv.Galera.ClusterStatus != "Primary":
		return fmt.Sprintf("MariaDB draait, maar zonder meerderheid (%s)", srv.Galera.ClusterStatus)
	}
	return fmt.Sprintf("%s in een cluster van %d", firstNonEmpty(srv.Galera.State, "?"), srv.Galera.ClusterSize)
}

// rollingFindings blokkeert een rollende wijziging als niet elke node
// meedoet, of een agent te oud is. what is wat de wijziging doet, zoals
// "de instellingen wijzigen".
func rollingFindings(nodes []rollNode, engine, what string, add func(level, code string, n *Node, msg, command string)) {
	writers := 0
	for i := range nodes {
		r := &nodes[i]
		if r.writer {
			writers++
		}
		if !r.healthy {
			add(Blocked, "not_healthy", &r.node, fmt.Sprintf("%s doet nu niet mee (%s); eerst moet elke node meedoen voordat ClusterForge %s, anders ligt er straks een tweede node stil",
				r.node.Hostname, r.state, what), "")
		}
		// Overschakelen en herstarten via Patroni vragen protocol 12.
		if engine == protocol.DBPostgreSQL && r.node.Role == RoleData && r.err == nil && r.protocol < protocol.DBPatroniSince {
			add(Blocked, "agent_old", &r.node, fmt.Sprintf("de agent op %s heeft protocol %d; %s vraagt protocol %d. Werk hem bij met het installatiescript en --upgrade",
				r.node.Hostname, r.protocol, what, protocol.DBPatroniSince), "")
		}
	}
	if engine == protocol.DBPostgreSQL && writers == 0 {
		add(Blocked, "no_primary", nil, "er is geen primary; eerst moet PostgreSQL weer schrijven", "")
	}
}

// rollingOrder zijn de nodes van een rollende wijziging, op volgorde.
func rollingOrder(nodes []rollNode) []uuid.UUID {
	out := make([]uuid.UUID, len(nodes))
	for i, r := range nodes {
		out[i] = r.node.NodeID
	}
	return out
}

// ordered zet de nodes van de spec in de volgorde van order: wie er niet
// in staat, komt erachter op de gewone volgorde. Zonder order is het de
// gewone volgorde.
func (s Spec) ordered(order []uuid.UUID) []Node {
	out := s.order()
	if len(order) == 0 {
		return out
	}
	pos := func(n Node) int {
		if i := slices.Index(order, n.NodeID); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(out, func(a, b Node) int { return pos(a) - pos(b) })
	return out
}

// dataNames zijn de hostnames van de datanodes, op volgorde.
func dataNames(nodes []rollNode, writers bool) []string {
	var out []string
	for _, r := range nodes {
		if r.node.Role == RoleData && r.writer == writers {
			out = append(out, r.node.Hostname)
		}
	}
	return out
}
