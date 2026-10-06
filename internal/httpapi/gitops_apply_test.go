package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/gitops/ghfake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type gitDecision struct {
	Change gitChange `json:"change"`
	Job    *job      `json:"job"`
}

// gitLinked koppelt de nep-GitHub, commit de export van het cluster,
// koppelt het en laat de lus elke 50 ms pollen.
func gitLinked(t *testing.T, e *testEnv, c *client, clusterID string) (*ghfake.Server, string, func(string) string) {
	t.Helper()
	_, _, export := c.get("/api/v1/clusters/" + clusterID + "/git/export")
	gh := ghfake.New("jonas", "cf-config", "github_pat_test")
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	gh.Commit("Webcluster uit ClusterForge", "Jonas", map[string]string{"clusters/web/cluster.yaml": export})
	in := map[string]any{"api_url": srv.URL, "repository": "jonas/cf-config", "token": "github_pat_test"}
	if s := c.do("PUT", "/api/v1/gitops/repo", in, nil); s != 200 {
		t.Fatalf("repository koppelen: %d", s)
	}
	e.git.Interval = 50 * time.Millisecond
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.git.Run(ctx) }()
	t.Cleanup(func() { stop(); <-done })
	eventually(t, "export gelezen", func() bool { return c.gitFiles()["clusters/web/cluster.yaml"].State == "unlinked" })
	if s := c.do("POST", "/api/v1/clusters/"+clusterID+"/git/link", nil, nil); s != 200 {
		t.Fatalf("cluster koppelen: %d", s)
	}
	// commit zet een nieuw bestand in Git en wacht op het plan ervan.
	commit := func(content string) string {
		t.Helper()
		sha := gh.Commit("Wijziging", "Jonas", map[string]string{"clusters/web/cluster.yaml": content})
		eventually(t, "plan voor "+sha[:7], func() bool {
			cs := c.gitChanges("?status=pending")
			return len(cs) == 1 && cs[0].Commit.SHA == sha
		})
		return c.gitChanges("?status=pending")[0].ID
	}
	return gh, export, commit
}

