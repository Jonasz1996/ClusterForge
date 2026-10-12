package agent

// Een database van een andere PostgreSQL verhuizen naar de primary op deze
// node: DBMigrate met Engine postgresql.
//
// Op de oude server maakt de agent een tijdelijke gebruiker die lid is van
// de eigenaar van de database. Zo leest pg_dump alles wat de eigenaar kan,
// zonder dat er een recht in de database verandert; elke transactie van
// die gebruiker is standaard alleen lezen, en hij verloopt na 12 uur. De
// nieuwe node haalt de dump met pg_dump over het netwerk op en stuurt hem
// meteen naar psql als postgres, zoals bij MariaDB zonder bestand. Het
// wachtwoord staat alleen in een passfile in een eigen map.
//
// De gebruikers van de app gaan mee met de SCRAM-verifier die PostgreSQL
// van hun wachtwoord bewaart; die werkt op elke PostgreSQL, zodat de app
// met hetzelfde wachtwoord inlogt. Het slot op de oude database zet
// default_transaction_read_only op de database zelf, voor iedereen.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// pgProbeUser is de naam waarmee de agent vraagt of pg_hba.conf de
	// tijdelijke gebruiker zou binnenlaten. Hij bestaat niet en geeft geen
	// wachtwoord, dus binnen komt hij nooit.
	pgProbeUser = protocol.DBMigrateUserPrefix + "0000000000000000"
	// pgNoPassfile bestaat niet, zodat psql geen .pgpass van root leest.
	pgNoPassfile = "/nonexistent/clusterforge-pgpass"
)

// sslRequest is het eerste pakket van een client die TLS wil: lengte 8 en
// de code 80877103. PostgreSQL antwoordt met één byte, S of N.
var sslRequest = []byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}

// pgHBARe is de fout als pg_hba.conf een adres niet binnenlaat, met het
// adres waarvan de server de verbinding ziet.
var pgHBARe = regexp.MustCompile(`(?:no pg_hba\.conf entry|pg_hba\.conf rejects connection) for host "([^"]+)"`)

// pgMigrate is DBMigrate voor PostgreSQL.
func (a *Agent) pgMigrate(ctx context.Context, req *protocol.DBMigrateRequest) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	if req == nil {
		res.Error = "een verhuisverzoek zonder inhoud"
		return res
	}
	out := &protocol.DBMigrateResult{}
	err := func() error {
		if req.Action == protocol.DBMigrateStatus {
			out.Run = a.migrateRun(req.Target)
			return nil
		}
		if err := a.needPG(ctx); err != nil {
			return err
		}
		switch req.Action {
		case protocol.DBMigrateInspect:
			info, err := a.pgMigrateInspect(ctx, req.Database)
			out.Info = info
			return err
		case protocol.DBMigrateGrant:
			return a.pgMigrateGrant(ctx, req)
		case protocol.DBMigrateRevoke:
			sql, err := dbsql.PGMigrateRevoke(req.User)
			if err != nil {
				return err
			}
			return a.pgWriteErr(ctx, sql, "")
		case protocol.DBMigrateProbe:
			if err := checkAddress(req.Host, req.Port); err != nil {
				return err
			}
			if err := dbsql.CheckPGDatabase(req.Database); err != nil {
				return err
			}
			p := a.pgProbe(ctx, req.Host, req.Port, req.Database)
			out.Probe = &p
			return nil
		case protocol.DBMigratePull:
			if err := a.pgMigratePull(ctx, req); err != nil {
				return err
			}
			out.Run = a.migrateRun(req.Target)
			return nil
		case protocol.DBMigrateAccounts:
			accounts, err := a.pgMigrateAccounts(ctx, req.Database, req.Secret)
			out.Accounts = accounts
			return err
		case protocol.DBMigrateAdopt:
			created, err := a.pgMigrateAdopt(ctx, req.Accounts)
			out.Accounts = created
			return err
		case protocol.DBMigrateLock, protocol.DBMigrateUnlock:
			sql, err := dbsql.PGMigrateLock(req.Database, req.Action == protocol.DBMigrateLock)
			if err != nil {
				return err
			}
			return a.pgWriteErr(ctx, sql, "")
		}
		return fmt.Errorf("onbekende verhuisactie %q", req.Action)
	}()
	if err != nil {
		res.Error = err.Error()
		if req.Action == protocol.DBMigrateAdopt && len(out.Accounts) > 0 {
			// Welke rollen er al nieuw zijn, ook als een volgende mislukte.
			res.Migrate = out
		}
		return res
	}
	res.Migrate = out
	return res
}

