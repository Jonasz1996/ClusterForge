package components

// De echte verhuizing van een database (fase 5, mijlpaal 9).
//
// Jonas zet de app in onderhoud en start de verhuizing. De taak kijkt of
// er nog iemand met de oude database verbonden is, maakt de gebruikers van
// de app op het nieuwe cluster met dezelfde hash als op de oude server,
// laat de nieuwe node de dump ophalen en importeren in de echte naam, en
// telt de rijen aan beide kanten: alles moet gelijk zijn. Wijkt er iets
// af, dan gaan de nieuwe database en de nieuwe gebruikers weer weg en
// staat de app nog op de oude, zonder verlies.
//
// Daarna wacht de verhuizing (stage copied) tot Jonas de app naar het
// nieuwe adres wijst en 'De app draait op de nieuwe database' kiest.
// ClusterForge kijkt dan of niemand meer met de oude database verbonden is
// en de app wel met de nieuwe, en zet de oude op slot: de gebruikers van
// de app mogen haar alleen nog lezen. Eén knop haalt het slot er weer af.
// Annuleren haalt de kopie weg, zolang niemand haar gebruikt.
//
// De hash van een wachtwoord gaat alleen van de oude agent via de server
// naar de nieuwe, in het geheugen; ClusterForge bewaart hem nergens.
//
// Bij PostgreSQL gaan de rollen mee met hun SCRAM-verifier, ook de rollen
// zonder inloggen die iets in de database bezitten of er rechten op hebben,
// en wie daar lid van is. De nieuwe database krijgt dezelfde eigenaar, en
// de dump neemt de eigenaars en rechten mee. Het slot zet
// default_transaction_read_only op de oude database zelf, voor iedereen.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
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
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// MigrateKind is de echte verhuizing.
	MigrateKind = "component.migrate"

	// De stappen van een verhuizing in db_migrations.stage.
	StageCopy      = "copy"
	StageCopied    = "copied"
	StageDone      = "done"
	StageCancelled = "cancelled"
)

// ErrMigrationNotFound: die verhuizing kent ClusterForge niet.
var ErrMigrationNotFound = errors.New("die verhuizing kent ClusterForge niet")

// errMoveDiffers: de nieuwe database is niet gelijk aan de oude; wat er
// verschilt, staat in het bericht van de vergelijking.
var errMoveDiffers = errors.New("de nieuwe database is niet gelijk aan de oude")

// MigrationConnection zijn de verbindingen van één gebruiker vanaf één
// adres met de database, op één server.
type MigrationConnection struct {
	Server  string
	User    string
	Address string
	// Hostname is de node met dat adres, als ClusterForge hem kent.
	Hostname string
	Count    int
}

func (c MigrationConnection) String() string {
	return fmt.Sprintf("%s vanaf %s op %s: %d", c.User, cmp.Or(c.Hostname, c.Address, "de server zelf"), c.Server, c.Count)
}

// MoveAccount is een gebruiker van de app die meeverhuist.
type MoveAccount struct {
	User string `json:"user"`
	// Old zijn zijn accounts op de oude server, met het schema en de
	// rechten zoals daar: het slot gebruikt ze. Zonder hash.
	Old []protocol.DBMigrateAccount `json:"old"`
	// Privileges zijn zijn rechten op de nieuwe database, Hosts waarvandaan
	// hij daar mag inloggen, en Created de hosts die ClusterForge nieuw
	// maakte: annuleren haalt alleen die weg.
	Privileges []string `json:"privileges"`
	Hosts      []string `json:"hosts"`
	Created    []string `json:"created"`
	// New: bij PostgreSQL maakte ClusterForge de rol nieuw; een rol heeft
	// daar geen hosts.
	New bool `json:"new,omitempty"`
}

// created zegt of ClusterForge iets van a nieuw maakte.
func (a MoveAccount) created() bool { return len(a.Created) > 0 || a.New }

// movable zegt of een verhuizing a kan meenemen.
func movable(a protocol.DBMigrateAccount) bool {
	return a.Plugin == protocol.DBMigratePlugin && dbsql.CheckAppAccount(a) == nil
}

// movablePG zegt of een verhuizing de rol a van PostgreSQL kan meenemen.
func movablePG(a protocol.DBMigrateAccount) bool { return dbsql.CheckPGAccount(a) == nil }

// Movable zegt of de echte verhuizing van een database van engine de
// gebruiker a meeneemt.
func Movable(engine string, a protocol.DBMigrateAccount) bool {
	if engine == protocol.DBPostgreSQL {
		return movablePG(a)
	}
	return movable(a)
}

// movePGAccounts zijn de rollen van PostgreSQL die meegaan.
func movePGAccounts(accounts []protocol.DBMigrateAccount) []MoveAccount {
	var out []MoveAccount
	for _, a := range accounts {
		if !movablePG(a) {
			continue
		}
		a.Hash = ""
		out = append(out, MoveAccount{User: a.User, Old: []protocol.DBMigrateAccount{a}, Privileges: slices.Clone(a.Privileges), Hosts: []string{},
			Created: []string{}})
	}
	return out
}

