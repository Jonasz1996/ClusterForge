package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakePatroni is een REST-API van Patroni met wachtwoord, die bijhoudt wat
// de agent vroeg.
type fakePatroni struct {
	mu      sync.Mutex
	calls   []string
	pending bool
	config  string
	// status is de statuscode van de acties; 0 is 200.
	status int
	answer string
}

func (f *fakePatroni) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet && r.URL.Path == "/patroni" {
		pending := ""
		if f.pending {
			pending = `, "pending_restart": true`
		}
		_, _ = io.WriteString(w, `{"state": "running", "role": "replica", "patroni": {"scope": "nextcloud", "name": "nc2"}`+pending+`}`)
		return
	}
	if user, pass, ok := r.BasicAuth(); !ok || user != "clusterforge" || pass != "rest-geheim" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
	if r.URL.Path == "/config" {
		if r.Method == http.MethodPatch {
			f.config = `{"postgresql": {"parameters": {"max_connections": 60, "shared_buffers": "256MB"}}}`
		}
		_, _ = io.WriteString(w, f.config)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
	_, _ = io.WriteString(w, f.answer)
}

func (f *fakePatroni) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func TestPatroniOp(t *testing.T) {
	f := &fakePatroni{config: `{"postgresql": {"parameters": {"max_connections": 100, "shared_buffers": "256MB"}}}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	root := t.TempDir()
	a := &Agent{Root: root, PatroniURL: srv.URL}
	ctx := context.Background()
	op := func(req protocol.DBPatroniRequest) protocol.DBResponse { return a.PatroniOp(ctx, &req) }

	// Zonder clusterforge.env weet de agent het wachtwoord niet.
	if res := op(protocol.DBPatroniRequest{Action: protocol.DBPatroniReinit}); !strings.Contains(res.Error, "staat niet in /etc/patroni/clusterforge.env") {
		t.Fatalf("zonder wachtwoord: %+v", res)
	}
	env := filepath.Join(root, "etc/patroni/clusterforge.env")
	if err := os.MkdirAll(filepath.Dir(env), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("# wachtwoorden\nPATRONI_REPLICATION_PASSWORD=repl\nPATRONI_RESTAPI_PASSWORD=fout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := op(protocol.DBPatroniRequest{Action: protocol.DBPatroniReinit}); res.Error != "Patroni weigert het wachtwoord uit /etc/patroni/clusterforge.env" {
		t.Fatalf("verkeerd wachtwoord: %+v", res)
	}
	if err := os.WriteFile(env, []byte("PATRONI_RESTAPI_PASSWORD=rest-geheim\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Een switchover met rare namen gaat niet naar Patroni.
	for _, req := range []protocol.DBPatroniRequest{
		{Action: protocol.DBPatroniSwitchover, Leader: "nc1", Candidate: "nc1"},
		{Action: protocol.DBPatroniSwitchover, Leader: "nc1", Candidate: "nc2; rm -rf /"},
		{Action: protocol.DBPatroniSwitchover, Leader: "", Candidate: "nc2"},
	} {
		if res := op(req); !strings.Contains(res.Error, "ongeldige switchover") {
			t.Errorf("%+v: %+v", req, res)
		}
	}
	if res := op(protocol.DBPatroniRequest{Action: "failover"}); !strings.Contains(res.Error, "onbekende actie") {
		t.Errorf("failover: %+v", res)
	}
	if got := f.seen(); len(got) != 0 {
		t.Fatalf("naar Patroni: %q", got)
	}

	f.status, f.answer = http.StatusPreconditionFailed, "candidate name does not match with sync_standby"
	if res := op(protocol.DBPatroniRequest{Action: protocol.DBPatroniSwitchover, Leader: "nc1", Candidate: "nc3"}); res.Error !=
		"Patroni schakelde niet over (HTTP 412): candidate name does not match with sync_standby" {
		t.Errorf("412: %+v", res)
	}
	f.status, f.answer = 0, `Successfully switched over to "nc2"`
	res := op(protocol.DBPatroniRequest{Action: protocol.DBPatroniSwitchover, Leader: "nc1", Candidate: "nc2"})
	if res.Error != "" || res.Patroni == nil || !res.Patroni.Changed || res.Patroni.Message != `Successfully switched over to "nc2"` {
		t.Errorf("switchover: %+v", res)
	}
	if got := f.seen(); len(got) != 2 || got[1] != `POST /switchover {"candidate":"nc2","leader":"nc1"}` {
		t.Errorf("switchover: %q", got)
	}

	// Herstarten met pending alleen als Patroni het nodig vindt.
	if res := op(protocol.DBPatroniRequest{Action: protocol.DBPatroniRestart, Pending: true}); res.Patroni == nil || res.Patroni.Changed ||
		res.Patroni.Message != "geen herstart nodig" || len(f.seen()) != 0 {
		t.Errorf("geen herstart nodig: %+v", res)
	}
	f.pending, f.answer = true, "restarted successfully"
	if res := op(protocol.DBPatroniRequest{Action: protocol.DBPatroniRestart, Pending: true}); res.Patroni == nil || !res.Patroni.Changed {
		t.Errorf("herstart: %+v", res)
	}
	if got := f.seen(); len(got) != 1 || got[0] != `POST /restart {"restart_pending":true}` {
		t.Errorf("herstart: %q", got)
	}

	// Instellingen: alleen max_connections, als geheel getal, en alleen wat
	// anders is.
	for _, p := range []map[string]string{{"shared_buffers": "1GB"}, {"max_connections": "veel"}, {"max_connections": "0"}} {
		if res := op(protocol.DBPatroniRequest{Action: protocol.DBPatroniConfig, Parameters: p}); res.Error == "" {
			t.Errorf("%v: %+v", p, res)
		}
	}
	if got := f.seen(); len(got) != 0 {
		t.Errorf("ongeldige instellingen naar Patroni: %q", got)
	}
	res = op(protocol.DBPatroniRequest{Action: protocol.DBPatroniConfig, Parameters: map[string]string{"max_connections": "60"}})
	if res.Patroni == nil || !res.Patroni.Changed || res.Patroni.Message != "in etcd gezet: max_connections = 60" || res.Patroni.Parameters["max_connections"] != "60" {
		t.Errorf("config: %+v", res)
	}
	if got := f.seen(); len(got) != 2 || got[1] != `PATCH /config {"postgresql":{"parameters":{"max_connections":60}}}` {
		t.Errorf("config: %q", got)
	}
	res = op(protocol.DBPatroniRequest{Action: protocol.DBPatroniConfig, Parameters: map[string]string{"max_connections": "60"}})
	if res.Patroni == nil || res.Patroni.Changed || res.Patroni.Message != "de instellingen stonden al zo" {
		t.Errorf("config opnieuw: %+v", res)
	}
	if got := f.seen(); len(got) != 1 || got[0] != "GET /config" {
		t.Errorf("config opnieuw: %q", got)
	}
}
