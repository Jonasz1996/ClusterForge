package docs

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/kube"
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

func TestSummaryText(t *testing.T) {
	raw := "Ik lees eerst de hoofdstukken.\r\n\r\n# In het kort\n\nWeb is de website, met **twee nodes** achter `10.0.20.100`.\n\n" +
		"* Zie [het runbook](https://evil.example/x) en ![logo](https://evil.example/pixel.png?d=geheim).\n" +
		"* Een <img src=x onerror=alert(1)> en <https://evil.example/a>.\n" +
		"1) Back-ups: dagelijks, `local_lvm` op pve1_a.\n\n" +
		"| Node | Host |\n|---|---|\n| web-01 | pve1 |\n\n```\nrm -rf /\n```\n\n---\n> Let op: één host.\n" +
		"Een `open backtick zonder einde"
	got := SummaryText(raw)
	for _, want := range []string{
		"Ik lees eerst de hoofdstukken.\n\n**In het kort**\n\nWeb is de website, met **twee nodes** achter `10.0.20.100`.",
		"- Zie het runbook en .",
		`- Een \<img src=x onerror=alert(1)\> en https://evil.example/a.`,
		"1. Back-ups: dagelijks, `local_lvm` op pve1\\_a.",
		"Node · Host\nweb-01 · pve1",
		"rm -rf /",
		"Let op: één host.",
		"Een \\`open backtick zonder einde",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mist %q in\n%s", want, got)
		}
	}
	for _, bad := range []string{"](", "![", "Een <img", "pixel.png", "```", "|", "\n\n\n", "---", "\r"} {
		if strings.Contains(got, bad) {
			t.Errorf("bevat %q:\n%s", bad, got)
		}
	}
	long := SummaryText(strings.Repeat("Een lange regel tekst.\n", 300))
	if n := len([]rune(long)); n > MaxSummary+2 || !strings.HasSuffix(long, " …") {
		t.Errorf("afgekapt: %d tekens, einde %q", n, long[len(long)-10:])
	}
	if SummaryText("  \n![x](y)\n") != "" {
		t.Error("alleen een afbeelding geeft geen samenvatting")
	}
}

func TestSummaryInArchitecture(t *testing.T) {
	f := fleet()
	f.Clusters[0].Summary = &Summary{
		Text: SummaryText("Web is de website.\n\n- Twee nodes op twee hosts."), Model: "claude-sonnet-5-5",
		At: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC), By: "jonas",
	}
	o := Opts{Loc: time.UTC, Usernames: true}
	arch := Chapter(&f.Clusters[0], KindArchitecture, o)
	want := "## In het kort\n\nWeb is de website.\n\n- Twee nodes op twee hosts.\n\n" +
		"*Samengevat door de assistent (Claude Sonnet 5.5) op 11 oktober 2026, op verzoek van jonas. Bij twijfel gelden de hoofdstukken zelf.*\n\n" +
		"## Wat het doet"
	if !strings.Contains(arch, want) {
		t.Fatalf("architectuur:\n%s", arch)
	}
	if strings.Index(arch, "| **Omgeving**") > strings.Index(arch, "## In het kort") {
		t.Error("de samenvatting staat voor de feiten")
	}
	if got := Chapter(&f.Clusters[0], KindArchitecture, Opts{Loc: time.UTC}); strings.Contains(got, "jonas") {
		t.Error("zonder gebruikersnamen noemt de samenvatting wie haar vroeg")
	}
	// Bij een nieuwe samenvatting leest de assistent zijn vorige niet.
	without := WithoutSummary(arch)
	if strings.Contains(without, "In het kort") || strings.Contains(without, "Samengevat") || !strings.Contains(without, "## Wat het doet") ||
		!strings.Contains(without, "| **Omgeving**") {
		t.Fatalf("zonder samenvatting:\n%s", without)
	}
	if WithoutSummary("# Titel\n\nTekst\n") != "# Titel\n\nTekst\n" {
		t.Error("zonder samenvatting verandert er niets")
	}
	if got := modelText("claude-opus-5-5-20261001, claude-opus-4-8"); got != "Claude Opus 5.5, Claude Opus 4.8" {
		t.Errorf("modelText = %q", got)
	}
}

