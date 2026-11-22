package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// audit leest de verharding van de node (fase 27, protocol 41). Alleen
// lezen; de server oordeelt.
func (a *Agent) audit(ctx context.Context) *protocol.SecurityAudit {
	c := a.Collector
	if c == nil {
		c = &Collector{Root: a.Root}
	}
	au := &protocol.SecurityAudit{Sysctl: map[string]string{}, Files: map[string]protocol.FileMode{}, Mounts: map[string]string{},
		Services: map[string]string{}, UID0: []string{}, EmptyPasswords: []string{}, LoginUsers: []string{}, SudoNopasswd: []string{},
		WorldWritable: []string{}, SUID: []string{}}
	fail := func(what string, err error) { au.Errors = append(au.Errors, what+": "+err.Error()) }

	if out, err := a.exec(ctx, "sshd", "-T"); err == nil {
		au.SSHD = parseSSHD(out)
	} else {
		var ee *exec.Error
		if !errors.As(err, &ee) {
			fail("sshd -T", errors.New(dpkgError(out, err)))
		}
	}
	for _, k := range protocol.SysctlKeys {
		if v := c.read("/proc/sys/" + strings.ReplaceAll(k, ".", "/")); v != "" {
			au.Sysctl[k] = strings.Join(strings.Fields(v), " ")
		}
	}
	users, groups := map[int]string{}, map[int]string{}
	for _, line := range strings.Split(c.read("/etc/passwd"), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 {
			continue
		}
		uid, _ := strconv.Atoi(f[2])
		if _, ok := users[uid]; !ok {
			users[uid] = f[0]
		}
		if f[2] == "0" {
			au.UID0 = append(au.UID0, f[0])
		}
		sh := f[6]
		if sh != "" && !strings.HasSuffix(sh, "nologin") && !strings.HasSuffix(sh, "/false") && !strings.HasSuffix(sh, "/sync") {
			au.LoginUsers = append(au.LoginUsers, f[0])
		}
	}
	for _, line := range strings.Split(c.read("/etc/group"), "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 3 {
			gid, _ := strconv.Atoi(f[2])
			if _, ok := groups[gid]; !ok {
				groups[gid] = f[0]
			}
		}
	}
	if b, err := os.ReadFile(c.path("/etc/shadow")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Split(line, ":")
			if len(f) >= 2 && f[0] != "" && f[1] == "" {
				au.EmptyPasswords = append(au.EmptyPasswords, f[0])
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		fail("/etc/shadow", err)
	}
	sudoers := []string{"/etc/sudoers"}
	if es, err := os.ReadDir(c.path("/etc/sudoers.d")); err == nil {
		for _, e := range es {
			if !e.IsDir() {
				sudoers = append(sudoers, "/etc/sudoers.d/"+e.Name())
			}
		}
	}
	for _, p := range sudoers {
		for _, line := range strings.Split(c.read(p), "\n") {
			l := strings.TrimSpace(line)
			if !strings.HasPrefix(l, "#") && strings.Contains(l, "NOPASSWD") {
				au.SudoNopasswd = append(au.SudoNopasswd, p)
				break
			}
		}
	}
	for _, p := range protocol.AuditFiles {
		fi, err := os.Stat(c.path(p))
		if err != nil {
			continue
		}
		m := protocol.FileMode{Mode: fmt.Sprintf("%04o", uint32(fi.Mode().Perm()))}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			m.Owner, m.Group = idName(users, int(st.Uid)), idName(groups, int(st.Gid))
		}
		au.Files[p] = m
	}
	deadline := time.Now().Add(20 * time.Second)
	au.WorldWritable = walkLimited(ctx, c.path("/etc"), c.path(""), deadline, func(d fs.DirEntry, fi fs.FileInfo) bool {
		return fi.Mode().Type() == 0 && fi.Mode().Perm()&0o002 != 0
	})
	for _, dir := range []string{"/usr", "/bin", "/sbin", "/opt"} {
		if len(au.SUID) >= protocol.MaxAuditList {
			break
		}
		au.SUID = append(au.SUID, walkLimited(ctx, c.path(dir), c.path(""), deadline, func(d fs.DirEntry, fi fs.FileInfo) bool {
			return fi.Mode().Type() == 0 && fi.Mode()&fs.ModeSetuid != 0
		})...)
	}
	au.SUID = uniqueSorted(au.SUID)
	for _, line := range strings.Split(c.read("/proc/mounts"), "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 && slices.Contains([]string{"/tmp", "/var/tmp", "/dev/shm"}, f[1]) {
			au.Mounts[f[1]] = f[3]
		}
	}
	out, _ := a.exec(ctx, "systemctl", append([]string{"is-active"}, protocol.AuditServices...)...)
	states := strings.Fields(string(out))
	if len(states) == len(protocol.AuditServices) {
		for i, s := range protocol.AuditServices {
			au.Services[s] = states[i]
		}
	}
	au.AutoUpgrades = autoUpgrades(c)
	au.AppArmor = c.read("/sys/module/apparmor/parameters/enabled")
	if b, err := os.ReadFile(c.path("/var/log/lynis-report.dat")); err == nil {
		au.Lynis = parseLynis(string(b))
	}
	return au
}

