package components

// Een database verhuizen naar de database van een cluster: in mijlpaal 8
// van fase 5 het plan en de proefverhuizing, in mijlpaal 9 de echte, en in
// mijlpaal 10 hetzelfde voor PostgreSQL.
//
// De bron is een MariaDB of PostgreSQL die ClusterForge onder Databases
// ziet: een cluster of een losse node. Daar maakt ClusterForge een
// tijdelijke gebruiker die alleen de dump mag maken, en de nieuwe node
// haalt de dump zelf op en importeert hem meteen; er komt nergens een
// bestand. Het wachtwoord van die gebruiker maakt ClusterForge per poging
// en bewaart het nergens; na afloop is de gebruiker weg. Bij PostgreSQL is
// hij lid van de eigenaar van de database en mag hij alleen lezen; de
// nieuwe node is de primary in Patroni.
//
// De proef importeert in <database>_proef terwijl de app doorwerkt, telt
// de rijen aan beide kanten, meet hoe lang het duurde en haalt de kopie
// weer weg. Aan de oude database verandert niets. De echte verhuizing staat
// in move.go.

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// MigrateTrialKind is de proefverhuizing.
	MigrateTrialKind = "component.migrate_trial"
	// trialSuffix: de proef importeert in <database>_proef.
	trialSuffix = "_proef"
	// Hoe de nieuwe node de oude server bereikt: rechtstreeks of via een
	// VIP van het oude cluster.
	ViaNode = "node"
	ViaVIP  = "vip"
)

// ErrSourceNotFound: ClusterForge ziet geen databaseserver met die sleutel.
var ErrSourceNotFound = errors.New("die databaseserver kent ClusterForge niet")

var mariaVersionRe = regexp.MustCompile(`^(\d+)\.(\d+)`)

// MigrationSource is de oude server.
type MigrationSource struct {
	// Key is de sleutel van de groep onder Databases.
	Key       string
	Name      string
	ClusterID *uuid.UUID
	// Slug en Environment van dat cluster; leeg bij een losse node.
	Slug        string
	Environment string
	Engine      string
	Kind        string
	Version     string
	Summary     string
	Servers     []string
	// Node is de server waarop ClusterForge de database bekijkt en de
	// tijdelijke gebruiker maakt; nil als er geen geschikte draait.
	Node *Node
	// Address en Port zijn waarmee de nieuwe node de oude server bereikt,
	// Via of dat rechtstreeks is of via een VIP; leeg zolang het niet
	// bekeken is.
	Address string
	Port    int
	Via     string
}

// MigrationTarget is de database van een cluster waar de data naartoe
// gaat.
type MigrationTarget struct {
	ClusterID   uuid.UUID
	ClusterName string
	Slug        string
	Environment string
	ComponentID uuid.UUID
	// Node importeert; Version is zijn MariaDB.
	Node    Node
	Version string
	// Address is waar de app de database straks vindt.
	Address string
	Port    int
	// Hosts zijn de adressen waarvandaan de tijdelijke gebruiker mag
	// inloggen.
	Hosts []string
	// BufferPoolBytes is de buffer pool, DataBytes wat er al aan databases
	// staat.
	BufferPoolBytes int64
	DataBytes       int64
	Disks           []MigrationDisk
	// Exists: de database staat er al.
	Exists bool
}

// MigrationDisk is de vrije ruimte voor MariaDB op een node.
type MigrationDisk struct {
	Hostname  string
	Mount     string
	FreeBytes int64
}

// MigrationOption is een database van een cluster die de data kan krijgen.
// Suggested: de app van dat cluster gebruikt de database nu al.
type MigrationOption struct {
	ClusterID   uuid.UUID
	ClusterName string
	ComponentID uuid.UUID
	Suggested   bool
}

// MigrationPlan is wat een verhuizing zou doen. Er verandert niets.
type MigrationPlan struct {
	Source   MigrationSource
	Database string
	// Info is wat de oude server over de database weet; nil als hij niet
	// te lezen was.
	Info *protocol.DBMigrateInfo
	// Clients zijn wie de database de laatste 30 dagen gebruikte.
	Clients []databases.User
	Targets []MigrationOption
	// Target is nil zolang er niets gekozen is.
	Target *MigrationTarget
	// Reach is wat de nieuwe node op het adres van de oude zag.
	Reach    *protocol.DBMigrateReach
	Findings []Finding
	// Blocked: de proef kan niet.
	Blocked bool
	// Voor de echte verhuizing: de gebruikers met rechten op de database op
	// de oude server (zonder hash), de hosts die ze op het nieuwe cluster
	// krijgen, wie nu met de oude database verbonden is, en wat alleen de
	// verhuizing aangaat. MoveBlocked: de verhuizing kan niet.
	Accounts     []protocol.DBMigrateAccount
	NewHosts     []string
	Connections  []MigrationConnection
	MoveFindings []Finding
	MoveBlocked  bool
	// Runs zijn de vorige proeven en verhuizingen, de nieuwste eerst.
	Runs []store.ListDBMigrationsRow
	// Active is een proef of verhuizing die nu kopieert.
	Active *ActiveJob
}

// trialOnly zijn de bevindingen die alleen de proef aangaan: haar kopie
// <database>_proef.
var trialOnly = []string{"trial_exists", "trial_running", "trial_leftover"}

// TrialName is de database van een proef: <database>_proef, zo nodig
// ingekort tot 63 tekens.
func TrialName(database string) string {
	return database[:min(len(database), 63-len(trialSuffix))] + trialSuffix
}

// trialUser is de tijdelijke gebruiker van een taak: herkenbaar, en kort
// genoeg voor elke MariaDB en MySQL.
func trialUser(jobID uuid.UUID) string {
	return protocol.DBMigrateUserPrefix + strings.ReplaceAll(jobID.String(), "-", "")[:16]
}

// migrateReq stuurt een DBMigrate voor een database van engine naar een
// node.
func (s *Service) migrateReq(ctx context.Context, n Node, engine string, req protocol.DBMigrateRequest) (*protocol.DBMigrateResult, error) {
	if engine == protocol.DBPostgreSQL {
		if err := s.pgMigrateAgent(ctx, n); err != nil {
			return nil, err
		}
	}
	res, err := s.dbRequest(ctx, dbTarget{node: n, engine: engine}, protocol.DBRequest{Op: protocol.DBMigrate, Migrate: &req})
	if err != nil {
		// Bij adopt van PostgreSQL staan daar de rollen die al gemaakt zijn.
		return res.Migrate, err
	}
	if res.Migrate == nil {
		return nil, &ConflictError{n.Hostname + ": de agent gaf geen antwoord over de verhuizing"}
	}
	return res.Migrate, nil
}

// pgMigrateAgent zegt waarom de agent op n geen database van PostgreSQL kan
// verhuizen: een oudere agent kent het veld Engine van DBMigrate niet en
// zou het verzoek op MariaDB uitvoeren.
func (s *Service) pgMigrateAgent(ctx context.Context, n Node) error {
	row, err := s.q.GetNode(ctx, n.NodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return &ConflictError{"ClusterForge kent de node " + n.Hostname + " niet meer"}
	}
	if err != nil {
		return err
	}
	return pgMigrateProtocol(n.Hostname, int(deref(row.AgentProtocol)))
}

func pgMigrateProtocol(hostname string, v int) error {
	if v < protocol.DBPGMigrateSince {
		return &ConflictError{fmt.Sprintf("de agent op %s heeft protocol %d; een database van PostgreSQL verhuizen vraagt protocol %d. "+
			"Werk hem bij met het installatiescript en --upgrade", hostname, v, protocol.DBPGMigrateSince)}
	}
	return nil
}

