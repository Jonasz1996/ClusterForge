package components

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// healthNodes maakt de datanodes van de spec, allemaal Synced in een
// cluster van size; pas ze daarna aan per geval.
func healthNodes(spec Spec, engine string, size int) []dbNode {
	var out []dbNode
	for i, n := range spec.Nodes {
		if n.Role != RoleData {
			continue
		}
		d := dbNode{node: n, answered: true, running: true, member: true, synced: true, uptime: 3600}
		if engine == protocol.DBPostgreSQL {
			d.primary = i == 0
			d.state = "replica, running, streaming"
			if d.primary {
				d.state = "primary, running"
			}
		} else {
			d.code, d.size, d.state = 4, size, "Synced in een cluster van 3"
		}
		out = append(out, d)
	}
	return out
}

func patroniSpec(t *testing.T) Spec {
	t.Helper()
	_, spec, _ := galeraSpec(t, false)
	tpl, ok := registry.Latest("postgresql-patroni")
	if !ok {
		t.Fatal("postgresql-patroni ontbreekt")
	}
	spec.Template.Name, spec.Template.Version = tpl.Name, tpl.Version
	return spec
}

func withVIP(spec Spec) Spec {
	spec.VIP = &VIP{ID: uuid.New(), Address: "10.0.20.185"}
	return spec
}

func TestDBHealthGalera(t *testing.T) {
	_, spec, _ := galeraSpec(t, false)
	_, arb, _ := galeraSpec(t, true)
	cases := []struct {
		name   string
		in     healthInput
		edit   func([]dbNode)
		status status.Status
		reason string
	}{
		{name: "gezond", in: healthInput{spec: withVIP(spec), holders: []string{"nc1"}}, status: status.Healthy},
		{
			name: "nc3 doet niet mee", in: healthInput{spec: spec},
			edit: func(n []dbNode) {
				n[2].member, n[2].synced, n[2].running, n[2].code, n[2].state = false, false, false, 0, "MariaDB staat uit"
				n[0].size, n[1].size = 2, 2
			},
			status: status.Degraded, reason: "MariaDB Galera: nc3 doet niet mee (MariaDB staat uit)",
		},
		{
			name: "geen Primary component", in: healthInput{spec: spec},
			edit: func(n []dbNode) {
				for i := range n {
					n[i].member, n[i].synced, n[i].state = false, false, "MariaDB draait, maar zonder meerderheid (non-Primary)"
				}
			},
			status: status.Down, reason: "MariaDB Galera: geen enkele node zit in een Primary component; nc1: MariaDB draait",
		},
		{
			name: "VIP op een node die niet meedoet", in: healthInput{spec: withVIP(spec), holders: []string{"nc3"}},
			edit: func(n []dbNode) {
				n[2].synced, n[2].code, n[2].state = false, 1, "Joining in een cluster van 3"
			},
			status: status.Down, reason: "MariaDB Galera: het VIP 10.0.20.185 staat op nc3, waar MariaDB niet meedoet (Joining in een cluster van 3)",
		},
		{
			name: "VIP op twee nodes zegt de status van het cluster al", in: healthInput{spec: withVIP(spec), holders: []string{"nc1", "nc3"}},
			edit:   func(n []dbNode) { n[2].synced, n[2].state = false, "Joining" },
			status: status.Degraded, reason: "MariaDB Galera: nc3 is niet Synced (Joining)",
		},
		{name: "scheidsrechter doet mee", in: healthInput{spec: arb}, status: status.Healthy},
		{
			name: "scheidsrechter doet niet mee", in: healthInput{spec: arb},
			edit:   func(n []dbNode) { n[0].size, n[1].size = 2, 2 },
			status: status.Degraded, reason: "MariaDB Galera: de scheidsrechter nc3 doet niet mee: Galera telt 2 van de 3 leden",
		},
		{
			name: "scheidsrechter offline", in: healthInput{spec: arb, arbitersDown: []string{"nc3"}},
			edit:   func(n []dbNode) { n[0].size, n[1].size = 2, 2 },
			status: status.Degraded, reason: "MariaDB Galera: de scheidsrechter nc3 is offline",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.in.engine = protocol.DBMariaDB
			c.in.nodes = healthNodes(c.in.spec, protocol.DBMariaDB, len(c.in.spec.Nodes))
			if c.edit != nil {
				c.edit(c.in.nodes)
			}
			st, reason := dbHealth(c.in)
			if st != c.status || !strings.HasPrefix(reason, c.reason) || (c.reason == "" && reason != "") {
				t.Fatalf("dbHealth = %s %q, wil %s %q", st, reason, c.status, c.reason)
			}
		})
	}
}

