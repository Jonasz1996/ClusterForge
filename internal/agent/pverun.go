package agent

// Fase 17: de run pve verandert iets op een Proxmox-host met pct, qm of
// pveam. Elke actie is getypt (protocol.PVERun); hier wordt het argv
// gebouwd, nooit een shellregel.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func pveTool(typ string) string {
	if typ == "lxc" {
		return "pct"
	}
	return "qm"
}

func boolArg(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func (r *RunExecutor) host() pveHost {
	h := pveHost{root: r.Root}
	if r.Cmd != nil {
		h.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			var buf bytes.Buffer
			code, err := r.Cmd(ctx, "", append([]string{name}, args...), nil, &buf)
			if err == nil && code != 0 {
				err = &errStep{name, code}
			}
			return buf.Bytes(), err
		}
	}
	return h
}

// pve voert een run pve uit.
func (r *RunExecutor) pve(ctx context.Context, dir string, p *protocol.PVERun, out io.Writer, st *protocol.RunStatus) (int, error) {
	if p.Action == protocol.AIVInstall {
		return r.aivInstall(ctx, p.AIVExt, out)
	}
	if p.Action == protocol.AIVCall {
		return r.aivCall(ctx, p.AIV, out, st)
	}
	if p.Action == protocol.TrueNASCall {
		return r.trueNASCall(ctx, dir, p.TrueNAS, out, st)
	}
	if p.Action == protocol.PBSAPI {
		return r.pbsAPI(ctx, dir, p.API, out, st)
	}
	if strings.HasPrefix(p.Action, "opn_") {
		return r.opn(ctx, p, out, st)
	}
	h := r.host()
	if !h.isHost() {
		return -1, errNotPVE
	}
	tool, id := pveTool(p.Type), strconv.Itoa(p.VMID)
	switch p.Action {
	case protocol.PVEAPI, protocol.PVEPassthrough, protocol.PVEVolumeFree, protocol.PVEZpoolScrub, protocol.PVEMaintenance:
		return r.pveHostAction(ctx, dir, p, out, st)
	case protocol.PVEStart, protocol.PVEStop, protocol.PVEReboot, protocol.PVESuspend, protocol.PVEResume:
		return r.step(ctx, out, nil, tool, p.Action, id)
	case protocol.PVEShutdown:
		return r.step(ctx, out, nil, tool, "shutdown", id, "--timeout", "180")
	case protocol.PVESet:
		return r.step(ctx, out, nil, append([]string{tool, "set", id}, pveSetArgs(p.Type, p.Set)...)...)
	case protocol.PVEResize:
		return r.step(ctx, out, nil, tool, "resize", id, p.Disk, p.Size)
	case protocol.PVESnapshot:
		argv := []string{tool, "snapshot", id, p.Snapshot}
		if p.Description != "" {
			argv = append(argv, "--description", p.Description)
		}
		if p.VMState {
			argv = append(argv, "--vmstate", "1")
		}
		return r.step(ctx, out, nil, argv...)
	case protocol.PVERollback, protocol.PVEDelSnapshot:
		return r.step(ctx, out, nil, tool, p.Action, id, p.Snapshot)
	case protocol.PVEMigrate:
		return r.pveMigrate(ctx, p, out)
	case protocol.PVEDestroy:
		status, err := r.pveStatus(ctx, p.Type, p.VMID)
		if err != nil {
			return -1, err
		}
		if status != "stopped" {
			return -1, fmt.Errorf("%s %d staat op %s; zet hem eerst uit", tool, p.VMID, status)
		}
		return r.step(ctx, out, nil, tool, "destroy", id, "--purge", "1")
	case protocol.PVECreateCT:
		return r.pveCreate(ctx, dir, p, out, st)
	case protocol.PVETemplate:
		return r.pveTemplate(ctx, p.Template, out, st)
	case protocol.PVEBootstrap:
		return r.pveBootstrap(ctx, dir, p, out)
	}
	return -1, fmt.Errorf("onbekende actie %q", p.Action)
}

