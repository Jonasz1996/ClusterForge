package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const dbTestPassword = "Wq7rT2kLm9Xp4Zs8Vb3N"

func usersAgent(h *agenttest.Host) *Agent {
	return &Agent{Exec: h.Exec, ExecStdin: h.ExecStdin, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func syncedMaria() *agenttest.MariaDB {
	return &agenttest.MariaDB{
		Version: "11.8.3-MariaDB-0+deb13u1", MaxConnections: 151,
		Galera: &agenttest.Galera{ClusterName: "nextcloud", Status: "Primary", Size: 3, State: 4, Comment: "Synced", Ready: true},
		Users:  agenttest.DebianUsers(),
	}
}

func dbRequest(t *testing.T, a *Agent, req protocol.DBRequest) protocol.DBResponse {
	t.Helper()
	req.ID = "r1"
	body, _ := json.Marshal(req)
	data, _ := json.Marshal(protocol.Envelope{Type: protocol.TypeDBRequest, Body: body})
	return a.handleDB(data)
}

func TestDBEnsureAndUsers(t *testing.T) {
	h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	h.MariaDB = syncedMaria()
	a := usersAgent(h)

	res := dbRequest(t, a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		Database: "next_cloud", User: "nextcloud", Hosts: []string{"10.0.20.186", "10.0.20.185"}, Password: dbTestPassword, Grant: true,
	}})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	// Het wachtwoord ging via stdin, niet als argument.
	for _, c := range h.Calls() {
		if strings.Contains(c, dbTestPassword) {
			t.Errorf("wachtwoord in een argument: %s", c)
		}
	}
	if in := h.Stdins(); len(in) != 1 || !strings.Contains(in[0], "IDENTIFIED BY '"+dbTestPassword+"'") {
		t.Errorf("stdin: %q", in)
	}
	if !slices.Contains(h.Calls(), "mariadb --batch --skip-print-query-on-error") {
		t.Errorf("aanroepen: %q", h.Calls())
	}

	res = dbRequest(t, a, protocol.DBRequest{Op: protocol.DBUsers})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	var nc []protocol.DBUser
	for _, u := range res.Users {
		if u.Name == "nextcloud" {
			nc = append(nc, u)
		}
	}
	if len(nc) != 2 || nc[0].Host != "10.0.20.185" || nc[1].Host != "10.0.20.186" {
		t.Fatalf("gebruikers: %+v", res.Users)
	}
	// In de rechten staat next\_cloud; de agent zegt next_cloud.
	if g := nc[0].Databases; len(g) != 1 || g[0].Database != "next_cloud" || len(g[0].Privileges) != len(agenttest.AllPrivileges) || len(nc[0].Global) != 0 {
		t.Errorf("rechten: %+v", nc[0])
	}
	if i := slices.IndexFunc(res.Users, func(u protocol.DBUser) bool { return u.Name == "root" }); i < 0 || len(res.Users[i].Global) == 0 {
		t.Errorf("root: %+v", res.Users)
	}
	if b, _ := json.Marshal(res); bytes.Contains(b, []byte(dbTestPassword)) {
		t.Errorf("wachtwoord in het antwoord: %s", b)
	}

	// Een nieuw wachtwoord.
	const pw2 = "Nieuw2wachtwoord3dat4lang5is"
	res = dbRequest(t, a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		User: "nextcloud", Hosts: []string{"10.0.20.186", "10.0.20.185"}, Password: pw2,
	}})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	for _, u := range h.MariaDB.Users {
		if u.User == "nextcloud" && u.Password != pw2 {
			t.Errorf("wachtwoord van %s@%s: %q", u.User, u.Host, u.Password)
		}
	}

	// Weghalen, alleen met de naam.
	res = dbRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{Database: "next_cloud", Confirm: "nextcloud"}})
	if !strings.Contains(res.Error, "niet de naam") {
		t.Errorf("verkeerde bevestiging: %q", res.Error)
	}
	res = dbRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{Database: "next_cloud", Confirm: "next_cloud"}})
	if res.Error != "" || len(h.MariaDB.Databases) != 0 {
		t.Errorf("database weghalen: %q %+v", res.Error, h.MariaDB.Databases)
	}
	res = dbRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{
		User: "nextcloud", Hosts: []string{"10.0.20.186", "10.0.20.185"}, Confirm: "nextcloud",
	}})
	if res.Error != "" || slices.ContainsFunc(h.MariaDB.Users, func(u agenttest.MariaUser) bool { return u.User == "nextcloud" }) {
		t.Errorf("gebruiker weghalen: %q %+v", res.Error, h.MariaDB.Users)
	}
}