func TestDBHealthPatroni(t *testing.T) {
	spec := patroniSpec(t)
	lag := func(s int64) *int64 { return &s }
	cases := []struct {
		name   string
		in     healthInput
		edit   func([]dbNode)
		status status.Status
		reason string
	}{
		{name: "gezond", in: healthInput{spec: withVIP(spec), holders: []string{"nc1"}, stablePrimary: "nc1"}, status: status.Healthy},
		{
			name: "geen primary", in: healthInput{spec: spec},
			edit: func(n []dbNode) {
				n[0].primary, n[0].member, n[0].synced, n[0].answered, n[0].state = false, false, false, false, "antwoordt niet: offline"
			},
			status: status.Down, reason: "PostgreSQL met Patroni: er is geen primary; nc1: antwoordt niet: offline; nc2: replica",
		},
		{
			name: "replica loopt achter", in: healthInput{spec: spec},
			edit:   func(n []dbNode) { n[1].lag, n[2].lag = lag(45), lag(30) },
			status: status.Degraded, reason: "PostgreSQL met Patroni: nc2 loopt 45 s achter",
		},
		{
			name: "replica streamt niet", in: healthInput{spec: spec},
			edit:   func(n []dbNode) { n[2].member, n[2].synced, n[2].state = false, false, "replica, starting" },
			status: status.Degraded, reason: "PostgreSQL met Patroni: nc3 doet niet mee (replica, starting)",
		},
		{
			name: "VIP niet op de primary", in: healthInput{spec: withVIP(spec), holders: []string{"nc2"}, stablePrimary: "nc1"},
			status: status.Down, reason: "PostgreSQL met Patroni: het VIP 10.0.20.185 staat op nc2, maar de primary is nc1",
		},
		{
			// Net na een failover is het VIP nog onderweg.
			name: "VIP onderweg na een failover", in: healthInput{spec: withVIP(spec), holders: []string{"nc2"}},
			status: status.Healthy,
		},
		{
			name: "scheidsrechter offline", in: healthInput{spec: spec, arbitersDown: []string{"pgarb"}},
			status: status.Degraded, reason: "PostgreSQL met Patroni: de scheidsrechter pgarb is offline",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.in.engine = protocol.DBPostgreSQL
			c.in.nodes = healthNodes(c.in.spec, protocol.DBPostgreSQL, 0)
			if c.edit != nil {
				c.edit(c.in.nodes)
			}
			st, reason := dbHealth(c.in)
			if st != c.status || !strings.HasPrefix(reason, c.reason) || (c.reason == "" && reason != "") {
				t.Fatalf("dbHealth = %s %q, wil %s %q", st, reason, c.status, c.reason)
			}
		})
	}
}

func TestPatroniNode(t *testing.T) {
	n := Node{NodeID: uuid.New(), Hostname: "nc2"}
	lag := int64(4)
	srv := &protocol.DBServer{Running: true, UptimeSeconds: 99,
		Patroni: &protocol.DBPatroni{Scope: "nextcloud", Role: "replica", State: "running", Replication: "streaming"},
		Source:  &protocol.DBSource{Host: "nc1", Running: true, LagSeconds: &lag},
	}
	d := patroniNode(n, srv, nil, "nextcloud")
	if !d.answered || !d.running || !d.member || !d.synced || d.primary || d.uptime != 99 || d.lag == nil || *d.lag != 4 {
		t.Fatalf("replica: %+v", d)
	}
	srv.Patroni.Role, srv.Source = "primary", nil
	if d := patroniNode(n, srv, nil, "nextcloud"); !d.primary || d.lag != nil {
		t.Fatalf("primary: %+v", d)
	}
	if d := patroniNode(n, srv, nil, "anders"); d.synced || d.primary {
		t.Fatalf("ander cluster: %+v", d)
	}
	if d := patroniNode(n, nil, errors.New("offline"), "nextcloud"); d.answered || d.state != "antwoordt niet: offline" {
		t.Fatalf("offline: %+v", d)
	}
}

