package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Het wijzigingsjournaal: wat er op de node veranderde, ook buiten
// ClusterForge om. De agent leest alleen bestanden en roept alleen
// systemctl show en journalctl aan. Hij bewaart zijn positie in
// DefaultJournalPath en stuurt wat er sindsdien gebeurde, met een maximum
// van protocol.JournalMaxAge.

const (
	// DefaultJournalPath is het statebestand van het journaal.
	DefaultJournalPath = "/var/lib/clusterforge/journal.json"
	// maxPending is hoeveel regels de agent bewaart als de server ze niet
	// aanneemt; daarboven vallen de oudste weg.
	maxPending = 2000
	// maxWatchedFiles is het grootste aantal bestanden dat de agent volgt.
	maxWatchedFiles = 2000
	// fileGroup: meer wijzigingen dan dit onder één gevolgde map in één
	// ronde worden één regel, zoals bij een pakketinstallatie.
	fileGroup = 5
)

// watchedPath is een bestand of map waarvan de agent mtime, grootte, mode
// en eigenaar volgt. Nooit de inhoud, en ook geen hash.
type watchedPath struct {
	path string
	// depth is hoe diep de agent in een map kijkt; 0 is alleen het pad zelf.
	depth int
}

var watchedPaths = []watchedPath{
	{"/etc/keepalived", 3}, {"/etc/nginx", 3}, {"/etc/haproxy", 3}, {"/etc/mysql", 3}, {"/etc/postgresql", 4},
	{"/etc/redis", 3}, {"/etc/apache2", 3}, {"/etc/php", 4}, {"/etc/systemd/system", 3}, {"/etc/network", 3},
	{"/etc/netplan", 2}, {"/etc/hosts", 0}, {"/etc/resolv.conf", 0}, {"/etc/fstab", 0}, {"/etc/crontab", 0},
	{"/etc/cron.d", 1}, {"/etc/sysctl.conf", 0}, {"/etc/sysctl.d", 1}, {"/etc/ssh/sshd_config", 0},
	{"/etc/ssh/sshd_config.d", 1}, {"/etc/docker", 2}, {"/etc/cf-app", 3}, {"/etc/default/keepalived", 0},
	// De ingebouwde templates schrijven ook hier.
	{"/var/www/html/index.html", 0}, {"/usr/local/bin/cf-leader", 0}, {"/opt/stacks", 2},
}

// Journal leest en bewaart het wijzigingsjournaal.
type Journal struct {
	// Root is de bestandssysteemwortel; leeg is "/". Tests gebruiken een map.
	Root string
	// Path is het statebestand; leeg is DefaultJournalPath onder Root.
	Path string
	// Run voert journalctl uit; nil gebruikt exec.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	Log *slog.Logger
	// Now en Location zijn de klok en de tijdzone van de logbestanden.
	Now      func() time.Time
	Location *time.Location

	mu     sync.Mutex
	st     journalState
	loaded bool
	dirty  bool
}