// MigrationPlan bekijkt hoe database van de server source naar het
// onderdeel componentID zou verhuizen; zonder componentID kiest het plan
// het cluster waarvan de app de database nu al gebruikt, als dat er één
// is.
func (s *Service) MigrationPlan(ctx context.Context, source, database string, componentID *uuid.UUID) (MigrationPlan, error) {
	p, _, err := s.migrationPlan(ctx, source, database, componentID, uuid.Nil)
	return p, err
}

// migrationRoute is wat de proef uit het plan nodig heeft. Engine is leeg
// bij MariaDB, zoals in een taak van voor PostgreSQL.
type migrationRoute struct {
	Engine     string   `json:"engine,omitempty"`
	SourceNode Node     `json:"source_node"`
	Address    string   `json:"address"`
	Port       int      `json:"port"`
	Via        string   `json:"via"`
	Hosts      []string `json:"hosts"`
	Import     Node     `json:"import"`
	Target     string   `json:"target"`
	Charset    string   `json:"charset"`
	Collation  string   `json:"collation"`
	Size       int64    `json:"size"`
	Tables     []string `json:"tables"`
	Views      int      `json:"views"`
	Routines   int      `json:"routines"`
	Triggers   int      `json:"triggers"`
	Events     int      `json:"events"`
	// Leftover: <database>_proef staat er nog van een vorige proef.
	Leftover bool `json:"leftover"`
	// Voor de verhuizing: de servers van de oude database die draaien, de
	// nodes van het nieuwe cluster, waar de app de nieuwe database vindt,
	// en de hosts van de gebruikers daar.
	Servers       []Node   `json:"servers,omitempty"`
	DataNodes     []Node   `json:"data_nodes,omitempty"`
	TargetAddress string   `json:"target_address,omitempty"`
	NewHosts      []string `json:"new_hosts,omitempty"`
	// Bij PostgreSQL: de eigenaar van de database, en Move bij de echte
	// verhuizing: dan krijgt de nieuwe database die eigenaar en neemt de
	// dump de eigenaars en rechten mee.
	Owner string `json:"owner,omitempty"`
	Move  bool   `json:"move,omitempty"`
}

// engine is de engine van de route.
func (r *migrationRoute) engine() string { return cmp.Or(r.Engine, protocol.DBMariaDB) }

// pg zegt of de route een database van PostgreSQL verhuist.
func (r *migrationRoute) pg() bool { return r.Engine == protocol.DBPostgreSQL }

// migrationPlan is MigrationPlan met wat de proef nodig heeft; self is de
// proef die het plan opnieuw bekijkt, of uuid.Nil.
func (s *Service) migrationPlan(ctx context.Context, source, database string, componentID *uuid.UUID, self uuid.UUID) (MigrationPlan, migrationRoute, error) {
	p, route, err := s.planMigration(ctx, source, database, componentID, self)
	p.Blocked = slices.ContainsFunc(p.Findings, func(f Finding) bool { return f.Level == Blocked })
	p.MoveBlocked = len(moveBlockers(p)) > 0
	return p, route, err
}

// moveBlockers zijn de redenen waarom de verhuizing niet kan.
func moveBlockers(p MigrationPlan) []string {
	var out []string
	for _, f := range p.Findings {
		if f.Level == Blocked && !slices.Contains(trialOnly, f.Code) {
			out = append(out, f.Message)
		}
	}
	for _, f := range p.MoveFindings {
		if f.Level == Blocked {
			out = append(out, f.Message)
		}
	}
	return out
}

