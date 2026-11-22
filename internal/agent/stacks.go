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
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Docker-stacks (fase 21): de agent geeft de containers en Compose-projecten
// van de node, de bestanden van een project voor de overname en de logs van
// een container. Het schrijven en starten gebeurt in een run (stackrun.go).

func (a *Agent) onStacks(m *nats.Msg) {
	go func() {
		res := a.handleStacks(m.Data)
		data, err := envelope(protocol.TypeStacksResult, res)
		if err != nil {
			return
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op stackverzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handleStacks(data []byte) protocol.StacksResponse {
	var env protocol.Envelope
	var req protocol.StacksRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeStacksRequest {
		return protocol.StacksResponse{Error: "ongeldig stackverzoek"}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil {
		return protocol.StacksResponse{Error: "ongeldig stackverzoek"}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.StacksResponse{Error: "stackverzoek verlopen; klopt de klok van deze node?"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.StacksRequestTimeout)
	defer cancel()
	run := func(ctx context.Context, argv ...string) ([]byte, error) {
		return a.exec(ctx, argv[0], argv[1:]...)
	}
	sr := stackReader{run: run, root: a.Root}
	switch req.Op {
	case protocol.StacksOpList:
		if req.Files && !protocol.ValidStackName(req.Project) {
			return protocol.StacksResponse{Error: "bestanden lezen gaat per project"}
		}
		return sr.list(ctx, req)
	case protocol.StacksOpLogs:
		if !protocol.ValidContainerName(req.Container) {
			return protocol.StacksResponse{Error: "ongeldige containernaam"}
		}
		return sr.logs(ctx, req.Container, req.Tail)
	}
	return protocol.StacksResponse{Error: "onbekend stackverzoek " + req.Op}
}

// stackReader leest containers, projecten en bestanden.
type stackReader struct {
	run  runFunc
	root string
}

func (s stackReader) path(p string) string {
	if s.root == "" {
		return p
	}
	return filepath.Join(s.root, p)
}

func (s stackReader) exists(p string) bool {
	_, err := os.Stat(s.path(p))
	return err == nil
}

// inspectedFull is wat de lijst uit docker inspect leest; inspected (fase 15)
// met wat de pagina Containers erbij toont.
type inspectedFull struct {
	inspected
	State struct {
		Status string `json:"Status"`
		Health *struct {
			Status string `json:"Status"`
		} `json:"Health"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
	HostConfig struct {
		NetworkMode   string `json:"NetworkMode"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

func (s stackReader) inspectAll(ctx context.Context) ([]inspectedFull, map[string]string, error) {
	ps, err := s.run(ctx, "docker", "ps", "-a", "--no-trunc", "--format", "{{.ID}}\t{{.Status}}")
	if err != nil {
		return nil, nil, fmt.Errorf("docker ps -a: %s", composeLastLine(string(ps)))
	}
	status := map[string]string{}
	var ids []string
	for line := range strings.SplitSeq(string(ps), "\n") {
		id, st, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || len(id) < 12 {
			continue
		}
		ids = append(ids, id)
		status[id] = st
	}
	if len(ids) == 0 {
		return nil, status, nil
	}
	data, err := s.run(ctx, append([]string{"docker", "inspect"}, ids...)...)
	if err != nil {
		return nil, nil, fmt.Errorf("docker inspect: %s", composeLastLine(string(data)))
	}
	var cs []inspectedFull
	if err := json.Unmarshal(jsonPart(bytes.TrimSpace(data)), &cs); err != nil {
		return nil, nil, fmt.Errorf("docker inspect lezen: %w", err)
	}
	for i := range cs {
		// inspected (fase 15) leest State apart; houd ze gelijk.
		cs[i].inspected.State.Status = cs[i].State.Status
		cs[i].inspected.State.Health = cs[i].State.Health
	}
	return cs, status, nil
}

func (s stackReader) list(ctx context.Context, req protocol.StacksRequest) protocol.StacksResponse {
	cs, status, err := s.inspectAll(ctx)
	if err != nil {
		return protocol.StacksResponse{Error: err.Error()}
	}
	res := protocol.StacksResponse{Docker: true}
	managed := map[string]bool{}
	if data, err := os.ReadFile(s.path(protocol.StackListFile)); err == nil {
		res.Lists = string(data)
		for _, l := range protocol.ParseStackList(string(data)) {
			managed[l.Name] = true
		}
	}
	plain := make([]inspected, 0, len(cs))
	for _, c := range cs {
		plain = append(plain, c.inspected)
		res.Containers = append(res.Containers, containerOf(c, status[c.ID]))
	}
	slices.SortFunc(res.Containers, func(a, b protocol.StackContainer) int {
		return strings.Compare(a.Project+"/"+a.Name, b.Project+"/"+b.Name)
	})
	projects := groupCompose(plain, s.exists)
	for _, p := range projects {
		res.Projects = append(res.Projects, protocol.StackProjectInfo{
			Name: p.Name, WorkingDir: p.WorkingDir, ConfigFiles: p.ConfigFiles, EnvFiles: p.EnvFiles,
			Status: p.Status, Missing: p.Missing, Managed: managed[p.Name],
		})
	}
	if !req.Files {
		return res
	}
	i := slices.IndexFunc(projects, func(p protocol.ComposeProject) bool { return p.Name == req.Project })
	if i < 0 {
		return protocol.StacksResponse{Error: "project " + req.Project + " staat niet op deze node"}
	}
	files, skipped, err := s.projectFiles(ctx, projects[i])
	if err != nil {
		return protocol.StacksResponse{Error: err.Error()}
	}
	res.Files, res.Skipped = files, skipped
	// Alleen dit project; de rest is voor de lijst niet nodig.
	res.Containers = slices.DeleteFunc(res.Containers, func(c protocol.StackContainer) bool { return c.Project != req.Project })
	return res
}

func containerOf(c inspectedFull, status string) protocol.StackContainer {
	out := protocol.StackContainer{
		Name: strings.TrimPrefix(c.Name, "/"), ID: c.ID, Image: c.Config.Image, State: c.State.Status, Restarts: c.RestartCount,
		Status: status, Project: c.Config.Labels[labelProject], Service: c.Config.Labels[labelService],
		Network: c.HostConfig.NetworkMode, Restart: c.HostConfig.RestartPolicy.Name,
	}
	if len(out.ID) > 12 {
		out.ID = out.ID[:12]
	}
	if c.State.Health != nil {
		out.Health = c.State.Health.Status
	}
	if t, err := time.Parse(time.RFC3339Nano, c.State.StartedAt); err == nil && t.Year() > 1 {
		t = t.UTC()
		out.StartedAt = &t
	}
	for port, binds := range c.NetworkSettings.Ports {
		for _, b := range binds {
			if b.HostPort == "" {
				continue
			}
			p := b.HostPort + "->" + port
			if !slices.Contains(out.Ports, p) {
				out.Ports = append(out.Ports, p)
			}
		}
	}
	sort.Strings(out.Ports)
	for _, m := range c.Mounts {
		out.Mounts = append(out.Mounts, protocol.StackMount{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination, RW: m.RW})
	}
	return out
}

// skipName zijn bestanden in de map van een project die data zijn, geen
// configuratie.
func skipName(name string) bool {
	low := strings.ToLower(name)
	for _, suf := range []string{".log", ".db", ".sqlite", ".sqlite3", ".db-wal", ".db-shm", ".bak", ".tar", ".gz", ".zip", ".pid", ".sock", ".swp"} {
		if strings.HasSuffix(low, suf) {
			return true
		}
	}
	return name == ".DS_Store"
}

// projectFiles leest de bestanden van een project voor de overname: de
// compose- en env-bestanden, de bestanden waar de compose-bestanden naar
// verwijzen (env_file, een gemapt bestand zoals nginx.conf) en de kleine
// tekstbestanden direct in de map. Alles moet in de map van het project
// staan; mappen met data neemt de sync mee, niet de stack.
func (s stackReader) projectFiles(ctx context.Context, p protocol.ComposeProject) ([]protocol.StackFile, []string, error) {
	dir := p.WorkingDir
	if !protocol.ValidStackDir(dir) {
		return nil, nil, fmt.Errorf("de map %q van %s kan ClusterForge niet beheren", dir, p.Name)
	}
	if len(p.Missing) > 0 {
		return nil, nil, fmt.Errorf("het compose-bestand %s bestaat niet meer", strings.Join(p.Missing, ", "))
	}
	rel := func(f string) (string, bool) {
		r, ok := strings.CutPrefix(f, dir+"/")
		return r, ok && protocol.ValidStackFile(r)
	}
	var must []string
	for _, f := range append(slices.Clone(p.ConfigFiles), p.EnvFiles...) {
		r, ok := rel(f)
		if !ok {
			return nil, nil, fmt.Errorf("%s staat niet in de map %s van het project; zet het daar en start het project opnieuw", f, dir)
		}
		must = append(must, r)
	}
	if s.exists(dir + "/.env") {
		must = appendNew(must, ".env")
	}
	// Waar de compose-bestanden naar verwijzen.
	argv := composeArgs(p)
	if data, err := s.run(ctx, append(argv, "config", "--format", "json")...); err == nil {
		var cfg struct {
			Services map[string]struct {
				EnvFile json.RawMessage `json:"env_file"`
				Volumes []struct {
					Type   string `json:"type"`
					Source string `json:"source"`
				} `json:"volumes"`
			} `json:"services"`
		}
		if json.Unmarshal(jsonPart(data), &cfg) == nil {
			for _, sv := range cfg.Services {
				for _, f := range envFilePaths(sv.EnvFile) {
					if !filepath.IsAbs(f) {
						f = path.Join(dir, f)
					}
					if r, ok := rel(f); ok && s.exists(f) {
						must = appendNew(must, r)
					}
				}
				for _, v := range sv.Volumes {
					if v.Type != "bind" {
						continue
					}
					if fi, err := os.Stat(s.path(v.Source)); err == nil && fi.Mode().IsRegular() && fi.Size() <= protocol.MaxStackFile {
						if r, ok := rel(v.Source); ok {
							must = appendNew(must, r)
						}
					}
				}
			}
		}
	}
	var files []protocol.StackFile
	var skipped []string
	total := 0
	add := func(r string, required bool) error {
		if slices.ContainsFunc(files, func(f protocol.StackFile) bool { return f.Path == r }) {
			return nil
		}
		full := s.path(path.Join(dir, r))
		fi, err := os.Lstat(full)
		if err != nil || !fi.Mode().IsRegular() {
			if required {
				return fmt.Errorf("%s/%s lezen: geen gewoon bestand", dir, r)
			}
			return nil
		}
		why := ""
		var data []byte
		switch {
		case fi.Size() > protocol.MaxStackFile:
			why = "groter dan 256 KB"
		case len(files) >= protocol.MaxStackFiles:
			why = "te veel bestanden"
		default:
			data, err = os.ReadFile(full)
			switch {
			case err != nil:
				why = err.Error()
			case bytes.IndexByte(data, 0) >= 0:
				why = "binair"
			case total+len(data) > protocol.MaxStackBytes:
				why = "samen te groot"
			}
		}
		if why != "" {
			if required {
				return fmt.Errorf("%s/%s kan niet mee: %s", dir, r, why)
			}
			skipped = append(skipped, r+" ("+why+")")
			return nil
		}
		total += len(data)
		files = append(files, protocol.StackFile{Path: r, Content: string(data), Mode: uint32(fi.Mode().Perm())})
		return nil
	}
	for _, r := range must {
		if err := add(r, true); err != nil {
			return nil, nil, err
		}
	}
	entries, err := os.ReadDir(s.path(dir))
	if err != nil {
		return nil, nil, fmt.Errorf("%s lezen: %w", dir, err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !protocol.ValidStackFile(e.Name()) {
			continue
		}
		if skipName(e.Name()) {
			skipped = append(skipped, e.Name()+" (data)")
			continue
		}
		if err := add(e.Name(), false); err != nil {
			return nil, nil, err
		}
	}
	slices.SortFunc(files, func(a, b protocol.StackFile) int { return strings.Compare(a.Path, b.Path) })
	return files, skipped, nil
}

// envFilePaths leest env_file uit docker compose config: een tekst, een
// lijst van teksten of een lijst van {path: …}.
func envFilePaths(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []json.RawMessage
	if json.Unmarshal(raw, &many) != nil {
		return nil
	}
	var out []string
	for _, m := range many {
		var s string
		var o struct {
			Path string `json:"path"`
		}
		switch {
		case json.Unmarshal(m, &s) == nil:
			out = append(out, s)
		case json.Unmarshal(m, &o) == nil && o.Path != "":
			out = append(out, o.Path)
		}
	}
	return out
}

func (s stackReader) logs(ctx context.Context, container string, tail int) protocol.StacksResponse {
	if tail <= 0 || tail > 2000 {
		tail = 300
	}
	data, err := s.run(ctx, "docker", "logs", "--timestamps", "--tail", fmt.Sprint(tail), container)
	text := protocol.MaskSecrets(string(data))
	if err != nil && len(data) == 0 {
		return protocol.StacksResponse{Error: "docker logs: " + err.Error()}
	}
	if err != nil && strings.Contains(text, "No such container") {
		return protocol.StacksResponse{Error: "container " + container + " bestaat niet op deze node"}
	}
	if len(text) > protocol.MaxStackLog {
		text = text[len(text)-protocol.MaxStackLog:]
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return protocol.StacksResponse{Docker: true, Logs: text}
}

// --- de pauze bij een sync ---------------------------------------------------

// pauseProjects pauzeert de draaiende containers van de projecten en geeft
// hun id's; unpause laat ze weer lopen.
func pauseProjects(ctx context.Context, run func(ctx context.Context, argv []string, out io.Writer) (int, error), projects []string, out io.Writer) ([]string, error) {
	var ids []string
	for _, p := range projects {
		var buf bytes.Buffer
		code, err := run(ctx, []string{"docker", "ps", "-q", "--filter", "label=" + labelProject + "=" + p, "--filter", "status=running"}, &buf)
		if err != nil || code != 0 {
			return ids, fmt.Errorf("docker ps voor %s mislukte", p)
		}
		ids = append(ids, strings.Fields(buf.String())...)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	_, _ = fmt.Fprintf(out, "== %d containers van %s pauzeren voor een consistente kopie ==\n", len(ids), strings.Join(projects, ", "))
	code, err := run(ctx, append([]string{"docker", "pause"}, ids...), out)
	if err != nil || code != 0 {
		return ids, errors.New("docker pause mislukte")
	}
	return ids, nil
}

func unpause(run func(ctx context.Context, argv []string, out io.Writer) (int, error), ids []string, out io.Writer) {
	if len(ids) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, _ = fmt.Fprintf(out, "== containers weer laten lopen ==\n")
	// Elk apart: een container die intussen stopte, mag de rest niet tegenhouden.
	for _, id := range ids {
		_, _ = run(ctx, []string{"docker", "unpause", id}, out)
	}
}
