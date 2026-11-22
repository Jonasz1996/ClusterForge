package databases

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Soorten gebruiker van een database.
const (
	// UserCluster: nodes van een cluster van ClusterForge.
	UserCluster = "cluster"
	// UserNode: een node zonder cluster.
	UserNode = "node"
	// UserUnknown: een adres dat ClusterForge niet kent.
	UserUnknown = "unknown"
	// UserLocal: de databasenode zelf, via de socket of loopback.
	UserLocal = "local"
)

// User is wie een database gebruikt, samengevat over de laatste
// ClientWindow.
type User struct {
	Kind      string
	ClusterID *uuid.UUID
	NodeID    *uuid.UUID
	// Name is de naam van het cluster, de hostname of het adres.
	Name string
	// Nodes zijn de hostnames, of de adressen die ClusterForge niet kent.
	Nodes []string
	// DBUsers zijn de gebruikers in de database.
	DBUsers []string
	// Connections is het aantal verbindingen bij de laatste keer lezen.
	Connections int
	LastSeen    time.Time
	// Dependency is de staat van de afhankelijkheid van dit cluster naar
	// een databasedienst van het databasecluster: confirmed, suggested of
	// ignored, en leeg als er geen is of als het hetzelfde cluster is.
	Dependency string
}

// Describe zegt wie een database gebruikte, zoals "nc-prod (nc1, nc2) als
// nextcloud".
func (u User) Describe() string {
	as := " als " + strings.Join(u.DBUsers, ", ")
	switch u.Kind {
	case UserLocal:
		return "de databasenode zelf (" + strings.Join(u.Nodes, ", ") + ")" + as
	case UserCluster:
		return u.Name + " (" + strings.Join(u.Nodes, ", ") + ")" + as
	case UserUnknown:
		return "het onbekende adres " + u.Name + as
	}
	return u.Name + as
}

// Key is uniek per gebruiker van een database.
func (u User) Key() string {
	switch {
	case u.Kind == UserLocal:
		return UserLocal
	case u.ClusterID != nil:
		return u.ClusterID.String()
	case u.NodeID != nil:
		return u.NodeID.String()
	}
	return "addr/" + u.Name
}

var kindOrder = map[string]int{UserCluster: 0, UserNode: 1, UserUnknown: 2, UserLocal: 3}

// who is wat ClusterForge van de nodes en afhankelijkheden weet om een
// client te herkennen.
type who struct {
	nets   deps.Nets
	nodes  map[uuid.UUID]store.ListDBNodesRow
	byName map[string]store.ListDBNodesRow
	// deps geeft per paar clusters (van, naar) de beste staat.
	deps map[[2]uuid.UUID]string
}

func (s *Service) loadWho(ctx context.Context) (*who, error) {
	nr, err := s.q.ListNodeNets(ctx)
	if err != nil {
		return nil, err
	}
	ns := make([]deps.Net, 0, len(nr))
	for _, r := range nr {
		ns = append(ns, deps.NetOf(r.ID, r.PrimaryIp, r.Addresses, r.Listeners))
	}
	nets := deps.NewNets(ns)
	rows, err := s.q.ListDBNodes(ctx)
	if err != nil {
		return nil, err
	}
	w := &who{nets: nets, nodes: map[uuid.UUID]store.ListDBNodesRow{}, byName: map[string]store.ListDBNodesRow{}, deps: map[[2]uuid.UUID]string{}}
	for _, r := range rows {
		w.nodes[r.ID] = r
		name := strings.ToLower(r.Hostname)
		w.byName[name] = r
		if short, _, ok := strings.Cut(name, "."); ok {
			if _, taken := w.byName[short]; !taken {
				w.byName[short] = r
			}
		}
	}
	dr, err := s.q.ListDBDependencies(ctx)
	if err != nil {
		return nil, err
	}
	rank := map[string]int{deps.StateIgnored: 1, deps.StateSuggested: 2, deps.StateConfirmed: 3}
	for _, d := range dr {
		from, to := cmp.Or(d.FromCluster, d.FromNodeCluster), cmp.Or(d.ToCluster, d.ToNodeCluster)
		if from == nil || to == nil {
			continue
		}
		k := [2]uuid.UUID{*from, *to}
		if rank[d.State] > rank[w.deps[k]] {
			w.deps[k] = d.State
		}
	}
	return w, nil
}

// node zoekt de node bij het adres van een client: een adres van een node,
// of bij MariaDB met naamresolutie een hostname.
func (w *who) node(addr string) (store.ListDBNodesRow, bool) {
	if id, ok := w.nets.NodeAt(addr); ok {
		n, ok := w.nodes[id]
		return n, ok
	}
	n, ok := w.byName[strings.ToLower(addr)]
	return n, ok
}

// users vat de clients van één database in een groep samen.
func (w *who) users(g *Group, clients []store.DbClient) []User {
	byKey := map[string]*User{}
	var order []string
	for _, c := range clients {
		u := User{Kind: UserUnknown, Name: c.Address}
		host := c.Address
		switch n, ok := w.node(c.Address); {
		case c.Address == "":
			u = User{Kind: UserLocal}
			if srv, ok := w.nodes[c.NodeID]; ok {
				host = srv.Hostname
			}
		case ok && n.ClusterID != nil:
			u = User{Kind: UserCluster, ClusterID: n.ClusterID, Name: deref(n.ClusterName)}
			host = n.Hostname
		case ok:
			u = User{Kind: UserNode, NodeID: &n.ID, Name: n.Hostname}
			host = n.Hostname
		}
		k := u.Key()
		have, ok := byKey[k]
		if !ok {
			have = &u
			byKey[k] = have
			order = append(order, k)
		}
		if !slices.Contains(have.Nodes, host) {
			have.Nodes = append(have.Nodes, host)
		}
		if !slices.Contains(have.DBUsers, c.DbUser) {
			have.DBUsers = append(have.DBUsers, c.DbUser)
		}
		have.Connections += int(c.Connections)
		if c.LastSeen.After(have.LastSeen) {
			have.LastSeen = c.LastSeen
		}
	}
	out := make([]User, 0, len(order))
	for _, k := range order {
		u := *byKey[k]
		slices.Sort(u.Nodes)
		slices.Sort(u.DBUsers)
		if u.Kind == UserCluster && g.ClusterID != nil && *u.ClusterID != *g.ClusterID {
			u.Dependency = w.deps[[2]uuid.UUID{*u.ClusterID, *g.ClusterID}]
		}
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b User) int {
		return cmp.Or(cmp.Compare(kindOrder[a.Kind], kindOrder[b.Kind]), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	return out
}

// shared is true als meer dan één cluster, node of onbekend adres de
// database gebruikt; de databasenode zelf telt niet mee.
func shared(users []User) bool {
	n := 0
	for _, u := range users {
		if u.Kind != UserLocal {
			n++
		}
	}
	return n > 1
}

func lastUsed(users []User) *time.Time {
	var last *time.Time
	for _, u := range users {
		if last == nil || u.LastSeen.After(*last) {
			t := u.LastSeen
			last = &t
		}
	}
	return last
}
