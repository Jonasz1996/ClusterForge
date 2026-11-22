package selfhost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

func TestNormalizeImage(t *testing.T) {
	s := &Service{Image: "ghcr.io/jonasz1996/clusterforge-server:b9116ae"}
	for in, want := range map[string]string{
		"c68510c":           "ghcr.io/jonasz1996/clusterforge-server:c68510c",
		" :pr-151 ":         "ghcr.io/jonasz1996/clusterforge-server:pr-151",
		"ghcr.io/x/y:1.2.3": "ghcr.io/x/y:1.2.3",
	} {
		got, err := s.NormalizeImage(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, wil %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "tag met spatie", "b9116ae-scanner", "ghcr.io/x/y:$(id)", "a;b"} {
		if got, err := s.NormalizeImage(in); err == nil {
			t.Errorf("%q aanvaard als %q", in, got)
		}
	}
	// Zonder CF_IMAGE: de repository van CI met de versie.
	s = &Service{Version: "abc1234"}
	if s.CurrentImage() != DefaultRepo+":abc1234" || s.Repo() != DefaultRepo {
		t.Errorf("zonder CF_IMAGE: %q %q", s.CurrentImage(), s.Repo())
	}
	if (&Service{Version: "dev"}).CurrentImage() != "" {
		t.Error("dev is geen image")
	}
}

func TestParseRole(t *testing.T) {
	r := parseRole([]string{"== rol", "index=2", "vip=1", "verhuisd=1", "actief=0", "image=ghcr.io/a/b:c"})
	if r.Index != 2 || !r.VIP || !r.Moved || r.Active || r.Image != "ghcr.io/a/b:c" {
		t.Fatalf("%+v", r)
	}
	vip, rest, err := vipOf([]Role{{Hostname: "a", Index: 1}, r, {Hostname: "c", Index: 3}})
	if err != nil || vip.Index != 2 || len(rest) != 2 {
		t.Fatalf("%+v %+v %v", vip, rest, err)
	}
	if _, _, err := vipOf([]Role{{VIP: true}, {VIP: true}}); err == nil {
		t.Error("split-brain niet gezien")
	}
	if _, _, err := vipOf([]Role{{}}); err == nil {
		t.Error("geen VIP niet gezien")
	}
}

func TestCheckImage(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:jonasz1996/clusterforge-server:pull" {
				http.Error(w, "scope", 400)
				return
			}
			_, _ = w.Write([]byte(`{"token":"t0k"}`))
		case r.Header.Get("Authorization") != "Bearer t0k":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="ghcr.io",scope="repository:jonasz1996/clusterforge-server:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/v2/jonasz1996/clusterforge-server/manifests/b9116ae":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	s := &Service{HTTP: srv.Client()}
	ok, err := s.CheckImage(context.Background(), host+"/jonasz1996/clusterforge-server:b9116ae")
	if err != nil || !ok {
		t.Fatalf("bestaande tag: %v %v", ok, err)
	}
	ok, err = s.CheckImage(context.Background(), host+"/jonasz1996/clusterforge-server:bestaatniet")
	if err != nil || ok {
		t.Fatalf("onbekende tag: %v %v", ok, err)
	}
}

// De scripts zijn geldig sh, met de waarden veilig erin.
func TestScripts(t *testing.T) {
	dir := t.TempDir()
	for name, script := range map[string]string{
		"update.sh": UpdateScript("ghcr.io/jonasz1996/clusterforge-server:b9116ae", 7*time.Minute, false),
		"export.sh": ExportScript(uuid.New()),
		"stop.sh":   StopScript(),
		"ctl.sh":    ctl("overnemen", "http://192.0.2.1:8080", "it's"),
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("sh", "-n", p).CombinedOutput(); err != nil {
			t.Errorf("%s: %s", name, out)
		}
		if _, err := exec.LookPath("shellcheck"); err == nil {
			if out, err := exec.Command("shellcheck", "-s", "sh", "-S", "warning", p).CombinedOutput(); err != nil {
				t.Errorf("shellcheck %s:\n%s", name, out)
			}
		}
	}
	if !strings.Contains(UpdateScript("x/y:z", time.Minute, true), "WAIT='60'\nKEEP='5'\nDRY='1'") {
		t.Error("header")
	}
	if !strings.Contains(ctl("a", "it's"), `'it'\''s'`) {
		t.Error("quoting")
	}
}

