package backups

import (
	"testing"
	"time"
)

func TestJudge(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	at := func(h float64) *time.Time {
		x := now.Add(-time.Duration(h * float64(time.Hour)))
		return &x
	}
	for _, tc := range []struct {
		latest *time.Time
		want   Freshness
	}{
		{nil, Missing},
		{at(1), OK},
		{at(30), OK},
		{at(30.01), Stale},
		{at(-1), OK}, // klok van Proxmox loopt voor
	} {
		if got := Judge(tc.latest, 30*time.Hour, now); got != tc.want {
			t.Errorf("%v: %s, wilde %s", tc.latest, got, tc.want)
		}
	}
	if Worse(OK, Stale) != Stale || Worse(Missing, Stale) != Missing || Worse(Unknown, OK) != Unknown || Worse(Stale, Unknown) != Stale {
		t.Fatal("Worse")
	}
}
