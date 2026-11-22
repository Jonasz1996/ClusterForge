package agenttest

// Gebruikers, databases en dumps van een nagespeelde PostgreSQL: de
// opdrachten uit dbsql via stdin aan psql als postgres, pg_dump, en psql
// over het netwerk met een tijdelijke beheerder en een passfile, zoals de
// agent een dump terugzet op de primary.

import (
	"bufio"
	"cmp"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// PGRole is een gebruiker van PostgreSQL. Verifier is de SCRAM-verifier
// die de agent stuurde, of een md5-hash; LogsIn rekent na of een wachtwoord
// bij een SCRAM-verifier past.
type PGRole struct {
	Name                                  string
	Super, CreateDB, CreateRole, Replicat bool
	Verifier                              string
	// ValidUntil: na dit tijdstip komt de gebruiker niet meer binnen; nul
	// is altijd.
	ValidUntil time.Time
	// NoLogin: een groep; MemberOf zijn de rollen waarvan hij lid is, en
	// ReadOnly: default_transaction_read_only staat aan voor hem.
	NoLogin  bool
	MemberOf []string
	ReadOnly bool
}

// PatroniRoles zijn de gebruikers die Patroni maakt.
func PatroniRoles() []PGRole {
	return []PGRole{{Name: "cf_replicatie", Replicat: true}, {Name: "cf_rewind"}, {Name: "postgres", Super: true, CreateDB: true, CreateRole: true, Replicat: true}}
}

// SCRAMVerifier is de verifier die PostgreSQL van password bewaart, met een
// vaste salt: zoals een rol op een oude server hem al heeft.
func SCRAMVerifier(password string) string {
	salt := sha256.Sum256([]byte("agenttest " + password))
	const iter = 4096
	salted, _ := pbkdf2.Key(sha256.New, password, salt[:16], iter, sha256.Size)
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	stored := sha256.Sum256(mac(salted, "Client Key"))
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iter, b64(salt[:16]), b64(stored[:]), b64(mac(salted, "Server Key")))
}

// LogsIn zegt of r met password binnenkomt via scram-sha-256.
func (r PGRole) LogsIn(password string, now time.Time) bool {
	if !r.ValidUntil.IsZero() && now.After(r.ValidUntil) {
		return false
	}
	m := scramRe.FindStringSubmatch(r.Verifier)
	if m == nil {
		return false
	}
	iter, _ := strconv.Atoi(m[1])
	salt, err1 := base64.StdEncoding.DecodeString(m[2])
	stored, err2 := base64.StdEncoding.DecodeString(m[3])
	if err1 != nil || err2 != nil {
		return false
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, iter, sha256.Size)
	if err != nil {
		return false
	}
	h := hmac.New(sha256.New, salted)
	h.Write([]byte("Client Key"))
	sum := sha256.Sum256(h.Sum(nil))
	return hmac.Equal(sum[:], stored)
}

