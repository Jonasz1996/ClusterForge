package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// applyTimeout is hoe lang alle stappen van één apply-commando samen mogen
// duren; de server stuurt meestal één stap per commando.
const applyTimeout = 14 * time.Minute

var (
	packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)
	unitName    = regexp.MustCompile(`^[a-zA-Z0-9@._:-]+$`)
	userName    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// apply voert de stappen in volgorde uit en stopt bij de eerste fout.
func (a *Agent) apply(ctx context.Context, steps []protocol.Step) protocol.Result {
	if len(steps) == 0 {
		return failed(nil, "geen stappen")
	}
	res := protocol.Result{OK: true}
	for i, s := range steps {
		changed, out, err := a.applyStep(ctx, s)
		sr := protocol.StepResult{Changed: changed, Output: out}
		if err != nil {
			sr.Error = err.Error()
		}
		res.Steps = append(res.Steps, sr)
		if err != nil {
			res.OK = false
			res.Error = fmt.Sprintf("stap %d (%s): %v", i+1, s.Kind(), err)
			return res
		}
	}
	return res
}

func (a *Agent) applyStep(ctx context.Context, s protocol.Step) (bool, []string, error) {
	switch s.Kind() {
	case "package":
		return a.applyPackage(ctx, s.Package)
	case "file":
		return a.applyFile(ctx, s.File)
	case "service":
		return a.applyService(ctx, s.Service)
	case "user":
		return a.applyUser(ctx, s.User)
	case "directory":
		return a.applyDirectory(s.Directory)
	case "command":
		return a.applyCommand(ctx, s.Command)
	}
	return false, nil, errors.New("onbekende of lege stap")
}

// path zet een absoluut pad om naar het bestandssysteem van de agent.
func (a *Agent) path(p string) (string, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return "", fmt.Errorf("pad %q moet absoluut en zonder .. zijn", p)
	}
	if a.Root == "" {
		return p, nil
	}
	return filepath.Join(a.Root, p), nil
}

func (a *Agent) applyPackage(ctx context.Context, p *protocol.PackageStep) (bool, []string, error) {
	if len(p.Names) == 0 {
		return false, nil, errors.New("geen pakketten")
	}
	states, err := a.packageStates(ctx, p.Names)
	if err != nil {
		return false, nil, err
	}
	var todo []string
	for _, n := range p.Names {
		if states[n].Installed != (p.State == "absent") {
			continue
		}
		todo = append(todo, n)
	}
	list := strings.Join(p.Names, ", ")
	switch {
	case len(todo) == 0 && p.State == "absent":
		return false, []string{list + ": niet geïnstalleerd"}, nil
	case len(todo) == 0:
		return false, []string{list + ": al geïnstalleerd"}, nil
	case p.State == "absent":
		out, err := a.execTimeout(ctx, 10*time.Minute, "env", append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "remove", "-y", "-q"}, todo...)...)
		if err != nil {
			return false, tail(out), aptError("apt-get remove", out, err)
		}
		return true, append(tail(out), "verwijderd: "+strings.Join(todo, ", ")), nil
	case p.State != "" && p.State != "present":
		return false, nil, fmt.Errorf("onbekende state %q", p.State)
	}
	lines, err := a.aptUpdate(ctx, todo)
	if err != nil {
		return false, lines, err
	}
	args := []string{
		"DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "-q", "--no-install-recommends",
		"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
	}
	out, err := a.execTimeout(ctx, 10*time.Minute, "env", append(args, todo...)...)
	lines = append(lines, tail(out)...)
	if err != nil {
		return false, lines, aptError("apt-get install", out, err)
	}
	return true, append(lines, "geïnstalleerd: "+strings.Join(todo, ", ")), nil
}

