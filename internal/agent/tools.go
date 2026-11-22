package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Beveiligingstools (fase 27 M7, protocol 45). Elke actie loopt op de
// achtergrond via systemd-run (een volledige ClamAV-scan of een AIDE-
// basislijn duurt lang); de stand en het resultaat komen bij een volgende
// status. Pakketten, bronnen en commando's staan hier vast.

const toolsDir = "/var/lib/clusterforge/tools"

const toolApt = "apt-get -q -y -o DPkg::Lock::Timeout=300"

// auditdRules is de regelset van ClusterForge: wijzigingen aan accounts,
// sudo, SSH, cron en systemd, kernelmodules en commando's als root door
// een echte gebruiker.
const auditdRules = `## Beheerd door ClusterForge (Kwetsbaarheden → Tools).
-w /etc/passwd -p wa -k identity
-w /etc/group -p wa -k identity
-w /etc/shadow -p wa -k identity
-w /etc/gshadow -p wa -k identity
-w /etc/sudoers -p wa -k sudoers
-w /etc/sudoers.d/ -p wa -k sudoers
-w /etc/ssh/sshd_config -p wa -k sshd
-w /etc/ssh/sshd_config.d/ -p wa -k sshd
-w /etc/crontab -p wa -k cron
-w /etc/cron.d/ -p wa -k cron
-w /var/spool/cron/ -p wa -k cron
-w /etc/systemd/system/ -p wa -k systemd
-w /usr/bin/sudo -p x -k priv
-w /usr/bin/su -p x -k priv
-a always,exit -F arch=b64 -S init_module,finit_module,delete_module -k modules
-a always,exit -F arch=b64 -S execve -F euid=0 -F auid>=1000 -F auid!=unset -k root_cmd
`

type toolDef struct {
	pkg     string // pakket voor de stand (dpkg)
	install string
	run     string
	remove  string
	// baseline: AIDE en rkhunter (na een verwachte wijziging).
	baseline string
}

var toolDefs = map[string]toolDef{
	protocol.ToolClamAV: {pkg: "clamav",
		install: toolApt + " update && " + toolApt + " install clamav clamav-freshclam",
		run: "freshclam --quiet || true\n" +
			"clamscan -r -i --no-summary --max-filesize=100M --max-scansize=300M " +
			"--exclude-dir='^/proc' --exclude-dir='^/sys' --exclude-dir='^/dev' --exclude-dir='^/run' " +
			"--exclude-dir='^/var/lib/docker/overlay2' --exclude-dir='^/var/lib/clusterforge' /",
		remove: toolApt + " remove clamav clamav-freshclam"},
	protocol.ToolAIDE: {pkg: "aide",
		install:  toolApt + " update && " + toolApt + " install aide && aideinit -y -f",
		run:      "aide --config=/etc/aide/aide.conf --check",
		baseline: "aideinit -y -f",
		remove:   toolApt + " remove aide"},
	protocol.ToolRkhunter: {pkg: "rkhunter",
		install:  toolApt + " update && " + toolApt + " install rkhunter && rkhunter --propupd --nocolors",
		run:      "rkhunter --check --sk --rwo --nocolors",
		baseline: "rkhunter --propupd --nocolors",
		remove:   toolApt + " remove rkhunter"},
	protocol.ToolChkrootkit: {pkg: "chkrootkit",
		install: toolApt + " update && " + toolApt + " install chkrootkit",
		run:     "chkrootkit -q",
		remove:  toolApt + " remove chkrootkit"},
	protocol.ToolLynis: {pkg: "lynis",
		install: toolApt + " update && " + toolApt + " install lynis",
		run:     "lynis audit system --quick --quiet --no-colors",
		remove:  toolApt + " remove lynis"},
	protocol.ToolAuditd: {pkg: "auditd",
		install: toolApt + " update && " + toolApt + " install auditd && mkdir -p /etc/audit/rules.d && " +
			"cat > /etc/audit/rules.d/90-clusterforge.rules <<'CFRULES'\n" + auditdRules + "CFRULES\n" +
			"systemctl enable --now auditd && augenrules --load",
		remove: "rm -f /etc/audit/rules.d/90-clusterforge.rules && augenrules --load; " + toolApt + " remove auditd"},
	// Trivy uit de apt-bron van Aqua Security (met signed-by); scant de
	// images van de draaiende containers op hoge en kritieke CVE's.
	protocol.ToolTrivy: {pkg: "trivy",
		install: toolApt + " update && " + toolApt + " install ca-certificates curl gpg && install -d -m 0755 /etc/apt/keyrings && " +
			"curl -fsSL https://aquasecurity.github.io/trivy-repo/deb/public.key | gpg --dearmor --yes -o /etc/apt/keyrings/trivy.gpg && " +
			"echo 'deb [signed-by=/etc/apt/keyrings/trivy.gpg] https://aquasecurity.github.io/trivy-repo/deb generic main' > /etc/apt/sources.list.d/trivy.list && " +
			toolApt + " update && " + toolApt + " install trivy",
		run: "command -v docker >/dev/null || { echo 'geen Docker op deze node'; exit 2; }\n" +
			"d=" + toolsDir + "/trivy/images; rm -rf $d; mkdir -p $d\n" +
			"trivy image --download-db-only -q || true\n" +
			"docker ps --format '{{.Image}}' | sort -u | head -n 50 | while read -r img; do\n" +
			"  f=$d/$(printf %s \"$img\" | tr -c 'A-Za-z0-9._-' '_')\n" +
			"  printf '%s' \"$img\" > \"$f.name\"\n" +
			"  trivy image -q --scanners vuln --severity HIGH,CRITICAL --format json -o \"$f.json\" \"$img\" 2> \"$f.err\"\n" +
			"done",
		remove: toolApt + " remove trivy && rm -f /etc/apt/sources.list.d/trivy.list /etc/apt/keyrings/trivy.gpg"},
}

