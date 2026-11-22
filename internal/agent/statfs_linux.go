package agent

import (
	"io/fs"
	"os"
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
	bs := uint64(st.Bsize) //nolint:gosec // blokgrootte is nooit negatief
	return fsStat{size: st.Blocks * bs, free: st.Bfree * bs, avail: st.Bavail * bs}, true
}

// fileOwner geeft uid en gid van een bestand.
func fileOwner(fi fs.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// allocate maakt een bestand van n bytes dat echt ruimte inneemt.
func allocate(path string, n int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Fallocate(int(f.Fd()), 0, 0, n); err != nil { //nolint:gosec // een fd past in int
		_ = f.Close()
		return writeZeros(path, n)
	}
	return f.Close()
}

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
