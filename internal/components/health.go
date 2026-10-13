package components

// De toestand van een database voor de status van het cluster, en wat de
// bewaking onthoudt om een herstart, een failover of een state transfer te
// herkennen. Alles hier rekent alleen; watch.go vraagt de nodes en schrijft.

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// lagLimit: een replica die langer achterloopt, maakt de database
	// verminderd.
	lagLimit = 30
	// observedGap: is de vorige waarneming ouder, dan herkent de bewaking
	// geen herstart of failover. Dan liep er een taak, of stond
	// ClusterForge zelf stil, en is een herstart misschien gepland.
	observedGap = 2 * time.Minute
	// restartSlack is hoeveel seconden de uptime mag afwijken voor het een
	// herstart is: de klokken van server en node lopen niet gelijk.
	restartSlack = 30
)

// dbNode is een datanode zoals de bewaking hem zag.
type dbNode struct {
	node     Node
	answered bool
	running  bool
	// member: Galera zit in een Primary component van dit cluster; bij
	// Patroni hetzelfde als synced.
	member bool
	// synced: Galera is Synced; bij Patroni de primary of een replica die
	// streamt.
	synced  bool
	primary bool
	// state zegt in woorden hoe het met de node staat; code is
	// wsrep_local_state bij Galera.
	state string
	code  int
	size  int
	// uptime is hoe lang de database draait, in seconden; 0 als het niet
	// bekend is.
	uptime int64
	// lag is hoeveel seconden een replica van Patroni achterloopt.
	lag *int64
}

// galeraNodes maakt de waarnemingen van Galera tot dbNodes.
func galeraNodes(all []obs, slug string) []dbNode {
	out := make([]dbNode, len(all))
	for i, o := range all {
		d := dbNode{node: o.node, answered: o.answered(), member: o.primary(slug), synced: o.synced(slug), state: o.describe(slug)}
		if d.answered {
			d.running, d.uptime = o.pos.Running, o.pos.UptimeSeconds
			if g := o.pos.Galera; g != nil && o.pos.Running {
				d.code, d.size = g.StateCode, g.ClusterSize
			}
		}
		out[i] = d
	}
	return out
}

// patroniNode maakt de snelle status van een node met Patroni tot een
// dbNode.
func patroniNode(n Node, srv *protocol.DBServer, err error, slug string) dbNode {
	d := dbNode{node: n}
	if err != nil {
		d.state = "antwoordt niet: " + err.Error()
		return d
	}
	d.answered = true
	d.state, d.synced = patroniHealthy(srv, slug)
	if srv == nil {
		return d
	}
	d.running, d.uptime = srv.Running, srv.UptimeSeconds
	d.member = d.synced
	d.primary = d.synced && srv.Patroni.Role == "primary"
	if srv.Source != nil && srv.Source.LagSeconds != nil {
		d.lag = srv.Source.LagSeconds
	}
	return d
}

// up is true als de database draait: bij Galera zit er een node in een
// Primary component, bij Patroni is er een primary.
func up(engine string, nodes []dbNode) bool {
	return slices.ContainsFunc(nodes, func(d dbNode) bool {
		if engine == protocol.DBPostgreSQL {
			return d.primary
		}
		return d.member
	})
}

// healthInput is wat dbHealth naast de nodes nodig heeft.
type healthInput struct {
	engine string
	spec   Spec
	nodes  []dbNode
	// holders zijn de nodes met het VIP van de database volgens hun
	// heartbeats; leeg zonder VIP of als het nergens staat (dat zegt de
	// status van het cluster al).
	holders []string
	// arbitersDown zijn de scheidsrechters waarvan de agent offline is.
	arbitersDown []string
	// stablePrimary is de primary van Patroni als die sinds de vorige
	// waarneming dezelfde is; leeg net na een wissel, als het VIP nog
	// onderweg is.
	stablePrimary string
}

