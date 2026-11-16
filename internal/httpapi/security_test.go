package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func TestSecurityEndToEnd(t *testing.T) {
	e, c := adminClient(t)
	feeds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/debian":
			_, _ = w.Write([]byte(`{"openssl":{"CVE-2025-1111":{"description":"lek","scope":"remote","releases":{"trixie":{"status":"resolved","fixed_version":"3.5.1-1+deb13u1","urgency":"high"}}}}}`))
		case "/kev":
			_, _ = w.Write([]byte(`{"vulnerabilities":[{"cveID":"CVE-2099-0001"}]}`))
		}
	}))
	defer feeds.Close()
	e.api.security.DebianURL, e.api.security.KEVURL = feeds.URL+"/debian", feeds.URL+"/kev"

	var web01 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01"}, &web01)
	var tok newToken
	if s := c.do("POST", "/api/v1/enrollment-tokens", map[string]any{"node_id": web01.ID}, &tok); s != http.StatusCreated {
		t.Fatalf("token: %d", s)
	}
	root := procRoot(t, strings.Repeat("5", 32))
	writeFile(t, filepath.Join(root, "etc/os-release"), "ID=debian\nVERSION_ID=\"13\"\nVERSION_CODENAME=trixie\n", 0o644)
	cfg, err := agent.Enroll(context.Background(), agent.EnrollOptions{ServerURL: e.srv.URL, AllowHTTP: true, Token: tok.Token, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{
		Config: cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Collector: fakeCollector(root),
		HeartbeatInterval: 100_000_000, StatePath: filepath.Join(root, "var/lib/clusterforge/agent-state.json"), Root: root,
		Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "dpkg-query" {
				return []byte("ii \topenssl\t3.5.1-1\tamd64\topenssl\t3.5.1-1\nii \tlibssl3t64\t3.5.1-1\tamd64\topenssl\t3.5.1-1\n"), nil
			}
			return nil, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = ag.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	type finding struct {
		ID       string `json:"id"`
		Key      string `json:"key"`
		Severity string `json:"severity"`
		Hostname string `json:"hostname"`
		Status   string `json:"status"`
	}
	var list struct {
		Items []finding `json:"items"`
	}
	eventually(t, "bevinding voor openssl", func() bool {
		if s := c.do("POST", "/api/v1/security/refresh", map[string]any{}, nil); s != http.StatusAccepted {
			t.Fatalf("verversen: %d", s)
		}
		c.do("GET", "/api/v1/security/findings", nil, &list)
		return len(list.Items) == 1
	})
	if f := list.Items[0]; f.Key != "openssl" || f.Severity != "hoog" || f.Hostname != "web01" {
		t.Fatalf("bevinding = %+v", f)
	}
	var o struct {
		Stats struct {
			Open         int `json:"open"`
			NodesChecked int `json:"nodes_checked"`
		} `json:"stats"`
		Feeds []struct {
			Name    string `json:"name"`
			Entries int    `json:"entries"`
		} `json:"feeds"`
	}
	if s := c.do("GET", "/api/v1/security", nil, &o); s != 200 || o.Stats.Open != 1 || o.Stats.NodesChecked != 1 || o.Feeds[0].Entries != 1 {
		t.Fatalf("overzicht: %d %+v", s, o)
	}
	var f finding
	if s := c.do("POST", "/api/v1/security/findings/"+list.Items[0].ID+"/accept", map[string]any{"note": "Alleen intern", "days": 30}, &f); s != 200 || f.Status != "aanvaard" {
		t.Fatalf("aanvaarden: %d %+v", s, f)
	}

	// Scopes: publiek adres vraagt een bevestiging; een kijker mag niets veranderen.
	if s := c.do("POST", "/api/v1/security/scopes", map[string]any{"name": "VPS", "cidrs": []string{"203.0.113.9"}}, nil); s != 400 {
		t.Fatalf("publiek zonder bevestiging: %d", s)
	}
	var sc struct {
		ID        string  `json:"id"`
		Addresses float64 `json:"addresses"`
	}
	if s := c.do("POST", "/api/v1/security/scopes", map[string]any{"name": "Thuis", "cidrs": []string{"192.168.10.0/24", "192.168.113.0/24"}}, &sc); s != 201 || sc.Addresses != 512 {
		t.Fatalf("scope: %d %+v", s, sc)
	}
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	if s := v.do("DELETE", "/api/v1/security/scopes/"+sc.ID, nil, nil); s != 403 {
		t.Fatalf("kijker verwijdert: %d", s)
	}
	if s := v.do("GET", "/api/v1/security/scopes", nil, nil); s != 200 {
		t.Fatalf("kijker leest: %d", s)
	}
	if s := c.do("DELETE", "/api/v1/security/scopes/"+sc.ID, nil, nil); s != 204 {
		t.Fatalf("verwijderen: %d", s)
	}
}
