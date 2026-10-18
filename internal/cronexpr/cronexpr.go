// Package cronexpr leest een cronschema van vijf velden of een @-woord,
// rekent de volgende runs uit en beschrijft het in gewone taal. Het volgt
// cron op Debian: is zowel de dag van de maand als de weekdag beperkt, dan
// draait de job als een van beide klopt.
package cronexpr

import (
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// Schedule is een gelezen schema. Elk veld is een bitmasker van de waarden
// die meedoen.
type Schedule struct {
	Minute, Hour, Dom, Month, Dow uint64
	// DomStar en DowStar zijn true als het veld met * begon; dan telt het
	// niet mee voor de "of" van cron.
	DomStar, DowStar bool
	// Reboot is @reboot: geen tijd, alleen bij het opstarten.
	Reboot bool
}

var macros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *", "@weekly": "0 0 * * 0",
	"@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
	// De periodieke mappen van Debian, met de tijden uit /etc/crontab.
	"hourly": "17 * * * *", "daily": "25 6 * * *", "weekly": "47 6 * * 7", "monthly": "52 6 1 * *",
}

var monthNames = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
var dowNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Parse leest een schema.
func Parse(s string) (Schedule, error) {
	s = strings.TrimSpace(s)
	if s == "@reboot" {
		return Schedule{Reboot: true}, nil
	}
	if m, ok := macros[strings.ToLower(s)]; ok {
		s = m
	}
	f := strings.Fields(s)
	if len(f) != 5 {
		return Schedule{}, fmt.Errorf("een cronschema heeft vijf velden (minuut uur dag maand weekdag), dit heeft er %d", len(f))
	}
	var sc Schedule
	var err error
	if sc.Minute, err = field(f[0], 0, 59, nil); err != nil {
		return sc, fmt.Errorf("minuut: %w", err)
	}
	if sc.Hour, err = field(f[1], 0, 23, nil); err != nil {
		return sc, fmt.Errorf("uur: %w", err)
	}
	if sc.Dom, err = field(f[2], 1, 31, nil); err != nil {
		return sc, fmt.Errorf("dag van de maand: %w", err)
	}
	if sc.Month, err = field(f[3], 1, 12, monthNames); err != nil {
		return sc, fmt.Errorf("maand: %w", err)
	}
	if sc.Dow, err = field(f[4], 0, 7, dowNames); err != nil {
		return sc, fmt.Errorf("weekdag: %w", err)
	}
	// 7 is ook zondag.
	if sc.Dow&(1<<7) != 0 {
		sc.Dow = sc.Dow&^(1<<7) | 1
	}
	sc.DomStar, sc.DowStar = strings.HasPrefix(f[2], "*"), strings.HasPrefix(f[4], "*")
	return sc, nil
}

func field(s string, lo, hi int, names []string) (uint64, error) {
	var mask uint64
	for part := range strings.SplitSeq(s, ",") {
		rng, stepS, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepS)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("ongeldige stap %q", stepS)
			}
			step = n
		}
		var from, to int
		switch {
		case rng == "*":
			from, to = lo, hi
			if names != nil && hi == 7 {
				to = 6
			}
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if from, err = value(a, lo, hi, names); err != nil {
				return 0, err
			}
			if to, err = value(b, lo, hi, names); err != nil {
				return 0, err
			}
			if to < from {
				return 0, fmt.Errorf("bereik %q loopt achteruit", rng)
			}
		default:
			v, err := value(rng, lo, hi, names)
			if err != nil {
				return 0, err
			}
			from, to = v, v
			if hasStep {
				to = hi
			}
		}
		for v := from; v <= to; v += step {
			mask |= 1 << uint(v)
		}
	}
	return mask, nil
}

