package agent

import "syscall"

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
