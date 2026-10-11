package risk

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) *time.Time {
	t := now.Add(-d)
	return &t
}

func fptr(f float64) *float64 { return &f }

// healthy is een gezond prod-cluster met twee nodes op twee hosts, een VIP
// met een recente geslaagde failovertest, verse en gecontroleerde back-ups
// en geen drift.
func healthy() ClusterInput {
	n1, n2, vip, test := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	node := func(id uuid.UUID, name, host string) NodeInput {
		return NodeInput{
			ID: id, Hostname: name, Lifecycle: "active", Host: host, HasFacts: true,
			OSID: "debian", OSVersion: "13", OSName: "Debian GNU/Linux 13 (trixie)", ProtocolVersion: protocol.Version,
			Filesystems: []FSInput{{Mount: "/", Size: 20 << 30, Used: 5 << 30}},
			Metrics:     &NodeMetrics{CPUP95: fptr(30), MemoryP95: fptr(50), SwapAvg: fptr(0), NetErrors: fptr(0)},
		}
	}
	return ClusterInput{
		ID: uuid.New(), Name: "web-prod", Env: "prod", Status: "healthy",
		Nodes:   []NodeInput{node(n1, "web-01", "pve1"), node(n2, "web-02", "pve2")},
		VIPs:    []VIPInput{{ID: vip, Address: "10.0.20.10", Candidates: []uuid.UUID{n1, n2}}},
		History: []Transition{{At: now.Add(-60 * day), To: "healthy"}},
		Since:   now.Add(-90 * day),
		Backups: []BackupInput{
			{NodeID: n1, Hostname: "web-01", Freshness: "ok", Latest: ago(time.Hour), MaxAgeHours: 30, Verification: "pass"},
			{NodeID: n2, Hostname: "web-02", Freshness: "ok", Latest: ago(time.Hour), MaxAgeHours: 30, Verification: "pass"},
		},
		LastVerified: ago(3 * day),
		FailoverTests: []FailoverInput{{ID: test, Name: "VIP naar web-02", VIPID: vip,
			LastRun: &RunInput{ID: uuid.New(), Name: "VIP naar web-02", Result: "pass", Finished: now.Add(-5 * day)}}},
		DesiredState: true, SpecRevision: 3, AppliedRevision: 3,
		MetricsRead: true,
	}
}

func findings(r Result) []Finding {
	var out []Finding
	for _, c := range r.Cards {
		out = append(out, c.Findings...)
	}
	return out
}

func find(t *testing.T, r Result, check string) Finding {
	t.Helper()
	for _, f := range findings(r) {
		if f.Check == check {
			return f
		}
	}
	t.Fatalf("geen bevinding %s in %+v", check, findings(r))
	return Finding{}
}

func card(r Result, id CardID) Card {
	for _, c := range r.Cards {
		if c.ID == id {
			return c
		}
	}
	return Card{}
}

func TestHealthy(t *testing.T) {
	r := Score(healthy(), nil, now)
	if fs := findings(r); len(fs) != 0 {
		t.Fatalf("bevindingen in een gezond cluster: %+v", fs)
	}
	if r.Score == nil || *r.Score != 100 || r.Grade != "A" || r.Cap != nil || len(r.Cards) != 7 {
		t.Fatalf("uitkomst: %+v", r)
	}
	for _, c := range r.Cards {
		if c.Score == nil || *c.Score != 100 {
			t.Errorf("kaart %s: %v", c.ID, c.Score)
		}
	}
}

