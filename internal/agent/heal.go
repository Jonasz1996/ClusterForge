package agent

// Zelfherstel (agentprotocol 22): vaststellen wat er met MariaDB, MySQL en
// PostgreSQL op deze node mis is, ook als ClusterForge ze niet toepaste. De
// agent leest wat systemd over de units zegt, welke processen er echt
// draaien (een mariadbd die blijft aansluiten terwijl systemd de unit al op
// failed zette), de vrije ruimte in de datamap, en de config van Galera of
// Patroni met het bestand en de regel waar elk adres staat. Alleen set_galera_address
// verandert iets: de adressen in dat bestand, na een kopie. Dat werkt pas
// bij de volgende start van MariaDB.

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// healUnits zijn de units die Zelfherstel bekijkt; dezelfde als dbUnits.
var healUnits = dbUnits

// Waar MariaDB en MySQL hun config lezen; de eerste die bestaat, met de
// !include en !includedir erin.
var mariaConfigRoots = []string{"/etc/mysql/my.cnf", "/etc/my.cnf"}

// De secties in de config die mariadbd leest.
func mariaSection(name string) bool {
	switch name {
	case "mysqld", "server", "galera", "mariadb", "mariadbd":
		return true
	}
	return strings.HasPrefix(name, "mariadb-") || strings.HasPrefix(name, "mysqld-") || strings.HasPrefix(name, "mariadbd-")
}

// De Galera-sleutels waarvan de agent bijhoudt waar ze staan.
const (
	keyClusterAddress = "wsrep_cluster_address"
	keyNodeAddress    = "wsrep_node_address"
	keySSTReceive     = "wsrep_sst_receive_address"
	keyClusterName    = "wsrep_cluster_name"
)

var healKeys = []string{keyClusterAddress, keyNodeAddress, keySSTReceive, keyClusterName}

// peerTimeout is de regel waarmee Galera meldt dat een peer niet antwoordt.
var peerTimeout = regexp.MustCompile(`connection to peer [0-9a-f-]+ with addr tcp://\[?([0-9A-Fa-f:.]+?)\]?:\d+ timed out`)

func (a *Agent) dbHeal(ctx context.Context, req *protocol.DBHealRequest) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	if req == nil {
		res.Error = "geen verzoek"
		return res
	}
	switch req.Action {
	case protocol.HealInspect:
		engines, err := a.healInspect(ctx)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.Heal = &protocol.DBHealResult{Engines: engines}
	case protocol.HealSetGaleraAddress:
		changed, err := a.setGaleraAddress(ctx, req)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		engines, _ := a.healInspect(ctx)
		res.Heal = &protocol.DBHealResult{Engines: engines, Changed: changed}
	default:
		res.Error = "onbekende actie " + req.Action
	}
	return res
}

// healUnit is wat systemctl show over een unit zegt.
type healUnit struct {
	name, active, sub, result, enabled string
	mainPID                            int
	changed                            uint64
}

func (a *Agent) healUnits(ctx context.Context) ([]healUnit, error) {
	out, err := a.dbRun(ctx, "systemctl", append([]string{"show",
		"--property=Id,LoadState,ActiveState,SubState,Result,UnitFileState,MainPID,StateChangeTimestampMonotonic"}, healUnits...)...)
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var units []healUnit
	for block := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n\n") {
		kv := map[string]string{}
		for line := range strings.SplitSeq(block, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				kv[k] = v
			}
		}
		name := strings.TrimSuffix(kv["Id"], ".service")
		if name == "" || kv["LoadState"] == "not-found" || slices.ContainsFunc(units, func(u healUnit) bool { return u.name == name }) {
			continue
		}
		u := healUnit{name: name, active: kv["ActiveState"], sub: kv["SubState"], result: kv["Result"], enabled: kv["UnitFileState"]}
		u.mainPID, _ = strconv.Atoi(kv["MainPID"])
		u.changed, _ = strconv.ParseUint(kv["StateChangeTimestampMonotonic"], 10, 64)
		units = append(units, u)
	}
	return units, nil
}