// pveSetArgs bouwt de opties van pct set of qm set.
func pveSetArgs(typ string, s *protocol.PVESetting) []string {
	var a []string
	if s.Cores != nil {
		a = append(a, "--cores", strconv.Itoa(*s.Cores))
	}
	if s.MemoryMiB != nil {
		a = append(a, "--memory", strconv.Itoa(*s.MemoryMiB))
	}
	if s.SwapMiB != nil && typ == "lxc" {
		a = append(a, "--swap", strconv.Itoa(*s.SwapMiB))
	}
	if s.OnBoot != nil {
		a = append(a, "--onboot", boolArg(*s.OnBoot))
	}
	if s.Tags != nil {
		a = append(a, "--tags", strings.Join(*s.Tags, ";"))
	}
	if s.Description != nil {
		a = append(a, "--description", *s.Description)
	}
	if s.Nameserver != nil && typ == "lxc" {
		a = append(a, "--nameserver", *s.Nameserver)
	}
	if s.Features != nil && typ == "lxc" {
		if *s.Features == "" {
			a = append(a, "--delete", "features")
		} else {
			a = append(a, "--features", *s.Features)
		}
	}
	return a
}

// pveStatus geeft running, stopped, … van een gast op deze host.
func (r *RunExecutor) pveStatus(ctx context.Context, typ string, vmid int) (string, error) {
	out, code, err := r.capture(ctx, pveTool(typ), "status", strconv.Itoa(vmid))
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("%s %d staat niet op deze host: %s", pveTool(typ), vmid, strings.TrimSpace(string(out)))
	}
	_, s, _ := strings.Cut(strings.TrimSpace(string(out)), "status:")
	return strings.TrimSpace(s), nil
}

func (r *RunExecutor) pveMigrate(ctx context.Context, p *protocol.PVERun, out io.Writer) (int, error) {
	status, err := r.pveStatus(ctx, p.Type, p.VMID)
	if err != nil {
		return -1, err
	}
	id := strconv.Itoa(p.VMID)
	if p.Type == "lxc" {
		argv := []string{"pct", "migrate", id, p.Target}
		if status == "running" {
			// Een CT kan niet live verhuizen: afsluiten, verhuizen, starten.
			argv = append(argv, "--restart", "1", "--timeout", "180")
		}
		if p.TargetStorage != "" {
			argv = append(argv, "--target-storage", p.TargetStorage)
		}
		return r.step(ctx, out, nil, argv...)
	}
	argv := []string{"qm", "migrate", id, p.Target}
	if status == "running" {
		argv = append(argv, "--online", "1")
	}
	if p.TargetStorage != "" {
		argv = append(argv, "--targetstorage", p.TargetStorage, "--with-local-disks", "1")
	}
	return r.step(ctx, out, nil, argv...)
}