// De voorbeelden uit het ontwerp, met hun zin en ernst.
func TestDesignExamples(t *testing.T) {
	in := healthy()
	in.Nodes[1].Host = "pve1"
	f := find(t, Score(in, nil, now), "redundancy.same_host")
	if f.Severity != High || f.Text != "web-01 en web-02 draaien allebei op pve1: valt pve1 uit, dan is web-prod down" || f.Card != Redundancy {
		t.Errorf("zelfde host op prod: %+v", f)
	}
	in.Env = "lab"
	if f := find(t, Score(in, nil, now), "redundancy.same_host"); f.Severity != Low {
		t.Errorf("zelfde host op lab: %s", f.Severity)
	}

	in = healthy()
	in.FailoverTests[0].LastRun.Finished = now.Add(-41 * day)
	f = find(t, Score(in, nil, now), "recovery.stale")
	if f.Severity != Medium || f.Text != "Laatste failovertest 'VIP naar web-02' is 41 dagen geleden" {
		t.Errorf("oude failovertest: %+v", f)
	}

	in = healthy()
	in.Nodes[0].Hostname = "db-01"
	in.Nodes[0].SecurityUpdates, in.Nodes[0].SecuritySince = 3, ago(9*day+time.Hour)
	f = find(t, Score(in, nil, now), "maintenance.security_updates")
	if f.Severity != High || f.Text != "3 beveiligingsupdates open op db-01, al 9 dagen" {
		t.Errorf("beveiligingsupdates: %+v", f)
	}

	in = healthy()
	in.Nodes[1].Metrics.DiskDaysLeft = map[string]float64{"/": 11.4, "/var": 40}
	f = find(t, Score(in, nil, now), "capacity.disk_trend")
	if f.Severity != High || !strings.HasPrefix(f.Text, "Schijf / op web-02 is vol over ongeveer 11 dagen") {
		t.Errorf("schijftrend: %+v", f)
	}

	in = healthy()
	in.Nodes[0].OSVersion, in.Nodes[0].OSName = "11", "Debian 11"
	f = find(t, Score(in, nil, now), "maintenance.os_eol")
	if f.Severity != High || f.Text != "web-01 draait Debian 11, sinds augustus 2026 end-of-life" {
		t.Errorf("end-of-life: %+v", f)
	}
}

func TestPenaltiesAndGrade(t *testing.T) {
	in := healthy()
	// Drie zware bevindingen op Onderhoud: de kaart gaat niet onder 0.
	in.Nodes[0].Certificates = []CertInput{{Path: "/etc/nginx/a.pem", Name: "a.example", NotAfter: now.Add(-day)}}
	in.Nodes[1].Certificates = []CertInput{{Path: "/etc/nginx/b.pem", Name: "b.example", NotAfter: now.Add(-day)}}
	in.Nodes[0].OSVersion = "10"
	r := Score(in, nil, now)
	m := card(r, Maintenance)
	if *m.Score != 0 {
		t.Fatalf("Onderhoud: %d", *m.Score)
	}
	// 100 overal behalve Onderhoud (gewicht 10 van 100): 90.
	if *r.Score != 90 || r.Grade != "A" {
		t.Fatalf("totaal %d %s", *r.Score, r.Grade)
	}
	// Samen 40+40+20 = 100 eraf. Zonder één certificaat is het 60 eraf: de
	// kaart haalt 40 en het totaal stijgt 4. Zonder de EOL haalt ze 20.
	for _, f := range m.Findings {
		if f.Check == "maintenance.certificate" && f.Gain != 4 {
			t.Errorf("opbrengst certificaat: %v", f.Gain)
		}
		if f.Check == "maintenance.os_eol" && f.Gain != 2 {
			t.Errorf("opbrengst EOL: %v", f.Gain)
		}
	}
	for score, grade := range map[int]string{100: "A", 90: "A", 89: "B", 80: "B", 79: "C", 70: "C", 69: "D", 60: "D", 59: "E", 50: "E", 49: "F", 0: "F"} {
		if g := Grade(score); g != grade {
			t.Errorf("Grade(%d) = %s", score, g)
		}
	}
}

