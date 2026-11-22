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

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

type remediationPlan struct {
	Slug              string   `json:"slug"`
	Environment       string   `json:"environment"`
	NeedsConfirmation bool     `json:"needs_confirmation"`
	Notes             []string `json:"notes"`
	Nodes             []struct {
		Hostname string   `json:"hostname"`
		VIPs     []string `json:"vips"`
		Ignored  []string `json:"ignored"`
		Steps    []struct {
			Step   string `json:"step"`
			Action string `json:"action"`
		} `json:"steps"`
	} `json:"nodes"`
}

type remediationResult struct {
	Plan remediationPlan `json:"plan"`
	Job  *job            `json:"job"`
}

// choice kiest op een node alle afwijkingen van één stap, met de
// vingerafdrukken uit het driftrapport.
func choice(t *testing.T, rep driftReport, hostname, step string) map[string]any {
	t.Helper()
	n := rep.node(t, hostname)
	var fps []string
	for _, f := range n.Findings {
		if f.Step == step {
			fps = append(fps, f.Fingerprint)
		}
	}
	if len(fps) == 0 {
		t.Fatalf("geen afwijking %s op %s: %+v", step, hostname, n.Findings)
	}
	return map[string]any{"node_id": n.NodeID, "steps": []map[string]any{{"step": step, "fingerprints": fps}}}
}

