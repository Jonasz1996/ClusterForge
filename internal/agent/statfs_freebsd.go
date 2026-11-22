package agent

import (
	"io/fs"
	"path/filepath"
	"syscall"
)

// statfs geeft grootte, vrije en voor gebruikers beschikbare ruimte van het
// bestandssysteem op path in bytes.
func statfs(path string) (fsStat, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return fsStat{}, false
	}
	bs := st.Bsize
	avail := uint64(0)
	if st.Bavail > 0 {
		avail = uint64(st.Bavail) * bs
	}
	return fsStat{size: st.Blocks * bs, free: st.Bfree * bs, avail: avail}, true
}

// fileOwner geeft uid en gid van een bestand.
func fileOwner(fi fs.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// allocate schrijft nullen; FreeBSD heeft geen fallocate met deze vorm.
func allocate(path string, n int64) error { return writeZeros(path, n) }

// sameDevice zegt of a en b op hetzelfde bestandssysteem liggen; een pad
// dat er nog niet is, telt via zijn bovenliggende map. Bij twijfel ja.
func sameDevice(a, b string) bool {
	dev := func(p string) (uint64, bool) {
		for {
			var st syscall.Stat_t
			if err := syscall.Stat(p, &st); err == nil {
				return st.Dev, true
			}
			parent := filepath.Dir(p)
			if parent == p {
				return 0, false
			}
			p = parent
		}
	}
	da, oka := dev(a)
	db, okb := dev(b)
	return !oka || !okb || da == db
}
