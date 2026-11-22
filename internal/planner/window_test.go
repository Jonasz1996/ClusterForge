package planner

import (
	"strings"
	"testing"
	"time"
)

func brussels(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestParseWindow(t *testing.T) {
	for in, want := range map[string]string{
		"zo 03:00-05:00":          "zondag van 03:00 tot 05:00",
		"Sun 03:00–05:00":         "zondag van 03:00 tot 05:00",
		"za,zo 02:00-04:30":       "zaterdag en zondag van 02:00 tot 04:30",
		"zo,ma,wo 23:30-01:00":    "maandag, woensdag en zondag van 23:30 tot 01:00",
		"dagelijks 03:00-05:00":   "elke dag van 03:00 tot 05:00",
		"  vrijdag   22:00-23:00": "vrijdag van 22:00 tot 23:00",
	} {
		w, err := ParseWindow(in)
		if err != nil || w.String() != want {
			t.Errorf("%q: %v %q, wil %q", in, err, w.String(), want)
		}
	}
	for _, in := range []string{"", "zo", "zo 03:00", "xx 03:00-05:00", "zo 3:00-05:00x", "zo 25:00-05:00", "zo 03:00-03:00", "zo 03:00 05:00"} {
		if _, err := ParseWindow(in); err == nil {
			t.Errorf("%q: geen fout", in)
		}
	}
}

// Elke 7 dagen om 04:00, over de overgang naar wintertijd op zondag 25
// oktober 2026: het venster blijft om 04:00 op de klok beginnen, dus de
// afstand is een week plus een uur.
func TestWeeklyAcrossDST(t *testing.T) {
	loc := brussels(t)
	w, err := ParseWindow("zo 04:00-06:00")
	if err != nil {
		t.Fatal(err)
	}
	w = w.In(loc)
	first := time.Date(2026, 10, 18, 4, 0, 0, 0, loc)
	next := w.After(first)
	if want := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("na 18 oktober: %s, wil %s", next, want)
	}
	if got := next.In(loc).Format("2006-01-02 15:04 MST"); got != "2026-10-25 04:00 CET" {
		t.Fatalf("op de klok: %s", got)
	}
	if d := next.Sub(first); d != 7*24*time.Hour+time.Hour {
		t.Fatalf("afstand %s", d)
	}
	// En terug naar zomertijd op zondag 28 maart 2027.
	spring := w.After(time.Date(2027, 3, 21, 4, 0, 0, 0, loc))
	if got := spring.In(loc).Format("2006-01-02 15:04 MST"); got != "2027-03-28 04:00 CEST" {
		t.Fatalf("voorjaar: %s", got)
	}
	// Het venster zelf duurt op de klok twee uur, ook op 25 oktober. Een
	// tijdstip in UTC rekent op de klok van het venster.
	start, end := w.Next(time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC))
	if start.In(loc).Hour() != 4 || end.In(loc).Hour() != 6 || end.Sub(start) != 2*time.Hour {
		t.Fatalf("venster op 25 oktober: %s tot %s", start, end)
	}
	// Een venster dat in het dubbele uur ligt, telt in wintertijd mee.
	w3 := DefaultWindow.In(loc)
	start, end = w3.Next(time.Date(2026, 10, 25, 0, 30, 0, 0, loc))
	if start.UTC().Format(time.RFC3339) != "2026-10-25T02:00:00Z" || end.UTC().Format(time.RFC3339) != "2026-10-25T04:00:00Z" {
		t.Fatalf("standaardvenster op 25 oktober: %s tot %s", start.UTC(), end.UTC())
	}
}

