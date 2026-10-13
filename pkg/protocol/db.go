package protocol

import (
	"strings"
	"time"
)

// DBSince is de eerste protocolversie met databasecommando's: de agent
// leest MariaDB en PostgreSQL op zijn eigen node via de socket, zonder
// wachtwoord en zonder vrije SQL.
const DBSince = 9

// DBPGSince is de eerste protocolversie die Engine in een DBRequest kent:
// gebruikers, databases en dumps van PostgreSQL. Een oudere agent leest het
// veld niet en zou zo'n verzoek op MariaDB uitvoeren; daarom stuurt de
// server een verzoek voor PostgreSQL alleen naar protocol 10 of later.
const DBPGSince = 10

// DBPGMigrateSince is de eerste protocolversie die een database van of naar
// PostgreSQL verhuist (DBMigrate met Engine). Een oudere agent zou het
// verzoek op MariaDB uitvoeren.
const DBPGMigrateSince = 11

// DBPatroniSince is de eerste protocolversie die Patroni beheert
// (DBPatroniOp): overschakelen, een replica opnieuw opbouwen, herstarten en
// de instellingen van PostgreSQL in etcd.
const DBPatroniSince = 12

// DBMetricsSince is de eerste protocolversie die metingen van MariaDB en
// PostgreSQL meestuurt (node_db_*), een snelle DBStatus kent (Quick) en de
// uptime van MariaDB in DBPosition zet. Een oudere agent stuurt geen
// metingen en geeft bij Quick de hele status.
const DBMetricsSince = 13

// DBProbeSince is de eerste protocolversie met DBWriteProbe: de schrijftest
// via het VIP voor de failovertests van een database.
const DBProbeSince = 14

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
	// DBDump maakt een dump van een database, toont de dumps op de node,
	// ruimt oude op, of zet een dump terug in een nieuwe database. Maken en
	// terugzetten lopen op de achtergrond; DBDumpList toont de voortgang.
	DBDump = "dump"
	// DBMigrate helpt een database verhuizen van een andere MariaDB naar
	// deze node, of met Engine postgresql van een andere PostgreSQL naar de
	// primary hier. Op de oude node: de database bekijken en een tijdelijke
	// gebruiker die alleen de dump mag maken. Op de nieuwe node: kijken of
	// de oude server te bereiken is, en de dump daar ophalen en meteen
	// importeren in een nieuwe database, met de rijen per tabel geteld aan
	// beide kanten. Dat laatste loopt op de achtergrond; DBMigrateStatus
	// toont de voortgang.
	DBMigrate = "migrate"
	// DBPatroniOp vraagt Patroni op deze node via zijn REST-API iets te
	// doen: overschakelen naar een andere node, deze replica opnieuw
	// opbouwen, PostgreSQL herstarten als dat nodig is, of de instellingen
	// van het cluster in etcd zetten. De agent leest het wachtwoord van de
	// REST-API uit /etc/patroni/clusterforge.env.
	DBPatroniOp = "patroni"
	// DBWriteProbe is de schrijftest van een failovertest: de agent schrijft
	// elke halve seconde een rij via het VIP in DBProbeDatabase, met de
	// gebruiker DBProbeUser die de server daarvoor maakte, en onthoudt welke
	// rijen bevestigd zijn. Na de test telt hij welke er nog zijn. De
	// pogingen staan alleen in zijn geheugen.
	DBWriteProbe = "write_probe"
)

// Wat een DBWriteProbe doet.
const (
	// DBProbeStart begint te schrijven; staat de schrijftest van Run er al,
	// dan geeft de agent die.
	DBProbeStart = "start"
	// DBProbeRead geeft de pogingen vanaf From.
	DBProbeRead = "read"
	// DBProbeFinish stopt met schrijven, telt welke bevestigde rijen er via
	// het VIP nog zijn, en haalt de rijen van Run weg. Een tweede keer geeft
	// hetzelfde antwoord, zodat een hervatte taak het opnieuw kan vragen.
	DBProbeFinish = "finish"
	// DBProbeStop stopt zonder te tellen, als de test niet doorgaat.
	DBProbeStop = "stop"
)

