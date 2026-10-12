// Package agenttest speelt een Debian-machine na voor tests en lokale
// ontwikkeling: apt, systemd, gebruikers en keepalived, zonder iets op de
// echte machine te veranderen.
package agenttest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Unit is de toestand van een systemd-unit. Failed: de unit startte niet.
// Activating: hij start nog, zoals MariaDB die op de andere Galera-nodes
// wacht.
type Unit struct {
	Enabled, Active, Failed, Activating bool
}

// Provides zegt welke units een pakket meebrengt en hoe ze na het
// installeren staan, zoals op Debian: nginx start meteen, keepalived pas
// als er een configuratie is.
var Provides = map[string]map[string]Unit{
	"nginx":      {"nginx": {Enabled: true, Active: true}},
	"keepalived": {"keepalived": {Enabled: true}},
	"docker.io":  {"docker": {Enabled: true, Active: true}},
	"cron":       {"cron": {Enabled: true, Active: true}},
	// MariaDB start meteen na het installeren; garbd pas met een
	// configuratie die zegt waar het cluster is.
	"mariadb-server":      {"mariadb": {Enabled: true, Active: true}},
	"galera-arbitrator-4": {"garb": {Enabled: true}},
}

// Host is een nagespeelde machine. Root is de map die de agent als / ziet;
// keepalived leest daar zijn configuratie.
type Host struct {
	Root string
	// Address is het eigen IP-adres van de machine.
	Address string
	// Interface is de naam van de netwerkkaart.
	Interface string
	// K3s speelt het commando k3s na, met de argumenten; nil kent het
	// niet. Het draait onder het slot van de host.
	K3s func(args []string) ([]byte, error)
	// MariaDB en Postgres zijn de databases op de machine; nil heeft er
	// geen. Wijzig ze met Locked terwijl de agent draait.
	MariaDB  *MariaDB
	Postgres *Postgres
	// Patroni is wat Patroni over de node zegt, voor ServePatroni; nil:
	// Patroni draait niet.
	Patroni *Patroni
	// PatroniAPI speelt de acties met wachtwoord van Patroni na; nil geeft
	// 503. Zet het met Locked terwijl de agent draait.
	PatroniAPI PatroniAPI
	// Network geeft de machine op een adres (ip:poort), voor een
	// verhuizing van haar MariaDB; nil kent geen andere machines. Zet het
	// met Locked terwijl de agent draait.
	Network func(addr string) *Host

	mu       sync.Mutex
	after    func(call string, err error)
	group    *Group
	packages map[string]bool
	versions map[string]string
	units    map[string]*Unit
	users    map[string]bool
	calls    []string
	stdins   []string
	fail     map[string]*failure
	journal  map[string]string

	// De storingen van de agent: de nft-tabel cf_fault, interfaces die down
	// staan en de root-qdisc per interface.
	nft      string
	linkDown map[string]bool
	qdisc    map[string]string
}

type failure struct {
	output string
	times  int
}

func NewHost(root, address string) *Host {
	return &Host{
		Root: root, Address: address, Interface: "eth0",
		packages: map[string]bool{}, versions: map[string]string{}, units: map[string]*Unit{}, users: map[string]bool{"root": true},
		fail: map[string]*failure{}, linkDown: map[string]bool{}, qdisc: map[string]string{},
	}
}

// Interfaces is wat de agent in zijn facts zet: de netwerkkaart met het
// eigen adres.
func (h *Host) Interfaces() []protocol.Interface {
	h.mu.Lock()
	defer h.mu.Unlock()
	return []protocol.Interface{{Name: h.Interface, Up: !h.linkDown[h.Interface], Addresses: []string{h.Address + "/24"}, Kind: "physical"}}
}

// VRRPBlocked is true als de tabel cf_fault VRRP laat vallen.
func (h *Host) VRRPBlocked() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.vrrpBlocked()
}

func (h *Host) vrrpBlocked() bool { return strings.Contains(h.nft, "meta l4proto 112 drop") }

var dportRe = regexp.MustCompile(`tcp dport (\d+) drop`)

// Blocked is true als de tabel cf_fault inkomend verkeer op de poort laat
// vallen.
func (h *Host) Blocked(port int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range dportRe.FindAllStringSubmatch(h.nft, -1) {
		if m[1] == strconv.Itoa(port) {
			return true
		}
	}
	return false
}

// LinkDown is true als de interface down staat.
func (h *Host) LinkDown(iface string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.linkDown[iface]
}

