package agenttest

// Een verhuizing tussen nagespeelde PostgreSQL-servers: de query's en
// opdrachten uit dbsql/pgmigrate.go, pg_dump over het netwerk met de
// tijdelijke gebruiker uit de passfile, en psql dat de dump als postgres in
// de nieuwe database zet.

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var (
	pgMigrateInfoRe = regexp.MustCompile(`^SELECT current_setting\('server_version'\), count\(d\.oid\), .* WHERE d\.datname = '([a-z0-9_]+)'$`)
	pgCountRe       = regexp.MustCompile(`\(SELECT count\(\*\) FROM "((?:[^"]|"")+)"\."((?:[^"]|"")+)"\)`)
	// De opdrachten uit dbsql.PGMigrateGrant, PGMigrateAdopt,
	// PGMigrateMembers, PGMigrateLock en PGMigrateCreate.
	pgMigrateExpiredRe = regexp.MustCompile(`^SELECT format\('DROP ROLE %I', rolname\) FROM pg_roles WHERE rolname ~ '\^cf_verhuizing_.*' AND rolvaliduntil < now\(\)\\gexec$`)
	pgNewRoleRe        = regexp.MustCompile(`^SELECT 'CREATE ROLE "([a-z0-9_]+)"' WHERE NOT EXISTS \(SELECT 1 FROM pg_roles WHERE rolname = '([a-z0-9_]+)'\)\\gexec$`)
	pgTempLoginRe      = regexp.MustCompile(`^SELECT format\('ALTER ROLE "([a-z0-9_]+)" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD %L VALID UNTIL %L', '([^']+)', now\(\) \+ interval '([0-9]+) hours'\)\\gexec$`)
	pgRoleReadOnlyRe   = regexp.MustCompile(`^ALTER ROLE "([a-z0-9_]+)" SET default_transaction_read_only = on;$`)
	pgGrantOwnerRe     = regexp.MustCompile(`^SELECT format\('GRANT %I TO "([a-z0-9_]+)"', r\.rolname\) FROM pg_database d JOIN pg_roles r ON r\.oid = d\.datdba WHERE d\.datname = '([a-z0-9_]+)' AND NOT r\.rolsuper\\gexec$`)
	pgLoginRe          = regexp.MustCompile(`^ALTER ROLE "([a-z0-9_]+)" WITH LOGIN;$`)
	pgMemberRe         = regexp.MustCompile(`^SELECT format\('GRANT %I TO "([a-z0-9_]+)"', g\.rolname\) FROM pg_roles g WHERE g\.rolname = '([a-z0-9_]+)' AND NOT pg_has_role\('([a-z0-9_]+)', g\.oid, 'MEMBER'\)\\gexec$`)
	pgLockRe           = regexp.MustCompile(`^ALTER DATABASE "([a-z0-9_]+)" (SET default_transaction_read_only = on|RESET default_transaction_read_only);$`)
	pgDBGrantRe        = regexp.MustCompile(`^GRANT ((?:CONNECT|CREATE|TEMPORARY)(?:, (?:CONNECT|CREATE|TEMPORARY))*) ON DATABASE "([a-z0-9_]+)" TO "([a-z0-9_]+)";$`)
	pgMigrateCreateRe  = regexp.MustCompile(`^CREATE DATABASE "([a-z0-9_]+)"(?: OWNER "([a-z0-9_]+)")? ENCODING '([A-Z0-9_]+)' TEMPLATE template0(?: LC_COLLATE 'C' LC_CTYPE 'C')?;$`)
	// In een dump van dumpOf.
	pgAlterOwnerRe = regexp.MustCompile(`(?m)^ALTER TABLE \S+ OWNER TO (\S+);$`)
	pgGrantRe      = regexp.MustCompile(`(?m)^GRANT SELECT ON TABLE \S+ TO (\S+);$`)
	pgExtensionRe  = regexp.MustCompile(`(?m)^CREATE EXTENSION IF NOT EXISTS (\S+) WITH SCHEMA public;$`)
)

// available zijn de extensies die op p te installeren zijn.
func (p *Postgres) available() []string {
	if p.Available != nil {
		return p.Available
	}
	return []string{"pg_trgm", "pgcrypto", "plpgsql", "uuid-ossp"}
}

// owner is de eigenaar van d.
func (d Database) owner() string { return cmp.Or(d.Owner, "postgres") }

// super zegt of de rol name superuser is.
func (p *Postgres) super(name string) bool {
	j := p.role(name)
	return name == "postgres" || j >= 0 && p.Roles[j].Super
}