func (a *Agent) tools(ctx context.Context, spec *protocol.ToolsSpec) (*protocol.ToolsResult, error) {
	if spec == nil {
		return nil, errors.New("geen toolverzoek")
	}
	if spec.Action != protocol.ToolStatus {
		if err := a.toolStart(ctx, spec.Tool, spec.Action); err != nil {
			return nil, err
		}
	}
	res := &protocol.ToolsResult{Tools: []protocol.ToolState{}}
	for _, t := range protocol.SecurityTools {
		res.Tools = append(res.Tools, a.toolState(ctx, t))
	}
	return res, nil
}

func toolUnit(tool string) string { return "cf-tool-" + tool }

func (a *Agent) toolRunning(ctx context.Context, tool string) bool {
	out, _ := a.exec(ctx, "systemctl", "is-active", toolUnit(tool)+".service")
	s := strings.TrimSpace(string(out))
	return s == "active" || s == "activating"
}

// toolScript is het script van één actie: het schrijft de uitvoer, de
// begin- en eindtijd en de exitcode naar toolsDir/<tool>/.
func toolScript(tool, action, body string) string {
	d := toolsDir + "/" + tool
	return `#!/bin/sh
# ClusterForge: ` + tool + ` ` + action + `
d=` + d + `
echo ` + action + ` > $d/running
date -u +%Y-%m-%dT%H:%M:%SZ > $d/` + action + `.started
rm -f $d/` + action + `.exit $d/` + action + `.ended
export DEBIAN_FRONTEND=noninteractive LC_ALL=C
(
` + body + `
) > $d/` + action + `.log 2>&1
echo $? > $d/` + action + `.exit
date -u +%Y-%m-%dT%H:%M:%SZ > $d/` + action + `.ended
rm -f $d/running
`
}

func (a *Agent) toolStart(ctx context.Context, tool, action string) error {
	def, ok := toolDefs[tool]
	if !ok {
		return fmt.Errorf("onbekende tool %q", tool)
	}
	body := map[string]string{protocol.ToolInstall: def.install, protocol.ToolRun: def.run, protocol.ToolRemove: def.remove,
		protocol.ToolBaseline: def.baseline}[action]
	if body == "" {
		return fmt.Errorf("%s kent de actie %q niet", tool, action)
	}
	if a.toolRunning(ctx, tool) {
		return fmt.Errorf("%s is al bezig", tool)
	}
	dir, err := a.path(toolsDir + "/" + tool)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	script := filepath.Join(dir, action+".sh")
	if err := os.WriteFile(script, []byte(toolScript(tool, action, body)), 0o700); err != nil {
		return err
	}
	// Laag in prioriteit, zodat de diensten op de node voorgaan.
	if out, err := a.exec(ctx, "systemd-run", "--unit="+toolUnit(tool), "--collect", "--property=Nice=10", "--property=IOSchedulingClass=idle",
		"--description=ClusterForge: "+tool+" "+action, "/bin/sh", toolsDir+"/"+tool+"/"+action+".sh"); err != nil {
		return fmt.Errorf("%s starten mislukt: %s", tool, dpkgError(out, err))
	}
	return nil
}

func (a *Agent) toolState(ctx context.Context, tool string) protocol.ToolState {
	st := protocol.ToolState{Tool: tool, Last: map[string]protocol.ToolRunState{}}
	def := toolDefs[tool]
	if out, err := a.exec(ctx, "dpkg-query", "-W", "-f=${db:Status-Abbrev} ${Version}", def.pkg); err == nil {
		f := strings.Fields(string(out))
		if len(f) >= 2 && strings.HasPrefix(f[0], "ii") {
			st.Installed, st.Version = true, f[1]
		}
	}
	dir, err := a.path(toolsDir + "/" + tool)
	if err != nil {
		return st
	}
	if a.toolRunning(ctx, tool) {
		b, _ := os.ReadFile(filepath.Join(dir, "running"))
		st.Running = strings.TrimSpace(string(b))
		if st.Running == "" {
			st.Running = "bezig"
		}
	}
	for _, action := range []string{protocol.ToolInstall, protocol.ToolRun, protocol.ToolBaseline, protocol.ToolRemove} {
		if r, ok := readToolRun(dir, tool, action); ok {
			st.Last[action] = r
		}
	}
	if tool == protocol.ToolAuditd && st.Installed {
		out, _ := a.exec(ctx, "systemctl", "is-active", "auditd")
		st.Active = strings.TrimSpace(string(out)) == "active"
		if out, err := a.exec(ctx, "auditctl", "-l"); err == nil {
			for _, l := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(l, "-") {
					st.Rules++
				}
			}
		}
	}
	return st
}

