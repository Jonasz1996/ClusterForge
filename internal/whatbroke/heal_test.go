package whatbroke

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/audit"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/journal"
)

// heal maakt een regel uit het logboek van een taak van Zelfherstel op
// cluster: een job.* over de taak zelf, de rest over het cluster.
func (f *fixture) heal(ts time.Time, action, summary string, cluster, job uuid.UUID) Entry {
	title := "Zelfherstel: MariaDB van " + f.world.Clusters[cluster]
	subject := audit.Subject{Type: "cluster", ID: cluster.String()}
	if strings.HasPrefix(action, "job.") {
		subject = audit.Subject{Type: "job", ID: job.String(), Name: title}
	}
	en := f.event(ts, action, summary, subject, &cluster, nil, &job,
		map[string]any{"job_id": job.String(), "title": title, "what": "MariaDB van " + f.world.Clusters[cluster]})
	en.Job = title
	return en
}

// Een herstel is een eigen soort met de taak als link, ook als het cluster
// of de node weg is. Vanzelf herstellen aan- of uitzetten heeft geen taak.
func TestFromEventHeal(t *testing.T) {
	f := newFixture()
	job := uuid.New()
	done := f.heal(t0, "cluster.heal_done", "Zelfherstel van MariaDB van Web gelukt", f.web, job)
	if done.Kind != "heal" || done.Class != events.ClassChange || done.Link != "/taken/detail?id="+job.String() ||
		done.JobID == nil || *done.JobID != job {
		t.Fatalf("herstel = %+v", done)
	}
	failed := FromEvent(audit.Entry{
		ID: 1, Ts: t0, Action: "node.heal_failed", Summary: "Zelfherstel van MariaDB op solo mislukt: mariadb start niet",
		Subject: audit.Subject{Type: "node", ID: f.solo.ID.String(), Name: "solo", Deleted: true},
		Node:    &audit.Ref{ID: f.solo.ID.String(), Name: "solo", Deleted: true},
		Job:     &audit.Ref{ID: job.String(), Name: "Zelfherstel: MariaDB op solo"},
	})
	if failed.Kind != "heal" || failed.Class != events.ClassInfo || failed.Link != "/taken/detail?id="+job.String() ||
		failed.Hostname != "solo" {
		t.Fatalf("mislukt herstel op een verwijderde node = %+v", failed)
	}
	auto := f.event(t0, "cluster.heal_auto_changed", "Zelfherstel vanzelf aangezet op cluster Web",
		audit.Subject{Type: "cluster", ID: f.web.String()}, &f.web, nil, nil, map[string]any{"name": "Web", "enabled": true})
	if auto.Kind != "heal" || auto.Link != "/clusters/detail?id="+f.web.String() {
		t.Fatalf("vanzelf aangezet = %+v", auto)
	}
	for _, a := range RepairActions {
		if !isHeal(a) || !slices.Contains(timelineActions, a) {
			t.Errorf("%s is geen herstelactie in de tijdlijn", a)
		}
	}
}