// Qdisc is de root-qdisc van een interface.
func (h *Host) Qdisc(iface string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return cmpOr(h.qdisc[iface], "noqueue")
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Fail laat het eerstvolgende commando dat met prefix begint mislukken met
// output als uitvoer.
func (h *Host) Fail(prefix, output string) { h.FailTimes(prefix, output, 1) }

// FailTimes laat de volgende n commando's die met prefix beginnen mislukken.
func (h *Host) FailTimes(prefix, output string, n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fail[prefix] = &failure{output: output, times: n}
}

// Unfail haalt een storing van Fail of FailTimes weg.
func (h *Host) Unfail(prefix string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.fail, prefix)
}

// Calls geeft de commando's die iets veranderen, zonder de vragen.
func (h *Host) Calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

// Installed is true als het pakket geïnstalleerd is.
func (h *Host) Installed(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.packages[name]
}

// Unit geeft de toestand van een unit.
func (h *Host) Unit(name string) (Unit, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	u, ok := h.units[name]
	if !ok {
		return Unit{}, false
	}
	return *u, true
}

// Addresses is wat de agent in zijn heartbeat zet: het eigen adres en de
// VIP's die deze machine nu heeft.
func (h *Host) Addresses() []string {
	h.mu.Lock()
	g := h.group
	h.mu.Unlock()
	h.mu.Lock()
	down := h.linkDown[h.Interface]
	h.mu.Unlock()
	if down {
		// Zonder netwerk is er niets te bereiken.
		return nil
	}
	out := []string{h.Address}
	if g != nil {
		out = append(out, g.held(h)...)
	}
	return out
}

// queries zijn commando's die niets veranderen; ze komen niet in Calls.
var queries = []string{
	"systemctl show", "systemctl --failed", "systemctl is-system-running", "systemctl is-active", "dpkg-query", "id ", "journalctl", "apt-get -s",
	"systemd-detect-virt", "docker ", "tc qdisc show",
}

func (h *Host) record(call string) {
	if !slices.ContainsFunc(queries, func(q string) bool { return strings.HasPrefix(call, q) }) {
		h.calls = append(h.calls, call)
	}
}

// SetAfter laat fn na elk commando weten welk commando het was, zoals
// "systemctl restart k3s", en zijn fout. fn draait buiten het slot, dus hij
// mag de host veranderen, zoals een unit laten falen.
func (h *Host) SetAfter(fn func(call string, err error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.after = fn
}

// Exec speelt de systeemcommando's na die de agent gebruikt.
func (h *Host) Exec(_ context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	if name == "env" && len(args) > 1 {
		// env DEBIAN_FRONTEND=noninteractive apt-get ...
		name, args = args[1], args[2:]
		call = strings.TrimSpace(name + " " + strings.Join(args, " "))
	}
	h.record(call)
	for prefix, f := range h.fail {
		if strings.HasPrefix(call, prefix) {
			if f.times--; f.times <= 0 {
				delete(h.fail, prefix)
			}
			h.mu.Unlock()
			return []byte(f.output), errors.New("exit status 1")
		}
	}
	out, err := h.exec(name, args)
	g, after := h.group, h.after
	h.mu.Unlock()
	if after != nil {
		after(call, err)
	}
	if g != nil && (name == "systemctl" || name == "nft" || name == "ip") {
		g.update()
	}
	return out, err
}

// ExecStdin speelt een commando met invoer op stdin na: de opdrachten van
// de agent aan MariaDB. In Calls staat het commando zonder de invoer, die
// een wachtwoord kan bevatten; Stdins geeft de invoer.
func (h *Host) ExecStdin(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	h.record(call)
	h.stdins = append(h.stdins, string(stdin))
	for prefix, f := range h.fail {
		if strings.HasPrefix(call, prefix) {
			if f.times--; f.times <= 0 {
				delete(h.fail, prefix)
			}
			h.mu.Unlock()
			return []byte(f.output), errors.New("exit status 1")
		}
	}
	var out []byte
	var err error
	switch name {
	case "mariadb", "mysql":
		out, err = h.mariaWrite(string(stdin))
	case "runuser":
		out, err = h.pgLocal(args, string(stdin))
	case "psql":
		out, err = h.pgRemoteWrite(args, string(stdin))
	default:
		out, err = []byte("agenttest: "+name+" leest geen stdin\n"), errors.New("exit status 127")
	}
	g, after := h.group, h.after
	h.mu.Unlock()
	if after != nil {
		after(call, err)
	}
	if g != nil {
		g.update()
	}
	return out, err
}

// Stream speelt mariadb-dump en het terugzetten van een dump na, met
// stdin en stdout als stroom. In Calls staat het commando zonder de
// invoer; Fail geldt ook hier.
func (h *Host) Stream(_ context.Context, in io.Reader, out io.Writer, name string, args ...string) error {
	var data []byte
	if in != nil {
		var err error
		if data, err = io.ReadAll(in); err != nil {
			return err
		}
	}
	h.mu.Lock()
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	h.record(call)
	for prefix, f := range h.fail {
		if strings.HasPrefix(call, prefix) {
			if f.times--; f.times <= 0 {
				delete(h.fail, prefix)
			}
			h.mu.Unlock()
			return errors.New(name + ": " + strings.TrimSpace(f.output))
		}
	}
	var dump []byte
	var err error
	switch name {
	case "mariadb-dump", "mysqldump":
		if cfg, rest, ok := clientConfig(args); ok {
			dump, err = h.remoteDump(cfg, rest)
		} else {
			dump, err = h.mariaDump(args)
		}
	case "mariadb", "mysql":
		err = h.mariaRestore(args, data)
	case "runuser":
		if len(args) > 3 && args[3] == "psql" {
			err = h.pgImport(args, data)
		} else {
			dump, err = h.pgDump(args)
		}
	case "pg_dump":
		dump, err = h.pgRemoteDump(args)
	case "psql":
		err = h.pgRestore(args, data)
	default:
		err = errors.New("agenttest: " + name + " kent geen stroom")
	}
	g, after := h.group, h.after
	h.mu.Unlock()
	if dump != nil {
		if _, werr := out.Write(dump); werr != nil {
			return werr
		}
	}
	if after != nil {
		after(call, err)
	}
	if g != nil {
		g.update()
	}
	return err
}

// Stdins geeft de invoer van elke ExecStdin, in volgorde.
func (h *Host) Stdins() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.stdins)
}

