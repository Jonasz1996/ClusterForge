package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type verifyRun struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Result    *string `json:"result"`
	Summary   string  `json:"summary"`
	JobID     *string `json:"job_id"`
	JobStatus *string `json:"job_status"`
	Hostname  string  `json:"hostname"`
	Checks    []struct {
		Name    string `json:"name"`
		OK      bool   `json:"ok"`
		Warning bool   `json:"warning"`
		Detail  string `json:"detail"`
		Code    string `json:"code"`
	} `json:"checks"`
	Timeline []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"timeline"`
	Measurements *struct{} `json:"measurements"`
	Backup       *struct {
		Definition struct {
			Volid  string `json:"volid"`
			Chosen bool   `json:"chosen"`
		} `json:"definition"`
		Measurements struct {
			RestoreSeconds   *float64 `json:"restore_seconds"`
			BootSeconds      *float64 `json:"boot_seconds"`
			TotalSeconds     *float64 `json:"total_seconds"`
			SandboxVMID      int      `json:"sandbox_vmid"`
			Host             string   `json:"host"`
			Storage          string   `json:"storage"`
			Hostname         string   `json:"hostname"`
			OS               string   `json:"os"`
			Filesystems      int      `json:"filesystems"`
			AgentVersion     string   `json:"agent_version"`
			ServicesExpected int      `json:"services_expected"`
			ServicesActive   int      `json:"services_active"`
			Databases        []string `json:"databases"`
		} `json:"measurements"`
		Sandbox *sandboxView `json:"sandbox"`
	} `json:"backup"`
}

type sandboxView struct {
	ID      string `json:"id"`
	VMID    int    `json:"vmid"`
	State   string `json:"state"`
	Running bool   `json:"running"`
	Error   string `json:"error"`
	Source  *struct {
		Name string `json:"name"`
	} `json:"source"`
}

func (r verifyRun) checkNames() []string {
	var out []string
	for _, c := range r.Checks {
		out = append(out, c.Name)
	}
	return out
}

func (r verifyRun) check(name string) (ok, warning bool, detail string) {
	for _, c := range r.Checks {
		if c.Name == name {
			return c.OK, c.Warning, c.Detail
		}
	}
	return false, false, "<geen controle " + name + ">"
}

func TestBackupVerify(t *testing.T) {
	e, c := adminClient(t)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	viewer.login("kijker", "een-lang-wachtwoord", "")
	ctx := context.Background()
	pve, srv, fp := newPVE(t)
	pve.AddPool("cf-sandbox")
	pve.OnExec((&sandboxAgent{host: agenttest.NewHost(t.TempDir(), "192.0.2.50")}).exec)
	now := time.Now().Truncate(time.Second)
	pve.AddStorage(pvefake.Storage{Name: "pbs", Node: "pve1", Shared: true, Content: "backup"})
	pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: 101, Time: now.Add(-6 * time.Hour), Size: 4 << 30, Verify: "ok"})
	pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: 200, Type: "lxc", Time: now.Add(-6 * time.Hour), Size: 1 << 30})

	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret",
		"tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "nginx", "environment": "prod"}, &cl)
	var web01, dns01, los pveNode
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": cl.ID, "proxmox": map[string]any{"connection_id": conn.ID, "vmid": 101}}, &web01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "dns01", "proxmox": map[string]any{"connection_id": conn.ID, "vmid": 200}}, &dns01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "los"}, &los)
	var agentCfg agent.Config
	startAgentWith(t, e, c, web01.ID, strings.Repeat("7", 32), &addrs{}, func(ag *agent.Agent, _ string) { agentCfg = *ag.Config })
	eventually(t, "agent van web01 verbonden", func() bool {
		n, _ := e.bus.Connections(nkeyOf(t, e, web01.ID))
		return n == 1
	})
	if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != 200 {
		t.Fatalf("sync: %d", s)
	}

	start := func(nodeID string, body any) (verifyRun, int, apiErr) {
		t.Helper()
		var raw struct {
			verifyRun
			apiErr
		}
		s := c.do("POST", "/api/v1/nodes/"+nodeID+"/backups/verify", body, &raw)
		return raw.verifyRun, s, raw.apiErr
	}
	finished := func(id string) verifyRun {
		t.Helper()
		var r verifyRun
		eventually(t, "controle "+id+" klaar", func() bool {
			c.do("GET", "/api/v1/test-runs/"+id, nil, &r)
			return r.Result != nil && r.JobStatus != nil && *r.JobStatus != "queued" && *r.JobStatus != "running"
		})
		return r
	}
	inPool := func() []int {
		var out []int
		for _, vmid := range pve.Guests() {
			if g, _ := pve.Guest(vmid); g.Pool == "cf-sandbox" {
				out = append(out, vmid)
			}
		}
		return out
	}
	overview := func() (out struct {
		Items []struct {
			VMID             int `json:"vmid"`
			LastVerification *struct {
				RunID           string   `json:"run_id"`
				Result          string   `json:"result"`
				RecoverySeconds *float64 `json:"recovery_seconds"`
			} `json:"last_verification"`
		} `json:"items"`
		Sandboxes []sandboxView `json:"sandboxes"`
	}) {
		t.Helper()
		c.do("GET", "/api/v1/backups", nil, &out)
		return out
	}

	// Weigeringen voor de start.
	if _, s, er := start(los.ID, nil); s != 409 || !strings.Contains(er.Message, "niet aan een VM") {
		t.Fatalf("node zonder VM: %d %+v", s, er)
	}
	if _, s, er := start(dns01.ID, nil); s != 409 || !strings.Contains(er.Message, "LXC") {
		t.Fatalf("container: %d %+v", s, er)
	}
	if _, s, _ := start(web01.ID, map[string]any{"volid": "pbs:backup/ct/200/x"}); s != 400 {
		t.Fatalf("back-up van een andere VM: %d", s)
	}
	if s := viewer.do("POST", "/api/v1/nodes/"+web01.ID+"/backups/verify", nil, nil); s != 403 {
		t.Fatalf("viewer: %d", s)
	}

	t.Run("geslaagd", func(t *testing.T) {
		before := len(pve.Calls())
		run, s, er := start(web01.ID, nil)
		if s != 202 || run.Kind != "backup.verify" || run.Result != nil || run.Backup == nil || run.Backup.Definition.Chosen ||
			!strings.HasPrefix(run.Backup.Definition.Volid, "pbs:backup/vm/101/") || run.Measurements != nil {
			t.Fatalf("starten: %d %+v %+v", s, run, er)
		}
		r := finished(run.ID)
		if *r.Result != "pass" || !strings.Contains(r.Summary, "web01, back-up van ") || !strings.Contains(r.Summary, ": geslaagd. Terugzetten") ||
			!strings.Contains(r.Summary, "Hostname web01, Debian GNU/Linux 13 (trixie), 1 bestandssysteem.") ||
			!strings.Contains(r.Summary, " verwijderd om ") {
			t.Fatalf("uitkomst: %s %q %+v", *r.Result, r.Summary, r.Checks)
		}
		want := []string{"Terugzetten", "Isolatie", "Opstarten", "Hostname", "Besturingssysteem", "Bestandssystemen", "Gefaalde units", "Agentverbinding", "Opruimen"}
		if !slices.Equal(r.checkNames(), want) {
			t.Fatalf("controles: %v", r.checkNames())
		}
		for _, ch := range r.Checks {
			if !ch.OK {
				t.Errorf("controle %s: %+v", ch.Name, ch)
			}
		}
		m := r.Backup.Measurements
		vmid := m.SandboxVMID
		if m.RestoreSeconds == nil || m.BootSeconds == nil || m.TotalSeconds == nil || vmid < 100 || vmid == 101 || m.Host != "pve1" ||
			m.Storage != "ceph" || m.Hostname != "web01" || m.Filesystems != 1 {
			t.Fatalf("metingen: %+v", m)
		}
		if sb := r.Backup.Sandbox; sb == nil || sb.VMID != vmid || sb.State != "destroyed" || sb.Running || sb.Source == nil || sb.Source.Name != "web01" {
			t.Fatalf("sandbox: %+v", sb)
		}
		if len(inPool()) != 0 || slices.Contains(pve.Guests(), vmid) {
			t.Fatalf("sandbox nog in Proxmox: %v", pve.Guests())
		}
		// Isoleren, teruglezen en pas dan starten; verwijderen alleen met purge.
		calls := pve.Calls()[before:]
		path := fmt.Sprintf("/nodes/pve1/qemu/%d", vmid)
		idx := func(call string) int {
			t.Helper()
			i := slices.Index(calls, call)
			if i < 0 {
				t.Fatalf("%s niet aangeroepen: %v", call, calls)
			}
			return i
		}
		restore, set := idx("POST /nodes/pve1/qemu"), idx("POST "+path+"/config")
		readBack := slices.Index(calls[set:], "GET "+path+"/config") + set
		started, destroyed := idx("POST "+path+"/status/start"), idx("DELETE "+path+"?purge=1")
		if restore >= set || readBack <= set || started <= readBack || destroyed <= started {
			t.Fatalf("volgorde: %v", calls)
		}
		for _, call := range calls {
			if strings.Contains(call, "/qemu/101/") {
				t.Fatalf("de bron-VM aangeraakt: %s", call)
			}
		}
		var j job
		c.do("GET", "/api/v1/jobs/"+*r.JobID, nil, &j)
		var steps []string
		for _, st := range j.Steps {
			steps = append(steps, st.Name+":"+st.Status)
		}
		if j.Status != "succeeded" || strings.Join(steps, " ") !=
			"Kiezen:succeeded Terugzetten:succeeded Isoleren:succeeded Starten:succeeded Controleren:succeeded Opruimen:succeeded" {
			t.Fatalf("taak: %s %v", j.Status, steps)
		}
		if a := eventActions(t, e, run.ID); !slices.Equal(a, []string{"backup.sandbox_created", "backup.sandbox_destroyed", "backup.verify_finished"}) {
			t.Fatalf("events: %v", a)
		}
		ov := overview()
		i := slices.IndexFunc(ov.Items, func(it struct {
			VMID             int `json:"vmid"`
			LastVerification *struct {
				RunID           string   `json:"run_id"`
				Result          string   `json:"result"`
				RecoverySeconds *float64 `json:"recovery_seconds"`
			} `json:"last_verification"`
		}) bool {
			return it.VMID == 101
		})
		if lv := ov.Items[i].LastVerification; lv == nil || lv.RunID != run.ID || lv.Result != "pass" || lv.RecoverySeconds == nil || len(ov.Sandboxes) != 0 {
			t.Fatalf("overzicht: %+v %+v", lv, ov.Sandboxes)
		}
		var list struct {
			Items []verifyRun `json:"items"`
		}
		c.do("GET", "/api/v1/test-runs?kind=backup.verify&node_id="+web01.ID, nil, &list)
		if len(list.Items) != 1 || list.Items[0].ID != run.ID || list.Items[0].Backup == nil {
			t.Fatalf("lijst: %+v", list.Items)
		}
		c.do("GET", "/api/v1/test-runs?kind=failover.test", nil, &list)
		if len(list.Items) != 0 {
			t.Fatalf("filter op soort: %+v", list.Items)
		}
	})

	t.Run("zonder guest agent een waarschuwing", func(t *testing.T) {
		b := pvefake.Backup{Storage: "pbs", VMID: 101, Time: now.Add(-30 * time.Hour), Size: 4 << 30, Config: map[string]string{
			"name": "web01", "cores": "2", "memory": "2048", "ostype": "l26", "scsi0": "local-lvm:vm-101-disk-0,size=20G",
			"net0": "virtio=BC:24:11:00:00:01,bridge=vmbr0", "boot": "order=scsi0",
		}}
		pve.AddBackup(b)
		c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil)
		volid := "pbs:backup/vm/101/" + b.Time.UTC().Format("2006-01-02T15:04:05Z")
		run, s, er := start(web01.ID, map[string]any{"volid": volid})
		if s != 202 || !run.Backup.Definition.Chosen || run.Backup.Definition.Volid != volid {
			t.Fatalf("starten: %d %+v", s, er)
		}
		r := finished(run.ID)
		if ok, warning, _ := r.check("Guest agent"); *r.Result != "warning" || ok || !warning ||
			!strings.Contains(r.Summary, "geslaagd met waarschuwing: de VM heeft geen guest agent") {
			t.Fatalf("uitkomst: %s %q %v", *r.Result, r.Summary, r.checkNames())
		}
		if r.Backup.Measurements.BootSeconds != nil || len(inPool()) != 0 {
			t.Fatalf("opstarttijd zonder agent, of sandbox over: %+v %v", r.Backup.Measurements, inPool())
		}
	})

	t.Run("niet te isoleren of geen ruimte", func(t *testing.T) {
		for i, tc := range []struct {
			name, key, value, want string
		}{
			{"virtiofs", "virtiofs0", "share,cache=auto", "niet te isoleren: virtiofs0"},
			{"geheugen", "memory", "65536", "host pve1 heeft te weinig vrij geheugen"},
			{"schijf", "scsi1", "local-lvm:vm-101-disk-1,size=3500G", "storage ceph zou boven 85 % komen"},
		} {
			name := tc.name
			cfg := map[string]string{"name": "web01", "agent": "1", "scsi0": "local-lvm:vm-101-disk-0,size=20G", "net0": "virtio=BC:24:11:00:00:01,bridge=vmbr0"}
			cfg[tc.key] = tc.value
			b := pvefake.Backup{Storage: "pbs", VMID: 101, Time: now.Add(-time.Duration(40+i) * time.Hour), Config: cfg}
			pve.AddBackup(b)
			c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil)
			before := len(pve.Calls())
			run, s, _ := start(web01.ID, map[string]any{"volid": "pbs:backup/vm/101/" + b.Time.UTC().Format("2006-01-02T15:04:05Z")})
			if s != 202 {
				t.Fatalf("%s: %d", name, s)
			}
			r := finished(run.ID)
			if *r.Result != "skipped" || !strings.Contains(r.Summary, tc.want) {
				t.Errorf("%s: %s %q", name, *r.Result, r.Summary)
			}
			if slices.Contains(pve.Calls()[before:], "POST /nodes/pve1/qemu") {
				t.Errorf("%s: toch teruggezet", name)
			}
		}
	})

	t.Run("weigeringen tijdens een controle", func(t *testing.T) {
		// De guest agent antwoordt pas na de weigeringen hieronder; die
		// moeten binnen BootTimeout (3 s) klaar zijn.
		pve.SetAgentDelay(time.Hour)
		defer pve.SetAgentDelay(0)
		run, s, _ := start(web01.ID, nil)
		if s != 202 {
			t.Fatalf("starten: %d", s)
		}
		var sb sandboxView
		eventually(t, "sandbox gestart", func() bool {
			ov := overview()
			if len(ov.Sandboxes) != 1 {
				return false
			}
			sb = ov.Sandboxes[0]
			g, _ := pve.Guest(sb.VMID)
			return g.Status == "running"
		})
		if !sb.Running || sb.State != "present" || sb.Source == nil || sb.Source.Name != "web01" {
			t.Fatalf("sandbox in het overzicht: %+v", sb)
		}
		// Het testslot is bezet.
		if _, s, er := start(web01.ID, nil); s != 409 || er.Code != "busy" {
			t.Fatalf("tweede controle: %d %+v", s, er)
		}
		// Opruimen kan pas als de controle klaar is.
		var er apiErr
		if s := c.do("POST", "/api/v1/backup-sandboxes/"+sb.ID+"/cleanup", nil, &er); s != 409 || er.Code != "running" {
			t.Fatalf("opruimen tijdens de controle: %d %+v", s, er)
		}
		if s := viewer.do("POST", "/api/v1/backup-sandboxes/"+sb.ID+"/cleanup", nil, nil); s != 403 {
			t.Fatalf("viewer ruimt op: %d", s)
		}
		// Geen VM-acties en geen koppeling aan een node.
		c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil)
		if s := c.do("POST", fmt.Sprintf("/api/v1/proxmox/%s/vms/%d/actions", conn.ID, sb.VMID), map[string]any{"action": "stop"}, &er); s != 409 ||
			!strings.Contains(er.Message, "sandbox") {
			t.Fatalf("VM-actie op de sandbox: %d %+v", s, er)
		}
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "kopie", "proxmox": map[string]any{"connection_id": conn.ID, "vmid": sb.VMID}}, &er); s != 400 ||
			!strings.Contains(er.Message, "sandbox") {
			t.Fatalf("node koppelen aan de sandbox: %d %+v", s, er)
		}
		var res struct {
			Guests []struct {
				VMID    int  `json:"vmid"`
				Sandbox bool `json:"sandbox"`
			} `json:"guests"`
		}
		c.do("GET", "/api/v1/proxmox/"+conn.ID+"/resources", nil, &res)
		if i := slices.IndexFunc(res.Guests, func(g struct {
			VMID    int  `json:"vmid"`
			Sandbox bool `json:"sandbox"`
		}) bool {
			return g.VMID == sb.VMID
		}); i < 0 || !res.Guests[i].Sandbox {
			t.Fatalf("Proxmox-pagina: %+v", res.Guests)
		}
		// Een kopie met dezelfde agent.json komt er niet in.
		copyCfg := agentCfg
		copyAgent := &agent.Agent{
			Config: &copyCfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Collector: fakeCollector(procRoot(t, strings.Repeat("8", 32))), HeartbeatInterval: 100 * time.Millisecond,
			StatePath: filepath.Join(t.TempDir(), "state.json"),
		}
		actx, stopCopy := context.WithCancel(ctx)
		defer stopCopy()
		// Zolang de kopie inlogt, staat ze al in de lijst van NATS. Een slot
		// op agents, op een eigen verbinding, houdt haar in Check vast; ze
		// mag dan niet meetellen, anders breekt de bewaking (elke 20 ms) de
		// controle ten onrechte af.
		key := nkeyOf(t, e, web01.ID)
		lockConn, err := pgx.Connect(ctx, os.Getenv("CF_TEST_DATABASE_URL"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lockConn.Close(ctx) }()
		tx, err := lockConn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "LOCK TABLE agents IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		go func() { _ = copyAgent.Run(actx) }()
		time.Sleep(500 * time.Millisecond)
		if n, _ := e.bus.Connections(key); n != 1 {
			_ = tx.Rollback(ctx)
			t.Fatalf("verbindingen met de sleutel van web01 terwijl de kopie inlogt: %d", n)
		}
		_ = tx.Rollback(ctx)
		eventually(t, "tweede verbinding geweigerd", func() bool {
			return slices.Contains(eventActions(t, e, web01.ID), "backup.sandbox_connection_refused")
		})
		if n, _ := e.bus.Connections(nkeyOf(t, e, web01.ID)); n != 1 {
			t.Fatalf("verbindingen met de sleutel van web01: %d", n)
		}
		stopCopy()
		pve.SetAgentDelay(0)

		r := finished(run.ID)
		if *r.Result != "pass" {
			t.Fatalf("uitkomst: %s %q", *r.Result, r.Summary)
		}
		if ok, _, detail := r.check("Agentverbinding"); !ok || !strings.Contains(detail, "geen tweede verbinding") {
			t.Fatalf("agentverbinding: %v %s", ok, detail)
		}
		if s := c.do("POST", "/api/v1/backup-sandboxes/"+sb.ID+"/cleanup", nil, &er); s != 409 || !strings.Contains(er.Message, "al opgeruimd") {
			t.Fatalf("opruimen na de controle: %d %+v", s, er)
		}
	})

	t.Run("tweede verbinding zet de sandbox hard uit", func(t *testing.T) {
		pve.SetAgentDelay(time.Second)
		defer pve.SetAgentDelay(0)
		run, _, _ := start(web01.ID, nil)
		eventually(t, "sandbox gestart", func() bool {
			ov := overview()
			if len(ov.Sandboxes) != 1 {
				return false
			}
			g, _ := pve.Guest(ov.Sandboxes[0].VMID)
			return g.Status == "running"
		})
		e.conns.extra.Store(1)
		r := finished(run.ID)
		e.conns.extra.Store(0)
		if *r.Result != "error" || !strings.Contains(r.Summary, "controle afgebroken: tweede verbinding met de sleutel van web01") ||
			!strings.Contains(r.Summary, "verwijderd om") || len(inPool()) != 0 {
			t.Fatalf("uitkomst: %s %q %v", *r.Result, r.Summary, inPool())
		}
	})

	t.Run("afbreken ruimt de sandbox op", func(t *testing.T) {
		pve.SetAgentDelay(2 * time.Second)
		defer pve.SetAgentDelay(0)
		run, _, _ := start(web01.ID, nil)
		var vmid int
		eventually(t, "sandbox gestart", func() bool {
			ov := overview()
			if len(ov.Sandboxes) != 1 {
				return false
			}
			vmid = ov.Sandboxes[0].VMID
			g, _ := pve.Guest(vmid)
			return g.Status == "running"
		})
		if s := c.do("POST", "/api/v1/jobs/"+*run.JobID+"/cancel", nil, nil); s != 200 && s != 202 {
			t.Fatalf("afbreken: %d", s)
		}
		r := finished(run.ID)
		if *r.Result != "canceled" || !strings.Contains(r.Summary, fmt.Sprintf("Sandbox-VM %d verwijderd", vmid)) {
			t.Fatalf("uitkomst: %s %q", *r.Result, r.Summary)
		}
		if slices.Contains(pve.Guests(), vmid) || len(inPool()) != 0 || len(overview().Sandboxes) != 0 {
			t.Fatalf("sandbox over na afbreken: %v", pve.Guests())
		}
	})

	t.Run("herstart van de server laat geen sandbox achter", func(t *testing.T) {
		pve.SetAgentDelay(2 * time.Second)
		defer pve.SetAgentDelay(0)
		run, _, _ := start(web01.ID, nil)
		var vmid int
		eventually(t, "sandbox gestart", func() bool {
			ov := overview()
			if len(ov.Sandboxes) != 1 {
				return false
			}
			vmid = ov.Sandboxes[0].VMID
			g, _ := pve.Guest(vmid)
			return g.Status == "running"
		})
		e.restartRunner()
		r := finished(run.ID)
		if *r.Result != "error" || !strings.Contains(r.Summary, "onderbroken door herstart van de server") ||
			!strings.Contains(r.Summary, fmt.Sprintf("Sandbox-VM %d verwijderd", vmid)) {
			t.Fatalf("uitkomst: %s %q", *r.Result, r.Summary)
		}
		if slices.Contains(pve.Guests(), vmid) || len(inPool()) != 0 || len(overview().Sandboxes) != 0 {
			t.Fatalf("sandbox over na de herstart: %v", pve.Guests())
		}
		var j job
		c.do("GET", "/api/v1/jobs/"+*r.JobID, nil, &j)
		var steps []string
		for _, st := range j.Steps {
			steps = append(steps, st.Name+":"+st.Status)
		}
		if strings.Join(steps, " ") != "Kiezen:succeeded Terugzetten:succeeded Isoleren:succeeded Starten:failed Opruimen:succeeded" {
			t.Fatalf("stappen: %v", steps)
		}
	})

	t.Run("verwijderen mislukt en de opruimer probeert opnieuw", func(t *testing.T) {
		pve.FailNext("qmdestroy", "storage is busy")
		run, _, _ := start(web01.ID, nil)
		r := finished(run.ID)
		ok, warning, detail := r.check("Opruimen")
		if *r.Result != "warning" || ok || !warning || !strings.Contains(detail, "storage is busy") ||
			!strings.Contains(r.Summary, "nog niet verwijderd; de opruimer probeert het opnieuw") {
			t.Fatalf("uitkomst: %s %q %s", *r.Result, r.Summary, detail)
		}
		ov := overview()
		if len(ov.Sandboxes) != 1 || ov.Sandboxes[0].State != "destroy_failed" || ov.Sandboxes[0].Running || len(inPool()) != 1 {
			t.Fatalf("sandboxes: %+v %v", ov.Sandboxes, inPool())
		}
		if a := eventActions(t, e, run.ID); !slices.Contains(a, "backup.sandbox_destroy_failed") {
			t.Fatalf("events: %v", a)
		}
		// Nog een mislukte ronde geeft geen tweede alarm.
		pve.FailNext("qmdestroy", "storage is busy")
		e.backups.Clean(ctx)
		if a := eventActions(t, e, run.ID); countOf(a, "backup.sandbox_destroy_failed") != 1 {
			t.Fatalf("events na een tweede ronde: %v", a)
		}
		e.backups.Clean(ctx)
		if ov := overview(); len(ov.Sandboxes) != 0 || len(inPool()) != 0 {
			t.Fatalf("na de opruimer: %+v %v", ov.Sandboxes, inPool())
		}

		// En met de hand, langs dezelfde weg.
		pve.FailNext("qmdestroy", "storage is busy")
		run, _, _ = start(web01.ID, nil)
		finished(run.ID)
		sb := overview().Sandboxes[0]
		var after sandboxView
		if s := c.do("POST", "/api/v1/backup-sandboxes/"+sb.ID+"/cleanup", nil, &after); s != 200 || after.State != "destroyed" || len(inPool()) != 0 {
			t.Fatalf("opruimen met de hand: %d %+v", s, after)
		}
	})

	t.Run("de opruimer raakt niets buiten de sandbox-pool", func(t *testing.T) {
		q := store.New(e.pool)
		connID := mustUUID(t, conn.ID)
		// Een crash tussen NextID en het terugzetten: de VM kwam er nooit.
		never, err := q.InsertBackupSandbox(ctx, store.InsertBackupSandboxParams{
			ConnectionID: connID, Vmid: 4321, SourceVmid: 101, Volid: "pbs:backup/vm/101/x", Host: "pve1", Storage: "ceph",
		})
		if err != nil {
			t.Fatal(err)
		}
		// Een VMID dat (nu) een gewone VM is.
		other, err := q.InsertBackupSandbox(ctx, store.InsertBackupSandboxParams{
			ConnectionID: connID, Vmid: 102, SourceVmid: 101, Volid: "pbs:backup/vm/101/x", Host: "pve1", Storage: "ceph",
		})
		if err != nil {
			t.Fatal(err)
		}
		before := len(pve.Calls())
		e.backups.Clean(ctx)
		for _, id := range []string{never.ID.String(), other.ID.String()} {
			sb, err := q.GetBackupSandbox(ctx, mustUUID(t, id))
			if err != nil || sb.State != "none" {
				t.Fatalf("sandbox %d: %+v %v", sb.Vmid, sb, err)
			}
		}
		for _, call := range pve.Calls()[before:] {
			if strings.Contains(call, "/102") {
				t.Fatalf("VM 102 aangeraakt: %s", call)
			}
		}
		if g, ok := pve.Guest(102); !ok || g.Status != "running" {
			t.Fatal("VM 102 is weg of gestopt")
		}
		if a := eventActions(t, e, conn.ID); slices.Contains(a, "backup.sandbox_destroy_failed") {
			t.Fatalf("alarm voor een rij zonder VM: %v", a)
		}
	})
}

// nkeyOf geeft de publieke sleutel van de agent van een node.
func nkeyOf(t *testing.T, e *testEnv, nodeID string) string {
	t.Helper()
	a, err := store.New(e.pool).GetActiveAgentByNode(context.Background(), mustUUID(t, nodeID))
	if err != nil {
		t.Fatal(err)
	}
	return a.NkeyPublic
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