// journalState staat in Path.
type journalState struct {
	BootID string `json:"boot_id,omitempty"`
	Kernel string `json:"kernel,omitempty"`
	// UnitsBoot is de boot waarvan Units de stand zijn; de monotone tijden
	// beginnen bij elke boot opnieuw.
	UnitsBoot string              `json:"units_boot,omitempty"`
	Units     map[string]unitSnap `json:"units"`
	// Files is nil tot de eerste ronde; die legt alleen de stand vast, ook
	// als die leeg is.
	Files   map[string]fileSnap `json:"files"`
	Dpkg    tailPos             `json:"dpkg"`
	History tailPos             `json:"history"`
	// AptRuns zijn de laatste apt-runs uit history.log; een regel in
	// dpkg.log binnen zo'n run komt uit apt en staat er al.
	AptRuns   []aptRun          `json:"apt_runs,omitempty"`
	Pending   []protocol.Change `json:"pending,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
}

type unitSnap struct {
	Active        string `json:"active"`
	Enabled       string `json:"enabled,omitempty"`
	Result        string `json:"result,omitempty"`
	NRestarts     int    `json:"n_restarts,omitempty"`
	ActiveEnter   uint64 `json:"active_enter,omitempty"`
	InactiveEnter uint64 `json:"inactive_enter,omitempty"`
}

type fileSnap struct {
	MTime int64  `json:"mtime"`
	Size  int64  `json:"size"`
	Mode  uint32 `json:"mode"`
	UID   uint32 `json:"uid"`
	GID   uint32 `json:"gid"`
}

type aptRun struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func (j *Journal) path(p string) string {
	if j.Root == "" {
		return p
	}
	return filepath.Join(j.Root, p)
}

func (j *Journal) statePath() string {
	if j.Path != "" {
		return j.Path
	}
	return j.path(DefaultJournalPath)
}

func (j *Journal) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *Journal) loc() *time.Location {
	if j.Location != nil {
		return j.Location
	}
	return time.Local
}

func (j *Journal) log() *slog.Logger {
	if j.Log != nil {
		return j.Log
	}
	return slog.Default()
}

func (j *Journal) read(p string) string {
	b, err := os.ReadFile(j.path(p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// load leest de state één keer. Een kapot bestand begint opnieuw: liever
// een week geschiedenis opnieuw sturen (de server ontdubbelt) dan stoppen.
func (j *Journal) load() {
	if j.loaded {
		return
	}
	j.loaded = true
	b, err := os.ReadFile(j.statePath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			j.log().Warn("journaal: state niet te lezen", "err", err)
		}
		return
	}
	if err := json.Unmarshal(b, &j.st); err != nil {
		j.log().Warn("journaal: state onleesbaar; begint opnieuw", "err", err)
		j.st = journalState{}
	}
}

func (j *Journal) save() error {
	if !j.dirty {
		return nil
	}
	b, err := json.Marshal(j.st)
	if err != nil {
		return err
	}
	p := j.statePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	j.dirty = false
	return nil
}

// add zet nieuwe regels bij de wachtende. Ouder dan JournalMaxAge valt weg,
// en boven maxPending de oudste.
func (j *Journal) add(entries ...protocol.Change) {
	if len(entries) == 0 {
		return
	}
	cutoff := j.now().Add(-protocol.JournalMaxAge)
	for _, e := range entries {
		if e.TS.Before(cutoff) {
			continue
		}
		e.TS = e.TS.UTC()
		j.st.Pending = append(j.st.Pending, e)
	}
	sort.SliceStable(j.st.Pending, func(a, b int) bool { return j.st.Pending[a].TS.Before(j.st.Pending[b].TS) })
	if over := len(j.st.Pending) - maxPending; over > 0 {
		j.st.Pending = slices.Delete(j.st.Pending, 0, over)
		j.st.Truncated = true
	}
	j.dirty = true
}

// Collect leest alle bronnen behalve de units (die komen met de heartbeat)
// en bewaart de state.
func (j *Journal) Collect(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.load()
	j.boot()
	j.packages()
	j.files()
	j.journald(ctx)
	return j.save()
}

// Next geeft de volgende regels om te sturen, hoogstens MaxChanges.
func (j *Journal) Next() protocol.Changes {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.load()
	n := min(len(j.st.Pending), protocol.MaxChanges)
	return protocol.Changes{Entries: slices.Clone(j.st.Pending[:n]), Truncated: j.st.Truncated}
}

// Sent vergeet de regels uit c, nadat de server ze aannam.
func (j *Journal) Sent(c protocol.Changes) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := min(len(c.Entries), len(j.st.Pending))
	if n == 0 {
		return nil
	}
	j.st.Pending = slices.Delete(j.st.Pending, 0, n)
	if c.Truncated {
		j.st.Truncated = false
	}
	j.dirty = true
	return j.save()
}

// boot ziet een herstart aan een nieuwe boot_id.
func (j *Journal) boot() {
	id := j.read("/proc/sys/kernel/random/boot_id")
	kernel := j.read("/proc/sys/kernel/osrelease")
	if id == "" {
		return
	}
	if j.st.BootID != "" && j.st.BootID != id {
		c := protocol.Change{TS: j.bootTime(), Kind: protocol.ChangeBoot, Subject: "boot", Action: "boot", To: kernel}
		if j.st.Kernel != kernel {
			c.From = j.st.Kernel
		}
		if c.TS.IsZero() {
			c.TS = j.now()
		}
		j.add(c)
	}
	if j.st.BootID != id || j.st.Kernel != kernel {
		j.st.BootID, j.st.Kernel = id, kernel
		j.dirty = true
	}
}

func (j *Journal) bootTime() time.Time {
	for line := range strings.SplitSeq(j.read("/proc/stat"), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(n, 0).UTC()
			}
		}
	}
	return time.Time{}
}

// monoTime zet een monotone tijd van systemd (microseconden sinds de boot)
// om naar een tijdstip.
func (j *Journal) monoTime(us uint64) time.Time {
	bt := j.bootTime()
	if us == 0 || bt.IsZero() {
		return j.now()
	}
	return bt.Add(time.Duration(us) * time.Microsecond)
}

// ObserveUnits vergelijkt de units uit de heartbeat met de vorige keer.
func (j *Journal) ObserveUnits(units []UnitInfo) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.load()
	boot := j.read("/proc/sys/kernel/random/boot_id")
	next := make(map[string]unitSnap, len(units))
	for _, u := range units {
		next[u.Name] = u.snap()
	}
	if j.st.Units != nil && j.st.UnitsBoot == boot {
		var out []protocol.Change
		for _, u := range units {
			if old, ok := j.st.Units[u.Name]; ok {
				out = append(out, j.unitChanges(u.Name, old, next[u.Name])...)
			}
		}
		j.add(out...)
	}
	if !mapsEqual(j.st.Units, next) || j.st.UnitsBoot != boot {
		j.st.Units, j.st.UnitsBoot = next, boot
		j.dirty = true
	}
	if err := j.save(); err != nil {
		j.log().Warn("journaal: state bewaren mislukt", "err", err)
	}
}

func mapsEqual[K comparable, V comparable](a, b map[K]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// up is true voor een unit die draait.
func up(state string) bool { return state == "active" || state == "reloading" }

// unitChanges zet het verschil tussen twee standen van een unit om in
// regels.
func (j *Journal) unitChanges(name string, o, n unitSnap) []protocol.Change {
	c := func(action string, ts uint64) protocol.Change {
		return protocol.Change{TS: j.monoTime(ts), Kind: protocol.ChangeService, Subject: name, Action: action}
	}
	var out []protocol.Change
	failedNow := n.Active == "failed" || (n.Active == "activating" && n.Result != "" && n.Result != "success")
	switch {
	case up(o.Active) && failedNow:
		e := c("failed", n.InactiveEnter)
		e.Detail = map[string]string{"result": n.Result}
		out = append(out, e)
	case up(o.Active) && !up(n.Active) && n.Active != "activating":
		out = append(out, c("stopped", n.InactiveEnter))
	case !up(o.Active) && up(n.Active):
		action := "started"
		if n.NRestarts > o.NRestarts {
			action = "auto_restarted"
		}
		e := c(action, n.ActiveEnter)
		if action == "auto_restarted" {
			e.Detail = map[string]string{"restarts": strconv.Itoa(n.NRestarts)}
		}
		out = append(out, e)
	case up(o.Active) && up(n.Active) && n.ActiveEnter != o.ActiveEnter && n.ActiveEnter != 0:
		// Tussen twee heartbeats gestopt en weer gestart.
		e := c("restarted", n.ActiveEnter)
		if n.NRestarts > o.NRestarts {
			e.Action = "auto_restarted"
			e.Detail = map[string]string{"restarts": strconv.Itoa(n.NRestarts)}
		}
		out = append(out, e)
	case o.Active == "failed" && n.Active == "failed" && n.InactiveEnter != o.InactiveEnter:
		e := c("failed", n.InactiveEnter)
		e.Detail = map[string]string{"result": n.Result}
		out = append(out, e)
	}
	enabled := func(s string) bool { return s == "enabled" || s == "enabled-runtime" }
	if o.Enabled != "" && n.Enabled != "" && enabled(o.Enabled) != enabled(n.Enabled) {
		action := "disabled"
		if enabled(n.Enabled) {
			action = "enabled"
		}
		e := protocol.Change{TS: j.now(), Kind: protocol.ChangeService, Subject: name, Action: action, From: o.Enabled, To: n.Enabled}
		out = append(out, e)
	}
	return out
}