// kubeCluster is het cluster Leren: één server, twee workers, een app met
// een Ingress en een volume op local-path.
func kubeCluster(env string) Cluster {
	return Cluster{
		ID: uuid.MustParse("77777777-7777-7777-7777-777777777777"), Name: "Leren", Slug: "leren", Env: env, Type: "kubernetes",
		Template: &Template{Name: "kubernetes", Version: "1.0.0", Available: true},
		VIPs:     []VIP{{Address: "10.0.40.100"}},
		Kube: &Kube{
			Synced: true, Version: "v1.35.9+k3s1", TemplateK3s: "v1.35.9+k3s1", NextVersion: "1.1.0", NextK3s: "v1.36.5+k3s1",
			Init: "leren-server-01", Snapshots: 5, Fresh: "ok",
			Nodes: []KubeNode{
				{Name: "leren-server-01", Hostname: "leren-server-01", Server: true, Kubelet: "v1.35.9+k3s1", IP: "10.0.40.11", CPUMillis: 2000, MemoryBytes: 4 << 30},
				{Name: "leren-worker-01", Hostname: "leren-worker-01", Kubelet: "v1.35.9+k3s1", IP: "10.0.40.12", CPUMillis: 1500},
			},
			Apps: []KubeApp{
				{Namespace: "demo", Kind: "Deployment", Name: "web", Desired: 2, Images: []string{"nginx:1.29"}},
				{Namespace: "kube-system", Kind: "Deployment", Name: "traefik", System: true, Desired: 1},
				{Namespace: "kube-system", Kind: "Deployment", Name: "coredns", System: true, Desired: 1},
			},
			Inventory: &kube.Inventory{
				Namespaces: []string{"clusterforge", "default", "demo", "kube-system"},
				Addresses: []kube.InventoryAddress{
					{Namespace: "demo", Kind: "Ingress", Name: "web", URL: "http://web.10.0.40.100.nip.io/", Host: "web.10.0.40.100.nip.io", Path: "/", Service: "web", Port: "80"},
					{Namespace: "kube-system", Kind: "Service", Name: "traefik", URL: "http://10.0.40.11/", Text: "LoadBalancer op 10.0.40.11, poorten 80, 443"},
				},
				Volumes: []kube.InventoryVolume{{Namespace: "demo", Name: "data", Class: "local-path", Size: "1Gi", Phase: "Bound", Node: "leren-worker-01",
					Path: "/var/lib/rancher/k3s/storage/pvc-1_demo_data", UsedBy: []string{"web-5d9c-x"}}},
				DefaultClass: "local-path",
			},
		},
	}
}

