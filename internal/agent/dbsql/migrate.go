package dbsql

// De opdrachten voor het verhuizen van een database. Op de oude MariaDB
// alleen catalogusgegevens en een tijdelijke gebruiker; op de nieuwe een
// nieuwe database en tellingen per tabel.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var (
	migrateUserRe = regexp.MustCompile(`^` + protocol.DBMigrateUserPrefix + `[a-z0-9]{1,40}$`)
	charsetRe     = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// CheckMigrateUser zegt waarom een naam geen tijdelijke gebruiker voor een
// verhuizing kan zijn.
func CheckMigrateUser(name string) error {
	if !migrateUserRe.MatchString(name) {
		return fmt.Errorf("%q is geen tijdelijke gebruiker voor een verhuizing", name)
	}
	return nil
}

// MigrateInfo leest in één rij: de versie, of db bestaat (0 of 1), haar
// tekenset en collatie, en hoeveel views, routines, triggers en events ze
// heeft.
func MigrateInfo(db string) (string, error) {
	if err := CheckDatabase(db); err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT VERSION(), "+
		"(SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '%[1]s'), "+
		"COALESCE((SELECT DEFAULT_CHARACTER_SET_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '%[1]s'), ''), "+
		"COALESCE((SELECT DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '%[1]s'), ''), "+
		"(SELECT COUNT(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA = '%[1]s'), "+
		"(SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = '%[1]s'), "+
		"(SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = '%[1]s'), "+
		"(SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA = '%[1]s')", db), nil
}

// MigrateTables leest de tabellen van db zonder de views: naam, engine, de
// schatting van het aantal rijen, de grootte, en of ze een primaire
// sleutel heeft (0 of 1).
func MigrateTables(db string) (string, error) {
	if err := CheckDatabase(db); err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT t.TABLE_NAME, COALESCE(t.ENGINE, ''), COALESCE(t.TABLE_ROWS, 0), "+
		"COALESCE(t.DATA_LENGTH + t.INDEX_LENGTH, 0), "+
		"EXISTS (SELECT 1 FROM information_schema.TABLE_CONSTRAINTS c WHERE c.TABLE_SCHEMA = t.TABLE_SCHEMA "+
		"AND c.TABLE_NAME = t.TABLE_NAME AND c.CONSTRAINT_TYPE = 'PRIMARY KEY') "+
		"FROM information_schema.TABLES t WHERE t.TABLE_SCHEMA = '%s' AND t.TABLE_TYPE = 'BASE TABLE' "+
		"ORDER BY t.TABLE_NAME", db), nil
}

// MigrateRights zegt welke rechten de tijdelijke gebruiker naast SELECT en
// SHOW VIEW nodig heeft om alles van de database in de dump te krijgen.
type MigrateRights struct {
	// Routines: "" zonder procedures of functies, "db" voor SHOW CREATE
	// ROUTINE op de database (MariaDB 11.3 en nieuwer), "proc" voor SELECT
	// op mysql.proc (ouder): de definities van alle routines, geen data.
	Routines string
	// Triggers en Events: mariadb-dump leest ze alleen met het recht
	// TRIGGER of EVENT.
	Triggers bool
	Events   bool
}

