//go:build !windows

package panel

import (
	"os"
	"syscall"
)

// fileOwner 取文件的属主 uid/gid。Windows 没有这套概念（见 owner_windows.go）。
func fileOwner(fi os.FileInfo) (int, int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
