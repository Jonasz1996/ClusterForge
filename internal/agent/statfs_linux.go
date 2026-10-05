package agent

import "syscall"

// statfs geeft grootte en gebruik van het bestandssysteem op path in bytes.
func statfs(path string) (size, used uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bs := uint64(st.Bsize) //nolint:gosec // blokgrootte is nooit negatief
	size = st.Blocks * bs
	used = (st.Blocks - st.Bfree) * bs
	return size, used
}
