package truenas

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	ok := []struct{ m, a, title string }{
		{"pool.scrub.scrub", `["tank","START"]`, "scrub van tank starten"},
		{"alert.dismiss", `["a1b2"]`, "melding wegklikken"},
		{"smart.test.manual_test", `[[{"identifier":"{serial}ABC","type":"SHORT"}]]`, "SMART-test short op {serial}ABC"},
		{"core.job_abort", `[42]`, "TrueNAS-taak 42 afbreken"},
		{"pool.snapshot.create", `[{"dataset":"tank/data","name":"cf-20261117-1300","recursive":true}]`, "snapshot tank/data@cf-20261117-1300 maken (met onderliggende datasets)"},
		{"pool.snapshot.delete", `["tank/data@auto-1"]`, "snapshot tank/data@auto-1 verwijderen"},
		{"pool.snapshottask.update", `[3,{"enabled":false}]`, "snapshot-taak 3 uitzetten"},
		{"cloudsync.sync", `[2]`, "cloud-sync-taak 2 nu uitvoeren"},
		{"service.control", `["RESTART","cifs",{"silent":false}]`, "dienst cifs herstarten"},
		{"service.update", `[4,{"enable":true}]`, "dienst 4 automatisch laten starten"},
		{"sharing.smb.update", `[1,{"enabled":false}]`, "SMB-share 1 uitzetten"},
	}
	for _, c := range ok {
		a, err := check(c.m, json.RawMessage(c.a))
		if err != nil || a.Title != c.title {
			t.Errorf("%s %s: %+v %v", c.m, c.a, a, err)
		}
	}
	bad := []struct{ m, a string }{
		{"pool.delete", `["tank"]`},
		{"pool.scrub.scrub", `["tank","DESTROY"]`},
		{"pool.scrub.scrub", `["../x","START"]`},
		{"smart.test.manual_test", `[[{"identifier":"sda; rm","type":"SHORT"}]]`},
		{"core.job_abort", `["x"]`},
		{"pool.snapshot.create", `[{"dataset":"tank/../x","name":"a"}]`},
		{"pool.snapshot.create", `[{"dataset":"tank","name":"a","vmware_sync":true}]`},
		{"pool.snapshottask.update", `[3,{"enabled":true,"lifetime_value":1}]`},
		{"pool.snapshot.delete", `["tank"]`},
		{"service.control", `["STOP","middlewared"]`},
		{"service.update", `[4,{"enable":true,"state":"x"}]`},
		{"sharing.smb.update", `[1,{"path":"/"}]`},
	}
	for _, c := range bad {
		if _, err := check(c.m, json.RawMessage(c.a)); err == nil {
			t.Errorf("%s %s toegelaten", c.m, c.a)
		}
	}
	if !readDenied("keychaincredential.query") || readDenied("pool.query") {
		t.Error("readDenied")
	}
}

func TestWarnings(t *testing.T) {
	w := warnings(map[string]json.RawMessage{
		"pools":  json.RawMessage(`[{"name":"tank","status":"DEGRADED","healthy":false,"size":100,"allocated":85}]`),
		"temps":  json.RawMessage(`{"sda":50,"sdb":{"temperature":60}}`),
		"alerts": json.RawMessage(`[{"level":"CRITICAL","formatted":"x","dismissed":false},{"level":"INFO","formatted":"y"}]`),
	})
	if len(w) != 5 {
		t.Fatalf("%d waarschuwingen: %+v", len(w), w)
	}
}

func TestRollbackConfirm(t *testing.T) {
	a, err := check("pool.snapshot.rollback", json.RawMessage(`["tank/data@x",{"force":true}]`))
	if err != nil || a.Confirm != "tank/data" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestScrubSecrets(t *testing.T) {
	in := `[{"id":1,"description":"b2","credentials":{"id":2,"name":"b2","attributes":{"account":"x","key":"y"}},"encryption_password":"z","enabled":true}]`
	out := string(scrubSecrets(json.RawMessage(in)))
	if strings.Contains(out, `"y"`) || strings.Contains(out, `"z"`) || !strings.Contains(out, `"b2"`) {
		t.Fatal(out)
	}
	same := `[{"name":"tank","used":1}]`
	if string(scrubSecrets(json.RawMessage(same))) != same {
		t.Fatal("onveranderd antwoord herschreven")
	}
}
