package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakeDockerAPI is een Docker-API op een unix-socket onder een eigen root.
type fakeDockerAPI struct {
	root  string
	mu    sync.Mutex
	calls []string
}

func newFakeDockerAPI(t *testing.T, h http.HandlerFunc) *fakeDockerAPI {
	t.Helper()
	// Een unix-socket mag niet langer dan 108 tekens zijn; t.TempDir kan dat zijn.
	root, err := os.MkdirTemp("", "cfd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	sock := filepath.Join(root, protocol.DockerSocket)
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDockerAPI{root: root}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		f.mu.Unlock()
		h(w, r)
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

func dockerReq(t *testing.T, req protocol.DockerRequest) []byte {
	t.Helper()
	data, err := envelope(protocol.TypeDockerRequest, req)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDockerGet(t *testing.T) {
	f := newFakeDockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			_, _ = io.WriteString(w, `[{"Id":"abc","Names":["/web"]}]`)
		case "/events":
			_, _ = io.WriteString(w, `{"Type":"container","Action":"die"}`+"\n"+`{"Type":"container","Action":"start"}`+"\n")
		case "/containers/nope/json":
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"message":"No such container: nope"}`)
		}
	})
	a := &Agent{Root: f.root, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	res := a.handleDocker(dockerReq(t, protocol.DockerRequest{Op: protocol.DockerOpGet, Path: "/containers/json", Query: map[string]string{"all": "1"}}))
	if res.Error != "" || !strings.Contains(string(res.Data), `"/web"`) {
		t.Fatalf("lijst: %+v", res)
	}
	if f.calls[0] != "GET /containers/json?all=1" {
		t.Fatalf("aanroep %q", f.calls[0])
	}

	res = a.handleDocker(dockerReq(t, protocol.DockerRequest{Op: protocol.DockerOpGet, Path: "/events", Query: map[string]string{"since": "1", "until": "2"}}))
	var evs []map[string]string
	if res.Error != "" || json.Unmarshal(res.Data, &evs) != nil || len(evs) != 2 || evs[1]["Action"] != "start" {
		t.Fatalf("gebeurtenissen: %+v", res)
	}

	res = a.handleDocker(dockerReq(t, protocol.DockerRequest{Op: protocol.DockerOpGet, Path: "/containers/nope/json"}))
	if res.Status != 404 || res.Error != "No such container: nope" {
		t.Fatalf("404: %+v", res)
	}

	for _, p := range []string{"/containers/create", "/secrets", "/containers/../info", "/exec/abc/json", "/plugins"} {
		res = a.handleDocker(dockerReq(t, protocol.DockerRequest{Op: protocol.DockerOpGet, Path: p}))
		if res.Error == "" {
			t.Errorf("%s mocht gelezen worden", p)
		}
	}
	res = a.handleDocker(dockerReq(t, protocol.DockerRequest{Op: protocol.DockerOpGet, Path: "/events"}))
	if !strings.Contains(res.Error, "until") {
		t.Fatalf("events zonder until: %+v", res)
	}
	if len(f.calls) != 3 {
		t.Fatalf("geweigerde verzoeken bereikten Docker: %v", f.calls)
	}
}

func TestDockerNoSocket(t *testing.T) {
	a := &Agent{Root: t.TempDir()}
	res := a.handleDocker(dockerReq(t, protocol.DockerRequest{Op: protocol.DockerOpGet, Path: "/info"}))
	if !strings.Contains(res.Error, "geen Docker") {
		t.Fatalf("%+v", res)
	}
}

func TestDockerStats(t *testing.T) {
	f := newFakeDockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			_, _ = io.WriteString(w, `[{"Id":"a1"},{"Id":"b2"}]`)
		case "/containers/a1/stats", "/containers/b2/stats":
			if r.URL.Query().Get("stream") != "false" {
				t.Errorf("stats zonder stream=false")
			}
			id := strings.Split(r.URL.Path, "/")[2]
			_, _ = io.WriteString(w, `{"id":"`+id+`","name":"/c-`+id+`",
				"cpu_stats":{"cpu_usage":{"total_usage":300},"system_cpu_usage":2000,"online_cpus":2},
				"precpu_stats":{"cpu_usage":{"total_usage":100},"system_cpu_usage":1000},
				"memory_stats":{"usage":1000,"limit":4000,"stats":{"inactive_file":200}},
				"networks":{"eth0":{"rx_bytes":5,"tx_bytes":7}},
				"blkio_stats":{"io_service_bytes_recursive":[{"op":"read","value":11},{"op":"write","value":13}]},
				"pids_stats":{"current":3}}`)
		}
	})
	a := &Agent{Root: f.root}
	res := a.handleDocker(dockerReq(t, protocol.DockerRequest{Op: protocol.DockerOpStats}))
	if res.Error != "" || len(res.Stats) != 2 {
		t.Fatalf("%+v", res)
	}
	s := res.Stats[0]
	if s.Name != "c-a1" || s.CPU != 40 || s.MemUsage != 800 || s.MemLimit != 4000 || s.NetRx != 5 || s.NetTx != 7 || s.BlockRead != 11 || s.BlockWrite != 13 || s.PIDs != 3 {
		t.Fatalf("meting: %+v", s)
	}
}

func TestDockerAPIRun(t *testing.T) {
	f := newFakeDockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/web/restart":
			w.WriteHeader(204)
		case "/containers/web/start":
			w.WriteHeader(304)
		case "/images/prune":
			_, _ = io.WriteString(w, `{"ImagesDeleted":[{"Deleted":"sha256:1"},{"Untagged":"x"}],"SpaceReclaimed":2097152}`)
		case "/volumes/old":
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"message":"no such volume"}`)
		case "/networks/create":
			body, _ := io.ReadAll(r.Body)
			if !bytes.Contains(body, []byte(`"Name":"front"`)) || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("body %s", body)
			}
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"Id":"n1"}`)
		case "/containers/db/stop":
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"message":"kapot"}`)
		}
	})
	var pulled []string
	r := &RunExecutor{Root: f.root, Cmd: func(_ context.Context, _ string, argv, _ []string, out io.Writer) (int, error) {
		pulled = append(pulled, strings.Join(argv, " "))
		_, _ = io.WriteString(out, "Status: Downloaded newer image\n")
		return 0, nil
	}}
	run := &protocol.DockerAPIRun{Calls: []protocol.DockerCall{
		{Method: "POST", Path: "/containers/web/restart", Title: "web herstarten"},
		{Method: "POST", Path: "/containers/web/start"},
		{Method: "POST", Path: "/images/create", Query: map[string]string{"fromImage": "ghcr.io/x/y", "tag": "1.2"}},
		{Method: "POST", Path: "/images/prune"},
		{Method: "DELETE", Path: "/volumes/old", IgnoreMissing: true},
		{Method: "POST", Path: "/networks/create", Body: json.RawMessage(`{"Name":"front"}`)},
	}}
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code, err := r.dockerAPIRun(context.Background(), run, &out)
	if code != 0 || err != nil {
		t.Fatalf("%d %v\n%s", code, err, out.String())
	}
	for _, want := range []string{"[1/6] web herstarten", "stond al zo", "Downloaded newer image", "2 weg, 2.0 MiB vrij", "al weg", "klaar"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("uitvoer mist %q:\n%s", want, out.String())
		}
	}
	if len(pulled) != 1 || pulled[0] != "docker pull ghcr.io/x/y:1.2" {
		t.Fatalf("pull %v", pulled)
	}

	out.Reset()
	code, err = r.dockerAPIRun(context.Background(), &protocol.DockerAPIRun{Calls: []protocol.DockerCall{{Method: "POST", Path: "/containers/db/stop"}, {Method: "POST", Path: "/containers/web/restart"}}}, &out)
	if code == 0 || err == nil || !strings.Contains(err.Error(), "kapot") || strings.Contains(out.String(), "[2/2]") {
		t.Fatalf("fout stopt de run niet: %d %v\n%s", code, err, out.String())
	}
}