// MigrateGrant maakt de tijdelijke gebruiker op elke host, met password,
// en geeft hem wat mariadb-dump van db nodig heeft. Een gebruiker die er
// al was, wordt vervangen.
func MigrateGrant(db, user string, hosts []string, password string, r MigrateRights) (string, error) {
	if err := CheckDatabase(db); err != nil {
		return "", err
	}
	if err := CheckMigrateUser(user); err != nil {
		return "", err
	}
	if err := checkHosts(hosts); err != nil {
		return "", err
	}
	if !passwordRe.MatchString(password) {
		return "", errors.New("een wachtwoord is 16 tot 128 letters en cijfers")
	}
	rights := []string{"SELECT", "SHOW VIEW"}
	if r.Triggers {
		rights = append(rights, "TRIGGER")
	}
	if r.Events {
		rights = append(rights, "EVENT")
	}
	switch r.Routines {
	case "", "proc":
	case "db":
		rights = append(rights, "SHOW CREATE ROUTINE")
	default:
		return "", fmt.Errorf("onbekend recht voor routines %q", r.Routines)
	}
	var b strings.Builder
	for _, h := range hosts {
		acc := account(user, h)
		fmt.Fprintf(&b, "CREATE OR REPLACE USER %s IDENTIFIED BY '%s';\n", acc, password)
		fmt.Fprintf(&b, "GRANT %s ON %s.* TO %s;\n", strings.Join(rights, ", "), quoteDB(db, true), acc)
		if r.Routines == "proc" {
			fmt.Fprintf(&b, "GRANT SELECT ON `mysql`.`proc` TO %s;\n", acc)
		}
	}
	return b.String(), nil
}

// MigrateRevoke haalt de tijdelijke gebruiker weg op elke host, met al
// zijn rechten.
func MigrateRevoke(user string, hosts []string) (string, error) {
	if err := CheckMigrateUser(user); err != nil {
		return "", err
	}
	if err := checkHosts(hosts); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, h := range hosts {
		fmt.Fprintf(&b, "DROP USER IF EXISTS %s;\n", account(user, h))
	}
	return b.String(), nil
}

// CreateDatabaseAs maakt een nieuwe database met de tekenset en collatie
// van de oude; zonder IF NOT EXISTS, zodat het mislukt als ze er al is.
func CreateDatabaseAs(db, charset, collation string) (string, error) {
	if err := CheckDatabase(db); err != nil {
		return "", err
	}
	if charset == "" {
		charset = "utf8mb4"
	}
	if !charsetRe.MatchString(charset) {
		return "", fmt.Errorf("%q is geen tekenset", charset)
	}
	q := fmt.Sprintf("CREATE DATABASE %s CHARACTER SET %s", quoteDB(db, false), charset)
	if collation != "" {
		if !charsetRe.MatchString(collation) {
			return "", fmt.Errorf("%q is geen collatie", collation)
		}
		q += " COLLATE " + collation
	}
	return q + ";\n", nil
}

// BaseTables leest de namen van de tabellen van db, zonder de views.
func BaseTables(db string) (string, error) {
	if err := CheckDatabase(db); err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = '%s' "+
		"AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME", db), nil
}

// countChunk is het aantal tabellen per telquery, zodat de query als
// argument klein blijft.
const countChunk = 100

// CountRows telt de rijen van tables in db: per groep van countChunk
// tabellen een query die één rij met de aantallen geeft, in dezelfde
// volgorde. De namen komen uit information_schema; een backtick erin
// wordt verdubbeld.
func CountRows(db string, tables []string) ([]string, error) {
	if err := CheckDatabase(db); err != nil {
		return nil, err
	}
	var out []string
	for i := 0; i < len(tables); i += countChunk {
		parts := make([]string, 0, countChunk)
		for _, t := range tables[i:min(i+countChunk, len(tables))] {
			if t == "" {
				return nil, errors.New("een tabel zonder naam")
			}
			parts = append(parts, fmt.Sprintf("(SELECT COUNT(*) FROM %s.%s)", quoteDB(db, false), QuoteIdent(t)))
		}
		out = append(out, "SELECT "+strings.Join(parts, ", "))
	}
	return out, nil
}