// aptUpdate draait apt-get update. Faalt daarbij alleen een pakketbron
// buiten Debian die geen van pkgs aanbiedt, zoals een kapotte bron van
// Zabbix, dan is dat een waarschuwing en gaat de installatie door met de
// lijsten die apt wel heeft. Een kapotte bron van Debian, een bron die een
// van pkgs aanbiedt, of een andere fout (een slot, een volle schijf) blijft
// een fout: dan zou apt een oude of verkeerde versie kunnen kiezen.
func (a *Agent) aptUpdate(ctx context.Context, pkgs []string) ([]string, error) {
	out, err := a.execTimeout(ctx, 5*time.Minute, "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update", "-q")
	if err == nil {
		return nil, nil
	}
	fail := aptError("apt-get update", out, err)
	broken, ok := brokenSources(out)
	if !ok {
		return tail(out), fail
	}
	for _, b := range broken {
		if strings.Contains(strings.ToLower(b.uri), "debian.org") {
			return tail(out), fail
		}
	}
	policy, perr := a.execTimeout(ctx, time.Minute, "apt-cache", append([]string{"policy"}, pkgs...)...)
	if perr != nil {
		return tail(out), fail
	}
	var lines []string
	for _, b := range broken {
		if strings.Contains(string(policy), " "+b.uri+" ") {
			return tail(out), fmt.Errorf("%w (de pakketbron %s %s biedt %s aan)", fail, b.uri, b.suite, strings.Join(pkgs, ", "))
		}
		lines = append(lines, fmt.Sprintf("waarschuwing: de pakketbron %s %s werkt niet (%s). Die bron hoort niet bij Debian en biedt deze pakketten niet aan, dus de installatie gaat door. Zet de bron goed of haal hem weg.", b.uri, b.suite, b.reason))
	}
	return lines, nil
}

type aptSource struct{ uri, suite, reason string }

// brokenSources haalt de pakketbronnen uit de uitvoer van een mislukte
// apt-get update (regels "Err:7 <uri> <suite> Release", met de reden op de
// regel eronder). ok is false als er geen bron faalde, of als apt een fout
// meldt die niet over die bronnen gaat.
func brokenSources(out []byte) (srcs []aptSource, ok bool) {
	var raw []string
	for l := range strings.Lines(string(out)) {
		if l = strings.TrimRight(l, " \t\r\n"); strings.TrimSpace(l) != "" {
			raw = append(raw, l)
		}
	}
	lines := outputLines(out)
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) < 3 || !strings.HasPrefix(f[0], "Err:") {
			continue
		}
		s := aptSource{uri: f[1], suite: f[2], reason: "fout bij ophalen"}
		// De reden staat ingesprongen op de regel eronder.
		if i+1 < len(raw) && len(raw[i+1])-len(strings.TrimLeft(raw[i+1], " \t")) > len(raw[i])-len(strings.TrimLeft(raw[i], " \t")) {
			s.reason = strings.Join(strings.Fields(raw[i+1]), " ")
		}
		if !slices.ContainsFunc(srcs, func(o aptSource) bool { return o.uri == s.uri && o.suite == s.suite }) {
			srcs = append(srcs, s)
		}
	}
	if len(srcs) == 0 {
		return nil, false
	}
	for _, l := range lines {
		msg, isErr := strings.CutPrefix(l, "E: ")
		if !isErr || strings.HasPrefix(msg, "Some index files failed to download") {
			continue
		}
		if !slices.ContainsFunc(srcs, func(s aptSource) bool { return strings.Contains(msg, s.uri+" ") || strings.Contains(msg, s.uri+"/") }) {
			return nil, false
		}
	}
	return srcs, true
}

// aptError noemt de laatste foutregel van apt (E: ...), die meestal zegt
// wat er misging.
func aptError(what string, out []byte, err error) error {
	lines := outputLines(out)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "E: ") {
			return fmt.Errorf("%s: %s", what, strings.TrimPrefix(lines[i], "E: "))
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// tail houdt de laatste regels van lange uitvoer, zoals die van apt.
func tail(out []byte) []string {
	lines := outputLines(out)
	if len(lines) > 15 {
		lines = append([]string{"…"}, lines[len(lines)-15:]...)
	}
	return lines
}

func parseMode(s string, def fs.FileMode) (fs.FileMode, error) {
	if s == "" {
		return def, nil
	}
	m, err := strconv.ParseUint(s, 8, 32)
	if err != nil || m > 0o7777 {
		return 0, fmt.Errorf("ongeldige mode %q", s)
	}
	return fs.FileMode(m), nil
}

// owner zoekt uid en gid op; -1 laat ze ongemoeid.
func owner(name, group string) (uid, gid int, err error) {
	uid, gid = -1, -1
	if name != "" {
		u, err := user.Lookup(name)
		if err != nil {
			return 0, 0, fmt.Errorf("gebruiker %s: %w", name, err)
		}
		uid, _ = strconv.Atoi(u.Uid)
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return 0, 0, fmt.Errorf("groep %s: %w", group, err)
		}
		gid, _ = strconv.Atoi(g.Gid)
	}
	return uid, gid, nil
}

