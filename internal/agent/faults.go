package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Faults voert de storingen van een rampoefening uit en draait ze terug.
// Elke storing staat in Path voordat ze aangaat; zo kan de agent haar
// terugdraaien als de lease of de bovengrens verloopt, bij elke start, en
// bij het opstarten van de machine met cf-agent fault-clear, ook als de
// agent zelf niet start.
type Faults struct {
	// Path is het statebestand, Dir de map voor het nft-script en het
	// vulbestand. Allebei naast de state van de agent.
	Path string
	Dir  string
	// Exec voert ip, nft en tc uit.
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
	Log  *slog.Logger
	// Now is de klok; nil is time.Now.
	Now func() time.Time
	// Interfaces zijn de interfaces van de node; CutOff zegt of de agent via
	// deze interface met de server praat.
	Interfaces func() []protocol.Interface
	CutOff     func(name string) bool
	// Statfs leest het bestandssysteem van Dir; nil is statfs.
	Statfs func(path string) (fsStat, bool)
	// Delay is hoe lang link_down op de interface naar de server wacht, zodat
	// het antwoord nog weg kan. Nul is een seconde.
	Delay time.Duration

	mu sync.Mutex
}

// activeFault is een storing in het statebestand.
type activeFault struct {
	protocol.Fault
	AppliedAt time.Time `json:"applied_at"`
	// LeaseUntil is nul als alleen de bovengrens telt: bij link_down op de
	// interface naar de server.
	LeaseUntil time.Time `json:"lease_until,omitzero"`
	HardUntil  time.Time `json:"hard_until"`
	// FillFile is bij disk_fill het vulbestand.
	FillFile string `json:"fill_file,omitempty"`
}

func (a activeFault) lease() time.Duration {
	if a.LeaseSeconds > 0 {
		return time.Duration(a.LeaseSeconds) * time.Second
	}
	return protocol.FaultLease
}

type faultFile struct {
	Faults []activeFault `json:"faults"`
}

// FaultsFile en fillFile liggen naast de state van de agent.
const (
	FaultsFile  = "faults.json"
	nftScript   = "cf-fault.nft"
	fillFile    = "cf-fault-fill"
	nftTable    = "cf_fault"
	vrrpProto   = 112
	nftPriority = -10
)

