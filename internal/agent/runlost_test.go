package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TestRunNotLostDuringSystemdReexec: tijdens een upgrade van systemd zegt
// systemctl even niets bruikbaars; zolang het proces van run-exec leeft, is
// de run niet weg. Pas als het proces weg is, telt hij als afgebroken.
func TestRunNotLostDuringSystemdReexec(t *testing.T) {
	root := t.TempDir()
	reply := "Failed to connect to bus: Connection refused"
	a := &Agent{Root: root, Exec: func(context.Context, string, ...string) ([]byte, error) { return []byte(reply), nil }}
	id := "apt-1"
	dir := a.runDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	st := protocol.RunStatus{State: protocol.RunRunning, StartedAt: time.Now().Add(-time.Hour).UTC()}
	if err := writeRunStatus(dir, st); err != nil {
		t.Fatal(err)
	}
	proc := filepath.Join(root, "proc", "4242")
	if err := os.MkdirAll(proc, 0o755); err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{
		filepath.Join(dir, runPIDFile): "4242\n",
		filepath.Join(proc, "cmdline"): "/usr/local/bin/cf-agent\x00run-exec\x00" + id + "\x00",
	} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if res := a.runPoll(context.Background(), id, 0); res.Status == nil || res.Status.State != protocol.RunRunning {
		t.Fatalf("run viel weg terwijl run-exec nog leeft: %+v", res)
	}
	// systemctl zegt inactive, maar het proces leeft: nog steeds bezig.
	reply = "inactive"
	if res := a.runPoll(context.Background(), id, 0); res.Status.State != protocol.RunRunning {
		t.Fatalf("run viel weg: %+v", res.Status)
	}
	// Het proces is weg: nu is de run afgebroken.
	if err := os.RemoveAll(proc); err != nil {
		t.Fatal(err)
	}
	res := a.runPoll(context.Background(), id, 0)
	if res.Status.State != protocol.RunFailed || res.Status.Error != protocol.RunLostError {
		t.Fatalf("run niet afgebroken: %+v", res.Status)
	}
}
