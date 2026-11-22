package dbsql

// De opdrachten voor het verhuizen van een database van PostgreSQL. Op de
// oude server leest de agent de catalogus en maakt hij een tijdelijke
// gebruiker die lid is van de eigenaar van de database, zodat pg_dump alles
// kan lezen wat die eigenaar kan, maar die standaard alleen leest en na 12
// uur verloopt. Op de nieuwe server maakt hij de database en de rollen van
// de app, en telt hij de rijen per tabel. Elke opdracht staat op één regel,
// voor psql via stdin als postgres.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// pgEncodingRe is de naam van een encoding zoals pg_encoding_to_char hem
// geeft, zoals UTF8 of LATIN1.
var pgEncodingRe = regexp.MustCompile(`^[A-Z0-9_]{1,32}$`)

// De query's van een verhuizing die in de database zelf lopen: de tabellen
// (schema, naam, de schatting van het aantal rijen, de grootte, en of ze
// een primaire sleutel heeft), de andere objecten en de extensies in één
// rij, en de tabellen en sequences die de eigenaar van de database niet mag
// lezen. Wat bij een extensie hoort, schrijft pg_dump als CREATE EXTENSION
// en telt niet mee. Een gepartitioneerde tabel heeft zelf geen rijen; haar
// partities wel.
const (
	pgUserObjects = "n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'"
	pgNotInExt    = "NOT EXISTS (SELECT 1 FROM pg_depend e WHERE e.classid = 'pg_class'::regclass AND e.objid = c.oid AND e.deptype = 'e')"

	PGMigrateTables = "SELECT n.nspname, c.relname, greatest(c.reltuples, 0)::bigint, pg_total_relation_size(c.oid), " +
		"EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conrelid = c.oid AND k.contype = 'p') " +
		"FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind = 'r' AND " + pgUserObjects + " AND " + pgNotInExt +
		" ORDER BY 1, 2"
	PGMigrateObjects = "SELECT (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace " +
		"WHERE c.relkind IN ('v', 'm') AND " + pgUserObjects + " AND " + pgNotInExt + "), " +
		"(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE " + pgUserObjects +
		" AND NOT EXISTS (SELECT 1 FROM pg_depend e WHERE e.classid = 'pg_proc'::regclass AND e.objid = p.oid AND e.deptype = 'e')), " +
		"(SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal), " +
		"coalesce((SELECT string_agg(extname, ',' ORDER BY extname) FROM pg_extension WHERE extname <> 'plpgsql'), '')"
	PGMigrateUnreadable = "SELECT n.nspname || '.' || c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace " +
		"JOIN pg_database d ON d.datname = current_database() WHERE c.relkind IN ('r', 'p', 'S') AND " + pgUserObjects + " AND " + pgNotInExt +
		" AND NOT (has_schema_privilege(d.datdba, n.oid, 'USAGE') AND has_table_privilege(d.datdba, c.oid, 'SELECT')) ORDER BY 1 LIMIT 50"
	// PGMigrateAvailable zijn de extensies die op de nieuwe server te
	// installeren zijn.
	PGMigrateAvailable = "SELECT name FROM pg_available_extensions ORDER BY 1"
)

// PGMigrateInfo leest in één rij, in de database postgres: de versie, of
// db bestaat (0 of 1), haar encoding, haar eigenaar en of die superuser is
// (t of f), haar grootte, en hoeveel instellingen er met ALTER DATABASE of
// ALTER ROLE ... IN DATABASE op staan.
func PGMigrateInfo(db string) (string, error) {
	if err := CheckPGDatabase(db); err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT current_setting('server_version'), count(d.oid), coalesce(max(pg_encoding_to_char(d.encoding)), ''), "+
		"coalesce(max(r.rolname::text), ''), coalesce(bool_or(r.rolsuper), false), coalesce(sum(pg_database_size(d.oid)), 0), "+
		"(SELECT count(*) FROM pg_db_role_setting s JOIN pg_database x ON x.oid = s.setdatabase WHERE x.datname = '%[1]s') "+
		"FROM pg_database d LEFT JOIN pg_roles r ON r.oid = d.datdba WHERE d.datname = '%[1]s'", db), nil
}

// pgMigrateExpired haalt de tijdelijke gebruikers van een verhuizing weg
// die verlopen zijn, als een taak ze niet zelf opruimde.
var pgMigrateExpired = fmt.Sprintf("SELECT format('DROP ROLE %%I', rolname) FROM pg_roles WHERE rolname ~ '%s' AND rolvaliduntil < now()\\gexec\n",
	migrateUserRe.String())

