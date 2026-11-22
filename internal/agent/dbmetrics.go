package agent

// De metingen van de databases voor de grafieken. Bij elke batch metrics
// leest de agent MariaDB en PostgreSQL op de node met één vaste query
// (dbsql.MariaMetrics en dbsql.PGMetrics) en stuurt de tellers mee als
// node_db_*, met het label engine. Eens per minuut komt de grootte per
// database erbij, met het label database. Draait er geen database, dan
// stuurt hij niets; antwoordt een database niet, dan alleen node_db_up 0.

import (
	"context"
	"strconv"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// dbMetricsTimeout is hoe lang de metingen van de databases samen mogen
// duren; een trage database houdt de metrics van de node niet op.
const dbMetricsTimeout = 5 * time.Second

const (
	// dbSizesEvery is hoe vaak de agent de grootte per database leest; bij
	// veel tabellen is die query zwaarder dan de rest.
	dbSizesEvery = time.Minute
	// dbMaxSizeSeries is het hoogste aantal databases met een eigen lijn.
	dbMaxSizeSeries = 100
)

// dbMetrics leest de databases op de node voor de grafieken; met sizes ook
// de grootte per database.
func (a *Agent) dbMetrics(ctx context.Context, sizes bool) []protocol.Sample {
	ctx, cancel := context.WithTimeout(ctx, dbMetricsTimeout)
	defer cancel()
	units, err := a.dbUnits(ctx)
	if err != nil {
		return nil
	}
	var m metricSet
	if u, ok := mariaUnit(units); ok && u.Active == "active" {
		if a.mariaMetrics(ctx, &m) && sizes {
			a.dbSizes(ctx, &m, protocol.DBMariaDB)
		}
	}
	if u, ok := pgUnit(units); ok && u.Active == "active" {
		if a.pgMetrics(ctx, &m) && sizes {
			a.dbSizes(ctx, &m, protocol.DBPostgreSQL)
		}
	}
	return m.samples
}

func (a *Agent) mariaMetrics(ctx context.Context, m *metricSet) bool {
	const engine = protocol.DBMariaDB
	rows, err := a.maria(ctx, false, dbsql.MariaMetrics)
	if err != nil {
		m.add("node_db_up", 0, "engine", engine)
		return false
	}
	m.add("node_db_up", 1, "engine", engine)
	kv := keyValues(rows)
	num := func(key string) (float64, bool) {
		v, err := strconv.ParseFloat(kv[key], 64)
		return v, err == nil
	}
	for _, x := range [][2]string{
		{"threads_connected", "node_db_connections"},
		{"max_connections", "node_db_max_connections"},
		{"questions", "node_db_queries_total"},
		{"innodb_buffer_pool_size", "node_db_buffer_pool_bytes"},
		{"innodb_buffer_pool_bytes_data", "node_db_buffer_pool_data_bytes"},
		{"wsrep_local_recv_queue", "node_db_galera_recv_queue"},
		{"wsrep_cluster_size", "node_db_galera_cluster_size"},
	} {
		if v, ok := num(x[0]); ok {
			m.add(x[1], v, "engine", engine)
		}
	}
	if v, ok := num("wsrep_flow_control_paused_ns"); ok {
		m.add("node_db_galera_flow_control_paused_seconds_total", v/1e9, "engine", engine)
	}
	if v, ok := num("wsrep_local_state"); ok {
		synced := 0.0
		if v == 4 {
			synced = 1
		}
		m.add("node_db_galera_synced", synced, "engine", engine)
	}
	return true
}

func (a *Agent) pgMetrics(ctx context.Context, m *metricSet) bool {
	const engine = protocol.DBPostgreSQL
	rows, err := a.psql(ctx, "", dbsql.PGMetrics)
	if err != nil || len(rows) != 1 || len(rows[0]) < 8 {
		m.add("node_db_up", 0, "engine", engine)
		return false
	}
	r := rows[0]
	m.add("node_db_up", 1, "engine", engine)
	for i, name := range []string{
		"node_db_connections", "node_db_max_connections", "node_db_transactions_total",
		"node_db_cache_hits_total", "node_db_cache_reads_total", "node_db_buffer_pool_bytes",
	} {
		if v, err := strconv.ParseFloat(r[i], 64); err == nil {
			m.add(name, v, "engine", engine)
		}
	}
	replica := r[6] == "t"
	primary := 1.0
	if replica {
		primary = 0
	}
	m.add("node_db_primary", primary, "engine", engine)
	if lag, err := strconv.ParseFloat(r[7], 64); err == nil && replica && lag >= 0 {
		m.add("node_db_replication_lag_seconds", lag, "engine", engine)
	}
	return true
}

// dbSizes voegt de grootte per database toe, met dezelfde query als de
// status van de database. Lukt de query niet, dan ontbreken ze deze keer.
func (a *Agent) dbSizes(ctx context.Context, m *metricSet, engine string) {
	var rows [][]string
	var err error
	if engine == protocol.DBPostgreSQL {
		rows, err = a.psql(ctx, "", dbsql.PGDatabases)
	} else {
		rows, err = a.maria(ctx, false, dbsql.MariaDatabases)
	}
	if err != nil {
		return
	}
	n := 0
	for _, r := range rows {
		if len(r) < 2 || n == dbMaxSizeSeries {
			continue
		}
		if v, err := strconv.ParseFloat(r[1], 64); err == nil {
			m.add("node_db_database_size_bytes", v, "engine", engine, "database", r[0])
			n++
		}
	}
}
