package agent

// Dumps van de databases op de node. Een dump komt in
// protocol.DBDumpDir/<database>/, met zstd gecomprimeerd, en naast elke
// dump staat een .json met wat de agent erover weet: de grootte, de
// tabellen en of hij compleet is. Maken en terugzetten lopen op de
// achtergrond, één tegelijk; de server vraagt met list hoe ver het is. Een
// dump gaat nooit naar de server. Dumps van MariaDB eindigen op .sql.zst,
// die van PostgreSQL op .pgsql.zst, zodat ze naast elkaar kunnen staan.
//
// Terugzetten gaat altijd in een nieuwe database, zodat de bestaande blijft
// zoals ze is. Mislukt het, dan haalt de agent die nieuwe database weer
// weg. MariaDB zet de agent terug op zijn eigen node. PostgreSQL schrijft
// alleen op de primary, en dat kan een andere node zijn dan die met de
// dump; daarom zet de agent een dump van PostgreSQL over het netwerk terug
// op de primary, als een tijdelijke beheerder die de server daar maakte.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var dumpFileRe = regexp.MustCompile(protocol.DBDumpPattern)

const (
	// dumpTableMarker staat boven elke tabel in een dump van mariadb-dump,
	// en dumpEndMarker op de laatste regel.
	dumpTableMarker = "-- Table structure for table `"
	dumpEndMarker   = "\n-- Dump completed"
	// pg_dump zet boven elke tabel "-- Name: <tabel>; Type: TABLE; Schema:
	// ...", en schrijft pgDumpEndMarker aan het eind, met daarna nog de
	// regel \unrestrict en een sleutel.
	pgTableMarker   = "-- Name: "
	pgTableType     = "; Type: TABLE; "
	pgDumpEndMarker = "\n-- PostgreSQL database dump complete\n"
	// dumpHead is hoeveel van het begin van een regel de scan bekijkt, en
	// dumpTail hoeveel van het eind van de dump.
	dumpHead = 512
	dumpTail = 256
	// dumpRestores is hoeveel keer terugzetten de agent onthoudt.
	dumpRestores = 20
	// dumpPartialAge is hoe oud een half geschreven dump zonder lopende
	// dump moet zijn voor prune hem weghaalt.
	dumpPartialAge = time.Hour
)

// dumpArgs zijn de opties van mariadb-dump: een consistente momentopname
// zonder tabellen op slot, rij voor rij gelezen, met routines, events en
// triggers, en binaire kolommen als hex. Zonder --databases, zodat de dump
// in een database met een andere naam terug kan.
var dumpArgs = []string{
	"--single-transaction", "--quick", "--routines", "--events", "--triggers", "--hex-blob",
	"--default-character-set=utf8mb4",
}

// pgDumpArgs zijn de opties van pg_dump, als postgres via de socket: platte
// SQL met de eigenaars en rechten, en een fout als een tabel na vijf
// minuten nog op slot is in plaats van eindeloos wachten. pg_dump leest
// altijd een consistente momentopname. De database komt erachter, zonder
// --create, zodat de dump in een database met een andere naam terug kan.
var pgDumpArgs = []string{"-u", "postgres", "--", "pg_dump", "--lock-wait-timeout=5min"}

// dumpState is wat er op de achtergrond loopt.
type dumpState struct {
	mu       sync.Mutex
	running  *protocol.DBDumpRun
	restores []protocol.DBDumpRun
	// finished zijn de dumps sinds de start van de agent, voor het geval
	// het .json-bestand niet te schrijven was.
	finished map[string]protocol.DBDumpFile
	// idle is dicht als er niets loopt.
	idle chan struct{}
}

func (a *Agent) dumps() *dumpState {
	a.initDB()
	return &a.db.dumps
}

func (a *Agent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Agent) dumpDir() string {
	if a.DumpDir != "" {
		return a.DumpDir
	}
	return protocol.DBDumpDir
}