func TestCaps(t *testing.T) {
	in := healthy()
	in.Status, in.StatusReason = "down", "geen VIP-eigenaar"
	r := Score(in, nil, now)
	if r.Cap == nil || r.Cap.Max != 40 || *r.Score != 40 || r.Grade != "F" {
		t.Fatalf("down: %+v", r)
	}
	if f := find(t, r, "availability.status"); f.Severity != Critical || f.Text != "web-prod is nu down: geen VIP-eigenaar" {
		t.Errorf("status: %+v", f)
	}

	in = healthy()
	for i := range in.Backups {
		in.Backups[i].Freshness, in.Backups[i].Latest = "missing", nil
	}
	r = Score(in, nil, now)
	if r.Cap == nil || r.Cap.Max != 60 || *r.Score > 60 {
		t.Fatalf("prod zonder back-up: %+v", r)
	}
	if f := find(t, r, "backups.missing"); f.Severity != Critical {
		t.Errorf("ernst: %s", f.Severity)
	}
	// Zijn alle ontbrekende back-ups geaccepteerd, dan geldt het plafond
	// niet meer.
	acc := map[Key]Acceptance{}
	for _, b := range in.Backups {
		acc[Key{"backups.missing", "node", b.NodeID}] = Acceptance{Reason: "stateless"}
	}
	if r := Score(in, acc, now); r.Cap != nil {
		t.Errorf("plafond na acceptatie: %+v", r.Cap)
	}
	// Op test geldt het plafond niet.
	in.Env = "test"
	if r := Score(in, nil, now); r.Cap != nil {
		t.Errorf("plafond op test: %+v", r.Cap)
	}
}

func TestUnknownCards(t *testing.T) {
	in := healthy()
	in.Backups, in.VIPs, in.FailoverTests = nil, nil, nil
	in.MetricsRead = false
	for i := range in.Nodes {
		in.Nodes[i].Metrics = nil
	}
	in.Nodes[0].Filesystems = []FSInput{{Mount: "/", Size: 10 << 30, Used: 9 << 30}}
	r := Score(in, nil, now)
	if c := card(r, Backups); c.Score != nil || !strings.Contains(c.Note, "gekoppeld") {
		t.Errorf("back-ups: %+v", c)
	}
	if c := card(r, Recovery); c.Score != nil {
		t.Errorf("herstelbaarheid: %+v", c)
	}
	// Zonder metrics telt de schijf uit de facts nog wel.
	c := card(r, Capacity)
	if c.Score == nil || *c.Score != 90 || !strings.Contains(c.Note, "Zonder metrics") {
		t.Errorf("capaciteit: %+v", c)
	}
	// De onbekende kaarten tellen niet mee: (25*100 + 15*100 + 10*100 +
	// 10*100 + 5*90) / 65.
	if *r.Score != 99 {
		t.Errorf("totaal: %d", *r.Score)
	}
	in.Status, in.History = "unknown", nil
	in.Nodes = nil
	r = Score(in, nil, now)
	if r.Score == nil {
		// Configuratie is altijd bekend.
		t.Fatalf("geen score: %+v", r)
	}
	if c := card(r, Availability); c.Score != nil {
		t.Errorf("beschikbaarheid zonder status: %+v", c)
	}
}

func TestAvailability(t *testing.T) {
	in := healthy()
	// Twintig dagen geleden 8 uur down, vijf dagen geleden 1 uur
	// split-brain.
	in.History = []Transition{
		{At: now.Add(-40 * day), To: "healthy"},
		{At: now.Add(-20 * day), To: "down"},
		{At: now.Add(-20*day + 8*time.Hour), To: "degraded"},
		{At: now.Add(-20*day + 9*time.Hour), To: "healthy"},
		{At: now.Add(-5 * day), To: "split_brain"},
		{At: now.Add(-5*day + time.Hour), To: "healthy"},
	}
	in.Incidents = 4
	in.VIPMoves = map[uuid.UUID]int{in.VIPs[0].ID: 9}
	r := Score(in, nil, now)
	// 9 uur van 720: 98,75 %, op prod een stap zwaarder dan hoog.
	f := find(t, r, "availability.uptime")
	if f.Text != "Beschikbaarheid over 30 dagen: 98,7 % (9 u down)" || f.Severity != Critical {
		t.Errorf("beschikbaarheid: %+v", f)
	}
	if f := find(t, r, "availability.incidents"); f.Severity != Medium || f.Text != "4 incidenten in de laatste 30 dagen" {
		t.Errorf("incidenten: %+v", f)
	}
	if f := find(t, r, "availability.vip_moves"); f.Severity != Medium || f.Subject.Type != "vip" {
		t.Errorf("VIP-wissels: %+v", f)
	}

	// Een cluster van tien dagen oud rekent over tien dagen.
	in = healthy()
	in.Since = now.Add(-10 * day)
	in.History = []Transition{{At: now.Add(-10 * day), To: "healthy"}, {At: now.Add(-2 * day), To: "down"}, {At: now.Add(-2*day + 2*time.Hour), To: "healthy"}}
	if f := find(t, Score(in, nil, now), "availability.uptime"); !strings.HasPrefix(f.Text, "Beschikbaarheid over 10 dagen: 99,16 %") {
		t.Errorf("jong cluster: %+v", f)
	}

	down, measured := downtime([]Transition{{At: now.Add(-3 * time.Hour), To: "down"}}, now.Add(-5*time.Hour), now)
	if down != 3*time.Hour || measured != 3*time.Hour {
		t.Errorf("onbekend begin: %v %v", down, measured)
	}
}

