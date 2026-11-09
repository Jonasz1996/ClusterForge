package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// RunExecutor voert één run uit: cf-agent run-exec <id>, gestart door de
// agent met systemd-run. De stand komt in status.json en de uitvoer in
// output.log, in de map van de run.
type RunExecutor struct {
	// Root is de bestandssysteemwortel; leeg is "/". Tests gebruiken een map.
	Root string
	// Now is de klok; nil is time.Now.
	Now func() time.Time
	// Cmd draait argv als user (leeg is de eigen gebruiker) met extra
	// omgevingsvariabelen, stdout en stderr naar out; afloop van ctx stopt
	// de hele procesgroep. nil draait het echt.
	Cmd func(ctx context.Context, user string, argv, env []string, out io.Writer) (int, error)
	// Stderr krijgt wat run-exec zelf meldt; nil is os.Stderr.
	Stderr io.Writer
}

func (r *RunExecutor) path(p string) string {
	if r.Root == "" {
		return p
	}
	return filepath.Join(r.Root, p)
}

func (r *RunExecutor) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *RunExecutor) cmd(ctx context.Context, user string, argv, env []string, out io.Writer) (int, error) {
	if r.Cmd != nil {
		return r.Cmd(ctx, user, argv, env, out)
	}
	return runArgvEnv(ctx, user, argv, env, out)
}

func (r *RunExecutor) logf(format string, args ...any) {
	w := r.Stderr
	if w == nil {
		w = os.Stderr
	}
	_, _ = fmt.Fprintf(w, format+"\n", args...)
}

// runDir is de map van een run, onder root.
func runDir(root, id string) string {
	p := path.Join(protocol.RunDir, id)
	if root == "" {
		return p
	}
	return filepath.Join(root, p)
}

func readRunStatus(dir string) (protocol.RunStatus, error) {
	var st protocol.RunStatus
	data, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(data, &st)
	return st, err
}

func writeRunStatus(dir string, st protocol.RunStatus) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "status.json"), data, 0o600)
}

// errStep is een stap van de run die mislukte, met zijn exitcode.
type errStep struct {
	what string
	exit int
}

func (e *errStep) Error() string { return fmt.Sprintf("%s eindigde met exitcode %d", e.what, e.exit) }

// Main voert de run uit en geeft 0 als hij lukte.
func (r *RunExecutor) Main(ctx context.Context, id string) int {
	if !protocol.ValidRunID(id) {
		r.logf("ongeldig run-id %q", id)
		return 2
	}
	dir := runDir(r.Root, id)
	data, err := os.ReadFile(filepath.Join(dir, "spec.json"))
	if err != nil {
		r.logf("run %s staat niet op deze node: %v", id, err)
		return 2
	}
	var spec protocol.RunSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		r.logf("run %s is onleesbaar: %v", id, err)
		return 2
	}
	st, _ := readRunStatus(dir)
	if st.Done() {
		r.logf("run %s is al afgelopen (%s)", id, st.State)
		return 0
	}
	if st.StartedAt.IsZero() {
		st.StartedAt = r.now().UTC()
	}
	st.State = protocol.RunRunning
	_ = writeRunStatus(dir, st)

	out, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		st.State, st.Error = protocol.RunFailed, "uitvoer: "+err.Error()
		r.finish(dir, &st, nil)
		return 1
	}
	defer func() { _ = out.Close() }()
	if spec.Title != "" {
		_, _ = fmt.Fprintf(out, "== %s\n", spec.Title)
	}

	timeout := spec.Timeout()
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var exit int
	switch err := spec.Validate(); {
	case err != nil:
		st.Error = err.Error()
		exit = -1
	case spec.Kind == protocol.RunScript:
		exit, err = r.script(rctx, dir, spec, out)
		if err != nil {
			st.Error = err.Error()
		}
	case spec.Kind == protocol.RunApt:
		exit, err = r.apt(rctx, id, spec.Apt, out, &st)
		if err != nil {
			st.Error = err.Error()
		}
	case spec.Kind == protocol.RunDocker:
		exit, err = r.docker(rctx, spec.Docker, out)
		if err != nil {
			st.Error = err.Error()
		}
	}
	timedOut := errors.Is(rctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	stopped := ctx.Err() != nil
	switch {
	case timedOut:
		st.State = protocol.RunTimeout
		_, _ = fmt.Fprintf(out, "\nClusterForge: gestopt na de tijdslimiet van %s\n", timeout)
	case stopped:
		st.State = protocol.RunStopped
		_, _ = fmt.Fprintf(out, "\nClusterForge: met de hand gestopt\n")
	case exit == 0 && st.Error == "":
		st.State = protocol.RunOK
	default:
		st.State = protocol.RunFailed
		if st.Error != "" {
			_, _ = fmt.Fprintf(out, "\nClusterForge: %s\n", st.Error)
		}
	}
	r.finish(dir, &st, &exit)
	if st.State == protocol.RunOK {
		return 0
	}
	return 1
}

