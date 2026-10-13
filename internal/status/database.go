package status

import (
	"strings"
	"time"
)

// DBHealthStale is hoe oud de toestand van een database mag zijn voor ze
// meetelt. De bewaking kijkt om de twintig seconden; tijdens een taak in
// het cluster kijkt ze niet, en dan telt de database niet mee.
const DBHealthStale = 2 * time.Minute

// DBHealth is wat de bewaking van een database van het cluster laatst zag.
// Reason begint met de naam van de database, zoals 'MariaDB Galera: nc3
// doet niet mee'.
type DBHealth struct {
	Status Status
	Reason string
	At     time.Time
}

// Database neemt de databases van een cluster mee in zijn status. Down als
// een database down is: geen Primary component of geen primary, of een VIP
// zonder database erachter. Verminderd als een database verminderd is. Is
// het cluster door zijn nodes al down, in split-brain of onbekend, dan
// blijft dat zo.
func Database(base Result, in []DBHealth, now time.Time) Result {
	if base.Status == Down || base.Status == SplitBrain || base.Status == Unknown {
		return base
	}
	var down, degraded []string
	for _, h := range in {
		if now.Sub(h.At) > DBHealthStale {
			continue
		}
		switch h.Status {
		case Down:
			down = append(down, h.Reason)
		case Degraded:
			degraded = append(degraded, h.Reason)
		}
	}
	switch {
	case len(down) > 0:
		return Result{Down, strings.Join(down, "; ")}
	case len(degraded) == 0:
		return base
	case base.Status == Degraded:
		degraded = append([]string{base.Reason}, degraded...)
	}
	return Result{Degraded, strings.Join(degraded, "; ")}
}
