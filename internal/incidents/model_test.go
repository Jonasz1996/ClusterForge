package incidents

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

func actionsOf(evs []Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Action+" "+e.Incident.Title)
	}
	return out
}

// sim speelt de detector na over een reeks rondes, zonder database.
type sim struct {
	t     *testing.T
	rules Rules
	incs  []*Incident
	world World
}

func newSim(t *testing.T) *sim {
	return &sim{t: t, rules: DefaultRules, world: World{Hosts: map[Key][]string{}, Related: map[[2]uuid.UUID]bool{}, Status: map[Key]string{}}}
}

func (s *sim) step(at time.Time, problems ...Problem) []string {
	s.t.Helper()
	s.world.Problems = problems
	var evs []Event
	s.incs, evs = Step(s.rules, s.world, s.incs, at)
	// Wat de database zou doen: een id voor nieuwe incidenten, en alleen
	// open of recent gesloten incidenten blijven geladen.
	var kept []*Incident
	for i, inc := range s.incs {
		if inc.ID == uuid.Nil {
			inc.ID, inc.Number = uuid.New(), int32(i+1)
		}
		if inc.Open || at.Sub(*inc.ResolvedAt) <= s.rules.Reopen {
			kept = append(kept, inc)
		}
	}
	s.incs = kept
	return actionsOf(evs)
}

func (s *sim) open() []*Incident {
	var out []*Incident
	for _, inc := range s.incs {
		if inc.Open {
			out = append(out, inc)
		}
	}
	return out
}

func cluster(id uuid.UUID, name, env, status string, since time.Time) Problem {
	return Problem{Key: Key{SubjectCluster, id}, Name: name, ClusterID: &id, Env: env, Status: status, Since: since, Primary: true}
}

func node(id uuid.UUID, name string, clusterID *uuid.UUID, env, status string, since time.Time) Problem {
	return Problem{Key: Key{SubjectNode, id}, Name: name, ClusterID: clusterID, Env: env, Status: status, Since: since,
		Primary: clusterID == nil}
}