// checkAccounts zegt welke gebruikers niet meegaan.
func checkAccounts(accounts []protocol.DBMigrateAccount, clients []databases.User, database string, add func(string, string, *Node, string, string)) {
	var plugin, other, global []string
	users := map[string]bool{}
	for _, a := range accounts {
		name := a.User + "@" + a.Host
		switch {
		case dbsql.CheckAppAccount(a) != nil:
			if !strings.HasPrefix(a.User, protocol.DBMigrateUserPrefix) && !slices.Contains(dbsql.SystemUsers, a.User) {
				other = append(other, name+": "+dbsql.CheckAppAccount(a).Error())
			}
		case a.Plugin != protocol.DBMigratePlugin:
			plugin = append(plugin, name+" ("+a.Plugin+")")
		default:
			users[a.User] = true
			if a.Global && !slices.Contains(global, a.User) {
				global = append(global, a.User)
			}
		}
	}
	if len(plugin) > 0 {
		add(Warning, "accounts_plugin", nil, fmt.Sprintf("%s niet met een gewoon wachtwoord in: %s. ClusterForge neemt alleen gebruikers met een "+
			"wachtwoord mee; maak de rest op het nieuwe cluster zelf als de app ze nodig heeft", plural(len(plugin), "één gebruiker logt", "gebruikers loggen"),
			listSome(plugin, 5)), "")
	}
	if len(other) > 0 {
		add(Warning, "accounts_other", nil, fmt.Sprintf("deze gebruikers gaan niet mee: %s", strings.Join(other, "; ")), "")
	}
	if len(global) > 0 {
		add(Info, "accounts_global", nil, fmt.Sprintf("%s heeft ook rechten op alle databases van de oude server. Op het nieuwe cluster krijgt %s "+
			"alleen rechten op %s, en het slot op de oude database houdt die andere rechten niet tegen", joinHosts(global),
			plural(len(global), "hij", "elk"), database), "")
	}
	var missing []string
	for _, c := range clients {
		for _, u := range c.DBUsers {
			if !users[u] && !strings.HasPrefix(u, protocol.DBMigrateUserPrefix) && !slices.Contains(missing, u) && !slices.Contains(dbsql.SystemUsers, u) {
				missing = append(missing, u)
			}
		}
	}
	switch {
	case len(users) == 0:
		add(Warning, "accounts_none", nil, fmt.Sprintf("geen gebruiker heeft rechten op %s zelf, dus er gaat er geen mee. Maak de gebruiker van de app op "+
			"het nieuwe cluster zelf, bij Databases en gebruikers", database), "")
	case len(missing) > 0:
		add(Warning, "accounts_missing", nil, fmt.Sprintf("%s gebruikte %s maar heeft er zelf geen rechten op, alleen op alle databases; die gaat niet mee",
			joinHosts(missing), database), "")
	}
}

// moveAccounts zijn de gebruikers die meegaan, per gebruiker, met alle
// rechten die hij op de oude server op een van zijn hosts had.
func moveAccounts(accounts []protocol.DBMigrateAccount, hosts []string) []MoveAccount {
	var out []MoveAccount
	for _, a := range accounts {
		if !movable(a) {
			continue
		}
		a.Hash = ""
		i := slices.IndexFunc(out, func(m MoveAccount) bool { return m.User == a.User })
		if i < 0 {
			out = append(out, MoveAccount{User: a.User, Hosts: hosts, Created: []string{}})
			i = len(out) - 1
		}
		m := &out[i]
		m.Old = append(m.Old, a)
		for _, p := range a.Privileges {
			if !slices.Contains(m.Privileges, p) {
				m.Privileges = append(m.Privileges, p)
			}
		}
	}
	for i := range out {
		slices.Sort(out[i].Privileges)
	}
	return out
}

// oldAccounts zijn de accounts op de oude server van alle gebruikers.
func oldAccounts(accounts []MoveAccount) []protocol.DBMigrateAccount {
	var out []protocol.DBMigrateAccount
	for _, a := range accounts {
		out = append(out, a.Old...)
	}
	return out
}

// liveConnections leest op elke server wie nu met database van engine
// verbonden is, zonder de tijdelijke gebruikers van een verhuizing. Unread
// zijn de servers die niet antwoordden.
func (s *Service) liveConnections(ctx context.Context, servers []Node, engine, database string) ([]MigrationConnection, []string, error) {
	out := []MigrationConnection{}
	var unread []string
	if len(servers) == 0 {
		return out, nil, nil
	}
	nodes, err := s.q.ListNodes(ctx)
	if err != nil {
		return nil, nil, err
	}
	byIP := map[string]string{}
	for _, n := range nodes {
		if n.Node.PrimaryIp != nil {
			byIP[n.Node.PrimaryIp.String()] = n.Node.Hostname
		}
	}
	for _, n := range servers {
		res, err := s.dbRequest(ctx, dbTarget{node: n}, protocol.DBRequest{Op: protocol.DBStatus})
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, context.Cause(ctx)
			}
			unread = append(unread, n.Hostname)
			continue
		}
		for _, sv := range res.Servers {
			if sv.Engine != engine {
				continue
			}
			for _, c := range sv.Clients {
				if c.Database != database || strings.HasPrefix(c.User, protocol.DBMigrateUserPrefix) {
					continue
				}
				out = append(out, MigrationConnection{Server: n.Hostname, User: c.User, Address: c.Address, Hostname: byIP[c.Address], Count: c.Count})
			}
		}
	}
	return out, unread, nil
}

func describeConnections(conns []MigrationConnection) string {
	total := 0
	var list []string
	for _, c := range conns {
		total += c.Count
		list = append(list, c.String())
	}
	return fmt.Sprintf("%s (%s)", plural(total, "één verbinding", "verbindingen"), listSome(list, 6))
}

func moveTitle(database, source, cluster string) string {
	return fmt.Sprintf("Verhuizing van %s van %s naar %s", database, source, cluster)
}

