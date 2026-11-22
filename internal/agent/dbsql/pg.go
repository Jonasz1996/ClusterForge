package dbsql

// De opdrachten die iets in PostgreSQL veranderen, en de query's voor de
// gebruikers en de dumps. Namen voldoen aan protocol.DBNamePattern en
// staan altijd tussen dubbele aanhalingstekens; een wachtwoord komt hier
// alleen als SCRAM-verifier, die de agent zelf uitrekent, zodat het
// wachtwoord nooit in een opdracht of het log van PostgreSQL staat. De
// agent stuurt de opdrachten via stdin naar psql, als postgres.

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De gebruikers van PostgreSQL en hun rechten op de databases: de
// eigenaar, en wat er expliciet op de database is gegeven (CONNECT,
// CREATE, TEMPORARY). Nooit een wachtwoord of verifier.
const (
	PGRoles = "SELECT rolname, rolsuper, rolcreatedb, rolcreaterole, rolreplication FROM pg_roles " +
		"WHERE rolcanlogin AND rolname !~ '^pg_' ORDER BY rolname"
	PGDatabaseGrants = "SELECT d.datname, r.rolname, 'OWNER' FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba " +
		"WHERE d.datallowconn AND NOT d.datistemplate AND r.rolcanlogin " +
		"UNION SELECT d.datname, r.rolname, a.privilege_type FROM pg_database d CROSS JOIN LATERAL aclexplode(d.datacl) a " +
		"JOIN pg_roles r ON r.oid = a.grantee WHERE d.datallowconn AND NOT d.datistemplate AND r.rolcanlogin ORDER BY 1, 2, 3"
	// PGDumpTables zijn de tabellen die pg_dump als TABLE schrijft: zonder
	// de catalogus en zonder de tabellen van een extensie.
	PGDumpTables = "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace " +
		"WHERE c.relkind IN ('r', 'p') AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%' " +
		"AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')"
)

// PGSystemDatabases en PGSystemUsers zijn van PostgreSQL en Patroni zelf;
// de agent maakt ze niet aan en haalt ze niet weg.
var (
	PGSystemDatabases = []string{"postgres", "template0", "template1"}
	PGSystemUsers     = []string{"postgres", "cf_replicatie", "cf_rewind"}
)

var (
	verifierRe = regexp.MustCompile(`^SCRAM-SHA-256\$[0-9]+:[A-Za-z0-9+/=]+\$[A-Za-z0-9+/=]+:[A-Za-z0-9+/=]+$`)
	tempRoleRe = regexp.MustCompile(protocol.DBTempRolePattern)
)

// CheckPGDatabase zegt waarom een naam geen database voor ClusterForge in
// PostgreSQL kan zijn.
func CheckPGDatabase(name string) error {
	switch {
	case !nameRe.MatchString(name):
		return fmt.Errorf("de naam van een database is 1 tot 63 kleine letters, cijfers of _, niet %q", name)
	case slices.Contains(PGSystemDatabases, name):
		return fmt.Errorf("%s is een database van PostgreSQL zelf", name)
	}
	return nil
}

// CheckPGUser zegt waarom een naam geen gebruiker voor ClusterForge in
// PostgreSQL kan zijn.
func CheckPGUser(name string) error {
	switch {
	case !nameRe.MatchString(name):
		return fmt.Errorf("de naam van een gebruiker is 1 tot 63 kleine letters, cijfers of _, niet %q", name)
	case slices.Contains(PGSystemUsers, name):
		return fmt.Errorf("%s is een gebruiker van PostgreSQL of Patroni zelf", name)
	case strings.HasPrefix(name, "pg_"):
		return fmt.Errorf("%s: namen met pg_ ervoor zijn van PostgreSQL zelf", name)
	}
	return nil
}

// CheckDatabaseOf en CheckUserOf kijken een naam na voor engine.
func CheckDatabaseOf(engine, name string) error {
	if engine == protocol.DBPostgreSQL {
		return CheckPGDatabase(name)
	}
	return CheckDatabase(name)
}

func CheckUserOf(engine, name string) error {
	if engine == protocol.DBPostgreSQL {
		return CheckPGUser(name)
	}
	return CheckUser(name)
}

// SystemUsersOf zijn de gebruikers van de database zelf.
func SystemUsersOf(engine string) []string {
	if engine == protocol.DBPostgreSQL {
		return PGSystemUsers
	}
	return SystemUsers
}

func ident(name string) string { return `"` + name + `"` }

