// Package agenttest speelt een Debian-machine na voor tests en lokale
// ontwikkeling: apt, systemd, gebruikers en keepalived, zonder iets op de
// echte machine te veranderen.
package agenttest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Unit is de toestand van een systemd-unit.
type Unit struct {
	Enabled, Active bool
}

// Provides zegt welke units een pakket meebrengt en hoe ze na het
// installeren staan, zoals op Debian: nginx start meteen, keepalived pas
// als er een configuratie is.
var Provides = map[string]map[string]Unit{
	"nginx":      {"nginx": {Enabled: true, Active: true}},
	"keepalived": {"keepalived": {Enabled: true}},
	"docker.io":  {"docker": {Enabled: true, Active: true}},
	"cron":       {"cron": {Enabled: true, Active: true}},
}

// Host is een nagespeelde machine. Root is de map die de agent als / ziet;
// keepalived leest daar zijn configuratie.
type Host struct {
	Root string
	// Address is het eigen IP-adres van de machine.
	Address string
	// Interface is de naam van de netwerkkaart.
	Interface string

	mu       sync.Mutex
	group    *Group
	packages map[string]bool
	versions map[string]string
	units    map[string]*Unit
	users    map[string]bool
	calls    []string
	fail     map[string]string
}

func NewHost(root, address string) *Host {
	return &Host{
		Root: root, Address: address, Interface: "eth0",
		packages: map[string]bool{}, versions: map[string]string{}, units: map[string]*Unit{}, users: map[string]bool{"root": true},
		fail: map[string]string{},
	}
}

// Fail laat het eerstvolgende commando dat met prefix begint mislukken met
// output als uitvoer.
func (h *Host) Fail(prefix, output string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fail[prefix] = output
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
	out := []string{h.Address}
	if g != nil {
		out = append(out, g.held(h)...)
	}
	return out
}

// queries zijn commando's die niets veranderen; ze komen niet in Calls.
var queries = []string{"systemctl show", "dpkg-query", "id ", "journalctl", "apt-get -s", "systemd-detect-virt", "docker "}

func (h *Host) record(call string) {
	if !slices.ContainsFunc(queries, func(q string) bool { return strings.HasPrefix(call, q) }) {
		h.calls = append(h.calls, call)
	}
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
	for prefix, out := range h.fail {
		if strings.HasPrefix(call, prefix) {
			delete(h.fail, prefix)
			h.mu.Unlock()
			return []byte(out), errors.New("exit status 1")
		}
	}
	out, err := h.exec(name, args)
	g := h.group
	h.mu.Unlock()
	if g != nil && name == "systemctl" {
		g.update()
	}
	return out, err
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
		return nil, nil
	}
	return nil, fmt.Errorf("onbekend commando %s", name)
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
	if len(args) < 2 {
		return nil, errors.New("systemctl zonder unit")
	}
	if args[0] == "show" {
		var b strings.Builder
		units := slices.DeleteFunc(slices.Clone(args[1:]), func(a string) bool { return strings.HasPrefix(a, "--") })
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
			fmt.Fprintf(&b, "LoadState=loaded\nUnitFileState=%s\nActiveState=%s\n\n",
				map[bool]string{true: "enabled", false: "disabled"}[u.Enabled],
				map[bool]string{true: "active", false: "inactive"}[u.Active])
		}
		return []byte(b.String()), nil
	}
	verb, name := args[0], strings.TrimSuffix(args[len(args)-1], ".service")
	u, ok := h.units[name]
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
		u.Active = true
	case "stop":
		u.Active = false
	default:
		return nil, fmt.Errorf("systemctl %s", verb)
	}
	return nil, nil
}

func (h *Host) keepalivedConf() string {
	b, err := os.ReadFile(filepath.Join(h.Root, "etc/keepalived/keepalived.conf"))
	if err != nil {
		return ""
	}
	return string(b)
}

var (
	priorityRe = regexp.MustCompile(`(?m)^\s*priority\s+(\d+)`)
	vipBlockRe = regexp.MustCompile(`(?s)virtual_ipaddress\s*\{([^}]*)\}`)
)

// vrrp leest prioriteit en VIP's uit de keepalived-configuratie.
func (h *Host) vrrp() (priority int, vips []string) {
	conf := h.keepalivedConf()
	if m := priorityRe.FindStringSubmatch(conf); m != nil {
		priority, _ = strconv.Atoi(m[1])
	}
	if m := vipBlockRe.FindStringSubmatch(conf); m != nil {
		for _, f := range strings.Fields(m[1]) {
			if ip, _, _ := strings.Cut(f, "/"); strings.Count(ip, ".") == 3 {
				vips = append(vips, ip)
			}
		}
	}
	return priority, vips
}

// Group is een L2-netwerk met keepalived: een VIP staat op de machine met de
// hoogste prioriteit waarop keepalived draait.
type Group struct {
	mu    sync.Mutex
	hosts []*Host
	owner map[string]*Host
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
	owner := map[string]*Host{}
	for _, h := range g.hosts {
		h.mu.Lock()
		u := h.units["keepalived"]
		running := u != nil && u.Active
		prio, vips := h.vrrp()
		h.mu.Unlock()
		if !running {
			continue
		}
		for _, v := range vips {
			if _, ok := owner[v]; !ok || prio > best[v] {
				owner[v], best[v] = h, prio
			}
		}
	}
	g.owner = owner
}

func (g *Group) held(h *Host) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for v, o := range g.owner {
		if o == h {
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