// pgWriteErr is pgWrite met een fout.
func (a *Agent) pgWriteErr(ctx context.Context, sql, secret string) error {
	if res := a.pgWrite(ctx, sql, secret); res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}

// pgMigrateInspect leest wat de oude PostgreSQL over db weet: de versie,
// encoding en eigenaar, de tabellen, de andere objecten en extensies, en
// wat de eigenaar niet mag lezen.
func (a *Agent) pgMigrateInspect(ctx context.Context, db string) (*protocol.DBMigrateInfo, error) {
	q, err := dbsql.PGMigrateInfo(db)
	if err != nil {
		return nil, err
	}
	rows, err := a.psql(ctx, "", q)
	if err != nil {
		return nil, errors.New("database lezen: " + dbError(err))
	}
	if len(rows) != 1 || len(rows[0]) < 7 {
		return nil, errors.New("onverwacht antwoord van PostgreSQL over de database")
	}
	r := rows[0]
	info := &protocol.DBMigrateInfo{
		Version: r[0], Exists: r[1] != "0", Charset: r[2], Owner: r[3], OwnerSuper: r[4] == "t", SizeBytes: atoi64(r[5]), Settings: atoi(r[6]),
		Tables: []protocol.DBMigrateTable{},
	}
	if !info.Exists {
		return info, nil
	}
	if rows, err = a.psql(ctx, db, dbsql.PGMigrateTables); err != nil {
		return nil, errors.New("tabellen lezen: " + dbError(err))
	}
	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		info.Tables = append(info.Tables, protocol.DBMigrateTable{
			Name: dbsql.PGTable{Schema: r[0], Name: r[1]}.String(), Rows: atoi64(r[2]), SizeBytes: atoi64(r[3]), PrimaryKey: r[4] == "t",
		})
	}
	if rows, err = a.psql(ctx, db, dbsql.PGMigrateObjects); err != nil {
		return nil, errors.New("objecten lezen: " + dbError(err))
	}
	if len(rows) != 1 || len(rows[0]) < 4 {
		return nil, errors.New("objecten lezen: onverwacht antwoord")
	}
	info.Views, info.Routines, info.Triggers = atoi(rows[0][0]), atoi(rows[0][1]), atoi(rows[0][2])
	if rows[0][3] != "" {
		info.Extensions = strings.Split(rows[0][3], ",")
	}
	if rows, err = a.psql(ctx, db, dbsql.PGMigrateUnreadable); err != nil {
		return nil, errors.New("rechten van de eigenaar lezen: " + dbError(err))
	}
	for _, r := range rows {
		info.Unreadable = append(info.Unreadable, r[0])
	}
	return info, nil
}

