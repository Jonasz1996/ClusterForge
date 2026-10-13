package components

// Het oude cluster afbouwen (fase 5, mijlpaal 14).
//
// Een verhuisde database blijft op de oude server staan, op slot. Is er
// zeven dagen niemand van buiten de databasenode meer mee verbonden
// geweest, dan kan Jonas haar afbouwen: een laatste dump op een node van de
// oude server, die blijft staan tot hij hem weghaalt, en daarna de database
// en de gebruikers van de app weghalen. Een gebruiker die ook rechten op een
// andere database heeft, of er de laatste dertig dagen een andere
// gebruikte, blijft staan.
//
// Het overzicht zegt per oude server welke verhuisde databases op afbouwen
// wachten, of er nog een database van een app op staat, en welke
// firewallregel van een app naar de oude server niet meer gebruikt wordt.
// De CT's of VM's weghalen doet Jonas zelf; ClusterForge verwijdert geen
// nodes en beheert de firewall niet.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// RetireKind bouwt de oude database van een verhuizing af.
	RetireKind = "component.retire"
	// RetireQuiet is hoe lang niemand van buiten de databasenode met de
	// oude database verbonden mag zijn geweest voor ze weg kan.
	RetireQuiet = 7 * 24 * time.Hour
)

// Wat er met de firewallregel van een app naar de oude server kan.
const (
	// RuleInUse: een node van de app verbond na het overschakelen nog met
	// de oude server.
	RuleInUse = "in_use"
	// RuleUntilRetired: niemand verbindt nog, maar een verhuisde database
	// staat er nog; met de regel kan de app terug.
	RuleUntilRetired = "until_retired"
	// RuleUnused: alles van de app is afgebouwd en niemand verbindt nog.
	RuleUnused = "unused"
)

// RetireMove is een verhuizing die klaar is, met wat er van haar oude
// database over is.
type RetireMove struct {
	Migration   store.DbMigration
	ClusterName string
	Engine      string
	// Exists: de oude database staat er volgens de laatste lezing nog.
	Exists bool
	// LastUsed is de laatste verbinding met de oude database van buiten de
	// databasenode, en LastBy wie dat was; nil als er in dertig dagen geen
	// was. LastLocal is de laatste op de databasenode zelf, zoals een
	// back-up: die houdt het afbouwen niet tegen.
	LastUsed  *time.Time
	LastBy    string
	LastLocal *time.Time
	LocalBy   string
	// ReadyAt is vanaf wanneer afbouwen kan; nil als het al gebeurde.
	ReadyAt *time.Time
	Ready   bool
	// Active is het afbouwen dat wacht of loopt.
	Active *ActiveJob
	// ProdSlug is de slug om in te tikken als het afbouwen een prodcluster
	// raakt; leeg als niet.
	ProdSlug string
}

// RetireRemaining is een database van een app die nog op de oude server
// staat.
type RetireRemaining struct {
	SourceKey string
	Engine    string
	Database  string
	// Moved: ze is verhuisd en wacht op afbouwen.
	Moved bool
}

// RetireRule is de firewallregel van een app naar de oude server.
type RetireRule struct {
	ClusterID   uuid.UUID
	ClusterName string
	Engine      string
	// Address is waarlangs de app de oude database gebruikte; leeg als
	// ClusterForge het niet weet.
	Address string
	Port    int
	// LastSeen is de laatste verbinding van een node van de app met de oude
	// servers; nil als er in dertig dagen geen was.
	LastSeen *time.Time
	State    string
}

// RetireSource is een oude databaseserver waarvan ClusterForge een database
// verhuisde: alle groepen onder Databases op dezelfde nodes, zoals MariaDB
// en PostgreSQL op db1 tot db3.
type RetireSource struct {
	Name      string
	ClusterID *uuid.UUID
	Keys      []string
	Servers   []string
	// Readable: elke groep gaf een volledige lijst met databases.
	Readable bool
	// Empty: er staat geen database van een app meer op.
	Empty     bool
	Remaining []RetireRemaining
	Moves     []RetireMove
	Rules     []RetireRule
}

// RetireOverview geeft de oude databaseservers waarvan ClusterForge een
// database verhuisde.
func (s *Service) RetireOverview(ctx context.Context) ([]RetireSource, error) {
	rows, err := s.q.ListDoneDBMigrations(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []RetireSource{}, nil
	}
	if s.Overview == nil {
		return nil, errors.New("de server leest de databases niet")
	}
	groups, err := s.Overview(ctx)
	if err != nil {
		return nil, err
	}
	out := retireSources(groups, rows, s.Now())
	for i := range out {
		for j := range out[i].Moves {
			m := out[i].Moves[j].Migration
			prod, slug, err := s.prodOf(ctx, m.ClusterID, m.SourceKey)
			if err != nil {
				return nil, err
			}
			if prod {
				out[i].Moves[j].ProdSlug = slug
			}
		}
	}
	return out, nil
}

