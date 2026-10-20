package observe

import (
	"os"
	"syscall"
)

func fileInode(st os.FileInfo) uint64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return s.Ino
	}
	return 0
}
