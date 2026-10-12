package protocol

import "time"

// DBSince is de eerste protocolversie met databasecommando's: de agent
// leest MariaDB en PostgreSQL op zijn eigen node via de socket, zonder
// wachtwoord en zonder vrije SQL.
const DBSince = 9

// SubjectDB draagt de databaseverzoeken: cf.node.<id>.db. Net als de
// kube-brug is het een eigen subject, zodat een trage database de
// commando's op cmd niet ophoudt.
const SubjectDB = "db"

const (
	TypeDBRequest = "db_request"
	TypeDBResult  = "db_result"
)

// Soorten databaseverzoek. De agent kent alleen deze.
const (
	// DBStatus leest elke MariaDB en PostgreSQL op de node: versie, rol,
	// de toestand van Galera, replicatie of Patroni, en de databases met
	// hun grootte. Alleen catalogus- en statusgegevens, nooit een rij uit
	// een tabel.
	DBStatus = "status"
)

// Engines.
const (
	DBMariaDB    = "mariadb"
	DBPostgreSQL = "postgresql"
)

// Rollen van een databaseserver.
const (
	// DBRoleMember is een node van een Galera-cluster: elke node kan
	// schrijven.
	DBRoleMember = "member"
	// DBRolePrimary schrijft en heeft replica's, of is de primary van
	// PostgreSQL.
	DBRolePrimary = "primary"
	// DBRoleReplica volgt een primary.
	DBRoleReplica = "replica"
	// DBRoleStandalone is een losse server zonder replicatie.
	DBRoleStandalone = "standalone"
)

// Grenzen van een DBStatus.
const (
	// DBMaxDatabases is het hoogste aantal databases per server in een
	// antwoord; de rest telt alleen mee in DBServer.DatabasesTotal.
	DBMaxDatabases = 500
	// DBMaxClients is het hoogste aantal clients per server in een
	// antwoord.
	DBMaxClients = 500
	// DBTimeout is hoe lang een verzoek op de agent mag duren.
	DBTimeout = 30 * time.Second
)

// DBRequest is een verzoek aan de databases op een node. De server stuurt
// het als request op cf.node.<id>.db; de agent antwoordt met een
// DBResponse.
type DBRequest struct {
	ID string `json:"id"`
	Op string `json:"op"`
	// Deadline: na dit tijdstip weigert de agent het verzoek.
	Deadline time.Time `json:"deadline"`
}

// DBResponse is het antwoord op een DBRequest. Error zegt waarom het
// verzoek helemaal niet lukte; een fout van één server staat in diens
// Error.
type DBResponse struct {
	Error string `json:"error,omitempty"`
	// Servers zijn de databaseservers op de node, bij DBStatus. Leeg als er
	// geen MariaDB of PostgreSQL op staat.
	Servers []DBServer `json:"servers"`
}

// DBServer is één MariaDB of PostgreSQL op een node.
type DBServer struct {
	Engine string `json:"engine"`
	// Unit is de systemd-unit die de server start, zoals mariadb,
	// postgresql@17-main of patroni, en Active wat systemd erover zegt.
	Unit   string `json:"unit"`
	Active string `json:"active"`
	// Running is true als de server via de socket antwoordde. Is hij
	// false, dan staat de reden in Error.
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
	// Version is de versie zonder het achtervoegsel van de distributie,
	// zoals 11.8.3 of 17.6.
	Version        string `json:"version,omitempty"`
	Role           string `json:"role,omitempty"`
	ReadOnly       bool   `json:"read_only,omitempty"`
	Connections    int    `json:"connections"`
	MaxConnections int    `json:"max_connections"`
	UptimeSeconds  int64  `json:"uptime_seconds"`

	Galera  *DBGalera  `json:"galera,omitempty"`
	Patroni *DBPatroni `json:"patroni,omitempty"`
	// Source is de replicatie van een replica: van wie en hoe ver achter.
	Source *DBSource `json:"source,omitempty"`
	// Replicas zijn de replica's die nu van deze server lezen.
	Replicas []DBReplica `json:"replicas,omitempty"`

	Databases      []DBDatabase `json:"databases"`
	DatabasesTotal int          `json:"databases_total"`
	// Clients zijn de verbindingen van apps en mensen, per gebruiker,
	// adres en database; niet die van de replicatie of van Galera zelf.
	Clients []DBClient `json:"clients,omitempty"`
}

// DBGalera is de toestand van een Galera-node, uit SHOW GLOBAL STATUS.
type DBGalera struct {
	ClusterName string `json:"cluster_name"`
	// ClusterUUID is wsrep_cluster_state_uuid: gelijk op alle nodes van
	// hetzelfde cluster.
	ClusterUUID string `json:"cluster_uuid"`
	// ClusterStatus is Primary, Non-Primary of Disconnected.
	ClusterStatus string `json:"cluster_status"`
	ClusterSize   int    `json:"cluster_size"`
	// State is wsrep_local_state_comment, zoals Synced, Donor/Desynced of
	// Joining, en StateCode wsrep_local_state (4 is Synced).
	State     string `json:"state"`
	StateCode int    `json:"state_code"`
	Ready     bool   `json:"ready"`
	// Members zijn de adressen uit wsrep_incoming_addresses.
	Members []string `json:"members,omitempty"`
}

// DBPatroni is wat Patroni op deze node over zichzelf zegt, uit zijn
// REST-API op 127.0.0.1.
type DBPatroni struct {
	Scope    string `json:"scope,omitempty"`
	Role     string `json:"role,omitempty"`
	State    string `json:"state,omitempty"`
	Timeline int    `json:"timeline,omitempty"`
	// Error zegt waarom de REST-API niet te lezen was.
	Error string `json:"error,omitempty"`
}

// DBSource is de replicatie van een replica.
type DBSource struct {
	Host string `json:"host"`
	// Running is true als de replicatie loopt.
	Running bool `json:"running"`
	// LagSeconds is hoe ver de replica achterloopt; nil als dat niet te
	// zeggen is.
	LagSeconds *int64 `json:"lag_seconds,omitempty"`
	Error      string `json:"error,omitempty"`
}

// DBReplica is een replica die van deze server leest.
type DBReplica struct {
	Address string `json:"address"`
	State   string `json:"state,omitempty"`
	// Sync is sync, async of potential bij PostgreSQL.
	Sync string `json:"sync,omitempty"`
	// LagBytes is hoeveel WAL de replica nog moet afspelen.
	LagBytes int64 `json:"lag_bytes"`
}

// DBDatabase is een database met haar grootte. Tables is -1 als het aantal
// tabellen niet te lezen was.
type DBDatabase struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Tables    int    `json:"tables"`
}

// DBClient zijn de verbindingen van één gebruiker vanaf één adres met één
// database. Address is leeg voor een verbinding via de socket of via
// loopback, dus van de node zelf; Database is leeg als de client er geen
// koos.
type DBClient struct {
	User     string `json:"user"`
	Address  string `json:"address,omitempty"`
	Database string `json:"database,omitempty"`
	Count    int    `json:"count"`
}