// pgMigrateGrant maakt de tijdelijke gebruiker, als pg_dump daarmee alles
// van de database kan lezen.
func (a *Agent) pgMigrateGrant(ctx context.Context, req *protocol.DBMigrateRequest) error {
	if err := dbsql.CheckMigrateUser(req.User); err != nil {
		return err
	}
	if !migratePasswordRe.MatchString(req.Password) {
		return errors.New("een wachtwoord is 16 tot 128 letters en cijfers")
	}
	info, err := a.pgMigrateInspect(ctx, req.Database)
	switch {
	case err != nil:
		return err
	case !info.Exists:
		return fmt.Errorf("de database %s bestaat niet", req.Database)
	case info.OwnerSuper:
		return fmt.Errorf("de eigenaar van %s, %s, is superuser; ClusterForge maakt geen tijdelijke gebruiker met die rechten", req.Database, info.Owner)
	case len(info.Unreadable) > 0:
		return fmt.Errorf("%s, de eigenaar van %s, mag niet alles lezen: %s", info.Owner, req.Database, strings.Join(info.Unreadable, ", "))
	}
	verifier, err := scramVerifier(req.Password)
	if err != nil {
		return err
	}
	sql, err := dbsql.PGMigrateGrant(req.Database, req.User, verifier)
	if err != nil {
		return err
	}
	return a.pgWriteErr(ctx, sql, verifier)
}

// pgReach stuurt een SSLRequest naar host:port: een PostgreSQL antwoordt
// met S (TLS kan) of N, zonder dat er iemand inlogt.
func (a *Agent) pgReach(ctx context.Context, host string, port int) protocol.DBMigrateReach {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn, err := a.dial(ctx, addr)
	if err != nil {
		return protocol.DBMigrateReach{Error: err.Error()}
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(probeTimeout))
	if _, err := conn.Write(sslRequest); err != nil {
		return protocol.DBMigrateReach{Error: fmt.Sprintf("%s: %v", addr, err)}
	}
	var b [1]byte
	if _, err := io.ReadFull(conn, b[:]); err != nil {
		return protocol.DBMigrateReach{Error: fmt.Sprintf("%s antwoordde niet zoals PostgreSQL: %v", addr, err)}
	}
	switch b[0] {
	case 'S':
		return protocol.DBMigrateReach{Reached: true, TLS: true}
	case 'N':
		return protocol.DBMigrateReach{Reached: true}
	}
	return protocol.DBMigrateReach{Error: addr + " is geen PostgreSQL"}
}

// pgProbe kijkt of de PostgreSQL op host:port antwoordt, en of pg_hba.conf
// daar de tijdelijke gebruiker van dit adres in db zou laten inloggen: met
// een naam die niet bestaat en zonder wachtwoord, zodat er nooit iemand
// binnenkomt. Vraagt de server een wachtwoord, dan liet pg_hba.conf de
// verbinding toe. Daarbij de extensies die op deze node te installeren
// zijn.
func (a *Agent) pgProbe(ctx context.Context, host string, port int, db string) protocol.DBMigrateReach {
	r := a.pgReach(ctx, host, port)
	if !r.Reached {
		return r
	}
	conninfo := fmt.Sprintf("host=%s port=%d dbname=%s user=%s passfile=%s connect_timeout=5 application_name=clusterforge",
		host, port, db, pgProbeUser, conninfoQuote(pgNoPassfile))
	_, err := a.dbRun(ctx, "psql", "-w", "-XAtq", "-d", conninfo, "-c", "SELECT 1")
	if err != nil {
		msg := err.Error()
		switch m := pgHBARe.FindStringSubmatch(msg); {
		case strings.Contains(msg, "no password supplied"), strings.Contains(msg, "password authentication failed"),
			strings.Contains(msg, "does not exist"):
		case m != nil:
			r.Code, r.Seen, r.Error = protocol.DBMigrateNoEntry, m[1], pgFatal(msg)
		default:
			r.Code, r.Error = 1, pgFatal(msg)
		}
	}
	rows, err := a.psql(ctx, "", dbsql.PGMigrateAvailable)
	if err != nil {
		r.Code, r.Error = 1, "extensies op deze node lezen: "+dbError(err)
		return r
	}
	for _, row := range rows {
		r.Extensions = append(r.Extensions, row[0])
	}
	return r
}