func TestKubernetesChapter(t *testing.T) {
	c := kubeCluster("lab")
	if got := c.kinds(); strings.Join(got, ",") != "architectuur,kubernetes,netwerk,configuratie,cronjobs,herstel,runbooks" {
		t.Fatalf("hoofdstukken: %v", got)
	}
	if !Valid(ScopeCluster, KindKubernetes) || len(Kinds("web", false)) != 6 {
		t.Fatal("kubernetes is een hoofdstuk van een cluster, alleen bij Kubernetes")
	}
	md := Chapter(&c, KindKubernetes, Opts{Loc: time.UTC})
	for _, want := range []string{
		"# Leren: kubernetes",
		"[Architectuur](architectuur.md) · **Kubernetes** · [Netwerk](netwerk.md)",
		"| **Versie** | k3s `v1.35.9` |",
		"| **Ondersteuning** | Kubernetes 1.35 krijgt updates tot februari 2027 |",
		"| **Bijwerken** | naar k3s `v1.36.5` met `kubernetes` 1.1.0; zie hieronder |",
		"| **API** | `https://10.0.40.100:6443`, via het VIP |",
		"| **leren-server-01** | server | leren-server-01 | 10.0.40.11 | `v1.35.9` | 2 cores | 4,0 GB |",
		"| **leren-worker-01** | worker | leren-worker-01 | 10.0.40.12 | `v1.35.9` | 1,5 cores | – |",
		"Eigen namespaces: `demo`.",
		"Van Kubernetes en ClusterForge: `clusterforge` (de kubeconfigs van ClusterForge), `default` en `kube-system`.",
		"| **demo/web** | Deployment | 2 | `nginx:1.29` |",
		"Van k3s zelf, in `kube-system`: `coredns` en `traefik`.",
		"| `http://web.10.0.40.100.nip.io/` | Ingress demo/web | Service `web`, poort 80 |",
		"| **demo/data** | local-path | 1Gi | op leren-worker-01 in `/var/lib/rancher/k3s/storage/pvc-1_demo_data` | web-5d9c-x |",
		"Een claim zonder klasse krijgt `local-path`.",
		"Er zijn er 5; de nieuwste is minder dan een dag oud.",
		"Alleen een snapshot op leren-server-01 kan terug",
		"Met één server is die server het cluster",
		"**Bijwerken naar k3s v1.36.5…**",
		"ClusterForge werkt de nodes één voor één bij, eerst leren-server-01, dan leren-worker-01.",
		"Kies in ClusterForge bij Kubernetes, tabblad Snapshots, **Terugzetten** bij een snapshot op leren-server-01.",
		"--cluster-reset-restore-path=/var/lib/rancher/k3s/server/db/snapshots/<bestand>",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("mist %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "**5.**") {
		t.Error("met één server is er geen stap voor de andere servers")
	}

	// Op prod met drie servers: geen Terugzetten, wel de stap voor de
	// andere servers.
	c = kubeCluster("prod")
	c.GitLinked = true
	c.Kube.Nodes = append(c.Kube.Nodes,
		KubeNode{Name: "leren-server-02", Hostname: "leren-server-02", Server: true}, KubeNode{Name: "leren-server-03", Hostname: "leren-server-03", Server: true})
	md = Chapter(&c, KindKubernetes, Opts{Loc: time.UTC})
	for _, want := range []string{
		"Op prod zet ClusterForge geen snapshot terug",
		"**5.** Zet op leren-server-02 en leren-server-03 de database opzij",
		"Een server van 3 mag uitvallen",
		"Dit cluster volgt Git: verander het aantal nodes in `leren-cluster.yaml`",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("prod: mist %q in:\n%s", want, md)
		}
	}
	rb := Chapter(&c, KindRunbooks, Opts{Loc: time.UTC})
	if !strings.Contains(rb, "[Kubernetes](kubernetes.md#een-node-vervangen)") || strings.Contains(rb, "Een node uit een cluster halen kan ClusterForge nog niet") ||
		!strings.Contains(rb, "maakt ClusterForge de node eerst leeg in Kubernetes") {
		t.Errorf("runbooks:\n%s", rb)
	}
	// Een ander cluster heeft geen hoofdstuk Kubernetes.
	f := fleet()
	if Chapter(&f.Clusters[0], KindKubernetes, Opts{}) != "" || len(RenderCluster(&f.Clusters[0], Opts{})) != 6 {
		t.Error("hoofdstuk Kubernetes bij een ander cluster")
	}
	if n := len(RenderCluster(&c, Opts{})); n != 7 {
		t.Errorf("hoofdstukken van Leren: %d", n)
	}
}

// dbCluster is Nextcloud met MariaDB Galera op nc1 tot nc3 achter het VIP
// .185, en de oude MariaDB-server die ClusterForge alleen leest.
func dbCluster() Cluster {
	return Cluster{
		ID: uuid.MustParse("88888888-8888-8888-8888-888888888888"), Name: "Nextcloud", Slug: "nextcloud", Env: "prod", Type: "keepalived",
		VIPs: []VIP{{Address: "10.0.20.185"}},
		DB: &ClusterDB{
			Components: []DBComponent{{
				Title: "MariaDB Galera", Engine: "mariadb", Version: "11.8.3-MariaDB", Port: 3306, VIP: "10.0.20.185",
				Data: []string{"nc1", "nc2", "nc3"}, AutoRecover: true, BackupOn: true, BackupAt: "02:00", KeepDays: 7,
				Databases: []DBDatabase{
					{Name: "nextcloud", SizeBytes: 2468 << 20, Tables: 121, Users: []string{"Nextcloud (nc1, nc2, nc3) als nextcloud"}, Dump: "ok"},
					{Name: "wiki", SizeBytes: 40 << 20, Tables: 12, Dump: "missing"},
				},
			}},
			Servers: []DBServers{{Engine: "postgresql", Kind: "single", Version: "17.6", Nodes: []string{"nc1"},
				Databases: []DBDatabase{{Name: "zabbix", SizeBytes: 900 << 20, Tables: 180, Users: []string{"de databasenode zelf (nc1) als zabbix"}}}}},
		},
	}
}

