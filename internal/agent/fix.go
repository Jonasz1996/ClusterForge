package agent

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
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Veilige oplossingen (fase 27 M6, protocol 44). De agent kent zelf wat
// elke regel schrijft; de server kiest alleen welke regels uit
// protocol.FixableRules. SSH krijgt een terugdraaitimer (systemd-run): wie
// zich buitensluit, krijgt na een paar minuten de oude instellingen terug.

const (
	fixSSHDropIn    = "/etc/ssh/sshd_config.d/01-clusterforge.conf"
	fixSysctlFile   = "/etc/sysctl.d/90-clusterforge.conf"
	fixStateDir     = "/var/lib/clusterforge/fix"
	fixSSHBackup    = fixStateDir + "/sshd-backup.conf"
	fixSSHScript    = fixStateDir + "/ssh-rollback.sh"
	fixRollbackUnit = "cf-ssh-rollback"
)

const fixHeader = "# Beheerd door ClusterForge (Kwetsbaarheden → Verharding).\n# ClusterForge past alleen zijn eigen regels aan; wat je zelf toevoegt blijft staan.\n"

type kv struct{ key, val string }

var fixSSH = map[string][]kv{
	"ssh-root-login":      {{"PermitRootLogin", "prohibit-password"}},
	"ssh-empty-passwords": {{"PermitEmptyPasswords", "no"}},
	"ssh-password-auth":   {{"PasswordAuthentication", "no"}, {"KbdInteractiveAuthentication", "no"}},
	"ssh-max-auth-tries":  {{"MaxAuthTries", "3"}},
	"ssh-x11":             {{"X11Forwarding", "no"}},
	// ssh-weak-crypto: berekend uit wat sshd nu aanbiedt, zonder de
	// zwakke algoritmen (zie weakSSH).
	"ssh-weak-crypto": nil,
}

// weakSSH zijn de zwakke delen per sshd-sleutel (zelfde als de regel op
// de server).
var weakSSH = []struct {
	key   string
	parts []string
}{
	{"Ciphers", []string{"cbc", "3des", "arcfour", "blowfish"}},
	{"MACs", []string{"md5", "hmac-sha1", "umac-64"}},
	{"KexAlgorithms", []string{"group1-", "group14-sha1", "group-exchange-sha1"}},
}

var fixSysctl = map[string][]kv{
	"net-redirects": {{"net.ipv4.conf.all.accept_redirects", "0"}, {"net.ipv4.conf.default.accept_redirects", "0"},
		{"net.ipv6.conf.all.accept_redirects", "0"}, {"net.ipv6.conf.default.accept_redirects", "0"}},
	"net-redirects6":     {{"net.ipv6.conf.all.accept_redirects", "0"}, {"net.ipv6.conf.default.accept_redirects", "0"}},
	"net-source-route":   {{"net.ipv4.conf.all.accept_source_route", "0"}, {"net.ipv4.conf.default.accept_source_route", "0"}},
	"net-send-redirects": {{"net.ipv4.conf.all.send_redirects", "0"}, {"net.ipv4.conf.default.send_redirects", "0"}},
	// 2 (los): werkt ook met keepalived en asymmetrische routes.
	"net-rp-filter":      {{"net.ipv4.conf.all.rp_filter", "2"}, {"net.ipv4.conf.default.rp_filter", "2"}},
	"net-syncookies":     {{"net.ipv4.tcp_syncookies", "1"}},
	"net-martians":       {{"net.ipv4.conf.all.log_martians", "1"}, {"net.ipv4.conf.default.log_martians", "1"}},
	"net-icmp-broadcast": {{"net.ipv4.icmp_echo_ignore_broadcasts", "1"}},
	"kern-aslr":          {{"kernel.randomize_va_space", "2"}},
	// 2 en niet 1: met 1 kan het pas na een herstart terug.
	"kern-bpf":       {{"kernel.unprivileged_bpf_disabled", "2"}},
	"kern-links":     {{"fs.protected_symlinks", "1"}, {"fs.protected_hardlinks", "1"}},
	"kern-kptr":      {{"kernel.kptr_restrict", "1"}},
	"kern-dmesg":     {{"kernel.dmesg_restrict", "1"}},
	"kern-ptrace":    {{"kernel.yama.ptrace_scope", "1"}},
	"kern-suid-dump": {{"fs.suid_dumpable", "0"}},
}

