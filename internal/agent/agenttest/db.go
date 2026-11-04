package agenttest

// De databases van een nagespeelde machine: MariaDB en PostgreSQL die
// antwoorden op de vaste query's van de agent uit dbsql, zoals mariadb -B
// en psql -At dat zouden doen. Een andere query geeft een fout, zodat een
// test meteen ziet als de agent iets nieuws vraagt.

import (
	"cmp"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
)

// MariaDB is een nagespeelde MariaDB-server. Down: hij antwoordt niet op
// de socket.
type MariaDB struct {
	Version        string
	Down           bool
	Connections    int
	MaxConnections int
	Uptime         int64
	ReadOnly       bool
	Galera         *Galera
	Source         *MariaSource
	// Replicas zijn de adressen (host:poort) van replica's die de binlog
	// lezen.
	Replicas  []string
	Databases []Database
	Clients   []Client
	// Recovered is wat galera_recovery vindt als MariaDB stilstaat, zoals
	// uuid:seqno; leeg laat galera_recovery mislukken.
	Recovered string
	// NoGaleraScripts: galera_recovery en galera_new_cluster ontbreken,
	// zoals op MariaDB 12.3.2 zonder het pakket mariadb-server-galera.
	// mariadbd --wsrep-recover vindt dan Recovered.
	NoGaleraScripts bool
	// Users zijn de gebruikers met hun rechten. De opdrachten van de agent
	// via stdin passen ze aan, net als Databases.
	Users []MariaUser
	// DumpIncomplete laat mariadb-dump stoppen zonder de slotregel.
	DumpIncomplete bool
	// Port is waar MariaDB luistert; 0 is 3306.
	Port int
	// ImportError laat mariadb met een dump op stdin met deze fout stoppen,
	// zoals een oudere MariaDB bij iets uit een nieuwere.
	ImportError string
	// DropFails laat zoveel keer DROP DATABASE mislukken, zoals een grote
	// database weghalen die langer duurt dan het verzoek mag.
	DropFails int
	// Questions en BufferPool zijn voor de metingen: het aantal query's
	// en de grootte van de buffer pool in bytes, waarvan BufferPoolData
	// in gebruik.
	Questions                  int64
	BufferPool, BufferPoolData int64
	// Probe is de tabel van de schrijftest; de nodes van één Galera-cluster
	// delen hem. Nil: de database clusterforge_probe bestaat niet.
	Probe *ProbeTable
}

// MariaUser is een gebruiker op één host. Password is het wachtwoord in
// klare tekst, zodat een test kan zien welk wachtwoord hij kreeg; Hash is
// de hash als hij die bij een verhuizing kreeg. Plugin is leeg voor een
// gewoon wachtwoord. Grants zijn de rechten per database zoals in de
// GRANT, met \_ voor _.
type MariaUser struct {
	User, Host string
	Password   string
	Hash       string
	Plugin     string
	Global     []string
	Grants     map[string][]string
}

// NativeHash is de hash van mysql_native_password: * en SHA1(SHA1(pw)) in
// hex.
func NativeHash(password string) string {
	a := sha1.Sum([]byte(password)) //nolint:gosec // zo rekent MariaDB
	b := sha1.Sum(a[:])             //nolint:gosec // zo rekent MariaDB
	return "*" + strings.ToUpper(hex.EncodeToString(b[:]))
}

// hash is de hash die MariaDB van u bewaart.
func (u MariaUser) hash() string {
	switch {
	case u.Hash != "":
		return u.Hash
	case u.Password != "":
		return NativeHash(u.Password)
	}
	return ""
}

// LogsIn zegt of u met password binnenkomt.
func (u MariaUser) LogsIn(password string) bool {
	return u.Plugin == "" && password != "" && u.hash() == NativeHash(password)
}