// exec doet het werk; de aanroeper heeft het slot.
func (h *Host) exec(name string, args []string) ([]byte, error) {
	switch name {
	case "dpkg-query":
		return h.dpkgQuery(args)
	case "apt-get":
		return h.apt(args)
	case "systemctl":
		return h.systemctl(args)
	case "journalctl":
		// journalctl -u <unit> ...
		if len(args) > 1 && args[0] == "-u" && h.journal[args[1]] != "" {
			return []byte(h.journal[args[1]]), nil
		}
		return []byte("-- No entries --\n"), nil
	case "id":
		if h.users[args[len(args)-1]] {
			return []byte("999\n"), nil
		}
		return []byte("id: no such user\n"), errors.New("exit status 1")
	case "useradd":
		h.users[args[len(args)-1]] = true
		return nil, nil
	case "sh":
		// cf-agent check db, als unless van een commando: lukt alleen als
		// MariaDB hier mag schrijven.
		if len(args) == 2 && args[1] == protocol.DBCheckCommand && !h.mariaWritable() {
			return []byte("deze Galera-node is niet Synced\n"), errExit1
		}
		if len(args) == 2 && args[1] == protocol.DBCheckPGCommand && !h.patroniPrimary() {
			return []byte("deze node is niet de primary in Patroni\n"), errExit1
		}
		return nil, nil
	case "nft":
		return h.nftExec(args)
	case "ip":
		// ip link set dev <x> up|down
		if len(args) != 5 || args[0] != "link" || args[1] != "set" || args[2] != "dev" || (args[4] != "up" && args[4] != "down") {
			return nil, fmt.Errorf("ip %s", strings.Join(args, " "))
		}
		if args[3] != h.Interface {
			return []byte("Cannot find device \"" + args[3] + "\"\n"), errors.New("exit status 1")
		}
		h.linkDown[args[3]] = args[4] == "down"
		return nil, nil
	case "tc":
		return h.tc(args)
	case "k3s":
		if h.K3s != nil {
			return h.K3s(args)
		}
	case "mariadb", "mysql":
		return h.mariaExec(args)
	case "keepalived":
		if len(args) == 3 && args[0] == "-t" && args[1] == "-f" {
			return h.keepalivedTest(args[2])
		}
	case "runuser":
		return h.runuser(args)
	case "psql":
		return h.pgRemoteQuery(args)
	case "galera_recovery":
		return h.galeraRecovery()
	}
	return nil, fmt.Errorf("onbekend commando %s", name)
}

