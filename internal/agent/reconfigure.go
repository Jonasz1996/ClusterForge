package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Herconfigureren (protocol 47): de server geeft een nieuw serveradres en/of
// NATS-adres. De agent test eerst of hij de server op het nieuwe adres
// bereikt (NATS met zijn eigen sleutel en het vastgepinde certificaat, en
// /api/v1/health over http(s)). Lukt dat, dan bewaart hij agent.json als
// agent.json.prev, zet hij een timer van systemd die die terugzet, schrijft
// hij het nieuwe agent.json en herstart hij. Praat hij daarna met de server,
// dan stopt hij de timer; anders komt het oude agent.json terug. Zo raakt
// ClusterForge nooit een agent kwijt door een verkeerd adres.

// reconfigRollbackUnit is de tijdelijke timer die het oude agent.json
// terugzet.
const reconfigRollbackUnit = "cf-agent-reconfig-rollback"

// reconfigMarker staat naast de state zolang een nieuw adres niet bewezen
// heeft dat het werkt, en daarna als het teruggezet werd.
type reconfigMarker struct {
	Status     string    `json:"status"`
	Config     string    `json:"config"`
	FromServer string    `json:"from_server"`
	ToServer   string    `json:"to_server"`
	FromNats   string    `json:"from_nats"`
	ToNats     string    `json:"to_nats"`
	Started    time.Time `json:"started"`
	Deadline   time.Time `json:"deadline"`
	Reason     string    `json:"reason,omitempty"`
}

func reconfigMarkerPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "reconfigure.json")
}

func (a *Agent) configPath() string {
	if a.ConfigPath != "" {
		return a.ConfigPath
	}
	return DefaultConfigPath
}

func (a *Agent) reconfigRollbackAfter() time.Duration {
	if a.ReconfigRollbackAfter > 0 {
		return a.ReconfigRollbackAfter
	}
	return protocol.ReconfigureRollbackAfter
}

// reconfigure zet de adressen van cmd.Reconfigure in agent.json, na een test.
func (a *Agent) reconfigure(ctx context.Context, cmd protocol.Command) protocol.Result {
	r := cmd.Reconfigure
	switch {
	case r == nil || (r.ServerURL == "" && r.NatsURL == ""):
		return failed(nil, "geen nieuw adres in het commando")
	case a.Config == nil:
		return failed(nil, "deze agent heeft geen agent.json")
	}
	next := *a.Config
	if r.ServerURL != "" {
		u, err := protocol.NormalizeServerURL(r.ServerURL)
		if err != nil {
			return failed(nil, "serveradres: "+err.Error())
		}
		next.ServerURL = u
	}
	if r.NatsURL != "" {
		u, err := protocol.NormalizeNatsURL(r.NatsURL, 4222)
		if err != nil {
			return failed(nil, "NATS-adres: "+err.Error())
		}
		next.NatsURL = u
	}
	if next.ServerURL == a.Config.ServerURL && next.NatsURL == a.Config.NatsURL {
		return protocol.Result{OK: true, Repeat: true, Output: []string{"agent.json heeft deze adressen al: " + next.NatsURL + " en " + next.ServerURL}}
	}

	var out []string
	if next.NatsURL != a.Config.NatsURL {
		if err := a.testNATS(ctx, next); err != nil {
			return failed(out, "NATS op "+next.NatsURL+" werkt niet vanaf deze node: "+err.Error()+"; agent.json blijft zoals het was")
		}
		out = append(out, "NATS op "+next.NatsURL+" getest: de server neemt deze agent daar aan")
	}
	if next.ServerURL != a.Config.ServerURL {
		if err := a.testServer(ctx, next.ServerURL); err != nil {
			return failed(out, "ClusterForge op "+next.ServerURL+" werkt niet vanaf deze node: "+err.Error()+"; agent.json blijft zoals het was")
		}
		out = append(out, "ClusterForge op "+next.ServerURL+" getest: /api/v1/health antwoordt")
	}

	cfgPath := a.configPath()
	prev := cfgPath + ".prev"
	if err := a.Config.Save(prev); err != nil {
		return failed(out, "het oude agent.json bewaren mislukt: "+err.Error())
	}
	out = append(out, "oud agent.json bewaard als "+prev)
	marker := reconfigMarkerPath(a.statePath())
	now := time.Now()
	m := reconfigMarker{
		Status: upgradePending, Config: cfgPath,
		FromServer: a.Config.ServerURL, ToServer: next.ServerURL, FromNats: a.Config.NatsURL, ToNats: next.NatsURL,
		Started: now.UTC(), Deadline: now.Add(a.reconfigRollbackAfter()).UTC(),
	}
	if err := saveJSON(marker, m); err != nil {
		return failed(out, "de herconfiguratie onthouden mislukt: "+err.Error())
	}
	if err := a.armReconfigRollback(ctx, cfgPath, a.reconfigRollbackAfter()); err != nil {
		_ = os.Remove(marker)
		return failed(out, "de terugvaltimer zetten mislukt, agent.json blijft zoals het was: "+err.Error())
	}
	if err := next.Save(cfgPath); err != nil {
		a.disarmReconfigRollback(ctx)
		_ = os.Remove(marker)
		return failed(out, "agent.json schrijven mislukt, het oude blijft: "+err.Error())
	}
	out = append(out, fmt.Sprintf("cf-agent herstart met de nieuwe adressen; praat hij niet binnen %s met de server, dan komt het oude agent.json terug",
		seconds(a.reconfigRollbackAfter())))
	a.Log.Warn("agent.json omgezet; herstarten", "nats", next.NatsURL, "server", next.ServerURL)
	go func() {
		time.Sleep(time.Second)
		a.restartSelf()
	}()
	return done(out)
}

