package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestGitOpsScale is de acceptatietest van mijlpaal 17: node_count van 2
// naar 3 maakt na goedkeuring een derde VM en zet keepalived.conf met drie
// peers op alle nodes, waarna het cluster healthy is zonder
// split_brain-event. Een nieuw bestand clusters/api/cluster.yaml wordt na
// goedkeuring een uitrol met spec-revisie 1 en bron git.
func TestGitOpsScale(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	fl, clusterID, _, _, _ := deployWebPVE(t, e, c)
	fl.mu.Lock()
	fl.addrs["web-03"], fl.addrs["api-01"], fl.addrs["api-02"] = "10.0.20.13", "10.0.20.21", "10.0.20.22"
	fl.mu.Unlock()
	if _, err := e.pool.Exec(ctx, "UPDATE users SET totp_enabled_at = now() WHERE username = 'admin'"); err != nil {
		t.Fatal(err)
	}
	gh, export, commit := gitLinked(t, e, c, clusterID)
	detail := func(id string) gitChange {
		t.Helper()
		var ch gitChange
		if s := c.do("GET", "/api/v1/gitops/changes/"+id, nil, &ch); s != 200 {
			t.Fatalf("wijziging %s: %d", id, s)
		}
		return ch
	}
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	splitEvents := func() int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action = 'cluster.status_changed' AND payload->>'to' = 'split_brain'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Zonder het adres van ClusterForge bij de koppeling kan het niet.
	id := commit(strings.Replace(export, "node_count: 2", "node_count: 3", 1))
	ch := detail(id)
	if ch.BlockedCode != "no_server_url" {
		t.Fatalf("zonder adres: %+v", ch)
	}
	var repo struct {
		Repo struct {
			APIURL     string `json:"api_url"`
			Repository string `json:"repository"`
			ServerURL  string `json:"server_url"`
		} `json:"repo"`
	}
	c.do("GET", "/api/v1/gitops/repo", nil, &repo)
	var ae struct {
		Code  string `json:"code"`
		Field string `json:"field"`
	}
	in := map[string]any{"api_url": repo.Repo.APIURL, "repository": repo.Repo.Repository, "server_url": "ftp://cf"}
	if s := c.do("PUT", "/api/v1/gitops/repo", in, &ae); s != 400 || ae.Field != "server_url" {
		t.Fatalf("ongeldig adres: %d %+v", s, ae)
	}
	in["server_url"] = e.srv.URL + "/"
	if s := c.do("PUT", "/api/v1/gitops/repo", in, &repo); s != 200 || repo.Repo.ServerURL != e.srv.URL {
		t.Fatalf("adres opslaan: %d %+v", s, repo)
	}

	// Het plan: web-03 met het volgende adres, en keepalived.conf op de
	// bestaande nodes met de nieuwe peer. Eerst de bestaande nodes, de
	// VIP-eigenaar als laatste daarvan, en dan web-03.
	ch = detail(id)
	if ch.Blocked != "" || ch.Kind != "update" || !strings.Contains(ch.Summary, "node_count van 2 naar 3") ||
		!strings.Contains(ch.Summary, "keepalived.conf op 2 nodes, 1 nieuwe node") {
		t.Fatalf("wijziging: %+v", ch)
	}
	if len(ch.Plan.NewNodes) != 1 || ch.Plan.NewNodes[0].Hostname != "web-03" || ch.Plan.NewNodes[0].Address != "10.0.20.13" {
		t.Fatalf("nieuwe nodes: %+v", ch.Plan.NewNodes)
	}
	var order []string
	for _, n := range ch.Plan.Nodes {
		order = append(order, n.Hostname)
		if n.Hostname == "web-03" {
			continue
		}
		if len(n.Steps) != 1 || n.Steps[0].Step != "file:/etc/keepalived/keepalived.conf" || !strings.Contains(n.Steps[0].Diff, "+        10.0.20.13\n") {
			t.Fatalf("stappen op %s: %+v", n.Hostname, n.Steps)
		}
	}
	if !slices.Equal(order, []string{"web-02", "web-01", "web-03"}) || !ch.Plan.Nodes[2].New {
		t.Fatalf("volgorde: %v %+v", order, ch.Plan.Nodes)
	}

	// Goedkeuren: web-03 staat meteen in de inventory, in provisioning, en
	// spec-revisie 2 kent hem met zijn echte id.
	e.stopRunner()
	var dec gitDecision
	if s := c.do("POST", "/api/v1/gitops/changes/"+id+"/approve", map[string]any{"confirm": "web"}, &dec); s != http.StatusAccepted || dec.Job == nil {
		t.Fatalf("goedkeuren: %d %+v", s, dec)
	}
	var web03ID, lifecycle string
	if err := e.pool.QueryRow(ctx, "SELECT id, lifecycle FROM nodes WHERE hostname = 'web-03'").Scan(&web03ID, &lifecycle); err != nil || lifecycle != "provisioning" {
		t.Fatalf("web-03 in de inventory: %v %s", err, lifecycle)
	}
	var spec string
	if err := e.pool.QueryRow(ctx, "SELECT spec::text FROM clusters WHERE id = $1", clusterID).Scan(&spec); err != nil || !strings.Contains(spec, web03ID) {
		t.Fatalf("spec zonder het id van web-03: %v %s", err, spec)
	}
	e.startRunner()
	j := waitJob(t, c, dec.Job.ID)
	var steps []string
	for _, st := range j.Steps {
		steps = append(steps, st.Name)
	}
	want := []string{"VM web-03 maken", "Agents aanmelden", "Toepassen op web-02", "Toepassen op web-01 (VIP-eigenaar)", "Toepassen op web-03 (nieuw)"}
	if j.Status != "succeeded" || !slices.Equal(steps, want) {
		t.Fatalf("taak: %s %v %s", j.Status, steps, j.Error)
	}
	if !strings.Contains(strings.Join(j.Steps[4].Log, "\n"), "web-03 is actief en telt mee") {
		t.Fatalf("log van web-03: %v", j.Steps[4].Log)
	}

	// keepalived.conf met drie peers op alle nodes: elk de andere twee.
	addrs := map[string]string{"web-01": "10.0.20.11", "web-02": "10.0.20.12", "web-03": "10.0.20.13"}
	peerRe := regexp.MustCompile(`(?s)unicast_peer \{\s*([^}]*?)\s*\}`)
	for host, addr := range addrs {
		conf := read(filepath.Join(fl.host(host).Root, "etc/keepalived/keepalived.conf"))
		m := peerRe.FindStringSubmatch(conf)
		var others []string
		for h, a := range addrs {
			if h != host {
				others = append(others, a)
			}
		}
		slices.Sort(others)
		if m == nil || !slices.Equal(strings.Fields(m[1]), others) || !strings.Contains(conf, "unicast_src_ip "+addr) {
			t.Fatalf("keepalived.conf op %s:\n%s", host, conf)
		}
	}
	if !strings.Contains(read(filepath.Join(fl.host("web-03").Root, "etc/keepalived/keepalived.conf")), "priority 130") {
		t.Fatal("web-03 heeft niet de prioriteit van index 3")
	}

	// Healthy met drie actieve nodes, zonder split-brain.
	var cl struct {
		Status          string `json:"status"`
		SpecRevision    int    `json:"spec_revision"`
		AppliedRevision int    `json:"applied_revision"`
		Nodes           []node `json:"nodes"`
	}
	eventually(t, "drie nodes, healthy", func() bool {
		c.do("GET", "/api/v1/clusters/"+clusterID, nil, &cl)
		return cl.Status == "healthy" && len(cl.Nodes) == 3 && cl.AppliedRevision == 2
	})
	for _, n := range cl.Nodes {
		if n.Lifecycle != "active" {
			t.Fatalf("%s is %s", n.Hostname, n.Lifecycle)
		}
	}
	if s := fl.group.Splits(); len(s) != 0 || splitEvents() != 0 {
		t.Fatalf("split-brain: %v, %d events", s, splitEvents())
	}
	if owner := fl.group.Owner("10.0.20.100"); owner != fl.host("web-01") {
		t.Fatalf("het VIP staat niet meer op web-01: %v", owner)
	}
	eventually(t, "toegepast", func() bool { return detail(id).Status == "applied" })
	var vmName string
	if err := e.pool.QueryRow(ctx, "SELECT g.name FROM nodes n JOIN proxmox_resources g ON g.connection_id = n.proxmox_id AND g.vmid = n.pve_vmid WHERE n.id = $1",
		web03ID).Scan(&vmName); err != nil || vmName != "web-03" {
		t.Fatalf("VM van web-03: %v %q", err, vmName)
	}

	// Een nieuw bestand: een uitrol met spec-revisie 1 en bron git.
	vrid := regexp.MustCompile(`vrid: \d+`)
	api := strings.NewReplacer("name: Web\n", "name: API\n", "slug: web", "slug: api", "environment: prod", "environment: lab",
		"vip: 10.0.20.100", "vip: 10.0.20.200", "first_ip: 10.0.20.11/24", "first_ip: 10.0.20.21/24").Replace(export)
	api = vrid.ReplaceAllString(api, "vrid: 60")
	sha := gh.Commit("API-cluster erbij", "Jonas", map[string]string{"clusters/api/cluster.yaml": api})
	var create gitChange
	eventually(t, "plan voor api", func() bool {
		for _, ch := range c.gitChanges("?status=pending") {
			if ch.Slug == "api" && ch.Commit.SHA == sha {
				create = ch
				return true
			}
		}
		return false
	})
	create = detail(create.ID)
	if create.Kind != "create" || create.Blocked != "" || create.NeedsConfirmation || len(create.Plan.NewNodes) != 2 ||
		create.Plan.NewNodes[0].Address != "10.0.20.21" || create.Plan.NewNodes[1].Hostname != "api-02" {
		t.Fatalf("nieuw cluster: %+v", create)
	}
	if s := c.do("POST", "/api/v1/gitops/changes/"+create.ID+"/approve", nil, &dec); s != http.StatusAccepted || dec.Job == nil ||
		dec.Change.Status != "applying" || dec.Change.Revision == nil || *dec.Change.Revision != 1 {
		t.Fatalf("nieuw cluster goedkeuren: %d %+v", s, dec)
	}
	if j := waitJob(t, c, dec.Job.ID); j.Status != "succeeded" || !strings.HasPrefix(j.Title, "Uitrollen: API") {
		t.Fatalf("uitrol: %+v", j)
	}
	created := detail(create.ID)
	if created.ClusterID == nil {
		t.Fatalf("de wijziging wijst niet naar het cluster: %+v", created)
	}
	apiURL := "/api/v1/clusters/" + *created.ClusterID
	var apiCl struct {
		Name        string `json:"name"`
		Status      string `json:"status"`
		GitManaged  bool   `json:"git_managed"`
		GitRepoURL  string `json:"git_repo_url"`
		Environment string `json:"environment"`
	}
	eventually(t, "api healthy", func() bool {
		c.do("GET", apiURL, nil, &apiCl)
		return apiCl.Status == "healthy"
	})
	if apiCl.Name != "API" || !apiCl.GitManaged || !strings.HasSuffix(apiCl.GitRepoURL, "/jonas/cf-config/blob/main/clusters/api/cluster.yaml") || apiCl.Environment != "lab" {
		t.Fatalf("api: %+v", apiCl)
	}
	var hist struct {
		Items []struct {
			Revision  int     `json:"revision"`
			Source    string  `json:"source"`
			CommitSha *string `json:"commit_sha"`
		} `json:"items"`
	}
	if s := c.do("GET", apiURL+"/spec-revisions", nil, &hist); s != 200 || len(hist.Items) != 1 || hist.Items[0].Revision != 1 ||
		hist.Items[0].Source != "git" || hist.Items[0].CommitSha == nil || *hist.Items[0].CommitSha != sha {
		t.Fatalf("revisies van api: %d %+v", s, hist)
	}
	eventually(t, "api toegepast en in sync", func() bool {
		return detail(create.ID).Status == "applied" && c.gitFiles()["clusters/api/cluster.yaml"].State == "in_sync"
	})
	if s := fl.group.Splits(); len(s) != 0 || splitEvents() != 0 {
		t.Fatalf("split-brain na api: %v", s)
	}

	// Het logboek.
	page := c.audit("category=gitops")
	var approved []string
	for _, it := range page.Items {
		if it.Action == "gitops.change_approved" {
			approved = append(approved, it.Summary)
		}
	}
	if len(approved) != 2 || !strings.Contains(approved[0], "Nieuw cluster API uit commit "+sha[:7]+" goedgekeurd") ||
		!strings.Contains(approved[1], "met 1 nieuwe node") {
		t.Fatalf("logboek: %q", approved)
	}
}