// nftExec speelt nft -f <script> en nft delete table inet cf_fault na.
func (h *Host) nftExec(args []string) ([]byte, error) {
	switch {
	case len(args) == 2 && args[0] == "-f":
		b, err := os.ReadFile(args[1])
		if err != nil {
			return []byte(err.Error() + "\n"), errors.New("exit status 1")
		}
		h.nft = string(b)
		return nil, nil
	case strings.Join(args, " ") == "delete table inet cf_fault":
		if h.nft == "" {
			return []byte("Error: Could not process rule: No such file or directory\ndelete table inet cf_fault\n"), errors.New("exit status 1")
		}
		h.nft = ""
		return nil, nil
	}
	return nil, fmt.Errorf("nft %s", strings.Join(args, " "))
}

// tc speelt tc qdisc show, replace en del op de root na.
func (h *Host) tc(args []string) ([]byte, error) {
	if len(args) < 5 || args[0] != "qdisc" || args[2] != "dev" || args[4] != "root" {
		return nil, fmt.Errorf("tc %s", strings.Join(args, " "))
	}
	dev := args[3]
	if dev != h.Interface {
		return []byte("Cannot find device \"" + dev + "\"\n"), errors.New("exit status 1")
	}
	switch args[1] {
	case "show":
		return []byte(fmt.Sprintf("qdisc %s 0: root refcnt 2\n", cmpOr(h.qdisc[dev], "noqueue"))), nil
	case "replace":
		if len(args) < 6 {
			return nil, errors.New("tc qdisc replace zonder qdisc")
		}
		h.qdisc[dev] = args[5]
		return nil, nil
	case "del":
		delete(h.qdisc, dev)
		return nil, nil
	}
	return nil, fmt.Errorf("tc %s", strings.Join(args, " "))
}

// dpkgQuery speelt dpkg-query -W -f=... namen... na, met de escapes \t en
// \n in het formaat. Een onbekend pakket geeft een melding en exit 1, de
// bekende staan dan toch in de uitvoer.
func (h *Host) dpkgQuery(args []string) ([]byte, error) {
	format := "${Package}\t${Version}\n"
	var names []string
	for _, a := range args {
		switch {
		case a == "-W":
		case strings.HasPrefix(a, "-f="):
			format = strings.TrimPrefix(a, "-f=")
		default:
			names = append(names, a)
		}
	}
	format = strings.NewReplacer(`\t`, "\t", `\n`, "\n").Replace(format)
	var b strings.Builder
	missing := false
	for _, n := range names {
		if !h.packages[n] {
			fmt.Fprintf(&b, "dpkg-query: no packages found matching %s\n", n)
			missing = true
			continue
		}
		b.WriteString(strings.NewReplacer("${Package}", n, "${Status}", "install ok installed", "${Version}", h.version(n)).Replace(format))
	}
	if missing {
		return []byte(b.String()), errors.New("exit status 1")
	}
	return []byte(b.String()), nil
}

// SetVersion zet de versie van een pakket, zoals dpkg-query hem meldt.
func (h *Host) SetVersion(name, version string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.versions[name] = version
}

func (h *Host) version(name string) string {
	if v, ok := h.versions[name]; ok {
		return v
	}
	return "1.0-1"
}

// SetUnit zet een unit zoals iemand die met de hand zou veranderen, zonder
// dat het in Calls komt.
func (h *Host) SetUnit(name string, u Unit) {
	h.mu.Lock()
	h.units[name] = &u
	g := h.group
	h.mu.Unlock()
	if g != nil {
		g.update()
	}
}

// SetJournal zet wat journalctl -u unit toont, zoals een foutmelding.
func (h *Host) SetJournal(unit, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.journal == nil {
		h.journal = map[string]string{}
	}
	h.journal[unit] = text
}

// Power zet de machine uit of aan, zoals een harde stop of een start van
// de VM: uit stopt elke unit, aan start de units die enabled zijn.
func (h *Host) Power(on bool) {
	h.mu.Lock()
	for _, u := range h.units {
		u.Active, u.Activating = on && u.Enabled, false
	}
	g := h.group
	h.mu.Unlock()
	if g != nil {
		g.update()
	}
}

// Remove haalt een pakket weg zoals iemand het met de hand zou doen.
func (h *Host) Remove(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.packages, name)
}

