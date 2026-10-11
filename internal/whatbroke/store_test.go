package whatbroke

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/journal"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

// world is een echte database met het cluster Web (web-01 en web-02), een
// losse node solo en db-01. web-01, web-02 en solo zijn VM's op Proxmox-host
// pve1, db-01 staat op pve2.
type world struct {
	t                     *testing.T
	ctx                   context.Context
	pool                  *pgxpool.Pool
	q                     *store.Queries
	web                   uuid.UUID
	conn                  uuid.UUID
	web1, web2, solo, db1 uuid.UUID
	now                   time.Time
}

func newWorld(t *testing.T) *world {
	pool := storetest.DB(t)
	w := &world{t: t, ctx: context.Background(), pool: pool, q: store.New(pool), conn: uuid.New(), now: time.Now().UTC().Truncate(time.Second)}
	w.exec(`INSERT INTO proxmox_connections (id, name, api_url, token_id, token_secret_enc, key_id)
		VALUES ($1, 'lab', 'https://pve.example:8006', 'cf@pve!t', '\x00', 'k')`, w.conn)
	w.scan(&w.web, `INSERT INTO clusters (slug, name, type, environment) VALUES ('web', 'Web', 'nginx', 'prod') RETURNING id`)
	for i, n := range []struct {
		dst     *uuid.UUID
		host    string
		cluster *uuid.UUID
		pve     string
	}{{&w.web1, "web-01", &w.web, "pve1"}, {&w.web2, "web-02", &w.web, "pve1"}, {&w.solo, "solo", nil, "pve1"}, {&w.db1, "db-01", nil, "pve2"}} {
		vmid := 101 + i
		w.scan(n.dst, `INSERT INTO nodes (hostname, cluster_id, proxmox_id, pve_vmid) VALUES ($1, $2, $3, $4) RETURNING id`,
			n.host, n.cluster, w.conn, vmid)
		w.exec(`INSERT INTO proxmox_resources (connection_id, pve_id, type, pve_node, vmid, name, status, data, synced_at)
			VALUES ($1, $2, 'qemu', $3, $4, $5, 'running', '{}', now())`, w.conn, "qemu/"+n.host, n.pve, vmid, n.host)
	}
	return w
}

func (w *world) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.pool.Exec(w.ctx, sql, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) scan(dst any, sql string, args ...any) {
	w.t.Helper()
	if err := w.pool.QueryRow(w.ctx, sql, args...).Scan(dst); err != nil {
		w.t.Fatal(err)
	}
}

// event schrijft een event met een tijdstip in het verleden.
func (w *world) event(ts time.Time, subjectType, subjectID string, cluster *uuid.UUID, action string, payload map[string]any) {
	w.t.Helper()
	p, _ := json.Marshal(payload)
	w.exec(`INSERT INTO events (ts, actor_type, subject_type, subject_id, cluster_id, action, payload)
		VALUES ($1, 'system', $2, $3, $4, $5, $6)`, ts, subjectType, subjectID, cluster, action, p)
}

func (w *world) nodeDown(node uuid.UUID, host string, cluster *uuid.UUID, ts time.Time, to, reason string) {
	w.event(ts, "node", node.String(), cluster, "node.status_changed",
		map[string]any{"hostname": host, "from": "healthy", "to": to, "reason": reason})
}

func (w *world) journal(node uuid.UUID, cluster *uuid.UUID, ts time.Time, source string, e journal.Entry) {
	w.t.Helper()
	e.NodeID, e.ClusterID, e.TS, e.Source = node, cluster, ts, source
	if err := journal.Record(w.ctx, w.q, e); err != nil {
		w.t.Fatal(err)
	}
}

type subject struct {
	typ     string
	id      uuid.UUID
	name    string
	cluster *uuid.UUID
	status  string
}

