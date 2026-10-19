package databases

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Soorten groep.
const (
	KindGalera      = "galera"
	KindPatroni     = "patroni"
	KindReplication = "replication"
	KindSingle      = "single"
)

// Toestand van een groep.
const (
	HealthOK      = "ok"
	HealthWarning = "warning"
	HealthDown    = "down"
	HealthUnknown = "unknown"
)

// Group is één databasecluster: de servers van één engine in één cluster
// van ClusterForge, of één losse node zonder cluster.
type Group struct {
	ClusterID   *uuid.UUID
	ClusterName string
	ClusterSlug string
	ClusterEnv  string
	Engine      string
	Kind        string
	Health      string
	// Summary zegt in één zin hoe het ervoor staat.
	Summary string
	// Version is de versie van de servers, of meer versies met komma's.
	Version string
	Servers []Server
	// Databases komen van DatabasesFrom: bij Galera een node die Synced
	// is, bij replicatie de primary.
	Databases      []Database
	DatabasesFrom  string
	DatabasesTotal int
	// Clients zijn wie de laatste ClientWindow met een server van de groep
	// verbonden was, met welke database ook, ook een die er niet meer is.
	Clients []User
}

// Key is uniek per groep: engine/<cluster> of engine/node/<node> voor een
// losse node. Een verhuizing noemt haar bron zo.
func (g Group) Key() string {
	switch {
	case g.ClusterID != nil:
		return g.Engine + "/" + g.ClusterID.String()
	case len(g.Servers) > 0:
		return g.Engine + "/node/" + g.Servers[0].NodeID.String()
	}
	return g.Engine
}

// Server is een databaseserver op een node.
type Server struct {
	NodeID         uuid.UUID
	Hostname       string
	Unit           string
	Active         string
	Running        bool
	Version        string
	Role           string
	ReadOnly       bool
	Connections    int
	MaxConnections int
	// Port is waar MariaDB luistert; 0 als de agent het niet zegt.
	Port int
	// State zegt in een paar woorden hoe de server ervoor staat, en OK of
	// dat goed is.
	State     string
	OK        bool
	Error     string
	ErrorKind string
	SyncedAt  time.Time
	OkAt      *time.Time

	Galera   *protocol.DBGalera
	Patroni  *protocol.DBPatroni
	Source   *protocol.DBSource
	Replicas []protocol.DBReplica
}

// Database is een database met haar grootte, en wie haar gebruikt.
type Database struct {
	Name      string
	SizeBytes int64
	Tables    int
	Users     []User
	// Shared is true als meer dan één cluster, losse node of onbekend
	// adres de database gebruikt.
	Shared bool
	// LastUsed is de laatste keer dat er een client verbonden was; nil als
	// dat in ClientWindow niet zo was.
	LastUsed *time.Time
}

// EngineName is de naam van een engine voor mensen.
func EngineName(engine string) string {
	switch engine {
	case protocol.DBMariaDB:
		return "MariaDB"
	case protocol.DBPostgreSQL:
		return "PostgreSQL"
	}
	return engine
}