func TestClusterIncidentLifecycle(t *testing.T) {
	s := newSim(t)
	web, web01 := uuid.New(), uuid.New()
	s.world.Status[Key{SubjectCluster, web}] = "healthy"
	s.world.Status[Key{SubjectNode, web01}] = "healthy"

	// Een node in een cluster opent zelf niets.
	if got := s.step(t0, node(web01, "web-01", &web, "prod", "down", t0)); len(got) != 0 || len(s.incs) != 0 {
		t.Fatalf("node alleen: %v", got)
	}
	// Het cluster wordt verminderd: één incident, met de node erbij.
	got := s.step(t0.Add(5*time.Second),
		node(web01, "web-01", &web, "prod", "down", t0),
		cluster(web, "Web", "prod", "degraded", t0.Add(4*time.Second)))
	if !slices.Equal(got, []string{"incident.opened Cluster Web is verminderd"}) {
		t.Fatalf("geopend: %v", got)
	}
	inc := s.open()[0]
	if inc.Severity != SeverityHigh || len(inc.Subjects) != 2 || !inc.StartedAt.Equal(t0) {
		t.Fatalf("incident: %+v", inc)
	}
	// Het cluster gaat down: kritiek, en dat staat één keer in het logboek.
	got = s.step(t0.Add(time.Minute),
		node(web01, "web-01", &web, "prod", "down", t0),
		cluster(web, "Web", "prod", "down", t0.Add(time.Minute)))
	if !slices.Equal(got, []string{"incident.escalated Cluster Web is verminderd"}) || inc.Severity != SeverityCritical {
		t.Fatalf("erger: %v %s", got, inc.Severity)
	}
	if sub := inc.subject(Key{SubjectCluster, web}); sub.First != "degraded" || sub.Worst != "down" || sub.Last != "down" {
		t.Fatalf("onderwerp: %+v", sub)
	}
	// Weer verminderd: de ernst blijft kritiek.
	if got := s.step(t0.Add(2*time.Minute), cluster(web, "Web", "prod", "degraded", t0.Add(2*time.Minute))); len(got) != 0 || inc.Severity != SeverityCritical {
		t.Fatalf("minder erg: %v %s", got, inc.Severity)
	}
	if sub := inc.subject(Key{SubjectNode, web01}); sub.Until == nil || sub.Last != "healthy" {
		t.Fatalf("node hersteld: %+v", sub)
	}
	// Alles gezond: pas na vijf minuten dicht.
	if got := s.step(t0.Add(3 * time.Minute)); len(got) != 0 || !inc.Open {
		t.Fatalf("meteen dicht: %v", got)
	}
	if got := s.step(t0.Add(7 * time.Minute)); len(got) != 0 {
		t.Fatalf("na vier minuten: %v", got)
	}
	if got := s.step(t0.Add(8 * time.Minute)); !slices.Equal(got, []string{"incident.resolved Cluster Web is verminderd"}) || inc.Open {
		t.Fatalf("dicht: %v", got)
	}
	// Binnen een kwartier terug: hetzelfde incident gaat weer open.
	got = s.step(t0.Add(20*time.Minute), cluster(web, "Web", "prod", "degraded", t0.Add(20*time.Minute)))
	if !slices.Equal(got, []string{"incident.reopened Cluster Web is verminderd"}) || !inc.Open || inc.ResolvedAt != nil || len(s.incs) != 1 {
		t.Fatalf("opnieuw: %v", got)
	}
	// Dicht, en na meer dan een kwartier is het een nieuw incident.
	s.step(t0.Add(21 * time.Minute))
	s.step(t0.Add(27 * time.Minute))
	got = s.step(t0.Add(50*time.Minute), cluster(web, "Web", "prod", "degraded", t0.Add(50*time.Minute)))
	if !slices.Equal(got, []string{"incident.opened Cluster Web is verminderd"}) || len(s.open()) != 1 || s.open()[0] == inc {
		t.Fatalf("nieuw: %v", got)
	}
}

func TestJoinRelated(t *testing.T) {
	s := newSim(t)
	web, db, dns, other := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	s.world.Related[[2]uuid.UUID{web, db}], s.world.Related[[2]uuid.UUID{db, web}] = true, true
	s.world.Hosts[Key{SubjectCluster, db}] = []string{"pve/pve1"}
	s.world.Hosts[Key{SubjectCluster, dns}] = []string{"pve/pve1"}
	s.world.Hosts[Key{SubjectCluster, other}] = []string{"pve/pve2"}

	s.step(t0, cluster(db, "DB", "prod", "down", t0))
	// Web hangt van DB af, DNS draait op dezelfde host, Other nergens mee.
	got := s.step(t0.Add(2*time.Minute),
		cluster(db, "DB", "prod", "down", t0),
		cluster(web, "Web", "prod", "degraded", t0.Add(2*time.Minute)),
		cluster(dns, "DNS", "lab", "down", t0.Add(3*time.Minute)),
		cluster(other, "Other", "lab", "down", t0.Add(time.Minute)))
	if !slices.Equal(got, []string{"incident.opened Cluster Other is down"}) {
		t.Fatalf("rondes: %v", got)
	}
	open := s.open()
	if len(open) != 2 || len(open[0].Subjects) != 3 || len(open[1].Subjects) != 1 {
		t.Fatalf("groepen: %d", len(open))
	}
	// Na tien minuten hoort een nieuwe overgang er niet meer bij.
	db2 := uuid.New()
	s.world.Related[[2]uuid.UUID{web, db2}] = true
	got = s.step(t0.Add(15*time.Minute),
		cluster(db, "DB", "prod", "down", t0),
		cluster(db2, "DB2", "prod", "down", t0.Add(15*time.Minute)))
	if !slices.Equal(got, []string{"incident.opened Cluster DB2 is down"}) {
		t.Fatalf("te laat: %v", got)
	}
}