// PGMigrateGrant maakt de tijdelijke gebruiker met de SCRAM-verifier van
// zijn wachtwoord, geldig tot protocol.DBTempRoleTTL, en maakt hem lid van
// de eigenaar van db. Hij erft zo de rechten van de eigenaar, zonder dat
// er iets aan de rechten in de database verandert, en elke transactie is
// standaard alleen lezen. Een eigenaar die superuser is, geeft hij niet.
func PGMigrateGrant(db, user, verifier string) (string, error) {
	if err := CheckPGDatabase(db); err != nil {
		return "", err
	}
	if err := CheckMigrateUser(user); err != nil {
		return "", err
	}
	if !verifierRe.MatchString(verifier) {
		return "", errors.New("de verifier van het wachtwoord klopt niet")
	}
	var b strings.Builder
	b.WriteString(pgMigrateExpired)
	fmt.Fprintf(&b, "SELECT 'CREATE ROLE %s' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s')\\gexec\n", ident(user), user)
	fmt.Fprintf(&b, "SELECT format('ALTER ROLE %s WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT "+
		"PASSWORD %%L VALID UNTIL %%L', '%s', now() + interval '%d hours')\\gexec\n", ident(user), verifier, int(protocol.DBTempRoleTTL.Hours()))
	fmt.Fprintf(&b, "ALTER ROLE %s SET default_transaction_read_only = on;\n", ident(user))
	fmt.Fprintf(&b, "SELECT format('GRANT %%I TO %s', r.rolname) FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba "+
		"WHERE d.datname = '%s' AND NOT r.rolsuper\\gexec\n", ident(user), db)
	return b.String(), nil
}

// PGMigrateRevoke haalt de tijdelijke gebruiker weg, met de verlopen.
func PGMigrateRevoke(user string) (string, error) {
	if err := CheckMigrateUser(user); err != nil {
		return "", err
	}
	return pgMigrateExpired + fmt.Sprintf("DROP ROLE IF EXISTS %s;\n", ident(user)), nil
}

// PGMigrateAccounts leest in de database zelf de rollen die er iets mee
// hebben: de eigenaar, wie objecten bezit of rechten op een object heeft
// (pg_shdepend), wie rechten op de database heeft, en wie lid is van een
// van die rollen en zo zijn rechten krijgt. Een extensie en wat erbij hoort
// tellen niet: pg_dump schrijft alleen CREATE EXTENSION, zonder eigenaar;
// de tijdelijke gebruikers van een verhuizing ook niet. Per rol: naam, hoe
// hij inlogt (nologin, scram-sha-256, md5, none of other), de verifier of
// hash (leeg zonder secret), superuser, eigenaar, de rechten op de
// database met komma's, of hij objecten bezit of er rechten op heeft, de
// rollen waarvan hij lid is (alleen namen zoals ClusterForge ze kent), en
// of hij databases, rollen of replicatie mag maken of RLS omzeilt. De
// rollen van PostgreSQL zelf (pg_) niet.
func PGMigrateAccounts(secret bool) string {
	hash := "''"
	if secret {
		hash = "coalesce(a.rolpassword, '')"
	}
	return "WITH RECURSIVE db AS (SELECT oid, datdba, datacl FROM pg_database WHERE datname = current_database()), " +
		"dep AS (SELECT s.refobjid FROM pg_shdepend s, db WHERE s.dbid = db.oid AND s.refclassid = 'pg_authid'::regclass " +
		"AND s.classid <> 'pg_extension'::regclass AND NOT EXISTS (SELECT 1 FROM pg_depend e WHERE e.classid = s.classid AND e.objid = s.objid AND e.deptype = 'e')), " +
		"r AS (SELECT datdba AS oid FROM db UNION SELECT refobjid FROM dep UNION SELECT a.grantee FROM db, aclexplode(db.datacl) a " +
		"UNION SELECT m.member FROM pg_auth_members m JOIN r ON m.roleid = r.oid) " +
		"SELECT a.rolname, CASE WHEN NOT a.rolcanlogin THEN 'nologin' WHEN a.rolpassword LIKE 'SCRAM-SHA-256$%' THEN 'scram-sha-256' " +
		"WHEN a.rolpassword LIKE 'md5%' THEN 'md5' WHEN a.rolpassword IS NULL THEN 'none' ELSE 'other' END, " + hash + ", a.rolsuper, " +
		"a.oid = (SELECT datdba FROM db), " +
		"coalesce((SELECT string_agg(x.privilege_type, ',' ORDER BY x.privilege_type) FROM db, aclexplode(db.datacl) x WHERE x.grantee = a.oid), ''), " +
		"a.oid IN (SELECT refobjid FROM dep), " +
		"coalesce((SELECT string_agg(g.rolname, ',' ORDER BY g.rolname) FROM pg_auth_members m JOIN pg_roles g ON g.oid = m.roleid " +
		"WHERE m.member = a.oid AND g.rolname ~ '^[a-z0-9_]{1,63}$'), ''), " +
		"a.rolcreatedb OR a.rolcreaterole OR a.rolreplication OR a.rolbypassrls " +
		"FROM pg_authid a JOIN r ON r.oid = a.oid WHERE a.rolname !~ '^pg_' AND a.rolname !~ '" + migrateUserRe.String() + "' ORDER BY 1"
}

