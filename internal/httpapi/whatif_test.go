package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/topo"
)

type whatIfView struct {
	Components []struct {
		Key  string `json:"key"`
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"components"`
	Guests []struct {
		VMID        int     `json:"vmid"`
		Name        string  `json:"name"`
		Host        string  `json:"host"`
		NodeID      *string `json:"node_id"`
		Outcome     string  `json:"outcome"`
		Reason      string  `json:"reason"`
		RestartHost string  `json:"restart_host"`
		Unknown     bool    `json:"unknown"`
	} `json:"guests"`
	Clusters []struct {
		Name   string `json:"name"`
		Before struct {
			Status string `json:"status"`
		} `json:"before"`
		During struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"during"`
		AfterHA   *struct{ Status string } `json:"after_ha"`
		NodesDown []string                 `json:"nodes_down"`
	} `json:"clusters"`
	Services []any `json:"services"`
	Self     *struct {
		Name        string `json:"name"`
		Outcome     string `json:"outcome"`
		RestartHost string `json:"restart_host"`
	} `json:"self"`
	Notes []string `json:"notes"`
}

type selfLocationView struct {
	Source string `json:"source"`
	Guest  *struct {
		ConnectionID string `json:"connection_id"`
		VMID         int    `json:"vmid"`
		Name         string `json:"name"`
		Host         string `json:"host"`
		Type         string `json:"type"`
	} `json:"guest"`
	VMID      *int   `json:"vmid"`
	MatchedOn string `json:"matched_on"`
	Note      string `json:"note"`
	Guests    []struct {
		VMID int `json:"vmid"`
	} `json:"guests"`
}

