package protocol

import "time"

// Beveiliging (fase 27): de agent vertelt wat er op zijn node staat, zodat
// de server bekende kwetsbaarheden kan vaststellen. De agent leest alleen;
// hij installeert, scant of verandert in dit onderwerp niets. Het matchen
// met de Debian Security Tracker en CISA KEV gebeurt op de server.

// SecuritySince is de eerste protocolversie met het subject security.
const SecuritySince = 40

// SecurityAuditSince is de eerste protocolversie die de verharding van de
// node uitleest (verzoek audit).
const SecurityAuditSince = 41

// SubjectSecurity draagt de beveiligingsverzoeken: cf.node.<id>.security.
const SubjectSecurity = "security"

const (
	TypeSecurityRequest = "security_request"
	TypeSecurityResult  = "security_result"
)

// Soorten beveiligingsverzoek.
const (
	// SecurityOpInventory geeft alle geïnstalleerde Debian-pakketten met
	// versie en bronpakket.
	SecurityOpInventory = "inventory"
	// SecurityOpAudit leest de instellingen die de server tegen de
	// verhardingsregels legt: sshd -T, sysctl, accounts, rechten op
	// gevoelige bestanden, mountopties, diensten en het Lynis-rapport. De
	// agent oordeelt niet; de regels staan op de server.
	SecurityOpAudit = "audit"
)

// SecurityRequestTimeout is hoe lang een beveiligingsverzoek mag duren.
const SecurityRequestTimeout = 90 * time.Second

// MaxSecurityPackages begrenst de pakketlijst (een volle desktop heeft er
// zo'n 3000).
const MaxSecurityPackages = 20000

type SecurityRequest struct {
	ID       string    `json:"id"`
	Op       string    `json:"op"`
	Deadline time.Time `json:"deadline"`
}

type SecurityResponse struct {
	Error     string             `json:"error,omitempty"`
	Inventory *SecurityInventory `json:"inventory,omitempty"`
	Audit     *SecurityAudit     `json:"audit,omitempty"`
}

// SecurityAudit is wat de agent over de verharding van zijn node ziet.
// Een leeg veld betekent: niet gevonden of niet te lezen; Errors zegt
// waarom.
type SecurityAudit struct {
	// SSHD is de uitvoer van sshd -T (sleutel in kleine letters); nil als
	// sshd er niet is.
	SSHD map[string]string `json:"sshd,omitempty"`
	// Sysctl zijn de waarden uit /proc/sys van SysctlKeys.
	Sysctl map[string]string `json:"sysctl"`
	// UID0 zijn de accounts met uid 0.
	UID0 []string `json:"uid0"`
	// EmptyPasswords zijn accounts met een leeg wachtwoordveld in
	// /etc/shadow (alleen de namen; nooit een hash).
	EmptyPasswords []string `json:"empty_passwords"`
	// LoginUsers zijn accounts met een login-shell.
	LoginUsers []string `json:"login_users"`
	// SudoNopasswd zijn de sudoers-bestanden met NOPASSWD.
	SudoNopasswd []string `json:"sudo_nopasswd"`
	// Files zijn de rechten van gevoelige bestanden (octaal, eigenaar).
	Files map[string]FileMode `json:"files"`
	// WorldWritable zijn bestanden onder /etc die iedereen mag schrijven
	// (hoogstens MaxAuditList).
	WorldWritable []string `json:"world_writable"`
	// SUID zijn programma's met het setuid-bit onder /usr, /bin, /sbin en
	// /opt (hoogstens MaxAuditList).
	SUID []string `json:"suid"`
	// Mounts zijn de opties van /tmp, /var/tmp en /dev/shm; ontbreekt een
	// sleutel, dan is het geen eigen mount.
	Mounts map[string]string `json:"mounts"`
	// Services is per dienst uit AuditServices of hij actief is.
	Services map[string]string `json:"services"`
	// AutoUpgrades is de inhoud van APT::Periodic::Unattended-Upgrade ("1"
	// is aan); leeg als niet ingesteld.
	AutoUpgrades string `json:"auto_upgrades"`
	// AppArmor is Y als AppArmor in de kernel aanstaat.
	AppArmor string `json:"apparmor"`
	// Lynis is het laatste Lynis-rapport, als het er is.
	Lynis  *LynisReport `json:"lynis,omitempty"`
	Errors []string     `json:"errors,omitempty"`
}

type FileMode struct {
	Mode  string `json:"mode"`
	Owner string `json:"owner"`
	Group string `json:"group"`
}

// LynisReport komt uit /var/log/lynis-report.dat.
type LynisReport struct {
	Date         string   `json:"date"`
	HardeningIdx int      `json:"hardening_index"`
	Warnings     []string `json:"warnings"`
	Suggestions  []string `json:"suggestions"`
	TestsDone    int      `json:"tests_done"`
	LynisVersion string   `json:"lynis_version"`
}

// MaxAuditList begrenst de lijsten van audit.
const MaxAuditList = 200

// SysctlKeys zijn de kernelinstellingen die de audit leest.
var SysctlKeys = []string{
	"net.ipv4.ip_forward",
	"net.ipv4.conf.all.accept_redirects",
	"net.ipv4.conf.default.accept_redirects",
	"net.ipv4.conf.all.send_redirects",
	"net.ipv4.conf.all.accept_source_route",
	"net.ipv4.conf.all.rp_filter",
	"net.ipv4.conf.all.log_martians",
	"net.ipv4.tcp_syncookies",
	"net.ipv4.icmp_echo_ignore_broadcasts",
	"net.ipv6.conf.all.accept_redirects",
	"net.ipv6.conf.all.accept_source_route",
	"kernel.randomize_va_space",
	"kernel.kptr_restrict",
	"kernel.dmesg_restrict",
	"kernel.yama.ptrace_scope",
	"kernel.unprivileged_bpf_disabled",
	"fs.protected_hardlinks",
	"fs.protected_symlinks",
	"fs.suid_dumpable",
}

// AuditFiles zijn de bestanden waarvan de audit de rechten leest.
var AuditFiles = []string{"/etc/shadow", "/etc/gshadow", "/etc/passwd", "/etc/group", "/etc/ssh/sshd_config", "/etc/sudoers", "/etc/crontab", "/boot/grub/grub.cfg"}

// AuditServices zijn de diensten waarvan de audit de stand leest.
var AuditServices = []string{"auditd", "systemd-timesyncd", "chrony", "ntpsec", "apparmor", "unattended-upgrades", "fail2ban", "crowdsec", "telnet.socket", "rsh.socket", "avahi-daemon", "cups", "rpcbind", "nfs-server", "smbd", "vsftpd"}

// SecurityInventory is de pakketlijst van een node.
type SecurityInventory struct {
	OS     OSInfo `json:"os"`
	Kernel string `json:"kernel"`
	// Dpkg is false op een node zonder dpkg; Packages is dan leeg.
	Dpkg     bool         `json:"dpkg"`
	Packages []DebPackage `json:"packages"`
	// Truncated is true als er meer dan MaxSecurityPackages waren.
	Truncated bool `json:"truncated,omitempty"`
}

// DebPackage is één geïnstalleerd pakket. Source en SourceVersion zijn die
// van het bronpakket; de Security Tracker werkt met bronpakketten.
type DebPackage struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Arch          string `json:"arch,omitempty"`
	Source        string `json:"source"`
	SourceVersion string `json:"source_version"`
}