// sameNodes geeft per groep de groepen op dezelfde nodes, ook via een
// derde: de index van de eerste groep van zijn set.
func sameNodes(groups []databases.Group) []int {
	parent := make([]int, len(groups))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	owner := map[uuid.UUID]int{}
	for i, g := range groups {
		for _, sv := range g.Servers {
			j, ok := owner[sv.NodeID]
			if !ok {
				owner[sv.NodeID] = i
				continue
			}
			a, b := find(i), find(j)
			parent[max(a, b)] = min(a, b)
		}
	}
	out := make([]int, len(groups))
	for i := range groups {
		out[i] = find(i)
	}
	return out
}

// complete zegt of de lijst met databases van g alles bevat.
func complete(g databases.Group) bool {
	return g.DatabasesFrom != "" && g.DatabasesTotal <= len(g.Databases)
}

// appDatabase zegt of een database van een app is: niet van MariaDB,
// PostgreSQL of ClusterForge zelf.
func appDatabase(engine, name string) bool {
	if engine == protocol.DBPostgreSQL && slices.Contains(dbsql.PGSystemDatabases, name) {
		return false
	}
	return name != protocol.DBProbeDatabase
}

func retireSources(groups []databases.Group, rows []store.ListDoneDBMigrationsRow, now time.Time) []RetireSource {
	byKey := map[string]int{}
	for i, g := range groups {
		byKey[g.Key()] = i
	}
	// De nieuwste verhuizing per oude database, als ClusterForge de oude
	// server nog ziet.
	seen := map[string]bool{}
	var moves []store.ListDoneDBMigrationsRow
	for _, r := range rows {
		k := r.DbMigration.SourceKey + "\x00" + r.DbMigration.Database
		if _, ok := byKey[r.DbMigration.SourceKey]; ok && !seen[k] {
			seen[k] = true
			moves = append(moves, r)
		}
	}
	set := sameNodes(groups)
	var roots []int
	for _, r := range moves {
		if root := set[byKey[r.DbMigration.SourceKey]]; !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	out := []RetireSource{}
	for _, root := range roots {
		var members []databases.Group
		for i, g := range groups {
			if set[i] == root {
				members = append(members, g)
			}
		}
		out = append(out, retireSource(members, moves, now))
	}
	return out
}

func retireSource(members []databases.Group, rows []store.ListDoneDBMigrationsRow, now time.Time) RetireSource {
	src := RetireSource{Keys: []string{}, Servers: []string{}, Remaining: []RetireRemaining{}, Moves: []RetireMove{}, Rules: []RetireRule{},
		Readable: true}
	clusters := map[uuid.UUID]bool{}
	byKey := map[string]databases.Group{}
	for _, g := range members {
		byKey[g.Key()] = g
		src.Keys = append(src.Keys, g.Key())
		if g.ClusterID != nil {
			clusters[*g.ClusterID] = true
			src.ClusterID = g.ClusterID
			src.Name = cmp.Or(src.Name, g.ClusterName)
		}
		for _, sv := range g.Servers {
			if !slices.Contains(src.Servers, sv.Hostname) {
				src.Servers = append(src.Servers, sv.Hostname)
			}
		}
		src.Readable = src.Readable && complete(g)
	}
	slices.Sort(src.Servers)
	if len(clusters) != 1 {
		src.ClusterID = nil
	}
	if src.Name == "" || len(clusters) > 1 {
		src.Name = joinHosts(src.Servers)
	}

	// De verhuisde databases.
	open := map[string]bool{}
	for _, r := range rows {
		g, ok := byKey[r.DbMigration.SourceKey]
		if !ok {
			continue
		}
		mv := retireMove(g, r.DbMigration, r.ClusterName, now)
		if r.RetireJobStatus.Valid && (r.RetireJobStatus.JobStatus == store.JobStatusQueued || r.RetireJobStatus.JobStatus == store.JobStatusRunning) &&
			r.DbMigration.RetireJobID != nil {
			mv.Active = &ActiveJob{ID: *r.DbMigration.RetireJobID, Kind: RetireKind, Title: r.RetireJobTitle}
			mv.Ready = false
		}
		if mv.Migration.RetiredAt == nil {
			open[g.Key()+"\x00"+mv.Migration.Database] = true
		}
		src.Moves = append(src.Moves, mv)
	}

	// Wat er nog op staat.
	for _, g := range members {
		for _, d := range g.Databases {
			k := g.Key() + "\x00" + d.Name
			retired := slices.ContainsFunc(src.Moves, func(m RetireMove) bool {
				return m.Migration.SourceKey == g.Key() && m.Migration.Database == d.Name && m.Migration.RetiredAt != nil
			})
			if !appDatabase(g.Engine, d.Name) || retired {
				continue
			}
			src.Remaining = append(src.Remaining, RetireRemaining{SourceKey: g.Key(), Engine: g.Engine, Database: d.Name, Moved: open[k]})
		}
	}
	src.Empty = src.Readable && len(src.Remaining) == 0

	// De firewallregels: per app en engine.
	type ruleKey struct {
		cluster uuid.UUID
		engine  string
	}
	var order []ruleKey
	rules := map[ruleKey]*RetireRule{}
	switched := map[ruleKey]time.Time{}
	retired := map[ruleKey]bool{}
	for _, mv := range src.Moves {
		m := mv.Migration
		k := ruleKey{m.ClusterID, mv.Engine}
		if _, ok := rules[k]; !ok {
			order = append(order, k)
			rules[k] = &RetireRule{ClusterID: m.ClusterID, ClusterName: mv.ClusterName, Engine: mv.Engine, Address: m.SourceAddress,
				Port: enginePort(mv.Engine)}
			retired[k] = true
		}
		if at := deref(m.SwitchedAt); at.After(switched[k]) {
			switched[k] = at
		}
		retired[k] = retired[k] && m.RetiredAt != nil
	}
	for _, k := range order {
		rule := rules[k]
		for _, g := range members {
			if g.Engine != k.engine {
				continue
			}
			for _, u := range g.Clients {
				if u.Kind == databases.UserCluster && u.ClusterID != nil && *u.ClusterID == k.cluster &&
					(rule.LastSeen == nil || u.LastSeen.After(*rule.LastSeen)) {
					t := u.LastSeen
					rule.LastSeen = &t
				}
			}
		}
		switch {
		case rule.LastSeen != nil && rule.LastSeen.After(switched[k]):
			rule.State = RuleInUse
		case !retired[k]:
			rule.State = RuleUntilRetired
		default:
			rule.State = RuleUnused
		}
		src.Rules = append(src.Rules, *rule)
	}
	return src
}

// retireMove zegt wat er met de oude database van verhuizing m in groep g
// kan.
func retireMove(g databases.Group, m store.DbMigration, clusterName string, now time.Time) RetireMove {
	mv := RetireMove{Migration: m, ClusterName: clusterName, Engine: g.Engine, Exists: !complete(g)}
	if i := slices.IndexFunc(g.Databases, func(d databases.Database) bool { return d.Name == m.Database }); i >= 0 {
		mv.Exists = true
		for _, u := range g.Databases[i].Users {
			t := u.LastSeen
			switch {
			case u.Kind == databases.UserLocal:
				if mv.LastLocal == nil || t.After(*mv.LastLocal) {
					mv.LastLocal, mv.LocalBy = &t, u.Describe()
				}
			case mv.LastUsed == nil || t.After(*mv.LastUsed):
				mv.LastUsed, mv.LastBy = &t, u.Describe()
			}
		}
	}
	if m.RetiredAt != nil {
		return mv
	}
	base := cmp.Or(deref(m.SwitchedAt), m.StartedAt)
	if mv.LastUsed != nil && mv.LastUsed.After(base) {
		base = *mv.LastUsed
	}
	at := base.Add(RetireQuiet)
	mv.ReadyAt = &at
	mv.Ready = !now.Before(at)
	return mv
}

// when schrijft een tijdstip zoals 20/10 om 14:05.
func when(t time.Time) string { return t.Local().Format("2/1 om 15:04") }

func retireTitle(database, source string) string {
	return fmt.Sprintf("Afbouwen van %s op %s", database, source)
}

// RetireProd zegt of het afbouwen een prodcluster raakt, en welke slug
// Jonas dan intikt: die van het cluster van de app, anders die van het
// oude cluster.
func (s *Service) RetireProd(ctx context.Context, id uuid.UUID) (bool, string, error) {
	row, err := s.GetMigration(ctx, id)
	if err != nil {
		return false, "", err
	}
	return s.prodOf(ctx, row.DbMigration.ClusterID, row.DbMigration.SourceKey)
}

// retireGroup is de groep van de oude database onder Databases, met alle
// groepen op dezelfde nodes.
func (s *Service) retireGroup(ctx context.Context, key string) (databases.Group, []databases.Group, error) {
	if s.Overview == nil {
		return databases.Group{}, nil, errors.New("de server leest de databases niet")
	}
	groups, err := s.Overview(ctx)
	if err != nil {
		return databases.Group{}, nil, err
	}
	i := slices.IndexFunc(groups, func(g databases.Group) bool { return g.Key() == key })
	if i < 0 {
		return databases.Group{}, nil, &ConflictError{"ClusterForge ziet de oude databaseserver niet meer"}
	}
	set := sameNodes(groups)
	var same []databases.Group
	for j, g := range groups {
		if set[j] == set[i] {
			same = append(same, g)
		}
	}
	return groups[i], same, nil
}

// retirable zegt waarom de oude database van m niet weg kan.
func retirable(m store.DbMigration) error {
	switch {
	case m.Kind != store.DbMigrationKindMigrate || m.Stage != StageDone:
		return &ConflictError{"alleen de oude database van een verhuizing die klaar is, kan weg"}
	case m.RetiredAt != nil:
		return &ConflictError{fmt.Sprintf("de oude database %s is al afgebouwd", m.Database)}
	}
	return nil
}

// retireCheck kijkt of de oude database van m nu weg kan: zeven dagen geen
// verbinding van buiten de databasenode, en nu niemand verbonden. Het geeft
// de groep, de groepen op dezelfde nodes en de server om op te werken.
func (s *Service) retireCheck(ctx context.Context, row store.GetDBMigrationRow) (databases.Group, []databases.Group, *Node, error) {
	m := row.DbMigration
	g, same, err := s.retireGroup(ctx, m.SourceKey)
	if err != nil {
		return g, nil, nil, err
	}
	mv := retireMove(g, m, row.ClusterName, s.Now())
	if !mv.Ready {
		why := fmt.Sprintf("zeven dagen na het overschakelen op %s", when(deref(m.SwitchedAt)))
		if mv.LastUsed != nil && mv.LastUsed.After(deref(m.SwitchedAt)) {
			why = fmt.Sprintf("zeven dagen nadat %s op %s nog met %s verbonden was", mv.LastBy, when(*mv.LastUsed), m.Database)
		}
		return g, nil, nil, &ConflictError{fmt.Sprintf("afbouwen kan vanaf %s: %s", when(*mv.ReadyAt), why)}
	}
	srv, _, err := s.migrationSource(ctx, g)
	if err != nil {
		return g, nil, nil, err
	}
	if srv == nil || srv.ErrorKind != "" {
		return g, nil, nil, &ConflictError{fmt.Sprintf("geen enkele server van %s draait goed genoeg: %s", m.SourceName, g.Summary)}
	}
	if err := s.retireQuiet(ctx, g, m.Database); err != nil {
		return g, nil, nil, err
	}
	return g, same, &Node{NodeID: srv.NodeID, Hostname: srv.Hostname}, nil
}

// retireQuiet zegt wie er nu met de oude database verbonden is, ook op de
// databasenode zelf: zolang iemand verbonden is, gaat ze niet weg.
func (s *Service) retireQuiet(ctx context.Context, g databases.Group, database string) error {
	conns, unread, err := s.liveConnections(ctx, runningServers(g), g.Engine, database)
	switch {
	case err != nil:
		return err
	case len(conns) > 0:
		return &ConflictError{fmt.Sprintf("nog open met %s: %s. Wie gebruikt de oude database nog?", database, describeConnections(conns))}
	case len(unread) > 0:
		return &ConflictError{"wie er met de oude database verbonden is, lukte niet op " + strings.Join(unread, ", ") + "; probeer het straks opnieuw"}
	}
	return nil
}

type retireParams struct {
	MigrationID uuid.UUID `json:"migration_id"`
}

// StartRetire begint het afbouwen van de oude database van een verhuizing.
// Confirm is de naam van de database. Er moeten zeven dagen voorbij zijn
// zonder verbinding van buiten de databasenode, en nu mag niemand
// verbonden zijn.
func (s *Service) StartRetire(ctx context.Context, actor events.Actor, id uuid.UUID, confirm string) (store.Job, error) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	row, err := s.GetMigration(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	m := row.DbMigration
	if err := retirable(m); err != nil {
		return store.Job{}, err
	}
	if confirm != m.Database {
		return store.Job{}, &InvalidError{"name", "tik ter bevestiging de naam van de database in: " + m.Database}
	}
	g, _, _, err := s.retireCheck(ctx, row)
	if err != nil {
		return store.Job{}, err
	}
	var j store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		j, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: RetireKind, Title: retireTitle(m.Database, m.SourceName), Params: retireParams{MigrationID: m.ID}, ClusterID: g.ClusterID,
			Actor: actor,
		})
		if err != nil {
			return err
		}
		_, err = q.StartDBMigrationRetire(ctx, store.StartDBMigrationRetireParams{ID: m.ID, RetireJobID: &j.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return &ConflictError{"de oude database van deze verhuizing wordt al afgebouwd, of is al weg"}
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

const (
	stepRetireCheck = "Controle"
	stepRetireDump  = "Laatste dump"
	stepRetireDrop  = "Database weghalen"
	stepRetireUsers = "Gebruikers weghalen"
	stepRetireLeft  = "Wat er nog op de oude server staat"
)

// retireRoute is wat de controle vond.
type retireRoute struct {
	Engine string `json:"engine"`
	// Source is de server waarop de dump komt en alles weggaat.
	Source Node `json:"source"`
	// Exists: de database stond er bij de controle nog.
	Exists bool `json:"exists"`
	// Servers zijn de nodes van de oude server met een database die
	// draait, en Hostnames al haar nodes.
	Servers   []Node   `json:"servers"`
	Hostnames []string `json:"hostnames"`
}

type retireDump struct {
	File   string `json:"file"`
	Done   bool   `json:"done"`
	Bytes  int64  `json:"bytes"`
	Raw    int64  `json:"raw"`
	Tables int    `json:"tables"`
}

type retireUsers struct {
	Done    bool     `json:"done"`
	Dropped []string `json:"dropped"`
	Kept    []string `json:"kept"`
	// Revoke zijn de opdrachten die de rechten op de oude database weghalen
	// van wie blijft; MariaDB laat ze staan na DROP DATABASE.
	Revoke []string `json:"revoke"`
}

type retireLeft struct {
	Remaining []string `json:"remaining"`
	Unread    []string `json:"unread"`
}

type retireResult struct {
	route retireRoute
	dump  retireDump
	users retireUsers
	left  retireLeft
}

func (s *Service) runRetire(ctx context.Context, j *jobs.Job) error {
	m, err := s.q.GetDBMigrationByRetireJob(ctx, &j.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("deze verhuizing staat niet meer in ClusterForge")
	}
	if err != nil {
		return err
	}
	var res retireResult
	err = s.retireSteps(ctx, j, m, &res)
	if jobs.Interrupted(ctx) {
		return err
	}
	return s.finishRetire(ctx, j, m, &res, err)
}

func (s *Service) retireSteps(ctx context.Context, j *jobs.Job, m store.DbMigration, res *retireResult) error {
	route := &res.route
	if err := j.Step(ctx, stepRetireCheck, func(ctx context.Context, st *jobs.Step) error {
		if st.State(route) {
			return nil
		}
		row, err := s.GetMigration(ctx, m.ID)
		if err != nil {
			return err
		}
		if err := retirable(row.DbMigration); err != nil {
			return err
		}
		g, same, src, err := s.retireCheck(ctx, row)
		if err != nil {
			return err
		}
		route.Engine, route.Source = g.Engine, *src
		for _, gg := range same {
			for _, sv := range gg.Servers {
				if !slices.Contains(route.Hostnames, sv.Hostname) {
					route.Hostnames = append(route.Hostnames, sv.Hostname)
				}
				if sv.Running && !slices.ContainsFunc(route.Servers, func(n Node) bool { return n.NodeID == sv.NodeID }) {
					route.Servers = append(route.Servers, Node{NodeID: sv.NodeID, Hostname: sv.Hostname})
				}
			}
		}
		slices.Sort(route.Hostnames)
		info, err := s.migrateReq(ctx, *src, g.Engine, protocol.DBMigrateRequest{Action: protocol.DBMigrateInspect, Database: m.Database})
		if err != nil {
			return fmt.Errorf("%s bekijken op %s: %w", m.Database, src.Hostname, err)
		}
		route.Exists = info.Info != nil && info.Info.Exists
		st.Logf("Niemand is met %s op %s verbonden.", m.Database, m.SourceName)
		if !route.Exists {
			st.Logf("%s staat niet meer op %s; er komt geen dump, alleen de gebruikers gaan nog weg.", m.Database, src.Hostname)
		}
		return st.SetState(ctx, route)
	}); err != nil {
		return err
	}
	j.Done(stepRetireCheck, route)

	if err := j.Step(ctx, stepRetireDump, func(ctx context.Context, st *jobs.Step) error {
		if !route.Exists {
			st.Logf("Geen database, dus geen dump.")
			return nil
		}
		return s.retireDump(ctx, st, m, route, &res.dump)
	}); err != nil {
		return err
	}
	j.Done(stepRetireDump, &res.dump)

	if err := j.Step(ctx, stepRetireDrop, func(ctx context.Context, st *jobs.Step) error {
		if !route.Exists {
			st.Logf("Niets weg te halen.")
			return nil
		}
		// De dump kan lang duren: nog een keer kijken.
		g, _, err := s.retireGroup(ctx, m.SourceKey)
		if err != nil {
			return err
		}
		if err := s.retireQuiet(ctx, g, m.Database); err != nil {
			return fmt.Errorf("%w. ClusterForge haalde niets weg; de dump blijft staan", err)
		}
		if _, err := s.dbRequest(ctx, dbTarget{node: route.Source, engine: route.Engine}, protocol.DBRequest{Op: protocol.DBDrop,
			Drop: &protocol.DBDropRequest{Database: m.Database, Confirm: m.Database}}); err != nil {
			return fmt.Errorf("%s weghalen op %s: %w", m.Database, route.Source.Hostname, err)
		}
		st.Logf("%s is weg van %s.", m.Database, m.SourceName)
		if s.DBChanged != nil {
			s.DBChanged()
		}
		return nil
	}); err != nil {
		return err
	}

	if err := j.Step(ctx, stepRetireUsers, func(ctx context.Context, st *jobs.Step) error {
		if st.State(&res.users) && res.users.Done {
			return nil
		}
		if err := s.retireUsers(ctx, st, m, route, &res.users); err != nil {
			return err
		}
		res.users.Done = true
		return st.SetState(ctx, &res.users)
	}); err != nil {
		return err
	}
	j.Done(stepRetireUsers, &res.users)

	if err := j.Step(ctx, stepRetireLeft, func(ctx context.Context, st *jobs.Step) error {
		if st.State(&res.left) {
			return nil
		}
		s.retireLeft(ctx, route, &res.left)
		switch {
		case len(res.left.Remaining) > 0:
			st.Logf("Nog op %s: %s.", joinHosts(route.Hostnames), strings.Join(res.left.Remaining, ", "))
		case len(res.left.Unread) > 0:
			st.Logf("Lezen lukte niet op %s.", strings.Join(res.left.Unread, ", "))
		default:
			st.Logf("Op %s staat geen database van een app meer.", joinHosts(route.Hostnames))
		}
		return st.SetState(ctx, &res.left)
	}); err != nil {
		return err
	}
	j.Done(stepRetireLeft, &res.left)
	return nil
}

// retireDump maakt de laatste dump op de server van de controle en wacht
// tot hij klaar is. Is hij niet compleet, dan stopt het afbouwen.
func (s *Service) retireDump(ctx context.Context, st *jobs.Step, m store.DbMigration, route *retireRoute, d *retireDump) error {
	n := route.Source
	if !st.State(d) {
		res, err := s.dumpReq(ctx, n, route.Engine, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: m.Database})
		if err != nil {
			return fmt.Errorf("de dump op %s beginnen: %w", n.Hostname, err)
		}
		d.File = res.Started
		st.Logf("%s begonnen op %s.", res.Started, n.Hostname)
		if err := st.SetState(ctx, d); err != nil {
			return err
		}
	}
	if d.Done {
		return nil
	}
	deadline := s.Now().Add(protocol.DBDumpTimeout + time.Hour)
	reported := int64(0)
	for {
		res, err := s.dumpReq(ctx, n, route.Engine, protocol.DBDumpRequest{Action: protocol.DBDumpList})
		switch {
		case err != nil && ctx.Err() != nil:
			return err
		case err != nil:
			if s.Now().After(deadline) {
				return fmt.Errorf("de dump op %s volgen lukte niet: %w", n.Hostname, err)
			}
		case res.Running != nil && res.Running.File == d.File:
			if b := res.Running.Bytes; b-reported >= 64<<20 {
				reported = b
				st.Logf("%s geschreven.", humanBytes(b))
				_ = st.Flush(ctx)
			}
		default:
			i := slices.IndexFunc(res.Dumps, func(f protocol.DBDumpFile) bool { return f.File == d.File })
			if i < 0 {
				return fmt.Errorf("de dump %s staat niet op %s; stopte de agent tijdens de dump? %s blijft staan", d.File, n.Hostname, m.Database)
			}
			f := res.Dumps[i]
			if why := dumpProblem(f, route.Engine); why != "" {
				return fmt.Errorf("de laatste dump is niet goed: %s. %s blijft staan", why, m.Database)
			}
			d.Done, d.Bytes, d.Raw, d.Tables = true, f.SizeBytes, f.RawBytes, f.Tables
			if err := s.q.SetDBMigrationRetireDump(ctx, store.SetDBMigrationRetireDumpParams{
				ID: m.ID, RetireNode: n.Hostname, RetireFile: d.File, RetireBytes: f.SizeBytes,
			}); err != nil {
				return err
			}
			st.Logf("Klaar: %s, %d tabellen, %s gecomprimeerd, in %s op %s.", humanBytes(f.RawBytes), f.Tables, humanBytes(f.SizeBytes),
				dumpPath(m.Database, d.File), n.Hostname)
			return st.SetState(ctx, d)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(s.DumpPoll):
		}
	}
}

// dumpProblem zegt waarom een dump niet goed genoeg is om de database
// daarna weg te halen.
func dumpProblem(f protocol.DBDumpFile, engine string) string {
	tool := "mariadb-dump"
	if engine == protocol.DBPostgreSQL {
		tool = "pg_dump"
	}
	switch {
	case f.Error != "":
		return f.Error
	case !f.Complete:
		return "hij is niet compleet: de laatste regel van " + tool + " ontbreekt"
	case f.Tables < f.TablesExpected:
		return fmt.Sprintf("er staan %d van de %d tabellen in", f.Tables, f.TablesExpected)
	}
	return ""
}

// dumpPath is waar een dump op de node staat.
func dumpPath(database, file string) string { return path.Join(protocol.DBDumpDir, database, file) }

// retireUsers haalt de gebruikers van de app weg van de oude server, als ze
// niets anders hebben of gebruikten.
func (s *Service) retireUsers(ctx context.Context, st *jobs.Step, m store.DbMigration, route *retireRoute, us *retireUsers) error {
	var accounts []MoveAccount
	_ = json.Unmarshal(m.Accounts, &accounts)
	if len(accounts) == 0 {
		st.Logf("De verhuizing nam geen gebruikers mee.")
		return nil
	}
	g, _, err := s.retireGroup(ctx, m.SourceKey)
	if err != nil {
		return err
	}
	others := otherUses(g, m.Database)
	t := dbTarget{node: route.Source, engine: route.Engine}
	cur, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBUsers})
	if err != nil {
		return fmt.Errorf("de gebruikers op %s lezen: %w", route.Source.Hostname, err)
	}
	drop := func(user string, hosts []string, name string) {
		_, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{User: user, Hosts: hosts, Confirm: user}})
		if err != nil {
			us.Kept = append(us.Kept, name+": weghalen lukte niet: "+err.Error())
			st.Logf("%s weghalen lukte niet: %v", name, err)
			return
		}
		for _, h := range hosts {
			us.Dropped = append(us.Dropped, accountName(user, h))
		}
		if len(hosts) == 0 {
			us.Dropped = append(us.Dropped, user)
		}
		st.Logf("%s weggehaald.", name)
	}
	keep := func(name, why string) {
		us.Kept = append(us.Kept, name+": "+why)
		st.Logf("%s blijft: %s.", name, why)
	}
	pg := route.Engine == protocol.DBPostgreSQL
	for _, a := range accounts {
		if pg {
			if dbsql.CheckPGUser(a.User) != nil {
				continue
			}
			var u *protocol.DBUser
			if i := slices.IndexFunc(cur.Users, func(x protocol.DBUser) bool { return x.Name == a.User }); i >= 0 {
				u = &cur.Users[i]
			}
			if why := keepReason(u, m.Database, others[a.User], true); why != "" {
				keep(a.User, why)
				continue
			}
			drop(a.User, nil, a.User)
			continue
		}
		var hosts []string
		for _, o := range a.Old {
			name := accountName(a.User, o.Host)
			i := slices.IndexFunc(cur.Users, func(x protocol.DBUser) bool { return x.Name == a.User && x.Host == o.Host })
			if i < 0 {
				st.Logf("%s bestond al niet meer.", name)
				continue
			}
			u := cur.Users[i]
			if why := keepReason(&u, m.Database, others[a.User], false); why != "" {
				keep(name, why)
				if slices.ContainsFunc(u.Databases, func(gr protocol.DBGrant) bool { return sameDB(gr.Database, m.Database) }) {
					us.Revoke = append(us.Revoke, fmt.Sprintf("REVOKE ALL PRIVILEGES ON `%s`.* FROM '%s'@'%s';", m.Database, a.User, o.Host))
				}
				continue
			}
			hosts = append(hosts, o.Host)
		}
		if len(hosts) > 0 {
			drop(a.User, hosts, joinHosts(accountNames(a.User, hosts)))
		}
	}
	if s.DBChanged != nil {
		s.DBChanged()
	}
	return nil
}