// pveExistingCT leest de configuratie van een CT met dit VMID ergens in
// het Proxmox-cluster; ok is false als het id vrij is.
func (r *RunExecutor) pveExistingCT(vmid int) (hostname string, onNode string, exists bool) {
	h := r.host()
	if slices.Contains(h.usedIDs(), vmid) {
		exists = true
	}
	files, _ := filepath.Glob(r.path("/etc/pve/nodes/*/lxc/" + strconv.Itoa(vmid) + ".conf"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		return parsePVEConfig(data).main["hostname"], filepath.Base(filepath.Dir(filepath.Dir(f))), true
	}
	if !exists {
		qm, _ := filepath.Glob(r.path("/etc/pve/nodes/*/qemu-server/" + strconv.Itoa(vmid) + ".conf"))
		exists = len(qm) > 0
	}
	return "", "", exists
}

func (r *RunExecutor) pveCreate(ctx context.Context, dir string, p *protocol.PVERun, out io.Writer, st *protocol.RunStatus) (int, error) {
	c := p.Create
	id := strconv.Itoa(p.VMID)
	if name, node, exists := r.pveExistingCT(p.VMID); exists {
		// Een hervatte uitrol: de CT is er al, met dezelfde naam.
		if name == c.Hostname && node == r.host().members().Nodename {
			_, _ = fmt.Fprintf(out, "CT %d (%s) bestaat al op deze host; niets opnieuw gemaakt\n", p.VMID, name)
			st.PVE = &protocol.PVERunResult{Existed: true}
			return 0, nil
		}
		if name != "" {
			return -1, fmt.Errorf("VMID %d is al in gebruik door CT %s op %s", p.VMID, name, node)
		}
		return -1, fmt.Errorf("VMID %d is al in gebruik in het Proxmox-cluster", p.VMID)
	}
	net0 := "name=eth0,bridge=" + c.Bridge
	if c.VLAN > 0 {
		net0 += ",tag=" + strconv.Itoa(c.VLAN)
	}
	if c.Address != "" {
		net0 += ",ip=" + c.Address + ",gw=" + c.Gateway
	} else {
		net0 += ",ip=dhcp"
	}
	argv := []string{
		"pct", "create", id, c.OSTemplate,
		"--hostname", c.Hostname,
		"--unprivileged", boolArg(!c.Privileged),
		"--cores", strconv.Itoa(c.Cores),
		"--memory", strconv.Itoa(c.MemoryMiB),
		"--swap", strconv.Itoa(c.SwapMiB),
		"--rootfs", c.Storage + ":" + strconv.Itoa(c.DiskGiB),
		"--net0", net0,
		"--onboot", boolArg(c.OnBoot),
	}
	var features []string
	if c.Nesting {
		features = append(features, "nesting=1")
	}
	if c.Keyctl {
		features = append(features, "keyctl=1")
	}
	if len(features) > 0 {
		argv = append(argv, "--features", strings.Join(features, ","))
	}
	if len(c.DNS) > 0 {
		argv = append(argv, "--nameserver", strings.Join(c.DNS, " "))
	}
	if len(c.Tags) > 0 {
		argv = append(argv, "--tags", strings.Join(c.Tags, ";"))
	}
	if c.Description != "" {
		argv = append(argv, "--description", c.Description)
	}
	keys := slices.Clone(c.SSHKeys)
	if c.HostKeys {
		keys = append(keys, r.host().sshKeys()...)
	}
	if len(keys) > 0 {
		file := filepath.Join(dir, "ssh-keys")
		if err := os.WriteFile(file, []byte(strings.Join(keys, "\n")+"\n"), 0o600); err != nil {
			return -1, err
		}
		argv = append(argv, "--ssh-public-keys", file)
		_, _ = fmt.Fprintf(out, "%d SSH-sleutel(s) voor root in de CT", len(keys))
		if c.HostKeys {
			_, _ = fmt.Fprintf(out, ", waarvan die van root op deze host")
		}
		_, _ = fmt.Fprintln(out)
	}
	if c.Start {
		argv = append(argv, "--start", "1")
	}
	return r.step(ctx, out, nil, argv...)
}

// pveTemplate zorgt dat een containertemplate op de host staat.
func (r *RunExecutor) pveTemplate(ctx context.Context, t *protocol.PVETemplateSpec, out io.Writer, st *protocol.RunStatus) (int, error) {
	file := t.File
	have := func(name string) bool {
		list, _, err := r.capture(ctx, "pveam", "list", t.Storage)
		if err != nil {
			return false
		}
		want := t.Storage + ":vztmpl/" + name
		for line := range strings.Lines(string(list)) {
			if f := strings.Fields(line); len(f) > 0 && f[0] == want {
				return true
			}
		}
		return false
	}
	if file == "" {
		// Eerst kijken of er al een passende staat, zodat een uitrol zonder
		// internet op de host ook lukt.
		var local []string
		if list, _, err := r.capture(ctx, "pveam", "list", t.Storage); err == nil {
			for line := range strings.Lines(string(list)) {
				f := strings.Fields(line)
				if len(f) == 0 {
					continue
				}
				if name, ok := strings.CutPrefix(f[0], t.Storage+":vztmpl/"); ok && matchTemplate(name, t.Match) {
					local = append(local, name)
				}
			}
		}
		_, _ = fmt.Fprintf(out, "$ pveam update\n")
		if code, err := r.cmd(ctx, "", []string{"pveam", "update"}, nil, out); err != nil || code != 0 {
			_, _ = fmt.Fprintf(out, "pveam update mislukte; de lijst kan oud zijn\n")
		}
		list, _, err := r.capture(ctx, "pveam", "available", "--section", "system")
		if err != nil {
			return -1, err
		}
		var names []string
		for _, a := range parsePveamAvailable(list) {
			if matchTemplate(a.File, t.Match) {
				names = append(names, a.File)
			}
		}
		names = append(names, local...)
		if len(names) == 0 {
			return -1, fmt.Errorf("geen containertemplate %s…_%s in pveam available; download er zelf een of kies een andere", t.Match, runtime.GOARCH)
		}
		slices.SortFunc(names, compareVersions)
		file = names[len(names)-1]
		_, _ = fmt.Fprintf(out, "nieuwste: %s\n", file)
	}
	volid := t.Storage + ":vztmpl/" + file
	st.PVE = &protocol.PVERunResult{Volid: volid}
	if have(file) {
		_, _ = fmt.Fprintf(out, "%s staat al op %s\n", file, t.Storage)
		st.PVE.Existed = true
		return 0, nil
	}
	return r.step(ctx, out, nil, "pveam", "download", t.Storage, file)
}

// matchTemplate: begint met prefix en is voor de architectuur van de host.
func matchTemplate(name, prefix string) bool {
	return strings.HasPrefix(name, prefix) && strings.Contains(name, "_"+runtime.GOARCH+".tar.")
}

// compareVersions vergelijkt namen met versienummers zoals sort -V.
func compareVersions(a, b string) int {
	split := func(s string) []string {
		var parts []string
		cur, digit := "", false
		for _, r := range s {
			d := r >= '0' && r <= '9'
			if cur != "" && d != digit {
				parts = append(parts, cur)
				cur = ""
			}
			cur += string(r)
			digit = d
		}
		return append(parts, cur)
	}
	pa, pb := split(a), split(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, ex := strconv.Atoi(pa[i])
		y, ey := strconv.Atoi(pb[i])
		if ex == nil && ey == nil {
			if x != y {
				return x - y
			}
			continue
		}
		if c := strings.Compare(pa[i], pb[i]); c != 0 {
			return c
		}
	}
	return len(pa) - len(pb)
}

// De units van cf-agent in een CT; zoals ct-template.sh.
const ctAgentUnit = `[Unit]
Description=ClusterForge agent
Documentation=https://github.com/Jonasz1996/ClusterForge
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/cf-agent run
Restart=always
RestartSec=5
# De agent voert later beheeracties uit (herstarten, services), dus root.
User=root

[Install]
WantedBy=multi-user.target
`

const ctFaultClearUnit = `[Unit]
Description=ClusterForge: achtergebleven storingen van een rampoefening terugdraaien
Documentation=https://github.com/Jonasz1996/ClusterForge
DefaultDependencies=no
After=local-fs.target
Before=network-pre.target cf-agent.service
Wants=network-pre.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/cf-agent fault-clear

[Install]
WantedBy=multi-user.target
`

// pveBootstrap zet cf-agent in een CT met een aanmeldbestand, en de
// hostsleutel van de CT in known_hosts van de host.
func (r *RunExecutor) pveBootstrap(ctx context.Context, dir string, p *protocol.PVERun, out io.Writer) (code int, err error) {
	b := p.Bootstrap
	id := strconv.Itoa(p.VMID)
	// Het token hoort na de run niet meer op de host.
	defer r.forgetToken(dir)

	status, err := r.pveStatus(ctx, "lxc", p.VMID)
	if err != nil {
		return -1, err
	}
	if status != "running" {
		if code, err := r.step(ctx, out, nil, "pct", "start", id); err != nil {
			return code, err
		}
	}
	_, _ = fmt.Fprintf(out, "wachten tot de CT antwoordt\n")
	ready := false
	for range 60 {
		if _, code, err := r.capture(ctx, "pct", "exec", id, "--", "true"); err == nil && code == 0 {
			ready = true
			break
		}
		if err := r.sleep(ctx, time.Second); err != nil {
			return -1, err
		}
	}
	if !ready {
		return -1, fmt.Errorf("CT %d antwoordt niet op pct exec", p.VMID)
	}
	for i := range 60 {
		if _, code, err := r.capture(ctx, "pct", "exec", id, "--", "sh", "-c", "ip -4 route show default | grep -q ."); err == nil && code == 0 {
			_, _ = fmt.Fprintf(out, "netwerk van de CT is op\n")
			break
		}
		if i == 59 {
			_, _ = fmt.Fprintf(out, "de CT heeft na een minuut nog geen standaardroute; toch verder\n")
			break
		}
		if err := r.sleep(ctx, time.Second); err != nil {
			return -1, err
		}
	}

	if _, code, err := r.capture(ctx, "pct", "exec", id, "--", "test", "-s", "/etc/clusterforge/agent.json"); err == nil && code == 0 {
		_, _ = fmt.Fprintf(out, "cf-agent is in deze CT al aangemeld; alleen de service aanzetten\n")
		return r.step(ctx, out, nil, "pct", "exec", id, "--", "systemctl", "enable", "--now", "cf-agent.service")
	}

	arch := runtime.GOARCH
	want := b.SHA256[arch]
	if want == "" {
		return -1, fmt.Errorf("ClusterForge biedt geen cf-agent voor %s aan", arch)
	}
	bin := filepath.Join(dir, "cf-agent")
	url := strings.TrimRight(b.Server, "/") + "/downloads/cf-agent-linux-" + arch
	_, _ = fmt.Fprintf(out, "cf-agent downloaden van %s\n", url)
	if err := r.download(ctx, url, bin, want); err != nil {
		return -1, err
	}
	defer func() { _ = os.Remove(bin) }()
	_, _ = fmt.Fprintf(out, "sha256 klopt (%s…)\n", want[:12])

	enroll, _ := json.Marshal(EnrollFile{Server: b.Server, Token: b.Token, AllowHTTP: b.AllowHTTP})
	enrollFile := filepath.Join(dir, "enroll.json")
	if err := os.WriteFile(enrollFile, append(enroll, '\n'), 0o600); err != nil {
		return -1, err
	}
	defer func() { _ = os.Remove(enrollFile) }()
	units := map[string]string{"cf-agent.service": ctAgentUnit, "cf-agent-fault-clear.service": ctFaultClearUnit}
	for name, body := range units {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return -1, err
		}
	}
	steps := [][]string{
		{"pct", "exec", id, "--", "install", "-d", "-m", "0700", "/etc/clusterforge"},
		{"pct", "push", id, bin, "/usr/local/bin/cf-agent", "--perms", "0755"},
		{"pct", "push", id, enrollFile, "/etc/clusterforge/enroll.json", "--perms", "0600"},
		{"pct", "push", id, filepath.Join(dir, "cf-agent.service"), "/etc/systemd/system/cf-agent.service", "--perms", "0644"},
		{"pct", "push", id, filepath.Join(dir, "cf-agent-fault-clear.service"), "/etc/systemd/system/cf-agent-fault-clear.service", "--perms", "0644"},
		{"pct", "exec", id, "--", "systemctl", "daemon-reload"},
		{"pct", "exec", id, "--", "systemctl", "enable", "cf-agent-fault-clear.service"},
		{"pct", "exec", id, "--", "systemctl", "enable", "--now", "cf-agent.service"},
	}
	for _, argv := range steps {
		if code, err := r.step(ctx, out, nil, argv...); err != nil {
			return code, err
		}
	}
	if b.KnownHost != "" {
		r.knownHost(ctx, b.KnownHost, out)
	}
	return 0, nil
}

