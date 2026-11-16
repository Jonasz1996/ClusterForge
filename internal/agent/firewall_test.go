package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const fwAfterRules = `# rules.input-after
*filter
:ufw-after-input - [0:0]
-A ufw-after-input -p udp --dport 137 -j ufw-skip-to-policy-input
-A ufw-after-input -m addrtype --dst-type BROADCAST -j ufw-skip-to-policy-input
COMMIT
`

type fwFake struct {
	mu     sync.Mutex
	calls  []string
	active bool
	fail   string
}

func (f *fwFake) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, call)
	if f.fail != "" && strings.HasPrefix(call, f.fail) {
		return nil, errors.New("mislukt")
	}
	switch {
	case call == "ufw version":
		return []byte("ufw 0.36.2\nCopyright 2008-2023 Canonical Ltd.\n"), nil
	case call == "ufw status":
		if f.active {
			return []byte("Status: active\n"), nil
		}
		return []byte("Status: inactive\n"), nil
	case call == "ufw --force enable":
		f.active = true
		return []byte("Firewall is active and enabled on system startup\n"), nil
	case name == "ufw":
		return []byte("Rule added\n"), nil
	case name == "docker":
		return nil, errors.New("docker: command not found")
	}
	return nil, nil
}

func (f *fwFake) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fwAgent maakt een agent met ufw (uit), sshd op 0.0.0.0:22 en Patroni
// alleen lokaal op 8008.
func fwAgent(t *testing.T) (*Agent, *fwFake) {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		ufwPath:       "",
		ufwUserRules:  "*filter\n### RULES ###\n### tuple ### allow tcp 2379:2380 0.0.0.0/0 any 192.168.103.243 in\n### END RULES ###\nCOMMIT\n",
		ufwUser6Rules: "",
		ufwAfter:      fwAfterRules,
		ufwAfter6:     strings.ReplaceAll(fwAfterRules, "ufw-after", "ufw6-after"),
		ufwConf:       "ENABLED=no\nLOGLEVEL=low\n",
		ufwDefault:    "IPV6=yes\nDEFAULT_INPUT_POLICY=\"ACCEPT\"\nDEFAULT_OUTPUT_POLICY=\"ACCEPT\"\nDEFAULT_FORWARD_POLICY=\"DROP\"\n",
		"/proc/net/tcp": "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
			"   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1234 1 0 100 0 0 10 0\n" +
			"   1: 0100007F:1F48 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1235 1 0 100 0 0 10 0\n",
		"/proc/100/comm":   "sshd\n",
		"/proc/100/cgroup": "0::/system.slice/ssh.service\n",
	})
	if err := os.MkdirAll(filepath.Join(root, "/proc/100/fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[1234]", filepath.Join(root, "/proc/100/fd/3")); err != nil {
		t.Fatal(err)
	}
	f := &fwFake{}
	a := &Agent{Root: root, Log: slog.New(slog.DiscardHandler), Exec: f.exec, Collector: &Collector{Root: root}}
	a.fw.mode = "nflog"
	return a, f
}

func fwRules() []protocol.FirewallRule {
	return []protocol.FirewallRule{
		{Action: "allow", Proto: "tcp", Port: "22", From: "192.168.0.0/16", Comment: "cf:ssh"},
		{Action: "allow", Proto: "tcp", Port: "2379:2380", From: "192.168.103.243", Comment: "cf:etcd (cluster)"},
		{Action: "allow", Proto: "vrrp", From: "192.168.103.243", Comment: "cf:vrrp (cluster)"},
	}
}

func TestFirewallStatus(t *testing.T) {
	a, _ := fwAgent(t)
	st := a.firewallStatus(context.Background())
	if !st.Installed || st.Active || st.Version != "0.36.2" || st.Incoming != "allow" || st.Routed != "deny" || st.Logging != "low" {
		t.Fatalf("stand: %+v", st)
	}
	if len(st.Rules) != 1 || st.Rules[0].Port != "2379:2380" {
		t.Fatalf("regels: %+v", st.Rules)
	}
	if len(st.Listeners) != 2 || st.Listeners[0].Process != "sshd" || st.Listeners[0].Unit != "ssh" || !st.Listeners[1].Local() {
		t.Fatalf("poorten: %+v", st.Listeners)
	}
}

