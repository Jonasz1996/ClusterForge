package docs

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

// fleet is een cluster met twee nodes op twee hosts, zoals keepalived-nginx
// het uitrolt, met een geheim in de parameters.
func fleet() Fleet {
	cid := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	other := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	at := time.Date(2026, 11, 3, 13, 5, 0, 0, time.UTC)
	nginx, pg := uuid.MustParse("33333333-3333-3333-3333-333333333333"), uuid.MustParse("44444444-4444-4444-4444-444444444444")
	nodes := []Node{}
	for i, h := range []string{"web-01", "web-02"} {
		ip := "10.0.20.1" + string(rune('1'+i))
		nodes = append(nodes, Node{
			ID: uuid.MustParse("6666666" + string(rune('6'+i)) + "-6666-6666-6666-666666666666"), Hostname: h, Role: "web", Index: i, Lifecycle: "active", Address: ip, Prefix: 24, Interface: "eth0",
			Interfaces: []Iface{{Name: "eth0", MAC: "BC:24:11:00:00:0" + string(rune('1'+i)), Addresses: []string{ip + "/24"}}},
			Gateway:    "10.0.20.1", DNS: []string{"10.0.20.1"}, OS: "Debian 13", HasAgent: true, HasFacts: true,
			VM: &VMShape{CPU: 2, MemoryMiB: 2048, DiskGiB: 20},
			Guest: &Guest{Proxmox: "Thuislab", VMID: 201 + i, Type: "qemu", Name: h, Host: "pve" + string(rune('1'+i)), HA: true, ConfigRead: true, OnBoot: true,
				NICs:  []NIC{{Key: "net0", Bridge: "vmbr0", MAC: "BC:24:11:00:00:0" + string(rune('1'+i)), Tag: 20}},
				Disks: []Disk{{Key: "scsi0", Storage: "local-lvm", SizeBytes: 20 << 30}}},
			Backup: &NodeBackup{Volid: "pbs:backup/vm/" + string(rune('1'+i)), Storage: "pbs", Host: "pve1", Time: at, Size: 3 << 30, Freshness: "ok", Count: 4},
		})
	}
	return Fleet{
		Clusters: []Cluster{{
			ID: cid, Name: "Web *front* <b>", Slug: "web", Env: "prod", Type: "web", Description: "De website [klik](javascript:alert(1))",
			Tags: []string{"klant"}, Status: "healthy", Grade: "B", Score: ptr(82),
			Template: &Template{Name: "keepalived-nginx", Version: "1.2.0", Title: "Keepalived met nginx", Latest: "1.2.0", Available: true, Failback: true},
			Network:  &Network{Proxmox: "Thuislab", Mode: "static", FirstIP: "10.0.20.11/24", Gateway: "10.0.20.1", DNS: []string{"10.0.20.1"}, Bridge: "vmbr0", VLAN: ptr(20), Storage: "local-lvm", ImageVMID: 9000},
			Nodes:    nodes,
			VIPs:     []VIP{{Address: "10.0.20.100", Interface: "eth0", VRID: ptr(51), Nodes: []string{"web-01", "web-02"}}},
			Services: []Svc{
				{ID: nginx, Name: "nginx", Kind: "http", Unit: "nginx.service", Port: 80, Source: "template"},
				{ID: uuid.MustParse("55555555-5555-5555-5555-555555555555"), Name: "node-exporter", Kind: "metrics", Port: 9100, On: "web-01", Source: "discovered"},
			},
			Deps: []Dep{{
				From:     End{ID: nginx, Name: "nginx", Group: "Web", ClusterID: &cid, Slug: "web"},
				To:       End{ID: pg, Name: "postgres", Group: "DB", ClusterID: &other, Slug: "db", Address: "10.0.30.10", Port: 5432},
				Strength: "hard", Source: "discovered",
			}},
			Params: []Param{{Name: "vip", Label: "VIP", Value: ptr("10.0.20.100")}, {Name: "auth_pass", Label: "Wachtwoord", Value: ptr("[geheim]"), Secret: true}},
			Files: []NodeFiles{{Hostname: "web-01", Packages: []string{"keepalived", "nginx"}, Services: []string{"keepalived (aan, draait)"},
				Files: []File{{Name: "keepalived", Path: "/etc/keepalived/keepalived.conf", Mode: "0640", Owner: "root", Group: "root",
					Content: "vrrp_instance VI_1 {\n  auth_pass [geheim]\n  ```\n}\n"}}}},
			YAML:     &File{Name: "cluster.yaml", Path: "clusters/web/cluster.yaml", Content: "name: Web\n"},
			Revision: 3, Applied: 3,
			Revisions: []Revision{{Revision: 3, Source: "ui", At: at, By: "jonas", Template: "keepalived-nginx 1.2.0", Changes: []Change{{Label: "Nodes", From: "1", To: "2"}}}},
			Backup:    BackupPolicy{MaxAgeHours: 30, Default: true, VerifyEnabled: true, Window: "zondag 03:00-05:00"},
			Failover: []FailoverTest{{Name: "VIP overname", Description: "Stopt keepalived op de master", VIP: "10.0.20.100", MaxTakeoverSeconds: 5, ExpectFailback: true, Scheduled: true,
				Last: &TestRun{Result: "pass", Summary: "Overname in 2,1 s", At: at, TakeoverMS: ptr(int64(2100)), DowntimeMS: ptr(int64(1800)), TakeoverNode: "web-02", ReturnedTo: "web-01"}}},
			Outages: []Outage{
				{Kind: "node", Name: "web-01", Status: "degraded", Reason: "1 van 2 nodes down", Nodes: []string{"web-01"}},
				{Kind: "host", Name: "pve1 (Thuislab)", Status: "degraded", Reason: "1 van 2 nodes down", During: "degraded", Nodes: []string{"web-01"}, Restarted: []string{"web-01"}},
			},
		}},
		Proxmox:  []Proxmox{{Name: "Thuislab", Hosts: []Host{{Name: "pve1", Online: true, MaxMem: 64 << 30, Storages: []Storage{{Name: "local-lvm", Type: "lvmthin", Active: true}}, Bridges: []Bridge{{Name: "vmbr0", Ports: []string{"eno1"}, VLANAware: true}}, Guests: 7}}}},
		Findings: []Finding{{Where: "Web", Text: "Geen back-upcontrole in 30 dagen", Points: 5}},
		Links:    []Link{{From: LinkEnd{Name: "Web", Slug: "web", Env: "prod"}, To: LinkEnd{Name: "DB", Slug: "db", Env: "prod"}, Count: 1, Hard: true, Services: []string{"postgres"}}},
	}
}

