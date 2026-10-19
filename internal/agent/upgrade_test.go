package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// upgradeHost speelt een node na: een binary op schijf, een server met de
// nieuwe agent en systemd voor de terugvaltimer.
type upgradeHost struct {
	dir, bin, state string
	srv             *httptest.Server
	served          []byte
	newSum          string
	// selfProtocol is wat de nieuwe binary bij self-check meldt.
	selfProtocol int
	failRun      bool

	mu       sync.Mutex
	calls    []string
	timer    bool
	restarts atomic.Int32
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func newUpgradeHost(t *testing.T) *upgradeHost {
	t.Helper()
	h := &upgradeHost{dir: t.TempDir(), served: []byte("nieuwe agent"), selfProtocol: protocol.Version}
	h.newSum = sum(h.served)
	h.bin = filepath.Join(h.dir, "cf-agent")
	h.state = filepath.Join(h.dir, "state", "agent-state.json")
	if err := os.WriteFile(h.bin, []byte("oude agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/downloads/cf-agent-linux-"+runtime.GOARCH {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(h.served)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *upgradeHost) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	call := filepath.Base(name) + " " + strings.Join(args, " ")
	h.calls = append(h.calls, call)
	switch {
	case len(args) == 1 && args[0] == "self-check":
		return json.Marshal(protocol.SelfCheck{Version: "v2", Protocol: h.selfProtocol, Arch: runtime.GOARCH})
	case name == "systemd-run":
		if h.failRun {
			return []byte("systemd-run: command not found"), errors.New("exit status 127")
		}
		h.timer = true
	case name == "systemctl" && args[0] == "stop" && args[1] == rollbackUnit+".timer":
		h.timer = false
	case name == "systemctl" && args[0] == "is-active":
		if !h.timer {
			return nil, errors.New("exit status 3")
		}
	}
	return nil, nil
}

func (h *upgradeHost) called(prefix string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func (h *upgradeHost) agent() *Agent {
	return &Agent{
		Config:     &Config{ServerURL: h.srv.URL + "/"},
		Version:    "v1",
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		StatePath:  h.state,
		BinaryPath: h.bin,
		Exec:       h.exec,
		Restart:    func() { h.restarts.Add(1) },
	}
}

func (h *upgradeHost) command(want string) protocol.Command {
	return protocol.Command{
		ID: "c1", Action: protocol.CmdAgentUpgrade, Deadline: time.Now().Add(time.Minute),
		Upgrade: &protocol.AgentUpgrade{Version: "v2", Protocol: protocol.Version, SHA256: map[string]string{runtime.GOARCH: want}},
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func marker(t *testing.T, h *upgradeHost) *upgradeMarker {
	t.Helper()
	m, err := loadMarker(upgradeMarkerPath(h.state))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestUpgradeReplacesItselfAndArmsRollback(t *testing.T) {
	h := newUpgradeHost(t)
	a := h.agent()
	res := a.handleCommand(command(t, h.command(h.newSum)))
	if !res.OK {
		t.Fatalf("update mislukt: %s %v", res.Error, res.Output)
	}
	if got := read(t, h.bin); got != "nieuwe agent" {
		t.Fatalf("binary = %q", got)
	}
	if got := read(t, h.bin+".prev"); got != "oude agent" {
		t.Fatalf("oude agent niet bewaard: %q", got)
	}
	m := marker(t, h)
	if m == nil || m.Status != upgradePending || m.From != sum([]byte("oude agent")) || m.To != h.newSum {
		t.Fatalf("marker = %+v", m)
	}
	run := h.called("systemd-run")
	if len(run) != 1 || !strings.Contains(run[0], "--on-active=180s") || !strings.Contains(run[0], "cf-agent.prev upgrade-rollback -binary "+h.bin) {
		t.Fatalf("terugvaltimer: %v", run)
	}
	// Geen half bestand achtergelaten.
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".new-") {
			t.Fatalf("tijdelijk bestand blijft staan: %s", e.Name())
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.restarts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if h.restarts.Load() != 1 {
		t.Fatal("cf-agent niet herstart")
	}

	// De nieuwe agent start en praat met de server: de timer gaat weg.
	b := h.agent()
	b.resumeUpgrade(context.Background())
	if len(h.called("systemd-run")) != 1 {
		t.Fatal("de nieuwe agent zette de timer opnieuw terwijl hij nog liep")
	}
	if !b.unconfirmed.Load() {
		t.Fatal("de nieuwe agent meldt zijn sha256 al voor de bevestiging")
	}
	b.confirmUpgrade(context.Background())
	if marker(t, h) != nil || h.timer || b.unconfirmed.Load() {
		t.Fatal("update niet bevestigd")
	}
	// Hetzelfde commando nog eens: al bijgewerkt.
	if res := b.handleCommand(command(t, h.command(h.newSum))); !res.OK || !res.Repeat {
		t.Fatalf("herhaald commando: %+v", res)
	}
}

func TestUpgradeRollsBack(t *testing.T) {
	h := newUpgradeHost(t)
	a := h.agent()
	if res := a.handleCommand(command(t, h.command(h.newSum))); !res.OK {
		t.Fatal(res.Error)
	}
	// Na een reboot is de tijdelijke timer weg; de nieuwe agent zet hem terug.
	h.timer = false
	b := h.agent()
	b.resumeUpgrade(context.Background())
	if run := h.called("systemd-run"); len(run) != 2 {
		t.Fatalf("timer niet opnieuw gezet: %v", run)
	}

	// De nieuwe agent praat nooit met de server: de timer zet de oude terug.
	if err := RollbackUpgrade(context.Background(), h.bin, h.state, a.Log, h.exec); err != nil {
		t.Fatal(err)
	}
	if got := read(t, h.bin); got != "oude agent" {
		t.Fatalf("binary = %q", got)
	}
	if m := marker(t, h); m == nil || m.Status != upgradeRolledBack || !strings.Contains(m.Reason, "3 min") {
		t.Fatalf("marker = %+v", m)
	}
	if got := h.called("systemctl --no-block restart cf-agent"); len(got) != 1 {
		t.Fatalf("niet herstart: %v", h.calls)
	}
	// De oude agent meldt in zijn heartbeat welke update mislukte.
	c := h.agent()
	c.resumeUpgrade(context.Background())
	if got, _ := c.rolledBack.Load().(string); got != h.newSum {
		t.Fatalf("rolledBack = %q", got)
	}
	// Nog eens draaien doet niets.
	if err := RollbackUpgrade(context.Background(), h.bin, h.state, a.Log, h.exec); err != nil {
		t.Fatal(err)
	}
	if got := read(t, h.bin); got != "oude agent" {
		t.Fatalf("binary = %q", got)
	}

	// Een nieuwe poging die mislukt, laat de oude terugzetting staan.
	h.served = []byte("iets anders")
	if res := c.handleCommand(command(t, h.command(h.newSum))); res.OK {
		t.Fatal("download met een andere sha256 aangenomen")
	}
	if got, _ := c.rolledBack.Load().(string); got != h.newSum {
		t.Fatalf("rolledBack na een mislukte poging = %q", got)
	}
	// Een nieuwe poging die lukt, meldt de oude terugzetting niet meer: de
	// server zou de oude heartbeat anders als een nieuwe terugzetting lezen.
	h.served = []byte("nieuwe agent")
	if res := c.handleCommand(command(t, h.command(h.newSum))); !res.OK {
		t.Fatal(res.Error)
	}
	if got, _ := c.rolledBack.Load().(string); got != "" {
		t.Fatalf("rolledBack tijdens een nieuwe poging = %q", got)
	}
}

func TestUpgradeRefuses(t *testing.T) {
	cases := []struct {
		name   string
		change func(h *upgradeHost, cmd *protocol.Command)
		want   string
	}{
		{"checksum", func(h *upgradeHost, _ *protocol.Command) { h.served = []byte("iets anders") }, "sha256 van de download klopt niet"},
		{"oudere server", func(_ *upgradeHost, cmd *protocol.Command) { cmd.Upgrade.Protocol = protocol.Version - 1 }, "nooit terug"},
		{"oudere binary", func(h *upgradeHost, _ *protocol.Command) { h.selfProtocol = protocol.Version - 1 }, "nooit terug"},
		{"andere architectuur", func(_ *upgradeHost, cmd *protocol.Command) {
			cmd.Upgrade.SHA256 = map[string]string{"riscv64": "abc"}
		}, "geen agent voor"},
		{"geen systemd-run", func(h *upgradeHost, _ *protocol.Command) { h.failRun = true }, "terugvaltimer"},
		{"geen server", func(h *upgradeHost, _ *protocol.Command) { h.srv.Close() }, "downloaden mislukt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpgradeHost(t)
			cmd := h.command(h.newSum)
			tc.change(h, &cmd)
			res := h.agent().handleCommand(command(t, cmd))
			if res.OK || !strings.Contains(res.Error, tc.want) {
				t.Fatalf("res = %+v, wil %q", res, tc.want)
			}
			if got := read(t, h.bin); got != "oude agent" {
				t.Fatalf("binary vervangen: %q", got)
			}
			if marker(t, h) != nil {
				t.Fatal("marker blijft staan")
			}
			if h.restarts.Load() != 0 || slices.ContainsFunc(h.calls, func(c string) bool { return strings.Contains(c, "restart") }) {
				t.Fatal("herstart na een geweigerde update")
			}
			entries, _ := os.ReadDir(h.dir)
			for _, e := range entries {
				if strings.Contains(e.Name(), ".new-") {
					t.Fatalf("tijdelijk bestand blijft staan: %s", e.Name())
				}
			}
		})
	}
}