func TestFirewallApplyConfirm(t *testing.T) {
	a, f := fwAgent(t)
	ctx := context.Background()
	in := protocol.FirewallApply{ID: "a1", Hash: "h1", Rules: fwRules(), ConfirmSeconds: 120, Monitor: true}
	res, err := a.firewallApply(ctx, in)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res)
	}
	if !res.Changed || len(res.Diff.Add) != 2 || len(res.Diff.Adopt) != 1 {
		t.Fatalf("diff: %+v", res.Diff)
	}
	for _, want := range []string{
		"systemd-run --unit=cf-firewall-rollback --on-active=120",
		"ufw allow proto tcp from 192.168.0.0/16 to any port 22 comment cf:ssh",
		"ufw allow proto vrrp from 192.168.103.243 to any comment cf:vrrp (cluster)",
		"ufw default deny incoming",
		"ufw --force enable",
	} {
		if !f.has(want) {
			t.Errorf("geen %q in %v", want, f.calls)
		}
	}
	if f.has("ufw default allow outgoing") {
		t.Errorf("te veel: %v", f.calls)
	}
	// De nep-ufw werkt het commentaar niet bij: dan weg en opnieuw.
	if !f.has("ufw delete allow proto tcp from 192.168.103.243 to any port 2379:2380") {
		t.Errorf("overnemen: %v", f.calls)
	}
	// De timer staat vóór de eerste ufw-regel.
	if i, j := indexOf(f.calls, "systemd-run"), indexOf(f.calls, "ufw allow"); i < 0 || i > j {
		t.Errorf("volgorde: %v", f.calls)
	}
	after, _ := os.ReadFile(filepath.Join(a.Root, ufwAfter))
	if !strings.Contains(string(after), "-A ufw-after-input -m conntrack --ctstate NEW -j NFLOG --nflog-group 4711") ||
		strings.Index(string(after), "NFLOG") > strings.Index(string(after), "COMMIT") {
		t.Errorf("after.rules:\n%s", after)
	}
	script, _ := os.ReadFile(filepath.Join(a.Root, fwScript()))
	if !strings.Contains(string(script), "ufw --force disable") || !strings.Contains(string(script), "cp /var/lib/clusterforge/firewall/backup/user.rules /etc/ufw/user.rules") {
		t.Errorf("script:\n%s", script)
	}
	p := a.fwReadPending()
	if p == nil || p.ApplyID != "a1" || p.WasActive {
		t.Fatalf("pending: %+v", p)
	}
	if _, err := a.firewallApply(ctx, in); err == nil {
		t.Error("tweede apply zonder bevestiging")
	}
	if err := a.firewallConfirm(ctx, "andere"); err == nil {
		t.Error("verkeerd id bevestigd")
	}
	if err := a.firewallConfirm(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if a.fwReadPending() != nil || !f.has("systemctl stop cf-firewall-rollback.timer") {
		t.Error("niet bevestigd")
	}
	if err := a.firewallConfirm(ctx, "a1"); err != nil {
		t.Errorf("nog eens bevestigen: %v", err)
	}
	var applied protocol.FirewallApplied
	if !readJSON(filepath.Join(a.Root, protocol.FirewallStateFile), &applied) || applied.Hash != "h1" {
		t.Errorf("stand: %+v", applied)
	}
}

func indexOf(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func TestFirewallApplyRefusesWithoutSSH(t *testing.T) {
	a, f := fwAgent(t)
	in := protocol.FirewallApply{ID: "a1", Rules: fwRules()[1:], ConfirmSeconds: 120}
	if _, err := a.firewallApply(context.Background(), in); err == nil || !strings.Contains(err.Error(), "SSH") {
		t.Fatalf("fout: %v", err)
	}
	if f.has("ufw allow") || f.has("systemd-run") {
		t.Errorf("toch iets gedaan: %v", f.calls)
	}
}

func TestFirewallApplyFailureRollsBack(t *testing.T) {
	a, f := fwAgent(t)
	f.fail = "ufw --force enable"
	in := protocol.FirewallApply{ID: "a2", Rules: fwRules(), ConfirmSeconds: 60}
	_, err := a.firewallApply(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "vorige regels staan terug") {
		t.Fatalf("fout: %v", err)
	}
	if !f.has("/bin/sh /var/lib/clusterforge/firewall/rollback.sh") || a.fwReadPending() != nil {
		t.Errorf("niet teruggedraaid: %v", f.calls)
	}
}

func TestFirewallApplyNoTimerNoChange(t *testing.T) {
	a, f := fwAgent(t)
	f.fail = "systemd-run"
	_, err := a.firewallApply(context.Background(), protocol.FirewallApply{ID: "a3", Rules: fwRules(), ConfirmSeconds: 60})
	if err == nil || !strings.Contains(err.Error(), "niets veranderd") || f.has("ufw allow") {
		t.Fatalf("fout: %v, %v", err, f.calls)
	}
}

func TestFirewallApplyServerCheck(t *testing.T) {
	a, f := fwAgent(t)
	a.Config = &Config{NatsURL: "tls://192.168.113.153:4222"}
	var dialed string
	a.Dial = func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = addr
		return nil, errors.New("i/o timeout")
	}
	_, err := a.firewallApply(context.Background(), protocol.FirewallApply{ID: "a4", Rules: fwRules(), ConfirmSeconds: 60})
	if err == nil || dialed != "192.168.113.153:4222" || !f.has("/bin/sh") {
		t.Fatalf("fout: %v, %q", err, dialed)
	}
}

