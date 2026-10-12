package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type kubeSnapshotView struct {
	Name        string     `json:"name"`
	File        string     `json:"file"`
	Node        string     `json:"node"`
	Hostname    *string    `json:"hostname"`
	Kind        string     `json:"kind"`
	S3          bool       `json:"s3"`
	SizeBytes   *int64     `json:"size_bytes"`
	TakenAt     *time.Time `json:"taken_at"`
	Ready       bool       `json:"ready"`
	Error       string     `json:"error"`
	Restorable  bool       `json:"restorable"`
	RestoreNote string     `json:"restore_note"`
}

type kubeSnapshotsView struct {
	Items     []kubeSnapshotView `json:"items"`
	Error     string             `json:"error"`
	Freshness string             `json:"freshness"`
	Servers   []struct {
		Node     string `json:"node"`
		Hostname string `json:"hostname"`
		Init     bool   `json:"init"`
	} `json:"servers"`
}

// TestKubernetesSnapshots is de acceptatie van mijlpaal 10 van fase 4: de
// snapshots per server staan in het dashboard en bij Back-ups, Snapshot nu
// werkt, en terugzetten werkt in lab, ook verder na een fout. Op prod zet
// ClusterForge niets terug.
func TestKubernetesSnapshots(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	fake := kubefake.New()
	fake.Start()
	t.Cleanup(fake.Close)
	servers := []struct{ name, ip string }{{"leren-server-01", "10.0.40.11"}, {"leren-server-02", "10.0.40.12"}, {"leren-server-03", "10.0.40.13"}}
	fake.SeedK3s(
		kubefake.NodeSpec{Name: servers[0].name, IP: servers[0].ip, Server: true}, kubefake.NodeSpec{Name: servers[1].name, IP: servers[1].ip, Server: true},
		kubefake.NodeSpec{Name: servers[2].name, IP: servers[2].ip, Server: true}, kubefake.NodeSpec{Name: "leren-worker-01", IP: "10.0.40.21"},
	)
	now := time.Now().Truncate(time.Second)
	scheduled := func(node string, at time.Time) string { return fmt.Sprintf("etcd-snapshot-%s-%d", node, at.Unix()) }
	files := map[string][]string{}
	for _, s := range servers {
		for _, at := range []time.Time{now.Add(-13 * time.Hour), now.Add(-time.Hour)} {
			f := scheduled(s.name, at)
			files[s.name] = append(files[s.name], f)
			fake.Add(kubefake.SnapshotFile(kubefake.SnapshotSpec{Node: s.name, File: f, Taken: at, Size: 6283296}))
		}
	}
	fake.Add(
		kubefake.SnapshotFile(kubefake.SnapshotSpec{Node: servers[0].name, File: "on-demand-leren-server-01-1", Taken: now.Add(-2 * time.Hour), S3: true}),
		kubefake.SnapshotFile(kubefake.SnapshotSpec{Node: servers[0].name, File: "etcd-snapshot-leren-server-01-2", Taken: now.Add(-25 * time.Hour),
			Error: "snapshot failed: no space left on device"}),
	)

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl)
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.40.10"}, nil)
	id := uuid.MustParse(cl.ID)

	// Elke server heeft een agent met een nagespeelde k3s. ran houdt de
	// commando's van alle servers bij, in volgorde.
	var mu sync.Mutex
	var ran []string
	roots := map[string]string{}
	hosts := map[string]*agenttest.Host{}
	for i, s := range servers {
		var n node
		c.do("POST", "/api/v1/nodes", map[string]any{"hostname": s.name, "cluster_id": cl.ID, "primary_ip": s.ip}, &n)
		startAgentWith(t, e, c, n.ID, fmt.Sprintf("a10000000000000000000000000001%02d", i), &addrs{}, func(ag *agent.Agent, root string) {
			if err := fake.WriteKubeconfig(root); err != nil {
				t.Fatal(err)
			}
			ag.KubeletHealthz = fake.KubeletURL()
			// De nagespeelde machines heten allemaal vm; k3s gebruikt dan
			// node-name.
			cfg := "token: \"geheim\"\nnode-name: " + s.name + "\nnode-ip: " + s.ip + "\n"
			if i == 0 {
				cfg += "cluster-init: true\n"
			} else {
				cfg += "server: https://10.0.40.10:6443\n"
			}
			write := map[string]string{"etc/rancher/k3s/config.yaml": cfg, "etc/systemd/system/k3s.service": "[Service]\n",
				protocol.KubeDBDir[1:] + "/etcd/member/snap/db": "etcd"}
			for _, f := range files[s.name] {
				write[protocol.KubeSnapshotDir[1:]+"/"+f] = "snapshot"
			}
			for p, body := range write {
				if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, p), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			h := agenttest.NewHost(root, s.ip)
			h.SetUnit("k3s", agenttest.Unit{Enabled: true, Active: true})
			h.K3s = func(args []string) ([]byte, error) {
				switch {
				case len(args) == 1 && args[0] == "--version":
					return []byte("k3s version v1.35.9+k3s1 (0a1b2c3d)\n"), nil
				case len(args) == 4 && args[0] == "etcd-snapshot":
					// Zoals k3s: de node en het tijdstip erachter, en
					// meteen een ETCDSnapshotFile in de API.
					at := time.Now()
					f := fmt.Sprintf("%s-%s-%d", args[3], s.name, at.Unix())
					fake.Add(kubefake.SnapshotFile(kubefake.SnapshotSpec{Node: s.name, File: f, Taken: at, Size: 6291488}))
					return []byte("INFO[0000] Snapshot " + f + " saved.\n"), nil
				}
				return []byte("INFO[0003] Managed etcd cluster membership has been reset, restart without --cluster-reset flag now.\n"), nil
			}
			ag.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				call := strings.Join(append([]string{name}, args...), " ")
				if call == "systemctl stop k3s" || call == "systemctl start k3s" || (name == "k3s" && args[0] != "--version") {
					mu.Lock()
					ran = append(ran, s.name+": "+call)
					mu.Unlock()
				}
				return h.Exec(ctx, name, args...)
			}
			roots[s.name], hosts[s.name] = root, h
		})
	}
	eventually(t, "drie servers met k3s", func() bool {
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM node_facts f JOIN nodes n ON n.id = f.node_id
			WHERE n.cluster_id = $1 AND f.facts -> 'kube' ->> 'role' = 'server'`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 3
	})
	eventually(t, "eerste sync", func() bool { return e.kube.Sync(ctx, id) == nil })
	base := "/api/v1/kubernetes/clusters/" + cl.ID + "/snapshots"
	list := func() kubeSnapshotsView {
		t.Helper()
		var v kubeSnapshotsView
		if s := c.do("GET", base, nil, &v); s != http.StatusOK {
			t.Fatalf("lijst: %d", s)
		}
		return v
	}
	find := func(v kubeSnapshotsView, file string) kubeSnapshotView {
		t.Helper()
		i := slices.IndexFunc(v.Items, func(it kubeSnapshotView) bool { return it.File == file })
		if i < 0 {
			t.Fatalf("%s niet in de lijst: %+v", file, v.Items)
		}
		return v.Items[i]
	}

	// De lijst: per server, met wat terug kan.
	v := list()
	if len(v.Items) != 8 || v.Freshness != "ok" || v.Error != "" || len(v.Servers) != 3 || v.Servers[0].Node != servers[0].name || !v.Servers[0].Init ||
		v.Servers[1].Init {
		t.Fatalf("lijst: %+v", v)
	}
	recent := find(v, files[servers[0].name][1])
	if !recent.Restorable || recent.Kind != "scheduled" || recent.SizeBytes == nil || *recent.SizeBytes != 6283296 || recent.Hostname == nil ||
		*recent.Hostname != servers[0].name || recent.TakenAt == nil || !recent.TakenAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("recent: %+v", recent)
	}
	for file, note := range map[string]string{
		files[servers[1].name][1]:         "alleen een snapshot op leren-server-01",
		"on-demand-leren-server-01-1":     "S3",
		"etcd-snapshot-leren-server-01-2": "no space left on device",
	} {
		if it := find(v, file); it.Restorable || !strings.Contains(it.RestoreNote, note) {
			t.Errorf("%s: %+v", file, it)
		}
	}
	if find(v, "on-demand-leren-server-01-1").Kind != "on_demand" {
		t.Error("soort van op verzoek")
	}

	// Een viewer ziet de lijst, maar maakt geen snapshot en zet niets
	// terug.
	e.createUser("lisa", "een-lang-wachtwoord", store.UserRoleViewer)
	lisa := e.client()
	if s, _, _ := lisa.login("lisa", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login lisa: %d", s)
	}
	var lv kubeSnapshotsView
	if s := lisa.do("GET", base, nil, &lv); s != http.StatusOK || len(lv.Items) != 8 {
		t.Fatalf("viewer leest: %d", s)
	}
	if s := lisa.do("POST", base, nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer maakt: %d", s)
	}
	if s := lisa.do("POST", base+"/restore", map[string]any{"name": recent.Name, "preview": true}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer zet terug: %d", s)
	}

	// Snapshot nu gaat naar de server die het cluster begon.
	var saved struct {
		Snapshot *kubeSnapshotView `json:"snapshot"`
		Hostname string            `json:"hostname"`
		Output   []string          `json:"output"`
	}
	if s := c.do("POST", base, nil, &saved); s != http.StatusCreated || saved.Hostname != servers[0].name || saved.Snapshot == nil ||
		!strings.HasPrefix(saved.Snapshot.File, "clusterforge-leren-server-01-") || saved.Snapshot.Kind != "clusterforge" ||
		!saved.Snapshot.Restorable || len(saved.Output) != 1 || !strings.Contains(saved.Output[0], "saved") {
		t.Fatalf("snapshot nu: %d %+v", s, saved)
	}
	if !slices.Equal(ran, []string{"leren-server-01: k3s etcd-snapshot save --name clusterforge"}) {
		t.Fatalf("commando's: %v", ran)
	}
	kubeLog := func(action string) []auditEntry {
		var out []auditEntry
		for _, it := range c.audit("category=kubernetes&cluster=" + cl.ID).Items {
			if it.Action == action {
				out = append(out, it)
			}
		}
		return out
	}
	if l := kubeLog("kube.snapshot_saved"); len(l) != 1 || l[0].Summary != "Snapshot van etcd gemaakt op leren-server-01: "+saved.Snapshot.File {
		t.Fatalf("logboek: %+v", l)
	}

	// Bij Back-ups: per server de nieuwste en hoeveel er zijn.
	var overview struct {
		Items []struct {
			ClusterID string     `json:"cluster_id"`
			Freshness string     `json:"freshness"`
			NewestAt  *time.Time `json:"newest_at"`
			Servers   []struct {
				Node     string     `json:"node"`
				Hostname *string    `json:"hostname"`
				NewestAt *time.Time `json:"newest_at"`
				Count    int        `json:"count"`
			} `json:"servers"`
		} `json:"items"`
	}
	if s := lisa.do("GET", "/api/v1/kubernetes/snapshots", nil, &overview); s != http.StatusOK || len(overview.Items) != 1 {
		t.Fatalf("overzicht: %d %+v", s, overview)
	}
	if o := overview.Items[0]; o.ClusterID != cl.ID || o.Freshness != "ok" || o.NewestAt == nil || !o.NewestAt.Equal(*saved.Snapshot.TakenAt) ||
		len(o.Servers) != 3 || o.Servers[0].Count != 5 || o.Servers[1].Count != 2 || o.Servers[1].Hostname == nil ||
		*o.Servers[1].Hostname != servers[1].name || !o.Servers[1].NewestAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("overzicht: %+v", o)
	}

	// Het plan, en wat niet kan.
	errOf := func(c *client, path string, body any) (int, apiErr) {
		t.Helper()
		var er apiErr
		s := c.do("POST", path, body, &er)
		return s, er
	}
	for _, tc := range []struct {
		body       map[string]any
		status     int
		code, text string
	}{
		{map[string]any{"name": find(v, files[servers[1].name][1]).Name, "preview": true}, 409, "conflict", "staat op leren-server-02"},
		{map[string]any{"name": find(v, "on-demand-leren-server-01-1").Name, "preview": true}, 409, "conflict", "S3"},
		{map[string]any{"name": "bestaat-niet", "preview": true}, 409, "conflict", "bestaat niet meer"},
		{map[string]any{"name": "", "preview": true}, 400, "validation", "kies een snapshot"},
		{map[string]any{"name": recent.Name}, 409, "needs_confirmation", "slug leren"},
		{map[string]any{"name": recent.Name, "confirm": "prod"}, 409, "needs_confirmation", "slug leren"},
	} {
		if s, er := errOf(c, base+"/restore", tc.body); s != tc.status || er.Code != tc.code || !strings.Contains(er.Message, tc.text) {
			t.Errorf("%v: %d %+v", tc.body, s, er)
		}
	}
	var plan struct {
		Snapshot kubeSnapshotView `json:"snapshot"`
		Init     string           `json:"init"`
		Others   []string         `json:"others"`
		Workers  int              `json:"workers"`
	}
	if s := c.do("POST", base+"/restore", map[string]any{"name": recent.Name, "preview": true}, &plan); s != http.StatusOK ||
		plan.Snapshot.File != recent.File || plan.Init != servers[0].name || !slices.Equal(plan.Others, []string{servers[1].name, servers[2].name}) ||
		plan.Workers != 1 {
		t.Fatalf("plan: %d %+v", s, plan)
	}

	// Terugzetten. Het terugzetten zelf mislukt eerst; opnieuw proberen
	// gaat verder bij die stap, met k3s nog overal gestopt.
	mu.Lock()
	ran = nil
	mu.Unlock()
	hosts[servers[0].name].Fail("k3s server", "FATA[0002] etcd: snapshot file is corrupt")
	var j job
	if s := c.do("POST", base+"/restore", map[string]any{"name": recent.Name, "confirm": "leren"}, &j); s != http.StatusAccepted ||
		j.Title != "Snapshot terugzetten: "+recent.File {
		t.Fatalf("terugzetten: %d %+v", s, j)
	}
	j = waitJob(t, c, j.ID)
	if j.Status != "failed" || !strings.Contains(j.Error, "snapshot file is corrupt") || len(j.Steps) != 4 || j.Steps[3].Name != "Terugzetten op leren-server-01" {
		t.Fatalf("eerste poging: %+v", j)
	}
	for _, s := range servers {
		if u, _ := hosts[s.name].Unit("k3s"); u.Active {
			t.Fatalf("k3s draait nog op %s", s.name)
		}
	}
	if s := c.do("POST", "/api/v1/jobs/"+j.ID+"/retry", nil, &j); s != http.StatusAccepted && s != http.StatusOK {
		t.Fatalf("opnieuw: %d", s)
	}
	j = waitJob(t, c, j.ID)
	if j.Status != "succeeded" || len(j.Steps) != 7 {
		t.Fatalf("terugzetten: %+v", j)
	}
	restorePath := protocol.KubeSnapshotDir + "/" + recent.File
	want := []string{
		"leren-server-01: k3s etcd-snapshot save --name clusterforge-voor-terugzetten",
		"leren-server-02: systemctl stop k3s", "leren-server-03: systemctl stop k3s", "leren-server-01: systemctl stop k3s",
		"leren-server-01: k3s server --cluster-reset --cluster-reset-restore-path=" + restorePath,
		"leren-server-01: k3s server --cluster-reset --cluster-reset-restore-path=" + restorePath,
		"leren-server-01: systemctl start k3s", "leren-server-02: systemctl start k3s", "leren-server-03: systemctl start k3s",
	}
	if !slices.Equal(ran, want) {
		t.Fatalf("commando's:\n%s", strings.Join(ran, "\n"))
	}
	log := strings.Join(slices.Concat(j.Steps[1].Log, j.Steps[5].Log), "\n")
	if !strings.Contains(log, "clusterforge-voor-terugzetten-leren-server-01-") || !strings.Contains(log, "de oude database staat nu in") {
		t.Fatalf("log:\n%s", log)
	}
	for _, s := range servers {
		if u, _ := hosts[s.name].Unit("k3s"); !u.Active {
			t.Fatalf("k3s draait niet op %s", s.name)
		}
	}
	// Op de andere servers staat de oude database opzij, en de snapshots
	// staan er nog.
	for _, s := range servers[1:] {
		old, _ := filepath.Glob(filepath.Join(roots[s.name], protocol.KubeDBDir[1:]+"-voor-terugzetten-*", "etcd", "member"))
		if len(old) != 1 {
			t.Fatalf("%s: oude database %v", s.name, old)
		}
		if _, err := os.Stat(filepath.Join(roots[s.name], protocol.KubeSnapshotDir[1:], files[s.name][0])); err != nil {
			t.Fatalf("%s: snapshot weg: %v", s.name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(roots[servers[0].name], protocol.KubeDBDir[1:], "etcd", "member")); err != nil {
		t.Fatalf("de eerste server raakt zijn database kwijt: %v", err)
	}
	if l := kubeLog("kube.snapshot_restored"); len(l) != 1 || !strings.HasPrefix(l[0].Summary, "Snapshot "+recent.File+" teruggezet op leren-server-01; "+
		"het cluster staat weer zoals op "+recent.TakenAt.Local().Format("2-1-2006 15:04")) {
		t.Fatalf("logboek: %+v", l)
	}
	// Het begin staat er één keer: opnieuw proberen sloeg het stoppen over.
	if l := kubeLog("kube.snapshot_restore_started"); len(l) != 1 || !strings.HasPrefix(l[0].Summary, "Terugzetten van snapshot "+recent.File+" op leren-server-01 gestart") {
		t.Fatalf("logboek begin: %+v", l)
	}

	// Op prod zet ClusterForge niets terug.
	if s := c.do("PATCH", "/api/v1/clusters/"+cl.ID, map[string]any{"environment": "prod"}, nil); s != http.StatusOK {
		t.Fatalf("prod: %d", s)
	}
	v = list()
	if it := find(v, recent.File); it.Restorable || !strings.Contains(it.RestoreNote, "runbook") {
		t.Fatalf("prod: %+v", it)
	}
	if s, er := errOf(c, base+"/restore", map[string]any{"name": recent.Name, "preview": true}); s != http.StatusConflict || !strings.Contains(er.Message, "runbook") {
		t.Fatalf("prod terugzetten: %d %+v", s, er)
	}
}