// dbDump doet een dumpactie voor engine; list geeft de dumps van beide.
func (a *Agent) dbDump(ctx context.Context, engine string, req *protocol.DBDumpRequest) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	if req == nil {
		res.Error = "dump zonder inhoud"
		return res
	}
	engine = cmp.Or(engine, protocol.DBMariaDB)
	var started string
	var removed []string
	var err error
	switch req.Action {
	case protocol.DBDumpList:
	case protocol.DBDumpStart:
		started, err = a.dumpStart(ctx, engine, req.Database)
	case protocol.DBDumpPrune:
		removed, err = a.dumpPrune(engine, req)
	case protocol.DBDumpRestore:
		err = a.dumpRestore(ctx, engine, req)
	default:
		err = fmt.Errorf("onbekende dumpactie %q", req.Action)
	}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	out, err := a.dumpList()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	out.Started, out.Removed = started, removed
	res.Dump = &out
	return res
}

// dumpInfo leest of db bestaat, met haar tabellen en hun grootte.
func (a *Agent) dumpInfo(ctx context.Context, engine, db string) (exists bool, tables int, size int64, err error) {
	if engine == protocol.DBPostgreSQL {
		return a.pgDumpInfo(ctx, db)
	}
	q, err := dbsql.DumpInfo(db)
	if err != nil {
		return false, 0, 0, err
	}
	rows, err := a.maria(ctx, false, q)
	if err != nil {
		return false, 0, 0, errors.New("database lezen: " + dbError(err))
	}
	if len(rows) != 1 || len(rows[0]) < 3 {
		return false, 0, 0, errors.New("database lezen: onverwacht antwoord")
	}
	tables, _ = strconv.Atoi(rows[0][1])
	size, _ = strconv.ParseInt(rows[0][2], 10, 64)
	return rows[0][0] != "0", tables, size, nil
}

// begin legt vast dat run loopt; er loopt er hoogstens één.
func (st *dumpState) begin(run *protocol.DBDumpRun) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if r := st.running; r != nil {
		if r.Action == protocol.DBDumpRestore {
			return fmt.Errorf("de agent zet al %s terug in %s; wacht tot dat klaar is", r.File, r.Target)
		}
		return fmt.Errorf("de agent maakt al een dump van %s; wacht tot die klaar is", r.Database)
	}
	st.running, st.idle = run, make(chan struct{})
	return nil
}

func (st *dumpState) end(fn func()) {
	st.mu.Lock()
	defer st.mu.Unlock()
	fn()
	st.running = nil
	if st.idle != nil {
		close(st.idle)
		st.idle = nil
	}
}

func (st *dumpState) progress(n int64) {
	st.mu.Lock()
	if st.running != nil {
		st.running.Bytes += n
	}
	st.mu.Unlock()
}

// waitDump wacht tot er geen dump of terugzetten meer loopt.
func (a *Agent) waitDump(ctx context.Context) error {
	st := a.dumps()
	st.mu.Lock()
	idle := st.idle
	st.mu.Unlock()
	if idle == nil {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// needEngine zegt waarom er op deze node niets met engine kan.
func (a *Agent) needEngine(ctx context.Context, engine string) error {
	if engine == protocol.DBPostgreSQL {
		return a.needPG(ctx)
	}
	return a.needMaria(ctx)
}

// dumpStart begint een dump van db en geeft de naam ervan.
func (a *Agent) dumpStart(ctx context.Context, engine, db string) (string, error) {
	if err := dbsql.CheckDatabaseOf(engine, db); err != nil {
		return "", err
	}
	if err := a.needEngine(ctx, engine); err != nil {
		return "", err
	}
	exists, tables, size, err := a.dumpInfo(ctx, engine, db)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("de database %s bestaat niet op deze node", db)
	}
	now := a.now().UTC()
	ext := ".sql.zst"
	if engine == protocol.DBPostgreSQL {
		ext = ".pgsql.zst"
	}
	run := &protocol.DBDumpRun{
		Action: protocol.DBDumpStart, Database: db, File: db + "-" + now.Format("20060102-150405") + ext,
		Started: now, Total: size,
	}
	dir, err := a.dumpMkdir(db)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, run.File)
	if _, err := os.Lstat(path + ".json"); err == nil {
		return "", errors.New("er is net al een dump van deze database begonnen; probeer het over een seconde opnieuw")
	}
	st := a.dumps()
	if err := st.begin(run); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path+".partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		st.end(func() {})
		return "", fmt.Errorf("dump beginnen: %w", err)
	}
	go a.runDump(f, path, engine, *run, tables)
	return run.File, nil
}