// MoveProd zegt of een verhuizing een prodcluster raakt, en welke slug
// Jonas dan intikt: die van het nieuwe cluster, anders die van het oude.
func (s *Service) MoveProd(ctx context.Context, source string, componentID uuid.UUID) (bool, string, error) {
	r, err := s.q.GetClusterComponent(ctx, componentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", &InvalidError{"component", "kies een cluster met een database van ClusterForge die draait"}
	}
	if err != nil {
		return false, "", err
	}
	c, err := s.q.GetCluster(ctx, r.ClusterID)
	if err != nil {
		return false, "", err
	}
	if c.Environment == store.EnvironmentProd {
		return true, c.Slug, nil
	}
	if engine, rest, ok := strings.Cut(source, "/"); ok && engine != "" {
		if id, err := uuid.Parse(rest); err == nil {
			c, err := s.q.GetCluster(ctx, id)
			switch {
			case err == nil && c.Environment == store.EnvironmentProd:
				return true, c.Slug, nil
			case err != nil && !errors.Is(err, pgx.ErrNoRows):
				return false, "", err
			}
		}
	}
	return false, "", nil
}

type moveParams struct {
	ComponentID uuid.UUID `json:"component_id"`
	Source      string    `json:"source"`
	Database    string    `json:"database"`
	Force       bool      `json:"force"`
}