// ownedBy is true als het bestand al de gevraagde eigenaar heeft.
func ownedBy(fi fs.FileInfo, uid, gid int) bool {
	u, g, ok := fileOwner(fi)
	if !ok {
		return true
	}
	return (uid < 0 || u == uid) && (gid < 0 || g == gid)
}

// unitDir is de map met de eigen systemd-units van de beheerder; een
// template zet daar zijn units, zoals cf-app.service.
const unitDir = "/etc/systemd/system/"

// isUnitFile is true voor een unit of een drop-in onder /etc/systemd/system.
// systemd leest die pas na daemon-reload opnieuw.
func isUnitFile(p string) bool {
	if !strings.HasPrefix(p, unitDir) {
		return false
	}
	switch filepath.Ext(p) {
	case ".service", ".socket", ".timer", ".path", ".mount", ".target", ".conf":
		return true
	}
	return false
}

func (a *Agent) applyFile(ctx context.Context, f *protocol.FileStep) (bool, []string, error) {
	path, err := a.path(f.Path)
	if err != nil {
		return false, nil, err
	}
	mode, err := parseMode(f.Mode, 0o644)
	if err != nil {
		return false, nil, err
	}
	uid, gid, err := owner(f.Owner, f.Group)
	if err != nil {
		return false, nil, err
	}
	content := []byte(f.Content)
	old, rerr := os.ReadFile(path)
	if rerr == nil {
		fi, err := os.Stat(path)
		if err != nil {
			return false, nil, err
		}
		if bytes.Equal(old, content) && fi.Mode().Perm() == mode.Perm() && ownedBy(fi, uid, gid) {
			return false, []string{f.Path + ": ongewijzigd"}, nil
		}
	} else if !errors.Is(rerr, fs.ErrNotExist) {
		return false, nil, rerr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, nil, err
	}
	if err := writeFileAtomic(path, content, mode); err != nil {
		return false, nil, err
	}
	if uid >= 0 || gid >= 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			return false, nil, err
		}
	}
	verb := "bijgewerkt"
	if rerr != nil {
		verb = "aangemaakt"
	}
	lines := []string{fmt.Sprintf("%s: %s (%d regels)", f.Path, verb, strings.Count(f.Content, "\n"))}
	// Een gewijzigde unit geldt pas als systemd haar opnieuw leest; een
	// herstart daarna gebruikt dan de nieuwe versie.
	if isUnitFile(f.Path) {
		out, err := a.exec(ctx, "systemctl", "daemon-reload")
		if err != nil {
			return true, append(lines, outputLines(out)...), fmt.Errorf("systemctl daemon-reload: %w", err)
		}
		lines = append(lines, "systemctl daemon-reload")
	}
	return true, lines, nil
}

func (a *Agent) applyService(ctx context.Context, s *protocol.ServiceStep) (bool, []string, error) {
	if !unitName.MatchString(s.Name) {
		return false, nil, fmt.Errorf("ongeldige servicenaam %q", s.Name)
	}
	loaded, enabled, active, err := a.unit(ctx, s.Name)
	if err != nil {
		return false, nil, err
	}
	if !loaded {
		return false, nil, fmt.Errorf("service %s bestaat niet op deze node", s.Name)
	}
	var verbs []string
	if s.Enabled != nil && *s.Enabled != enabled {
		verbs = append(verbs, map[bool]string{true: "enable", false: "disable"}[*s.Enabled])
	}
	switch s.State {
	case "":
	case "started":
		if !active {
			verbs = append(verbs, "start")
		}
	case "stopped":
		if active {
			verbs = append(verbs, "stop")
		}
	case "restarted":
		verbs = append(verbs, "restart")
	case "reloaded":
		if active {
			verbs = append(verbs, "reload-or-restart")
		} else {
			verbs = append(verbs, "start")
		}
	default:
		return false, nil, fmt.Errorf("onbekende state %q", s.State)
	}
	if len(verbs) == 0 {
		return false, []string{s.Name + ": ongewijzigd"}, nil
	}
	var lines []string
	for _, v := range verbs {
		out, err := a.exec(ctx, "systemctl", v, s.Name)
		lines = append(lines, outputLines(out)...)
		if err != nil {
			// De laatste regels van het journal zeggen meestal waarom.
			if j, jerr := a.exec(ctx, "journalctl", "-u", s.Name, "-n", "10", "--no-pager", "-q"); jerr == nil {
				lines = append(lines, outputLines(j)...)
			}
			return false, lines, fmt.Errorf("systemctl %s %s: %w", v, s.Name, err)
		}
		lines = append(lines, fmt.Sprintf("systemctl %s %s", v, s.Name))
	}
	return true, lines, nil
}