func TestChanges(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	at := func(at time.Time, primary string, nodes ...seenNode) observed {
		o := observed{At: at, Primary: primary, Nodes: map[string]seenNode{}}
		for i, n := range nodes {
			o.Nodes[ids[i]] = n
		}
		return o
	}
	up := func(host string, uptime int64, code int) seenNode {
		return seenNode{Hostname: host, Running: true, Uptime: uptime, Code: code}
	}
	prev := at(t0, "", up("nc1", 1000, 4), up("nc2", 1000, 4), up("nc3", 1000, 4))
	cases := []struct {
		name   string
		engine string
		prev   observed
		cur    observed
		want   []watchEvent
	}{
		{name: "eerste keer", engine: protocol.DBMariaDB, cur: at(t0, "", up("nc1", 5, 4))},
		{name: "niets veranderd", engine: protocol.DBMariaDB, prev: prev, cur: at(t0.Add(20*time.Second), "", up("nc1", 1020, 4), up("nc2", 1021, 4), up("nc3", 1019, 4))},
		{
			name: "herstart", engine: protocol.DBMariaDB, prev: prev,
			cur:  at(t0.Add(20*time.Second), "", up("nc1", 1020, 4), up("nc2", 8, 4), up("nc3", 1020, 4)),
			want: []watchEvent{{"cluster.database_restarted", map[string]any{"hostname": "nc2", "server": "MariaDB", "uptime": int64(8)}}},
		},
		{
			name: "te lang niet gekeken", engine: protocol.DBMariaDB, prev: prev,
			cur: at(t0.Add(3*time.Minute), "", up("nc1", 1180, 4), up("nc2", 8, 4), up("nc3", 1180, 4)),
		},
		{
			// Stond MariaDB stil, dan zegt node_back het al.
			name: "start na stilstand", engine: protocol.DBMariaDB,
			prev: at(t0, "", up("nc1", 1000, 4), up("nc2", 1000, 4), seenNode{Hostname: "nc3"}),
			cur:  at(t0.Add(20*time.Second), "", up("nc1", 1020, 4), up("nc2", 1020, 4), up("nc3", 10, 4)),
		},
		{
			name: "state transfer", engine: protocol.DBMariaDB, prev: prev,
			cur: at(t0.Add(20*time.Second), "", up("nc1", 1020, 2), up("nc2", 1020, 4), up("nc3", 15, 1)),
			want: []watchEvent{
				{"cluster.database_restarted", map[string]any{"hostname": "nc3", "server": "MariaDB", "uptime": int64(15)}},
				{"cluster.database_state_transfer", map[string]any{"hostname": "nc3", "donors": []string{"nc1"}}},
			},
		},
		{
			name: "state transfer loopt nog", engine: protocol.DBMariaDB,
			prev: at(t0, "", up("nc1", 1000, 2), up("nc2", 1000, 4), up("nc3", 15, 1)),
			cur:  at(t0.Add(20*time.Second), "", up("nc1", 1020, 2), up("nc2", 1020, 4), up("nc3", 35, 1)),
		},
		{
			name: "failover", engine: protocol.DBPostgreSQL,
			prev: at(t0, "nc1", up("nc1", 1000, 0), up("nc2", 1000, 0)),
			cur:  at(t0.Add(20*time.Second), "nc2", seenNode{Hostname: "nc1"}, up("nc2", 1020, 0)),
			want: []watchEvent{{"cluster.database_failover", map[string]any{"from": "nc1", "to": "nc2"}}},
		},
		{
			name: "herstart van PostgreSQL", engine: protocol.DBPostgreSQL,
			prev: at(t0, "nc1", up("nc1", 1000, 0), up("nc2", 1000, 0)),
			cur:  at(t0.Add(20*time.Second), "nc1", up("nc1", 1020, 0), up("nc2", 3, 0)),
			want: []watchEvent{{"cluster.database_restarted", map[string]any{"hostname": "nc2", "server": "PostgreSQL", "uptime": int64(3)}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := changes(c.engine, c.prev, c.cur)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("changes = %+v, wil %+v", got, c.want)
			}
		})
	}
}

func TestObserveNodes(t *testing.T) {
	spec := patroniSpec(t)
	nodes := healthNodes(spec, protocol.DBPostgreSQL, 0)
	nodes[2].answered = false
	o := observeNodes(protocol.DBPostgreSQL, nodes, time.Unix(100, 0))
	if o.Primary != "nc1" || len(o.Nodes) != 2 || o.Nodes[spec.Nodes[1].NodeID.String()].Uptime != 3600 {
		t.Fatalf("observeNodes = %+v", o)
	}
}
