package agenttest

// Patroni op een nagespeelde machine: de REST-API die de agent leest
// (/patroni) en die cf-agent check db postgresql vraagt (/primary).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
}

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
		body := map[string]any{"state": p.State, "role": p.Role, "timeline": p.Timeline, "patroni": map[string]any{"scope": p.Scope, "version": "4.0.4"}}
		if p.Replication != "" {
			body["replication_state"] = p.Replication
		}
		if p.Sync {
			body["sync_standby"] = true
		}
		code := http.StatusOK
		switch r.URL.Path {
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

// patroniPrimary is cf-agent check db postgresql: Patroni zegt dat deze
// node de primary is en PostgreSQL draait. De aanroeper heeft het slot.
func (h *Host) patroniPrimary() bool {
	if u := h.units["patroni"]; u != nil && !u.Active {
		return false
	}
	p := h.Patroni
	return p != nil && p.Role == "primary" && p.State == "running"
}