// TestWhatIf: web01 en web02 staan allebei op pve1 op lokale storage.
// ClusterForge zelf is container 300 op pve2, onder HA op ceph, en wordt
// gevonden aan het IP-adres dat Proxmox van zijn interface geeft.
func TestWhatIf(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	e.topo.Own = func(context.Context, []string) topo.Own {
		return topo.Own{MACs: []string{"02:42:ac:11:00:02"}, IPs: []string{"172.17.0.2", "192.168.1.50"}}
	}

	pve, srv, fp := newPVE(t)
	now := time.Now().Truncate(time.Second)
	pve.SetGuestStatus(101, "running", "pve1")
	pve.SetGuestStatus(102, "running", "pve1")
	pve.AddStorage(pvefake.Storage{Name: "local", Node: "pve2", Disk: 10 << 30, MaxDisk: 100 << 30})
	pve.AddStorage(pvefake.Storage{Name: "ceph", Node: "pve2", Shared: true, Disk: 1 << 40, MaxDisk: 4 << 40})
	pve.AddStorage(pvefake.Storage{Name: "pbs", Node: "pve2", Shared: true, Content: "backup"})
	for _, vmid := range []int{101, 102} {
		pve.SetGuestConfig(vmid, "scsi0", "local:vm-"+strconv.Itoa(vmid)+"-disk-0,size=32G")
		pve.SetGuestConfig(vmid, "net0", "virtio=BC:24:11:00:01:0"+strconv.Itoa(vmid-100)+",bridge=vmbr0")
	}
	pve.AddGuest(pvefake.Guest{Type: "qemu", VMID: 400, Name: "oud-ding", Node: "pve1", Status: "running", MaxMem: 1 << 30,
		Config: map[string]string{"scsi0": "local:vm-400-disk-0,size=8G", "net0": "virtio=BC:24:11:00:04:00,bridge=vmbr0"}})
	pve.AddGuest(pvefake.Guest{Type: "lxc", VMID: 300, Name: "clusterforge", Node: "pve2", Status: "running", MaxMem: 2 << 30, HAState: "started",
		Config: map[string]string{"rootfs": "ceph:vm-300-disk-0,size=16G", "net0": "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:00:03:00,ip=dhcp,type=veth"},
		Inet:   map[string]string{"eth0": "192.168.1.50/24"}})
	pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: 300, Type: "lxc", Time: now.Add(-40 * time.Hour), Size: 1 << 30})
	pve.SetBackupJobs(300)

	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != http.StatusCreated {
		t.Fatalf("koppelen: %d", s)
	}
	var cl struct{ ID string }
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "prod"}, &cl)
	for _, n := range []struct {
		name string
		vmid int
	}{{"web01", 101}, {"web02", 102}} {
		if s := c.do("POST", "/api/v1/nodes", map[string]any{
			"hostname": n.name, "cluster_id": cl.ID, "proxmox": map[string]any{"connection_id": conn.ID, "vmid": n.vmid},
		}, nil); s != http.StatusCreated {
			t.Fatalf("node %s: %d", n.name, s)
		}
	}
	if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != http.StatusOK {
		t.Fatalf("sync: %d", s)
	}

	// Zonder gelezen configuratie weet ClusterForge nog niet waar hij
	// draait.
	var loc selfLocationView
	if s := viewer.do("GET", "/api/v1/self-location", nil, &loc); s != http.StatusOK || loc.Source != "none" ||
		!strings.Contains(loc.Note, "nog niet gelezen") {
		t.Fatalf("voor het lezen: %d %+v", s, loc)
	}

	// De configuraties lezen; een tweede keer binnen tien minuten leest
	// niets opnieuw.
	connID := uuid.MustParse(conn.ID)
	before := len(pve.Calls())
	if err := e.pve.ReadConfigs(ctx, connID); err != nil {
		t.Fatal(err)
	}
	reads := 0
	for _, call := range pve.Calls()[before:] {
		if strings.HasPrefix(call, "GET ") && strings.HasSuffix(call, "/config") {
			reads++
		}
		if strings.Contains(call, "9000") {
			t.Fatalf("template gelezen: %s", call)
		}
	}
	if reads != 5 {
		t.Fatalf("%d configuraties gelezen: %v", reads, pve.Calls()[before:])
	}
	before = len(pve.Calls())
	if err := e.pve.ReadConfigs(ctx, connID); err != nil || len(pve.Calls()) != before {
		t.Fatalf("tweede keer: %v %v", err, pve.Calls()[before:])
	}
	var disks, nets string
	var addrs []string
	if err := e.pool.QueryRow(ctx, `SELECT disks::text, nets::text, addresses FROM proxmox_guest_config WHERE vmid = 300`).Scan(&disks, &nets, &addrs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(disks, `"storage": "ceph"`) || !strings.Contains(nets, `"mac": "bc:24:11:00:03:00"`) || !slices.Equal(addrs, []string{"192.168.1.50"}) {
		t.Fatalf("configuratie van 300: %s %s %v", disks, nets, addrs)
	}

	loc = selfLocationView{}
	viewer.do("GET", "/api/v1/self-location", nil, &loc)
	if loc.Source != "auto" || loc.Guest == nil || loc.Guest.VMID != 300 || loc.Guest.Host != "pve2" || loc.Guest.Type != "lxc" ||
		loc.MatchedOn != "IP-adres 192.168.1.50" || len(loc.Guests) != 5 {
		t.Fatalf("gevonden: %+v", loc)
	}

	// De onderdelen.
	var comps whatIfView
	if s := viewer.do("GET", "/api/v1/risk/components", nil, &comps); s != http.StatusOK {
		t.Fatalf("onderdelen: %d", s)
	}
	keys := map[string]string{}
	for _, k := range comps.Components {
		keys[k.Kind+" "+k.Name] = k.Key
	}
	for _, want := range []string{"host pve1", "host pve2", "storage local op pve1", "storage ceph (gedeeld)", "bridge vmbr0 op pve1",
		"bridge vmbr0 op pve2", "cluster Web", "node web01", "node web02"} {
		if keys[want] == "" {
			t.Fatalf("onderdeel %q ontbreekt: %v", want, keys)
		}
	}

	// Wat als pve1 uitvalt: ook een viewer mag rekenen.
	var r whatIfView
	if s := viewer.do("POST", "/api/v1/risk/what-if", map[string]any{"components": []string{keys["host pve1"]}}, &r); s != http.StatusOK {
		t.Fatalf("wat als pve1: %d", s)
	}
	var got []string
	for _, g := range r.Guests {
		got = append(got, strconv.Itoa(g.VMID)+" "+g.Outcome+": "+g.Reason)
		if (g.VMID == 400) != g.Unknown {
			t.Fatalf("onbekend: %+v", g)
		}
	}
	if strings.Join(got, "\n") != "101 down: draait op pve1\n102 down: draait op pve1\n400 down: draait op pve1" {
		t.Fatalf("gasten:\n%s", strings.Join(got, "\n"))
	}
	if len(r.Clusters) != 1 || r.Clusters[0].Name != "Web" || r.Clusters[0].During.Status != "down" ||
		r.Clusters[0].During.Reason != "alle nodes zijn down" || !slices.Equal(r.Clusters[0].NodesDown, []string{"web01", "web02"}) || r.Self != nil {
		t.Fatalf("pve1: %+v", r)
	}
	// Wat als pve2 uitvalt: HA herstart ClusterForge op pve1.
	r = whatIfView{}
	c.do("POST", "/api/v1/risk/what-if", map[string]any{"components": []string{keys["host pve2"]}}, &r)
	if r.Self == nil || r.Self.Outcome != "restarted" || r.Self.RestartHost != "pve1" || r.Self.Name != "clusterforge (300)" {
		t.Fatalf("pve2: %+v", r.Self)
	}
	for _, bad := range []map[string]any{{"components": []string{}}, {"components": []string{"host:onbekend"}}} {
		if s := c.do("POST", "/api/v1/risk/what-if", bad, nil); s != http.StatusBadRequest {
			t.Fatalf("%v: %d", bad, s)
		}
	}

	// De SPOF-lijst: pve1, zijn lokale storage en zijn bridge maken Web
	// down.
	var spofs struct {
		Spofs []struct {
			Component struct{ Kind, Name string }
			Clusters  []struct{ Name string } `json:"clusters"`
			Down      int                     `json:"down"`
		} `json:"spofs"`
	}
	if s := viewer.do("GET", "/api/v1/risk/spof", nil, &spofs); s != http.StatusOK {
		t.Fatalf("spof: %d", s)
	}
	got = nil
	for _, sp := range spofs.Spofs {
		got = append(got, sp.Component.Kind+" "+sp.Component.Name+" → "+sp.Clusters[0].Name)
	}
	if strings.Join(got, "\n") != "host pve1 → Web\nstorage local op pve1 → Web\nbridge vmbr0 op pve1 → Web" {
		t.Fatalf("SPOF's:\n%s", strings.Join(got, "\n"))
	}

	// De score: same_host staat er al, dus geen dubbele SPOF-bevinding; de
	// back-up van ClusterForge zelf is 40 uur oud.
	e.risk.Warmup = 0
	if err := e.risk.Compute(ctx); err != nil {
		t.Fatal(err)
	}
	var v clusterRiskView
	c.do("GET", "/api/v1/clusters/"+cl.ID+"/risk", nil, &v)
	if len(v.findings("redundancy.same_host")) != 1 || len(v.findings("redundancy.spof")) != 0 {
		t.Fatalf("redundantie: %+v", v.card(t, "redundancy"))
	}
	var ov riskOverview
	c.do("GET", "/api/v1/risk", nil, &ov)
	i := slices.IndexFunc(ov.Platform.Findings, func(f riskFinding) bool { return f.Check == "platform.self_backup" })
	if i < 0 || ov.Platform.Findings[i].Severity != "medium" ||
		ov.Platform.Findings[i].Text != "De nieuwste back-up van de VM van ClusterForge, clusterforge (300), is 40 u oud; het beleid is 30 uur" {
		t.Fatalf("platform: %+v", ov.Platform)
	}

	// Zelf invullen: alleen een beheerder, geen template, en het logboek
	// zegt het.
	if s := viewer.do("PUT", "/api/v1/self-location", map[string]any{"connection_id": conn.ID, "vmid": 102}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer: %d", s)
	}
	if s := c.do("PUT", "/api/v1/self-location", map[string]any{"connection_id": conn.ID, "vmid": 9000}, nil); s != http.StatusNotFound {
		t.Fatalf("template: %d", s)
	}
	if s := c.do("PUT", "/api/v1/self-location", map[string]any{"connection_id": conn.ID, "vmid": 102}, nil); s != http.StatusNoContent {
		t.Fatalf("invullen: %d", s)
	}
	loc = selfLocationView{}
	c.do("GET", "/api/v1/self-location", nil, &loc)
	if loc.Source != "manual" || loc.Guest == nil || loc.Guest.VMID != 102 || loc.Guest.Name != "web02" {
		t.Fatalf("ingevuld: %+v", loc)
	}
	r = whatIfView{}
	c.do("POST", "/api/v1/risk/what-if", map[string]any{"components": []string{keys["host pve1"]}}, &r)
	if r.Self == nil || r.Self.Outcome != "down" {
		t.Fatalf("ingevuld, pve1: %+v", r.Self)
	}
	var summary string
	if err := e.pool.QueryRow(ctx, `SELECT payload->>'guest' FROM events WHERE action = 'proxmox.self_location_set'`).Scan(&summary); err != nil || summary != "web02" {
		t.Fatalf("event: %q %v", summary, err)
	}
	if s := viewer.do("DELETE", "/api/v1/self-location", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer weghalen: %d", s)
	}
	if s := c.do("DELETE", "/api/v1/self-location", nil, nil); s != http.StatusNoContent {
		t.Fatalf("weghalen: %d", s)
	}
	loc = selfLocationView{}
	c.do("GET", "/api/v1/self-location", nil, &loc)
	if loc.Source != "auto" || loc.Guest.VMID != 300 {
		t.Fatalf("weer zelf gezocht: %+v", loc)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE action = 'proxmox.self_location_cleared'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("event weghalen: %d %v", n, err)
	}

	// Een VM die verdwijnt, verliest zijn configuratie.
	pve.RemoveGuest(400)
	c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil)
	if err := e.pve.ReadConfigs(ctx, connID); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM proxmox_guest_config WHERE vmid = 400`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("configuratie van 400: %d %v", n, err)
	}
}
