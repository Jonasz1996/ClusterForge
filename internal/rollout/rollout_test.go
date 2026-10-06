package rollout

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func yes() *bool { b := true; return &b }

// steps zijn de stappen van keepalived-nginx in hun volgorde.
func steps() []templates.Step {
	return []templates.Step{
		{Step: protocol.Step{Package: &protocol.PackageStep{Names: []string{"keepalived", "nginx"}}}, Title: "Pakketten keepalived, nginx"},
		{Step: protocol.Step{File: &protocol.FileStep{Path: "/var/www/html/index.html", Content: "hallo"}}, Title: "Bestand /var/www/html/index.html"},
		{Step: protocol.Step{Service: &protocol.ServiceStep{Name: "nginx", Enabled: yes(), State: "started"}}, Title: "Service nginx"},
		{
			Step:   protocol.Step{File: &protocol.FileStep{Path: "/etc/keepalived/keepalived.conf", Content: "vrrp", Mode: "0640"}},
			Title:  "Bestand /etc/keepalived/keepalived.conf",
			Notify: []protocol.ServiceStep{{Name: "keepalived", State: "reloaded"}},
		},
		{Step: protocol.Step{Service: &protocol.ServiceStep{Name: "keepalived", Enabled: yes(), State: "started"}}, Title: "Service keepalived"},
	}
}

func TestAction(t *testing.T) {
	s := steps()
	for _, c := range []struct {
		step templates.Step
		id   string
		want string
	}{
		{s[3], "file:/etc/keepalived/keepalived.conf", "bestand /etc/keepalived/keepalived.conf overschrijven, daarna keepalived herladen"},
		{s[2], "service:nginx", "service nginx enablen en starten"},
		{s[0], "package:nginx", "pakket nginx installeren"},
		{templates.Step{Step: protocol.Step{Package: &protocol.PackageStep{Names: []string{"apache2"}, State: "absent"}}}, "package:apache2", "pakket apache2 verwijderen"},
		{templates.Step{Step: protocol.Step{Service: &protocol.ServiceStep{Name: "x", State: "restarted"}}}, "service:x", "service x herstarten"},
		{templates.Step{Step: protocol.Step{Directory: &protocol.DirectoryStep{Path: "/srv"}}}, "directory:/srv", "map /srv aanmaken en rechten zetten"},
		{templates.Step{Step: protocol.Step{User: &protocol.UserStep{Name: "app"}}}, "user:app", "gebruiker app aanmaken"},
		{templates.Step{Step: protocol.Step{Command: &protocol.CommandStep{Run: "x", Creates: "/etc/x"}}}, "command:/etc/x", "het commando opnieuw uitvoeren dat /etc/x maakt"},
	} {
		if got := Action(c.step, c.id); got != c.want {
			t.Errorf("Action(%s) = %q, wil %q", c.id, got, c.want)
		}
	}
}

func TestOrderOwnerLast(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	nodes := map[uuid.UUID]PlanNode{
		a: {NodeID: a, Hostname: "web-01", VIPs: []string{"10.0.20.100"}},
		b: {NodeID: b, Hostname: "web-02"},
		c: {NodeID: c, Hostname: "web-03"},
	}
	var got []string
	for _, n := range Order([]uuid.UUID{a, b, c}, nodes) {
		got = append(got, n.Hostname)
	}
	if want := []string{"web-02", "web-03", "web-01"}; !slices.Equal(got, want) {
		t.Fatalf("volgorde %v, wil %v", got, want)
	}
}

func TestSelect(t *testing.T) {
	got := Select(steps(), []string{"file:/etc/keepalived/keepalived.conf", "package:nginx"})
	if len(got) != 2 {
		t.Fatalf("%d stappen, wil 2", len(got))
	}
	if p := got[0].Package; p == nil || !slices.Equal(p.Names, []string{"nginx"}) {
		t.Fatalf("eerste stap %+v, wil alleen het pakket nginx", got[0])
	}
	if f := got[1].File; f == nil || f.Path != "/etc/keepalived/keepalived.conf" || len(got[1].Notify) != 1 {
		t.Fatalf("tweede stap %+v, wil keepalived.conf met zijn notify-handler", got[1])
	}
	// Het origineel blijft heel.
	if s := steps(); len(s[0].Package.Names) != 2 {
		t.Fatal("Select veranderde de stappen")
	}
}