func (s *Service) planMigration(ctx context.Context, source, database string, componentID *uuid.UUID, self uuid.UUID) (MigrationPlan, migrationRoute, error) {
	var route migrationRoute
	if dbsql.CheckDatabase(database) != nil && dbsql.CheckPGDatabase(database) != nil {
		return MigrationPlan{}, route, &InvalidError{"database", dbsql.CheckDatabase(database).Error()}
	}
	if s.Overview == nil {
		return MigrationPlan{}, route, errors.New("de server leest de databases niet")
	}
	groups, err := s.Overview(ctx)
	if err != nil {
		return MigrationPlan{}, route, err
	}
	gi := slices.IndexFunc(groups, func(g databases.Group) bool { return g.Key() == source })
	if gi < 0 {
		return MigrationPlan{}, route, ErrSourceNotFound
	}
	g := groups[gi]
	if err := dbsql.CheckDatabaseOf(g.Engine, database); err != nil {
		return MigrationPlan{}, route, &InvalidError{"database", err.Error()}
	}
	pg := g.Engine == protocol.DBPostgreSQL
	port := enginePort(g.Engine)
	p := MigrationPlan{
		Database: database, Clients: []databases.User{}, Targets: []MigrationOption{}, Findings: []Finding{},
		Accounts: []protocol.DBMigrateAccount{}, NewHosts: []string{}, Connections: []MigrationConnection{}, MoveFindings: []Finding{},
	}
	p.Source = MigrationSource{
		Key: source, Name: g.ClusterName, ClusterID: g.ClusterID, Slug: g.ClusterSlug, Environment: g.ClusterEnv, Engine: g.Engine, Kind: g.Kind,
		Version: g.Version, Summary: g.Summary, Port: port, Servers: []string{},
	}
	for _, sv := range g.Servers {
		p.Source.Servers = append(p.Source.Servers, sv.Hostname)
	}
	if p.Source.Name == "" && len(g.Servers) > 0 {
		p.Source.Name = g.Servers[0].Hostname
	}
	finding := func(level, code string, n *Node, msg, command string) Finding {
		f := Finding{Level: level, Code: code, Message: msg, Command: command}
		if n != nil {
			f.NodeID, f.Hostname = &n.NodeID, n.Hostname
		}
		return f
	}
	add := func(level, code string, n *Node, msg, command string) {
		p.Findings = append(p.Findings, finding(level, code, n, msg, command))
	}
	addMove := func(level, code string, n *Node, msg, command string) {
		p.MoveFindings = append(p.MoveFindings, finding(level, code, n, msg, command))
	}
	if i := slices.IndexFunc(g.Databases, func(d databases.Database) bool { return d.Name == database }); i >= 0 {
		p.Clients = g.Databases[i].Users
	}
	runs, err := s.q.ListDBMigrations(ctx, store.ListDBMigrationsParams{SourceKey: source, Database: database})
	if err != nil {
		return p, route, err
	}
	p.Runs = runs
	trialOK := false
	for _, r := range runs {
		m := r.DbMigration
		switch {
		case m.Status == store.DbMigrationStatusRunning && m.Stage == StageCopied && m.ID != self:
			add(Blocked, "move_open", nil, fmt.Sprintf("de verhuizing van %s naar %s is gekopieerd en wacht op overschakelen; schakel over of annuleer "+
				"haar eerst", database, r.ClusterName), "")
		case m.Status == store.DbMigrationStatusRunning && m.JobID != nil && m.Kind == store.DbMigrationKindMigrate:
			p.Active = &ActiveJob{ID: *m.JobID, Kind: MigrateKind, Title: moveTitle(m.Database, p.Source.Name, r.ClusterName)}
		case m.Status == store.DbMigrationStatusRunning && m.JobID != nil:
			p.Active = &ActiveJob{ID: *m.JobID, Kind: MigrateTrialKind, Title: trialTitle(m.Database, p.Source.Name, r.ClusterName)}
		case m.Kind == store.DbMigrationKindTrial && m.Status == store.DbMigrationStatusOk:
			trialOK = true
		}
	}

	// De clusters met een database van ClusterForge met dezelfde engine.
	comps, err := s.q.ListDBComponents(ctx, g.Engine)
	if err != nil {
		return p, route, err
	}
	using := map[uuid.UUID]bool{}
	for _, u := range p.Clients {
		if u.Kind == databases.UserCluster && u.ClusterID != nil {
			using[*u.ClusterID] = true
		}
	}
	for _, c := range comps {
		if g.ClusterID != nil && c.ClusterID == *g.ClusterID {
			continue
		}
		p.Targets = append(p.Targets, MigrationOption{ClusterID: c.ClusterID, ClusterName: c.ClusterName, ComponentID: c.ID, Suggested: using[c.ClusterID]})
	}
	if componentID == nil {
		var suggested []MigrationOption
		for _, o := range p.Targets {
			if o.Suggested {
				suggested = append(suggested, o)
			}
		}
		if len(suggested) == 1 {
			componentID = &suggested[0].ComponentID
		}
	}

	if g.Engine != protocol.DBMariaDB && !pg {
		add(Blocked, "engine", nil, fmt.Sprintf("%s verhuizen kan ClusterForge niet; alleen MariaDB en PostgreSQL", databases.EngineName(g.Engine)), "")
		return p, route, nil
	}

	// De oude server.
	srv, vipAddrs, err := s.migrationSource(ctx, g)
	if err != nil {
		return p, route, err
	}
	if srv == nil {
		add(Blocked, "source_down", nil, fmt.Sprintf("geen enkele server van %s draait goed genoeg om van te lezen: %s", p.Source.Name, g.Summary), "")
		return p, route, nil
	}
	sn := Node{NodeID: srv.NodeID, Hostname: srv.Hostname}
	p.Source.Node = &sn
	if srv.ErrorKind == databases.ErrOldAgent {
		add(Blocked, "old_agent", &sn, fmt.Sprintf("de agent op %s is te oud; werk hem bij met het installatiescript en --upgrade", sn.Hostname), "")
		return p, route, nil
	}
	res, err := s.migrateReq(ctx, sn, g.Engine, protocol.DBMigrateRequest{Action: protocol.DBMigrateInspect, Database: database})
	var ce *ConflictError
	switch {
	case errors.As(err, &ce):
		add(Blocked, "source_error", &sn, ce.Msg, "")
		return p, route, nil
	case err != nil:
		return p, route, err
	case res.Info == nil:
		add(Blocked, "source_error", &sn, sn.Hostname+": de agent zei niets over de database", "")
		return p, route, nil
	}
	info := res.Info
	p.Info = info
	if !info.Exists {
		add(Blocked, "not_found", &sn, fmt.Sprintf("op %s staat geen database %s", sn.Hostname, database), "")
		return p, route, nil
	}
	sourceMaria := !pg && strings.Contains(info.Version, "MariaDB")
	switch {
	case pg:
		checkPGSource(info, database, &sn, add)
	case !sourceMaria:
		add(Warning, "mysql", &sn, fmt.Sprintf("%s draait MySQL %s. MariaDB leest een dump van MySQL meestal, maar niet alles, zoals de collatie "+
			"utf8mb4_0900_ai_ci van MySQL 8; de proef laat zien of het lukt", sn.Hostname, shortVersion(info.Version)), "")
	}
	if !pg {
		checkTables(info, database, add)
	}
	if info.Routines > 0 && sourceMaria && !versionAtLeast(info.Version, 11, 3) {
		add(Info, "routines_proc", &sn, fmt.Sprintf("%s heeft %s. MariaDB %s kent het recht SHOW CREATE ROUTINE nog niet, dus de tijdelijke "+
			"gebruiker mag mysql.proc lezen: de definities van alle routines op %s, geen data", database,
			plural(info.Routines, "één routine", "routines"), shortVersion(info.Version), sn.Hostname), "")
	}

	// De gebruikers van de app, en wie nu verbonden is.
	acc, err := s.migrateReq(ctx, sn, g.Engine, protocol.DBMigrateRequest{Action: protocol.DBMigrateAccounts, Database: database})
	switch {
	case errors.As(err, &ce):
		addMove(Blocked, "accounts_error", &sn, fmt.Sprintf("de gebruikers van %s op %s lezen lukte niet: %s. Werk de agent bij met het installatiescript "+
			"en --upgrade als hij ouder is dan ClusterForge", database, sn.Hostname, ce.Msg), "")
	case err != nil:
		return p, route, err
	default:
		for _, a := range acc.Accounts {
			// Het plan vraagt nooit een hash; ook als een agent er een stuurt,
			// gaat hij niet verder.
			a.Hash = ""
			p.Accounts = append(p.Accounts, a)
		}
		if pg {
			checkPGAccounts(p.Accounts, p.Clients, database, addMove)
		} else {
			checkAccounts(p.Accounts, p.Clients, database, addMove)
		}
	}
	for _, sv := range g.Servers {
		if sv.Running {
			route.Servers = append(route.Servers, Node{NodeID: sv.NodeID, Hostname: sv.Hostname})
		}
	}
	conns, unread, err := s.liveConnections(ctx, route.Servers, g.Engine, database)
	if err != nil {
		return p, route, err
	}
	p.Connections = conns
	if len(unread) > 0 {
		addMove(Info, "connections_unknown", nil, "wie er nu met "+database+" verbonden is, lukte niet op "+strings.Join(unread, ", "), "")
	}
	if !trialOK {
		addMove(Info, "no_trial", nil, "er is nog geen geslaagde proef; die zegt vooraf hoe lang de app in onderhoud staat en of alles meekomt", "")
	}

	// Wie de database gebruikt.
	if componentID == nil {
		return p, route, nil
	}
	oi := slices.IndexFunc(p.Targets, func(o MigrationOption) bool { return o.ComponentID == *componentID })
	if oi < 0 {
		if r, err := s.q.GetClusterComponent(ctx, *componentID); err == nil && g.ClusterID != nil && r.ClusterID == *g.ClusterID {
			add(Blocked, "same", nil, database+" staat al in de database van dit cluster", "")
			return p, route, nil
		}
		return p, route, &InvalidError{"component", fmt.Sprintf("kies een cluster met een %s van ClusterForge die draait", databases.EngineName(g.Engine))}
	}
	opt := p.Targets[oi]
	checkClients(p.Clients, opt, database, add)

	// De nieuwe database.
	t, err := s.migrateTarget(ctx, opt.ClusterID, opt.ComponentID, g.Engine)
	switch {
	case errors.As(err, &ce):
		add(Blocked, "target_down", nil, opt.ClusterName+": "+ce.Msg, "")
		return p, route, nil
	case err != nil:
		return p, route, err
	}
	target := &MigrationTarget{
		ClusterID: opt.ClusterID, ClusterName: opt.ClusterName, Slug: t.cluster.Slug, Environment: string(t.cluster.Environment),
		ComponentID: opt.ComponentID, Node: t.node, Port: port, Hosts: []string{}, Disks: []MigrationDisk{},
	}
	p.Target = target
	if pg {
		if err := s.pgMigrateAgent(ctx, t.node); errors.As(err, &ce) {
			add(Blocked, "target_agent", &t.node, ce.Msg, "")
			return p, route, nil
		} else if err != nil {
			return p, route, err
		}
		// Een rol die al op het nieuwe cluster inlogt, krijgt bij de
		// verhuizing het wachtwoord van de oude server.
		if us, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBUsers}); err == nil {
			checkPGExisting(p.Accounts, us.Users, opt.ClusterName, addMove)
		}
	}
	cands, err := s.q.ListComponentCandidates(ctx, &opt.ClusterID)
	if err != nil {
		return p, route, err
	}
	byID := map[uuid.UUID]store.ListComponentCandidatesRow{}
	for _, r := range cands {
		byID[r.ID] = r
	}
	if hosts, address, err := s.userHosts(ctx, t); err == nil {
		target.Address = address
		if !pg {
			p.NewHosts = append(hosts, "localhost")
		}
	}
	target.Hosts = nodeAddresses(byID[t.node.NodeID])
	tg := slices.IndexFunc(groups, func(x databases.Group) bool {
		return x.ClusterID != nil && *x.ClusterID == opt.ClusterID && x.Engine == g.Engine
	})
	trial := TrialName(database)
	trialExists := false
	if tg >= 0 {
		for _, sv := range groups[tg].Servers {
			if sv.NodeID == t.node.NodeID {
				target.Version = sv.Version
			}
		}
		for _, d := range groups[tg].Databases {
			target.DataBytes += d.SizeBytes
			switch d.Name {
			case database:
				target.Exists = true
			case trial:
				trialExists = true
			}
		}
	}
	var pool int64
	if !pg {
		pool, _ = templates.ParseSize(fmt.Sprint(t.spec.Params["buffer_pool"]))
	}
	target.BufferPoolBytes = pool

	// Bereikbaarheid: eerst de server zelf, dan een VIP van het oude
	// cluster.
	ip, err := s.primaryIP(ctx, srv.NodeID)
	if err != nil {
		return p, route, err
	}
	addrs := slices.DeleteFunc(append([]string{ip}, vipAddrs...), func(a string) bool { return a == "" })
	if len(addrs) == 0 {
		add(Blocked, "no_address", &sn, "ClusterForge kent geen adres van "+sn.Hostname, "")
		return p, route, nil
	}
	var tried []string
	for i, a := range addrs {
		pr, err := s.migrateReq(ctx, t.node, g.Engine, protocol.DBMigrateRequest{Action: protocol.DBMigrateProbe, Host: a, Port: port, Database: database})
		if errors.As(err, &ce) {
			add(Blocked, "target_agent", &t.node, ce.Msg, "")
			return p, route, nil
		}
		if err != nil {
			return p, route, err
		}
		if pr.Probe == nil {
			add(Blocked, "target_agent", &t.node, t.node.Hostname+": de agent zei niets over het adres", "")
			return p, route, nil
		}
		if !pr.Probe.Reached {
			tried = append(tried, a+": "+pr.Probe.Error)
			continue
		}
		p.Reach = pr.Probe
		p.Source.Address, p.Source.Via = a, ViaNode
		if i > 0 {
			p.Source.Via = ViaVIP
		}
		break
	}
	engineName := databases.EngineName(g.Engine)
	switch {
	case p.Reach == nil:
		add(Blocked, "unreachable", &t.node, fmt.Sprintf("%s bereikt %s op %s niet (%s). Zet in de firewall poort %d open van %s naar %s, zolang de verhuizing duurt",
			t.node.Hostname, engineName, sn.Hostname, strings.Join(tried, "; "), port, joinHosts(target.Hosts), addrs[0]), "")
	case pg:
		checkPGReach(&p, info, ip, sn, tried, add)
	default:
		if p.Source.Via == ViaVIP {
			// Achter een VIP staat soms HAProxy; MariaDB ziet dan het adres
			// van de node met HAProxy, niet dat van de nieuwe node.
			for _, sv := range g.Servers {
				if a, err := s.primaryIP(ctx, sv.NodeID); err == nil && a != "" && !slices.Contains(target.Hosts, a) {
					target.Hosts = append(target.Hosts, a)
				}
			}
			add(Info, "via_vip", &t.node, fmt.Sprintf("%s bereikt %s niet rechtstreeks (%s), maar het VIP %s wel. Staat daar HAProxy voor, dan ziet MariaDB "+
				"het adres van de node met HAProxy; daarom mag de tijdelijke gebruiker ook inloggen vanaf de servers van %s", t.node.Hostname, ip,
				strings.Join(tried, "; "), p.Source.Address, p.Source.Name), "")
		}
		r := p.Reach
		switch r.Code {
		case 0:
		case 1130:
			add(Info, "host_unknown", &sn, fmt.Sprintf("MariaDB op %s kent nog geen gebruiker voor het adres van %s; dat is goed, de proef maakt er een", p.Source.Address, t.node.Hostname), "")
		case 1129:
			add(Blocked, "host_blocked", &sn, fmt.Sprintf("MariaDB op %s blokkeert het adres van %s na te veel mislukte verbindingen. Geef het vrij met dit commando op %s",
				p.Source.Address, t.node.Hostname, sn.Hostname), "mariadb-admin flush-hosts")
		default:
			add(Blocked, "refused", &sn, fmt.Sprintf("MariaDB op %s weigert %s: %d %s", p.Source.Address, t.node.Hostname, r.Code, r.Error), "")
		}
		if r.Code == 0 && !r.TLS {
			add(Info, "no_tls", &sn, fmt.Sprintf("MariaDB op %s kan geen TLS, dus de dump gaat onversleuteld over het netwerk naar %s. In je eigen netwerk is dat meestal goed", p.Source.Address, t.node.Hostname), "")
		}
	}

	// Versies.
	if pg && target.Version != "" {
		checkPGVersions(info.Version, target.Version, sn, t.node, add)
	}
	if sourceMaria && target.Version != "" {
		sMaj, sMin, ok1 := majorMinor(info.Version)
		tMaj, tMin, ok2 := majorMinor(target.Version)
		switch {
		case !ok1 || !ok2:
		case tMaj < sMaj || tMaj == sMaj && tMin < sMin:
			add(Blocked, "downgrade", &t.node, fmt.Sprintf("de nieuwe MariaDB (%d.%d op %s) is ouder dan de oude (%d.%d op %s); een dump van een nieuwere MariaDB gaat niet "+
				"altijd goed in een oudere. Neem nodes met Debian 13 (MariaDB 11.8)", tMaj, tMin, t.node.Hostname, sMaj, sMin, sn.Hostname), "")
		case tMaj != sMaj || tMin != sMin:
			add(Info, "upgrade", nil, fmt.Sprintf("van MariaDB %d.%d naar %d.%d: een dump gaat goed in een nieuwere versie", sMaj, sMin, tMaj, tMin), "")
		}
	}

	// Wat er al staat.
	if target.Exists {
		add(Warning, "exists", nil, fmt.Sprintf("op %s staat al een database %s. De proef gebruikt %s, maar de echte verhuizing zet de data in %s; "+
			"haal die eerst weg als ze leeg is", opt.ClusterName, database, trial, database), "")
		addMove(Blocked, "exists", nil, fmt.Sprintf("op %s staat al een database %s, en de verhuizing schrijft niet over een bestaande database. "+
			"Haal haar weg bij Databases en gebruikers als ze leeg is", opt.ClusterName, database), "")
	}
	leftover := false
	if trialExists {
		prev, err := s.q.LatestDBMigrationTarget(ctx, store.LatestDBMigrationTargetParams{ComponentID: opt.ComponentID, TargetDatabase: trial, Exclude: self})
		switch {
		case err == nil && prev.Status != store.DbMigrationStatusRunning:
			leftover = true
			add(Info, "trial_leftover", nil, fmt.Sprintf("%s van een vorige proef staat nog op %s; de proef haalt haar eerst weg", trial, opt.ClusterName), "")
		case err == nil:
			add(Blocked, "trial_running", nil, fmt.Sprintf("op %s loopt al een proef met %s, van %s; wacht tot die klaar is", opt.ClusterName, trial, prev.SourceName), "")
		case errors.Is(err, pgx.ErrNoRows):
			add(Blocked, "trial_exists", nil, fmt.Sprintf("op %s staat al een database %s die ClusterForge niet maakte; de proef importeert in die naam. "+
				"Haal haar weg bij Databases en gebruikers als ze niet nodig is", opt.ClusterName, trial), "")
		default:
			return p, route, err
		}
	}

	// Schijf en geheugen op elke datanode: Galera en Patroni zetten de data
	// overal.
	dataDir, logs := "/var/lib/mysql", "de logs van MariaDB"
	if pg {
		dataDir, logs = "/var/lib/postgresql", "de WAL van PostgreSQL"
	}
	for _, n := range dataNodes(t.spec, nil) {
		f := nodeState(byID[n.NodeID]).Facts
		if f == nil {
			continue
		}
		var pn PlanNode
		checkDisk(&pn, &n, f, dataDir, func(string, string, *Node, string, string) {})
		if pn.DiskFreeBytes == nil {
			continue
		}
		free := *pn.DiskFreeBytes
		target.Disks = append(target.Disks, MigrationDisk{Hostname: n.Hostname, Mount: pn.DiskMount, FreeBytes: free})
		switch {
		case free < info.SizeBytes+(1<<30):
			add(Blocked, "disk", &n, fmt.Sprintf("op %s is er %s vrij op %s; %s is %s, plus ruimte voor %s", n.Hostname,
				humanBytes(free), pn.DiskMount, database, humanBytes(info.SizeBytes), logs), "")
		case free < 2*info.SizeBytes+(1<<30):
			add(Warning, "disk_low", &n, fmt.Sprintf("op %s is er %s vrij op %s; %s is %s. Dat past, maar de dumps van elke nacht komen er ook", n.Hostname,
				humanBytes(free), pn.DiskMount, database, humanBytes(info.SizeBytes)), "")
		}
	}
	if total := target.DataBytes + info.SizeBytes; pool > 0 && total > pool {
		add(Warning, "buffer_pool", nil, fmt.Sprintf("met %s erbij staat er %s aan data in de MariaDB van %s, en de buffer pool is %s. Wat er niet in past, "+
			"komt van schijf: trager, niet fout. Geef de buffer pool meer bij Instellen als de nodes het geheugen hebben", database, humanBytes(total),
			opt.ClusterName, humanBytes(pool)), "")
	}

	route = migrationRoute{
		SourceNode: sn, Address: p.Source.Address, Port: port, Via: p.Source.Via, Hosts: target.Hosts, Import: t.node, Owner: info.Owner,
		Target: trial, Charset: info.Charset, Collation: info.Collation, Size: info.SizeBytes,
		Views: info.Views, Routines: info.Routines, Triggers: info.Triggers, Events: info.Events, Leftover: leftover,
		Servers: route.Servers, DataNodes: dataNodes(t.spec, nil), TargetAddress: target.Address, NewHosts: p.NewHosts,
	}
	if pg {
		// pg_hba.conf van de oude server zegt vanwaar de tijdelijke
		// gebruiker mag inloggen, niet de gebruiker zelf.
		route.Engine, route.Hosts = protocol.DBPostgreSQL, nil
	}
	for _, tb := range info.Tables {
		route.Tables = append(route.Tables, tb.Name)
	}
	return p, route, nil
}

