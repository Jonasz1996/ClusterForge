package failover

import (
	"errors"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

func TestRestartScenario(t *testing.T) {
	in := Input{Name: "herstart", Scenario: RestartAll, MaxTakeoverSeconds: 1800, ExpectFailback: true, Probe: DefaultProbe}
	if err := in.validate(store.EnvironmentProd); err != nil || in.ExpectFailback {
		t.Fatalf("restart_all op prod met de hand: %v %+v", err, in)
	}
	for name, tc := range map[string]struct {
		in    Input
		field string
	}{
		"te lang":            {Input{Name: "x", Scenario: RestartAll, MaxTakeoverSeconds: 1801, Probe: DefaultProbe}, "max_takeover_seconds"},
		"nul":                {Input{Name: "x", Scenario: RestartAll, Probe: DefaultProbe}, "max_takeover_seconds"},
		"gepland op prod":    {Input{Name: "x", Scenario: RestartAll, MaxTakeoverSeconds: 300, Probe: DefaultProbe, Scheduled: true}, "scheduled"},
		"stoppen blijft 120": {Input{Name: "x", Scenario: DatabaseStop, MaxTakeoverSeconds: 121, Probe: DefaultProbe}, "max_takeover_seconds"},
	} {
		var fe *FieldError
		if err := tc.in.validate(store.EnvironmentProd); !errors.As(err, &fe) || fe.Field != tc.field {
			t.Errorf("%s: %v", name, err)
		}
	}
	if Describe(RestartAll, "patroni") != "alle nodes van de database (patroni) herstarten" || unitOf(RestartAll, "mariadb") != "mariadb" {
		t.Error("Describe of unitOf")
	}
	row := store.GetFailoverTestRow{FailoverTest: store.FailoverTest{Scenario: RestartAll, Service: "mariadb", MaxTakeoverSeconds: 600,
		Probe: []byte(`{"tcp":{"port":3306}}`)}}
	if d, err := definitionOf(row); err != nil || d.Unit != "mariadb" || d.expect().Seconds() != 600 {
		t.Errorf("definitie: %v %+v", err, d)
	}
	row.FailoverTest.Service = "nginx"
	if _, err := definitionOf(row); err == nil {
		t.Error("restart_all met nginx")
	}
	if joinNames(nil) != "" || joinNames([]string{"nc1"}) != "nc1" || joinNames([]string{"nc1", "nc2"}) != "nc1 en nc2" ||
		joinNames([]string{"nc1", "nc2", "nc3"}) != "nc1, nc2 en nc3" {
		t.Error("joinNames")
	}
}

func TestRestartVerdict(t *testing.T) {
	def := Definition{Name: "alles herstarten", Scenario: RestartAll, Unit: "mariadb", VIP: "10.0.20.185", MaxTakeoverSeconds: 300}
	plan := Plan{}
	ret := &returnState{Owner: "nc1"}
	ms := func(d int64) *int64 { return &d }
	m := func(change func(*Measurement, *RestartMeasurement)) *Measurement {
		rm := &RestartMeasurement{Title: "MariaDB Galera", Engine: "mariadb", Began: "nc2", Started: []string{"nc1", "nc3"},
			BackMS: ms(61000), HealthyMS: ms(154200), Nodes: []RestartNode{
				{Hostname: "nc1", BackMS: ms(58000)}, {Hostname: "nc2", BackMS: ms(61000)}, {Hostname: "nc3", BackMS: ms(59500)},
			}}
		out := &Measurement{DowntimeMS: 98000, Recovered: true, WindowMS: 600000, Restart: rm}
		if change != nil {
			change(out, rm)
		}
		return out
	}
	w := func(confirmed int, lost int) *WriteMeasurements {
		return &WriteMeasurements{Node: "nc2", VIP: def.VIP, Confirmed: confirmed, Lost: &lost}
	}
	const back = "; daarna op nc1, alles hersteld"
	for name, tc := range map[string]struct {
		o       outcome
		result  string
		summary string
	}{
		"pass": {outcome{injected: true, m: m(nil), ret: ret, writes: w(23, 0)}, "pass",
			"PASS: na het herstarten van nc1, nc2 en nc3 was MariaDB Galera na 2 min 34 s weer gezond (ClusterForge begon het cluster opnieuw op nc2), " +
				"10.0.20.185 antwoordde weer na 98 s; alle 23 bevestigde rijen waren er nog" + back},
		"vanzelf terug": {outcome{injected: true, ret: ret, writes: w(1, 0), m: m(func(m *Measurement, rm *RestartMeasurement) {
			m.DowntimeMS, rm.Title, rm.Began, rm.Started, rm.HealthyMS = 0, "PostgreSQL met Patroni", "", nil, ms(41000)
		})}, "pass",
			"PASS: na het herstarten van nc1, nc2 en nc3 was PostgreSQL met Patroni na 41 s weer gezond, 10.0.20.185 bleef antwoorden; " +
				"de bevestigde rij was er nog" + back},
		"opnieuw gestart": {outcome{injected: true, ret: ret, m: m(func(_ *Measurement, rm *RestartMeasurement) { rm.Began, rm.Started = "", []string{"nc3"} })},
			"pass", "PASS: na het herstarten van nc1, nc2 en nc3 was MariaDB Galera na 2 min 34 s weer gezond (ClusterForge startte MariaDB opnieuw op nc3), " +
				"10.0.20.185 antwoordde weer na 98 s" + back},
		"node niet terug": {outcome{injected: true, ret: ret, writes: w(23, 0), m: m(func(_ *Measurement, rm *RestartMeasurement) {
			rm.Nodes[2].BackMS, rm.BackMS, rm.HealthyMS = nil, nil, nil
		})}, "fail", "FAIL: nc3 was 10 min na het herstarten nog niet terug" + back},
		"niet gezond": {outcome{injected: true, ret: ret, m: m(func(m *Measurement, rm *RestartMeasurement) {
			rm.HealthyMS, rm.Reason, m.Recovered = nil, "nc3 is niet Synced", false
		})}, "fail", "FAIL: MariaDB Galera was 10 min na het herstarten nog niet gezond (nc3 is niet Synced)" + back},
		"opnieuw beginnen mislukt": {outcome{injected: true, ret: ret, m: m(func(_ *Measurement, rm *RestartMeasurement) {
			rm.HealthyMS, rm.Reason, rm.RecoverError = nil, "geen Primary component", "nc2: de agent antwoordt niet"
		})}, "fail", "FAIL: MariaDB Galera was 10 min na het herstarten nog niet gezond (opnieuw beginnen lukte niet: nc2: de agent antwoordt niet)" + back},
		"VIP antwoordt niet": {outcome{injected: true, ret: ret, m: m(func(m *Measurement, _ *RestartMeasurement) { m.Recovered = false })}, "fail",
			"FAIL: 10.0.20.185 antwoordde 10 min na het herstarten nog niet" + back},
		"te traag": {outcome{injected: true, ret: ret, m: m(func(_ *Measurement, rm *RestartMeasurement) { rm.HealthyMS = ms(320000) })}, "fail",
			"FAIL: MariaDB Galera was pas na 5 min 20 s weer gezond (ClusterForge begon het cluster opnieuw op nc2), meer dan de verwachte 5 min" + back},
		"rij verloren": {outcome{injected: true, ret: ret, m: m(nil), writes: w(23, 1)}, "fail",
			"FAIL: 1 bevestigde rij verloren bij schrijven via 10.0.20.185: de database bevestigde ze, maar na de test waren ze weg" + back},
		"niet nagekeken": {outcome{injected: true, ret: ret, m: m(nil), writes: &WriteMeasurements{VIP: def.VIP, Confirmed: 23, CheckError: "nc2 antwoordt niet"}},
			"error", "ERROR: de schrijftest kon niet nagaan of er rijen verloren gingen: nc2 antwoordt niet" + back},
		"geen meting": {outcome{injected: true, ret: ret, m: &Measurement{}}, "error", "ERROR: er is geen meting van de herstart" + back},
		"herstart deels": {outcome{injected: true, ret: ret, m: m(nil), injectErr: "de herstart lukte niet op nc3: geen antwoord"}, "error",
			"ERROR: storing niet gelukt: de herstart lukte niet op nc3: geen antwoord" + back},
	} {
		result, summary, restored := verdict(def, plan, tc.o)
		if result != tc.result || summary != tc.summary || restored == nil || !*restored {
			t.Errorf("%s:\n kreeg  %s %q\n wilde  %s %q", name, result, summary, tc.result, tc.summary)
		}
	}
}