// PGRoleNames zijn alle rollen op de server, zonder die van PostgreSQL
// zelf: adopt zegt zo welke rollen het nieuw maakte.
const PGRoleNames = "SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_' ORDER BY 1"

// Hoe een rol van PostgreSQL inlogt, in DBMigrateAccount.Plugin.
const (
	PGLoginNone     = "nologin"
	PGLoginSCRAM    = "scram-sha-256"
	PGLoginMD5      = "md5"
	PGLoginNoSecret = "none"
)

// Wat een rol van PostgreSQL met de database heeft, in
// DBMigrateAccount.Privileges, naast CONNECT, CREATE en TEMPORARY.
const (
	PGOwner   = "OWNER"
	PGObjects = "OBJECTS"
)

// CheckPGAccount zegt waarom een verhuizing a niet kan meenemen.
func CheckPGAccount(a protocol.DBMigrateAccount) error {
	switch {
	case !nameRe.MatchString(a.User):
		return fmt.Errorf("%q: ClusterForge neemt alleen rollen mee met 1 tot 63 kleine letters, cijfers of _", a.User)
	case migrateUserRe.MatchString(a.User) || tempRoleRe.MatchString(a.User):
		return fmt.Errorf("%s is een tijdelijke gebruiker van ClusterForge", a.User)
	case slices.Contains(PGSystemUsers, a.User) || strings.HasPrefix(a.User, "pg_"):
		return fmt.Errorf("%s is een rol van PostgreSQL of Patroni zelf", a.User)
	case a.Super:
		return fmt.Errorf("%s is superuser; ClusterForge maakt geen superusers op het nieuwe cluster", a.User)
	}
	switch a.Plugin {
	case PGLoginNone, PGLoginNoSecret:
	case PGLoginSCRAM:
		if a.Hash != "" && !verifierRe.MatchString(a.Hash) {
			return fmt.Errorf("%s heeft geen geldige SCRAM-verifier", a.User)
		}
	case PGLoginMD5:
		return fmt.Errorf("het wachtwoord van %s staat als md5-hash op de oude server, en het nieuwe cluster laat alleen scram-sha-256 toe", a.User)
	default:
		return fmt.Errorf("%s logt in op een manier die ClusterForge niet meeneemt (%q)", a.User, a.Plugin)
	}
	return nil
}

// PGMigrateAdopt maakt a op de nieuwe server: zonder LOGIN als hij niet
// inlogt, anders met dezelfde verifier als op de oude server. Een rol die
// er al is, houdt zijn andere rechten en krijgt het wachtwoord van de oude
// server.
func PGMigrateAdopt(a protocol.DBMigrateAccount) (string, error) {
	if err := CheckPGAccount(a); err != nil {
		return "", err
	}
	if a.Plugin == PGLoginSCRAM && a.Hash == "" {
		return "", fmt.Errorf("%s zonder verifier", a.User)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "SELECT 'CREATE ROLE %s' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s')\\gexec\n", ident(a.User), a.User)
	switch a.Plugin {
	case PGLoginSCRAM:
		fmt.Fprintf(&b, "ALTER ROLE %s WITH LOGIN PASSWORD '%s';\n", ident(a.User), a.Hash)
	case PGLoginNoSecret:
		fmt.Fprintf(&b, "ALTER ROLE %s WITH LOGIN;\n", ident(a.User))
	}
	return b.String(), nil
}

