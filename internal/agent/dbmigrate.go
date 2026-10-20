package agent

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// migrateRuns is hoeveel imports de agent onthoudt.
	migrateRuns = 10
	// probeTimeout is hoe lang de agent wacht op de begroeting van de oude
	// MariaDB.
	probeTimeout = 5 * time.Second
)

var (
	// Een host is een IP-adres of een DNS-naam.
	migrateHostRe     = regexp.MustCompile(`^[A-Za-z0-9.:\-]{1,253}$`)
	migratePasswordRe = regexp.MustCompile(`^[A-Za-z0-9]{16,128}$`)
	versionRe         = regexp.MustCompile(`^(\d+)\.(\d+)`)
)

// migrateState is de import die op de achtergrond loopt, en de laatste die
// klaar zijn.
type migrateState struct {
	mu      sync.Mutex
	running *protocol.DBMigrateRun
	runs    []protocol.DBMigrateRun
}

func (a *Agent) migrations() *migrateState {
	a.initDB()
	return &a.db.migrate
}

func (a *Agent) dbMigrate(ctx context.Context, req *protocol.DBMigrateRequest) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	if req == nil {
		res.Error = "een verhuisverzoek zonder inhoud"
		return res
	}
	out := &protocol.DBMigrateResult{}
	var err error
	switch req.Action {
	case protocol.DBMigrateInspect:
		out.Info, err = a.migrateInspect(ctx, req.Database)
	case protocol.DBMigrateGrant:
		err = a.migrateGrant(ctx, req)
	case protocol.DBMigrateRevoke:
		var sql string
		if sql, err = dbsql.MigrateRevoke(req.User, req.Hosts); err == nil {
			err = a.writeErr(ctx, sql, "")
		}
	case protocol.DBMigrateProbe:
		if err = checkAddress(req.Host, req.Port); err == nil {
			p := a.probe(ctx, req.Host, req.Port)
			out.Probe = &p
		}
	case protocol.DBMigratePull:
		err = a.migratePull(ctx, req)
		if err == nil {
			out.Run = a.migrateRun(req.Target)
		}
	case protocol.DBMigrateStatus:
		out.Run = a.migrateRun(req.Target)
	case protocol.DBMigrateAccounts:
		out.Accounts, err = a.migrateAccounts(ctx, req.Database, req.Secret)
	case protocol.DBMigrateAdopt:
		err = a.migrateAdopt(ctx, req.Database, req.Accounts)
	case protocol.DBMigrateLock, protocol.DBMigrateUnlock:
		var sql string
		if sql, err = dbsql.MigrateLock(req.Database, req.Accounts, req.Action == protocol.DBMigrateLock); err == nil {
			err = a.writeErr(ctx, sql, "")
		}
	default:
		err = fmt.Errorf("onbekende verhuisactie %q", req.Action)
	}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Migrate = out
	return res
}

// writeErr is dbWrite met een fout.
func (a *Agent) writeErr(ctx context.Context, sql, secret string) error {
	if res := a.dbWrite(ctx, sql, secret); res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}

// migrateInspect leest wat de oude MariaDB over db weet: tekenset,
// tabellen met engine en primaire sleutel, en de andere objecten.
func (a *Agent) migrateInspect(ctx context.Context, db string) (*protocol.DBMigrateInfo, error) {
	q, err := dbsql.MigrateInfo(db)
	if err != nil {
		return nil, err
	}
	if err := a.needMaria(ctx); err != nil {
		return nil, err
	}
	rows, err := a.maria(ctx, false, q)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || len(rows[0]) < 8 {
		return nil, errors.New("onverwacht antwoord van MariaDB over de database")
	}
	r := rows[0]
	info := &protocol.DBMigrateInfo{
		Version: r[0], Exists: r[1] == "1", Charset: r[2], Collation: r[3],
		Views: atoi(r[4]), Routines: atoi(r[5]), Triggers: atoi(r[6]), Events: atoi(r[7]),
		Tables: []protocol.DBMigrateTable{},
	}
	if !info.Exists {
		return info, nil
	}
	q, _ = dbsql.MigrateTables(db)
	rows, err = a.maria(ctx, false, q)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		t := protocol.DBMigrateTable{
			Name: r[0], Engine: r[1], Rows: atoi64(r[2]), SizeBytes: atoi64(r[3]), PrimaryKey: r[4] == "1",
		}
		info.SizeBytes += t.SizeBytes
		info.Tables = append(info.Tables, t)
	}
	return info, nil
}