// dumpMkdir maakt de map van db, en de map erboven, met modus 0700.
func (a *Agent) dumpMkdir(db string) (string, error) {
	root := a.dumpDir()
	dir := filepath.Join(root, db)
	// /var/backups is er op Debian altijd.
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return "", fmt.Errorf("map voor de dumps: %w", err)
	}
	for _, d := range []string{root, dir} {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("map voor de dumps: %w", err)
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return "", fmt.Errorf("map voor de dumps: %w", err)
		}
	}
	return dir, nil
}

// runDump schrijft de dump naar f, en daarna het .json-bestand ernaast.
func (a *Agent) runDump(f *os.File, path, engine string, run protocol.DBDumpRun, expected int) {
	ctx, cancel := context.WithTimeout(context.Background(), protocol.DBDumpTimeout)
	defer cancel()
	st := a.dumps()
	scan := newDumpScan(engine, st.progress)
	err := func() error {
		zw, err := zstd.NewWriter(f)
		if err != nil {
			_ = f.Close()
			return err
		}
		w := io.MultiWriter(zw, scan)
		if engine == protocol.DBPostgreSQL {
			err = a.stream(ctx, nil, w, "runuser", append(slices.Clone(pgDumpArgs), run.Database)...)
		} else {
			args := append(slices.Clone(dumpArgs), run.Database)
			err = a.stream(ctx, nil, w, "mariadb-dump", args...)
			if err != nil && notInstalled(err) {
				err = a.stream(ctx, nil, w, "mysqldump", args...)
			}
		}
		if cerr := zw.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(path+".partial", path)
		}
		return err
	}()
	meta := protocol.DBDumpFile{
		Engine: engine, Database: run.Database, File: run.File, RawBytes: scan.n, Tables: scan.tables, TablesExpected: expected,
		Complete: scan.complete(), Started: run.Started, Finished: a.now().UTC(),
	}
	if err != nil {
		_ = os.Remove(path + ".partial")
		meta.Error = clip(err.Error(), 500)
		a.Log.Warn("dump mislukt", "database", run.Database, "err", err)
	} else if fi, err := os.Stat(path); err == nil {
		meta.SizeBytes = fi.Size()
	}
	if err := writeJSON0600(path+".json", meta); err != nil {
		a.Log.Warn("gegevens van een dump schrijven mislukt", "file", run.File, "err", err)
	}
	st.end(func() {
		if st.finished == nil {
			st.finished = map[string]protocol.DBDumpFile{}
		}
		st.finished[run.File] = meta
	})
}

// writeJSON0600 schrijft v naar path via een tijdelijk bestand, zodat er
// nooit een half bestand staat.
func writeJSON0600(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// dumpList leest de dumps op de node.
func (a *Agent) dumpList() (protocol.DBDumpResult, error) {
	out := protocol.DBDumpResult{Dumps: []protocol.DBDumpFile{}}
	files, err := a.dumpFiles()
	if err != nil {
		return out, err
	}
	st := a.dumps()
	st.mu.Lock()
	for name, f := range st.finished {
		if !slices.ContainsFunc(files, func(x protocol.DBDumpFile) bool { return x.File == name }) {
			if _, err := os.Lstat(filepath.Join(a.dumpDir(), f.Database, name)); err == nil || f.Error != "" {
				files = append(files, f)
			}
		}
	}
	if st.running != nil {
		r := *st.running
		out.Running = &r
	}
	out.Restores = slices.Clone(st.restores)
	st.mu.Unlock()
	slices.SortFunc(files, func(x, y protocol.DBDumpFile) int {
		return cmp.Or(x.Started.Compare(y.Started), strings.Compare(x.File, y.File))
	})
	out.Dumps = files
	return out, nil
}

// dumpFiles leest de .json-bestanden in de mappen per database.
func (a *Agent) dumpFiles() ([]protocol.DBDumpFile, error) {
	root := a.dumpDir()
	dirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dumps lezen: %w", err)
	}
	var out []protocol.DBDumpFile
	for _, d := range dirs {
		if !d.IsDir() || dbsql.CheckDatabase(d.Name()) != nil && dbsql.CheckPGDatabase(d.Name()) != nil {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			return nil, fmt.Errorf("dumps lezen: %w", err)
		}
		for _, e := range entries {
			name, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok || !e.Type().IsRegular() {
				continue
			}
			engine := protocol.DBDumpEngine(name)
			if m := dumpFileRe.FindStringSubmatch(name); m == nil || m[1] != d.Name() || dbsql.CheckDatabaseOf(engine, m[1]) != nil {
				continue
			}
			f, err := readDumpMeta(filepath.Join(root, d.Name(), e.Name()))
			if err != nil {
				a.Log.Warn("gegevens van een dump lezen mislukt", "file", e.Name(), "err", err)
				continue
			}
			f.Engine, f.Database, f.File = engine, d.Name(), name
			if f.Error == "" {
				fi, err := os.Stat(filepath.Join(root, d.Name(), name))
				if err != nil {
					f.Error = "het bestand van de dump staat er niet meer"
				} else {
					f.SizeBytes = fi.Size()
				}
			}
			out = append(out, f)
		}
	}
	return out, nil
}