func (a *Agent) healInspect(ctx context.Context) ([]protocol.HealEngine, error) {
	units, err := a.healUnits(ctx)
	if err != nil {
		return nil, fmt.Errorf("systemctl show: %w", err)
	}
	uptime := a.uptime()
	procs := a.dbProcesses(uptime)
	var out []protocol.HealEngine
	pick := func(engine string, match func(string) bool) *healUnit {
		var best *healUnit
		for i, u := range units {
			if !match(u.name) {
				continue
			}
			// Patroni gaat voor PostgreSQL; een instantie die draait voor
			// een die stilstaat.
			if best == nil || u.name == "patroni" || best.name != "patroni" && u.active == "active" && best.active != "active" {
				best = &units[i]
			}
		}
		return best
	}
	if u := pick(protocol.DBMariaDB, func(n string) bool { return n == "mariadb" || n == "mysql" }); u != nil {
		e := a.healEngine(*u, uptime, procs, protocol.DBMariaDB)
		a.healMaria(ctx, &e)
		out = append(out, e)
	}
	if u := pick(protocol.DBPostgreSQL, func(n string) bool { return n == "patroni" || n == "postgresql" || strings.HasPrefix(n, "postgresql@") }); u != nil {
		e := a.healEngine(*u, uptime, procs, protocol.DBPostgreSQL)
		e.DataDir = "/var/lib/postgresql"
		if u.name == "patroni" {
			a.healPatroni(ctx, &e)
		}
		a.healDisk(&e)
		out = append(out, e)
	}
	if out == nil {
		out = []protocol.HealEngine{}
	}
	return out, nil
}

func (a *Agent) healEngine(u healUnit, uptime time.Duration, procs []protocol.HealProcess, engine string) protocol.HealEngine {
	e := protocol.HealEngine{Engine: engine, Unit: u.name, Active: u.active, Sub: u.sub, Result: u.result, Enabled: u.enabled, MainPID: u.mainPID}
	if u.changed > 0 && uptime > 0 {
		e.StateSeconds = max(0, int64(uptime/time.Second)-int64(u.changed/1_000_000)) //nolint:gosec // monotone tijd past in int64
	}
	for _, p := range procs {
		if engineOf(p.Name) == engine {
			e.Processes = append(e.Processes, p)
		}
	}
	return e
}

func engineOf(process string) string {
	switch process {
	case "mariadbd", "mysqld":
		return protocol.DBMariaDB
	case "postgres", "patroni":
		return protocol.DBPostgreSQL
	}
	return ""
}