// forgetToken haalt het aanmeldtoken uit spec.json van de run.
func (r *RunExecutor) forgetToken(dir string) {
	file := filepath.Join(dir, "spec.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var spec protocol.RunSpec
	if json.Unmarshal(data, &spec) != nil || spec.PVE == nil || spec.PVE.Bootstrap == nil {
		return
	}
	spec.PVE.Bootstrap.Token = "verwijderd"
	if data, err := json.Marshal(spec); err == nil {
		_ = writeFileAtomic(file, data, 0o600)
	}
}

// download haalt url naar file en controleert de sha256.
func (r *RunExecutor) download(ctx context.Context, url, file, want string) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	h := sha256.New()
	w := io.MultiWriter(f, h)
	if r.Download != nil {
		err = r.Download(ctx, url, w)
	} else {
		err = httpDownload(ctx, url, w)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(file)
		return fmt.Errorf("downloaden mislukt: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		_ = os.Remove(file)
		return errors.New("de sha256 van de download klopt niet met die van de server; niets in de CT gezet")
	}
	return nil
}

func httpDownload(ctx context.Context, url string, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, protocol.UpgradeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s gaf %s", url, resp.Status)
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, maxAgentSize))
	return err
}

// knownHost zet de hostsleutels van addr in known_hosts van root, in de
// plaats van oude regels voor dat adres. Mislukken is geen fout: de CT werkt
// ook zonder.
func (r *RunExecutor) knownHost(ctx context.Context, addr string, out io.Writer) {
	keys, code, err := r.capture(ctx, "ssh-keyscan", "-T", "5", "-t", "ed25519,ecdsa,rsa", addr)
	var lines []string
	for line := range strings.Lines(string(keys)) {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	if err != nil || code != 0 || len(lines) == 0 {
		_, _ = fmt.Fprintf(out, "hostsleutel van %s niet gelezen (staat sshd in de CT?); known_hosts van de host blijft zo\n", addr)
		return
	}
	file := r.path("/root/.ssh/known_hosts")
	old, _ := os.ReadFile(file)
	var keep []string
	for line := range strings.Lines(string(old)) {
		l := strings.TrimRight(line, "\n")
		hosts, _, _ := strings.Cut(l, " ")
		if slices.Contains(strings.Split(hosts, ","), addr) {
			continue
		}
		keep = append(keep, l)
	}
	keep = append(keep, lines...)
	if err := os.WriteFile(file, []byte(strings.Join(keep, "\n")+"\n"), 0o600); err != nil {
		_, _ = fmt.Fprintf(out, "known_hosts schrijven mislukt: %v\n", err)
		return
	}
	_, _ = fmt.Fprintf(out, "hostsleutel van %s staat in known_hosts van de host: ssh root@%s werkt zonder vraag\n", addr, addr)
}
