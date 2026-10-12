package agenttest

// Patroni op een nagespeelde machine: de REST-API die de agent leest
// (/patroni en /cluster), die cf-agent check db postgresql vraagt
// (/primary), en de acties met wachtwoord (switchover, reinitialize,
// restart en config) via Host.PatroniAPI.

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Patroni is wat de nagespeelde Patroni over de node zegt. Role is primary
// of replica, State zoals running of starting, Replication zoals streaming
// bij een replica die bijhoudt, en Sync true voor de synchrone replica.
type Patroni struct {
	Scope       string
	Role        string
	State       string
	Timeline    int
	Replication string
	Sync        bool
	// Name is de naam van de node in Patroni; Lag hoeveel WAL hij als
	// replica nog niet heeft, in /cluster; PendingRestart: een instelling
	// wacht op een herstart.
	Name           string
	Lag            *int64
	PendingRestart bool
}

// PatroniAPI speelt een actie van de REST-API van Patroni na, met de
// methode, het pad en de body: POST /switchover, /reinitialize en /restart,
// en GET en PATCH /config. Het antwoord is de statuscode en de body. Het
// draait buiten het slot van de host, dus het mag de host veranderen.
type PatroniAPI func(method, path string, body []byte) (int, string)

// ServePatroni start de REST-API van Patroni voor deze machine en geeft
// haar adres, voor Agent.PatroniURL. Zolang h.Patroni nil is, verbreekt ze
// elke verbinding, zoals een Patroni die niet draait. Ze stopt aan het eind
// van de test.
func (h *Host) ServePatroni(t testing.TB) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		var p *Patroni
		if h.Patroni != nil {
			c := *h.Patroni
			p = &c
		}
		primary := h.patroniPrimary()
		h.mu.Unlock()
		if p == nil {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path == "/config" {
			h.patroniAction(w, r)
			return
		}
		info := map[string]any{"scope": p.Scope, "version": "4.0.4"}
		if p.Name != "" {
			info["name"] = p.Name
		}
		body := map[string]any{"state": p.State, "role": p.Role, "timeline": p.Timeline, "patroni": info}
		if p.Replication != "" {
			body["replication_state"] = p.Replication
		}
		if p.Sync {
			body["sync_standby"] = true
		}
		if p.PendingRestart {
			body["pending_restart"] = true
		}
		code := http.StatusOK
		switch r.URL.Path {
		case "/cluster":
			member := map[string]any{"name": p.Name, "role": p.Role, "state": p.State}
			if p.Lag != nil {
				member["lag"] = *p.Lag
			}
			body = map[string]any{"members": []any{member}}
		case "/patroni":
		case "/primary", "/leader":
			if !primary {
				code = http.StatusServiceUnavailable
			}
		case "/replica":
			if p.Role != "replica" || p.State != "running" {
				code = http.StatusServiceUnavailable
			}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// patroniAction is een actie met wachtwoord: het wachtwoord moet kloppen
// met PATRONI_RESTAPI_PASSWORD in /etc/patroni/clusterforge.env, en
// PatroniAPI doet de rest.
func (h *Host) patroniAction(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if want := h.patroniPassword(); !ok || want == "" || user != "clusterforge" || pass != want {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "no auth header received")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	h.mu.Lock()
	api := h.PatroniAPI
	h.mu.Unlock()
	if api == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "agenttest: geen PatroniAPI")
		return
	}
	h.mu.Lock()
	h.record("patroni " + r.Method + " " + r.URL.Path + " " + strings.TrimSpace(string(body)))
	h.mu.Unlock()
	code, out := api(r.Method, r.URL.Path, body)
	w.WriteHeader(code)
	_, _ = io.WriteString(w, out)
}

// patroniPassword leest het wachtwoord van de REST-API zoals de uitrol het
// op de machine zette.
func (h *Host) patroniPassword() string {
	f, err := os.Open(filepath.Join(h.Root, "etc/patroni/clusterforge.env"))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PATRONI_RESTAPI_PASSWORD="); ok {
			return v
		}
	}
	return ""
}

// patroniPrimary is cf-agent check db postgresql: Patroni zegt dat deze
// node de primary is en PostgreSQL draait. De aanroeper heeft het slot.
func (h *Host) patroniPrimary() bool {
	if u := h.units["patroni"]; u != nil && !u.Active {
		return false
	}
	p := h.Patroni
	return p != nil && p.Role == "primary" && p.State == "running"
}
