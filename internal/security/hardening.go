package security

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Verharding (M2): vaste regels op wat de agent uitleest. De regels staan
// hier en niet in de agent, zodat een betere regel geen agentupdate vraagt.

// KindHardening is de soort bevinding van een verhardingsregel.
const KindHardening = "hardening"

// Uitkomst van een regel.
const (
	ResultPass = "ok"
	ResultFail = "fout"
	ResultSkip = "nvt"
)

// HardeningContext is wat een regel naast de audit mag weten.
type HardeningContext struct {
	Audit *protocol.SecurityAudit
	// LXC: de node is een container; kernelinstellingen, tijd en AppArmor
	// regelt de Proxmox-host.
	LXC bool
	// Docker: er draait Docker (die zet ip_forward aan).
	Docker bool
	// Router: keepalived of k3s; doorsturen kan nodig zijn.
	Router bool
}

// Rule is één verhardingsregel.
type Rule struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Fix      string `json:"fix"`
	// check geeft de uitkomst, wat er staat en wat er hoort te staan.
	check func(h HardeningContext) (result, value, expected string)
}

// RuleResult is de uitkomst van één regel op één node.
type RuleResult struct {
	ID       string `json:"id"`
	Result   string `json:"result"`
	Value    string `json:"value"`
	Expected string `json:"expected"`
}

const sshdDropIn = "Zet het in /etc/ssh/sshd_config.d/50-clusterforge.conf en controleer met `sshd -t && systemctl reload ssh`."

func sshdRule(key string, ok func(v string) bool, expected string) func(h HardeningContext) (string, string, string) {
	return func(h HardeningContext) (string, string, string) {
		if h.Audit.SSHD == nil {
			return ResultSkip, "geen sshd", expected
		}
		v := h.Audit.SSHD[key]
		if ok(v) {
			return ResultPass, v, expected
		}
		return ResultFail, v, expected
	}
}

// sysctlRule: ok als de waarde in want staat. kernel: geregeld door de
// host in een LXC-container.
func sysctlRule(key string, kernel bool, want ...string) func(h HardeningContext) (string, string, string) {
	exp := strings.Join(want, " of ")
	return func(h HardeningContext) (string, string, string) {
		if kernel && h.LXC {
			return ResultSkip, "geregeld door de Proxmox-host", exp
		}
		v, ok := h.Audit.Sysctl[key]
		if !ok {
			return ResultSkip, "niet te lezen", exp
		}
		if slices.Contains(want, v) {
			return ResultPass, v, exp
		}
		return ResultFail, v, exp
	}
}

func sysctlFix(key, val string) string {
	return fmt.Sprintf("Zet `%s = %s` in /etc/sysctl.d/90-clusterforge.conf en laad met `sysctl --system`.", key, val)
}

func listRule(get func(a *protocol.SecurityAudit) []string, allowed []string, expected string) func(h HardeningContext) (string, string, string) {
	return func(h HardeningContext) (string, string, string) {
		var bad []string
		for _, x := range get(h.Audit) {
			if !slices.Contains(allowed, x) {
				bad = append(bad, x)
			}
		}
		if len(bad) == 0 {
			return ResultPass, "geen", expected
		}
		return ResultFail, joinMax(bad, 8), expected
	}
}

func joinMax(list []string, n int) string {
	if len(list) <= n {
		return strings.Join(list, ", ")
	}
	return strings.Join(list[:n], ", ") + fmt.Sprintf(" en %d meer", len(list)-n)
}

// modeRule: het bestand mag niet meer rechten hebben dan max.
func modeRule(path string, max uint64) func(h HardeningContext) (string, string, string) {
	exp := fmt.Sprintf("%04o of strenger", max)
	return func(h HardeningContext) (string, string, string) {
		m, ok := h.Audit.Files[path]
		if !ok {
			return ResultSkip, "bestaat niet", exp
		}
		v, err := strconv.ParseUint(m.Mode, 8, 32)
		if err != nil {
			return ResultSkip, m.Mode, exp
		}
		val := m.Mode + " " + m.Owner + ":" + m.Group
		if v&^max != 0 {
			return ResultFail, val, exp
		}
		return ResultPass, val, exp
	}
}

