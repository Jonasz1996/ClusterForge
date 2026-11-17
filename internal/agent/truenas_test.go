package agent

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTrueNASArgv(t *testing.T) {
	argv, err := trueNASArgv("pool.scrub.scrub", json.RawMessage(`["tank", "START"]`), true)
	if err != nil || !reflect.DeepEqual(argv, []string{"call", "-job", "-jp", "description", "pool.scrub.scrub", `"tank"`, `"START"`}) {
		t.Fatalf("%q %v", argv, err)
	}
	if string(trueNASResult([]byte("TrueNAS-25.04.2\n"))) != `"TrueNAS-25.04.2"` {
		t.Error("tekst niet als JSON")
	}
	if string(trueNASResult([]byte(`[{"a":1}]`))) != `[{"a":1}]` {
		t.Error("JSON veranderd")
	}
	p := parseZpoolHealth("boot-pool\tONLINE\ntank\tDEGRADED\n")
	if len(p) != 1 || p[0].Name != "tank" || p[0].Health != "DEGRADED" {
		t.Fatalf("%+v", p)
	}
}
