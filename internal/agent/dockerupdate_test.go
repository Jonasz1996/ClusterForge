package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakeDocker speelt docker en docker compose na voor één project "web"
// met de dienst "app".
type fakeDocker struct {
	mu    sync.Mutex
	root  string
	calls []string
	// images: naam of id → id.
	images map[string]string
	// pulled is het id dat pull op elke naam zet ("" laat het zoals het is).
	pulled string
	// upState is de toestand na up -d; upStates per keer up -d.
	upStates []string
	ups      int
	state    string
	imageID  string
	ref      string
	// localOnly zijn images zonder registry (zelf gebouwd).
	localOnly map[string]bool
	// pullFails laat pull mislukken.
	pullFails bool
}

func newFakeDocker(t *testing.T, compose, env string) *fakeDocker {
	root := t.TempDir()
	dir := filepath.Join(root, "srv/web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeDocker{root: root, images: map[string]string{}, state: "running", imageID: "sha256:old"}
	f.ref = f.composeImage()
	f.images[f.ref] = "sha256:old"
	return f
}

var fakeImageLine = regexp.MustCompile(`(?m)^\s*image:\s*(\S+)`)

// composeImage leest het image uit het compose-bestand, met de variabele
// uit .env ingevuld.
func (f *fakeDocker) composeImage() string {
	data, _ := os.ReadFile(filepath.Join(f.root, "srv/web/compose.yml"))
	m := fakeImageLine.FindStringSubmatch(string(data))
	if m == nil {
		return ""
	}
	img := m[1]
	env, _ := os.ReadFile(filepath.Join(f.root, "srv/web/.env"))
	for l := range strings.Lines(string(env)) {
		k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
		img = strings.ReplaceAll(img, "${"+k+"}", v)
	}
	return img
}

func (f *fakeDocker) container() []map[string]any {
	envFile := ""
	if _, err := os.Stat(filepath.Join(f.root, "srv/web/.env")); err == nil {
		envFile = "/srv/web/.env"
	}
	c := map[string]any{
		"Id": "c1", "Name": "/web-app-1", "Image": f.imageID, "RestartCount": 0,
		"State": map[string]any{"Status": f.state},
		"Config": map[string]any{"Image": f.ref, "Labels": map[string]string{
			labelProject: "web", labelWorkingDir: "/srv/web", labelConfig: "/srv/web/compose.yml", labelService: "app", labelEnvFile: envFile,
		}},
		"Mounts": []map[string]any{
			{"Type": "bind", "Source": "/data/web/conf", "Destination": "/etc/app", "RW": false},
			{"Type": "volume", "Name": "web_data", "Source": "/var/lib/docker/volumes/web_data/_data", "Destination": "/data", "RW": true},
		},
	}
	return []map[string]any{c}
}

func (f *fakeDocker) cmd(_ context.Context, _ string, argv, _ []string, out io.Writer) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := strings.Join(argv, " ")
	f.calls = append(f.calls, c)
	sub := ""
	if len(argv) > 2 && argv[1] == "compose" {
		// het werkwoord staat na de -p/-f/--env-file-opties.
		for i := 2; i < len(argv); i++ {
			switch argv[i] {
			case "-p", "-f", "--env-file", "--project-directory":
				i++
				continue
			}
			sub = strings.Join(argv[i:], " ")
			break
		}
	}
	switch {
	case strings.HasPrefix(c, "docker ps -a -q"):
		_, _ = io.WriteString(out, "c1\n")
	case strings.HasPrefix(c, "docker inspect"):
		b, _ := json.Marshal(f.container())
		_, _ = out.Write(b)
	case strings.HasPrefix(sub, "pull") && f.pullFails:
		_, _ = io.WriteString(out, "pull access denied\n")
		return 1, nil
	case strings.HasPrefix(sub, "pull"):
		if f.pulled != "" {
			f.images[f.composeImage()] = f.pulled
		}
	case sub == "config -q":
	case sub == "config --format json":
		_, _ = fmt.Fprintf(out, `{"services":{"app":{"image":%q}}}`, f.composeImage())
	case sub == "down":
		f.state = "exited"
	case sub == "up -d":
		f.ref = f.composeImage()
		f.imageID = f.images[f.ref]
		f.state = "running"
		if f.ups < len(f.upStates) {
			f.state = f.upStates[f.ups]
		}
		f.ups++
	case strings.HasPrefix(sub, "logs"):
		_, _ = io.WriteString(out, "app-1  | 2026-11-15T21:00:00Z started\napp-1  | DB_PASSWORD=hunter2\n")
	case strings.HasPrefix(c, "docker image inspect --format {{len .RepoDigests}}"):
		ref := argv[len(argv)-1]
		if f.localOnly[ref] {
			_, _ = io.WriteString(out, "0\n")
		} else {
			_, _ = io.WriteString(out, "1\n")
		}
	case strings.HasPrefix(c, "docker image inspect --format"):
		id, ok := f.images[argv[len(argv)-1]]
		if !ok {
			return 1, nil
		}
		_, _ = io.WriteString(out, id+"\n")
	case strings.HasPrefix(c, "docker image inspect"):
		id := argv[len(argv)-1]
		env := []string{"PATH=/bin", "APP_PORT=80"}
		labels := map[string]string{labelOCIVersion: "1.27", labelOCISource: "https://github.com/acme/app"}
		if id == "sha256:new" {
			env = []string{"PATH=/bin", "APP_LISTEN=0.0.0.0:80"}
			labels[labelOCIVersion] = "1.29"
		}
		b, _ := json.Marshal([]map[string]any{{"Config": map[string]any{"Env": env, "Labels": labels}}})
		_, _ = out.Write(b)
	case strings.HasPrefix(c, "docker tag"):
		f.images[argv[3]] = argv[2]
	}
	return 0, nil
}

