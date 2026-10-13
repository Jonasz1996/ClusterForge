package failover

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestVerdictWrites(t *testing.T) {
	def := Definition{Name: "database", Scenario: DatabaseStop, Unit: "mariadb", VIP: "192.168.1.185", MaxTakeoverSeconds: 20}
	plan := Plan{Target: health.Holder{ID: uuid.New(), Hostname: "nc1"}}
	ret := &returnState{Owner: "nc2"}
	m := &Measurement{DowntimeMS: 3400, Recovered: true, TakenOver: true, TakeoverNode: "nc2", WindowMS: 50000}
	w := func(change func(*WriteMeasurements)) *WriteMeasurements {
		down, lost := int64(9000), 0
		out := &WriteMeasurements{Node: "nc3", VIP: def.VIP, DowntimeMS: &down, Recovered: true, Confirmed: 120, Lost: &lost}
		if change != nil {
			change(out)
		}
		return out
	}
	for name, tc := range map[string]struct {
		o       outcome
		result  string
		summary string
	}{
		"pass": {outcome{injected: true, m: m, ret: ret, writes: w(nil)}, "pass",
			"PASS: schrijven via 192.168.1.185 lukte weer na 9 s, er ging niets verloren; overgenomen door nc2, daarna op nc2, alles hersteld"},
		"lukte steeds": {outcome{injected: true, m: m, ret: ret, writes: w(func(w *WriteMeasurements) { *w.DowntimeMS = 0 })}, "pass",
			"PASS: schrijven via 192.168.1.185 lukte steeds, er ging niets verloren; overgenomen door nc2, daarna op nc2, alles hersteld"},
		"rijen verloren": {outcome{injected: true, m: m, ret: ret, writes: w(func(w *WriteMeasurements) { *w.Lost = 3 })}, "fail",
			"FAIL: 3 bevestigde rijen verloren bij schrijven via 192.168.1.185: de database bevestigde ze, maar na de test waren ze weg; daarna op nc2, alles hersteld"},
		"verloren in een afgebroken test": {outcome{injected: true, canceled: true, m: &Measurement{Canceled: true}, ret: ret,
			writes: w(func(w *WriteMeasurements) { *w.Lost = 1 })}, "fail",
			"FAIL: 1 bevestigde rij verloren bij schrijven via 192.168.1.185: de database bevestigde ze, maar na de test waren ze weg; daarna op nc2, alles hersteld"},
		"schrijven te traag": {outcome{injected: true, m: m, ret: ret, writes: w(func(w *WriteMeasurements) { *w.DowntimeMS = 31200 })}, "fail",
			"FAIL: schrijven via 192.168.1.185 lukte 31,2 s niet, meer dan de verwachte 20 s; overgenomen door nc2, daarna op nc2, alles hersteld"},
		"schrijven lukte niet meer": {outcome{injected: true, m: m, ret: ret, writes: w(func(w *WriteMeasurements) { w.Recovered = false })}, "fail",
			"FAIL: schrijven via 192.168.1.185 lukte niet meer tot het einde van de meting (50 s); overgenomen door nc2, daarna op nc2, alles hersteld"},
		"niet geteld": {outcome{injected: true, m: m, ret: ret, writes: w(func(w *WriteMeasurements) { w.Lost, w.CheckError = nil, "geen antwoord" })}, "error",
			"ERROR: de schrijftest kon niet nagaan of er rijen verloren gingen: geen antwoord; overgenomen door nc2, daarna op nc2, alles hersteld"},
		"agent weg": {outcome{injected: true, m: m, ret: ret, writes: &WriteMeasurements{VIP: def.VIP, CheckError: "nc3 antwoordt niet"}}, "error",
			"ERROR: de schrijftest kon niet nagaan of er rijen verloren gingen: nc3 antwoordt niet; overgenomen door nc2, daarna op nc2, alles hersteld"},
	} {
		result, summary, _ := verdict(def, plan, tc.o)
		if result != tc.result || summary != tc.summary {
			t.Errorf("%s:\n kreeg  %s %q\n wilde  %s %q", name, result, summary, tc.result, tc.summary)
		}
	}
}