type fileFix struct {
	path         string
	mode         os.FileMode
	owner, group string
}

var fixFiles = map[string]fileFix{
	"file-shadow":      {"/etc/shadow", 0o640, "root", "shadow"},
	"file-gshadow":     {"/etc/gshadow", 0o640, "root", "shadow"},
	"file-passwd":      {"/etc/passwd", 0o644, "root", "root"},
	"file-sshd-config": {"/etc/ssh/sshd_config", 0o600, "root", "root"},
	"file-sudoers":     {"/etc/sudoers", 0o440, "root", "root"},
}

var fixServices = map[string]bool{"svc-telnet": true, "svc-fail2ban": true}

func (a *Agent) fix(ctx context.Context, spec *protocol.FixSpec) (*protocol.FixResult, error) {
	if spec == nil || len(spec.Rules) == 0 {
		return nil, errors.New("geen regels om op te lossen")
	}
	after := protocol.FixRollbackDefault
	if spec.RollbackAfter > 0 {
		after = time.Duration(spec.RollbackAfter) * time.Second
	}
	if after < protocol.FixRollbackMin || after > protocol.FixRollbackMax {
		return nil, fmt.Errorf("terugdraaitimer moet tussen %s en %s liggen", protocol.FixRollbackMin, protocol.FixRollbackMax)
	}
	res := &protocol.FixResult{Outcomes: []protocol.FixOutcome{}}
	var ssh, sysctl []string
	seen := map[string]bool{}
	for _, r := range spec.Rules {
		if seen[r] {
			continue
		}
		seen[r] = true
		_, isSSH := fixSSH[r]
		switch {
		case !slices.Contains(protocol.FixableRules, r):
			res.Outcomes = append(res.Outcomes, protocol.FixOutcome{Rule: r, Error: "deze regel lost ClusterForge niet zelf op"})
		case isSSH:
			ssh = append(ssh, r)
		case fixSysctl[r] != nil:
			sysctl = append(sysctl, r)
		case fixFiles[r].path != "":
			res.Outcomes = append(res.Outcomes, a.fixFile(r, fixFiles[r]))
		case fixServices[r]:
			res.Outcomes = append(res.Outcomes, a.fixService(ctx, r))
		default:
			res.Outcomes = append(res.Outcomes, protocol.FixOutcome{Rule: r, Error: "deze agent kent die oplossing niet"})
		}
	}
	if len(sysctl) > 0 {
		res.Outcomes = append(res.Outcomes, a.fixSysctl(ctx, sysctl)...)
	}
	if len(ssh) > 0 {
		outs, pending := a.fixSSH(ctx, ssh, after)
		res.Outcomes = append(res.Outcomes, outs...)
		if pending {
			at := time.Now().Add(after).UTC().Truncate(time.Second)
			res.SSHPending, res.RollbackAt = true, &at
		}
	}
	return res, nil
}

