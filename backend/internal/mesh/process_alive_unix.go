//go:build !windows

package mesh

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid is still running.
//
// signal 0 sends nothing and only performs the existence/permission check:
// EPERM means the process exists but belongs to another user (alive), ESRCH
// means it does not exist (dead).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// terminateProcess 强制结束 pid（Stop --force 的唯一系统调用）。
//
// SIGKILL 而不是 SIGTERM：force 的语义是「立刻停止」，graceful 已经由
// /exit 覆盖；进程不在（ESRCH）不是错误（目标是幂等的）。
func terminateProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
