package security

import "testing"

func TestCompareDeb(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0", "1.1", -1},
		{"1.10", "1.9", 1},
		{"1.0~rc1", "1.0", -1},
		{"1.0~rc1", "1.0~rc2", -1},
		{"1.0", "1.0+deb12u1", -1},
		{"1:1.0", "2.0", 1},
		{"3.0.11-1~deb12u2", "3.0.11-1~deb12u1", 1},
		{"3.0.11-1~deb12u2", "3.0.11-1", -1},
		{"3.5.1-1+deb13u1", "3.5.1-1", 1},
		{"2.36-9+deb12u7", "2.36-9+deb12u10", -1},
		{"1.2.3-1", "1.2.3-1.1", -1},
		{"0", "0.1", -1},
		{"1.0a", "1.0", 1},
		{"1.0-1", "1.0-1", 0},
		{"6.1.0-26", "6.1.0-9", 1},
		{"00012", "12", 0},
		{"1.0.0", "1.0", 1},
	}
	for _, c := range cases {
		got := CompareDeb(c.a, c.b)
		if sign(got) != c.want {
			t.Errorf("CompareDeb(%q, %q) = %d, wil %d", c.a, c.b, got, c.want)
		}
		if sign(CompareDeb(c.b, c.a)) != -c.want {
			t.Errorf("CompareDeb(%q, %q) is niet symmetrisch", c.b, c.a)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