// testNATS verbindt één keer met NATS op c.NatsURL, met de sleutel en het
// vastgepinde certificaat van deze agent. Zo is zeker dat het dezelfde
// server is en dat hij deze agent aanneemt.
func (a *Agent) testNATS(ctx context.Context, c Config) error {
	if a.TestNATS != nil {
		return a.TestNATS(ctx, c)
	}
	kp, err := nkeys.FromSeed([]byte(c.NkeySeed))
	if err != nil {
		return err
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return err
	}
	nc, err := nats.Connect(c.NatsURL,
		nats.Nkey(pub, kp.Sign),
		nats.Secure(protocol.PinnedTLSConfig(c.NatsCertSHA256)),
		nats.CustomInboxPrefix(protocol.InboxPrefix(c.NodeID)),
		nats.Name("cf-agent "+a.Version+" (test nieuw adres)"),
		nats.Timeout(10*time.Second),
		nats.NoReconnect(),
	)
	if err != nil {
		return err
	}
	defer nc.Close()
	return nc.FlushTimeout(5 * time.Second)
}

// testServer kijkt of /api/v1/health van ClusterForge op server antwoordt.
func (a *Agent) testServer(ctx context.Context, server string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(server, "/")+"/api/v1/health", nil)
	if err != nil {
		return err
	}
	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("antwoord " + resp.Status)
	}
	return nil
}

func (a *Agent) armReconfigRollback(ctx context.Context, cfgPath string, after time.Duration) error {
	a.disarmReconfigRollback(ctx)
	bin, _ := a.binary()
	out, err := a.exec(ctx, "systemd-run", "--unit="+reconfigRollbackUnit, "--collect",
		fmt.Sprintf("--on-active=%ds", max(int(after.Seconds()), 1)), "--timer-property=AccuracySec=1s",
		"--description=ClusterForge: oud agent.json terugzetten als de agent de server niet bereikt",
		bin, "reconfigure-rollback", "-config", cfgPath, "-state", a.statePath())
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a *Agent) disarmReconfigRollback(ctx context.Context) {
	_, _ = a.exec(ctx, "systemctl", "stop", reconfigRollbackUnit+".timer")
	_, _ = a.exec(ctx, "systemctl", "reset-failed", reconfigRollbackUnit+".timer", reconfigRollbackUnit+".service")
}

func loadReconfigMarker(path string) (*reconfigMarker, error) {
	var m reconfigMarker
	ok, err := loadJSON(path, &m)
	if !ok || err != nil {
		return nil, err
	}
	return &m, nil
}

