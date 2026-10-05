//go:build !linux

package agent

// statfs is alleen op Linux geïmplementeerd; cf-agent draait alleen daar.
func statfs(string) (fsStat, bool) { return fsStat{}, false }
