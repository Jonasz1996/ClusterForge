package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestUpdateStateKeepsFields(t *testing.T) {
	a, _ := applyAgent(t)
	// Een veld van een nieuwere agent blijft staan, ook na een wijziging.
	if err := os.MkdirAll(filepath.Dir(a.StatePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.StatePath, []byte(`{"maintenance":true,"keepalived_enabled":true,"sandbox":{"vmid":900}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.updateState(func(st *agentState) { st.PowerCommand = string(rune('a' + i)) }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := a.updateState(func(st *agentState) { st.Maintenance, st.KeepalivedEnabled = false, false }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(a.StatePath)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	var sandbox bytes.Buffer
	_ = json.Compact(&sandbox, raw["sandbox"])
	if sandbox.String() != `{"vmid":900}` || raw["power_command"] == nil || raw["keepalived_enabled"] != nil || string(raw["maintenance"]) != "false" {
		t.Fatalf("state: %s", b)
	}
	if fi, _ := os.Stat(a.StatePath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("rechten: %v", fi.Mode())
	}
}
