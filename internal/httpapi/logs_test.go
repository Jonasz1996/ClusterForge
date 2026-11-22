package httpapi

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

type logNodeT struct {
	ID        string `json:"id"`
	Hostname  string `json:"hostname"`
	Supported bool   `json:"supported"`
	Online    bool   `json:"online"`
}

type logResultT struct {
	Sources []struct {
		Hostname  string `json:"hostname"`
		Label     string `json:"label"`
		Error     string `json:"error"`
		Count     int    `json:"count"`
		Truncated bool   `json:"truncated"`
	} `json:"sources"`
	Entries []struct {
		Source    int        `json:"source"`
		TS        *time.Time `json:"ts"`
		Inherited bool       `json:"inherited"`
		Level     string     `json:"level"`
		Proc      string     `json:"proc"`
		Msg       string     `json:"msg"`
	} `json:"entries"`
	Levels map[string]int `json:"levels"`
}

func TestLogsCompareNodes(t *testing.T) {
	e, c := adminClient(t)
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "galera", "name": "Galera", "type": "keepalived", "environment": "lab"}, &cl)
	var n1, n2 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "nc1", "cluster_id": cl.ID}, &n1)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "nc2", "cluster_id": cl.ID}, &n2)
	d1 := startDockerNode(t, e, c, n1.ID, strings.Repeat("a", 32))
	d2 := startDockerNode(t, e, c, n2.ID, strings.Repeat("b", 32))
	writeStackFile(t, filepath.Join(d1.root, "var/log/syslog"),
		"2026-11-16T12:00:01+00:00 nc1 mariadbd[1]: [Note] WSREP: gestart\n"+
			"2026-11-16T12:00:03+00:00 nc1 mariadbd[1]: [ERROR] WSREP: verbinding weg password=geheim\n"+
			"  vervolg van de fout\n")
	writeStackFile(t, filepath.Join(d2.root, "var/log/syslog"),
		"2026-11-16T12:00:02+00:00 nc2 mariadbd[1]: [Warning] WSREP: traag\n")

	// Wachten tot beide agents zich melden.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var out struct {
			Nodes    []logNodeT `json:"nodes"`
			Protocol int        `json:"protocol"`
		}
		if s := c.do("GET", "/api/v1/logs/nodes", nil, &out); s != http.StatusOK {
			t.Fatalf("nodes: %d", s)
		}
		ok := 0
		for _, n := range out.Nodes {
			if n.Supported && n.Online {
				ok++
			}
		}
		if ok == 2 && out.Protocol == 34 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agents niet klaar: %+v", out)
		}
		time.Sleep(50 * time.Millisecond)
	}

	var src struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
		Docker bool `json:"docker"`
	}
	if s := c.do("GET", "/api/v1/nodes/"+n1.ID+"/log-sources", nil, &src); s != http.StatusOK || len(src.Files) != 1 || src.Files[0].Path != "/var/log/syslog" {
		t.Fatalf("bronnen: %d %+v", s, src)
	}

	// Vergelijken: beide nodes op één tijdlijn; nc3 bestaat niet als bron
	// maar een ontbrekend bestand geeft een fout per bron.
	q := map[string]any{"sources": []map[string]any{
		{"node_id": n1.ID, "kind": "file", "name": "/var/log/syslog"},
		{"node_id": n2.ID, "kind": "file", "name": "/var/log/syslog"},
		{"node_id": n2.ID, "kind": "file", "name": "/var/log/weg.log"},
	}}
	var res logResultT
	if s := c.do("POST", "/api/v1/logs/query", q, &res); s != http.StatusOK {
		t.Fatalf("query: %d", s)
	}
	if len(res.Entries) != 3 || res.Entries[0].Source != 0 || res.Entries[1].Source != 1 || res.Entries[2].Source != 0 {
		t.Fatalf("tijdlijn: %+v", res.Entries)
	}
	if e := res.Entries[2]; e.Level != "error" || e.Proc != "mariadbd" || !strings.Contains(e.Msg, "vervolg van de fout") || strings.Contains(e.Msg, "geheim") {
		t.Fatalf("fout: %+v", e)
	}
	if res.Sources[0].Hostname != "nc1" || res.Sources[1].Count != 1 || !strings.Contains(res.Sources[2].Error, "bestaat niet") || res.Levels["error"] != 1 || res.Levels["warn"] != 1 {
		t.Fatalf("bronnen: %+v %v", res.Sources, res.Levels)
	}
	// Alleen fouten en waarschuwingen met een zoekterm.
	q["sources"] = q["sources"].([]map[string]any)[:2]
	q["levels"] = []string{"error"}
	q["pattern"] = "wsrep"
	q["ignore_case"] = true
	if s := c.do("POST", "/api/v1/logs/query", q, &res); s != http.StatusOK || len(res.Entries) != 1 {
		t.Fatalf("filter: %d %+v", s, res.Entries)
	}
	// Ongeldige invoer.
	if s := c.do("POST", "/api/v1/logs/query", map[string]any{"sources": []map[string]any{{"node_id": n1.ID, "kind": "file", "name": "/etc/shadow"}}}, nil); s != http.StatusBadRequest {
		t.Fatalf("buiten /var/log: %d", s)
	}
	if s := c.do("POST", "/api/v1/logs/query", map[string]any{"sources": []map[string]any{{"node_id": n1.ID, "kind": "file", "name": "/var/log/syslog"}}, "pattern": "(", "regex": true}, nil); s != http.StatusBadRequest {
		t.Fatalf("regex: %d", s)
	}

	// Weergaven bewaren, aanpassen, verwijderen.
	view := map[string]any{"name": "Galera-fouten", "cluster_id": cl.ID, "sources": q["sources"], "filter": map[string]any{"range": "1h", "levels": []string{"error"}}}
	var v struct {
		ID     string         `json:"id"`
		Name   string         `json:"name"`
		Filter map[string]any `json:"filter"`
	}
	if s := c.do("POST", "/api/v1/logs/views", view, &v); s != http.StatusCreated || v.Filter["range"] != "1h" {
		t.Fatalf("bewaren: %d %+v", s, v)
	}
	if s := c.do("POST", "/api/v1/logs/views", view, nil); s != http.StatusBadRequest {
		t.Fatalf("dubbele naam: %d", s)
	}
	view["name"] = "Galera"
	if s := c.do("PUT", "/api/v1/logs/views/"+v.ID, view, &v); s != http.StatusOK || v.Name != "Galera" {
		t.Fatalf("aanpassen: %d %+v", s, v)
	}
	var list struct {
		Views []struct {
			Name    string `json:"name"`
			Sources []any  `json:"sources"`
		} `json:"views"`
	}
	if s := c.do("GET", "/api/v1/logs/views", nil, &list); s != http.StatusOK || len(list.Views) != 1 || len(list.Views[0].Sources) != 2 {
		t.Fatalf("lijst: %d %+v", s, list)
	}
	if s := c.do("DELETE", "/api/v1/logs/views/"+v.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("verwijderen: %d", s)
	}

	// Een kijker mag geen logs lezen.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := viewer.do("POST", "/api/v1/logs/query", q, nil); s != http.StatusForbidden {
		t.Fatalf("kijker: %d", s)
	}
}