var (
	faultID   = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,100}$`)
	ifaceName = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)
)

// NewFaults maakt Faults naast het statebestand van de agent.
func NewFaults(statePath string, exec func(ctx context.Context, name string, args ...string) ([]byte, error), log *slog.Logger) *Faults {
	dir := filepath.Dir(statePath)
	return &Faults{Path: filepath.Join(dir, FaultsFile), Dir: dir, Exec: exec, Log: log}
}

func (f *Faults) logger() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (f *Faults) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Faults) load() (faultFile, error) {
	var ff faultFile
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return ff, nil
	}
	if err != nil {
		return ff, err
	}
	if err := json.Unmarshal(b, &ff); err != nil {
		return ff, fmt.Errorf("%s: %w", f.Path, err)
	}
	return ff, nil
}

func (f *Faults) save(ff faultFile) error {
	if len(ff.Faults) == 0 {
		err := os.Remove(f.Path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := json.MarshalIndent(ff, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(f.Path, append(b, '\n'), 0o600)
}

// Active geeft de ids van de storingen die nu actief zijn.
func (f *Faults) Active() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, err := f.load()
	if err != nil {
		return nil
	}
	out := make([]string, len(ff.Faults))
	for i, a := range ff.Faults {
		out[i] = a.ID
	}
	return out
}

// Inject zet een storing aan. Een id dat al actief is, doet niets.
func (f *Faults) Inject(ctx context.Context, fault protocol.Fault) protocol.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, err := f.load()
	if err != nil {
		return failed(nil, err.Error())
	}
	if slices.ContainsFunc(ff.Faults, func(a activeFault) bool { return a.ID == fault.ID }) {
		return protocol.Result{OK: true, Repeat: true, Output: []string{"deze storing is al actief"}}
	}
	cutOff, err := f.validate(fault, ff)
	if err != nil {
		return failed(nil, err.Error())
	}
	now := f.now()
	a := activeFault{Fault: fault, AppliedAt: now, HardUntil: now.Add(time.Duration(fault.MaxSeconds) * time.Second)}
	if !cutOff {
		a.LeaseUntil = earliest(now.Add(a.lease()), a.HardUntil)
	}
	if fault.Kind == protocol.FaultDiskFill {
		a.FillFile = filepath.Join(f.Dir, fillFile)
	}
	// Eerst opschrijven, dan pas aanzetten: zo kan het terugdraaien altijd.
	ff.Faults = append(ff.Faults, a)
	if err := f.save(ff); err != nil {
		return failed(nil, "storing bewaren mislukt: "+err.Error())
	}
	if cutOff {
		// Het antwoord gaat over deze interface; eerst dat, dan de storing.
		go f.applyLater(a)
		return done([]string{fmt.Sprintf("%s gaat over %s down; de agent zet hem na %d s zelf weer aan, ook zonder ClusterForge",
			fault.Interface, delayText(f.delay()), fault.MaxSeconds)})
	}
	out, err := f.apply(ctx, a, ff.Faults)
	if err != nil {
		ff.Faults = ff.Faults[:len(ff.Faults)-1]
		rout, rerr := f.revert(context.WithoutCancel(ctx), a, ff.Faults)
		out = append(out, rout...)
		if rerr != nil {
			out = append(out, "terugdraaien: "+rerr.Error())
		}
		if serr := f.save(ff); serr != nil {
			out = append(out, "state bewaren: "+serr.Error())
		}
		return failed(out, err.Error())
	}
	return done(append(out, describeFault(fault)+fmt.Sprintf("; zonder verlenging na %d s weer weg, hoe dan ook na %d s",
		int(a.LeaseUntil.Sub(now).Seconds()), fault.MaxSeconds)))
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func (f *Faults) delay() time.Duration {
	if f.Delay > 0 {
		return f.Delay
	}
	return time.Second
}

func delayText(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%d s", int(d.Seconds()))
}

// applyLater zet link_down op de interface naar de server aan, na het
// antwoord. Is de storing intussen opgeheven, dan gebeurt er niets.
func (f *Faults) applyLater(a activeFault) {
	time.Sleep(f.delay())
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, err := f.load()
	if err != nil || !slices.ContainsFunc(ff.Faults, func(x activeFault) bool { return x.ID == a.ID }) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := f.apply(ctx, a, ff.Faults); err != nil {
		f.logger().Error("storing aanzetten mislukt", "id", a.ID, "err", err)
		ff.Faults = slices.DeleteFunc(ff.Faults, func(x activeFault) bool { return x.ID == a.ID })
		_, _ = f.revert(ctx, a, ff.Faults)
		_ = f.save(ff)
		return
	}
	f.logger().Warn("storing aangezet", "id", a.ID, "kind", a.Kind, "interface", a.Interface, "tot", a.HardUntil)
}

// validate controleert een storing tegen de vaste grenzen en de storingen
// die er al zijn. cutOff is true bij link_down op de interface naar de
// server.
func (f *Faults) validate(fault protocol.Fault, ff faultFile) (cutOff bool, err error) {
	if !faultID.MatchString(fault.ID) {
		return false, errors.New("ongeldig id voor een storing")
	}
	if fault.MaxSeconds < 1 || time.Duration(fault.MaxSeconds)*time.Second > protocol.FaultMax {
		return false, fmt.Errorf("een storing duurt hoogstens %d s", int(protocol.FaultMax.Seconds()))
	}
	if fault.LeaseSeconds < 0 || fault.LeaseSeconds > 120 {
		return false, errors.New("de lease ligt tussen 1 en 120 s")
	}
	same := func(match func(a activeFault) bool) bool { return slices.ContainsFunc(ff.Faults, match) }
	switch fault.Kind {
	case protocol.FaultLinkDown, protocol.FaultNetem:
		if err := f.checkInterface(fault.Interface); err != nil {
			return false, err
		}
		if same(func(a activeFault) bool { return a.Interface == fault.Interface }) {
			return false, fmt.Errorf("op %s staat al een storing", fault.Interface)
		}
		if fault.Kind == protocol.FaultNetem {
			if fault.DelayMS < 0 || fault.DelayMS > protocol.NetemMaxDelayMS || fault.LossPercent < 0 || fault.LossPercent > protocol.NetemMaxLoss ||
				fault.DelayMS == 0 && fault.LossPercent == 0 {
				return false, fmt.Errorf("netem: vertraging tot %d ms en verlies tot %d %%, en minstens een van beide",
					protocol.NetemMaxDelayMS, protocol.NetemMaxLoss)
			}
			return false, nil
		}
		if f.CutOff != nil && f.CutOff(fault.Interface) {
			if time.Duration(fault.MaxSeconds)*time.Second > protocol.FaultMaxCutOff {
				return false, fmt.Errorf("%s is de interface naar ClusterForge; daar duurt link_down hoogstens %d s",
					fault.Interface, int(protocol.FaultMaxCutOff.Seconds()))
			}
			return true, nil
		}
	case protocol.FaultVRRPBlock:
		if same(func(a activeFault) bool { return a.Kind == protocol.FaultVRRPBlock }) {
			return false, errors.New("VRRP is al geblokkeerd")
		}
	case protocol.FaultPortBlock:
		if fault.Port < 1 || fault.Port > 65535 {
			return false, errors.New("ongeldige poort")
		}
		if same(func(a activeFault) bool { return a.Kind == protocol.FaultPortBlock && a.Port == fault.Port }) {
			return false, fmt.Errorf("poort %d is al geblokkeerd", fault.Port)
		}
	case protocol.FaultDiskFill:
		if fault.FillPercent < 1 || fault.FillPercent > protocol.DiskFillMax {
			return false, fmt.Errorf("disk_fill gaat hoogstens tot %d %%", protocol.DiskFillMax)
		}
		if same(func(a activeFault) bool { return a.Kind == protocol.FaultDiskFill }) {
			return false, errors.New("de schijf wordt al gevuld")
		}
	default:
		return false, fmt.Errorf("onbekende storing %q", fault.Kind)
	}
	return false, nil
}

func (f *Faults) checkInterface(name string) error {
	if !ifaceName.MatchString(name) {
		return errors.New("ongeldige interface")
	}
	if name == "lo" {
		return errors.New("de loopback-interface mag niet")
	}
	ifs := interfaces()
	if f.Interfaces != nil {
		ifs = f.Interfaces()
	}
	if !slices.ContainsFunc(ifs, func(i protocol.Interface) bool { return i.Name == name }) {
		return fmt.Errorf("interface %s bestaat niet op deze node", name)
	}
	return nil
}

func describeFault(fault protocol.Fault) string {
	switch fault.Kind {
	case protocol.FaultLinkDown:
		return fault.Interface + " down"
	case protocol.FaultVRRPBlock:
		return "VRRP geblokkeerd"
	case protocol.FaultPortBlock:
		return fmt.Sprintf("TCP-poort %d geblokkeerd voor inkomend verkeer", fault.Port)
	case protocol.FaultNetem:
		return fault.Interface + ": " + netemText(fault)
	case protocol.FaultDiskFill:
		return fmt.Sprintf("schijf gevuld tot %d %%", fault.FillPercent)
	}
	return fault.Kind
}

func netemText(fault protocol.Fault) string {
	var parts []string
	if fault.DelayMS > 0 {
		parts = append(parts, fmt.Sprintf("%d ms vertraging", fault.DelayMS))
	}
	if fault.LossPercent > 0 {
		parts = append(parts, fmt.Sprintf("%d %% pakketverlies", fault.LossPercent))
	}
	return strings.Join(parts, " en ")
}

// apply zet een storing aan; all zijn alle actieve storingen, met deze.
func (f *Faults) apply(ctx context.Context, a activeFault, all []activeFault) ([]string, error) {
	switch a.Kind {
	case protocol.FaultLinkDown:
		return f.run(ctx, "ip", "link", "set", "dev", a.Interface, "down")
	case protocol.FaultVRRPBlock, protocol.FaultPortBlock:
		return f.syncNft(ctx, all)
	case protocol.FaultNetem:
		out, err := f.run(ctx, "tc", "qdisc", "show", "dev", a.Interface, "root")
		if err != nil {
			return out, err
		}
		if q := rootQdisc(out); !slices.Contains(defaultQdiscs, q) {
			return out, fmt.Errorf("op %s staat al een eigen qdisc (%s); netem zou die vervangen", a.Interface, q)
		}
		args := []string{"qdisc", "replace", "dev", a.Interface, "root", "netem"}
		if a.DelayMS > 0 {
			args = append(args, "delay", strconv.Itoa(a.DelayMS)+"ms")
		}
		if a.LossPercent > 0 {
			args = append(args, "loss", strconv.Itoa(a.LossPercent)+"%")
		}
		return f.run(ctx, "tc", args...)
	case protocol.FaultDiskFill:
		return f.fill(a)
	}
	return nil, fmt.Errorf("onbekende storing %q", a.Kind)
}

// revert draait een storing terug; rest zijn de storingen die blijven.
func (f *Faults) revert(ctx context.Context, a activeFault, rest []activeFault) ([]string, error) {
	switch a.Kind {
	case protocol.FaultLinkDown:
		return f.run(ctx, "ip", "link", "set", "dev", a.Interface, "up")
	case protocol.FaultVRRPBlock, protocol.FaultPortBlock:
		return f.syncNft(ctx, rest)
	case protocol.FaultNetem:
		out, err := f.run(ctx, "tc", "qdisc", "show", "dev", a.Interface, "root")
		if err != nil || rootQdisc(out) != "netem" {
			// Weg, of nooit gezet: niets te doen.
			return nil, nil
		}
		return f.run(ctx, "tc", "qdisc", "del", "dev", a.Interface, "root")
	case protocol.FaultDiskFill:
		if a.FillFile == "" {
			return nil, nil
		}
		if err := os.Remove(a.FillFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return []string{"vulbestand verwijderd"}, nil
	}
	return nil, nil
}

// defaultQdiscs zijn de qdiscs die een kernel zelf zet; netem mag ze
// vervangen, want na het verwijderen komt de standaard terug.
var defaultQdiscs = []string{"noqueue", "pfifo_fast", "fq_codel", "mq", "fq", "pfifo", "cake"}

// rootQdisc leest de soort uit "qdisc fq_codel 0: root refcnt 2 ...".
func rootQdisc(out []string) string {
	for _, l := range out {
		if f := strings.Fields(l); len(f) > 1 && f[0] == "qdisc" {
			return f[1]
		}
	}
	return ""
}

func (f *Faults) run(ctx context.Context, name string, args ...string) ([]string, error) {
	run := f.Exec
	if run == nil {
		run = systemExec
	}
	out, err := run(ctx, name, args...)
	lines := outputLines(out)
	if err != nil {
		return lines, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return lines, nil
}

// syncNft zet de tabel cf_fault gelijk aan de storingen met nft. Zonder
// zulke storingen verdwijnt de tabel.
func (f *Faults) syncNft(ctx context.Context, all []activeFault) ([]string, error) {
	var in, out []string
	for _, a := range all {
		comment := strconv.Quote(a.ID)
		switch a.Kind {
		case protocol.FaultVRRPBlock:
			rule := fmt.Sprintf("meta l4proto %d drop comment %s", vrrpProto, comment)
			in, out = append(in, rule), append(out, rule)
		case protocol.FaultPortBlock:
			in = append(in, fmt.Sprintf("iifname != \"lo\" tcp dport %d drop comment %s", a.Port, comment))
		}
	}
	if len(in) == 0 && len(out) == 0 {
		lines, err := f.run(ctx, "nft", "delete", "table", "inet", nftTable)
		if err != nil && slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "No such file") }) {
			return nil, nil
		}
		return lines, err
	}
	var b strings.Builder
	// Bestaat de tabel nog niet, dan maakt de eerste regel haar, zodat het
	// verwijderen altijd lukt; daarna komt ze in één keer terug.
	fmt.Fprintf(&b, "table inet %s\ndelete table inet %s\ntable inet %s {\n", nftTable, nftTable, nftTable)
	for _, ch := range []struct {
		name  string
		rules []string
	}{{"input", in}, {"output", out}} {
		fmt.Fprintf(&b, "\tchain %s {\n\t\ttype filter hook %s priority %d; policy accept;\n", ch.name, ch.name, nftPriority)
		for _, r := range ch.rules {
			fmt.Fprintf(&b, "\t\t%s\n", r)
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	path := filepath.Join(f.Dir, nftScript)
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, []byte(b.String()), 0o600); err != nil {
		return nil, err
	}
	return f.run(ctx, "nft", "-f", path)
}

// fill maakt het vulbestand tot het bestandssysteem op het percentage
// staat, zoals df het rekent.
func (f *Faults) fill(a activeFault) ([]string, error) {
	sf := f.Statfs
	if sf == nil {
		sf = statfs
	}
	st, ok := sf(f.Dir)
	if !ok || st.size == 0 {
		return nil, errors.New("de schijf van " + f.Dir + " is niet te lezen")
	}
	used := st.size - st.free
	target := float64(a.FillPercent) / 100 * float64(used+st.avail)
	if float64(used) >= target {
		return []string{fmt.Sprintf("de schijf stond al op %.0f %%; er is niets bijgeschreven", 100*float64(used)/float64(used+st.avail))}, nil
	}
	n := int64(target) - int64(used) //nolint:gosec // past ruim in int64
	if err := allocate(a.FillFile, n); err != nil {
		_ = os.Remove(a.FillFile)
		return nil, fmt.Errorf("vulbestand: %w", err)
	}
	return []string{fmt.Sprintf("%s geschreven in %s", formatSize(n), a.FillFile)}, nil
}

// systemExec voert een commando uit met een eigen tijdslimiet.
func systemExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.CombinedOutput()
}

// writeZeros vult een bestand met n nullen, als fallocate niet kan.
func writeZeros(path string, n int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	buf := make([]byte, 4<<20)
	for n > 0 {
		k := min(n, int64(len(buf)))
		if _, err := f.Write(buf[:k]); err != nil {
			_ = f.Close()
			return err
		}
		n -= k
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func formatSize(n int64) string {
	const gib, mib = 1 << 30, 1 << 20
	if n >= gib {
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	}
	return fmt.Sprintf("%d MiB", n/mib)
}

// Renew verlengt de lease van storingen met hun eigen lease. Wat de agent
// niet meer kent, is al teruggedraaid; dat staat in de fout.
func (f *Faults) Renew(ids []string) protocol.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, err := f.load()
	if err != nil {
		return failed(nil, err.Error())
	}
	now := f.now()
	var gone []string
	for _, id := range ids {
		i := slices.IndexFunc(ff.Faults, func(a activeFault) bool { return a.ID == id })
		if i < 0 {
			gone = append(gone, id)
			continue
		}
		if a := &ff.Faults[i]; !a.LeaseUntil.IsZero() {
			a.LeaseUntil = earliest(now.Add(a.lease()), a.HardUntil)
		}
	}
	if err := f.save(ff); err != nil {
		return failed(nil, err.Error())
	}
	if len(gone) > 0 {
		return failed(nil, "niet meer actief: "+strings.Join(gone, ", "))
	}
	return done(nil)
}

// Clear draait storingen terug; zonder ids alles. Een onbekend id telt niet.
func (f *Faults) Clear(ctx context.Context, ids []string) protocol.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clear(ctx, func(a activeFault) bool { return len(ids) == 0 || slices.Contains(ids, a.ID) }, "opgeheven")
}

// Expire draait storingen terug waarvan de lease of de bovengrens voorbij
// is; true als er iets veranderde.
func (f *Faults) Expire(ctx context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	res := f.clear(ctx, func(a activeFault) bool {
		return !now.Before(a.HardUntil) || !a.LeaseUntil.IsZero() && !now.Before(a.LeaseUntil)
	}, "verlopen")
	if !res.OK {
		f.logger().Error("verlopen storing terugdraaien mislukt", "err", res.Error)
	}
	return len(res.Output) > 0
}

// ClearAll draait alle storingen terug die nog in de state staan: bij de
// start van de agent en met cf-agent fault-clear bij het opstarten.
func (f *Faults) ClearAll(ctx context.Context) protocol.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clear(ctx, func(activeFault) bool { return true }, "achtergebleven, teruggedraaid")
}

// clear draait de storingen terug waarvoor match true is, de laatste eerst.
// Wat niet terug kan, blijft in de state voor een volgende poging.
func (f *Faults) clear(ctx context.Context, match func(a activeFault) bool, why string) protocol.Result {
	ff, err := f.load()
	if err != nil {
		return failed(nil, err.Error())
	}
	var out, errs []string
	keep := slices.Clone(ff.Faults)
	for i := len(ff.Faults) - 1; i >= 0; i-- {
		a := ff.Faults[i]
		if !match(a) {
			continue
		}
		rest := slices.DeleteFunc(slices.Clone(keep), func(x activeFault) bool { return x.ID == a.ID })
		lines, err := f.revert(ctx, a, rest)
		out = append(out, lines...)
		if err != nil {
			errs = append(errs, a.ID+": "+err.Error())
			continue
		}
		keep = rest
		msg := describeFault(a.Fault) + " " + why
		out = append(out, msg)
		f.logger().Info("storing "+why, "id", a.ID, "kind", a.Kind)
	}
	ff.Faults = keep
	if err := f.save(ff); err != nil {
		errs = append(errs, "state bewaren: "+err.Error())
	}
	if len(errs) > 0 {
		return failed(out, strings.Join(errs, "; "))
	}
	return done(out)
}

// initFaults maakt de storingen naast de state van de agent.
func (a *Agent) initFaults() {
	if a.faults != nil {
		return
	}
	a.faults = NewFaults(a.statePath(), a.exec, a.Log)
	a.faults.Interfaces = a.ifaces
	a.faults.CutOff = a.cutOff
}

// faultLoop draait verlopen storingen elke seconde terug; de server ziet
// dat meteen in de facts.
func (a *Agent) faultLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if a.faults.Expire(ctx) {
				a.signal(a.factsNow)
			}
		}
	}
}

// cutOff is true voor de interface met het adres waarmee de agent de server
// bereikt.
func (a *Agent) cutOff(name string) bool {
	addr := ""
	if a.Collector != nil && a.Collector.PrimaryAddress != nil {
		addr = a.Collector.PrimaryAddress()
	}
	if addr == "" {
		// Onbekend: dan geldt de strengste grens.
		return true
	}
	for _, i := range a.ifaces() {
		if i.Name != name {
			continue
		}
		return slices.ContainsFunc(i.Addresses, func(cidr string) bool {
			ip, _, _ := strings.Cut(cidr, "/")
			return ip == addr
		})
	}
	return false
}

func (a *Agent) ifaces() []protocol.Interface {
	if a.Collector != nil && a.Collector.Interfaces != nil {
		return a.Collector.Interfaces()
	}
	return interfaces()
}