// De database en de gebruiker van de schrijftest. De tabel heeft per run
// de volgnummers van de rijen.
const (
	DBProbeDatabase = "clusterforge_probe"
	DBProbeUser     = "cf_probe"
	DBProbeTable    = "writes"
)

// Grenzen van een schrijftest.
const (
	// DBProbeInterval is hoe vaak de agent schrijft, en DBProbeTimeout hoe
	// lang één poging mag duren.
	DBProbeInterval = 500 * time.Millisecond
	DBProbeTimeout  = 2 * time.Second
	// DBProbeMaxSeconds is hoe lang een schrijftest hoogstens loopt; daarna
	// stopt de agent vanzelf.
	DBProbeMaxSeconds = 30 * 60
	// DBProbeKeep is hoe lang de agent een afgeronde schrijftest onthoudt.
	DBProbeKeep = time.Hour
	// DBProbeMissing is het hoogste aantal ontbrekende volgnummers in een
	// antwoord; Missing telt ze allemaal.
	DBProbeMissing = 50
)

// DBProbeRequest zegt wat een DBWriteProbe doet. Engine staat in het
// DBRequest.
type DBProbeRequest struct {
	Action string `json:"action"`
	// Run is het id van de failovertest (een uuid); de rijen in de tabel
	// hebben het.
	Run string `json:"run"`
	// Host en Port zijn het VIP van de database, bij start.
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	// Password is van DBProbeUser: 16 tot 128 letters en cijfers. Het gaat
	// in een bestand dat alleen root kan lezen.
	Password string `json:"password,omitempty"`
	// MaxSeconds: na zoveel seconden stopt de agent vanzelf.
	MaxSeconds int `json:"max_seconds,omitempty"`
	// From is bij read de eerste poging die de server nog niet heeft.
	From int `json:"from,omitempty"`
}

// DBProbeResult is het antwoord op een DBWriteProbe.
type DBProbeResult struct {
	Run string `json:"run"`
	// Running: de agent schrijft nog.
	Running bool `json:"running"`
	// ElapsedMS is hoe lang geleden de schrijftest begon, op het moment van
	// het antwoord. Een poging heeft AtMS sinds dat begin; zo rekent de
	// server met zijn eigen klok.
	ElapsedMS int64 `json:"elapsed_ms"`
	// Total is het aantal pogingen; Attempts zijn die vanaf From.
	Total    int              `json:"total"`
	Attempts []DBProbeAttempt `json:"attempts"`
	// Stopped zegt waarom de agent vanzelf stopte, zoals na MaxSeconds.
	Stopped string `json:"stopped,omitempty"`
	// Checked: bij finish telde de agent de rijen. Confirmed is het aantal
	// bevestigde rijen, Missing hoeveel daarvan er niet meer zijn (met de
	// eerste volgnummers in MissingSeqs), en Extra hoeveel rijen er zijn
	// waarvan de bevestiging niet aankwam. CheckError zegt waarom tellen
	// niet lukte.
	Checked     bool    `json:"checked,omitempty"`
	Confirmed   int     `json:"confirmed,omitempty"`
	Missing     int     `json:"missing,omitempty"`
	MissingSeqs []int64 `json:"missing_seqs,omitempty"`
	Extra       int     `json:"extra,omitempty"`
	CheckError  string  `json:"check_error,omitempty"`
}