func TestAcceptance(t *testing.T) {
	in := healthy()
	in.Nodes[1].Host = "pve1"
	acc := map[Key]Acceptance{{"redundancy.same_host", "cluster", in.ID}: {Reason: "Eén host is genoeg", By: "jonas"}}
	r := Score(in, acc, now)
	f := find(t, r, "redundancy.same_host")
	if f.Accepted == nil || f.Points != 0 || f.Gain != 0 || *card(r, Redundancy).Score != 100 || *r.Score != 100 {
		t.Errorf("geaccepteerd: %+v, %d", f, *r.Score)
	}
}

func TestChecks(t *testing.T) {
	in := healthy()
	n := &in.Nodes[0]
	n.Lifecycle, n.LifecycleSince = "maintenance", ago(8*day)
	n.RebootRequired, n.Kernel, n.KernelInstalled = true, "6.12.30-amd64", "6.12.38-amd64"
	n.AptUpdatedAt = now.Add(-20 * day)
	n.ProtocolVersion = 4
	n.Certificates = []CertInput{
		{Path: "/etc/letsencrypt/live/a/cert.pem", Name: "a.example", NotAfter: now.Add(5 * day)},
		{Path: "/etc/nginx/b.pem", Name: "b.example", NotAfter: now.Add(20 * day)},
		{Path: "/etc/nginx/c.pem", Name: "c.example", NotAfter: now.Add(200 * day)},
	}
	n.Filesystems = append(n.Filesystems, FSInput{Mount: "/var", Size: 10_000_000_000, Used: 9_600_000_000}, FSInput{Mount: "/boot/efi", Size: 1 << 20, Used: 1 << 20})
	n.Metrics.MemoryP95, n.Metrics.CPUP95, n.Metrics.SwapAvg, n.Metrics.NetErrors = fptr(93.4), fptr(97), fptr(62), fptr(4312)
	in.Backups[0].Freshness, in.Backups[0].Latest = "stale", ago(3*day)
	in.Backups[1].PBSFailed, in.Backups[1].Verification = true, "fail"
	in.LastVerified = ago(40 * day)
	in.FailoverTests[0].LastRun.Result, in.FailoverTests[0].LastRun.Summary = "warning", "De overname duurde 14 s; het doel is 5 s."
	in.VIPs = append(in.VIPs, VIPInput{ID: uuid.New(), Address: "10.0.20.11"})
	in.Unrestored = []RunInput{{ID: uuid.New(), Name: "keepalived stoppen", Finished: now.Add(-day)}}
	in.NonProdDeps = []DepInput{{ID: uuid.New(), From: "nginx", To: "postgres", ToCluster: "db-test", ToEnv: "test"}}
	in.Drift = []DriftInput{{NodeID: in.Nodes[1].ID, Hostname: "web-02", Findings: 2, Since: ago(3 * day)}}
	in.WildcardIgnores = []IgnoreInput{{ID: uuid.New(), ExpiresAt: now.Add(10 * day)}}
	in.SpecRevision, in.AppliedRevision, in.RevisionSince = 5, 3, ago(26*time.Hour)
	in.FailedGit = &GitInput{ID: uuid.New(), Commit: "abcdef1234567", Reason: "stap 2 mislukte", At: now.Add(-day)}
	r := Score(in, nil, now)
	want := map[string]struct {
		sev  Severity
		text string
	}{
		"redundancy.maintenance":         {Medium, "web-01 staat al 8 dagen in onderhoud"},
		"maintenance.reboot":             {Medium, "web-01 moet herstarten: kernel 6.12.38-amd64 is geïnstalleerd, 6.12.30-amd64 draait"},
		"maintenance.apt_lists":          {Low, "De pakketlijsten op web-01 zijn al 20 dagen niet ververst; open updates zijn dan niet te zien"},
		"maintenance.agent_old":          {Low, "De agent op web-01 is verouderd; installeer hem opnieuw voor alle controles"},
		"maintenance.certificate":        {High, "Certificaat a.example op web-01 verloopt over 5 dagen (en nog 1 certificaat)"},
		"capacity.disk":                  {High, "Schijf /var op web-01 is 96 % vol"},
		"capacity.memory":                {Medium, "Het geheugen van web-01 zat de afgelopen 7 dagen vaak boven 90 % (p95 93 %)"},
		"capacity.cpu":                   {Medium, "De CPU van web-01 zat de afgelopen 7 dagen vaak boven 90 % (p95 97 %)"},
		"capacity.swap":                  {Low, "web-01 gebruikt gemiddeld 62 % van zijn swap"},
		"capacity.network_errors":        {Low, "web-01 had 4.312 netwerkfouten in 7 dagen"},
		"backups.stale":                  {High, "De nieuwste back-up van web-01 is 3 dagen oud; het beleid is 30 uur"},
		"backups.pbs_failed":             {High, "Proxmox Backup Server kon de nieuwste back-up van web-02 niet verifiëren"},
		"backups.verify_failed":          {High, "De laatste back-upcontrole van web-02 in de sandbox mislukte"},
		"backups.unverified":             {Medium, "De laatste geslaagde back-upcontrole in de sandbox is 40 dagen geleden"},
		"recovery.goal_missed":           {Medium, "Failovertest 'VIP naar web-02' haalde zijn doel niet: De overname duurde 14 s; het doel is 5 s."},
		"recovery.no_test":               {Medium, "VIP 10.0.20.11 heeft geen failovertest"},
		"recovery.unrestored":            {High, "Failovertest 'keepalived stoppen' van " + date(now.Add(-day)) + " is niet hersteld"},
		"redundancy.nonprod_dependency":  {High, "nginx hangt af van postgres in db-test, een test-cluster"},
		"configuration.drift":            {Medium, "Drift op web-02 sinds 3 dagen (2 verschillen)"},
		"configuration.ignore_all":       {Medium, "Alle drift op het hele cluster wordt genegeerd tot " + date(now.Add(10*day))},
		"configuration.revision_pending": {Medium, "Revisie 5 is al 26 u niet toegepast; toegepast is revisie 3"},
		"configuration.git_failed":       {Medium, "De Git-wijziging uit commit abcdef1 mislukte op " + date(now.Add(-day)) + ": stap 2 mislukte"},
	}
	for check, w := range want {
		f := find(t, r, check)
		if f.Severity != w.sev || f.Text != w.text {
			t.Errorf("%s: %s %q, verwacht %s %q", check, f.Severity, f.Text, w.sev, w.text)
		}
		if f.Link == "" || f.Points != Penalty[f.Severity] {
			t.Errorf("%s: link %q, punten %d", check, f.Link, f.Points)
		}
	}
	if got, wantN := len(findings(r)), len(want); got != wantN {
		t.Errorf("%d bevindingen, verwacht %d: %+v", got, wantN, findings(r))
	}
}