func TestStandaloneNodeAndService(t *testing.T) {
	s := newSim(t)
	n, svc := uuid.New(), uuid.New()
	got := s.step(t0,
		node(n, "backup-01", nil, "", "down", t0),
		Problem{Key: Key{SubjectService, svc}, Name: "mariadb", Status: "down", Since: t0, Primary: true})
	want := []string{"incident.opened backup-01 is down", "incident.opened Dienst mariadb is down"}
	if !slices.Equal(got, want) {
		t.Fatalf("kreeg %v", got)
	}
	for _, inc := range s.open() {
		if inc.Severity != SeverityLow {
			t.Fatalf("zonder omgeving: %s", inc.Severity)
		}
	}
}

func TestCausedByTest(t *testing.T) {
	s := newSim(t)
	lb, run := uuid.New(), uuid.New()
	p := cluster(lb, "LB", "test", "degraded", t0)
	p.TestRun = &run
	s.step(t0, p)
	inc := s.open()[0]
	if !inc.CausedByTest || inc.TestRunID == nil || *inc.TestRunID != run {
		t.Fatalf("door test: %+v", inc)
	}
	// Een echte storing in hetzelfde cluster is een eigen incident.
	n := uuid.New()
	s.step(t0.Add(time.Minute), p, node(n, "solo", nil, "", "down", t0.Add(time.Minute)), cluster(uuid.New(), "LB2", "test", "down", t0.Add(time.Minute)))
	if len(s.open()) != 3 {
		t.Fatalf("incidenten: %d", len(s.open()))
	}
}

func TestFailedScheduledTest(t *testing.T) {
	s := newSim(t)
	lb, run := uuid.New(), uuid.New()
	p := Problem{Key: Key{SubjectTestRun, run}, Name: "Geplande failovertest op LB", ClusterID: &lb, Env: "lab", Status: "fail",
		Since: t0, Primary: true, Point: true}
	if got := s.step(t0.Add(time.Second), p); !slices.Equal(got, []string{"incident.opened Geplande failovertest op LB mislukt"}) {
		t.Fatalf("geopend: %v", got)
	}
	// Hij blijft in de lijst van de detector staan, maar telt één keer en
	// opent niets opnieuw.
	if got := s.step(t0.Add(6*time.Minute), p); !slices.Equal(got, []string{"incident.resolved Geplande failovertest op LB mislukt"}) {
		t.Fatalf("dicht: %v", got)
	}
	if got := s.step(t0.Add(8*time.Minute), p); len(got) != 0 {
		t.Fatalf("opnieuw: %v", got)
	}
}

// Een dienst in een cluster dat al lang in een incident zit, hoort daarbij,
// ook buiten de tien minuten; is het cluster weer goed, dan niet meer.
func TestServiceJoinsClusterIncident(t *testing.T) {
	s := newSim(t)
	web, ka, ng := uuid.New(), uuid.New(), uuid.New()
	s.step(t0, cluster(web, "Web", "prod", "down", t0))
	svc := func(id uuid.UUID, name string, at time.Time) Problem {
		return Problem{Key: Key{SubjectService, id}, Name: name, ClusterID: &web, Env: "prod", Status: "down", Since: at, Primary: true}
	}
	got := s.step(t0.Add(2*time.Hour), cluster(web, "Web", "prod", "down", t0), svc(ka, "keepalived", t0.Add(2*time.Hour)))
	if len(got) != 0 || len(s.incs) != 1 || len(s.incs[0].Subjects) != 2 {
		t.Fatalf("keepalived: %v, %d incidenten", got, len(s.incs))
	}
	// Het cluster is weer gezond, keepalived nog niet: nginx is een nieuw
	// incident.
	got = s.step(t0.Add(3*time.Hour), svc(ka, "keepalived", t0.Add(2*time.Hour)), svc(ng, "nginx", t0.Add(3*time.Hour)))
	if !slices.Equal(got, []string{"incident.opened Dienst nginx is down"}) {
		t.Fatalf("nginx: %v", got)
	}
}