func readDumpMeta(path string) (protocol.DBDumpFile, error) {
	var f protocol.DBDumpFile
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if len(b) > 64<<10 {
		return f, errors.New("te groot")
	}
	return f, json.Unmarshal(b, &f)
}

// dumpPrune haalt de dumps van engine weg die ouder zijn dan KeepDays,
// behalve die in Keep. Zonder Keep blijft de nieuwste complete per database
// staan, zodat een reeks mislukte dumps nooit de laatste goede weghaalt;
// met Keep beslist de server, die weet welke goede dump op een andere node
// staat. De dumps van de andere engine blijven altijd staan.
func (a *Agent) dumpPrune(engine string, req *protocol.DBDumpRequest) ([]string, error) {
	if req.KeepDays < 1 {
		return nil, errors.New("bewaar dumps minstens één dag")
	}
	if req.Database != "" {
		if err := dbsql.CheckDatabaseOf(engine, req.Database); err != nil {
			return nil, err
		}
	}
	files, err := a.dumpFiles()
	if err != nil {
		return nil, err
	}
	files = slices.DeleteFunc(files, func(f protocol.DBDumpFile) bool { return f.Engine != engine })
	newest := map[string]string{}
	for _, f := range files {
		if req.Keep == nil && f.Error == "" && f.Complete {
			if cur, ok := newest[f.Database]; !ok || f.File > cur {
				newest[f.Database] = f.File
			}
		}
	}
	st := a.dumps()
	st.mu.Lock()
	running := ""
	if st.running != nil {
		running = st.running.File
	}
	st.mu.Unlock()
	cutoff := a.now().Add(-time.Duration(req.KeepDays) * 24 * time.Hour)
	removed := []string{}
	for _, f := range files {
		switch {
		case req.Database != "" && f.Database != req.Database,
			!f.Started.Before(cutoff), slices.Contains(req.Keep, f.File), newest[f.Database] == f.File, f.File == running:
			continue
		}
		path := filepath.Join(a.dumpDir(), f.Database, f.File)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, fmt.Errorf("%s weghalen: %w", f.File, err)
		}
		if err := os.Remove(path + ".json"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, fmt.Errorf("%s weghalen: %w", f.File, err)
		}
		removed = append(removed, f.File)
		st.mu.Lock()
		delete(st.finished, f.File)
		st.mu.Unlock()
	}
	a.prunePartials(running)
	return removed, nil
}

