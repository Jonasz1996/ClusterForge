//go:build unix

package agent

import (
	"os"
	"syscall"
)

// aivChown geeft path dezelfde eigenaar als app.py.
func aivChown(path string, like os.FileInfo) {
	if st, ok := like.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(path, int(st.Uid), int(st.Gid))
	}
}
