package agenttest

// Een verhuizing tussen nagespeelde machines: de nieuwe machine verbindt
// met de MariaDB van een andere machine (Network), met de gebruiker en het
// wachtwoord uit het optiebestand van de agent, en die laat haar alleen
// binnen als ze een gebruiker op het adres van de nieuwe machine heeft.

import (
	"bufio"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	// De query's uit dbsql.MigrateInfo, MigrateTables, BaseTables en
	// CountRows.
	migrateInfoRe   = regexp.MustCompile(`^SELECT VERSION\(\), \(SELECT COUNT\(\*\) FROM information_schema\.SCHEMATA WHERE SCHEMA_NAME = '([a-z0-9_]+)'\)`)
	migrateTablesRe = regexp.MustCompile(`FROM information_schema\.TABLES t WHERE t\.TABLE_SCHEMA = '([a-z0-9_]+)' AND t\.TABLE_TYPE = 'BASE TABLE'`)
	baseTablesRe    = regexp.MustCompile(`^SELECT TABLE_NAME FROM information_schema\.TABLES WHERE TABLE_SCHEMA = '([a-z0-9_]+)' AND TABLE_TYPE = 'BASE TABLE'`)
	countRe         = regexp.MustCompile("\\(SELECT COUNT\\(\\*\\) FROM `([^`]+)`\\.`((?:[^`]|``)+)`\\)")
	// De opdrachten uit dbsql.MigrateGrant en CreateDatabaseAs.
	replaceUserRe = regexp.MustCompile(`^CREATE OR REPLACE USER '([^']*)'@'([^']*)' IDENTIFIED BY '([^']*)';$`)
	grantSomeRe   = regexp.MustCompile("^GRANT ([A-Z ,]+) ON `([^`]+)`\\.\\* TO '([^']*)'@'([^']*)';$")
	grantProcRe   = regexp.MustCompile("^GRANT SELECT ON `mysql`\\.`proc` TO '([^']*)'@'([^']*)';$")
	newDBAsRe     = regexp.MustCompile("^CREATE DATABASE `([^`]+)` CHARACTER SET ([a-z0-9_]+)(?: COLLATE ([a-z0-9_]+))?;$")
	// De query uit dbsql.MigrateAccounts en de opdrachten uit MigrateAdopt
	// en MigrateLock.
	accountsRe    = regexp.MustCompile(`^SELECT u\.User, u\.Host, u\.plugin, ('{2}|u\.authentication_string), .* WHERE s\.TABLE_SCHEMA IN \('([a-z0-9_]+)', `)
	createViaRe   = regexp.MustCompile(`^CREATE USER IF NOT EXISTS '([^']*)'@'([^']*)' IDENTIFIED VIA mysql_native_password USING '([^']*)';$`)
	alterViaRe    = regexp.MustCompile(`^ALTER USER '([^']*)'@'([^']*)' IDENTIFIED VIA mysql_native_password USING '([^']*)';$`)
	revokeAllOnRe = regexp.MustCompile("^REVOKE ALL PRIVILEGES ON `([^`]+)`\\.\\* FROM '([^']*)'@'([^']*)';$")
	// metaRe is de regel die mariaDump voor mariaRestore schrijft.
	metaRe = regexp.MustCompile(`(?m)^-- agenttest: rows=(\d+) views=(\d+) routines=(\d+) triggers=(\d+) events=(\d+)$`)
)

// tableNames zijn de tabellen van d: t1 tot tN.
func (d Database) tableNames() []string {
	names := make([]string, d.Tables)
	for i := range names {
		names[i] = "t" + strconv.Itoa(i+1)
	}
	slices.Sort(names)
	return names
}

func (d Database) rows() int64 {
	if d.Rows == 0 {
		return 1
	}
	return d.Rows
}

func (m *MariaDB) database(name string) (int, bool) {
	i := slices.IndexFunc(m.Databases, func(d Database) bool { return d.Name == name })
	return i, i >= 0
}