// DBProbeAttempt is één keer schrijven. OK: de database bevestigde de rij.
type DBProbeAttempt struct {
	Seq  int64 `json:"seq"`
	AtMS int64 `json:"at_ms"`
	// MS is hoe lang de poging duurde.
	MS    int64  `json:"ms"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Wat een DBPatroniOp doet.
const (
	// DBPatroniSwitchover maakt Candidate de primary in plaats van Leader.
	// Patroni stopt eerst de schrijvers op de oude primary, dus er gaat
	// niets verloren.
	DBPatroniSwitchover = "switchover"
	// DBPatroniReinit gooit de data van deze replica weg en haalt alles
	// opnieuw bij de primary. Patroni weigert het op de primary.
	DBPatroniReinit = "reinitialize"
	// DBPatroniRestart herstart PostgreSQL op deze node via Patroni; met
	// Pending alleen als een gewijzigde instelling dat vraagt.
	DBPatroniRestart = "restart"
	// DBPatroniConfig zet Parameters in de instellingen van het cluster in
	// etcd, die voor alle nodes gelden, en geeft terug wat er nu staat.
	DBPatroniConfig = "config"
)

// DBPatroniTimeout is hoe lang een DBPatroniOp mag duren: een switchover
// wacht tot de nieuwe primary er is.
const DBPatroniTimeout = 90 * time.Second

// DBPatroniParameters zijn de parameters van PostgreSQL die DBPatroniConfig
// mag zetten. Patroni neemt ze alleen uit etcd, niet uit clusterforge.yml.
var DBPatroniParameters = []string{"max_connections"}

// DBPatroniRequest zegt wat een DBPatroniOp doet.
type DBPatroniRequest struct {
	Action string `json:"action"`
	// Leader en Candidate horen bij switchover: de primary nu, en de
	// replica die het wordt.
	Leader    string `json:"leader,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	// Pending hoort bij restart: alleen herstarten als Patroni zegt dat het
	// nodig is.
	Pending bool `json:"pending,omitempty"`
	// Parameters horen bij config, met namen uit DBPatroniParameters en
	// gehele getallen als waarde. Leeg leest alleen.
	Parameters map[string]string `json:"parameters,omitempty"`
}

// DBPatroniResult is het antwoord op een DBPatroniOp.
type DBPatroniResult struct {
	// Message is wat Patroni antwoordde.
	Message string `json:"message,omitempty"`
	// Changed is true als er iets veranderde: een herstart die nodig was,
	// of een parameter met een andere waarde.
	Changed bool `json:"changed"`
	// Parameters zijn de parameters uit DBPatroniParameters zoals ze na
	// config in etcd staan.
	Parameters map[string]string `json:"parameters,omitempty"`
}

// Wat een DBMigrate doet.
const (
	// Op de oude node: catalogusgegevens van de database, zonder rijen.
	DBMigrateInspect = "inspect"
	// Op de oude node: de tijdelijke gebruiker aanmaken of weghalen.
	DBMigrateGrant  = "grant"
	DBMigrateRevoke = "revoke"
	// Op de nieuwe node: de begroeting van de oude MariaDB lezen, zonder in
	// te loggen.
	DBMigrateProbe = "probe"
	// Op de nieuwe node: de dump ophalen en importeren, en de voortgang.
	DBMigratePull   = "pull"
	DBMigrateStatus = "status"
	// Op de oude node: de gebruikers met rechten op de database, met hun
	// hash als Secret aan staat.
	DBMigrateAccounts = "accounts"
	// Op de nieuwe node: die gebruikers maken met dezelfde hash, en hun
	// rechten op de database geven.
	DBMigrateAdopt = "adopt"
	// Op de oude node: de gebruikers alleen nog laten lezen, of hun rechten
	// teruggeven.
	DBMigrateLock   = "lock"
	DBMigrateUnlock = "unlock"
)

// DBMigratePlugin is de enige manier van inloggen die een verhuizing
// meeneemt: een gewoon wachtwoord, met dezelfde hash op MariaDB en MySQL.
const DBMigratePlugin = "mysql_native_password"

// DBMigrateUserPrefix begint de naam van de tijdelijke gebruiker op de oude
// MariaDB; de rest is letters en cijfers.
const DBMigrateUserPrefix = "cf_verhuizing_"

// Wat een DBDump doet.
const (
	DBDumpStart   = "start"
	DBDumpList    = "list"
	DBDumpPrune   = "prune"
	DBDumpRestore = "restore"
)

// DBDumpDir is de map met de dumps, een map per database, alles met modus
// 0700 en 0600 van root. De Proxmox-back-up van de node neemt hem mee.
const DBDumpDir = "/var/backups/clusterforge-db"

// DBDumpPattern is de naam van een dump: database, datum en tijd in UTC,
// .sql voor MariaDB of .pgsql voor PostgreSQL, met zstd gecomprimeerd.
// Naast elke dump staat een bestand met dezelfde naam en .json erachter,
// met wat de agent erover weet.
const DBDumpPattern = `^([a-z0-9_]{1,63})-([0-9]{8}-[0-9]{6})\.(sql|pgsql)\.zst$`