// pgFatal is de fout van PostgreSQL uit een fout van psql: vanaf FATAL, of
// het hele bericht.
func pgFatal(msg string) string {
	if i := strings.Index(msg, "FATAL:"); i >= 0 {
		msg = msg[i:]
	}
	return clip(strings.Join(strings.Fields(msg), " "), 300)
}

// pgMigratePull begint de import van req.Database van de oude PostgreSQL
// in de nieuwe database req.Target op deze node, die de primary moet zijn.
func (a *Agent) pgMigratePull(ctx context.Context, req *protocol.DBMigrateRequest) error {
	if err := dbsql.CheckPGDatabase(req.Database); err != nil {
		return err
	}
	if err := dbsql.CheckPGDatabase(req.Target); err != nil {
		return err
	}
	if err := dbsql.CheckMigrateUser(req.User); err != nil {
		return err
	}
	if !migratePasswordRe.MatchString(req.Password) {
		return errors.New("een wachtwoord is 16 tot 128 letters en cijfers")
	}
	if err := checkAddress(req.Host, req.Port); err != nil {
		return err
	}
	if req.Trial == (req.Owner != "") {
		return errors.New("een proef heeft geen eigenaar, een verhuizing wel")
	}
	create, err := dbsql.PGMigrateCreate(req.Target, req.Owner, req.Charset)
	if err != nil {
		return err
	}
	if _, err := dbsql.PGMigrateDatabaseGrants(req.Target, req.Accounts); err != nil {
		return err
	}
	exists, _, _, err := a.pgDumpInfo(ctx, req.Target)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("de database %s bestaat al op deze node", req.Target)
	}
	p := a.pgReach(ctx, req.Host, req.Port)
	if !p.Reached {
		return errors.New(p.Error)
	}
	run := &protocol.DBMigrateRun{
		Database: req.Database, Target: req.Target, Host: req.Host, Port: req.Port, TLS: p.TLS, Started: a.now().UTC(), Total: req.Total,
	}
	st := a.migrations()
	st.mu.Lock()
	if st.running != nil {
		busy := st.running.Database
		st.mu.Unlock()
		return fmt.Errorf("de agent importeert al %s; wacht tot dat klaar is", busy)
	}
	st.running = run
	st.mu.Unlock()
	if err := a.pgWriteErr(ctx, create, ""); err != nil {
		st.mu.Lock()
		st.running = nil
		st.mu.Unlock()
		return fmt.Errorf("database %s maken: %w", req.Target, err)
	}
	go a.runPGPull(*req, *run)
	return nil
}

