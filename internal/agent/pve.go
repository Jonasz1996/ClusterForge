package agent

// Fase 17: cf-agent op een Proxmox-host. De agent leest de configuratie van
// de gasten rechtstreeks uit /etc/pve (snel, en daar staan ook de
// snapshots), de toestand en de storages met pvesh, en de
// containertemplates per storage. Alles wat iets verandert, gaat als run
// (pverun.go).

import (
	"bufio"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// pveHost leest een Proxmox-host. Root en Run zijn er voor tests.
type pveHost struct {
	root string
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (h pveHost) path(p string) string {
	if h.root == "" {
		return p
	}
	return filepath.Join(h.root, p)
}

func (h pveHost) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if h.run != nil {
		return h.run(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.Output()
}

// isHost zegt of dit een Proxmox-host is.
func (h pveHost) isHost() bool {
	if _, err := os.Stat(h.path("/usr/bin/pveversion")); err != nil {
		return false
	}
	fi, err := os.Stat(h.path("/etc/pve"))
	return err == nil && fi.IsDir()
}

type pveMembers struct {
	Nodename string `json:"nodename"`
	Cluster  struct {
		Name string `json:"name"`
	} `json:"cluster"`
}

func (h pveHost) members() pveMembers {
	var m pveMembers
	if data, err := os.ReadFile(h.path("/etc/pve/.members")); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	if m.Nodename == "" {
		name, _ := os.Hostname()
		m.Nodename, _, _ = strings.Cut(name, ".")
	}
	return m
}

// facts geeft de facts van een Proxmox-host, of nil.
func (h pveHost) facts(ctx context.Context) *protocol.PVEFacts {
	if !h.isHost() {
		return nil
	}
	m := h.members()
	f := &protocol.PVEFacts{Node: m.Nodename, Cluster: m.Cluster.Name}
	if out, err := h.exec(ctx, "pveversion"); err == nil {
		f.Version = parsePVEVersion(string(out))
	}
	return f
}

// parsePVEVersion haalt 9.0.10 uit "pve-manager/9.0.10/abcdef (running kernel: …)".
func parsePVEVersion(s string) string {
	_, rest, ok := strings.Cut(strings.TrimSpace(s), "pve-manager/")
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, "/")
	v, _, _ = strings.Cut(v, " ")
	return v
}

// proxmox geeft de facts van de Proxmox-host voor de Collector.
func (c *Collector) proxmox(ctx context.Context) *protocol.PVEFacts {
	return pveHost{root: c.Root, run: c.Run}.facts(ctx)
}

// pveConfig is één gastconfiguratie uit /etc/pve: de huidige stand en de
// snapshots.
type pveConfig struct {
	main      map[string]string
	desc      string
	snapshots []pveSnap
}

type pveSnap struct {
	name string
	kv   map[string]string
	desc string
}

// parsePVEConfig leest een .conf van pct of qm. Regels die met # beginnen,
// zijn de beschrijving (URL-gecodeerd); [naam] begint een snapshot.
func parsePVEConfig(data []byte) pveConfig {
	cfg := pveConfig{main: map[string]string{}}
	kv, desc := cfg.main, &cfg.desc
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			cfg.snapshots = append(cfg.snapshots, pveSnap{name: line[1 : len(line)-1], kv: map[string]string{}})
			s := &cfg.snapshots[len(cfg.snapshots)-1]
			kv, desc = s.kv, &s.desc
		case strings.HasPrefix(line, "#"):
			text, err := url.PathUnescape(line[1:])
			if err != nil {
				text = line[1:]
			}
			if *desc != "" {
				*desc += "\n"
			}
			*desc += text
		default:
			k, v, ok := strings.Cut(line, ":")
			if ok {
				kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	// Een snapshot "pending" is geen snapshot maar een wachtende wijziging.
	cfg.snapshots = slices.DeleteFunc(cfg.snapshots, func(s pveSnap) bool { return s.name == "PENDING" || s.name == "pending" })
	return cfg
}

var pveDiskKeys = []string{"rootfs", "scsi", "virtio", "sata", "ide", "mp"}

// guest maakt een PVEGuest uit een configuratie.
func (cfg pveConfig) guest(typ string, vmid int) protocol.PVEGuest {
	m := cfg.main
	g := protocol.PVEGuest{Type: typ, VMID: vmid, Description: cfg.desc, Lock: m["lock"]}
	g.Name = m["hostname"]
	if typ == "qemu" {
		g.Name = m["name"]
	}
	g.Template = m["template"] == "1"
	g.Cores, _ = strconv.Atoi(m["cores"])
	if g.Cores == 0 {
		if typ == "qemu" {
			g.Cores = 1
		} else if v, err := strconv.Atoi(m["cpulimit"]); err == nil {
			g.Cores = v
		}
	}
	g.MemoryMiB, _ = strconv.Atoi(m["memory"])
	g.SwapMiB, _ = strconv.Atoi(m["swap"])
	g.OnBoot = m["onboot"] == "1"
	if t := m["tags"]; t != "" {
		g.Tags = strings.FieldsFunc(t, func(r rune) bool { return r == ';' || r == ',' || r == ' ' })
	}
	if typ == "lxc" {
		g.Features = m["features"]
		g.Unprivileged = m["unprivileged"] == "1"
		g.Nameserver = m["nameserver"]
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		if strings.HasPrefix(k, "net") && isDigits(k[3:]) {
			g.Nets = append(g.Nets, k+": "+v)
			continue
		}
		isDisk := false
		for _, p := range pveDiskKeys {
			if k == p || (strings.HasPrefix(k, p) && isDigits(k[len(p):])) {
				isDisk = true
			}
		}
		if !isDisk || strings.Contains(v, "media=cdrom") || strings.Contains(v, "cloudinit") || k == "scsihw" {
			continue
		}
		vol, opts, _ := strings.Cut(v, ",")
		st, _, ok := strings.Cut(vol, ":")
		if !ok {
			continue
		}
		d := protocol.PVEDisk{Name: k, Storage: st}
		for _, o := range strings.Split(opts, ",") {
			if size, isSize := strings.CutPrefix(o, "size="); isSize {
				d.SizeGiB = pveSizeGiB(size)
			}
		}
		g.Disks = append(g.Disks, d)
	}
	for _, s := range cfg.snapshots {
		snap := protocol.PVESnapshotInfo{Name: s.name, Description: s.desc, Parent: s.kv["parent"], VMState: s.kv["vmstate"] != ""}
		if t, err := strconv.ParseInt(s.kv["snaptime"], 10, 64); err == nil {
			snap.Time = time.Unix(t, 0).UTC()
		}
		g.Snapshots = append(g.Snapshots, snap)
	}
	return g
}

// pveSizeGiB rekent een grootte van Proxmox (32G, 512M, 1T, 4096K) om naar
// hele GB, naar boven afgerond.
func pveSizeGiB(s string) int {
	if s == "" {
		return 0
	}
	unit := map[byte]float64{'K': 1.0 / (1 << 20), 'M': 1.0 / 1024, 'G': 1, 'T': 1024}
	f, ok := unit[s[len(s)-1]]
	n := s
	if ok {
		n = s[:len(s)-1]
	} else {
		f = 1.0 / (1 << 30)
	}
	v, err := strconv.ParseFloat(n, 64)
	if err != nil {
		return 0
	}
	g := v * f
	if g != float64(int(g)) {
		return int(g) + 1
	}
	return int(g)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// pveNum leest een getal uit JSON van pvesh: soms een getal, soms tekst.
func pveNum(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

func (h pveHost) pvesh(ctx context.Context, path string, args ...string) ([]map[string]any, error) {
	out, err := h.exec(ctx, "pvesh", append([]string{"get", path, "--output-format", "json"}, args...)...)
	if err != nil {
		return nil, err
	}
	var list []map[string]any
	err = json.Unmarshal(out, &list)
	return list, err
}

// guests leest de gasten van deze host uit /etc/pve; status zijn de
// standen uit pvesh (vmid → status, uptime).
func (h pveHost) guests(node string, status map[int]map[string]any) []protocol.PVEGuest {
	var out []protocol.PVEGuest
	for _, kind := range []struct{ typ, dir string }{{"lxc", "lxc"}, {"qemu", "qemu-server"}} {
		files, _ := filepath.Glob(h.path(filepath.Join("/etc/pve/nodes", node, kind.dir, "*.conf")))
		for _, f := range files {
			vmid, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".conf"))
			if err != nil {
				continue
			}
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			g := parsePVEConfig(data).guest(kind.typ, vmid)
			if s, ok := status[vmid]; ok {
				g.Status, _ = s["status"].(string)
				g.Uptime = pveNum(s["uptime"])
			}
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VMID < out[j].VMID })
	return out
}

// usedIDs zijn alle VMID's van het Proxmox-cluster.
func (h pveHost) usedIDs() []int {
	var vl struct {
		IDs map[string]json.RawMessage `json:"ids"`
	}
	data, err := os.ReadFile(h.path("/etc/pve/.vmlist"))
	if err != nil || json.Unmarshal(data, &vl) != nil {
		return nil
	}
	var out []int
	for k := range vl.IDs {
		if n, err := strconv.Atoi(k); err == nil {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// sshKeys zijn de publieke sleutels van root op de host.
func (h pveHost) sshKeys() []string {
	files, _ := filepath.Glob(h.path("/root/.ssh/id_*.pub"))
	var out []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for line := range strings.Lines(string(data)) {
			if l := strings.TrimSpace(line); protocol.SSHKeyRe.MatchString(l) {
				out = append(out, l)
			}
		}
	}
	return out
}

func (h pveHost) bridges() []string {
	entries, _ := os.ReadDir(h.path("/sys/class/net"))
	var out []string
	for _, e := range entries {
		if fi, err := os.Stat(h.path(filepath.Join("/sys/class/net", e.Name(), "bridge"))); err == nil && fi.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// inventory leest alles van de host.
func (h pveHost) inventory(ctx context.Context) (protocol.PVEInventory, error) {
	if !h.isHost() {
		return protocol.PVEInventory{}, errNotPVE
	}
	m := h.members()
	inv := protocol.PVEInventory{
		Node: m.Nodename, Cluster: m.Cluster.Name, Arch: runtime.GOARCH,
		Bridges: h.bridges(), SSHKeys: h.sshKeys(), UsedIDs: h.usedIDs(),
		Guests: []protocol.PVEGuest{}, Storages: []protocol.PVEStorage{}, Templates: []protocol.PVEOSTemplate{},
	}
	if f := h.facts(ctx); f != nil {
		inv.Version = f.Version
	}
	status := map[int]map[string]any{}
	for _, typ := range []string{"lxc", "qemu"} {
		list, err := h.pvesh(ctx, "/nodes/"+inv.Node+"/"+typ)
		if err != nil {
			continue
		}
		for _, s := range list {
			status[int(pveNum(s["vmid"]))] = s
		}
	}
	inv.Guests = append(inv.Guests, h.guests(inv.Node, status)...)
	storages, err := h.pvesh(ctx, "/nodes/"+inv.Node+"/storage")
	if err != nil {
		return inv, err
	}
	for _, s := range storages {
		name, _ := s["storage"].(string)
		typ, _ := s["type"].(string)
		content, _ := s["content"].(string)
		st := protocol.PVEStorage{
			Name: name, Type: typ, Content: content, Active: pveNum(s["active"]) == 1 && pveNum(s["enabled"]) != 0,
			Shared: pveNum(s["shared"]) == 1, Total: pveNum(s["total"]), Avail: pveNum(s["avail"]),
		}
		if _, hasEnabled := s["enabled"]; !hasEnabled {
			st.Active = pveNum(s["active"]) == 1
		}
		inv.Storages = append(inv.Storages, st)
		if !st.Active || !st.Holds("vztmpl") || !protocol.ValidPVEStorage(name) {
			continue
		}
		list, err := h.pvesh(ctx, "/nodes/"+inv.Node+"/storage/"+name+"/content", "--content", "vztmpl")
		if err != nil {
			continue
		}
		for _, t := range list {
			volid, _ := t["volid"].(string)
			inv.Templates = append(inv.Templates, protocol.PVEOSTemplate{Volid: volid, Size: pveNum(t["size"])})
		}
	}
	sort.Slice(inv.Storages, func(i, j int) bool { return inv.Storages[i].Name < inv.Storages[j].Name })
	sort.Slice(inv.Templates, func(i, j int) bool { return inv.Templates[i].Volid < inv.Templates[j].Volid })
	return inv, nil
}

// available zijn de containertemplates die pveam kan downloaden.
func (h pveHost) available(ctx context.Context) ([]protocol.PVEAppliance, error) {
	if !h.isHost() {
		return nil, errNotPVE
	}
	// Een oude lijst is beter dan geen; pveam update mag mislukken.
	_, _ = h.exec(ctx, "pveam", "update")
	out, err := h.exec(ctx, "pveam", "available")
	if err != nil {
		return nil, err
	}
	return parsePveamAvailable(out), nil
}

// parsePveamAvailable leest "system   debian-13-standard_13.1-2_amd64.tar.zst".
func parsePveamAvailable(out []byte) []protocol.PVEAppliance {
	list := []protocol.PVEAppliance{}
	for line := range strings.Lines(string(out)) {
		f := strings.Fields(line)
		if len(f) == 2 {
			list = append(list, protocol.PVEAppliance{Section: f[0], File: f[1]})
		}
	}
	return list
}

type pveError string

func (e pveError) Error() string { return string(e) }

const errNotPVE = pveError("dit is geen Proxmox-host (pveversion of /etc/pve ontbreekt)")

func (a *Agent) pveHost() pveHost {
	h := pveHost{root: a.Root}
	if a.Exec != nil {
		h.run = a.Exec
	}
	return h
}

func (a *Agent) onPVE(m *nats.Msg) {
	go func() {
		res := a.handlePVE(m.Data)
		data, err := envelope(protocol.TypePVEResult, res)
		if err != nil {
			return
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op Proxmox-verzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handlePVE(data []byte) protocol.PVEResponse {
	var env protocol.Envelope
	var req protocol.PVERequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypePVERequest {
		return protocol.PVEResponse{Error: "ongeldig Proxmox-verzoek"}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil {
		return protocol.PVEResponse{Error: "ongeldig Proxmox-verzoek"}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.PVEResponse{Error: "Proxmox-verzoek verlopen; klopt de klok van deze host?"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.PVERequestTimeout)
	defer cancel()
	h := a.pveHost()
	switch req.Op {
	case protocol.PVEOpInventory:
		inv, err := h.inventory(ctx)
		if err != nil {
			return protocol.PVEResponse{Error: err.Error(), Inventory: &inv}
		}
		return protocol.PVEResponse{Inventory: &inv}
	case protocol.PVEOpAvailable:
		list, err := h.available(ctx)
		if err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		return protocol.PVEResponse{Available: list}
	case protocol.PVEOpGet:
		if err := req.ValidateGet(); err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		data, err := h.get(ctx, req.Path, req.Params)
		if err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		return protocol.PVEResponse{Data: data}
	case protocol.PBSOpGet:
		if err := req.ValidatePBSGet(); err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		data, err := h.pbsGet(ctx, req.Path, req.Params)
		if err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		return protocol.PVEResponse{Data: data}
	case protocol.TrueNASOpGet:
		if err := req.ValidateTrueNASGet(); err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		data, err := h.trueNASGet(ctx, req.Path, req.Args)
		if err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		return protocol.PVEResponse{Data: data}
	case protocol.AIVOpGet:
		if err := req.ValidateAIVGet(); err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		data, err := h.aivGet(ctx, req.Path, req.Params)
		if err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		return protocol.PVEResponse{Data: data}
	case protocol.AIVOpPost:
		if err := req.ValidateAIVPost(); err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		data, err := h.aivPost(ctx, req.Path, req.Args)
		if err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		return protocol.PVEResponse{Data: data}
	case protocol.OPNOpGet, protocol.OPNOpPost, protocol.OPNOpConfigctl, protocol.OPNOpRevisions, protocol.OPNOpRevision, protocol.OPNOpFacts, protocol.OPNOpBackup, protocol.OPNOpFilterLog:
		return a.handleOPN(ctx, req)
	case protocol.PVEOpHardware:
		hw, err := h.hardware(ctx)
		if err != nil {
			return protocol.PVEResponse{Error: err.Error()}
		}
		return protocol.PVEResponse{Hardware: &hw}
	}
	return protocol.PVEResponse{Error: "onbekend Proxmox-verzoek " + req.Op}
}
