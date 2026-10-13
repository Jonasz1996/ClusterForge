package agent

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// sampleMap zet metingen om naar naam en waarde, voor één engine per test.
func sampleMap(samples []protocol.Sample) map[string]float64 {
	out := map[string]float64{}
	for _, s := range samples {
		out[s.Name] = s.Value
	}
	return out
}

// sizeMap geeft de grootte per database uit de metingen.
func sizeMap(samples []protocol.Sample) map[string]float64 {
	out := map[string]float64{}
	for _, s := range samples {
		if s.Name == "node_db_database_size_bytes" {
			out[s.Labels["database"]] = s.Value
		}
	}
	return out
}

func TestDBMetricsGalera(t *testing.T) {
	h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	h.MariaDB = &agenttest.MariaDB{
		Version: "11.8.3", Connections: 12, MaxConnections: 200, Questions: 123456, BufferPool: 1 << 30, BufferPoolData: 600 << 20,
		Galera: &agenttest.Galera{ClusterName: "nextcloud", Status: "Primary", Size: 3, State: 4, Comment: "Synced", Ready: true,
			RecvQueue: 2, FlowPausedNs: 1_500_000_000},
	}
	samples := dbAgent(h).dbMetrics(context.Background(), false)
	for _, s := range samples {
		if s.Labels["engine"] != protocol.DBMariaDB {
			t.Errorf("%s zonder engine mariadb: %v", s.Name, s.Labels)
		}
	}
	want := map[string]float64{
		"node_db_up": 1, "node_db_connections": 12, "node_db_max_connections": 200, "node_db_queries_total": 123456,
		"node_db_buffer_pool_bytes": 1 << 30, "node_db_buffer_pool_data_bytes": 600 << 20, "node_db_galera_recv_queue": 2,
		"node_db_galera_cluster_size": 3, "node_db_galera_flow_control_paused_seconds_total": 1.5, "node_db_galera_synced": 1,
	}
	if got := sampleMap(samples); !maps.Equal(got, want) {
		t.Errorf("metingen:\n%v\nwant\n%v", got, want)
	}
	if q := queries(h); !slices.Equal(q, []string{dbsql.MariaMetrics}) {
		t.Errorf("query's: %q", q)
	}

	// Eens per minuut de grootte per database.
	h.MariaDB.Databases = []agenttest.Database{{Name: "nextcloud", Size: 5 << 20, Tables: 3}, {Name: "zabbix", Size: 7 << 20, Tables: 2}}
	if got, want := sizeMap(dbAgent(h).dbMetrics(context.Background(), true)), map[string]float64{"nextcloud": 5 << 20, "zabbix": 7 << 20}; !maps.Equal(got, want) {
		t.Errorf("grootte: %v", got)
	}

	// Een node die aansluit, is niet Synced.
	h.MariaDB.Galera.State, h.MariaDB.Galera.Comment = 1, "Joining"
	if m := sampleMap(dbAgent(h).dbMetrics(context.Background(), false)); m["node_db_galera_synced"] != 0 {
		t.Errorf("joining: %v", m)
	}
	// Antwoordt MariaDB niet, dan alleen node_db_up 0.
	h.MariaDB.Down = true
	if m := sampleMap(dbAgent(h).dbMetrics(context.Background(), false)); len(m) != 1 || m["node_db_up"] != 0 {
		t.Errorf("down: %v", m)
	}
	// Staat MariaDB stil, dan niets: dan is er niets te meten.
	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: false})
	if s := dbAgent(h).dbMetrics(context.Background(), false); len(s) != 0 {
		t.Errorf("gestopt: %v", s)
	}
}

func TestDBMetricsPostgres(t *testing.T) {
	h := agenttest.NewHost(t.TempDir(), "10.0.20.187")
	h.SetUnit("patroni", agenttest.Unit{Enabled: true, Active: true})
	h.Postgres = &agenttest.Postgres{
		Version: "17.6", Connections: 5, MaxConnections: 100, Recovery: true, Transactions: 9000, CacheHits: 99000, CacheReads: 1000,
		SharedBuffers: 512 << 20, Source: &agenttest.PGSource{Host: "10.0.20.186", Status: "streaming", Lag: 42},
	}
	want := map[string]float64{
		"node_db_up": 1, "node_db_connections": 5, "node_db_max_connections": 100, "node_db_transactions_total": 9000,
		"node_db_cache_hits_total": 99000, "node_db_cache_reads_total": 1000, "node_db_buffer_pool_bytes": 512 << 20,
		"node_db_primary": 0, "node_db_replication_lag_seconds": 42,
	}
	if got := sampleMap(dbAgent(h).dbMetrics(context.Background(), false)); !maps.Equal(got, want) {
		t.Errorf("replica:\n%v\nwant\n%v", got, want)
	}
	h.Postgres.Databases = []agenttest.Database{{Name: "nextcloud", Size: 9 << 20}}
	if got, want := sizeMap(dbAgent(h).dbMetrics(context.Background(), true)), map[string]float64{"nextcloud": 9 << 20, "postgres": 7520783}; !maps.Equal(got, want) {
		t.Errorf("grootte: %v", got)
	}
	// De primary heeft geen achterstand.
	h.Postgres.Recovery = false
	got := sampleMap(dbAgent(h).dbMetrics(context.Background(), false))
	if _, ok := got["node_db_replication_lag_seconds"]; ok || got["node_db_primary"] != 1 {
		t.Errorf("primary: %v", got)
	}
	// Zonder database stuurt de agent niets.
	if s := dbAgent(agenttest.NewHost(t.TempDir(), "10.0.20.188")).dbMetrics(context.Background(), false); len(s) != 0 {
		t.Errorf("zonder database: %v", s)
	}
}

// TestDBStatusQuick laat de databases en clients weg, voor de bewaking.
func TestDBStatusQuick(t *testing.T) {
	h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	h.MariaDB = &agenttest.MariaDB{Version: "11.8.3", Connections: 3, MaxConnections: 151, Uptime: 77,
		Galera:    &agenttest.Galera{ClusterName: "nextcloud", Status: "Primary", Size: 3, State: 4, Comment: "Synced", Ready: true},
		Databases: []agenttest.Database{{Name: "nextcloud", Size: 1 << 20, Tables: 3}},
	}
	res := dbAgent(h).dbStatus(context.Background(), true)
	if len(res.Servers) != 1 || res.Servers[0].Galera == nil || res.Servers[0].UptimeSeconds != 77 || len(res.Servers[0].Databases) != 0 {
		t.Fatalf("snel: %+v", res)
	}
	if q := queries(h); !slices.Equal(q, []string{dbsql.MariaVariables, dbsql.MariaStatus}) {
		t.Errorf("query's: %q", q)
	}
}
