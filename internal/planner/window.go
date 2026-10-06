// Package planner rekent geplande runs uit: het testvenster waarin geplande
// tests draaien, met zomer- en wintertijd, en of een run die te laat is nog
// mag starten. Elke module bewaart haar eigen next_run_at; er is geen
// algemene planningstabel.
package planner

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Late is hoe lang na het geplande moment een run nog mag starten. Een run
// die later komt, bijvoorbeeld omdat ClusterForge niet draaide, wordt
// overgeslagen in plaats van midden op de dag ingehaald.
const Late = 30 * time.Minute

// Clock is een tijdstip op de klok, in de tijdzone van de server.
type Clock struct{ Hour, Min int }

func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c.Hour, c.Min) }

func (c Clock) minutes() int { return c.Hour*60 + c.Min }

func parseClock(s string) (Clock, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	hour, err1 := strconv.Atoi(h)
	minute, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || len(m) != 2 || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return Clock{}, fmt.Errorf("%q is geen tijd zoals 03:00", s)
	}
	return Clock{hour, minute}, nil
}

// Window is het testvenster: op de gekozen dagen van Start tot End, op de
// klok van Loc. Ligt End niet na Start, dan eindigt het venster de volgende
// dag. Een venster telt bij de dag waarop het begint.
type Window struct {
	Days       [7]bool
	Start, End Clock
	// Loc is de tijdzone van de klok; nil is de tijdzone van de server (TZ).
	Loc *time.Location
}

// In geeft hetzelfde venster op de klok van loc.
func (w Window) In(loc *time.Location) Window {
	w.Loc = loc
	return w
}

func (w Window) loc() *time.Location {
	if w.Loc != nil {
		return w.Loc
	}
	return time.Local
}

// DefaultWindow is zondag van 03:00 tot 05:00.
var DefaultWindow = Window{Days: [7]bool{time.Sunday: true}, Start: Clock{3, 0}, End: Clock{5, 0}}

var dayNames = map[string]time.Weekday{
	"zo": time.Sunday, "zondag": time.Sunday, "sun": time.Sunday, "sunday": time.Sunday,
	"ma": time.Monday, "maandag": time.Monday, "mon": time.Monday, "monday": time.Monday,
	"di": time.Tuesday, "dinsdag": time.Tuesday, "tue": time.Tuesday, "tuesday": time.Tuesday,
	"wo": time.Wednesday, "woensdag": time.Wednesday, "wed": time.Wednesday, "wednesday": time.Wednesday,
	"do": time.Thursday, "donderdag": time.Thursday, "thu": time.Thursday, "thursday": time.Thursday,
	"vr": time.Friday, "vrijdag": time.Friday, "fri": time.Friday, "friday": time.Friday,
	"za": time.Saturday, "zaterdag": time.Saturday, "sat": time.Saturday, "saturday": time.Saturday,
}

var everyDay = map[string]bool{"dagelijks": true, "elke-dag": true, "daily": true}

// ParseWindow leest een venster zoals "zo 03:00-05:00", "za,zo 02:00-04:30"
// of "dagelijks 03:00-05:00".
func ParseWindow(s string) (Window, error) {
	fields := strings.Fields(strings.ToLower(strings.ReplaceAll(s, "–", "-")))
	if len(fields) != 2 {
		return Window{}, errors.New(`schrijf het venster als "zo 03:00-05:00": dagen, een spatie en de tijden`)
	}
	var w Window
	if everyDay[fields[0]] {
		w.Days = [7]bool{true, true, true, true, true, true, true}
	} else {
		for _, d := range strings.Split(fields[0], ",") {
			wd, ok := dayNames[d]
			if !ok {
				return Window{}, fmt.Errorf("%q is geen dag; gebruik ma, di, wo, do, vr, za, zo of dagelijks", d)
			}
			w.Days[wd] = true
		}
	}
	from, to, ok := strings.Cut(fields[1], "-")
	if !ok {
		return Window{}, errors.New(`schrijf de tijden als 03:00-05:00`)
	}
	var err error
	if w.Start, err = parseClock(from); err != nil {
		return Window{}, err
	}
	if w.End, err = parseClock(to); err != nil {
		return Window{}, err
	}
	if w.Start == w.End {
		return Window{}, errors.New("het venster begint en eindigt op hetzelfde moment")
	}
	return w, nil
}

var dutchDays = [7]string{"zondag", "maandag", "dinsdag", "woensdag", "donderdag", "vrijdag", "zaterdag"}

