package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// whatIfDBView is de uitkomst van de wat-als met de diensten erbij.
type whatIfDBView struct {
	whatIfView
	Services []struct {
		Name   string `json:"name"`
		Impact string `json:"impact"`
		Reason string `json:"reason"`
	} `json:"services"`
}

// TestWhatIfDatabaseMajority is de acceptatie van mijlpaal 13 deel 3 van
// fase 5: de wat-als kent de meerderheid van Galera. Vallen nc2 en nc3 weg,
// dan stopt MariaDB, ook al draait het op nc1 nog. Doet nc3 nu al niet mee,
// dan stopt het zonder nc2 alleen al.
func TestWhatIfDatabaseMajority(t *testing.T) {
	lab := newNCGalera(t, nil)
	lab.deploy()
	c, h := lab.c, lab.h
	h.check()
	h.clusterIs("healthy", "")

	whatIf := func(names ...string) whatIfDBView {
		t.Helper()
		var keys []string
		for _, n := range names {
			keys = append(keys, "node:"+lab.ids[n])
		}
		var r whatIfDBView
		if s := c.do("POST", "/api/v1/risk/what-if", map[string]any{"components": keys}, &r); s != http.StatusOK || len(r.Clusters) != 1 {
			t.Fatalf("wat als %v: %d %+v", names, s, r)
		}
		return r
	}
	service := func(r whatIfDBView, name string) string {
		for _, s := range r.Services {
			if s.Name == name {
				return s.Impact + ": " + s.Reason
			}
		}
		return ""
	}

	r := whatIf("nc3")
	if cl := r.Clusters[0]; cl.Before.Status != "healthy" || cl.During.Status != "degraded" ||
		!strings.HasSuffix(cl.During.Reason, "MariaDB Galera: nc3 valt uit; nog 2 van de 3 leden, genoeg voor een meerderheid") {
		t.Fatalf("nc3: %+v", cl)
	}
	if got := service(r, "mariadb"); got != "degraded: draait nog op nc1 en nc2" {
		t.Fatalf("nc3, mariadb: %s", got)
	}

	r = whatIf("nc2", "nc3")
	if cl := r.Clusters[0]; cl.During.Status != "down" ||
		cl.During.Reason != "MariaDB Galera: zonder nc2 en nc3 is er geen meerderheid (1 van de 3 leden), dus MariaDB stopt" {
		t.Fatalf("nc2 en nc3: %+v", cl)
	}
	if got := service(r, "mariadb"); got != "down: zonder nc2 en nc3 is er geen meerderheid (1 van de 3 leden)" {
		t.Fatalf("nc2 en nc3, mariadb: %s", got)
	}

	// MariaDB stopt op nc3: Galera gaat met nc1 en nc2 verder, en de
	// bewaking onthoudt wie meedoet.
	lab.g.leave("nc3", lab.g.hosts["nc3"])
	h.check()
	h.clusterIs("degraded", "MariaDB Galera: nc3 doet niet mee")
	r = whatIf("nc2")
	if cl := r.Clusters[0]; cl.Before.Status != "degraded" || cl.During.Status != "down" ||
		cl.During.Reason != "MariaDB Galera: zonder nc2 is er geen meerderheid (1 van de 2 leden; nc3 doet nu niet mee), dus MariaDB stopt" {
		t.Fatalf("nc2 met nc3 al uit: %+v", cl)
	}
}