// migrateQuery beantwoordt de query's van een verhuizing; ok is false voor
// een andere query.
func (m *MariaDB) migrateQuery(q string) (rows [][]string, failed []byte, ok bool) {
	switch {
	case migrateInfoRe.MatchString(q):
		name := migrateInfoRe.FindStringSubmatch(q)[1]
		i, found := m.database(name)
		if !found {
			return [][]string{{m.Version, "0", "", "", "0", "0", "0", "0"}}, nil, true
		}
		d := m.Databases[i]
		cs, coll := d.Charset, d.Collation
		if cs == "" {
			cs, coll = "utf8mb4", "utf8mb4_general_ci"
		}
		return [][]string{{m.Version, "1", cs, coll, strconv.Itoa(d.Views), strconv.Itoa(d.Routines),
			strconv.Itoa(d.Triggers), strconv.Itoa(d.Events)}}, nil, true
	case migrateTablesRe.MatchString(q):
		i, found := m.database(migrateTablesRe.FindStringSubmatch(q)[1])
		if !found {
			return nil, nil, true
		}
		d := m.Databases[i]
		names := d.tableNames()
		for _, n := range names {
			engine := "InnoDB"
			if e := d.Engines[n]; e != "" {
				engine = e
			}
			pk := "1"
			if slices.Contains(d.NoPK, n) {
				pk = "0"
			}
			rows = append(rows, []string{n, engine, strconv.FormatInt(d.rows(), 10), strconv.FormatInt(d.Size/int64(max(len(names), 1)), 10), pk})
		}
		return rows, nil, true
	case baseTablesRe.MatchString(q):
		i, found := m.database(baseTablesRe.FindStringSubmatch(q)[1])
		if !found {
			return nil, nil, true
		}
		for _, n := range m.Databases[i].tableNames() {
			rows = append(rows, []string{n})
		}
		return rows, nil, true
	case accountsRe.MatchString(q):
		r := accountsRe.FindStringSubmatch(q)
		secret, db := r[1] != "''", r[2]
		users := slices.Clone(m.Users)
		slices.SortFunc(users, func(a, b MariaUser) int { return strings.Compare(a.User+"\x00"+a.Host, b.User+"\x00"+b.Host) })
		for _, u := range users {
			for _, schema := range slices.Sorted(maps.Keys(u.Grants)) {
				if strings.ReplaceAll(schema, `\_`, "_") != db || len(u.Grants[schema]) == 0 {
					continue
				}
				plugin, hash := cmp.Or(u.Plugin, "mysql_native_password"), ""
				if secret && u.Plugin == "" {
					hash = u.hash()
				}
				privs := slices.Sorted(slices.Values(u.Grants[schema]))
				rows = append(rows, []string{u.User, u.Host, plugin, hash, schema, strings.Join(privs, ","), strconv.Itoa(len(u.Global))})
			}
		}
		return rows, nil, true
	case strings.HasPrefix(q, "SELECT (SELECT COUNT(*) FROM `"):
		var row []string
		for _, r := range countRe.FindAllStringSubmatch(q, -1) {
			i, found := m.database(r[1])
			table := strings.ReplaceAll(r[2], "``", "`")
			if !found || !slices.Contains(m.Databases[i].tableNames(), table) {
				return nil, []byte(fmt.Sprintf("ERROR 1146 (42S02) at line 1: Table '%s.%s' doesn't exist\n", r[1], table)), true
			}
			row = append(row, strconv.FormatInt(m.Databases[i].rows(), 10))
		}
		return [][]string{row}, nil, true
	}
	return nil, nil, false
}