func accountName(user, host string) string {
	if host == "" {
		return user
	}
	return user + "@" + host
}

func accountNames(user string, hosts []string) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = accountName(user, h)
	}
	return out
}

// sameDB zegt of een recht op naam over database gaat: dezelfde naam, of
// met \_ voor een _.
func sameDB(name, database string) bool {
	return name == database || strings.ReplaceAll(name, `\_`, "_") == database
}

// otherUses zijn per gebruiker de andere databases van g die hij de laatste
// dertig dagen gebruikte.
func otherUses(g databases.Group, database string) map[string][]string {
	out := map[string][]string{}
	for _, d := range g.Databases {
		if d.Name == database {
			continue
		}
		for _, u := range d.Users {
			for _, name := range u.DBUsers {
				if !slices.Contains(out[name], d.Name) {
					out[name] = append(out[name], d.Name)
				}
			}
		}
	}
	return out
}

// keepReason zegt waarom een gebruiker blijft staan; leeg als hij weg kan.
// u is nil als de server hem niet als gebruiker kent, zoals een rol van
// PostgreSQL zonder inloggen.
func keepReason(u *protocol.DBUser, database string, others []string, pg bool) string {
	if len(others) > 0 {
		return "hij gebruikte de laatste 30 dagen ook " + joinHosts(others)
	}
	if u == nil {
		return ""
	}
	global := slices.Clone(u.Global)
	if pg {
		// CREATEDB alleen houdt niemand tegen.
		global = slices.DeleteFunc(global, func(r string) bool { return r == "CREATEDB" })
	}
	if len(global) > 0 {
		if pg {
			return "hij is " + strings.Join(global, ", ")
		}
		return "hij heeft ook rechten op alle databases (" + listSome(global, 3) + ")"
	}
	var dbs []string
	for _, gr := range u.Databases {
		if !sameDB(gr.Database, database) && !slices.Contains(dbs, gr.Database) {
			dbs = append(dbs, gr.Database)
		}
	}
	if len(dbs) > 0 {
		return "hij heeft ook rechten op " + joinHosts(dbs)
	}
	return ""
}