// migrationSource kiest de server om van te lezen, met de VIP's van het
// oude cluster die op een van zijn servers staan. Bij Galera een server die
// Synced is, liefst zonder VIP zodat de app er geen last van heeft; bij
// replicatie de primary. Nil als geen enkele geschikt is.
func (s *Service) migrationSource(ctx context.Context, g databases.Group) (*databases.Server, []string, error) {
	owned := map[uuid.UUID][]string{}
	var vipAddrs []string
	if g.ClusterID != nil {
		vips, err := s.q.ListVIPsByCluster(ctx, *g.ClusterID)
		if err != nil {
			return nil, nil, err
		}
		for _, v := range vips {
			o := v.Vip.OwnerNodeID
			if o != nil && slices.ContainsFunc(g.Servers, func(sv databases.Server) bool { return sv.NodeID == *o }) {
				owned[*o] = append(owned[*o], v.Vip.Address.String())
				vipAddrs = append(vipAddrs, v.Vip.Address.String())
			}
		}
	}
	// Hoe lager hoe beter; -1 kan niet. Een server met een te oude agent
	// telt mee, zodat het plan zegt wat er moet.
	rank := func(sv *databases.Server) int {
		usable := sv.Running && sv.ErrorKind == ""
		switch g.Kind {
		case databases.KindGalera:
			usable = usable && sv.OK
		case databases.KindPatroni, databases.KindReplication:
			usable = usable && sv.Role == protocol.DBRolePrimary
		}
		switch {
		case usable && len(owned[sv.NodeID]) == 0:
			return 0
		case usable:
			return 1
		case sv.ErrorKind == databases.ErrOldAgent:
			return 2
		}
		return -1
	}
	var best *databases.Server
	for i := range g.Servers {
		sv := &g.Servers[i]
		if r := rank(sv); r >= 0 && (best == nil || r < rank(best)) {
			best = sv
		}
	}
	return best, vipAddrs, nil
}