// DebianUsers zijn de gebruikers die MariaDB op Debian zelf heeft.
func DebianUsers() []MariaUser {
	return []MariaUser{
		{User: "mariadb.sys", Host: "localhost"},
		{User: "mysql", Host: "localhost", Global: []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "RELOAD", "SHUTDOWN"}},
		{User: "root", Host: "localhost", Global: []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "RELOAD", "SHUTDOWN"}},
	}
}

// AllPrivileges zijn de rechten op een database na GRANT ALL PRIVILEGES in
// MariaDB 11.8.
var AllPrivileges = []string{
	"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "REFERENCES", "INDEX", "ALTER", "CREATE TEMPORARY TABLES",
	"LOCK TABLES", "EXECUTE", "CREATE VIEW", "SHOW VIEW", "CREATE ROUTINE", "ALTER ROUTINE", "EVENT", "TRIGGER",
	"DELETE HISTORY", "SHOW CREATE ROUTINE",
}

// Client zijn Count verbindingen van een gebruiker vanaf een adres met een
// database. Host is een IP-adres of localhost; de nagespeelde MariaDB
// zet er zelf een poort achter, zoals in de processlist.
type Client struct {
	User, Host, Database string
	Count                int
}

// Galera is de toestand van een Galera-node.
type Galera struct {
	ClusterName string
	UUID        string
	// Status is Primary of Non-Primary.
	Status string
	Size   int
	// State is wsrep_local_state, Comment wsrep_local_state_comment.
	State   int
	Comment string
	Ready   bool
	// Members zijn de adressen in wsrep_incoming_addresses, als host:poort.
	Members []string
	// Seqno is wsrep_last_committed.
	Seqno int64
	// RecvQueue is wsrep_local_recv_queue, FlowPausedNs
	// wsrep_flow_control_paused_ns.
	RecvQueue    int
	FlowPausedNs int64
}

// MariaSource is de replicatie van een MariaDB-replica.
type MariaSource struct {
	Host    string
	IO, SQL bool
	Lag     *int64
	Error   string
}

// Postgres is een nagespeelde PostgreSQL-server. Recovery: hij is een
// replica.
type Postgres struct {
	Version        string
	Down           bool
	Recovery       bool
	Connections    int
	MaxConnections int
	Uptime         int64
	Source         *PGSource
	Replicas       []PGReplica
	Databases      []Database
	// Clients: Host is client_addr, leeg voor de socket.
	Clients []Client
	// Roles zijn de gebruikers met LOGIN. De opdrachten van de agent via
	// stdin passen ze aan, net als Databases.
	Roles []PGRole
	// DumpIncomplete laat pg_dump stoppen zonder de slotregel.
	DumpIncomplete bool
	// Transactions, CacheHits en CacheReads zijn voor de metingen, net als
	// SharedBuffers in bytes.
	Transactions, CacheHits, CacheReads, SharedBuffers int64
	// Voor een verhuizing: de adressen die pg_hba.conf niet binnenlaat, of
	// de server TLS kan, en de extensies die te installeren zijn (nil is
	// een gewone lijst met pgcrypto, pg_trgm en uuid-ossp).
	HBADeny   []string
	TLS       bool
	Available []string
	// Probe is de tabel van de schrijftest; de nodes van één cluster delen
	// hem. Een replica (Recovery) weigert er te schrijven.
	Probe *ProbeTable
}

// PGSource is de WAL-ontvanger van een replica.
type PGSource struct {
	Host, Status string
	Lag          int64
}

// PGReplica is een replica in pg_stat_replication.
type PGReplica struct {
	Address, State, Sync string
	Lag                  int64
}

