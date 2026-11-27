package agent

import (
	"context"
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

// Een run van de soort stack (fase 21): de bestanden van elke stack
// schrijven (alleen wat anders is, met een kopie van het oude in de map van
// de run), controleren met docker compose config, en de stack in de
// gewenste stand brengen. Na up wacht de run tot elke dienst gezond draait;
// lukt dat niet na nieuwe bestanden, dan gaan de oude bestanden terug.

func (r *RunExecutor) stackRun(ctx context.Context, dir string, sr *protocol.StackRun, out io.Writer, st *protocol.RunStatus) (int, error) {
	if sr.List != nil {
		p := r.path(protocol.StackListFile)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return -1, err
		}
		old, _ := os.ReadFile(p)
		if string(old) != *sr.List {
			if err := writeFileAtomic(p, []byte(*sr.List), 0o644); err != nil {
				return -1, fmt.Errorf("%s schrijven: %w", protocol.StackListFile, err)
			}
			_, _ = fmt.Fprintf(out, "ClusterForge: %s bijgewerkt (%d stacks voor keepalived)\n", protocol.StackListFile, len(protocol.ParseStackList(*sr.List)))
		}
	}
	failed := []string{}
	for _, s := range sr.Stacks {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		_, _ = fmt.Fprintf(out, "\n== %s (%s): %s\n", s.Name, s.Dir, wantText(s))
		rep := r.stackOne(ctx, dir, s, sr, out)
		rep.States = r.stackStates(ctx, s.Name)
		st.Stacks = append(st.Stacks, rep)
		if rep.Result == protocol.StackResultFailed {
			failed = append(failed, s.Name)
			_, _ = fmt.Fprintf(out, "ClusterForge: %s: %s\n", s.Name, rep.Problem)
		}
	}
	if len(failed) > 0 {
		return 1, fmt.Errorf("niet gelukt: %s", strings.Join(failed, ", "))
	}
	return 0, nil
}

func wantText(s protocol.StackApply) string {
	switch s.Want {
	case protocol.StackUp:
		if s.Pull {
			return "bijwerken en starten"
		}
		return "starten"
	case protocol.StackStop:
		return "stoppen"
	case protocol.StackRestart:
		return "herstarten"
	case protocol.StackDown:
		return "weghalen (down; data blijft)"
	}
	return "alleen de bestanden"
}

// stackArgs is docker compose voor een stack.
func stackArgs(s protocol.StackApply) []string {
	argv := []string{"docker", "compose", "-p", s.Name, "--project-directory", s.Dir}
	for _, f := range s.ComposeFiles {
		argv = append(argv, "-f", path.Join(s.Dir, f))
	}
	return argv
}

// writeStackFiles schrijft wat anders is en bewaart het oude in b.
func (r *RunExecutor) writeStackFiles(b *backup, s protocol.StackApply, out io.Writer) ([]string, []string, error) {
	var written, created []string
	for _, f := range s.Files {
		p := path.Join(s.Dir, f.Path)
		full := r.path(p)
		mode := os.FileMode(f.Mode)
		cur, err := os.ReadFile(full)
		switch {
		case err == nil:
			fi, _ := os.Stat(full)
			if mode == 0 {
				mode = fi.Mode().Perm()
			}
			if string(cur) == f.Content && fi.Mode().Perm() == mode {
				continue
			}
			if err := r.save(b, p); err != nil {
				return written, created, fmt.Errorf("kopie van %s: %w", p, err)
			}
		case os.IsNotExist(err):
			created = append(created, p)
		default:
			return written, created, err
		}
		if mode == 0 {
			mode = 0o644
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return written, created, err
		}
		if err := writeFileAtomic(full, []byte(f.Content), mode); err != nil {
			return written, created, fmt.Errorf("%s schrijven: %w", p, err)
		}
		_, _ = fmt.Fprintf(out, "ClusterForge: %s geschreven\n", p)
		written = append(written, f.Path)
	}
	return written, created, nil
}

func (r *RunExecutor) undoStackFiles(b *backup, created []string, out io.Writer) {
	r.restore(b, out)
	for _, p := range created {
		if err := os.Remove(r.path(p)); err == nil {
			_, _ = fmt.Fprintf(out, "ClusterForge: %s weer weggehaald\n", p)
		}
	}
}

