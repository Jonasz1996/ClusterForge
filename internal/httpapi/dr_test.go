package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/planner"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// drSandboxes speelt cf-agent verify na in de sandboxes van een
// DR-repetitie: elke sandbox is een kopie van zijn node, met het
// serienummer van een sandbox. Op de afgesloten bridge heeft holder het VIP
// (vanaf zijn tweede adresaanvraag, zoals keepalived dat nog moet kiezen),
// antwoordt de website op het VIP en weigeren de nodes poort 22.
type drSandboxes struct {
	pve    *pvefake.Server
	hosts  map[string]*agenttest.Host
	addrs  map[string]string
	vip    string
	holder string
	site   string
	// before draait voor elke aanvraag; zo grijpt een test in.
	before func(vmid int, name string)
	// old speelt een cf-agent met protocol 6: zonder adressen en probes.
	old bool

	mu       sync.Mutex
	asked    map[string]int
	requests map[string][]protocol.VerifyRequest
	vmids    map[string]int
	nets     map[string]string
}

func (s *drSandboxes) exec(vmid int, name string, command []string, input string) (int, string, string) {
	if !slices.Equal(command, []string{protocol.VerifyPath, "verify", "-"}) {
		return 127, "", "onbekend programma"
	}
	host := s.hosts[name]
	if host == nil {
		return 1, "", "onbekende VM " + name
	}
	var req protocol.VerifyRequest
	_ = json.Unmarshal([]byte(input), &req)
	g, _ := s.pve.Guest(vmid)
	s.mu.Lock()
	s.requests[name] = append(s.requests[name], req)
	s.vmids[name], s.nets[name] = vmid, g.Config["net0"]
	if req.Addresses {
		s.asked[name]++
	}
	asked := s.asked[name]
	before := s.before
	s.mu.Unlock()
	if before != nil {
		before(vmid, name)
	}
	own := []string{s.addrs[name]}
	if name == s.holder && asked > 1 {
		own = append(own, s.vip)
	}
	peers := map[string]bool{}
	for _, a := range s.addrs {
		peers[net.JoinHostPort(a, "22")] = true
	}
	v := &agent.Verifier{
		Version: "test", Exec: host.Exec, Root: host.Root,
		BootWait: time.Second, Settle: 300 * time.Millisecond, Retry: 20 * time.Millisecond,
		Addrs: func() ([]net.Addr, error) {
			out := []net.Addr{&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)}}
			for _, a := range own {
				out = append(out, &net.IPNet{IP: net.ParseIP(a), Mask: net.CIDRMask(24, 32)})
			}
			return out, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			switch {
			case addr == "127.0.0.1:80" || addr == net.JoinHostPort(s.vip, "80"):
				var d net.Dialer
				return d.DialContext(ctx, network, s.site)
			case peers[addr]:
				return nil, &net.OpError{Op: "dial", Net: network, Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
			}
			return nil, &net.OpError{Op: "dial", Net: network, Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}
		},
	}
	var out strings.Builder
	code := v.Main(context.Background(), strings.NewReader(input), &out)
	if s.old {
		var res protocol.VerifyResult
		_ = json.Unmarshal([]byte(out.String()), &res)
		res.ProtocolVersion, res.Addresses = protocol.RehearsalSince-1, nil
		res.Checks = slices.DeleteFunc(res.Checks, func(c protocol.VerifyCheck) bool { return c.Kind == "probe" })
		b, _ := json.Marshal(res)
		return code, string(b), ""
	}
	return code, out.String(), ""
}

func (s *drSandboxes) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked, s.requests, s.vmids, s.nets = map[string]int{}, map[string][]protocol.VerifyRequest{}, map[string]int{}, map[string]string{}
}