func value(s string, lo, hi int, names []string) (int, error) {
	for i, n := range names {
		if strings.EqualFold(s, n) {
			if hi == 12 {
				return i + 1, nil
			}
			return i, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("%q ligt niet tussen %d en %d", s, lo, hi)
	}
	return v, nil
}

func (s Schedule) dayMatches(t time.Time) bool {
	dom := s.Dom&(1<<uint(t.Day())) != 0
	dow := s.Dow&(1<<uint(t.Weekday())) != 0
	if s.DomStar || s.DowStar {
		return dom && dow
	}
	return dom || dow
}

// Next geeft de eerste run na t, in de tijdzone van t. Nul als er binnen
// vijf jaar geen is, of bij @reboot.
func (s Schedule) Next(t time.Time) time.Time {
	if s.Reboot || s.Minute == 0 || s.Hour == 0 || s.Month == 0 {
		return time.Time{}
	}
	loc := t.Location()
	t = t.Truncate(time.Minute).Add(time.Minute)
	end := t.AddDate(5, 0, 0)
	for t.Before(end) {
		if s.Month&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if s.Hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}
		if s.Minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// list geeft de waarden in een masker.
func list(mask uint64, lo, hi int) []int {
	var out []int
	for v := lo; v <= hi; v++ {
		if mask&(1<<uint(v)) != 0 {
			out = append(out, v)
		}
	}
	return out
}

func full(mask uint64, lo, hi int) bool { return bits.OnesCount64(mask) == hi-lo+1 }

// step geeft de stap als het masker lo, lo+n, lo+2n, ... tot hi is.
func step(mask uint64, lo, hi int) int {
	vals := list(mask, lo, hi)
	if len(vals) < 2 || vals[0] != lo {
		return 0
	}
	n := vals[1] - vals[0]
	for i := 1; i < len(vals); i++ {
		if vals[i]-vals[i-1] != n {
			return 0
		}
	}
	if vals[len(vals)-1]+n <= hi {
		return 0
	}
	return n
}

var dagen = []string{"zondag", "maandag", "dinsdag", "woensdag", "donderdag", "vrijdag", "zaterdag"}
var maanden = []string{"", "januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}

func join(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " en " + words[len(words)-1]
}

// Describe beschrijft het schema in gewone taal, zoals "elke 6 uur, op
// minuut 0" of "elke dag om 03:00".
func (s Schedule) Describe() string {
	if s.Reboot {
		return "bij het opstarten"
	}
	mins, hours := list(s.Minute, 0, 59), list(s.Hour, 0, 23)
	var when string
	switch {
	case full(s.Minute, 0, 59) && full(s.Hour, 0, 23):
		when = "elke minuut"
	case step(s.Minute, 0, 59) > 0 && full(s.Hour, 0, 23):
		when = fmt.Sprintf("elke %d minuten", step(s.Minute, 0, 59))
	case full(s.Hour, 0, 23):
		if len(mins) == 1 {
			when = fmt.Sprintf("elk uur, op minuut %d", mins[0])
		} else {
			when = fmt.Sprintf("elk uur, op minuut %s", join(ints(mins)))
		}
	case step(s.Hour, 0, 23) > 0 && len(mins) == 1:
		when = fmt.Sprintf("elke %d uur, op minuut %d", step(s.Hour, 0, 23), mins[0])
	case len(mins) == 1 && len(hours) <= 6:
		var times []string
		for _, h := range hours {
			times = append(times, fmt.Sprintf("%02d:%02d", h, mins[0]))
		}
		when = "om " + join(times)
	case len(mins) == 1:
		when = fmt.Sprintf("om minuut %d van de uren %s", mins[0], join(ints(hours)))
	default:
		when = fmt.Sprintf("op minuut %s van de uren %s", join(ints(mins)), join(ints(hours)))
	}
	var days []string
	domAll, dowAll := full(s.Dom, 1, 31), full(s.Dow, 0, 6)
	if !domAll {
		days = append(days, "op dag "+join(ints(list(s.Dom, 1, 31)))+" van de maand")
	}
	if !dowAll {
		var names []string
		dows := list(s.Dow, 0, 6)
		if s.Dow == 0b0111110 {
			names = []string{"werkdagen"}
		} else {
			// Maandag eerst, zondag laatst.
			for _, d := range dows {
				if d != 0 {
					names = append(names, dagen[d])
				}
			}
			if dows[0] == 0 {
				names = append(names, dagen[0])
			}
		}
		days = append(days, "op "+join(names))
	}
	dayText := ""
	switch {
	case len(days) == 2 && !s.DomStar && !s.DowStar:
		dayText = days[0] + " of " + days[1]
	case len(days) > 0:
		dayText = strings.Join(days, " ")
	}
	if !full(s.Month, 1, 12) {
		var ms []string
		for _, m := range list(s.Month, 1, 12) {
			ms = append(ms, maanden[m])
		}
		if dayText != "" {
			dayText += " "
		}
		dayText += "in " + join(ms)
	}
	if dayText == "" {
		if strings.HasPrefix(when, "om ") {
			return "elke dag " + when
		}
		return when
	}
	return dayText + ", " + when
}

func ints(v []int) []string {
	out := make([]string, len(v))
	for i, n := range v {
		out[i] = strconv.Itoa(n)
	}
	return out
}