// migrateWrite voert een opdracht van een verhuizing uit; ok is false voor
// een andere opdracht.
func (m *MariaDB) migrateWrite(line string, fail func(code, msg string) ([]byte, error)) (out []byte, err error, ok bool) {
	user := func(name, host string) int {
		return slices.IndexFunc(m.Users, func(u MariaUser) bool { return u.User == name && u.Host == host })
	}
	switch {
	case replaceUserRe.MatchString(line):
		r := replaceUserRe.FindStringSubmatch(line)
		m.Users = slices.DeleteFunc(m.Users, func(u MariaUser) bool { return u.User == r[1] && u.Host == r[2] })
		m.Users = append(m.Users, MariaUser{User: r[1], Host: r[2], Password: r[3]})
	case createViaRe.MatchString(line):
		r := createViaRe.FindStringSubmatch(line)
		if user(r[1], r[2]) < 0 {
			m.Users = append(m.Users, MariaUser{User: r[1], Host: r[2], Hash: r[3]})
		}
	case alterViaRe.MatchString(line):
		r := alterViaRe.FindStringSubmatch(line)
		j := user(r[1], r[2])
		if j < 0 {
			out, err = fail("1396 (HY000)", fmt.Sprintf("Operation ALTER USER failed for '%s'@'%s'", r[1], r[2]))
			return out, err, true
		}
		m.Users[j].Hash, m.Users[j].Password = r[3], ""
	case revokeAllOnRe.MatchString(line):
		r := revokeAllOnRe.FindStringSubmatch(line)
		j := user(r[2], r[3])
		if j < 0 || len(m.Users[j].Grants[r[1]]) == 0 {
			out, err = fail("1141 (42000)", fmt.Sprintf("There is no such grant defined for user '%s' on host '%s'", r[2], r[3]))
			return out, err, true
		}
		delete(m.Users[j].Grants, r[1])
	case grantProcRe.MatchString(line):
		r := grantProcRe.FindStringSubmatch(line)
		j := user(r[1], r[2])
		if j < 0 {
			out, err = fail("1133 (28000)", "Can't find any matching row in the user table")
			return out, err, true
		}
		if m.Users[j].Grants == nil {
			m.Users[j].Grants = map[string][]string{}
		}
		m.Users[j].Grants["mysql.proc"] = []string{"SELECT"}
	case grantSomeRe.MatchString(line):
		r := grantSomeRe.FindStringSubmatch(line)
		j := user(r[3], r[4])
		if j < 0 {
			out, err = fail("1133 (28000)", "Can't find any matching row in the user table")
			return out, err, true
		}
		if m.Users[j].Grants == nil {
			m.Users[j].Grants = map[string][]string{}
		}
		m.Users[j].Grants[r[2]] = strings.Split(r[1], ", ")
	case newDBAsRe.MatchString(line):
		r := newDBAsRe.FindStringSubmatch(line)
		if _, found := m.database(r[1]); found {
			out, err = fail("1007 (HY000)", fmt.Sprintf("Can't create database '%s'; database exists", r[1]))
			return out, err, true
		}
		m.Databases = append(m.Databases, Database{Name: r[1], Charset: r[2], Collation: r[3]})
		slices.SortFunc(m.Databases, func(a, b Database) int { return strings.Compare(a.Name, b.Name) })
	default:
		return nil, nil, false
	}
	return nil, nil, true
}

// clientConfig leest het optiebestand dat de agent als eerste optie
// meegeeft: adres, poort, gebruiker en wachtwoord van de oude MariaDB.
// rest zijn de andere opties zonder die voor TLS.
func clientConfig(args []string) (cfg map[string]string, rest []string, ok bool) {
	if len(args) == 0 || !strings.HasPrefix(args[0], "--defaults-extra-file=") {
		return nil, args, false
	}
	cfg = map[string]string{}
	if f, err := os.Open(strings.TrimPrefix(args[0], "--defaults-extra-file=")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if k, v, found := strings.Cut(sc.Text(), "="); found {
				cfg[k] = v
			}
		}
		_ = f.Close()
	}
	for _, a := range args[1:] {
		if a != "--ssl" && a != "--skip-ssl" && a != "--skip-ssl-verify-server-cert" {
			rest = append(rest, a)
		}
	}
	return cfg, rest, true
}