// migrateAccounts leest de gebruikers met rechten op db. Zonder secret is
// de hash leeg.
func (a *Agent) migrateAccounts(ctx context.Context, db string, secret bool) ([]protocol.DBMigrateAccount, error) {
	q, err := dbsql.MigrateAccounts(db, secret)
	if err != nil {
		return nil, err
	}
	if err := a.needMaria(ctx); err != nil {
		return nil, err
	}
	rows, err := a.maria(ctx, false, q)
	if err != nil {
		return nil, err
	}
	out := []protocol.DBMigrateAccount{}
	for _, r := range rows {
		if len(r) < 7 {
			continue
		}
		acc := protocol.DBMigrateAccount{
			User: r[0], Host: r[1], Plugin: r[2], Schema: r[4], Privileges: strings.Split(r[5], ","), Global: atoi(r[6]) > 0,
		}
		// MariaDB vóór 10.4 en MySQL 5 zetten een leeg plugin voor een
		// gewoon wachtwoord.
		if acc.Plugin == "" {
			acc.Plugin = protocol.DBMigratePlugin
		}
		if secret {
			acc.Hash = r[3]
		}
		out = append(out, acc)
	}
	return out, nil
}

// migrateAdopt maakt de gebruikers met hun hash, elk in een eigen opdracht
// zodat een fout zijn hash niet kan tonen.
func (a *Agent) migrateAdopt(ctx context.Context, db string, accounts []protocol.DBMigrateAccount) error {
	if len(accounts) == 0 {
		return errors.New("geen gebruikers om te maken")
	}
	for _, acc := range accounts {
		sql, err := dbsql.MigrateAdopt(db, acc)
		if err != nil {
			return err
		}
		if err := a.writeErr(ctx, sql, strings.ToUpper(acc.Hash)); err != nil {
			return fmt.Errorf("%s@%s: %w", acc.User, acc.Host, err)
		}
	}
	return nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// migrateGrant maakt de tijdelijke gebruiker met wat mariadb-dump nodig
// heeft: SELECT en SHOW VIEW, en alleen als de database ze heeft, de
// rechten voor triggers, events en routines.
func (a *Agent) migrateGrant(ctx context.Context, req *protocol.DBMigrateRequest) error {
	info, err := a.migrateInspect(ctx, req.Database)
	if err != nil {
		return err
	}
	if !info.Exists {
		return fmt.Errorf("de database %s bestaat niet", req.Database)
	}
	r := dbsql.MigrateRights{Triggers: info.Triggers > 0, Events: info.Events > 0}
	if info.Routines > 0 {
		r.Routines = "proc"
		if mariaAtLeast(info.Version, 11, 3) {
			r.Routines = "db"
		}
	}
	sql, err := dbsql.MigrateGrant(req.Database, req.User, req.Hosts, req.Password, r)
	if err != nil {
		return err
	}
	return a.writeErr(ctx, sql, req.Password)
}

// mariaAtLeast zegt of version een MariaDB van minstens major.minor is.
func mariaAtLeast(version string, major, minor int) bool {
	if !strings.Contains(version, "MariaDB") {
		return false
	}
	m := versionRe.FindStringSubmatch(version)
	if m == nil {
		return false
	}
	maj, _ := strconv.Atoi(m[1])
	mnr, _ := strconv.Atoi(m[2])
	return maj > major || maj == major && mnr >= minor
}

func checkAddress(host string, port int) error {
	if !migrateHostRe.MatchString(host) {
		return fmt.Errorf("%q is geen adres", host)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("%d is geen poort", port)
	}
	return nil
}

func (a *Agent) dial(ctx context.Context, addr string) (net.Conn, error) {
	if a.Dial != nil {
		return a.Dial(ctx, "tcp", addr)
	}
	d := net.Dialer{Timeout: probeTimeout}
	return d.DialContext(ctx, "tcp", addr)
}

// probe leest de begroeting van de MariaDB op host:port, zonder in te
// loggen: de versie, of ze TLS kan, of de fout die ze meteen stuurt.
func (a *Agent) probe(ctx context.Context, host string, port int) protocol.DBMigrateReach {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn, err := a.dial(ctx, addr)
	if err != nil {
		return protocol.DBMigrateReach{Error: err.Error()}
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(probeTimeout))
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return protocol.DBMigrateReach{Error: fmt.Sprintf("%s gaf geen begroeting van MariaDB: %v", addr, err)}
	}
	n := int(head[0]) | int(head[1])<<8 | int(head[2])<<16
	if n < 1 || n > 1<<16 {
		return protocol.DBMigrateReach{Error: addr + " is geen MariaDB of MySQL"}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		return protocol.DBMigrateReach{Error: fmt.Sprintf("%s gaf geen volledige begroeting: %v", addr, err)}
	}
	return parseGreeting(addr, body)
}