// Een herstel voor het eerste symptoom kan de oorzaak zijn, zoals een
// herstart of een bootstrap van Galera. Het herstel beschrijft de taak, en
// een mislukte taak blijft voorgaan.
func TestAnalyzeHealBeforeSymptom(t *testing.T) {
	f := newFixture()
	job := uuid.New()
	queued := f.heal(t0.Add(-6*time.Minute), "job.queued", "Taak aangevraagd: Zelfherstel: MariaDB van Web", f.web, job)
	started := f.heal(t0.Add(-6*time.Minute), "cluster.heal_started", "Zelfherstel gestart voor MariaDB van Web", f.web, job)
	done := f.heal(t0.Add(-5*time.Minute), "cluster.heal_done", "Zelfherstel van MariaDB van Web gelukt", f.web, job)
	succeeded := f.heal(t0.Add(-5*time.Minute), "job.succeeded", "Taak gelukt: Zelfherstel: MariaDB van Web", f.web, job)
	in := Input{
		Started:        t0,
		Subjects:       []Subject{clusterSubject(f, f.web, "degraded", t0)},
		World:          f.world,
		Entries:        []Entry{queued, started, done, succeeded, f.clusterStatus(f.web, t0, "healthy", "degraded")},
		MetricsUnknown: true,
	}
	a := Analyze(in)
	if len(a.Causes) != 1 || a.Changes != 4 {
		t.Fatalf("het herstel is één oorzaak: %+v (%d veranderingen)", a.Causes, a.Changes)
	}
	top := a.Causes[0]
	if top.EntryID != done.ID || top.Kind != "heal" || top.Summary != "Zelfherstel van MariaDB van Web gelukt" ||
		top.Link != "/taken/detail?id="+job.String() || top.Also != 3 || top.Level != LevelLikely || top.Cluster != "Web" {
		t.Fatalf("oorzaak = %+v", top)
	}
	if !slices.Contains(top.Reasons, "Samen met 3 andere regels van de taak 'Zelfherstel: MariaDB van Web'.") {
		t.Fatalf("redenen = %q", top.Reasons)
	}
	want := "Waarschijnlijke oorzaak: Zelfherstel van MariaDB van Web gelukt, 4 min voor het eerste symptoom."
	if a.Conclusion != want {
		t.Fatalf("conclusie = %q", a.Conclusion)
	}

	// Een mislukt herstel: de mislukte taak beschrijft de groep, want het
	// mislukken zelf is info.
	healFailed := f.heal(t0.Add(-5*time.Minute), "cluster.heal_failed", "Zelfherstel van MariaDB van Web mislukt: stap 2", f.web, job)
	jobFailed := f.heal(t0.Add(-5*time.Minute), "job.failed", "Taak mislukt: Zelfherstel: MariaDB van Web: stap 2", f.web, job)
	in.Entries = []Entry{queued, started, healFailed, jobFailed, f.clusterStatus(f.web, t0, "healthy", "degraded")}
	a = Analyze(in)
	if len(a.Causes) != 1 || a.Causes[0].EntryID != jobFailed.ID || a.Causes[0].Kind != "job" ||
		!slices.Contains(a.Causes[0].Reasons, "De wijziging mislukte.") {
		t.Fatalf("mislukt herstel = %+v", a.Causes)
	}
}

// Een herstel na het eerste symptoom is een antwoord op de storing: geen
// oorzaak en geen verandering, net als de andere regels van zijn taak.
func TestAnalyzeHealIsResponse(t *testing.T) {
	f := newFixture()
	job := uuid.New()
	in := Input{
		Started:  t0,
		Subjects: []Subject{clusterSubject(f, f.db, "down", t0)},
		World:    f.world,
		Entries: []Entry{
			f.clusterStatus(f.db, t0, "healthy", "down"),
			f.heal(t0.Add(40*time.Second), "cluster.heal_started", "ClusterForge herstelt MariaDB van DB vanzelf", f.db, job),
			f.heal(t0.Add(40*time.Second), "job.queued", "Taak aangevraagd: Zelfherstel (vanzelf): MariaDB van DB", f.db, job),
			f.change(f.db1, t0.Add(45*time.Second), journal.SourceClusterForge,
				journal.Entry{Kind: journal.KindService, Subject: "mariadb.service", Action: "restarted", By: "clusterforge"}, &job),
			f.heal(t0.Add(55*time.Second), "cluster.heal_done", "Zelfherstel van MariaDB van DB gelukt", f.db, job),
			f.heal(t0.Add(55*time.Second), "job.succeeded", "Taak gelukt: Zelfherstel (vanzelf): MariaDB van DB", f.db, job),
		},
		MetricsUnknown: true,
	}
	a := Analyze(in)
	want := "ClusterForge zag in de twee uur ervoor geen verandering. De metrics waren niet te lezen; de oorzaak ligt waarschijnlijk buiten de nodes."
	if len(a.Causes) != 0 || a.Changes != 0 || a.Conclusion != want {
		t.Fatalf("analyse = %+v", a)
	}

	// Een andere taak vlak na het eerste symptoom telt wel: alleen een
	// herstel is een antwoord.
	other := uuid.New()
	in.Entries = append(in.Entries, f.event(t0.Add(50*time.Second), "job.queued", "Taak aangevraagd: DB bijwerken",
		audit.Subject{Type: "job", ID: other.String()}, &f.db, nil, &other, nil))
	if a := Analyze(in); a.Changes != 1 || len(a.Causes) != 1 || a.Causes[0].Level != LevelPossible {
		t.Fatalf("een andere taak = %+v (%d veranderingen)", a.Causes, a.Changes)
	}
}