// runPGPull haalt de dump bij de oude PostgreSQL en stuurt hem meteen naar
// de nieuwe database, telt daarna de rijen aan beide kanten, en haalt de
// nieuwe database weg als de import mislukt.
func (a *Agent) runPGPull(req protocol.DBMigrateRequest, run protocol.DBMigrateRun) {
	ctx, cancel := context.WithTimeout(context.Background(), protocol.DBDumpTimeout)
	defer cancel()
	st := a.migrations()
	progress := func(n int64) {
		st.mu.Lock()
		if st.running != nil {
			st.running.Bytes += n
		}
		st.mu.Unlock()
	}
	imported := false
	err := func() error {
		login, err := newPGLogin(req.Host, req.Port, req.User, req.Password)
		if err != nil {
			return err
		}
		defer login.close()
		// Een proef heeft de gebruikers van de app niet op deze node, dus
		// zonder eigenaars en rechten; alles is dan van postgres.
		dumpArgs := []string{"-d", login.conninfo(req.Database), "--no-password", "--lock-wait-timeout=5min"}
		if req.Trial {
			dumpArgs = append(dumpArgs, "--no-owner", "--no-privileges")
		}
		dump := func(ctx context.Context, w io.Writer) error { return a.stream(ctx, nil, w, "pg_dump", dumpArgs...) }
		imp := func(ctx context.Context, r io.Reader) error {
			return a.stream(ctx, r, io.Discard, "runuser", "-u", "postgres", "--", "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", req.Target)
		}
		if err := pipe(ctx, protocol.DBPostgreSQL, dump, imp, progress); err != nil {
			return err
		}
		imported = true
		// De rechten op de database zelf staan niet in de dump.
		if grants, _ := dbsql.PGMigrateDatabaseGrants(req.Target, req.Accounts); grants != "" && !req.Trial {
			if err := a.pgWriteErr(ctx, grants, ""); err != nil {
				return fmt.Errorf("rechten op %s geven: %w", req.Target, err)
			}
		}
		now := a.now().UTC()
		st.mu.Lock()
		st.running.Imported = now
		st.mu.Unlock()
		run.Imported = now
		return a.pgCountBoth(ctx, login, req.Database, req.Target, &run)
	}()
	if err != nil {
		run.Error = clip(strings.ReplaceAll(err.Error(), req.Password, "********"), 500)
		a.Log.Warn("database importeren mislukt", "database", req.Database, "target", req.Target, "err", run.Error)
		if !imported {
			dctx, dcancel := context.WithTimeout(context.Background(), protocol.DBTimeout)
			sql, _ := dbsql.PGDrop(protocol.DBDropRequest{Database: req.Target, Confirm: req.Target})
			if derr := a.pgWriteErr(dctx, sql, ""); derr != nil {
				run.Error += "; " + req.Target + " weghalen mislukte ook: " + derr.Error()
			}
			dcancel()
		}
	}
	run.Finished = a.now().UTC()
	st.mu.Lock()
	if st.running != nil {
		run.Bytes = st.running.Bytes
	}
	st.running = nil
	st.runs = append(st.runs, run)
	if len(st.runs) > migrateRuns {
		st.runs = st.runs[len(st.runs)-migrateRuns:]
	}
	st.mu.Unlock()
}

// pgCountBoth telt de rijen van elke tabel in de nieuwe database en
// dezelfde tabellen op de oude server, en leest de views, routines en
// triggers in de nieuwe database.
func (a *Agent) pgCountBoth(ctx context.Context, login *pgLogin, db, target string, run *protocol.DBMigrateRun) error {
	rows, err := a.psql(ctx, target, dbsql.PGMigrateTables)
	if err != nil {
		return fmt.Errorf("tabellen lezen: %s", dbError(err))
	}
	tables := make([]dbsql.PGTable, 0, len(rows))
	for _, r := range rows {
		if len(r) >= 2 {
			tables = append(tables, dbsql.PGTable{Schema: r[0], Name: r[1]})
		}
	}
	newCounts, err := a.pgCountRows(ctx, nil, target, tables)
	if err != nil {
		return fmt.Errorf("tellen in %s: %w", target, err)
	}
	oldCounts, err := a.pgCountRows(ctx, login, db, tables)
	if err != nil {
		return fmt.Errorf("tellen op de oude server: %w", err)
	}
	run.Tables = make([]protocol.DBMigrateCount, len(tables))
	for i, t := range tables {
		run.Tables[i] = protocol.DBMigrateCount{Name: t.String(), Source: oldCounts[i], Target: newCounts[i]}
	}
	if rows, err = a.psql(ctx, target, dbsql.PGMigrateObjects); err != nil {
		return fmt.Errorf("objecten lezen in %s: %s", target, dbError(err))
	}
	if len(rows) != 1 || len(rows[0]) < 3 {
		return fmt.Errorf("objecten lezen in %s: onverwacht antwoord", target)
	}
	run.Views, run.Routines, run.Triggers = atoi(rows[0][0]), atoi(rows[0][1]), atoi(rows[0][2])
	return nil
}

