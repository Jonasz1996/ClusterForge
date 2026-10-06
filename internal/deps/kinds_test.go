package deps

import (
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// De soorten van de diensten in de ingebouwde templates staan in Kinds;
// templates zelf kan deps niet importeren.
func TestTemplateKinds(t *testing.T) {
	for _, tpl := range templates.Builtin() {
		for _, v := range templates.BuiltinRegistry().Versions(tpl.Name) {
			tv, _ := templates.Get(tpl.Name, v)
			for _, s := range tv.Services {
				if !KnownKind(s.Kind) {
					t.Errorf("%s %s: dienst %s heeft onbekende soort %q", tpl.Name, v, s.Name, s.Kind)
				}
				if !nameRe.MatchString(s.Name) {
					t.Errorf("%s %s: ongeldige naam %q", tpl.Name, v, s.Name)
				}
			}
		}
	}
}

func TestUnitKind(t *testing.T) {
	for _, tc := range []struct {
		unit, clusterType, kind string
		ok                      bool
	}{
		{"nginx", "keepalived", "web", true},
		{"apache2", "generic", "web", true},
		{"haproxy", "keepalived", "lb", true},
		{"postgresql@16-main", "generic", "database", true},
		{"postgresql@16-replica", "generic", "", false},
		{"php8.2-fpm", "generic", "app", true},
		{"cron", "cron", "cron", true},
		{"cron", "keepalived", "cron", false},
		{"ssh", "generic", "", false},
		{"cf-agent", "generic", "", false},
		{"pve-cluster", "generic", "", false},
		{"containerd", "docker", "", false},
	} {
		kind, ok := unitKind(tc.unit, tc.clusterType)
		if ok != tc.ok || (ok && kind != tc.kind) {
			t.Errorf("%s in %s: %q %v", tc.unit, tc.clusterType, kind, ok)
		}
	}
}