func (r *RunExecutor) finish(dir string, st *protocol.RunStatus, exit *int) {
	end := r.now().UTC()
	st.EndedAt, st.Exit = &end, exit
	for _, p := range []string{"/run/reboot-required", "/var/run/reboot-required"} {
		if _, err := os.Stat(r.path(p)); err == nil {
			st.RebootRequired = true
		}
	}
	if err := writeRunStatus(dir, *st); err != nil {
		r.logf("stand bewaren: %v", err)
	}
}

// script schrijft het script naast de run en draait het als de gebruiker.
// Met een #!-regel kiest het script zelf zijn interpreter; anders /bin/sh.
func (r *RunExecutor) script(ctx context.Context, dir string, spec protocol.RunSpec, out io.Writer) (int, error) {
	file := filepath.Join(dir, "script")
	body := spec.Script
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(file, []byte(body), 0o700); err != nil {
		return -1, err
	}
	if spec.User != "" && spec.User != "root" {
		// De gebruiker moet het script kunnen lezen; de map zelf geeft
		// alleen doorgang.
		if err := chownTo(file, spec.User); err != nil {
			return -1, err
		}
		_ = os.Chmod(dir, 0o711)
	}
	argv := []string{"/bin/sh", file}
	if strings.HasPrefix(body, "#!") {
		argv = []string{file}
	}
	return r.cmd(ctx, spec.User, argv, nil, out)
}

var aptEnv = []string{"DEBIAN_FRONTEND=noninteractive", "APT_LISTCHANGES_FRONTEND=none", "NEEDRESTART_MODE=a"}

// step draait één commando met een kopregel en geeft een fout als het niet
// met 0 eindigt.
func (r *RunExecutor) step(ctx context.Context, out io.Writer, env []string, argv ...string) (int, error) {
	_, _ = fmt.Fprintf(out, "\n$ %s\n", strings.Join(argv, " "))
	code, err := r.cmd(ctx, "", argv, env, out)
	if err != nil {
		return code, err
	}
	if code != 0 {
		return code, &errStep{argv[0] + " " + argv[1], code}
	}
	return 0, nil
}

// capture draait een commando zonder kopregel en geeft de uitvoer.
func (r *RunExecutor) capture(ctx context.Context, argv ...string) ([]byte, int, error) {
	var buf bytes.Buffer
	code, err := r.cmd(ctx, "", argv, aptEnv, &buf)
	return buf.Bytes(), code, err
}

type runHolds struct {
	RunID    string   `json:"run_id"`
	Packages []string `json:"packages"`
}

// apt werkt de pakketten bij. Een kapotte pakketbron buiten Debian is een
// waarschuwing, zoals bij een deploystap; dpkg dat onderbroken was, wordt
// eerst afgemaakt.
func (r *RunExecutor) apt(ctx context.Context, id string, a *protocol.AptRun, out io.Writer, st *protocol.RunStatus) (int, error) {
	// apt-get update, met de uitvoer ook in een buffer voor de bronnen.
	var buf bytes.Buffer
	_, _ = fmt.Fprintf(out, "$ apt-get update\n")
	code, err := r.cmd(ctx, "", []string{"apt-get", "update", "-q"}, aptEnv, io.MultiWriter(out, &buf))
	if err != nil {
		return code, err
	}
	if code != 0 {
		broken, ok := brokenSources(buf.Bytes())
		if !ok || slices.ContainsFunc(broken, func(b aptSource) bool { return strings.Contains(strings.ToLower(b.uri), "debian.org") }) {
			return code, &errStep{"apt-get update", code}
		}
		for _, b := range broken {
			_, _ = fmt.Fprintf(out, "ClusterForge: waarschuwing: de pakketbron %s %s werkt niet (%s). Die bron hoort niet bij Debian; de pakketten ervan worden nu niet bijgewerkt.\n", b.uri, b.suite, b.reason)
		}
	}
	if audit, _, _ := r.capture(ctx, "dpkg", "--audit"); len(bytes.TrimSpace(audit)) > 0 {
		_, _ = fmt.Fprintf(out, "ClusterForge: dpkg was onderbroken; eerst dpkg --configure -a\n")
		if code, err := r.step(ctx, out, aptEnv, "dpkg", "--configure", "-a", "--force-confdef", "--force-confold"); err != nil {
			return code, err
		}
	}
	held, err := r.hold(ctx, id, a.Hold, out)
	defer r.unhold(id, held, out)
	if err != nil {
		return -1, err
	}
	st.Held = held
	verb := "upgrade"
	if a.Full {
		verb = "full-upgrade"
	}
	if code, err := r.step(ctx, out, aptEnv, "apt-get", verb, "-y", "-q",
		"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"); err != nil {
		return code, err
	}
	if a.Autoremove {
		if code, err := r.step(ctx, out, aptEnv, "apt-get", "autoremove", "-y", "-q"); err != nil {
			return code, err
		}
	}
	return 0, nil
}