func TestDatabaseChapter(t *testing.T) {
	c := dbCluster()
	if got := strings.Join(c.kinds(), ","); got != "architectuur,database,netwerk,configuratie,cronjobs,herstel,runbooks" {
		t.Fatalf("hoofdstukken: %s", got)
	}
	if got := strings.Join(Kinds("kubernetes", true), ","); got != "architectuur,kubernetes,database,netwerk,configuratie,cronjobs,herstel,runbooks" || !Valid(ScopeCluster, KindDatabase) {
		t.Fatalf("met Kubernetes: %s", got)
	}
	md := Chapter(&c, KindDatabase, Opts{Loc: time.UTC})
	for _, want := range []string{
		"# Nextcloud: database",
		"[Architectuur](architectuur.md) · **Database** · [Netwerk](netwerk.md)",
		"Nextcloud heeft MariaDB Galera op nc1, nc2 en nc3, uitgerold en beheerd door ClusterForge. Daarnaast draait er PostgreSQL op nc1; ClusterForge leest die server, maar beheert hem niet.",
		"| **Versie** | MariaDB `11.8.3-MariaDB` |",
		"| **Adres voor de app** | `10.0.20.185:3306`, via het VIP van de app, dat alleen op een node staat die mag schrijven |",
		"| **Galera-cluster** | `nextcloud` |",
		"| **Dumps** | elke nacht om 02:00, 7 dagen bewaard, in `/var/backups/clusterforge-db` op een datanode zonder het VIP |",
		"| **Na een volledige uitval** | ClusterForge begint het cluster vanzelf opnieuw op de node met de nieuwste data |",
		"| **nextcloud** | 2,4 GB | 121 | Nextcloud (nc1, nc2, nc3) als nextcloud | goed |",
		"| **wiki** | 40 MB | 12 | – | nog geen |",
		"galera_new_cluster",
		"**4.** Kijk of alle nodes meedoen: `wsrep_cluster_size` is 3.",
		"`nextcloud_herstel_<datum>`",
		"zstd -dc /var/backups/clusterforge-db/nextcloud/<bestand>.sql.zst | mariadb nextcloud",
		"De app schrijft naar de node met het VIP `10.0.20.185`.",
		"## PostgreSQL op nc1",
		"| **Soort** | losse server |",
		"| **zabbix** | 900 MB | 180 | de databasenode zelf (nc1) als zabbix |",
		"verhuis haar dan naar MariaDB Galera.",
		"zoals tabellen zonder primaire sleutel of buiten InnoDB.",
		"4. Wijs de app naar `10.0.20.185`, met dezelfde gebruiker en hetzelfde wachtwoord",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Galera: mist %q in:\n%s", want, md)
		}
	}
	for _, chapter := range []string{KindRecovery, KindRunbooks} {
		if !strings.Contains(Chapter(&c, chapter, Opts{Loc: time.UTC}), "het hoofdstuk [Database](database.md)") {
			t.Errorf("%s linkt niet naar het hoofdstuk Database", chapter)
		}
	}

	// PostgreSQL met Patroni op twee datanodes en een scheidsrechter, met
	// een eigen VIP.
	cp := &c.DB.Components[0]
	cp.Engine, cp.Title, cp.Port, cp.Version, cp.OwnVIP, cp.VIP = "postgresql", "PostgreSQL met Patroni", 5432, "17.6", true, "10.0.20.190"
	cp.Data, cp.Arbiters = []string{"nc1", "nc2"}, []string{"nc3"}
	c.DB.Servers = nil
	md = Chapter(&c, KindDatabase, Opts{Loc: time.UTC})
	for _, want := range []string{
		"| **Nodes** | nc1 en nc2; scheidsrechter nc3 (alleen etcd) |",
		"| **Adres voor de app** | `10.0.20.190:5432`, via het VIP, dat alleen op de primary staat |",
		"| **Patroni-cluster (scope)** | `nextcloud` |",
		"in `/var/backups/clusterforge-db` op de primary |",
		"kijk dan als root op nc1 en nc2, en naar etcd op de scheidsrechter nc3:",
		"patronictl -c /etc/patroni/clusterforge.yml reinit nextcloud nc2 --force'",
		"runuser -u postgres -- createdb -O <eigenaar> nextcloud_terug",
		"switchover nextcloud --candidate nc2 --force'",
		"Het plan verandert niets en toont wat in de weg staat.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Patroni: mist %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "Na een volledige uitval") || strings.Contains(md, "galera") {
		t.Errorf("Patroni noemt Galera:\n%s", md)
	}

	// Een cluster met alleen een server die ClusterForge leest.
	c = dbCluster()
	c.DB.Components = nil
	md = Chapter(&c, KindDatabase, Opts{Loc: time.UTC})
	if !strings.Contains(md, "Er draait PostgreSQL op nc1; ClusterForge leest die server, maar beheert hem niet.") || strings.Contains(md, "Proefverhuizing") ||
		strings.Contains(Chapter(&c, KindRunbooks, Opts{Loc: time.UTC}), "het hoofdstuk [Database](database.md)") {
		t.Errorf("alleen lezen:\n%s", md)
	}
	c.DB = &ClusterDB{}
	if md = Chapter(&c, KindDatabase, Opts{Loc: time.UTC}); !strings.Contains(md, "ClusterForge ziet geen database op de nodes van Nextcloud.") {
		t.Errorf("leeg:\n%s", md)
	}
	c.DB = nil
	if Chapter(&c, KindDatabase, Opts{}) != "" || strings.Join(c.kinds(), ",") != strings.Join(ClusterKinds, ",") {
		t.Error("zonder database is er geen hoofdstuk Database")
	}
}

