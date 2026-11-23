package publish

import (
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestOtherApp(t *testing.T) {
	ok := func(st int, title string) *protocol.WebCheckResult {
		return &protocol.WebCheckResult{Status: st, Title: title}
	}
	for _, c := range []struct {
		name      string
		before    *protocol.WebCheckResult
		after     protocol.WebCheckResult
		protected bool
		other     bool
	}{
		{"zelfde titel", ok(200, "AdGuard Home"), protocol.WebCheckResult{Status: 200, Title: "AdGuard  Home"}, false, false},
		{"andere app", ok(200, "AdGuard Home"), protocol.WebCheckResult{Status: 200, Title: "Web Check"}, false, true},
		{"vooraf kapot", ok(502, "502 Bad Gateway"), protocol.WebCheckResult{Status: 200, Title: "Web Check"}, false, false},
		{"vooraf geen antwoord", &protocol.WebCheckResult{Error: "refused"}, protocol.WebCheckResult{Status: 200, Title: "x"}, false, false},
		{"nu 404", ok(200, "Grafana"), protocol.WebCheckResult{Status: 404}, false, true},
		{"achter Authentik", ok(200, "Grafana"), protocol.WebCheckResult{Status: 302}, true, false},
		{"401 beschermd", ok(200, "Grafana"), protocol.WebCheckResult{Status: 401}, true, false},
		{"titel deels gelijk", ok(200, "Grafana"), protocol.WebCheckResult{Status: 200, Title: "Login - Grafana"}, false, false},
		{"oude agent zonder titel", ok(200, ""), protocol.WebCheckResult{Status: 200, Title: "Web Check"}, false, false},
		{"niets vooraf", nil, protocol.WebCheckResult{Status: 200, Title: "x"}, false, false},
	} {
		if got := otherApp(c.before, c.after, c.protected) != ""; got != c.other {
			t.Errorf("%s: andere app = %v, wil %v", c.name, got, c.other)
		}
	}
}

func TestSuggestOnlyExactNameIsSure(t *testing.T) {
	svcs := []ServiceRow{
		{Key: "a", Name: "Web Check", Proto: "tcp", Web: true, Target: "10.0.0.5", Port: 3000, Kind: "http", Container: "web-check"},
		{Key: "b", Name: "AdGuard Home", Proto: "tcp", Web: true, Target: "10.0.0.6", Port: 3000, Kind: "http", Container: "adguardhome"},
	}
	// Alleen dezelfde poort: een gok.
	if sg := suggestTarget("speedtest.jbogaert.be", 3000, svcs); sg == nil || sg.Sure {
		t.Fatalf("zelfde poort moet een gok zijn: %+v", sg)
	}
	// Naam lijkt erop (adguard ⊂ adguardhome): ook een gok.
	if sg := suggestTarget("adguard.jbogaert.be", 80, svcs); sg == nil || sg.Sure {
		t.Fatalf("naam die erop lijkt moet een gok zijn: %+v", sg)
	}
	// Precies dezelfde naam: zeker.
	if sg := suggestTarget("adguardhome.jbogaert.be", 80, svcs); sg == nil || !sg.Sure || sg.ServiceKey != "b" {
		t.Fatalf("precies dezelfde naam moet zeker zijn: %+v", sg)
	}
}

func TestSameTarget(t *testing.T) {
	hosts := []NPMHost{
		{ID: 1, DomainNames: []string{"adguard.jbogaert.be"}, ForwardHost: "10.0.0.5", ForwardPort: 3000},
		{ID: 2, DomainNames: []string{"webcheck.jbogaert.be"}, ForwardHost: "10.0.0.5", ForwardPort: 3000},
		{ID: 3, DomainNames: []string{"other.jbogaert.be"}, ForwardHost: "10.0.0.5", ForwardPort: 80},
	}
	if got := sameTarget(hosts, 1, "10.0.0.5", 3000); len(got) != 1 || got[0] != "webcheck.jbogaert.be" {
		t.Fatalf("sameTarget = %v", got)
	}
	var p Plan
	warnSameTarget(&p, hosts, PlannedHost{HostID: 3, Domain: "other.jbogaert.be", Body: map[string]any{"forward_port": 3000}})
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "adguard.jbogaert.be, webcheck.jbogaert.be") {
		t.Fatalf("waarschuwing = %v", p.Warnings)
	}
}