// dbHealth zegt hoe het met de database gaat, met de reden. De reden begint
// met de naam van de database, zoals de status van het cluster hem toont.
func dbHealth(in healthInput) (status.Status, string) {
	name := title(in.spec)
	byHost := map[string]dbNode{}
	for _, d := range in.nodes {
		byHost[d.node.Hostname] = d
	}
	describe := func() string {
		parts := make([]string, len(in.nodes))
		for i, d := range in.nodes {
			parts[i] = d.node.Hostname + ": " + d.state
		}
		return strings.Join(parts, "; ")
	}
	pg := in.engine == protocol.DBPostgreSQL
	if !up(in.engine, in.nodes) {
		if pg {
			return status.Down, name + ": er is geen primary; " + describe()
		}
		return status.Down, name + ": geen enkele node zit in een Primary component; " + describe()
	}

	// Een VIP zonder database erachter: de app kan niet schrijven.
	if in.spec.VIP != nil && len(in.holders) == 1 {
		h := in.holders[0]
		d, ok := byHost[h]
		switch {
		case !ok:
		case pg && in.stablePrimary != "" && in.stablePrimary != h:
			return status.Down, fmt.Sprintf("%s: het VIP %s staat op %s, maar de primary is %s", name, in.spec.VIP.Address, h, in.stablePrimary)
		case !pg && !d.synced:
			return status.Down, fmt.Sprintf("%s: het VIP %s staat op %s, waar MariaDB niet meedoet (%s)", name, in.spec.VIP.Address, h, d.state)
		}
	}

	var problems []string
	size := 0
	for _, d := range in.nodes {
		size = max(size, d.size)
		switch {
		case !d.synced && d.member:
			// Een donor of een node die nog bijhaalt: in het cluster, maar
			// nog niet Synced.
			problems = append(problems, fmt.Sprintf("%s is niet Synced (%s)", d.node.Hostname, d.state))
		case !d.synced:
			problems = append(problems, fmt.Sprintf("%s doet niet mee (%s)", d.node.Hostname, d.state))
		case pg && d.lag != nil && *d.lag > lagLimit:
			problems = append(problems, fmt.Sprintf("%s loopt %d s achter", d.node.Hostname, *d.lag))
		}
	}
	arbiters := in.spec.hosts(RoleArbiter)
	switch {
	case len(in.arbitersDown) > 0:
		problems = append(problems, fmt.Sprintf("de scheidsrechter %s is offline", joinHosts(in.arbitersDown)))
	case !pg && len(problems) == 0 && size > 0 && size < len(in.spec.Nodes) && len(arbiters) > 0:
		// Alle datanodes doen mee, maar Galera telt er een te weinig.
		problems = append(problems, fmt.Sprintf("de scheidsrechter %s doet niet mee: Galera telt %d van de %d leden", joinHosts(arbiters), size, len(in.spec.Nodes)))
	}
	if len(problems) > 0 {
		return status.Degraded, name + ": " + strings.Join(problems, "; ")
	}
	return status.Healthy, ""
}

// observed is wat de bewaking de vorige keer zag.
type observed struct {
	At time.Time `json:"at"`
	// Primary is de hostname van de primary van Patroni.
	Primary string `json:"primary,omitempty"`
	// Nodes zijn de datanodes per id.
	Nodes map[string]seenNode `json:"nodes,omitempty"`
}

type seenNode struct {
	Hostname string `json:"hostname"`
	Running  bool   `json:"running"`
	Uptime   int64  `json:"uptime,omitempty"`
	// Code is wsrep_local_state bij Galera.
	Code int `json:"code,omitempty"`
}

// observe onthoudt wat de bewaking nu ziet.
func observeNodes(engine string, nodes []dbNode, now time.Time) observed {
	o := observed{At: now, Nodes: map[string]seenNode{}}
	for _, d := range nodes {
		if !d.answered {
			continue
		}
		o.Nodes[d.node.NodeID.String()] = seenNode{Hostname: d.node.Hostname, Running: d.running, Uptime: d.uptime, Code: d.code}
		if engine == protocol.DBPostgreSQL && d.primary {
			o.Primary = d.node.Hostname
		}
	}
	return o
}

// watchEvent is een gebeurtenis die de bewaking in het logboek zet.
type watchEvent struct {
	action  string
	payload map[string]any
}

// changes vergelijkt wat de bewaking nu ziet met de vorige keer: een
// database die herstartte, een failover van Patroni, en een Galera-node
// die een state transfer krijgt. Is de vorige waarneming te oud, dan niets.
func changes(engine string, prev, cur observed) []watchEvent {
	if prev.At.IsZero() || cur.At.Sub(prev.At) > observedGap {
		return nil
	}
	elapsed := int64(cur.At.Sub(prev.At).Seconds())
	var out []watchEvent
	server := "MariaDB"
	if engine == protocol.DBPostgreSQL {
		server = "PostgreSQL"
	}
	ids := slices.Collect(maps.Keys(cur.Nodes))
	slices.SortFunc(ids, func(a, b string) int { return cmp.Compare(cur.Nodes[a].Hostname, cur.Nodes[b].Hostname) })
	for _, id := range ids {
		c, p := cur.Nodes[id], prev.Nodes[id]
		known := prev.Nodes != nil && p.Hostname != ""
		if known && p.Running && c.Running && p.Uptime > 0 && c.Uptime > 0 && c.Uptime+restartSlack < p.Uptime+elapsed {
			out = append(out, watchEvent{"cluster.database_restarted", map[string]any{"hostname": c.Hostname, "server": server, "uptime": c.Uptime}})
		}
		if engine != protocol.DBPostgreSQL && c.Code == galeraJoining && (!known || p.Code != galeraJoining) {
			donors := []string{}
			for _, o := range cur.Nodes {
				if o.Code == galeraDonor {
					donors = append(donors, o.Hostname)
				}
			}
			slices.Sort(donors)
			out = append(out, watchEvent{"cluster.database_state_transfer", map[string]any{"hostname": c.Hostname, "donors": donors}})
		}
	}
	if engine == protocol.DBPostgreSQL && prev.Primary != "" && cur.Primary != "" && prev.Primary != cur.Primary {
		out = append(out, watchEvent{"cluster.database_failover", map[string]any{"from": prev.Primary, "to": cur.Primary}})
	}
	return out
}

// De toestanden van een Galera-node in wsrep_local_state.
const (
	galeraJoining = 1
	galeraDonor   = 2
)