func TestPlanNode(t *testing.T) {
	conf := drift.Finding{Key: "file:/etc/keepalived/keepalived.conf:content", Step: "file:/etc/keepalived/keepalived.conf", Fingerprint: "fp1"}
	mode := drift.Finding{Key: "file:/etc/keepalived/keepalived.conf:mode", Step: "file:/etc/keepalived/keepalived.conf", Fingerprint: "fp2"}
	index := drift.Finding{Key: "file:/var/www/html/index.html:content", Step: "file:/var/www/html/index.html", Fingerprint: "fp3", Ignored: true}
	nginx := drift.Finding{Key: "service:nginx:enabled", Step: "service:nginx", Fingerprint: "fp4"}
	findings := []drift.Finding{nginx, conf, mode, index}

	pn, err := planNode("web-02", steps(), findings, []StepChoice{
		{Step: "file:/etc/keepalived/keepalived.conf", Fingerprints: []string{"fp2", "fp1"}},
		{Step: "service:nginx", Fingerprints: []string{"fp4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, st := range pn.Steps {
		ids = append(ids, st.Step)
	}
	// In de volgorde van de template, niet van de keuze.
	if want := []string{"service:nginx", "file:/etc/keepalived/keepalived.conf"}; !slices.Equal(ids, want) {
		t.Fatalf("stappen %v, wil %v", ids, want)
	}
	if !slices.Equal(pn.Ignored, []string{"file:/var/www/html/index.html"}) || !slices.Equal(pn.Services, []string{"keepalived"}) {
		t.Fatalf("genegeerd %v, services %v", pn.Ignored, pn.Services)
	}

	for _, c := range []struct {
		name   string
		choice StepChoice
		code   string
	}{
		{"andere vingerafdruk", StepChoice{Step: "file:/etc/keepalived/keepalived.conf", Fingerprints: []string{"fp1"}}, "drift_changed"},
		{"genegeerd", StepChoice{Step: "file:/var/www/html/index.html", Fingerprints: []string{"fp3"}}, "ignored"},
		{"geen afwijking", StepChoice{Step: "service:keepalived", Fingerprints: []string{"x"}}, "drift_changed"},
	} {
		_, err := planNode("web-02", steps(), findings, []StepChoice{c.choice})
		var ce *ConflictError
		if !errors.As(err, &ce) || ce.Code != c.code {
			t.Errorf("%s: %v, wil %s", c.name, err, c.code)
		}
	}
	if _, err := planNode("web-02", steps(), findings, nil); err == nil {
		t.Error("een lege keuze is geen fout")
	}
}

func TestChanged(t *testing.T) {
	planned := []PlanStep{
		{Step: "file:/etc/keepalived/keepalived.conf", Fingerprints: []string{"fp1"}},
		{Step: "service:nginx", Fingerprints: []string{"fp4"}},
	}
	ids := []string{"file:/etc/keepalived/keepalived.conf", "service:nginx"}
	same := []drift.Finding{{Step: "service:nginx", Fingerprint: "fp4"}, {Step: "file:/etc/keepalived/keepalived.conf", Fingerprint: "fp1"}}
	if got := Changed(planned, ids, same); len(got) != 0 {
		t.Fatalf("niets veranderd, maar %v", got)
	}
	// Een tweede wijziging aan hetzelfde bestand geeft een andere vingerafdruk.
	again := []drift.Finding{{Step: "service:nginx", Fingerprint: "fp4"}, {Step: "file:/etc/keepalived/keepalived.conf", Fingerprint: "fp9"}}
	if got := Changed(planned, ids, again); !slices.Equal(got, []string{"file:/etc/keepalived/keepalived.conf"}) {
		t.Fatalf("veranderd %v", got)
	}
	// Met de hand teruggezet telt ook als veranderd.
	if got := Changed(planned, ids, same[:1]); !slices.Equal(got, []string{"file:/etc/keepalived/keepalived.conf"}) {
		t.Fatalf("verdwenen afwijking: %v", got)
	}
	// Een stap die nu genegeerd wordt, valt buiten de vergelijking.
	if got := Changed(planned, ids[1:], same[:1]); len(got) != 0 {
		t.Fatalf("genegeerde stap vergeleken: %v", got)
	}
}

func TestNotes(t *testing.T) {
	p := Plan{Nodes: []PlanNode{
		{Hostname: "web-02", Ignored: []string{"file:/var/www/html/index.html"}, Services: []string{"keepalived"}},
		{Hostname: "web-01", VIPs: []string{"10.0.20.100"}, Services: []string{"keepalived"}},
	}}
	got := notes(p)
	if len(got) != 3 {
		t.Fatalf("%d zinnen: %q", len(got), got)
	}
}
