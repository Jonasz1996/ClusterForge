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

// SecurityScanSince is de eerste protocolversie die een netwerkscan (nmap)
// binnen de eigen scopes kan draaien (verzoek scan).
const SecurityScanSince = 42

// SecurityProbeSince is de eerste protocolversie die na een scan de
// gevonden diensten aanspreekt (verzoek probe): panelen zonder login en,
// als de beheerder het aanzet, één standaardwachtwoord per dienst.
const SecurityProbeSince = 43

// SecurityFixSince is de eerste protocolversie die veilige oplossingen
// uit FixableRules zelf toepast (verzoek fix, fix_confirm, fix_rollback).
const SecurityFixSince = 44

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
	// SecurityOpScan draait nmap op de doelen van Scan. Alleen een agent
	// met nmap (de scannercontainer of een eigen scanner-VM) kan dit; de
	// agent controleert zelf nog eens dat elk doel binnen Allowed valt.
	SecurityOpScan = "scan"
	// SecurityOpProbe spreekt de diensten uit Probe aan met één anonieme
	// aanvraag (en hoogstens één aanmeldpoging met een standaardwachtwoord
	// als DefaultPasswords aanstaat). Ook hier alleen binnen Allowed.
	SecurityOpProbe = "probe"
	// SecurityOpFix past de oplossingen van Fix.Rules toe. De agent kent
	// zelf wat elke regel schrijft (vaste lijst, geen vrije tekst van de
	// server). SSH-wijzigingen krijgen een terugdraaitimer: zonder
	// fix_confirm zet de node na Fix.RollbackAfter de oude instellingen terug.
	SecurityOpFix = "fix"
	// SecurityOpFixConfirm houdt de SSH-wijziging en stopt de timer.
	SecurityOpFixConfirm = "fix_confirm"
	// SecurityOpFixRollback zet de SSH-wijziging meteen terug.
	SecurityOpFixRollback = "fix_rollback"
)

// FixableRules zijn de verhardingsregels die de agent met één klik kan
// oplossen. Bewust niet: IP-doorsturen (Docker, VPN), mounts, accounts,
// setuid en diensten die iemand nodig kan hebben.
var FixableRules = []string{
	"ssh-root-login", "ssh-empty-passwords", "ssh-password-auth", "ssh-max-auth-tries", "ssh-x11", "ssh-weak-crypto",
	"file-shadow", "file-gshadow", "file-passwd", "file-sshd-config", "file-sudoers",
	"net-redirects", "net-redirects6", "net-source-route", "net-send-redirects", "net-rp-filter", "net-syncookies", "net-martians",
	"net-icmp-broadcast",
	"kern-aslr", "kern-bpf", "kern-links", "kern-kptr", "kern-dmesg", "kern-ptrace", "kern-suid-dump",
	"svc-telnet", "svc-fail2ban",
}

// Grenzen van de SSH-terugdraaitimer.
const (
	FixRollbackMin     = 2 * time.Minute
	FixRollbackDefault = 10 * time.Minute
	FixRollbackMax     = 60 * time.Minute
)

type FixSpec struct {
	Rules []string `json:"rules"`
	// RollbackAfter in seconden; 0 is FixRollbackDefault.
	RollbackAfter int `json:"rollback_after,omitempty"`
}

// FixResult is wat de agent deed.
type FixResult struct {
	Outcomes []FixOutcome `json:"outcomes"`
	// SSHPending: de SSH-instellingen zijn gewijzigd en worden op
	// RollbackAt teruggezet tenzij de beheerder bevestigt.
	SSHPending bool       `json:"ssh_pending"`
	RollbackAt *time.Time `json:"rollback_at,omitempty"`
}