func mountRule(path string, opts ...string) func(h HardeningContext) (string, string, string) {
	exp := strings.Join(opts, ",")
	return func(h HardeningContext) (string, string, string) {
		o, ok := h.Audit.Mounts[path]
		if !ok {
			return ResultSkip, "geen eigen mount", exp
		}
		have := strings.Split(o, ",")
		var miss []string
		for _, x := range opts {
			if !slices.Contains(have, x) {
				miss = append(miss, x)
			}
		}
		if len(miss) > 0 {
			return ResultFail, "ontbreekt: " + strings.Join(miss, ","), exp
		}
		return ResultPass, exp, exp
	}
}

func serviceOff(unit string) func(h HardeningContext) (string, string, string) {
	return func(h HardeningContext) (string, string, string) {
		st := h.Audit.Services[unit]
		if st == "active" {
			return ResultFail, "actief", "uit"
		}
		return ResultPass, orDash(st), "uit"
	}
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func weakParts(s string, parts ...string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		for _, p := range parts {
			if strings.Contains(x, p) {
				out = append(out, x)
				break
			}
		}
	}
	return out
}

// knownSUID zijn de setuid-programma's van een gewone Debian-installatie.
var knownSUID = []string{"sudo", "su", "passwd", "chsh", "chfn", "gpasswd", "newgrp", "mount", "umount", "ping", "ping6", "pkexec",
	"fusermount", "fusermount3", "ssh-keysign", "dbus-daemon-launch-helper", "Xorg.wrap", "polkit-agent-helper-1", "ntfs-3g",
	"exim4", "at", "crontab", "newuidmap", "newgidmap", "chrome-sandbox", "traceroute6.iputils", "mount.nfs", "mount.cifs",
	"lxc-user-nic", "userhelper", "staprun", "kismet_cap_linux_wifi", "doas", "unix_chkpwd", "pam_timestamp_check", "sg"}