// hold zet een apt-mark hold op de geïnstalleerde pakketten die op een
// patroon lijken en nog niet vastgehouden werden, en onthoudt ze in
// RunHoldsFile.
func (r *RunExecutor) hold(ctx context.Context, id string, patterns []string, out io.Writer) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	var found []string
	for _, p := range patterns {
		data, _, _ := r.capture(ctx, "dpkg-query", "-W", "-f", "${binary:Package} ${db:Status-Status}\\n", p)
		for l := range strings.Lines(string(data)) {
			f := strings.Fields(l)
			if len(f) == 2 && f[1] == "installed" && !slices.Contains(found, f[0]) {
				found = append(found, f[0])
			}
		}
	}
	data, _, _ := r.capture(ctx, "apt-mark", "showhold")
	already := strings.Fields(string(data))
	var todo []string
	for _, p := range found {
		if !slices.Contains(already, p) {
			todo = append(todo, p)
		}
	}
	if len(todo) == 0 {
		if len(found) > 0 {
			_, _ = fmt.Fprintf(out, "ClusterForge: blijven staan (al vastgehouden): %s\n", strings.Join(found, ", "))
		}
		return nil, nil
	}
	slices.Sort(todo)
	b, _ := json.Marshal(runHolds{RunID: id, Packages: todo})
	if err := writeFileAtomic(r.path(protocol.RunHoldsFile), b, 0o600); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(out, "ClusterForge: deze pakketten blijven tijdens de update staan: %s\n", strings.Join(todo, ", "))
	if _, err := r.step(ctx, out, aptEnv, append([]string{"apt-mark", "hold"}, todo...)...); err != nil {
		return todo, err
	}
	return todo, nil
}

// unhold haalt de holds van deze run weer weg, ook als de context al
// afgelopen is.
func (r *RunExecutor) unhold(id string, held []string, out io.Writer) {
	if len(held) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := r.step(ctx, out, aptEnv, append([]string{"apt-mark", "unhold"}, held...)...); err != nil {
		_, _ = fmt.Fprintf(out, "ClusterForge: de holds weghalen mislukte (%v); de agent probeert het bij zijn volgende start opnieuw\n", err)
		return
	}
	_ = os.Remove(r.path(protocol.RunHoldsFile))
}

// docker werkt de Compose-projecten bij: pull en up -d per project.
func (r *RunExecutor) docker(ctx context.Context, d *protocol.DockerRun, out io.Writer) (int, error) {
	data, code, err := r.capture(ctx, "docker", "compose", "ls", "-a", "--format", "json")
	if err != nil || code != 0 {
		_, _ = out.Write(data)
		if err == nil {
			err = &errStep{"docker compose", code}
		}
		return code, fmt.Errorf("docker compose ls: %w", err)
	}
	projects := parseComposeLs(data)
	var todo []protocol.ComposeProject
	for _, want := range d.Projects {
		i := slices.IndexFunc(projects, func(p protocol.ComposeProject) bool { return p.Name == want })
		if i < 0 {
			return -1, fmt.Errorf("Compose-project %s staat niet op deze node", want)
		}
		todo = append(todo, projects[i])
	}
	if len(d.Projects) == 0 {
		todo = projects
	}
	if len(todo) == 0 {
		_, _ = fmt.Fprintf(out, "ClusterForge: geen Docker Compose-projecten op deze node\n")
	}
	for _, p := range todo {
		base := []string{"docker", "compose", "-p", p.Name}
		for _, f := range p.ConfigFiles {
			base = append(base, "-f", f)
		}
		_, _ = fmt.Fprintf(out, "\n== %s\n", p.Name)
		if code, err := r.step(ctx, out, nil, append(slices.Clone(base), "pull")...); err != nil {
			return code, fmt.Errorf("%s: %w", p.Name, err)
		}
		if code, err := r.step(ctx, out, nil, append(slices.Clone(base), "up", "-d")...); err != nil {
			return code, fmt.Errorf("%s: %w", p.Name, err)
		}
	}
	if d.Prune {
		if code, err := r.step(ctx, out, nil, "docker", "image", "prune", "-f"); err != nil {
			return code, err
		}
	}
	return 0, nil
}

// parseComposeLs leest docker compose ls --format json.
func parseComposeLs(data []byte) []protocol.ComposeProject {
	var raw []struct {
		Name        string `json:"Name"`
		Status      string `json:"Status"`
		ConfigFiles string `json:"ConfigFiles"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &raw); err != nil {
		return nil
	}
	out := make([]protocol.ComposeProject, 0, len(raw))
	for _, p := range raw {
		cp := protocol.ComposeProject{Name: p.Name, Status: p.Status}
		for f := range strings.SplitSeq(p.ConfigFiles, ",") {
			if f = strings.TrimSpace(f); f != "" {
				cp.ConfigFiles = append(cp.ConfigFiles, f)
			}
		}
		out = append(out, cp)
	}
	slices.SortFunc(out, func(a, b protocol.ComposeProject) int { return strings.Compare(a.Name, b.Name) })
	return out
}