// parseGreeting leest het eerste pakket van MariaDB: een handshake met de
// versie en de mogelijkheden, of een fout zoals 1130 (dit adres mag niet
// verbinden).
func parseGreeting(addr string, body []byte) protocol.DBMigrateReach {
	switch {
	case body[0] == 0xff && len(body) >= 3:
		code := int(binary.LittleEndian.Uint16(body[1:3]))
		msg := body[3:]
		if len(msg) >= 6 && msg[0] == '#' {
			msg = msg[6:]
		}
		return protocol.DBMigrateReach{Reached: true, Code: code, Error: string(msg)}
	case body[0] == 10:
		end := slices.Index(body[1:], 0)
		if end < 0 {
			break
		}
		version := strings.TrimPrefix(string(body[1:1+end]), "5.5.5-")
		// Daarna: thread-id (4), scramble (8), opvulling (1) en de onderste
		// twee bytes van de mogelijkheden.
		rest := body[1+end+1:]
		tls := len(rest) >= 15 && binary.LittleEndian.Uint16(rest[13:15])&0x0800 != 0
		return protocol.DBMigrateReach{Reached: true, Version: version, TLS: tls}
	}
	return protocol.DBMigrateReach{Error: addr + " is geen MariaDB of MySQL"}
}

func (a *Agent) migrateRun(target string) *protocol.DBMigrateRun {
	st := a.migrations()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.running != nil && st.running.Target == target {
		r := *st.running
		return &r
	}
	for i := len(st.runs) - 1; i >= 0; i-- {
		if st.runs[i].Target == target {
			r := st.runs[i]
			return &r
		}
	}
	return nil
}

// migrateSpool maakt de spool voor een import van ongeveer total bytes.
// Er loopt maar één import tegelijk, dus wat er nog staat, is van een
// afgebroken import en gaat eerst weg.
func (a *Agent) migrateSpool(total int64, consumed func(int64)) (*spool, error) {
	dir, _ := a.path(defaultSpoolDir)
	dataDir, _ := a.path(mariaDataDir)
	dir = cmp.Or(a.SpoolDir, dir)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return newSpool(dir, spoolLimit(dir, dataDir, total), consumed)
}

// migratePull begint de import van req.Database van de oude MariaDB in de
// nieuwe database req.Target op deze node.
func (a *Agent) migratePull(ctx context.Context, req *protocol.DBMigrateRequest) error {
	if err := dbsql.CheckDatabase(req.Database); err != nil {
		return err
	}
	if err := dbsql.CheckDatabase(req.Target); err != nil {
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
	create, err := dbsql.CreateDatabaseAs(req.Target, req.Charset, req.Collation)
	if err != nil {
		return err
	}
	if err := a.needMaria(ctx); err != nil {
		return err
	}
	exists, _, _, err := a.dumpInfo(ctx, protocol.DBMariaDB, req.Target)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("de database %s bestaat al op deze node", req.Target)
	}
	p := a.probe(ctx, req.Host, req.Port)
	if !p.Reached {
		return errors.New(p.Error)
	}
	run := &protocol.DBMigrateRun{
		Database: req.Database, Target: req.Target, Host: req.Host, Port: req.Port, TLS: p.TLS,
		Started: a.now().UTC(), Total: req.Total,
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
	if err := a.writeErr(ctx, create, ""); err != nil {
		st.mu.Lock()
		st.running = nil
		st.mu.Unlock()
		return fmt.Errorf("database %s maken: %w", req.Target, err)
	}
	go a.runPull(*req, *run)
	return nil
}

// clientArgs zijn de opties om met de oude MariaDB te verbinden: het
// bestand met adres, gebruiker en wachtwoord (moet de eerste optie zijn),
// en TLS als de server het kan. Een MariaDB-client vanaf 11.4 eist anders
// TLS; het certificaat van de oude server is meestal zelfgemaakt, dus de
// agent controleert het niet.
func clientArgs(cnf string, tls bool) []string {
	args := []string{"--defaults-extra-file=" + cnf}
	if tls {
		return append(args, "--ssl", "--skip-ssl-verify-server-cert")
	}
	return append(args, "--skip-ssl")
}

// writeClientConfig schrijft het optiebestand voor de oude MariaDB in een
// eigen map met modus 0700; het wachtwoord staat zo nooit in ps.
func writeClientConfig(req protocol.DBMigrateRequest) (dir, path string, err error) {
	dir, err = os.MkdirTemp("", "cf-verhuizing-")
	if err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, "client.cnf")
	body := fmt.Sprintf("[client]\nhost=%s\nport=%d\nuser=%s\npassword=%s\n", req.Host, req.Port, req.User, req.Password)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}
	return dir, path, nil
}

