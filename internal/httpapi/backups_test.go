package httpapi

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type backupItem struct {
	VMID        int    `json:"vmid"`
	Name        string `json:"name"`
	Label       string `json:"label"`
	MaxAgeHours int    `json:"max_age_hours"`
	Watched     bool   `json:"watched"`
	Freshness   string `json:"freshness"`
	Reason      string `json:"reason"`
	Count       int    `json:"count"`
	Node        *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"node"`
	Cluster *struct {
		ID string `json:"id"`
	} `json:"cluster"`
	Latest *struct {
		Volid   string    `json:"volid"`
		Storage string    `json:"storage"`
		Time    time.Time `json:"time"`
		Size    int64     `json:"size"`
		Verify  string    `json:"verify"`
	} `json:"latest"`
}

type backupOverview struct {
	Items       []backupItem `json:"items"`
	Connections []struct {
		ID    string `json:"id"`
		Error string `json:"error"`
		Watch []struct {
			VMID  int    `json:"vmid"`
			Label string `json:"label"`
		} `json:"watch"`
		BackupCount int `json:"backup_count"`
		NotBackedUp []struct {
			VMID int    `json:"vmid"`
			Name string `json:"name"`
			Node *struct {
				Name string `json:"name"`
			} `json:"node"`
		} `json:"not_backed_up"`
	} `json:"connections"`
	Clusters []struct {
		ID        string `json:"id"`
		Freshness string `json:"freshness"`
	} `json:"clusters"`
}

func (o backupOverview) item(t *testing.T, vmid int) backupItem {
	t.Helper()
	i := slices.IndexFunc(o.Items, func(it backupItem) bool { return it.VMID == vmid })
	if i < 0 {
		t.Fatalf("VM %d niet in het overzicht: %+v", vmid, o.Items)
	}
	return o.Items[i]
}

func TestBackups(t *testing.T) {
	e, c := adminClient(t)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	ctx := context.Background()
	pve, srv, fp := newPVE(t)
	now := time.Now().Truncate(time.Second)
	pve.AddStorage(pvefake.Storage{Name: "pbs", Node: "pve1", Shared: true, Content: "backup"})
	pve.AddStorage(pvefake.Storage{Name: "dump2", Node: "pve2", Content: "backup,iso"})
	pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: 101, Time: now.Add(-50 * time.Hour), Size: 1 << 30, Verify: "ok"})
	pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: 101, Time: now.Add(-2 * time.Hour), Size: 2 << 30, Verify: "ok"})
	pve.AddBackup(pvefake.Backup{Storage: "dump2", VMID: 102, Time: now.Add(-40 * time.Hour), Size: 3 << 30})
	pve.SetBackupJobs(101, 102)

	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret",
		"tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}
	var cl struct{ ID string }
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "prod"}, &cl)
	var web01, web02, los pveNode
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": cl.ID, "proxmox": map[string]any{"connection_id": conn.ID, "vmid": 101}}, &web01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web02", "cluster_id": cl.ID, "proxmox": map[string]any{"connection_id": conn.ID, "vmid": 102}}, &web02)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "los"}, &los)

	// Voor de eerste ronde is alles onbekend.
	var ov backupOverview
	c.do("GET", "/api/v1/backups", nil, &ov)
	if it := ov.item(t, 101); it.Freshness != "unknown" || !strings.Contains(it.Reason, "nog niet gelezen") {
		t.Fatalf("voor de eerste ronde: %+v", it)
	}

	sync := func() {
		t.Helper()
		if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != 200 {
			t.Fatalf("sync: %d", s)
		}
		if err := e.backups.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	c.do("GET", "/api/v1/backups", nil, &ov)
	it := ov.item(t, 101)
	if it.Freshness != "ok" || it.Count != 2 || it.Latest == nil || !it.Latest.Time.Equal(now.Add(-2*time.Hour)) ||
		it.Latest.Storage != "pbs" || it.Latest.Verify != "ok" || it.Latest.Size != 2<<30 ||
		!strings.HasPrefix(it.Latest.Volid, "pbs:backup/vm/101/") || it.Node == nil || it.Node.Name != "web01" {
		t.Fatalf("web01: %+v %+v", it, it.Latest)
	}
	if it := ov.item(t, 102); it.Freshness != "stale" || it.Reason != "De nieuwste back-up is ouder dan 30 uur." || it.Latest.Storage != "dump2" {
		t.Fatalf("web02: %+v", it)
	}
	if len(ov.Clusters) != 1 || ov.Clusters[0].ID != cl.ID || ov.Clusters[0].Freshness != "stale" {
		t.Fatalf("cluster: %+v", ov.Clusters)
	}
	cn := ov.Connections[0]
	if cn.Error != "" || cn.BackupCount != 3 || len(cn.Watch) != 0 || len(cn.NotBackedUp) != 1 || cn.NotBackedUp[0].VMID != 200 ||
		cn.NotBackedUp[0].Name != "dns01" || cn.NotBackedUp[0].Node != nil {
		t.Fatalf("koppeling: %+v", cn)
	}
	// De eerste berekening schrijft geen events.
	if a := eventActions(t, e, web02.ID); slices.ContainsFunc(a, func(s string) bool { return strings.HasPrefix(s, "backup.") }) {
		t.Fatalf("events bij de eerste berekening: %v", a)
	}

	// De back-ups van web01 verdwijnen: precies één backup.missing.
	pve.RemoveBackups(101)
	sync()
	sync()
	c.do("GET", "/api/v1/backups", nil, &ov)
	if it := ov.item(t, 101); it.Freshness != "missing" || it.Latest != nil || it.Count != 0 {
		t.Fatalf("zonder back-ups: %+v", it)
	}
	if a := eventActions(t, e, web01.ID); countOf(a, "backup.missing") != 1 {
		t.Fatalf("events web01: %v", a)
	}
	if ov.Clusters[0].Freshness != "missing" {
		t.Fatalf("cluster met een node zonder back-up: %+v", ov.Clusters)
	}

	// Een nieuwe back-up van web02 maakt hem weer vers.
	pve.AddBackup(pvefake.Backup{Storage: "dump2", VMID: 102, Time: now.Add(-time.Hour), Size: 3 << 30})
	sync()
	if a := eventActions(t, e, web02.ID); countOf(a, "backup.fresh") != 1 || countOf(a, "backup.stale") != 0 {
		t.Fatalf("events web02: %v", a)
	}

	// Met de tijd wordt hij weer te oud.
	e.backups.Now = func() time.Time { return now.Add(31 * time.Hour) }
	if err := e.backups.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if a := eventActions(t, e, web02.ID); countOf(a, "backup.stale") != 1 {
		t.Fatalf("na 31 uur: %v", a)
	}
	e.backups.Now = time.Now

	// Beleid per cluster: web02 is nu 31 uur oud, met 48 uur weer vers.
	policy := "/api/v1/clusters/" + cl.ID + "/backup-policy"
	var pol struct {
		MaxAgeHours int  `json:"max_age_hours"`
		Default     bool `json:"default"`
	}
	if s := c.do("GET", policy, nil, &pol); s != 200 || pol.MaxAgeHours != 30 || !pol.Default {
		t.Fatalf("standaardbeleid: %d %+v", s, pol)
	}
	var er apiErr
	if s := c.do("PUT", policy, map[string]any{"max_age_hours": 0}, &er); s != 400 || !strings.Contains(er.Message, "tussen 1 en 720") {
		t.Fatalf("leeftijd 0: %d %+v", s, er)
	}
	kijker := e.client()
	kijker.login("kijker", "een-lang-wachtwoord", "")
	if s := kijker.do("PUT", policy, map[string]any{"max_age_hours": 48}, nil); s != 403 {
		t.Fatalf("kijker: %d", s)
	}
	if s := kijker.do("GET", "/api/v1/backups", nil, nil); s != 200 {
		t.Fatalf("kijker leest: %d", s)
	}
	if s := c.do("PUT", policy, map[string]any{"max_age_hours": 48}, &pol); s != 200 || pol.MaxAgeHours != 48 || pol.Default {
		t.Fatalf("beleid: %d %+v", s, pol)
	}
	e.backups.Now = func() time.Time { return now.Add(31 * time.Hour) }
	if err := e.backups.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/backups", nil, &ov)
	if it := ov.item(t, 102); it.Freshness != "ok" || it.MaxAgeHours != 48 {
		t.Fatalf("met 48 uur: %+v", it)
	}
	if a := eventActions(t, e, web02.ID); countOf(a, "backup.fresh") != 2 {
		t.Fatalf("weer vers met 48 uur: %v", a)
	}
	if a := eventActions(t, e, cl.ID); countOf(a, "backup.policy_updated") != 1 {
		t.Fatalf("events cluster: %v", a)
	}
	e.backups.Now = time.Now

	// ClusterForge zelf (hier VM 200) ook bewaken.
	watch := "/api/v1/proxmox/" + conn.ID + "/backup-watch"
	if s := c.do("PUT", watch, map[string]any{"items": []map[string]any{{"vmid": 7, "label": ""}}}, nil); s != 400 {
		t.Fatalf("VMID 7: %d", s)
	}
	if s := c.do("PUT", watch, map[string]any{"items": []map[string]any{{"vmid": 200, "label": "a"}, {"vmid": 200, "label": "b"}}}, nil); s != 400 {
		t.Fatalf("dubbel: %d", s)
	}
	if s := kijker.do("PUT", watch, map[string]any{"items": []map[string]any{}}, nil); s != 403 {
		t.Fatalf("kijker: %d", s)
	}
	var wc struct {
		Watch []struct {
			VMID  int    `json:"vmid"`
			Label string `json:"label"`
		} `json:"watch"`
	}
	if s := c.do("PUT", watch, map[string]any{"items": []map[string]any{{"vmid": 200, "label": " clusterforge "}}}, &wc); s != 200 ||
		len(wc.Watch) != 1 || wc.Watch[0].Label != "clusterforge" {
		t.Fatalf("ook bewaken: %d %+v", s, wc)
	}
	if err := e.backups.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/backups", nil, &ov)
	if it := ov.item(t, 200); !it.Watched || it.Name != "dns01" || it.Label != "clusterforge" || it.Freshness != "missing" ||
		it.Node != nil || it.MaxAgeHours != 30 {
		t.Fatalf("ook bewaakt: %+v", it)
	}
	if a := eventActions(t, e, conn.ID); countOf(a, "backup.watch_updated") != 1 || countOf(a, "backup.missing") != 0 {
		t.Fatalf("events koppeling: %v", a)
	}
	// Dezelfde lijst nog eens geeft geen nieuw event.
	c.do("PUT", watch, map[string]any{"items": []map[string]any{{"vmid": 200, "label": "clusterforge"}}}, nil)
	if a := eventActions(t, e, conn.ID); countOf(a, "backup.watch_updated") != 1 {
		t.Fatalf("zelfde lijst: %v", a)
	}

	// Per node: alle back-ups, nieuwste eerst.
	var nb struct {
		Item    *backupItem `json:"item"`
		Backups []struct {
			Time time.Time `json:"time"`
		} `json:"backups"`
	}
	c.do("GET", "/api/v1/nodes/"+web02.ID+"/backups", nil, &nb)
	if nb.Item == nil || nb.Item.Freshness != "ok" || len(nb.Backups) != 2 || !nb.Backups[0].Time.After(nb.Backups[1].Time) {
		t.Fatalf("node web02: %+v", nb)
	}
	c.do("GET", "/api/v1/nodes/"+los.ID+"/backups", nil, &nb)
	if nb.Item != nil || len(nb.Backups) != 0 {
		t.Fatalf("node zonder VM: %+v", nb)
	}
	if s := c.do("GET", "/api/v1/nodes/00000000-0000-0000-0000-000000000000/backups", nil, nil); s != 404 {
		t.Fatalf("onbekende node: %d", s)
	}

	// Een host offline: de back-ups op zijn lokale storage blijven staan.
	pve.SetHostOnline("pve2", false)
	sync()
	c.do("GET", "/api/v1/backups", nil, &ov)
	if it := ov.item(t, 102); it.Count != 2 || it.Freshness != "ok" || ov.Connections[0].Error != "" {
		t.Fatalf("host offline: %+v %+v", it, ov.Connections[0])
	}
	pve.SetHostOnline("pve2", true)

	// Een token zonder VM.Backup ziet geen back-ups: één inventory_failed en
	// geen missing per node.
	before := len(eventActions(t, e, web02.ID))
	pve.HideBackups(true)
	sync()
	sync()
	c.do("GET", "/api/v1/backups", nil, &ov)
	if it := ov.item(t, 102); it.Freshness != "unknown" || !strings.Contains(it.Reason, "VM.Backup") || it.Count != 2 {
		t.Fatalf("zonder VM.Backup: %+v", it)
	}
	if a := eventActions(t, e, conn.ID); countOf(a, "backup.inventory_failed") != 1 {
		t.Fatalf("inventory_failed: %v", a)
	}
	if a := eventActions(t, e, web02.ID); len(a) != before {
		t.Fatalf("events op de node bij een leesfout: %v", a[before:])
	}
	pve.HideBackups(false)
	sync()
	if a := eventActions(t, e, conn.ID); countOf(a, "backup.inventory_recovered") != 1 {
		t.Fatalf("inventory_recovered: %v", a)
	}

	// In het logboek.
	p := c.audit("category=backups")
	var sentences []string
	for _, it := range p.Items {
		sentences = append(sentences, it.Summary)
	}
	for _, want := range []string{
		"Geen back-up van web01 in Proxmox",
		"Back-up van web02 weer vers",
		"Back-up van web02 te oud: de nieuwste is ouder dan 30 uur",
		"Back-ups van Proxmox-koppeling Thuislab weer te lezen",
		"Back-upbeleid van cluster Web gewijzigd: maximale leeftijd in uren van 30 naar 48",
		"Lijst ook bewaken van Proxmox-koppeling Thuislab gewijzigd: ook bewaken VM 200 (clusterforge)",
	} {
		if !slices.Contains(sentences, want) {
			t.Errorf("logboek mist %q: %q", want, sentences)
		}
	}
}