// StartMove begint de echte verhuizing. Het plan mag niets blokkeren, en
// zonder force mag er niemand meer met de oude database verbonden zijn.
func (s *Service) StartMove(ctx context.Context, actor events.Actor, source, database string, componentID uuid.UUID, force bool) (store.Job, error) {
	p, _, err := s.migrationPlan(ctx, source, database, &componentID, uuid.Nil)
	if err != nil {
		return store.Job{}, err
	}
	if p.Active != nil {
		return store.Job{}, &ConflictError{"er loopt al een proef of verhuizing van " + database + " (" + p.Active.Title + "); wacht tot die klaar is"}
	}
	if why := moveBlockers(p); len(why) > 0 {
		return store.Job{}, &ConflictError{"zo kan de verhuizing niet: " + strings.Join(why, "; ")}
	}
	if p.Target == nil {
		return store.Job{}, &InvalidError{"component", "kies het cluster waar de database naartoe gaat"}
	}
	if len(p.Connections) > 0 && !force {
		return store.Job{}, &ConflictError{fmt.Sprintf("nog open met %s: %s. Zet de app eerst in onderhoud, of verhuis toch", database,
			describeConnections(p.Connections))}
	}
	t := p.Target
	var j store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		j, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: MigrateKind, Title: moveTitle(database, p.Source.Name, t.ClusterName),
			Params: moveParams{ComponentID: t.ComponentID, Source: source, Database: database, Force: force}, ClusterID: &t.ClusterID, Actor: actor,
		})
		if err != nil {
			return err
		}
		_, err = q.InsertDBMigration(ctx, store.InsertDBMigrationParams{
			Kind: store.DbMigrationKindMigrate, ComponentID: t.ComponentID, ClusterID: t.ClusterID, SourceKey: source, SourceName: p.Source.Name,
			Database: database, TargetDatabase: database, StartedAt: s.Now(), JobID: &j.ID, RequestedBy: j.RequestedBy, Stage: StageCopy, Force: force,
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

// moveResult is wat een verhuizing opleverde.
type moveResult struct {
	trialResult
	accounts []MoveAccount
	// adopted: ClusterForge begon de gebruikers te maken.
	adopted bool
}

// moveUsers is de state van de stap die de gebruikers maakt.
type moveUsers struct {
	Accounts []MoveAccount `json:"accounts"`
	Done     bool          `json:"done"`
}

const (
	stepMoveCheck   = "Controle"
	stepMoveQuiet   = "Onderhoud: niemand meer verbonden"
	stepMoveCompare = "Rijen en objecten vergelijken"
)

func (s *Service) runMove(ctx context.Context, j *jobs.Job) error {
	var p moveParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	m, err := s.q.GetDBMigrationByJob(ctx, &j.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("deze verhuizing staat niet meer in ClusterForge")
	}
	if err != nil {
		return err
	}
	var res moveResult
	err = s.moveSteps(ctx, j, p, m, &res)
	if jobs.Interrupted(ctx) {
		return err
	}
	return s.finishMove(ctx, j, m, &res, err)
}

func (s *Service) moveSteps(ctx context.Context, j *jobs.Job, p moveParams, m store.DbMigration, res *moveResult) error {
	route := &res.route
	if err := j.Step(ctx, stepMoveCheck, func(ctx context.Context, st *jobs.Step) error {
		if st.State(route) {
			return nil
		}
		plan, r, err := s.migrationPlan(ctx, p.Source, p.Database, &p.ComponentID, m.ID)
		if err != nil {
			return err
		}
		if why := moveBlockers(plan); len(why) > 0 {
			return errors.New(strings.Join(why, "; "))
		}
		for _, f := range append(slices.Clone(plan.Findings), plan.MoveFindings...) {
			if f.Level == Warning {
				st.Logf("Let op: %s.", f.Message)
			}
		}
		if plan.Target == nil || plan.Target.ComponentID != p.ComponentID {
			return errors.New("de database van het cluster is er niet meer")
		}
		r.Target, r.Leftover, r.Move = p.Database, false, true
		*route = r
		st.Logf("%s staat op %s: %s, %d tabellen.", p.Database, r.SourceNode.Hostname, humanBytes(r.Size), len(r.Tables))
		st.Logf("%s haalt de dump op van %s:%d en importeert hem als %s; de app vindt de database straks op %s.", r.Import.Hostname, r.Address, r.Port,
			r.Target, r.TargetAddress)
		if err := s.q.SetDBMigrationRoute(ctx, store.SetDBMigrationRouteParams{
			ID: m.ID, SourceNode: r.SourceNode.Hostname, SourceAddress: r.Address, TargetNode: r.Import.Hostname, SizeBytes: r.Size,
			TargetAddress: r.TargetAddress,
		}); err != nil {
			return err
		}
		return st.SetState(ctx, r)
	}); err != nil {
		return err
	}
	j.Done(stepMoveCheck, route)

	if err := j.Step(ctx, stepMoveQuiet, func(ctx context.Context, st *jobs.Step) error {
		var done bool
		if st.State(&done) && done {
			return nil
		}
		deadline := s.Now().Add(s.MoveDrain)
		for {
			conns, unread, err := s.liveConnections(ctx, route.Servers, route.engine(), p.Database)
			if err != nil {
				return err
			}
			if len(conns) == 0 && len(unread) == 0 {
				st.Logf("Niemand is nog met %s op %s verbonden.", p.Database, joinHosts(nodeNames(route.Servers)))
				return st.SetState(ctx, true)
			}
			var why []string
			if len(conns) > 0 {
				why = append(why, "nog open met "+p.Database+": "+describeConnections(conns))
			}
			if len(unread) > 0 {
				why = append(why, "wie er verbonden is, lukte niet op "+strings.Join(unread, ", "))
			}
			if p.Force {
				st.Logf("Toch verder, zoals gevraagd: %s.", strings.Join(why, "; "))
				return st.SetState(ctx, true)
			}
			if !s.Now().Before(deadline) {
				return fmt.Errorf("%s. Zet de app in onderhoud, voor Nextcloud met 'sudo -u www-data php occ maintenance:mode --on', en begin opnieuw", strings.Join(why, "; "))
			}
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-time.After(s.DumpPoll):
			}
		}
	}); err != nil {
		return err
	}

	usersStep := "Gebruikers maken op " + route.Import.Hostname
	var us moveUsers
	err := j.Step(ctx, usersStep, func(ctx context.Context, st *jobs.Step) error {
		return s.moveUsers(ctx, st, m, p.Database, route, &us, res)
	})
	j.Done(usersStep, &us)
	if len(us.Accounts) > 0 {
		res.accounts, res.adopted = us.Accounts, true
	}
	if err != nil {
		return err
	}

	pullStep := fmt.Sprintf("%s ophalen van %s en importeren op %s", p.Database, route.SourceNode.Hostname, route.Import.Hostname)
	var pull trialPull
	tp := trialParams{Database: p.Database}
	if route.pg() {
		tp.Grants = oldAccounts(res.accounts)
	}
	err = j.Step(ctx, pullStep, func(ctx context.Context, st *jobs.Step) error {
		return s.trialPull(ctx, j, st, tp, route, &pull, &res.trialResult)
	})
	j.Done(pullStep, &pull)
	if pull.Run != nil {
		res.run, res.pulled = pull.Run, true
	}
	if err != nil {
		return err
	}

	err = j.Step(ctx, stepMoveCompare, func(ctx context.Context, st *jobs.Step) error {
		compareTrial(route, pull.Run, &res.trialResult, true)
		for _, line := range strings.Split(res.message, "\n") {
			st.Logf("%s", line)
		}
		if res.failed {
			return errMoveDiffers
		}
		return nil
	})
	if res.message == "" {
		compareTrial(route, pull.Run, &res.trialResult, true)
	}
	return err
}

// moveUsers maakt de gebruikers van de app op het nieuwe cluster, met de
// hash van hun wachtwoord op de oude server. Een hervatte stap leest de
// hashes opnieuw; welke hosts nieuw waren, staat al in de state.
func (s *Service) moveUsers(ctx context.Context, st *jobs.Step, m store.DbMigration, database string, route *migrationRoute, us *moveUsers, res *moveResult) error {
	if st.State(us) && us.Done {
		return nil
	}
	got, err := s.migrateReq(ctx, route.SourceNode, route.engine(), protocol.DBMigrateRequest{Action: protocol.DBMigrateAccounts, Database: database, Secret: true})
	if err != nil {
		return err
	}
	if route.pg() {
		return s.movePGUsers(ctx, st, m, database, route, got.Accounts, us, res)
	}
	// De hash per gebruiker: dezelfde op al zijn hosts.
	hashes := map[string]string{}
	for _, a := range got.Accounts {
		if !movable(a) {
			continue
		}
		h := strings.ToUpper(a.Hash)
		if prev, ok := hashes[a.User]; ok && prev != h {
			return fmt.Errorf("%s heeft op %s niet op elke host hetzelfde wachtwoord; maak ze gelijk, of maak %s op het nieuwe cluster zelf en "+
				"haal zijn rechten op %s op de oude server weg", a.User, route.SourceNode.Hostname, a.User, database)
		}
		hashes[a.User] = h
	}
	if us.Accounts == nil {
		accounts := moveAccounts(got.Accounts, route.NewHosts)
		if len(accounts) > 0 {
			existing, err := s.dbRequest(ctx, dbTarget{node: route.Import}, protocol.DBRequest{Op: protocol.DBUsers})
			if err != nil {
				return err
			}
			for i := range accounts {
				a := &accounts[i]
				for _, h := range a.Hosts {
					if !slices.ContainsFunc(existing.Users, func(u protocol.DBUser) bool { return u.Name == a.User && u.Host == h }) {
						a.Created = append(a.Created, h)
					}
				}
			}
		}
		us.Accounts = accounts
		raw, _ := json.Marshal(accounts)
		if err := s.q.SetDBMigrationAccounts(ctx, store.SetDBMigrationAccountsParams{ID: m.ID, Accounts: raw}); err != nil {
			return err
		}
		if err := st.SetState(ctx, us); err != nil {
			return err
		}
	}
	if len(us.Accounts) == 0 {
		st.Logf("Geen gebruiker om mee te nemen. Maak de gebruiker van de app op het nieuwe cluster zelf, bij Databases en gebruikers.")
		us.Done = true
		return st.SetState(ctx, us)
	}
	res.accounts, res.adopted = us.Accounts, true
	var adopt []protocol.DBMigrateAccount
	for _, a := range us.Accounts {
		hash, ok := hashes[a.User]
		if !ok {
			return fmt.Errorf("%s staat niet meer met rechten op %s op %s", a.User, database, route.SourceNode.Hostname)
		}
		for _, h := range a.Hosts {
			adopt = append(adopt, protocol.DBMigrateAccount{User: a.User, Host: h, Plugin: protocol.DBMigratePlugin, Hash: hash, Privileges: a.Privileges})
		}
	}
	if _, err := s.migrateReq(ctx, route.Import, route.engine(), protocol.DBMigrateRequest{Action: protocol.DBMigrateAdopt, Database: database, Accounts: adopt}); err != nil {
		return err
	}
	for _, a := range us.Accounts {
		var old []string
		for _, o := range a.Old {
			old = append(old, o.Host)
		}
		st.Logf("Gebruiker %s op %s, met hetzelfde wachtwoord als op %s (%s), vanaf %s, met %s op %s.", a.User, route.Import.Hostname,
			route.SourceNode.Hostname, strings.Join(old, ", "), joinHosts(a.Hosts), rightsText(a.Privileges), database)
	}
	us.Done = true
	return st.SetState(ctx, us)
}

// movePGUsers maakt de rollen van PostgreSQL op de primary van het nieuwe
// cluster, met dezelfde SCRAM-verifier als op de oude server, en maakt ze
// lid van elkaar zoals daar. De agent zegt welke rollen er nog niet waren;
// alleen die haalt annuleren weer weg.
func (s *Service) movePGUsers(ctx context.Context, st *jobs.Step, m store.DbMigration, database string, route *migrationRoute,
	got []protocol.DBMigrateAccount, us *moveUsers, res *moveResult) error {
	var adopt []protocol.DBMigrateAccount
	for _, a := range got {
		if movablePG(a) {
			adopt = append(adopt, a)
		}
	}
	save := func() error {
		raw, _ := json.Marshal(us.Accounts)
		if err := s.q.SetDBMigrationAccounts(ctx, store.SetDBMigrationAccountsParams{ID: m.ID, Accounts: raw}); err != nil {
			return err
		}
		return st.SetState(ctx, us)
	}
	if us.Accounts == nil {
		us.Accounts = movePGAccounts(got)
		if us.Accounts == nil {
			us.Accounts = []MoveAccount{}
		}
		if err := save(); err != nil {
			return err
		}
	}
	if len(us.Accounts) == 0 {
		st.Logf("Geen rol om mee te nemen. Maak de gebruiker van de app op het nieuwe cluster zelf, bij Databases en gebruikers.")
		us.Done = true
		return st.SetState(ctx, us)
	}
	res.accounts, res.adopted = us.Accounts, true
	for _, a := range us.Accounts {
		if !slices.ContainsFunc(adopt, func(x protocol.DBMigrateAccount) bool { return x.User == a.User }) {
			return fmt.Errorf("%s staat niet meer met rechten in %s op %s", a.User, database, route.SourceNode.Hostname)
		}
	}
	r, err := s.migrateReq(ctx, route.Import, route.engine(), protocol.DBMigrateRequest{Action: protocol.DBMigrateAdopt, Database: database, Accounts: adopt})
	// Ook als een rol mislukte: wat er al nieuw is, haalt opruimen weg.
	if r != nil {
		for _, c := range r.Accounts {
			if i := slices.IndexFunc(us.Accounts, func(a MoveAccount) bool { return a.User == c.User }); i >= 0 {
				us.Accounts[i].New = true
			}
		}
		res.accounts = us.Accounts
		if serr := save(); serr != nil && err == nil {
			return serr
		}
	}
	if err != nil {
		return err
	}
	for _, a := range us.Accounts {
		how := "met hetzelfde wachtwoord als op " + route.SourceNode.Hostname
		switch a.Old[0].Plugin {
		case dbsql.PGLoginNone:
			how = "zonder inloggen"
		case dbsql.PGLoginNoSecret:
			how = "zonder wachtwoord; geef hem er een bij Databases en gebruikers"
		}
		made := "nieuw"
		if !a.New {
			made = "bestond al en kreeg het wachtwoord van de oude server"
			if a.Old[0].Plugin == dbsql.PGLoginNone {
				made = "bestond al"
			}
		}
		member := ""
		if len(a.Old[0].MemberOf) > 0 {
			member = ", lid van " + joinHosts(a.Old[0].MemberOf)
		}
		st.Logf("Rol %s op %s (%s), %s%s.", a.User, route.Import.Hostname, made, how, member)
	}
	us.Done = true
	return st.SetState(ctx, us)
}

// rightsText zijn de rechten voor mensen: alle rechten, of de lijst.
func rightsText(rights []string) string {
	for _, r := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "INDEX", "ALTER"} {
		if !slices.Contains(rights, r) {
			return strings.Join(rights, ", ")
		}
	}
	return "alle rechten"
}

