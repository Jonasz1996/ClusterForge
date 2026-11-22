package metrics

import (
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Eenheden van de grafieken van databases.
const (
	UnitCount     Unit = "count"
	UnitPerSecond Unit = "per_second"
	UnitSeconds   Unit = "seconds"
)

// dbConnections en dbSizes zijn hetzelfde voor MariaDB en PostgreSQL. De
// grootte komt eens per minuut, vandaar last_over_time; elke node heeft
// dezelfde data, dus de grootste waarde is die van het cluster.
var (
	dbConnections = Panel{ID: "connections", Title: "Verbindingen per node", Unit: UnitCount, Queries: []PanelQuery{
		{`node_db_connections{$sel}`, "{node}"},
		{`max(node_db_max_connections{$sel})`, "maximum"},
	}}
	dbSizes = Panel{ID: "size", Title: "Grootte per database", Unit: UnitBytes, Queries: []PanelQuery{
		{`max by (database) (last_over_time(node_db_database_size_bytes{$sel}[3m]))`, "{database}"},
	}}
)

// MariaDBPanels zijn de grafieken van MariaDB met Galera: een lijn per
// node, en voor de query's ook het totaal van het cluster.
var MariaDBPanels = []Panel{
	dbConnections,
	{ID: "queries", Title: "Query's per seconde", Unit: UnitPerSecond, Queries: []PanelQuery{
		{`rate(node_db_queries_total{$sel}[$w])`, "{node}"},
		{`sum(rate(node_db_queries_total{$sel}[$w]))`, "cluster"},
	}},
	{ID: "buffer_pool", Title: "Buffer pool gevuld met data", Unit: UnitPercent, Queries: []PanelQuery{
		{`100 * node_db_buffer_pool_data_bytes{$sel} / (node_db_buffer_pool_bytes{$sel} > 0)`, "{node}"},
	}},
	{ID: "recv_queue", Title: "Wachtrij van Galera (te verwerken writesets)", Unit: UnitCount, Queries: []PanelQuery{
		{`node_db_galera_recv_queue{$sel}`, "{node}"},
	}},
	{ID: "flow_control", Title: "Flow control (tijd dat Galera schrijven pauzeert)", Unit: UnitPercent, Queries: []PanelQuery{
		{`100 * rate(node_db_galera_flow_control_paused_seconds_total{$sel}[$w])`, "{node}"},
	}},
	dbSizes,
}

// PostgreSQLPanels zijn de grafieken van PostgreSQL met Patroni.
var PostgreSQLPanels = []Panel{
	dbConnections,
	{ID: "transactions", Title: "Transacties per seconde", Unit: UnitPerSecond, Queries: []PanelQuery{
		{`rate(node_db_transactions_total{$sel}[$w])`, "{node}"},
		{`sum(rate(node_db_transactions_total{$sel}[$w]))`, "cluster"},
	}},
	{ID: "cache_hits", Title: "Gelezen uit het geheugen (cache-hits)", Unit: UnitPercent, Queries: []PanelQuery{
		{`100 * rate(node_db_cache_hits_total{$sel}[$w]) / ` +
			`((rate(node_db_cache_hits_total{$sel}[$w]) + rate(node_db_cache_reads_total{$sel}[$w])) > 0)`, "{node}"},
	}},
	{ID: "lag", Title: "Achterstand van de replica's", Unit: UnitSeconds, Queries: []PanelQuery{
		{`node_db_replication_lag_seconds{$sel}`, "{node}"},
	}},
	dbSizes,
}

// DatabasePanels geeft de grafieken voor een engine.
func DatabasePanels(engine string) []Panel {
	if engine == protocol.DBPostgreSQL {
		return PostgreSQLPanels
	}
	return MariaDBPanels
}

// DatabaseSelector maakt de labelselector voor de database van een
// cluster: de engine en de nodes van de database, zodat een andere MariaDB
// op een node van het cluster er niet in komt. Uuids en de namen van de
// engines bevatten geen tekens die in PromQL iets betekenen.
func DatabaseSelector(clusterID uuid.UUID, engine string, nodes []uuid.UUID) string {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.String()
	}
	return ClusterSelector(clusterID) + `,engine="` + engine + `",node_id=~"` + strings.Join(ids, "|") + `"`
}
