package disaster

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/failover"
)

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		role, predicted, measured, want string
	}{
		{RoleTarget, "down", "down", MatchAsPredicted},
		{RoleTarget, "down", "degraded", MatchMilder},
		{RoleTarget, "down", "healthy", MatchStayedHealthy},
		{RoleTarget, "degraded", "down", MatchUnexpected},
		{RoleTarget, "healthy", "degraded", MatchUnexpected},
		{RoleTarget, "split_brain", "down", MatchAsPredicted},
		// Verminderd is bij een cluster dat alleen afhangt niet te zien.
		{RoleDependent, "degraded", "healthy", MatchAsPredicted},
		{RoleDependent, "down", "healthy", MatchStayedHealthy},
		{RoleTarget, "degraded", "healthy", MatchStayedHealthy},
		{RoleTarget, "unknown", "down", MatchUnknown},
	} {
		if got := compare(tc.role, tc.predicted, tc.measured); got != tc.want {
			t.Errorf("%s voorspeld %s, gemeten %s: %s, verwacht %s", tc.role, tc.predicted, tc.measured, got, tc.want)
		}
	}
}

func TestVerdict(t *testing.T) {
	lb, app := uuid.New(), uuid.New()
	def := Definition{
		Name: "nginx weg", Kind: ServiceDown, Unit: "nginx", Expect: Expect{FaultSeconds: 60, RecoverySeconds: 120},
		Targets: []Target{{Hostname: "web01"}, {Hostname: "web02"}},
		Clusters: []ClusterPlan{
			{ID: lb, Name: "LB", Role: RoleTarget, Before: "healthy", Predicted: Prediction{Status: "down"}, VIPs: []VIPPlan{{Address: "10.0.0.10"}}},
			{ID: app, Name: "App", Role: RoleDependent, Before: "healthy", Predicted: Prediction{Status: "degraded"}},
		},
	}
	short := 10 * time.Second
	ms := func(d time.Duration) *int64 { v := d.Milliseconds(); return &v }
	rec := &recoveryState{Clusters: []ClusterRecovery{{ClusterID: lb, RecoveredMS: ms(4 * time.Second)}, {ClusterID: app, RecoveredMS: ms(6 * time.Second)}}}
	measure := func(lbOut time.Duration, appWorst string) *Measurement {
		return &Measurement{
			VIPs:   []VIPMeasure{{ClusterID: lb, Address: "10.0.0.10", LongestMS: lbOut.Milliseconds(), Error: "connection refused"}},
			Status: []StatusMeasure{{ClusterID: lb, Worst: "down"}, {ClusterID: app, Worst: appWorst, Reason: "php: nginx in LB is down"}},
		}
	}

	result, summary, restored, rows := verdict(def, outcome{injected: true, m: measure(60*time.Second, "degraded"), rec: rec}, short)
	if result != "pass" || summary != "PASS: 2 clusters gedroegen zich zoals voorspeld, alles hersteld na 6 s" || restored == nil || !*restored {
		t.Fatalf("zoals voorspeld: %s %q %v", result, summary, restored)
	}
	if rows[0].Measured.Status != "down" || rows[0].Match != MatchAsPredicted || rows[0].LongestMS != 60000 ||
		rows[1].Measured.Status != "degraded" || rows[1].Match != MatchAsPredicted || *rows[1].RecoveredMS != 6000 {
		t.Fatalf("rijen: %+v", rows)
	}

	// App bleef gezond: verminderd is met een probe niet te zien, dus nog
	// steeds zoals voorspeld.
	result, _, _, rows = verdict(def, outcome{injected: true, m: measure(60*time.Second, "healthy"), rec: rec}, short)
	if result != "pass" || !strings.Contains(rows[1].Measured.Reason, "verminderd is met een probe niet te zien") {
		t.Fatalf("app gezond: %s %+v", result, rows[1])
	}

	// Een korte onderbreking is een overname, geen uitval.
	m := measure(3*time.Second, "healthy")
	m.Status[0].Worst = "healthy"
	result, summary, _, rows = verdict(def, outcome{injected: true, m: m, rec: rec}, short)
	if result != "warning" || rows[0].Measured.Status != "healthy" || rows[0].Match != MatchStayedHealthy ||
		!strings.Contains(rows[0].Measured.Reason, "korte onderbreking: 10.0.0.10 3 s onbereikbaar") ||
		!strings.HasPrefix(summary, "WARNING: LB bleef gezond, maar zou down worden; alles hersteld na 6 s") {
		t.Fatalf("korte onderbreking: %s %q %+v", result, summary, rows[0])
	}

	// Erger dan voorspeld: FAIL.
	def2 := def
	def2.Clusters = append([]ClusterPlan(nil), def.Clusters...)
	def2.Clusters[1].Predicted = Prediction{Status: "healthy"}
	result, summary, _, _ = verdict(def2, outcome{injected: true, m: measure(60*time.Second, "down"), rec: rec}, short)
	if result != "fail" || !strings.HasPrefix(summary, "FAIL: App ging onverwacht down (php: nginx in LB is down)") {
		t.Fatalf("onverwacht: %s %q", result, summary)
	}

	// Een cluster dat niet herstelde: FAIL; een traag herstel: WARNING.
	slow := &recoveryState{Clusters: []ClusterRecovery{{ClusterID: lb, RecoveredMS: ms(200 * time.Second)}, {ClusterID: app, Waiting: "status verminderd"}}}
	result, summary, _, rows = verdict(def, outcome{injected: true, m: measure(60*time.Second, "degraded"), rec: slow}, short)
	if result != "fail" || !strings.Contains(summary, "App herstelde niet") || !rows[0].Slow || rows[1].Match != MatchNotRecovered ||
		!strings.HasSuffix(summary, "alles weer gestart, maar niet alles herstelde binnen 2 min 30 s") {
		t.Fatalf("niet hersteld: %s %q %+v", result, summary, rows)
	}
	slow.Clusters[1] = ClusterRecovery{ClusterID: app, RecoveredMS: ms(time.Second)}
	result, summary, _, _ = verdict(def, outcome{injected: true, m: measure(60*time.Second, "degraded"), rec: slow}, short)
	if result != "warning" || !strings.Contains(summary, "LB herstelde pas na 3 min 20 s, verwacht binnen 2 min") {
		t.Fatalf("traag: %s %q", result, summary)
	}

	// Herstel mislukt: error en niet hersteld.
	result, summary, restored, _ = verdict(def, outcome{injected: true, m: measure(60*time.Second, "degraded"), restoreErr: errors.New("web01: de agent meldt: exit status 1")}, short)
	if result != "error" || restored == nil || *restored ||
		summary != "ERROR: herstel mislukt: web01: de agent meldt: exit status 1. nginx staat mogelijk nog uit op web01 en web02; kies Opnieuw herstellen" {
		t.Fatalf("herstel mislukt: %s %q", result, summary)
	}

	// Voor de storing: niets veranderd.
	result, summary, restored, _ = verdict(def, outcome{precheckErr: &failover.PrecheckError{}}, short)
	if result != "error" || restored != nil || !strings.HasSuffix(summary, "; er is niets veranderd") {
		t.Fatalf("niet gestart: %s %q", result, summary)
	}
	result, _, restored, _ = verdict(def, outcome{canceled: true}, short)
	if result != "canceled" || restored != nil {
		t.Fatalf("geannuleerd voor de storing: %s", result)
	}
	result, summary, _, _ = verdict(def, outcome{injected: true, m: &Measurement{Canceled: true}, rec: rec}, short)
	if result != "canceled" || summary != "Afgebroken tijdens de meting; alles hersteld na 6 s" {
		t.Fatalf("geannuleerd tijdens de meting: %s %q", result, summary)
	}
}