func (w *world) incident(started time.Time, subjects ...subject) uuid.UUID {
	w.t.Helper()
	inc, err := w.q.CreateIncident(w.ctx, store.CreateIncidentParams{
		Title: "test", Severity: "high", StartedAt: started, DetectedAt: started.Add(5 * time.Second),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, s := range subjects {
		if err := w.q.UpsertIncidentSubject(w.ctx, store.UpsertIncidentSubjectParams{
			IncidentID: inc.ID, SubjectType: s.typ, SubjectID: s.id, Name: s.name, ClusterID: s.cluster,
			FirstStatus: s.status, WorstStatus: s.status, LastStatus: s.status, Since: started,
		}); err != nil {
			w.t.Fatal(err)
		}
	}
	return inc.ID
}

func (w *world) analyzer() *Analyzer {
	a := NewAnalyzer(w.pool, metrics.NewClient(""), slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.Now = func() time.Time { return w.now }
	return a
}

func (w *world) analysis(id uuid.UUID) (*Analysis, store.Incident) {
	w.t.Helper()
	inc, err := w.q.GetIncident(w.ctx, id)
	if err != nil {
		w.t.Fatal(err)
	}
	a := Decode(inc)
	if a == nil {
		w.t.Fatalf("geen analyse bij %s", id)
	}
	return a, inc
}

// TestAnalyzerPackageUpgrade: 'pakket nginx bijgewerkt, daarna nginx
// failed' geeft die upgrade als waarschijnlijke oorzaak, met de reden.
func TestAnalyzerPackageUpgrade(t *testing.T) {
	w := newWorld(t)
	t0 := w.now.Add(-10 * time.Minute)
	w.journal(w.web1, &w.web, t0.Add(-160*time.Second), journal.SourceAgent,
		journal.Entry{Kind: journal.KindPackage, Subject: "nginx", Action: "upgrade", From: "1.26.0", To: "1.26.3", By: "jonas"})
	w.journal(w.web1, &w.web, t0.Add(-120*time.Second), journal.SourceAgent,
		journal.Entry{Kind: journal.KindService, Subject: "nginx.service", Action: "failed", Detail: map[string]string{"result": "exit-code"}})
	// Een login op een node die niets met het incident te maken heeft.
	w.journal(w.db1, nil, t0.Add(-130*time.Second), journal.SourceAgent,
		journal.Entry{Kind: journal.KindLogin, Subject: "root", Action: "login", Detail: map[string]string{"ip": "10.0.0.7"}})
	w.nodeDown(w.web1, "web-01", &w.web, t0, "degraded", "service nginx is gefaald")
	w.event(t0, "cluster", w.web.String(), &w.web, "cluster.status_changed", map[string]any{"name": "Web", "from": "healthy", "to": "degraded"})
	id := w.incident(t0, subject{"cluster", w.web, "Web", &w.web, "degraded"}, subject{"node", w.web1, "web-01", &w.web, "degraded"})

	an := w.analyzer()
	if err := an.RunOnce(w.ctx); err != nil {
		t.Fatal(err)
	}
	a, inc := w.analysis(id)
	if !strings.HasPrefix(a.FirstSymptom.Summary, "nginx.service mislukt") || !a.FirstSymptom.Ts.Equal(t0.Add(-120*time.Second)) {
		t.Fatalf("eerste symptoom = %+v", a.FirstSymptom)
	}
	top := a.Causes[0]
	if top.Level != LevelLikely || top.Kind != journal.KindPackage || top.Hostname != "web-01" ||
		!slices.Contains(top.Reasons, "nginx op web-01 faalde 40 s na deze update van nginx.") {
		t.Fatalf("oorzaak = %+v", top)
	}
	want := "Waarschijnlijke oorzaak: nginx bijgewerkt van 1.26.0 naar 1.26.3 (jonas) op web-01, 40 s voor het eerste symptoom."
	if a.Conclusion != want || !a.MetricsUnknown {
		t.Fatalf("conclusie = %q, metrics onbekend %v", a.Conclusion, a.MetricsUnknown)
	}
	for _, c := range a.Causes {
		if strings.Contains(c.Summary, "10.0.0.7") {
			t.Fatalf("een oorzaak noemt een IP-adres: %+v", c)
		}
	}

	// Niets nieuw: de analyse wordt niet opnieuw geschreven.
	w.now = w.now.Add(time.Minute)
	if err := an.RunOnce(w.ctx); err != nil {
		t.Fatal(err)
	}
	if _, again := w.analysis(id); !again.AnalyzedAt.Equal(*inc.AnalyzedAt) {
		t.Fatalf("analyse herschreven zonder verandering: %v na %v", again.AnalyzedAt, inc.AnalyzedAt)
	}
	// Een journaal dat later binnenkomt, verandert de analyse zolang het
	// incident open is.
	w.journal(w.web1, &w.web, t0.Add(-150*time.Second), journal.SourceAgent,
		journal.Entry{Kind: journal.KindFile, Subject: "/etc/nginx/nginx.conf", Action: "modified"})
	if err := an.RunOnce(w.ctx); err != nil {
		t.Fatal(err)
	}
	if a, again := w.analysis(id); !again.AnalyzedAt.After(*inc.AnalyzedAt) || a.Changes != 2 {
		t.Fatalf("na het late journaal: %d veranderingen, gerekend %v", a.Changes, again.AnalyzedAt)
	}

	// Een uur na het einde ligt de analyse vast.
	w.exec(`UPDATE incidents SET status = 'resolved', resolved_at = $2 WHERE id = $1`, id, w.now.Add(-2*time.Hour))
	before, _ := w.analysis(id)
	w.journal(w.web1, &w.web, t0.Add(-140*time.Second), journal.SourceAgent,
		journal.Entry{Kind: journal.KindLogin, Subject: "jonas", Action: "login"})
	if err := an.RunOnce(w.ctx); err != nil {
		t.Fatal(err)
	}
	if after, _ := w.analysis(id); after.Changes != before.Changes {
		t.Fatalf("een vastgelegde analyse veranderde: %d na %d", after.Changes, before.Changes)
	}
}

// TestAnalyzerHostOffline: een Proxmox-host die offline gaat met drie VM's
// erop geeft de host.
func TestAnalyzerHostOffline(t *testing.T) {
	w := newWorld(t)
	t0 := w.now.Add(-5 * time.Minute)
	w.event(t0.Add(-100*time.Second), "proxmox", w.conn.String(), nil, "proxmox.host_offline",
		map[string]any{"name": "lab", "host": "pve1", "from": "online", "to": "offline"})
	w.event(t0.Add(-90*time.Second), "proxmox", w.conn.String(), nil, "proxmox.host_offline",
		map[string]any{"name": "lab", "host": "pve2", "from": "online", "to": "offline"})
	w.nodeDown(w.web1, "web-01", &w.web, t0, "down", "geen heartbeat meer")
	w.nodeDown(w.web2, "web-02", &w.web, t0.Add(5*time.Second), "down", "geen heartbeat meer")
	w.nodeDown(w.solo, "solo", nil, t0.Add(5*time.Second), "down", "geen heartbeat meer")
	id := w.incident(t0, subject{"cluster", w.web, "Web", &w.web, "down"}, subject{"node", w.web1, "web-01", &w.web, "down"},
		subject{"node", w.web2, "web-02", &w.web, "down"}, subject{"node", w.solo, "solo", nil, "down"})
	if err := w.analyzer().RunOnce(w.ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := w.analysis(id)
	top := a.Causes[0]
	if top.Level != LevelLikely || top.Summary != "Host pve1 van Proxmox lab is offline" || top.Link != "/proxmox" ||
		!slices.Contains(top.Reasons, "De Proxmox-host ging offline voordat 3 VM's erop down gingen: solo, web-01 en web-02.") {
		t.Fatalf("oorzaak = %+v", top)
	}
	// pve2 draait niets van het incident.
	for _, c := range a.Causes[1:] {
		if c.Level == LevelLikely {
			t.Fatalf("alleen pve1 is waarschijnlijk: %+v", c)
		}
	}
	if !strings.HasPrefix(a.Conclusion, "Waarschijnlijke oorzaak: Host pve1 van Proxmox lab is offline,") {
		t.Fatalf("conclusie = %q", a.Conclusion)
	}
}

// TestAnalyzerGitOpsCommit: een mislukte GitOps-wijziging geeft de commit
// met sha.
func TestAnalyzerGitOpsCommit(t *testing.T) {
	w := newWorld(t)
	t0 := w.now.Add(-5 * time.Minute)
	var job uuid.UUID
	w.scan(&job, `INSERT INTO jobs (kind, title, status, cluster_id) VALUES ('rollout', 'Wijziging uit Git toepassen op Web', 'failed', $1) RETURNING id`, w.web)
	sha := "3f9c2a7d41e0b6c8a5d2e9f7c1b4a8d6e3f0c2b1"
	w.event(t0.Add(-4*time.Minute), "job", job.String(), &w.web, "job.queued", map[string]any{"title": "Wijziging uit Git toepassen op Web"})
	w.journal(w.web1, &w.web, t0.Add(-3*time.Minute), journal.SourceClusterForge,
		journal.Entry{Kind: journal.KindFile, Subject: "/etc/nginx/sites-enabled/web.conf", Action: "written", By: "clusterforge", JobID: &job})
	w.journal(w.web1, &w.web, t0.Add(-170*time.Second), journal.SourceAgent,
		journal.Entry{Kind: journal.KindService, Subject: "nginx.service", Action: "failed"})
	w.event(t0.Add(-2*time.Minute), "job", job.String(), &w.web, "job.failed", map[string]any{"error": "stap 4: nginx -t"})
	w.event(t0.Add(-2*time.Minute), "git_change", uuid.NewString(), &w.web, "gitops.change_failed",
		map[string]any{"slug": "web", "path": "clusters/web.yaml", "commit": sha, "revision": 7, "job_id": job.String(), "error": "stap 4: nginx -t"})
	w.nodeDown(w.web1, "web-01", &w.web, t0.Add(-150*time.Second), "degraded", "service nginx is gefaald")
	id := w.incident(t0.Add(-150*time.Second), subject{"cluster", w.web, "Web", &w.web, "degraded"},
		subject{"node", w.web1, "web-01", &w.web, "degraded"})
	if err := w.analyzer().RunOnce(w.ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := w.analysis(id)
	if len(a.Causes) != 1 {
		t.Fatalf("de taak is één oorzaak: %+v", a.Causes)
	}
	top := a.Causes[0]
	if top.Commit != sha || top.Level != LevelLikely || top.Kind != "gitops" || !strings.Contains(top.Summary, "3f9c2a7") ||
		!slices.Contains(top.Reasons, "De wijziging mislukte.") ||
		!slices.Contains(top.Reasons, "Samen met 3 andere regels van de taak 'Wijziging uit Git toepassen op Web'.") {
		t.Fatalf("oorzaak = %+v", top)
	}
}

// TestAnalyzerNoChange: zonder verandering zegt het rapport dat ook.
func TestAnalyzerNoChange(t *testing.T) {
	w := newWorld(t)
	t0 := w.now.Add(-5 * time.Minute)
	w.nodeDown(w.web1, "web-01", &w.web, t0, "degraded", "heartbeat vertraagd")
	// Een verandering van drie uur ervoor valt buiten het venster.
	w.journal(w.web1, &w.web, t0.Add(-3*time.Hour), journal.SourceAgent,
		journal.Entry{Kind: journal.KindPackage, Subject: "curl", Action: "upgrade", From: "8.13", To: "8.14"})
	id := w.incident(t0, subject{"cluster", w.web, "Web", &w.web, "degraded"}, subject{"node", w.web1, "web-01", &w.web, "degraded"})
	if err := w.analyzer().RunOnce(w.ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := w.analysis(id)
	want := "ClusterForge zag in de twee uur ervoor geen verandering. De metrics waren niet te lezen; de oorzaak ligt waarschijnlijk buiten de nodes."
	if a.Changes != 0 || len(a.Causes) != 0 || a.Conclusion != want {
		t.Fatalf("analyse = %+v", a)
	}
}

// TestTimelineMerges: de tijdlijn voegt het logboek en het journaal samen,
// oudste eerst, zonder beveiliging, en kapt af op de laatste regels.
func TestTimelineMerges(t *testing.T) {
	w := newWorld(t)
	t0 := w.now.Add(-time.Hour)
	w.event(t0, "user", uuid.NewString(), nil, "auth.login", map[string]any{"node_id": w.web1.String()})
	w.nodeDown(w.web1, "web-01", &w.web, t0.Add(2*time.Minute), "degraded", "service nginx is gefaald")
	w.journal(w.web1, &w.web, t0.Add(time.Minute), journal.SourceAgent,
		journal.Entry{Kind: journal.KindService, Subject: "nginx.service", Action: "failed"})
	w.journal(w.web1, &w.web, t0.Add(3*time.Minute), journal.SourceAgent,
		journal.Entry{Kind: journal.KindService, Subject: "nginx.service", Action: "started"})
	w.journal(w.web2, &w.web, t0.Add(4*time.Minute), journal.SourceAgent,
		journal.Entry{Kind: journal.KindLogin, Subject: "root", Action: "login"})
	au := w.analyzer().au
	entries, more, err := Timeline(w.ctx, w.q, au, Scope{Nodes: []uuid.UUID{w.web1}}, t0.Add(-time.Minute), w.now, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, string(e.Class)+" "+e.Summary)
	}
	want := []string{
		"symptom nginx.service mislukt",
		"symptom Node web-01 van gezond naar verminderd: service nginx is gefaald",
		"change nginx.service gestart",
	}
	if more || len(got) != len(want) || got[0] != want[0] || got[2] != want[2] || entries[1].Action != "node.status_changed" ||
		entries[1].Delay == "" || entries[0].Cluster != "Web" {
		t.Fatalf("tijdlijn = %q, meer %v", got, more)
	}
	entries, more, err = Timeline(w.ctx, w.q, au, Scope{Clusters: []uuid.UUID{w.web}}, t0.Add(-time.Minute), w.now, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !more || len(entries) != 2 || entries[1].Hostname != "web-02" {
		t.Fatalf("laatste twee van het cluster: %+v, meer %v", entries, more)
	}
	if entries, _, _ := Timeline(w.ctx, w.q, au, Scope{}, t0, w.now, 10); len(entries) != 0 {
		t.Fatalf("lege scope: %+v", entries)
	}
}