func idName(m map[int]string, id int) string {
	if n, ok := m[id]; ok {
		return n
	}
	return strconv.Itoa(id)
}

func parseSSHD(out []byte) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		k, v, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if k == "" {
			continue
		}
		k = strings.ToLower(k)
		if old, ok := m[k]; ok {
			v = old + "," + v // listenaddress en port komen meermaals
		}
		m[k] = v
	}
	return m
}

// walkLimited loopt door dir en geeft de paden (zonder root) waarvoor
// match waar is, hoogstens MaxAuditList, tot de deadline.
func walkLimited(ctx context.Context, dir, root string, deadline time.Time, match func(fs.DirEntry, fs.FileInfo) bool) []string {
	out := []string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) || len(out) >= protocol.MaxAuditList {
			return fs.SkipAll
		}
		if d.IsDir() {
			// Niet naar een ander bestandssysteem zoals /proc of een mount.
			if p != dir && (strings.HasSuffix(p, "/proc") || strings.HasSuffix(p, "/sys")) {
				return fs.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if match(d, fi) {
			rel := strings.TrimPrefix(p, strings.TrimSuffix(root, "/"))
			if !strings.HasPrefix(rel, "/") {
				rel = "/" + rel
			}
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func uniqueSorted(list []string) []string {
	sort.Strings(list)
	return slices.Compact(list)
}

var reAutoUpgrade = regexp.MustCompile(`APT::Periodic::Unattended-Upgrade\s+"([^"]*)"`)

func autoUpgrades(c *Collector) string {
	es, err := os.ReadDir(c.path("/etc/apt/apt.conf.d"))
	if err != nil {
		return ""
	}
	val := ""
	for _, e := range es { // later bestanden winnen, zoals bij apt
		if m := reAutoUpgrade.FindStringSubmatch(c.read("/etc/apt/apt.conf.d/" + e.Name())); m != nil {
			val = m[1]
		}
	}
	return val
}

func parseLynis(s string) *protocol.LynisReport {
	r := &protocol.LynisReport{Warnings: []string{}, Suggestions: []string{}}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "report_datetime_end":
			r.Date = v
		case "hardening_index":
			r.HardeningIdx, _ = strconv.Atoi(v)
		case "lynis_tests_done":
			r.TestsDone, _ = strconv.Atoi(v)
		case "lynis_version":
			r.LynisVersion = v
		case "warning[]":
			if len(r.Warnings) < protocol.MaxAuditList {
				r.Warnings = append(r.Warnings, lynisItem(v))
			}
		case "suggestion[]":
			if len(r.Suggestions) < protocol.MaxAuditList {
				r.Suggestions = append(r.Suggestions, lynisItem(v))
			}
		}
	}
	return r
}

// lynisItem maakt van "SSH-7408|Consider hardening SSH|AllowTcpForwarding|-|"
// "SSH-7408: Consider hardening SSH (AllowTcpForwarding)".
func lynisItem(v string) string {
	f := strings.Split(v, "|")
	if len(f) < 2 {
		return v
	}
	s := f[0] + ": " + f[1]
	if len(f) > 2 && f[2] != "" && f[2] != "-" {
		s += " (" + f[2] + ")"
	}
	return s
}
