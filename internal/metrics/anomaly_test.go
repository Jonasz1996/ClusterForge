package metrics

import "testing"

func series(vals ...float64) ([]int64, []*float64) {
	ts := make([]int64, len(vals))
	out := make([]*float64, len(vals))
	for i, v := range vals {
		ts[i] = int64(i * 60)
		out[i] = &v
	}
	return ts, out
}

func TestDetect(t *testing.T) {
	split := int64(5 * 60)
	for _, c := range []struct {
		metric string
		vals   []float64
		want   bool
		at     int64
	}{
		{"disk", []float64{80, 81, 82, 82, 83, 90, 96, 99}, true, 6 * 60},
		{"disk", []float64{96, 96, 97, 97, 97, 97, 98, 98}, false, 0},
		{"cpu", []float64{10, 12, 11, 9, 10, 50, 95, 97}, true, 6 * 60},
		{"cpu", []float64{70, 75, 80, 70, 72, 85, 91, 92}, false, 0},
		{"memory", []float64{60, 61, 60, 62, 61, 70, 90, 96}, true, 7 * 60},
		{"network_errors", []float64{0, 0, 0, 0, 0, 0, 5, 7}, true, 6 * 60},
		{"network_errors", []float64{0, 0, 0, 0, 0}, false, 0},
	} {
		ts, vals := series(c.vals...)
		a := Detect(c.metric, "/", ts, vals, split)
		if (a != nil) != c.want {
			t.Errorf("%s %v: %+v", c.metric, c.vals, a)
			continue
		}
		if a != nil && a.At.Unix() != c.at {
			t.Errorf("%s: at %d, want %d", c.metric, a.At.Unix(), c.at)
		}
	}
	// Ontbrekende punten tellen niet mee.
	ts, vals := series(80, 80, 80, 99, 99, 99)
	vals[0], vals[1] = nil, nil
	if a := Detect("disk", "/", ts, vals, 3*60); a != nil {
		t.Fatalf("te weinig punten: %+v", a)
	}
}