func nodeNames(nodes []Node) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Hostname)
	}
	return out
}

// dropMoved haalt de nieuwe database en de gebruikers die ClusterForge
// maakte weer weg van het nieuwe cluster.
func (s *Service) dropMoved(ctx context.Context, node Node, engine, database string, dropDB bool, accounts []MoveAccount) error {
	var errs []error
	t := dbTarget{node: node, engine: engine}
	if dropDB {
		if _, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{
			Database: database, Confirm: database,
		}}); err != nil {
			errs = append(errs, fmt.Errorf("%s weghalen: %w", database, err))
		}
	}
	for _, a := range accounts {
		if !a.created() {
			continue
		}
		if _, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{
			User: a.User, Hosts: a.Created, Confirm: a.User,
		}}); err != nil {
			errs = append(errs, fmt.Errorf("gebruiker %s weghalen: %w", a.User, err))
		}
	}
	if s.DBChanged != nil {
		s.DBChanged()
	}
	return errors.Join(errs...)
}

// finishMove legt de uitkomst van het kopiëren vast. Gelukt: de verhuizing
// wacht op overschakelen. Mislukt: de nieuwe database en de nieuwe
// gebruikers gaan weg, en de app staat nog op de oude database.
func (s *Service) finishMove(ctx context.Context, j *jobs.Job, m store.DbMigration, res *moveResult, err error) error {
	wctx, cancel := jobs.Detach(ctx)
	defer cancel()
	tables, objects := []byte("[]"), []byte("[]")
	var importMs, countMs *int64
	var dumpBytes int64
	if r := res.run; r != nil {
		dumpBytes = r.Bytes
		if !r.Imported.IsZero() {
			importMs = ptr(r.Imported.Sub(r.Started).Milliseconds())
			if !r.Finished.IsZero() {
				countMs = ptr(r.Finished.Sub(r.Imported).Milliseconds())
			}
		}
		if len(r.Tables) > 0 {
			tables, _ = json.Marshal(r.Tables)
		}
	}
	if len(res.objects) > 0 {
		objects, _ = json.Marshal(res.objects)
	}
	differences := int32(min(res.differences, math.MaxInt32)) //nolint:gosec // begrensd
	if err == nil && !res.failed {
		now := s.Now()
		row, ferr := s.q.CopiedDBMigration(wctx, store.CopiedDBMigrationParams{
			ID: m.ID, CopiedAt: &now, DumpBytes: dumpBytes, ImportMs: importMs, CountMs: countMs, Tables: tables, Objects: objects,
			Differences: differences, Message: res.message,
		})
		if ferr != nil {
			s.log.Error("verhuizing vastleggen mislukt", "job", j.ID, "err", ferr)
			return ferr
		}
		s.moveEvent(wctx, actorOf(j), "cluster.database_move_copied", row, nil)
		if s.DBChanged != nil {
			s.DBChanged()
		}
		return nil
	}
	if err == nil {
		err = errMoveDiffers
	}
	msg := res.message
	if !errors.Is(err, errMoveDiffers) || msg == "" {
		msg = strings.TrimPrefix(res.message+"\n"+err.Error(), "\n")
	}
	if res.route.Import.Hostname != "" && (res.pulled || res.adopted) {
		var gone []string
		if res.pulled {
			gone = append(gone, "de nieuwe database "+m.Database)
		}
		for _, a := range res.accounts {
			if a.created() {
				gone = append(gone, "gebruiker "+a.User)
			}
		}
		if derr := s.dropMoved(wctx, res.route.Import, res.route.engine(), m.Database, res.pulled, res.accounts); derr != nil {
			msg += "\nOpruimen op " + res.route.Import.Hostname + " lukte niet helemaal: " + derr.Error()
		} else if len(gone) > 0 {
			msg += fmt.Sprintf("\n%s weggehaald van %s.", upperFirst(joinHosts(gone)), res.route.Import.Hostname)
		}
	}
	msg += "\nDe app gebruikt nog de oude database; zet het onderhoud weer uit."
	now := s.Now()
	row, ferr := s.q.FinishDBMigration(wctx, store.FinishDBMigrationParams{
		ID: m.ID, Status: store.DbMigrationStatusFailed, FinishedAt: &now, Message: msg, DumpBytes: dumpBytes, ImportMs: importMs, CountMs: countMs,
		Tables: tables, Objects: objects, Differences: differences,
	})
	if ferr != nil {
		s.log.Error("verhuizing vastleggen mislukt", "job", j.ID, "err", ferr)
		row = m
	}
	s.moveEvent(wctx, actorOf(j), "cluster.database_move_failed", row, nil)
	return err
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Service) moveEvent(ctx context.Context, actor events.Actor, action string, m store.DbMigration, extra map[string]any) {
	c, err := s.q.GetCluster(ctx, m.ClusterID)
	if err == nil {
		payload := map[string]any{
			"name": c.Name, "source": m.SourceName, "source_node": m.SourceNode, "database": m.Database, "node": m.TargetNode,
			"address": m.TargetAddress, "size_bytes": m.SizeBytes, "differences": m.Differences, "message": m.Message, "migration_id": m.ID,
		}
		if m.ImportMs != nil {
			payload["import_ms"] = *m.ImportMs
		}
		var accounts []MoveAccount
		_ = json.Unmarshal(m.Accounts, &accounts)
		var users []string
		for _, a := range accounts {
			users = append(users, a.User)
		}
		payload["users"] = nonNilStrings(users)
		for k, v := range extra {
			payload[k] = v
		}
		err = s.ev.Write(ctx, s.q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: action, Payload: payload,
		})
	}
	if err != nil {
		s.log.Error("event schrijven mislukt", "action", action, "err", err)
	}
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// GetMigration geeft één proef of verhuizing.
func (s *Service) GetMigration(ctx context.Context, id uuid.UUID) (store.GetDBMigrationRow, error) {
	row, err := s.q.GetDBMigration(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, ErrMigrationNotFound
	}
	return row, err
}

