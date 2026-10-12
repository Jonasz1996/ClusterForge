package components

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// collect verzamelt bevindingen als niveau:code, met het bericht en het
// commando per code.
type collect struct {
	codes    []string
	messages map[string]string
	commands map[string]string
}

func (c *collect) add(level, code string, _ *Node, msg, command string) {
	if c.messages == nil {
		c.messages, c.commands = map[string]string{}, map[string]string{}
	}
	c.codes = append(c.codes, level+":"+code)
	c.messages[code], c.commands[code] = msg, command
}

func TestCheckPGSource(t *testing.T) {
	sn := &Node{Hostname: "db1"}
	var c collect
	checkPGSource(&protocol.DBMigrateInfo{Owner: "postgres", OwnerSuper: true, Unreadable: []string{"public.x"}}, "wiki", sn, c.add)
	if strings.Join(c.codes, " ") != "blocked:owner_super" || !strings.Contains(c.messages["owner_super"], "postgres") {
		t.Errorf("superuser: %v %v", c.codes, c.messages)
	}

	c = collect{}
	checkPGSource(&protocol.DBMigrateInfo{Owner: "wiki", Unreadable: []string{"public.geheim", "public.geheim_id_seq", "audit.log"}, Settings: 2}, "wiki", sn, c.add)
	if strings.Join(c.codes, " ") != "blocked:unreadable info:settings" {
		t.Fatalf("niet te lezen: %v", c.codes)
	}
	want := "sudo -u postgres psql -X -d wiki <<'SQL'\nGRANT USAGE ON SCHEMA public TO \"wiki\";\nGRANT SELECT ON public.geheim TO \"wiki\";\n" +
		"GRANT SELECT ON public.geheim_id_seq TO \"wiki\";\nGRANT USAGE ON SCHEMA audit TO \"wiki\";\nGRANT SELECT ON audit.log TO \"wiki\";\nSQL"
	if c.commands["unreadable"] != want {
		t.Errorf("commando:\n%s\nwil\n%s", c.commands["unreadable"], want)
	}
	if !strings.Contains(c.messages["settings"], "2 instellingen staan") {
		t.Errorf("instellingen: %s", c.messages["settings"])
	}

	// Een naam die psql tussen aanhalingstekens nodig heeft: geen commando.
	c = collect{}
	checkPGSource(&protocol.DBMigrateInfo{Owner: "wiki", Unreadable: []string{`Data.Mixed "Case"`}}, "wiki", sn, c.add)
	if c.commands["unreadable"] != "" || !strings.Contains(c.messages["unreadable"], `Data.Mixed "Case"`) {
		t.Errorf("rare naam: %q %q", c.commands["unreadable"], c.messages["unreadable"])
	}
}