// TestTimelineHeal: een herstel staat in de tijdlijn als zelfherstel, met
// de taak uit het logboek, ook bij een losse node en bij een node die weg is.
func TestTimelineHeal(t *testing.T) {
	w := newWorld(t)
	t0 := w.now.Add(-10 * time.Minute)
	var job uuid.UUID
	w.scan(&job, `INSERT INTO jobs (kind, title, status, cluster_id) VALUES ('heal.run', 'Zelfherstel: MariaDB van Web', 'succeeded', $1)
		RETURNING id`, w.web)
	w.event(t0, "cluster", w.web.String(), &w.web, "cluster.heal_done",
		map[string]any{"job_id": job.String(), "title": "Zelfherstel: MariaDB van Web", "what": "MariaDB van Web", "name": "Web"})
	soloJob, oldJob := uuid.New(), uuid.New()
	w.event(t0.Add(time.Minute), "node", w.solo.String(), nil, "node.heal_failed", map[string]any{
		"job_id": soloJob.String(), "title": "Zelfherstel: MariaDB op solo", "what": "MariaDB op solo", "hostname": "solo",
		"error": "mariadb start niet",
	})
	oldNode := uuid.New()
	w.event(t0.Add(2*time.Minute), "node", oldNode.String(), nil, "node.heal_done",
		map[string]any{"job_id": oldJob.String(), "title": "Zelfherstel: MySQL op oud", "what": "MySQL op oud", "hostname": "oud"})
	au := w.analyzer().au

	entries, _, err := Timeline(w.ctx, w.q, au, Scope{Clusters: []uuid.UUID{w.web}}, t0.Add(-time.Minute), w.now, 10)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(entries, func(e Entry) bool { return e.Action == "cluster.heal_done" })
	if i < 0 {
		t.Fatalf("geen herstel in de tijdlijn van het cluster: %+v", entries)
	}
	if e := entries[i]; e.Kind != "heal" || e.Class != events.ClassChange || e.JobID == nil || *e.JobID != job ||
		e.Job != "Zelfherstel: MariaDB van Web" || e.Link != "/taken/detail?id="+job.String() ||
		e.Summary != "Zelfherstel van MariaDB van Web gelukt" || e.Cluster != "Web" {
		t.Fatalf("herstel = %+v", e)
	}

	entries, _, err = Timeline(w.ctx, w.q, au, Scope{Nodes: []uuid.UUID{w.solo}}, t0.Add(-time.Minute), w.now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("tijdlijn van solo = %+v", entries)
	}
	if e := entries[0]; e.Kind != "heal" || e.Class != events.ClassInfo || e.Hostname != "solo" ||
		e.Job != "Zelfherstel: MariaDB op solo" || e.Link != "/taken/detail?id="+soloJob.String() ||
		e.Summary != "Zelfherstel van MariaDB op solo mislukt: mariadb start niet" {
		t.Fatalf("mislukt herstel op solo = %+v", e)
	}

	entries, _, err = Timeline(w.ctx, w.q, au, Scope{Nodes: []uuid.UUID{oldNode}}, t0.Add(-time.Minute), w.now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Link != "/taken/detail?id="+oldJob.String() || entries[0].Hostname != "oud" {
		t.Fatalf("herstel op een verwijderde node = %+v", entries)
	}
}

// TestRepairsAfterWindow: een herstel meer dan een half uur na het begin
// van een incident valt buiten de tijdlijn van de analyse, maar staat bij
// de herstelacties. Vanzelf herstellen aanzetten is geen herstel.
func TestRepairsAfterWindow(t *testing.T) {
	w := newWorld(t)
	t0 := w.now.Add(-50 * time.Minute)
	job, late := uuid.New(), uuid.New()
	w.event(t0.Add(3*time.Minute), "cluster", w.web.String(), &w.web, "cluster.heal_started",
		map[string]any{"job_id": job.String(), "what": "MariaDB van Web", "name": "Web", "auto": true})
	w.event(t0.Add(40*time.Minute), "cluster", w.web.String(), &w.web, "cluster.heal_done",
		map[string]any{"job_id": late.String(), "what": "MariaDB van Web", "name": "Web"})
	w.event(t0.Add(41*time.Minute), "cluster", w.web.String(), &w.web, "cluster.heal_auto_changed",
		map[string]any{"name": "Web", "enabled": true})
	id := w.incident(t0, subject{"cluster", w.web, "Web", &w.web, "down"})
	inc, err := w.q.GetIncident(w.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	from, to := AnalysisWindow(inc, w.now)
	sc := Scope{Clusters: []uuid.UUID{w.web}, Others: []uuid.UUID{id}}
	au := w.analyzer().au
	entries, _, err := Timeline(w.ctx, w.q, au, sc, from, to, MaxEntries)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	if !slices.Contains(actions, "cluster.heal_started") || slices.Contains(actions, "cluster.heal_done") {
		t.Fatalf("tijdlijn van de analyse = %q", actions)
	}
	repairs, more, err := Repairs(w.ctx, au, sc, to, w.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if more || len(repairs) != 1 || repairs[0].Action != "cluster.heal_done" || repairs[0].Link != "/taken/detail?id="+late.String() {
		t.Fatalf("herstelacties na het venster = %+v", repairs)
	}
	if r, more, err := Repairs(w.ctx, au, sc, to, to); err != nil || more || len(r) != 0 {
		t.Fatalf("leeg venster = %+v, %v", r, err)
	}
}

// TestRepairScope: de herstelacties van een incident gaan over de geraakte
// clusters met hun leveranciers en de nodes die zelf misgingen, niet over
// een losse node op dezelfde Proxmox-host.
func TestRepairScope(t *testing.T) {
	f := newFixture()
	id := uuid.New()
	subjects := []Subject{
		{Type: "cluster", ID: f.web, Name: "Web", Status: "down", Since: t0},
		{Type: "node", ID: f.web1.ID, Name: "web-01", ClusterID: &f.web, Status: "down", Since: t0},
	}
	full := ScopeFor(id, subjects, f.world)
	if !slices.Contains(full.Nodes, f.solo.ID) {
		t.Fatalf("ScopeFor = %+v", full)
	}
	sc := RepairScope(id, subjects, f.world)
	if !slices.Equal(sc.Clusters, full.Clusters) || !slices.Equal(sc.Nodes, []uuid.UUID{f.web1.ID}) || !slices.Equal(sc.Others, []uuid.UUID{id}) {
		t.Fatalf("scope = %+v, ScopeFor = %+v", sc, full)
	}
	solo := RepairScope(id, []Subject{{Type: "node", ID: f.solo.ID, Name: "solo", Status: "down", Since: t0}}, f.world)
	if len(solo.Clusters) != 0 || !slices.Equal(solo.Nodes, []uuid.UUID{f.solo.ID}) {
		t.Fatalf("losse node = %+v", solo)
	}
}
