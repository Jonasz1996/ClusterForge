package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Zelf bijwerken: de agent haalt de nieuwe binary van zijn eigen server,
// controleert de sha256 uit het commando en laat de nieuwe binary zeggen
// welk protocol hij heeft. Daarna bewaart hij zichzelf als <binary>.prev,
// zet hij een timer van systemd die die terugzet, vervangt hij zichzelf in
// één rename en herstart hij. Praat de nieuwe agent met de server, dan
// stopt hij de timer; anders zet de timer de oude agent terug.

// rollbackUnit is de tijdelijke timer van systemd die een nieuwe agent
// terugzet als hij niet met de server praat.
const rollbackUnit = "cf-agent-rollback"

// maxAgentSize is de grootste binary die de agent downloadt.
const maxAgentSize = 512 << 20

const (
	upgradePending    = "pending"
	upgradeRolledBack = "rolled_back"
)

// upgradeMarker staat naast de state zolang een nieuwe agent niet bewezen
// heeft dat hij werkt, en daarna als hij teruggezet werd.
type upgradeMarker struct {
	Status      string    `json:"status"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	FromVersion string    `json:"from_version"`
	ToVersion   string    `json:"to_version"`
	Binary      string    `json:"binary"`
	Started     time.Time `json:"started"`
	Deadline    time.Time `json:"deadline"`
	Reason      string    `json:"reason,omitempty"`
}

// selfBinary geeft het pad en de sha256 van de binary die draait, één keer
// berekend. Fouten geven een lege sha256.
type selfBinary struct {
	once  sync.Once
	path  string
	sum   string
	ready atomic.Bool
}

// binarySum is de sha256 als die al berekend is, anders leeg: een
// heartbeat wacht niet op het lezen van de binary.
func (a *Agent) binarySum() string {
	if !a.self.ready.Load() {
		go a.binary()
		return ""
	}
	return a.self.sum
}

func (a *Agent) binary() (path, sum string) {
	a.self.once.Do(func() {
		defer a.self.ready.Store(true)
		p := a.BinaryPath
		if p == "" {
			if exe, err := os.Executable(); err == nil {
				p = exe
			}
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		a.self.path = p
		a.self.sum = cachedSHA256(p)
	})
	return a.self.path, a.self.sum
}

// sumCache bewaart de sha256 per bestand voor het hele proces: agents in
// hetzelfde proces (de tests) lezen dezelfde binary zo maar één keer.
var sumCache struct {
	sync.Mutex
	m map[string]string
}

// cachedSHA256 is fileSHA256 met een cache op pad, grootte en wijzigtijd.
// Fouten geven een lege sha256.
func cachedSHA256(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	key := fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
	sumCache.Lock()
	defer sumCache.Unlock()
	if s, ok := sumCache.m[key]; ok {
		return s
	}
	s, err := fileSHA256(path)
	if err != nil {
		return ""
	}
	if sumCache.m == nil {
		sumCache.m = map[string]string{}
	}
	sumCache.m[key] = s
	return s
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func upgradeMarkerPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "upgrade.json")
}

func loadMarker(path string) (*upgradeMarker, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m upgradeMarker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
}

func saveMarker(path string, m upgradeMarker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}

func (a *Agent) rollbackAfter() time.Duration {
	if a.RollbackAfter > 0 {
		return a.RollbackAfter
	}
	return protocol.UpgradeRollbackAfter
}

// upgrade werkt de agent bij naar cmd.Upgrade. Een herhaald commando na de
// herstart komt bij de nieuwe agent aan, en die is dan al bijgewerkt.
func (a *Agent) upgrade(ctx context.Context, cmd protocol.Command) protocol.Result {
	u := cmd.Upgrade
	if u == nil {
		return failed(nil, "geen nieuwe agent in het commando")
	}
	bin, own := a.binary()
	if own == "" {
		return failed(nil, "de eigen binary is niet te lezen: "+bin)
	}
	want := strings.ToLower(u.SHA256[runtime.GOARCH])
	switch {
	case want == "":
		return failed(nil, "de server heeft geen agent voor "+runtime.GOARCH)
	case want == own:
		return protocol.Result{OK: true, Repeat: true, Output: []string{"deze agent is al bijgewerkt (" + a.Version + ")"}}
	case u.Protocol < protocol.Version:
		return failed(nil, fmt.Sprintf("de server biedt een agent met protocol %d aan en deze heeft %d; een agent gaat nooit terug naar een oudere versie",
			u.Protocol, protocol.Version))
	case a.Config == nil || a.Config.ServerURL == "":
		return failed(nil, "agent.json heeft geen server_url; werk deze agent bij met agent.sh --upgrade")
	}
	var out []string
	tmp, err := a.download(ctx, bin, want)
	if err != nil {
		return failed(out, err.Error())
	}
	defer func() { _ = os.Remove(tmp) }()
	out = append(out, "nieuwe agent gedownload van "+a.Config.ServerURL+"; sha256 klopt ("+want[:12]+"…)")

	// De nieuwe binary moet hier draaien en zeggen welk protocol hij heeft,
	// voor hij iets vervangt.
	raw, err := a.execTimeout(ctx, 15*time.Second, tmp, "self-check")
	var sc protocol.SelfCheck
	if err == nil {
		err = json.Unmarshal(lastOutputLine(raw), &sc)
	}
	switch {
	case err != nil:
		return failed(append(out, outputLines(raw)...), "de nieuwe agent start niet op deze machine: "+err.Error())
	case sc.Protocol < protocol.Version:
		return failed(out, fmt.Sprintf("de nieuwe agent heeft protocol %d en deze %d; een agent gaat nooit terug naar een oudere versie", sc.Protocol, protocol.Version))
	case u.Protocol != 0 && sc.Protocol != u.Protocol:
		return failed(out, fmt.Sprintf("de nieuwe agent meldt protocol %d, de server zei %d", sc.Protocol, u.Protocol))
	}
	out = append(out, fmt.Sprintf("de nieuwe agent start hier: versie %s, protocol %d", sc.Version, sc.Protocol))

	prev := bin + ".prev"
	if err := copyFileAtomic(bin, prev); err != nil {
		return failed(out, "de oude agent bewaren mislukt: "+err.Error())
	}
	out = append(out, "oude agent bewaard als "+prev)

	marker := upgradeMarkerPath(a.statePath())
	now := time.Now()
	m := upgradeMarker{
		Status: upgradePending, From: own, To: want, FromVersion: a.Version, ToVersion: sc.Version, Binary: bin,
		Started: now.UTC(), Deadline: now.Add(a.rollbackAfter()).UTC(),
	}
	if err := saveMarker(marker, m); err != nil {
		return failed(out, "de update onthouden mislukt: "+err.Error())
	}
	if err := a.armRollback(ctx, prev, bin, a.rollbackAfter()); err != nil {
		_ = os.Remove(marker)
		return failed(out, "de terugvaltimer zetten mislukt, de oude agent blijft: "+err.Error())
	}
	if err := os.Rename(tmp, bin); err != nil {
		a.disarmRollback(ctx)
		_ = os.Remove(marker)
		return failed(out, "de agent vervangen mislukt, de oude blijft: "+err.Error())
	}
	out = append(out, fmt.Sprintf("cf-agent herstart; praat de nieuwe niet binnen %s met de server, dan komt de oude terug", seconds(a.rollbackAfter())))
	a.Log.Warn("agent bijgewerkt; herstarten", "from", a.Version, "to", sc.Version)
	go func() {
		// Even wachten, zodat het antwoord nog weg kan.
		time.Sleep(time.Second)
		a.restartSelf()
	}()
	return done(out)
}

// download haalt de agent voor deze architectuur in de map van bin en
// controleert de sha256; het geeft het pad van het tijdelijke bestand.
func (a *Agent) download(ctx context.Context, bin, want string) (string, error) {
	url := strings.TrimRight(a.Config.ServerURL, "/") + "/downloads/cf-agent-linux-" + runtime.GOARCH
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloaden mislukt: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloaden mislukt: %s gaf %s", url, resp.Status)
	}
	f, err := os.CreateTemp(filepath.Dir(bin), "."+filepath.Base(bin)+".new-*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxAgentSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxAgentSize {
		err = errors.New("de download is groter dan 512 MB")
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = errors.New("de sha256 van de download klopt niet met die van de server; niets vervangen")
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o755)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// armRollback zet een tijdelijke timer van systemd die over after de oude
// agent terugzet. Hij staat buiten cf-agent.service, dus een herstart of een
// nieuwe agent die niet start, raakt hem niet.
func (a *Agent) armRollback(ctx context.Context, prev, bin string, after time.Duration) error {
	a.disarmRollback(ctx)
	out, err := a.exec(ctx, "systemd-run", "--unit="+rollbackUnit, "--collect",
		fmt.Sprintf("--on-active=%ds", max(int(after.Seconds()), 1)), "--timer-property=AccuracySec=1s",
		"--description=ClusterForge: oude cf-agent terugzetten als de nieuwe niet verbindt",
		prev, "upgrade-rollback", "-binary", bin, "-state", a.statePath())
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a *Agent) disarmRollback(ctx context.Context) {
	_, _ = a.exec(ctx, "systemctl", "stop", rollbackUnit+".timer")
	_, _ = a.exec(ctx, "systemctl", "reset-failed", rollbackUnit+".timer", rollbackUnit+".service")
}

func (a *Agent) restartSelf() {
	if a.Restart != nil {
		a.Restart()
		return
	}
	if out, err := a.exec(context.Background(), "systemctl", "--no-block", "restart", "cf-agent"); err != nil {
		a.Log.Error("cf-agent herstarten mislukt; de nieuwe agent start bij de volgende herstart", "err", err, "output", strings.TrimSpace(string(out)))
	}
}

// resumeUpgrade draait bij de start. Is deze agent de nieuwe van een update
// die nog niet bevestigd is, bijvoorbeeld na een reboot, dan staat de
// terugvaltimer er weer, met de tijd die er nog over was.
func (a *Agent) resumeUpgrade(ctx context.Context) {
	path := upgradeMarkerPath(a.statePath())
	m, err := loadMarker(path)
	if err != nil {
		a.Log.Warn("update-status onleesbaar", "err", err)
		return
	}
	if m == nil {
		return
	}
	bin, own := a.binary()
	switch {
	case m.Status == upgradeRolledBack && own == m.From:
		a.rolledBack.Store(m.To)
		a.Log.Warn("de vorige update werd teruggezet", "to_version", m.ToVersion, "reason", m.Reason)
	case m.Status == upgradePending && own == m.To:
		if _, err := a.exec(ctx, "systemctl", "is-active", "--quiet", rollbackUnit+".timer"); err == nil {
			return
		}
		left := max(time.Until(m.Deadline), 5*time.Second)
		if err := a.armRollback(ctx, bin+".prev", bin, left); err != nil {
			a.Log.Error("terugvaltimer opnieuw zetten mislukt", "err", err)
		}
	case m.Status == upgradePending:
		// De oude agent draait (weer): de update kwam nooit aan.
		_ = os.Remove(path)
	}
}

// confirmUpgrade draait als de server een bericht van deze agent aannam: de
// nieuwe agent werkt, dus de terugvaltimer gaat weg.
func (a *Agent) confirmUpgrade(ctx context.Context) {
	if a.confirmed.Swap(true) {
		return
	}
	path := upgradeMarkerPath(a.statePath())
	m, err := loadMarker(path)
	if err != nil || m == nil || m.Status != upgradePending {
		return
	}
	if _, own := a.binary(); own != m.To {
		return
	}
	a.disarmRollback(ctx)
	if err := os.Remove(path); err != nil {
		a.Log.Warn("update-status opruimen mislukt", "err", err)
	}
	a.rolledBack.Store("")
	a.Log.Info("update bevestigd: de nieuwe agent praat met de server", "from_version", m.FromVersion, "version", a.Version)
}

// RollbackUpgrade is cf-agent upgrade-rollback: de terugvaltimer start het
// met de bewaarde oude agent. Praat de nieuwe nog niet met de server, dan
// komt de oude terug en herstart cf-agent.
func RollbackUpgrade(ctx context.Context, bin, statePath string, log *slog.Logger, exec func(ctx context.Context, name string, args ...string) ([]byte, error)) error {
	path := upgradeMarkerPath(statePath)
	m, err := loadMarker(path)
	if err != nil {
		return err
	}
	if m == nil || m.Status != upgradePending {
		log.Info("niets terug te zetten: de nieuwe agent is bevestigd")
		return nil
	}
	cur, err := fileSHA256(bin)
	if err != nil {
		return err
	}
	switch cur {
	case m.From:
		return os.Remove(path)
	case m.To:
	default:
		return fmt.Errorf("%s is intussen een andere binary; niets teruggezet", bin)
	}
	prev := bin + ".prev"
	if sum, err := fileSHA256(prev); err != nil || sum != m.From {
		return fmt.Errorf("%s is niet de oude agent; niets teruggezet", prev)
	}
	if err := copyFileAtomic(prev, bin); err != nil {
		return err
	}
	m.Status = upgradeRolledBack
	m.Reason = "de nieuwe agent praatte niet binnen " + seconds(m.Deadline.Sub(m.Started)) + " met de server"
	if err := saveMarker(path, *m); err != nil {
		log.Warn("update-status bewaren mislukt", "err", err)
	}
	log.Warn("oude agent teruggezet", "version", m.FromVersion, "failed_version", m.ToVersion)
	out, err := exec(ctx, "systemctl", "--no-block", "restart", "cf-agent")
	if err != nil {
		return fmt.Errorf("cf-agent herstarten mislukt: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// copyFileAtomic kopieert src naar dst via een tijdelijk bestand in de map
// van dst, met rechten 0755.
func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// lastOutputLine is de laatste niet-lege regel van out.
func lastOutputLine(out []byte) []byte {
	lines := outputLines(out)
	if len(lines) == 0 {
		return nil
	}
	return []byte(lines[len(lines)-1])
}

// seconds schrijft een duur als "45 s" of "3 min".
func seconds(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 120 {
		return fmt.Sprintf("%d s", s)
	}
	if s%60 == 0 {
		return fmt.Sprintf("%d min", s/60)
	}
	return fmt.Sprintf("%d min %d s", s/60, s%60)
}

// SelfCheckOf is wat cf-agent self-check print.
func SelfCheckOf(version string) protocol.SelfCheck {
	return protocol.SelfCheck{Version: version, Protocol: protocol.Version, Arch: runtime.GOARCH}
}