func TestRenderDeterministic(t *testing.T) {
	o := Opts{Loc: time.UTC, Usernames: true}
	a, b := Render(fleet(), o), Render(fleet(), o)
	if len(a) != 1+len(ClusterKinds) {
		t.Fatalf("%d documenten, want %d", len(a), 1+len(ClusterKinds))
	}
	for i := range a {
		if a[i].Content != b[i].Content {
			t.Fatalf("%s/%s verschilt tussen twee keer schrijven", a[i].Scope, a[i].Kind)
		}
		if Hash(a[i].Content) != Hash(b[i].Content) {
			t.Fatal("hash verschilt")
		}
	}
}

func TestRenderContent(t *testing.T) {
	o := Opts{Loc: time.UTC, Usernames: true}
	byKind := map[string]string{}
	for _, r := range Render(fleet(), o) {
		byKind[r.Kind] = r.Content
	}
	for kind, want := range map[string][]string{
		KindOverview:      {"# ClusterForge: overzicht", "[Web \\*front\\* \\<b\\>](clusters/web/architectuur.md)", "```mermaid", "pve1"},
		KindArchitecture:  {"# Web \\*front\\* \\<b\\>: architectuur", "[Overzicht](../../README.md)", "web-01", "10.0.20.100", "nginx"},
		KindNetwork:       {"```mermaid", "flowchart", "10.0.20.11", "vmbr0"},
		KindConfiguration: {"`[geheim]`", "/etc/keepalived/keepalived.conf", "````", "jonas"},
		KindRecovery:      {"VIP overname", "geslaagd", "host pve1 (Thuislab)"},
		KindRunbooks:      {"systemctl stop keepalived", "qmrestore pbs:backup/vm/1 201"},
	} {
		for _, w := range want {
			if !strings.Contains(byKind[kind], w) {
				t.Errorf("%s mist %q", kind, w)
			}
		}
	}
	// Tekst van buiten wordt nooit een link of HTML.
	for kind, c := range byKind {
		if strings.Contains(c, "](javascript:") && !strings.Contains(c, `\](javascript:`) || strings.Contains(c, "<b>") {
			t.Errorf("%s laat tekst van buiten door als Markdown", kind)
		}
	}
	// Zonder gebruikersnamen staat de gebruiker er niet in.
	for _, r := range Render(fleet(), Opts{Loc: time.UTC}) {
		if strings.Contains(r.Content, "jonas") {
			t.Errorf("%s noemt de gebruiker terwijl dat uit staat", r.Kind)
		}
	}
}

