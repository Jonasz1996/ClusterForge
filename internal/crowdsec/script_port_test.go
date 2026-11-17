package crowdsec

import (
	"strings"
	"testing"
)

// De lokale API mag niet blind op 8080 blijven: daar luistert vaak de
// ClusterForge-server of een container (storing 2026-11-17).
func TestScriptMovesLAPIOffTakenPort(t *testing.T) {
	s := Script(Plan{Hash: "h", Bouncer: true, Files: map[string]string{bouncerLocal: "x"}})
	for _, want := range []string{"== Poort van de lokale API", "HostPort", "seq 8081 8099", "BOUNCER_PORT_CHANGED", "INSTALL_RETRY=1", "dpkg --configure -a"} {
		if !strings.Contains(s, want) {
			t.Errorf("script mist %q", want)
		}
	}
	if strings.Index(s, "Poort van de lokale API") > strings.Index(s, "Configuratie testen") {
		t.Error("de poort moet vóór de configuratietest en de herstart goed staan")
	}
}