// runPull haalt de dump bij de oude MariaDB en stuurt hem meteen naar de
// nieuwe database, telt daarna de rijen aan beide kanten, en haalt de
// nieuwe database weg als de import mislukt.
func (a *Agent) runPull(req protocol.DBMigrateRequest, run protocol.DBMigrateRun) {
	ctx, cancel := context.WithTimeout(context.Background(), protocol.DBDumpTimeout)
	defer cancel()
	st := a.migrations()
	progress := func(n int64) {
		st.mu.Lock()
		if st.running != nil {
			st.running.Bytes += n
			st.running.Buffered += n
		}
		st.mu.Unlock()
	}
	imported := false
	err := func() error {
		dir, cnf, err := writeClientConfig(req)
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		client := clientArgs(cnf, run.TLS)
		sp, err := a.migrateSpool(req.Total, func(n int64) {
			st.mu.Lock()
			if st.running != nil {
				st.running.Buffered -= n
			}
			st.mu.Unlock()
		})
		if err != nil {
			return fmt.Errorf("buffer op schijf: %w", err)
		}
		defer sp.remove()
		if err := a.pipeDump(ctx, client, req.Database, req.Target, req.SkipEvents, sp, progress); err != nil {
			return err
		}
		imported = true
		now := a.now().UTC()
		st.mu.Lock()
		st.running.Imported = now
		st.mu.Unlock()
		run.Imported = now
		return a.countBoth(ctx, client, req.Database, req.Target, &run)
	}()
	if err != nil {
		run.Error = clip(strings.ReplaceAll(err.Error(), req.Password, "********"), 500)
		a.Log.Warn("database importeren mislukt", "database", req.Database, "target", req.Target, "err", run.Error)
		if !imported {
			dctx, dcancel := context.WithTimeout(context.Background(), protocol.DBTimeout)
			sql, _ := dbsql.Drop(protocol.DBDropRequest{Database: req.Target, Confirm: req.Target})
			if derr := a.writeErr(dctx, sql, ""); derr != nil {
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

// importArgs zijn de opties van mariadb om een dump in te lezen. Met
// --binary-mode voert de client geen eigen commando's (zoals \! voor een
// shell) uit die in de dump staan: de agent draait als root, en de inhoud
// van een dump komt van een andere server.
var importArgs = []string{"--batch", "--binary-mode", "--skip-print-query-on-error"}

// pipeDump stuurt de uitvoer van mariadb-dump op de oude server via de
// spool naar mariadb op deze node; met skipEvents zonder --events.
func (a *Agent) pipeDump(ctx context.Context, client []string, db, target string, skipEvents bool, sp *spool, progress func(int64)) error {
	dump := func(ctx context.Context, w io.Writer) error {
		args := append(slices.Clone(client), dumpArgs...)
		if skipEvents {
			args = slices.DeleteFunc(args, func(a string) bool { return a == "--events" })
		}
		args = append(args, db)
		err := a.stream(ctx, nil, w, "mariadb-dump", args...)
		if err != nil && notInstalled(err) {
			err = a.stream(ctx, nil, w, "mysqldump", args...)
		}
		return err
	}
	imp := func(ctx context.Context, r io.Reader) error {
		args := append(slices.Clone(importArgs), target)
		err := a.stream(ctx, r, io.Discard, "mariadb", args...)
		if err != nil && notInstalled(err) {
			err = a.stream(ctx, r, io.Discard, "mysql", args...)
		}
		return err
	}
	return pipe(ctx, protocol.DBMariaDB, dump, imp, progress, sp)
}

// pipeBuffer verbindt dump en import: een io.Pipe of een spool.
type pipeBuffer interface {
	io.Reader
	io.Writer
	// Close: de dump is compleet. CloseWithError: de dump mislukte.
	Close() error
	CloseWithError(err error)
	// CloseRead: de import leest niet meer.
	CloseRead(err error)
}

// memPipe is een io.Pipe als pipeBuffer. Ook na een mislukte dump krijgt
// de import gewoon het einde van de invoer.
type memPipe struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p memPipe) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p memPipe) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p memPipe) Close() error                { return p.w.Close() }
func (p memPipe) CloseWithError(error)        { _ = p.w.Close() }
func (p memPipe) CloseRead(err error)         { _ = p.r.CloseWithError(err) }

// pipe stuurt wat dump schrijft naar imp, via sp als die er is en anders
// direct. Stopt de dump met een fout, dan stopt de import en is de fout
// van de dump de oorzaak; mislukt de import eerst, dan stopt de dump. Een
// dump zonder slotregel is niet compleet.
func pipe(ctx context.Context, engine string, dump func(context.Context, io.Writer) error, imp func(context.Context, io.Reader) error,
	progress func(int64), sp *spool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var buf pipeBuffer = sp
	if sp == nil {
		pr, pw := io.Pipe()
		buf = memPipe{r: pr, w: pw}
	}
	scan := newDumpScan(engine, progress)
	var dumpFailed atomic.Bool
	dumpErr := make(chan error, 1)
	go func() {
		err := dump(ctx, io.MultiWriter(buf, scan))
		if err == nil {
			err = buf.Close()
		}
		if err != nil {
			dumpFailed.Store(true)
			buf.CloseWithError(err)
		}
		dumpErr <- err
	}()
	err := imp(ctx, buf)
	causedByDump := dumpFailed.Load()
	if err != nil {
		cancel()
		buf.CloseRead(err)
	} else {
		// Wat de import niet las, moet toch weg, anders blijft de dump hangen.
		_, _ = io.Copy(io.Discard, buf)
	}
	derr := <-dumpErr
	switch {
	case derr != nil && (err == nil || causedByDump):
		return fmt.Errorf("dump: %w", derr)
	case err != nil:
		return fmt.Errorf("importeren: %w", err)
	case !scan.complete():
		return errors.New("de dump van de oude server is niet compleet: de slotregel ontbreekt")
	}
	return nil
}

// countBoth telt de rijen van elke tabel in de nieuwe database, en
// dezelfde tabellen op de oude server, en leest de views, routines,
// triggers en events in de nieuwe database.
func (a *Agent) countBoth(ctx context.Context, client []string, db, target string, run *protocol.DBMigrateRun) error {
	q, _ := dbsql.BaseTables(target)
	rows, err := a.maria(ctx, false, q)
	if err != nil {
		return fmt.Errorf("tabellen lezen: %w", err)
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r[0])
	}
	newCounts, err := a.countRows(ctx, nil, target, names)
	if err != nil {
		return fmt.Errorf("tellen in %s: %w", target, err)
	}
	oldCounts, err := a.countRows(ctx, client, db, names)
	if err != nil {
		return fmt.Errorf("tellen op de oude server: %w", err)
	}
	run.Tables = make([]protocol.DBMigrateCount, len(names))
	for i, n := range names {
		run.Tables[i] = protocol.DBMigrateCount{Name: n, Source: oldCounts[i], Target: newCounts[i]}
	}
	info, err := a.migrateInspect(ctx, target)
	if err != nil {
		return fmt.Errorf("%s lezen: %w", target, err)
	}
	run.Views, run.Routines, run.Triggers, run.Events = info.Views, info.Routines, info.Triggers, info.Events
	return nil
}

// countRows telt de rijen van tables in db, op deze node (client nil) of
// op de oude server.
func (a *Agent) countRows(ctx context.Context, client []string, db string, tables []string) ([]int64, error) {
	queries, err := dbsql.CountRows(db, tables)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(tables))
	for _, q := range queries {
		rows, err := a.mariaWith(ctx, client, q)
		if err != nil {
			return nil, err
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
