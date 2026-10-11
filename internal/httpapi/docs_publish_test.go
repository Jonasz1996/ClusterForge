package httpapi

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/docs"
	"github.com/Jonasz1996/clusterforge/internal/gitops/ghfake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type testDocsCommit struct {
	Sha   string `json:"sha"`
	Url   string `json:"url"`
	Files int    `json:"files"`
}

type testDocsStatus struct {
	Enabled bool `json:"enabled"`
	Pending bool `json:"pending"`
	Target  *struct {
		Repository string          `json:"repository"`
		Branch     string          `json:"branch"`
		Path       string          `json:"path"`
		FolderURL  string          `json:"folder_url"`
		Enabled    bool            `json:"enabled"`
		LastCommit *testDocsCommit `json:"last_commit"`
		LastError  string          `json:"last_error"`
	} `json:"target"`
}

type testDocsPublish struct {
	Commit *testDocsCommit `json:"commit"`
	Files  []struct {
		Path   string `json:"path"`
		Change string `json:"change"`
	} `json:"files"`
}

func TestDocsPublish(t *testing.T) {
	e, c := adminClient(t)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login: %d", s)
	}
	ctx := context.Background()
	_, clusterID, _, _, _ := deployWebPVE(t, e, c)
	clock := time.Date(2030, 1, 1, 2, 0, 0, 0, time.Local)
	e.docs.Now = func() time.Time { return clock }
	count := func(action string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action = $1", action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Eén nep-GitHub: GitOps leest main met een token dat alleen mag lezen,
	// de documentatie schrijft met een eigen token.
	gh := ghfake.New("jonas", "cf-config", "docs-token")
	gh.AddToken("gitops-token", false)
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	gh.Commit("Begin", "Jonas", map[string]string{"README.md": "# Clusters\n"})
	if s := c.do("PUT", "/api/v1/gitops/repo", map[string]any{"api_url": srv.URL, "repository": "jonas/cf-config", "token": "gitops-token"}, nil); s != http.StatusOK {
		t.Fatalf("GitOps koppelen: %d", s)
	}
	if err := e.git.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	mainHead := gh.Head("main")

	var st testDocsStatus
	if s := viewer.do("GET", "/api/v1/docs/target", nil, &st); s != http.StatusOK || !st.Enabled || st.Target != nil {
		t.Fatalf("zonder koppeling: %d %+v", s, st)
	}
	var ae apiErr
	if s := c.do("POST", "/api/v1/docs/publish", nil, &ae); s != http.StatusNotFound || !strings.Contains(ae.Message, "koppel eerst") {
		t.Fatalf("publiceren zonder koppeling: %d %+v", s, ae)
	}

	// Dezelfde repository en branch als GitOps, en een token dat alleen mag
	// lezen, worden geweigerd.
	in := map[string]any{"api_url": srv.URL, "repository": "https://github.com/Jonas/CF-config", "token": "docs-token"}
	for _, path := range []string{"/api/v1/docs/target/test", "/api/v1/docs/target"} {
		method := map[bool]string{true: "POST", false: "PUT"}[strings.HasSuffix(path, "/test")]
		if s := c.do(method, path, in, &ae); s != http.StatusBadRequest || !strings.Contains(ae.Message, "GitOps leest deze repository en branch") {
			t.Fatalf("%s %s op de GitOps-branch: %d %+v", method, path, s, ae)
		}
	}
	gh.AddToken("alleen-lezen", false)
	in["branch"], in["token"] = "documentatie", "alleen-lezen"
	if s := c.do("PUT", "/api/v1/docs/target", in, &ae); s != http.StatusBadRequest || !strings.Contains(ae.Message, "mag niet schrijven") {
		t.Fatalf("token zonder schrijfrecht: %d %+v", s, ae)
	}
	in["token"] = "docs-token"
	for _, r := range []struct{ method, path string }{
		{"PUT", "/api/v1/docs/target"}, {"POST", "/api/v1/docs/target/test"}, {"POST", "/api/v1/docs/publish"}, {"DELETE", "/api/v1/docs/target"},
	} {
		if s := viewer.do(r.method, r.path, in, nil); s != http.StatusForbidden {
			t.Fatalf("viewer %s %s: %d", r.method, r.path, s)
		}
	}
	var probe struct {
		BranchExists bool `json:"branch_exists"`
		Files        int  `json:"files"`
	}
	if s := c.do("POST", "/api/v1/docs/target/test", in, &probe); s != http.StatusOK || probe.BranchExists || probe.Files != 0 {
		t.Fatalf("testen: %d %+v", s, probe)
	}

	if s := c.do("PUT", "/api/v1/docs/target", in, &st); s != http.StatusOK || st.Target == nil || st.Target.Repository != "Jonas/CF-config" ||
		st.Target.Branch != "documentatie" || st.Target.Path != "docs" || !st.Target.Enabled || st.Target.LastCommit != nil || !st.Pending {
		t.Fatalf("koppelen: %d %+v", s, st)
	}
	_, _, raw := c.get("/api/v1/docs/target")
	if strings.Contains(raw, "docs-token") || !strings.Contains(raw, `"folder_url":"`+srv.URL+`/Jonas/CF-config/tree/documentatie/docs"`) {
		t.Fatalf("status: %s", raw)
	}
	// GitOps mag op zijn beurt niet naar de branch van de documentatie.
	gitIn := map[string]any{"api_url": srv.URL, "repository": "jonas/cf-config", "branch": "documentatie"}
	if s := c.do("PUT", "/api/v1/gitops/repo", gitIn, &ae); s != http.StatusBadRequest || !strings.Contains(ae.Message, "documentatie-export") {
		t.Fatalf("GitOps op de documentatiebranch: %d %+v", s, ae)
	}

	// De eerste ronde schrijft alles en publiceert in dezelfde Tick.
	if n, err := e.docs.GenerateAll(ctx, docs.CauseStart); err != nil || n != 6 {
		t.Fatalf("schrijven: %d %v", n, err)
	}
	if err := e.docs.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	files := gh.FilesOn("documentatie")
	wantPaths := []string{"docs/README.md", "docs/clusters/web/architectuur.md", "docs/clusters/web/configuratie.md",
		"docs/clusters/web/herstel.md", "docs/clusters/web/netwerk.md", "docs/clusters/web/runbooks.md"}
	if got := slices.Sorted(maps.Keys(files)); !slices.Equal(got, wantPaths) {
		t.Fatalf("bestanden: %v", got)
	}
	for p, content := range files {
		if !strings.HasPrefix(content, "*Geschreven door ClusterForge op ") {
			t.Errorf("%s zonder kopregel", p)
		}
		for _, secret := range []string{"geheim12", "geheim-secret", "docs-token", "gitops-token"} {
			if strings.Contains(content, secret) {
				t.Errorf("%s bevat %q", p, secret)
			}
		}
	}
	first, _ := gh.CommitOf(gh.Head("documentatie"))
	if !strings.HasPrefix(first.Message, "Documentatie bijgewerkt: overzicht, web\n") || first.Author != "ClusterForge" || len(first.Parents) != 0 {
		t.Fatalf("eerste commit: %+v", first)
	}
	if c.do("GET", "/api/v1/docs/target", nil, &st); st.Target.LastCommit == nil || st.Target.LastCommit.Sha != first.SHA ||
		st.Target.LastCommit.Files != 6 || st.Target.LastError != "" || st.Pending {
		t.Fatalf("na de eerste commit: %+v", st.Target)
	}

	// Zonder wijziging geen commit, met de hand of in de lus.
	var pub testDocsPublish
	if s := c.do("POST", "/api/v1/docs/publish", nil, &pub); s != http.StatusOK || pub.Commit != nil || len(pub.Files) != 0 {
		t.Fatalf("tweede keer: %d %+v", s, pub)
	}
	if err := e.docs.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if gh.Head("documentatie") != first.SHA || count("docs.published") != 1 {
		t.Fatalf("commit zonder wijziging: %s", gh.Head("documentatie"))
	}

	// Een wijziging: tien minuten later één commit met alleen de bestanden
	// die veranderden.
	if s := c.do("PATCH", "/api/v1/clusters/"+clusterID, map[string]any{"description": "Nieuwe beschrijving van de website"}, nil); s != http.StatusOK {
		t.Fatalf("beschrijving: %d", s)
	}
	if err := e.docs.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if gh.Head("documentatie") != first.SHA {
		t.Fatal("gepubliceerd voor de wachttijd")
	}
	clock = clock.Add(11 * time.Minute)
	if err := e.docs.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	second, _ := gh.CommitOf(gh.Head("documentatie"))
	if len(second.Parents) != 1 || second.Parents[0] != first.SHA {
		t.Fatalf("tweede commit: %+v", second)
	}
	var changed []string
	for _, p := range wantPaths {
		if second.Files[p] != first.Files[p] {
			changed = append(changed, p)
		}
	}
	var idx testDocIndex
	c.do("GET", "/api/v1/docs", nil, &idx)
	var fresh []string
	for _, m := range append([]testDocMeta{idx.Overview}, idx.Clusters[0].Docs...) {
		if m.GeneratedAt != nil && m.GeneratedAt.Equal(clock) {
			fresh = append(fresh, "docs/"+m.Path)
		}
	}
	slices.Sort(fresh)
	if len(changed) == 0 || len(changed) == len(wantPaths) || !slices.Equal(changed, fresh) {
		t.Fatalf("veranderd in Git %v, nieuwe versies %v", changed, fresh)
	}

	// Een eigen bestand in de map blijft staan; een document van een
	// cluster dat er niet meer is, gaat weg.
	gh.CommitTo("documentatie", "Eigen notitie", "Jonas", map[string]string{"docs/eigen.md": "mijn tekst\n", "docs/clusters/oud/netwerk.md": "# Oud\n"})
	if s := c.do("POST", "/api/v1/docs/publish", nil, &pub); s != http.StatusOK || pub.Commit == nil || len(pub.Files) != 1 ||
		pub.Files[0].Path != "docs/clusters/oud/netwerk.md" || pub.Files[0].Change != "deleted" {
		t.Fatalf("opruimen: %d %+v", s, pub)
	}
	if gh.FilesOn("documentatie")["docs/eigen.md"] != "mijn tekst\n" {
		t.Fatal("eigen bestand verdwenen")
	}

	// GitHub valt weg: één event, de fout bij de koppeling, en na de
	// wachttijd lukt het weer.
	gh.SetDown(true)
	if s := c.do("POST", "/api/v1/docs/publish", nil, &ae); s != http.StatusConflict || ae.Code != "publish_failed" || !strings.Contains(ae.Message, "503") {
		t.Fatalf("GitHub weg: %d %+v", s, ae)
	}
	if err := e.docs.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if c.do("GET", "/api/v1/docs/target", nil, &st); st.Target.LastError == "" || !st.Pending || count("docs.publish_failed") != 1 {
		t.Fatalf("na de fout: %+v", st)
	}
	gh.SetDown(false)
	if err := e.docs.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if c.do("GET", "/api/v1/docs/target", nil, &st); st.Target.LastError == "" {
		t.Fatal("opnieuw geprobeerd voor de wachttijd")
	}
	clock = clock.Add(6 * time.Minute)
	if err := e.docs.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if c.do("GET", "/api/v1/docs/target", nil, &st); st.Target.LastError != "" || st.Pending || count("docs.publish_recovered") != 1 {
		t.Fatalf("na herstel: %+v", st)
	}

	// Uitgezet: een wijziging komt niet vanzelf in Git, wel met de hand.
	in["enabled"], in["token"] = false, ""
	if s := c.do("PUT", "/api/v1/docs/target", in, &st); s != http.StatusOK || st.Target.Enabled || count("docs.target_updated") != 1 {
		t.Fatalf("uitzetten: %d %+v", s, st)
	}
	head := gh.Head("documentatie")
	if s := c.do("PATCH", "/api/v1/clusters/"+clusterID, map[string]any{"description": "Nog een beschrijving"}, nil); s != http.StatusOK {
		t.Fatalf("beschrijving: %d", s)
	}
	_ = e.docs.Tick(ctx)
	clock = clock.Add(11 * time.Minute)
	_ = e.docs.Tick(ctx)
	if gh.Head("documentatie") != head {
		t.Fatal("gepubliceerd terwijl het uit staat")
	}
	if s := c.do("POST", "/api/v1/docs/publish", nil, &pub); s != http.StatusOK || pub.Commit == nil {
		t.Fatalf("met de hand: %d %+v", s, pub)
	}

	// GitOps deed alleen GET's; de export schreef alleen in zijn eigen
	// branch en map.
	if err := e.git.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	allowed := []string{"/git/blobs", "/git/trees", "/git/commits", "/git/refs", "/git/refs/heads/documentatie"}
	reads := 0
	for _, call := range gh.Calls() {
		switch {
		case call.Token == "gitops-token":
			reads++
			if call.Method != http.MethodGet {
				t.Errorf("GitOps schreef: %+v", call)
			}
		case call.Token == "docs-token" && call.Method != http.MethodGet &&
			!slices.Contains(allowed, strings.TrimPrefix(strings.ToLower(call.Path), "/repos/jonas/cf-config")):
			t.Errorf("de export schreef buiten zijn branch: %+v", call)
		}
	}
	if reads == 0 || gh.Head("main") != mainHead {
		t.Fatalf("GitOps las %d keer; main %s (was %s)", reads, gh.Head("main"), mainHead)
	}
	for p := range gh.FilesOn("documentatie") {
		if !strings.HasPrefix(p, "docs/") {
			t.Errorf("buiten de map: %s", p)
		}
	}

	// Het logboek.
	var audit struct {
		Items []struct {
			Action  string `json:"action"`
			Summary string `json:"summary"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/audit?category=docs", nil, &audit)
	var sums []string
	for _, it := range audit.Items {
		sums = append(sums, it.Summary)
	}
	text := strings.Join(sums, "\n")
	for _, want := range []string{
		"Documentatie naar GitHub-repository Jonas/CF-config (branch documentatie, map docs)",
		"Documentatie naar Jonas/CF-config geschreven: commit " + first.SHA[:7] + " met 6 bestanden (overzicht, web)",
		"Documentatie naar Jonas/CF-config schrijven mislukt: GitHub antwoordt met 503",
		"Documentatie naar Jonas/CF-config schrijven lukt weer",
		"Koppeling van de documentatie met GitHub gewijzigd: automatisch publiceren",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("logboek mist %q:\n%s", want, text)
		}
	}

	// Ontkoppelen laat de bestanden in Git staan.
	if s := c.do("DELETE", "/api/v1/docs/target", nil, nil); s != http.StatusNoContent {
		t.Fatalf("ontkoppelen: %d", s)
	}
	if c.do("GET", "/api/v1/docs/target", nil, &st); st.Target != nil || len(gh.FilesOn("documentatie")) != 7 || count("docs.target_disconnected") != 1 {
		t.Fatalf("na ontkoppelen: %+v", st)
	}
}