// mergeConf zet de sleutels van kvs in content: een bestaande regel met die
// sleutel wordt vervangen, een nieuwe komt achteraan. sshd: "Sleutel
// waarde" (hoofdletterongevoelig); sysctl: "sleutel = waarde".
func mergeConf(content string, kvs []kv, sysctl bool) string {
	keyOf := func(line string) string {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, ";") {
			return ""
		}
		if sysctl {
			k, _, _ := strings.Cut(l, "=")
			return strings.TrimSpace(k)
		}
		if f := strings.Fields(l); len(f) > 0 {
			return strings.ToLower(f[0])
		}
		return ""
	}
	format := func(x kv) string {
		if sysctl {
			return x.key + " = " + x.val
		}
		return x.key + " " + x.val
	}
	if content == "" {
		content = fixHeader
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	for _, x := range kvs {
		want := x.key
		if !sysctl {
			want = strings.ToLower(x.key)
		}
		done := false
		for i, l := range lines {
			if keyOf(l) == want {
				if done {
					lines[i] = "\x00" // dubbel: alleen de eerste telt bij sshd
					continue
				}
				lines[i], done = format(x), true
			}
		}
		if !done {
			lines = append(lines, format(x))
		}
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l != "\x00" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func (a *Agent) fixSysctl(ctx context.Context, rules []string) []protocol.FixOutcome {
	outs := make([]protocol.FixOutcome, 0, len(rules))
	fail := func(err error) []protocol.FixOutcome {
		for _, r := range rules {
			outs = append(outs, protocol.FixOutcome{Rule: r, Error: err.Error()})
		}
		return outs
	}
	p, err := a.path(fixSysctlFile)
	if err != nil {
		return fail(err)
	}
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	var all []kv
	for _, r := range rules {
		all = append(all, fixSysctl[r]...)
	}
	if err := writeFileAtomic(p, []byte(mergeConf(string(old), all, true)), 0o644); err != nil {
		return fail(fmt.Errorf("%s schrijven: %w", fixSysctlFile, err))
	}
	for _, r := range rules {
		o := protocol.FixOutcome{Rule: r}
		var errs []string
		for _, x := range fixSysctl[r] {
			if out, err := a.exec(ctx, "sysctl", "-w", x.key+"="+x.val); err != nil {
				errs = append(errs, x.key+": "+dpkgError(out, err))
				continue
			}
			o.Changed = append(o.Changed, x.key+" = "+x.val)
		}
		if len(errs) > 0 {
			o.Error = "staat in " + fixSysctlFile + " maar kon nu niet gezet worden (" + strings.Join(errs, "; ") + ")"
		} else {
			o.OK = true
		}
		outs = append(outs, o)
	}
	return outs
}

// fixFile maakt de rechten alleen strenger, nooit ruimer.
func (a *Agent) fixFile(rule string, f fileFix) protocol.FixOutcome {
	o := protocol.FixOutcome{Rule: rule}
	p, err := a.path(f.path)
	if err != nil {
		o.Error = err.Error()
		return o
	}
	fi, err := os.Stat(p)
	if err != nil {
		o.Error = err.Error()
		return o
	}
	c := a.Collector
	if c == nil {
		c = &Collector{Root: a.Root}
	}
	uid, gid := idOf(c.read("/etc/passwd"), f.owner), idOf(c.read("/etc/group"), f.group)
	if uid < 0 || gid < 0 {
		o.Error = fmt.Sprintf("gebruiker %s of groep %s bestaat niet", f.owner, f.group)
		return o
	}
	if err := os.Chown(p, uid, gid); err != nil {
		o.Error = err.Error()
		return o
	}
	mode := fi.Mode().Perm() & f.mode
	if err := os.Chmod(p, mode); err != nil {
		o.Error = err.Error()
		return o
	}
	o.OK, o.Changed = true, []string{fmt.Sprintf("%s: %s:%s %04o", f.path, f.owner, f.group, uint32(mode))}
	return o
}

// idOf zoekt de uid of gid van name in /etc/passwd of /etc/group; -1 als
// die er niet is.
func idOf(file, name string) int {
	for _, line := range strings.Split(file, "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 3 && f[0] == name {
			if n, err := strconv.Atoi(f[2]); err == nil {
				return n
			}
		}
	}
	return -1
}

func (a *Agent) fixService(ctx context.Context, rule string) protocol.FixOutcome {
	o := protocol.FixOutcome{Rule: rule}
	active := func(u string) bool {
		out, _ := a.exec(ctx, "systemctl", "is-active", u)
		return strings.TrimSpace(string(out)) == "active"
	}
	var units []string
	switch rule {
	case "svc-telnet":
		units = []string{"telnet.socket", "rsh.socket"}
	case "svc-fail2ban":
		if !active("crowdsec") {
			o.Error = "CrowdSec draait niet; fail2ban blijft aan tot CrowdSec actief is"
			return o
		}
		units = []string{"fail2ban"}
	}
	for _, u := range units {
		if !active(u) {
			continue
		}
		if out, err := a.exec(ctx, "systemctl", "disable", "--now", u); err != nil {
			o.Error = u + ": " + dpkgError(out, err)
			return o
		}
		o.Changed = append(o.Changed, u+" uitgezet")
	}
	o.OK = true
	if len(o.Changed) == 0 {
		o.Changed = []string{"draaide al niet meer"}
	}
	return o
}

var reSSHInclude = regexp.MustCompile(`(?mi)^\s*Include\s+\S*sshd_config\.d/`)

// dropWeak haalt de zwakke algoritmen uit een kommalijst van sshd -T.
func dropWeak(list string, parts []string) (kept, removed []string) {
	for _, x := range strings.Split(list, ",") {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		weak := false
		for _, p := range parts {
			if strings.Contains(x, p) {
				weak = true
				break
			}
		}
		if weak {
			removed = append(removed, x)
		} else {
			kept = append(kept, x)
		}
	}
	return kept, removed
}

// hasAuthorizedKeys: heeft root of een gebruiker met login-shell een
// niet-lege authorized_keys? Zonder sleutel geen wachtwoorden uitzetten.
func hasAuthorizedKeys(c *Collector) bool {
	for _, line := range strings.Split(c.read("/etc/passwd"), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 || f[5] == "" {
			continue
		}
		sh := f[6]
		if f[0] != "root" && (sh == "" || strings.HasSuffix(sh, "nologin") || strings.HasSuffix(sh, "/false")) {
			continue
		}
		for _, name := range []string{"authorized_keys", "authorized_keys2"} {
			for _, l := range strings.Split(c.read(filepath.Join(f[5], ".ssh", name)), "\n") {
				l = strings.TrimSpace(l)
				if l != "" && !strings.HasPrefix(l, "#") {
					return true
				}
			}
		}
	}
	return false
}

func (a *Agent) sshRollbackPending(ctx context.Context) bool {
	out, _ := a.exec(ctx, "systemctl", "is-active", fixRollbackUnit+".timer")
	return strings.TrimSpace(string(out)) == "active"
}

func (a *Agent) reloadSSH(ctx context.Context) error {
	out, err := a.exec(ctx, "systemctl", "reload", "ssh")
	if err == nil {
		return nil
	}
	if out2, err2 := a.exec(ctx, "systemctl", "reload", "sshd"); err2 == nil {
		return nil
	} else if len(out2) > 0 {
		out = out2
	}
	return errors.New("sshd herladen mislukt: " + dpkgError(out, err))
}

// sshEffective: zet sshd -T de waarde zoals gevraagd?
func sshEffective(cur map[string]string, x kv) bool {
	v := strings.ToLower(cur[strings.ToLower(x.key)])
	want := strings.ToLower(x.val)
	if want == "prohibit-password" && v == "without-password" {
		return true
	}
	return v == want
}

// fixSSH schrijft de SSH-instellingen in de drop-in, controleert met
// sshd -t, zet de terugdraaitimer en herlaadt sshd. pending is true als
// de instellingen veranderd zijn en de timer loopt.
func (a *Agent) fixSSH(ctx context.Context, rules []string, after time.Duration) ([]protocol.FixOutcome, bool) {
	outs := make([]protocol.FixOutcome, 0, len(rules))
	failAll := func(msg string) ([]protocol.FixOutcome, bool) {
		for _, r := range rules {
			outs = append(outs, protocol.FixOutcome{Rule: r, Error: msg})
		}
		return outs, false
	}
	c := a.Collector
	if c == nil {
		c = &Collector{Root: a.Root}
	}
	if a.sshRollbackPending(ctx) {
		return failAll("er wacht nog een SSH-wijziging op bevestiging; bevestig of draai die eerst terug")
	}
	if !reSSHInclude.MatchString(c.read("/etc/ssh/sshd_config")) {
		return failAll("/etc/ssh/sshd_config leest /etc/ssh/sshd_config.d/ niet in (geen Include-regel); pas dit met de hand aan")
	}
	out, err := a.exec(ctx, "sshd", "-T")
	if err != nil {
		return failAll("sshd -T: " + dpkgError(out, err))
	}
	cur := parseSSHD(out)

	var all []kv
	want := map[string][]kv{}
	for _, r := range rules {
		switch r {
		case "ssh-weak-crypto":
			var list []kv
			for _, w := range weakSSH {
				kept, removed := dropWeak(cur[strings.ToLower(w.key)], w.parts)
				if len(removed) == 0 {
					continue
				}
				if len(kept) == 0 {
					list = nil
					break
				}
				list = append(list, kv{w.key, strings.Join(kept, ",")})
			}
			if len(list) == 0 {
				outs = append(outs, protocol.FixOutcome{Rule: r, Error: "geen sterke algoritmen over om te houden, of niets zwaks gevonden"})
				continue
			}
			want[r] = list
		case "ssh-password-auth":
			if !hasAuthorizedKeys(c) {
				outs = append(outs, protocol.FixOutcome{Rule: r,
					Error: "geen enkele authorized_keys gevonden (root of gebruikers met login-shell); zet eerst een sleutel, anders sluit je jezelf buiten"})
				continue
			}
			want[r] = fixSSH[r]
		default:
			want[r] = fixSSH[r]
		}
		all = append(all, want[r]...)
	}
	if len(all) == 0 {
		return outs, false
	}

	dropIn, err := a.path(fixSSHDropIn)
	if err != nil {
		return failAll(err.Error())
	}
	old, err := os.ReadFile(dropIn)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return failAll(err.Error())
	}
	restore := func() {
		if existed {
			_ = writeFileAtomic(dropIn, old, 0o644)
		} else {
			_ = os.Remove(dropIn)
		}
	}
	next := mergeConf(string(old), all, false)
	if existed && next == string(old) {
		for r, list := range want {
			outs = append(outs, protocol.FixOutcome{Rule: r, OK: true, Changed: []string{"stond al in " + fixSSHDropIn + " (" + kvText(list) + ")"}})
		}
		return outs, false
	}
	if err := writeFileAtomic(dropIn, []byte(next), 0o644); err != nil {
		return failAll(fmt.Sprintf("%s schrijven: %v", fixSSHDropIn, err))
	}
	if out, err := a.exec(ctx, "sshd", "-t"); err != nil {
		restore()
		return failAll("sshd -t keurt de nieuwe instellingen af, niets gewijzigd: " + dpkgError(out, err))
	}

	// Terugdraaitimer vóór het herladen.
	stateDir, err := a.path(fixStateDir)
	if err != nil {
		restore()
		return failAll(err.Error())
	}
	backup, script := filepath.Join(stateDir, filepath.Base(fixSSHBackup)), filepath.Join(stateDir, filepath.Base(fixSSHScript))
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		restore()
		return failAll(err.Error())
	}
	_ = os.Remove(backup)
	if existed {
		if err := os.WriteFile(backup, old, 0o600); err != nil {
			restore()
			return failAll(err.Error())
		}
	}
	if err := os.WriteFile(script, []byte(rollbackScript()), 0o700); err != nil {
		restore()
		return failAll(err.Error())
	}
	secs := strconv.Itoa(int(after / time.Second))
	if out, err := a.exec(ctx, "systemd-run", "--unit="+fixRollbackUnit, "--on-active="+secs+"s", "--timer-property=AccuracySec=1s",
		"--description=ClusterForge: SSH-wijziging terugdraaien als ze niet bevestigd wordt", "/bin/sh", fixSSHScript); err != nil {
		restore()
		return failAll("kon geen terugdraaitimer zetten, daarom niets gewijzigd: " + dpkgError(out, err))
	}
	if err := a.reloadSSH(ctx); err != nil {
		_, _ = a.exec(ctx, "systemctl", "stop", fixRollbackUnit+".timer")
		restore()
		_ = a.reloadSSH(ctx)
		return failAll(err.Error() + "; oude instellingen teruggezet")
	}

	now := map[string]string{}
	if out, err := a.exec(ctx, "sshd", "-T"); err == nil {
		now = parseSSHD(out)
	}
	for _, r := range rules {
		list, ok := want[r]
		if !ok {
			continue
		}
		o := protocol.FixOutcome{Rule: r, OK: true, Changed: []string{kvText(list)}}
		for _, x := range list {
			if r == "ssh-weak-crypto" {
				for _, w := range weakSSH {
					if w.key == x.key {
						if _, removed := dropWeak(now[strings.ToLower(x.key)], w.parts); len(removed) > 0 {
							o.OK, o.Error = false, x.key+" bevat nog "+strings.Join(removed, ",")+"; een ander bestand of Match-blok zet het eerder"
						}
					}
				}
				continue
			}
			if !sshEffective(now, x) {
				o.OK, o.Error = false, fmt.Sprintf("sshd gebruikt nog %s %s: een ander bestand in sshd_config.d of een Match-blok zet het eerder", x.key, orDashAgent(now[strings.ToLower(x.key)]))
			}
		}
		outs = append(outs, o)
	}
	return outs, true
}