func (f *fakeDocker) called(part string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, part) {
			n++
		}
	}
	return n
}

// dockerRun draait een Docker-run met nep-docker en een klok die
// meeloopt met het wachten.
func dockerRun(t *testing.T, f *fakeDocker, d *protocol.DockerRun) (protocol.RunStatus, string) {
	t.Helper()
	now := time.Date(2026, 11, 15, 21, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	ex := &RunExecutor{
		Root: f.root, Cmd: f.cmd, Stderr: io.Discard,
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()
			now = now.Add(d)
			return nil
		},
	}
	d.Smart = true
	spec := protocol.RunSpec{ID: "dock-1", Kind: protocol.RunDocker, Docker: d}
	dir := runDir(f.root, spec.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	ex.Main(context.Background(), spec.ID)
	st, err := readRunStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "output.log"))
	return st, string(out)
}

const composeWeb = "services:\n  app:\n    image: acme/app:1.27 # versie\n    environment:\n      DB_PASSWORD: hunter2\n      - API_TOKEN=abc123\n"

func TestDockerSmartUpdated(t *testing.T) {
	f := newFakeDocker(t, composeWeb, "")
	f.pulled = "sha256:new"
	st, out := dockerRun(t, f, &protocol.DockerRun{})
	if st.State != protocol.RunOK || len(st.Docker) != 1 {
		t.Fatalf("stand %+v\n%s", st, out)
	}
	rep := st.Docker[0]
	if rep.Result != protocol.DockerUpdated || rep.WorkingDir != "/srv/web" || len(rep.Services) != 1 {
		t.Fatalf("rapport %+v", rep)
	}
	s := rep.Services[0]
	if s.OldID != "sha256:old" || s.NewID != "sha256:new" || s.OldVersion != "1.27" || s.NewVersion != "1.29" || s.Source != "https://github.com/acme/app" {
		t.Errorf("dienst %+v", s)
	}
	if !slices.Equal(s.EnvAdded, []string{"APP_LISTEN"}) || !slices.Equal(s.EnvRemoved, []string{"APP_PORT"}) {
		t.Errorf("variabelen erbij %v, weg %v", s.EnvAdded, s.EnvRemoved)
	}
	if !strings.Contains(rep.Logs, "started") || strings.Contains(rep.Logs, "hunter2") {
		t.Errorf("logs %q", rep.Logs)
	}
	if f.called(" down") != 1 || f.called(" up -d") != 1 || f.called("docker tag") != 0 {
		t.Errorf("aanroepen %v", f.calls)
	}
	if !strings.Contains(out, "gemapt: /data/web/conf:/etc/app:ro, web_data:/data") {
		t.Errorf("uitvoer zonder gemapte mappen:\n%s", out)
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "docker compose -p web --project-directory /srv/web -f /srv/web/compose.yml pull") {
		t.Errorf("pull zonder map en bestand: %v", f.calls)
	}
}

func TestDockerSmartUpToDate(t *testing.T) {
	f := newFakeDocker(t, composeWeb, "")
	st, _ := dockerRun(t, f, &protocol.DockerRun{})
	if st.State != protocol.RunOK || st.Docker[0].Result != protocol.DockerUpToDate {
		t.Fatalf("stand %+v", st)
	}
	if f.called(" down") != 0 || f.called(" up -d") != 0 {
		t.Errorf("containers opnieuw gemaakt zonder nieuw image: %v", f.calls)
	}
}