func TestSingleNodeAndVIPHost(t *testing.T) {
	in := healthy()
	n3 := NodeInput{ID: uuid.New(), Hostname: "web-03", Lifecycle: "active", Host: "pve2"}
	in.Nodes = append(in.Nodes, n3)
	// Het VIP staat alleen op web-02 en web-03, allebei op pve2.
	in.VIPs[0].Candidates = []uuid.UUID{in.Nodes[1].ID, n3.ID}
	f := find(t, Score(in, nil, now), "redundancy.vip_same_host")
	if f.Text != "De nodes van VIP 10.0.20.10 (web-02 en web-03) draaien allebei op pve2" || f.Severity != High {
		t.Errorf("VIP op één host: %+v", f)
	}

	in = healthy()
	in.Nodes = in.Nodes[:1]
	in.Env = "test"
	if f := find(t, Score(in, nil, now), "redundancy.single_node"); f.Severity != Low {
		t.Errorf("één node: %+v", f)
	}
}

func TestPlatformAndFleet(t *testing.T) {
	pc := PlatformCard(PlatformInput{
		AdminsWithoutTOTP: []UserInput{{ID: uuid.New(), Username: "jonas"}},
		Proxmox: []ProxmoxInput{
			{ID: uuid.New(), Name: "pve", LastSyncAt: ago(time.Minute), CertNotAfter: ago(-12 * day)},
			{ID: uuid.New(), Name: "lab", LastError: "geen verbinding", CreatedAt: now.Add(-day)},
		},
	}, nil, now)
	if pc.Score == nil || *pc.Score != 50 || len(pc.Findings) != 3 {
		t.Fatalf("platform: %+v", pc)
	}
	if pc.Findings[0].Text != "Beheerder jonas heeft geen tweestapsverificatie" && pc.Findings[1].Text != "Beheerder jonas heeft geen tweestapsverificatie" {
		t.Errorf("platform: %+v", pc.Findings)
	}

	prod, lab := healthy(), healthy()
	lab.ID, lab.Env = uuid.New(), "lab"
	lab.Nodes[1].Host = "pve1"
	rp, rl := Score(prod, nil, now), Score(lab, nil, now)
	// Lab: Redundantie 95, dus 99,25, afgerond 99; oplossen levert 0,75 op.
	if *rl.Score != 99 {
		t.Fatalf("lab: %d", *rl.Score)
	}
	scored := []Scored{{prod.ID, "prod", rp}, {lab.ID, "lab", rl}}
	s, g := Fleet(scored, &pc)
	// Clusters: (3*100 + 0,5*99) / 3,5 = 99,86; 0,8*99,86 + 0,2*50 = 89,9.
	if *s != 90 || g != "A" {
		t.Errorf("geheel: %d %s", *s, g)
	}
	f := scored[1].Result.Cards[3].Findings[0]
	if f.Gain != 0.8 || f.FleetGain != round1(0.8*0.8*0.5/3.5) {
		t.Errorf("opbrengst lab: %v %v", f.Gain, f.FleetGain)
	}
	if pc.Findings[0].FleetGain != round1(pc.Findings[0].Gain*0.2) {
		t.Errorf("opbrengst platform: %+v", pc.Findings[0])
	}
	if s, _ := Fleet(nil, &pc); *s != 50 {
		t.Errorf("alleen platform: %d", *s)
	}
	if s, _ := Fleet(nil, nil); s != nil {
		t.Errorf("niets: %v", *s)
	}
}

