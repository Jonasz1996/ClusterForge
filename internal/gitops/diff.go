package gitops

import (
	"fmt"
	"strings"
)

// maxDiffCells begrenst het werk van een diff: regels oud keer regels nieuw.
const maxDiffCells = 2_000_000

type op struct {
	kind byte // ' ', '-' of '+'
	line string
}

// Diff geeft een unified diff van a naar b met drie regels context, en
// hoeveel regels er anders zijn. Is het bestand te groot om te vergelijken,
// dan is de diff leeg en changed -1.
func Diff(a, b, from, to string) (diff string, changed int) {
	if a == b {
		return "", 0
	}
	x, y := lines(a), lines(b)
	if len(x)*len(y) > maxDiffCells {
		return "", -1
	}
	ops := lcsOps(x, y)
	// Tel per blok wijzigingen het grootste van weg en erbij.
	del, ins := 0, 0
	flush := func() {
		changed += max(del, ins)
		del, ins = 0, 0
	}
	for _, o := range ops {
		switch o.kind {
		case '-':
			del++
		case '+':
			ins++
		default:
			flush()
		}
	}
	flush()

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", from, to)
	const ctx = 3
	// Posities in a en b per op.
	type pos struct{ a, b int }
	at := make([]pos, len(ops)+1)
	for i, o := range ops {
		at[i+1] = at[i]
		if o.kind != '+' {
			at[i+1].a++
		}
		if o.kind != '-' {
			at[i+1].b++
		}
	}
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(0, i-ctx)
		end := i
		// Breid het blok uit zolang de volgende wijziging binnen 2*ctx ligt.
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			j := end
			for j < len(ops) && ops[j].kind == ' ' {
				j++
			}
			if j == len(ops) || j-end > 2*ctx {
				end = min(len(ops), end+ctx)
				break
			}
			end = j
		}
		na, nb := at[end].a-at[start].a, at[end].b-at[start].b
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", rangeText(at[start].a, na), rangeText(at[start].b, nb))
		for _, o := range ops[start:end] {
			out.WriteByte(o.kind)
			out.WriteString(o.line)
			out.WriteByte('\n')
		}
		i = end
	}
	return out.String(), changed
}

func rangeText(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if n == 1 {
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, n)
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lcsOps geeft de bewerkingen van x naar y langs de langste gemeenschappelijke
// deelreeks.
func lcsOps(x, y []string) []op {
	n, m := len(x), len(y)
	t := make([]int32, (n+1)*(m+1))
	at := func(i, j int) *int32 { return &t[i*(m+1)+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				*at(i, j) = *at(i+1, j+1) + 1
			} else {
				*at(i, j) = max(*at(i+1, j), *at(i, j+1))
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case x[i] == y[j]:
			ops = append(ops, op{' ', x[i]})
			i++
			j++
		case *at(i+1, j) >= *at(i, j+1):
			ops = append(ops, op{'-', x[i]})
			i++
		default:
			ops = append(ops, op{'+', y[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', x[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', y[j]})
	}
	return ops
}