// Rules zijn alle verhardingsregels, op volgorde van categorie.
var Rules = []Rule{
	{ID: "ssh-root-login", Category: "SSH", Title: "root mag inloggen met een wachtwoord", Severity: SevHigh,
		Fix:   "Zet `PermitRootLogin prohibit-password` (alleen met sleutel) of `no`. " + sshdDropIn,
		check: sshdRule("permitrootlogin", func(v string) bool { return v != "yes" }, "prohibit-password of no")},
	{ID: "ssh-empty-passwords", Category: "SSH", Title: "SSH laat lege wachtwoorden toe", Severity: SevCritical,
		Fix:   "Zet `PermitEmptyPasswords no`. " + sshdDropIn,
		check: sshdRule("permitemptypasswords", func(v string) bool { return v == "no" }, "no")},
	{ID: "ssh-password-auth", Category: "SSH", Title: "SSH aanvaardt wachtwoorden", Severity: SevMedium,
		Fix:   "Log in met een sleutel en zet daarna `PasswordAuthentication no` en `KbdInteractiveAuthentication no`. Controleer eerst dat je sleutel werkt! " + sshdDropIn,
		check: sshdRule("passwordauthentication", func(v string) bool { return v == "no" }, "no")},
	{ID: "ssh-max-auth-tries", Category: "SSH", Title: "SSH laat veel inlogpogingen per verbinding toe", Severity: SevLow,
		Fix:   "Zet `MaxAuthTries 3`. " + sshdDropIn,
		check: sshdRule("maxauthtries", func(v string) bool { n, err := strconv.Atoi(v); return err == nil && n <= 4 }, "4 of minder")},
	{ID: "ssh-x11", Category: "SSH", Title: "X11-doorsturen staat aan", Severity: SevLow,
		Fix:   "Zet `X11Forwarding no`. " + sshdDropIn,
		check: sshdRule("x11forwarding", func(v string) bool { return v != "yes" }, "no")},
	{ID: "ssh-weak-crypto", Category: "SSH", Title: "SSH biedt zwakke versleuteling aan", Severity: SevMedium,
		Fix: "Laat CBC-ciphers, 3DES/arcfour, MD5/SHA1-MAC's en diffie-hellman-group1/group14-sha1 weg met `Ciphers`, `MACs` en `KexAlgorithms`. " + sshdDropIn,
		check: func(h HardeningContext) (string, string, string) {
			if h.Audit.SSHD == nil {
				return ResultSkip, "geen sshd", "geen"
			}
			var weak []string
			weak = append(weak, weakParts(h.Audit.SSHD["ciphers"], "cbc", "3des", "arcfour", "blowfish")...)
			weak = append(weak, weakParts(h.Audit.SSHD["macs"], "md5", "hmac-sha1", "umac-64")...)
			weak = append(weak, weakParts(h.Audit.SSHD["kexalgorithms"], "group1-", "group14-sha1", "group-exchange-sha1")...)
			if len(weak) > 0 {
				return ResultFail, joinMax(weak, 5), "geen"
			}
			return ResultPass, "geen", "geen"
		}},

	{ID: "acc-uid0", Category: "Accounts", Title: "Een ander account dan root heeft uid 0", Severity: SevCritical,
		Fix:   "Geef dat account een eigen uid of verwijder het (`usermod -u` of `userdel`). Uid 0 is root.",
		check: listRule(func(a *protocol.SecurityAudit) []string { return a.UID0 }, []string{"root"}, "alleen root")},
	{ID: "acc-empty-password", Category: "Accounts", Title: "Account zonder wachtwoord", Severity: SevCritical,
		Fix:   "Vergrendel het account met `passwd -l <naam>` of geef het een wachtwoord.",
		check: listRule(func(a *protocol.SecurityAudit) []string { return a.EmptyPasswords }, nil, "geen")},
	{ID: "acc-sudo-nopasswd", Category: "Accounts", Title: "sudo zonder wachtwoord", Severity: SevMedium,
		Fix:   "Haal NOPASSWD weg uit het genoemde sudoers-bestand (bewerken met `visudo -f`), tenzij een automatisering het echt nodig heeft; beperk het dan tot dat ene commando.",
		check: listRule(func(a *protocol.SecurityAudit) []string { return a.SudoNopasswd }, nil, "geen")},

	{ID: "file-shadow", Category: "Bestanden", Title: "/etc/shadow is voor te veel mensen leesbaar", Severity: SevHigh,
		Fix: "`chown root:shadow /etc/shadow && chmod 640 /etc/shadow`", check: modeRule("/etc/shadow", 0o640)},
	{ID: "file-gshadow", Category: "Bestanden", Title: "/etc/gshadow is voor te veel mensen leesbaar", Severity: SevMedium,
		Fix: "`chown root:shadow /etc/gshadow && chmod 640 /etc/gshadow`", check: modeRule("/etc/gshadow", 0o640)},
	{ID: "file-passwd", Category: "Bestanden", Title: "/etc/passwd is schrijfbaar voor anderen", Severity: SevCritical,
		Fix: "`chown root:root /etc/passwd && chmod 644 /etc/passwd`", check: modeRule("/etc/passwd", 0o644)},
	{ID: "file-sshd-config", Category: "Bestanden", Title: "sshd_config is schrijfbaar voor anderen", Severity: SevMedium,
		Fix: "`chown root:root /etc/ssh/sshd_config && chmod 600 /etc/ssh/sshd_config`", check: modeRule("/etc/ssh/sshd_config", 0o644)},
	{ID: "file-sudoers", Category: "Bestanden", Title: "/etc/sudoers heeft te ruime rechten", Severity: SevMedium,
		Fix: "`chmod 440 /etc/sudoers`", check: modeRule("/etc/sudoers", 0o440)},
	{ID: "file-world-writable", Category: "Bestanden", Title: "Bestanden in /etc die iedereen mag aanpassen", Severity: SevHigh,
		Fix:   "Haal het schrijfrecht voor iedereen weg: `chmod o-w <bestand>`.",
		check: listRule(func(a *protocol.SecurityAudit) []string { return a.WorldWritable }, nil, "geen")},
	{ID: "file-suid", Category: "Bestanden", Title: "Ongewone setuid-programma's", Severity: SevMedium,
		Fix: "Kijk na of het programma setuid moet zijn. Zo niet: `chmod u-s <pad>`; staat het er onverwacht, onderzoek dan de node.",
		check: func(h HardeningContext) (string, string, string) {
			var odd []string
			for _, p := range h.Audit.SUID {
				name := p[strings.LastIndexByte(p, '/')+1:]
				if !slices.Contains(knownSUID, name) && !strings.HasPrefix(p, "/usr/lib/") && !strings.HasPrefix(p, "/usr/libexec/") {
					odd = append(odd, p)
				}
			}
			if len(odd) > 0 {
				return ResultFail, joinMax(odd, 6), "alleen de gewone"
			}
			return ResultPass, fmt.Sprintf("%d gewone", len(h.Audit.SUID)), "alleen de gewone"
		}},

	{ID: "net-redirects", Category: "Netwerk", Title: "ICMP-redirects worden aanvaard", Severity: SevMedium,
		Fix:   sysctlFix("net.ipv4.conf.all.accept_redirects", "0") + " Doe hetzelfde voor default en voor IPv6.",
		check: sysctlRule("net.ipv4.conf.all.accept_redirects", false, "0")},
	{ID: "net-redirects6", Category: "Netwerk", Title: "IPv6-redirects worden aanvaard", Severity: SevLow,
		Fix: sysctlFix("net.ipv6.conf.all.accept_redirects", "0"), check: sysctlRule("net.ipv6.conf.all.accept_redirects", false, "0")},
	{ID: "net-source-route", Category: "Netwerk", Title: "Source routing wordt aanvaard", Severity: SevMedium,
		Fix: sysctlFix("net.ipv4.conf.all.accept_source_route", "0"), check: sysctlRule("net.ipv4.conf.all.accept_source_route", false, "0")},
	{ID: "net-send-redirects", Category: "Netwerk", Title: "De node stuurt ICMP-redirects", Severity: SevLow,
		Fix: sysctlFix("net.ipv4.conf.all.send_redirects", "0"),
		check: func(h HardeningContext) (string, string, string) {
			if h.Router || h.Docker {
				return ResultSkip, "router of Docker", "0"
			}
			return sysctlRule("net.ipv4.conf.all.send_redirects", false, "0")(h)
		}},
	{ID: "net-forward", Category: "Netwerk", Title: "IP-doorsturen staat aan zonder reden", Severity: SevLow,
		Fix: sysctlFix("net.ipv4.ip_forward", "0") + " Niet doen op een node met Docker, k3s of een VPN.",
		check: func(h HardeningContext) (string, string, string) {
			if h.Router || h.Docker {
				return ResultSkip, "nodig voor Docker/k3s/keepalived", "0"
			}
			return sysctlRule("net.ipv4.ip_forward", false, "0")(h)
		}},
	{ID: "net-rp-filter", Category: "Netwerk", Title: "Geen controle op vervalste afzenders (rp_filter)", Severity: SevLow,
		Fix: sysctlFix("net.ipv4.conf.all.rp_filter", "2"), check: sysctlRule("net.ipv4.conf.all.rp_filter", false, "1", "2")},
	{ID: "net-syncookies", Category: "Netwerk", Title: "SYN-cookies staan uit", Severity: SevLow,
		Fix: sysctlFix("net.ipv4.tcp_syncookies", "1"), check: sysctlRule("net.ipv4.tcp_syncookies", false, "1")},
	{ID: "net-martians", Category: "Netwerk", Title: "Vreemde pakketten worden niet gelogd", Severity: SevInfo,
		Fix: sysctlFix("net.ipv4.conf.all.log_martians", "1"), check: sysctlRule("net.ipv4.conf.all.log_martians", false, "1")},
	{ID: "net-icmp-broadcast", Category: "Netwerk", Title: "Antwoordt op ping naar een broadcastadres", Severity: SevLow,
		Fix: sysctlFix("net.ipv4.icmp_echo_ignore_broadcasts", "1"), check: sysctlRule("net.ipv4.icmp_echo_ignore_broadcasts", false, "1")},

	{ID: "kern-aslr", Category: "Kernel", Title: "Adresrandomisatie (ASLR) staat niet volledig aan", Severity: SevHigh,
		Fix: sysctlFix("kernel.randomize_va_space", "2"), check: sysctlRule("kernel.randomize_va_space", true, "2")},
	{ID: "kern-bpf", Category: "Kernel", Title: "Gewone gebruikers mogen BPF-programma's laden", Severity: SevMedium,
		Fix:   sysctlFix("kernel.unprivileged_bpf_disabled", "1") + " Op een Proxmox-host geldt dit voor alle containers.",
		check: sysctlRule("kernel.unprivileged_bpf_disabled", true, "1", "2")},
	{ID: "kern-links", Category: "Kernel", Title: "Bescherming van harde en symbolische links staat uit", Severity: SevMedium,
		Fix: sysctlFix("fs.protected_symlinks", "1") + " en `fs.protected_hardlinks = 1`.",
		check: func(h HardeningContext) (string, string, string) {
			r, v, e := sysctlRule("fs.protected_symlinks", true, "1")(h)
			if r != ResultPass {
				return r, "symlinks " + v, e
			}
			r, v, e = sysctlRule("fs.protected_hardlinks", true, "1")(h)
			if r != ResultPass {
				return r, "hardlinks " + v, e
			}
			return ResultPass, "1", "1"
		}},
	{ID: "kern-kptr", Category: "Kernel", Title: "Kerneladressen zijn zichtbaar", Severity: SevLow,
		Fix: sysctlFix("kernel.kptr_restrict", "1"), check: sysctlRule("kernel.kptr_restrict", true, "1", "2")},
	{ID: "kern-dmesg", Category: "Kernel", Title: "Iedereen mag dmesg lezen", Severity: SevLow,
		Fix: sysctlFix("kernel.dmesg_restrict", "1"), check: sysctlRule("kernel.dmesg_restrict", true, "1")},
	{ID: "kern-ptrace", Category: "Kernel", Title: "Processen mogen elkaar vrij debuggen (ptrace)", Severity: SevLow,
		Fix: sysctlFix("kernel.yama.ptrace_scope", "1"), check: sysctlRule("kernel.yama.ptrace_scope", true, "1", "2", "3")},
	{ID: "kern-suid-dump", Category: "Kernel", Title: "Setuid-programma's mogen een core dump schrijven", Severity: SevLow,
		Fix: sysctlFix("fs.suid_dumpable", "0"), check: sysctlRule("fs.suid_dumpable", true, "0")},
	{ID: "kern-apparmor", Category: "Kernel", Title: "AppArmor staat uit", Severity: SevMedium,
		Fix: "Installeer en activeer AppArmor: `apt install apparmor apparmor-utils` en herstart; Debian zet het standaard aan.",
		check: func(h HardeningContext) (string, string, string) {
			if h.LXC {
				return ResultSkip, "geregeld door de Proxmox-host", "Y"
			}
			if h.Audit.AppArmor == "Y" {
				return ResultPass, "Y", "Y"
			}
			return ResultFail, orDash(h.Audit.AppArmor), "Y"
		}},

	{ID: "mnt-tmp", Category: "Opslag", Title: "/tmp zonder nodev en nosuid", Severity: SevLow,
		Fix: "Mount /tmp met `nodev,nosuid` (tmpfs: `systemctl edit tmp.mount` of /etc/fstab).", check: mountRule("/tmp", "nodev", "nosuid")},
	{ID: "mnt-shm", Category: "Opslag", Title: "/dev/shm zonder nodev, nosuid en noexec", Severity: SevLow,
		Fix:   "Voeg in /etc/fstab toe: `tmpfs /dev/shm tmpfs defaults,nodev,nosuid,noexec 0 0` en `mount -o remount /dev/shm`.",
		check: mountRule("/dev/shm", "nodev", "nosuid", "noexec")},

	{ID: "svc-timesync", Category: "Diensten", Title: "Geen tijdsynchronisatie", Severity: SevMedium,
		Fix: "`apt install systemd-timesyncd` (of chrony). Een juiste klok is nodig voor logs, TLS en TOTP.",
		check: func(h HardeningContext) (string, string, string) {
			if h.LXC {
				return ResultSkip, "de klok komt van de Proxmox-host", "actief"
			}
			for _, s := range []string{"systemd-timesyncd", "chrony", "ntpsec"} {
				if h.Audit.Services[s] == "active" {
					return ResultPass, s, "actief"
				}
			}
			return ResultFail, "geen", "actief"
		}},
	{ID: "svc-auditd", Category: "Diensten", Title: "auditd draait niet", Severity: SevLow,
		Fix: "Komt in fase 27 M7 via de GUI; met de hand: `apt install auditd`.",
		check: func(h HardeningContext) (string, string, string) {
			if h.LXC {
				return ResultSkip, "kan niet in een container", "actief"
			}
			if h.Audit.Services["auditd"] == "active" {
				return ResultPass, "actief", "actief"
			}
			return ResultFail, orDash(h.Audit.Services["auditd"]), "actief"
		}},
	{ID: "svc-auto-upgrades", Category: "Diensten", Title: "Beveiligingsupdates worden niet vanzelf geïnstalleerd", Severity: SevLow,
		Fix: "Laat ClusterForge bijwerken (Updates → automatisch, clusterbewust) of `apt install unattended-upgrades && dpkg-reconfigure -plow unattended-upgrades`.",
		check: func(h HardeningContext) (string, string, string) {
			if h.Audit.AutoUpgrades == "1" {
				return ResultPass, "aan", "aan"
			}
			return ResultFail, "uit", "aan"
		}},
	{ID: "svc-telnet", Category: "Diensten", Title: "telnet of rsh draait", Severity: SevHigh,
		Fix: "`systemctl disable --now telnet.socket rsh.socket` en verwijder de pakketten; gebruik SSH.",
		check: func(h HardeningContext) (string, string, string) {
			for _, u := range []string{"telnet.socket", "rsh.socket"} {
				if h.Audit.Services[u] == "active" {
					return ResultFail, u, "uit"
				}
			}
			return ResultPass, "uit", "uit"
		}},
	{ID: "svc-ftp", Category: "Diensten", Title: "FTP-server zonder versleuteling (vsftpd)", Severity: SevMedium,
		Fix: "Gebruik SFTP via SSH en zet vsftpd uit: `systemctl disable --now vsftpd`.", check: serviceOff("vsftpd")},
	{ID: "svc-extra", Category: "Diensten", Title: "Diensten die een server zelden nodig heeft", Severity: SevLow,
		Fix: "Zet uit wat je niet gebruikt: `systemctl disable --now avahi-daemon cups rpcbind`.",
		check: func(h HardeningContext) (string, string, string) {
			var on []string
			for _, u := range []string{"avahi-daemon", "cups", "rpcbind"} {
				if h.Audit.Services[u] == "active" {
					on = append(on, u)
				}
			}
			if len(on) > 0 {
				return ResultFail, strings.Join(on, ", "), "uit"
			}
			return ResultPass, "uit", "uit"
		}},
	{ID: "svc-fail2ban", Category: "Diensten", Title: "fail2ban draait naast CrowdSec", Severity: SevLow,
		Fix: "CrowdSec doet dit werk al; twee banners geven dubbele bans. Zet fail2ban uit (`systemctl disable --now fail2ban`) en beheer CrowdSec via ClusterForge.",
		check: func(h HardeningContext) (string, string, string) {
			if h.Audit.Services["fail2ban"] == "active" && h.Audit.Services["crowdsec"] == "active" {
				return ResultFail, "beide actief", "één van de twee"
			}
			return ResultPass, orDash(h.Audit.Services["fail2ban"]), "één van de twee"
		}},
	{ID: "lynis-warnings", Category: "Lynis", Title: "Lynis meldt waarschuwingen", Severity: SevMedium,
		Fix: "Lees de waarschuwingen hieronder; elke regel begint met de Lynis-test (zie `lynis show details <test>`).",
		check: func(h HardeningContext) (string, string, string) {
			l := h.Audit.Lynis
			if l == nil {
				return ResultSkip, "geen Lynis-rapport", "geen waarschuwingen"
			}
			if len(l.Warnings) > 0 {
				return ResultFail, joinMax(l.Warnings, 3), "geen waarschuwingen"
			}
			return ResultPass, fmt.Sprintf("index %d", l.HardeningIdx), "geen waarschuwingen"
		}},
}