// prunePartials haalt halve dumps weg die van een afgebroken agent zijn.
func (a *Agent) prunePartials(running string) {
	dirs, _ := os.ReadDir(a.dumpDir())
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(a.dumpDir(), d.Name())
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name, ok := strings.CutSuffix(e.Name(), ".partial")
			if !ok || name == running || !dumpFileRe.MatchString(name) {
				continue
			}
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > dumpPartialAge {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

// dumpRestore begint het terugzetten van een dump in een nieuwe database.
func (a *Agent) dumpRestore(ctx context.Context, engine string, req *protocol.DBDumpRequest) error {
	pg := engine == protocol.DBPostgreSQL
	if err := dbsql.CheckDatabaseOf(engine, req.Database); err != nil {
		return err
	}
	if m := dumpFileRe.FindStringSubmatch(req.File); m == nil || m[1] != req.Database || protocol.DBDumpEngine(req.File) != engine {
		return fmt.Errorf("%q is geen dump van %s", req.File, req.Database)
	}
	if err := dbsql.CheckDatabaseOf(engine, req.Target); err != nil {
		return err
	}
	if pg {
		if err := dbsql.CheckPGLogin(req.Primary, req.User, req.Password); err != nil {
			return err
		}
	} else if err := a.needMaria(ctx); err != nil {
		return err
	}
	path := filepath.Join(a.dumpDir(), req.Database, req.File)
	meta, err := readDumpMeta(path + ".json")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("de dump %s staat niet op deze node", req.File)
	case err != nil:
		return fmt.Errorf("de dump %s: %w", req.File, err)
	case meta.Error != "":
		return fmt.Errorf("de dump %s is mislukt: %s", req.File, meta.Error)
	case !meta.Complete:
		return fmt.Errorf("de dump %s is niet compleet", req.File)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("de dump %s staat niet op deze node", req.File)
	}
	var login *pgLogin
	if pg {
		if login, err = newPGLogin(req.Primary, protocol.DBPGPort, req.User, req.Password); err != nil {
			return err
		}
	}
	started := false
	defer func() {
		if !started {
			login.close()
		}
	}()
	var exists bool
	if pg {
		exists, err = a.pgRemoteExists(ctx, login, req.Target)
	} else {
		exists, _, _, err = a.dumpInfo(ctx, engine, req.Target)
	}
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("de database %s bestaat al; kies een andere naam", req.Target)
	}
	run := &protocol.DBDumpRun{
		Action: protocol.DBDumpRestore, Database: req.Database, File: req.File, Target: req.Target,
		Started: a.now().UTC(), Total: fi.Size(),
	}
	st := a.dumps()
	if err := st.begin(run); err != nil {
		return err
	}
	if pg {
		sql, _ := dbsql.PGRestoreCreate(req.Database, req.Target)
		if out, err := a.runStdin(ctx, []byte(sql), "psql", login.args("postgres")...); err != nil {
			st.end(func() {})
			return fmt.Errorf("database %s maken op %s: %s", req.Target, req.Primary, pgError(out, err, ""))
		}
	} else {
		sql, _ := dbsql.CreateDatabase(req.Target)
		if res := a.dbWrite(ctx, sql, ""); res.Error != "" {
			st.end(func() {})
			return fmt.Errorf("database %s maken: %s", req.Target, res.Error)
		}
	}
	started = true
	go a.runRestore(path, login, *run)
	return nil
}

// runRestore zet de dump op path terug in run.Target: met login in
// PostgreSQL op de primary, anders in de MariaDB van deze node.
func (a *Agent) runRestore(path string, login *pgLogin, run protocol.DBDumpRun) {
	defer login.close()
	ctx, cancel := context.WithTimeout(context.Background(), protocol.DBDumpTimeout)
	defer cancel()
	st := a.dumps()
	err := func() error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		zr, err := zstd.NewReader(&countReader{r: f, progress: st.progress})
		if err != nil {
			return err
		}
		defer zr.Close()
		if login != nil {
			// Wat de tijdelijke beheerder zelf maakte, gaat daarna naar de
			// eigenaar van de database.
			in := io.MultiReader(zr, strings.NewReader(dbsql.PGRestoreFinish))
			return a.stream(ctx, in, io.Discard, "psql", login.args(run.Target)...)
		}
		args := []string{"--batch", "--skip-print-query-on-error", run.Target}
		err = a.stream(ctx, zr, io.Discard, "mariadb", args...)
		if err != nil && notInstalled(err) {
			err = a.stream(ctx, zr, io.Discard, "mysql", args...)
		}
		return err
	}()
	if err != nil {
		a.Log.Warn("dump terugzetten mislukt", "file", run.File, "target", run.Target, "err", err)
		run.Error = clip(err.Error(), 500)
		dctx, dcancel := context.WithTimeout(context.Background(), protocol.DBTimeout)
		if login != nil {
			sql, _ := dbsql.PGDrop(protocol.DBDropRequest{Database: run.Target, Confirm: run.Target})
			if out, err := a.runStdin(dctx, []byte(sql), "psql", login.args("postgres")...); err != nil {
				run.Error += "; " + run.Target + " weghalen mislukte ook: " + pgError(out, err, "")
			}
		} else {
			sql, _ := dbsql.Drop(protocol.DBDropRequest{Database: run.Target, Confirm: run.Target})
			if res := a.dbWrite(dctx, sql, ""); res.Error != "" {
				run.Error += "; " + run.Target + " weghalen mislukte ook: " + res.Error
			}
		}
		dcancel()
	}
	run.Finished = a.now().UTC()
	st.end(func() {
		if st.running != nil {
			run.Bytes = st.running.Bytes
		}
		st.restores = append(st.restores, run)
		if len(st.restores) > dumpRestores {
			st.restores = st.restores[len(st.restores)-dumpRestores:]
		}
	})
}