// retireLeft kijkt op de nodes van de oude server welke databases van een
// app er nog staan.
func (s *Service) retireLeft(ctx context.Context, route *retireRoute, left *retireLeft) {
	listed := map[string]bool{}
	tried := map[string]bool{}
	for _, n := range route.Servers {
		res, err := s.dbRequest(ctx, dbTarget{node: n}, protocol.DBRequest{Op: protocol.DBStatus})
		if err != nil {
			left.Unread = append(left.Unread, n.Hostname)
			continue
		}
		for _, sv := range res.Servers {
			tried[sv.Engine] = true
			if !sv.Running || sv.Error != "" {
				continue
			}
			listed[sv.Engine] = true
			for _, d := range sv.Databases {
				name := d.Name + " (" + databases.EngineName(sv.Engine) + ")"
				if appDatabase(sv.Engine, d.Name) && !slices.Contains(left.Remaining, name) {
					left.Remaining = append(left.Remaining, name)
				}
			}
			if sv.DatabasesTotal > len(sv.Databases) {
				more := fmt.Sprintf("meer databases van %s dan de agent toont", databases.EngineName(sv.Engine))
				if !slices.Contains(left.Remaining, more) {
					left.Remaining = append(left.Remaining, more)
				}
			}
		}
	}
	for engine := range tried {
		if !listed[engine] {
			left.Unread = append(left.Unread, "de databases van "+databases.EngineName(engine))
		}
	}
	slices.Sort(left.Remaining)
	slices.Sort(left.Unread)
}