// pgMigrateQuery beantwoordt de query's van een verhuizing in database db.
func (p *Postgres) pgMigrateQuery(db, q string) (rows [][]string, ok bool) {
	t := map[bool]string{true: "t", false: "f"}
	i := p.database(db)
	switch {
	case pgMigrateInfoRe.MatchString(q):
		j := p.database(pgMigrateInfoRe.FindStringSubmatch(q)[1])
		if j < 0 {
			return [][]string{{p.Version, "0", "", "", "f", "0", "0"}}, true
		}
		d := p.Databases[j]
		return [][]string{{p.Version, "1", cmp.Or(d.Charset, "UTF8"), d.owner(), t[p.super(d.owner())], strconv.FormatInt(d.Size, 10),
			strconv.Itoa(d.Settings)}}, true
	case q == dbsql.PGMigrateTables:
		if i < 0 {
			return nil, true
		}
		d := p.Databases[i]
		for _, n := range d.tableNames() {
			rows = append(rows, []string{"public", n, strconv.FormatInt(d.rows(), 10), strconv.FormatInt(d.Size/int64(max(d.Tables, 1)), 10),
				t[!slices.Contains(d.NoPK, n)]})
		}
		return rows, true
	case q == dbsql.PGMigrateObjects:
		if i < 0 {
			return [][]string{{"0", "0", "0", ""}}, true
		}
		d := p.Databases[i]
		return [][]string{{strconv.Itoa(d.Views), strconv.Itoa(d.Routines), strconv.Itoa(d.Triggers), strings.Join(d.Extensions, ",")}}, true
	case q == dbsql.PGMigrateUnreadable:
		if i >= 0 {
			for _, n := range p.Databases[i].Unreadable {
				rows = append(rows, []string{n})
			}
		}
		return rows, true
	case q == dbsql.PGRoleNames:
		for _, r := range p.Roles {
			if !strings.HasPrefix(r.Name, "pg_") {
				rows = append(rows, []string{r.Name})
			}
		}
		if p.role("postgres") < 0 {
			rows = append(rows, []string{"postgres"})
		}
		return rows, true
	case q == dbsql.PGMigrateAvailable:
		for _, n := range p.available() {
			rows = append(rows, []string{n})
		}
		return rows, true
	case q == dbsql.PGMigrateAccounts(true) || q == dbsql.PGMigrateAccounts(false):
		if i < 0 {
			return nil, true
		}
		return p.accounts(p.Databases[i], q == dbsql.PGMigrateAccounts(true)), true
	case strings.HasPrefix(q, `SELECT (SELECT count(*) FROM "`):
		if i < 0 {
			return nil, false
		}
		var row []string
		for _, r := range pgCountRe.FindAllStringSubmatch(q, -1) {
			if r[1] != "public" || !slices.Contains(p.Databases[i].tableNames(), strings.ReplaceAll(r[2], `""`, `"`)) {
				return nil, false
			}
			row = append(row, strconv.FormatInt(p.Databases[i].rows(), 10))
		}
		return [][]string{row}, true
	}
	return nil, false
}

// accounts is het antwoord op dbsql.PGMigrateAccounts in d.
func (p *Postgres) accounts(d Database, secret bool) [][]string {
	t := map[bool]string{true: "t", false: "f"}
	names := []string{d.owner()}
	names = append(names, slices.Collect(maps.Keys(d.ACL))...)
	names = append(names, d.Grantees...)
	// En wie lid is van een van die rollen, ook via een andere.
	for grew := true; grew; {
		grew = false
		for _, r := range p.Roles {
			if !slices.Contains(names, r.Name) && slices.ContainsFunc(r.MemberOf, func(g string) bool { return slices.Contains(names, g) }) {
				names, grew = append(names, r.Name), true
			}
		}
	}
	slices.Sort(names)
	var rows [][]string
	for _, n := range slices.Compact(names) {
		j := p.role(n)
		if j < 0 || strings.HasPrefix(n, "pg_") || strings.HasPrefix(n, protocol.DBMigrateUserPrefix) {
			continue
		}
		r := p.Roles[j]
		plugin := "other"
		switch {
		case r.NoLogin:
			plugin = "nologin"
		case r.Verifier == "":
			plugin = "none"
		case strings.HasPrefix(r.Verifier, "SCRAM-SHA-256$"):
			plugin = "scram-sha-256"
		case strings.HasPrefix(r.Verifier, "md5"):
			plugin = "md5"
		}
		hash := ""
		if secret {
			hash = r.Verifier
		}
		acl := slices.Sorted(slices.Values(d.ACL[n]))
		member := slices.DeleteFunc(slices.Sorted(slices.Values(r.MemberOf)), func(g string) bool { return !nameRe.MatchString(g) })
		objects := slices.Contains(d.Grantees, n) || n == d.owner() && d.Tables > 0
		rows = append(rows, []string{n, plugin, hash, t[r.Super || n == "postgres"], t[n == d.owner()], strings.Join(acl, ","), t[objects],
			strings.Join(member, ","), t[r.CreateDB || r.CreateRole || r.Replicat]})
	}
	return rows
}

