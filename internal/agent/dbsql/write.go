package dbsql

// De opdrachten die iets in MariaDB veranderen. Ze komen alleen uit deze
// functies: namen voldoen aan protocol.DBNamePattern, een host bevat geen
// aanhalingsteken, en een wachtwoord alleen letters en cijfers. De agent
// stuurt ze via stdin naar mariadb, zodat een wachtwoord nooit als
// argument in ps staat.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var (
	nameRe = regexp.MustCompile(protocol.DBNamePattern)
	// Een host is een IP-adres, localhost, of een patroon met % en _ zoals
	// 10.0.20.%.
	hostRe     = regexp.MustCompile(`^(localhost|[0-9A-Fa-f.:%_]{1,60})$`)
	passwordRe = regexp.MustCompile(`^[A-Za-z0-9]{16,128}$`)
)

// SystemDatabases en SystemUsers zijn van MariaDB zelf; de agent maakt ze
// niet aan en haalt ze niet weg.
var (
	SystemDatabases = []string{"information_schema", "mysql", "performance_schema", "sys"}
	SystemUsers     = []string{"mariadb.sys", "mysql", "root"}
)

// CheckDatabase zegt waarom een naam geen database voor ClusterForge kan
// zijn.
func CheckDatabase(name string) error {
	switch {
	case !nameRe.MatchString(name):
		return fmt.Errorf("de naam van een database is 1 tot 63 kleine letters, cijfers of _, niet %q", name)
	case slices.Contains(SystemDatabases, name):
		return fmt.Errorf("%s is een database van MariaDB zelf", name)
	}
	return nil
}

// CheckUser zegt waarom een naam geen gebruiker voor ClusterForge kan
// zijn.
func CheckUser(name string) error {
	switch {
	case !nameRe.MatchString(name):
		return fmt.Errorf("de naam van een gebruiker is 1 tot 63 kleine letters, cijfers of _, niet %q", name)
	case slices.Contains(SystemUsers, name):
		return fmt.Errorf("%s is een gebruiker van MariaDB zelf", name)
	}
	return nil
}

func checkHosts(hosts []string) error {
	if len(hosts) == 0 {
		return errors.New("een gebruiker heeft minstens één host nodig")
	}
	for _, h := range hosts {
		if !hostRe.MatchString(h) {
			return fmt.Errorf("%q is geen host voor een gebruiker: een IP-adres, localhost of een patroon met %%", h)
		}
	}
	return nil
}

// quoteDB zet een databasenaam tussen backticks. In GRANT is _ een
// jokerteken, dus daar krijgt hij een backslash.
func quoteDB(name string, grant bool) string {
	if grant {
		name = strings.ReplaceAll(name, "_", `\_`)
	}
	return "`" + name + "`"
}

func account(user, host string) string { return "'" + user + "'@'" + host + "'" }

// Ensure geeft de opdrachten voor req, een per regel.
func Ensure(req protocol.DBEnsureRequest) (string, error) {
	var b strings.Builder
	if req.Database != "" {
		if err := CheckDatabase(req.Database); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4;\n", quoteDB(req.Database, false))
	}
	switch {
	case req.User == "" && (req.Password != "" || req.Grant || len(req.Hosts) > 0):
		return "", errors.New("een wachtwoord, host of recht zonder gebruiker")
	case req.Grant && req.Database == "":
		return "", errors.New("rechten zonder database")
	case req.User == "" && req.Database == "":
		return "", errors.New("er is niets om aan te maken")
	}
	if req.User != "" {
		if err := CheckUser(req.User); err != nil {
			return "", err
		}
		if err := checkHosts(req.Hosts); err != nil {
			return "", err
		}
		if req.Password != "" && !passwordRe.MatchString(req.Password) {
			return "", errors.New("een wachtwoord is 16 tot 128 letters en cijfers")
		}
		for _, h := range req.Hosts {
			acc := account(req.User, h)
			if req.Password != "" {
				fmt.Fprintf(&b, "CREATE USER IF NOT EXISTS %s IDENTIFIED BY '%s';\n", acc, req.Password)
				fmt.Fprintf(&b, "ALTER USER %s IDENTIFIED BY '%s';\n", acc, req.Password)
			} else {
				fmt.Fprintf(&b, "CREATE USER IF NOT EXISTS %s;\n", acc)
			}
			if req.Grant {
				fmt.Fprintf(&b, "GRANT ALL PRIVILEGES ON %s.* TO %s;\n", quoteDB(req.Database, true), acc)
			}
		}
	}
	return b.String(), nil
}

// Drop geeft de opdrachten voor req: een database of een gebruiker weg,
// alleen als Confirm haar naam is.
func Drop(req protocol.DBDropRequest) (string, error) {
	switch {
	case req.Database != "" && req.User != "":
		return "", errors.New("een database en een gebruiker tegelijk weghalen kan niet")
	case req.Database != "":
		if err := CheckDatabase(req.Database); err != nil {
			return "", err
		}
		if req.Confirm != req.Database {
			return "", fmt.Errorf("de bevestiging %q is niet de naam van de database %s", req.Confirm, req.Database)
		}
		return fmt.Sprintf("DROP DATABASE IF EXISTS %s;\n", quoteDB(req.Database, false)), nil
	case req.User != "":
		if err := CheckUser(req.User); err != nil {
			return "", err
		}
		if err := checkHosts(req.Hosts); err != nil {
			return "", err
		}
		if req.Confirm != req.User {
			return "", fmt.Errorf("de bevestiging %q is niet de naam van de gebruiker %s", req.Confirm, req.User)
		}
		var b strings.Builder
		for _, h := range req.Hosts {
			fmt.Fprintf(&b, "DROP USER IF EXISTS %s;\n", account(req.User, h))
		}
		return b.String(), nil
	}
	return "", errors.New("er is niets om weg te halen")
}