// PGMigrateMembers maakt de rollen weer lid van elkaar zoals op de oude
// server, voor zover die rollen er op de nieuwe zijn.
func PGMigrateMembers(accounts []protocol.DBMigrateAccount) string {
	var b strings.Builder
	for _, a := range accounts {
		if CheckPGAccount(a) != nil {
			continue
		}
		for _, g := range a.MemberOf {
			if g == a.User || CheckPGUser(g) != nil {
				continue
			}
			fmt.Fprintf(&b, "SELECT format('GRANT %%I TO %s', g.rolname) FROM pg_roles g WHERE g.rolname = '%s' AND NOT pg_has_role('%s', g.oid, 'MEMBER')\\gexec\n",
				ident(a.User), g, a.User)
		}
	}
	return b.String()
}

// PGMigrateDatabaseGrants geeft de rollen hun rechten op de nieuwe
// database db zoals op de oude: CONNECT, CREATE en TEMPORARY. Een dump
// zonder --create neemt die niet mee.
func PGMigrateDatabaseGrants(db string, accounts []protocol.DBMigrateAccount) (string, error) {
	if err := CheckPGDatabase(db); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, a := range accounts {
		var privs []string
		for _, p := range a.Privileges {
			if slices.Contains(pgDatabasePrivileges, p) && !slices.Contains(privs, p) {
				privs = append(privs, p)
			}
		}
		if len(privs) == 0 {
			continue
		}
		if err := CheckPGAccount(a); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "GRANT %s ON DATABASE %s TO %s;\n", strings.Join(privs, ", "), ident(db), ident(a.User))
	}
	return b.String(), nil
}

// pgDatabasePrivileges zijn de rechten op een database zelf.
var pgDatabasePrivileges = []string{"CONNECT", "CREATE", "TEMPORARY"}

// PGMigrateLock laat iedereen die met db verbindt alleen nog lezen, of
// haalt dat er weer af. Het geldt voor nieuwe verbindingen.
func PGMigrateLock(db string, lock bool) (string, error) {
	if err := CheckPGDatabase(db); err != nil {
		return "", err
	}
	if lock {
		return fmt.Sprintf("ALTER DATABASE %s SET default_transaction_read_only = on;\n", ident(db)), nil
	}
	return fmt.Sprintf("ALTER DATABASE %s RESET default_transaction_read_only;\n", ident(db)), nil
}

// PGMigrateCreate maakt de nieuwe database met de encoding van de oude,
// uit template0 zodat er niets in staat, met owner als eigenaar (leeg is
// postgres). Een andere encoding dan UTF8 krijgt de locale C, want de
// locale van het cluster is UTF-8. Zonder IF NOT EXISTS, zodat het mislukt
// als ze er al is.
func PGMigrateCreate(db, owner, encoding string) (string, error) {
	if err := CheckPGDatabase(db); err != nil {
		return "", err
	}
	if encoding == "" {
		encoding = "UTF8"
	}
	if !pgEncodingRe.MatchString(encoding) {
		return "", fmt.Errorf("%q is geen encoding", encoding)
	}
	q := "CREATE DATABASE " + ident(db)
	if owner != "" {
		if err := CheckPGUser(owner); err != nil {
			return "", err
		}
		q += " OWNER " + ident(owner)
	}
	q += fmt.Sprintf(" ENCODING '%s' TEMPLATE template0", encoding)
	if encoding != "UTF8" {
		q += " LC_COLLATE 'C' LC_CTYPE 'C'"
	}
	return q + ";\n", nil
}

// PGTable is een tabel in een schema.
type PGTable struct {
	Schema, Name string
}

// String is de naam voor mensen: zonder schema als dat public is.
func (t PGTable) String() string {
	if t.Schema == "public" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

// PGQuoteIdent zet een naam tussen dubbele aanhalingstekens; een
// aanhalingsteken erin wordt verdubbeld.
func PGQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// PGCountRows telt de rijen van tables: per groep van countChunk tabellen
// een query die één rij met de aantallen geeft, in dezelfde volgorde.
func PGCountRows(tables []PGTable) ([]string, error) {
	var out []string
	for i := 0; i < len(tables); i += countChunk {
		parts := make([]string, 0, countChunk)
		for _, t := range tables[i:min(i+countChunk, len(tables))] {
			if t.Schema == "" || t.Name == "" || strings.ContainsRune(t.Schema+t.Name, 0) {
				return nil, errors.New("een tabel zonder naam")
			}
			parts = append(parts, fmt.Sprintf("(SELECT count(*) FROM %s.%s)", PGQuoteIdent(t.Schema), PGQuoteIdent(t.Name)))
		}
		out = append(out, "SELECT "+strings.Join(parts, ", "))
	}
	return out, nil
}
