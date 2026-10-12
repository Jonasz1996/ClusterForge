package agenttest

// De databases van een nagespeelde machine: MariaDB en PostgreSQL die
// antwoorden op de vaste query's van de agent uit dbsql, zoals mariadb -B
// en psql -At dat zouden doen. Een andere query geeft een fout, zodat een
// test meteen ziet als de agent iets nieuws vraagt.

import (
	"errors"
	"fmt"
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

// Database is een database met haar grootte en aantal tabellen.
type Database struct {
	Name   string
	Size   int64
	Tables int
}

// Locked voert fn uit onder het slot van de host, om de databases te
// wijzigen terwijl de agent draait.
func (h *Host) Locked(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn()
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
	case dbsql.MariaStatus:
		rows = [][]string{{"Threads_connected", strconv.Itoa(m.Connections)}, {"Uptime", strconv.FormatInt(m.Uptime, 10)}}
		if g := m.Galera; g != nil {
			rows = append(rows,
				[]string{"wsrep_cluster_size", strconv.Itoa(g.Size)}, []string{"wsrep_cluster_state_uuid", g.UUID},
				[]string{"wsrep_cluster_status", g.Status}, []string{"wsrep_incoming_addresses", strings.Join(g.Members, ",")},
				[]string{"wsrep_local_state", strconv.Itoa(g.State)}, []string{"wsrep_local_state_comment", g.Comment},
				[]string{"wsrep_ready", onOff(g.Ready)})
		}
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
	default:
		return []byte("ERROR 1064 (42000): agenttest kent deze query niet: " + q + "\n"), errExit1
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
	default:
		return []byte("ERROR:  agenttest kent deze query niet: " + q + "\n"), errExit1
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