func TestCheckPGReach(t *testing.T) {
	p := &MigrationPlan{
		Database: "nextcloud",
		Source:   MigrationSource{Name: "db-cluster", Address: "10.0.20.180", Via: ViaVIP},
		Target:   &MigrationTarget{ClusterName: "Nextcloud", Node: Node{Hostname: "nc1"}, Hosts: []string{"10.0.20.186"}},
		Reach:    &protocol.DBMigrateReach{Reached: true, Code: protocol.DBMigrateNoEntry, Seen: "10.0.20.181", Extensions: []string{"plpgsql"}},
	}
	info := &protocol.DBMigrateInfo{Extensions: []string{"pg_trgm"}}
	var c collect
	checkPGReach(p, info, "10.0.20.181", Node{Hostname: "db1"}, []string{"10.0.20.181: connection refused"}, c.add)
	if strings.Join(c.codes, " ") != "info:via_vip blocked:pg_hba blocked:extensions" {
		t.Fatalf("bevindingen: %v", c.codes)
	}
	// Achter het VIP staat HAProxy op db1: de oude server ziet dat adres.
	if !strings.Contains(c.commands["pg_hba"], `echo "host nextcloud all 10.0.20.181/32 scram-sha-256"`) ||
		!strings.Contains(c.messages["pg_hba"], "ziet nc1 als 10.0.20.181") || !strings.Contains(c.messages["pg_hba"], "patronictl edit-config") {
		t.Errorf("pg_hba: %s | %s", c.messages["pg_hba"], c.commands["pg_hba"])
	}
	if !strings.Contains(c.messages["extensions"], "de extensie pg_trgm") || !strings.Contains(c.messages["extensions"], "op nc1 is die niet te installeren") {
		t.Errorf("extensies: %s", c.messages["extensions"])
	}

	// IPv6, en zonder lijst van extensies geen oordeel erover.
	p.Source.Via, p.Reach.Seen, p.Reach.Extensions = ViaNode, "fd00::186", nil
	c = collect{}
	checkPGReach(p, info, "10.0.20.181", Node{Hostname: "db1"}, nil, c.add)
	if strings.Join(c.codes, " ") != "blocked:pg_hba" || !strings.Contains(c.commands["pg_hba"], "fd00::186/128") {
		t.Errorf("IPv6: %v %s", c.codes, c.commands["pg_hba"])
	}

	p.Reach = &protocol.DBMigrateReach{Reached: true, Code: 1, Error: "FATAL: too many connections"}
	c = collect{}
	checkPGReach(p, info, "10.0.20.181", Node{Hostname: "db1"}, nil, c.add)
	if strings.Join(c.codes, " ") != "blocked:refused" || !strings.Contains(c.messages["refused"], "too many connections") {
		t.Errorf("geweigerd: %v %v", c.codes, c.messages)
	}
}

func TestCheckPGVersions(t *testing.T) {
	for _, tc := range []struct{ source, target, want string }{
		{"15.8 (Debian 15.8-0+deb12u1)", "17.6", "info:upgrade"},
		{"17.6 (Debian 17.6-1.pgdg13+1)", "17.2", ""},
		{"18.0", "17.6 (Debian 17.6-1.pgdg13+1)", "blocked:downgrade"},
		{"onbekend", "17.6", ""},
	} {
		var c collect
		checkPGVersions(tc.source, tc.target, Node{Hostname: "db1"}, Node{Hostname: "nc1"}, c.add)
		if got := strings.Join(c.codes, " "); got != tc.want {
			t.Errorf("%s naar %s: %q, wil %q", tc.source, tc.target, got, tc.want)
		}
	}
}

func TestCheckPGAccounts(t *testing.T) {
	acc := func(user, plugin string, privileges ...string) protocol.DBMigrateAccount {
		return protocol.DBMigrateAccount{User: user, Plugin: plugin, Privileges: privileges}
	}
	super := acc("beheer", "scram-sha-256", "OBJECTS")
	super.Super, super.Global = true, true
	creator := acc("deploy", "scram-sha-256", "CONNECT")
	creator.Global = true
	accounts := []protocol.DBMigrateAccount{
		acc("nextcloud", "scram-sha-256", "OWNER"), acc("postgres", "scram-sha-256", "OBJECTS"), acc("rapport", "md5", "CONNECT"),
		super, creator, acc("monitor", "none", "CONNECT"), acc("Raar", "scram-sha-256", "CONNECT"), acc("ldap", "other", "OBJECTS"),
		acc("cf_verhuizing_0123456789abcdef", "scram-sha-256"),
	}
	clients := []databases.User{{DBUsers: []string{"nextcloud", "rapport", "oud", "postgres"}}}
	var c collect
	checkPGAccounts(accounts, clients, "nextcloud", c.add)
	if got := strings.Join(c.codes, " "); got != "blocked:accounts_md5 blocked:accounts_blocked warning:accounts_other warning:accounts_no_password "+
		"info:accounts_global warning:accounts_missing" {
		t.Fatalf("bevindingen: %s", got)
	}
	if !strings.Contains(c.commands["accounts_md5"], `\password rapport`) {
		t.Errorf("md5: %q", c.commands["accounts_md5"])
	}
	for code, want := range map[string][]string{
		"accounts_blocked":     {"beheer is superuser", "ldap logt in"},
		"accounts_other":       {`"Raar"`},
		"accounts_no_password": {"monitor"},
		"accounts_global":      {"deploy"},
		"accounts_missing":     {"oud gebruikte nextcloud"},
	} {
		for _, w := range want {
			if !strings.Contains(c.messages[code], w) {
				t.Errorf("%s mist %q: %s", code, w, c.messages[code])
			}
		}
	}
	if slices.ContainsFunc(c.codes, func(s string) bool {
		return strings.Contains(c.messages[strings.SplitN(s, ":", 2)[1]], "cf_verhuizing_")
	}) {
		t.Errorf("de tijdelijke gebruiker staat in een bevinding: %v", c.messages)
	}
	if !Movable(protocol.DBPostgreSQL, accounts[0]) || Movable(protocol.DBPostgreSQL, accounts[2]) || Movable(protocol.DBPostgreSQL, super) {
		t.Errorf("Movable")
	}
}

