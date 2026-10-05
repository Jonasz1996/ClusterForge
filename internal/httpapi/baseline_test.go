package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type baselineItem struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Enabled *bool  `json:"enabled"`
	Active  *bool  `json:"active"`
	Mode    string `json:"mode"`
	Size    int64  `json:"size"`
	Content bool   `json:"content"`
}

type baselineResult struct {
	Revision int `json:"revision"`
	Nodes    []struct {
		NodeID   string         `json:"node_id"`
		Hostname string         `json:"hostname"`
		Items    []baselineItem `json:"items"`
		Notes    []string       `json:"notes"`
	} `json:"nodes"`
}

type driftIgnore struct {
	ID        string     `json:"id"`
	NodeID    *string    `json:"node_id"`
	Hostname  *string    `json:"hostname"`
	Key       string     `json:"key"`
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expires_at"`
	Expired   bool       `json:"expired"`
	CreatedBy *struct {
		Name string `json:"name"`
	} `json:"created_by"`
}

type fieldErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field"`
}

// ignoredFinding is een afwijking met de velden die een negeerregel zet.
type ignoredFinding struct {
	Key      string  `json:"key"`
	Step     string  `json:"step"`
	Expected string  `json:"expected"`
	Ignored  bool    `json:"ignored"`
	IgnoreID *string `json:"ignore_id"`
}

type baselineReport struct {
	Source *struct {
		Kind         string     `json:"kind"`
		SpecRevision int        `json:"spec_revision"`
		BaselineAt   *time.Time `json:"baseline_at"`
		Items        *struct {
			Packages []string `json:"packages"`
			Services []string `json:"services"`
			Files    []string `json:"files"`
		} `json:"items"`
	} `json:"source"`
	Notes []string `json:"notes"`
	Nodes []struct {
		Hostname string           `json:"hostname"`
		Status   string           `json:"status"`
		Findings []ignoredFinding `json:"findings"`
	} `json:"nodes"`
}

func (r baselineReport) status(hostname string) string {
	for _, n := range r.Nodes {
		if n.Hostname == hostname {
			return n.Status
		}
	}
	return ""
}

func (r baselineReport) findings(hostname string) []ignoredFinding {
	for _, n := range r.Nodes {
		if n.Hostname == hostname {
			return n.Findings
		}
	}
	return nil
}

const nginxConf = "worker_processes auto;\nevents {}\n"