// openMove is een verhuizing die gekopieerd is en op overschakelen wacht.
func (s *Service) openMove(ctx context.Context, id uuid.UUID) (store.GetDBMigrationRow, error) {
	row, err := s.GetMigration(ctx, id)
	if err != nil {
		return row, err
	}
	m := row.DbMigration
	if m.Kind != store.DbMigrationKindMigrate || m.Stage != StageCopied || m.Status != store.DbMigrationStatusRunning {
		return row, &ConflictError{"deze verhuizing wacht niet op overschakelen"}
	}
	return row, nil
}

// moveSource is de groep van de oude database en de server om op te
// schrijven: bij Galera een server die Synced is, bij replicatie de
// primary.
func (s *Service) moveSource(ctx context.Context, key string) (databases.Group, *Node, error) {
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
	g := groups[i]
	srv, _, err := s.migrationSource(ctx, g)
	if err != nil {
		return g, nil, err
	}
	if srv == nil || srv.ErrorKind != "" {
		return g, nil, &ConflictError{fmt.Sprintf("geen enkele server van %s draait goed genoeg: %s", cmp.Or(g.ClusterName, key), g.Summary)}
	}
	return g, &Node{NodeID: srv.NodeID, Hostname: srv.Hostname}, nil
}

// engineOfKey is de engine van de oude database, uit de sleutel van haar
// groep onder Databases.
func engineOfKey(key string) string {
	engine, _, _ := strings.Cut(key, "/")
	return cmp.Or(engine, protocol.DBMariaDB)
}