func orDashAgent(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func kvText(list []kv) string {
	parts := make([]string, len(list))
	for i, x := range list {
		v := x.val
		if len(v) > 60 {
			v = v[:57] + "…"
		}
		parts[i] = x.key + " " + v
	}
	return strings.Join(parts, ", ")
}

func rollbackScript() string {
	return `#!/bin/sh
# ClusterForge: SSH-wijziging terugdraaien (niet op tijd bevestigd).
if [ -f ` + fixSSHBackup + ` ]; then
	cp ` + fixSSHBackup + ` ` + fixSSHDropIn + `
else
	rm -f ` + fixSSHDropIn + `
fi
systemctl reload ssh 2>/dev/null || systemctl reload sshd
rm -f ` + fixSSHBackup + ` ` + fixSSHScript + `
`
}

// fixConfirm houdt de SSH-wijziging.
func (a *Agent) fixConfirm(ctx context.Context) error {
	if !a.sshRollbackPending(ctx) {
		return errors.New("er wacht geen SSH-wijziging; de timer liep misschien al af en zette de oude instellingen terug")
	}
	if out, err := a.exec(ctx, "systemctl", "stop", fixRollbackUnit+".timer"); err != nil {
		return errors.New("timer stoppen mislukt: " + dpkgError(out, err))
	}
	for _, p := range []string{fixSSHBackup, fixSSHScript} {
		if q, err := a.path(p); err == nil {
			_ = os.Remove(q)
		}
	}
	return nil
}

// fixRollback zet de SSH-wijziging meteen terug.
func (a *Agent) fixRollback(ctx context.Context) error {
	if !a.sshRollbackPending(ctx) {
		return errors.New("er wacht geen SSH-wijziging; de timer liep misschien al af en zette de oude instellingen terug")
	}
	_, _ = a.exec(ctx, "systemctl", "stop", fixRollbackUnit+".timer")
	if out, err := a.exec(ctx, "/bin/sh", fixSSHScript); err != nil {
		return errors.New("terugdraaien mislukt: " + dpkgError(out, err))
	}
	return nil
}