// TestBaseline is de acceptatie van mijlpaal 6: een cluster dat met de hand
// is aangemaakt krijgt een baseline, een gewijzigd nginx.conf geeft drift,
// en opnieuw vastleggen of negeren haalt die weg, zonder dat er ergens een
// hash van de inhoud te zien is.
func TestBaseline(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()

	var cl cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "prod"}, &cl); s != 201 {
		t.Fatalf("cluster: %d", s)
	}
	var other cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "ander", "name": "Ander", "type": "generic", "environment": "lab"}, &other)
	hosts := map[string]*agenttest.Host{}
	ids := map[string]string{}
	for i, name := range []string{"web01", "web02", "web03"} {
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": cl.ID}, &n); s != 201 {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			h := agenttest.NewHost(root, "10.0.30.1"+string(rune('1'+i)))
			ag.Exec, ag.Root, ag.Collector.Run = h.Exec, root, h.Exec
			if _, err := h.Exec(ctx, "apt-get", "install", "-y", "nginx"); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(root, "etc/nginx/nginx.conf"), nginxConf, 0o644)
			hosts[name] = h
		})
	}
	var loose node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "los01", "cluster_id": other.ID}, &loose)
	eventually(t, "agents verbonden", func() bool {
		for _, id := range ids {
			var n struct {
				Status string `json:"status"`
			}
			c.do("GET", "/api/v1/nodes/"+id, nil, &n)
			if n.Status != "healthy" {
				return false
			}
		}
		return true
	})

	driftURL := "/api/v1/clusters/" + cl.ID + "/drift"
	baseURL := driftURL + "/baseline"
	ignURL := driftURL + "/ignores"
	var raws []string // elk API-antwoord, om op hashes te controleren
	get := func(path string, out any) {
		t.Helper()
		var raw json.RawMessage
		if s := c.do("GET", path, nil, &raw); s != 200 {
			t.Fatalf("GET %s: %d", path, s)
		}
		raws = append(raws, string(raw))
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
	post := func(path string, body, out any) int {
		t.Helper()
		var raw json.RawMessage
		s := c.do("POST", path, body, &raw)
		raws = append(raws, string(raw))
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	actions := func(prefix string) []string {
		t.Helper()
		var out []string
		rows, err := e.pool.Query(ctx, "SELECT action FROM events WHERE action LIKE $1 ORDER BY id", prefix+"%")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var a string
			_ = rows.Scan(&a)
			out = append(out, a)
		}
		return out
	}

	// Zonder baseline is er niets om mee te vergelijken.
	var rep baselineReport
	get(driftURL, &rep)
	if rep.Source != nil || len(rep.Nodes) != 0 {
		t.Fatalf("drift zonder baseline: %+v", rep)
	}

	items := map[string]any{
		"packages": []string{"nginx", "keepalived"},
		"services": []string{"nginx.service", "bestaat-niet"},
		"files":    []string{"/etc/nginx/nginx.conf", "/etc/nginx", "/etc/ontbreekt.conf"},
	}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{"node_ids": []string{ids["web01"], ids["web02"]}}
		for k, v := range items {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	// Fouten noemen het veld.
	for _, tc := range []struct {
		body  map[string]any
		field string
	}{
		{with(map[string]any{"packages": []string{"Nginx!"}}), "packages"},
		{with(map[string]any{"services": []string{"a b"}}), "services"},
		{with(map[string]any{"files": []string{"etc/nginx/nginx.conf"}}), "files"},
		{with(map[string]any{"files": []string{"/etc/../etc/passwd"}}), "files"},
		{with(map[string]any{"packages": []string{}, "services": []string{" "}, "files": []string{}}), "packages"},
		{with(map[string]any{"node_ids": []string{}}), "node_ids"},
		{with(map[string]any{"node_ids": []string{loose.ID}}), "node_ids"},
	} {
		var fe fieldErr
		if s := c.do("POST", baseURL, tc.body, &fe); s != http.StatusBadRequest || fe.Field != tc.field || fe.Message == "" {
			t.Errorf("%v: %d %+v", tc.body, s, fe)
		}
	}

	// Eerst bekijken: niets wordt opgeslagen.
	var prev baselineResult
	if s := post(baseURL, with(map[string]any{"preview": true}), &prev); s != 200 || prev.Revision != 0 || len(prev.Nodes) != 2 {
		t.Fatalf("voorbeeld: %d %+v", s, prev)
	}
	for _, n := range prev.Nodes {
		kinds := map[string]baselineItem{}
		for _, it := range n.Items {
			kinds[it.Kind+":"+it.Name] = it
		}
		pkg, svc, file, dir := kinds["package:nginx"], kinds["service:nginx"], kinds["file:/etc/nginx/nginx.conf"], kinds["directory:/etc/nginx"]
		if len(n.Items) != 4 || pkg.Version != "1.0-1" || svc.Enabled == nil || !*svc.Enabled || svc.Active == nil || !*svc.Active ||
			!file.Content || file.Size != int64(len(nginxConf)) || file.Mode != "0644" || dir.Mode == "" {
			t.Fatalf("voorbeeld %s: %+v", n.Hostname, n.Items)
		}
		want := []string{"pakket keepalived is niet geïnstalleerd", "service bestaat-niet bestaat niet", "/etc/ontbreekt.conf bestaat niet"}
		if !slices.Equal(n.Notes, want) {
			t.Fatalf("opmerkingen %s: %q", n.Hostname, n.Notes)
		}
	}
	if ev := actions("drift.baseline"); len(ev) != 0 {
		t.Fatalf("events na een voorbeeld: %v", ev)
	}
	get(driftURL, &rep)
	if rep.Source != nil {
		t.Fatalf("voorbeeld legde een baseline vast: %+v", rep)
	}

	// Lezen mag iedereen, vastleggen en negeren alleen een beheerder.
	e.createUser("viewer", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("viewer", "een-lang-wachtwoord", "")
	if s := v.do("POST", baseURL, with(nil), nil); s != http.StatusForbidden {
		t.Fatalf("viewer legt vast: %d", s)
	}
	if s := v.do("POST", ignURL, map[string]any{"key": "file:/etc/nginx/nginx.conf", "reason": "test"}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer negeert: %d", s)
	}

	// Vastleggen: revisie 1, en meteen gecontroleerd.
	var res baselineResult
	if s := post(baseURL, with(nil), &res); s != 200 || res.Revision != 1 || len(res.Nodes) != 2 {
		t.Fatalf("vastleggen: %d %+v", s, res)
	}
	get(driftURL, &rep)
	if rep.Source == nil || rep.Source.Kind != "baseline" || rep.Source.SpecRevision != 1 || rep.Source.BaselineAt == nil ||
		rep.Source.Items == nil || !slices.Equal(rep.Source.Items.Files, []string{"/etc/nginx/nginx.conf", "/etc/nginx", "/etc/ontbreekt.conf"}) ||
		!slices.Equal(rep.Source.Items.Services, []string{"nginx", "bestaat-niet"}) {
		t.Fatalf("bron: %+v", rep.Source)
	}
	if rep.status("web01") != "in_sync" || rep.status("web02") != "in_sync" || rep.status("web03") != "none" {
		t.Fatalf("na vastleggen: %+v", rep)
	}
	if len(rep.Notes) != 1 || !strings.HasPrefix(rep.Notes[0], "web03 is een actieve node van dit cluster, maar heeft nog geen baseline") {
		t.Fatalf("opmerkingen: %q", rep.Notes)
	}
	if ev := actions("cluster.spec_changed"); len(ev) != 1 {
		t.Fatalf("spec_changed: %v", ev)
	}
	var setPayload struct {
		Revision int      `json:"revision"`
		Nodes    []string `json:"nodes"`
		Files    []string `json:"files"`
	}
	if err := e.pool.QueryRow(ctx, "SELECT payload FROM events WHERE action = 'drift.baseline_set'").Scan(&setPayload); err != nil {
		t.Fatal(err)
	}
	if setPayload.Revision != 1 || !slices.Equal(setPayload.Nodes, []string{"web01", "web02"}) || len(setPayload.Files) != 3 {
		t.Fatalf("drift.baseline_set: %+v", setPayload)
	}
	var list struct {
		Items []struct {
			ID    string `json:"id"`
			Drift struct {
				Status string `json:"status"`
			} `json:"drift"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/clusters", nil, &list)
	for _, it := range list.Items {
		want := map[string]string{cl.ID: "in_sync", other.ID: "none"}[it.ID]
		if it.Drift.Status != want {
			t.Fatalf("clusterlijst %s: %s, verwacht %s", it.ID, it.Drift.Status, want)
		}
	}

	// nginx.conf verandert met de hand op web02.
	changed := nginxConf + "# met de hand\n"
	writeFile(t, filepath.Join(hosts["web02"].Root, "etc/nginx/nginx.conf"), changed, 0o644)
	post(driftURL+"/check", nil, &rep)
	f2 := rep.findings("web02")
	if rep.status("web02") != "drift" || rep.status("web01") != "in_sync" || len(f2) != 1 ||
		f2[0].Key != "file:/etc/nginx/nginx.conf:content" || f2[0].Expected != "inhoud zoals vastgelegd" || f2[0].Ignored {
		t.Fatalf("na de wijziging: %+v", rep)
	}
	if ev := actions("drift."); !slices.Equal(ev, []string{"drift.baseline_set", "drift.detected"}) {
		t.Fatalf("events: %v", ev)
	}

	// Negeren op web02: meteen in orde, de afwijking blijft zichtbaar.
	var fe fieldErr
	for _, tc := range []struct {
		body  map[string]any
		field string
	}{
		{map[string]any{"key": "nginx.conf", "reason": "test"}, "key"},
		{map[string]any{"key": "file:/etc/*/x", "reason": "test"}, "key"},
		{map[string]any{"key": "*", "reason": "onderhoud"}, "expires_at"},
		{map[string]any{"key": "file:/etc/nginx/nginx.conf", "reason": "x"}, "reason"},
		{map[string]any{"key": "file:/etc/nginx/nginx.conf", "reason": "test", "expires_at": time.Now().Add(-time.Hour)}, "expires_at"},
		{map[string]any{"key": "file:/etc/nginx/nginx.conf", "reason": "test", "node_id": loose.ID}, "node_id"},
	} {
		if s := c.do("POST", ignURL, tc.body, &fe); s != http.StatusBadRequest || fe.Field != tc.field {
			t.Errorf("negeren %v: %d %+v", tc.body, s, fe)
		}
	}
	var ig driftIgnore
	if s := post(ignURL, map[string]any{
		"key": "file:/etc/nginx/nginx.conf", "reason": "test met een nieuwe worker-instelling", "node_id": ids["web02"],
	}, &ig); s != http.StatusCreated || ig.Hostname == nil || *ig.Hostname != "web02" || ig.CreatedBy == nil || ig.CreatedBy.Name != "admin" {
		t.Fatalf("negeren: %d %+v", s, ig)
	}
	get(driftURL, &rep)
	f2 = rep.findings("web02")
	if rep.status("web02") != "in_sync" || len(f2) != 1 || !f2[0].Ignored || f2[0].IgnoreID == nil || *f2[0].IgnoreID != ig.ID {
		t.Fatalf("na negeren: %+v", rep)
	}
	if ev := actions("drift."); !slices.Equal(ev, []string{"drift.baseline_set", "drift.detected", "drift.ignore_added", "drift.resolved"}) {
		t.Fatalf("events na negeren: %v", ev)
	}
	// Een nieuwe controle houdt het zo.
	post(driftURL+"/check", nil, &rep)
	if rep.status("web02") != "in_sync" || !rep.findings("web02")[0].Ignored {
		t.Fatalf("controle na negeren: %+v", rep)
	}
	var igs struct {
		Items []driftIgnore `json:"items"`
	}
	get(ignURL, &igs)
	if len(igs.Items) != 1 || igs.Items[0].ID != ig.ID || igs.Items[0].Reason != "test met een nieuwe worker-instelling" || igs.Items[0].Expired {
		t.Fatalf("negeerregels: %+v", igs)
	}
	var vigs struct {
		Items []driftIgnore `json:"items"`
	}
	if s := v.do("GET", ignURL, nil, &vigs); s != 200 || len(vigs.Items) != 1 {
		t.Fatalf("viewer leest negeerregels: %d", s)
	}

	// Opheffen: weer drift.
	if s := v.do("DELETE", "/api/v1/drift-ignores/"+ig.ID, nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer heft op: %d", s)
	}
	if s := c.do("DELETE", "/api/v1/drift-ignores/"+ig.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("opheffen: %d", s)
	}
	if s := c.do("DELETE", "/api/v1/drift-ignores/"+ig.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("nog eens opheffen: %d", s)
	}
	get(driftURL, &rep)
	if rep.status("web02") != "drift" || rep.findings("web02")[0].Ignored {
		t.Fatalf("na opheffen: %+v", rep)
	}

	// Een voorvoegsel voor het hele cluster.
	if s := post(ignURL, map[string]any{"key": "file:/etc/nginx/*", "reason": "nginx wordt omgebouwd"}, &ig); s != http.StatusCreated || ig.NodeID != nil {
		t.Fatalf("voorvoegsel: %d %+v", s, ig)
	}
	get(driftURL, &rep)
	if rep.status("web02") != "in_sync" {
		t.Fatalf("met voorvoegsel: %+v", rep)
	}

	// Een verlopen regel blijft zichtbaar maar telt niet meer.
	if _, err := e.pool.Exec(ctx, "UPDATE drift_ignores SET expires_at = now() - interval '1 minute' WHERE id = $1", ig.ID); err != nil {
		t.Fatal(err)
	}
	get(driftURL, &rep)
	if rep.status("web02") != "drift" {
		t.Fatalf("na verlopen: %+v", rep)
	}
	get(ignURL, &igs)
	if len(igs.Items) != 1 || !igs.Items[0].Expired {
		t.Fatalf("verlopen regel: %+v", igs)
	}

	// Alleen web02 opnieuw vastleggen: in orde, web01 houdt zijn baseline.
	var spec1 []byte
	_ = e.pool.QueryRow(ctx, "SELECT spec FROM clusters WHERE id = $1", cl.ID).Scan(&spec1)
	var re baselineResult
	if s := post(baseURL, map[string]any{
		"node_ids": []string{ids["web02"]}, "packages": items["packages"], "services": items["services"], "files": items["files"],
	}, &re); s != 200 || re.Revision != 2 || len(re.Nodes) != 1 {
		t.Fatalf("opnieuw vastleggen: %d %+v", s, re)
	}
	get(driftURL, &rep)
	if rep.status("web02") != "in_sync" || len(rep.findings("web02")) != 0 || rep.status("web01") != "in_sync" || rep.Source.SpecRevision != 2 {
		t.Fatalf("na opnieuw vastleggen: %+v", rep)
	}
	var spec2 []byte
	_ = e.pool.QueryRow(ctx, "SELECT spec FROM clusters WHERE id = $1", cl.ID).Scan(&spec2)
	web01Of := func(spec []byte) string {
		var b struct {
			Nodes []json.RawMessage `json:"nodes"`
		}
		_ = json.Unmarshal(spec, &b)
		for _, n := range b.Nodes {
			if strings.Contains(string(n), `"web01"`) {
				return string(n)
			}
		}
		return ""
	}
	if web01Of(spec1) == "" || web01Of(spec1) != web01Of(spec2) {
		t.Fatalf("web01 veranderde:\n%s\n%s", web01Of(spec1), web01Of(spec2))
	}

	// web03 erbij: de opmerking verdwijnt.
	if s := post(baseURL, map[string]any{"node_ids": []string{ids["web03"]}, "packages": []string{"nginx"}}, &re); s != 200 || re.Revision != 3 {
		t.Fatalf("web03 vastleggen: %d %+v", s, re)
	}
	get(driftURL, &rep)
	if rep.status("web03") != "in_sync" || len(rep.Notes) != 0 {
		t.Fatalf("met web03: %+v", rep)
	}
	get("/api/v1/nodes/"+ids["web03"]+"/drift", &rep)

	// Een cluster uit een template krijgt geen baseline.
	if _, err := e.pool.Exec(ctx, "UPDATE clusters SET template_name = 'keepalived-nginx' WHERE id = $1", other.ID); err != nil {
		t.Fatal(err)
	}
	if s := c.do("POST", "/api/v1/clusters/"+other.ID+"/drift/baseline", map[string]any{"node_ids": []string{loose.ID}, "packages": []string{"nginx"}}, nil); s != http.StatusConflict {
		t.Fatalf("baseline op een templatecluster: %d", s)
	}

	// Nergens een hash van de inhoud: niet in de API, niet in de database,
	// niet in het logboek.
	var stored string
	_ = e.pool.QueryRow(ctx, `SELECT (SELECT coalesce(string_agg(d::text, ' '), '') FROM drift_checks d) || ' ' ||
		(SELECT coalesce(string_agg(payload::text, ' '), '') FROM events) || ' ' ||
		(SELECT coalesce(string_agg(spec::text, ' '), '') FROM cluster_spec_revisions) || ' ' ||
		(SELECT coalesce(string_agg(spec::text, ' '), '') FROM clusters)`).Scan(&stored)
	for _, s := range []string{sha(nginxConf), sha(changed)} {
		if strings.Contains(stored, s) {
			t.Fatalf("de database bevat %s", s)
		}
		for _, raw := range raws {
			if strings.Contains(raw, s) {
				t.Fatalf("een API-antwoord bevat %s: %s", s, raw)
			}
		}
	}
	for _, raw := range raws {
		if strings.Contains(raw, "content_hmac") || strings.Contains(raw, "sha256") {
			t.Fatalf("een API-antwoord bevat een hash: %s", raw)
		}
	}
	if !strings.Contains(string(spec2), "content_hmac") {
		t.Fatalf("de baseline vergelijkt de inhoud niet: %s", spec2)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