func runningServers(g databases.Group) []Node {
	var out []Node
	for _, sv := range g.Servers {
		if sv.Running {
			out = append(out, Node{NodeID: sv.NodeID, Hostname: sv.Hostname})
		}
	}
	return out
}

// SwitchMove zegt dat de app op de nieuwe database draait. Zonder force
// mag niemand meer met de oude database verbonden zijn en moet de app met
// de nieuwe verbonden zijn. Daarna mogen de gebruikers van de app de oude
// database alleen nog lezen.
func (s *Service) SwitchMove(ctx context.Context, actor events.Actor, id uuid.UUID, force bool) (store.GetDBMigrationRow, error) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	row, err := s.openMove(ctx, id)
	if err != nil {
		return row, err
	}
	m := row.DbMigration
	engine := engineOfKey(m.SourceKey)
	g, src, err := s.moveSource(ctx, m.SourceKey)
	if err != nil {
		return row, err
	}
	t, err := s.migrateTarget(ctx, m.ClusterID, m.ComponentID, engine)
	if err != nil {
		return row, err
	}
	oldConns, oldUnread, err := s.liveConnections(ctx, runningServers(g), engine, m.Database)
	if err != nil {
		return row, err
	}
	newConns, _, err := s.liveConnections(ctx, dataNodes(t.spec, nil), engine, m.Database)
	if err != nil {
		return row, err
	}
	var why []string
	if len(oldConns) > 0 {
		why = append(why, "nog open met "+m.Database+" op "+m.SourceName+": "+describeConnections(oldConns))
	}
	if len(oldUnread) > 0 {
		why = append(why, "wie er met de oude database verbonden is, lukte niet op "+strings.Join(oldUnread, ", "))
	}
	if len(newConns) == 0 {
		why = append(why, "op "+row.ClusterName+" is nog niemand met "+m.Database+" verbonden")
	}
	if len(why) > 0 && !force {
		return row, &ConflictError{upperFirst(strings.Join(why, "; ")) + ". Wijs de app naar " + cmp.Or(m.TargetAddress, row.ClusterName) +
			" en zet het onderhoud uit, of schakel toch over"}
	}
	var accounts []MoveAccount
	_ = json.Unmarshal(m.Accounts, &accounts)
	old := oldAccounts(accounts)
	lines := []string{m.Message, fmt.Sprintf("Overgeschakeld: de app gebruikt %s op %s.", m.Database, cmp.Or(m.TargetAddress, row.ClusterName))}
	if len(why) > 0 {
		lines = append(lines, "Toch overgeschakeld: "+strings.Join(why, "; ")+".")
	}
	locked := false
	switch {
	case engine == protocol.DBPostgreSQL:
		if _, err := s.migrateReq(ctx, *src, engine, protocol.DBMigrateRequest{Action: protocol.DBMigrateLock, Database: m.Database}); err != nil {
			return row, fmt.Errorf("de oude database op slot zetten: %w", err)
		}
		locked = true
		lines = append(lines, fmt.Sprintf("Op slot: wie nu met %s op %s verbindt, kan alleen nog lezen.", m.Database, m.SourceName))
	case len(old) > 0:
		if _, err := s.migrateReq(ctx, *src, engine, protocol.DBMigrateRequest{Action: protocol.DBMigrateLock, Database: m.Database, Accounts: old}); err != nil {
			return row, fmt.Errorf("de oude database op slot zetten: %w", err)
		}
		locked = true
		users := accountUsers(accounts)
		lines = append(lines, fmt.Sprintf("Op slot: %s %s %s op %s alleen nog lezen.", joinHosts(users), mayVerb(len(users)), m.Database, m.SourceName))
	default:
		lines = append(lines, "Er was geen gebruiker om op slot te zetten.")
	}
	now := s.Now()
	upd, err := s.q.SwitchDBMigration(ctx, store.SwitchDBMigrationParams{ID: m.ID, SwitchedAt: &now, Locked: locked, Message: strings.Join(lines, "\n")})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, &ConflictError{"deze verhuizing wacht niet meer op overschakelen"}
	}
	if err != nil {
		return row, err
	}
	row.DbMigration = upd
	s.moveEvent(ctx, actor, "cluster.database_moved", upd, map[string]any{"locked": locked, "forced": len(why) > 0})
	return row, nil
}

