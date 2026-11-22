package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type pveConn struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	APIURL         string  `json:"api_url"`
	TLSFingerprint string  `json:"tls_fingerprint"`
	PveVersion     string  `json:"pve_version"`
	LastSyncAt     *string `json:"last_sync_at"`
	LastError      string  `json:"last_error"`
	Counts         struct {
		Hosts, Vms, Containers int
	} `json:"counts"`
}

type pveGuest struct {
	VMID         int     `json:"vmid"`
	Type         string  `json:"type"`
	Name         string  `json:"name"`
	Host         string  `json:"host"`
	Status       string  `json:"status"`
	Template     bool    `json:"template"`
	NodeHostname *string `json:"node_hostname"`
}

type pveNode struct {
	ID           string `json:"id"`
	Hostname     string `json:"hostname"`
	Status       string `json:"status"`
	StatusReason string `json:"status_reason"`
	Proxmox      *struct {
		ConnectionID string `json:"connection_id"`
		VMID         int    `json:"vmid"`
		Found        bool   `json:"found"`
		Host         string `json:"host"`
		Status       string `json:"status"`
	} `json:"proxmox"`
}

type job struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Status      string  `json:"status"`
	Error       string  `json:"error"`
	NodeID      *string `json:"node_id"`
	RequestedBy *string `json:"requested_by"`
	Steps       []struct {
		Name   string   `json:"name"`
		Status string   `json:"status"`
		Log    []string `json:"log"`
	} `json:"steps"`
}

func newPVE(t *testing.T) (*pvefake.Server, *httptest.Server, string) {
	t.Helper()
	pve := pvefake.New("clusterforge@pve!cf=geheim-secret")
	pve.AddHost(pvefake.Host{Name: "pve1", Online: true, MaxCPU: 8, MaxMem: 32 << 30, Mem: 12 << 30, CPU: 0.12})
	pve.AddHost(pvefake.Host{Name: "pve2", Online: true, MaxCPU: 8, MaxMem: 32 << 30, Mem: 8 << 30, CPU: 0.05})
	pve.AddGuest(pvefake.Guest{Type: "qemu", VMID: 101, Name: "web01", Node: "pve1", Status: "stopped", MaxCPU: 2, MaxMem: 2 << 30})
	pve.AddGuest(pvefake.Guest{Type: "qemu", VMID: 102, Name: "web02", Node: "pve2", Status: "running", MaxCPU: 2, MaxMem: 2 << 30})
	pve.AddGuest(pvefake.Guest{Type: "lxc", VMID: 200, Name: "dns01", Node: "pve2", Status: "running", Tags: "dns;infra"})
	pve.AddGuest(pvefake.Guest{Type: "qemu", VMID: 9000, Name: "debian-13", Node: "pve1", Status: "stopped", Template: true})
	pve.AddStorage(pvefake.Storage{Name: "local", Node: "pve1", Disk: 10 << 30, MaxDisk: 100 << 30})
	pve.AddStorage(pvefake.Storage{Name: "ceph", Node: "pve1", Shared: true, Disk: 1 << 40, MaxDisk: 4 << 40})
	srv := httptest.NewTLSServer(pve.Handler())
	t.Cleanup(srv.Close)
	return pve, srv, proxmox.FormatFingerprint(proxmox.Fingerprint(srv.Certificate()))
}

// waitJob wacht tot een taak klaar is.
func waitJob(t *testing.T, c *client, id string) job {
	t.Helper()
	var j job
	eventually(t, "taak "+id+" klaar", func() bool {
		c.do("GET", "/api/v1/jobs/"+id, nil, &j)
		return j.Status != "queued" && j.Status != "running"
	})
	return j
}