// uptime leest /proc/uptime; nul als dat niet lukt.
func (a *Agent) uptime() time.Duration {
	p, err := a.path("/proc/uptime")
	if err != nil {
		return 0
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	f, err := strconv.ParseFloat(strings.Fields(string(b) + " 0")[0], 64)
	if err != nil {
		return 0
	}
	return time.Duration(f * float64(time.Second))
}

// clockTicks is USER_HZ van Linux; op elke gangbare kernel 100.
const clockTicks = 100

// dbProcesses zoekt in /proc de hoofdprocessen van de databases: mariadbd,
// mysqld, patroni, en van PostgreSQL alleen de postmaster (een postgres
// waarvan de ouder geen postgres is).
func (a *Agent) dbProcesses(uptime time.Duration) []protocol.HealProcess {
	root, err := a.path("/proc")
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	comm := func(pid string) string {
		b, _ := os.ReadFile(filepath.Join(root, pid, "comm"))
		return strings.TrimSpace(string(b))
	}
	var out []protocol.HealProcess
	for _, ent := range entries {
		pid, err := strconv.Atoi(ent.Name())
		if err != nil {
			continue
		}
		name := comm(ent.Name())
		if engineOf(name) == "" {
			continue
		}
		stat, _ := os.ReadFile(filepath.Join(root, ent.Name(), "stat"))
		ppid, start := procStat(string(stat))
		if name == "postgres" && comm(strconv.Itoa(ppid)) == "postgres" {
			continue
		}
		p := protocol.HealProcess{PID: pid, Name: name}
		if cg, err := os.ReadFile(filepath.Join(root, ent.Name(), "cgroup")); err == nil {
			p.Unit = unitFromCgroup(string(cg))
		}
		if uptime > 0 && start > 0 {
			p.AgeSeconds = max(0, int64(uptime/time.Second)-int64(start/clockTicks)) //nolint:gosec // tikken passen in int64
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(x, y protocol.HealProcess) int { return cmp.Compare(x.PID, y.PID) })
	return out
}

// procStat haalt de ouder en de starttijd (in tikken na de boot) uit
// /proc/<pid>/stat. De naam staat tussen haakjes en kan spaties hebben.
func procStat(s string) (ppid int, start uint64) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0
	}
	f := strings.Fields(s[i+1:])
	// Na de naam: state (3), ppid (4), ..., starttime (22).
	if len(f) > 1 {
		ppid, _ = strconv.Atoi(f[1])
	}
	if len(f) > 19 {
		start, _ = strconv.ParseUint(f[19], 10, 64)
	}
	return ppid, start
}

func (a *Agent) healDisk(e *protocol.HealEngine) {
	if e.DataDir == "" {
		return
	}
	p, err := a.path(e.DataDir)
	if err != nil {
		return
	}
	if st, ok := statfs(p); ok {
		e.DataFree, e.DataTotal = st.avail, st.size
	}
}

// healMaria vult de datamap, de vrije ruimte en de Galera-config.
func (a *Agent) healMaria(ctx context.Context, e *protocol.HealEngine) {
	defaults, err := a.printDefaults(ctx)
	if err != nil {
		e.Error = err.Error()
	}
	e.DataDir = cmp.Or(defaults["datadir"], "/var/lib/mysql")
	a.healDisk(e)
	settings := a.mariaSettings()
	g := &protocol.HealGalera{
		ClusterName: defaults[keyClusterName], ClusterAddress: defaults[keyClusterAddress],
		NodeAddress: defaults[keyNodeAddress], SSTReceiveAddress: defaults[keySSTReceive], Settings: settings,
	}
	g.On = isOn(defaults["wsrep_on"]) || defaults["wsrep_provider"] != "" && defaults["wsrep_provider"] != "none"
	if !g.On && g.ClusterAddress == "" {
		return
	}
	g.PeerTimeouts = a.peerTimeouts(ctx, e.Unit)
	e.Galera = g
}

func isOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "1", "true", "yes":
		return true
	}
	return false
}

// printDefaults vraagt de geldende opties van mariadbd aan
// my_print_defaults, met de sleutels in de vorm met underscores. Een optie
// die vaker staat, geldt met haar laatste waarde.
func (a *Agent) printDefaults(ctx context.Context) (map[string]string, error) {
	groups := []string{"--mysqld", "server", "galera", "mariadb", "mariadbd"}
	var out []byte
	var err error
	for _, prog := range []string{"my_print_defaults", "mariadb-print-defaults"} {
		out, err = a.dbRun(ctx, prog, groups...)
		if err == nil {
			break
		}
	}
	if err != nil {
		return map[string]string{}, fmt.Errorf("my_print_defaults: %w", err)
	}
	m := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		k, v, _ := strings.Cut(strings.TrimPrefix(line, "--"), "=")
		if k == "" || !strings.HasPrefix(line, "--") {
			continue
		}
		m[normKey(k)] = unquote(v)
	}
	return m, nil
}

func normKey(k string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(k), "-", "_"))
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