// Overview geeft alle databaseclusters, gegroepeerd per cluster en engine.
func (s *Service) Overview(ctx context.Context) ([]Group, error) {
	rows, err := s.q.ListDBServers(ctx)
	if err != nil {
		return nil, err
	}
	dbRows, err := s.q.ListDBDatabases(ctx)
	if err != nil {
		return nil, err
	}
	type key struct {
		node   uuid.UUID
		engine string
	}
	dbs := map[key][]Database{}
	for _, d := range dbRows {
		k := key{d.NodeID, d.Engine}
		dbs[k] = append(dbs[k], Database{Name: d.Name, SizeBytes: d.SizeBytes, Tables: int(d.Tables)})
	}
	clients, err := s.q.ListDBClients(ctx, s.Now().Add(-ClientWindow))
	if err != nil {
		return nil, err
	}
	w, err := s.loadWho(ctx)
	if err != nil {
		return nil, err
	}
	var groups []Group
	index := map[string]int{}
	totals := map[uuid.UUID]int{}
	for _, r := range rows {
		gk := r.Engine + "/" + r.NodeID.String()
		if r.ClusterID != nil {
			gk = r.Engine + "/" + r.ClusterID.String()
		}
		i, ok := index[gk]
		if !ok {
			g := Group{ClusterID: r.ClusterID, Engine: r.Engine}
			if r.ClusterID != nil {
				g.ClusterName, g.ClusterSlug = deref(r.ClusterName), deref(r.ClusterSlug)
				if r.ClusterEnvironment.Valid {
					g.ClusterEnv = string(r.ClusterEnvironment.Environment)
				}
			}
			groups = append(groups, g)
			i = len(groups) - 1
			index[gk] = i
		}
		srv := server(r)
		totals[srv.NodeID] = int(r.DatabasesTotal)
		groups[i].Servers = append(groups[i].Servers, srv)
	}
	for i := range groups {
		g := &groups[i]
		summarize(g)
		if from := databaseSource(g); from != nil {
			g.Databases = slices.Clone(dbs[key{from.NodeID, g.Engine}])
			g.DatabasesFrom = from.Hostname
			g.DatabasesTotal = max(totals[from.NodeID], len(g.Databases))
		}
		if g.Databases == nil {
			g.Databases = []Database{}
		}
		// De clients van alle servers van de groep, per database.
		perDB := map[string][]store.DbClient{}
		var all []store.DbClient
		for _, c := range clients {
			if c.Engine == g.Engine && slices.ContainsFunc(g.Servers, func(s Server) bool { return s.NodeID == c.NodeID }) {
				perDB[c.DbName] = append(perDB[c.DbName], c)
				all = append(all, c)
			}
		}
		g.Clients = w.users(g, all)
		for j := range g.Databases {
			d := &g.Databases[j]
			d.Users = w.users(g, perDB[d.Name])
			d.Shared, d.LastUsed = shared(d.Users), lastUsed(d.Users)
		}
	}
	return groups, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func server(r store.ListDBServersRow) Server {
	srv := Server{
		NodeID: r.NodeID, Hostname: r.Hostname, Unit: r.Unit, Active: r.Active, Running: r.Running, Version: r.Version,
		Role: r.Role, Error: r.Error, ErrorKind: r.ErrorKind, SyncedAt: r.SyncedAt, OkAt: r.OkAt,
	}
	var st protocol.DBServer
	if err := json.Unmarshal(r.Status, &st); err == nil {
		srv.ReadOnly, srv.Connections, srv.MaxConnections, srv.Port = st.ReadOnly, st.Connections, st.MaxConnections, st.Port
		srv.Galera, srv.Patroni, srv.Source, srv.Replicas = st.Galera, st.Patroni, st.Source, st.Replicas
	}
	// Kon ClusterForge het niet vragen, dan is de laatst bekende toestand
	// niet meer te vertrouwen.
	if srv.ErrorKind != "" {
		srv.Running = false
	}
	srv.State, srv.OK = serverState(srv)
	return srv
}

func serverState(s Server) (string, bool) {
	switch {
	case s.ErrorKind == ErrOldAgent:
		return "agent te oud", false
	case s.ErrorKind == ErrOffline:
		return "agent offline", false
	case s.ErrorKind == ErrNoAnswer:
		return "geen antwoord", false
	case s.ErrorKind != "":
		return "niet te lezen", false
	case !s.Running && s.Active == "failed":
		return "gestopt na een fout", false
	case !s.Running && s.Active != "active":
		return "gestopt", false
	case !s.Running:
		return "antwoordt niet", false
	}
	if g := s.Galera; g != nil {
		if g.ClusterStatus != "Primary" {
			return cmp.Or(g.ClusterStatus, "zonder cluster") + ", " + g.State, false
		}
		return g.State, g.StateCode == 4 && g.Ready
	}
	switch s.Role {
	case protocol.DBRoleReplica:
		src := s.Source
		switch {
		case src == nil || src.Host == "" && !src.Running:
			return "replica zonder bron", false
		case !src.Running:
			return "replica van " + src.Host + ", replicatie staat stil", false
		case src.LagSeconds != nil && *src.LagSeconds > 0:
			return fmt.Sprintf("replica van %s, %d s achter", src.Host, *src.LagSeconds), *src.LagSeconds <= 30
		}
		return "replica van " + src.Host, true
	case protocol.DBRolePrimary:
		return fmt.Sprintf("primary, %d %s", len(s.Replicas), plural(len(s.Replicas), "replica", "replica's")), true
	case protocol.DBRoleMember:
		return "lid", true
	}
	return "draait", true
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// summarize zet Kind, Health, Summary en Version van een groep.
func summarize(g *Group) {
	var versions []string
	running := 0
	oldAgents := 0
	for _, s := range g.Servers {
		if s.Version != "" && !slices.Contains(versions, s.Version) {
			versions = append(versions, s.Version)
		}
		if s.Running {
			running++
		}
		if s.ErrorKind == ErrOldAgent {
			oldAgents++
		}
		switch {
		case s.Galera != nil:
			g.Kind = KindGalera
		case s.Patroni != nil && g.Kind != KindGalera:
			g.Kind = KindPatroni
		case (s.Role == protocol.DBRolePrimary || s.Role == protocol.DBRoleReplica) && g.Kind == "":
			g.Kind = KindReplication
		}
	}
	slices.Sort(versions)
	g.Version = strings.Join(versions, ", ")
	if g.Kind == "" {
		g.Kind = KindSingle
	}
	n := len(g.Servers)
	switch {
	case oldAgents == n:
		g.Health, g.Summary = HealthUnknown, "De agent is te oud om de database te lezen: werk hem bij naar protocol 9."
		return
	case running == 0:
		g.Health, g.Summary = HealthDown, "Geen enkele server antwoordt."
		return
	}
	switch g.Kind {
	case KindGalera:
		summarizeGalera(g)
	case KindPatroni, KindReplication:
		summarizeReplication(g)
	default:
		bad := notOK(g.Servers)
		switch {
		case len(bad) == 0 && n == 1:
			g.Health, g.Summary = HealthOK, "Draait."
		case len(bad) == 0:
			g.Health, g.Summary = HealthOK, fmt.Sprintf("%d losse servers, zonder replicatie.", n)
		default:
			g.Health, g.Summary = HealthWarning, fmt.Sprintf("%d van %d servers draaien; %s.", n-len(bad), n, describe(bad))
		}
	}
}

func notOK(servers []Server) []Server {
	var out []Server
	for _, s := range servers {
		if !s.OK {
			out = append(out, s)
		}
	}
	return out
}

func describe(servers []Server) string {
	parts := make([]string, len(servers))
	for i, s := range servers {
		parts[i] = s.Hostname + ": " + s.State
	}
	return strings.Join(parts, ", ")
}

func summarizeGalera(g *Group) {
	n := len(g.Servers)
	primary, size := 0, 0
	for _, s := range g.Servers {
		if s.Running && s.Galera != nil && s.Galera.ClusterStatus == "Primary" {
			primary++
			size = max(size, s.Galera.ClusterSize)
		}
	}
	bad := notOK(g.Servers)
	synced := n - len(bad)
	switch {
	case primary == 0:
		g.Health = HealthDown
		g.Summary = "Geen node zit in een Primary-component: het cluster neemt geen schrijfacties aan."
	case len(bad) == 0 && size >= n:
		g.Health = HealthOK
		g.Summary = fmt.Sprintf("%d van %d nodes Synced.", synced, n)
	case len(bad) == 0:
		g.Health = HealthWarning
		g.Summary = fmt.Sprintf("%d van %d nodes Synced, maar het cluster telt %d leden.", synced, n, size)
	default:
		g.Health = HealthWarning
		g.Summary = fmt.Sprintf("%d van %d nodes Synced; %s.", synced, n, describe(bad))
	}
}

func summarizeReplication(g *Group) {
	var primaries []string
	for _, s := range g.Servers {
		if s.Running && s.Role == protocol.DBRolePrimary {
			primaries = append(primaries, s.Hostname)
		}
	}
	bad := notOK(g.Servers)
	switch {
	case len(primaries) == 0:
		g.Health, g.Summary = HealthDown, "Er is geen primary: niemand neemt schrijfacties aan."
	case len(primaries) > 1:
		g.Health, g.Summary = HealthDown, "Meer dan één primary: "+strings.Join(primaries, " en ")+"."
	case len(bad) > 0:
		g.Health, g.Summary = HealthWarning, "Primary "+primaries[0]+"; "+describe(bad)+"."
	default:
		reps := len(g.Servers) - 1
		g.Health, g.Summary = HealthOK, fmt.Sprintf("Primary %s met %d %s.", primaries[0], reps, plural(reps, "replica", "replica's"))
	}
}

// databaseSource kiest de server waarvan de lijst met databases komt.
func databaseSource(g *Group) *Server {
	var first *Server
	for i := range g.Servers {
		s := &g.Servers[i]
		if !s.Running {
			continue
		}
		switch {
		case g.Kind == KindGalera && s.OK:
			return s
		case (g.Kind == KindPatroni || g.Kind == KindReplication) && s.Role == protocol.DBRolePrimary:
			return s
		}
		if first == nil {
			first = s
		}
	}
	return first
}
