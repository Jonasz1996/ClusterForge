//go:build !linux

package agent

import "io/fs"

// statfs is alleen op Linux geïmplementeerd; cf-agent draait alleen daar.
func statfs(string) (fsStat, bool) { return fsStat{}, false }

// fileOwner is alleen op Linux geïmplementeerd.
func fileOwner(fs.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
