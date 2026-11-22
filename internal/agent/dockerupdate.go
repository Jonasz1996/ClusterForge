package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Docker Compose bijwerken zoals in fase 15: de projecten ontdekken met
// docker ps -a en docker inspect, pull, alleen bij een nieuw image down en
// up -d, testen of alles weer draait, de logs lezen en bij een fout de
// vorige images en bestanden terugzetten.

// Labels die Docker Compose op elke container zet.
const (
	labelProject    = "com.docker.compose.project"
	labelWorkingDir = "com.docker.compose.project.working_dir"
	labelConfig     = "com.docker.compose.project.config_files"
	labelEnvFile    = "com.docker.compose.project.environment_file"
	labelService    = "com.docker.compose.service"
	labelOneOff     = "com.docker.compose.oneoff"
	labelOCISource  = "org.opencontainers.image.source"
	labelOCIVersion = "org.opencontainers.image.version"
)

// inspected is wat de agent uit docker inspect van een container leest.
type inspected struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Image string `json:"Image"`
	State struct {
		Status string `json:"Status"`
		Health *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	RestartCount int `json:"RestartCount"`
	Config       struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

// runFunc draait een commando en geeft de uitvoer; een exitcode die niet 0
// is, is een fout.
type runFunc func(ctx context.Context, argv ...string) ([]byte, error)

// discoverCompose vindt de Compose-projecten via de labels van de
// containers, ook als het project stilstaat. exists zegt of een bestand er
// is; project beperkt tot één project (leeg is alle).
func discoverCompose(ctx context.Context, run runFunc, exists func(string) bool, project string) ([]protocol.ComposeProject, error) {
	filter := "label=" + labelProject
	if project != "" {
		filter += "=" + project
	}
	ids, err := run(ctx, "docker", "ps", "-a", "-q", "--no-trunc", "--filter", filter)
	if err != nil {
		return nil, fmt.Errorf("docker ps -a: %w", err)
	}
	list := strings.Fields(string(ids))
	if len(list) == 0 {
		return []protocol.ComposeProject{}, nil
	}
	data, err := run(ctx, append([]string{"docker", "inspect"}, list...)...)
	if err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	var cs []inspected
	if err := json.Unmarshal(bytes.TrimSpace(data), &cs); err != nil {
		return nil, fmt.Errorf("docker inspect lezen: %w", err)
	}
	return groupCompose(cs, exists), nil
}

func splitList(s string) []string {
	var out []string
	for f := range strings.SplitSeq(s, ",") {
		if f = strings.TrimSpace(f); f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

func groupCompose(cs []inspected, exists func(string) bool) []protocol.ComposeProject {
	byName := map[string]*protocol.ComposeProject{}
	var names []string
	counts := map[string]map[string]int{}
	for _, c := range cs {
		l := c.Config.Labels
		name := l[labelProject]
		if name == "" || strings.EqualFold(l[labelOneOff], "true") {
			continue
		}
		p := byName[name]
		if p == nil {
			p = &protocol.ComposeProject{Name: name}
			byName[name], counts[name] = p, map[string]int{}
			names = append(names, name)
		}
		if p.WorkingDir == "" {
			p.WorkingDir = l[labelWorkingDir]
		}
		for _, f := range splitList(l[labelConfig]) {
			if !slices.Contains(p.ConfigFiles, f) {
				p.ConfigFiles = append(p.ConfigFiles, f)
			}
		}
		for _, f := range splitList(l[labelEnvFile]) {
			if !slices.Contains(p.EnvFiles, f) {
				p.EnvFiles = append(p.EnvFiles, f)
			}
		}
		svc := protocol.ComposeService{
			Service: l[labelService], Container: strings.TrimPrefix(c.Name, "/"), Image: c.Config.Image, ImageID: c.Image,
			State: c.State.Status, Restarts: c.RestartCount,
		}
		if c.State.Health != nil {
			svc.Health = c.State.Health.Status
		}
		for _, m := range c.Mounts {
			src := m.Source
			if m.Type == "volume" && m.Name != "" {
				src = m.Name
			}
			s := src + ":" + m.Destination
			if !m.RW {
				s += ":ro"
			}
			svc.Mounts = append(svc.Mounts, s)
		}
		p.Services = append(p.Services, svc)
		counts[name][c.State.Status]++
	}
	out := make([]protocol.ComposeProject, 0, len(names))
	for _, name := range names {
		p := byName[name]
		if p.WorkingDir != "" && len(p.EnvFiles) == 0 {
			if f := filepath.Join(p.WorkingDir, ".env"); exists(f) {
				p.EnvFiles = append(p.EnvFiles, f)
			}
		}
		for _, f := range p.ConfigFiles {
			if !exists(f) {
				p.Missing = append(p.Missing, f)
			}
		}
		var states []string
		for st, n := range counts[name] {
			states = append(states, fmt.Sprintf("%s(%d)", st, n))
		}
		slices.Sort(states)
		p.Status = strings.Join(states, ", ")
		slices.SortFunc(p.Services, func(a, b protocol.ComposeService) int {
			return strings.Compare(a.Service+a.Container, b.Service+b.Container)
		})
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b protocol.ComposeProject) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// running is true als de dienst draaide.
func running(s protocol.ComposeService) bool { return s.State == "running" || s.State == "restarting" }

// --- uitvoeren --------------------------------------------------------------

func (r *RunExecutor) exists(p string) bool {
	_, err := os.Stat(r.path(p))
	return err == nil
}

// runQuiet draait een commando zonder kopregel; exitcode ≠ 0 is een fout
// met de uitvoer erin.
func (r *RunExecutor) runQuiet(ctx context.Context, argv ...string) ([]byte, error) {
	var buf bytes.Buffer
	code, err := r.cmd(ctx, "", argv, nil, &buf)
	if err != nil {
		return buf.Bytes(), err
	}
	if code != 0 {
		return buf.Bytes(), fmt.Errorf("%s eindigde met exitcode %d: %s", strings.Join(argv[:min(3, len(argv))], " "), code, composeLastLine(buf.String()))
	}
	return buf.Bytes(), nil
}

func composeLastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func (r *RunExecutor) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// composeArgs is docker compose met het project, zijn map en bestanden.
func composeArgs(p protocol.ComposeProject) []string {
	argv := []string{"docker", "compose", "-p", p.Name}
	if p.WorkingDir != "" {
		argv = append(argv, "--project-directory", p.WorkingDir)
	}
	for _, f := range p.ConfigFiles {
		argv = append(argv, "-f", f)
	}
	for _, f := range p.EnvFiles {
		argv = append(argv, "--env-file", f)
	}
	return argv
}

// dockerSmart is de Docker-run van fase 15.
func (r *RunExecutor) dockerSmart(ctx context.Context, dir string, d *protocol.DockerRun, out io.Writer, st *protocol.RunStatus) (int, error) {
	project := ""
	if len(d.Projects) == 1 {
		project = d.Projects[0]
	}
	_, _ = fmt.Fprintf(out, "$ docker ps -a (Compose-projecten zoeken)\n")
	projects, err := discoverCompose(ctx, r.runQuiet, r.exists, project)
	if err != nil {
		return -1, err
	}
	var todo []protocol.ComposeProject
	for _, want := range d.Projects {
		i := slices.IndexFunc(projects, func(p protocol.ComposeProject) bool { return p.Name == want })
		if i < 0 {
			return -1, fmt.Errorf("compose-project %s staat niet op deze node", want)
		}
		todo = append(todo, projects[i])
	}
	if len(d.Projects) == 0 {
		todo = projects
	}
	if d.Read {
		files, err := r.readComposeFiles(todo[0])
		st.Files = files
		return 0, err
	}
	if len(todo) == 0 {
		_, _ = fmt.Fprintf(out, "ClusterForge: geen Docker Compose-projecten op deze node\n")
	}
	var untouched []string
	for _, p := range todo {
		_, _ = fmt.Fprintf(out, "\n== %s (%s)\n", p.Name, p.WorkingDir)
		for _, s := range p.Services {
			_, _ = fmt.Fprintf(out, "   %s: %s, %s", s.Service, s.Image, s.State)
			if len(s.Mounts) > 0 {
				_, _ = fmt.Fprintf(out, ", gemapt: %s", strings.Join(s.Mounts, ", "))
			}
			_, _ = fmt.Fprintln(out)
		}
		rep := r.updateProject(ctx, dir, p, d, out)
		st.Docker = append(st.Docker, rep)
		if rep.Result == protocol.DockerFailed || rep.Result == protocol.DockerRolledBack {
			if !rep.Unchanged {
				return 1, fmt.Errorf("%s: %s", p.Name, rep.Problem)
			}
			// Niets veranderd (zoals een pull die faalde): de andere
			// projecten gaan door, de fout komt in het rapport.
			untouched = append(untouched, p.Name+": "+rep.Problem)
		}
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
	}
	if len(untouched) > 0 {
		return 1, errors.New(strings.Join(untouched, "; "))
	}
	if d.Prune {
		if code, err := r.step(ctx, out, nil, "docker", "image", "prune", "-f"); err != nil {
			return code, err
		}
	}
	return 0, nil
}

// backup bewaart de bestanden van een project in de map van de run.
type backup struct {
	dir   string
	files map[string]string // pad → kopie
}

func (r *RunExecutor) save(b *backup, p string) error {
	if _, ok := b.files[p]; ok {
		return nil
	}
	data, err := os.ReadFile(r.path(p))
	if err != nil {
		return err
	}
	cp := filepath.Join(b.dir, strconv.Itoa(len(b.files))+"-"+filepath.Base(p))
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(cp, data, 0o600); err != nil {
		return err
	}
	b.files[p] = cp
	return nil
}

func (r *RunExecutor) restore(b *backup, out io.Writer) {
	for p, cp := range b.files {
		data, err := os.ReadFile(cp)
		if err == nil {
			err = writeKeepMode(r.path(p), data)
		}
		if err != nil {
			_, _ = fmt.Fprintf(out, "ClusterForge: %s terugzetten mislukte: %v (de kopie staat in %s)\n", p, err, cp)
			continue
		}
		_, _ = fmt.Fprintf(out, "ClusterForge: %s teruggezet\n", p)
	}
}

// writeKeepMode schrijft een bestand met dezelfde rechten en eigenaar.
func writeKeepMode(p string, data []byte) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(p, data, fi.Mode().Perm()); err != nil {
		return err
	}
	keepOwner(p, fi)
	return nil
}

// updateProject werkt één project bij en zegt wat er gebeurde.
func (r *RunExecutor) updateProject(ctx context.Context, dir string, p protocol.ComposeProject, d *protocol.DockerRun, out io.Writer) protocol.DockerReport {
	rep := protocol.DockerReport{Project: p.Name, WorkingDir: p.WorkingDir, ConfigFiles: p.ConfigFiles, EnvFiles: p.EnvFiles}
	fail := func(format string, args ...any) protocol.DockerReport {
		rep.Result, rep.Problem = protocol.DockerFailed, fmt.Sprintf(format, args...)
		_, _ = fmt.Fprintf(out, "ClusterForge: %s\n", rep.Problem)
		return rep
	}
	if len(p.Missing) > 0 {
		return fail("het compose-bestand %s bestaat niet meer; ClusterForge kan het project niet opnieuw maken", strings.Join(p.Missing, ", "))
	}
	before := map[string]protocol.ComposeService{}
	wasRunning := false
	for _, s := range p.Services {
		before[s.Service] = s
		wasRunning = wasRunning || running(s)
	}
	b := &backup{dir: filepath.Join(dir, "backup", p.Name), files: map[string]string{}}
	base := composeArgs(p)

	// Nieuwe tags en het bestand uit de editor.
	for _, t := range d.Tags {
		if t.Project != p.Name {
			continue
		}
		f, err := r.applyTag(b, p, t, out)
		if err != nil {
			r.restore(b, out)
			return fail("%v", err)
		}
		rep.Edited = appendNew(rep.Edited, f)
	}
	if e := d.Edit; e != nil {
		if err := r.applyEdit(b, p, e); err != nil {
			r.restore(b, out)
			return fail("%v", err)
		}
		_, _ = fmt.Fprintf(out, "ClusterForge: %s aangepast vanuit de GUI\n", e.Path)
		rep.Edited = appendNew(rep.Edited, e.Path)
	}
	if len(rep.Edited) > 0 {
		if _, err := r.step(ctx, out, nil, append(slices.Clone(base), "config", "-q")...); err != nil {
			r.restore(b, out)
			return fail("het aangepaste compose-bestand klopt niet (docker compose config); niets veranderd")
		}
	}

	if err := r.composePull(ctx, out, base, nil); err != nil {
		r.restore(b, out)
		rep.Unchanged = true
		return fail("docker compose pull mislukte; niets veranderd, het project draait zoals voordien")
	}
	images := r.serviceImages(ctx, base)
	changed := len(rep.Edited) > 0 || d.Always
	for svc, ref := range images {
		sr := protocol.ServiceReport{Service: svc, Image: ref}
		if old, ok := before[svc]; ok {
			sr.OldID = old.ImageID
		}
		if id, err := r.runQuiet(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", ref); err == nil {
			sr.NewID = strings.TrimSpace(string(id))
		}
		if sr.NewID != "" && sr.NewID != sr.OldID {
			changed = true
		}
		rep.Services = append(rep.Services, sr)
	}
	slices.SortFunc(rep.Services, func(a, b protocol.ServiceReport) int { return strings.Compare(a.Service, b.Service) })
	if !wasRunning {
		_, _ = fmt.Fprintf(out, "ClusterForge: %s stond stil; alleen de images zijn opgehaald, het project blijft uit\n", p.Name)
		rep.Result = protocol.DockerUpToDate
		if changed {
			rep.Result = protocol.DockerUpdated
		}
		return rep
	}
	if !changed {
		_, _ = fmt.Fprintf(out, "ClusterForge: geen nieuw image voor %s; de containers blijven draaien\n", p.Name)
		rep.Result = protocol.DockerUpToDate
		return rep
	}
	r.imageDetails(ctx, &rep)

	upErr := r.recreate(ctx, out, base)
	ok, problem := false, ""
	if upErr != nil {
		problem = "docker compose up -d mislukte"
	} else {
		ok, problem = r.waitHealthy(ctx, p, before, d, out)
	}
	rep.Logs = r.composeLogs(ctx, base)
	if rep.Logs != "" {
		_, _ = fmt.Fprintf(out, "\n-- docker compose logs (laatste regels) --\n%s\n", tailLines(rep.Logs, 30))
	}
	r.fillStates(ctx, p, &rep)
	if ok {
		_, _ = fmt.Fprintf(out, "ClusterForge: %s draait weer, elke container is gezond\n", p.Name)
		rep.Result = protocol.DockerUpdated
		return rep
	}
	rep.Problem = problem
	if d.NoRollback {
		rep.Result = protocol.DockerFailed
		_, _ = fmt.Fprintf(out, "ClusterForge: %s; terugzetten staat uit, het project blijft zoals het nu is\n", problem)
		return rep
	}
	_, _ = fmt.Fprintf(out, "\nClusterForge: %s; de vorige versie terugzetten\n", problem)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	r.restore(b, out)
	for _, s := range p.Services {
		if s.ImageID != "" && s.Image != "" {
			_, _ = r.step(rctx, out, nil, "docker", "tag", s.ImageID, s.Image)
		}
	}
	if err := r.recreate(rctx, out, base); err == nil {
		if back, why := r.waitHealthy(rctx, p, before, d, out); back {
			rep.Result = protocol.DockerRolledBack
			rep.Problem = problem + "; de vorige versie draait weer"
			_, _ = fmt.Fprintf(out, "ClusterForge: de vorige versie van %s draait weer\n", p.Name)
			return rep
		} else {
			rep.Problem = problem + "; ook de vorige versie komt niet gezond terug: " + why
		}
	} else {
		rep.Problem = problem + "; ook terugzetten mislukte (docker compose up -d)"
	}
	rep.Result = protocol.DockerFailed
	return rep
}

func appendNew(xs []string, x string) []string {
	if slices.Contains(xs, x) {
		return xs
	}
	return append(xs, x)
}

func (r *RunExecutor) recreate(ctx context.Context, out io.Writer, base []string) error {
	if _, err := r.step(ctx, out, nil, append(slices.Clone(base), "down")...); err != nil {
		return err
	}
	_, err := r.step(ctx, out, nil, append(slices.Clone(base), "up", "-d")...)
	return err
}

// serviceImages geeft per dienst het image zoals de compose-bestanden het
// nu noemen.
// composePull haalt de images van een project op, behalve van diensten met
// build: en van images die alleen lokaal bestaan (zelf gebouwd, zoals
// cf-platform:local): die staan in geen registry, en een pull ervan laat de
// hele update stranden. only beperkt tot die diensten (leeg is alle).
func (r *RunExecutor) composePull(ctx context.Context, out io.Writer, base []string, only []string) error {
	cfg := r.composeConfig(ctx, base)
	var pull, skipped []string
	for name, svc := range cfg {
		if len(only) > 0 && !slices.Contains(only, name) {
			continue
		}
		switch {
		case svc.Build:
			skipped = append(skipped, name+" (build)")
		case svc.Image != "" && r.localOnlyImage(ctx, svc.Image):
			skipped = append(skipped, name+" ("+svc.Image+", lokaal gebouwd)")
		default:
			pull = append(pull, name)
		}
	}
	if len(skipped) == 0 {
		// Niets over te slaan (of de config was onleesbaar): gewoon alles.
		argv := append(slices.Clone(base), "pull", "--ignore-buildable")
		_, err := r.step(ctx, out, nil, append(argv, only...)...)
		return err
	}
	slices.Sort(skipped)
	slices.Sort(pull)
	_, _ = fmt.Fprintf(out, "ClusterForge: niet opgehaald, want niet uit een registry: %s\n", strings.Join(skipped, ", "))
	if len(pull) == 0 {
		return nil
	}
	_, err := r.step(ctx, out, nil, append(append(slices.Clone(base), "pull", "--ignore-buildable"), pull...)...)
	return err
}

// localOnlyImage is true voor een image dat lokaal bestaat maar nooit uit
// een registry kwam (geen RepoDigests).
func (r *RunExecutor) localOnlyImage(ctx context.Context, ref string) bool {
	data, err := r.runQuiet(ctx, "docker", "image", "inspect", "--format", "{{len .RepoDigests}}", ref)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "0"
}

type composeService struct {
	Image string
	Build bool
}

// composeConfig leest de diensten uit docker compose config; leeg bij een
// fout.
func (r *RunExecutor) composeConfig(ctx context.Context, base []string) map[string]composeService {
	data, err := r.runQuiet(ctx, append(slices.Clone(base), "config", "--format", "json")...)
	out := map[string]composeService{}
	if err != nil {
		return out
	}
	var cfg struct {
		Services map[string]struct {
			Image string          `json:"image"`
			Build json.RawMessage `json:"build"`
		} `json:"services"`
	}
	if json.Unmarshal(jsonPart(data), &cfg) != nil {
		return out
	}
	for name, s := range cfg.Services {
		b := strings.TrimSpace(string(s.Build))
		out[name] = composeService{Image: s.Image, Build: b != "" && b != "null"}
	}
	return out
}

func (r *RunExecutor) serviceImages(ctx context.Context, base []string) map[string]string {
	data, err := r.runQuiet(ctx, append(slices.Clone(base), "config", "--format", "json")...)
	out := map[string]string{}
	if err != nil {
		return out
	}
	var cfg struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if json.Unmarshal(jsonPart(data), &cfg) != nil {
		return out
	}
	for name, s := range cfg.Services {
		if s.Image != "" {
			out[name] = s.Image
		}
	}
	return out
}

// jsonPart slaat waarschuwingen van docker compose voor de JSON over.
func jsonPart(data []byte) []byte {
	if i := bytes.IndexAny(data, "{["); i > 0 {
		return data[i:]
	}
	return data
}

// imageDetails vult per dienst met een nieuw image de versies, de bron en
// de omgevingsvariabelen die erbij kwamen of verdwenen.
func (r *RunExecutor) imageDetails(ctx context.Context, rep *protocol.DockerReport) {
	type img struct {
		Config struct {
			Env    []string          `json:"Env"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	look := func(id string) (img, bool) {
		var out []img
		if id == "" {
			return img{}, false
		}
		data, err := r.runQuiet(ctx, "docker", "image", "inspect", id)
		if err != nil || json.Unmarshal(bytes.TrimSpace(data), &out) != nil || len(out) == 0 {
			return img{}, false
		}
		return out[0], true
	}
	keys := func(env []string) []string {
		var ks []string
		for _, e := range env {
			k, _, _ := strings.Cut(e, "=")
			if k != "" && k != "PATH" {
				ks = append(ks, k)
			}
		}
		return ks
	}
	for i := range rep.Services {
		s := &rep.Services[i]
		if s.NewID == "" || s.NewID == s.OldID {
			continue
		}
		nw, ok := look(s.NewID)
		if !ok {
			continue
		}
		s.NewVersion, s.Source = nw.Config.Labels[labelOCIVersion], nw.Config.Labels[labelOCISource]
		old, ok := look(s.OldID)
		if !ok {
			continue
		}
		s.OldVersion = old.Config.Labels[labelOCIVersion]
		if s.Source == "" {
			s.Source = old.Config.Labels[labelOCISource]
		}
		ok1, nk := keys(old.Config.Env), keys(nw.Config.Env)
		for _, k := range nk {
			if !slices.Contains(ok1, k) {
				s.EnvAdded = append(s.EnvAdded, k)
			}
		}
		for _, k := range ok1 {
			if !slices.Contains(nk, k) {
				s.EnvRemoved = append(s.EnvRemoved, k)
			}
		}
	}
}

// waitHealthy wacht tot elke dienst die vóór de update draaide, aan één
// stuk DockerStable draait en gezond is.
func (r *RunExecutor) waitHealthy(ctx context.Context, p protocol.ComposeProject, before map[string]protocol.ComposeService, d *protocol.DockerRun, out io.Writer) (bool, string) {
	limit := protocol.DockerHealthDefault
	if d.HealthSeconds > 0 {
		limit = time.Duration(d.HealthSeconds) * time.Second
	}
	stable := protocol.DockerStable
	if r.Stable > 0 {
		stable = r.Stable
	}
	every := 2 * time.Second
	if r.Poll > 0 {
		every = r.Poll
	}
	_, _ = fmt.Fprintf(out, "ClusterForge: testen of %s weer draait (hoogstens %s)\n", p.Name, limit)
	deadline := r.now().Add(limit)
	var goodSince time.Time
	var problem string
	exited := map[string]int{}
	for {
		now := r.now()
		cur, err := discoverCompose(ctx, r.runQuiet, r.exists, p.Name)
		switch {
		case err != nil:
			problem, goodSince = err.Error(), time.Time{}
		default:
			problem = ""
			got := map[string]protocol.ComposeService{}
			for _, c := range cur {
				for _, s := range c.Services {
					got[s.Service] = s
				}
			}
			names := make([]string, 0, len(before))
			for n := range before {
				names = append(names, n)
			}
			slices.Sort(names)
			for _, name := range names {
				if !running(before[name]) {
					continue
				}
				s, ok := got[name]
				switch {
				case !ok:
					problem = fmt.Sprintf("de container van %s bestaat niet", name)
				case s.State == "exited" || s.State == "dead":
					exited[name]++
					problem = fmt.Sprintf("%s is gestopt (%s)", name, s.State)
				case s.State != "running":
					problem = fmt.Sprintf("%s is %s", name, s.State)
				case s.Restarts > 0:
					problem = fmt.Sprintf("%s herstartte %d keer", name, s.Restarts)
				case s.Health == "unhealthy":
					problem = fmt.Sprintf("%s is unhealthy volgens zijn healthcheck", name)
				case s.Health == "starting":
					problem = fmt.Sprintf("%s start nog (healthcheck)", name)
				}
				if problem != "" {
					break
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
			}
			// Een container die twee keer na elkaar gestopt is, komt niet
			// vanzelf terug.
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

// fillStates zet de toestand na de update in het rapport.
func (r *RunExecutor) fillStates(ctx context.Context, p protocol.ComposeProject, rep *protocol.DockerReport) {
	cur, err := discoverCompose(ctx, r.runQuiet, r.exists, p.Name)
	if err != nil {
		return
	}
	for _, c := range cur {
		for _, s := range c.Services {
			for i := range rep.Services {
				if rep.Services[i].Service == s.Service {
					rep.Services[i].State, rep.Services[i].Health, rep.Services[i].Restarts = s.State, s.Health, s.Restarts
				}
			}
		}
	}
}

// composeLogs geeft de laatste regels van docker compose logs, gemaskeerd.
func (r *RunExecutor) composeLogs(ctx context.Context, base []string) string {
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	data, _ := r.runQuiet(lctx, append(slices.Clone(base), "logs", "--no-color", "--timestamps", "--tail", "100")...)
	s := protocol.MaskSecrets(string(data))
	if len(s) > protocol.MaxDockerLog {
		s = s[len(s)-protocol.MaxDockerLog:]
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return strings.TrimRight(s, "\n")
}

func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// --- tags en de editor -----------------------------------------------------

var (
	imageLine = regexp.MustCompile(`^(\s*image:\s*)(["']?)([^"'#\s]+)(["']?)(\s*(?:#.*)?)$`)
	varRef    = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)(?:[:-][^}]*)?\}?`)
)

// applyTag zet in de compose-bestanden image From op To. Gebruikt de regel
// een variabele (nginx:${NGINX_TAG}), dan past het de variabele in het
// env-bestand aan. Het geeft het aangepaste bestand.
func (r *RunExecutor) applyTag(b *backup, p protocol.ComposeProject, t protocol.ImageTag, out io.Writer) (string, error) {
	for _, f := range p.ConfigFiles {
		data, err := os.ReadFile(r.path(f))
		if err != nil {
			return "", err
		}
		lines := strings.Split(string(data), "\n")
		hit := false
		var vars []string
		for i, l := range lines {
			m := imageLine.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			switch {
			case m[3] == t.From:
				lines[i] = m[1] + m[2] + t.To + m[4] + m[5]
				hit = true
			case strings.Contains(m[3], "$"):
				vars = append(vars, m[3])
			}
		}
		if hit {
			if err := r.save(b, f); err != nil {
				return "", err
			}
			if err := writeKeepMode(r.path(f), []byte(strings.Join(lines, "\n"))); err != nil {
				return "", err
			}
			_, _ = fmt.Fprintf(out, "ClusterForge: %s: image %s → %s\n", f, t.From, t.To)
			return f, nil
		}
		// nginx:${NGINX_TAG} met NGINX_TAG=1.27 in het env-bestand.
		for _, v := range vars {
			if f, ok, err := r.tagViaEnv(b, p, v, t, out); err != nil || ok {
				return f, err
			}
		}
	}
	return "", fmt.Errorf("het image %s staat niet in de compose-bestanden van %s", t.From, p.Name)
}

func (r *RunExecutor) tagViaEnv(b *backup, p protocol.ComposeProject, pattern string, t protocol.ImageTag, out io.Writer) (string, bool, error) {
	refs := varRef.FindAllStringSubmatchIndex(pattern, -1)
	if len(refs) != 1 {
		return "", false, nil
	}
	m := refs[0]
	prefix, suffix, name := pattern[:m[0]], pattern[m[1]:], pattern[m[2]:m[3]]
	if !strings.HasPrefix(t.From, prefix) || !strings.HasSuffix(t.From, suffix) || !strings.HasPrefix(t.To, prefix) || !strings.HasSuffix(t.To, suffix) {
		return "", false, nil
	}
	oldVal := strings.TrimSuffix(strings.TrimPrefix(t.From, prefix), suffix)
	newVal := strings.TrimSuffix(strings.TrimPrefix(t.To, prefix), suffix)
	for _, f := range p.EnvFiles {
		data, err := os.ReadFile(r.path(f))
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for i, l := range lines {
			k, v, ok := strings.Cut(l, "=")
			if !ok || strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(k), "export ")) != name {
				continue
			}
			if strings.Trim(strings.TrimSpace(v), `"'`) != oldVal {
				return "", false, fmt.Errorf("%s in %s is %s, niet %s", name, f, strings.TrimSpace(v), oldVal)
			}
			lines[i] = k + "=" + newVal
			if err := r.save(b, f); err != nil {
				return "", false, err
			}
			if err := writeKeepMode(r.path(f), []byte(strings.Join(lines, "\n"))); err != nil {
				return "", false, err
			}
			_, _ = fmt.Fprintf(out, "ClusterForge: %s: %s=%s → %s\n", f, name, oldVal, newVal)
			return f, true, nil
		}
	}
	return "", false, nil
}

// maskFile en unmaskFile staan in protocol (fase 21 gebruikt ze ook in de
// server).
func maskFile(content string) (string, []string) { return protocol.MaskFile(content) }

func unmaskFile(content string, secrets []string) (string, error) {
	out, err := protocol.UnmaskFile(content, secrets)
	if err != nil {
		err = fmt.Errorf("%s", strings.Replace(err.Error(), "in het bewaarde bestand", "in het bestand op de node", 1))
	}
	return out, err
}

func sha(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// projectFiles zijn de bestanden die de editor mag lezen en schrijven.
func projectFiles(p protocol.ComposeProject) []string {
	return append(slices.Clone(p.ConfigFiles), p.EnvFiles...)
}

func (r *RunExecutor) readComposeFiles(p protocol.ComposeProject) ([]protocol.ComposeFile, error) {
	var out []protocol.ComposeFile
	for _, f := range projectFiles(p) {
		data, err := os.ReadFile(r.path(f))
		if err != nil {
			return out, fmt.Errorf("%s lezen: %w", f, err)
		}
		if len(data) > protocol.MaxComposeFile {
			return out, fmt.Errorf("%s is groter dan %d bytes", f, protocol.MaxComposeFile)
		}
		masked, secrets := maskFile(string(data))
		out = append(out, protocol.ComposeFile{
			Path: f, Content: masked, SHA256: sha(data), Env: slices.Contains(p.EnvFiles, f), Secrets: len(secrets),
		})
	}
	return out, nil
}

func (r *RunExecutor) applyEdit(b *backup, p protocol.ComposeProject, e *protocol.ComposeEdit) error {
	if !slices.Contains(projectFiles(p), e.Path) {
		return fmt.Errorf("%s hoort niet bij project %s", e.Path, p.Name)
	}
	cur, err := os.ReadFile(r.path(e.Path))
	if err != nil {
		return err
	}
	if sha(cur) != e.OldSHA256 {
		return errors.New("het bestand veranderde op de node sinds je het opende; open het opnieuw")
	}
	_, secrets := maskFile(string(cur))
	content, err := unmaskFile(e.Content, secrets)
	if err != nil {
		return err
	}
	if err := r.save(b, e.Path); err != nil {
		return err
	}
	return writeKeepMode(r.path(e.Path), []byte(content))
}