func TestFirewallResume(t *testing.T) {
	a, f := fwAgent(t)
	if err := writeJSON(filepath.Join(a.Root, fwPending()), protocol.FirewallPending{ApplyID: "a5", Deadline: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	a.firewallResume(context.Background())
	if !f.has("/bin/sh /var/lib/clusterforge/firewall/rollback.sh niet bevestigd voor een herstart") {
		t.Errorf("calls: %v", f.calls)
	}
}

func TestWithMonitorBlock(t *testing.T) {
	on := withMonitorBlock(fwAfterRules, "ufw-after-input", true)
	if withMonitorBlock(on, "ufw-after-input", true) != on {
		t.Error("niet idempotent")
	}
	if withMonitorBlock(on, "ufw-after-input", false) != fwAfterRules {
		t.Errorf("weghalen:\n%s", withMonitorBlock(on, "ufw-after-input", false))
	}
}

func TestParsePacket(t *testing.T) {
	v4 := []byte{0x45, 0, 0, 40, 0, 0, 0, 0, 64, 6, 0, 0, 192, 168, 90, 12, 192, 168, 103, 243, 0xc3, 0x50, 0x09, 0x51}
	src, proto, port, ok := parsePacket(v4)
	if !ok || src != "192.168.90.12" || proto != "tcp" || port != 2385 {
		t.Errorf("v4: %s %s %d", src, proto, port)
	}
	vrrp := append([]byte{}, v4...)
	vrrp[9] = 112
	if _, proto, port, _ := parsePacket(vrrp); proto != "vrrp" || port != 0 {
		t.Errorf("vrrp: %s %d", proto, port)
	}
	v6 := make([]byte, 44)
	v6[0], v6[6] = 0x60, 17
	copy(v6[8:24], net.ParseIP("fd00::5"))
	v6[42], v6[43] = 0x00, 0x35
	if src, proto, port, ok := parsePacket(v6); !ok || src != "fd00::5" || proto != "udp" || port != 53 {
		t.Errorf("v6: %s %s %d", src, proto, port)
	}
}

func TestFirewallBlocked(t *testing.T) {
	a, _ := fwAgent(t)
	at := time.Date(2026, 11, 15, 22, 0, 0, 0, time.UTC)
	for range 3 {
		a.fw.add("192.168.103.241", "tcp", 2379, at)
	}
	a.fw.add("203.0.113.9", "tcp", 3306, at)
	b := a.fwTake(context.Background())
	if b.Source != "nflog" || len(b.Hits) != 2 || b.Hits[0].Count != 3 || b.Hits[0].Port != 2379 {
		t.Fatalf("hits: %+v", b)
	}
	if len(a.fwTake(context.Background()).Hits) != 0 {
		t.Error("tellers niet op nul")
	}
	out := []byte("1763244001.123456 node kernel: [UFW BLOCK] IN=eth0 OUT= MAC=aa SRC=192.168.103.241 DST=192.168.103.243 LEN=60 PROTO=TCP SPT=40000 DPT=8008 WINDOW=64240\n" +
		"1763244002.000000 node kernel: something else\n")
	n := 0
	last := parseUfwBlocks(out, func(src, proto string, port int, _ time.Time) {
		n++
		if src != "192.168.103.241" || proto != "tcp" || port != 8008 {
			t.Errorf("%s %s %d", src, proto, port)
		}
	})
	if n != 1 || last.Unix() != 1763244001 {
		t.Errorf("n=%d last=%v", n, last)
	}
}

func TestHandleFirewallProbe(t *testing.T) {
	a, _ := fwAgent(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	body, _ := json.Marshal(protocol.FirewallRequest{Op: protocol.FirewallOpProbe, Probe: []string{ln.Addr().String()}})
	data, _ := json.Marshal(protocol.Envelope{Type: protocol.TypeFirewallRequest, Body: body})
	res := a.handleFirewall(data)
	if res.Error != "" || len(res.Probe) != 1 || !res.Probe[0].OK {
		t.Fatalf("probe: %+v", res)
	}
	body, _ = json.Marshal(protocol.FirewallRequest{Op: protocol.FirewallOpProbe, Probe: []string{"host;rm:22"}})
	data, _ = json.Marshal(protocol.Envelope{Type: protocol.TypeFirewallRequest, Body: body})
	if a.handleFirewall(data).Error == "" {
		t.Error("ongeldig doel aanvaard")
	}
}
