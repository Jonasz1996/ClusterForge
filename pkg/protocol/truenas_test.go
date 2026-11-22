package protocol

import (
	"encoding/json"
	"testing"
)

func TestTrueNASReadMethod(t *testing.T) {
	for _, m := range []string{"pool.query", "pool.dataset.query", "system.info", "disk.temperatures", "smb.config", "pool.snapshottask.get_instance"} {
		if !TrueNASReadMethod(m) {
			t.Errorf("%s hoort te lezen", m)
		}
	}
	for _, m := range []string{"pool.delete", "pool.scrub.scrub", "system.reboot", "update.update", "query", "pool.query;x", "Pool.query"} {
		if TrueNASReadMethod(m) {
			t.Errorf("%s is geen leesmethode", m)
		}
	}
	r := PVERequest{Op: TrueNASOpGet, Path: "pool.query", Args: json.RawMessage(`{"a":1}`)}
	if r.ValidateTrueNASGet() == nil {
		t.Error("args moeten een lijst zijn")
	}
	run := PVERun{Action: TrueNASCall, TrueNAS: &TrueNASMethodCall{Method: "pool.scrub.scrub", Args: json.RawMessage(`["tank","START"]`), Job: true}}
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
	if (PVERun{Action: TrueNASCall}).Validate() == nil {
		t.Error("zonder aanroep")
	}
}
