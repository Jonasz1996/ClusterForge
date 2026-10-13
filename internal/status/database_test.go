package status

import (
	"testing"
	"time"
)

func TestDatabase(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	fresh, old := now.Add(-20*time.Second), now.Add(-3*time.Minute)
	healthy := Result{Healthy, ""}
	cases := []struct {
		name string
		base Result
		in   []DBHealth
		want Result
	}{
		{"gezond", healthy, []DBHealth{{Healthy, "", fresh}}, healthy},
		{"verminderd", healthy, []DBHealth{{Degraded, "MariaDB Galera: nc3 doet niet mee (Joining)", fresh}},
			Result{Degraded, "MariaDB Galera: nc3 doet niet mee (Joining)"}},
		{"verminderd erbij", Result{Degraded, "nc2: schijf / is 92 % vol"}, []DBHealth{{Degraded, "PostgreSQL met Patroni: nc3 loopt 45 s achter", fresh}},
			Result{Degraded, "nc2: schijf / is 92 % vol; PostgreSQL met Patroni: nc3 loopt 45 s achter"}},
		{"down wint", Result{Degraded, "nc2: schijf / is 92 % vol"},
			[]DBHealth{{Degraded, "MariaDB Galera: nc3 doet niet mee", fresh}, {Down, "PostgreSQL met Patroni: geen primary", fresh}},
			Result{Down, "PostgreSQL met Patroni: geen primary"}},
		{"oud telt niet", healthy, []DBHealth{{Down, "MariaDB Galera: geen Primary component", old}}, healthy},
		{"nodes down blijft", Result{Down, "alle nodes zijn down"}, []DBHealth{{Degraded, "x", fresh}}, Result{Down, "alle nodes zijn down"}},
		{"split-brain blijft", Result{SplitBrain, "VIP op nc1 en nc2"}, []DBHealth{{Down, "x", fresh}}, Result{SplitBrain, "VIP op nc1 en nc2"}},
	}
	for _, c := range cases {
		if got := Database(c.base, c.in, now); got != c.want {
			t.Errorf("%s: %+v, verwacht %+v", c.name, got, c.want)
		}
	}
}