// TestGitOpsApply is de acceptatietest van mijlpaal 15: goedkeuren van een
// naamwijziging maakt spec-revisie 2 met bron git en de commit, en
// cluster.apply past index.html eerst toe op de node zonder VIP en daarna op
// de eigenaar. Een commit die nginx laat falen, stopt vóór de VIP-eigenaar,
// en git revert brengt de naam langs hetzelfde pad terug.
func TestGitOpsApply(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	fl, clusterID, _ := deployWeb(t, e, c)
	web01, web02 := fl.host("web-01"), fl.host("web-02")
	index01 := filepath.Join(web01.Root, "var/www/html/index.html")
	index02 := filepath.Join(web02.Root, "var/www/html/index.html")
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	e.createUser("piet", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("piet", "een-lang-wachtwoord", "")
	_, export, commit := gitLinked(t, e, c, clusterID)
	renamed := func(name string) string { return strings.Replace(export, "name: Web\n", "name: "+name+"\n", 1) }
	clusterURL := "/api/v1/clusters/" + clusterID
	type cluster struct {
		Name            string `json:"name"`
		SpecRevision    int    `json:"spec_revision"`
		AppliedRevision int    `json:"applied_revision"`
	}
	getCluster := func() cluster {
		t.Helper()
		var cl cluster
		c.do("GET", clusterURL, nil, &cl)
		return cl
	}
	detail := func(id string) gitChange {
		t.Helper()
		var ch gitChange
		if s := c.do("GET", "/api/v1/gitops/changes/"+id, nil, &ch); s != 200 {
			t.Fatalf("wijziging %s: %d", id, s)
		}
		return ch
	}
	if cl := getCluster(); cl.SpecRevision != 1 || cl.AppliedRevision != 1 {
		t.Fatalf("na de uitrol: %+v", cl)
	}

	// Afwijzen met een reden; het bestand blijft afgewezen, ook na een
	// nieuwe synchronisatie.
	id := commit(renamed("Webwinkel"))
	if s := v.do("POST", "/api/v1/gitops/changes/"+id+"/reject", map[string]any{"reason": "nee"}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer wijst af: %d", s)
	}
	var ae apiErr
	if s := c.do("POST", "/api/v1/gitops/changes/"+id+"/reject", map[string]any{"reason": " "}, &ae); s != 400 {
		t.Fatalf("afwijzen zonder reden: %d %+v", s, ae)
	}
	var rejected gitChange
	if s := c.do("POST", "/api/v1/gitops/changes/"+id+"/reject", map[string]any{"reason": "de winkel komt later"}, &rejected); s != 200 ||
		rejected.Status != "rejected" || rejected.Reason != "de winkel komt later" || rejected.DecidedBy == nil || *rejected.DecidedBy != "admin" {
		t.Fatalf("afwijzen: %d %+v", s, rejected)
	}
	if s := c.do("POST", "/api/v1/gitops/sync", nil, nil); s != http.StatusAccepted {
		t.Fatalf("synchroniseren: %d", s)
	}
	eventually(t, "afgewezen", func() bool {
		f := c.gitFiles()["clusters/web/cluster.yaml"]
		return f.State == "rejected" && f.ChangeID != nil && *f.ChangeID == id
	})
	time.Sleep(200 * time.Millisecond)
	if cs := c.gitChanges("?status=pending"); len(cs) != 0 {
		t.Fatalf("nieuw plan na afwijzen: %+v", cs)
	}
	if s := c.do("POST", "/api/v1/gitops/changes/"+id+"/approve", map[string]any{"confirm": "web"}, &ae); s != 409 || ae.Code != "not_pending" {
		t.Fatalf("afgewezen goedkeuren: %d %+v", s, ae)
	}

	// Een nieuwe naam: op prod eerst tweestapsverificatie, dan de slug.
	id = commit(renamed("Website"))
	ch := detail(id)
	if !ch.NeedsConfirmation || ch.FullApply || ch.Blocked != "" {
		t.Fatalf("detail: %+v", ch)
	}
	if s := v.do("POST", "/api/v1/gitops/changes/"+id+"/approve", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer keurt goed: %d", s)
	}
	if s := c.do("POST", "/api/v1/gitops/changes/"+id+"/approve", map[string]any{"confirm": "web"}, &ae); s != http.StatusForbidden || ae.Code != "totp_required" {
		t.Fatalf("zonder tweestapsverificatie: %d %+v", s, ae)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE users SET totp_enabled_at = now() WHERE username = 'admin'"); err != nil {
		t.Fatal(err)
	}
	for _, confirm := range []any{nil, "Website"} {
		b := map[string]any{}
		if confirm != nil {
			b["confirm"] = confirm
		}
		if s := c.do("POST", "/api/v1/gitops/changes/"+id+"/approve", b, &ae); s != 409 || ae.Code != "needs_confirmation" {
			t.Fatalf("bevestiging %v: %d %+v", confirm, s, ae)
		}
	}

	// Goedkeuren terwijl de runner stilstaat: de spec en de naam veranderen
	// meteen, de nodes pas in de taak. Een tweede wijziging intussen vindt
	// het clusterslot bezet.
	e.stopRunner()
	var dec gitDecision
	if s := c.do("POST", "/api/v1/gitops/changes/"+id+"/approve", map[string]any{"confirm": "web"}, &dec); s != http.StatusAccepted ||
		dec.Job == nil || dec.Change.Status != "applying" || dec.Change.Revision == nil || *dec.Change.Revision != 2 {
		t.Fatalf("goedkeuren: %d %+v", s, dec)
	}
	sha := dec.Change.Commit.SHA
	if cl := getCluster(); cl.Name != "Website" || cl.SpecRevision != 2 || cl.AppliedRevision != 1 {
		t.Fatalf("cluster na goedkeuren: %+v", cl)
	}
	var hist struct {
		Items []struct {
			Revision  int     `json:"revision"`
			Source    string  `json:"source"`
			CommitSha *string `json:"commit_sha"`
			CommitURL string  `json:"commit_url"`
			CreatedBy *struct {
				Name string `json:"name"`
			} `json:"created_by"`
		} `json:"items"`
	}
	if s := v.do("GET", clusterURL+"/spec-revisions", nil, &hist); s != 200 || len(hist.Items) != 2 || hist.Items[0].Revision != 2 ||
		hist.Items[0].Source != "git" || hist.Items[0].CommitSha == nil || *hist.Items[0].CommitSha != sha ||
		!strings.HasSuffix(hist.Items[0].CommitURL, "/jonas/cf-config/commit/"+sha) || hist.Items[0].CreatedBy == nil || hist.Items[0].CreatedBy.Name != "admin" ||
		hist.Items[1].CommitSha != nil {
		t.Fatalf("revisies: %d %+v", s, hist)
	}
	eventually(t, "wordt toegepast", func() bool { return c.gitFiles()["clusters/web/cluster.yaml"].State == "applying" })
	tags := strings.Replace(renamed("Website"), "tags: []", "tags: [web]", 1)
	if tags == renamed("Website") {
		t.Fatalf("geen tags in de export:\n%s", export)
	}
	tagsID := commit(tags)
	if ch := detail(tagsID); ch.Plan.BaseRevision != 2 || len(ch.Plan.Nodes) != 0 {
		t.Fatalf("plan met tags: %+v", ch.Plan)
	}
	if s := c.do("POST", "/api/v1/gitops/changes/"+tagsID+"/approve", map[string]any{"confirm": "web"}, &ae); s != 409 || ae.Code != "busy" {
		t.Fatalf("tweede goedkeuring tijdens de eerste: %d %+v", s, ae)
	}
	if index := read(index02); !strings.Contains(index, "<h1>Web</h1>") {
		t.Fatalf("web-02 al aangepast: %s", index)
	}

	// De taak: eerst web-02, dan de VIP-eigenaar web-01.
	calls01 := web01.Calls()
	e.startRunner()
	j := waitJob(t, c, dec.Job.ID)
	if j.Status != "succeeded" || len(j.Steps) != 2 || j.Steps[0].Name != "Toepassen op web-02" || j.Steps[1].Name != "Toepassen op web-01 (VIP-eigenaar)" {
		t.Fatalf("taak: %+v", j)
	}
	for _, p := range []string{index01, index02} {
		if index := read(p); !strings.Contains(index, "<title>Website</title>") || !strings.Contains(index, "<h1>Website</h1>") {
			t.Fatalf("%s: %s", p, index)
		}
	}
	if got := web01.Calls()[len(calls01):]; len(got) != 0 {
		t.Fatalf("web-01 voerde uit: %v", got)
	}
	eventually(t, "toegepast", func() bool { return detail(id).Status == "applied" && getCluster().AppliedRevision == 2 })
	var commands int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action = 'agent.command' AND job_ref = $1", dec.Job.ID).Scan(&commands)
	if commands == 0 {
		t.Fatal("de commando's van de toepassing staan niet onder de taak")
	}

	// Alleen andere tags: geen stap verandert, dus geen taak.
	if s := c.do("POST", "/api/v1/gitops/changes/"+tagsID+"/approve", map[string]any{"confirm": "web"}, &dec); s != 200 || dec.Job != nil ||
		dec.Change.Status != "applied" || dec.Change.Revision == nil || *dec.Change.Revision != 3 {
		t.Fatalf("tags goedkeuren: %d %+v", s, dec)
	}
	if cl := getCluster(); cl.SpecRevision != 3 || cl.AppliedRevision != 3 {
		t.Fatalf("na tags: %+v", cl)
	}
	eventually(t, "in sync", func() bool { return c.gitFiles()["clusters/web/cluster.yaml"].State == "in_sync" })

	// Omhoog schalen kan nog niet uit Git.
	grow := strings.Replace(tags, "node_count: 2", "node_count: 3", 1)
	growID := commit(grow)
	if ch := detail(growID); ch.Blocked == "" {
		t.Fatalf("omhoog schalen niet geblokkeerd: %+v", ch)
	}
	if s := c.do("POST", "/api/v1/gitops/changes/"+growID+"/approve", map[string]any{"confirm": "web"}, &ae); s != 409 || ae.Code != "not_supported" {
		t.Fatalf("omhoog schalen: %d %+v", s, ae)
	}

	// Een commit die nginx laat falen: de taak stopt na web-02, vóór de
	// VIP-eigenaar, en de wijziging is mislukt.
	broken := strings.Replace(tags, "name: Website\n", "name: Kapot\n", 1)
	brokenID := commit(broken)
	e.deploy.HTTPGet = func(context.Context, string) (int, error) { return http.StatusServiceUnavailable, nil }
	calls01 = web01.Calls()
	if s := c.do("POST", "/api/v1/gitops/changes/"+brokenID+"/approve", map[string]any{"confirm": "web"}, &dec); s != http.StatusAccepted || dec.Job == nil {
		t.Fatalf("kapotte wijziging goedkeuren: %d %+v", s, dec)
	}
	j = waitJob(t, c, dec.Job.ID)
	if j.Status != "failed" || len(j.Steps) != 1 || j.Steps[0].Name != "Toepassen op web-02" || !strings.Contains(j.Error, "controle na het toepassen op web-02") {
		t.Fatalf("kapotte taak: %+v", j)
	}
	if !strings.Contains(read(index02), "<h1>Kapot</h1>") || !strings.Contains(read(index01), "<h1>Website</h1>") {
		t.Fatalf("index.html: web-02 %q, web-01 %q", read(index02), read(index01))
	}
	if got := web01.Calls()[len(calls01):]; len(got) != 0 {
		t.Fatalf("web-01 voerde uit: %v", got)
	}
	eventually(t, "mislukt", func() bool {
		ch := detail(brokenID)
		return ch.Status == "failed" && strings.Contains(ch.Reason, "controle na het toepassen op web-02")
	})
	if cl := getCluster(); cl.Name != "Kapot" || cl.SpecRevision != 4 || cl.AppliedRevision != 3 {
		t.Fatalf("na de mislukte wijziging: %+v", cl)
	}
	eventually(t, "niet toegepast", func() bool {
		f := c.gitFiles()["clusters/web/cluster.yaml"]
		return f.State == "not_applied" && f.ChangeID != nil && *f.ChangeID == brokenID
	})

	// Opnieuw toepassen vraagt op prod de slug en mislukt zolang nginx faalt.
	if s := c.do("POST", clusterURL+"/git/reapply", nil, &ae); s != 409 || ae.Code != "needs_confirmation" {
		t.Fatalf("opnieuw toepassen zonder slug: %d %+v", s, ae)
	}
	var rj job
	if s := c.do("POST", clusterURL+"/git/reapply", map[string]any{"confirm": "web"}, &rj); s != http.StatusAccepted || !strings.Contains(rj.Title, "Revisie 4 opnieuw") {
		t.Fatalf("opnieuw toepassen: %d %+v", s, rj)
	}
	if j := waitJob(t, c, rj.ID); j.Status != "failed" || len(j.Steps) != 1 || j.Steps[0].Name != "Toepassen op web-02" {
		t.Fatalf("opnieuw toepassen met falende nginx: %+v", j)
	}
	if ch := detail(brokenID); ch.Status != "failed" {
		t.Fatalf("een mislukte wijziging blijft mislukt: %+v", ch)
	}

	// git revert: dezelfde weg terug. Omdat revisie 4 niet overal staat,
	// past de taak alle stappen toe.
	e.deploy.HTTPGet = func(context.Context, string) (int, error) { return http.StatusOK, nil }
	revertID := commit(tags)
	if ch := detail(revertID); !ch.FullApply || ch.Plan.BaseRevision != 4 || len(ch.Plan.Nodes) != 2 {
		t.Fatalf("revert: %+v", ch)
	}
	if s := c.do("POST", "/api/v1/gitops/changes/"+revertID+"/approve", map[string]any{"confirm": "web"}, &dec); s != http.StatusAccepted || dec.Job == nil {
		t.Fatalf("revert goedkeuren: %d %+v", s, dec)
	}
	j = waitJob(t, c, dec.Job.ID)
	if j.Status != "succeeded" || len(j.Steps) != 2 || j.Steps[0].Name != "Toepassen op web-02" || j.Steps[1].Name != "Toepassen op web-01 (VIP-eigenaar)" {
		t.Fatalf("revert-taak: %+v", j)
	}
	for _, p := range []string{index01, index02} {
		if !strings.Contains(read(p), "<h1>Website</h1>") {
			t.Fatalf("na de revert %s: %s", p, read(p))
		}
	}
	eventually(t, "revert toegepast", func() bool {
		cl := getCluster()
		return detail(revertID).Status == "applied" && cl.Name == "Website" && cl.SpecRevision == 5 && cl.AppliedRevision == 5
	})
	if s := c.do("POST", clusterURL+"/git/reapply", map[string]any{"confirm": "web"}, &ae); s != 409 || ae.Code != "up_to_date" {
		t.Fatalf("opnieuw toepassen zonder achterstand: %d %+v", s, ae)
	}

	// Het logboek.
	page := c.audit("category=gitops")
	count := map[string]int{}
	for _, it := range page.Items {
		count[it.Action]++
	}
	if count["gitops.change_approved"] != 4 || count["gitops.change_applied"] != 3 || count["gitops.change_failed"] != 1 ||
		count["gitops.change_rejected"] != 1 || count["gitops.reapply_requested"] != 1 {
		t.Fatalf("events: %v", count)
	}
	var spec struct {
		Source   string `json:"source"`
		Commit   string `json:"commit"`
		ChangeID string `json:"change_id"`
	}
	if err := e.pool.QueryRow(ctx, "SELECT payload FROM events WHERE action = 'cluster.spec_changed' AND payload->>'revision' = '2'").Scan(&spec); err != nil ||
		spec.Source != "git" || spec.Commit != sha || spec.ChangeID != id {
		t.Fatalf("cluster.spec_changed: %+v %v", spec, err)
	}

	// Het VRRP-wachtwoord staat in geen taak, stap of event.
	for _, table := range []string{"jobs", "job_steps", "git_changes", "events"} {
		var n int
		if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" t WHERE row_to_json(t)::text LIKE '%geheim12%'").Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rijen met het wachtwoord (%v)", table, n, err)
		}
	}
}