func (r *RunExecutor) stackOne(ctx context.Context, dir string, s protocol.StackApply, sr *protocol.StackRun, out io.Writer) protocol.StackReport {
	rep := protocol.StackReport{Name: s.Name, Result: protocol.StackResultOK}
	fail := func(format string, args ...any) protocol.StackReport {
		rep.Result, rep.Problem = protocol.StackResultFailed, fmt.Sprintf(format, args...)
		return rep
	}
	if err := os.MkdirAll(r.path(s.Dir), 0o755); err != nil {
		return fail("map %s: %v", s.Dir, err)
	}
	b := &backup{dir: filepath.Join(dir, "backup", s.Name), files: map[string]string{}}
	written, created, err := r.writeStackFiles(b, s, out)
	rep.Written = written
	if err != nil {
		r.undoStackFiles(b, created, out)
		return fail("%v", err)
	}
	for _, f := range s.ComposeFiles {
		if !r.exists(path.Join(s.Dir, f)) {
			return fail("het compose-bestand %s/%s bestaat niet op deze node", s.Dir, f)
		}
	}
	base := stackArgs(s)
	if len(written) > 0 {
		if _, err := r.step(ctx, out, nil, append(slices.Clone(base), "config", "-q")...); err != nil {
			r.undoStackFiles(b, created, out)
			return fail("de bestanden kloppen niet volgens docker compose config; de vorige bestanden staan terug")
		}
	}
	svc := s.Services
	restarts := r.stackRestarts(ctx, s.Name)
	// Bijwerken op een node waar de stack niet draait (een reserve bij
	// plaatsing vip): alleen de nieuwe images ophalen, zodat een failover
	// meteen de nieuwe versie start.
	if s.Pull && (s.Want == protocol.StackFiles || s.Want == protocol.StackStop) {
		if err := r.composePull(ctx, out, base, nil); err != nil {
			return fail("docker compose pull mislukte")
		}
	}
	switch s.Want {
	case protocol.StackFiles:
		if len(written) == 0 {
			rep.Result = protocol.StackResultUnchanged
		}
		return rep
	case protocol.StackStop:
		if _, err := r.step(ctx, out, nil, append(append(slices.Clone(base), "stop"), svc...)...); err != nil {
			return fail("docker compose stop mislukte")
		}
		return rep
	case protocol.StackRestart:
		if _, err := r.step(ctx, out, nil, append(append(slices.Clone(base), "restart"), svc...)...); err != nil {
			return fail("docker compose restart mislukte")
		}
	case protocol.StackDown:
		if _, err := r.step(ctx, out, nil, append(slices.Clone(base), "down")...); err != nil {
			return fail("docker compose down mislukte")
		}
		return rep
	case protocol.StackUp:
		want := r.stackServices(ctx, base, svc)
		if len(written) == 0 && !s.Pull && r.allRunning(ctx, s.Name, want) {
			_, _ = fmt.Fprintf(out, "ClusterForge: %s draait al zoals gevraagd\n", s.Name)
			rep.Result = protocol.StackResultUnchanged
			return rep
		}
		if s.Pull {
			// Diensten met build: hebben geen image om op te halen.
			if err := r.composePull(ctx, out, base, svc); err != nil {
				return fail("docker compose pull mislukte; er is niets veranderd")
			}
		}
		if _, err := r.step(ctx, out, nil, append(append(slices.Clone(base), "up", "-d", "--remove-orphans"), svc...)...); err != nil {
			if len(written) > 0 {
				r.undoStackFiles(b, created, out)
				_, _ = r.step(ctx, out, nil, append(slices.Clone(base), "up", "-d", "--remove-orphans")...)
				return fail("docker compose up -d mislukte; de vorige bestanden staan terug")
			}
			return fail("docker compose up -d mislukte")
		}
	}
	// Testen na up of restart.
	ok, problem := r.stackWait(ctx, s.Name, r.stackServices(ctx, base, svc), restarts, sr.HealthSeconds, out)
	if logs := r.composeLogs(ctx, base); logs != "" && !ok {
		_, _ = fmt.Fprintf(out, "\n-- docker compose logs (laatste regels) --\n%s\n", tailLines(logs, 40))
	}
	if ok {
		_, _ = fmt.Fprintf(out, "ClusterForge: %s draait, elke container is gezond\n", s.Name)
		return rep
	}
	if len(written) > 0 && s.Want == protocol.StackUp {
		_, _ = fmt.Fprintf(out, "\nClusterForge: %s; de vorige bestanden terugzetten\n", problem)
		r.undoStackFiles(b, created, out)
		_, _ = r.step(ctx, out, nil, append(slices.Clone(base), "up", "-d", "--remove-orphans")...)
		return fail("%s; de vorige bestanden staan terug", problem)
	}
	return fail("%s", problem)
}

