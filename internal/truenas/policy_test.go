package truenas

import (
	"encoding/json"
	"testing"
)

func TestCheck(t *testing.T) {
	ok := []struct{ m, a, title string }{
		{"pool.scrub.scrub", `["tank","START"]`, "scrub van tank starten"},
		{"alert.dismiss", `["a1b2"]`, "melding wegklikken"},
		{"smart.test.manual_test", `[[{"identifier":"{serial}ABC","type":"SHORT"}]]`, "SMART-test short op {serial}ABC"},
		{"core.job_abort", `[42]`, "TrueNAS-taak 42 afbreken"},
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
