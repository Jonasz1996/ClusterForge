package httpapi

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/gitops/ghfake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// recorder bewaart elk antwoord van de API, om er een geheim in te zoeken.
type recorder struct {
	next http.RoundTripper
	mu   sync.Mutex
	all  bytes.Buffer
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all.WriteString(req.Method + " " + req.URL.Path + "\n")
	r.all.Write(body)
	r.all.WriteString("\n")
	return resp, nil
}

func (r *recorder) contains(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Contains(r.all.String(), s)
}

func (c *client) record() *recorder {
	rec := &recorder{next: http.DefaultTransport}
	c.http.Transport = rec
	return rec
}

// get leest een antwoord dat geen JSON is.
func (c *client) get(path string) (int, http.Header, string) {
	c.t.Helper()
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

type gitCommit struct {
	SHA      string `json:"sha"`
	Message  string `json:"message"`
	Author   string `json:"author"`
	Verified bool   `json:"verified"`
}

type gitStatus struct {
	Enabled bool `json:"enabled"`
	Repo    *struct {
		Repository string     `json:"repository"`
		Branch     string     `json:"branch"`
		Path       string     `json:"path"`
		Head       *gitCommit `json:"head"`
		LastError  string     `json:"last_error"`
	} `json:"repo"`
	PendingChanges int `json:"pending_changes"`
}

type gitFile struct {
	Path      string  `json:"path"`
	State     string  `json:"state"`
	ClusterID *string `json:"cluster_id"`
	ChangeID  *string `json:"change_id"`
	Secret    bool    `json:"secret"`
	URL       string  `json:"url"`
	Errors    []struct {
		Field   string `json:"field"`
		Line    int    `json:"line"`
		Message string `json:"message"`
	} `json:"errors"`
}

type gitChange struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	Kind        string    `json:"kind"`
	ClusterName string    `json:"cluster_name"`
	Summary     string    `json:"summary"`
	Reason      string    `json:"reason"`
	Commit      gitCommit `json:"commit"`
	Plan        struct {
		BaseRevision int `json:"base_revision"`
		Metadata     []struct {
			Field, From, To string
		} `json:"metadata"`
		Nodes []struct {
			Hostname string   `json:"hostname"`
			VIPs     []string `json:"vips"`
			Steps    []struct {
				Step   string `json:"step"`
				Change string `json:"change"`
				Diff   string `json:"diff"`
			} `json:"steps"`
		} `json:"nodes"`
		Unchanged []string `json:"unchanged"`
	} `json:"plan"`
	Local             []string `json:"local"`
	Revision          *int     `json:"revision"`
	JobID             *string  `json:"job_id"`
	DecidedBy         *string  `json:"decided_by"`
	NeedsConfirmation bool     `json:"needs_confirmation"`
	FullApply         bool     `json:"full_apply"`
	Blocked           string   `json:"blocked"`
}

func (c *client) gitFiles() map[string]gitFile {
	c.t.Helper()
	var res struct {
		Items []gitFile `json:"items"`
	}
	if s := c.do("GET", "/api/v1/gitops/files", nil, &res); s != 200 {
		c.t.Fatalf("bestanden: %d", s)
	}
	out := map[string]gitFile{}
	for _, f := range res.Items {
		out[f.Path] = f
	}
	return out
}

func (c *client) gitChanges(query string) []gitChange {
	c.t.Helper()
	var res struct {
		Items []gitChange `json:"items"`
	}
	if s := c.do("GET", "/api/v1/gitops/changes"+query, nil, &res); s != 200 {
		c.t.Fatalf("wijzigingen: %d", s)
	}
	return res.Items
}