type FixOutcome struct {
	Rule string `json:"rule"`
	OK   bool   `json:"ok"`
	// Changed zegt in mensentaal wat er veranderde.
	Changed []string `json:"changed,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Soorten diensten voor een probe.
const (
	ProbeHTTP      = "http"
	ProbeHTTPS     = "https"
	ProbeRedis     = "redis"
	ProbeMemcached = "memcached"
)

// MaxProbeTargets begrenst een probe.
const MaxProbeTargets = 512

type ProbeSpec struct {
	Allowed []string      `json:"allowed"`
	Targets []ProbeTarget `json:"targets"`
	// DefaultPasswords: per dienst één aanmeldpoging met het bekende
	// standaardwachtwoord (opt-in; nooit meer dan één poging).
	DefaultPasswords bool `json:"default_passwords,omitempty"`
}

type ProbeTarget struct {
	Addr string `json:"addr"`
	Port int    `json:"port"`
	Kind string `json:"kind"`
}

// ProbeResult is wat een probe zag. Hits zijn vaste id's (zie
// internal/security), geen vrije tekst van de dienst.
type ProbeResult struct {
	Addr   string     `json:"addr"`
	Port   int        `json:"port"`
	Kind   string     `json:"kind"`
	Status int        `json:"status,omitempty"`
	Title  string     `json:"title,omitempty"`
	Server string     `json:"server,omitempty"`
	Hits   []ProbeHit `json:"hits,omitempty"`
	Error  string     `json:"error,omitempty"`
}

type ProbeHit struct {
	ID     string `json:"id"`
	Detail string `json:"detail,omitempty"`
}

// MaxScanDuration is hoe lang één netwerkscan hoogstens mag duren.
const MaxScanDuration = 3 * time.Hour

// Grenzen op het scanresultaat, zodat het in één NATS-bericht past.
const (
	MaxScanHosts        = 1024
	MaxScanPortsPerHost = 256
	MaxScanScriptOutput = 4096
)

// Scanprofielen.
const (
	ScanProfileQuick    = "snel"      // top 100 TCP-poorten, geen versies
	ScanProfileStandard = "standaard" // top 1000, versies, veilige scripts
	ScanProfileFull     = "volledig"  // alle 65535 TCP-poorten, versies, veilige scripts
)

// SafeScanScripts zijn de enige NSE-scripts die een scan mag draaien: ze
// lezen alleen wat een dienst zelf vertelt (categorie safe, geen brute
// force, geen exploits, geen DoS).
var SafeScanScripts = []string{"ssl-enum-ciphers", "ssl-cert", "ssh2-enum-algos", "ssh-hostkey", "http-title", "http-server-header"}

// ScanSpec zegt wat een scan doet. Targets en Exclude zijn IP-adressen of
// CIDR's (geen hostnamen); Allowed zijn de CIDR's van de scope, die de
// agent zelf nog eens nakijkt.
type ScanSpec struct {
	Targets []string `json:"targets"`
	Exclude []string `json:"exclude,omitempty"`
	Allowed []string `json:"allowed"`
	Profile string   `json:"profile"`
	// MaxRate is het hoogste aantal pakketten per seconde (0 = 300).
	MaxRate int `json:"max_rate,omitempty"`
}

// ScanResult is wat nmap vond.
type ScanResult struct {
	Nmap      string     `json:"nmap"`
	Args      []string   `json:"args"`
	Started   time.Time  `json:"started"`
	Finished  time.Time  `json:"finished"`
	HostsUp   int        `json:"hosts_up"`
	HostsSeen int        `json:"hosts_seen"`
	Hosts     []ScanHost `json:"hosts"`
	Truncated bool       `json:"truncated,omitempty"`
}

type ScanHost struct {
	Addr     string     `json:"addr"`
	MAC      string     `json:"mac,omitempty"`
	Vendor   string     `json:"vendor,omitempty"`
	Hostname string     `json:"hostname,omitempty"`
	Ports    []ScanPort `json:"ports"`
}

type ScanPort struct {
	Proto   string            `json:"proto"`
	Port    int               `json:"port"`
	State   string            `json:"state"`
	Service string            `json:"service,omitempty"`
	Product string            `json:"product,omitempty"`
	Version string            `json:"version,omitempty"`
	Extra   string            `json:"extra,omitempty"`
	Tunnel  string            `json:"tunnel,omitempty"`
	CPE     []string          `json:"cpe,omitempty"`
	Scripts map[string]string `json:"scripts,omitempty"`
}

// SecurityRequestTimeout is hoe lang een beveiligingsverzoek mag duren.
const SecurityRequestTimeout = 90 * time.Second

// MaxSecurityPackages begrenst de pakketlijst (een volle desktop heeft er
// zo'n 3000).
const MaxSecurityPackages = 20000

type SecurityRequest struct {
	ID       string     `json:"id"`
	Op       string     `json:"op"`
	Deadline time.Time  `json:"deadline"`
	Scan     *ScanSpec  `json:"scan,omitempty"`
	Probe    *ProbeSpec `json:"probe,omitempty"`
	Fix      *FixSpec   `json:"fix,omitempty"`
}

type SecurityResponse struct {
	Error     string             `json:"error,omitempty"`
	Inventory *SecurityInventory `json:"inventory,omitempty"`
	Audit     *SecurityAudit     `json:"audit,omitempty"`
	Scan      *ScanResult        `json:"scan,omitempty"`
	Probe     []ProbeResult      `json:"probe,omitempty"`
	Fix       *FixResult         `json:"fix,omitempty"`
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