func TestDockerSmartRollback(t *testing.T) {
	f := newFakeDocker(t, composeWeb, "")
	f.pulled = "sha256:new"
	f.upStates = []string{"exited", "running"}
	st, out := dockerRun(t, f, &protocol.DockerRun{})
	if st.State != protocol.RunFailed || st.Docker[0].Result != protocol.DockerRolledBack {
		t.Fatalf("stand %+v\n%s", st, out)
	}
	if f.called("docker tag sha256:old acme/app:1.27") != 1 || f.called(" up -d") != 2 {
		t.Errorf("terugzetten: %v", f.calls)
	}
	if f.imageID != "sha256:old" || f.state != "running" {
		t.Errorf("na terugzetten draait %s (%s)", f.imageID, f.state)
	}
	if !strings.Contains(st.Docker[0].Problem, "app is gestopt") {
		t.Errorf("probleem %q", st.Docker[0].Problem)
	}
}

func TestDockerSmartNoRollback(t *testing.T) {
	f := newFakeDocker(t, composeWeb, "")
	f.pulled = "sha256:new"
	f.upStates = []string{"restarting"}
	st, _ := dockerRun(t, f, &protocol.DockerRun{NoRollback: true, HealthSeconds: 10})
	if st.State != protocol.RunFailed || st.Docker[0].Result != protocol.DockerFailed || f.called("docker tag") != 0 {
		t.Fatalf("stand %+v, aanroepen %v", st, f.calls)
	}
}

func TestDockerSmartTag(t *testing.T) {
	f := newFakeDocker(t, composeWeb, "")
	f.images["acme/app:1.29"] = "sha256:new"
	st, out := dockerRun(t, f, &protocol.DockerRun{Tags: []protocol.ImageTag{{Project: "web", From: "acme/app:1.27", To: "acme/app:1.29"}}})
	if st.State != protocol.RunOK || st.Docker[0].Result != protocol.DockerUpdated {
		t.Fatalf("stand %+v\n%s", st, out)
	}
	data, _ := os.ReadFile(filepath.Join(f.root, "srv/web/compose.yml"))
	if !strings.Contains(string(data), "image: acme/app:1.29 # versie") {
		t.Errorf("compose:\n%s", data)
	}
	if !slices.Equal(st.Docker[0].Edited, []string{"/srv/web/compose.yml"}) {
		t.Errorf("aangepast %v", st.Docker[0].Edited)
	}
}

func TestDockerSmartTagViaEnv(t *testing.T) {
	f := newFakeDocker(t, "services:\n  app:\n    image: acme/app:${APP_TAG}\n", "APP_TAG=1.27\nOTHER=x\n")
	f.images["acme/app:1.29"] = "sha256:new"
	st, out := dockerRun(t, f, &protocol.DockerRun{Tags: []protocol.ImageTag{{Project: "web", From: "acme/app:1.27", To: "acme/app:1.29"}}})
	if st.State != protocol.RunOK {
		t.Fatalf("stand %+v\n%s", st, out)
	}
	data, _ := os.ReadFile(filepath.Join(f.root, "srv/web/.env"))
	if string(data) != "APP_TAG=1.29\nOTHER=x\n" {
		t.Errorf(".env:\n%s", data)
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "--env-file /srv/web/.env") {
		t.Errorf("geen --env-file: %v", f.calls)
	}
}

func TestDockerSmartTagFailsRestores(t *testing.T) {
	f := newFakeDocker(t, composeWeb, "")
	f.images["acme/app:1.29"] = "sha256:new"
	f.upStates = []string{"exited", "running"}
	st, _ := dockerRun(t, f, &protocol.DockerRun{Tags: []protocol.ImageTag{{Project: "web", From: "acme/app:1.27", To: "acme/app:1.29"}}})
	if st.Docker[0].Result != protocol.DockerRolledBack {
		t.Fatalf("stand %+v", st)
	}
	data, _ := os.ReadFile(filepath.Join(f.root, "srv/web/compose.yml"))
	if string(data) != composeWeb {
		t.Errorf("compose niet teruggezet:\n%s", data)
	}
}