// TestGitOpsReadValidatePlan is de acceptatietest van mijlpaal 14: binnen
// een poll na een commit staat elk bestand op geldig of ongeldig met veld
// en regel, en een andere naam geeft een plan met de diff van index.html,
// zonder dat er op de nodes iets verandert. Het VRRP-wachtwoord staat
// nergens in de GitOps-tabellen, de events of de API.
func TestGitOpsReadValidatePlan(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	rec := c.record()
	fl, clusterID, ids := deployWeb(t, e, c)
	web01, web02 := fl.host("web-01"), fl.host("web-02")
	e.createUser("piet", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	vrec := v.record()
	if s, _, _ := v.login("piet", "een-lang-wachtwoord", ""); s != 200 {
		t.Fatalf("login piet: %d", s)
	}

	// Op web-01 wijkt index.html al af; de driftcontrole ziet het.
	index01 := filepath.Join(web01.Root, "var/www/html/index.html")
	if err := os.WriteFile(index01, []byte("<h1>Onderhoud</h1>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := c.do("POST", "/api/v1/clusters/"+clusterID+"/drift/check", nil, nil); s != 200 {
		t.Fatalf("driftcontrole: %d", s)
	}
	calls01, calls02 := web01.Calls(), web02.Calls()
	files := func(root string) map[string]string {
		out := map[string]string{}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				b, _ := os.ReadFile(p)
				out[p] = string(b)
			}
			return nil
		})
		return out
	}
	before01, before02 := files(web01.Root), files(web02.Root)

	// Zonder koppeling: GitOps staat aan, export werkt, ook voor een viewer.
	var st gitStatus
	if s := v.do("GET", "/api/v1/gitops/repo", nil, &st); s != 200 || !st.Enabled || st.Repo != nil {
		t.Fatalf("status zonder koppeling: %d %+v", s, st)
	}
	s, h, export := v.get("/api/v1/clusters/" + clusterID + "/git/export")
	if s != 200 || !strings.Contains(h.Get("Content-Disposition"), `filename="web-cluster.yaml"`) {
		t.Fatalf("export: %d %v", s, h)
	}
	for _, want := range []string{"clusterforge: 1", "name: Web", "slug: web", "environment: prod", "name: keepalived-nginx",
		"version: 1.1.0", "vip: 10.0.20.100", "proxmox: Thuislab", "first_ip: 10.0.20.11/24"} {
		if !strings.Contains(export, want) {
			t.Fatalf("export mist %q:\n%s", want, export)
		}
	}
	if strings.Contains(export, "auth_pass") || strings.Contains(export, "geheim12") {
		t.Fatalf("export met geheim:\n%s", export)
	}

	// De nep-GitHub met één commit zonder clusters.
	gh := ghfake.New("jonas", "cf-config", "github_pat_test")
	ghsrv := httptest.NewServer(gh)
	t.Cleanup(ghsrv.Close)
	gh.Commit("Begin", "Jonas", map[string]string{"README.md": "# Clusters\n"})
	in := map[string]any{"api_url": ghsrv.URL, "repository": "https://github.com/jonas/cf-config.git", "token": "fout"}
	if s := v.do("PUT", "/api/v1/gitops/repo", in, nil); s != http.StatusForbidden {
		t.Fatalf("viewer koppelt: %d", s)
	}
	var ae apiErr
	if s := c.do("POST", "/api/v1/gitops/repo/probe", in, &ae); s != 400 || !strings.Contains(ae.Message, "ongeldig of verlopen") {
		t.Fatalf("test met fout token: %d %+v", s, ae)
	}
	in["token"] = "github_pat_test"
	var probe struct {
		Head  gitCommit `json:"head"`
		Files []string  `json:"files"`
	}
	if s := c.do("POST", "/api/v1/gitops/repo/probe", in, &probe); s != 200 || probe.Head.Message != "Begin" || len(probe.Files) != 0 {
		t.Fatalf("test: %d %+v", s, probe)
	}
	if st := (gitStatus{}); c.do("GET", "/api/v1/gitops/repo", nil, &st) != 200 || st.Repo != nil {
		t.Fatalf("test sloeg op: %+v", st)
	}
	if s := c.do("PUT", "/api/v1/gitops/repo", in, &st); s != 200 || st.Repo == nil || st.Repo.Repository != "jonas/cf-config" ||
		st.Repo.Branch != "main" || st.Repo.Path != "clusters" {
		t.Fatalf("koppelen: %d %+v", s, st)
	}

	// De lus pollt; in de test elke 50 ms in plaats van elke minuut.
	e.git.Interval = 50 * time.Millisecond
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); e.git.Run(runCtx) }()
	t.Cleanup(func() { stop(); <-done })
	waitHead := func(sha string) {
		t.Helper()
		eventually(t, "commit "+sha[:7]+" gelezen", func() bool {
			var st gitStatus
			c.do("GET", "/api/v1/gitops/repo", nil, &st)
			return st.Repo != nil && st.Repo.Head != nil && st.Repo.Head.SHA == sha && st.Repo.LastError == ""
		})
	}

	// Drie bestanden: de export met een andere naam, een ongeldig bestand,
	// en een nieuw cluster met het VRRP-wachtwoord erin.
	renamed := strings.Replace(export, "name: Web\n", "name: Website\n", 1)
	invalid := `clusterforge: 1
cluster:
  name: Database
  slug: db
  environment: productie
template:
  name: keepalived-nginx
  version: 1.0.0
params:
  vip: 10.0.30.100
  kleur: blauw
`
	secret := strings.NewReplacer("name: Web\n", "name: Lab\n", "slug: web", "slug: lab", "environment: prod", "environment: lab",
		"vip: 10.0.20.100", "vip: 10.0.40.100\n  auth_pass: geheim12").Replace(export)
	sha := gh.Commit("Clusters toevoegen", "Jonas", map[string]string{
		"clusters/web/cluster.yaml": renamed, "clusters/db/cluster.yaml": invalid, "clusters/lab/cluster.yaml": secret,
		"clusters/notities.md": "geen cluster",
	})
	waitHead(sha)
	fs := v.gitFiles()
	if len(fs) != 3 {
		t.Fatalf("bestanden: %+v", fs)
	}
	if f := fs["clusters/web/cluster.yaml"]; f.State != "unlinked" || f.ClusterID == nil || *f.ClusterID != clusterID ||
		!strings.HasSuffix(f.URL, "/jonas/cf-config/blob/main/clusters/web/cluster.yaml") {
		t.Fatalf("web: %+v", f)
	}
	db := fs["clusters/db/cluster.yaml"]
	if db.State != "invalid" {
		t.Fatalf("db: %+v", db)
	}
	errAt := func(f gitFile, field string, line int) {
		t.Helper()
		for _, e := range f.Errors {
			if e.Field == field && e.Line == line && e.Message != "" {
				return
			}
		}
		t.Errorf("%s: geen fout op %s regel %d: %+v", f.Path, field, line, f.Errors)
	}
	errAt(db, "cluster.environment", 5)
	errAt(db, "params.kleur", 11)
	lab := fs["clusters/lab/cluster.yaml"]
	if lab.State != "invalid" || !lab.Secret {
		t.Fatalf("lab: %+v", lab)
	}
	errAt(lab, "params.auth_pass", lineOf(secret, "auth_pass"))

	// Koppelen lukt alleen met een bestand gelijk aan de export.
	if s := v.do("POST", "/api/v1/clusters/"+clusterID+"/git/link", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer koppelt cluster: %d", s)
	}
	var mismatch struct {
		Code string `json:"code"`
		Diff string `json:"diff"`
	}
	if s := c.do("POST", "/api/v1/clusters/"+clusterID+"/git/link", nil, &mismatch); s != 409 || mismatch.Code != "git_mismatch" ||
		!strings.Contains(mismatch.Diff, "-  name: Web\n") || !strings.Contains(mismatch.Diff, "+  name: Website\n") {
		t.Fatalf("koppelen met ander bestand: %d %+v", s, mismatch)
	}
	sha = gh.Commit("Webcluster uit ClusterForge", "Jonas", map[string]string{"clusters/web/cluster.yaml": export})
	waitHead(sha)
	var cl struct {
		Name       string `json:"name"`
		GitManaged bool   `json:"git_managed"`
		GitRepoURL string `json:"git_repo_url"`
		Revision   int    `json:"spec_revision"`
	}
	if s := c.do("POST", "/api/v1/clusters/"+clusterID+"/git/link", nil, &cl); s != 200 || !cl.GitManaged ||
		!strings.HasSuffix(cl.GitRepoURL, "/jonas/cf-config/blob/main/clusters/web/cluster.yaml") {
		t.Fatalf("koppelen: %d %+v", s, cl)
	}
	revision := cl.Revision
	eventually(t, "web in sync", func() bool { return c.gitFiles()["clusters/web/cluster.yaml"].State == "in_sync" })

	// Wat uit Git komt, is nu alleen-lezen.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/api/v1/clusters/" + clusterID, map[string]any{"name": "Anders"}},
		{"DELETE", "/api/v1/clusters/" + clusterID, nil},
		{"POST", "/api/v1/clusters/" + clusterID + "/vips", map[string]any{"address": "10.0.20.101", "interface": "eth0", "vrid": 52}},
		{"PATCH", "/api/v1/nodes/" + ids["web-01"], map[string]any{"primary_ip": "10.0.20.21"}},
	} {
		var ae apiErr
		if s := c.do(tc.method, tc.path, tc.body, &ae); s != 409 || ae.Code != "git_managed" {
			t.Errorf("%s %s: %d %+v", tc.method, tc.path, s, ae)
		}
	}
	if s := c.do("PATCH", "/api/v1/nodes/"+ids["web-01"], map[string]any{"description": "rek 2"}, nil); s != 200 {
		t.Errorf("omschrijving van een node: %d", s)
	}

	// Een andere naam geeft een plan: index.html op beide nodes, de
	// VIP-eigenaar als laatste, en de afwijking op web-01.
	sha = gh.CommitVerified("Webcluster heet voortaan Website", "Jonas", map[string]string{"clusters/web/cluster.yaml": renamed})
	waitHead(sha)
	var ch gitChange
	eventually(t, "plan", func() bool {
		cs := c.gitChanges("?status=pending")
		if len(cs) == 1 {
			ch = cs[0]
		}
		return len(cs) == 1
	})
	if ch.Kind != "update" || ch.ClusterName != "Web" || ch.Commit.SHA != sha || !ch.Commit.Verified || ch.Commit.Author != "Jonas" ||
		!strings.Contains(ch.Summary, "name van Web naar Website") || !strings.Contains(ch.Summary, "index.html op 2 nodes") {
		t.Fatalf("wijziging: %+v", ch)
	}
	if f := c.gitFiles()["clusters/web/cluster.yaml"]; f.State != "pending" || f.ChangeID == nil || *f.ChangeID != ch.ID {
		t.Fatalf("web na de wijziging: %+v", f)
	}
	if s := v.do("GET", "/api/v1/gitops/changes/"+ch.ID, nil, &ch); s != 200 {
		t.Fatalf("detail: %d", s)
	}
	if ch.Plan.BaseRevision != revision || len(ch.Plan.Metadata) != 1 || ch.Plan.Metadata[0].Field != "cluster.name" {
		t.Fatalf("plan: %+v", ch.Plan)
	}
	if len(ch.Plan.Nodes) != 2 || ch.Plan.Nodes[0].Hostname != "web-02" || ch.Plan.Nodes[1].Hostname != "web-01" ||
		!slices.Equal(ch.Plan.Nodes[1].VIPs, []string{"10.0.20.100"}) {
		t.Fatalf("volgorde: %+v", ch.Plan.Nodes)
	}
	for _, n := range ch.Plan.Nodes {
		if len(n.Steps) != 1 || n.Steps[0].Step != "file:/var/www/html/index.html" || n.Steps[0].Change != "changed" ||
			!strings.Contains(n.Steps[0].Diff, "-<title>Web</title>") || !strings.Contains(n.Steps[0].Diff, "+<h1>Website</h1>") {
			t.Fatalf("stappen op %s: %+v", n.Hostname, n.Steps)
		}
	}
	if len(ch.Local) != 1 || !strings.HasPrefix(ch.Local[0], "web-01: ") || !strings.Contains(ch.Local[0], "wordt overschreven") {
		t.Fatalf("lokale afwijkingen: %q", ch.Local)
	}

	// Een nieuwere commit vervangt het plan.
	sha = gh.Commit("Toch Webcluster", "Jonas", map[string]string{
		"clusters/web/cluster.yaml": strings.Replace(export, "name: Web\n", "name: Webcluster\n", 1),
	})
	waitHead(sha)
	eventually(t, "nieuw plan", func() bool {
		cs := c.gitChanges("?status=pending")
		return len(cs) == 1 && cs[0].Commit.SHA == sha
	})
	if old := c.gitChanges("?status=superseded"); len(old) != 1 || old[0].ID != ch.ID || !strings.Contains(old[0].Reason, "nieuwere commit") {
		t.Fatalf("vervangen: %+v", old)
	}

	// Niets veranderde op de nodes of aan het cluster.
	if !slices.Equal(web01.Calls(), calls01) || !slices.Equal(web02.Calls(), calls02) {
		t.Fatalf("de nodes voerden iets uit: %v %v", web01.Calls()[len(calls01):], web02.Calls()[len(calls02):])
	}
	if !maps.Equal(files(web01.Root), before01) || !maps.Equal(files(web02.Root), before02) {
		t.Fatal("bestanden op de nodes veranderd")
	}
	if c.do("GET", "/api/v1/clusters/"+clusterID, nil, &cl); cl.Name != "Web" || cl.Revision != revision {
		t.Fatalf("cluster veranderd: %+v", cl)
	}

	// GitHub onbereikbaar: één event, de fout in de status, en weer terug.
	gh.SetDown(true)
	if s := c.do("POST", "/api/v1/gitops/sync", nil, nil); s != http.StatusAccepted {
		t.Fatalf("synchroniseren: %d", s)
	}
	eventually(t, "fout", func() bool {
		var st gitStatus
		c.do("GET", "/api/v1/gitops/repo", nil, &st)
		return st.Repo.LastError != ""
	})
	time.Sleep(200 * time.Millisecond)
	gh.SetDown(false)
	eventually(t, "hersteld", func() bool {
		var st gitStatus
		c.do("GET", "/api/v1/gitops/repo", nil, &st)
		return st.Repo.LastError == "" && st.PendingChanges == 1
	})
	page := c.audit("category=gitops")
	count := map[string]int{}
	for _, it := range page.Items {
		count[it.Action]++
	}
	if count["gitops.sync_failed"] != 1 || count["gitops.sync_recovered"] != 1 || count["gitops.repo_connected"] != 1 ||
		count["gitops.change_planned"] != 2 || count["gitops.change_superseded"] != 1 || count["gitops.file_invalid"] < 2 {
		t.Fatalf("events: %v", count)
	}

	// Ontkoppelen: het plan vervalt en het cluster is weer te wijzigen.
	if s := c.do("POST", "/api/v1/clusters/"+clusterID+"/git/unlink", nil, &cl); s != 200 || cl.GitManaged {
		t.Fatalf("ontkoppelen: %d %+v", s, cl)
	}
	if cs := c.gitChanges("?status=pending"); len(cs) != 0 {
		t.Fatalf("plan na ontkoppelen: %+v", cs)
	}
	if s := c.do("PATCH", "/api/v1/clusters/"+clusterID, map[string]any{"description": "met de hand"}, nil); s != 200 {
		t.Fatalf("wijzigen na ontkoppelen: %d", s)
	}

	// De poll gebruikt de ETag: na de eerste keer kost een poll zonder
	// commit niets, en een bestand dat niet verandert, wordt één keer
	// gelezen.
	reqs := gh.Requests()
	notModified, blobs := 0, map[string]int{}
	for _, r := range reqs {
		if strings.HasPrefix(r, "GET /repos/jonas/cf-config/commits/main ") && strings.HasSuffix(r, " 304") {
			notModified++
		}
		if strings.Contains(r, "/git/blobs/") {
			blobs[strings.TrimPrefix(strings.Fields(r)[1], "/repos/jonas/cf-config/git/blobs/")]++
		}
	}
	if notModified == 0 {
		t.Fatalf("geen 304: %v", reqs)
	}
	if blobs[ghfake.BlobSHA(invalid)] != 1 || blobs[ghfake.BlobSHA(secret)] != 1 {
		t.Fatalf("blobs: %v", blobs)
	}

	// Het VRRP-wachtwoord en het token staan nergens.
	for _, table := range []string{"git_changes", "git_repos", "events"} {
		var n int
		err := e.pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" t WHERE row_to_json(t)::text LIKE '%geheim12%' OR row_to_json(t)::text LIKE '%github_pat_test%'").Scan(&n)
		if err != nil || n != 0 {
			t.Errorf("%s: %d rijen met een geheim (%v)", table, n, err)
		}
	}
	for _, r := range []*recorder{rec, vrec} {
		if r.contains("geheim12") || r.contains("github_pat_test") {
			t.Error("een API-antwoord bevat een geheim")
		}
	}

	// De koppeling verwijderen kan alleen een admin.
	if s := v.do("DELETE", "/api/v1/gitops/repo", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer verwijdert: %d", s)
	}
	if s := c.do("DELETE", "/api/v1/gitops/repo", nil, nil); s != http.StatusNoContent {
		t.Fatalf("verwijderen: %d", s)
	}
	if fs := c.gitFiles(); len(fs) != 0 {
		t.Fatalf("bestanden na verwijderen: %+v", fs)
	}
}

func lineOf(text, needle string) int {
	for i, l := range strings.Split(text, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	return 0
}
