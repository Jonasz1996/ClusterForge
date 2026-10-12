// Package dbsql bevat de vaste query's waarmee cf-agent MariaDB en
// PostgreSQL leest. De agent voert geen andere SQL uit; de nagespeelde
// databases in agenttest antwoorden op precies deze query's.
package dbsql

// De query's van MariaDB. Ze lezen alleen variabelen, status en
// information_schema.
const (
	MariaVariables = "SHOW GLOBAL VARIABLES WHERE Variable_name IN " +
		"('version', 'wsrep_on', 'wsrep_cluster_name', 'max_connections', 'read_only')"
	MariaStatus = "SHOW GLOBAL STATUS WHERE Variable_name IN ('Threads_connected', 'Uptime', " +
		"'wsrep_cluster_size', 'wsrep_cluster_status', 'wsrep_cluster_state_uuid', 'wsrep_local_state', " +
		"'wsrep_local_state_comment', 'wsrep_ready', 'wsrep_incoming_addresses')"
	// MariaSource is de replicatie van een replica, met kolomnamen.
	MariaSource = "SHOW ALL SLAVES STATUS"
	// MariaReplicas zijn de replica's die nu de binlog van deze server lezen.
	MariaReplicas  = "SELECT HOST FROM information_schema.PROCESSLIST WHERE COMMAND LIKE 'Binlog Dump%'"
	MariaDatabases = "SELECT s.SCHEMA_NAME, COALESCE(SUM(t.DATA_LENGTH + t.INDEX_LENGTH), 0), COUNT(t.TABLE_NAME) " +
		"FROM information_schema.SCHEMATA s LEFT JOIN information_schema.TABLES t " +
		"ON t.TABLE_SCHEMA = s.SCHEMA_NAME AND t.TABLE_TYPE = 'BASE TABLE' " +
		"WHERE s.SCHEMA_NAME NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys') " +
		"GROUP BY s.SCHEMA_NAME ORDER BY s.SCHEMA_NAME"
	// MariaClients zijn de verbindingen van clients, één rij per
	// verbinding, zonder de agent zelf, de replicatie en de threads van
	// MariaDB en Galera.
	MariaClients = "SELECT USER, HOST, COALESCE(DB, '') FROM information_schema.PROCESSLIST " +
		"WHERE ID <> CONNECTION_ID() AND COMMAND NOT IN ('Daemon', 'Binlog Dump', 'Binlog Dump GTID') " +
		"AND USER NOT IN ('system user', 'event_scheduler', 'unauthenticated user')"
)

// De query's van PostgreSQL. Ze lezen alleen instellingen, statistieken en
// de catalogus.
const (
	PGInfo = "SELECT current_setting('server_version'), pg_is_in_recovery(), current_setting('max_connections'), " +
		"(SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'), " +
		"extract(epoch FROM now() - pg_postmaster_start_time())::bigint, current_setting('default_transaction_read_only')"
	PGDatabases = "SELECT datname, pg_database_size(oid) FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname"
	PGTables    = "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace " +
		"WHERE c.relkind IN ('r', 'p') AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%'"
	PGReplicas = "SELECT coalesce(host(client_addr), ''), coalesce(state, ''), coalesce(sync_state, ''), " +
		"coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn), 0)::bigint FROM pg_stat_replication ORDER BY 1"
	PGSource = "SELECT coalesce(sender_host, ''), status, " +
		"CASE WHEN pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn() THEN 0 " +
		"ELSE coalesce(extract(epoch FROM now() - pg_last_xact_replay_timestamp()), -1) END::bigint FROM pg_stat_wal_receiver"
	// PGClients zijn de verbindingen van clients per gebruiker, adres en
	// database, zonder de agent zelf en zonder walsenders en de processen
	// van PostgreSQL.
	PGClients = "SELECT usename, coalesce(host(client_addr), ''), coalesce(datname, ''), count(*) FROM pg_stat_activity " +
		"WHERE backend_type = 'client backend' AND pid <> pg_backend_pid() AND usename IS NOT NULL GROUP BY 1, 2, 3 ORDER BY 1, 2, 3"
)

// All zijn alle query's die de agent ooit uitvoert. Tests en de
// nagespeelde databases in agenttest gebruiken de lijst.
var All = []string{
	MariaVariables, MariaStatus, MariaSource, MariaReplicas, MariaDatabases, MariaClients,
	PGInfo, PGDatabases, PGTables, PGReplicas, PGSource, PGClients,
}

// Scheidingstekens voor de uitvoer van psql: unit separator tussen velden,
// record separator tussen rijen, zodat een naam met een tab of een
// regeleinde de uitvoer niet breekt.
const (
	PGFieldSep  = "\x1f"
	PGRecordSep = "\x1e"
)
