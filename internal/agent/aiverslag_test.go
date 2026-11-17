package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakeAIV doet na wat AI-verslag doet bij het koppelen: een code alleen
// voor dezelfde site, een sleutel voor de code, en een 401 op /api/app/*
// zonder geldige sleutel.
type fakeAIV struct {
	mu     sync.Mutex
	codes  map[string]bool
	tokens map[string]int
	nextID int
	pairs  int
	posts  []string
	ext    bool
}

func newFakeAIV() *fakeAIV { return &fakeAIV{codes: map[string]bool{}, tokens: map[string]int{}} }

func (f *fakeAIV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	_, valid := f.tokens[tok]
	same := r.Header.Get("Origin") == "http://"+r.Host && r.Header.Get("X-Requested-With") == "aiverslag"
	switch {
	case r.URL.Path == "/api/app/ping":
		_, _ = w.Write([]byte(`{"app":"aiverslag"}`))
	case r.URL.Path == "/api/app/tokens" && r.Method == http.MethodPost:
		if !same {
			http.Error(w, `{"detail":"Forbidden"}`, http.StatusForbidden)
			return
		}
		c := "code" + string(rune('a'+len(f.codes)))
		f.codes[c] = true
		_, _ = w.Write([]byte(`{"code":"` + c + `"}`))
	case r.URL.Path == "/api/app/pair":
		var in struct{ Code string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if !f.codes[in.Code] {
			http.Error(w, `{"detail":"code ongeldig of verlopen"}`, http.StatusForbidden)
			return
		}
		delete(f.codes, in.Code)
		f.pairs++
		f.nextID++
		t := "tok" + string(rune('0'+f.nextID))
		f.tokens[t] = f.nextID
		_, _ = w.Write([]byte(`{"token":"` + t + `","type":"Bearer"}`))
	case r.URL.Path == "/api/app/tokens" && r.Method == http.MethodGet:
		var list []map[string]any
		for _, id := range f.tokens {
			list = append(list, map[string]any{"id": id, "naam": "ClusterForge"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": list})
	case strings.HasPrefix(r.URL.Path, "/api/app/tokens/") && r.Method == http.MethodDelete:
		for t, id := range f.tokens {
			if "/api/app/tokens/"+itoa(id) == r.URL.Path {
				delete(f.tokens, t)
			}
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	case !valid:
		http.Error(w, `{"detail":"geen geldige app-sleutel"}`, http.StatusUnauthorized)
	case r.URL.Path == "/api/inzicht/clusterforge":
		if !f.ext {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ext":"56.1"}`))
	case r.URL.Path == "/api/app/start":
		_, _ = w.Write([]byte(`{"niveau":"normaal"}`))
	case r.URL.Path == "/api/state":
		_, _ = w.Write([]byte(`{"running":false,"q":"` + r.URL.Query().Get("x") + `"}`))
	case r.Method == http.MethodPost:
		if strings.HasPrefix(r.URL.Path, "/api/inzicht/") && r.Header.Get("X-Inzicht") != "1" {
			http.Error(w, `{"fout":"kop X-Inzicht ontbreekt"}`, http.StatusForbidden)
			return
		}
		f.posts = append(f.posts, r.URL.Path)
		_, _ = w.Write([]byte(`{"ok":true}`))
	default:
		http.NotFound(w, r)
	}
}

func itoa(n int) string { return string(rune('0' + n)) }

func aivRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{"/opt/aiverslag/app.py", "/etc/systemd/system/aiverslag-web.service", "/opt/aiverslag/v2/kern.py", "/etc/aiverslag.env"} {
		if err := os.MkdirAll(filepath.Dir(root+p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(root+"/opt/aiverslag/app.py", []byte("app"), 0o644)
	_ = os.WriteFile(root+"/etc/systemd/system/aiverslag-web.service", []byte("ExecStart=/opt/aiverslag/venv/bin/uvicorn app:app --host 127.0.0.1 --port 8099\n"), 0o644)
	_ = os.WriteFile(root+"/opt/aiverslag/v2/kern.py", []byte("import os\nAPPVERSIE = \"55.0\"   # opmerking\n"), 0o644)
	_ = os.WriteFile(root+"/etc/aiverslag.env", []byte("OPN_KEY=geheim\nMODEL=qwen3.5:4b\n#MODEL=x\nMODEL=qwen3.5:9b\nCS_PASSWORD=geheim\n"), 0o600)
	return root
}

func TestAIVPairAndGet(t *testing.T) {
	f := newFakeAIV()
	srv := httptest.NewServer(f)
	defer srv.Close()
	aivBaseOverride = srv.URL
	defer func() { aivBaseOverride = "" }()
	root := aivRoot(t)
	h := pveHost{root: root, run: func(ctx context.Context, name string, args ...string) ([]byte, error) { return nil, os.ErrNotExist }}

	data, err := h.aivGet(context.Background(), "/api/state", map[string]string{"x": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"q":"1"`)) {
		t.Fatalf("antwoord %s", data)
	}
	tok, _ := os.ReadFile(root + aivTokenFile)
	if strings.TrimSpace(string(tok)) != "tok1" {
		t.Fatalf("sleutel %q", tok)
	}
	if fi, _ := os.Stat(root + aivTokenFile); fi.Mode().Perm() != 0o600 {
		t.Fatalf("rechten %v", fi.Mode())
	}

	// Ingetrokken sleutel: opnieuw koppelen, de oude koppeling verdwijnt.
	f.mu.Lock()
	delete(f.tokens, "tok1")
	f.mu.Unlock()
	if _, err := h.aivGet(context.Background(), "/api/state", nil); err != nil {
		t.Fatal(err)
	}
	if f.pairs != 2 || len(f.tokens) != 1 {
		t.Fatalf("koppelingen %d, sleutels %v", f.pairs, f.tokens)
	}

	// Een tweede koppeling naast de eigen: alleen de nieuwste blijft.
	f.mu.Lock()
	f.tokens["vreemd"] = 1
	f.mu.Unlock()
	c := h.aivClient(0)
	c.forgetOld(context.Background(), "tok2")
	if _, ok := f.tokens["vreemd"]; ok {
		t.Fatal("oude koppeling niet ingetrokken")
	}

	if _, err := h.aivGet(context.Background(), "/api/../etc", nil); err == nil {
		t.Fatal("ongeldig pad aanvaard")
	}
	if _, err := h.aivGet(context.Background(), "/api/onbekend", nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("fout %v", err)
	}
}

func TestAIVCallRun(t *testing.T) {
	f := newFakeAIV()
	srv := httptest.NewServer(f)
	defer srv.Close()
	aivBaseOverride = srv.URL
	defer func() { aivBaseOverride = "" }()
	root := aivRoot(t)
	r := &RunExecutor{Root: root}
	var out bytes.Buffer
	st := &protocol.RunStatus{}
	code, err := r.aivCall(context.Background(), &protocol.AIVCallSpec{Method: "POST", Path: "/api/run"}, &out, st)
	if err != nil || code != 0 {
		t.Fatalf("code %d, fout %v, uitvoer %s", code, err, out.String())
	}
	if len(f.posts) != 1 || f.posts[0] != "/api/run" || st.PVE == nil || string(st.PVE.Data) != `{"ok":true}` {
		t.Fatalf("posts %v, status %+v", f.posts, st.PVE)
	}
}

func TestAIVFacts(t *testing.T) {
	srv := httptest.NewServer(newFakeAIV())
	defer srv.Close()
	aivBaseOverride = srv.URL
	defer func() { aivBaseOverride = "" }()
	root := aivRoot(t)
	show := "Id=aiverslag-web.service\nLoadState=loaded\nActiveState=active\nSubState=running\nResult=success\nStateChangeTimestamp=@1763380000\n\n" +
		"Id=aiverslag-github.timer\nLoadState=not-found\nActiveState=inactive\nSubState=dead\nResult=success\nStateChangeTimestamp=\n\n" +
		"Id=aiverslag-run.service\nLoadState=loaded\nActiveState=failed\nSubState=failed\nResult=exit-code\nStateChangeTimestamp=@1763380100\n"
	h := pveHost{root: root, run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			return []byte(show), nil
		case "df":
			return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\nrpool/data/subvol-151-disk-0 78643200 61865984 16777216 79% /\n"), nil
		}
		return nil, os.ErrNotExist
	}}
	f := h.aivFacts(context.Background())
	if f == nil {
		t.Fatal("geen facts")
	}
	if f.Version != "55.0" || f.Model != "qwen3.5:9b" || !f.Reachable || f.Paired || f.DataUsedPct != 79 {
		t.Fatalf("facts %+v", f)
	}
	if len(f.Units) != 2 || f.Units[1].Name != "aiverslag-run.service" || f.Units[1].Result != "exit-code" || f.Units[0].Since != 1763380000 {
		t.Fatalf("units %+v", f.Units)
	}
	if (pveHost{root: t.TempDir()}).aivFacts(context.Background()) != nil {
		t.Fatal("facts zonder AI-verslag")
	}
}

func TestAIVInstall(t *testing.T) {
	f := newFakeAIV()
	srv := httptest.NewServer(f)
	defer srv.Close()
	aivBaseOverride = srv.URL
	defer func() { aivBaseOverride = "" }()
	aivStartWait, aivStartPoll = 300*time.Millisecond, 50*time.Millisecond
	root := aivRoot(t)
	app := "import ext\nimport ext55; ext55.routes(app, guard)\n"
	_ = os.WriteFile(root+"/opt/aiverslag/app.py", []byte(app), 0o644)
	var cmds []string
	r := &RunExecutor{Root: root, Cmd: func(ctx context.Context, user string, argv, env []string, out io.Writer) (int, error) {
		cmds = append(cmds, strings.Join(argv, " "))
		if argv[0] == "systemctl" && argv[1] == "restart" {
			f.mu.Lock()
			f.ext = strings.Contains(readFile(root+"/opt/aiverslag/app.py"), "ext56.routes")
			f.mu.Unlock()
		}
		return 0, nil
	}}
	code := "VERSIE = '56.1'\n"
	sum := sha256.Sum256([]byte(code))
	ext := &protocol.AIVExtension{Name: "ext56", Code: code, SHA256: hex.EncodeToString(sum[:])}
	var out bytes.Buffer
	if c, err := r.aivInstall(context.Background(), ext, &out); err != nil || c != 0 {
		t.Fatalf("code %d, fout %v\n%s", c, err, out.String())
	}
	got := readFile(root + "/opt/aiverslag/app.py")
	if got != app+"import ext56; ext56.routes(app, guard)\n" || readFile(root+"/opt/aiverslag/ext56.py") != code {
		t.Fatalf("app.py %q", got)
	}
	// Een tweede keer: de regel staat er al één keer.
	if _, err := r.aivInstall(context.Background(), ext, &out); err != nil || strings.Count(readFile(root+"/opt/aiverslag/app.py"), "ext56") != 2 {
		t.Fatalf("tweede keer: %v", err)
	}

	// De webdienst start niet met de extensie: alles terug.
	_ = os.WriteFile(root+"/opt/aiverslag/app.py", []byte(app), 0o644)
	_ = os.Remove(root + "/opt/aiverslag/ext56.py")
	r.Cmd = func(ctx context.Context, user string, argv, env []string, out io.Writer) (int, error) {
		f.mu.Lock()
		f.ext = false
		f.mu.Unlock()
		return 0, nil
	}
	if _, err := r.aivInstall(context.Background(), ext, &out); err == nil {
		t.Fatal("geen fout")
	}
	if readFile(root+"/opt/aiverslag/app.py") != app {
		t.Fatal("app.py niet teruggezet")
	}
	if _, err := os.Stat(root + "/opt/aiverslag/ext56.py"); err == nil {
		t.Fatal("ext56.py blijft staan")
	}
	ext.SHA256 = "00"
	if _, err := r.aivInstall(context.Background(), ext, &out); err == nil {
		t.Fatal("verkeerde controlesom aanvaard")
	}
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}