// finishRetire legt de uitkomst vast. Mislukt het, dan blijft de reden bij
// de verhuizing en kan Jonas het opnieuw proberen.
func (s *Service) finishRetire(ctx context.Context, j *jobs.Job, m store.DbMigration, res *retireResult, err error) error {
	wctx, cancel := jobs.Detach(ctx)
	defer cancel()
	if err != nil {
		msg := upperFirst(err.Error()) + "."
		if res.dump.Done {
			msg += fmt.Sprintf("\nDe laatste dump staat op %s: %s.", res.route.Source.Hostname, dumpPath(m.Database, res.dump.File))
		}
		if ferr := s.q.FailDBMigrationRetire(wctx, store.FailDBMigrationRetireParams{RetireJobID: &j.ID, RetireMessage: msg}); ferr != nil {
			s.log.Error("afbouwen vastleggen mislukt", "job", j.ID, "err", ferr)
		}
		return err
	}
	r := res.route
	var lines []string
	switch {
	case res.dump.Done:
		lines = append(lines, fmt.Sprintf("Laatste dump op %s: %s (%s). Hij blijft staan tot je hem weghaalt.", r.Source.Hostname,
			dumpPath(m.Database, res.dump.File), humanBytes(res.dump.Bytes)))
	case !r.Exists:
		lines = append(lines, fmt.Sprintf("%s stond al niet meer op %s, dus er is geen laatste dump.", m.Database, m.SourceName))
	}
	if r.Exists {
		lines = append(lines, fmt.Sprintf("%s is weg van %s.", m.Database, m.SourceName))
	}
	if len(res.users.Dropped) > 0 {
		lines = append(lines, "Weggehaald: "+joinHosts(res.users.Dropped)+".")
	}
	for _, k := range res.users.Kept {
		lines = append(lines, "Blijft staan: "+k+".")
	}
	if len(res.users.Revoke) > 0 {
		lines = append(lines, fmt.Sprintf("Wie blijft, houdt zijn rechten op %s tot je ze weghaalt: %s", m.Database, strings.Join(res.users.Revoke, " ")))
	}
	empty := len(res.left.Remaining) == 0 && len(res.left.Unread) == 0
	switch {
	case len(res.left.Remaining) > 0:
		lines = append(lines, fmt.Sprintf("Nog op %s: %s.", joinHosts(r.Hostnames), strings.Join(res.left.Remaining, ", ")))
	case len(res.left.Unread) > 0:
		lines = append(lines, fmt.Sprintf("Of er nog iets op %s staat, lukte niet te lezen: %s.", joinHosts(r.Hostnames),
			strings.Join(res.left.Unread, ", ")))
	default:
		lines = append(lines, fmt.Sprintf("Op %s staat geen database van een app meer. De CT's of VM's weghalen doe je in Proxmox; "+
			"ClusterForge verwijdert geen nodes.", joinHosts(r.Hostnames)))
	}
	now := s.Now()
	row, ferr := s.q.RetireDBMigration(wctx, store.RetireDBMigrationParams{ID: m.ID, RetiredAt: &now, RetireMessage: strings.Join(lines, "\n")})
	if ferr != nil {
		s.log.Error("afbouwen vastleggen mislukt", "job", j.ID, "err", ferr)
		return ferr
	}
	s.moveEvent(wctx, actorOf(j), "cluster.database_retired", row, map[string]any{
		"dump_node": row.RetireNode, "dump_file": row.RetireFile, "dump_bytes": row.RetireBytes, "dropped": nonNilStrings(res.users.Dropped),
		"kept": nonNilStrings(res.users.Kept), "servers": nonNilStrings(r.Hostnames), "remaining": nonNilStrings(res.left.Remaining), "empty": empty,
	})
	if s.DBChanged != nil {
		s.DBChanged()
	}
	return nil
}

// failRetireOf legt een afbouwen vast dat stopte zonder uitkomst, zoals
// een geannuleerde taak.
func (s *Service) failRetireOf(ctx context.Context, j store.Job) {
	if j.Status == store.JobStatusSucceeded {
		return
	}
	if err := s.q.FailDBMigrationRetire(ctx, store.FailDBMigrationRetireParams{RetireJobID: &j.ID,
		RetireMessage: upperFirst(cmp.Or(j.Error, "de taak stopte")) + "."}); err != nil {
		s.log.Error("afbouwen van een gestopte taak vastleggen mislukt", "job", j.ID, "err", err)
	}
}
