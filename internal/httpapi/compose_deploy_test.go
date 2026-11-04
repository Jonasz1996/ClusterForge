package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// TestComposeDeploy: een cluster uit een eigen recept, van samenstellen tot
// de database met gebruiker en de kaart Verbinding.
func TestComposeDeploy(t *testing.T) {
	t.Cleanup(func() { templates.BuiltinRegistry().Remove("eigen-app-e2e") })
	e, c := adminClient(t)
	recipe := `RECIPE app 1.0
FROM debian:13
NEEDS database mariadb
SECRET db_password
APT nginx
DATABASE app
DBUSER app PASSWORD {{ secret.db_password }} GRANT ALL ON app
SET /etc/app/app.ini host={{ db.host }}
ONCE RUN CREATES /var/lib/app-init touch /var/lib/app-init
SERVICE nginx ENABLE START
HEALTH service nginx
`
	if s := c.do("POST", "/api/v1/recipes", map[string]any{"source": recipe}, nil); s != http.StatusCreated {
		t.Fatal(s)
	}
	var comp struct {
		Template string `json:"template"`
		Version  string `json:"version"`
	}
	if s := c.do("POST", "/api/v1/blueprints", map[string]any{
		"name": "app-e2e", "title": "App",
		"roles":    []map[string]any{{"name": "server", "recipe": "app", "recipe_version": "1.0", "count": 3}},
		"vip":      map[string]any{"role": "server"},
		"database": map[string]any{"role": "server", "buffer_pool": "512M"},
	}, &comp); s != http.StatusCreated {
		t.Fatalf("samenstellen: %d", s)
	}

	pve, srv, fp := newPVE(t)
	pve.AddGuest(pvefake.Guest{
		Type: "qemu", VMID: 9001, Name: "debian-13-cf", Node: "pve1", Status: "stopped", Template: true, MaxCPU: 1, MaxMem: 1 << 30,
		Config: map[string]string{
			"scsi0": "ceph:base-9001-disk-0,size=3G", "ide2": "ceph:vm-9001-cloudinit,media=cdrom",
			"boot": "order=scsi0", "agent": "enabled=1", "name": "debian-13-cf",
		},
	})
	f := newFleet(t, map[string]string{"app-01": "10.0.20.41", "app-02": "10.0.20.42", "app-03": "10.0.20.43"})
	g := &galeraHosts{hosts: map[string]*agenttest.Host{}, slug: "app"}
	f.setup = func(name string, h *agenttest.Host) {
		writeFile(t, filepath.Join(h.Root, "etc/os-release"), "ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n", 0o644)
		g.mu.Lock()
		g.hosts[name] = h
		g.mu.Unlock()
		h.SetAfter(g.after(name, h))
	}
	pve.OnFileWrite(f.boot)
	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}
	var res struct {
		Job       job    `json:"job"`
		ClusterID string `json:"cluster_id"`
	}
	if s := c.do("POST", "/api/v1/deployments", map[string]any{
		"template": comp.Template,
		"cluster":  map[string]any{"name": "App", "slug": "app", "environment": "lab"},
		"params":   map[string]any{"vip": "10.0.20.40"},
		"target": map[string]any{
			"proxmox_id": conn.ID, "image_vmid": 9001, "network": "static",
			"first_ip": "10.0.20.41/24", "gateway": "10.0.20.1", "dns": []string{"10.0.20.1"},
		},
		"server_url": e.srv.URL,
	}, &res); s != 202 {
		t.Fatalf("uitrollen: %d", s)
	}
	if j := waitJobLong(t, c, res.Job.ID); j.Status != "succeeded" {
		t.Fatalf("uitrol: %s %s", j.Status, j.Error)
	}
	for i, n := range []string{"app-01", "app-02", "app-03"} {
		h := f.host(n)
		if b, err := os.ReadFile(filepath.Join(h.Root, "etc/app/app.ini")); err != nil || string(b) != "host=10.0.20.40\n" {
			t.Errorf("%s app.ini: %v %q", n, err, b)
		}
		ran := slices.ContainsFunc(h.Calls(), func(c string) bool { return strings.Contains(c, "touch /var/lib/app-init") })
		if ran != (i == 0) {
			t.Errorf("%s: ONCE liep %v", n, ran)
		}
		if _, err := os.Stat(filepath.Join(h.Root, "etc/keepalived/check-cluster.sh")); err != nil {
			t.Errorf("%s: geen keepalived-check", n)
		}
	}

	base := "/api/v1/clusters/" + res.ClusterID + "/components"
	var comps list[componentView]
	eventually(t, "database erbij", func() bool {
		c.do("GET", base, nil, &comps)
		return len(comps.Items) == 1 && comps.Items[0].JobID != nil
	})
	if j := waitJobLong(t, c, *comps.Items[0].JobID); j.Status != "succeeded" {
		t.Fatalf("database: %s %s", j.Status, j.Error)
	}
	e.cmps.WaitRecipes()
	if b, _ := os.ReadFile(filepath.Join(f.host("app-01").Root, "etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf")); !strings.Contains(string(b), "512M") {
		t.Errorf("buffer pool niet uit de samenstelling:\n%s", b)
	}
	var v componentDatabasesView
	c.do("GET", base+"/"+comps.Items[0].ID+"/databases", nil, &v)
	if dbs, _ := v.names(); !strings.Contains(strings.Join(dbs, " "), "app(app)") {
		t.Fatalf("databases: %v", dbs)
	}

	// De kaart Verbinding.
	var cc struct {
		Available bool     `json:"available"`
		Reason    string   `json:"reason"`
		Host      string   `json:"host"`
		Port      int      `json:"port"`
		Databases []string `json:"databases"`
		Users     []struct {
			Name, Database, Secret string
		} `json:"users"`
	}
	if s := c.do("GET", "/api/v1/clusters/"+res.ClusterID+"/connection", nil, &cc); s != 200 || !cc.Available || cc.Host != "10.0.20.40" ||
		cc.Port != 3306 || strings.Join(cc.Databases, ",") != "app" || len(cc.Users) != 1 || cc.Users[0].Secret != "db_password" {
		t.Fatalf("verbinding: %d %+v", s, cc)
	}
	var sec struct{ Secret, Value string }
	if s := c.do("POST", "/api/v1/clusters/"+res.ClusterID+"/connection/reveal", map[string]any{"secret": "db_password"}, nil); s != http.StatusUnauthorized && s != http.StatusForbidden && s != http.StatusBadRequest {
		t.Errorf("zonder wachtwoord: %d", s)
	}
	if s := c.do("POST", "/api/v1/clusters/"+res.ClusterID+"/connection/reveal", map[string]any{"secret": "auth_pass", "password": "een-lang-wachtwoord"}, nil); s != http.StatusNotFound {
		t.Errorf("ander geheim: %d", s)
	}
	if s := c.do("POST", "/api/v1/clusters/"+res.ClusterID+"/connection/reveal", map[string]any{"secret": "db_password", "password": "een-lang-wachtwoord"}, &sec); s != 200 || len(sec.Value) != 32 {
		t.Errorf("tonen: %d %+v", s, sec)
	}

	// Een nieuwe receptversie met een tweede database: opnieuw samenstellen
	// geeft versie 1.0.1, en Wijzigen op het cluster past haar toe.
	recipe2 := strings.Replace(strings.Replace(recipe, "RECIPE app 1.0", "RECIPE app 1.1", 1), "DATABASE app\n", "DATABASE app\nDATABASE app_log\n", 1)
	if s := c.do("POST", "/api/v1/recipes", map[string]any{"source": recipe2}, nil); s != http.StatusCreated {
		t.Fatal(s)
	}
	if s := c.do("POST", "/api/v1/blueprints", map[string]any{
		"name": "app-e2e", "title": "App",
		"roles":    []map[string]any{{"name": "server", "recipe": "app", "recipe_version": "1.1", "count": 3}},
		"vip":      map[string]any{"role": "server"},
		"database": map[string]any{"role": "server", "buffer_pool": "512M"},
	}, &comp); s != http.StatusCreated || comp.Version != "1.0.1" {
		t.Fatalf("opnieuw samenstellen: %d %+v", s, comp)
	}
	cbase := "/api/v1/clusters/" + res.ClusterID
	var sp struct {
		Hash string `json:"hash"`
	}
	if s := c.do("POST", cbase+"/spec/plan", map[string]any{"version": "1.0.1", "params": map[string]any{}}, &sp); s != 200 {
		t.Fatalf("plan: %d", s)
	}
	var ap specApplied
	if s := c.do("POST", cbase+"/spec", map[string]any{"version": "1.0.1", "params": map[string]any{}, "hash": sp.Hash}, &ap); s != http.StatusOK && s != http.StatusAccepted {
		t.Fatalf("toepassen: %d", s)
	}
	if ap.Job != nil {
		if j := waitJobLong(t, c, ap.Job.ID); j.Status != "succeeded" {
			t.Fatalf("toepassen: %s %s", j.Status, j.Error)
		}
	}
	eventually(t, "tweede database", func() bool {
		e.cmps.WaitRecipes()
		c.do("GET", base+"/"+comps.Items[0].ID+"/databases", nil, &v)
		dbs, _ := v.names()
		return strings.Contains(strings.Join(dbs, " "), "app_log")
	})
}
