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
	// DBTrack laat keepalived de database bewaken in de vrrp_instance met
	// een VIP: een include van /etc/keepalived/conf.d en track_script
	// cf_db. Zonder Apply alleen de diff.
	DBTrack = "track"
	// DBPosition leest waar een Galera-node staat: draait MariaDB, in welke
	// component, en tot welke transactie heeft hij de data. Met Recover
	// haalt de agent de positie na een crash uit InnoDB (galera_recovery).
	DBPosition = "position"
	// DBUsers leest de gebruikers van MariaDB met hun hosts en rechten,
	// zonder wachtwoord of hash.
	DBUsers = "users"
	// DBEnsure maakt een database, een gebruiker of een recht aan, of geeft
	// een gebruiker een nieuw wachtwoord. Het wachtwoord gaat via stdin naar
	// MariaDB, nooit als argument.
	DBEnsure = "ensure"
	// DBDrop haalt een database of gebruiker weg, alleen als Confirm de naam
	// is.
	DBDrop = "drop"
)

// Waar de positie van een Galera-node vandaan komt.
const (
	// DBFromRunning: wsrep_last_committed van een MariaDB die draait.
	DBFromRunning = "running"
	// DBFromGrastate: grastate.dat, na een gewone stop.
	DBFromGrastate = "grastate"
	// DBFromRecovery: galera_recovery, na een crash.
	DBFromRecovery = "recovery"
)

// DBTrackScript is de vrrp_script die ClusterForge in
// DBTrackFile zet; hij roept DBCheckCommand aan.
const (
	DBTrackScript  = "cf_db"
	DBTrackFile    = "/etc/keepalived/conf.d/clusterforge-db.conf"
	DBCheckCommand = "/usr/local/bin/cf-agent check db"
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
	// DBRecoverTimeout is hoe lang een DBPosition met Recover mag duren:
	// galera_recovery start mariadbd even zonder netwerk, en InnoDB kan na
	// een crash lang bezig zijn.
	DBRecoverTimeout = 10 * time.Minute
)

// DBRequest is een verzoek aan de databases op een node. De server stuurt
// het als request op cf.node.<id>.db; de agent antwoordt met een
// DBResponse.
type DBRequest struct {
	ID string `json:"id"`
	Op string `json:"op"`
	// Deadline: na dit tijdstip weigert de agent het verzoek.
	Deadline time.Time `json:"deadline"`
	// Track hoort bij DBTrack.
	Track *DBTrackRequest `json:"track,omitempty"`
	// Position hoort bij DBPosition.
	Position *DBPositionRequest `json:"position,omitempty"`
	// Ensure hoort bij DBEnsure, Drop bij DBDrop.
	Ensure *DBEnsureRequest `json:"ensure,omitempty"`
	Drop   *DBDropRequest   `json:"drop,omitempty"`
}

// DBEnsureRequest zegt wat er in MariaDB moet zijn. Namen voldoen aan
// DBNamePattern; een host is een IP-adres, localhost of een patroon met %.
type DBEnsureRequest struct {
	// Database wordt aangemaakt als ze er niet is, met utf8mb4.
	Database string `json:"database,omitempty"`
	// User wordt aangemaakt op elke host in Hosts als hij er niet is. Met
	// Password krijgt hij dat wachtwoord, ook als hij er al was.
	User     string   `json:"user,omitempty"`
	Hosts    []string `json:"hosts,omitempty"`
	Password string   `json:"password,omitempty"`
	// Grant geeft User op elke host alle rechten op Database.
	Grant bool `json:"grant,omitempty"`
}

// DBDropRequest haalt Database weg, of User op elke host in Hosts.
// Confirm is de naam die de gebruiker intikte; de agent weigert als die
// niet gelijk is aan wat weg moet.
type DBDropRequest struct {
	Database string   `json:"database,omitempty"`
	User     string   `json:"user,omitempty"`
	Hosts    []string `json:"hosts,omitempty"`
	Confirm  string   `json:"confirm"`
}

// DBNamePattern zijn de namen van databases en gebruikers die de agent
// aanmaakt of weghaalt.
const DBNamePattern = `^[a-z0-9_]{1,63}$`

// DBUser is een gebruiker van MariaDB op één host, met zijn rechten.
type DBUser struct {
	Name string `json:"name"`
	Host string `json:"host"`
	// Global zijn de rechten op alle databases, zonder USAGE.
	Global []string `json:"global,omitempty"`
	// Databases zijn de rechten per database; een naam kan een patroon met
	// % of _ zijn.
	Databases []DBGrant `json:"databases,omitempty"`
}

// DBGrant zijn de rechten van een gebruiker op één database.
type DBGrant struct {
	Database   string   `json:"database"`
	Privileges []string `json:"privileges"`
}

// DBPositionRequest: Recover laat de agent galera_recovery draaien als
// MariaDB stilstaat en grastate.dat geen seqno heeft. Zonder Recover is
// de positie dan onbekend.
type DBPositionRequest struct {
	Recover bool `json:"recover"`
}

// DBPositionResult is waar een Galera-node staat.
type DBPositionResult struct {
	// Unit is de unit van MariaDB en Active wat systemd erover zegt:
	// active, activating (hij start of wacht op de andere nodes), failed
	// of inactive.
	Unit   string `json:"unit"`
	Active string `json:"active"`
	// Running: MariaDB antwoordt via de socket, en Galera is zijn toestand.
	Running bool      `json:"running"`
	Galera  *DBGalera `json:"galera,omitempty"`
	// UUID en Seqno zijn de positie: de geschiedenis van het cluster en de
	// laatste transactie. Seqno is -1 als ze onbekend is; Error zegt dan
	// waarom.
	UUID            string `json:"uuid"`
	Seqno           int64  `json:"seqno"`
	SafeToBootstrap bool   `json:"safe_to_bootstrap"`
	// From is DBFromRunning, DBFromGrastate of DBFromRecovery.
	From  string `json:"from,omitempty"`
	Error string `json:"error,omitempty"`
}

// DBTrackRequest zegt welk VIP keepalived met de database moet bewaken.
type DBTrackRequest struct {
	VIP string `json:"vip"`
	// Apply schrijft de nieuwe keepalived.conf, na keepalived -t; anders
	// alleen de diff.
	Apply bool `json:"apply"`
}

// DBTrackResult is het antwoord op DBTrack.
type DBTrackResult struct {
	// Instance is de vrrp_instance met het VIP.
	Instance string `json:"instance"`
	// Changed: er moest iets veranderen (zonder Apply), of het is
	// veranderd (met Apply).
	Changed bool `json:"changed"`
	// Diff is de unified diff van keepalived.conf, met auth_pass
	// gemaskeerd; leeg als er niets verandert.
	Diff string `json:"diff,omitempty"`
}

// DBResponse is het antwoord op een DBRequest. Error zegt waarom het
// verzoek helemaal niet lukte; een fout van één server staat in diens
// Error.
type DBResponse struct {
	Error string `json:"error,omitempty"`
	// Servers zijn de databaseservers op de node, bij DBStatus. Leeg als er
	// geen MariaDB of PostgreSQL op staat.
	Servers []DBServer `json:"servers"`
	// Track is het antwoord op DBTrack.
	Track *DBTrackResult `json:"track,omitempty"`
	// Position is het antwoord op DBPosition.
	Position *DBPositionResult `json:"position,omitempty"`
	// Users is het antwoord op DBUsers.
	Users []DBUser `json:"users,omitempty"`
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