func TestRoughSize(t *testing.T) {
	for in, want := range map[int64]string{
		512 << 10: "510 kB", 1536 << 10: "1,5 MB", 40 << 20: "40 MB", 2468 << 20: "2,4 GB", 123 << 30: "120 GB", 3 << 40: "3,0 TB",
	} {
		if got := roughSize(in); got != want {
			t.Errorf("%d: %s, verwacht %s", in, got, want)
		}
	}
}

func TestCronjobsChapter(t *testing.T) {
	c := dbCluster()
	c.Cron = ClusterCron{
		Managed: []CronManaged{
			{ID: "1b2c", Name: "opruimen", Schedule: "0 */6 * * *", Describe: "elke 6 uur, op minuut 0", RunOn: "vip", User: "www-data",
				Command: "php occ trashbin:cleanup --all-users", Enabled: true},
			{ID: "3d4e", Name: "data naar backup", Schedule: "0 */4 * * *", RunOn: "vip", User: "root", Enabled: false,
				Sync: &CronSync{Source: "/srv/data/", Target: "/srv/kopie/", ToCluster: "Backup", Delete: true, Exclude: []string{"*.part"}, BwLimitMB: 20}},
		},
		Found: []CronFound{
			{Source: "cron.d", File: "/etc/cron.d/php", Line: 3, User: "root", Schedule: "09,39 * * * *", Command: "/usr/lib/php/sessionclean",
				Package: "php8.2-common", Nodes: []string{"nc1", "nc2", "nc3"}},
			{Source: "user", File: "/var/spool/cron/crontabs/root", Line: 2, User: "root", Schedule: "0 2 * * *", Describe: "elke dag om 02:00",
				Command: "rsync -a /srv/data/ root@nc2:/srv/data/", Nodes: []string{"nc1"}, Off: []string{"nc1"}},
		},
	}
	md := Chapter(&c, KindCronjobs, Opts{Loc: time.UTC})
	for _, want := range []string{
		"# Nextcloud: cronjobs",
		"| opruimen | elke 6 uur, op minuut 0 `0 */6 * * *` | node met het VIP | `php occ trashbin:cleanup --all-users` als www-data | aan |",
		"| data naar backup |",
		"sync `/srv/data/` naar alle nodes van Backup, in `/srv/kopie/` | uit |",
		"### Sync data naar backup",
		"Wat op de bron weg is, gaat ook op het doel weg (`--delete`).",
		"Niet mee: `*.part`.",
		"Hoogstens 20 MB/s.",
		"uitgezet door ClusterForge op nc1",
		"(pakket php8.2-common)",
		"### Een sync terugdraaien",
		"[Herstel](herstel.md)",
		"opruimen: `1b2c`",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("mist %q in:\n%s", want, md)
		}
	}
	// Eerst wat Jonas zelf maakte.
	if strings.Index(md, "rsync -a") > strings.Index(md, "sessionclean") {
		t.Error("de job uit een pakket staat voor de eigen job")
	}
	c.Cron = ClusterCron{}
	md = Chapter(&c, KindCronjobs, Opts{Loc: time.UTC})
	if !strings.Contains(md, "Geen. Een job of bestandssync") || !strings.Contains(md, "geen andere cronjobs of timers") {
		t.Errorf("zonder jobs:\n%s", md)
	}
}