func TestValidate(t *testing.T) {
	conn := uuid.New()
	o := Options{
		Allowed: true, Units: []string{"nginx"},
		Hosts: []HostOption{{ConnectionID: conn, Connection: "Thuislab", Host: "pve1"}},
	}
	good := func(change func(in *Input)) Input {
		in := Input{Name: " nginx weg ", Kind: ServiceDown, Unit: "nginx", FaultSeconds: 60, RecoverySeconds: 120}
		if change != nil {
			change(&in)
		}
		return in
	}
	in := good(nil)
	if err := in.validate(o); err != nil || in.Name != "nginx weg" {
		t.Fatalf("geldig: %v %q", err, in.Name)
	}
	for field, change := range map[string]func(in *Input){
		"name":             func(in *Input) { in.Name = " " },
		"kind":             func(in *Input) { in.Kind = "reboot" },
		"unit":             func(in *Input) { in.Unit = "mariadb" },
		"fault_seconds":    func(in *Input) { in.FaultSeconds = MinFaultSeconds - 1 },
		"recovery_seconds": func(in *Input) { in.RecoverySeconds = MaxRecoverySeconds + 1 },
		"host":             func(in *Input) { in.Kind, in.ConnectionID, in.Host, in.FaultSeconds = HostDown, &conn, "pve9", 120 },
		// Na een harde stop is een node pas na 90 s down.
		"fault_seconds ": func(in *Input) { in.Kind, in.ConnectionID, in.Host, in.FaultSeconds = HostDown, &conn, "pve1", 60 },
	} {
		in := good(change)
		var fe *failover.FieldError
		if err := in.validate(o); !errors.As(err, &fe) || fe.Field != strings.TrimSpace(field) {
			t.Errorf("%s: %v", field, err)
		}
	}
	// Een andere unit dan de toegestane gaat nooit naar de agent.
	in = good(func(in *Input) { in.Unit = "sshd" })
	if err := in.validate(Options{Allowed: true, Units: []string{"sshd"}}); err == nil {
		t.Fatal("sshd toegestaan")
	}
	in = good(func(in *Input) { in.Kind, in.ConnectionID, in.Host, in.FaultSeconds = HostDown, &conn, " pve1 ", 120 })
	if err := in.validate(o); err != nil || in.Unit != "" || in.Host != "pve1" {
		t.Fatalf("host_down: %v %+v", err, in)
	}
	in = good(func(in *Input) { in.Kind = VIPLoss })
	if err := in.validate(o); err != nil || in.Unit != "" {
		t.Fatalf("vip_loss: %v %+v", err, in)
	}
	prod := Options{Allowed: false, Reason: msgProd}
	in = good(nil)
	var fe *failover.FieldError
	if err := in.validate(prod); !errors.As(err, &fe) || fe.Field != "kind" || fe.Message != msgProd {
		t.Fatalf("prod: %v", err)
	}
}

func TestList(t *testing.T) {
	for want, items := range map[string][]string{"": nil, "a": {"a"}, "a en b": {"a", "b"}, "a, b en c": {"a", "b", "c"}} {
		if got := list(items); got != want {
			t.Errorf("%v: %q", items, got)
		}
	}
}