func TestMermaidLabel(t *testing.T) {
	f := newFlow("LR")
	a := f.node("a", nil, "box", `x"] --> evil["`, "<br>&#")
	b := f.node("b", f.group("groep `x`"), "db", "db")
	f.edge(a, b, "-->", "a|b")
	out := f.String()
	if strings.Contains(out, `x"]`) || strings.Contains(out, "<br>") {
		t.Fatalf("label breekt het diagram:\n%s", out)
	}
	for _, w := range []string{`n1["x#quot;] --#gt; evil[#quot;<br/>#lt;br#gt;#amp;#35;"]`, `subgraph g1["groep #96;x#96;"]`, `n2[("db")]`, `n1 -->|"a|b"| n2`} {
		if !strings.Contains(out, w) {
			t.Errorf("mist %q in\n%s", w, out)
		}
	}
}

func TestEscape(t *testing.T) {
	if got := esc("a*b_[c](d)<e>|#!~`\\\nf"); got != "a\\*b\\_\\[c\\](d)\\<e\\>\\|\\#\\!\\~\\`\\\\ f" {
		t.Fatalf("esc = %q", got)
	}
	if got := code("a`b"); got != "`` a`b ``" {
		t.Fatalf("code = %q", got)
	}
	if got := cell("a|b\\|c"); got != "a\\|b\\|c" {
		t.Fatalf("cell = %q", got)
	}
}