// primaryIP is het adres van een node; leeg als ClusterForge het niet kent.
func (s *Service) primaryIP(ctx context.Context, nodeID uuid.UUID) (string, error) {
	n, err := s.q.GetNode(ctx, nodeID)
	if err != nil {
		return "", err
	}
	if n.Node.PrimaryIp == nil {
		return "", nil
	}
	return n.Node.PrimaryIp.String(), nil
}

// nodeAddresses zijn de adressen van een node waarvandaan hij kan
// verbinden: het hoofdadres en de andere adressen van zijn interfaces,
// zonder loopback en link-local.
func nodeAddresses(r store.ListComponentCandidatesRow) []string {
	out := []string{}
	add := func(a string) {
		ip, err := netip.ParseAddr(a)
		if err != nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
			return
		}
		if s := ip.Unmap().String(); !slices.Contains(out, s) && len(out) < 16 {
			out = append(out, s)
		}
	}
	add(r.PrimaryIp)
	if f := nodeState(r).Facts; f != nil {
		add(f.PrimaryAddress)
		for _, i := range f.Interfaces {
			if !i.Up {
				continue
			}
			for _, a := range i.Addresses {
				if pfx, err := netip.ParsePrefix(a); err == nil {
					add(pfx.Addr().String())
				} else {
					add(a)
				}
			}
		}
	}
	return out
}

// checkTables kijkt of de tabellen goed in Galera passen.
func checkTables(info *protocol.DBMigrateInfo, database string, add func(string, string, *Node, string, string)) {
	var noPK, other []string
	var alter []string
	for _, t := range info.Tables {
		if !t.PrimaryKey {
			noPK = append(noPK, t.Name)
		}
		if t.Engine != "" && !strings.EqualFold(t.Engine, "InnoDB") {
			other = append(other, t.Name+" ("+t.Engine+")")
			if len(alter) < 10 {
				alter = append(alter, fmt.Sprintf("ALTER TABLE %s.%s ENGINE=InnoDB;", dbsql.QuoteIdent(database), dbsql.QuoteIdent(t.Name)))
			}
		}
	}
	if len(noPK) > 0 {
		msg := fmt.Sprintf("%s zonder primaire sleutel: %s. Galera werkt ook zonder, maar een UPDATE of DELETE op zo'n tabel is traag op de "+
			"andere nodes, want die zoeken de rij dan in de hele tabel", plural(len(noPK), "één tabel", "tabellen"), listSome(noPK, 8))
		command := ""
		if strings.Contains(database, "nextcloud") {
			msg += ". Nextcloud voegt ze zelf toe"
			command = "sudo -u www-data php occ db:add-missing-primary-keys"
		}
		add(Warning, "no_primary_key", nil, msg, command)
	}
	if len(other) > 0 {
		add(Warning, "not_innodb", nil, fmt.Sprintf("%s niet in InnoDB: %s. Galera zet alleen InnoDB op de andere nodes, dus de rest staat "+
			"straks alleen op de node die importeerde. Zet ze vóór de verhuizing om, op de oude server", plural(len(other), "één tabel staat", "tabellen staan"),
			listSome(other, 8)), strings.Join(alter, "\n"))
	}
}