func (a *Agent) applyUser(ctx context.Context, u *protocol.UserStep) (bool, []string, error) {
	exists, err := a.userExists(ctx, u.Name)
	if err != nil {
		return false, nil, err
	}
	if exists {
		return false, []string{"gebruiker " + u.Name + ": bestaat al"}, nil
	}
	args := []string{}
	if u.System {
		args = append(args, "--system")
	}
	if u.Home != "" {
		if !filepath.IsAbs(u.Home) {
			return false, nil, fmt.Errorf("home %q moet absoluut zijn", u.Home)
		}
		args = append(args, "--home-dir", u.Home, "--create-home")
	}
	shell := u.Shell
	if shell == "" && u.System {
		shell = "/usr/sbin/nologin"
	}
	if shell != "" {
		args = append(args, "--shell", shell)
	}
	out, err := a.exec(ctx, "useradd", append(args, u.Name)...)
	if err != nil {
		return false, outputLines(out), fmt.Errorf("useradd: %w", err)
	}
	return true, []string{"gebruiker " + u.Name + ": aangemaakt"}, nil
}

func (a *Agent) applyDirectory(d *protocol.DirectoryStep) (bool, []string, error) {
	path, err := a.path(d.Path)
	if err != nil {
		return false, nil, err
	}
	mode, err := parseMode(d.Mode, 0o755)
	if err != nil {
		return false, nil, err
	}
	uid, gid, err := owner(d.Owner, d.Group)
	if err != nil {
		return false, nil, err
	}
	fi, err := os.Stat(path)
	switch {
	case err == nil && !fi.IsDir():
		return false, nil, fmt.Errorf("%s bestaat al en is geen map", d.Path)
	case err == nil && fi.Mode().Perm() == mode.Perm() && ownedBy(fi, uid, gid):
		return false, []string{d.Path + ": ongewijzigd"}, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return false, nil, err
	}
	verb := "bijgewerkt"
	if err != nil {
		verb = "aangemaakt"
		if err := os.MkdirAll(path, mode); err != nil {
			return false, nil, err
		}
	}
	if err := os.Chmod(path, mode); err != nil {
		return false, nil, err
	}
	if uid >= 0 || gid >= 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			return false, nil, err
		}
	}
	return true, []string{d.Path + ": " + verb}, nil
}

func (a *Agent) applyCommand(ctx context.Context, c *protocol.CommandStep) (bool, []string, error) {
	if strings.TrimSpace(c.Run) == "" {
		return false, nil, errors.New("leeg commando")
	}
	switch {
	case c.Creates != "":
		p, err := a.path(c.Creates)
		if err != nil {
			return false, nil, err
		}
		if _, err := os.Stat(p); err == nil {
			return false, []string{"overgeslagen: " + c.Creates + " bestaat al"}, nil
		}
	case c.Unless != "":
		if _, err := a.exec(ctx, "sh", "-c", c.Unless); err == nil {
			return false, []string{"overgeslagen: " + c.Unless + " lukt al"}, nil
		}
	default:
		return false, nil, errors.New("een commando heeft creates of unless nodig")
	}
	out, err := a.execTimeout(ctx, 10*time.Minute, "sh", "-c", c.Run)
	if err != nil {
		return false, tail(out), fmt.Errorf("%s: %w", c.Run, err)
	}
	return true, tail(out), nil
}