var (
	scramRe = regexp.MustCompile(`^SCRAM-SHA-256\$([0-9]+):([A-Za-z0-9+/=]+)\$([A-Za-z0-9+/=]+):[A-Za-z0-9+/=]+$`)
	// De opdrachten uit dbsql.PGEnsure, PGDrop en PGRestoreCreate.
	pgCreateRoleRe  = regexp.MustCompile(`^SELECT 'CREATE ROLE "([a-z0-9_]+)" LOGIN' WHERE NOT EXISTS \(SELECT 1 FROM pg_roles WHERE rolname = '([a-z0-9_]+)'\)\\gexec$`)
	pgRestoreRoleRe = regexp.MustCompile(`^SELECT format\('ALTER ROLE "([a-z0-9_]+)" WITH LOGIN SUPERUSER PASSWORD %L VALID UNTIL %L', '([^']+)', now\(\) \+ interval '([0-9]+) hours'\)\\gexec$`)
	pgAlterRoleRe   = regexp.MustCompile(`^ALTER ROLE "([a-z0-9_]+)" WITH LOGIN PASSWORD '([^']+)';$`)
	pgCreateDBRe    = regexp.MustCompile(`^SELECT 'CREATE DATABASE "([a-z0-9_]+)"(?: OWNER "([a-z0-9_]+)")?' WHERE NOT EXISTS \(SELECT 1 FROM pg_database WHERE datname = '([a-z0-9_]+)'\)\\gexec$`)
	pgOwnerRe       = regexp.MustCompile(`^ALTER DATABASE "([a-z0-9_]+)" OWNER TO "([a-z0-9_]+)";$`)
	pgDropDBRe      = regexp.MustCompile(`^DROP DATABASE IF EXISTS "([a-z0-9_]+)";$`)
	pgDropRoleRe    = regexp.MustCompile(`^DROP ROLE IF EXISTS "([a-z0-9_]+)";$`)
	pgRestoreDBRe   = regexp.MustCompile(`^SELECT format\('CREATE DATABASE %I OWNER %I', '([a-z0-9_]+)', coalesce\(\(SELECT pg_get_userbyid\(datdba\) FROM pg_database WHERE datname = '([a-z0-9_]+)'\), 'postgres'\)\)\\gexec$`)
	// pgDumpInfoRe is de query uit dbsql.PGDumpInfo.
	pgDumpInfoRe = regexp.MustCompile(`^SELECT count\(\*\), coalesce\(sum\(pg_database_size\(oid\)\), 0\) FROM pg_database WHERE datname = '([a-z0-9_]+)'$`)
	// pgTableRe telt de tabellen in een dump, zoals pg_dump ze schrijft.
	pgTableRe = regexp.MustCompile(`(?m)^-- Name: [^;]+; Type: TABLE; `)
	tempRole  = regexp.MustCompile(protocol.DBTempRolePattern)
)

func (p *Postgres) role(name string) int {
	return slices.IndexFunc(p.Roles, func(r PGRole) bool { return r.Name == name })
}

func (p *Postgres) database(name string) int {
	return slices.IndexFunc(p.Databases, func(d Database) bool { return d.Name == name })
}

// pgQuery beantwoordt de query's over gebruikers en dumps in database db;
// ok is false voor een andere query.
func (p *Postgres) pgQuery(db, q string) (rows [][]string, ok bool) {
	t := map[bool]string{true: "t", false: "f"}
	switch {
	case q == dbsql.PGRoles:
		roles := slices.Clone(p.Roles)
		slices.SortFunc(roles, func(a, b PGRole) int { return strings.Compare(a.Name, b.Name) })
		for _, r := range roles {
			rows = append(rows, []string{r.Name, t[r.Super], t[r.CreateDB], t[r.CreateRole], t[r.Replicat]})
		}
	case q == dbsql.PGDatabaseGrants:
		for _, d := range p.Databases {
			if d.Name == "postgres" {
				continue
			}
			if d.Owner != "" && p.role(d.Owner) >= 0 {
				rows = append(rows, []string{d.Name, d.Owner, "OWNER"})
			}
			for _, r := range slices.Sorted(maps.Keys(d.ACL)) {
				for _, priv := range d.ACL[r] {
					rows = append(rows, []string{d.Name, r, priv})
				}
			}
		}
		slices.SortFunc(rows, func(a, b []string) int { return strings.Compare(strings.Join(a, "\x00"), strings.Join(b, "\x00")) })
	case q == dbsql.PGDumpTables:
		n := 0
		if i := p.database(db); i >= 0 {
			n = p.Databases[i].Tables
		}
		rows = [][]string{{strconv.Itoa(n)}}
	case pgDumpInfoRe.MatchString(q):
		rows = [][]string{{"0", "0"}}
		if i := p.database(pgDumpInfoRe.FindStringSubmatch(q)[1]); i >= 0 {
			rows = [][]string{{"1", strconv.FormatInt(p.Databases[i].Size, 10)}}
		}
	default:
		return p.pgMigrateQuery(db, q)
	}
	return rows, true
}