func TestDBEnsureRefuses(t *testing.T) {
	h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	h.MariaDB = syncedMaria()
	a := usersAgent(h)
	ensure := func(req protocol.DBEnsureRequest) string {
		return dbRequest(t, a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &req}).Error
	}

	// Ongeldige namen komen niet bij MariaDB.
	if err := ensure(protocol.DBEnsureRequest{Database: "mysql"}); !strings.Contains(err, "van MariaDB zelf") {
		t.Errorf("mysql: %q", err)
	}
	if err := dbRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{User: "root", Hosts: []string{"localhost"}, Confirm: "root"}}).Error; !strings.Contains(err, "van MariaDB zelf") {
		t.Errorf("root: %q", err)
	}
	if len(h.Stdins()) != 0 {
		t.Errorf("stdin: %q", h.Stdins())
	}

	// Een node die niet Synced is, weigert, zoals MariaDB.
	h.Locked(func() { h.MariaDB.Galera.State, h.MariaDB.Galera.Comment = 1, "Joining" })
	if err := ensure(protocol.DBEnsureRequest{Database: "zabbix"}); !strings.HasPrefix(err, "ERROR 1047") {
		t.Errorf("Joining: %q", err)
	}
	h.Locked(func() { h.MariaDB.Galera.State, h.MariaDB.Galera.Comment = 4, "Synced" })

	// mariadb herhaalt een mislukte opdracht; alleen de regel met ERROR
	// komt terug, zonder wachtwoord.
	h.Fail("mariadb --batch", "--------------\nCREATE USER 'app'@'localhost' IDENTIFIED BY '"+dbTestPassword+"'\n--------------\n\n"+
		"ERROR 1396 (HY000) at line 1: Operation CREATE USER failed for 'app'@'localhost' ("+dbTestPassword+")\n")
	err := ensure(protocol.DBEnsureRequest{User: "app", Hosts: []string{"localhost"}, Password: dbTestPassword})
	if err != "ERROR 1396 (HY000) at line 1: Operation CREATE USER failed for 'app'@'localhost' (********)" {
		t.Errorf("fout: %q", err)
	}

	// Zonder MariaDB.
	h2 := agenttest.NewHost(t.TempDir(), "10.0.20.187")
	if err := dbRequest(t, usersAgent(h2), protocol.DBRequest{Op: protocol.DBUsers}).Error; err != "op deze node staat geen MariaDB" {
		t.Errorf("zonder MariaDB: %q", err)
	}
}