func TestDatabaseScenario(t *testing.T) {
	in := Input{Name: "db", Scenario: DatabaseStop, Service: "nginx", MaxTakeoverSeconds: 20, Probe: DefaultProbe}
	if err := in.validate(store.EnvironmentProd); err != nil {
		t.Fatalf("database_stop op prod met de hand: %v", err)
	}
	if Describe(DatabaseStop, "patroni") != "de database (patroni) stoppen op de eigenaar" || unitOf(DatabaseStop, "mariadb") != "mariadb" {
		t.Error("Describe of unitOf")
	}
	row := store.GetFailoverTestRow{FailoverTest: store.FailoverTest{Scenario: DatabaseStop, Service: "patroni", MaxTakeoverSeconds: 20,
		Probe: []byte(`{"tcp":{"port":5432}}`)}}
	if d, err := definitionOf(row); err != nil || d.Unit != "patroni" {
		t.Errorf("definitie: %v %+v", err, d)
	}
	row.FailoverTest.Service = "sshd"
	if _, err := definitionOf(row); err == nil {
		t.Error("database_stop met sshd")
	}
	row.FailoverTest.Scenario, row.FailoverTest.Service = ServiceStop, "patroni"
	if _, err := definitionOf(row); err == nil {
		t.Error("service_stop met patroni")
	}
}

func TestDatabasesOn(t *testing.T) {
	nc1, nc2, old := uuid.New(), uuid.New(), uuid.New()
	services := func(active ...string) []byte {
		m := map[string]string{}
		for _, s := range active {
			m[s] = "active"
		}
		b, _ := json.Marshal(m)
		return b
	}
	nodes := []store.ListFailoverNodesRow{
		{ID: nc1, Hostname: "nc1", Services: services("mariadb", "keepalived")},
		{ID: nc2, Hostname: "nc2", Services: services("mariadb", "postgresql")},
		{ID: old, Hostname: "db1", Services: services("mysql")},
	}
	dbs := []Database{{Engine: protocol.DBMariaDB, Nodes: []DatabaseNode{{ID: nc1}, {ID: nc2}}}}
	if got := databasesOn(nodes, dbs); !slices.Equal(got, []string{"postgresql op nc2", "mysql op db1"}) {
		t.Errorf("buiten de onderdelen: %q", got)
	}
	if got := databasesOn(nodes, nil); len(got) != 4 {
		t.Errorf("zonder onderdelen: %q", got)
	}
}

func TestWriteTarget(t *testing.T) {
	galera := Database{ComponentID: uuid.New(), VIP: "10.0.0.185"}
	pg := Database{ComponentID: uuid.New(), VIP: "10.0.0.190", OwnVIP: true}
	dbs := []Database{galera, pg}
	for name, tc := range map[string]struct {
		def     Definition
		target  []string
		want    uuid.UUID
		matched bool
	}{
		"VIP van de test":             {Definition{Scenario: KeepalivedStop, VIP: "10.0.0.185"}, []string{"10.0.0.185"}, galera.ComponentID, true},
		"keepalived neemt alles mee":  {Definition{Scenario: KeepalivedStop, VIP: "10.0.0.10"}, []string{"10.0.0.10", "10.0.0.190"}, pg.ComponentID, true},
		"nginx verplaatst alleen dat": {Definition{Scenario: ServiceStop, VIP: "10.0.0.10"}, []string{"10.0.0.10", "10.0.0.190"}, uuid.Nil, false},
		"geen database":               {Definition{Scenario: KeepalivedStop, VIP: "10.0.0.10"}, []string{"10.0.0.10"}, uuid.Nil, false},
	} {
		d, ok := writeTarget(dbs, tc.def, tc.target)
		if ok != tc.matched || d.ComponentID != tc.want {
			t.Errorf("%s: %v %v", name, d.ComponentID, ok)
		}
	}
}

// fakeDB speelt de databasekant na voor finishWrites.
type fakeDB struct {
	finish *protocol.DBProbeResult
	reqs   []protocol.DBProbeRequest
}

func (f *fakeDB) TestDatabases(context.Context, uuid.UUID) ([]Database, error) { return nil, nil }
func (f *fakeDB) TestHealth(context.Context, uuid.UUID, uuid.UUID) (bool, string, error) {
	return true, "", nil
}
func (f *fakeDB) PrepareWrites(context.Context, uuid.UUID, uuid.UUID, string) error { return nil }
func (f *fakeDB) WriteProbe(_ context.Context, _ uuid.UUID, _ string, req protocol.DBProbeRequest) (*protocol.DBProbeResult, error) {
	f.reqs = append(f.reqs, req)
	if req.Action == protocol.DBProbeFinish {
		return f.finish, nil
	}
	return &protocol.DBProbeResult{}, nil
}