func TestRemediate(t *testing.T) {
	e, c := adminClient(t)
	f, clusterID, ids := deployWeb(t, e, c)
	ctx := context.Background()
	web01, web02 := f.host("web-01"), f.host("web-02")
	const vip = "10.0.20.100"
	if o := f.group.Owner(vip); o != web01 {
		t.Fatalf("het VIP staat niet op web-01")
	}
	driftURL := "/api/v1/clusters/" + clusterID + "/drift"
	url := driftURL + "/remediate"
	conf := filepath.Join(web02.Root, "etc/keepalived/keepalived.conf")
	desired, err := e.deploy.Desired(ctx, uuid.MustParse(clusterID))
	if err != nil {
		t.Fatal(err)
	}
	steps, err := desired.Render(uuid.MustParse(ids["web-02"]))
	if err != nil {
		t.Fatal(err)
	}
	var want string
	for _, st := range steps {
		if st.File != nil && st.File.Path == "/etc/keepalived/keepalived.conf" {
			want = st.File.Content
		}
	}
	check := func() driftReport {
		t.Helper()
		var rep driftReport
		if s := c.do("POST", driftURL+"/check", nil, &rep); s != 200 {
			t.Fatalf("controleren: %d", s)
		}
		return rep
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// Iemand wijzigde keepalived.conf op web-02 met de hand.
	write(conf, "vrrp_instance VI_1 {}\n")
	rep := check()
	body := map[string]any{"nodes": []any{choice(t, rep, "web-02", "file:/etc/keepalived/keepalived.conf")}}

	// Een viewer mag niet herstellen.
	e.createUser("viewer", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("viewer", "een-lang-wachtwoord", "")
	var ae apiErr
	if s := v.do("POST", url, body, &ae); s != http.StatusForbidden {
		t.Fatalf("viewer: %d %+v", s, ae)
	}

	// De voorvertoning zegt in gewone woorden wat er gebeurt.
	var res remediationResult
	preview := map[string]any{"nodes": body["nodes"], "preview": true}
	if s := c.do("POST", url, preview, &res); s != 200 || res.Job != nil || !res.Plan.NeedsConfirmation || res.Plan.Slug != "web" ||
		len(res.Plan.Nodes) != 1 || res.Plan.Nodes[0].Hostname != "web-02" || len(res.Plan.Nodes[0].Steps) != 1 ||
		res.Plan.Nodes[0].Steps[0].Action != "bestand /etc/keepalived/keepalived.conf overschrijven, daarna keepalived herladen" {
		t.Fatalf("voorvertoning: %d %+v", s, res)
	}

	// Op prod: eerst tweestapsverificatie, dan de slug.
	if s := c.do("POST", url, body, &ae); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Fatalf("zonder tweestapsverificatie: %d %+v", s, ae)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE users SET totp_enabled_at = now() WHERE username = 'admin'"); err != nil {
		t.Fatal(err)
	}
	for _, confirm := range []any{nil, "Web", "web-prod"} {
		b := map[string]any{"nodes": body["nodes"]}
		if confirm != nil {
			b["confirm"] = confirm
		}
		if s := c.do("POST", url, b, &ae); s != http.StatusConflict || ae.Code != "needs_confirmation" {
			t.Fatalf("bevestiging %v: %d %+v", confirm, s, ae)
		}
	}
	// Een vingerafdruk die niet meer klopt.
	stale := map[string]any{"nodes": []any{map[string]any{"node_id": ids["web-02"], "steps": []map[string]any{
		{"step": "file:/etc/keepalived/keepalived.conf", "fingerprints": []string{strings.Repeat("0", 64)}},
	}}}, "confirm": "web"}
	if s := c.do("POST", url, stale, &ae); s != http.StatusConflict || ae.Code != "drift_changed" {
		t.Fatalf("oude vingerafdruk: %d %+v", s, ae)
	}

	// Herstellen: keepalived.conf terug, keepalived herladen, web-01 ongemoeid.
	calls01, calls02 := web01.Calls(), web02.Calls()
	body["confirm"] = "web"
	if s := c.do("POST", url, body, &res); s != http.StatusAccepted || res.Job == nil {
		t.Fatalf("herstellen: %d %+v", s, res)
	}
	jobID := res.Job.ID
	var detail struct {
		Status string `json:"status"`
		Steps  []struct {
			Name   string   `json:"name"`
			Status string   `json:"status"`
			Log    []string `json:"log"`
			Error  string   `json:"error"`
		} `json:"steps"`
	}
	waitDetail := func(id string) {
		t.Helper()
		waitJob(t, c, id)
		c.do("GET", "/api/v1/jobs/"+id, nil, &detail)
	}
	waitDetail(jobID)
	if detail.Status != "succeeded" || len(detail.Steps) != 1 || detail.Steps[0].Name != "Herstel op web-02" {
		t.Fatalf("herstel: %+v", detail)
	}
	if got := read(conf); got != want {
		t.Fatalf("keepalived.conf na het herstel: %q", got)
	}
	if got := web02.Calls()[len(calls02):]; !slices.Equal(got, []string{"systemctl reload-or-restart keepalived"}) {
		t.Fatalf("web-02 voerde uit: %v", got)
	}
	if got := web01.Calls(); !slices.Equal(got, calls01) {
		t.Fatalf("web-01 voerde uit: %v", got[len(calls01):])
	}
	if o := f.group.Owner(vip); o != web01 {
		t.Fatal("het VIP verhuisde")
	}
	eventually(t, "drift verdwenen met de taak", func() bool {
		var jid *string
		_ = e.pool.QueryRow(ctx, "SELECT payload->>'job_id' FROM events WHERE action = 'drift.resolved' ORDER BY id DESC LIMIT 1").Scan(&jid)
		return jid != nil && *jid == jobID
	})
	c.do("GET", driftURL, nil, &rep)
	if n := rep.node(t, "web-02"); n.Status != "in_sync" {
		t.Fatalf("web-02 na het herstel: %+v", n)
	}
	var requested struct {
		Name  string   `json:"name"`
		JobID string   `json:"job_id"`
		Nodes []string `json:"nodes"`
		Steps int      `json:"steps"`
	}
	var actor *string
	if err := e.pool.QueryRow(ctx, "SELECT payload, actor_id FROM events WHERE action = 'drift.remediation_requested'").Scan(&requested, &actor); err != nil {
		t.Fatal(err)
	}
	if requested.JobID != jobID || requested.Steps != 1 || !slices.Equal(requested.Nodes, []string{"web-02"}) || actor == nil {
		t.Fatalf("drift.remediation_requested: %+v", requested)
	}
	var commands int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action = 'agent.command' AND job_ref = $1", jobID).Scan(&commands)
	if commands == 0 {
		t.Fatal("de commando's van het herstel staan niet onder de taak in het logboek")
	}

	// Een tweede wijziging tussen tonen en uitvoeren: de nodestap mislukt
	// zonder iets toe te passen.
	write(conf, "vrrp_instance VI_1 { eerste }\n")
	rep = check()
	body["nodes"] = []any{choice(t, rep, "web-02", "file:/etc/keepalived/keepalived.conf")}
	e.stopRunner()
	if s := c.do("POST", url, body, &res); s != http.StatusAccepted {
		t.Fatalf("tweede herstel: %d", s)
	}
	jobID = res.Job.ID
	// Er loopt hoogstens één schrijvende taak per cluster.
	if s := c.do("POST", url, body, &ae); s != http.StatusConflict || ae.Code != "busy" {
		t.Fatalf("tweede aanvraag tegelijk: %d %+v", s, ae)
	}
	write(conf, "vrrp_instance VI_1 { tweede }\n")
	calls02 = web02.Calls()
	e.startRunner()
	waitDetail(jobID)
	if detail.Status != "failed" || len(detail.Steps) != 1 || !strings.Contains(detail.Steps[0].Error, "veranderde bestand /etc/keepalived/keepalived.conf") ||
		!strings.Contains(detail.Steps[0].Error, "niets toegepast") {
		t.Fatalf("na een tweede wijziging: %+v", detail)
	}
	if got := read(conf); got != "vrrp_instance VI_1 { tweede }\n" {
		t.Fatalf("keepalived.conf toch aangepast: %q", got)
	}
	if got := web02.Calls(); !slices.Equal(got, calls02) {
		t.Fatalf("web-02 voerde toch uit: %v", got[len(calls02):])
	}

	// Faalt de HTTP-controle na web-02, dan stopt de taak vóór de
	// VIP-eigenaar.
	index01 := filepath.Join(web01.Root, "var/www/html/index.html")
	if err := os.Chmod(index01, 0o600); err != nil {
		t.Fatal(err)
	}
	rep = check()
	body["nodes"] = []any{
		choice(t, rep, "web-01", "file:/var/www/html/index.html"),
		choice(t, rep, "web-02", "file:/etc/keepalived/keepalived.conf"),
	}
	c.do("POST", url, map[string]any{"nodes": body["nodes"], "preview": true}, &res)
	if len(res.Plan.Nodes) != 2 || res.Plan.Nodes[0].Hostname != "web-02" || res.Plan.Nodes[1].Hostname != "web-01" ||
		!slices.Equal(res.Plan.Nodes[1].VIPs, []string{vip}) {
		t.Fatalf("volgorde: %+v", res.Plan)
	}
	e.deploy.HTTPGet = func(context.Context, string) (int, error) { return http.StatusServiceUnavailable, nil }
	calls01 = web01.Calls()
	if s := c.do("POST", url, body, &res); s != http.StatusAccepted {
		t.Fatalf("herstel met falende controle: %d", s)
	}
	waitDetail(res.Job.ID)
	if detail.Status != "failed" || len(detail.Steps) != 1 || detail.Steps[0].Name != "Herstel op web-02" ||
		!strings.Contains(detail.Steps[0].Error, "controle na het herstel van web-02") {
		t.Fatalf("met falende HTTP-controle: %+v", detail)
	}
	if got := read(conf); got != want {
		t.Fatalf("web-02 niet hersteld: %q", got)
	}
	if fi, _ := os.Stat(index01); fi.Mode().Perm() != 0o600 {
		t.Fatalf("web-01 toch aangepast: %v", fi.Mode())
	}
	if got := web01.Calls(); !slices.Equal(got, calls01) {
		t.Fatalf("web-01 voerde uit: %v", got[len(calls01):])
	}

	// Met een werkende website lukt de eigenaar ook, als laatste.
	e.deploy.HTTPGet = func(context.Context, string) (int, error) { return http.StatusOK, nil }
	rep = check()
	body["nodes"] = []any{choice(t, rep, "web-01", "file:/var/www/html/index.html")}
	if s := c.do("POST", url, body, &res); s != http.StatusAccepted {
		t.Fatalf("herstel op de eigenaar: %d", s)
	}
	waitDetail(res.Job.ID)
	if detail.Status != "succeeded" || detail.Steps[0].Name != "Herstel op web-01 (VIP-eigenaar)" ||
		!slices.ContainsFunc(detail.Steps[0].Log, func(l string) bool { return strings.Contains(l, "web-02 kan het overnemen") }) {
		t.Fatalf("herstel op de eigenaar: %+v", detail)
	}
	if fi, _ := os.Stat(index01); fi.Mode().Perm() != 0o644 {
		t.Fatalf("index.html op web-01: %v", fi.Mode())
	}

	// Een genegeerde stap wordt nooit hersteld.
	if err := os.Chmod(index01, 0o600); err != nil {
		t.Fatal(err)
	}
	rep = check()
	body["nodes"] = []any{choice(t, rep, "web-01", "file:/var/www/html/index.html")}
	if s := c.do("POST", driftURL+"/ignores", map[string]any{"key": "file:/var/www/html/index.html", "reason": "onderhoudspagina"}, nil); s != 201 {
		t.Fatalf("negeren: %d", s)
	}
	if s := c.do("POST", url, body, &ae); s != http.StatusConflict || ae.Code != "ignored" {
		t.Fatalf("genegeerde stap: %d %+v", s, ae)
	}

	// Alleen bij een cluster uit een template.
	var manual struct{ ID string }
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "db", "name": "DB", "type": "keepalived", "environment": "lab"}, &manual); s != 201 {
		t.Fatalf("cluster: %d", s)
	}
	if s := c.do("POST", "/api/v1/clusters/"+manual.ID+"/drift/remediate", map[string]any{"nodes": []any{}}, &ae); s != http.StatusConflict || ae.Code != "not_template" {
		t.Fatalf("handmatig cluster: %d %+v", s, ae)
	}
	var raw json.RawMessage
	if s := c.do("POST", "/api/v1/clusters/"+uuid.NewString()+"/drift/remediate", map[string]any{"nodes": []any{}}, &raw); s != http.StatusNotFound {
		t.Fatalf("onbekend cluster: %d", s)
	}
}