type countReader struct {
	r        io.Reader
	progress func(int64)
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.progress(int64(n))
	}
	return n, err
}

// dumpScan telt wat mariadb-dump of pg_dump schrijft: de bytes, de
// tabellen, en of de slotregel er staat.
type dumpScan struct {
	n        int64
	tables   int
	progress func(int64)
	// table zegt of een regel, tot dumpHead tekens, boven een tabel staat.
	table func(head []byte) bool
	end   string
	// head is het begin van de regel waarin de scan staat; mid: dat begin
	// is al bekeken.
	head []byte
	mid  bool
	tail []byte
}

func newDumpScan(engine string, progress func(int64)) *dumpScan {
	if engine == protocol.DBPostgreSQL {
		return &dumpScan{progress: progress, end: pgDumpEndMarker, table: func(head []byte) bool {
			return bytes.HasPrefix(head, []byte(pgTableMarker)) && bytes.Contains(head, []byte(pgTableType))
		}}
	}
	return &dumpScan{progress: progress, end: dumpEndMarker, table: func(head []byte) bool {
		return bytes.HasPrefix(head, []byte(dumpTableMarker))
	}}
}

func (s *dumpScan) Write(p []byte) (int, error) {
	n := len(p)
	s.n += int64(n)
	if s.progress != nil {
		s.progress(int64(n))
	}
	s.tail = append(s.tail, p[max(0, n-dumpTail):]...)
	if len(s.tail) > dumpTail {
		s.tail = s.tail[len(s.tail)-dumpTail:]
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		line := p
		if i >= 0 {
			line = p[:i]
		}
		if !s.mid {
			s.head = append(s.head, line[:min(len(line), dumpHead-len(s.head))]...)
			if i >= 0 || len(s.head) == dumpHead {
				if s.table(s.head) {
					s.tables++
				}
				s.mid = true
			}
		}
		if i < 0 {
			return n, nil
		}
		p = p[i+1:]
		s.mid = false
		s.head = s.head[:0]
	}
	return n, nil
}

func (s *dumpScan) complete() bool { return bytes.Contains(s.tail, []byte(s.end)) }

// stream voert een programma uit met in op stdin en stdout naar out.
func (a *Agent) stream(ctx context.Context, in io.Reader, out io.Writer, name string, args ...string) error {
	if a.Stream != nil {
		return a.Stream(ctx, in, out, name, args...)
	}
	if a.Exec != nil {
		return errors.New("agent: Exec zonder Stream")
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Dir = "/"
	cmd.Stdin, cmd.Stdout = in, out
	var stderr tailWriter
	cmd.Stderr = &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if err != nil {
		if line := stderr.errorLine(); line != "" {
			return fmt.Errorf("%s: %s", name, line)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// tailWriter houdt de laatste 4 KiB van stderr.
type tailWriter struct{ b []byte }

func (t *tailWriter) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	return len(p), nil
}

// errorLine is de laatste regel met een fout, of anders de laatste regel.
// mariadb herhaalt met --skip-print-query-on-error de mislukte opdracht
// niet, dus er staan geen rijen uit de dump in.
func (t *tailWriter) errorLine() string {
	lines := strings.Split(strings.TrimSpace(string(t.b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.Contains(strings.ToLower(l), "error") {
			return clip(l, 300)
		}
	}
	return clip(strings.TrimSpace(lines[len(lines)-1]), 300)
}
