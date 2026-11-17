package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type reconfHarness struct {
	a        *Agent
	dir      string
	mu       sync.Mutex
	calls    []string
	restarts atomic.Int32
	natsOK   bool
}

func (h *reconfHarness) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, name+" "+strings.Join(args, " "))
	if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
		return nil, errors.New("inactive")
	}
	return nil, nil
}

func (h *reconfHarness) called(prefix string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func newReconfHarness(t *testing.T) *reconfHarness {
	t.Helper()
	dir := t.TempDir()
	h := &reconfHarness{dir: dir, natsOK: true}
	cfg := &Config{
		ServerURL: "http://clusterforge.example:8080", AgentID: "a1", NodeID: "n1",
		NatsURL: "tls://clusterforge.example:4222", NatsCertSHA256: strings.Repeat("ab", 32), NkeySeed: "seed",
	}
	cfgPath := filepath.Join(dir, "agent.json")
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	h.a = &Agent{
		Config: cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		StatePath: filepath.Join(dir, "state.json"), ConfigPath: cfgPath, BinaryPath: "/bin/true",
		Exec: h.exec, Restart: func() { h.restarts.Add(1) },
		TestNATS: func(_ context.Context, c Config) error {
			if !h.natsOK {
				return errors.New("connection refused")
			}
			if c.NatsURL != "tls://192.168.113.153:4222" {
				return errors.New("verkeerd adres getest: " + c.NatsURL)
			}
			return nil
		},
	}
	return h
}

func reconfCmd(server, nats string) protocol.Command {
	return protocol.Command{ID: "r1", Action: protocol.CmdAgentReconfigure, Reconfigure: &protocol.AgentReconfigure{ServerURL: server, NatsURL: nats}}
}

func TestReconfigureTestsSavesAndRollsBack(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer health.Close()
	h := newReconfHarness(t)
	ctx := context.Background()

	res := h.a.reconfigure(ctx, reconfCmd(health.URL+"/", "192.168.113.153"))
	if !res.OK {
		t.Fatalf("reconfigure: %s %v", res.Error, res.Output)
	}
	got, err := LoadConfig(h.a.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.NatsURL != "tls://192.168.113.153:4222" || got.ServerURL != health.URL || got.NkeySeed != "seed" {
		t.Fatalf("agent.json = %+v", got)
	}
	if prev, err := LoadConfig(h.a.ConfigPath + ".prev"); err != nil || prev.NatsURL != "tls://clusterforge.example:4222" {
		t.Fatalf("agent.json.prev = %+v, %v", prev, err)
	}
	if !h.called("systemd-run --unit=" + reconfigRollbackUnit) {
		t.Fatalf("geen terugvaltimer: %v", h.calls)
	}
	deadline := time.Now().Add(3 * time.Second)
	for h.restarts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if h.restarts.Load() != 1 {
		t.Fatal("de agent herstartte niet")
	}

	// De agent bereikt de server niet: de timer zet het oude terug.
	if err := RollbackReconfigure(ctx, h.a.ConfigPath, h.a.StatePath, h.a.Log, h.exec); err != nil {
		t.Fatal(err)
	}
	back, _ := LoadConfig(h.a.ConfigPath)
	if back.NatsURL != "tls://clusterforge.example:4222" || back.ServerURL != "http://clusterforge.example:8080" {
		t.Fatalf("niet teruggezet: %+v", back)
	}
	if !h.called("systemctl --no-block restart cf-agent") {
		t.Fatal("cf-agent niet herstart na terugzetten")
	}
	// De oude agent start en meldt de teruggezette herconfiguratie.
	h.a.Config = back
	h.a.resumeReconfigure(ctx)
	if v, _ := h.a.reconfRolledBack.Load().(string); v != "tls://192.168.113.153:4222" {
		t.Fatalf("reconfRolledBack = %q", v)
	}
}

func TestReconfigureConfirm(t *testing.T) {
	h := newReconfHarness(t)
	ctx := context.Background()
	res := h.a.reconfigure(ctx, reconfCmd("", "tls://192.168.113.153:4222"))
	if !res.OK {
		t.Fatalf("reconfigure: %s", res.Error)
	}
	// Na de herstart: de nieuwe config, de server neemt facts aan.
	cfg, _ := LoadConfig(h.a.ConfigPath)
	b := &Agent{Config: cfg, Log: h.a.Log, StatePath: h.a.StatePath, ConfigPath: h.a.ConfigPath, Exec: h.exec, BinaryPath: "/bin/true"}
	b.resumeReconfigure(ctx)
	b.confirmReconfigure(ctx)
	if _, err := os.Stat(reconfigMarkerPath(h.a.StatePath)); !os.IsNotExist(err) {
		t.Fatalf("status niet opgeruimd: %v", err)
	}
	if !h.called("systemctl stop " + reconfigRollbackUnit + ".timer") {
		t.Fatal("timer niet gestopt")
	}
	// Een late timer doet niets meer.
	if err := RollbackReconfigure(ctx, h.a.ConfigPath, h.a.StatePath, h.a.Log, h.exec); err != nil {
		t.Fatal(err)
	}
	if now, _ := LoadConfig(h.a.ConfigPath); now.NatsURL != "tls://192.168.113.153:4222" {
		t.Fatalf("toch teruggezet: %+v", now)
	}
}

func TestReconfigureRefuses(t *testing.T) {
	h := newReconfHarness(t)
	ctx := context.Background()
	h.natsOK = false
	res := h.a.reconfigure(ctx, reconfCmd("", "192.168.113.153:4222"))
	if res.OK || !strings.Contains(res.Error, "connection refused") {
		t.Fatalf("NATS faalt maar toch OK: %+v", res)
	}
	if got, _ := LoadConfig(h.a.ConfigPath); got.NatsURL != "tls://clusterforge.example:4222" {
		t.Fatalf("agent.json toch gewijzigd: %+v", got)
	}
	if res := h.a.reconfigure(ctx, reconfCmd("ftp://x", "")); res.OK {
		t.Fatal("ftp-adres aangenomen")
	}
	if res := h.a.reconfigure(ctx, reconfCmd("http://127.0.0.1:1", "")); res.OK || !strings.Contains(res.Error, "werkt niet") {
		t.Fatalf("onbereikbare server aangenomen: %+v", res)
	}
	if res := h.a.reconfigure(ctx, reconfCmd("http://clusterforge.example:8080", "")); !res.OK || !res.Repeat {
		t.Fatalf("zelfde adres: %+v", res)
	}
	if h.restarts.Load() != 0 {
		t.Fatal("herstart zonder wijziging")
	}
}