// Database is een database met haar grootte en aantal tabellen, t1 tot
// tN.
type Database struct {
	Name   string
	Size   int64
	Tables int
	// Voor een verhuizing: de tekenset en collatie (leeg is utf8mb4 en
	// utf8mb4_general_ci), de rijen per tabel (0 is 1), de tabellen zonder
	// primaire sleutel, de engine per tabel (standaard InnoDB), en de
	// andere objecten.
	Charset, Collation                string
	Rows                              int64
	NoPK                              []string
	Engines                           map[string]string
	Views, Routines, Triggers, Events int
	// In PostgreSQL: de eigenaar, en de rechten per gebruiker op de
	// database (CONNECT, CREATE, TEMPORARY).
	Owner string
	ACL   map[string][]string
	// Voor een verhuizing in PostgreSQL (Charset is de encoding, leeg is
	// UTF8): de extensies, de tabellen die de eigenaar niet mag lezen, de
	// rollen met rechten op een tabel, het aantal instellingen met ALTER
	// DATABASE, en of default_transaction_read_only aan staat.
	Extensions []string
	Unreadable []string
	Grantees   []string
	Settings   int
	ReadOnly   bool
}

// Locked voert fn uit onder het slot van de host, om de databases te
// wijzigen terwijl de agent draait. Daarna kijkt keepalived opnieuw, want
// een track_script cf_db hangt af van de database.
func (h *Host) Locked(fn func()) {
	h.mu.Lock()
	fn()
	g := h.group
	h.mu.Unlock()
	if g != nil {
		g.update()
	}
}

var errExit1 = errors.New("exit status 1")

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

