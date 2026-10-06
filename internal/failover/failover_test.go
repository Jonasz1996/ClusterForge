package failover

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

func TestProbeValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		p  Probe
		ok bool
	}{
		"standaard":     {DefaultProbe, true},
		"pad met punt":  {Probe{HTTP: &HTTPProbe{Path: "/health.txt", Expect: 204}}, true},
		"tcp":           {Probe{TCP: &TCPProbe{Port: 443}}, true},
		"niets":         {Probe{}, false},
		"beide":         {Probe{HTTP: &HTTPProbe{Path: "/", Expect: 200}, TCP: &TCPProbe{Port: 80}}, false},
		"poort 0":       {Probe{TCP: &TCPProbe{Port: 0}}, false},
		"status 99":     {Probe{HTTP: &HTTPProbe{Path: "/", Expect: 99}}, false},
		"volledige url": {Probe{HTTP: &HTTPProbe{Path: "http://elders/", Expect: 200}}, false},
		"query":         {Probe{HTTP: &HTTPProbe{Path: "/?x=1", Expect: 200}}, false},
		"spatie":        {Probe{HTTP: &HTTPProbe{Path: "/a b", Expect: 200}}, false},
		"backslash":     {Probe{HTTP: &HTTPProbe{Path: `/\elders`, Expect: 200}}, false},
	} {
		err := tc.p.Validate()
		var fe *FieldError
		if tc.ok != (err == nil) || err != nil && (!errors.As(err, &fe) || fe.Field != "probe") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestInputValidate(t *testing.T) {
	ok := func() Input {
		return Input{Name: " web ", Scenario: KeepalivedStop, Service: "nginx", MaxTakeoverSeconds: 5, Probe: DefaultProbe}
	}
	in := ok()
	if err := in.validate(store.EnvironmentLab); err != nil || in.Name != "web" || in.Service != "" {
		t.Fatalf("geldig: %v %+v", err, in)
	}
	vm := Input{Name: "vm", Scenario: VMHardStop, Service: "nginx", MaxTakeoverSeconds: 10, Probe: DefaultProbe, Scheduled: true}
	if err := vm.validate(store.EnvironmentTest); err != nil || vm.Service != "" {
		t.Fatalf("vm_hard_stop gepland op test: %v %+v", err, vm)
	}
	for name, tc := range map[string]struct {
		change func(*Input)
		env    store.Environment
		field  string
	}{
		"naam":                {func(in *Input) { in.Name = strings.Repeat("x", 201) }, store.EnvironmentLab, "name"},
		"scenario":            {func(in *Input) { in.Scenario = "reboot" }, store.EnvironmentLab, "scenario"},
		"dienst":              {func(in *Input) { in.Scenario, in.Service = ServiceStop, "sshd" }, store.EnvironmentLab, "service"},
		"verwachting":         {func(in *Input) { in.MaxTakeoverSeconds = 0 }, store.EnvironmentLab, "max_takeover_seconds"},
		"probe":               {func(in *Input) { in.Probe = Probe{} }, store.EnvironmentLab, "probe"},
		"vm hard uit op prod": {func(in *Input) { in.Scenario = VMHardStop }, store.EnvironmentProd, "scenario"},
		"gepland op prod":     {func(in *Input) { in.Scheduled = true }, store.EnvironmentProd, "scheduled"},
	} {
		in := ok()
		tc.change(&in)
		var fe *FieldError
		if err := in.validate(tc.env); !errors.As(err, &fe) || fe.Field != tc.field {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Met de hand op prod mag wel.
	prod := ok()
	if err := prod.validate(store.EnvironmentProd); err != nil {
		t.Fatalf("met de hand op prod: %v", err)
	}
}

func TestNetProber(t *testing.T) {
	var host string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host = r.Host
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
		case "/weg":
			http.Redirect(w, r, "/", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	var dialed []string
	n := NetProber{Timeout: time.Second, dial: func(ctx context.Context, network, a string) (net.Conn, error) {
		dialed = append(dialed, a)
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	ctx := context.Background()
	if err := n.Probe(ctx, "10.0.20.10", DefaultProbe); err != nil || host != "10.0.20.10" || dialed[0] != "10.0.20.10:80" {
		t.Fatalf("200: %v, host %q, gebeld %v", err, host, dialed)
	}
	if err := n.Probe(ctx, "10.0.20.10", Probe{HTTP: &HTTPProbe{Path: "/health", Expect: 200}}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: %v", err)
	}
	// Een redirect volgt de probe niet: het VIP zelf moet antwoorden.
	if err := n.Probe(ctx, "10.0.20.10", Probe{HTTP: &HTTPProbe{Path: "/weg", Expect: 200}}); err == nil || !strings.Contains(err.Error(), "302") {
		t.Errorf("redirect: %v", err)
	}
	if err := n.Probe(ctx, "fd00::10", DefaultProbe); err != nil || host != "[fd00::10]" || dialed[len(dialed)-1] != "[fd00::10]:80" {
		t.Errorf("IPv6: %v, host %q, gebeld %v", err, host, dialed)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	direct := NetProber{Timeout: time.Second}
	if err := direct.Probe(ctx, "127.0.0.1", Probe{TCP: &TCPProbe{Port: port}}); err != nil {
		t.Errorf("tcp open: %v", err)
	}
	_ = l.Close()
	if err := direct.Probe(ctx, "127.0.0.1", Probe{TCP: &TCPProbe{Port: port}}); err == nil {
		t.Error("tcp dicht: geen fout")
	}
}

func TestProbeLog(t *testing.T) {
	start := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	at := func(ms int) time.Time { return start.Add(time.Duration(ms) * time.Millisecond) }
	fail := errors.New("connection refused")
	pl := newProbeLog(start, "10.0.20.10")
	for _, s := range []struct {
		ms int
		ok bool
	}{{0, true}, {250, false}, {500, false}, {750, true}, {1000, false}, {1250, true}, {1500, true}, {1750, true}} {
		var err error
		if !s.ok {
			err = fail
		}
		pl.add(at(s.ms), err)
		if s.ms == 1500 && pl.recovered() {
			t.Fatal("hersteld na twee goede")
		}
	}
	if !pl.recovered() {
		t.Fatal("niet hersteld na drie goede")
	}
	// Van de eerste fout (250) tot de eerste van de drie goede (1250).
	if d := pl.downtime(at(2000)); d != time.Second {
		t.Fatalf("onderbreking %s", d)
	}
	segs, events := pl.finish(at(2000))
	want := []Segment{{0, 250, true}, {250, 750, false}, {750, 1000, true}, {1000, 1250, false}, {1250, 2000, true}}
	if len(segs) != len(want) {
		t.Fatalf("stukken %+v", segs)
	}
	for i := range want {
		if segs[i] != want[i] {
			t.Fatalf("stuk %d: %+v, wilde %+v", i, segs[i], want[i])
		}
	}
	if len(events) != 2 || events[0].Kind != "down" || events[0].TMS != 250 || events[1].Kind != "up" || events[1].TMS != 1250 ||
		events[0].Text != "10.0.20.10 onbereikbaar: connection refused" {
		t.Fatalf("tijdlijn %+v", events)
	}

	// Komt het VIP niet terug, dan loopt de onderbreking tot het einde.
	pl = newProbeLog(start, "10.0.20.10")
	pl.add(at(100), fail)
	if d := pl.downtime(at(5000)); d != 4900*time.Millisecond {
		t.Fatalf("zonder herstel: %s", d)
	}
	// Zonder fout is er geen onderbreking.
	pl = newProbeLog(start, "10.0.20.10")
	pl.add(at(0), nil)
	if pl.downtime(at(100)) != 0 {
		t.Fatal("onderbreking zonder fout")
	}
}

func TestVerdict(t *testing.T) {
	def := Definition{Name: "keepalived", Unit: "keepalived", VIP: "10.0.20.10", MaxTakeoverSeconds: 5, ExpectFailback: true}
	plan := Plan{Target: health.Holder{ID: uuid.New(), Hostname: "web-01"}}
	ret := &returnState{Owner: "web-01"}
	m := func(change func(*Measurement)) *Measurement {
		out := &Measurement{DowntimeMS: 3400, Recovered: true, TakenOver: true, TakeoverNode: "web-02", WindowMS: 20000}
		if change != nil {
			change(out)
		}
		return out
	}
	for name, tc := range map[string]struct {
		o        outcome
		result   string
		summary  string
		restored *bool
	}{
		"pass": {outcome{injected: true, m: m(nil), ret: ret}, "pass",
			"PASS: 3,4 s onbereikbaar, overgenomen door web-02, daarna terug op web-01, alles hersteld", ptr(true)},
		"geen onderbreking": {outcome{injected: true, m: m(func(m *Measurement) { m.DowntimeMS = 0 }), ret: ret}, "pass",
			"PASS: geen onderbreking gemeten, overgenomen door web-02, daarna terug op web-01, alles hersteld", ptr(true)},
		"te traag": {outcome{injected: true, m: m(func(m *Measurement) { m.DowntimeMS = 12800 }), ret: ret}, "fail",
			"FAIL: 12,8 s onbereikbaar, meer dan de verwachte 5 s; overgenomen door web-02, daarna terug op web-01, alles hersteld", ptr(true)},
		"geen overname": {outcome{injected: true, m: m(func(m *Measurement) { m.TakenOver, m.Recovered = false, false }), ret: ret}, "fail",
			"FAIL: geen andere node nam 10.0.20.10 over binnen 20 s; daarna terug op web-01, alles hersteld", ptr(true)},
		"bleef onbereikbaar": {outcome{injected: true, m: m(func(m *Measurement) { m.Recovered = false }), ret: ret}, "fail",
			"FAIL: 10.0.20.10 bleef onbereikbaar tot het einde van de meting (20 s); daarna terug op web-01, alles hersteld", ptr(true)},
		"herstel mislukt": {outcome{injected: true, m: m(nil), restoreErr: errors.New("de agent meldt: exit status 1")}, "error",
			"ERROR: herstel mislukt: de agent meldt: exit status 1. keepalived staat mogelijk nog uit op web-01; kies Opnieuw herstellen", ptr(false)},
		"onderbroken": {outcome{injected: true, interrupted: true, ret: ret}, "error",
			"ERROR: meting onderbroken door een herstart van ClusterForge; daarna terug op web-01, alles hersteld", ptr(true)},
		"geannuleerd": {outcome{injected: true, canceled: true, m: m(func(m *Measurement) { m.Canceled = true }), ret: ret}, "canceled",
			"Afgebroken tijdens de meting; daarna terug op web-01, alles hersteld", ptr(true)},
		"voor de storing geannuleerd": {outcome{canceled: true}, "canceled", "Afgebroken voor de storing; er is niets veranderd", nil},
		"overgeslagen": {outcome{skipped: "het cluster is niet gezond maar verminderd"}, "skipped",
			"Overgeslagen: het cluster is niet gezond maar verminderd", nil},
		"storing mislukt": {outcome{injected: true, injectErr: "agent offline", ret: ret}, "error",
			"ERROR: storing niet gelukt: agent offline; daarna terug op web-01, alles hersteld", ptr(true)},
	} {
		result, summary, restored := verdict(def, plan, tc.o)
		if result != tc.result || summary != tc.summary || (restored == nil) != (tc.restored == nil) || restored != nil && *restored != *tc.restored {
			t.Errorf("%s:\n kreeg  %s %q %v\n wilde  %s %q %v", name, result, summary, restored, tc.result, tc.summary, tc.restored)
		}
	}
	// Na een harde stop gaat het om de VM, niet om keepalived.
	vm := def
	vm.Scenario = VMHardStop
	_, summary, _ := verdict(vm, plan, outcome{injected: true, m: m(nil), restoreErr: errors.New("VM 101 start niet")})
	if want := "ERROR: herstel mislukt: VM 101 start niet. De VM van web-01 staat mogelijk nog uit; kies Opnieuw herstellen"; summary != want {
		t.Errorf("VM niet hersteld: %q", summary)
	}
}

func TestTemplateDefaults(t *testing.T) {
	s := &Service{Templates: templates.BuiltinRegistry()}
	name := "keepalived-nginx"
	c := store.Cluster{
		Name: "Web", Slug: "web", Environment: store.EnvironmentLab, TemplateName: &name,
		Spec: []byte(`{"template":{"name":"keepalived-nginx","version":"1.0.0"},"params":{"vip":"10.0.20.10","vrid":30}}`),
	}
	tpl := s.templateOf(c)
	if !tpl.failback {
		t.Error("keepalived-nginx zonder failback")
	}
	if p := tpl.probe("10.0.20.10"); p.HTTP == nil || p.HTTP.Path != "/" || p.HTTP.Expect != 200 {
		t.Errorf("probe uit de template: %+v", p)
	}
	if p := tpl.probe("10.0.20.11"); p.String() != DefaultProbe.String() {
		t.Errorf("ander VIP: %+v", p)
	}
	manual := s.templateOf(store.Cluster{Spec: []byte(`{"baseline":{}}`)})
	if manual.failback || manual.probe("10.0.20.10").String() != DefaultProbe.String() {
		t.Errorf("zonder template: %+v", manual)
	}
}

func TestDescribeAndSeconds(t *testing.T) {
	if Describe(ServiceStop, "haproxy") != "haproxy stoppen op de eigenaar" || Describe(KeepalivedStop, "") != "keepalived stoppen op de eigenaar" {
		t.Error("Describe")
	}
	for d, want := range map[time.Duration]string{
		3400 * time.Millisecond: "3,4 s", 12800 * time.Millisecond: "12,8 s", 20 * time.Second: "20 s", 3 * time.Minute: "3 min",
		0: "0 s", 90500 * time.Millisecond: "91 s", 3449 * time.Millisecond: "3,4 s",
	} {
		if got := seconds(d); got != want {
			t.Errorf("seconds(%s) = %q, wilde %q", d, got, want)
		}
	}
}