func TestRecoveryDR(t *testing.T) {
	o := Opts{Loc: time.UTC}
	f := fleet()
	c := &f.Clusters[0]
	c.DR = DR{RTOMinutes: 60, RPOHours: 26, Default: true}
	contains := func(doc string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(doc, w) {
				t.Errorf("mist %q in\n%s", w, doc)
			}
		}
	}
	contains(recovery(c, o),
		"Er is nog geen DR-repetitie gedaan, dus de hersteltijd (RTO) en het gegevensverlies (RPO) van het hele cluster zijn niet gemeten.",
		"Het doel is een RTO van hoogstens 60 minuten en een RPO van hoogstens 26 uur (de standaard voor prod).",
		"Er is geen DR-repetitie ingesteld.")
	contains(runbooks(c, o), "Hoe lang dat duurt, meet de DR-repetitie; voor dit cluster heeft die nog geen hersteltijd gemeten.")

	at := time.Date(2026, 11, 3, 3, 20, 0, 0, time.UTC)
	run := &DRRun{
		ID: uuid.New(), Name: "Web DR", Result: "pass", Summary: "Het hele cluster kwam terug", At: at,
		Proxmox: "Thuislab", Host: "pve1", Storage: "local-lvm", Bridge: "vmbr99",
		RTO: ptr(12*time.Minute + 4*time.Second), RPO: ptr(5*time.Hour + 12*time.Minute),
		Nodes: []DRNode{
			{Hostname: "web-01", SourceVMID: 201, SandboxVMID: 9901, BackupTime: at.Add(-5 * time.Hour), Restore: ptr(3*time.Minute + 10*time.Second), Boot: ptr(41 * time.Second)},
			{Hostname: "web-02", SourceVMID: 202, SandboxVMID: 9902, BackupTime: at.Add(-5*time.Hour - 12*time.Minute)},
		},
		VIPs: []DRVIP{{Address: "10.0.20.100", Holders: []string{"web-01"}, Checked: true, Probe: "HTTP / geeft 200", ProbeOK: ptr(true)}},
	}
	c.DR.Last, c.DR.Measured = run, run
	c.DR.Scenarios = []DRScenario{{Name: "Web DR", Schedule: "monthly", Bridge: "vmbr99"}, {Name: "Met de hand", Bridge: "vmbr98"}}
	contains(recovery(c, o),
		"Gemeten in de sandbox op 3 november 2026, met de DR-repetitie 'Web DR'.",
		"| **RTO**, hersteltijd | 12 min 4 s | hoogstens 60 minuten | ja |",
		"| **RPO**, gegevensverlies | 5 u 12 min | hoogstens 26 uur | ja |",
		"op pve1 (Thuislab) en storage `local-lvm`, met de netwerkkaarten aan de afgesloten bridge `vmbr99`.",
		"| **web-01** | VM 201 | 2 november 2026 om 22:20 | 3 min 10 s | 41 s |",
		"| **web-02** | VM 202 | 2 november 2026 om 22:08 | niet gemeten | niet gemeten |",
		"`10.0.20.100` had in de sandbox één houder, web-01, en de probe (HTTP / geeft 200) slaagde.",
		"'Web DR' op de afgesloten bridge `vmbr99`: in het eerste testvenster van elke maand (zondag 03:00-05:00).",
		"'Met de hand' op de afgesloten bridge `vmbr98`: alleen met de hand.")
	contains(runbooks(c, o),
		"qmrestore pbs:backup/vm/1 201 --force 1 --storage local-lvm",
		"qmrestore pbs:backup/vm/2 202 --force 1 --storage local-lvm",
		"Moet het hele cluster terug, voer dan de commando's van alle nodes tegelijk uit. Controleer daarna of elk VIP precies één houder heeft. "+
			"Zo deed de DR-repetitie het op 3 november 2026 in de sandbox: na 12 min 4 s werkte het hele cluster weer.",
		"In de DR-repetitie van 3 november 2026 duurde terugzetten 3 min 10 s en opstarten 41 s.")

	// Een strenger doel, en daarna een mislukte repetitie.
	c.DR.RPOHours, c.DR.Default = 4, false
	c.DR.Last = &DRRun{ID: uuid.New(), Name: "Web DR", Result: "fail", Summary: "web-02 startte niet in de sandbox.", At: at.AddDate(0, 0, 7)}
	got := recovery(c, o)
	contains(got,
		"Het doel is een RTO van hoogstens 60 minuten en een RPO van hoogstens 4 uur.",
		"| **RPO**, gegevensverlies | 5 u 12 min | hoogstens 4 uur | **nee** |",
		"De laatste repetitie, 'Web DR' op 10 november 2026, is mislukt. web-02 startte niet in de sandbox.")
	if strings.Contains(got, "de standaard voor prod") {
		t.Error("een eigen doel heet de standaard")
	}
	c.DR.Measured = nil
	contains(recovery(c, o), "Er is nog geen DR-repetitie geslaagd, dus de hersteltijd (RTO) van het hele cluster is niet gemeten.")
	c.DR.RTOMinutes, c.DR.RPOHours = 0, 0
	contains(recovery(c, o), "Dit cluster heeft geen doel voor de hersteltijd (RTO) en het gegevensverlies (RPO).")
}
