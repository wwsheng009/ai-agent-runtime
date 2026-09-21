//go:build !windows

package transport

import (
	"errors"
	"syscall"
)

// processExitStatus 在非 Windows 平台用信号 0 探测进程存活：
// ESRCH 表示进程已不存在。退出码无法在不 reap 的前提下获得，故 hasCode=false。
func processExitStatus(pid int) (exited bool, code int, hasCode bool, known bool) {
	if pid <= 0 {
		return false, 0, false, false
	}
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return false, 0, false, true
	case errors.Is(err, syscall.ESRCH):
		return true, 0, false, true
	case errors.Is(err, syscall.EPERM):
		// 进程存在但无权限发信号。
		return false, 0, false, true
	default:
		return false, 0, false, false
	}
}
