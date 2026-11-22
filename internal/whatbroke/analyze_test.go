package whatbroke

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/audit"
	"github.com/Jonasz1996/clusterforge/internal/journal"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

var t0 = time.Date(2026, 10, 11, 14, 0, 0, 0, time.UTC)

// fixture is een kleine inventory: Web met web-01 en web-02 op pve1, DB
// waar Web van afhangt, App die van Web afhangt, en een losse node elders.
type fixture struct {
	web, db, app, lb          uuid.UUID
	web1, web2, db1, lb1, lb2 Node
	solo                      Node
	conn                      uuid.UUID
	world                     World
	nextID                    int64
}

func newFixture() *fixture {
	f := &fixture{web: uuid.New(), db: uuid.New(), app: uuid.New(), lb: uuid.New(), conn: uuid.New()}
	pve1, pve2 := f.conn.String()+"/pve1", f.conn.String()+"/pve2"
	f.web1 = Node{ID: uuid.New(), Hostname: "web-01", ClusterID: &f.web, Host: pve1}
	f.web2 = Node{ID: uuid.New(), Hostname: "web-02", ClusterID: &f.web, Host: pve1}
	f.db1 = Node{ID: uuid.New(), Hostname: "db-01", ClusterID: &f.db, Host: pve2}
	f.lb1 = Node{ID: uuid.New(), Hostname: "lb-01", ClusterID: &f.lb}
	f.lb2 = Node{ID: uuid.New(), Hostname: "lb-02", ClusterID: &f.lb}
	f.solo = Node{ID: uuid.New(), Hostname: "solo", Host: pve1}
	f.world = World{
		Nodes:    []Node{f.web1, f.web2, f.db1, f.lb1, f.lb2, f.solo},
		Clusters: map[uuid.UUID]string{f.web: "Web", f.db: "DB", f.app: "App", f.lb: "LB"},
		Deps:     [][2]uuid.UUID{{f.web, f.db}, {f.app, f.web}},
	}
	return f
}

// change maakt een regel uit het journaal zoals de database hem teruggeeft.
func (f *fixture) change(n Node, ts time.Time, source string, e journal.Entry, job *uuid.UUID) Entry {
	f.nextID++
	e.Detail = mapOr(e.Detail)
	d := map[string]string{}
	for k, v := range e.Detail {
		d[k] = v
	}
	for k, v := range map[string]string{"from": e.From, "to": e.To, "by": e.By} {
		if v != "" {
			d[k] = v
		}
	}
	raw, _ := json.Marshal(d)
	return FromChange(store.ListChangesInScopeRow{
		ID: f.nextID, NodeID: n.ID, ClusterID: n.ClusterID, Ts: ts, ObservedAt: ts, Source: source, Kind: e.Kind,
		Subject: e.Subject, Action: e.Action, Summary: journal.Describe(e, false), Detail: raw, JobID: job, Hostname: n.Hostname,
	})
}