// login zoekt de MariaDB uit het optiebestand op het netwerk en logt in
// zoals MariaDB: een gebruiker op het adres van deze machine met dat
// wachtwoord. De andere machine is daarna op slot; unlock geeft haar vrij.
func (h *Host) login(cfg map[string]string) (remote *Host, unlock func(), errMsg string) {
	addr := net.JoinHostPort(cfg["host"], cfg["port"])
	if h.Network != nil {
		remote = h.Network(addr)
	}
	if remote == nil || remote == h {
		return nil, nil, fmt.Sprintf("ERROR 2002 (HY000): Can't connect to server on '%s' (115)", cfg["host"])
	}
	remote.mu.Lock()
	m := remote.MariaDB
	if m == nil || m.Down {
		remote.mu.Unlock()
		return nil, nil, fmt.Sprintf("ERROR 2002 (HY000): Can't connect to server on '%s' (111)", cfg["host"])
	}
	if !slices.ContainsFunc(m.Users, func(u MariaUser) bool {
		return u.User == cfg["user"] && u.Host == h.Address && u.LogsIn(cfg["password"])
	}) {
		remote.mu.Unlock()
		return nil, nil, fmt.Sprintf("ERROR 1045 (28000): Access denied for user '%s'@'%s' (using password: YES)", cfg["user"], h.Address)
	}
	return remote, remote.mu.Unlock, ""
}

// remoteExec speelt mariadb met een optiebestand na: de query op de andere
// machine.
func (h *Host) remoteExec(cfg map[string]string, args []string) ([]byte, error) {
	remote, unlock, msg := h.login(cfg)
	if msg != "" {
		return []byte(msg + "\n"), errExit1
	}
	defer unlock()
	return remote.mariaExec(args)
}

// remoteDump speelt mariadb-dump met een optiebestand na: een dump van de
// andere machine, als de gebruiker SELECT op de database heeft.
func (h *Host) remoteDump(cfg map[string]string, args []string) ([]byte, error) {
	remote, unlock, msg := h.login(cfg)
	if msg != "" {
		return nil, errors.New("mariadb-dump: Got error: " + strings.TrimPrefix(msg, "ERROR "))
	}
	defer unlock()
	db := args[len(args)-1]
	ok := slices.ContainsFunc(remote.MariaDB.Users, func(u MariaUser) bool {
		return u.User == cfg["user"] && u.Host == h.Address && slices.Contains(u.Grants[strings.ReplaceAll(db, "_", `\_`)], "SELECT")
	})
	if !ok {
		return nil, fmt.Errorf("mariadb-dump: Got error: 1044: \"Access denied for user '%s'@'%s' to database '%s'\" when selecting the database",
			cfg["user"], h.Address, db)
	}
	return remote.mariaDump(args)
}

// Dial speelt een TCP-verbinding met de MariaDB van een andere machine na:
// ze stuurt haar begroeting met de versie, zonder TLS.
func (h *Host) Dial(_ context.Context, _, addr string) (net.Conn, error) {
	h.mu.Lock()
	network := h.Network
	h.mu.Unlock()
	var remote *Host
	if network != nil {
		remote = network(addr)
	}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	if remote == nil {
		return nil, refused
	}
	remote.mu.Lock()
	m := remote.MariaDB
	version := ""
	if m != nil && !m.Down {
		version = m.Version
	}
	remote.mu.Unlock()
	if version == "" {
		return nil, refused
	}
	client, server := net.Pipe()
	go func() {
		defer func() { _ = server.Close() }()
		body := append([]byte{10}, version...)
		body = append(body, 0, 1, 0, 0, 0)
		body = append(body, "abcdefgh"...)
		body = append(body, 0)
		body = binary.LittleEndian.AppendUint16(body, 0xf7fe)
		head := []byte{byte(len(body)), byte(len(body) >> 8), byte(len(body) >> 16), 0}
		_, _ = server.Write(append(head, body...))
	}()
	return client, nil
}