func TestComposeReadAndEdit(t *testing.T) {
	f := newFakeDocker(t, composeWeb, "")
	st, _ := dockerRun(t, f, &protocol.DockerRun{Projects: []string{"web"}, Read: true})
	if st.State != protocol.RunOK || len(st.Files) != 1 {
		t.Fatalf("lezen %+v", st)
	}
	file := st.Files[0]
	if strings.Contains(file.Content, "hunter2") || strings.Contains(file.Content, "abc123") || file.Secrets != 2 {
		t.Fatalf("geheimen in de editor:\n%s", file.Content)
	}
	if !strings.Contains(file.Content, "DB_PASSWORD: «geheim-1»") || !strings.Contains(file.Content, "- API_TOKEN=«geheim-2»") {
		t.Fatalf("plaatshouders:\n%s", file.Content)
	}
	edited := strings.Replace(file.Content, "acme/app:1.27", "acme/app:1.29", 1) + "      EXTRA: \"1\"\n"
	f.images["acme/app:1.29"] = "sha256:new"
	f.calls = nil
	runID := "dock-1"
	_ = os.RemoveAll(runDir(f.root, runID))
	st, out := dockerRun(t, f, &protocol.DockerRun{Projects: []string{"web"}, Edit: &protocol.ComposeEdit{Path: file.Path, Content: edited, OldSHA256: file.SHA256}})
	if st.State != protocol.RunOK || st.Docker[0].Result != protocol.DockerUpdated {
		t.Fatalf("aanpassen %+v\n%s", st, out)
	}
	data, _ := os.ReadFile(filepath.Join(f.root, "srv/web/compose.yml"))
	if !strings.Contains(string(data), "DB_PASSWORD: hunter2") || !strings.Contains(string(data), "API_TOKEN=abc123") || !strings.Contains(string(data), "acme/app:1.29") {
		t.Errorf("bestand na aanpassen:\n%s", data)
	}
	// Een tweede keer met de oude sha256 weigert de agent.
	_ = os.RemoveAll(runDir(f.root, runID))
	st, _ = dockerRun(t, f, &protocol.DockerRun{Projects: []string{"web"}, Edit: &protocol.ComposeEdit{Path: file.Path, Content: edited, OldSHA256: file.SHA256}})
	if st.State != protocol.RunFailed || !strings.Contains(st.Docker[0].Problem, "veranderde op de node") {
		t.Errorf("verouderd bestand: %+v", st)
	}
}

func TestMaskFile(t *testing.T) {
	in := "A=1\nexport MY_SECRET='x y'\nDB_PASS=\nREF_TOKEN=${OTHER}\n  password: s3cr3t # niet\n"
	masked, secrets := maskFile(in)
	if len(secrets) != 2 || secrets[0] != "'x y'" || secrets[1] != "s3cr3t # niet" {
		t.Fatalf("geheimen %q\n%s", secrets, masked)
	}
	back, err := unmaskFile(masked, secrets)
	if err != nil || back != in {
		t.Fatalf("terug %q %v", back, err)
	}
	if _, err := unmaskFile("X=«geheim-7»", secrets); err == nil {
		t.Error("onbekende plaatshouder aanvaard")
	}
}

func TestDiscoverComposeMissingFile(t *testing.T) {
	f := newFakeDocker(t, composeWeb, "")
	_ = os.Remove(filepath.Join(f.root, "srv/web/compose.yml"))
	ex := &RunExecutor{Root: f.root, Cmd: f.cmd}
	ps, err := discoverCompose(context.Background(), ex.runQuiet, ex.exists, "")
	if err != nil || len(ps) != 1 || !slices.Equal(ps[0].Missing, []string{"/srv/web/compose.yml"}) || ps[0].Status != "running(1)" {
		t.Fatalf("%+v %v", ps, err)
	}
	st, _ := dockerRun(t, f, &protocol.DockerRun{})
	if st.State != protocol.RunFailed || f.called(" pull") != 0 {
		t.Errorf("project zonder compose-bestand: %+v", st)
	}
}

// TestDockerSmartLocalImage: een zelf gebouwd image (zonder registry) wordt
// niet opgehaald; de rest van de update loopt gewoon.
func TestDockerSmartLocalImage(t *testing.T) {
	f := newFakeDocker(t, composeWeb, "")
	f.localOnly = map[string]bool{f.composeImage(): true}
	f.pullFails = true // een pull zou falen: dat mag niet gebeuren
	st, out := dockerRun(t, f, &protocol.DockerRun{})
	if st.State != protocol.RunOK || len(st.Docker) != 1 || st.Docker[0].Result != protocol.DockerUpToDate {
		t.Fatalf("stand %+v\n%s", st, out)
	}
	if f.called(" pull") != 0 || !strings.Contains(out, "lokaal gebouwd") {
		t.Errorf("pull van een lokaal image: %v\n%s", f.calls, out)
	}
}

// TestDockerSmartPullFails: een pull die faalt verandert niets; het rapport
// zegt dat (Unchanged), zodat de update van het cluster doorgaat.
func TestDockerSmartPullFails(t *testing.T) {
	f := newFakeDocker(t, composeWeb, "")
	f.pullFails = true
	st, out := dockerRun(t, f, &protocol.DockerRun{})
	if st.State != protocol.RunFailed || len(st.Docker) != 1 || !st.Docker[0].Unchanged || st.Docker[0].Result != protocol.DockerFailed {
		t.Fatalf("stand %+v\n%s", st, out)
	}
	if f.called(" down") != 0 || f.called(" up -d") != 0 {
		t.Errorf("na een mislukte pull toch aangeraakt: %v", f.calls)
	}
}