func TestFinishWrites(t *testing.T) {
	start := time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)
	injected := start.Add(10 * time.Second)
	// Pogingen elke halve seconde vanaf de start: tot de storing goed, dan
	// 4 s mislukt, weer goed tot de terugkeer (na 30 s), daar 1 s mislukt.
	var attempts []protocol.DBProbeAttempt
	for i := range 100 {
		at := int64(i) * 500
		since := at - 10_000
		ok := (since < 1000 || since >= 5000) && (since < 35_000 || since >= 36_000)
		a := protocol.DBProbeAttempt{Seq: int64(i + 1), AtMS: at, OK: ok}
		if !ok {
			a.Error = "ERROR 2002 (HY000): Can't connect"
		}
		attempts = append(attempts, a)
	}
	db := &fakeDB{finish: &protocol.DBProbeResult{Attempts: attempts, Checked: true, Confirmed: 88, Missing: 0, Extra: 1}}
	s := &Service{DB: db, WriteFinishTimeout: time.Second, EmergencyTimeout: time.Second, log: slog.New(slog.DiscardHandler)}
	r := &testRun{s: s, run: store.TestRun{ID: uuid.New()}, injectedAt: injected, plan: Plan{Writes: &WritePlan{
		VIP: "10.0.0.185", Port: 3306, Title: "MariaDB met Galera", Prober: health.Holder{Hostname: "nc3"}, StartedAt: start,
	}}}
	w := r.finishWrites(context.Background(), outcome{injected: true, m: &Measurement{EndMS: 20_000}})
	switch {
	case w == nil || w.DowntimeMS == nil || *w.DowntimeMS != 4000 || !w.Recovered:
		t.Fatalf("tijdens de meting: %+v", w)
	case w.FailbackMS == nil || *w.FailbackMS != 1000:
		t.Fatalf("bij de terugkeer: %v", w.FailbackMS)
	case w.Lost == nil || *w.Lost != 0 || w.Confirmed != 88 || w.Extra != 1 || w.CheckError != "":
		t.Fatalf("tellen: %+v", w)
	case w.Attempts != 80 || w.Failed != 10:
		t.Fatalf("pogingen: %d, mislukt %d", w.Attempts, w.Failed)
	}
	var kinds []string
	for _, e := range w.Timeline {
		kinds = append(kinds, e.Kind+" "+e.Text)
	}
	want := []string{
		"write_down schrijven via 10.0.0.185 mislukt: ERROR 2002 (HY000): Can't connect",
		"write_up schrijven via 10.0.0.185 lukt weer",
		"write_down schrijven via 10.0.0.185 mislukt: ERROR 2002 (HY000): Can't connect bij de terugkeer",
		"write_up schrijven via 10.0.0.185 lukt weer",
	}
	if !slices.Equal(kinds, want) {
		t.Errorf("tijdlijn:\n%q", kinds)
	}
	if len(db.reqs) != 1 || db.reqs[0].Action != protocol.DBProbeFinish || db.reqs[0].Run != r.run.ID.String() {
		t.Errorf("verzoeken: %+v", db.reqs)
	}

	// Zonder storing stopt de schrijftest alleen.
	db.reqs = nil
	if w := r.finishWrites(context.Background(), outcome{}); w != nil || len(db.reqs) != 1 || db.reqs[0].Action != protocol.DBProbeStop {
		t.Errorf("zonder storing: %+v %+v", w, db.reqs)
	}
	// Kon de agent niet tellen, dan zegt het rapport waarom.
	db.finish = &protocol.DBProbeResult{Attempts: attempts, CheckError: "geen antwoord via 10.0.0.185"}
	if w := r.finishWrites(context.Background(), outcome{injected: true, m: &Measurement{EndMS: 20_000}}); w.Lost != nil || w.CheckError != "geen antwoord via 10.0.0.185" {
		t.Errorf("niet geteld: %+v", w)
	}
}