// De dump-controle leest de dump in de exportmap.
func TestSumDump(t *testing.T) {
	s := &Service{ExportDir: t.TempDir()}
	id := uuid.New()
	if _, _, err := s.sumDump(id); err == nil {
		t.Fatal("geen dump, geen fout")
	}
	if err := os.WriteFile(s.dumpPath(id), []byte("PGDMP"), 0o400); err != nil {
		t.Fatal(err)
	}
	sum, n, err := s.sumDump(id)
	if err != nil || n != 5 || len(sum) != 64 {
		t.Fatalf("%s %d %v", sum, n, err)
	}
}

func TestMoveExport(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ev := events.NewWriter(store.New(pool), log)
	runner := jobs.NewRunner(pool, ev, log)
	key := make([]byte, 32)
	key[0] = 7
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(pool, ev, log, runner, nil, nil, box, nil)
	s.Version, s.Image, s.MasterKey = "b9116ae", "ghcr.io/jonasz1996/clusterforge-server:b9116ae", key
	s.ExportDir = t.TempDir()
	s.Settings = map[string]string{"CF_SECURE_COOKIES": "false"}
	var cluster uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment, template_name, template_version)
		VALUES ('clusterforge', 'ClusterForge', 'generic', 'prod', 'clusterforge', '1.0.0') RETURNING id`).Scan(&cluster); err != nil {
		t.Fatal(err)
	}
	host := uuid.New()
	if _, err := s.StartMove(ctx, events.System(), uuid.New(), host, "http://192.0.2.153:8080", false, true); err == nil {
		t.Fatal("verhuizen naar een cluster zonder de template")
	}
	j, err := s.StartMove(ctx, events.System(), cluster, host, "http://192.0.2.153:8080/", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartMove(ctx, events.System(), cluster, host, "http://192.0.2.153:8080", false, true); err == nil {
		t.Fatal("twee verhuizingen tegelijk")
	}
	var p moveParams
	if err := json.Unmarshal(j.Params, &p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(j.Params), "master_key") || p.ServerURL != "http://192.0.2.153:8080" {
		t.Fatalf("params: %s", j.Params)
	}
	token, err := s.token(p)
	if err != nil || len(token) != 64 || strings.Contains(string(j.Params), token) {
		t.Fatalf("sleutel: %q %v", token, err)
	}
	get := func(tok, what string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeExport(w, httptest.NewRequest(http.MethodGet, "/selfexport/"+tok+"/"+what, nil), tok, what)
		return w
	}
	// Alleen een lopende taak geeft iets.
	if w := get(token, "meta"); w.Code != http.StatusForbidden {
		t.Fatalf("wachtende taak: %d", w.Code)
	}
	if _, err := pool.Exec(ctx, "UPDATE jobs SET status = 'running' WHERE id = $1", j.ID); err != nil {
		t.Fatal(err)
	}
	if w := get(strings.Repeat("0", 64), "meta"); w.Code != http.StatusForbidden {
		t.Fatalf("verkeerde sleutel: %d", w.Code)
	}
	if w := get(token, "meta"); w.Code != http.StatusConflict {
		t.Fatalf("zonder dump: %d", w.Code)
	}
	if err := os.WriteFile(s.dumpPath(j.ID), []byte("PGDMP-test"), 0o400); err != nil {
		t.Fatal(err)
	}
	w := get(token, "meta")
	body := w.Body.String()
	for _, want := range []string{"job=" + j.ID.String(), "image=ghcr.io/jonasz1996/clusterforge-server:b9116ae", "master_key=" + base64.StdEncoding.EncodeToString(key),
		"env.CF_SECURE_COOKIES=false", "sha256="} {
		if !strings.Contains(body, want) {
			t.Errorf("meta mist %q:\n%s", want, body)
		}
	}
	if w := get(token, "dump"); w.Body.String() != "PGDMP-test" {
		t.Fatalf("dump: %q", w.Body.String())
	}
	in, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if in.Mode != ModeCompose || len(in.Targets) != 1 || in.Targets[0].Slug != "clusterforge" || len(in.Jobs) != 1 || !in.Export {
		t.Fatalf("info: %+v", in)
	}
	// Na de taak werkt de sleutel niet meer.
	if _, err := pool.Exec(ctx, "UPDATE jobs SET status = 'succeeded' WHERE id = $1", j.ID); err != nil {
		t.Fatal(err)
	}
	if w := get(token, "dump"); w.Code != http.StatusForbidden {
		t.Fatalf("na de taak: %d", w.Code)
	}
}