// stackServices zijn de diensten die moeten draaien: svc, of elke dienst
// van de compose-bestanden.
func (r *RunExecutor) stackServices(ctx context.Context, base, svc []string) []string {
	if len(svc) > 0 {
		return svc
	}
	data, err := r.runQuiet(ctx, append(slices.Clone(base), "config", "--services")...)
	if err != nil {
		return nil
	}
	var out []string
	for l := range strings.SplitSeq(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, " ") {
			out = append(out, l)
		}
	}
	return out
}

// allRunning is true als elke dienst draait en niet unhealthy is.
func (r *RunExecutor) allRunning(ctx context.Context, project string, want []string) bool {
	if len(want) == 0 {
		return false
	}
	cur, err := discoverCompose(ctx, r.runQuiet, r.exists, project)
	if err != nil || len(cur) == 0 {
		return false
	}
	got := map[string]protocol.ComposeService{}
	for _, s := range cur[0].Services {
		got[s.Service] = s
	}
	for _, w := range want {
		s, ok := got[w]
		if !ok || s.State != "running" || s.Health == "unhealthy" {
			return false
		}
	}
	return true
}

func (r *RunExecutor) stackStates(ctx context.Context, project string) map[string]string {
	cur, err := discoverCompose(context.WithoutCancel(ctx), r.runQuiet, r.exists, project)
	if err != nil || len(cur) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, s := range cur[0].Services {
		v := s.State
		if s.Health != "" {
			v += " (" + s.Health + ")"
		}
		out[s.Container] = v
	}
	return out
}

// stackRestarts geeft per dienst het aantal herstarts nu, zodat de test
// alleen nieuwe herstarts telt (een container die niet opnieuw gemaakt
// wordt, houdt zijn teller).
func (r *RunExecutor) stackRestarts(ctx context.Context, project string) map[string]int {
	out := map[string]int{}
	cur, err := discoverCompose(ctx, r.runQuiet, r.exists, project)
	if err != nil || len(cur) == 0 {
		return out
	}
	for _, s := range cur[0].Services {
		out[s.Service+"/"+s.Container] = s.Restarts
	}
	return out
}

// stackWait wacht tot elke dienst in want aan één stuk DockerStable draait,
// zonder nieuwe herstarts en niet unhealthy.
func (r *RunExecutor) stackWait(ctx context.Context, project string, want []string, restarts map[string]int, seconds int, out io.Writer) (bool, string) {
	limit := protocol.DockerHealthDefault
	if seconds > 0 {
		limit = time.Duration(seconds) * time.Second
	}
	stable := protocol.DockerStable
	if r.Stable > 0 {
		stable = r.Stable
	}
	every := 2 * time.Second
	if r.Poll > 0 {
		every = r.Poll
	}
	if len(want) == 0 {
		return false, "docker compose config --services gaf geen diensten"
	}
	_, _ = fmt.Fprintf(out, "ClusterForge: testen of %s draait (hoogstens %s)\n", project, limit)
	deadline := r.now().Add(limit)
	var goodSince time.Time
	exited := map[string]int{}
	problem := ""
	for {
		now := r.now()
		problem = ""
		cur, err := discoverCompose(ctx, r.runQuiet, r.exists, project)
		got := map[string][]protocol.ComposeService{}
		if err != nil {
			problem = err.Error()
		} else {
			for _, p := range cur {
				for _, s := range p.Services {
					got[s.Service] = append(got[s.Service], s)
				}
			}
		}
		for _, name := range want {
			if problem != "" {
				break
			}
			cs := got[name]
			if len(cs) == 0 {
				problem = fmt.Sprintf("de container van %s bestaat niet", name)
				break
			}
			for _, c := range cs {
				switch {
				case c.State == "exited" || c.State == "dead":
					exited[name]++
					problem = fmt.Sprintf("%s is gestopt (%s)", name, c.State)
				case c.State != "running":
					problem = fmt.Sprintf("%s is %s", name, c.State)
				case c.Restarts > restarts[c.Service+"/"+c.Container]:
					problem = fmt.Sprintf("%s herstartte", name)
				case c.Health == "unhealthy":
					problem = fmt.Sprintf("%s is unhealthy volgens zijn healthcheck", name)
				case c.Health == "starting":
					problem = fmt.Sprintf("%s start nog (healthcheck)", name)
				}
				if problem != "" {
					break
				}
			}
		}
		if problem == "" {
			if goodSince.IsZero() {
				goodSince = now
			}
			if now.Sub(goodSince) >= stable {
				return true, ""
			}
		} else {
			goodSince = time.Time{}
			for _, n := range exited {
				if n >= 2 {
					return false, problem
				}
			}
		}
		if !now.Before(deadline) {
			return false, problem + fmt.Sprintf(" (na %s)", limit)
		}
		if err := r.sleep(ctx, every); err != nil {
			return false, "gestopt tijdens de test"
		}
	}
}