// DBDumpEngine is de engine van een dump uit zijn naam; leeg als het geen
// dump is.
func DBDumpEngine(file string) string {
	switch {
	case strings.HasSuffix(file, ".pgsql.zst"):
		return DBPostgreSQL
	case strings.HasSuffix(file, ".sql.zst"):
		return DBMariaDB
	}
	return ""
}

// DBTempRolePattern zijn de tijdelijke beheerders van PostgreSQL waarmee
// een agent een dump van een andere node terugzet. Ze vervallen na
// DBTempRoleTTL.
const (
	DBTempRolePattern = `^cf_herstel_[0-9a-f]{16}$`
	DBTempRoleTTL     = 12 * time.Hour
)

// DBPGPort is de poort van PostgreSQL.
const DBPGPort = 5432

// DBDumpTimeout is hoe lang een dump of het terugzetten ervan mag duren.
const DBDumpTimeout = 12 * time.Hour

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
// DBTrackFile zet; hij roept DBCheckCommand aan. Voor PostgreSQL zijn dat
// DBTrackPGScript, DBTrackPGFile en DBCheckPGCommand, met daarin ook de
// vrrp_instance van het eigen VIP.
const (
	DBTrackScript  = "cf_db"
	DBTrackFile    = "/etc/keepalived/conf.d/clusterforge-db.conf"
	DBCheckCommand = "/usr/local/bin/cf-agent check db"

	DBTrackPGScript  = "cf_pg"
	DBTrackPGFile    = "/etc/keepalived/conf.d/clusterforge-postgresql.conf"
	DBCheckPGCommand = "/usr/local/bin/cf-agent check db postgresql"
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
	// Engine is de database van DBUsers, DBEnsure, DBDrop en DBDump: leeg of
	// DBMariaDB, of DBPostgreSQL. Een oudere agent kent het niet en doet
	// MariaDB.
	Engine string `json:"engine,omitempty"`
	// Deadline: na dit tijdstip weigert de agent het verzoek.
	Deadline time.Time `json:"deadline"`
	// Track hoort bij DBTrack.
	Track *DBTrackRequest `json:"track,omitempty"`
	// Position hoort bij DBPosition.
	Position *DBPositionRequest `json:"position,omitempty"`
	// Ensure hoort bij DBEnsure, Drop bij DBDrop.
	Ensure *DBEnsureRequest `json:"ensure,omitempty"`
	Drop   *DBDropRequest   `json:"drop,omitempty"`
	// Dump hoort bij DBDump.
	Dump *DBDumpRequest `json:"dump,omitempty"`
	// Migrate hoort bij DBMigrate.
	Migrate *DBMigrateRequest `json:"migrate,omitempty"`
	// Patroni hoort bij DBPatroniOp.
	Patroni *DBPatroniRequest `json:"patroni,omitempty"`
	// Quick hoort bij DBStatus: zonder de databases en de clients, voor de
	// bewaking.
	Quick bool `json:"quick,omitempty"`
	// Probe hoort bij DBWriteProbe.
	Probe *DBProbeRequest `json:"probe,omitempty"`
}

// DBMigrateRequest zegt wat een DBMigrate doet.
type DBMigrateRequest struct {
	// Action is een van de DBMigrate-acties.
	Action string `json:"action"`
	// Database is de database op de oude MariaDB.
	Database string `json:"database,omitempty"`
	// User is de tijdelijke gebruiker, Hosts de adressen van de nieuwe node
	// waarvandaan hij mag inloggen, en Password zijn wachtwoord: 16 tot 128
	// letters en cijfers.
	User     string   `json:"user,omitempty"`
	Hosts    []string `json:"hosts,omitempty"`
	Password string   `json:"password,omitempty"`
	// Host en Port zijn het adres van de oude MariaDB, bij probe en pull.
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	// Target is de nieuwe database bij pull en status; ze mag nog niet
	// bestaan en krijgt Charset en Collation van de oude.
	Target    string `json:"target,omitempty"`
	Charset   string `json:"charset,omitempty"`
	Collation string `json:"collation,omitempty"`
	// Total is ongeveer hoeveel SQL er komt, voor de voortgang; 0 als dat
	// onbekend is.
	Total int64 `json:"total,omitempty"`
	// Secret: accounts geeft ook de hash van elk wachtwoord.
	Secret bool `json:"secret,omitempty"`
	// Accounts zijn de gebruikers bij adopt, lock en unlock: één per host.
	// Bij PostgreSQL ook bij pull: hun rechten op de database zelf, die de
	// dump niet meeneemt.
	Accounts []DBMigrateAccount `json:"accounts,omitempty"`
	// Bij PostgreSQL: Owner is de eigenaar van de nieuwe database bij pull,
	// en Trial laat pg_dump de eigenaars en rechten weglaten, zodat een proef
	// geen gebruikers op de nieuwe server nodig heeft. Charset is daar de
	// encoding, zoals UTF8.
	Owner string `json:"owner,omitempty"`
	Trial bool   `json:"trial,omitempty"`
}

