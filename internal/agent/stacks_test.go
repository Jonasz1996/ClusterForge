package agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakeStack speelt docker na voor één project "app" in /srv/app met de
// dienst "web".
type fakeStack struct {
	mu       sync.Mutex
	root     string
	calls    []string
	state    string
	health   string
	restarts int
	// afterUp is de toestand na up -d; "" is running.
	afterUp   string
	exists    bool
	badConfig bool
}

func (f *fakeStack) inspect() []byte {
	if !f.exists {
		return []byte("[]")
	}
	st := map[string]any{"Status": f.state, "StartedAt": "2026-11-16T03:00:00Z"}
	if f.health != "" {
		st["Health"] = map[string]any{"Status": f.health}
	}
	b, _ := json.Marshal([]map[string]any{{
		"Id": "c1aaaaaaaaaaaaaa", "Name": "/app-web-1", "Image": "sha256:1", "RestartCount": f.restarts, "State": st,
		"Config": map[string]any{"Image": "nginx:1.27", "Labels": map[string]string{
			labelProject: "app", labelWorkingDir: "/srv/app", labelConfig: "/srv/app/compose.yml", labelService: "web",
		}},
		"Mounts":     []map[string]any{{"Type": "bind", "Source": "/srv/app/data", "Destination": "/data", "RW": true}},
		"HostConfig": map[string]any{"NetworkMode": "app_default", "RestartPolicy": map[string]any{"Name": "unless-stopped"}},
		"NetworkSettings": map[string]any{"Ports": map[string]any{
			"80/tcp": []map[string]any{{"HostIp": "0.0.0.0", "HostPort": "8080"}, {"HostIp": "::", "HostPort": "8080"}},
		}},
	}})
	return b
}

func composeVerb(argv []string) string {
	if len(argv) < 3 || argv[1] != "compose" {
		return ""
	}
	for i := 2; i < len(argv); i++ {
		switch argv[i] {
		case "-p", "-f", "--env-file", "--project-directory":
			i++
			continue
		}
		return strings.Join(argv[i:], " ")
	}
	return ""
}

func (f *fakeStack) run(argv []string) ([]byte, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := strings.Join(argv, " ")
	f.calls = append(f.calls, c)
	switch verb := composeVerb(argv); {
	case strings.HasPrefix(c, "docker ps -a --no-trunc --format"):
		if !f.exists {
			return nil, 0
		}
		return []byte("c1aaaaaaaaaaaaaa\tUp 4 days\n"), 0
	case strings.HasPrefix(c, "docker ps -a -q"):
		if !f.exists {
			return nil, 0
		}
		return []byte("c1aaaaaaaaaaaaaa\n"), 0
	case strings.HasPrefix(c, "docker inspect"):
		return f.inspect(), 0
	case verb == "config -q":
		if f.badConfig {
			return []byte("yaml: line 2: did not find expected key"), 1
		}
		return nil, 0
	case verb == "config --services":
		return []byte("web\n"), 0
	case verb == "config --format json":
		return []byte(`{"services":{"web":{"env_file":[{"path":"/srv/app/config/prod.env","required":true}],"volumes":[{"type":"bind","source":"/srv/app/nginx.conf"},{"type":"bind","source":"/srv/app/data"}]}}}`), 0
	case strings.HasPrefix(verb, "up -d"):
		f.exists = true
		f.state = "running"
		if f.afterUp != "" {
			f.state = f.afterUp
		}
		return []byte("Container app-web-1 Started\n"), 0
	case strings.HasPrefix(verb, "stop"):
		f.state = "exited"
		return nil, 0
	case strings.HasPrefix(verb, "pull"), strings.HasPrefix(verb, "restart"), strings.HasPrefix(verb, "down"), strings.HasPrefix(verb, "logs"):
		return nil, 0
	case strings.HasPrefix(c, "docker logs"):
		return []byte("2026-11-16T03:00:00Z started DB_PASSWORD=geheim123\n"), 0
	}
	return []byte("onbekend: " + c), 1
}

func (f *fakeStack) cmd(_ context.Context, _ string, argv, _ []string, out io.Writer) (int, error) {
	data, code := f.run(argv)
	_, _ = out.Write(data)
	return code, nil
}

func (f *fakeStack) did(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, prefix) })
}

