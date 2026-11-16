package security

import "strings"

// CompareDeb vergelijkt twee Debian-versies zoals dpkg dat doet:
// [epoch:]upstream[-revision]. Het resultaat is <0, 0 of >0.
func CompareDeb(a, b string) int {
	ea, ua, ra := splitDeb(a)
	eb, ub, rb := splitDeb(b)
	if c := compareNum(ea, eb); c != 0 {
		return c
	}
	if c := compareFragment(ua, ub); c != 0 {
		return c
	}
	return compareFragment(ra, rb)
}

func splitDeb(v string) (epoch, upstream, revision string) {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ':'); i >= 0 {
		epoch, v = v[:i], v[i+1:]
	}
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

// order is de volgorde van een teken in het niet-numerieke deel: ~ komt
// voor alles (ook voor het einde), letters voor andere tekens.
func order(c byte) int {
	switch {
	case c == '~':
		return -1
	case c >= '0' && c <= '9':
		return 0
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		return int(c)
	default:
		return int(c) + 256
	}
}

func compareFragment(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		// Niet-numeriek deel.
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			ac, bc := 0, 0
			if i < len(a) && !isDigit(a[i]) {
				ac = order(a[i])
			}
			if j < len(b) && !isDigit(b[j]) {
				bc = order(b[j])
			}
			if ac != bc {
				return ac - bc
			}
			if i < len(a) && !isDigit(a[i]) {
				i++
			}
			if j < len(b) && !isDigit(b[j]) {
				j++
			}
		}
		// Numeriek deel.
		si := i
		for i < len(a) && isDigit(a[i]) {
			i++
		}
		sj := j
		for j < len(b) && isDigit(b[j]) {
			j++
		}
		if c := compareNum(a[si:i], b[sj:j]); c != 0 {
			return c
		}
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// compareNum vergelijkt twee cijferreeksen zonder overloop.
func compareNum(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}