// String is het venster in gewone taal, zoals "zondag van 03:00 tot 05:00".
func (w Window) String() string {
	var days []string
	// Maandag eerst, zondag laatst.
	for i := 1; i <= 7; i++ {
		if d := time.Weekday(i % 7); w.Days[d] {
			days = append(days, dutchDays[d])
		}
	}
	which := "elke dag"
	switch {
	case len(days) == 0:
		which = "nooit"
	case len(days) < 7:
		which = days[0]
		if len(days) > 1 {
			which = strings.Join(days[:len(days)-1], ", ") + " en " + days[len(days)-1]
		}
	}
	return fmt.Sprintf("%s van %s tot %s", which, w.Start, w.End)
}

// on geeft het venster dat begint op de kalenderdag van day, in loc.
func (w Window) on(y int, m time.Month, d int, loc *time.Location) (start, end time.Time) {
	start = time.Date(y, m, d, w.Start.Hour, w.Start.Min, 0, 0, loc)
	ed := d
	if w.End.minutes() <= w.Start.minutes() {
		ed++
	}
	return start, time.Date(y, m, ed, w.End.Hour, w.End.Min, 0, 0, loc)
}

// Next geeft het venster waarin t valt, of anders het eerste venster na t.
// Begin en einde liggen op de klok van Loc, dus een venster van 03:00 tot
// 05:00 blijft dat ook rond de overgang naar zomer- of wintertijd.
func (w Window) Next(t time.Time) (start, end time.Time) {
	loc := w.loc()
	t = t.In(loc)
	y, m, d := t.Date()
	// Een venster van gisteren kan over middernacht nog lopen.
	for i := -1; i <= 8; i++ {
		noon := time.Date(y, m, d+i, 12, 0, 0, 0, loc)
		if !w.Days[noon.Weekday()] {
			continue
		}
		s, e := w.on(noon.Year(), noon.Month(), noon.Day(), loc)
		if e.After(t) {
			return s, e
		}
	}
	return time.Time{}, time.Time{}
}

// After geeft het begin van het eerste venster na het venster van due.
// Zo schuift een geplande run na een uitvoering of een overgeslagen run
// naar het volgende venster.
func (w Window) After(due time.Time) time.Time {
	_, end := w.Next(due)
	start, _ := w.Next(end)
	return start
}

// Following is de volgende run na een run die op due stond: het venster na
// dat van due. Lag de server langer stil, dan is dat venster misschien al
// voorbij; dan is het het eerste moment vanaf now, zodat een gemiste week
// één overgeslagen run geeft en niet één per venster.
func (w Window) Following(due, now time.Time) time.Time {
	next := w.After(due)
	if _, end := w.Next(next); !end.After(now) {
		return w.First(now)
	}
	return next
}

// First is het eerste moment vanaf now waarop een net geplande run mag
// draaien: meteen als het venster nu loopt, en anders het begin van het
// volgende venster.
func (w Window) First(now time.Time) time.Time {
	start, _ := w.Next(now)
	if start.Before(now) {
		return now
	}
	return start
}

// Decision is wat de planner met een geplande run doet.
type Decision int

const (
	// Wait: nog niet aan de beurt, of het testslot is bezet.
	Wait Decision = iota
	// Run: nu starten.
	Run
	// Skip: overslaan en naar het volgende venster.
	Skip
)

// Decide zegt wat de planner nu met een run doet die op due gepland stond.
// free is het moment waarop het testslot vrijkwam; een run die op een
// andere test wachtte, telt die wachttijd niet als te laat. busy betekent
// dat er nu een test loopt. Bij Skip is reason de uitleg.
func (w Window) Decide(due, free, now time.Time, busy bool) (d Decision, reason string) {
	if now.Before(due) {
		return Wait, ""
	}
	start, end := w.Next(due)
	if start.IsZero() {
		return Skip, "er is geen testvenster"
	}
	if !now.Before(end) {
		return Skip, fmt.Sprintf("het testvenster van %s eindigde om %s voordat de run kon starten", dayDate(start), end.Format("15:04"))
	}
	if now.Before(start) {
		return Wait, ""
	}
	if busy {
		return Wait, ""
	}
	from := later(due, start)
	// Een vrijgave na now kan alleen door een verzette klok; die telt niet.
	if free.After(from) && !free.After(now) {
		from = free
	}
	if now.Sub(from) > Late {
		return Skip, fmt.Sprintf("de run stond om %s gepland en is meer dan %d minuten te laat, bijvoorbeeld omdat ClusterForge niet draaide",
			from.In(w.loc()).Format("15:04"), int(Late.Minutes()))
	}
	return Run, ""
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// dayDate schrijft een dag als "zondag 25-10-2026".
func dayDate(t time.Time) string {
	return dutchDays[t.Weekday()] + " " + t.Format("02-01-2006")
}
