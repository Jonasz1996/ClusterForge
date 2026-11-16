package security

import (
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func goodAudit() *protocol.SecurityAudit {
	return &protocol.SecurityAudit{
		SSHD: map[string]string{"permitrootlogin": "prohibit-password", "permitemptypasswords": "no", "passwordauthentication": "no",
			"maxauthtries": "3", "x11forwarding": "no", "ciphers": "chacha20-poly1305@openssh.com,aes256-gcm@openssh.com",
			"macs": "hmac-sha2-256-etm@openssh.com", "kexalgorithms": "sntrup761x25519-sha512,curve25519-sha256"},
		Sysctl: map[string]string{"net.ipv4.conf.all.accept_redirects": "0", "net.ipv6.conf.all.accept_redirects": "0",
			"net.ipv4.conf.all.accept_source_route": "0", "net.ipv4.conf.all.send_redirects": "0", "net.ipv4.ip_forward": "0",
			"net.ipv4.conf.all.rp_filter": "2", "net.ipv4.tcp_syncookies": "1", "net.ipv4.conf.all.log_martians": "1",
			"net.ipv4.icmp_echo_ignore_broadcasts": "1", "kernel.randomize_va_space": "2", "kernel.unprivileged_bpf_disabled": "2",
			"fs.protected_symlinks": "1", "fs.protected_hardlinks": "1", "kernel.kptr_restrict": "1", "kernel.dmesg_restrict": "1",
			"kernel.yama.ptrace_scope": "1", "fs.suid_dumpable": "0"},
		UID0: []string{"root"}, Files: map[string]protocol.FileMode{"/etc/shadow": {Mode: "0640"}, "/etc/passwd": {Mode: "0644"}},
		SUID:     []string{"/usr/bin/sudo", "/usr/bin/passwd", "/usr/lib/openssh/ssh-keysign"},
		Mounts:   map[string]string{"/dev/shm": "rw,nosuid,nodev,noexec"},
		Services: map[string]string{"systemd-timesyncd": "active", "auditd": "active"}, AutoUpgrades: "1", AppArmor: "Y",
	}
}

func TestEvaluateHardening(t *testing.T) {
	results, drafts, score := EvaluateHardening(HardeningContext{Audit: goodAudit()})
	if score != 100 || len(drafts) != 0 {
		t.Fatalf("goede node: score %d, bevindingen %+v", score, drafts)
	}
	if len(results) != len(Rules) {
		t.Errorf("%d uitkomsten voor %d regels", len(results), len(Rules))
	}

	a := goodAudit()
	a.SSHD["permitrootlogin"] = "yes"
	a.SSHD["ciphers"] += ",aes128-cbc"
	a.UID0 = append(a.UID0, "toor")
	a.Sysctl["kernel.randomize_va_space"] = "0"
	a.Sysctl["net.ipv4.ip_forward"] = "1"
	a.SUID = append(a.SUID, "/opt/raar/achterdeur")
	a.Lynis = &protocol.LynisReport{HardeningIdx: 60, Warnings: []string{"SSH-7408: x"}}
	_, drafts, score = EvaluateHardening(HardeningContext{Audit: a, LXC: true, Docker: true})
	got := map[string]string{}
	for _, d := range drafts {
		got[d.Key] = d.Severity
	}
	for id, sev := range map[string]string{"ssh-root-login": SevHigh, "ssh-weak-crypto": SevMedium, "acc-uid0": SevCritical, "file-suid": SevMedium, "lynis-warnings": SevMedium} {
		if got[id] != sev {
			t.Errorf("%s = %q, wil %q (alle: %v)", id, got[id], sev, got)
		}
	}
	// In LXC regelt de host ASLR; met Docker is doorsturen nodig.
	if _, ok := got["kern-aslr"]; ok {
		t.Error("ASLR mag in LXC geen bevinding zijn")
	}
	if _, ok := got["net-forward"]; ok {
		t.Error("ip_forward mag met Docker geen bevinding zijn")
	}
	if score >= 80 || score <= 0 {
		t.Errorf("score = %d", score)
	}
}