func mapOr(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// event maakt een regel uit het logboek.
func (f *fixture) event(ts time.Time, action, summary string, subject audit.Subject, cluster *uuid.UUID, node *Node,
	job *uuid.UUID, payload map[string]any) Entry {
	f.nextID++
	e := audit.Entry{ID: f.nextID, Ts: ts, Action: action, Summary: summary, Subject: subject, Payload: payload}
	if cluster != nil {
		e.Cluster = &audit.Ref{ID: cluster.String(), Name: f.world.Clusters[*cluster]}
	}
	if node != nil {
		e.Node = &audit.Ref{ID: node.ID.String(), Name: node.Hostname}
	}
	if job != nil {
		e.Job = &audit.Ref{ID: job.String(), Name: "Web uitrollen"}
	}
	return FromEvent(e)
}

func (f *fixture) nodeStatus(n Node, ts time.Time, from, to, reason string) Entry {
	return f.event(ts, "node.status_changed", "Node "+n.Hostname+" van "+from+" naar "+to,
		audit.Subject{Type: "node", ID: n.ID.String(), Name: n.Hostname}, n.ClusterID, &n, nil,
		map[string]any{"hostname": n.Hostname, "from": from, "to": to, "reason": reason})
}

func (f *fixture) clusterStatus(c uuid.UUID, ts time.Time, from, to string) Entry {
	return f.event(ts, "cluster.status_changed", "Cluster "+f.world.Clusters[c]+" van "+from+" naar "+to,
		audit.Subject{Type: "cluster", ID: c.String()}, &c, nil, nil, map[string]any{"from": from, "to": to})
}

func clusterSubject(f *fixture, c uuid.UUID, status string, since time.Time) Subject {
	return Subject{Type: "cluster", ID: c, Name: f.world.Clusters[c], ClusterID: &c, Status: status, Since: since}
}

func nodeSubject(n Node, status string, since time.Time) Subject {
	return Subject{Type: "node", ID: n.ID, Name: n.Hostname, ClusterID: n.ClusterID, Status: status, Since: since}
}

func TestAnalyzePackageUpgrade(t *testing.T) {
	f := newFixture()
	upgrade := f.change(f.web1, t0.Add(-160*time.Second), journal.SourceAgent,
		journal.Entry{Kind: journal.KindPackage, Subject: "nginx", Action: "upgrade", From: "1.26.0", To: "1.26.3", By: "root"}, nil)
	libssl := f.change(f.web1, t0.Add(-150*time.Second), journal.SourceAgent,
		journal.Entry{Kind: journal.KindPackage, Subject: "libssl3", Action: "upgrade", From: "3.0.15", To: "3.0.17", By: "root"}, nil)
	login := f.change(f.web1, t0.Add(-5*time.Minute), journal.SourceAgent,
		journal.Entry{Kind: journal.KindLogin, Subject: "root", Action: "login", Detail: map[string]string{"ip": "10.0.0.9", "method": "publickey"}}, nil)
	failed := f.change(f.web1, t0.Add(-2*time.Minute), journal.SourceAgent,
		journal.Entry{Kind: journal.KindService, Subject: "nginx.service", Action: "failed", Detail: map[string]string{"result": "exit-code"}}, nil)
	old := f.change(f.web2, t0.Add(-100*time.Minute), journal.SourceAgent,
		journal.Entry{Kind: journal.KindPackage, Subject: "curl", Action: "upgrade", From: "8.13", To: "8.14"}, nil)
	in := Input{
		Started:  t0.Add(-105 * time.Second),
		Subjects: []Subject{clusterSubject(f, f.web, "degraded", t0.Add(-105*time.Second)), nodeSubject(f.web1, "degraded", t0.Add(-105*time.Second))},
		World:    f.world,
		Entries: []Entry{
			upgrade, libssl, login, failed, old,
			f.nodeStatus(f.web1, t0.Add(-105*time.Second), "healthy", "degraded", "service nginx is gefaald"),
			f.clusterStatus(f.web, t0.Add(-105*time.Second), "healthy", "degraded"),
		},
		MetricsUnknown: true,
	}
	a := Analyze(in)

	if a.FirstSymptom.EntryID != failed.ID || a.FirstSymptom.Summary != "nginx.service mislukt: afgesloten met een fout op web-01" {
		t.Fatalf("eerste symptoom = %+v", a.FirstSymptom)
	}
	if len(a.Causes) == 0 {
		t.Fatal("geen oorzaken")
	}
	top := a.Causes[0]
	if top.EntryID != upgrade.ID || top.Level != LevelLikely || top.Also != 1 || top.Hostname != "web-01" {
		t.Fatalf("eerste oorzaak = %+v", top)
	}
	wantReasons := []string{
		"Op web-01, de node die misging.",
		"40 s voor het eerste symptoom.",
		"nginx op web-01 faalde 40 s na deze update van nginx.",
		"Samen met 1 ander pakket in dezelfde update.",
	}
	if !slices.Equal(top.Reasons, wantReasons) {
		t.Fatalf("redenen = %q", top.Reasons)
	}
	want := "Waarschijnlijke oorzaak: nginx bijgewerkt van 1.26.0 naar 1.26.3 (root) op web-01, 40 s voor het eerste symptoom."
	if a.Conclusion != want {
		t.Fatalf("conclusie = %q", a.Conclusion)
	}
	// De login is mogelijk, zonder IP-adres.
	var sawLogin bool
	for _, c := range a.Causes {
		if c.EntryID == login.ID {
			sawLogin = true
			if c.Level != LevelPossible || strings.Contains(c.Summary, "10.0.0.9") {
				t.Fatalf("login = %+v", c)
			}
		}
		if c.EntryID == old.ID {
			t.Fatalf("een update van 100 minuten ervoor op een andere node telt niet: %+v", c)
		}
	}
	if !sawLogin {
		t.Fatalf("login ontbreekt: %+v", a.Causes)
	}
	if a.Changes != 4 {
		t.Fatalf("veranderingen = %d", a.Changes)
	}
	if len(a.Consumers) != 1 || a.Consumers[0].Name != "App" {
		t.Fatalf("afnemers = %+v", a.Consumers)
	}
}

func TestAnalyzeHostOffline(t *testing.T) {
	f := newFixture()
	offline := f.event(t0.Add(-100*time.Second), "proxmox.host_offline", "Proxmox-host pve1 van lab is offline",
		audit.Subject{Type: "proxmox", ID: f.conn.String(), Name: "lab"}, nil, nil, nil,
		map[string]any{"name": "lab", "host": "pve1", "from": "online", "to": "offline"})
	elsewhere := f.change(f.db1, t0.Add(-3*time.Minute), journal.SourceAgent,
		journal.Entry{Kind: journal.KindLogin, Subject: "jonas", Action: "login"}, nil)
	in := Input{
		Started: t0,
		Subjects: []Subject{
			clusterSubject(f, f.web, "down", t0), nodeSubject(f.web1, "down", t0), nodeSubject(f.web2, "down", t0.Add(5*time.Second)),
			nodeSubject(f.solo, "down", t0.Add(5*time.Second)),
		},
		World: f.world,
		Entries: []Entry{
			offline, elsewhere,
			f.nodeStatus(f.web1, t0, "healthy", "down", "geen heartbeat meer"),
			f.nodeStatus(f.web2, t0.Add(5*time.Second), "healthy", "down", "geen heartbeat meer"),
			f.nodeStatus(f.solo, t0.Add(5*time.Second), "healthy", "down", "geen heartbeat meer"),
			f.clusterStatus(f.web, t0.Add(5*time.Second), "healthy", "down"),
		},
	}
	a := Analyze(in)
	if !a.FirstSymptom.From.Equal(t0.Add(-heartbeatDelay)) || a.FirstSymptom.Why == "" {
		t.Fatalf("eerste symptoom = %+v", a.FirstSymptom)
	}
	top := a.Causes[0]
	if top.EntryID != offline.ID || top.Level != LevelLikely || top.Score != 1 {
		t.Fatalf("eerste oorzaak = %+v", top)
	}
	if !slices.Contains(top.Reasons, "De Proxmox-host ging offline voordat 3 VM's erop down gingen: solo, web-01 en web-02.") ||
		!slices.Contains(top.Reasons, "Op Proxmox-host pve1, waar solo, web-01 en web-02 op draaien.") {
		t.Fatalf("redenen = %q", top.Reasons)
	}
	for _, c := range a.Causes[1:] {
		if c.Level == LevelLikely {
			t.Fatalf("alleen de host is waarschijnlijk: %+v", c)
		}
	}
	if !strings.HasPrefix(a.Conclusion, "Waarschijnlijke oorzaak: Proxmox-host pve1 van lab is offline,") {
		t.Fatalf("conclusie = %q", a.Conclusion)
	}
	sc := ScopeFor(uuid.Nil, in.Subjects, f.world)
	if !slices.Contains(sc.Others, f.conn) || !slices.Contains(sc.Nodes, f.solo.ID) || !slices.Contains(sc.Clusters, f.db) {
		t.Fatalf("scope = %+v", sc)
	}
}

func TestAnalyzeGitOpsChange(t *testing.T) {
	f := newFixture()
	job := uuid.New()
	sha := "3f9c2a7d41e0b6c8a5d2e9f7c1b4a8d6e3f0c2b1"
	jobSubject := audit.Subject{Type: "job", ID: job.String(), Name: "Web uitrollen"}
	queued := f.event(t0.Add(-4*time.Minute), "job.queued", "Taak Web uitrollen in de wachtrij", jobSubject, &f.web, nil, &job, nil)
	written := f.change(f.web1, t0.Add(-3*time.Minute), journal.SourceClusterForge,
		journal.Entry{Kind: journal.KindFile, Subject: "/etc/nginx/sites-enabled/web.conf", Action: "written", By: "clusterforge"}, &job)
	restarted := f.change(f.web1, t0.Add(-3*time.Minute+time.Second), journal.SourceClusterForge,
		journal.Entry{Kind: journal.KindService, Subject: "nginx", Action: "restarted", By: "clusterforge"}, &job)
	failed := f.change(f.web1, t0.Add(-170*time.Second), journal.SourceAgent,
		journal.Entry{Kind: journal.KindService, Subject: "nginx.service", Action: "failed"}, nil)
	jobFailed := f.event(t0.Add(-2*time.Minute), "job.failed", "Taak Web uitrollen mislukt", jobSubject, &f.web, nil, &job, nil)
	change := f.event(t0.Add(-2*time.Minute), "gitops.change_failed", "Wijziging voor web uit commit 3f9c2a7 mislukt: stap 4",
		audit.Subject{Type: "git_change", ID: uuid.NewString()}, &f.web, nil, &job,
		map[string]any{"slug": "web", "commit": sha, "job_id": job.String(), "error": "stap 4"})
	in := Input{
		Started:  t0.Add(-150 * time.Second),
		Subjects: []Subject{clusterSubject(f, f.web, "degraded", t0.Add(-150*time.Second)), nodeSubject(f.web1, "degraded", t0.Add(-150*time.Second))},
		World:    f.world,
		Entries: []Entry{
			queued, written, restarted, failed, jobFailed, change,
			f.nodeStatus(f.web1, t0.Add(-150*time.Second), "healthy", "degraded", "service nginx is gefaald"),
		},
	}
	a := Analyze(in)
	if len(a.Causes) != 1 {
		t.Fatalf("de taak is één oorzaak: %+v", a.Causes)
	}
	top := a.Causes[0]
	if top.EntryID != change.ID || top.Commit != sha || top.Also != 4 || top.Level != LevelLikely || top.Ts != written.Ts {
		t.Fatalf("oorzaak = %+v", top)
	}
	for _, want := range []string{
		"Het bestand hoort bij nginx, dat 10 s later faalde op web-01.",
		"De wijziging mislukte.",
		"Samen met 4 andere regels van de taak 'Web uitrollen'.",
	} {
		if !slices.Contains(top.Reasons, want) {
			t.Fatalf("reden %q ontbreekt: %q", want, top.Reasons)
		}
	}
	if top.Cluster != "Web" || top.Link != "/gitops" {
		t.Fatalf("oorzaak = %+v", top)
	}
}

func TestAnalyzeNoChange(t *testing.T) {
	f := newFixture()
	base := Input{
		Started:  t0,
		Subjects: []Subject{clusterSubject(f, f.web, "degraded", t0), nodeSubject(f.web1, "degraded", t0)},
		World:    f.world,
		Entries:  []Entry{f.nodeStatus(f.web1, t0, "healthy", "degraded", "heartbeat vertraagd")},
	}

	withCPU := base
	withCPU.Anomalies = []NodeAnomaly{{NodeID: f.web1.ID, Hostname: "web-01",
		Anomaly: metrics.Anomaly{Metric: "cpu", Before: 21.44, After: 99.2, At: t0.Add(-4 * time.Minute)}}}
	a := Analyze(withCPU)
	want := "ClusterForge zag in de twee uur ervoor geen verandering. De afwijkingen in de metrics staan hieronder; de oorzaak ligt waarschijnlijk buiten de nodes."
	if a.Changes != 0 || a.Conclusion != want {
		t.Fatalf("conclusie = %q (%d veranderingen)", a.Conclusion, a.Changes)
	}
	if len(a.Anomalies) != 1 || a.Anomalies[0].Summary != "CPU van web-01 piekte op 99 % (gemiddeld 21 % in de twee uur ervoor)" ||
		a.Anomalies[0].Before != 21.4 {
		t.Fatalf("afwijkingen = %+v", a.Anomalies)
	}
	if len(a.Causes) != 1 || a.Causes[0].Kind != "metrics" || a.Causes[0].Level != LevelPossible {
		t.Fatalf("de piek is een mogelijke oorzaak: %+v", a.Causes)
	}

	unknown := base
	unknown.MetricsUnknown = true
	if got := Analyze(unknown).Conclusion; got != "ClusterForge zag in de twee uur ervoor geen verandering. De metrics waren niet te lezen; de oorzaak ligt waarschijnlijk buiten de nodes." {
		t.Fatalf("conclusie = %q", got)
	}
	if got := Analyze(base).Conclusion; got != "ClusterForge zag in de twee uur ervoor geen verandering. Ook de metrics toonden geen afwijking; de oorzaak ligt waarschijnlijk buiten de nodes." {
		t.Fatalf("conclusie = %q", got)
	}
}

func TestAnalyzeDiskFull(t *testing.T) {
	f := newFixture()
	in := Input{
		Started:  t0,
		Subjects: []Subject{nodeSubject(f.web1, "degraded", t0), clusterSubject(f, f.web, "degraded", t0)},
		World:    f.world,
		Entries:  []Entry{f.nodeStatus(f.web1, t0, "healthy", "degraded", "schijf / is 96 % vol")},
		Anomalies: []NodeAnomaly{{NodeID: f.web1.ID, Hostname: "web-01",
			Anomaly: metrics.Anomaly{Metric: "disk", Label: "/", Before: 70, After: 96, At: t0.Add(-6 * time.Minute)}}},
	}
	a := Analyze(in)
	top := a.Causes[0]
	if top.Level != LevelLikely || !slices.Contains(top.Reasons, "De schijf liep vol voordat web-01 verminderd raakte.") {
		t.Fatalf("oorzaak = %+v", top)
	}
	if !strings.HasPrefix(a.Conclusion, "ClusterForge zag in de twee uur ervoor geen verandering. Waarschijnlijke oorzaak: Schijf / van web-01 liep vol tot 96 %") {
		t.Fatalf("conclusie = %q", a.Conclusion)
	}
}

func TestAnalyzeVIPOwnerReboot(t *testing.T) {
	f := newFixture()
	boot := f.change(f.lb1, t0.Add(-40*time.Second), journal.SourceAgent,
		journal.Entry{Kind: journal.KindBoot, Subject: "boot", Action: "boot", From: "6.1.0-25", To: "6.1.0-26"}, nil)
	vip := f.event(t0, "vip.owner_changed", "VIP 10.0.0.10 heeft geen eigenaar meer",
		audit.Subject{Type: "vip", ID: uuid.NewString()}, &f.lb, nil, nil,
		map[string]any{"address": "10.0.0.10", "from": f.lb1.ID.String(), "to": nil, "node_ids": []any{f.lb1.ID.String()}})
	in := Input{
		Started:  t0,
		Subjects: []Subject{clusterSubject(f, f.lb, "down", t0)},
		World:    f.world,
		Entries:  []Entry{boot, vip, f.clusterStatus(f.lb, t0, "healthy", "down")},
	}
	a := Analyze(in)
	if a.FirstSymptom.EntryID != vip.ID {
		t.Fatalf("eerste symptoom = %+v", a.FirstSymptom)
	}
	top := a.Causes[0]
	if top.EntryID != boot.ID || top.Level != LevelLikely ||
		!slices.Contains(top.Reasons, "lb-01 had VIP 10.0.0.10 en herstartte of stopte vlak voordat het VIP geen eigenaar meer had.") {
		t.Fatalf("oorzaak = %+v", top)
	}
}

func TestAnalyzeSupplierAndDeterminism(t *testing.T) {
	f := newFixture()
	job := uuid.New()
	dbJob := f.event(t0.Add(-5*time.Minute), "job.succeeded", "Taak DB bijwerken geslaagd",
		audit.Subject{Type: "job", ID: job.String()}, &f.db, nil, &job, nil)
	in := Input{
		Started:  t0,
		Subjects: []Subject{clusterSubject(f, f.web, "degraded", t0)},
		World:    f.world,
		Entries:  []Entry{dbJob, f.clusterStatus(f.web, t0, "healthy", "degraded")},
	}
	a := Analyze(in)
	top := a.Causes[0]
	if top.Level != LevelPossible || top.Reasons[0] != "In cluster DB, waar het geraakte cluster van afhangt." {
		t.Fatalf("oorzaak = %+v", top)
	}
	if !strings.HasPrefix(a.Conclusion, "Geen duidelijke oorzaak. Het meest verdacht: Taak DB bijwerken geslaagd, 4 min voor") {
		t.Fatalf("conclusie = %q", a.Conclusion)
	}
	x, _ := json.Marshal(Analyze(in))
	y, _ := json.Marshal(Analyze(in))
	if string(x) != string(y) {
		t.Fatal("de analyse is niet deterministisch")
	}
}

func TestAnalyzeWithoutSymptomInTimeline(t *testing.T) {
	f := newFixture()
	a := Analyze(Input{Started: t0, Subjects: []Subject{clusterSubject(f, f.web, "split_brain", t0)}, World: f.world})
	if a.FirstSymptom.Summary != "Cluster Web: split-brain" || !a.FirstSymptom.To.Equal(t0) || len(a.Causes) != 0 {
		t.Fatalf("analyse = %+v", a)
	}
}

func TestWhen(t *testing.T) {
	for _, tc := range []struct {
		t    time.Time
		want string
	}{
		{t0.Add(-40 * time.Second), "40 s voor het eerste symptoom"},
		{t0.Add(-80 * time.Minute), "1 u 20 min voor het eerste symptoom"},
		{t0.Add(-2 * time.Second), "tegelijk met het eerste symptoom"},
		{t0.Add(30 * time.Second), "tegelijk met het eerste symptoom"},
		{t0.Add(3 * time.Minute), "2 min na het eerste symptoom"},
	} {
		if got := when(tc.t, t0, t0.Add(30*time.Second)); got != tc.want {
			t.Errorf("when(%s) = %q, wil %q", tc.t.Sub(t0), got, tc.want)
		}
	}
}

func TestLinks(t *testing.T) {
	for _, tc := range []struct {
		pkg, unit string
		want      bool
	}{
		{"nginx", "nginx.service", true},
		{"nginx-core", "nginx", true},
		{"postgresql-16", "postgresql@16-main.service", true},
		{"mariadb-server", "mysql", true},
		{"openssh-server", "ssh.service", true},
		{"apache2", "httpd", true},
		{"libssl3", "nginx", false},
		{"ngin", "nginx", false},
	} {
		if got := packageMatches(tc.pkg, tc.unit); got != tc.want {
			t.Errorf("packageMatches(%q, %q) = %v", tc.pkg, tc.unit, got)
		}
	}
	for _, tc := range []struct {
		path, unit string
		want       bool
	}{
		{"/etc/nginx/sites-enabled/default", "nginx", true},
		{"/etc/haproxy/haproxy.cfg", "haproxy.service", true},
		{"/etc/systemd/system/keepalived.service", "keepalived", true},
		{"/etc/apache2/apache2.conf", "httpd", true},
		{"/etc/hosts", "nginx", false},
	} {
		if got := fileMatches(tc.path, tc.unit); got != tc.want {
			t.Errorf("fileMatches(%q, %q) = %v", tc.path, tc.unit, got)
		}
	}
	for _, tc := range []struct {
		proc, unit string
		want       bool
	}{
		{"mysqld", "mysql", true},
		{"postgres", "postgresql", true},
		{"mariadbd", "mysql.service", true},
		{"java", "nginx", false},
	} {
		if got := processMatches(tc.proc, tc.unit); got != tc.want {
			t.Errorf("processMatches(%q, %q) = %v", tc.proc, tc.unit, got)
		}
	}
	if got := packageNames("nginx, nginx-common:amd64"); !slices.Equal(got, []string{"nginx", "nginx-common"}) {
		t.Errorf("packageNames = %q", got)
	}
	if got := unitsFromReason("heartbeat vertraagd; service nginx is gefaald; service php8.2-fpm@www draait niet"); !slices.Equal(got, []string{"nginx", "php8.2-fpm"}) {
		t.Errorf("unitsFromReason = %q", got)
	}
}

func TestRedact(t *testing.T) {
	f := newFixture()
	login := f.change(f.web1, t0, journal.SourceAgent,
		journal.Entry{Kind: journal.KindLogin, Subject: "root", Action: "login", Detail: map[string]string{"ip": "10.0.0.9", "method": "password"}}, nil)
	r := Redact(login)
	if r.Summary != "SSH-login als root (wachtwoord)" || r.Detail["ip"] != "" || login.Detail["ip"] != "10.0.0.9" {
		t.Fatalf("redact = %+v, origineel %+v", r, login)
	}
}

// TestAnalyzeTieBreak: bij een gelijke score gaat de oorzaak met meer
// verbanden voor, zoals op de dev-server toen twee VM's tegelijk verdwenen.
func TestAnalyzeTieBreak(t *testing.T) {
	f := newFixture()
	missing := func(n Node) Entry {
		return f.event(t0.Add(-10*time.Second), "vm.missing", "VM van "+n.Hostname+" is verdwenen uit Proxmox",
			audit.Subject{Type: "node", ID: n.ID.String(), Name: n.Hostname}, n.ClusterID, &n, nil, map[string]any{"hostname": n.Hostname})
	}
	web1, web2 := missing(f.web1), missing(f.web2)
	vip := f.event(t0.Add(5*time.Second), "vip.owner_changed", "VIP 10.0.0.10 heeft geen eigenaar meer",
		audit.Subject{Type: "vip", ID: uuid.NewString()}, &f.web, nil, nil, map[string]any{"address": "10.0.0.10", "from": f.web1.ID.String(), "to": nil})
	in := Input{
		Started:  t0,
		Subjects: []Subject{clusterSubject(f, f.web, "down", t0), nodeSubject(f.web1, "down", t0), nodeSubject(f.web2, "down", t0)},
		World:    f.world,
		Entries: []Entry{
			web2, web1, vip,
			f.nodeStatus(f.web2, t0, "healthy", "down", "geen heartbeat meer"),
			f.nodeStatus(f.web1, t0, "healthy", "down", "geen heartbeat meer"),
		},
	}
	a := Analyze(in)
	if len(a.Causes) != 2 || a.Causes[0].EntryID != web1.ID || a.Causes[0].Score != a.Causes[1].Score ||
		!slices.Contains(a.Causes[0].Reasons, "De VM verdween uit Proxmox voordat web-01 down ging.") {
		t.Fatalf("oorzaken = %+v", a.Causes)
	}
}