type drRun struct {
	verifyRun
	Rehearsal *struct {
		Bridge           string   `json:"bridge"`
		Host             string   `json:"host"`
		Storage          string   `json:"storage"`
		Fix              []string `json:"fix"`
		RTOTargetMinutes int      `json:"rto_target_minutes"`
		RPOTargetHours   int      `json:"rpo_target_hours"`
		RTOSeconds       *float64 `json:"rto_seconds"`
		RPOSeconds       *float64 `json:"rpo_seconds"`
		RTOMet           *bool    `json:"rto_met"`
		RPOMet           *bool    `json:"rpo_met"`
		Nodes            []struct {
			Hostname    string   `json:"hostname"`
			SourceVMID  int      `json:"source_vmid"`
			SandboxVMID *int     `json:"sandbox_vmid"`
			Addresses   []string `json:"addresses"`
			Destroyed   bool     `json:"destroyed"`
		} `json:"nodes"`
		VIPs []struct {
			Address string   `json:"address"`
			Holders []string `json:"holders"`
			Checked bool     `json:"checked"`
			ProbeOK *bool    `json:"probe_ok"`
		} `json:"vips"`
		DestroyedAt *time.Time `json:"destroyed_at"`
		Aborted     string     `json:"aborted"`
	} `json:"rehearsal"`
}

type drPreview struct {
	OK     bool `json:"ok"`
	Checks []struct {
		Name    string `json:"name"`
		OK      bool   `json:"ok"`
		Warning bool   `json:"warning"`
		Detail  string `json:"detail"`
		Code    string `json:"code"`
	} `json:"checks"`
	Rehearsal *struct {
		Host       string   `json:"host"`
		Storage    string   `json:"storage"`
		Bridge     string   `json:"bridge"`
		Fix        []string `json:"fix"`
		RPOSeconds float64  `json:"rpo_seconds"`
		Nodes      []struct {
			Hostname string `json:"hostname"`
			Address  string `json:"address"`
		} `json:"nodes"`
	} `json:"rehearsal"`
}

func (p drPreview) check(name string) (bool, string, string) {
	for _, c := range p.Checks {
		if c.Name == name {
			return c.OK, c.Code, c.Detail
		}
	}
	return false, "", ""
}