// checkClients zegt wie er na de verhuizing nog naar de oude server wijst.
func checkClients(clients []databases.User, opt MigrationOption, database string, add func(string, string, *Node, string, string)) {
	var others []string
	seen := false
	for _, u := range clients {
		switch {
		case u.Kind == databases.UserLocal:
		case u.Kind == databases.UserCluster && u.ClusterID != nil && *u.ClusterID == opt.ClusterID:
			seen = true
		default:
			others = append(others, u.Name+" ("+strings.Join(u.Nodes, ", ")+")")
		}
	}
	switch {
	case len(others) > 0:
		add(Warning, "clients_elsewhere", nil, fmt.Sprintf("%s wordt ook gebruikt door %s; die wijzen na de verhuizing nog naar de oude server", database,
			joinHosts(others)), "")
	case !seen:
		add(Info, "no_clients", nil, fmt.Sprintf("de laatste 30 dagen verbond niemand van %s met %s", opt.ClusterName, database), "")
	}
}

func majorMinor(v string) (int, int, bool) {
	m := mariaVersionRe.FindStringSubmatch(v)
	if m == nil {
		return 0, 0, false
	}
	maj, _ := strconv.Atoi(m[1])
	mnr, _ := strconv.Atoi(m[2])
	return maj, mnr, true
}

func versionAtLeast(v string, major, minor int) bool {
	maj, mnr, ok := majorMinor(v)
	return ok && (maj > major || maj == major && mnr >= minor)
}

// shortVersion is 11.8.3 van 11.8.3-MariaDB-0+deb13u1.
func shortVersion(v string) string {
	if i := strings.IndexAny(v, "-+ "); i > 0 {
		return v[:i]
	}
	return v
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// listSome noemt de eerste max namen en hoeveel er nog meer zijn.
func listSome(names []string, max int) string {
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s en nog %d", strings.Join(names[:max], ", "), len(names)-max)
}

func trialTitle(database, source, cluster string) string {
	return fmt.Sprintf("Proefverhuizing van %s van %s naar %s", database, source, cluster)
}

type trialParams struct {
	ComponentID uuid.UUID `json:"component_id"`
	Source      string    `json:"source"`
	Database    string    `json:"database"`
	// Grants zijn bij de verhuizing van PostgreSQL de rollen met hun rechten
	// op de database zelf; die staan niet in de dump.
	Grants []protocol.DBMigrateAccount `json:"-"`
}

// StartTrial begint een proefverhuizing. Het plan moet een doel hebben en
// mag niets blokkeren.
func (s *Service) StartTrial(ctx context.Context, actor events.Actor, source, database string, componentID uuid.UUID) (store.Job, error) {
	p, _, err := s.migrationPlan(ctx, source, database, &componentID, uuid.Nil)
	if err != nil {
		return store.Job{}, err
	}
	if p.Active != nil {
		return store.Job{}, &ConflictError{"er loopt al een proef of verhuizing van " + database + " (" + p.Active.Title + "); wacht tot die klaar is"}
	}
	if p.Blocked {
		var msgs []string
		for _, f := range p.Findings {
			if f.Level == Blocked {
				msgs = append(msgs, f.Message)
			}
		}
		return store.Job{}, &ConflictError{"zo kan de proef niet: " + strings.Join(msgs, "; ")}
	}
	if p.Target == nil {
		return store.Job{}, &InvalidError{"component", "kies het cluster waar de database naartoe gaat"}
	}
	t := p.Target
	var j store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		j, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: MigrateTrialKind, Title: trialTitle(database, p.Source.Name, t.ClusterName),
			Params: trialParams{ComponentID: t.ComponentID, Source: source, Database: database}, ClusterID: &t.ClusterID, Actor: actor,
		})
		if err != nil {
			return err
		}
		_, err = q.InsertDBMigration(ctx, store.InsertDBMigrationParams{
			Kind: store.DbMigrationKindTrial, ComponentID: t.ComponentID, ClusterID: t.ClusterID, SourceKey: source, SourceName: p.Source.Name,
			Database: database, TargetDatabase: TrialName(database), StartedAt: s.Now(), JobID: &j.ID, RequestedBy: j.RequestedBy,
		})
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return &ConflictError{"er loopt al een proef of verhuizing van " + database + "; wacht tot die klaar is"}
		}
		return err
	})
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{busy.Error()}
	}
	if err == nil {
		s.jobs.Kick()
	}
	return j, err
}

// trialResult is wat een proef opleverde.
type trialResult struct {
	run     *protocol.DBMigrateRun
	route   migrationRoute
	pulled  bool
	dropped bool
	// failed en message: het oordeel na het vergelijken.
	failed      bool
	message     string
	differences int
	objects     []objectCount
}

type objectCount struct {
	Kind   string `json:"kind"`
	Source int    `json:"source"`
	Target int    `json:"target"`
}

// trialPull is de state van de stap die ophaalt en importeert.
type trialPull struct {
	Started time.Time              `json:"started"`
	Run     *protocol.DBMigrateRun `json:"run,omitempty"`
}

const (
	stepTrialCheck   = "Controle"
	stepTrialCleanup = "Vorige proef opruimen"
	stepTrialCompare = "Rijen en objecten vergelijken"
)

func (s *Service) runTrial(ctx context.Context, j *jobs.Job) error {
	var p trialParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	m, err := s.q.GetDBMigrationByJob(ctx, &j.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("deze proef staat niet meer in ClusterForge")
	}
	if err != nil {
		return err
	}
	var res trialResult
	err = s.trialSteps(ctx, j, p, m, &res)
	if jobs.Interrupted(ctx) {
		return err
	}
	return s.finishTrial(ctx, j, m, &res, err)
}