// mariaSettings leest de configbestanden in de volgorde van MariaDB en geeft
// elke regel die een van de healKeys zet in een sectie van mariadbd.
func (a *Agent) mariaSettings() []protocol.HealSetting {
	var out []protocol.HealSetting
	seen := map[string]bool{}
	var read func(path string, depth int)
	read = func(path string, depth int) {
		if depth > 8 || seen[path] {
			return
		}
		seen[path] = true
		p, err := a.path(path)
		if err != nil {
			return
		}
		f, err := os.Open(p)
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()
		section := ""
		sc := bufio.NewScanner(f)
		n := 0
		for sc.Scan() {
			n++
			line := strings.TrimSpace(sc.Text())
			switch {
			case line == "" || line[0] == '#' || line[0] == ';':
			case strings.HasPrefix(line, "!includedir"):
				dir := strings.TrimSpace(strings.TrimPrefix(line, "!includedir"))
				for _, file := range a.cnfFiles(dir) {
					read(file, depth+1)
				}
			case strings.HasPrefix(line, "!include"):
				read(strings.TrimSpace(strings.TrimPrefix(line, "!include")), depth+1)
			case line[0] == '[':
				section = strings.ToLower(strings.Trim(line, "[] "))
			case mariaSection(section):
				k, v, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				if key := normKey(k); slices.Contains(healKeys, key) {
					out = append(out, protocol.HealSetting{Key: key, Value: unquote(stripComment(v)), File: path, Line: n})
				}
			}
		}
	}
	for _, root := range mariaConfigRoots {
		if p, err := a.path(root); err == nil {
			if _, err := os.Stat(p); err == nil {
				read(root, 0)
				break
			}
		}
	}
	return out
}

// cnfFiles zijn de bestanden die !includedir leest: *.cnf, op naam.
func (a *Agent) cnfFiles(dir string) []string {
	p, err := a.path(filepath.Clean(dir))
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".cnf") {
			out = append(out, filepath.Join(filepath.Clean(dir), e.Name()))
		}
	}
	slices.Sort(out)
	return out
}