// pgWrite speelt psql met opdrachten op stdin na, als user, een per regel,
// tot de eerste fout. Een replica weigert, zoals PostgreSQL.
func (p *Postgres) pgWrite(stdin, user string) ([]byte, error) {
	for i, line := range strings.Split(stdin, "\n") {
		fail := func(msg string) ([]byte, error) {
			return []byte(fmt.Sprintf("psql:<stdin>:%d: ERROR:  %s\n", i+1, msg)), errors.New("exit status 3")
		}
		if line == "" {
			continue
		}
		if p.Recovery {
			cmd := strings.Fields(strings.TrimPrefix(strings.TrimPrefix(line, "SELECT format('"), "SELECT '"))
			return fail("cannot execute " + strings.Join(cmd[:min(2, len(cmd))], " ") + " in a read-only transaction")
		}
		switch {
		case pgCreateRoleRe.MatchString(line):
			r := pgCreateRoleRe.FindStringSubmatch(line)
			if r[1] != r[2] {
				return fail("agenttest: twee namen in CREATE ROLE")
			}
			if p.role(r[1]) < 0 {
				p.Roles = append(p.Roles, PGRole{Name: r[1]})
			}
		case pgRestoreRoleRe.MatchString(line):
			r := pgRestoreRoleRe.FindStringSubmatch(line)
			j := p.role(r[1])
			if j < 0 {
				return fail(fmt.Sprintf("role %q does not exist", r[1]))
			}
			hours, _ := strconv.Atoi(r[3])
			p.Roles[j].Super, p.Roles[j].Verifier, p.Roles[j].ValidUntil = true, r[2], time.Now().Add(time.Duration(hours)*time.Hour)
		case pgAlterRoleRe.MatchString(line):
			r := pgAlterRoleRe.FindStringSubmatch(line)
			j := p.role(r[1])
			if j < 0 {
				return fail(fmt.Sprintf("role %q does not exist", r[1]))
			}
			p.Roles[j].Verifier, p.Roles[j].NoLogin = r[2], false
		case pgCreateDBRe.MatchString(line):
			r := pgCreateDBRe.FindStringSubmatch(line)
			if r[1] != r[3] {
				return fail("agenttest: twee namen in CREATE DATABASE")
			}
			if p.database(r[1]) < 0 {
				if out, err := p.createDB(r[1], r[2], fail); err != nil {
					return out, err
				}
			}
		case pgRestoreDBRe.MatchString(line):
			r := pgRestoreDBRe.FindStringSubmatch(line)
			owner := "postgres"
			if j := p.database(r[2]); j >= 0 && p.Databases[j].Owner != "" {
				owner = p.Databases[j].Owner
			}
			if p.database(r[1]) >= 0 {
				return fail(fmt.Sprintf("database %q already exists", r[1]))
			}
			if out, err := p.createDB(r[1], owner, fail); err != nil {
				return out, err
			}
		case pgOwnerRe.MatchString(line):
			r := pgOwnerRe.FindStringSubmatch(line)
			j := p.database(r[1])
			switch {
			case j < 0:
				return fail(fmt.Sprintf("database %q does not exist", r[1]))
			case p.role(r[2]) < 0:
				return fail(fmt.Sprintf("role %q does not exist", r[2]))
			}
			p.Databases[j].Owner = r[2]
		case pgDropDBRe.MatchString(line):
			name := pgDropDBRe.FindStringSubmatch(line)[1]
			if slices.ContainsFunc(p.Clients, func(c Client) bool { return c.Database == name && c.Count > 0 }) {
				return fail(fmt.Sprintf("database %q is being accessed by other users", name))
			}
			p.Databases = slices.DeleteFunc(p.Databases, func(d Database) bool { return d.Name == name })
		case pgDropRoleRe.MatchString(line):
			name := pgDropRoleRe.FindStringSubmatch(line)[1]
			if name == user {
				return fail("current user cannot be dropped")
			}
			if slices.ContainsFunc(p.Databases, func(d Database) bool { return d.Owner == name }) {
				return fail(fmt.Sprintf("role %q cannot be dropped because some objects depend on it", name))
			}
			p.Roles = slices.DeleteFunc(p.Roles, func(r PGRole) bool { return r.Name == name })
			for i := range p.Databases {
				delete(p.Databases[i].ACL, name)
			}
		case line == strings.TrimSuffix(dbsql.PGDropExpired, "\n"):
			now := time.Now()
			p.Roles = slices.DeleteFunc(p.Roles, func(r PGRole) bool {
				return tempRole.MatchString(r.Name) && !r.ValidUntil.IsZero() && r.ValidUntil.Before(now)
			})
		default:
			if msg, ok := p.pgMigrateWrite(line); ok {
				if msg != "" {
					return fail(msg)
				}
				continue
			}
			return fail("agenttest kent deze opdracht niet: " + line)
		}
	}
	return nil, nil
}