var nameRe = regexp.MustCompile(protocol.DBNamePattern)

// pgMigrateWrite voert een opdracht van een verhuizing uit: ok is false
// voor een andere opdracht, en msg is de fout van PostgreSQL.
func (p *Postgres) pgMigrateWrite(line string) (msg string, ok bool) {
	switch {
	case pgMigrateExpiredRe.MatchString(line):
		now := time.Now()
		p.Roles = slices.DeleteFunc(p.Roles, func(r PGRole) bool {
			return strings.HasPrefix(r.Name, protocol.DBMigrateUserPrefix) && !r.ValidUntil.IsZero() && r.ValidUntil.Before(now)
		})
	case pgNewRoleRe.MatchString(line):
		r := pgNewRoleRe.FindStringSubmatch(line)
		if r[1] != r[2] {
			return "agenttest: twee namen in CREATE ROLE", true
		}
		if p.role(r[1]) < 0 {
			p.Roles = append(p.Roles, PGRole{Name: r[1], NoLogin: true})
		}
	case pgTempLoginRe.MatchString(line):
		r := pgTempLoginRe.FindStringSubmatch(line)
		j := p.role(r[1])
		if j < 0 {
			return fmt.Sprintf("role %q does not exist", r[1]), true
		}
		hours, _ := strconv.Atoi(r[3])
		p.Roles[j].NoLogin, p.Roles[j].Verifier, p.Roles[j].ValidUntil = false, r[2], time.Now().Add(time.Duration(hours)*time.Hour)
	case pgRoleReadOnlyRe.MatchString(line):
		j := p.role(pgRoleReadOnlyRe.FindStringSubmatch(line)[1])
		if j < 0 {
			return "role does not exist", true
		}
		p.Roles[j].ReadOnly = true
	case pgGrantOwnerRe.MatchString(line):
		r := pgGrantOwnerRe.FindStringSubmatch(line)
		j, k := p.role(r[1]), p.database(r[2])
		if j < 0 {
			return fmt.Sprintf("role %q does not exist", r[1]), true
		}
		if k >= 0 && !p.super(p.Databases[k].owner()) && !slices.Contains(p.Roles[j].MemberOf, p.Databases[k].owner()) {
			p.Roles[j].MemberOf = append(p.Roles[j].MemberOf, p.Databases[k].owner())
		}
	case pgLoginRe.MatchString(line):
		j := p.role(pgLoginRe.FindStringSubmatch(line)[1])
		if j < 0 {
			return "role does not exist", true
		}
		p.Roles[j].NoLogin = false
	case pgMemberRe.MatchString(line):
		r := pgMemberRe.FindStringSubmatch(line)
		j := p.role(r[1])
		if j < 0 || r[1] != r[3] {
			return fmt.Sprintf("role %q does not exist", r[3]), true
		}
		if p.role(r[2]) >= 0 && !slices.Contains(p.Roles[j].MemberOf, r[2]) {
			p.Roles[j].MemberOf = append(p.Roles[j].MemberOf, r[2])
		}
	case pgLockRe.MatchString(line):
		r := pgLockRe.FindStringSubmatch(line)
		j := p.database(r[1])
		if j < 0 {
			return fmt.Sprintf("database %q does not exist", r[1]), true
		}
		p.Databases[j].ReadOnly = strings.HasPrefix(r[2], "SET")
	case pgDBGrantRe.MatchString(line):
		r := pgDBGrantRe.FindStringSubmatch(line)
		j := p.database(r[2])
		switch {
		case j < 0:
			return fmt.Sprintf("database %q does not exist", r[2]), true
		case p.role(r[3]) < 0:
			return fmt.Sprintf("role %q does not exist", r[3]), true
		}
		d := &p.Databases[j]
		if d.ACL == nil {
			d.ACL = map[string][]string{}
		}
		for _, priv := range strings.Split(r[1], ", ") {
			if !slices.Contains(d.ACL[r[3]], priv) {
				d.ACL[r[3]] = append(d.ACL[r[3]], priv)
			}
		}
	case pgMigrateCreateRe.MatchString(line):
		r := pgMigrateCreateRe.FindStringSubmatch(line)
		fail := func(m string) ([]byte, error) { return []byte(m), errors.New(m) }
		if _, err := p.createDB(r[1], r[2], fail); err != nil {
			return err.Error(), true
		}
		p.Databases[p.database(r[1])].Charset = r[3]
	default:
		return "", false
	}
	return "", true
}

