package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Firewall (fase 16). De agent beheert ufw: hij leest de regels en de
// luisterende poorten, zet de regels die de server stuurt met een
// terugdraaitimer van systemd, en telt het verkeer dat ufw weggooit.

const (
	ufwPath       = "/usr/sbin/ufw"
	ufwUserRules  = "/etc/ufw/user.rules"
	ufwUser6Rules = "/etc/ufw/user6.rules"
	ufwAfter      = "/etc/ufw/after.rules"
	ufwAfter6     = "/etc/ufw/after6.rules"
	ufwConf       = "/etc/ufw/ufw.conf"
	ufwDefault    = "/etc/default/ufw"

	fwMarkBegin = "# BEGIN ClusterForge: verkeer naar dichte poorten tellen (niet aanpassen)"
	fwMarkEnd   = "# END ClusterForge"
)

// fwBackupFiles zijn de bestanden die een toepassing kan veranderen.
var fwBackupFiles = []string{ufwUserRules, ufwUser6Rules, ufwAfter, ufwAfter6, ufwConf, ufwDefault}

func fwPending() string    { return protocol.FirewallDir + "/pending.json" }
func fwRolledBack() string { return protocol.FirewallDir + "/rolledback.json" }
func fwBackupDir() string  { return protocol.FirewallDir + "/backup" }
func fwScript() string     { return protocol.FirewallDir + "/rollback.sh" }