// TestDBUsersRealMariaDB maakt een database en gebruiker aan in de echte
// MariaDB in de container CF_TEST_MARIADB, logt als die gebruiker in, en
// haalt alles weer weg.
func TestDBUsersRealMariaDB(t *testing.T) {
	container := os.Getenv("CF_TEST_MARIADB")
	if container == "" {
		t.Skip("CF_TEST_MARIADB niet gezet")
	}
	const db, user = "cf_agent_m6", "cf_agent_m6"
	// as logt in als user met wachtwoord pw via de socket.
	as := func(pw, query string) error {
		out, err := exec.Command("docker", "exec", "-e", "MYSQL_PWD="+pw, container, "mariadb", "-u", user, "-e", query, db).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
		return nil
	}
	a := &Agent{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			return []byte("Id=mariadb.service\nLoadState=loaded\nActiveState=active\n\n"), nil
		case "mariadb":
			out, err := exec.CommandContext(ctx, "docker", append([]string{"exec", container, "mariadb"}, args...)...).Output()
			if ee, ok := err.(*exec.ExitError); ok {
				err = fmt.Errorf("%w: %s", err, ee.Stderr)
			}
			return out, err
		}
		return nil, fmt.Errorf("onbekend commando %s", name)
	}
	a.ExecStdin = func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", container, name}, args...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		return cmd.CombinedOutput()
	}
	hosts := []string{"localhost", "10.0.20.185"}
	drop := func() {
		dbRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{User: user, Hosts: hosts, Confirm: user}})
		dbRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{Database: db, Confirm: db}})
	}
	drop()
	t.Cleanup(drop)

	res := dbRequest(t, a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		Database: db, User: user, Hosts: hosts, Password: dbTestPassword, Grant: true,
	}})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if err := as(dbTestPassword, "CREATE TABLE t (id int PRIMARY KEY); INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("inloggen als %s: %v", user, err)
	}
	// Met \_ in de GRANT geldt het recht alleen voor deze database, niet
	// voor cf_agentXm6.
	if err := as(dbTestPassword, "CREATE DATABASE cf_agentXm6"); err == nil {
		t.Error("de gebruiker mocht een andere database maken")
		_ = as(dbTestPassword, "DROP DATABASE cf_agentXm6")
	}

	res = dbRequest(t, a, protocol.DBRequest{Op: protocol.DBUsers})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	var got []protocol.DBUser
	for _, u := range res.Users {
		if u.Name == user {
			got = append(got, u)
		}
	}
	if len(got) != 2 || got[0].Host != "10.0.20.185" || got[1].Host != "localhost" {
		t.Fatalf("gebruikers: %+v", res.Users)
	}
	for _, u := range got {
		if len(u.Databases) != 1 || u.Databases[0].Database != db || !slices.Contains(u.Databases[0].Privileges, "SELECT") ||
			!slices.Contains(u.Databases[0].Privileges, "TRIGGER") || len(u.Global) != 0 {
			t.Errorf("rechten van %s@%s: %+v", u.Name, u.Host, u)
		}
	}
	b, _ := json.Marshal(res)
	if bytes.Contains(b, []byte(dbTestPassword)) || regexp.MustCompile(`\*[0-9A-F]{40}`).Match(b) {
		t.Errorf("wachtwoord of hash in het antwoord: %s", b)
	}

	const pw2 = "Nieuw2wachtwoord3dat4lang5is"
	res = dbRequest(t, a, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{User: user, Hosts: hosts, Password: pw2}})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if err := as(pw2, "SELECT * FROM t"); err != nil {
		t.Errorf("nieuw wachtwoord: %v", err)
	}
	if err := as(dbTestPassword, "SELECT 1"); err == nil {
		t.Error("het oude wachtwoord werkt nog")
	}

	// Een mislukte opdracht: alleen de regel met ERROR, zonder wachtwoord.
	if out, err := exec.Command("docker", "exec", container, "mariadb", "-e", "CREATE USER 'cf_agent_ro'@'localhost'; GRANT SELECT ON *.* TO 'cf_agent_ro'@'localhost'").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "exec", container, "mariadb", "-e", "DROP USER IF EXISTS 'cf_agent_ro'@'localhost'").Run()
	})
	ro := &Agent{Log: a.Log, Exec: a.Exec}
	ro.ExecStdin = func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", container, name, "-u", "cf_agent_ro"}, args...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		return cmd.CombinedOutput()
	}
	res = dbRequest(t, ro, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{User: user, Hosts: hosts, Password: dbTestPassword}})
	if !strings.HasPrefix(res.Error, "ERROR ") || strings.Contains(res.Error, dbTestPassword) || strings.Contains(res.Error, "\n") {
		t.Errorf("fout: %q", res.Error)
	}

	res = dbRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{Database: db, Confirm: db}})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	res = dbRequest(t, a, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{User: user, Hosts: hosts, Confirm: user}})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if err := as(pw2, "SELECT 1"); err == nil {
		t.Error("de gebruiker kan nog inloggen")
	}
}