func TestCheckPGExisting(t *testing.T) {
	accounts := []protocol.DBMigrateAccount{
		{User: "nextcloud", Plugin: "scram-sha-256", Privileges: []string{"OWNER"}}, {User: "grp", Plugin: "nologin", Privileges: []string{"OBJECTS"}},
		{User: "monitor", Plugin: "none", Privileges: []string{"CONNECT"}}, {User: "rapport", Plugin: "md5", Privileges: []string{"CONNECT"}},
		{User: "nieuw", Plugin: "scram-sha-256", Privileges: []string{"CONNECT"}},
	}
	existing := []protocol.DBUser{{Name: "nextcloud"}, {Name: "grp"}, {Name: "monitor"}, {Name: "rapport"}, {Name: "postgres"}}
	var c collect
	checkPGExisting(accounts, existing, "Nextcloud", c.add)
	if strings.Join(c.codes, " ") != "warning:accounts_existing" ||
		c.messages["accounts_existing"] != "nextcloud staat al op Nextcloud en krijgt bij de verhuizing het wachtwoord van de oude server. "+
			"Gebruikt iets op Nextcloud hem nu met een ander wachtwoord, zet dat daar dan op het wachtwoord van de oude server" {
		t.Errorf("bestaande rollen: %v %v", c.codes, c.messages)
	}
	c = collect{}
	checkPGExisting(accounts, nil, "Nextcloud", c.add)
	if len(c.codes) != 0 {
		t.Errorf("zonder bestaande rollen: %v", c.codes)
	}
}

func TestPGMigrateProtocol(t *testing.T) {
	var ce *ConflictError
	if err := pgMigrateProtocol("nc1", protocol.DBPGMigrateSince-1); !errors.As(err, &ce) || !strings.Contains(err.Error(), "protocol 10") ||
		!strings.Contains(err.Error(), "--upgrade") {
		t.Errorf("protocol 10: %v", err)
	}
	if err := pgMigrateProtocol("nc1", protocol.DBPGMigrateSince); err != nil {
		t.Errorf("protocol 11: %v", err)
	}
}

func TestMovePGAccounts(t *testing.T) {
	got := movePGAccounts([]protocol.DBMigrateAccount{
		{User: "nextcloud", Plugin: "scram-sha-256", Hash: "SCRAM-SHA-256$4096:c2FsdA==$a2V5:c2Vydg==", Privileges: []string{"OWNER"}, MemberOf: []string{"grp"}},
		{User: "grp", Plugin: "nologin", Privileges: []string{"OBJECTS"}},
		{User: "rapport", Plugin: "md5", Privileges: []string{"CONNECT"}},
	})
	if len(got) != 2 || got[0].User != "nextcloud" || got[0].Old[0].Hash != "" || !slices.Equal(got[0].Old[0].MemberOf, []string{"grp"}) ||
		len(got[0].Hosts) != 0 || got[1].User != "grp" || got[0].New {
		t.Fatalf("rollen: %+v", got)
	}
	got[1].New = true
	if !got[1].created() || got[0].created() {
		t.Errorf("created")
	}
	if engineOfKey("postgresql/node/abc") != protocol.DBPostgreSQL || engineOfKey("mariadb/123") != protocol.DBMariaDB || engineOfKey("") != protocol.DBMariaDB {
		t.Errorf("engineOfKey")
	}
}