func TestProxmox(t *testing.T) {
	e, c := adminClient(t)
	pve, srv, fp := newPVE(t)

	var probe struct {
		Fingerprint string `json:"fingerprint"`
		Trusted     bool   `json:"trusted"`
	}
	if s := c.do("POST", "/api/v1/proxmox/probe", map[string]any{"api_url": srv.URL}, &probe); s != 200 || probe.Fingerprint != fp || probe.Trusted {
		t.Fatalf("probe: %d %+v", s, probe)
	}

	input := map[string]any{"name": "Thuislab", "api_url": srv.URL + "/api2/json", "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret"}
	var er struct{ Code, Message string }
	if s := c.do("POST", "/api/v1/proxmox", input, &er); s != 400 || !strings.Contains(er.Message, "niet vertrouwd") {
		t.Fatalf("zonder vingerafdruk: %d %+v", s, er)
	}
	input["tls_fingerprint"] = fp
	input["token_secret"] = "fout"
	if s := c.do("POST", "/api/v1/proxmox", input, &er); s != 400 || !strings.Contains(er.Message, "weigert het API-token") {
		t.Fatalf("fout secret: %d %+v", s, er)
	}
	input["token_secret"] = "geheim-secret"
	var raw json.RawMessage
	if s := c.do("POST", "/api/v1/proxmox", input, &raw); s != 201 {
		t.Fatalf("koppelen: %d %s", s, raw)
	}
	if bytes.Contains(raw, []byte("geheim")) {
		t.Fatal("secret in het antwoord")
	}
	var conn pveConn
	_ = json.Unmarshal(raw, &conn)
	if conn.APIURL != srv.URL || conn.PveVersion != "8.4.1" || conn.LastSyncAt == nil || conn.TLSFingerprint != fp ||
		conn.Counts.Hosts != 2 || conn.Counts.Vms != 2 || conn.Counts.Containers != 1 {
		t.Fatalf("verbinding: %+v", conn)
	}
	var enc []byte
	_ = e.pool.QueryRow(context.Background(), "SELECT token_secret_enc FROM proxmox_connections").Scan(&enc)
	if len(enc) == 0 || bytes.Contains(enc, []byte("geheim")) {
		t.Fatal("secret niet versleuteld")
	}
	if s := c.do("POST", "/api/v1/proxmox", input, nil); s != 409 {
		t.Fatalf("dubbele naam: %d", s)
	}

	var res struct {
		Hosts    []struct{ Name string }
		Guests   []pveGuest
		Storages []struct {
			Name   string
			Shared bool
		}
	}
	base := "/api/v1/proxmox/" + conn.ID
	c.do("GET", base+"/resources", nil, &res)
	if len(res.Hosts) != 2 || len(res.Guests) != 4 || len(res.Storages) != 2 || !res.Storages[0].Shared && !res.Storages[1].Shared {
		t.Fatalf("resources: %+v", res)
	}

	// web01 als node opnemen, gekoppeld aan VM 101 die uit staat.
	link := map[string]any{"connection_id": conn.ID, "vmid": 101}
	var web01 pveNode
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "proxmox": link}, &web01); s != 201 {
		t.Fatalf("node: %d", s)
	}
	if p := web01.Proxmox; p == nil || !p.Found || p.Host != "pve1" || p.Status != "stopped" || p.VMID != 101 {
		t.Fatalf("koppeling: %+v", web01.Proxmox)
	}
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01-kopie", "proxmox": link}, nil); s != 409 {
		t.Fatalf("dubbele koppeling: %d", s)
	}
	c.do("GET", base+"/resources", nil, &res)
	if i := slices.IndexFunc(res.Guests, func(g pveGuest) bool { return g.VMID == 101 }); i < 0 || res.Guests[i].NodeHostname == nil {
		t.Fatalf("guest zonder node: %+v", res.Guests)
	}
	e.eval.Kick()
	eventually(t, "node down door uitgeschakelde VM", func() bool {
		c.do("GET", "/api/v1/nodes/"+web01.ID, nil, &web01)
		return web01.Status == "down" && web01.StatusReason == "VM staat uit in Proxmox"
	})

	// Starten als taak.
	var j job
	if s := c.do("POST", base+"/vms/101/actions", map[string]any{"action": "start"}, &j); s != 202 || j.Status != "queued" || j.NodeID == nil || *j.NodeID != web01.ID {
		t.Fatalf("start: %d %+v", s, j)
	}
	j = waitJob(t, c, j.ID)
	if j.Status != "succeeded" || len(j.Steps) != 1 || !slices.Contains(j.Steps[0].Log, "TASK OK") || j.RequestedBy == nil || *j.RequestedBy != "admin" {
		t.Fatalf("start klaar: %+v", j)
	}
	if g, _ := pve.Guest(101); g.Status != "running" {
		t.Fatalf("VM draait niet: %s", g.Status)
	}
	c.do("GET", "/api/v1/nodes/"+web01.ID, nil, &web01)
	if web01.Proxmox.Status != "running" {
		t.Fatalf("na start: %+v", web01.Proxmox)
	}
	eventually(t, "node unknown met draaiende VM", func() bool {
		c.do("GET", "/api/v1/nodes/"+web01.ID, nil, &web01)
		return web01.Status == "unknown"
	})
	if s := c.do("POST", base+"/vms/101/actions", map[string]any{"action": "start"}, &er); s != 409 {
		t.Fatalf("tweede start: %d", s)
	}
	if s := c.do("POST", base+"/vms/9000/actions", map[string]any{"action": "start"}, &er); s != 400 {
		t.Fatalf("template starten: %d", s)
	}
	if s := c.do("POST", base+"/vms/555/actions", map[string]any{"action": "start"}, &er); s != 404 {
		t.Fatalf("onbekende VM: %d", s)
	}

	// Snapshot en migratie.
	c.do("POST", base+"/vms/101/actions", map[string]any{"action": "snapshot", "snapshot_name": "voor-update", "vmstate": true}, &j)
	if j = waitJob(t, c, j.ID); j.Status != "succeeded" || j.Title != "Snapshot voor-update: web01 (VM 101)" {
		t.Fatalf("snapshot: %+v", j)
	}
	var snaps struct {
		Items []struct{ Name string }
	}
	if s := c.do("GET", base+"/vms/101/snapshots", nil, &snaps); s != 200 || len(snaps.Items) != 1 || snaps.Items[0].Name != "voor-update" {
		t.Fatalf("snapshots: %d %+v", s, snaps)
	}
	if s := c.do("POST", base+"/vms/101/actions", map[string]any{"action": "snapshot", "snapshot_name": "1 fout"}, nil); s != 400 {
		t.Fatalf("ongeldige snapshotnaam: %d", s)
	}
	c.do("POST", base+"/vms/101/actions", map[string]any{"action": "migrate", "target": "pve2"}, &j)
	if j = waitJob(t, c, j.ID); j.Status != "succeeded" {
		t.Fatalf("migratie: %+v", j)
	}
	if g, _ := pve.Guest(101); g.Node != "pve2" {
		t.Fatalf("VM staat op %s", g.Node)
	}
	if !slices.Contains(pve.Calls(), "POST /nodes/pve1/qemu/101/migrate") {
		t.Fatalf("calls: %v", pve.Calls())
	}
	if s := c.do("POST", base+"/vms/101/actions", map[string]any{"action": "migrate", "target": "pve2"}, nil); s != 400 {
		t.Fatalf("migreren naar dezelfde host: %d", s)
	}
	if s := c.do("POST", base+"/vms/101/actions", map[string]any{"action": "migrate", "target": "pve9"}, nil); s != 400 {
		t.Fatalf("migreren naar onbekende host: %d", s)
	}

	// Een mislukte Proxmox-taak maakt de taak mislukt, met de reden.
	pve.FailNext("qmshutdown", "VM quit/powerdown failed")
	c.do("POST", base+"/vms/101/actions", map[string]any{"action": "shutdown"}, &j)
	if j = waitJob(t, c, j.ID); j.Status != "failed" || j.Error != "taak in Proxmox mislukt: VM quit/powerdown failed" {
		t.Fatalf("mislukte taak: %+v", j)
	}

	// Annuleren stopt de Proxmox-taak.
	pve.SetTaskDuration(time.Hour)
	c.do("POST", base+"/vms/102/actions", map[string]any{"action": "stop"}, &j)
	eventually(t, "taak loopt", func() bool {
		c.do("GET", "/api/v1/jobs/"+j.ID, nil, &j)
		return j.Status == "running" && len(j.Steps) == 1
	})
	if s := c.do("POST", "/api/v1/jobs/"+j.ID+"/cancel", nil, nil); s != 202 {
		t.Fatalf("annuleren: %d", s)
	}
	if j = waitJob(t, c, j.ID); j.Status != "canceled" {
		t.Fatalf("na annuleren: %+v", j)
	}
	if !slices.ContainsFunc(pve.Calls(), func(s string) bool { return strings.HasPrefix(s, "DELETE /nodes/pve2/tasks/UPID:pve2:") }) {
		t.Fatalf("Proxmox-taak niet gestopt: %v", pve.Calls())
	}
	if s := c.do("POST", "/api/v1/jobs/"+j.ID+"/cancel", nil, nil); s != 409 {
		t.Fatalf("tweede keer annuleren: %d", s)
	}
	pve.SetTaskDuration(0)

	var jobs struct{ Items []job }
	c.do("GET", "/api/v1/jobs?node_id="+web01.ID, nil, &jobs)
	if len(jobs.Items) != 4 || jobs.Items[0].Status != "failed" {
		t.Fatalf("taken van web01: %+v", jobs.Items)
	}

	// Een wijziging buiten ClusterForge wordt een event bij de node.
	pve.SetGuestStatus(101, "stopped", "")
	var synced pveConn
	if s := c.do("POST", base+"/sync", nil, &synced); s != 200 || synced.LastError != "" {
		t.Fatalf("sync: %d %+v", s, synced)
	}
	if got := eventActions(t, e, web01.ID); !slices.Contains(got, "vm.status_changed") {
		t.Fatalf("events: %v", got)
	}

	// Een sync die mislukt, komt in last_error en één keer in de eventlog.
	pve.SetToken("iets-anders")
	c.do("POST", base+"/sync", nil, &synced)
	c.do("POST", base+"/sync", nil, &synced)
	if !strings.Contains(synced.LastError, "401") {
		t.Fatalf("last_error: %q", synced.LastError)
	}
	pve.SetToken("clusterforge@pve!cf=geheim-secret")
	c.do("POST", base+"/sync", nil, &synced)
	if synced.LastError != "" {
		t.Fatalf("na herstel: %q", synced.LastError)
	}
	got := eventActions(t, e, conn.ID)
	if n := countOf(got, "proxmox.sync_failed"); n != 1 || countOf(got, "proxmox.sync_recovered") != 1 || countOf(got, "proxmox.sync_requested") != 4 {
		t.Fatalf("sync-events: %v", got)
	}

	// Wijzigen: een nieuwe naam mag zonder test, een fout secret niet.
	if s := c.do("PATCH", base, map[string]any{"name": "Lab"}, &synced); s != 200 || synced.Name != "Lab" {
		t.Fatalf("hernoemen: %d %+v", s, synced)
	}
	if s := c.do("PATCH", base, map[string]any{"token_secret": "fout"}, &er); s != 400 {
		t.Fatalf("fout secret: %d", s)
	}
	if s := c.do("POST", base+"/sync", nil, &synced); s != 200 || synced.LastError != "" {
		t.Fatalf("oude secret kwijt: %+v", synced)
	}

	// Een viewer mag kijken maar niets doen.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	if s := v.do("GET", base+"/resources", nil, nil); s != 200 {
		t.Fatalf("viewer resources: %d", s)
	}
	if s := v.do("POST", base+"/vms/101/actions", map[string]any{"action": "start"}, nil); s != 403 {
		t.Fatalf("viewer actie: %d", s)
	}

	// Verwijderen laat de node bestaan, zonder koppeling.
	if s := c.do("DELETE", base, nil, nil); s != 204 {
		t.Fatalf("verwijderen: %d", s)
	}
	c.do("GET", "/api/v1/nodes/"+web01.ID, nil, &web01)
	if web01.Proxmox != nil {
		t.Fatalf("koppeling bleef: %+v", web01.Proxmox)
	}
}

func eventActions(t *testing.T, e *testEnv, subject string) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), "SELECT action FROM events WHERE subject_id = $1 ORDER BY id", subject)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		out = append(out, a)
	}
	return out
}

func countOf(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}

func TestProxmoxWithoutMasterKey(t *testing.T) {
	e, c := adminClient(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.api.pve = proxmox.NewService(e.pool, events.NewWriter(store.New(e.pool), log), log, nil, nil)
	var l struct{ Enabled bool }
	if s := c.do("GET", "/api/v1/proxmox", nil, &l); s != 200 || l.Enabled {
		t.Fatalf("lijst: %d %+v", s, l)
	}
	var er struct{ Code string }
	input := map[string]any{"name": "Lab", "api_url": "pve1.lan", "token_id": "a@pve!b", "token_secret": "x"}
	if s := c.do("POST", "/api/v1/proxmox", input, &er); s != http.StatusServiceUnavailable || er.Code != "no_master_key" {
		t.Fatalf("koppelen: %d %+v", s, er)
	}
}