func (p *Postgres) createDB(name, owner string, fail func(string) ([]byte, error)) ([]byte, error) {
	if p.database(name) >= 0 {
		return fail(fmt.Sprintf("database %q already exists", name))
	}
	if owner != "" && owner != "postgres" && p.role(owner) < 0 {
		return fail(fmt.Sprintf("role %q does not exist", owner))
	}
	p.Databases = append(p.Databases, Database{Name: name, Owner: owner})
	slices.SortFunc(p.Databases, func(a, b Database) int { return strings.Compare(a.Name, b.Name) })
	return nil, nil
}

// pgLocal speelt runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 -d
// postgres met opdrachten op stdin na.
func (h *Host) pgLocal(args []string, stdin string) ([]byte, error) {
	want := []string{"-u", "postgres", "--", "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", "postgres"}
	if !slices.Equal(args, want) {
		return []byte("agenttest: runuser " + strings.Join(args, " ") + "\n"), errors.New("exit status 2")
	}
	p := h.Postgres
	if p == nil || p.Down {
		return []byte("psql: error: connection to server on socket \"/var/run/postgresql/.s.PGSQL.5432\" failed: No such file or directory\n"),
			errors.New("exit status 2")
	}
	return p.pgWrite(stdin, "postgres")
}

// pgConn is een verbinding van psql met de PostgreSQL van een andere
// machine; unlock geeft die machine weer vrij.
type pgConn struct {
	p      *Postgres
	db     string
	user   string
	unlock func()
}

// pgConnect speelt het verbinden van psql -d <conninfo> na: het adres op
// het netwerk, de gebruiker met het wachtwoord uit de passfile, en de
// database. psql leest de passfile alleen als niemand anders hem kan lezen.
func (h *Host) pgConnect(args []string) (*pgConn, []string, string) {
	i := slices.Index(args, "-d")
	if i < 0 || i+1 >= len(args) || !strings.Contains(args[i+1], "host=") {
		return nil, nil, "agenttest: psql zonder conninfo"
	}
	rest := append(slices.Clone(args[:i]), args[i+2:]...)
	cfg := map[string]string{}
	for f := range strings.FieldsSeq(args[i+1]) {
		k, v, _ := strings.Cut(f, "=")
		cfg[k] = strings.Trim(v, "'")
	}
	password := ""
	if fi, err := os.Stat(cfg["passfile"]); err == nil && fi.Mode().Perm()&0o077 == 0 {
		if f, err := os.Open(cfg["passfile"]); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				parts := strings.Split(sc.Text(), ":")
				if len(parts) == 5 && parts[3] == cfg["user"] {
					password = parts[4]
				}
			}
			_ = f.Close()
		}
	}
	failed := fmt.Sprintf("psql: error: connection to server at %q, port %s failed: ", cfg["host"], cfg["port"])
	var remote *Host
	if h.Network != nil {
		remote = h.Network(net.JoinHostPort(cfg["host"], cfg["port"]))
	}
	if remote == nil {
		return nil, nil, failed + "Connection refused"
	}
	unlock := func() {}
	if remote != h {
		remote.mu.Lock()
		unlock = remote.mu.Unlock
	}
	p := remote.Postgres
	if p == nil || p.Down {
		unlock()
		return nil, nil, failed + "Connection refused"
	}
	if slices.Contains(p.HBADeny, h.Address) {
		unlock()
		return nil, nil, failed + fmt.Sprintf("FATAL:  no pg_hba.conf entry for host %q, user %q, database %q, no encryption", h.Address, cfg["user"], cfg["dbname"])
	}
	if password == "" {
		unlock()
		return nil, nil, failed + "fe_sendauth: no password supplied"
	}
	j := p.role(cfg["user"])
	if j < 0 || p.Roles[j].NoLogin || !p.Roles[j].LogsIn(password, time.Now()) {
		unlock()
		return nil, nil, failed + fmt.Sprintf("FATAL:  password authentication failed for user %q", cfg["user"])
	}
	if cfg["dbname"] != "postgres" && p.database(cfg["dbname"]) < 0 {
		unlock()
		return nil, nil, failed + fmt.Sprintf("FATAL:  database %q does not exist", cfg["dbname"])
	}
	return &pgConn{p: p, db: cfg["dbname"], user: cfg["user"], unlock: unlock}, rest, ""
}

