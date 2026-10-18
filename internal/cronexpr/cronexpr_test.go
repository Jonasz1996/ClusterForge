package cronexpr

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
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

func TestOnCalendar(t *testing.T) {
	for _, c := range []struct{ in, want, err string }{
		{"0 */6 * * *", "*-*-* 00,06,12,18:00:00", ""},
		{"*/15 * * * *", "*-*-* *:00,15,30,45:00", ""},
		{"30 3 * * 1-5", "Mon,Tue,Wed,Thu,Fri *-*-* 03:30:00", ""},
		{"0 2 1 * *", "*-*-01 02:00:00", ""},
		{"@daily", "*-*-* 00:00:00", ""},
		{"0 4 * * 0", "Sun *-*-* 04:00:00", ""},
		{"0 0 1 1 *", "*-01-01 00:00:00", ""},
		{"0 1 1 * 1", "", "weekdag"},
		{"@reboot", "", "@reboot"},
	} {
		sc, err := Parse(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		got, err := sc.OnCalendar()
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: fout %v, wil %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, wil %q", c.in, got, err, c.want)
		}
	}
}

// TestOnCalendarSystemd laat systemd-analyze de omgezette schema's lezen en
// vergelijkt de volgende run met Next, als systemd-analyze er is.
func TestOnCalendarSystemd(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("geen systemd-analyze")
	}
	for _, in := range []string{"0 */6 * * *", "*/15 * * * *", "30 3 * * 1-5", "0 2 1 * *", "17 * * * *", "0 4 * * 0", "5 0 1,15 2-4 *"} {
		sc, _ := Parse(in)
		cal, err := sc.OnCalendar()
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		out, err := exec.Command("systemd-analyze", "calendar", "--base-time=@"+strconv.FormatInt(now.Unix(), 10), cal).CombinedOutput()
		if err != nil {
			t.Fatalf("%s → %s: %v\n%s", in, cal, err, out)
		}
		m := regexp.MustCompile(`Next elapse: \w+ (\S+ \S+) UTC`).FindSubmatch(out)
		if m == nil {
			t.Skipf("systemd-analyze rekent niet in UTC:\n%s", out)
		}
		want := sc.Next(now).Format("2006-01-02 15:04:05")
		if string(m[1]) != want {
			t.Errorf("%s → %s: systemd %s, Next %s", in, cal, m[1], want)
		}
	}
}
