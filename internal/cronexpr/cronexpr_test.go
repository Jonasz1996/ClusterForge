package cronexpr

import (
	"testing"
	"time"
)

func TestDescribe(t *testing.T) {
	cases := map[string]string{
		"* * * * *":       "elke minuut",
		"*/5 * * * *":     "elke 5 minuten",
		"17 * * * *":      "elk uur, op minuut 17",
		"0 */6 * * *":     "elke 6 uur, op minuut 0",
		"0 3 * * *":       "elke dag om 03:00",
		"30 2,14 * * *":   "elke dag om 02:30 en 14:30",
		"0 4 * * 1-5":     "op werkdagen, om 04:00",
		"0 4 * * 0,6":     "op zaterdag en zondag, om 04:00",
		"52 6 1 * *":      "op dag 1 van de maand, om 06:52",
		"0 0 1 1 *":       "op dag 1 van de maand in januari, om 00:00",
		"0 1 1 * 1":       "op dag 1 van de maand of op maandag, om 01:00",
		"@reboot":         "bij het opstarten",
		"weekly":          "op zondag, om 06:47",
		"0,30 8-17 * * *": "op minuut 0 en 30 van de uren 8, 9, 10, 11, 12, 13, 14, 15, 16 en 17",
	}
	for in, want := range cases {
		s, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if got := s.Describe(); got != want {
			t.Errorf("Describe(%q) = %q, wil %q", in, got, want)
		}
	}
}

func TestNext(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Brussels")
	from := time.Date(2026, 10, 18, 2, 10, 0, 0, loc)
	cases := map[string]time.Time{
		"*/5 * * * *": time.Date(2026, 10, 18, 2, 15, 0, 0, loc),
		"0 */6 * * *": time.Date(2026, 10, 18, 6, 0, 0, 0, loc),
		"0 3 * * *":   time.Date(2026, 10, 18, 3, 0, 0, 0, loc),
		"0 4 * * 1-5": time.Date(2026, 10, 19, 4, 0, 0, 0, loc),
		// Dag 1 of maandag: maandag 19 oktober komt eerst.
		"0 1 1 * 1":  time.Date(2026, 10, 19, 1, 0, 0, 0, loc),
		"0 0 29 2 *": time.Date(2028, 2, 29, 0, 0, 0, 0, loc),
	}
	for in, want := range cases {
		s, err := Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Next(from); !got.Equal(want) {
			t.Errorf("Next(%q) = %v, wil %v", in, got, want)
		}
	}
	if s, _ := Parse("@reboot"); !s.Next(from).IsZero() {
		t.Error("@reboot heeft geen volgende run")
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"* * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "5-1 * * * *", "* * * foo *"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) zonder fout", in)
		}
	}
}
