package pbs

import (
	"strings"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	cases := []struct {
		method, path string
		params       map[string]string
		ok           bool
		title        string
		confirm      string
	}{
		{"create", "/admin/datastore/store1/gc", nil, true, "garbage collection op store1", ""},
		{"create", "/admin/datastore/store1/gc", map[string]string{"x": "1"}, false, "", ""},
		{"delete", "/config/datastore/store1", map[string]string{"destroy-data": "1"}, true, "datastore store1 verwijderen met alle back-ups", "store1"},
		{"delete", "/admin/datastore/store1/groups", map[string]string{"backup-type": "vm", "backup-id": "101"}, true, "groep vm/101 met alle snapshots verwijderen", "101"},
		{"create", "/admin/sync/s-1a2b/run", nil, true, "sync-job s-1a2b nu uitvoeren", ""},
		{"delete", `/nodes/localhost/tasks/UPID:pbs:000012AB:0001A2B3:00000003:65F0A1B2:verificationjob:store1\x3av\x2d1:root@pam:`, nil, true, "PBS-taak stoppen", ""},
		{"create", "/nodes/localhost/status", map[string]string{"command": "reboot"}, true, "PBS herstarten", "localhost"},
		{"create", "/access/users/cf@pbs/token/pve", nil, true, "API-token cf@pbs!pve maken", ""},
		{"set", "/nodes/localhost/apt/update", nil, false, "", ""},
		{"set", "/access/acl", map[string]string{"path": "/datastore/store1", "role": "DatastoreBackup", "auth-id": "pve@pbs!pve"}, true, "recht DatastoreBackup op /datastore/store1 geven aan pve@pbs!pve", ""},
		{"set", "/nodes/localhost/disks/wipedisk", map[string]string{"disk": "sdb"}, true, "schijf sdb wissen", "sdb"},
		{"set", "/nodes/localhost/network", nil, true, "netwerkwijzigingen van PBS toepassen", "netwerk"},
		{"create", "/nodes/localhost/services/proxmox-backup-proxy/restart", nil, true, "dienst proxmox-backup-proxy herstarten", ""},
		{"create", "/nodes/localhost/services/ssh;rm/restart", nil, false, "", ""},
		{"create", "/admin/datastore/store1/namespace", map[string]string{"name": "prod"}, true, "namespace prod in store1", ""},
		{"delete", "/admin/datastore/store1/namespace", map[string]string{"ns": "prod", "delete-groups": "1"}, true, "namespace prod verwijderen uit store1", "prod"},
		{"get", "/admin/datastore", nil, false, "", ""},
	}
	for _, c := range cases {
		a, err := check(c.method, c.path, c.params)
		if (err == nil) != c.ok {
			t.Errorf("%s %s: err %v, ok verwacht %v", c.method, c.path, err, c.ok)
			continue
		}
		if err == nil && (a.Title != c.title || a.Confirm != c.confirm) {
			t.Errorf("%s %s: %q/%q, verwacht %q/%q", c.method, c.path, a.Title, a.Confirm, c.title, c.confirm)
		}
	}
	if a, _ := check("create", "/access/users/cf@pbs/token/pve", nil); !a.Quiet {
		t.Error("een nieuw token moet verborgen blijven")
	}
	if a, _ := check("create", "/config/remote", map[string]string{"name": "x", "password": "geheim"}); len(a.Secret) != 1 {
		t.Error("het wachtwoord van een remote is geheim")
	}
}

func TestReadDenied(t *testing.T) {
	for p, want := range map[string]bool{"/access/users": true, "/config/remote": true, "/config/remote/x": true, "/config/remotes": false, "/admin/datastore": false} {
		if readDenied(p) != want {
			t.Errorf("%s: %v verwacht", p, want)
		}
	}
}

func TestWarnings(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	o := Overview{
		Datastores: []Datastore{{Store: "a", Total: 100, Used: 96}, {Store: "b", Total: 100, Used: 10, EstimatedFull: now.Add(10 * 24 * time.Hour).Unix()}},
		Jobs:       []Job{{Kind: "verify", ID: "v1", Store: "a", LastState: "error: chunk missing"}, {Kind: "gc", Store: "b"}},
		Failed:     []Task{{WorkerType: "backup"}, {WorkerType: "backup"}},
	}
	w := warnings(o, now)
	text := ""
	for _, x := range w {
		text += x.Level + " " + x.Text + "\n"
	}
	for _, want := range []string{"crit Datastore a is 96% vol", "crit Datastore b is volgens de trend over 10 dagen vol", "crit Verify-job v1 (a) mislukte", "warn Datastore b heeft geen schema voor garbage collection", "warn Datastore b heeft geen verify-job", "crit 2 mislukte taken in het laatste etmaal: 2× back-up"} {
		if !strings.Contains(text, want) {
			t.Errorf("mist %q in\n%s", want, text)
		}
	}
	if w[len(w)-1].Level != "warn" {
		t.Error("ernstige eerst")
	}
}

func TestJudge(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		n     NodeCoverage
		state string
		why   string
	}{
		{NodeCoverage{}, "crit", "geen back-up"},
		{NodeCoverage{LastBackup: now.Add(-2 * time.Hour).Unix(), Verify: "ok"}, "ok", ""},
		{NodeCoverage{LastBackup: now.Add(-2 * time.Hour).Unix()}, "ok", "nog niet geverifieerd"},
		{NodeCoverage{LastBackup: now.Add(-48 * time.Hour).Unix(), Verify: "ok"}, "warn", "48 uur oud"},
		{NodeCoverage{LastBackup: now.Add(-10 * 24 * time.Hour).Unix(), Verify: "ok"}, "crit", "10 dagen oud"},
		{NodeCoverage{LastBackup: now.Add(-2 * time.Hour).Unix(), Verify: "failed"}, "crit", "kapot"},
		{NodeCoverage{LastBackup: now.Add(-2 * time.Hour).Unix(), Verify: "ok", FailedVerify: 2}, "warn", "2 oudere"},
	}
	for i, c := range cases {
		judge(&c.n, now)
		if c.n.State != c.state || !strings.Contains(c.n.Why, c.why) {
			t.Errorf("%d: %s %q, verwacht %s %q", i, c.n.State, c.n.Why, c.state, c.why)
		}
	}
}