// TestDRRehearsal is de acceptatie van mijlpaal 13 van fase 3: de repetitie
// zet het prod-cluster Web helemaal terug op vmbr99. In de sandbox heeft
// het VIP één houder en antwoordt HTTP, het rapport geeft RTO en RPO tegen
// de doelen, en daarna is alles weg. Zonder afgesloten bridge start ze
// niet, en het rapport geeft de commando's.
func TestDRRehearsal(t *testing.T) {
	e, c := adminClient(t)
	f, clusterID, ids, pve, conn := deployWebPVE(t, e, c)
	ctx := context.Background()
	src := map[string]int{}
	for _, h := range []string{"web-01", "web-02"} {
		var vmid int
		if err := e.pool.QueryRow(ctx, "SELECT pve_vmid FROM nodes WHERE id = $1", ids[h]).Scan(&vmid); err != nil {
			t.Fatal(err)
		}
		src[h] = vmid
		// Het serienummer van een sandbox; alleen cf-agent verify leest het.
		dir := filepath.Join(f.host(h).Root, "sys/class/dmi/id")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "product_serial"), []byte(protocol.SandboxSerial+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pve.AddPool("cf-sandbox")
	pve.AddStorage(pvefake.Storage{Name: "pbs", Node: "pve1", Shared: true, Content: "backup"})
	now := time.Now().Truncate(time.Second)
	pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: src["web-01"], Time: now.Add(-6 * time.Hour), Size: 4 << 30})
	pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: src["web-02"], Time: now.Add(-5 * time.Hour), Size: 4 << 30})
	if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != 200 {
		t.Fatalf("sync: %d", s)
	}
	uplink := []map[string]any{
		{"iface": "vmbr0", "type": "bridge", "active": 1, "bridge_ports": "eno1", "cidr": "192.168.1.11/24", "gateway": "192.168.1.1"},
		{"iface": "eno1", "type": "eth", "active": 1},
		{"iface": "eno2", "type": "eth", "active": 1},
	}
	pve.SetHostNetwork("pve1", uplink)

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("web")) }))
	defer site.Close()
	sb := &drSandboxes{
		pve: pve, hosts: map[string]*agenttest.Host{"web-01": f.host("web-01"), "web-02": f.host("web-02")},
		addrs: map[string]string{"web-01": "10.0.20.11", "web-02": "10.0.20.12"}, vip: "10.0.20.100", holder: "web-01",
		site: site.Listener.Addr().String(),
	}
	sb.reset()
	pve.OnExec(sb.exec)

	// Het formulier: de DR-repetitie kan ook op prod, met de doelen van prod.
	listURL := "/api/v1/clusters/" + clusterID + "/disaster-scenarios"
	var list struct {
		Options struct {
			Allowed bool `json:"allowed"`
			Kinds   []struct {
				Key       string `json:"key"`
				Available bool   `json:"available"`
			} `json:"kinds"`
			Targets struct {
				RTOMinutes int  `json:"rto_minutes"`
				RPOHours   int  `json:"rpo_hours"`
				Custom     bool `json:"custom"`
			} `json:"targets"`
		} `json:"options"`
	}
	if s := c.do("GET", listURL, nil, &list); s != http.StatusOK {
		t.Fatalf("lijst: %d", s)
	}
	o := list.Options
	if o.Allowed || len(o.Kinds) != 5 || o.Kinds[4].Key != "dr_rehearsal" || !o.Kinds[4].Available || o.Kinds[0].Available ||
		o.Targets.RTOMinutes != 60 || o.Targets.RPOHours != 26 || o.Targets.Custom {
		t.Fatalf("opties op prod: %+v", o)
	}
	var scenario struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Bridge string `json:"bridge"`
	}
	if s := c.do("POST", listURL, map[string]any{"name": "DR Web", "kind": "dr_rehearsal", "fault_seconds": 0, "recovery_seconds": 0}, &scenario); s != http.StatusCreated ||
		scenario.Bridge != "vmbr99" {
		t.Fatalf("scenario: %d %+v", s, scenario)
	}
	if s := c.do("POST", listURL, map[string]any{"name": "Web weg", "kind": "vip_loss", "fault_seconds": 60, "recovery_seconds": 120}, nil); s != http.StatusBadRequest {
		t.Fatalf("storing op prod: %d", s)
	}
	if s := c.do("POST", listURL, map[string]any{"name": "DR", "kind": "dr_rehearsal", "bridge": "vmbr99; reboot", "fault_seconds": 0, "recovery_seconds": 0}, nil); s != http.StatusBadRequest {
		t.Fatalf("rare bridge: %d", s)
	}
	previewURL := "/api/v1/disaster-scenarios/" + scenario.ID + "/preview"
	runURL := "/api/v1/disaster-scenarios/" + scenario.ID + "/run"
	preview := func() drPreview {
		t.Helper()
		var p drPreview
		if s := c.do("GET", previewURL, nil, &p); s != http.StatusOK || p.Rehearsal == nil {
			t.Fatalf("voorcontrole: %d %+v", s, p)
		}
		return p
	}
	finished := func(id string) drRun {
		t.Helper()
		var r drRun
		eventually(t, "repetitie klaar", func() bool {
			c.do("GET", "/api/v1/test-runs/"+id, nil, &r)
			return r.Result != nil && (r.JobStatus == nil || *r.JobStatus != "queued" && *r.JobStatus != "running")
		})
		return r
	}
	start := func() drRun {
		t.Helper()
		sb.reset()
		var run drRun
		if s := c.do("POST", runURL, map[string]any{"confirm": []string{}}, &run); s != http.StatusAccepted {
			t.Fatalf("starten: %d", s)
		}
		return finished(run.ID)
	}

	t.Run("zonder bridge", func(t *testing.T) {
		p := preview()
		ok, code, detail := p.check("Bridge")
		if p.OK || ok || code != "bridge_not_isolated" || detail != "op pve1 bestaat geen bridge vmbr99; maak een afgesloten bridge zonder poorten en zonder adres" {
			t.Fatalf("voorcontrole: %+v", p.Checks)
		}
		r := p.Rehearsal
		if r.Host != "pve1" || r.Storage != "ceph" || len(r.Nodes) != 2 || r.Nodes[0].Address != "10.0.20.11" ||
			r.RPOSeconds < 6*3600 || r.RPOSeconds > 6*3600+120 || !slices.Equal(r.Fix, []string{
			"pvesh create /nodes/pve1/network --iface vmbr99 --type bridge --autostart 1 --comments 'ClusterForge DR-repetitie: afgesloten'",
			"pvesh set /nodes/pve1/network",
		}) {
			t.Fatalf("plan: %+v", r)
		}
		var pe precheckErr
		if s := c.do("POST", runURL, map[string]any{"confirm": []string{}}, &pe); s != http.StatusConflict || pe.Code != "precheck_failed" ||
			!slices.Equal(pe.failed(), []string{"Bridge"}) {
			t.Fatalf("starten: %d %+v", s, pe)
		}
	})

	t.Run("bridge met poort", func(t *testing.T) {
		pve.SetHostNetwork("pve1", append(slices.Clone(uplink), map[string]any{"iface": "vmbr99", "type": "bridge", "active": 1, "bridge_ports": "eno2"}))
		p := preview()
		if _, _, detail := p.check("Bridge"); p.OK || detail != "vmbr99 op pve1 is niet afgesloten: ze hangt aan eno2" ||
			!slices.Equal(p.Rehearsal.Fix, []string{"pvesh set /nodes/pve1/network/vmbr99 --type bridge --delete bridge_ports", "pvesh set /nodes/pve1/network"}) {
			t.Fatalf("voorcontrole: %+v %+v", p.Checks, p.Rehearsal.Fix)
		}
	})

	isolated := append(slices.Clone(uplink), map[string]any{"iface": "vmbr99", "type": "bridge", "active": 1, "comments": "afgesloten"})
	pve.SetHostNetwork("pve1", isolated)

	t.Run("geslaagd", func(t *testing.T) {
		if p := preview(); !p.OK || len(p.Rehearsal.Fix) != 0 {
			t.Fatalf("voorcontrole: %+v", p.Checks)
		}
		before := len(pve.Calls())
		r := start()
		if *r.Result != "pass" || !strings.HasPrefix(r.Summary, "PASS: Web op vmbr99: het cluster kwam werkend terug. RTO ") ||
			!strings.Contains(r.Summary, " (doel 1 u). RPO 6 u (doel 26 u). VIP 10.0.20.100 bij web-01, HTTP antwoordt. 2 sandbox-VM's verwijderd om ") {
			t.Fatalf("uitkomst: %s %q\n%+v\n%s", *r.Result, r.Summary, r.Checks, stepLog(t, e, *r.JobID))
		}
		for _, ch := range r.Checks {
			if !ch.OK {
				t.Errorf("controle %s: %+v", ch.Name, ch)
			}
		}
		for _, name := range []string{
			"Voorcontrole", "Terugzetten", "Afsluiten", "web-01: Opstarten", "web-02: Opstarten", "Opstarten",
			"web-01: Service nginx", "web-02: HTTP http://127.0.0.1/", "VIP 10.0.20.100",
			"Van web-02 naar VIP 10.0.20.100", "Van web-02 naar web-01", "Opruimen",
		} {
			if !slices.Contains(r.checkNames(), name) {
				t.Errorf("controle %s ontbreekt: %v", name, r.checkNames())
			}
		}
		if ok, _, detail := r.check("VIP 10.0.20.100"); !ok || detail != "één houder: web-01" {
			t.Fatalf("VIP: %s", detail)
		}
		if _, _, detail := r.check("Van web-02 naar web-01"); detail != "10.0.20.11 antwoordt: poort 22 weigert de verbinding, maar de machine is bereikbaar" {
			t.Fatalf("verbinding: %s", detail)
		}
		rep := r.Rehearsal
		if rep == nil || rep.Host != "pve1" || rep.Bridge != "vmbr99" || rep.RTOSeconds == nil || rep.RPOSeconds == nil || rep.RTOMet == nil ||
			!*rep.RTOMet || rep.RPOMet == nil || !*rep.RPOMet || rep.RTOTargetMinutes != 60 || rep.RPOTargetHours != 26 || rep.DestroyedAt == nil ||
			*rep.RPOSeconds < 6*3600 || *rep.RPOSeconds > 6*3600+120 || rep.Aborted != "" {
			t.Fatalf("rapport: %+v", rep)
		}
		if len(rep.VIPs) != 1 || !slices.Equal(rep.VIPs[0].Holders, []string{"web-01"}) || !rep.VIPs[0].Checked ||
			rep.VIPs[0].ProbeOK == nil || !*rep.VIPs[0].ProbeOK {
			t.Fatalf("VIP's: %+v", rep.VIPs)
		}
		sandboxes := map[int]bool{}
		for _, n := range rep.Nodes {
			if n.SandboxVMID == nil || *n.SandboxVMID == n.SourceVMID || !n.Destroyed {
				t.Fatalf("node: %+v", n)
			}
			sandboxes[*n.SandboxVMID] = true
			if _, still := pve.Guest(*n.SandboxVMID); still {
				t.Fatalf("sandbox %d staat er nog", *n.SandboxVMID)
			}
		}
		if len(sandboxes) != 2 || !slices.Equal(rep.Nodes[0].Addresses, []string{"10.0.20.100", "10.0.20.11"}) {
			t.Fatalf("nodes: %+v", rep.Nodes)
		}
		var j job
		c.do("GET", "/api/v1/jobs/"+*r.JobID, nil, &j)
		var steps []string
		for _, st := range j.Steps {
			steps = append(steps, st.Name)
			if st.Name != "Opruimen" {
				continue
			}
			for vmid := range sandboxes {
				if !slices.Contains(st.Log, fmt.Sprintf("VM %d verwijderd", vmid)) {
					t.Errorf("log van Opruimen zonder VM %d: %v", vmid, st.Log)
				}
			}
		}
		if !slices.Equal(steps, []string{"Voorcontrole", "Terugzetten", "Afsluiten", "Starten", "Controleren", "Opruimen"}) {
			t.Fatalf("stappen: %v", steps)
		}
		// In de sandbox: de bridge vmbr99 met het MAC-adres van de bron.
		for h, vmid := range src {
			g, _ := pve.Guest(vmid)
			mac, _, _ := strings.Cut(g.Config["net0"], ",")
			sb.mu.Lock()
			got, sbVMID := sb.nets[h], sb.vmids[h]
			sb.mu.Unlock()
			if !strings.HasPrefix(got, mac+",") || !strings.Contains(got, "bridge=vmbr99") || strings.Contains(got, "vmbr0,") ||
				strings.Contains(got, "link_down") || !sandboxes[sbVMID] {
				t.Fatalf("%s: net0 %q in sandbox %d, bron %q", h, got, sbVMID, g.Config["net0"])
			}
		}
		// Alleen in de sandboxes, nooit op de productie-VM's.
		for _, call := range pve.Calls()[before:] {
			if strings.HasSuffix(call, "/agent/exec") && (strings.Contains(call, fmt.Sprintf("/%d/", src["web-01"])) ||
				strings.Contains(call, fmt.Sprintf("/%d/", src["web-02"]))) {
				t.Fatalf("exec op productie: %s", call)
			}
		}
		// De probes gaan vanaf web-02, zonder VIP, naar het VIP en web-01.
		sb.mu.Lock()
		reqs := sb.requests["web-02"]
		sb.mu.Unlock()
		last := reqs[len(reqs)-1]
		if !slices.Equal(last.Probes, []protocol.VerifyProbe{
			{Name: "VIP 10.0.20.100", Host: "10.0.20.100", Port: 80, Path: "/", Expect: 200},
			{Name: "web-01", Host: "10.0.20.11", Port: 22, Reach: true},
		}) {
			t.Fatalf("probes: %+v", last.Probes)
		}
		var actions []string
		rows, err := e.pool.Query(ctx, "SELECT action FROM events WHERE subject_id = $1 AND action LIKE 'dr.%' ORDER BY id", r.ID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var a string
			_ = rows.Scan(&a)
			actions = append(actions, a)
		}
		if !slices.Equal(actions, []string{"dr.started", "dr.finished"}) {
			t.Fatalf("events: %v", actions)
		}

		// De documentatie noemt de meting met datum en doel; het runbook
		// zet de echte VM's terug op hun storage.
		maanden := []string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}
		today := time.Now()
		date := fmt.Sprintf("%d %s %d", today.Day(), maanden[today.Month()-1], today.Year())
		var herstel, runbooks testDoc
		c.do("GET", "/api/v1/docs/cluster/herstel?cluster="+clusterID, nil, &herstel)
		for _, w := range []string{
			"Gemeten in de sandbox op " + date + ", met de DR-repetitie 'DR Web'. Het doel is een RTO van hoogstens 60 minuten en een RPO van hoogstens 26 uur (de standaard voor prod).",
			"| **RPO**, gegevensverlies | 6 u | hoogstens 26 uur | ja |",
			"op pve1 (Thuislab) en storage `ceph`, met de netwerkkaarten aan de afgesloten bridge `vmbr99`.",
			"`10.0.20.100` had in de sandbox één houder, web-01, en de probe (HTTP / geeft 200) slaagde.",
			"'DR Web' op de afgesloten bridge `vmbr99`: alleen met de hand.",
		} {
			if !strings.Contains(herstel.Content, w) {
				t.Errorf("herstel mist %q:\n%s", w, herstel.Content)
			}
		}
		if !regexp.MustCompile(`\| \*\*RTO\*\*, hersteltijd \| [0-9,]+ s \| hoogstens 60 minuten \| ja \|`).MatchString(herstel.Content) {
			t.Errorf("herstel zonder gemeten RTO:\n%s", herstel.Content)
		}
		// Zoals elke tien minuten: Proxmox geeft de configuratie van de VM's.
		if err := e.pve.ReadConfigs(ctx, uuid.MustParse(conn.ID)); err != nil {
			t.Fatal(err)
		}
		c.do("GET", "/api/v1/docs/cluster/runbooks?cluster="+clusterID, nil, &runbooks)
		for h, vmid := range src {
			if !regexp.MustCompile(fmt.Sprintf(`qmrestore \S+ %d --force 1 --storage ceph`, vmid)).MatchString(runbooks.Content) {
				t.Errorf("runbook zonder terugzetten van %s (VM %d):\n%s", h, vmid, runbooks.Content)
			}
		}
		for _, w := range []string{"Zo deed de DR-repetitie het op " + date + " in de sandbox: na ", "In de DR-repetitie van " + date + " duurde terugzetten "} {
			if !strings.Contains(runbooks.Content, w) {
				t.Errorf("runbook mist %q", w)
			}
		}
	})

	t.Run("doelen", func(t *testing.T) {
		targetsURL := "/api/v1/clusters/" + clusterID + "/recovery-targets"
		viewer := e.client()
		e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
		if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
			t.Fatalf("login kijker: %d", s)
		}
		if s := viewer.do("PUT", targetsURL, map[string]any{"rto_minutes": 30, "rpo_hours": 1}, nil); s != http.StatusForbidden {
			t.Fatalf("kijker: %d", s)
		}
		if s := c.do("PUT", targetsURL, map[string]any{"rto_minutes": 30, "rpo_hours": 99999}, nil); s != http.StatusBadRequest {
			t.Fatalf("RPO te groot: %d", s)
		}
		var tg struct {
			RTOMinutes int  `json:"rto_minutes"`
			RPOHours   int  `json:"rpo_hours"`
			Custom     bool `json:"custom"`
		}
		if s := c.do("PUT", targetsURL, map[string]any{"rto_minutes": 30, "rpo_hours": 1}, &tg); s != http.StatusOK || tg.RTOMinutes != 30 || tg.RPOHours != 1 || !tg.Custom {
			t.Fatalf("doelen: %d %+v", s, tg)
		}
		r := start()
		if *r.Result != "warning" || !strings.HasPrefix(r.Summary, "WARNING: Web op vmbr99: het cluster kwam werkend terug, maar RPO 6 u boven het doel van 1 u.") ||
			!strings.Contains(r.Summary, "(doel 30 min)") || r.Rehearsal.RPOMet == nil || *r.Rehearsal.RPOMet || !*r.Rehearsal.RTOMet {
			t.Fatalf("uitkomst: %s %q", *r.Result, r.Summary)
		}
		if s := c.do("PUT", targetsURL, map[string]any{"rto_minutes": nil, "rpo_hours": nil}, &tg); s != http.StatusOK || tg.RTOMinutes != 60 || tg.Custom {
			t.Fatalf("terug naar standaard: %d %+v", s, tg)
		}
	})

	t.Run("oude agent", func(t *testing.T) {
		sb.old = true
		defer func() { sb.old = false }()
		r := start()
		ok, warning, detail := r.check("VIP 10.0.20.100")
		if *r.Result != "warning" || ok || !warning || detail != "niet gecontroleerd: geen node meldde zijn adressen; daarvoor is cf-agent met protocol 7 in de back-up nodig" ||
			!strings.HasPrefix(r.Summary, "WARNING: Web op vmbr99: het cluster kwam werkend terug, maar VIP 10.0.20.100: niet gecontroleerd") ||
			r.Rehearsal.RTOSeconds == nil || r.Rehearsal.VIPs[0].Checked {
			t.Fatalf("uitkomst: %s %q %+v", *r.Result, r.Summary, r.Checks)
		}
		if _, warning, _ := r.check("Verbindingen"); !warning {
			t.Fatalf("verbindingen: %+v", r.Checks)
		}
	})

	t.Run("bewaking", func(t *testing.T) {
		// Het echte VIP verhuist terwijl de sandboxes draaien: alles gaat
		// meteen hard uit.
		var stopped sync.Once
		sb.before = func(vmid int, _ string) {
			stopped.Do(func() {
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					_, _ = e.pool.Exec(ctx, "UPDATE vips SET owner_node_id = $1 WHERE address = '10.0.20.100'", ids["web-02"])
					if g, ok := pve.Guest(vmid); !ok || g.Status != "running" {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
			})
		}
		defer func() { sb.before = nil }()
		r := start()
		if *r.Result != "error" ||
			!strings.HasPrefix(r.Summary, "ERROR: Web op vmbr99: afgebroken, want het echte VIP 10.0.20.100 verhuisde naar een andere node; alle sandboxes zijn hard uitgezet.") ||
			!strings.Contains(r.Summary, "2 sandbox-VM's verwijderd om") || r.Rehearsal.Aborted == "" || r.Rehearsal.RTOSeconds != nil {
			t.Fatalf("uitkomst: %s %q\n%s", *r.Result, r.Summary, stepLog(t, e, *r.JobID))
		}
		var n int
		if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE subject_id = $1 AND action = 'dr.aborted'", r.ID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("dr.aborted: %d %v", n, err)
		}
		for _, node := range r.Rehearsal.Nodes {
			if _, still := pve.Guest(*node.SandboxVMID); still {
				t.Fatalf("sandbox %d staat er nog", *node.SandboxVMID)
			}
		}
	})

	t.Run("gepland zonder bridge", func(t *testing.T) {
		pve.SetHostNetwork("pve1", uplink)
		var upd struct {
			Schedule  string     `json:"schedule"`
			NextRunAt *time.Time `json:"next_run_at"`
		}
		if s := c.do("PUT", "/api/v1/disaster-scenarios/"+scenario.ID, map[string]any{
			"name": "DR Web", "kind": "dr_rehearsal", "schedule": "weekly", "fault_seconds": 0, "recovery_seconds": 0,
		}, &upd); s != http.StatusOK || upd.Schedule != "weekly" || upd.NextRunAt == nil {
			t.Fatalf("planning: %d %+v", s, upd)
		}
		// Het testvenster van zondag, tien minuten na de start.
		w, _ := planner.DefaultWindow.Next(time.Now())
		if _, err := e.pool.Exec(ctx, "UPDATE disaster_scenarios SET next_run_at = $2 WHERE id = $1", scenario.ID, w); err != nil {
			t.Fatal(err)
		}
		if err := e.dis.RunScheduled(ctx, w.Add(10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		var id, trigger string
		if err := e.pool.QueryRow(ctx, "SELECT id, trigger FROM test_runs WHERE scenario_id = $1 ORDER BY created_at DESC LIMIT 1", scenario.ID).Scan(&id, &trigger); err != nil {
			t.Fatal(err)
		}
		r := finished(id)
		if *r.Result != "error" || trigger != "schedule" || r.JobID != nil ||
			r.Summary != "ERROR: Web op vmbr99: de DR-repetitie startte niet, want de voorcontrole faalt: "+
				"Bridge: op pve1 bestaat geen bridge vmbr99; maak een afgesloten bridge zonder poorten en zonder adres. "+
				"De commando's om de bridge af te sluiten staan in het rapport." ||
			len(r.Rehearsal.Fix) != 2 || !strings.HasPrefix(r.Rehearsal.Fix[0], "pvesh create /nodes/pve1/network --iface vmbr99 --type bridge") {
			t.Fatalf("geplande run: %s %q %+v", *r.Result, r.Summary, r.Rehearsal)
		}
		var next time.Time
		if err := e.pool.QueryRow(ctx, "SELECT next_run_at FROM disaster_scenarios WHERE id = $1", scenario.ID).Scan(&next); err != nil ||
			!next.After(w.Add(6*24*time.Hour)) {
			t.Fatalf("volgende run: %v %v (venster %v)", next, err, w)
		}
	})
}