func (a *Agent) onFirewall(m *nats.Msg) {
	go func() {
		res := a.handleFirewall(m.Data)
		data, err := envelope(protocol.TypeFirewallResult, res)
		if err != nil {
			return
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op firewallverzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handleFirewall(data []byte) protocol.FirewallResponse {
	var env protocol.Envelope
	var req protocol.FirewallRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeFirewallRequest {
		return protocol.FirewallResponse{Error: "ongeldig firewallverzoek"}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil {
		return protocol.FirewallResponse{Error: "ongeldig firewallverzoek"}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.FirewallResponse{Error: "firewallverzoek verlopen; klopt de klok van deze node?"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.FirewallRequestTimeout)
	defer cancel()
	switch req.Op {
	case protocol.FirewallOpStatus:
		st := a.firewallStatus(ctx)
		return protocol.FirewallResponse{Status: &st}
	case protocol.FirewallOpApply:
		if req.Apply == nil {
			return protocol.FirewallResponse{Error: "apply zonder regels"}
		}
		res, err := a.firewallApply(ctx, *req.Apply)
		if err != nil {
			return protocol.FirewallResponse{Error: err.Error(), Apply: res}
		}
		return protocol.FirewallResponse{Apply: res}
	case protocol.FirewallOpConfirm:
		if err := a.firewallConfirm(ctx, req.ApplyID); err != nil {
			return protocol.FirewallResponse{Error: err.Error()}
		}
		return protocol.FirewallResponse{}
	case protocol.FirewallOpRollback:
		if err := a.firewallRollback(ctx, req.ApplyID, "ClusterForge"); err != nil {
			return protocol.FirewallResponse{Error: err.Error()}
		}
		return protocol.FirewallResponse{}
	case protocol.FirewallOpDisable:
		if err := a.firewallDisable(ctx); err != nil {
			return protocol.FirewallResponse{Error: err.Error()}
		}
		return protocol.FirewallResponse{}
	case protocol.FirewallOpProbe:
		if len(req.Probe) > protocol.MaxFirewallProbes {
			return protocol.FirewallResponse{Error: "te veel doelen"}
		}
		res, err := a.firewallProbe(ctx, req.Probe)
		if err != nil {
			return protocol.FirewallResponse{Error: err.Error()}
		}
		return protocol.FirewallResponse{Probe: res}
	case protocol.FirewallOpBlocked:
		b := a.fwTake(ctx)
		return protocol.FirewallResponse{Blocked: &b}
	}
	return protocol.FirewallResponse{Error: "onbekend firewallverzoek " + req.Op}
}

func (a *Agent) ufwInstalled() bool {
	_, err := os.Stat(a.rootPath(ufwPath))
	return err == nil
}

func (a *Agent) readFile(p string) string {
	b, err := os.ReadFile(a.rootPath(p))
	if err != nil {
		return ""
	}
	return string(b)
}

func (a *Agent) firewallStatus(ctx context.Context) protocol.FirewallStatus {
	st := protocol.FirewallStatus{Installed: a.ufwInstalled(), Rules: []protocol.FirewallRule{}}
	st.Listeners = a.firewallListeners()
	if inspect := a.dockerInspectAll(ctx); inspect != nil {
		st.Containers, _ = parseFirewallInspect(inspect)
		fillContainers(st.Listeners, inspect)
	}
	a.fw.mu.Lock()
	st.Monitor, st.MonitorError = a.fw.mode, a.fw.err
	a.fw.mu.Unlock()
	if st.Monitor == "" {
		st.Monitor = "off"
	}
	st.Pending = a.fwReadPending()
	var applied protocol.FirewallApplied
	if readJSON(a.rootPath(protocol.FirewallStateFile), &applied) {
		st.Applied = &applied
	}
	var rb protocol.FirewallRolledBack
	if readJSON(a.rootPath(fwRolledBack()), &rb) {
		st.RolledBack = &rb
	}
	if !st.Installed {
		return st
	}
	if out, err := a.stdout(ctx, "ufw", "version"); err == nil {
		if f := strings.Fields(string(out)); len(f) >= 2 {
			st.Version = f[1]
		}
	} else {
		st.Errors = append(st.Errors, "ufw version: "+err.Error())
	}
	if out, err := a.stdout(ctx, "ufw", "status"); err == nil {
		st.Active = strings.Contains(string(out), "Status: active")
	} else {
		st.Errors = append(st.Errors, "ufw status: "+err.Error())
		st.Active = shellVar(a.readFile(ufwConf), "ENABLED") == "yes"
	}
	def := a.readFile(ufwDefault)
	st.Incoming = policyName(shellVar(def, "DEFAULT_INPUT_POLICY"))
	st.Outgoing = policyName(shellVar(def, "DEFAULT_OUTPUT_POLICY"))
	st.Routed = policyName(shellVar(def, "DEFAULT_FORWARD_POLICY"))
	st.Logging = shellVar(a.readFile(ufwConf), "LOGLEVEL")
	st.Rules = protocol.ParseUfwTuples(a.readFile(ufwUserRules), a.readFile(ufwUser6Rules))
	if st.Rules == nil {
		st.Rules = []protocol.FirewallRule{}
	}
	st.MonitorRule = strings.Contains(a.readFile(ufwAfter), fwMarkBegin)
	return st
}

var shellVarRe = regexp.MustCompile(`(?m)^\s*([A-Z_]+)\s*=\s*"?([^"\n]*)"?\s*$`)

// shellVar leest NAAM="waarde" uit een bestand als /etc/default/ufw.
func shellVar(s, name string) string {
	for _, m := range shellVarRe.FindAllStringSubmatch(s, -1) {
		if m[1] == name {
			return strings.TrimSpace(m[2])
		}
	}
	return ""
}

func policyName(p string) string {
	switch p {
	case "DROP":
		return "deny"
	case "ACCEPT":
		return "allow"
	case "REJECT":
		return "reject"
	}
	return strings.ToLower(p)
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *Agent) fwReadPending() *protocol.FirewallPending {
	var p protocol.FirewallPending
	if readJSON(a.rootPath(fwPending()), &p) {
		return &p
	}
	return nil
}

// containerID haalt het id van een Docker-container uit /proc/<pid>/cgroup.
var containerID = regexp.MustCompile(`(?:docker-|/docker/)([0-9a-f]{64})`)

type sockOwner struct{ process, unit, container string }

// firewallListeners geeft de luisterende poorten met proces, unit en
// container.
func (a *Agent) firewallListeners() []protocol.FirewallListener {
	c := a.Collector
	if c == nil {
		c = &Collector{Root: a.Root}
	}
	owners := c.socketOwners()
	lo, hi := c.ephemeral()
	seen := map[string]bool{}
	out := []protocol.FirewallListener{}
	for _, proto := range []string{"tcp", "udp"} {
		for _, s := range c.sockets(proto) {
			if proto == "tcp" && s.state != "0A" || proto == "udp" && (s.state != "07" || s.rport != 0) {
				continue
			}
			if s.lport >= lo && s.lport <= hi {
				continue
			}
			o := owners[s.inode]
			l := protocol.FirewallListener{Proto: proto, Addr: s.local.String(), Port: s.lport, Process: o.process, Unit: o.unit, Container: o.container}
			key := proto + " " + net.JoinHostPort(l.Addr, strconv.Itoa(l.Port))
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		if out[i].Proto != out[j].Proto {
			return out[i].Proto < out[j].Proto
		}
		return out[i].Addr < out[j].Addr
	})
	if len(out) > maxListeners {
		out = out[:maxListeners]
	}
	return out
}

// socketOwners koppelt het inode van elke socket aan het proces (comm), de
// systemd-unit en de container.
func (c *Collector) socketOwners() map[string]sockOwner {
	out := map[string]sockOwner{}
	procs, err := os.ReadDir(c.path("/proc"))
	if err != nil {
		return out
	}
	for _, p := range procs {
		if _, err := strconv.Atoi(p.Name()); err != nil {
			continue
		}
		fds, err := os.ReadDir(c.path("/proc/" + p.Name() + "/fd"))
		if err != nil {
			continue
		}
		var o sockOwner
		loaded := false
		for _, fd := range fds {
			target, err := os.Readlink(c.path("/proc/" + p.Name() + "/fd/" + fd.Name()))
			if err != nil {
				continue
			}
			inode, ok := strings.CutPrefix(target, "socket:[")
			if !ok {
				continue
			}
			inode = strings.TrimSuffix(inode, "]")
			if _, taken := out[inode]; taken {
				continue
			}
			if !loaded {
				loaded = true
				cg := c.read("/proc/" + p.Name() + "/cgroup")
				o = sockOwner{process: c.read("/proc/" + p.Name() + "/comm"), unit: unitFromCgroup(cg)}
				if m := containerID.FindStringSubmatch(cg); m != nil {
					o.container = m[1]
				}
			}
			out[inode] = o
		}
	}
	return out
}

// dockerInspectAll geeft docker inspect van de draaiende containers; nil
// zonder Docker.
func (a *Agent) dockerInspectAll(ctx context.Context) []byte {
	ids, err := a.stdout(ctx, "docker", "ps", "-q", "--no-trunc")
	if err != nil || len(bytes.TrimSpace(ids)) == 0 {
		return nil
	}
	out, err := a.stdout(ctx, "docker", append([]string{"inspect"}, strings.Fields(string(ids))...)...)
	if err != nil {
		return nil
	}
	return out
}

// parseFirewallInspect leest docker inspect; de map gaat van id naar index.
func parseFirewallInspect(out []byte) ([]protocol.FirewallContainer, map[string]int) {
	var raw []struct {
		ID         string `json:"Id"`
		Name       string
		Config     struct{ Image string }
		HostConfig struct{ NetworkMode string }
		Network    struct {
			Ports map[string][]struct{ HostIp, HostPort string }
		} `json:"NetworkSettings"`
	}
	if json.Unmarshal(out, &raw) != nil {
		return nil, nil
	}
	res := make([]protocol.FirewallContainer, 0, len(raw))
	idx := map[string]int{}
	for _, r := range raw {
		c := protocol.FirewallContainer{Name: strings.TrimPrefix(r.Name, "/"), Image: r.Config.Image, Network: r.HostConfig.NetworkMode}
		for spec, binds := range r.Network.Ports {
			port, proto, _ := strings.Cut(spec, "/")
			cp, _ := strconv.Atoi(port)
			for _, b := range binds {
				hp, err := strconv.Atoi(b.HostPort)
				if err != nil {
					continue
				}
				c.Ports = append(c.Ports, protocol.FirewallPublished{Proto: proto, HostIP: b.HostIp, HostPort: hp, ContainerPort: cp})
			}
		}
		sort.Slice(c.Ports, func(i, j int) bool {
			if c.Ports[i].HostPort != c.Ports[j].HostPort {
				return c.Ports[i].HostPort < c.Ports[j].HostPort
			}
			return c.Ports[i].HostIP < c.Ports[j].HostIP
		})
		idx[r.ID] = len(res)
		res = append(res, c)
	}
	return res, idx
}

// fillContainers zet naam en image van de container bij de listeners.
func fillContainers(ls []protocol.FirewallListener, inspect []byte) {
	cs, idx := parseFirewallInspect(inspect)
	for i := range ls {
		if ls[i].Container == "" {
			continue
		}
		if j, ok := idx[ls[i].Container]; ok {
			ls[i].Container, ls[i].Image = cs[j].Name, cs[j].Image
		}
	}
}

// firewallApply zet de regels. Eerst een kopie en een terugdraaitimer, dan
// de nieuwe regels, het beleid en ufw aan, dan pas weghalen. Faalt iets, dan
// draait hij meteen terug.
func (a *Agent) firewallApply(ctx context.Context, in protocol.FirewallApply) (*protocol.FirewallApplyResult, error) {
	a.fwMu.Lock()
	defer a.fwMu.Unlock()
	if len(in.Rules) > protocol.MaxFirewallRules {
		return nil, fmt.Errorf("te veel regels (%d)", len(in.Rules))
	}
	if !validID.MatchString(in.ID) {
		return nil, fmt.Errorf("ongeldig id %q", in.ID)
	}
	for _, r := range in.Rules {
		if err := r.Validate(); err != nil {
			return nil, err
		}
	}
	if in.ConfirmSeconds < protocol.MinFirewallConfirm || in.ConfirmSeconds > protocol.MaxFirewallConfirm {
		return nil, fmt.Errorf("wachttijd %d s buiten %d-%d", in.ConfirmSeconds, protocol.MinFirewallConfirm, protocol.MaxFirewallConfirm)
	}
	if p := a.fwReadPending(); p != nil {
		return nil, fmt.Errorf("toepassing %s wacht nog op bevestiging (tot %s)", p.ApplyID, p.Deadline.Format(time.TimeOnly))
	}
	if err := sshCovered(a.firewallListeners(), in.Rules); err != nil {
		return nil, err
	}
	res := &protocol.FirewallApplyResult{ApplyID: in.ID, Log: []string{}}
	logf := func(format string, args ...any) { res.Log = append(res.Log, fmt.Sprintf(format, args...)) }
	if !a.ufwInstalled() {
		if !in.Install {
			return res, errors.New("ufw staat niet op deze node")
		}
		logf("ufw installeren")
		if _, err := a.stdout(ctx, "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-o", "DPkg::Lock::Timeout=120",
			"install", "-y", "--no-install-recommends", "ufw"); err != nil {
			return res, fmt.Errorf("ufw installeren: %w", err)
		}
	}
	st := a.firewallStatus(ctx)
	res.Diff = protocol.DiffFirewall(st.Rules, in.Rules, in.Strict)
	monitor := in.Monitor && st.Monitor == "nflog"
	after := map[string]string{}
	for _, f := range []string{ufwAfter, ufwAfter6} {
		cur := a.readFile(f)
		if cur == "" {
			continue
		}
		chain := "ufw-after-input"
		if f == ufwAfter6 {
			chain = "ufw6-after-input"
		}
		if want := withMonitorBlock(cur, chain, monitor); want != cur {
			after[f] = want
		}
	}
	policyOK := st.Incoming == "deny" && st.Outgoing == "allow"
	if res.Diff.Empty() && st.Active && policyOK && len(after) == 0 {
		logf("niets te doen: de regels kloppen al")
		a.fwRemember(in)
		return res, nil
	}
	res.Changed = true

	// Kopie en terugdraaitimer.
	if err := a.fwBackup(in.ID, st.Active); err != nil {
		return res, err
	}
	_, _ = a.stdout(ctx, "systemctl", "stop", protocol.FirewallRollbackUnit+".timer")
	_, _ = a.stdout(ctx, "systemctl", "reset-failed", protocol.FirewallRollbackUnit+".service", protocol.FirewallRollbackUnit+".timer")
	if _, err := a.stdout(ctx, "systemd-run", "--unit="+protocol.FirewallRollbackUnit, fmt.Sprintf("--on-active=%d", in.ConfirmSeconds),
		"--timer-property=AccuracySec=1s", "/bin/sh", fwScript(), "timer"); err != nil {
		return res, fmt.Errorf("terugdraaitimer zetten mislukt, niets veranderd: %w", err)
	}
	res.Deadline = a.now().Add(time.Duration(in.ConfirmSeconds) * time.Second).UTC()
	pending := protocol.FirewallPending{ApplyID: in.ID, Hash: in.Hash, Deadline: res.Deadline, WasActive: st.Active}
	if err := writeJSON(a.rootPath(fwPending()), pending); err != nil {
		a.fwUndo(ctx, in.ID, "pending schrijven: "+err.Error(), logf)
		return res, err
	}
	logf("terugdraaitimer: %d s (tot %s UTC)", in.ConfirmSeconds, res.Deadline.Format(time.TimeOnly))

	fail := func(err error) (*protocol.FirewallApplyResult, error) {
		a.fwUndo(ctx, in.ID, err.Error(), logf)
		return res, fmt.Errorf("%w; de vorige regels staan terug", err)
	}
	ufw := func(args ...string) error {
		out, err := a.stdout(ctx, "ufw", args...)
		if err != nil {
			return fmt.Errorf("ufw %s: %w", strings.Join(args, " "), err)
		}
		if msg := strings.TrimSpace(string(out)); msg != "" {
			logf("ufw %s → %s", strings.Join(args, " "), lastLine(msg))
		}
		return nil
	}
	for _, r := range append(slices.Clone(res.Diff.Add), res.Diff.Adopt...) {
		if err := ufw(append(r.Args(), "comment", r.Comment)...); err != nil {
			return fail(err)
		}
	}
	// ufw werkt het commentaar van een bestaande regel niet altijd bij:
	// dan weg en opnieuw.
	if len(res.Diff.Adopt) > 0 {
		now := protocol.ParseUfwTuples(a.readFile(ufwUserRules), a.readFile(ufwUser6Rules))
		for _, r := range res.Diff.Adopt {
			if i := slices.IndexFunc(now, func(c protocol.FirewallRule) bool { return c.Key() == r.Key() }); i >= 0 && now[i].Comment != r.Comment {
				if err := ufw(append([]string{"delete"}, r.Args()...)...); err != nil {
					return fail(err)
				}
				if err := ufw(append(r.Args(), "comment", r.Comment)...); err != nil {
					return fail(err)
				}
			}
		}
	}
	for f, content := range after {
		if err := os.WriteFile(a.rootPath(f), []byte(content), 0o640); err != nil {
			return fail(err)
		}
		logf("%s: telregel %s", f, map[bool]string{true: "erbij", false: "weg"}[monitor])
	}
	if st.Incoming != "deny" {
		if err := ufw("default", "deny", "incoming"); err != nil {
			return fail(err)
		}
	}
	if st.Outgoing != "allow" {
		if err := ufw("default", "allow", "outgoing"); err != nil {
			return fail(err)
		}
	}
	switch {
	case !st.Active:
		if err := ufw("--force", "enable"); err != nil {
			return fail(err)
		}
	case len(after) > 0:
		if err := ufw("reload"); err != nil {
			return fail(err)
		}
	}
	for _, r := range res.Diff.Remove {
		if err := ufw(append([]string{"delete"}, r.Args()...)...); err != nil {
			return fail(err)
		}
	}
	// Een nieuwe verbinding naar de server moet nog lukken.
	if err := a.fwCheckServer(ctx); err != nil {
		return fail(fmt.Errorf("nieuwe verbinding naar ClusterForge lukt niet meer: %w", err))
	}
	logf("verbinding naar ClusterForge: in orde")
	return res, nil
}

var validID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// sshCovered weigert een plan dat de poort van sshd niet openzet.
func sshCovered(ls []protocol.FirewallListener, rules []protocol.FirewallRule) error {
	for _, l := range ls {
		if l.Proto != "tcp" || l.Process != "sshd" || l.Local() {
			continue
		}
		if !slices.ContainsFunc(rules, func(r protocol.FirewallRule) bool { return r.Proto == "tcp" && portIn(l.Port, r.Port) }) {
			return fmt.Errorf("plan zonder regel voor SSH (poort %d) geweigerd", l.Port)
		}
	}
	return nil
}

func portIn(p int, spec string) bool {
	lo, hi, rng := strings.Cut(spec, ":")
	a, err := strconv.Atoi(lo)
	if err != nil {
		return false
	}
	b := a
	if rng {
		if b, err = strconv.Atoi(hi); err != nil {
			return false
		}
	}
	return p >= a && p <= b
}

// withMonitorBlock zet de NFLOG-regel in after.rules vóór de COMMIT van
// *filter, of haalt hem weg.
func withMonitorBlock(s, chain string, on bool) string {
	lines := strings.Split(s, "\n")
	var out []string
	skip := false
	for _, l := range lines {
		switch {
		case strings.TrimSpace(l) == fwMarkBegin:
			skip = true
			continue
		case skip && strings.TrimSpace(l) == fwMarkEnd:
			skip = false
			continue
		case skip:
			continue
		}
		out = append(out, l)
	}
	if !on {
		return strings.Join(out, "\n")
	}
	inFilter := false
	for i, l := range out {
		t := strings.TrimSpace(l)
		if t == "*filter" {
			inFilter = true
		}
		if inFilter && t == "COMMIT" {
			block := []string{fwMarkBegin,
				fmt.Sprintf("-A %s -m conntrack --ctstate NEW -j NFLOG --nflog-group %d --nflog-size 96 --nflog-prefix cf-block", chain, protocol.FirewallNflogGroup),
				fwMarkEnd}
			return strings.Join(slices.Concat(out[:i], block, out[i:]), "\n")
		}
	}
	return strings.Join(out, "\n")
}

// fwBackup kopieert ufw en schrijft het terugdraaiscript.
func (a *Agent) fwBackup(id string, wasActive bool) error {
	dir := a.rootPath(fwBackupDir())
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var restore []string
	for _, f := range fwBackupFiles {
		b, err := os.ReadFile(a.rootPath(f))
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), b, 0o600); err != nil {
			return err
		}
		restore = append(restore, fmt.Sprintf("cp %s/%s %s", fwBackupDir(), filepath.Base(f), f))
	}
	again := "ufw --force disable"
	if wasActive {
		again = "ufw --force enable && ufw reload"
	}
	script := fmt.Sprintf(`#!/bin/sh
# ClusterForge: zet ufw terug zoals vóór toepassing %[1]s.
R="${1:-timer}"
%[2]s
%[3]s
printf '{"apply_id":"%[1]s","at":"%%s","reason":"%%s"}\n' "$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)" "$R" > %[4]s
rm -f %[5]s
`, id, strings.Join(restore, "\n"), again, fwRolledBack(), fwPending())
	return os.WriteFile(a.rootPath(fwScript()), []byte(script), 0o700)
}

// fwUndo draait meteen terug en stopt de timer.
func (a *Agent) fwUndo(ctx context.Context, id, reason string, logf func(string, ...any)) {
	ctx = context.WithoutCancel(ctx)
	if _, err := a.stdout(ctx, "/bin/sh", fwScript(), reasonText(reason)); err != nil {
		logf("terugdraaien mislukt: %v (de timer probeert het opnieuw)", err)
		a.Log.Error("firewall terugdraaien mislukt", "apply", id, "err", err)
		return
	}
	_, _ = a.stdout(ctx, "systemctl", "stop", protocol.FirewallRollbackUnit+".timer")
	_ = os.Remove(a.rootPath(fwPending()))
	logf("teruggedraaid: %s", reason)
	a.Log.Warn("firewall teruggedraaid", "apply", id, "reason", reason)
}

var reasonUnsafe = regexp.MustCompile(`[^\pL\pN ._:/()-]`)

func reasonText(s string) string {
	s = reasonUnsafe.ReplaceAllString(s, " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (a *Agent) fwRemember(in protocol.FirewallApply) {
	_ = writeJSON(a.rootPath(protocol.FirewallStateFile), protocol.FirewallApplied{ApplyID: in.ID, Hash: in.Hash, AppliedAt: a.now().UTC()})
}

// fwCheckServer maakt een nieuwe TCP-verbinding naar NATS van de server.
func (a *Agent) fwCheckServer(ctx context.Context) error {
	if a.Config == nil || a.Config.NatsURL == "" {
		return nil
	}
	u, err := url.Parse(a.Config.NatsURL)
	if err != nil || u.Host == "" {
		return nil
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "4222")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dial := a.Dial
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	c, err := dial(ctx, "tcp", host)
	if err != nil {
		return err
	}
	return c.Close()
}

func (a *Agent) firewallConfirm(ctx context.Context, id string) error {
	a.fwMu.Lock()
	defer a.fwMu.Unlock()
	p := a.fwReadPending()
	if p == nil {
		var applied protocol.FirewallApplied
		if readJSON(a.rootPath(protocol.FirewallStateFile), &applied) && applied.ApplyID == id {
			return nil
		}
		var rb protocol.FirewallRolledBack
		if readJSON(a.rootPath(fwRolledBack()), &rb) && rb.ApplyID == id {
			return fmt.Errorf("toepassing %s is al teruggedraaid (%s)", id, rb.Reason)
		}
		return fmt.Errorf("geen openstaande toepassing %s", id)
	}
	if p.ApplyID != id {
		return fmt.Errorf("de openstaande toepassing is %s, niet %s", p.ApplyID, id)
	}
	if _, err := a.stdout(ctx, "systemctl", "stop", protocol.FirewallRollbackUnit+".timer"); err != nil {
		return fmt.Errorf("terugdraaitimer stoppen: %w", err)
	}
	if err := os.Remove(a.rootPath(fwPending())); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	a.fwRemember(protocol.FirewallApply{ID: p.ApplyID, Hash: p.Hash})
	return nil
}

func (a *Agent) firewallRollback(ctx context.Context, id, reason string) error {
	a.fwMu.Lock()
	defer a.fwMu.Unlock()
	p := a.fwReadPending()
	if p == nil {
		return errors.New("er is geen toepassing om terug te draaien")
	}
	if id != "" && p.ApplyID != id {
		return fmt.Errorf("de openstaande toepassing is %s, niet %s", p.ApplyID, id)
	}
	var failed error
	a.fwUndo(ctx, p.ApplyID, reason, func(f string, args ...any) {
		if strings.HasPrefix(f, "terugdraaien mislukt") {
			failed = fmt.Errorf(f, args...)
		}
	})
	return failed
}

func (a *Agent) firewallDisable(ctx context.Context) error {
	a.fwMu.Lock()
	defer a.fwMu.Unlock()
	if !a.ufwInstalled() {
		return nil
	}
	if a.fwReadPending() != nil {
		return errors.New("er wacht nog een toepassing op bevestiging")
	}
	if _, err := a.stdout(ctx, "ufw", "--force", "disable"); err != nil {
		return err
	}
	_ = os.Remove(a.rootPath(protocol.FirewallStateFile))
	return nil
}

// firewallResume draait een onbevestigde toepassing terug als de timer weg
// is (na een herstart van de node).
func (a *Agent) firewallResume(ctx context.Context) {
	p := a.fwReadPending()
	if p == nil {
		return
	}
	out, _ := a.stdout(ctx, "systemctl", "is-active", protocol.FirewallRollbackUnit+".timer")
	if strings.TrimSpace(string(out)) == "active" {
		return
	}
	if err := a.firewallRollback(ctx, p.ApplyID, "niet bevestigd voor een herstart"); err != nil {
		a.Log.Error("onbevestigde firewall terugdraaien mislukt", "err", err)
	}
}

func (a *Agent) firewallProbe(ctx context.Context, targets []string) ([]protocol.FirewallProbe, error) {
	for _, t := range targets {
		host, port, err := net.SplitHostPort(t)
		if err != nil || net.ParseIP(host) == nil {
			return nil, fmt.Errorf("ongeldig doel %q", t)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("ongeldig doel %q", t)
		}
	}
	dial := a.Dial
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	res := make([]protocol.FirewallProbe, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			c, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			start := time.Now()
			conn, err := dial(c, "tcp", t)
			r := protocol.FirewallProbe{Target: t, Millis: int(time.Since(start).Milliseconds())}
			if err == nil {
				r.OK = true
				_ = conn.Close()
			} else {
				r.Error = err.Error()
				r.Refused = strings.Contains(err.Error(), "refused")
			}
			res[i] = r
		})
	}
	wg.Wait()
	return res, nil
}