func newStackExec(t *testing.T, f *fakeStack) *RunExecutor {
	f.root = t.TempDir()
	return &RunExecutor{Root: f.root, Cmd: f.cmd, Stable: time.Millisecond, Poll: time.Millisecond,
		Sleep: func(ctx context.Context, d time.Duration) error { return ctx.Err() }}
}

func appStack(content string) protocol.StackApply {
	return protocol.StackApply{Name: "app", Dir: "/srv/app", ComposeFiles: []string{"compose.yml"}, Want: protocol.StackUp,
		Files: []protocol.StackFile{{Path: "compose.yml", Content: content}, {Path: "config/prod.env", Content: "A=1\n", Mode: 0o600}}}
}

func TestStackRunUpWritesFilesAndWaits(t *testing.T) {
	f := &fakeStack{}
	r := newStackExec(t, f)
	list := protocol.FormatStackList([]protocol.StackListLine{{Name: "app", Dir: "/srv/app", ComposeFiles: []string{"compose.yml"}, Placement: protocol.PlaceVIP, Critical: true}})
	var out strings.Builder
	var st protocol.RunStatus
	code, err := r.stackRun(context.Background(), t.TempDir(), &protocol.StackRun{Stacks: []protocol.StackApply{appStack("services: {web: {image: nginx:1.27}}\n")}, List: &list}, &out, &st)
	if err != nil || code != 0 {
		t.Fatalf("code %d, err %v\n%s", code, err, out.String())
	}
	if len(st.Stacks) != 1 || st.Stacks[0].Result != protocol.StackResultOK || len(st.Stacks[0].Written) != 2 {
		t.Fatalf("rapport %+v", st.Stacks)
	}
	fi, err := os.Stat(filepath.Join(f.root, "srv/app/config/prod.env"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("prod.env: %v %v", fi, err)
	}
	data, _ := os.ReadFile(filepath.Join(f.root, protocol.StackListFile))
	if got := protocol.ParseStackList(string(data)); len(got) != 1 || !got[0].Critical || got[0].Placement != protocol.PlaceVIP {
		t.Fatalf("lijst %q", data)
	}
	if !f.did("config -q") || !f.did("up -d --remove-orphans") {
		t.Fatalf("calls %v", f.calls)
	}
	if st.Stacks[0].States["app-web-1"] != "running" {
		t.Fatalf("states %v", st.Stacks[0].States)
	}

	// Dezelfde bestanden en alles draait: niets te doen.
	f.calls = nil
	st = protocol.RunStatus{}
	if code, err := r.stackRun(context.Background(), t.TempDir(), &protocol.StackRun{Stacks: []protocol.StackApply{appStack("services: {web: {image: nginx:1.27}}\n")}}, &out, &st); err != nil || code != 0 {
		t.Fatal(err)
	}
	if st.Stacks[0].Result != protocol.StackResultUnchanged || f.did("up -d") {
		t.Fatalf("niet ongewijzigd: %+v %v", st.Stacks[0], f.calls)
	}
}

func TestStackRunRollsBackFilesWhenUnhealthy(t *testing.T) {
	f := &fakeStack{exists: true, state: "running"}
	r := newStackExec(t, f)
	dir := filepath.Join(f.root, "srv/app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("oud\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.afterUp = "restarting"
	var out strings.Builder
	var st protocol.RunStatus
	code, err := r.stackRun(context.Background(), t.TempDir(), &protocol.StackRun{Stacks: []protocol.StackApply{appStack("nieuw\n")}, HealthSeconds: 1}, &out, &st)
	if err == nil || code != 1 {
		t.Fatalf("verwacht een fout, code %d\n%s", code, out.String())
	}
	if st.Stacks[0].Result != protocol.StackResultFailed || !strings.Contains(st.Stacks[0].Problem, "vorige bestanden staan terug") {
		t.Fatalf("rapport %+v", st.Stacks[0])
	}
	data, _ := os.ReadFile(filepath.Join(dir, "compose.yml"))
	if string(data) != "oud\n" {
		t.Fatalf("compose.yml niet teruggezet: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "config/prod.env")); !os.IsNotExist(err) {
		t.Fatalf("nieuw bestand prod.env niet weggehaald: %v", err)
	}
}

func TestStackRunBadConfigChangesNothing(t *testing.T) {
	f := &fakeStack{badConfig: true}
	r := newStackExec(t, f)
	var out strings.Builder
	var st protocol.RunStatus
	if _, err := r.stackRun(context.Background(), t.TempDir(), &protocol.StackRun{Stacks: []protocol.StackApply{appStack("x: [\n")}}, &out, &st); err == nil {
		t.Fatal("verwacht een fout")
	}
	if f.did("up -d") {
		t.Fatal("up na een foute config")
	}
	if _, err := os.Stat(filepath.Join(f.root, "srv/app/compose.yml")); !os.IsNotExist(err) {
		t.Fatalf("compose.yml niet weggehaald: %v", err)
	}
}

func TestStackRunStopAndOldRestarts(t *testing.T) {
	f := &fakeStack{exists: true, state: "running", restarts: 7}
	r := newStackExec(t, f)
	dir := filepath.Join(f.root, "srv/app")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("x\n"), 0o644)
	var out strings.Builder
	var st protocol.RunStatus
	s := protocol.StackApply{Name: "app", Dir: "/srv/app", ComposeFiles: []string{"compose.yml"}, Want: protocol.StackRestart}
	// Oude herstarts tellen niet: de test slaagt.
	if code, err := r.stackRun(context.Background(), t.TempDir(), &protocol.StackRun{Stacks: []protocol.StackApply{s}}, &out, &st); err != nil || code != 0 {
		t.Fatalf("%v\n%s", err, out.String())
	}
	s.Want = protocol.StackStop
	if _, err := r.stackRun(context.Background(), t.TempDir(), &protocol.StackRun{Stacks: []protocol.StackApply{s}}, &out, &st); err != nil {
		t.Fatal(err)
	}
	if f.state != "exited" {
		t.Fatalf("niet gestopt: %s", f.state)
	}
}

func TestStackReaderListAndFiles(t *testing.T) {
	f := &fakeStack{exists: true, state: "running", health: "healthy"}
	root := t.TempDir()
	f.root = root
	dir := filepath.Join(root, "srv/app")
	for p, c := range map[string]string{
		"compose.yml": "services: {}\n", ".env": "DB_PASSWORD=x\n", "nginx.conf": "server {}\n",
		"config/prod.env": "SECRET_KEY=y\n", "config/other.txt": "niet\n", "app.db": "data", "bin.dat": "a\x00b",
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.MkdirAll(filepath.Join(root, "etc/clusterforge"), 0o755)
	_ = os.WriteFile(filepath.Join(root, protocol.StackListFile), []byte(protocol.FormatStackList([]protocol.StackListLine{{Name: "app", Dir: "/srv/app", ComposeFiles: []string{"compose.yml"}, Placement: "alle"}})), 0o644)
	sr := stackReader{root: root, run: func(_ context.Context, argv ...string) ([]byte, error) {
		data, code := f.run(argv)
		if code != 0 {
			return data, os.ErrInvalid
		}
		return data, nil
	}}
	res := sr.list(context.Background(), protocol.StacksRequest{Op: protocol.StacksOpList, Project: "app", Files: true})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if len(res.Containers) != 1 || res.Containers[0].Health != "healthy" || !slices.Equal(res.Containers[0].Ports, []string{"8080->80/tcp"}) {
		t.Fatalf("containers %+v", res.Containers)
	}
	if len(res.Projects) != 1 || !res.Projects[0].Managed {
		t.Fatalf("projecten %+v", res.Projects)
	}
	var paths []string
	for _, fl := range res.Files {
		paths = append(paths, fl.Path)
	}
	if want := []string{".env", "compose.yml", "config/prod.env", "nginx.conf"}; !slices.Equal(paths, want) {
		t.Fatalf("bestanden %v, verwacht %v (overgeslagen %v)", paths, want, res.Skipped)
	}
	if res.Files[0].Mode != 0o640 {
		t.Fatalf("rechten %o", res.Files[0].Mode)
	}
	if !slices.ContainsFunc(res.Skipped, func(s string) bool { return strings.HasPrefix(s, "app.db") }) ||
		!slices.ContainsFunc(res.Skipped, func(s string) bool { return strings.HasPrefix(s, "bin.dat") }) {
		t.Fatalf("overgeslagen %v", res.Skipped)
	}
	logs := sr.logs(context.Background(), "app-web-1", 50)
	if logs.Error != "" || strings.Contains(logs.Logs, "geheim123") {
		t.Fatalf("logs %+v", logs)
	}
}
