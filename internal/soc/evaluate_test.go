package soc

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func protected(host string) CoverageRow {
	return CoverageRow{NodeID: uuid.New(), Hostname: host, Online: true, Firewall: StateOn, CrowdSec: StateOn, Bouncer: StateOn, Gaps: []string{}}
}

func TestEvaluateNormal(t *testing.T) {
	r := evaluate(Inputs{Coverage: []CoverageRow{protected("a"), protected("b")}})
	if r.Level.Level != LevelNormal || len(r.Level.Reasons) != 1 {
		t.Fatalf("niveau = %+v", r.Level)
	}
	if r.Score.Score != 100 {
		t.Fatalf("score = %d, delen %+v", r.Score.Score, r.Score.Parts)
	}
	if len(r.Now) != 0 {
		t.Fatalf("nu belangrijk = %+v", r.Now)
	}
}

func TestEvaluateNoNodes(t *testing.T) {
	r := evaluate(Inputs{})
	if r.Level.Level != LevelNormal || r.Score.Score != 100 {
		t.Fatalf("zonder nodes: %+v %d", r.Level, r.Score.Score)
	}
}

func TestEvaluateBouncerDownOnAttackedNodeIsCritical(t *testing.T) {
	hit, quiet := protected("web1"), protected("web2")
	hit.Bouncer, quiet.Bouncer = StateDown, StateDown
	r := evaluate(Inputs{Coverage: []CoverageRow{hit, quiet}, AttackedNodes: map[uuid.UUID]int{hit.NodeID: 4}})
	if r.Level.Level != LevelCritical {
		t.Fatalf("niveau = %+v", r.Level)
	}
	if !strings.Contains(r.Level.Reasons[0], "web1") || strings.Contains(r.Level.Reasons[0], "web2") {
		t.Fatalf("reden = %q", r.Level.Reasons[0])
	}
	if r.Now[0].Key != "bouncer-hit" || r.Now[1].Key != "bouncer-down" {
		t.Fatalf("volgorde = %s, %s", r.Now[0].Key, r.Now[1].Key)
	}
	// Twee nodes zonder werkende bouncer: 2 × 35/2 × 0,2 = 7 punten dekking,
	// plus 8 voor de dreiging.
	if r.Score.Score != 85 {
		t.Fatalf("score = %d, delen %+v", r.Score.Score, r.Score.Parts)
	}
}

func TestEvaluateLevels(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    Inputs
		level string
		key   string
	}{
		{"kritiek incident", Inputs{Incidents: []Incident{{ID: uuid.New(), Number: 3, Title: "NPM plat", Severity: "critical"}}}, LevelCritical, "incident-"},
		{"ernstig incident", Inputs{Incidents: []Incident{{ID: uuid.New(), Number: 4, Title: "x", Severity: "high"}}}, LevelElevated, "incident-"},
		{"brute force", Inputs{BruteForce: &Burst{IP: "203.0.113.9", Count: BruteForceLogins}}, LevelCritical, "bruteforce-203.0.113.9"},
		{"enkele mislukte login van buiten", Inputs{BruteForce: &Burst{IP: "203.0.113.9", Count: 2}, ExternalLoginFailures: 2}, LevelElevated, "logins-extern"},
		{"verspreid en niet geband", Inputs{Attackers: []Attacker{{IP: "198.51.100.7", Kind: KindExternal, Alerts: 6, Nodes: []string{"a", "b", "c"}}}}, LevelCritical, "spread-198.51.100.7"},
		{"verspreid maar geband", Inputs{Attackers: []Attacker{{IP: "198.51.100.7", Kind: KindExternal, Alerts: 6, Banned: true, Nodes: []string{"a", "b", "c"}}}}, LevelNormal, ""},
		{"intern op drie nodes is geen aanval", Inputs{Attackers: []Attacker{{IP: "192.168.10.5", Kind: KindInternal, Blocked: 900, Nodes: []string{"a", "b", "c"}}}}, LevelNormal, ""},
		{"piek", Inputs{Attacks24h: 90, AttacksBaseline: 10}, LevelElevated, "spike"},
		{"geen piek onder het minimum", Inputs{Attacks24h: SpikeMin - 1}, LevelNormal, ""},
		{"tweestap uit", Inputs{AdminsWithoutTOTP: []string{"jonas"}}, LevelWatch, "totp"},
		{"ssh voor iedereen", Inputs{SSHFromAny: true}, LevelWatch, "ssh-any"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := evaluate(tc.in)
			if r.Level.Level != tc.level {
				t.Fatalf("niveau = %+v", r.Level)
			}
			if tc.key == "" {
				return
			}
			if len(r.Now) == 0 || !strings.HasPrefix(r.Now[0].Key, tc.key) {
				t.Fatalf("nu belangrijk = %+v", r.Now)
			}
		})
	}
}