// DBMigrateAccount is een gebruiker op één host met zijn rechten op de
// database die verhuist.
type DBMigrateAccount struct {
	User string `json:"user"`
	Host string `json:"host"`
	// Plugin is hoe hij inlogt, zoals mysql_native_password.
	Plugin string `json:"plugin,omitempty"`
	// Hash is de hash van zijn wachtwoord: alleen in het antwoord op
	// accounts met Secret, en bij adopt. Nooit het wachtwoord zelf.
	Hash string `json:"hash,omitempty"`
	// Schema is de database zoals in de GRANT op de oude server, met \_
	// voor _ als hij zo gegeven is; lock en unlock gebruiken hem zo.
	Schema string `json:"schema,omitempty"`
	// Privileges zijn zijn rechten op de database, zoals SELECT. Bij
	// PostgreSQL: OWNER, CONNECT, CREATE en TEMPORARY, en OBJECTS als hij
	// objecten in de database bezit of er rechten op heeft. Host en Schema
	// zijn daar leeg, en Plugin is nologin, scram-sha-256, md5, none
	// (inloggen zonder wachtwoord) of other.
	Privileges []string `json:"privileges"`
	// Global: hij heeft ook rechten op alle databases. Bij PostgreSQL: hij
	// is superuser of mag databases, gebruikers of replicatie maken.
	Global bool `json:"global,omitempty"`
	// MemberOf zijn bij PostgreSQL de rollen waarvan hij lid is.
	MemberOf []string `json:"member_of,omitempty"`
	// Super: bij PostgreSQL een superuser; die gaat niet mee.
	Super bool `json:"super,omitempty"`
}

// DBMigrateResult is het antwoord op een DBMigrate.
type DBMigrateResult struct {
	Info  *DBMigrateInfo  `json:"info,omitempty"`
	Probe *DBMigrateReach `json:"probe,omitempty"`
	// Run is de import van Target: bezig, of de laatste die klaar is.
	Run *DBMigrateRun `json:"run,omitempty"`
	// Accounts is het antwoord op accounts; bij PostgreSQL op adopt de
	// rollen die er nog niet waren.
	Accounts []DBMigrateAccount `json:"accounts,omitempty"`
}

