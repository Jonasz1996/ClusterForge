package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/docs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type testDocMeta struct {
	Scope       string     `json:"scope"`
	Kind        string     `json:"kind"`
	Path        string     `json:"path"`
	Version     *int64     `json:"version"`
	GeneratedAt *time.Time `json:"generated_at"`
}

type testDocIndex struct {
	Overview testDocMeta `json:"overview"`
	Clusters []struct {
		ID   string        `json:"id"`
		Slug string        `json:"slug"`
		Docs []testDocMeta `json:"docs"`
	} `json:"clusters"`
}

type testDoc struct {
	testDocMeta
	ClusterName string `json:"cluster_name"`
	Content     string `json:"content"`
	Cause       string `json:"cause"`
	CauseText   string `json:"cause_text"`
}

type testDocHistory struct {
	Versions []struct {
		ID        int64  `json:"id"`
		Cause     string `json:"cause"`
		CauseText string `json:"cause_text"`
	} `json:"versions"`
	Keep int `json:"keep"`
}

func TestDocs(t *testing.T) {
	e, c := adminClient(t)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login: %d", s)
	}
	ctx := context.Background()
	_, clusterID, _, pve, conn := deployWebPVE(t, e, c)

	// Een back-up van web-01, zodat het runbook het echte volume noemt.
	var vmid int
	if err := e.pool.QueryRow(ctx, "SELECT pve_vmid FROM nodes WHERE hostname = 'web-01'").Scan(&vmid); err != nil {
		t.Fatal(err)
	}
	pve.AddStorage(pvefake.Storage{Name: "pbs", Node: "pve1", Shared: true, Content: "backup"})
	pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: vmid, Time: time.Now().Add(-time.Hour).Truncate(time.Second), Size: 2 << 30})
	if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != http.StatusOK {
		t.Fatalf("sync: %d", s)
	}
	if err := e.backups.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}

	// De klok staat in de nacht voor zes uur, zodat Tick niet zelf alles
	// opnieuw schrijft.
	clock := time.Date(2030, 1, 1, 2, 0, 0, 0, time.Local)
	e.docs.Now = func() time.Time { return clock }

	// Voor de eerste ronde is er geen versie, maar een document kan al
	// gelezen worden.
	var idx testDocIndex
	if s := viewer.do("GET", "/api/v1/docs", nil, &idx); s != http.StatusOK || len(idx.Clusters) != 1 || idx.Clusters[0].ID != clusterID ||
		len(idx.Clusters[0].Docs) != 6 || idx.Overview.Version != nil || idx.Clusters[0].Docs[1].Path != "clusters/web/netwerk.md" {
		t.Fatalf("index: %d %+v", s, idx)
	}
	q := "?cluster=" + clusterID
	var live testDoc
	if s := viewer.do("GET", "/api/v1/docs/cluster/architectuur"+q, nil, &live); s != http.StatusOK || live.Version != nil ||
		live.ClusterName != "Web" || !strings.HasPrefix(live.Content, "# Web: architectuur") {
		t.Fatalf("live: %d %+v", s, live.testDocMeta)
	}

	n, err := e.docs.GenerateAll(ctx, docs.CauseStart)
	if err != nil || n != 7 {
		t.Fatalf("eerste ronde: %d %v", n, err)
	}
	if n, err := e.docs.GenerateAll(ctx, docs.CauseNight); err != nil || n != 0 {
		t.Fatalf("tweede ronde zonder wijziging: %d %v", n, err)
	}

	get := func(cl *client, scope, kind string) testDoc {
		t.Helper()
		path := "/api/v1/docs/" + scope + "/" + kind
		if scope == docs.ScopeCluster {
			path += q
		}
		var d testDoc
		if s := cl.do("GET", path, nil, &d); s != http.StatusOK || d.Version == nil {
			t.Fatalf("%s/%s: %d %+v", scope, kind, s, d.testDocMeta)
		}
		return d
	}
	all := map[string]string{"overzicht": get(viewer, docs.ScopeOverview, docs.KindOverview).Content}
	for _, k := range docs.ClusterKinds {
		all[k] = get(viewer, docs.ScopeCluster, k).Content
	}
	for kind, want := range map[string][]string{
		"overzicht":    {"[Web](clusters/web/architectuur.md)", "### Thuislab"},
		"architectuur": {"| **web-01** |", "`10.0.20.100` op `eth0`", "eerst web-01, dan web-02", "keepalived-nginx"},
		"netwerk":      {"```mermaid", "flowchart TB", "`10.0.20.11/24`", "VIP 10.0.20.100"},
		"configuratie": {"| `auth_pass` | `[geheim]` |", "#### `/etc/keepalived/keepalived.conf`", "auth_pass [geheim]", "```yaml", "| admin |"},
		"herstel":      {"## Back-ups", "| **web-01** |", "`pbs`"},
		"runbooks": {
			"systemctl stop keepalived", "ip -brief address show dev eth0 | grep -F 10.0.20.100",
			"qmrestore pbs:backup/vm/" + strconv.Itoa(vmid) + "/",
		},
	} {
		for _, w := range want {
			if !strings.Contains(all[kind], w) {
				t.Errorf("%s mist %q:\n%s", kind, w, all[kind])
			}
		}
	}

	// Download: een zip met Markdown, voor viewers ook.
	s, h, body := viewer.get("/api/v1/docs/download")
	if s != http.StatusOK || h.Get("Content-Type") != "application/zip" {
		t.Fatalf("download: %d %s", s, h.Get("Content-Type"))
	}
	zr, err := zip.NewReader(bytes.NewReader([]byte(body)), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	files := map[string]string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r)
		_ = r.Close()
		files[f.Name] = string(b)
		if !strings.HasPrefix(string(b), "*Geschreven door ClusterForge op ") {
			t.Errorf("%s heeft geen kopregel", f.Name)
		}
	}
	if strings.Join(names, " ") != "README.md clusters/web/architectuur.md clusters/web/netwerk.md clusters/web/configuratie.md clusters/web/cronjobs.md "+
		"clusters/web/herstel.md clusters/web/runbooks.md" {
		t.Fatalf("zip: %v", names)
	}

	// Geen enkel geheim uit de fleet staat in de documentatie: niet het
	// wachtwoord van keepalived en niet het token van Proxmox.
	secrets := []string{"geheim12", "geheim-secret"}
	for name, text := range files {
		all["zip:"+name] = text
	}
	for name, text := range all {
		for _, sec := range secrets {
			if strings.Contains(text, sec) {
				t.Errorf("%s bevat een geheim", name)
			}
		}
	}

	// De assistent leest de documentatie zonder de inhoud van de bestanden.
	for _, tool := range e.asst.Tools {
		if tool.Name != "documentatie" {
			continue
		}
		u, _ := store.New(e.pool).GetUserByUsername(ctx, "kijker")
		res, err := tool.Run(ctx, assistant.Caller{Principal: auth.Principal{User: u}}, json.RawMessage(`{"cluster":"web","hoofdstuk":"configuratie"}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res)
		if out := string(b); strings.Contains(out, "vrrp_instance") || strings.Contains(out, "ssh-ed25519") ||
			!strings.Contains(out, "(inhoud weggelaten)") || !strings.Contains(out, "auth_pass") {
			t.Fatalf("tool: %s", out)
		}
		var te assistant.ToolError
		if _, err := tool.Run(ctx, assistant.Caller{Principal: auth.Principal{User: u}}, json.RawMessage(`{"cluster":"","hoofdstuk":"netwerk"}`)); !errors.As(err, &te) {
			t.Fatalf("hoofdstuk zonder cluster: %v", err)
		}
	}

	// Een wijziging komt in de rij en wordt pas na tien minuten een
	// nieuwe versie.
	if err := e.docs.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if s := c.do("PATCH", "/api/v1/clusters/"+clusterID, map[string]any{"description": "Nieuwe beschrijving van de website"}, nil); s != http.StatusOK {
		t.Fatalf("beschrijving: %d", s)
	}
	if err := e.docs.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/docs", nil, &idx)
	var hist testDocHistory
	c.do("GET", "/api/v1/docs/cluster/architectuur/history"+q, nil, &hist)
	if len(hist.Versions) != 1 || hist.Keep != docs.Keep || idx.Clusters[0].Docs[0].Version == nil {
		t.Fatalf("na de wijziging, voor de wachttijd: %+v", hist)
	}
	var queued struct {
		Clusters []struct {
			QueuedUntil *time.Time `json:"queued_until"`
		} `json:"clusters"`
	}
	if c.do("GET", "/api/v1/docs", nil, &queued); queued.Clusters[0].QueuedUntil == nil || !queued.Clusters[0].QueuedUntil.Equal(clock.Add(10*time.Minute)) {
		t.Fatalf("in de rij: %+v", queued)
	}
	clock = clock.Add(11 * time.Minute)
	if err := e.docs.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/docs/cluster/architectuur/history"+q, nil, &hist)
	if len(hist.Versions) != 2 || !strings.HasPrefix(hist.Versions[0].Cause, "event:") || !strings.HasPrefix(hist.Versions[0].CauseText, "Na: ") ||
		hist.Versions[1].Cause != docs.CauseStart {
		t.Fatalf("geschiedenis: %+v", hist)
	}
	// Alleen het hoofdstuk dat veranderde, kreeg een nieuwe versie.
	var nh testDocHistory
	c.do("GET", "/api/v1/docs/cluster/netwerk/history"+q, nil, &nh)
	if len(nh.Versions) != 1 {
		t.Fatalf("netwerk: %+v", nh)
	}
	var diff struct {
		Diff    string `json:"diff"`
		Changed int    `json:"changed"`
	}
	from, to := strconv.FormatInt(hist.Versions[1].ID, 10), strconv.FormatInt(hist.Versions[0].ID, 10)
	if s := viewer.do("GET", "/api/v1/docs/cluster/architectuur/diff"+q+"&from="+from+"&to="+to, nil, &diff); s != http.StatusOK ||
		!strings.Contains(diff.Diff, "+Nieuwe beschrijving van de website") || diff.Changed < 1 {
		t.Fatalf("diff: %d %+v", s, diff)
	}
	var old testDoc
	if s := viewer.do("GET", "/api/v1/docs/cluster/architectuur"+q+"&version="+from, nil, &old); s != http.StatusOK ||
		strings.Contains(old.Content, "Nieuwe beschrijving") || old.CauseText != "Bij de start van de server" {
		t.Fatalf("oude versie: %d %+v", s, old.testDocMeta)
	}

	// 's Nachts wordt alles opnieuw geschreven; zonder wijziging komt er
	// geen versie bij.
	clock = time.Date(2030, 1, 1, 6, 30, 0, 0, time.Local)
	if err := e.docs.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/docs/cluster/architectuur/history"+q, nil, &hist)
	if len(hist.Versions) != 2 {
		t.Fatalf("na de nacht: %+v", hist)
	}

	// Fouten en rechten.
	for path, want := range map[string]int{
		"/api/v1/docs/cluster/overzicht" + q:                                        http.StatusNotFound,
		"/api/v1/docs/overview/overzicht" + q:                                       http.StatusBadRequest,
		"/api/v1/docs/cluster/netwerk":                                              http.StatusBadRequest,
		"/api/v1/docs/cluster/handboek" + q:                                         http.StatusNotFound,
		"/api/v1/docs/cluster/netwerk" + q + "&version=" + from:                     http.StatusNotFound,
		"/api/v1/docs/cluster/netwerk?cluster=00000000-0000-0000-0000-000000000000": http.StatusNotFound,
	} {
		if s := viewer.do("GET", path, nil, nil); s != want {
			t.Errorf("%s: %d, want %d", path, s, want)
		}
	}
	if s := viewer.do("POST", "/api/v1/docs/generate", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer schrijft: %d", s)
	}
	var gen struct {
		Changed int `json:"changed"`
	}
	if s := c.do("POST", "/api/v1/docs/generate", nil, &gen); s != http.StatusOK || gen.Changed != 0 {
		t.Fatalf("generate: %d %+v", s, gen)
	}
}