// pgRemoteDump speelt pg_dump -d <conninfo> na: een dump van de andere
// machine, als de gebruiker lid is van de eigenaar van de database en die
// alles mag lezen.
func (h *Host) pgRemoteDump(args []string) ([]byte, error) {
	c, rest, msg := h.pgConnect(args)
	if msg != "" {
		return nil, errors.New(strings.Replace(msg, "psql:", "pg_dump:", 1))
	}
	defer c.unlock()
	d := c.p.Databases[c.p.database(c.db)]
	role := c.p.Roles[c.p.role(c.user)]
	switch {
	case !slices.Contains(role.MemberOf, d.owner()) && !role.Super && c.user != d.owner():
		return nil, fmt.Errorf("pg_dump: error: query failed: ERROR:  permission denied for table %s", cmp.Or(firstOf(d.tableNames()), "t1"))
	case len(d.Unreadable) > 0:
		return nil, fmt.Errorf("pg_dump: error: query failed: ERROR:  permission denied for table %s", d.Unreadable[0])
	case !slices.Contains(rest, "--no-password") || !slices.Contains(rest, "--lock-wait-timeout=5min"):
		return nil, fmt.Errorf("agenttest: pg_dump %s", strings.Join(rest, " "))
	}
	return []byte(c.p.dumpOf(d, !slices.Contains(rest, "--no-owner"), !slices.Contains(rest, "--no-privileges"))), nil
}

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// pgImport speelt runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 -d
// <database> met een dump op stdin na: de database krijgt de tabellen uit
// de dump, als elke rol en extensie die de dump noemt hier bestaat.
func (h *Host) pgImport(args []string, dump []byte) error {
	want := []string{"-u", "postgres", "--", "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d"}
	if len(args) != len(want)+1 || !slices.Equal(args[:len(want)], want) {
		return fmt.Errorf("runuser %s", strings.Join(args, " "))
	}
	p := h.Postgres
	if p == nil || p.Down {
		return errors.New("psql: error: connection to server on socket \"/var/run/postgresql/.s.PGSQL.5432\" failed: No such file or directory")
	}
	if p.Recovery {
		return errors.New("psql:<stdin>:20: ERROR:  cannot execute CREATE TABLE in a read-only transaction")
	}
	i := p.database(args[len(args)-1])
	if i < 0 {
		return fmt.Errorf("psql: error: FATAL:  database %q does not exist", args[len(args)-1])
	}
	// Een dump zonder slotregel leest psql gewoon in; de agent ziet zelf
	// dat hij niet compleet is.
	text := string(dump)
	for _, re := range []*regexp.Regexp{pgAlterOwnerRe, pgGrantRe} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if !p.super(m[1]) && p.role(m[1]) < 0 {
				return fmt.Errorf("psql:<stdin>:30: ERROR:  role %q does not exist", m[1])
			}
		}
	}
	d := &p.Databases[i]
	for _, m := range pgExtensionRe.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(p.available(), m[1]) {
			return fmt.Errorf("psql:<stdin>:12: ERROR:  extension %q is not available", m[1])
		}
		d.Extensions = append(d.Extensions, m[1])
	}
	d.Tables = len(pgTableRe.FindAllString(text, -1))
	d.Size = int64(len(dump))
	if r := metaRe.FindStringSubmatch(text); r != nil {
		d.Rows, _ = strconv.ParseInt(r[1], 10, 64)
		d.Views, d.Routines, d.Triggers, d.Events = atoi(r[2]), atoi(r[3]), atoi(r[4]), atoi(r[5])
	}
	return nil
}

// dialPG speelt een TCP-verbinding met de PostgreSQL van remote na: op een
// SSLRequest antwoordt ze S of N.
func dialPG(remote *Host) (net.Conn, error) {
	remote.mu.Lock()
	p := remote.Postgres
	up, tls := p != nil && !p.Down, p != nil && p.TLS
	remote.mu.Unlock()
	if !up {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	}
	client, server := net.Pipe()
	go func() {
		defer func() { _ = server.Close() }()
		buf := make([]byte, 8)
		if _, err := io.ReadFull(server, buf); err != nil {
			return
		}
		answer := byte('N')
		if tls {
			answer = 'S'
		}
		_, _ = server.Write([]byte{answer})
	}()
	return client, nil
}