// DBMigrateInfo is wat de oude MariaDB over een database weet, uit
// information_schema.
type DBMigrateInfo struct {
	Exists    bool   `json:"exists"`
	Version   string `json:"version"`
	Charset   string `json:"charset,omitempty"`
	Collation string `json:"collation,omitempty"`
	// SizeBytes is de data en indexen van alle tabellen.
	SizeBytes int64            `json:"size_bytes"`
	Tables    []DBMigrateTable `json:"tables"`
	Views     int              `json:"views"`
	Routines  int              `json:"routines"`
	Triggers  int              `json:"triggers"`
	Events    int              `json:"events"`
	// Bij PostgreSQL: de eigenaar van de database en of die superuser is,
	// de extensies, de tabellen en sequences die de eigenaar niet mag
	// lezen (hoogstens 50), en hoeveel instellingen er met ALTER DATABASE op
	// staan; die neemt een dump niet mee. Charset is de encoding, SizeBytes
	// de grootte van de hele database, en Events is altijd 0.
	Owner      string   `json:"owner,omitempty"`
	OwnerSuper bool     `json:"owner_super,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
	Unreadable []string `json:"unreadable,omitempty"`
	Settings   int      `json:"settings,omitempty"`
}

// DBMigrateTable is een tabel, zonder de views. Rows is de schatting van
// InnoDB, geen telling.
type DBMigrateTable struct {
	Name       string `json:"name"`
	Engine     string `json:"engine"`
	Rows       int64  `json:"rows"`
	SizeBytes  int64  `json:"size_bytes"`
	PrimaryKey bool   `json:"primary_key"`
}

// DBMigrateReach is wat er op het adres van de oude MariaDB antwoordde.
type DBMigrateReach struct {
	// Reached: er antwoordde een MariaDB of MySQL, en Version en TLS komen
	// uit zijn begroeting.
	Reached bool   `json:"reached"`
	Version string `json:"version,omitempty"`
	TLS     bool   `json:"tls"`
	// Code is de fout die MariaDB meteen stuurde, zoals 1130 als het geen
	// enkele gebruiker voor dit adres kent; Error de tekst, of waarom er
	// niets antwoordde. Bij PostgreSQL is Code DBMigrateNoEntry als
	// pg_hba.conf de verbinding weigert, en 1 bij een andere fout.
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
	// Bij PostgreSQL: Seen is het adres waarvan de oude server de nieuwe
	// node ziet, als pg_hba.conf hem weigert; Extensions zijn de extensies
	// die op de nieuwe node te installeren zijn.
	Seen       string   `json:"seen,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
}

// DBMigrateNoEntry is de Code van een DBMigrateReach als pg_hba.conf van de
// oude PostgreSQL een tijdelijke gebruiker van dit adres niet binnenlaat.
const DBMigrateNoEntry = 28000

// DBMigrateRun is een import van een dump van de oude MariaDB.
type DBMigrateRun struct {
	Database string    `json:"database"`
	Target   string    `json:"target"`
	Host     string    `json:"host"`
	Port     int       `json:"port"`
	TLS      bool      `json:"tls"`
	Started  time.Time `json:"started"`
	// Imported is wanneer de import klaar was en het tellen begon.
	Imported time.Time `json:"imported,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	// Bytes is de SQL die al binnen is, Total wat er ongeveer komt.
	Bytes int64 `json:"bytes"`
	Total int64 `json:"total"`
	// Tables zijn de rijen per tabel aan beide kanten, na de import.
	Tables []DBMigrateCount `json:"tables,omitempty"`
	// Views, Routines, Triggers en Events staan in de nieuwe database.
	Views    int    `json:"views"`
	Routines int    `json:"routines"`
	Triggers int    `json:"triggers"`
	Events   int    `json:"events"`
	Error    string `json:"error,omitempty"`
}

// DBMigrateCount is het aantal rijen van een tabel, oud en nieuw.
type DBMigrateCount struct {
	Name   string `json:"name"`
	Source int64  `json:"source"`
	Target int64  `json:"target"`
}

// DBDumpRequest zegt wat een DBDump doet.
type DBDumpRequest struct {
	// Action is DBDumpStart, DBDumpList, DBDumpPrune of DBDumpRestore.
	Action string `json:"action"`
	// Database: waarvan een dump komt (start), uit welke map (restore), of
	// welke map opgeruimd wordt (prune; leeg is alle).
	Database string `json:"database,omitempty"`
	// File is de dump die terugkomt, in de map van Database.
	File string `json:"file,omitempty"`
	// Target is de nieuwe database waarin de dump terugkomt; ze mag nog
	// niet bestaan.
	Target string `json:"target,omitempty"`
	// KeepDays: prune haalt dumps weg die ouder zijn, behalve die in Keep.
	// Zonder Keep blijft de nieuwste complete dump per database staan.
	KeepDays int      `json:"keep_days,omitempty"`
	Keep     []string `json:"keep"`
	// Bij PostgreSQL zet de agent een dump terug op de primary, over het
	// netwerk: Primary is haar adres, en User en Password zijn van een
	// tijdelijke beheerder (DBTempRolePattern) die de server daar maakte.
	// Het wachtwoord gaat in een bestand dat alleen root kan lezen, nooit
	// in een argument.
	Primary  string `json:"primary,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
}