func TestEOL(t *testing.T) {
	for _, c := range []struct {
		id, version, want string
	}{
		{"debian", "11", "2026-08-31"}, {"Ubuntu", "24.04", "2029-05-31"}, {"rocky", "9.4", "2032-05-31"},
		{"debian", "14", ""}, {"arch", "", ""},
	} {
		got, ok := EOL(c.id, c.version)
		if (c.want == "") == ok || (ok && got.Format(time.DateOnly) != c.want) {
			t.Errorf("EOL(%s %s) = %v %v", c.id, c.version, got, ok)
		}
	}
}

// Dezelfde invoer geeft altijd dezelfde uitkomst.
func TestDeterministic(t *testing.T) {
	in := healthy()
	in.Nodes[1].Host = "pve1"
	in.Nodes[0].Metrics.DiskDaysLeft = map[string]float64{"/": 3, "/var": 3, "/srv": 9}
	a, _ := json.Marshal(Score(in, nil, now))
	for range 20 {
		b, _ := json.Marshal(Score(in, nil, now))
		if string(a) != string(b) {
			t.Fatalf("verschil:\n%s\n%s", a, b)
		}
	}
	if !strings.Contains(string(a), "Schijf / op web-01 is vol over ongeveer 3 dagen") {
		t.Errorf("gelijke trend kiest het eerste mountpoint: %s", a)
	}
}