func TestEvaluateCoverageGaps(t *testing.T) {
	missing := protected("nc1")
	missing.Firewall, missing.CrowdSec, missing.Bouncer = StateMissing, StateMissing, ""
	off := protected("pve1")
	off.Firewall, off.CrowdSec, off.Bouncer = StateOff, StateOff, ""
	upd := protected("db1")
	upd.SecurityUpdates = 4
	r := evaluate(Inputs{Coverage: []CoverageRow{missing, off, upd, protected("db2")}})
	if r.Level.Level != LevelWatch {
		t.Fatalf("niveau = %+v", r.Level)
	}
	keys := map[string]bool{}
	for _, it := range r.Now {
		keys[it.Key] = true
	}
	// Bewust uit is geen punt in de lijst, nooit aangezet wel.
	for _, k := range []string{"gap-firewall", "gap-crowdsec", "security-updates"} {
		if !keys[k] {
			t.Errorf("punt %s ontbreekt: %+v", k, r.Now)
		}
	}
	for _, it := range r.Now {
		if strings.Contains(it.Title, "pve1") {
			t.Errorf("bewust uit staat in de lijst: %q", it.Title)
		}
	}
	// Maar in de score telt het wel: twee van de vier nodes zonder
	// firewall, CrowdSec en bouncer = 2 × 35/4 × 0,9.
	cov := r.Score.Parts[0]
	if cov.Points != 19.3 {
		t.Fatalf("dekking = %v, aftrek %+v", cov.Points, cov.Deductions)
	}
	var bewust bool
	for _, d := range cov.Deductions {
		if strings.Contains(d.Text, "bewust uit") {
			bewust = true
		}
	}
	if !bewust {
		t.Errorf("geen aftrekpost voor bewust uit: %+v", cov.Deductions)
	}
	if pat := r.Score.Parts[1]; pat.Points != 18.8 || !strings.Contains(pat.Deductions[0].Text, "4 pakketten") {
		t.Fatalf("patches = %+v", pat)
	}
}

func TestLevelReasonsAreTheHeaviest(t *testing.T) {
	l := level([]reason{
		{LevelWatch, 3, "gat"},
		{LevelCritical, 8.5, "verspreid"},
		{LevelCritical, 10, "incident"},
		{LevelCritical, 9, "brute force"},
	})
	if l.Level != LevelCritical || len(l.Reasons) != 3 || l.Reasons[0] != "incident" || l.Reasons[1] != "brute force" || l.Reasons[2] != "en 1 andere" {
		t.Fatalf("niveau = %+v", l)
	}
}

func TestNamesAndKinds(t *testing.T) {
	if got := names([]string{"e", "d", "c", "b", "a"}); got != "a, b, c, d en 1 andere" {
		t.Fatalf("names = %q", got)
	}
	n := nets{
		lan: []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10")},
		own: map[netip.Addr]string{netip.MustParseAddr("192.168.10.191"): "db1"},
	}
	for ip, want := range map[string]string{
		"192.168.10.191": KindNode, "192.168.90.20": KindInternal, "100.64.1.2": KindInternal,
		"203.0.113.4": KindExternal, "::ffff:192.168.10.191": KindNode, "2a02:1810::1": KindExternal, "1.2.3.0/24": KindExternal, "geen-ip": KindExternal,
	} {
		if got, _ := n.kind(ip); got != want {
			t.Errorf("kind(%s) = %s, wil %s", ip, got, want)
		}
	}
}

func TestEvaluateVulns(t *testing.T) {
	avg := 80
	r := evaluate(Inputs{Vulns: &Vulns{KEVNodes: []string{"nc1"}, CriticalNet: []string{"192.168.10.50"}, HighCVE: 2, HardeningAvg: &avg}})
	if r.Level.Level != LevelCritical {
		t.Errorf("niveau %+v", r.Level)
	}
	var vul *ScorePart
	for i := range r.Score.Parts {
		if r.Score.Parts[i].Key == "kwetsbaarheden" {
			vul = &r.Score.Parts[i]
		}
		if r.Score.Parts[i].Key == "patches" && r.Score.Parts[i].Max != 15 {
			t.Errorf("patches max %d", r.Score.Parts[i].Max)
		}
	}
	// 4 (netwerk) + 3 (KEV) + 1 (2 hoog) + 2 (verharding 80%) = 10
	if vul == nil || vul.Points != 0 || len(vul.Deductions) != 4 {
		t.Fatalf("deel kwetsbaarheden %+v", vul)
	}
	keys := map[string]bool{}
	for _, it := range r.Now {
		keys[it.Key] = true
	}
	if !keys["vuln-network"] || !keys["vuln-kev"] || !keys["vuln-high"] {
		t.Errorf("punten %v", keys)
	}
	if r := evaluate(Inputs{}); len(r.Score.Parts) != 4 {
		t.Errorf("zonder module: %d delen", len(r.Score.Parts))
	}
}
