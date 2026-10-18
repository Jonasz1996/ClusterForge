package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// healEntry is een regel van de tijdlijn van een incident, met de taak.
type healEntry struct {
	Ts      time.Time `json:"ts"`
	Class   string    `json:"class"`
	Kind    string    `json:"kind"`
	Action  string    `json:"action"`
	Summary string    `json:"summary"`
	Cluster string    `json:"cluster"`
	JobID   *string   `json:"job_id"`
	Job     string    `json:"job"`
	Link    string    `json:"link"`
}

// TestIncidentTimelineHeal: de herstelacties van Zelfherstel staan in de
// tijdlijn en de geschiedenis van een incident, oudste eerst, met de taak
// als link. Ook een herstel meer dan een half uur na het begin; een herstel
// van een ander cluster en het aanzetten van vanzelf herstellen niet.
func TestIncidentTimelineHeal(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	var db, other cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "db", "name": "DB", "type": "generic", "environment": "lab"}, &db)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "ander", "name": "Ander", "type": "generic", "environment": "lab"}, &other)
	write := func(ts time.Time, cl cluster, action string, payload map[string]any) {
		t.Helper()
		p, _ := json.Marshal(payload)
		if _, err := e.pool.Exec(ctx, `INSERT INTO events (ts, actor_type, subject_type, subject_id, cluster_id, action, payload)
			VALUES ($1, 'system', 'cluster', $2, $3, $4, $5)`, ts, cl.ID, uuid.MustParse(cl.ID), action, p); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now().UTC().Add(-50 * time.Minute).Truncate(time.Second)
	first, second := uuid.NewString(), uuid.NewString()
	title := "Zelfherstel (vanzelf): MariaDB van DB"
	heal := func(job string) map[string]any {
		return map[string]any{"what": "MariaDB van DB", "auto": true, "job_id": job, "title": title, "name": "DB"}
	}
	failed := heal(first)
	failed["error"] = "stap 1: mariadb start niet"
	write(t0.Add(3*time.Minute), db, "cluster.heal_started", heal(first))
	write(t0.Add(5*time.Minute), db, "cluster.heal_failed", failed)
	write(t0.Add(35*time.Minute), db, "cluster.heal_started", heal(second))
	write(t0.Add(36*time.Minute), other, "cluster.heal_started",
		map[string]any{"what": "MariaDB van Ander", "auto": true, "job_id": uuid.NewString(), "name": "Ander"})
	write(t0.Add(37*time.Minute), db, "cluster.heal_auto_changed", map[string]any{"name": "DB", "enabled": true})
	write(t0.Add(40*time.Minute), db, "cluster.heal_auto_paused",
		map[string]any{"name": "DB", "reason": "3 pogingen op rij mislukten", "job_id": second})

	q := store.New(e.pool)
	inc, err := q.CreateIncident(ctx, store.CreateIncidentParams{Title: "Cluster DB is down", Severity: "high", StartedAt: t0, DetectedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	dbID := uuid.MustParse(db.ID)
	if err := q.UpsertIncidentSubject(ctx, store.UpsertIncidentSubjectParams{
		IncidentID: inc.ID, SubjectType: "cluster", SubjectID: dbID, Name: "DB", ClusterID: &dbID, Since: t0,
		FirstStatus: "down", WorstStatus: "down", LastStatus: "down",
	}); err != nil {
		t.Fatal(err)
	}

	var d struct {
		incidentDetail
		Timeline []healEntry `json:"timeline"`
	}
	if s := c.do("GET", "/api/v1/incidents/"+inc.ID.String(), nil, &d); s != http.StatusOK {
		t.Fatalf("incident: %d", s)
	}
	var heals []healEntry
	var actions []string
	for _, en := range d.Timeline {
		if en.Kind == "heal" {
			heals, actions = append(heals, en), append(actions, en.Action)
		}
	}
	want := []string{"cluster.heal_started", "cluster.heal_failed", "cluster.heal_started", "cluster.heal_auto_paused"}
	if !slices.Equal(actions, want) {
		t.Fatalf("herstelacties in de tijdlijn = %q", actions)
	}
	if h := heals[0]; h.Class != "change" || h.Summary != "ClusterForge herstelt MariaDB van DB vanzelf" || h.Cluster != "DB" ||
		h.JobID == nil || *h.JobID != first || h.Job != title || h.Link != "/taken/detail?id="+first || !h.Ts.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("eerste herstel = %+v", h)
	}
	if h := heals[1]; h.Class != "info" || h.Summary != "Zelfherstel van MariaDB van DB mislukt: stap 1: mariadb start niet" {
		t.Fatalf("mislukt herstel = %+v", h)
	}
	// Na het half uur van de analyse: de derde poging en de pauze.
	if h := heals[2]; !h.Ts.Equal(t0.Add(35*time.Minute)) || h.Link != "/taken/detail?id="+second {
		t.Fatalf("late poging = %+v", h)
	}
	if h := heals[3]; h.Summary != "Zelfherstel vanzelf gepauzeerd op cluster DB: 3 pogingen op rij mislukten" ||
		h.Link != "/taken/detail?id="+second {
		t.Fatalf("pauze = %+v", h)
	}
	for i := 1; i < len(d.Timeline); i++ {
		if d.Timeline[i].Ts.Before(d.Timeline[i-1].Ts) {
			t.Fatalf("de tijdlijn staat niet op tijd: %+v na %+v", d.Timeline[i], d.Timeline[i-1])
		}
	}

	var history []string
	for _, h := range d.History {
		history = append(history, h.Action)
	}
	for _, a := range want {
		if !slices.Contains(history, a) {
			t.Errorf("geen %s in de geschiedenis: %q", a, history)
		}
	}
	if slices.Contains(history, "cluster.heal_auto_changed") {
		t.Errorf("vanzelf herstellen aanzetten staat in de geschiedenis: %q", history)
	}
	for _, h := range d.History {
		if h.Cluster != nil && h.Cluster.Name == "Ander" {
			t.Errorf("het andere cluster staat in de geschiedenis: %+v", h)
		}
	}
}
