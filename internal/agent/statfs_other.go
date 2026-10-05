//go:build !linux

package agent

// statfs is alleen op Linux geïmplementeerd; cf-agent draait alleen daar.
func statfs(string) (size, used uint64) { return 0, 0 }