func (h *Host) apt(args []string) ([]byte, error) {
	if slices.Contains(args, "-s") {
		return []byte("0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"), nil
	}
	var verb string
	var pkgs []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-o":
			i++
		case strings.HasPrefix(a, "-"):
		case verb == "":
			verb = a
		default:
			pkgs = append(pkgs, a)
		}
	}
	switch verb {
	case "update":
		return []byte("Reading package lists...\n"), nil
	case "install":
		for _, p := range pkgs {
			h.packages[p] = true
			for unit, st := range Provides[p] {
				if _, ok := h.units[unit]; !ok {
					u := st
					// keepalived start alleen met een configuratie.
					if unit == "keepalived" && h.keepalivedConf() != "" {
						u.Active = true
					}
					h.units[unit] = &u
				}
			}
		}
		return []byte("Setting up " + strings.Join(pkgs, ", ") + " ...\n"), nil
	case "remove":
		for _, p := range pkgs {
			delete(h.packages, p)
			for unit := range Provides[p] {
				delete(h.units, unit)
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("apt-get %s", verb)
}

func (h *Host) systemctl(args []string) ([]byte, error) {
	if len(args) == 1 && args[0] == "daemon-reload" {
		h.daemonReload()
		return nil, nil
	}
	if args[0] == "is-system-running" {
		for _, u := range h.units {
			if u.Failed {
				return []byte("degraded\n"), errors.New("exit status 1")
			}
		}
		return []byte("running\n"), nil
	}
	if args[0] == "--failed" {
		var b strings.Builder
		for _, name := range slices.Sorted(maps.Keys(h.units)) {
			if h.units[name].Failed {
				fmt.Fprintf(&b, "%s.service loaded failed failed %s\n", name, name)
			}
		}
		return []byte(b.String()), nil
	}
	if len(args) < 2 {
		return nil, errors.New("systemctl zonder unit")
	}
	if args[0] == "reboot" || args[0] == "poweroff" {
		// Wat er bij een herstart gebeurt, speelt de test na met SetAfter.
		return nil, nil
	}
	if args[0] == "show" {
		var b strings.Builder
		var units []string
		for _, a := range args[1:] {
			switch {
			case strings.HasPrefix(a, "--"):
			case strings.Contains(a, "*"):
				// Een patroon vindt alleen geladen units, zoals bij systemd.
				for _, name := range slices.Sorted(maps.Keys(h.units)) {
					if ok, _ := path.Match(a, name+".service"); ok {
						units = append(units, name)
					}
				}
			default:
				units = append(units, a)
			}
		}
		for _, name := range units {
			name = strings.TrimSuffix(name, ".service")
			if len(units) > 1 {
				fmt.Fprintf(&b, "Id=%s.service\n", name)
			}
			u, ok := h.units[name]
			if !ok {
				b.WriteString("LoadState=not-found\nUnitFileState=\nActiveState=inactive\n\n")
				continue
			}
			active := map[bool]string{true: "active", false: "inactive"}[u.Active]
			switch {
			case u.Failed:
				active = "failed"
			case u.Activating:
				active = "activating"
			}
			fmt.Fprintf(&b, "LoadState=loaded\nUnitFileState=%s\nActiveState=%s\n\n",
				map[bool]string{true: "enabled", false: "disabled"}[u.Enabled], active)
		}
		return []byte(b.String()), nil
	}
	verb, name := args[0], strings.TrimSuffix(args[len(args)-1], ".service")
	u, ok := h.units[name]
	if verb == "is-active" {
		switch {
		case ok && u.Failed:
			return []byte("failed\n"), errors.New("exit status 3")
		case ok && u.Activating:
			return []byte("activating\n"), errors.New("exit status 3")
		case ok && u.Active:
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit status 3")
	}
	if !ok {
		return []byte("Failed to " + verb + " " + name + ".service: Unit " + name + ".service not found.\n"), errors.New("exit status 5")
	}
	switch verb {
	case "enable":
		u.Enabled = true
	case "disable":
		u.Enabled = false
		if slices.Contains(args, "--now") {
			u.Active = false
		}
	case "start", "restart", "reload-or-restart":
		u.Active, u.Failed, u.Activating = true, false, false
	case "stop":
		u.Active, u.Activating = false, false
	default:
		return nil, fmt.Errorf("systemctl %s", verb)
	}
	return nil, nil
}

// daemonReload laadt de units uit /etc/systemd/system die systemd nog niet
// kende, zoals systemctl daemon-reload: disabled en gestopt. Zonder reload
// bestaat een nieuwe unit voor systemctl niet.
func (h *Host) daemonReload() {
	files, _ := filepath.Glob(filepath.Join(h.Root, "etc/systemd/system/*.service"))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".service")
		if _, ok := h.units[name]; !ok {
			h.units[name] = &Unit{}
		}
	}
}

// keepalivedConf is keepalived.conf met elk bestand van een include op
// zijn plaats, zoals keepalived ze leest.
func (h *Host) keepalivedConf() string {
	return h.readKeepalived(filepath.Join(h.Root, "etc/keepalived/keepalived.conf"), 0)
}

func (h *Host) readKeepalived(p string, depth int) string {
	b, err := os.ReadFile(p)
	if err != nil || depth > 4 {
		return ""
	}
	var out strings.Builder
	for line := range strings.SplitSeq(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "include" && filepath.IsAbs(f[1]) {
			files, _ := filepath.Glob(filepath.Join(h.Root, f[1]))
			for _, inc := range files {
				out.WriteString(h.readKeepalived(inc, depth+1))
			}
			continue
		}
		out.WriteString(line + "\n")
	}
	return out.String()
}

// keepalivedTest speelt keepalived -t -f <bestand> na: elke track_script
// moet een vrrp_script zijn die eerder in de configuratie staat, want
// keepalived zoekt hem op terwijl hij leest.
func (h *Host) keepalivedTest(p string) ([]byte, error) {
	if _, err := os.Stat(p); err != nil {
		return []byte("Configuration file '" + p + "' does not exist\n"), errExit1
	}
	conf := h.readKeepalived(p, 0)
	var words []string
	for line := range strings.SplitSeq(conf, "\n") {
		if i := strings.IndexAny(line, "#!"); i >= 0 {
			line = line[:i]
		}
		line = strings.NewReplacer("{", " { ", "}", " } ").Replace(line)
		words = append(words, strings.Fields(line)...)
	}
	defined := map[string]bool{}
	depth, track := 0, -1
	for i, w := range words {
		switch {
		case w == "{":
			depth++
			if i > 0 && words[i-1] == "track_script" {
				track = depth
			}
		case w == "}":
			if depth == track {
				track = -1
			}
			depth--
			if depth < 0 {
				return []byte("Configuration error: unexpected '}'\n"), errExit1
			}
		case w == "vrrp_script" && i+1 < len(words):
			defined[words[i+1]] = true
		case track == depth && w != "weight" && strings.Trim(w, "-0123456789") != "":
			if !defined[w] {
				return []byte("track script " + w + " not found, ignoring...\n"), errExit1
			}
		}
	}
	if depth != 0 {
		return []byte("Configuration error: missing '}'\n"), errExit1
	}
	return nil, nil
}

// mariaWritable is cf-agent check db: MariaDB antwoordt, is Synced in een
// Primary component en wsrep_ready staat aan. De aanroeper heeft het slot.
func (h *Host) mariaWritable() bool {
	m := h.MariaDB
	if u := h.units["mariadb"]; u != nil && !u.Active {
		return false
	}
	if m == nil || m.Down || m.Galera == nil {
		return false
	}
	return m.Galera.State == 4 && m.Galera.Status == "Primary" && m.Galera.Ready
}

var isActiveRe = regexp.MustCompile(`is-active\s+--quiet\s+([\w@.-]+)`)

// kaNode is een regel uit keepalived.conf: woorden, en eventueel een blok.
type kaNode struct {
	words []string
	kids  []*kaNode
}

// parseKA leest keepalived.conf als boom; commentaar (# en !) valt weg.
func parseKA(conf string) []*kaNode {
	root := &kaNode{}
	stack := []*kaNode{root}
	var last *kaNode
	for line := range strings.SplitSeq(conf, "\n") {
		if i := strings.IndexAny(line, "#!"); i >= 0 {
			line = line[:i]
		}
		line = strings.NewReplacer("{", " { ", "}", " } ").Replace(line)
		var cur *kaNode
		for _, w := range strings.Fields(line) {
			top := stack[len(stack)-1]
			switch w {
			case "{":
				owner := cur
				if owner == nil {
					owner = last
				}
				if owner == nil {
					owner = &kaNode{}
					top.kids = append(top.kids, owner)
				}
				stack = append(stack, owner)
				cur, last = nil, nil
			case "}":
				if len(stack) > 1 {
					stack = stack[:len(stack)-1]
				}
				cur, last = nil, nil
			default:
				if cur == nil {
					cur = &kaNode{}
					top.kids = append(top.kids, cur)
				}
				cur.words = append(cur.words, w)
			}
		}
		if cur != nil {
			last = cur
		}
	}
	return root.kids
}

// vrrpInstance is een vrrp_instance uit de keepalived-configuratie, met de
// scripts van zijn track_script.
type vrrpInstance struct {
	priority int
	vips     []string
	scripts  []string
	unicast  bool
	src      string
	peers    []string
}

// vrrpConfig leest de instances en de vrrp_scripts (naam en commando) uit
// de keepalived-configuratie, met de includes.
func (h *Host) vrrpConfig() ([]vrrpInstance, map[string]string) {
	var insts []vrrpInstance
	scripts := map[string]string{}
	for _, n := range parseKA(h.keepalivedConf()) {
		if len(n.words) < 2 {
			continue
		}
		switch n.words[0] {
		case "vrrp_script":
			for _, k := range n.kids {
				if len(k.words) > 1 && k.words[0] == "script" {
					scripts[n.words[1]] = strings.Trim(strings.Join(k.words[1:], " "), `"'`)
				}
			}
		case "vrrp_instance":
			in := vrrpInstance{src: h.Address}
			for _, k := range n.kids {
				if len(k.words) == 0 {
					continue
				}
				switch k.words[0] {
				case "priority":
					if len(k.words) > 1 {
						in.priority, _ = strconv.Atoi(k.words[1])
					}
				case "virtual_ipaddress":
					for _, v := range k.kids {
						if ip, _, _ := strings.Cut(v.words[0], "/"); strings.Count(ip, ".") == 3 {
							in.vips = append(in.vips, ip)
						}
					}
				case "track_script":
					for _, t := range k.kids {
						for _, w := range t.words {
							if w != "weight" && strings.Trim(w, "-+0123456789") != "" {
								in.scripts = append(in.scripts, w)
							}
						}
					}
				case "unicast_src_ip":
					if len(k.words) > 1 {
						in.src = k.words[1]
					}
				case "unicast_peer":
					in.unicast = true
					for _, p := range k.kids {
						in.peers = append(in.peers, p.words...)
					}
				}
			}
			insts = append(insts, in)
		}
	}
	return insts, scripts
}

// scriptOK zegt of een vrrp_script slaagt: cf-agent check db als de
// database hier mag schrijven, systemctl is-active als de unit draait. Een
// track_script zonder vrrp_script negeert keepalived. De aanroeper heeft
// het slot.
func (h *Host) scriptOK(script string, defined bool) bool {
	switch {
	case !defined:
		return true
	case strings.Contains(script, protocol.DBCheckPGCommand):
		return h.patroniPrimary()
	case strings.Contains(script, protocol.DBCheckCommand):
		return h.mariaWritable()
	}
	if m := isActiveRe.FindStringSubmatch(script); m != nil {
		u := h.units[strings.TrimSuffix(m[1], ".service")]
		return u != nil && u.Active
	}
	return true
}

// vrrpPeer is wat een draaiende keepalived over unicast weet. blocked: de
// machine laat VRRP in en uit vallen.
type vrrpPeer struct {
	unicast bool
	src     string
	peers   []string
	blocked bool
}

// hears zegt of h de adverts van o ontvangt. Met unicast stuurt o alleen
// naar zijn peers, en neemt h alleen adverts aan van zijn peers. Kent een
// van beide de ander niet, of blokkeert een van beide VRRP, dan hoort h
// niets en wordt hij zelf MASTER.
func hears(h, o vrrpPeer) bool {
	if h.blocked || o.blocked {
		return false
	}
	if !h.unicast || !o.unicast {
		return true
	}
	return slices.Contains(o.peers, h.src) && slices.Contains(h.peers, o.src)
}

// Group is een L2-netwerk met keepalived: een VIP staat op de machine met de
// hoogste prioriteit waarop keepalived draait. Bewaakt een track_script een
// unit die niet draait, of cf_db een MariaDB die niet mag schrijven, dan
// staat de machine in FAULT en krijgt ze geen VIP.
// Met unicast_peer hoort een machine alleen de peers die elkaar kennen; een
// machine die de houder niet hoort, neemt het VIP er ook bij (split-brain).
type Group struct {
	// Takeover is hoe lang het duurt voor een andere machine een VIP
	// overneemt van een machine die wegvalt; zolang heeft niemand het. Een
	// machine met een hogere prioriteit die terugkomt, neemt het meteen.
	Takeover time.Duration

	mu    sync.Mutex
	hosts []*Host
	owner map[string]*Host
	// extra zijn per VIP de machines die de houder niet horen en het VIP
	// ook hebben.
	extra map[string][]*Host
	// splits onthoudt elk split-brain, zoals "10.0.20.100 op 10.0.20.11 en
	// 10.0.20.13".
	splits []string
	// lost is wanneer een VIP zijn houder verloor.
	lost  map[string]time.Time
	timer *time.Timer
}

// Join zet een machine in het netwerk.
func (g *Group) Join(h *Host) {
	g.mu.Lock()
	g.hosts = append(g.hosts, h)
	g.mu.Unlock()
	h.mu.Lock()
	h.group = g
	h.mu.Unlock()
	g.update()
}

func (g *Group) update() {
	g.mu.Lock()
	defer g.mu.Unlock()
	best := map[string]int{}
	want := map[string]*Host{}
	eligible := map[*Host][]string{}
	peer := map[*Host]map[string]vrrpPeer{}
	for _, h := range g.hosts {
		h.mu.Lock()
		u := h.units["keepalived"]
		// Zonder netwerk hoort niemand de machine.
		running := u != nil && u.Active && !h.linkDown[h.Interface]
		insts, scripts := h.vrrpConfig()
		blocked := h.vrrpBlocked()
		type ok struct {
			in vrrpInstance
			up bool
		}
		var states []ok
		for _, in := range insts {
			up := running
			for _, name := range in.scripts {
				script, defined := scripts[name]
				up = up && h.scriptOK(script, defined)
			}
			states = append(states, ok{in, up})
		}
		h.mu.Unlock()
		// Een instance in FAULT krijgt geen VIP.
		for _, st := range states {
			if !st.up {
				continue
			}
			if peer[h] == nil {
				peer[h] = map[string]vrrpPeer{}
			}
			for _, v := range st.in.vips {
				eligible[h] = append(eligible[h], v)
				peer[h][v] = vrrpPeer{unicast: st.in.unicast, src: st.in.src, peers: st.in.peers, blocked: blocked}
				if _, ok := want[v]; !ok || st.in.priority > best[v] {
					want[v], best[v] = h, st.in.priority
				}
			}
		}
	}
	if g.owner == nil {
		g.owner, g.lost = map[string]*Host{}, map[string]time.Time{}
	}
	now := time.Now()
	var wait time.Duration
	for v := range g.owner {
		if _, ok := want[v]; !ok {
			want[v] = nil
		}
	}
	for v, w := range want {
		cur := g.owner[v]
		switch {
		case cur != nil && slices.Contains(eligible[cur], v):
			// De houder draait nog: een hogere prioriteit neemt meteen over.
			g.owner[v] = w
			continue
		case cur != nil:
			delete(g.owner, v)
			g.lost[v] = now
		}
		if w == nil {
			continue
		}
		if since, ok := g.lost[v]; ok && now.Sub(since) < g.Takeover {
			if d := g.Takeover - now.Sub(since); wait == 0 || d < wait {
				wait = d
			}
			continue
		}
		g.owner[v] = w
		delete(g.lost, v)
	}
	if wait > 0 {
		if g.timer != nil {
			g.timer.Stop()
		}
		g.timer = time.AfterFunc(wait, g.update)
	}
	// Wie de houder niet hoort, is zelf MASTER.
	g.extra = map[string][]*Host{}
	for v, o := range g.owner {
		if o == nil {
			continue
		}
		addrs := []string{o.Address}
		for h, vips := range eligible {
			if h != o && slices.Contains(vips, v) && !hears(peer[h][v], peer[o][v]) {
				g.extra[v] = append(g.extra[v], h)
				addrs = append(addrs, h.Address)
			}
		}
		if len(addrs) > 1 {
			slices.Sort(addrs)
			split := v + " op " + strings.Join(addrs, " en ")
			if len(g.splits) == 0 || g.splits[len(g.splits)-1] != split {
				g.splits = append(g.splits, split)
			}
		}
	}
}

// Splits geeft elk split-brain dat de groep zag, in volgorde.
func (g *Group) Splits() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.splits)
}

func (g *Group) held(h *Host) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for v, o := range g.owner {
		if o == h || slices.Contains(g.extra[v], h) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

// Owner geeft de machine die de VIP nu heeft, of nil.
func (g *Group) Owner(vip string) *Host {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.owner[vip]
}