func readTime(p string) *time.Time {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil {
		return nil
	}
	return &t
}

func readToolRun(dir, tool, action string) (protocol.ToolRunState, bool) {
	r := protocol.ToolRunState{Started: readTime(filepath.Join(dir, action+".started")), Ended: readTime(filepath.Join(dir, action+".ended"))}
	if r.Started == nil {
		return r, false
	}
	if b, err := os.ReadFile(filepath.Join(dir, action+".exit")); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			r.Exit = &n
		}
	}
	log, _ := os.ReadFile(filepath.Join(dir, action+".log"))
	r.Tail = toolTail(string(log), 20, 2048)
	if action == protocol.ToolRun && r.Ended != nil {
		r.Items = parseToolItems(tool, string(log))
		if tool == protocol.ToolTrivy {
			r.Images = readTrivy(filepath.Join(dir, "images"))
		}
	}
	return r, true
}

func toolTail(s string, lines, max int) string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(l) > lines {
		l = l[len(l)-lines:]
	}
	out := strings.Join(l, "\n")
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// parseToolItems haalt de vondsten uit de uitvoer van een run.
func parseToolItems(tool, log string) []string {
	items := []string{}
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && len(items) < protocol.MaxToolItems {
			items = append(items, s)
		}
	}
	lines := strings.Split(log, "\n")
	switch tool {
	case protocol.ToolClamAV:
		for _, l := range lines {
			if strings.HasSuffix(l, " FOUND") {
				add(l)
			}
		}
	case protocol.ToolAIDE:
		section := ""
		for _, l := range lines {
			t := strings.TrimSpace(l)
			switch {
			case strings.HasPrefix(t, "Added entries"):
				section = "toegevoegd"
			case strings.HasPrefix(t, "Removed entries"):
				section = "verwijderd"
			case strings.HasPrefix(t, "Changed entries"):
				section = "gewijzigd"
			case strings.HasPrefix(t, "Detailed information"):
				section = ""
			case section != "":
				if i := strings.Index(t, ": /"); i > 0 {
					add(section + " " + t[i+2:])
				}
			}
		}
	case protocol.ToolRkhunter:
		for _, l := range lines {
			if strings.Contains(l, "Warning:") {
				add(l)
			}
		}
	case protocol.ToolChkrootkit:
		for _, l := range lines {
			add(l)
		}
	}
	return items
}

func readTrivy(dir string) []protocol.TrivyImage {
	names, _ := filepath.Glob(filepath.Join(dir, "*.name"))
	sort.Strings(names)
	out := []protocol.TrivyImage{}
	rank := map[string]int{"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3, "UNKNOWN": 4}
	for _, n := range names {
		base := strings.TrimSuffix(n, ".name")
		img, _ := os.ReadFile(n)
		ti := protocol.TrivyImage{Image: strings.TrimSpace(string(img)), Counts: map[string]int{}, Vulns: []protocol.TrivyVuln{}}
		b, err := os.ReadFile(base + ".json")
		if err != nil {
			e, _ := os.ReadFile(base + ".err")
			ti.Error = toolTail(string(e), 3, 300)
			if ti.Error == "" {
				ti.Error = "geen resultaat"
			}
			out = append(out, ti)
			continue
		}
		var rep struct {
			Results []struct {
				Vulnerabilities []struct {
					VulnerabilityID  string
					PkgName          string
					InstalledVersion string
					FixedVersion     string
					Severity         string
					Title            string
				}
			}
		}
		if err := json.Unmarshal(b, &rep); err != nil {
			ti.Error = "uitvoer van trivy niet te lezen"
			out = append(out, ti)
			continue
		}
		seen := map[string]bool{}
		for _, r := range rep.Results {
			for _, v := range r.Vulnerabilities {
				k := v.VulnerabilityID + "|" + v.PkgName
				if seen[k] {
					continue
				}
				seen[k] = true
				ti.Counts[strings.ToLower(v.Severity)]++
				t := v.Title
				if len(t) > 120 {
					t = t[:117] + "…"
				}
				ti.Vulns = append(ti.Vulns, protocol.TrivyVuln{ID: v.VulnerabilityID, Pkg: v.PkgName, Version: v.InstalledVersion, Fixed: v.FixedVersion,
					Severity: strings.ToLower(v.Severity), Title: t})
			}
		}
		slices.SortStableFunc(ti.Vulns, func(x, y protocol.TrivyVuln) int {
			return rank[strings.ToUpper(x.Severity)] - rank[strings.ToUpper(y.Severity)]
		})
		if len(ti.Vulns) > 20 {
			ti.Vulns = ti.Vulns[:20]
		}
		out = append(out, ti)
	}
	return out
}