// pgRemoteQuery speelt psql -XAtq ... -d <conninfo> -c <query> na.
func (h *Host) pgRemoteQuery(args []string) ([]byte, error) {
	c, rest, msg := h.pgConnect(args)
	if msg != "" {
		return []byte(msg + "\n"), errors.New("exit status 2")
	}
	defer c.unlock()
	i := slices.Index(rest, "-c")
	if i < 0 || i+1 >= len(rest) {
		return []byte("agenttest: psql zonder -c\n"), errors.New("exit status 2")
	}
	q := rest[i+1]
	if isProbe(q) {
		if c.db != protocol.DBProbeDatabase {
			return []byte("ERROR:  relation \"writes\" does not exist\n"), errExit1
		}
		prows, failed := c.p.pgProbe(q)
		if failed != nil {
			return failed, errExit1
		}
		recs := make([]string, len(prows))
		for i, r := range prows {
			recs[i] = strings.Join(r, dbsql.PGFieldSep)
		}
		return []byte(strings.Join(recs, dbsql.PGRecordSep) + "\n"), nil
	}
	rows, ok := c.p.pgQuery(c.db, q)
	if !ok {
		return []byte("ERROR:  agenttest kent deze query niet: " + rest[i+1] + "\n"), errExit1
	}
	recs := make([]string, len(rows))
	for i, r := range rows {
		recs[i] = strings.Join(r, dbsql.PGFieldSep)
	}
	return []byte(strings.Join(recs, dbsql.PGRecordSep) + "\n"), nil
}

// pgRemoteWrite speelt psql -X -q -v ON_ERROR_STOP=1 -d <conninfo> met
// opdrachten op stdin na. Alleen een superuser mag ze.
func (h *Host) pgRemoteWrite(args []string, stdin string) ([]byte, error) {
	c, _, msg := h.pgConnect(args)
	if msg != "" {
		return []byte(msg + "\n"), errors.New("exit status 2")
	}
	defer c.unlock()
	if !c.p.Roles[c.p.role(c.user)].Super {
		return []byte("psql:<stdin>:1: ERROR:  permission denied to create database\n"), errors.New("exit status 3")
	}
	return c.p.pgWrite(stdin, c.user)
}

// pgDump speelt runuser -u postgres -- pg_dump ... <database> na: per
// tabel de kop die pg_dump schrijft, opvulling naar de grootte van de
// database (hoogstens 256 KiB), en de slotregel met \unrestrict.
func (h *Host) pgDump(args []string) ([]byte, error) {
	if len(args) < 5 || !slices.Equal(args[:4], []string{"-u", "postgres", "--", "pg_dump"}) {
		return nil, fmt.Errorf("runuser %s", strings.Join(args, " "))
	}
	p := h.Postgres
	if p == nil || p.Down {
		return nil, errors.New("pg_dump: error: connection to server on socket \"/var/run/postgresql/.s.PGSQL.5432\" failed: No such file or directory")
	}
	db := args[len(args)-1]
	i := p.database(db)
	if i < 0 {
		return nil, fmt.Errorf("pg_dump: error: connection to server on socket \"/var/run/postgresql/.s.PGSQL.5432\" failed: FATAL:  database %q does not exist", db)
	}
	return []byte(p.dumpOf(p.Databases[i], true, true)), nil
}