// Elke check in de broncode staat in de catalogus, en de catalogus heeft
// geen checks die nergens gebruikt worden.
func TestCatalog(t *testing.T) {
	src, err := os.ReadFile("checks.go")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`"((?:availability|backups|recovery|redundancy|configuration|maintenance|capacity|platform)\.[a-z_]+)"`).FindAllStringSubmatch(string(src), -1) {
		used[m[1]] = true
		if Checks[m[1]] == "" {
			t.Errorf("check %s ontbreekt in de catalogus", m[1])
		}
	}
	for c := range Checks {
		if !used[c] {
			t.Errorf("check %s staat in de catalogus maar wordt niet gebruikt", c)
		}
	}
	for _, w := range Weights {
		if CardLabels[w.Card] == "" {
			t.Errorf("kaart %s heeft geen naam", w.Card)
		}
	}
}

func TestRehearsalChecks(t *testing.T) {
	in := healthy()
	in.VIPs, in.FailoverTests = nil, nil
	if c := card(Score(in, nil, now), Recovery); c.Score != nil {
		t.Errorf("zonder VIP en zonder DR-repetitie telt de kaart: %+v", c)
	}
	in.Rehearsals = []RehearsalInput{{ID: uuid.New(), Name: "Web DR"}}
	if f := find(t, Score(in, nil, now), "recovery.dr_never_run"); f.Severity != Medium || f.Text != "DR-repetitie 'Web DR' liep nog nooit" {
		t.Errorf("nooit: %+v", f)
	}
	run := &RunInput{ID: uuid.New(), Kind: "dr.rehearsal", Name: "Web DR", Result: "pass", Finished: now.Add(-10 * day)}
	in.Rehearsals[0].LastRun = run
	if c := card(Score(in, nil, now), Recovery); c.Score == nil || *c.Score != 100 || len(c.Findings) != 0 {
		t.Errorf("geslaagd zonder VIP: %+v", c)
	}
	run.Result, run.Summary = "fail", "web-02 startte niet in de sandbox"
	f := find(t, Score(in, nil, now), "recovery.dr_failed")
	if f.Severity != High || f.Text != "DR-repetitie 'Web DR' mislukte op "+date(run.Finished)+": web-02 startte niet in de sandbox" ||
		f.Link != "/tests/detail?id="+run.ID.String() {
		t.Errorf("mislukt: %+v", f)
	}
	run.Result, run.Summary = "warning", "RPO 30 u boven het doel van 26 u"
	if f := find(t, Score(in, nil, now), "recovery.dr_goal_missed"); f.Severity != Medium || !strings.HasSuffix(f.Text, ": RPO 30 u boven het doel van 26 u") {
		t.Errorf("doel niet gehaald: %+v", f)
	}
	run.Result = "pass"
	for _, c := range []struct {
		age time.Duration
		sev Severity
	}{{50 * day, Medium}, {100 * day, High}} {
		run.Finished = now.Add(-c.age)
		if f := find(t, Score(in, nil, now), "recovery.dr_stale"); f.Severity != c.sev || f.Text != "Laatste DR-repetitie 'Web DR' is "+days(c.age)+" geleden" {
			t.Errorf("%v oud: %+v", c.age, f)
		}
	}
	in.Env = "lab"
	if f := find(t, Score(in, nil, now), "recovery.dr_stale"); f.Severity != Low {
		t.Errorf("lab, 100 dagen: %+v", f)
	}
}