// DBDumpResult is het antwoord op een DBDump.
type DBDumpResult struct {
	// Dumps zijn de dumps op de node, de oudste eerst, ook die mislukten.
	Dumps []DBDumpFile `json:"dumps"`
	// Running is de dump of het terugzetten dat nu loopt; de agent doet er
	// één tegelijk.
	Running *DBDumpRun `json:"running,omitempty"`
	// Restores zijn de keren terugzetten sinds de agent startte.
	Restores []DBDumpRun `json:"restores,omitempty"`
	// Started is de dump die DBDumpStart begon.
	Started string `json:"started,omitempty"`
	// Removed zijn de dumps die DBDumpPrune weghaalde.
	Removed []string `json:"removed,omitempty"`
}

// DBDumpFile is een dump op de node.
type DBDumpFile struct {
	// Engine is DBMariaDB of DBPostgreSQL, uit de naam van het bestand.
	Engine   string `json:"engine,omitempty"`
	Database string `json:"database"`
	File     string `json:"file"`
	// SizeBytes is de grootte op schijf, RawBytes die van de SQL.
	SizeBytes int64 `json:"size_bytes"`
	RawBytes  int64 `json:"raw_bytes"`
	// Tables zijn de tabellen in de dump, TablesExpected die in de database
	// toen de dump begon.
	Tables         int `json:"tables"`
	TablesExpected int `json:"tables_expected"`
	// Complete: de dump eindigt met de regel die mariadb-dump of pg_dump
	// als laatste schrijft.
	Complete bool      `json:"complete"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	// Error zegt waarom de dump mislukte; er is dan geen bestand.
	Error string `json:"error,omitempty"`
}

// DBDumpRun is een dump of het terugzetten ervan.
type DBDumpRun struct {
	// Action is DBDumpStart of DBDumpRestore.
	Action   string    `json:"action"`
	Database string    `json:"database"`
	File     string    `json:"file"`
	Target   string    `json:"target,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	// Bytes is wat er al is: SQL geschreven bij een dump, gelezen van de
	// dump bij terugzetten. Total is wat er ongeveer komt; 0 als dat
	// onbekend is.
	Bytes int64  `json:"bytes"`
	Total int64  `json:"total"`
	Error string `json:"error,omitempty"`
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
	// Grant geeft User op elke host alle rechten op Database. Bij
	// PostgreSQL wordt User de eigenaar van Database.
	Grant bool `json:"grant,omitempty"`
	// Restore maakt User, bij PostgreSQL, een tijdelijke beheerder om een
	// dump terug te zetten: SUPERUSER, en geldig tot DBTempRoleTTL. Alleen
	// voor een naam volgens DBTempRolePattern.
	Restore bool `json:"restore,omitempty"`
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
	// UptimeSeconds is hoe lang MariaDB draait; 0 als hij niet draait of
	// bij een agent ouder dan DBMetricsSince.
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`
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
	// Include: alleen de include van conf.d bovenaan keepalived.conf, voor
	// een eigen VIP met zijn vrrp_instance in conf.d. Zonder keepalived.conf
	// maakt de agent er een met alleen die include. VIP is dan het eigen
	// VIP, en dat mag nog in geen enkele vrrp_instance van keepalived.conf
	// staan.
	Include bool `json:"include,omitempty"`
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
	// Dump is het antwoord op DBDump.
	Dump *DBDumpResult `json:"dump,omitempty"`
	// Migrate is het antwoord op DBMigrate.
	Migrate *DBMigrateResult `json:"migrate,omitempty"`
	// Patroni is het antwoord op DBPatroniOp.
	Patroni *DBPatroniResult `json:"patroni,omitempty"`
	// Probe is het antwoord op DBWriteProbe.
	Probe *DBProbeResult `json:"probe,omitempty"`
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
	// Replication is replication_state van een replica, zoals streaming;
	// Sync is true voor de synchrone replica. Een oudere Patroni zegt het
	// niet.
	Replication string `json:"replication,omitempty"`
	Sync        bool   `json:"sync,omitempty"`
	// PendingRestart: een gewijzigde instelling werkt pas na een herstart
	// van PostgreSQL.
	PendingRestart bool `json:"pending_restart,omitempty"`
	// LagBytes is hoeveel WAL een replica nog niet afgespeeld heeft; nil
	// op de primary, of als Patroni het niet zegt.
	LagBytes *int64 `json:"lag_bytes,omitempty"`
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