// resumeReconfigure draait bij de start: wacht een nieuw adres nog op
// bevestiging (bijvoorbeeld na een reboot), dan staat de timer er weer; werd
// het teruggezet, dan meldt de heartbeat dat.
func (a *Agent) resumeReconfigure(ctx context.Context) {
	path := reconfigMarkerPath(a.statePath())
	m, err := loadReconfigMarker(path)
	if err != nil {
		a.Log.Warn("herconfiguratie-status onleesbaar", "err", err)
		return
	}
	if m == nil || a.Config == nil {
		return
	}
	switch {
	case m.Status == upgradeRolledBack && a.Config.NatsURL == m.FromNats && a.Config.ServerURL == m.FromServer:
		a.reconfRolledBack.Store(m.ToNats)
		a.Log.Warn("de vorige herconfiguratie werd teruggezet", "nats", m.ToNats, "server", m.ToServer, "reason", m.Reason)
	case m.Status == upgradePending && a.Config.NatsURL == m.ToNats && a.Config.ServerURL == m.ToServer:
		if _, err := a.exec(ctx, "systemctl", "is-active", "--quiet", reconfigRollbackUnit+".timer"); err == nil {
			return
		}
		left := max(time.Until(m.Deadline), 5*time.Second)
		if err := a.armReconfigRollback(ctx, a.configPath(), left); err != nil {
			a.Log.Error("terugvaltimer van de herconfiguratie opnieuw zetten mislukt", "err", err)
		}
	case m.Status == upgradePending:
		// Het oude agent.json staat er (weer): de herconfiguratie kwam niet aan.
		_ = os.Remove(path)
	}
}

// confirmReconfigure draait als de server een bericht van deze agent aannam:
// het nieuwe adres werkt, dus de timer gaat weg.
func (a *Agent) confirmReconfigure(ctx context.Context) {
	if a.reconfConfirmed.Swap(true) || a.Config == nil {
		return
	}
	path := reconfigMarkerPath(a.statePath())
	m, err := loadReconfigMarker(path)
	if err != nil || m == nil || m.Status != upgradePending {
		return
	}
	if a.Config.NatsURL != m.ToNats || a.Config.ServerURL != m.ToServer {
		return
	}
	a.disarmReconfigRollback(ctx)
	if err := os.Remove(path); err != nil {
		a.Log.Warn("herconfiguratie-status opruimen mislukt", "err", err)
	}
	a.reconfRolledBack.Store("")
	a.Log.Info("herconfiguratie bevestigd: de agent praat met de server op het nieuwe adres", "nats", a.Config.NatsURL, "server", a.Config.ServerURL)
}

// RollbackReconfigure is cf-agent reconfigure-rollback: de terugvaltimer
// start het. Praat de agent nog niet met de server op het nieuwe adres, dan
// komt het oude agent.json terug en herstart cf-agent.
func RollbackReconfigure(ctx context.Context, cfgPath, statePath string, log *slog.Logger, exec func(ctx context.Context, name string, args ...string) ([]byte, error)) error {
	path := reconfigMarkerPath(statePath)
	m, err := loadReconfigMarker(path)
	if err != nil {
		return err
	}
	if m == nil || m.Status != upgradePending {
		log.Info("niets terug te zetten: het nieuwe adres is bevestigd")
		return nil
	}
	cur, err := LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	switch {
	case cur.NatsURL == m.FromNats && cur.ServerURL == m.FromServer:
		return os.Remove(path)
	case cur.NatsURL == m.ToNats && cur.ServerURL == m.ToServer:
	default:
		return fmt.Errorf("%s heeft intussen andere adressen; niets teruggezet", cfgPath)
	}
	prev, err := LoadConfig(cfgPath + ".prev")
	if err != nil {
		return fmt.Errorf("het oude agent.json is er niet: %w", err)
	}
	if prev.NatsURL != m.FromNats || prev.ServerURL != m.FromServer || prev.NodeID != cur.NodeID {
		return fmt.Errorf("%s.prev is niet het oude agent.json; niets teruggezet", cfgPath)
	}
	if err := prev.Save(cfgPath); err != nil {
		return err
	}
	m.Status = upgradeRolledBack
	m.Reason = "de agent bereikte de server niet binnen " + seconds(m.Deadline.Sub(m.Started)) + " op het nieuwe adres"
	if err := saveJSON(path, *m); err != nil {
		log.Warn("herconfiguratie-status bewaren mislukt", "err", err)
	}
	log.Warn("oud agent.json teruggezet", "nats", m.FromNats, "server", m.FromServer, "failed_nats", m.ToNats)
	out, err := exec(ctx, "systemctl", "--no-block", "restart", "cf-agent")
	if err != nil {
		return fmt.Errorf("cf-agent herstarten mislukt: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func loadJSON(path string, v any) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}
