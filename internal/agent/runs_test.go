package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakeHost speelt apt, dpkg en docker na voor RunExecutor.
type fakeHost struct {
	mu    sync.Mutex
	calls []string
	// reply geeft per commando (argv samen) de uitvoer en exitcode.
	reply func(cmd string) (string, int)
}

func (h *fakeHost) cmd(_ context.Context, user string, argv, env []string, out io.Writer) (int, error) {
	c := strings.Join(argv, " ")
	h.mu.Lock()
	h.calls = append(h.calls, c)
	h.mu.Unlock()
	text, code := "", 0
	if h.reply != nil {
		text, code = h.reply(c)
	}
	_, _ = io.WriteString(out, text)
	return code, nil
}

func (h *fakeHost) called(prefix string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// runAgent geeft een agent die een run meteen in het eigen proces uitvoert.
func runAgent(t *testing.T, h *fakeHost) (*Agent, string) {
	root := t.TempDir()
	ex := &RunExecutor{Root: root, Cmd: h.cmd, Stderr: io.Discard}
	a := &Agent{Root: root, Exec: func(context.Context, string, ...string) ([]byte, error) { return []byte("inactive"), nil }}
	a.StartRun = func(_ context.Context, id string) error {
		if code := ex.Main(context.Background(), id); code != 0 && code != 1 {
			return fmt.Errorf("exit %d", code)
		}
		return nil
	}
	return a, root
}

func TestRunApt(t *testing.T) {
	h := &fakeHost{reply: func(c string) (string, int) {
		switch {
		case c == "apt-get update -q":
			return "Hit:1 http://deb.debian.org/debian trixie InRelease\n", 0
		case strings.HasPrefix(c, "dpkg-query") && strings.HasSuffix(c, "mariadb-*"):
			return "mariadb-server installed\nmariadb-client installed\nmariadb-test not-installed\n", 0
		case strings.HasPrefix(c, "dpkg-query"):
			return "", 1
		case c == "apt-mark showhold":
			return "mariadb-client\n", 0
		case strings.HasPrefix(c, "apt-get upgrade"):
			return "The following packages will be upgraded:\n  openssl\nmysql -uroot -pGeheim123\n", 0
		}
		return "", 0
	}}
	a, root := runAgent(t, h)
	spec := protocol.RunSpec{ID: "upd-1", Kind: protocol.RunApt, Title: "Pakketten bijwerken", Apt: &protocol.AptRun{Autoremove: true, Hold: []string{"mariadb-*", "galera-*"}}}
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run/reboot-required"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	res := a.runStart(context.Background(), spec)
	if res.Error != "" || res.Status == nil {
		t.Fatalf("%+v", res)
	}
	st := res.Status
	if st.State != protocol.RunOK || !st.RebootRequired || st.Exit == nil || *st.Exit != 0 {
		t.Errorf("stand %+v", st)
	}
	if strings.Join(st.Held, ",") != "mariadb-server" {
		t.Errorf("vastgehouden %v", st.Held)
	}
	want := []string{"apt-mark hold mariadb-server", "apt-mark unhold mariadb-server"}
	if got := h.called("apt-mark "); strings.Join(got, ";") != "apt-mark showhold;"+strings.Join(want, ";") {
		t.Errorf("apt-mark: %v", got)
	}
	if got := h.called("apt-get "); len(got) != 3 || !strings.HasPrefix(got[1], "apt-get upgrade -y -q -o Dpkg::Options::=--force-confdef") || got[2] != "apt-get autoremove -y -q" {
		t.Errorf("apt-get: %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, protocol.RunHoldsFile)); !os.IsNotExist(err) {
		t.Errorf("holds.json blijft staan: %v", err)
	}
	if !strings.Contains(res.Output, "== Pakketten bijwerken") || !strings.Contains(res.Output, "-p***") || strings.Contains(res.Output, "Geheim123") {
		t.Errorf("uitvoer:\n%s", res.Output)
	}
	// Opnieuw starten met hetzelfde id start niets.
	before := len(h.called(""))
	if again := a.runStart(context.Background(), spec); again.Status == nil || again.Status.State != protocol.RunOK || len(h.called("")) != before {
		t.Errorf("tweede start: %+v", again)
	}
	// Verder lezen vanaf de offset geeft niets meer.
	if more := a.runPoll(context.Background(), spec.ID, res.Offset); more.Output != "" || more.Offset != res.Offset {
		t.Errorf("poll: %+v", more)
	}
}

func TestRunAptBrokenSource(t *testing.T) {
	for _, tc := range []struct {
		name, uri string
		ok        bool
	}{
		{"buiten Debian", "https://repo.zabbix.com/zabbix/7.4/release/debian", true},
		{"Debian zelf", "http://deb.debian.org/debian", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &fakeHost{reply: func(c string) (string, int) {
				if c == "apt-get update -q" {
					return "Err:7 " + tc.uri + " trixie Release\n  404  Not Found [IP: 1.2.3.4 443]\nE: The repository '" + tc.uri + " trixie Release' does not have a Release file.\n", 100
				}
				return "", 0
			}}
			a, _ := runAgent(t, h)
			res := a.runStart(context.Background(), protocol.RunSpec{ID: "upd-2", Kind: protocol.RunApt, Apt: &protocol.AptRun{Full: true}})
			if res.Status == nil {
				t.Fatalf("%+v", res)
			}
			if got := res.Status.State == protocol.RunOK; got != tc.ok {
				t.Errorf("ok=%v, wil %v: %+v\n%s", got, tc.ok, res.Status, res.Output)
			}
			if tc.ok && len(h.called("apt-get full-upgrade")) != 1 {
				t.Errorf("geen full-upgrade: %v", h.calls)
			}
		})
	}
}

func TestRunDocker(t *testing.T) {
	h := &fakeHost{reply: func(c string) (string, int) {
		switch {
		case c == "docker compose ls -a --format json":
			return `[{"Name":"web","Status":"running(2)","ConfigFiles":"/srv/web/compose.yml"},{"Name":"npm","Status":"running(1)","ConfigFiles":"/srv/npm/a.yml,/srv/npm/b.yml"}]`, 0
		case strings.HasSuffix(c, " up -d") && strings.Contains(c, "-p npm"):
			return "fout bij starten\n", 1
		}
		return "", 0
	}}
	a, _ := runAgent(t, h)
	res := a.runStart(context.Background(), protocol.RunSpec{ID: "dock-1", Kind: protocol.RunDocker, Docker: &protocol.DockerRun{Projects: []string{"web"}, Prune: true}})
	if res.Status == nil || res.Status.State != protocol.RunOK {
		t.Fatalf("%+v", res)
	}
	want := "docker compose ls -a --format json;docker compose -p web -f /srv/web/compose.yml pull;docker compose -p web -f /srv/web/compose.yml up -d;docker image prune -f"
	if got := strings.Join(h.called("docker"), ";"); got != want {
		t.Errorf("commando's:\n%s\nwil\n%s", got, want)
	}
	// Alle projecten: npm faalt bij up en de run stopt daar.
	res = a.runStart(context.Background(), protocol.RunSpec{ID: "dock-2", Kind: protocol.RunDocker, Docker: &protocol.DockerRun{}})
	if res.Status == nil || res.Status.State != protocol.RunFailed || !strings.Contains(res.Status.Error, "npm") {
		t.Fatalf("%+v", res.Status)
	}
	if got := h.called("docker compose -p npm -f /srv/npm/a.yml -f /srv/npm/b.yml up -d"); len(got) != 1 {
		t.Errorf("npm: %v", h.calls)
	}
	if got := h.called("docker compose -p web"); len(got) != 2 {
		t.Errorf("web na npm (alfabetisch npm eerst): %v", h.calls)
	}
	// Een onbekend project is een fout.
	res = a.runStart(context.Background(), protocol.RunSpec{ID: "dock-3", Kind: protocol.RunDocker, Docker: &protocol.DockerRun{Projects: []string{"weg"}}})
	if res.Status == nil || res.Status.State != protocol.RunFailed {
		t.Errorf("%+v", res.Status)
	}
}

func TestRunScriptReal(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("geen sh")
	}
	root := t.TempDir()
	ex := &RunExecutor{Root: root, Stderr: io.Discard}
	a := &Agent{Root: root, Exec: func(context.Context, string, ...string) ([]byte, error) { return []byte("inactive"), nil }}
	a.StartRun = func(_ context.Context, id string) error {
		go ex.Main(context.Background(), id)
		return nil
	}
	script := "echo begin\nprintf 'Include=x\\n' > " + filepath.Join(root, "conf") + "\necho klaar\nexit 3\n"
	res := a.runStart(context.Background(), protocol.RunSpec{ID: "s-1", Kind: protocol.RunScript, Script: script})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	var out strings.Builder
	offset := int64(0)
	deadline := time.Now().Add(10 * time.Second)
	for {
		p := a.runPoll(context.Background(), "s-1", offset)
		out.WriteString(p.Output)
		offset = p.Offset
		if p.Status != nil && p.Status.Done() {
			if p.Status.State != protocol.RunFailed || p.Status.Exit == nil || *p.Status.Exit != 3 {
				t.Errorf("stand %+v", p.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("de run stopt niet")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "begin\nklaar\n") {
		t.Errorf("uitvoer:\n%s", out.String())
	}
	if b, _ := os.ReadFile(filepath.Join(root, "conf")); string(b) != "Include=x\n" {
		t.Errorf("bestand: %q", b)
	}
}

func TestRunStopAndLost(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("geen sleep")
	}
	root := t.TempDir()
	ex := &RunExecutor{Root: root, Stderr: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	a := &Agent{Root: root, Exec: func(context.Context, string, ...string) ([]byte, error) { return []byte("active"), nil }}
	a.StartRun = func(_ context.Context, id string) error {
		go func() { ex.Main(ctx, id); close(done) }()
		return nil
	}
	a.StopRun = func(context.Context, string) error {
		cancel()
		<-done
		return nil
	}
	if res := a.runStart(context.Background(), protocol.RunSpec{ID: "s-2", Kind: protocol.RunScript, Script: "echo start\nsleep 30\n", TimeoutSeconds: 60}); res.Error != "" {
		t.Fatal(res.Error)
	}
	time.Sleep(200 * time.Millisecond)
	res := a.runStop(context.Background(), "s-2")
	if res.Status == nil || res.Status.State != protocol.RunStopped {
		t.Fatalf("%+v", res)
	}

	// Een run die "running" zegt zonder unit, is na runLost mislukt.
	dir := runDir(root, "s-3")
	_ = os.MkdirAll(dir, 0o700)
	_ = writeRunStatus(dir, protocol.RunStatus{State: protocol.RunRunning, StartedAt: time.Now().Add(-time.Minute)})
	a.Exec = func(context.Context, string, ...string) ([]byte, error) { return []byte("inactive"), nil }
	if p := a.runPoll(context.Background(), "s-3", 0); p.Status == nil || p.Status.State != protocol.RunFailed {
		t.Errorf("verloren run: %+v", p.Status)
	}
}

func TestReleaseHolds(t *testing.T) {
	root := t.TempDir()
	var calls []string
	a := &Agent{Root: root, Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return []byte("inactive"), nil
	}}
	a.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	_ = writeFileAtomic(filepath.Join(root, protocol.RunHoldsFile), []byte(`{"run_id":"upd-9","packages":["mariadb-server","galera-4"]}`), 0o600)
	a.releaseHolds(context.Background())
	if strings.Join(calls, ";") != "apt-mark unhold mariadb-server galera-4" {
		t.Errorf("%v", calls)
	}
	if _, err := os.Stat(filepath.Join(root, protocol.RunHoldsFile)); !os.IsNotExist(err) {
		t.Errorf("holds.json blijft: %v", err)
	}
}

func TestRunSpecValidate(t *testing.T) {
	for _, tc := range []struct {
		spec protocol.RunSpec
		ok   bool
	}{
		{protocol.RunSpec{ID: "a", Kind: protocol.RunScript, Script: "true"}, true},
		{protocol.RunSpec{ID: "../a", Kind: protocol.RunScript, Script: "true"}, false},
		{protocol.RunSpec{ID: "a", Kind: protocol.RunScript}, false},
		{protocol.RunSpec{ID: "a", Kind: protocol.RunScript, Script: "true", User: "Root;"}, false},
		{protocol.RunSpec{ID: "a", Kind: protocol.RunApt, Apt: &protocol.AptRun{Hold: []string{"mariadb-*"}}}, true},
		{protocol.RunSpec{ID: "a", Kind: protocol.RunApt, Apt: &protocol.AptRun{Hold: []string{"-x"}}}, false},
		{protocol.RunSpec{ID: "a", Kind: protocol.RunDocker, Docker: &protocol.DockerRun{Projects: []string{"web; rm"}}}, false},
		{protocol.RunSpec{ID: "a", Kind: "shell"}, false},
		{protocol.RunSpec{ID: "a", Kind: protocol.RunScript, Script: "true", Apt: &protocol.AptRun{}}, false},
	} {
		if err := tc.spec.Validate(); (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc.spec, err)
		}
	}
}