// QuoteIdent zet een naam tussen backticks; een backtick erin wordt
// verdubbeld.
func QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// De gebruikers van een app die meeverhuizen. Hun namen en hosts komen
// van de oude server, dus ze mogen meer dan wat ClusterForge zelf maakt,
// maar nooit een aanhalingsteken, backslash of backtick.
var (
	appUserRe   = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,80}$`)
	appHostRe   = regexp.MustCompile(`^[A-Za-z0-9.:%_\-]{1,255}$`)
	schemaRe    = regexp.MustCompile(`^[a-z0-9_\\]{1,128}$`)
	nativeHash  = regexp.MustCompile(`^\*[0-9A-F]{40}$`)
	appRightSet = map[string]bool{
		"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "CREATE": true, "DROP": true, "REFERENCES": true,
		"INDEX": true, "ALTER": true, "CREATE TEMPORARY TABLES": true, "LOCK TABLES": true, "EXECUTE": true, "CREATE VIEW": true,
		"SHOW VIEW": true, "CREATE ROUTINE": true, "ALTER ROUTINE": true, "EVENT": true, "TRIGGER": true, "DELETE HISTORY": true,
		"SHOW CREATE ROUTINE": true,
	}
)

// lockedRights zijn de rechten van een app op haar oude database na de
// verhuizing: lezen, zodat er niets verloren gaat, maar niet schrijven.
var lockedRights = []string{"SELECT", "SHOW VIEW"}

// baseRights zijn de rechten die ALL PRIVILEGES op een database op elke
// MariaDB en MySQL geeft. Wie ze allemaal heeft, krijgt ALL PRIVILEGES:
// een nieuwere versie geeft daar meer bij, zoals SHOW CREATE ROUTINE.
var baseRights = []string{
	"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "REFERENCES", "INDEX", "ALTER", "CREATE TEMPORARY TABLES",
	"LOCK TABLES", "EXECUTE", "CREATE VIEW", "SHOW VIEW", "CREATE ROUTINE", "ALTER ROUTINE", "EVENT", "TRIGGER",
}

// rightsSQL zijn de rechten zoals in een GRANT.
func rightsSQL(rights []string) string {
	all := true
	for _, r := range baseRights {
		all = all && slices.Contains(rights, r)
	}
	if all {
		return "ALL PRIVILEGES"
	}
	return strings.Join(rights, ", ")
}

// UnescapeSchema is de naam van de database achter een schema uit een
// GRANT: \_ wordt _.
func UnescapeSchema(schema string) string { return strings.ReplaceAll(schema, `\_`, "_") }

// MigrateAccounts leest de gebruikers met rechten op db, één rij per
// gebruiker en host: naam, host, plugin, hash (leeg zonder secret), het
// schema zoals in de GRANT, de rechten met komma's, en hoeveel rechten op
// alle databases hij heeft.
func MigrateAccounts(db string, secret bool) (string, error) {
	if err := CheckDatabase(db); err != nil {
		return "", err
	}
	hash := "''"
	if secret {
		hash = "u.authentication_string"
	}
	escaped := strings.ReplaceAll(db, "_", `\\_`)
	return fmt.Sprintf("SELECT u.User, u.Host, u.plugin, %s, s.TABLE_SCHEMA, "+
		"GROUP_CONCAT(s.PRIVILEGE_TYPE ORDER BY s.PRIVILEGE_TYPE SEPARATOR ','), "+
		"(SELECT COUNT(*) FROM information_schema.USER_PRIVILEGES g WHERE g.GRANTEE = s.GRANTEE AND g.PRIVILEGE_TYPE <> 'USAGE') "+
		"FROM information_schema.SCHEMA_PRIVILEGES s JOIN mysql.user u ON s.GRANTEE = CONCAT('''', u.User, '''@''', u.Host, '''') "+
		"WHERE s.TABLE_SCHEMA IN ('%s', '%s') "+
		"GROUP BY u.User, u.Host, u.plugin, u.authentication_string, s.TABLE_SCHEMA, s.GRANTEE ORDER BY u.User, u.Host", hash, db, escaped), nil
}

// CheckAppAccount zegt waarom a geen gebruiker is die een verhuizing kan
// meenemen of op slot zetten.
func CheckAppAccount(a protocol.DBMigrateAccount) error {
	switch {
	case !appUserRe.MatchString(a.User):
		return fmt.Errorf("%q is geen gebruiker die ClusterForge kan verhuizen: alleen letters, cijfers, _, . en -", a.User)
	case strings.HasPrefix(a.User, protocol.DBMigrateUserPrefix):
		return fmt.Errorf("%s is de tijdelijke gebruiker van een verhuizing", a.User)
	case slices.Contains(SystemUsers, a.User):
		return fmt.Errorf("%s is een gebruiker van MariaDB zelf", a.User)
	case !appHostRe.MatchString(a.Host):
		return fmt.Errorf("%q is geen host voor een gebruiker", a.Host)
	case len(a.Privileges) == 0:
		return fmt.Errorf("%s@%s zonder rechten", a.User, a.Host)
	}
	for _, p := range a.Privileges {
		if !appRightSet[p] {
			return fmt.Errorf("%s@%s heeft het recht %q, dat ClusterForge niet kent", a.User, a.Host, p)
		}
	}
	return nil
}

// MigrateAdopt maakt a op de nieuwe server met dezelfde hash als op de
// oude, en geeft hem zijn rechten op db. Een gebruiker die er al is,
// houdt zijn andere rechten en krijgt het wachtwoord van de oude server.
func MigrateAdopt(db string, a protocol.DBMigrateAccount) (string, error) {
	if err := CheckDatabase(db); err != nil {
		return "", err
	}
	if err := CheckAppAccount(a); err != nil {
		return "", err
	}
	if a.Plugin != protocol.DBMigratePlugin {
		return "", fmt.Errorf("%s logt in met %q; ClusterForge verhuist alleen gebruikers met %s", a.User, a.Plugin, protocol.DBMigratePlugin)
	}
	hash := strings.ToUpper(a.Hash)
	if !nativeHash.MatchString(hash) {
		return "", fmt.Errorf("%s@%s heeft geen hash van een wachtwoord", a.User, a.Host)
	}
	acc := account(a.User, a.Host)
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE USER IF NOT EXISTS %s IDENTIFIED VIA %s USING '%s';\n", acc, protocol.DBMigratePlugin, hash)
	fmt.Fprintf(&b, "ALTER USER %s IDENTIFIED VIA %s USING '%s';\n", acc, protocol.DBMigratePlugin, hash)
	fmt.Fprintf(&b, "GRANT %s ON %s.* TO %s;\n", rightsSQL(a.Privileges), quoteDB(db, true), acc)
	return b.String(), nil
}

// MigrateLock laat de gebruikers alleen nog db lezen (lock), of geeft
// hun hun rechten terug. Op slot houdt een gebruiker van SELECT en SHOW
// VIEW wat hij al had; wie geen van beide had, houdt niets.
func MigrateLock(db string, accounts []protocol.DBMigrateAccount, lock bool) (string, error) {
	if err := CheckDatabase(db); err != nil {
		return "", err
	}
	if len(accounts) == 0 {
		return "", errors.New("geen gebruikers om op slot te zetten")
	}
	var b strings.Builder
	for _, a := range accounts {
		if err := CheckAppAccount(a); err != nil {
			return "", err
		}
		if !schemaRe.MatchString(a.Schema) || UnescapeSchema(a.Schema) != db {
			return "", fmt.Errorf("%q is geen GRANT op %s", a.Schema, db)
		}
		rights := a.Privileges
		if lock {
			rights = slices.DeleteFunc(slices.Clone(lockedRights), func(r string) bool { return !slices.Contains(a.Privileges, r) })
		}
		acc := account(a.User, a.Host)
		fmt.Fprintf(&b, "REVOKE ALL PRIVILEGES ON `%s`.* FROM %s;\n", a.Schema, acc)
		if len(rights) > 0 {
			fmt.Fprintf(&b, "GRANT %s ON `%s`.* TO %s;\n", rightsSQL(rights), a.Schema, acc)
		}
	}
	return b.String(), nil
}
