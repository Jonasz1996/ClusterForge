package gitops

import "github.com/Jonasz1996/clusterforge/internal/textdiff"

// Diff geeft een unified diff van a naar b; zie textdiff.Diff.
func Diff(a, b, from, to string) (diff string, changed int) {
	return textdiff.Diff(a, b, from, to)
}

func lines(s string) []string { return textdiff.Lines(s) }