// dumpOf is wat pg_dump van d schrijft: per tabel de kop, met de eigenaar
// en de rechten als owner en privileges, de extensies, opvulling naar de
// grootte van de database (hoogstens 256 KiB), en de slotregel met
// \unrestrict.
func (p *Postgres) dumpOf(d Database, owner, privileges bool) string {
	key := strings.Repeat("k", 64)
	var b strings.Builder
	fmt.Fprintf(&b, "--\n-- PostgreSQL database dump\n--\n\n\\restrict %s\n\n-- Dumped from database version %s\n", key, p.Version)
	for _, e := range d.Extensions {
		fmt.Fprintf(&b, "\nCREATE EXTENSION IF NOT EXISTS %s WITH SCHEMA public;\n", e)
	}
	shown := "-"
	if owner {
		shown = cmp.Or(d.Owner, "postgres")
	}
	for t := range d.Tables {
		fmt.Fprintf(&b, "\n--\n-- Name: t%d; Type: TABLE; Schema: public; Owner: %s\n--\n\nCREATE TABLE public.t%d (id integer NOT NULL);\n", t+1, shown, t+1)
		if owner {
			fmt.Fprintf(&b, "ALTER TABLE public.t%d OWNER TO %s;\n", t+1, shown)
		}
		if privileges {
			for _, g := range d.Grantees {
				fmt.Fprintf(&b, "GRANT SELECT ON TABLE public.t%d TO %s;\n", t+1, g)
			}
		}
	}
	fmt.Fprintf(&b, "-- agenttest: rows=%d views=%d routines=%d triggers=%d events=%d\n", d.rows(), d.Views, d.Routines, d.Triggers, d.Events)
	if pad := min(d.Size/4096, 256<<10); pad > 0 {
		fmt.Fprintf(&b, "-- %s\n", strings.Repeat("x", int(pad)))
	}
	if !p.DumpIncomplete {
		fmt.Fprintf(&b, "\n--\n-- PostgreSQL database dump complete\n--\n\n\\unrestrict %s\n\n", key)
	}
	return b.String()
}

// pgRestore speelt psql -d <conninfo> met een dump en dan
// dbsql.PGRestoreFinish op stdin na: de database krijgt de tabellen uit de
// dump. Zonder PGRestoreFinish aan het eind mislukt het, zodat een test
// ziet als de agent het vergeet.
func (h *Host) pgRestore(args []string, dump []byte) error {
	c, _, msg := h.pgConnect(args)
	if msg != "" {
		return errors.New(msg)
	}
	defer c.unlock()
	i := c.p.database(c.db)
	if i < 0 || c.db == "postgres" {
		return fmt.Errorf("psql: agenttest: terugzetten in %s", c.db)
	}
	if !strings.HasSuffix(string(dump), dbsql.PGRestoreFinish) {
		return errors.New("psql: agenttest: de dump eindigt niet met REASSIGN OWNED")
	}
	if !strings.Contains(string(dump), "\\unrestrict ") {
		return errors.New("psql:<stdin>:1: agenttest: dump zonder slotregel")
	}
	d := &c.p.Databases[i]
	d.Tables = len(pgTableRe.FindAllString(string(dump), -1))
	d.Size = int64(len(dump))
	if r := metaRe.FindStringSubmatch(string(dump)); r != nil {
		d.Rows, _ = strconv.ParseInt(r[1], 10, 64)
		d.Views, d.Routines, d.Triggers, d.Events = atoi(r[2]), atoi(r[3]), atoi(r[4]), atoi(r[5])
	}
	return nil
}
