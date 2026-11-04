//go:build !linux

package agent

import "io/fs"

// statfs is alleen op Linux geïmplementeerd; cf-agent draait alleen daar.
func statfs(string) (fsStat, bool) { return fsStat{}, false }

// fileOwner is alleen op Linux geïmplementeerd.
func fileOwner(fs.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }

// allocate schrijft nullen; fallocate is er alleen op Linux.
func allocate(path string, n int64) error { return writeZeros(path, n) }

// sameDevice is alleen op Linux geïmplementeerd; zonder weten: ja.
func sameDevice(string, string) bool { return true }