func (s *Service) trialSteps(ctx context.Context, j *jobs.Job, p trialParams, m store.DbMigration, res *trialResult) error {
	route := &res.route
	if err := j.Step(ctx, stepTrialCheck, func(ctx context.Context, st *jobs.Step) error {
		if st.State(route) {
			return nil
		}
		plan, r, err := s.migrationPlan(ctx, p.Source, p.Database, &p.ComponentID, m.ID)
		if err != nil {
			return err
		}
		var blocked []string
		for _, f := range plan.Findings {
			switch f.Level {
			case Blocked:
				blocked = append(blocked, f.Message)
			case Warning:
				st.Logf("Let op: %s.", f.Message)
			}
		}
		if len(blocked) > 0 {
			return errors.New(strings.Join(blocked, "; "))
		}
		if plan.Target == nil || plan.Target.ComponentID != p.ComponentID {
			return errors.New("de database van het cluster is er niet meer")
		}
		*route = r
		st.Logf("%s staat op %s: %s, %d tabellen.", p.Database, r.SourceNode.Hostname, humanBytes(r.Size), len(r.Tables))
		st.Logf("%s haalt de dump op van %s:%d en importeert hem als %s.", r.Import.Hostname, r.Address, r.Port, r.Target)
		if err := s.q.SetDBMigrationRoute(ctx, store.SetDBMigrationRouteParams{
			ID: m.ID, SourceNode: r.SourceNode.Hostname, SourceAddress: r.Address, TargetNode: r.Import.Hostname, SizeBytes: r.Size,
		}); err != nil {
			return err
		}
		return st.SetState(ctx, r)
	}); err != nil {
		return err
	}
	j.Done(stepTrialCheck, route)

	if err := j.Step(ctx, stepTrialCleanup, func(ctx context.Context, st *jobs.Step) error {
		cur, err := s.migrateReq(ctx, route.Import, route.engine(), protocol.DBMigrateRequest{Action: protocol.DBMigrateStatus, Target: route.Target})
		if err != nil {
			return err
		}
		if r := cur.Run; r != nil && r.Finished.IsZero() {
			return fmt.Errorf("%s importeert nog in %s, van een vorige proef; wacht tot dat klaar is", route.Import.Hostname, route.Target)
		}
		if !route.Leftover {
			st.Logf("Er staat niets van een vorige proef.")
			return nil
		}
		if err := s.dropTrial(ctx, route); err != nil {
			return err
		}
		st.Logf("%s van een vorige proef weggehaald.", route.Target)
		return nil
	}); err != nil {
		return err
	}

	pullStep := fmt.Sprintf("%s ophalen van %s en importeren als %s op %s", p.Database, route.SourceNode.Hostname, route.Target, route.Import.Hostname)
	var pull trialPull
	err := j.Step(ctx, pullStep, func(ctx context.Context, st *jobs.Step) error {
		return s.trialPull(ctx, j, st, p, route, &pull, res)
	})
	j.Done(pullStep, &pull)
	if pull.Run != nil {
		res.run, res.pulled = pull.Run, true
	}
	if err != nil {
		return err
	}

	if err := j.Step(ctx, stepTrialCompare, func(ctx context.Context, st *jobs.Step) error {
		compareTrial(route, pull.Run, res, false)
		for _, line := range strings.Split(res.message, "\n") {
			st.Logf("%s", line)
		}
		return nil
	}); err != nil {
		return err
	}
	if res.message == "" {
		compareTrial(route, pull.Run, res, false)
	}

	return j.Step(ctx, route.Target+" weghalen", func(ctx context.Context, st *jobs.Step) error {
		if err := s.dropTrial(ctx, route); err != nil {
			return err
		}
		res.dropped = true
		st.Logf("%s weggehaald van %s; aan %s op %s is niets veranderd.", route.Target, route.Import.Hostname, p.Database, route.SourceNode.Hostname)
		return nil
	})
}

// trialPull maakt de tijdelijke gebruiker, laat de nieuwe node de dump
// ophalen en importeren, wacht tot hij klaar is, en haalt de gebruiker
// weer weg. Een hervatte stap wacht verder op dezelfde import.
func (s *Service) trialPull(ctx context.Context, j *jobs.Job, st *jobs.Step, p trialParams, route *migrationRoute, pull *trialPull, res *trialResult) error {
	user, engine := trialUser(j.ID), route.engine()
	revoke := protocol.DBMigrateRequest{Action: protocol.DBMigrateRevoke, User: user, Hosts: route.Hosts}
	dropUser := "DROP USER"
	if route.pg() {
		dropUser = "DROP ROLE"
	}
	granted := st.State(pull)
	defer func() {
		if !granted || jobs.Interrupted(ctx) {
			return
		}
		// Ook als iemand de taak annuleert.
		dctx, cancel := jobs.Detach(ctx)
		defer cancel()
		if _, err := s.migrateReq(dctx, route.SourceNode, engine, revoke); err != nil {
			st.Logf("De tijdelijke gebruiker %s op %s weghalen mislukte: %v. Haal hem weg met %s op %s.", user, route.SourceNode.Hostname, err,
				dropUser, route.SourceNode.Hostname)
			return
		}
		st.Logf("Tijdelijke gebruiker %s op %s weggehaald.", user, route.SourceNode.Hostname)
	}()
	if !granted {
		pw := rand.Text()
		if _, err := s.migrateReq(ctx, route.SourceNode, engine, protocol.DBMigrateRequest{
			Action: protocol.DBMigrateGrant, Database: p.Database, User: user, Hosts: route.Hosts, Password: pw,
		}); err != nil {
			return err
		}
		granted = true
		if route.pg() {
			st.Logf("Tijdelijke gebruiker %s op %s gemaakt: lid van %s, de eigenaar van %s, en alleen om te lezen; hij verloopt na %d uur.", user,
				route.SourceNode.Hostname, route.Owner, p.Database, int(protocol.DBTempRoleTTL.Hours()))
		} else {
			st.Logf("Tijdelijke gebruiker %s op %s gemaakt, alleen om %s te lezen, vanaf %s.", user, route.SourceNode.Hostname, p.Database, joinHosts(route.Hosts))
		}
		pullReq := protocol.DBMigrateRequest{
			Action: protocol.DBMigratePull, Database: p.Database, User: user, Password: pw, Host: route.Address, Port: route.Port,
			Target: route.Target, Charset: route.Charset, Collation: route.Collation, Total: route.Size,
		}
		switch {
		case route.pg() && route.Move:
			// De eigenaars en rechten gaan mee; de rollen zijn er al.
			pullReq.Owner, pullReq.Accounts = route.Owner, p.Grants
		case route.pg():
			// Een proef zonder de rollen van de app: alles is van postgres.
			pullReq.Trial = true
		}
		r, err := s.migrateReq(ctx, route.Import, engine, pullReq)
		if err != nil {
			return err
		}
		if r.Run == nil {
			return errors.New(route.Import.Hostname + ": de agent zei niet dat de import begon")
		}
		pull.Started = r.Run.Started
		tls := "zonder TLS"
		if r.Run.TLS {
			tls = "met TLS"
		}
		st.Logf("%s maakte %s en haalt de dump op, %s.", route.Import.Hostname, route.Target, tls)
		if err := st.SetState(ctx, pull); err != nil {
			return err
		}
	}
	res.pulled = true
	deadline := s.Now().Add(protocol.DBDumpTimeout + time.Hour)
	reported, counting := int64(0), false
	for {
		cur, err := s.migrateReq(ctx, route.Import, engine, protocol.DBMigrateRequest{Action: protocol.DBMigrateStatus, Target: route.Target})
		var run *protocol.DBMigrateRun
		if err == nil {
			run = cur.Run
		}
		switch {
		case err != nil && ctx.Err() != nil:
			return err
		case err != nil:
			if s.Now().After(deadline) {
				return err
			}
		case run == nil || !run.Started.Equal(pull.Started):
			return fmt.Errorf("de agent op %s weet niets van deze import; startte hij opnieuw? Probeer de proef opnieuw", route.Import.Hostname)
		case run.Finished.IsZero() && !run.Imported.IsZero():
			if !counting {
				counting = true
				st.Logf("Import klaar na %s, %s binnen; nu de rijen tellen aan beide kanten.", run.Imported.Sub(run.Started).Round(time.Second), humanBytes(run.Bytes))
				_ = st.Flush(ctx)
			}
		case run.Finished.IsZero():
			if b := run.Bytes; b-reported >= 64<<20 {
				reported = b
				st.Logf("%s binnen.", humanBytes(b))
				_ = st.Flush(ctx)
			}
		default:
			pull.Run = run
			if err := st.SetState(ctx, pull); err != nil {
				return err
			}
			if run.Error != "" {
				return errors.New(run.Error)
			}
			st.Logf("Klaar: %s in %s, tellen %s.", humanBytes(run.Bytes), run.Imported.Sub(run.Started).Round(time.Second),
				run.Finished.Sub(run.Imported).Round(time.Second))
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(s.DumpPoll):
		}
	}
}