func TestNextAndFirst(t *testing.T) {
	loc := brussels(t)
	w := DefaultWindow.In(loc)
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, loc) }
	// Dinsdag 6 oktober: het volgende venster is zondag 11 oktober.
	if s, e := w.Next(at(6, 14, 0)); !s.Equal(at(11, 3, 0)) || !e.Equal(at(11, 5, 0)) {
		t.Fatalf("na dinsdag: %s tot %s", s, e)
	}
	// Tijdens het venster: dat venster, en een net geplande run mag meteen.
	if s, _ := w.Next(at(11, 4, 10)); !s.Equal(at(11, 3, 0)) {
		t.Fatalf("tijdens: %s", s)
	}
	if f := w.First(at(11, 4, 10)); !f.Equal(at(11, 4, 10)) {
		t.Fatalf("first tijdens: %s", f)
	}
	if f := w.First(at(11, 5, 0)); !f.Equal(at(18, 3, 0)) {
		t.Fatalf("first op het einde: %s", f)
	}
	// Over middernacht: zaterdag 23:30 tot zondag 01:00.
	night, _ := ParseWindow("za 23:30-01:00")
	night = night.In(loc)
	if s, e := night.Next(at(11, 0, 30)); !s.Equal(at(10, 23, 30)) || !e.Equal(at(11, 1, 0)) {
		t.Fatalf("over middernacht: %s tot %s", s, e)
	}
	if a := night.After(at(10, 23, 30)); !a.Equal(at(17, 23, 30)) {
		t.Fatalf("na het nachtvenster: %s", a)
	}
}

func TestDecide(t *testing.T) {
	loc := brussels(t)
	w := DefaultWindow.In(loc)
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, loc) }
	due := at(11, 3, 0)
	never := time.Time{}
	for _, c := range []struct {
		name   string
		now    time.Time
		free   time.Time
		busy   bool
		want   Decision
		reason string
	}{
		{"nog niet", at(11, 2, 59), never, false, Wait, ""},
		{"op tijd", at(11, 3, 0), never, false, Run, ""},
		{"wat later", at(11, 3, 25), never, false, Run, ""},
		{"te laat", at(11, 3, 31), never, false, Skip, "meer dan 30 minuten te laat"},
		{"slot bezet", at(11, 4, 20), never, true, Wait, ""},
		{"na een andere test", at(11, 4, 20), at(11, 4, 10), false, Run, ""},
		{"lang na een andere test", at(11, 4, 50), at(11, 4, 10), false, Skip, "te laat"},
		{"venster voorbij", at(11, 5, 0), at(11, 4, 59), false, Skip, "eindigde om 05:00"},
		{"venster voorbij en bezet", at(11, 6, 0), never, true, Skip, "zondag 11-10-2026"},
		{"server lag eruit", at(13, 9, 0), never, false, Skip, "eindigde"},
	} {
		d, reason := w.Decide(due, c.free, c.now, c.busy)
		if d != c.want || !strings.Contains(reason, c.reason) {
			t.Errorf("%s: %v %q", c.name, d, reason)
		}
	}
	// Na een wijziging van het venster valt een run buiten elk venster: hij
	// wacht op het volgende en is dan niet te laat.
	if d, _ := w.Decide(at(7, 12, 0), never, at(11, 3, 10), false); d != Run {
		t.Fatalf("oude planning: %v", d)
	}
	if d, _ := w.Decide(at(7, 12, 0), never, at(8, 12, 0), false); d != Wait {
		t.Fatalf("oude planning buiten het venster: %v", d)
	}
}

func TestFollowing(t *testing.T) {
	loc := brussels(t)
	w := DefaultWindow.In(loc)
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, loc) }
	due := at(11, 3, 0)
	for _, c := range []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"gedraaid in het venster", at(11, 3, 5), at(18, 3, 0)},
		{"te laat in hetzelfde venster", at(11, 3, 45), at(18, 3, 0)},
		{"server lag een week stil", at(19, 9, 0), at(25, 3, 0)},
		{"terug tijdens het volgende venster", at(18, 3, 10), at(18, 3, 0)},
		{"terug net na het volgende venster", at(18, 5, 0), at(25, 3, 0)},
	} {
		if got := w.Following(due, c.now); !got.Equal(c.want) {
			t.Errorf("%s: %s, wil %s", c.name, got, c.want)
		}
	}
}