// mayVerb is mag of mogen.
func mayVerb(n int) string {
	if n == 1 {
		return "mag"
	}
	return "mogen"
}

func accountUsers(accounts []MoveAccount) []string {
	var out []string
	for _, a := range accounts {
		out = append(out, a.User)
	}
	return out
}

// CancelMove haalt de kopie van een verhuizing die op overschakelen
// wacht weer weg, met de gebruikers die ClusterForge maakte. Confirm is
// de naam van de database. Gebruikt iemand de nieuwe database al, dan kan
// het niet: dan is overschakelen de weg.
func (s *Service) CancelMove(ctx context.Context, actor events.Actor, id uuid.UUID, confirm string) (store.GetDBMigrationRow, error) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	row, err := s.openMove(ctx, id)
	if err != nil {
		return row, err
	}
	m := row.DbMigration
	if confirm != m.Database {
		return row, &InvalidError{"confirm", "tik ter bevestiging de naam van de database in: " + m.Database}
	}
	engine := engineOfKey(m.SourceKey)
	t, err := s.migrateTarget(ctx, m.ClusterID, m.ComponentID, engine)
	if err != nil {
		return row, err
	}
	conns, unread, err := s.liveConnections(ctx, dataNodes(t.spec, nil), engine, m.Database)
	if err != nil {
		return row, err
	}
	if len(conns) > 0 {
		return row, &ConflictError{fmt.Sprintf("de app gebruikt %s op %s al: %s. Schakel over in plaats van te annuleren", m.Database,
			row.ClusterName, describeConnections(conns))}
	}
	if len(unread) > 0 {
		return row, &ConflictError{"wie er met de nieuwe database verbonden is, lukte niet op " + strings.Join(unread, ", ") + "; probeer het straks opnieuw"}
	}
	var accounts []MoveAccount
	_ = json.Unmarshal(m.Accounts, &accounts)
	if err := s.dropMoved(ctx, t.node, engine, m.Database, true, accounts); err != nil {
		return row, err
	}
	msg := m.Message + fmt.Sprintf("\nGeannuleerd: %s en de nieuwe gebruikers zijn weg van %s; de app gebruikt de oude database.", m.Database, row.ClusterName)
	now := s.Now()
	upd, err := s.q.CancelDBMigration(ctx, store.CancelDBMigrationParams{ID: m.ID, FinishedAt: &now, Message: msg})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, &ConflictError{"deze verhuizing wacht niet meer op overschakelen"}
	}
	if err != nil {
		return row, err
	}
	row.DbMigration = upd
	s.moveEvent(ctx, actor, "cluster.database_move_cancelled", upd, nil)
	return row, nil
}

// LockMove zet de oude database van een verhuisde app op slot (alleen
// lezen) of geeft de gebruikers hun rechten terug.
func (s *Service) LockMove(ctx context.Context, actor events.Actor, id uuid.UUID, lock bool) (store.GetDBMigrationRow, error) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	row, err := s.GetMigration(ctx, id)
	if err != nil {
		return row, err
	}
	m := row.DbMigration
	if m.Kind != store.DbMigrationKindMigrate || m.Stage != StageDone {
		return row, &ConflictError{"alleen de oude database van een verhuizing die klaar is, kan op slot"}
	}
	if m.Locked == lock {
		return row, nil
	}
	var accounts []MoveAccount
	_ = json.Unmarshal(m.Accounts, &accounts)
	old := oldAccounts(accounts)
	engine := engineOfKey(m.SourceKey)
	if engine == protocol.DBPostgreSQL {
		// Het slot staat op de database zelf.
		old = nil
	} else if len(old) == 0 {
		return row, &ConflictError{"deze verhuizing nam geen gebruikers mee, dus er is niets op slot te zetten"}
	}
	_, src, err := s.moveSource(ctx, m.SourceKey)
	if err != nil {
		return row, err
	}
	action := protocol.DBMigrateUnlock
	if lock {
		action = protocol.DBMigrateLock
	}
	if _, err := s.migrateReq(ctx, *src, engine, protocol.DBMigrateRequest{Action: action, Database: m.Database, Accounts: old}); err != nil {
		return row, err
	}
	upd, err := s.q.SetDBMigrationLocked(ctx, store.SetDBMigrationLockedParams{ID: m.ID, Locked: lock})
	if err != nil {
		return row, err
	}
	row.DbMigration = upd
	ev := "cluster.database_unlocked"
	if lock {
		ev = "cluster.database_locked"
	}
	s.moveEvent(ctx, actor, ev, upd, map[string]any{"server": src.Hostname})
	return row, nil
}
