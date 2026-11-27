package agentupgrade

import (
	"testing"
	"time"
)

func TestNightWindow(t *testing.T) {
	bru := brussels()
	at := func(h, m int) time.Time { return time.Date(2026, 11, 23, h, m, 0, 0, bru) }
	for _, c := range []struct {
		t  time.Time
		in bool
	}{{at(1, 59), false}, {at(2, 0), true}, {at(4, 59), true}, {at(5, 0), false}, {at(14, 0), false}} {
		if got := inWindow(c.t); got != c.in {
			t.Errorf("inWindow(%s) = %v", c.t.Format("15:04"), got)
		}
	}
	if got := nextWindow(at(14, 0)); !got.Equal(time.Date(2026, 11, 24, 2, 0, 0, 0, bru)) {
		t.Errorf("nextWindow(14:00) = %s", got)
	}
	if got := nextWindow(at(1, 0)); !got.Equal(at(2, 0)) {
		t.Errorf("nextWindow(01:00) = %s", got)
	}
}
