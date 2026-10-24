//go:build windows

package panel

import "os"

// Windows 上没有 systemd、也没有 uid/gid：判断不了就返回 false，
// 调用方据此不下结论（doctor 的那条检查只对 Linux 落地机有意义）。
func fileOwner(os.FileInfo) (int, int, bool) { return 0, 0, false }