// pgCountRows telt de rijen van tables in db: op deze node als login nil
// is, anders op de oude server.
func (a *Agent) pgCountRows(ctx context.Context, login *pgLogin, db string, tables []dbsql.PGTable) ([]int64, error) {
	queries, err := dbsql.PGCountRows(tables)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(tables))
	for _, q := range queries {
		var rows [][]string
		if login == nil {
			rows, err = a.psql(ctx, db, q)
		} else {
			var raw []byte
			raw, err = a.dbRun(ctx, "psql", "-XAtq", "-F", dbsql.PGFieldSep, "-R", dbsql.PGRecordSep, "-d", login.conninfo(db), "-c", q)
			rows = psqlRows(raw)
		}
		if err != nil {
			return nil, errors.New(dbError(err))
		}
		if len(rows) != 1 {
			return nil, errors.New("onverwacht antwoord bij het tellen")
		}
		for _, f := range rows[0] {
			n, err := strconv.ParseInt(f, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("onverwacht aantal %q", f)
			}
			out = append(out, n)
		}
	}
	if len(out) != len(tables) {
		return nil, fmt.Errorf("%d aantallen voor %d tabellen", len(out), len(tables))
	}
	return out, nil
}

// pgMigrateAccounts leest de rollen met iets in db. Zonder secret is de
// verifier leeg.
func (a *Agent) pgMigrateAccounts(ctx context.Context, db string, secret bool) ([]protocol.DBMigrateAccount, error) {
	if err := dbsql.CheckPGDatabase(db); err != nil {
		return nil, err
	}
	rows, err := a.psql(ctx, db, dbsql.PGMigrateAccounts(secret))
	if err != nil {
		return nil, errors.New("gebruikers lezen: " + dbError(err))
	}
	out := []protocol.DBMigrateAccount{}
	for _, r := range rows {
		if len(r) < 9 {
			continue
		}
		acc := protocol.DBMigrateAccount{User: r[0], Plugin: r[1], Super: r[3] == "t", Global: r[3] == "t" || r[8] == "t", Privileges: []string{}}
		if secret {
			acc.Hash = r[2]
		}
		if r[4] == "t" {
			acc.Privileges = append(acc.Privileges, dbsql.PGOwner)
		}
		if r[5] != "" {
			acc.Privileges = append(acc.Privileges, strings.Split(r[5], ",")...)
		}
		if r[6] == "t" && r[4] != "t" {
			acc.Privileges = append(acc.Privileges, dbsql.PGObjects)
		}
		if r[7] != "" {
			acc.MemberOf = strings.Split(r[7], ",")
		}
		out = append(out, acc)
	}
	return out, nil
}

// pgMigrateAdopt maakt de rollen met hun verifier, elk in een eigen
// opdracht zodat een fout zijn verifier niet kan tonen, en maakt ze daarna
// lid van elkaar zoals op de oude server. Het antwoord zijn de rollen die
// er nog niet waren, zonder verifier: annuleren haalt alleen die weg.
func (a *Agent) pgMigrateAdopt(ctx context.Context, accounts []protocol.DBMigrateAccount) ([]protocol.DBMigrateAccount, error) {
	if len(accounts) == 0 {
		return nil, errors.New("geen gebruikers om te maken")
	}
	rows, err := a.psql(ctx, "", dbsql.PGRoleNames)
	if err != nil {
		return nil, errors.New("rollen lezen: " + dbError(err))
	}
	existing := map[string]bool{}
	for _, r := range rows {
		existing[r[0]] = true
	}
	created := []protocol.DBMigrateAccount{}
	for _, acc := range accounts {
		sql, err := dbsql.PGMigrateAdopt(acc)
		if err != nil {
			return created, err
		}
		if err := a.pgWriteErr(ctx, sql, acc.Hash); err != nil {
			return created, fmt.Errorf("%s: %w", acc.User, err)
		}
		if !existing[acc.User] {
			existing[acc.User] = true
			created = append(created, protocol.DBMigrateAccount{User: acc.User, Plugin: acc.Plugin, Privileges: []string{}})
		}
	}
	if sql := dbsql.PGMigrateMembers(accounts); sql != "" {
		return created, a.pgWriteErr(ctx, sql, "")
	}
	return created, nil
}