// batch zet waarden om zoals mariadb -B.
func batch(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`).Replace(s)
}

// mariaExec speelt mariadb [-N] -B -e <query> na.
func (h *Host) mariaExec(args []string) ([]byte, error) {
	m := h.MariaDB
	if m == nil || m.Down {
		return []byte("ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)\n"), errExit1
	}
	if cfg, rest, ok := clientConfig(args); ok {
		return h.remoteExec(cfg, rest)
	}
	i := slices.Index(args, "-e")
	if i < 0 || i+1 >= len(args) {
		return nil, fmt.Errorf("mariadb %s", strings.Join(args, " "))
	}
	header := !slices.Contains(args, "-N")
	var rows [][]string
	switch q := args[i+1]; q {
	case dbsql.MariaVariables:
		rows = [][]string{
			{"max_connections", strconv.Itoa(m.MaxConnections)}, {"read_only", onOff(m.ReadOnly)}, {"version", m.Version},
			{"wsrep_cluster_name", ""}, {"wsrep_on", onOff(m.Galera != nil)},
		}
		if m.Galera != nil {
			rows[3][1] = m.Galera.ClusterName
		}
		rows = append(rows, []string{"port", strconv.Itoa(cmp.Or(m.Port, 3306))})
	case dbsql.MariaStatus:
		rows = [][]string{{"Threads_connected", strconv.Itoa(m.Connections)}, {"Uptime", strconv.FormatInt(m.Uptime, 10)}}
		if g := m.Galera; g != nil {
			rows = append(rows,
				[]string{"wsrep_cluster_size", strconv.Itoa(g.Size)}, []string{"wsrep_cluster_state_uuid", g.UUID},
				[]string{"wsrep_cluster_status", g.Status}, []string{"wsrep_incoming_addresses", strings.Join(g.Members, ",")},
				[]string{"wsrep_local_state", strconv.Itoa(g.State)}, []string{"wsrep_local_state_comment", g.Comment},
				[]string{"wsrep_ready", onOff(g.Ready)})
		}
	case dbsql.MariaPosition:
		if g := m.Galera; g != nil {
			rows = [][]string{
				{"Uptime", strconv.FormatInt(m.Uptime, 10)},
				{"wsrep_cluster_size", strconv.Itoa(g.Size)}, {"wsrep_cluster_state_uuid", g.UUID}, {"wsrep_cluster_status", g.Status},
				{"wsrep_last_committed", strconv.FormatInt(g.Seqno, 10)}, {"wsrep_local_state", strconv.Itoa(g.State)},
				{"wsrep_local_state_comment", g.Comment}, {"wsrep_ready", onOff(g.Ready)},
			}
		}
	case dbsql.MariaMetrics:
		rows = [][]string{
			{"threads_connected", strconv.Itoa(m.Connections)}, {"questions", strconv.FormatInt(m.Questions, 10)},
			{"innodb_buffer_pool_bytes_data", strconv.FormatInt(m.BufferPoolData, 10)},
		}
		if g := m.Galera; g != nil {
			rows = append(rows, []string{"wsrep_local_recv_queue", strconv.Itoa(g.RecvQueue)},
				[]string{"wsrep_flow_control_paused_ns", strconv.FormatInt(g.FlowPausedNs, 10)},
				[]string{"wsrep_local_state", strconv.Itoa(g.State)}, []string{"wsrep_cluster_size", strconv.Itoa(g.Size)})
		}
		rows = append(rows, []string{"max_connections", strconv.Itoa(m.MaxConnections)}, []string{"innodb_buffer_pool_size", strconv.FormatInt(m.BufferPool, 10)})
	case dbsql.MariaSource:
		if s := m.Source; s != nil {
			yes := map[bool]string{true: "Yes", false: "No"}
			lag := "NULL"
			if s.Lag != nil {
				lag = strconv.FormatInt(*s.Lag, 10)
			}
			if !header {
				return nil, errors.New("agenttest: SHOW ALL SLAVES STATUS zonder kolomnamen")
			}
			rows = [][]string{
				{"Connection_name", "Master_Host", "Slave_IO_Running", "Slave_SQL_Running", "Last_Error", "Seconds_Behind_Master", "Last_IO_Error"},
				{"", s.Host, yes[s.IO], yes[s.SQL], s.Error, lag, ""},
			}
		}
		header = false
	case dbsql.MariaReplicas:
		for _, r := range m.Replicas {
			rows = append(rows, []string{r})
		}
	case dbsql.MariaDatabases:
		for _, d := range m.Databases {
			rows = append(rows, []string{d.Name, strconv.FormatInt(d.Size, 10), strconv.Itoa(d.Tables)})
		}
	case dbsql.MariaClients:
		port := 40000
		for _, c := range m.Clients {
			for range c.Count {
				host := c.Host
				if host != "localhost" {
					port++
					host += ":" + strconv.Itoa(port)
				}
				rows = append(rows, []string{c.User, host, c.Database})
			}
		}
	case dbsql.MariaUsers:
		users := slices.Clone(m.Users)
		slices.SortFunc(users, func(a, b MariaUser) int { return strings.Compare(a.User+"@"+a.Host, b.User+"@"+b.Host) })
		for _, u := range users {
			rows = append(rows, []string{u.User, u.Host})
		}
	case dbsql.MariaUserGrants:
		for _, u := range m.Users {
			rows = append(rows, []string{grantee(u), "USAGE"})
			for _, p := range u.Global {
				rows = append(rows, []string{grantee(u), p})
			}
		}
	case dbsql.MariaSchemaGrants:
		for _, u := range m.Users {
			dbs := slices.Sorted(maps.Keys(u.Grants))
			for _, db := range dbs {
				for _, p := range u.Grants[db] {
					rows = append(rows, []string{grantee(u), db, p})
				}
			}
		}
	default:
		if isProbe(q) {
			prows, failed := h.mariaProbe(q)
			if failed != nil {
				return failed, errExit1
			}
			rows = prows
			break
		}
		if mrows, failed, ok := m.migrateQuery(q); ok {
			if failed != nil {
				return failed, errExit1
			}
			rows = mrows
			break
		}
		r := dumpInfoRe.FindStringSubmatch(q)
		if r == nil {
			return []byte("ERROR 1064 (42000): agenttest kent deze query niet: " + q + "\n"), errExit1
		}
		rows = [][]string{{"0", "0", "0"}}
		if i := slices.IndexFunc(m.Databases, func(d Database) bool { return d.Name == r[1] }); i >= 0 {
			d := m.Databases[i]
			rows = [][]string{{"1", strconv.Itoa(d.Tables), strconv.FormatInt(d.Size, 10)}}
		}
	}
	if header {
		return nil, errors.New("agenttest: kolomnamen alleen bij SHOW ALL SLAVES STATUS")
	}
	var b strings.Builder
	for _, r := range rows {
		for j, f := range r {
			if j > 0 {
				b.WriteByte('\t')
			}
			b.WriteString(batch(f))
		}
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

func grantee(u MariaUser) string { return "'" + u.User + "'@'" + u.Host + "'" }

// De opdrachten uit dbsql.Ensure en dbsql.Drop.
var (
	createDBRe   = regexp.MustCompile("^CREATE DATABASE IF NOT EXISTS `([^`]+)` CHARACTER SET utf8mb4;$")
	createUserRe = regexp.MustCompile(`^CREATE USER IF NOT EXISTS '([^']*)'@'([^']*)'(?: IDENTIFIED BY '([^']*)')?;$`)
	alterUserRe  = regexp.MustCompile(`^ALTER USER '([^']*)'@'([^']*)' IDENTIFIED BY '([^']*)';$`)
	grantRe      = regexp.MustCompile("^GRANT ALL PRIVILEGES ON `([^`]+)`\\.\\* TO '([^']*)'@'([^']*)';$")
	dropDBRe     = regexp.MustCompile("^DROP DATABASE IF EXISTS `([^`]+)`;$")
	newDBRe      = regexp.MustCompile("^CREATE DATABASE `([^`]+)` CHARACTER SET utf8mb4;$")
	// dumpInfoRe is de query uit dbsql.DumpInfo.
	dumpInfoRe = regexp.MustCompile(`^SELECT \(SELECT COUNT\(\*\) FROM information_schema\.SCHEMATA WHERE SCHEMA_NAME = '([a-z0-9_]+)'\)`)
	dropUserRe = regexp.MustCompile(`^DROP USER IF EXISTS '([^']*)'@'([^']*)';$`)
)

// mariaWrite speelt mariadb --batch met opdrachten op stdin na, een per
// regel, tot de eerste fout. Een Galera-node die niet Synced is in een
// Primary component weigert, zoals MariaDB.
func (h *Host) mariaWrite(stdin string) ([]byte, error) {
	m := h.MariaDB
	if m == nil || m.Down {
		return []byte("ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)\n"), errExit1
	}
	if g := m.Galera; g != nil && (g.Status != "Primary" || g.State != 4 || !g.Ready) {
		return []byte("ERROR 1047 (08S01) at line 1: WSREP has not yet prepared node for application use\n"), errExit1
	}
	user := func(name, host string) int {
		return slices.IndexFunc(m.Users, func(u MariaUser) bool { return u.User == name && u.Host == host })
	}
	for i, line := range strings.Split(strings.TrimRight(stdin, "\n"), "\n") {
		fail := func(code, msg string) ([]byte, error) {
			return []byte(fmt.Sprintf("ERROR %s at line %d: %s\n", code, i+1, msg)), errExit1
		}
		switch {
		case createDBRe.MatchString(line):
			name := createDBRe.FindStringSubmatch(line)[1]
			if !slices.ContainsFunc(m.Databases, func(d Database) bool { return d.Name == name }) {
				m.Databases = append(m.Databases, Database{Name: name})
				slices.SortFunc(m.Databases, func(a, b Database) int { return strings.Compare(a.Name, b.Name) })
			}
		case createUserRe.MatchString(line):
			r := createUserRe.FindStringSubmatch(line)
			if user(r[1], r[2]) < 0 {
				m.Users = append(m.Users, MariaUser{User: r[1], Host: r[2], Password: r[3]})
			}
		case alterUserRe.MatchString(line):
			r := alterUserRe.FindStringSubmatch(line)
			j := user(r[1], r[2])
			if j < 0 {
				return fail("1396 (HY000)", fmt.Sprintf("Operation ALTER USER failed for '%s'@'%s'", r[1], r[2]))
			}
			m.Users[j].Password = r[3]
		case grantRe.MatchString(line):
			r := grantRe.FindStringSubmatch(line)
			j := user(r[2], r[3])
			if j < 0 {
				return fail("1133 (28000)", "Can't find any matching row in the user table")
			}
			if m.Users[j].Grants == nil {
				m.Users[j].Grants = map[string][]string{}
			}
			m.Users[j].Grants[r[1]] = slices.Clone(AllPrivileges)
		case newDBRe.MatchString(line):
			name := newDBRe.FindStringSubmatch(line)[1]
			if slices.ContainsFunc(m.Databases, func(d Database) bool { return d.Name == name }) {
				return fail("1007 (HY000)", fmt.Sprintf("Can't create database '%s'; database exists", name))
			}
			m.Databases = append(m.Databases, Database{Name: name})
			slices.SortFunc(m.Databases, func(a, b Database) int { return strings.Compare(a.Name, b.Name) })
		case dropDBRe.MatchString(line) && m.DropFails > 0:
			m.DropFails--
			return fail("1205 (HY000)", "Lock wait timeout exceeded; try restarting transaction")
		case dropDBRe.MatchString(line):
			name := dropDBRe.FindStringSubmatch(line)[1]
			m.Databases = slices.DeleteFunc(m.Databases, func(d Database) bool { return d.Name == name })
		case dropUserRe.MatchString(line):
			r := dropUserRe.FindStringSubmatch(line)
			m.Users = slices.DeleteFunc(m.Users, func(u MariaUser) bool { return u.User == r[1] && u.Host == r[2] })
		default:
			if out, err, ok := m.migrateWrite(line, fail); ok {
				if err != nil {
					return out, err
				}
				continue
			}
			return fail("1064 (42000)", "agenttest kent deze opdracht niet")
		}
	}
	return nil, nil
}

// galeraRecovery speelt galera_recovery na: alleen als MariaDB stilstaat.
func (h *Host) galeraRecovery() ([]byte, error) {
	m := h.MariaDB
	if m == nil {
		return []byte("sh: 1: galera_recovery: not found\n"), errors.New("exit status 127")
	}
	if m.NoGaleraScripts {
		return nil, &exec.Error{Name: "galera_recovery", Err: exec.ErrNotFound}
	}
	if u := h.units["mariadb"]; !m.Down || u != nil && (u.Active || u.Activating) {
		return []byte("WSREP: Failed to start mysqld for wsrep recovery: the data directory is in use\n"), errExit1
	}
	if m.Recovered == "" {
		return []byte("WSREP: Failed to recover position: InnoDB has no position\n"), errExit1
	}
	return []byte("WSREP: Recovered position " + m.Recovered + "\n--wsrep_start_position=" + m.Recovered + "\n"), nil
}

// wsrepRecover speelt mariadbd --user=<u> --wsrep-recover --disable-log-error
// na, met de log op stderr zoals mariadbd hem schrijft.
func (h *Host) wsrepRecover(args []string) ([]byte, error) {
	m := h.MariaDB
	if m == nil {
		return nil, &exec.Error{Name: "mariadbd", Err: exec.ErrNotFound}
	}
	if !slices.Contains(args, "--wsrep-recover") || !slices.Contains(args, "--disable-log-error") ||
		!slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--user=") }) {
		return nil, fmt.Errorf("mariadbd %s", strings.Join(args, " "))
	}
	const ts = "2026-10-19 18:20:01 0 "
	if u := h.units["mariadb"]; !m.Down || u != nil && (u.Active || u.Activating) {
		return []byte(ts + "[ERROR] mariadbd: Can't lock aria control file '/var/lib/mysql/aria_log_control' for exclusive use, error: 11. Will retry for 30 seconds\n" +
			ts + "[ERROR] Aborting\n"), errExit1
	}
	if m.Recovered == "" {
		return []byte(ts + "[Note] InnoDB: Completed initialization of buffer pool\n" + ts + "[Note] Server socket created on IP: '0.0.0.0'.\n"), nil
	}
	return []byte(ts + "[Note] InnoDB: Completed initialization of buffer pool\n" +
		ts + "[Note] WSREP: Recovered position: " + m.Recovered + "\n"), nil
}

// commandV speelt sh -c 'command -v <naam>' na voor de scripts van Galera;
// elk ander programma is er.
func (h *Host) commandV(name string) ([]byte, error) {
	switch name {
	case "galera_recovery", "galera_new_cluster":
		if h.MariaDB == nil || h.MariaDB.NoGaleraScripts {
			return nil, errors.New("exit status 127")
		}
		return []byte("/usr/bin/" + name + "\n"), nil
	}
	return []byte("/usr/bin/" + name + "\n"), nil
}

// runuser speelt runuser -u postgres -- psql -XAtq -F <fs> -R <rs> [-d db]
// -c <query> na.
func (h *Host) runuser(args []string) ([]byte, error) {
	if len(args) < 4 || args[0] != "-u" || args[1] != "postgres" || args[2] != "--" || args[3] != "psql" {
		return nil, fmt.Errorf("runuser %s", strings.Join(args, " "))
	}
	p := h.Postgres
	if p == nil || p.Down {
		return []byte("psql: error: connection to server on socket \"/var/run/postgresql/.s.PGSQL.5432\" failed: No such file or directory\n"),
			errors.New("exit status 2")
	}
	opt := func(name string) string {
		if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	if opt("-F") != dbsql.PGFieldSep || opt("-R") != dbsql.PGRecordSep {
		return nil, errors.New("agenttest: psql zonder de scheidingstekens van dbsql")
	}
	db := opt("-d")
	if db != "" && !slices.ContainsFunc(p.Databases, func(d Database) bool { return d.Name == db }) && db != "postgres" {
		return []byte(fmt.Sprintf("psql: error: connection to server on socket \"/var/run/postgresql/.s.PGSQL.5432\" failed: FATAL:  database %q does not exist\n", db)),
			errors.New("exit status 2")
	}
	t := map[bool]string{true: "t", false: "f"}
	var rows [][]string
	switch q := opt("-c"); q {
	case dbsql.PGInfo:
		rows = [][]string{{p.Version, t[p.Recovery], strconv.Itoa(p.MaxConnections), strconv.Itoa(p.Connections), strconv.FormatInt(p.Uptime, 10), "off"}}
	case dbsql.PGDatabases:
		rows = append(rows, []string{"postgres", "7520783"})
		for _, d := range p.Databases {
			rows = append(rows, []string{d.Name, strconv.FormatInt(d.Size, 10)})
		}
		slices.SortFunc(rows, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	case dbsql.PGTables:
		n := 0
		for _, d := range p.Databases {
			if d.Name == db {
				n = d.Tables
			}
		}
		rows = [][]string{{strconv.Itoa(n)}}
	case dbsql.PGReplicas:
		if p.Recovery {
			return []byte("ERROR:  recovery is in progress\n"), errExit1
		}
		for _, r := range p.Replicas {
			rows = append(rows, []string{r.Address, r.State, r.Sync, strconv.FormatInt(r.Lag, 10)})
		}
	case dbsql.PGSource:
		if s := p.Source; s != nil && p.Recovery {
			rows = [][]string{{s.Host, s.Status, strconv.FormatInt(s.Lag, 10)}}
		}
	case dbsql.PGMetrics:
		lag := int64(-1)
		if s := p.Source; s != nil && p.Recovery {
			lag = s.Lag
		}
		rows = [][]string{{strconv.Itoa(p.Connections), strconv.Itoa(p.MaxConnections), strconv.FormatInt(p.Transactions, 10),
			strconv.FormatInt(p.CacheHits, 10), strconv.FormatInt(p.CacheReads, 10), strconv.FormatInt(p.SharedBuffers, 10), t[p.Recovery],
			strconv.FormatInt(lag, 10)}}
	case dbsql.PGClients:
		for _, c := range p.Clients {
			rows = append(rows, []string{c.User, c.Host, c.Database, strconv.Itoa(c.Count)})
		}
	default:
		var ok bool
		if rows, ok = p.pgQuery(db, q); !ok {
			return []byte("ERROR:  agenttest kent deze query niet: " + q + "\n"), errExit1
		}
	}
	if len(rows) == 0 {
		return nil, nil
	}
	recs := make([]string, len(rows))
	for i, r := range rows {
		recs[i] = strings.Join(r, dbsql.PGFieldSep)
	}
	return []byte(strings.Join(recs, dbsql.PGRecordSep) + "\n"), nil
}

// mariaDump speelt mariadb-dump <database> na: per tabel de kop die
// mariadb-dump schrijft met een rij, opvulling naar de grootte van de
// database (hoogstens 256 KiB), en de slotregel.
func (h *Host) mariaDump(args []string) ([]byte, error) {
	m := h.MariaDB
	if m == nil || m.Down {
		return nil, errors.New("mariadb-dump: Got error: 2002: \"Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)\" when trying to connect")
	}
	db := args[len(args)-1]
	i := slices.IndexFunc(m.Databases, func(d Database) bool { return d.Name == db })
	if i < 0 {
		return nil, fmt.Errorf("mariadb-dump: Got error: 1049: \"Unknown database '%s'\" when selecting the database", db)
	}
	d := m.Databases[i]
	var b strings.Builder
	fmt.Fprintf(&b, "-- MariaDB dump 10.19-%s, for debian-linux-gnu (x86_64)\n--\n-- Host: localhost    Database: %s\n", m.Version, db)
	for t := range d.Tables {
		fmt.Fprintf(&b, "\n--\n-- Table structure for table `t%d`\n--\n\nDROP TABLE IF EXISTS `t%d`;\n", t+1, t+1)
		fmt.Fprintf(&b, "CREATE TABLE `t%d` (`id` int NOT NULL, PRIMARY KEY (`id`));\nINSERT INTO `t%d` VALUES (1);\n", t+1, t+1)
	}
	fmt.Fprintf(&b, "-- agenttest: rows=%d views=%d routines=%d triggers=%d events=%d\n", d.rows(), d.Views, d.Routines, d.Triggers, d.Events)
	if pad := min(d.Size/4096, 256<<10); pad > 0 {
		fmt.Fprintf(&b, "-- %s\n", strings.Repeat("x", int(pad)))
	}
	if !m.DumpIncomplete {
		b.WriteString("\n-- Dump completed on 2026-10-12  2:00:00\n")
	}
	return []byte(b.String()), nil
}

// mariaRestore speelt mariadb <database> met een dump op stdin na: de
// database krijgt de tabellen uit de dump.
func (h *Host) mariaRestore(args []string, dump []byte) error {
	m := h.MariaDB
	if m == nil || m.Down {
		return errors.New("ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)")
	}
	if g := m.Galera; g != nil && (g.Status != "Primary" || g.State != 4 || !g.Ready) {
		return errors.New("ERROR 1047 (08S01) at line 1: WSREP has not yet prepared node for application use")
	}
	db := args[len(args)-1]
	i := slices.IndexFunc(m.Databases, func(d Database) bool { return d.Name == db })
	if i < 0 {
		return fmt.Errorf("ERROR 1049 (42000): Unknown database '%s'", db)
	}
	if m.ImportError != "" {
		return errors.New(m.ImportError)
	}
	m.Databases[i].Tables = strings.Count(string(dump), "-- Table structure for table `")
	m.Databases[i].Size = int64(len(dump))
	if r := metaRe.FindStringSubmatch(string(dump)); r != nil {
		d := &m.Databases[i]
		d.Rows, _ = strconv.ParseInt(r[1], 10, 64)
		d.Views, d.Routines, d.Triggers, d.Events = atoi(r[2]), atoi(r[3]), atoi(r[4]), atoi(r[5])
	}
	return nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