// compareTrial vergelijkt de nieuwe database met de oude. Andere aantallen
// rijen zijn bij een proef te verwachten, want de app werkt door; een
// tabel, view, routine, trigger of event die ontbreekt niet. Bij de echte
// verhuizing (strict) staat de app stil en moet alles gelijk zijn.
func compareTrial(route *migrationRoute, run *protocol.DBMigrateRun, res *trialResult, strict bool) {
	res.differences, res.failed, res.objects = 0, false, nil
	if run == nil {
		res.failed, res.message = true, "geen uitkomst van de import"
		return
	}
	var lines, problems []string
	var rowsSource, rowsTarget int64
	var diffs []string
	for _, t := range run.Tables {
		rowsSource += t.Source
		rowsTarget += t.Target
		if t.Source != t.Target {
			res.differences++
			diffs = append(diffs, fmt.Sprintf("%s (%d oud, %d nieuw)", t.Name, t.Source, t.Target))
		}
	}
	var missing []string
	for _, name := range route.Tables {
		if !slices.ContainsFunc(run.Tables, func(c protocol.DBMigrateCount) bool { return c.Name == name }) {
			missing = append(missing, name)
		}
	}
	res.objects = []objectCount{
		{"views", route.Views, run.Views}, {"routines", route.Routines, run.Routines}, {"triggers", route.Triggers, run.Triggers},
	}
	if !route.pg() {
		// PostgreSQL kent geen events.
		res.objects = append(res.objects, objectCount{"events", route.Events, run.Events})
	}
	for _, o := range res.objects {
		if o.Target < o.Source {
			kind := o.Kind
			if route.pg() && kind == "routines" {
				kind = "functies"
			}
			problems = append(problems, fmt.Sprintf("%d van de %d %s", o.Target, o.Source, kind))
		}
	}
	lines = append(lines, fmt.Sprintf("%d tabellen, %d rijen oud en %d nieuw.", len(run.Tables), rowsSource, rowsTarget))
	if len(missing) > 0 {
		res.failed = true
		lines = append(lines, fmt.Sprintf("Ontbreekt in de kopie: %s.", listSome(missing, 10)))
	}
	if len(problems) > 0 {
		res.failed = true
		lines = append(lines, fmt.Sprintf("In de kopie staan maar %s.", strings.Join(problems, ", ")))
	}
	differ := plural(res.differences, "één tabel heeft een ander aantal rijen", "tabellen hebben een ander aantal rijen")
	switch {
	case res.differences > 0 && strict:
		res.failed = true
		lines = append(lines, fmt.Sprintf("%s: %s. Dan schreef er tijdens de verhuizing nog iemand in de oude database, zoals een cronjob of een "+
			"andere node van de app.", differ, listSome(diffs, 5)))
	case res.differences > 0:
		lines = append(lines, fmt.Sprintf("%s: %s. Bij een proef is dat te verwachten, want de app werkte door; bij de echte verhuizing staat ze stil en moet alles gelijk zijn.",
			differ, listSome(diffs, 5)))
	case !res.failed:
		lines = append(lines, "Alle tabellen hebben aan beide kanten evenveel rijen.")
	}
	res.message = strings.Join(lines, "\n")
}

// dropTrial haalt de kopie van een proef weg.
func (s *Service) dropTrial(ctx context.Context, route *migrationRoute) error {
	_, err := s.dbRequest(ctx, dbTarget{node: route.Import, engine: route.engine()}, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{
		Database: route.Target, Confirm: route.Target,
	}})
	if err == nil && s.DBChanged != nil {
		s.DBChanged()
	}
	return err
}

// finishTrial legt de uitkomst vast, haalt een kopie weg die bleef staan,
// en schrijft het logboek.
func (s *Service) finishTrial(ctx context.Context, j *jobs.Job, m store.DbMigration, res *trialResult, err error) error {
	if res.pulled && !res.dropped && res.route.Target != "" {
		dctx, cancel := jobs.Detach(ctx)
		if derr := s.dropTrial(dctx, &res.route); derr != nil && err == nil {
			err = fmt.Errorf("%s weghalen: %w", res.route.Target, derr)
		}
		cancel()
	}
	status, msg := store.DbMigrationStatusOk, res.message
	switch {
	case err != nil:
		status, msg = store.DbMigrationStatusFailed, strings.TrimPrefix(res.message+"\n"+err.Error(), "\n")
	case res.failed:
		status = store.DbMigrationStatusFailed
		err = errors.New(strings.ReplaceAll(msg, "\n", " "))
	}
	now := s.Now()
	params := store.FinishDBMigrationParams{ID: m.ID, Status: status, FinishedAt: &now, Message: msg, Tables: []byte("[]"), Objects: []byte("[]"),
		Differences: int32(min(res.differences, math.MaxInt32))} //nolint:gosec // begrensd
	if r := res.run; r != nil {
		params.DumpBytes = r.Bytes
		if !r.Imported.IsZero() {
			params.ImportMs = ptr(r.Imported.Sub(r.Started).Milliseconds())
			if !r.Finished.IsZero() {
				params.CountMs = ptr(r.Finished.Sub(r.Imported).Milliseconds())
			}
		}
		if len(r.Tables) > 0 {
			params.Tables, _ = json.Marshal(r.Tables)
		}
	}
	if len(res.objects) > 0 {
		params.Objects, _ = json.Marshal(res.objects)
	}
	wctx, cancel := jobs.Detach(ctx)
	defer cancel()
	row, ferr := s.q.FinishDBMigration(wctx, params)
	if ferr != nil {
		s.log.Error("proefverhuizing vastleggen mislukt", "job", j.ID, "err", ferr)
		row = m
	}
	s.trialEvent(wctx, actorOf(j), row)
	return err
}

func (s *Service) trialEvent(ctx context.Context, actor events.Actor, m store.DbMigration) {
	action := "cluster.database_trial_ok"
	if m.Status != store.DbMigrationStatusOk {
		action = "cluster.database_trial_failed"
	}
	c, err := s.q.GetCluster(ctx, m.ClusterID)
	if err == nil {
		payload := map[string]any{
			"name": c.Name, "source": m.SourceName, "source_node": m.SourceNode, "database": m.Database, "target": m.TargetDatabase,
			"node": m.TargetNode, "size_bytes": m.SizeBytes, "dump_bytes": m.DumpBytes, "differences": m.Differences,
			"message": m.Message, "migration_id": m.ID,
		}
		if m.ImportMs != nil {
			payload["import_ms"] = *m.ImportMs
		}
		err = s.ev.Write(ctx, s.q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: action, Payload: payload,
		})
	}
	if err != nil {
		s.log.Error("event schrijven mislukt", "action", action, "err", err)
	}
}

// failMigrationsOf zet een proef van een taak die stopte op mislukt, ook
// als de taak nooit begon.
func (s *Service) failMigrationsOf(ctx context.Context, j store.Job) {
	if err := s.q.FailRunningDBMigrations(ctx, store.FailRunningDBMigrationsParams{JobID: &j.ID, Message: cmp.Or(j.Error, "de taak stopte")}); err != nil {
		s.log.Error("proef van een gestopte taak afronden mislukt", "job", j.ID, "err", err)
	}
}