func stripComment(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// peerTimeouts zijn de adressen die Galera de laatste vijftien minuten niet
// bereikte.
func (a *Agent) peerTimeouts(ctx context.Context, unit string) []string {
	out, err := a.dbRun(ctx, "journalctl", "-u", unit+".service", "--since", "-15min", "--no-pager", "-o", "cat", "-n", "400")
	if err != nil && len(out) == 0 {
		return nil
	}
	var addrs []string
	for _, m := range peerTimeout.FindAllStringSubmatch(string(out), -1) {
		if !slices.Contains(addrs, m[1]) {
			addrs = append(addrs, m[1])
		}
	}
	slices.Sort(addrs)
	return addrs
}

// Een gcomm-adres: gcomm:// met adressen, elk met een poort mag.
var gcommAddress = regexp.MustCompile(`^gcomm://([0-9A-Za-z.:\[\]-]+)(,[0-9A-Za-z.:\[\]-]+)*$`)

// setGaleraAddress zet de adressen in het bestand waar ze nu gelden. Een
// adres dat nergens staat, komt onder wsrep_cluster_address in hetzelfde
// bestand. Daarna moet my_print_defaults de nieuwe waarden geven; anders
// zet de agent de kopieën terug. Een MariaDB die draait, leest het bestand
// pas bij zijn volgende start; de agent herstart niets.
func (a *Agent) setGaleraAddress(ctx context.Context, req *protocol.DBHealRequest) ([]protocol.HealChange, error) {
	if req.ClusterAddress == "" && req.NodeAddress == "" {
		return nil, errors.New("geen adres om te zetten")
	}
	if req.ClusterAddress != "" && !gcommAddress.MatchString(req.ClusterAddress) {
		return nil, fmt.Errorf("%q is geen gcomm-adres", req.ClusterAddress)
	}
	if req.NodeAddress != "" && net.ParseIP(hostOnly(req.NodeAddress)) == nil {
		return nil, fmt.Errorf("%q is geen IP-adres", req.NodeAddress)
	}
	settings := a.mariaSettings()
	last := func(key string) *protocol.HealSetting {
		for i := len(settings) - 1; i >= 0; i-- {
			if settings[i].Key == key {
				return &settings[i]
			}
		}
		return nil
	}
	cluster := last(keyClusterAddress)
	if cluster == nil {
		return nil, errors.New("wsrep_cluster_address staat in geen enkel configbestand")
	}
	type edit struct {
		line  int // 0: invoegen na after
		after int
		text  string
	}
	edits := map[string][]edit{}
	if req.ClusterAddress != "" {
		edits[cluster.File] = append(edits[cluster.File], edit{line: cluster.Line, text: keyClusterAddress + " = " + req.ClusterAddress})
	}
	if req.NodeAddress != "" {
		if node := last(keyNodeAddress); node != nil {
			edits[node.File] = append(edits[node.File], edit{line: node.Line, text: keyNodeAddress + " = " + req.NodeAddress})
		} else {
			edits[cluster.File] = append(edits[cluster.File], edit{after: cluster.Line, text: keyNodeAddress + " = " + req.NodeAddress})
		}
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	stamp := now().UTC().Format("20060102T150405Z")
	var changed []protocol.HealChange
	restore := func() {
		for _, c := range changed {
			if p, err := a.path(c.Backup); err == nil {
				if b, err := os.ReadFile(p); err == nil {
					if q, err := a.path(c.File); err == nil {
						_ = os.WriteFile(q, b, 0o644) //nolint:gosec // een configbestand van MariaDB is leesbaar
					}
				}
			}
		}
	}
	files := make([]string, 0, len(edits))
	for f := range edits {
		files = append(files, f)
	}
	slices.Sort(files)
	for _, file := range files {
		p, err := a.path(file)
		if err != nil {
			restore()
			return nil, err
		}
		info, err := os.Stat(p)
		if err != nil {
			restore()
			return nil, err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			restore()
			return nil, err
		}
		backup := file + protocol.HealBackupSuffix + stamp
		bp, err := a.path(backup)
		if err != nil {
			restore()
			return nil, err
		}
		if err := os.WriteFile(bp, b, info.Mode().Perm()); err != nil {
			restore()
			return nil, err
		}
		lines := strings.Split(string(b), "\n")
		var news []string
		var out []string
		for i, l := range lines {
			n := i + 1
			replaced := false
			for _, e := range edits[file] {
				if e.line == n {
					out = append(out, e.text)
					news = append(news, e.text)
					replaced = true
				}
			}
			if !replaced {
				out = append(out, l)
			}
			for _, e := range edits[file] {
				if e.line == 0 && e.after == n {
					out = append(out, e.text)
					news = append(news, e.text)
				}
			}
		}
		if err := writeFileAtomic(p, []byte(strings.Join(out, "\n")), info.Mode().Perm()); err != nil {
			restore()
			return nil, err
		}
		changed = append(changed, protocol.HealChange{File: file, Backup: backup, Lines: news})
	}
	defaults, err := a.printDefaults(ctx)
	if err != nil {
		restore()
		return nil, err
	}
	switch {
	case req.ClusterAddress != "" && defaults[keyClusterAddress] != req.ClusterAddress:
		restore()
		return nil, fmt.Errorf("na het wijzigen geldt nog wsrep_cluster_address = %s; een ander bestand zet hem; de kopie is teruggezet", defaults[keyClusterAddress])
	case req.NodeAddress != "" && defaults[keyNodeAddress] != req.NodeAddress:
		restore()
		return nil, fmt.Errorf("na het wijzigen geldt nog wsrep_node_address = %s; een ander bestand zet hem; de kopie is teruggezet", defaults[keyNodeAddress])
	}
	return changed, nil
}