func TestDockerCallValidate(t *testing.T) {
	ok := []protocol.DockerCall{
		{Method: "POST", Path: "/containers/web_1/kill"},
		{Method: "DELETE", Path: "/images/ghcr.io/jonasz1996/clusterforge-server:abc"},
		{Method: "DELETE", Path: "/images/sha256:0123abcd"},
		{Method: "POST", Path: "/networks/front/connect", Body: json.RawMessage(`{"Container":"web"}`)},
		{Method: "POST", Path: "/build/prune", Query: map[string]string{"all": "true"}},
	}
	for _, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("%s %s: %v", c.Method, c.Path, err)
		}
	}
	bad := []protocol.DockerCall{
		{Method: "POST", Path: "/containers/create"},
		{Method: "POST", Path: "/containers/web/exec"},
		{Method: "POST", Path: "/containers/web/update"},
		{Method: "PUT", Path: "/containers/web/archive"},
		{Method: "POST", Path: "/swarm/init"},
		{Method: "POST", Path: "/plugins/pull"},
		{Method: "POST", Path: "/build"},
		{Method: "POST", Path: "/containers/web/stop", Body: json.RawMessage(`{}`)},
		{Method: "DELETE", Path: "/containers/../x"},
		{Method: "POST", Path: "/containers/web/start", Query: map[string]string{"a\nb": "1"}},
	}
	for _, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s %s mocht", c.Method, c.Path)
		}
	}
}