// PGEnsure geeft de opdrachten voor req in psql, een per regel. verifier
// is de SCRAM-verifier van req.Password, of leeg zonder wachtwoord. Een
// gebruiker komt eerst, zodat hij eigenaar van een nieuwe database kan
// zijn. Hosts horen bij MariaDB; in PostgreSQL bepaalt pg_hba vanwaar een
// gebruiker binnenkomt.
func PGEnsure(req protocol.DBEnsureRequest, verifier string) (string, error) {
	switch {
	case req.User == "" && (req.Password != "" || req.Grant || req.Restore):
		return "", errors.New("een wachtwoord of recht zonder gebruiker")
	case req.Grant && req.Database == "":
		return "", errors.New("rechten zonder database")
	case req.User == "" && req.Database == "":
		return "", errors.New("er is niets om aan te maken")
	case req.Restore && (req.Database != "" || req.Grant || !tempRoleRe.MatchString(req.User)):
		return "", errors.New("een tijdelijke beheerder heet cf_herstel_ met 16 hexadecimale tekens, zonder database")
	case (req.Password == "") != (verifier == ""):
		return "", errors.New("een wachtwoord zonder verifier")
	case verifier != "" && !verifierRe.MatchString(verifier):
		return "", errors.New("de verifier van het wachtwoord klopt niet")
	case req.Password != "" && !passwordRe.MatchString(req.Password):
		return "", errors.New("een wachtwoord is 16 tot 128 letters en cijfers")
	}
	var b strings.Builder
	if req.User != "" {
		if !req.Restore {
			if err := CheckPGUser(req.User); err != nil {
				return "", err
			}
		}
		fmt.Fprintf(&b, "SELECT 'CREATE ROLE %s LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s')\\gexec\n", ident(req.User), req.User)
		switch {
		case req.Restore:
			fmt.Fprintf(&b, "SELECT format('ALTER ROLE %s WITH LOGIN SUPERUSER PASSWORD %%L VALID UNTIL %%L', '%s', now() + interval '%d hours')\\gexec\n",
				ident(req.User), verifier, int(protocol.DBTempRoleTTL.Hours()))
		case verifier != "":
			fmt.Fprintf(&b, "ALTER ROLE %s WITH LOGIN PASSWORD '%s';\n", ident(req.User), verifier)
		}
	}
	if req.Database != "" {
		if err := CheckPGDatabase(req.Database); err != nil {
			return "", err
		}
		owner := ""
		if req.Grant {
			owner = " OWNER " + ident(req.User)
		}
		fmt.Fprintf(&b, "SELECT 'CREATE DATABASE %s%s' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = '%s')\\gexec\n",
			ident(req.Database), owner, req.Database)
		if req.Grant {
			fmt.Fprintf(&b, "ALTER DATABASE %s OWNER TO %s;\n", ident(req.Database), ident(req.User))
		}
	}
	return b.String(), nil
}

// PGDrop geeft de opdracht voor req: een database of een gebruiker weg,
// alleen als Confirm haar naam is. Een tijdelijke beheerder mag zonder
// bevestiging weg. Een database met verbindingen of een gebruiker die nog
// iets bezit, weigert PostgreSQL zelf.
func PGDrop(req protocol.DBDropRequest) (string, error) {
	switch {
	case req.Database != "" && req.User != "":
		return "", errors.New("een database en een gebruiker tegelijk weghalen kan niet")
	case req.Database != "":
		if err := CheckPGDatabase(req.Database); err != nil {
			return "", err
		}
		if req.Confirm != req.Database {
			return "", fmt.Errorf("de bevestiging %q is niet de naam van de database %s", req.Confirm, req.Database)
		}
		return fmt.Sprintf("DROP DATABASE IF EXISTS %s;\n", ident(req.Database)), nil
	case req.User != "":
		if !tempRoleRe.MatchString(req.User) {
			if err := CheckPGUser(req.User); err != nil {
				return "", err
			}
			if req.Confirm != req.User {
				return "", fmt.Errorf("de bevestiging %q is niet de naam van de gebruiker %s", req.Confirm, req.User)
			}
		}
		return fmt.Sprintf("DROP ROLE IF EXISTS %s;\n", ident(req.User)), nil
	}
	return "", errors.New("er is niets om weg te halen")
}

// CheckPGLogin zegt waarom een agent met deze gegevens geen dump terug mag
// zetten op de primary: een IP-adres, een tijdelijke beheerder en een
// wachtwoord zoals ClusterForge ze maakt.
func CheckPGLogin(host, user, password string) error {
	switch {
	case net.ParseIP(host) == nil:
		return fmt.Errorf("het adres van de primary is geen IP-adres: %q", host)
	case !tempRoleRe.MatchString(user):
		return fmt.Errorf("%q is geen tijdelijke beheerder om terug te zetten", user)
	case !passwordRe.MatchString(password):
		return errors.New("het wachtwoord van de tijdelijke beheerder is 16 tot 128 letters en cijfers")
	}
	return nil
}

// PGDropExpired haalt de tijdelijke beheerders weg die verlopen zijn, als
// een terugzetten ze niet zelf opruimde.
const PGDropExpired = "SELECT format('DROP ROLE %I', rolname) FROM pg_roles " +
	"WHERE rolname ~ '^cf_herstel_[0-9a-f]{16}$' AND rolvaliduntil < now()\\gexec\n"

// PGDumpInfo leest of db bestaat (1 of 0) en hoe groot ze is.
func PGDumpInfo(db string) (string, error) {
	if err := CheckPGDatabase(db); err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT count(*), coalesce(sum(pg_database_size(oid)), 0) FROM pg_database WHERE datname = '%s'", db), nil
}

// PGRestoreCreate maakt de database om een dump van source in terug te
// zetten, met dezelfde eigenaar als source, of postgres als source er niet
// meer is. Zonder IF NOT EXISTS, zodat het mislukt als ze er al is.
func PGRestoreCreate(source, target string) (string, error) {
	if err := CheckPGDatabase(source); err != nil {
		return "", err
	}
	if err := CheckPGDatabase(target); err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT format('CREATE DATABASE %%I OWNER %%I', '%s', "+
		"coalesce((SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = '%s'), 'postgres'))\\gexec\n", target, source), nil
}

// PGRestoreFinish staat na de dump: wat de tijdelijke beheerder zelf
// maakte, zoals een extensie, gaat naar de eigenaar van de database, zodat
// hij daarna weg kan.
const PGRestoreFinish = "\nSELECT format('REASSIGN OWNED BY CURRENT_USER TO %I', pg_get_userbyid(datdba)) " +
	"FROM pg_database WHERE datname = current_database()\\gexec\n"