var weights = map[string]int{SevCritical: 10, SevHigh: 6, SevMedium: 3, SevLow: 1, SevInfo: 0}

// EvaluateHardening legt de regels op een audit. De score is het gewogen
// aandeel geslaagde regels (kritiek 10, hoog 6, middel 3, laag 1).
func EvaluateHardening(h HardeningContext) (results []RuleResult, drafts []Draft, score int) {
	total, passed := 0, 0
	for _, r := range Rules {
		res, val, exp := r.check(h)
		results = append(results, RuleResult{ID: r.ID, Result: res, Value: val, Expected: exp})
		if res == ResultSkip {
			continue
		}
		w := weights[r.Severity]
		total += w
		if res == ResultPass {
			passed += w
			continue
		}
		summary := fmt.Sprintf("Nu: %s. Verwacht: %s.", val, exp)
		detail := map[string]any{"rule": r.ID, "category": r.Category, "value": val, "expected": exp}
		if r.ID == "lynis-warnings" && h.Audit.Lynis != nil {
			detail["lynis"] = h.Audit.Lynis
		}
		drafts = append(drafts, Draft{Kind: KindHardening, Key: r.ID, Severity: r.Severity, Title: r.Category + ": " + r.Title,
			Summary: summary, Fix: r.Fix, Detail: detail})
	}
	score = 100
	if total > 0 {
		score = passed * 100 / total
	}
	return results, drafts, score
}

// HardeningNode is een node op de pagina Verharding.
type HardeningNode struct {
	Node
	Results []RuleResult          `json:"results"`
	Lynis   *protocol.LynisReport `json:"lynis"`
	Note    string                `json:"note"`
}

// HardeningOverview is de pagina Verharding.
type HardeningOverview struct {
	Protocol int             `json:"protocol"`
	Rules    []Rule          `json:"rules"`
	Nodes    []HardeningNode `json:"nodes"`
}
