//go:build !windows

package mesh

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// processAlive reports whether pid is still running.
//
// signal 0 sends nothing and only performs the existence/permission check:
// EPERM means the process exists but belongs to another user (alive), ESRCH
// means it does not exist (dead). A zombie has already exited and only lingers
// until its parent reaps it: it still answers signal 0, but it is no longer a
// running process, so it counts as dead here.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if processZombie(pid) {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processZombie reports whether pid is a zombie. Linux exposes the state in
// /proc/<pid>/stat; without /proc (or when that file cannot be read) this
// conservatively reports false and processAlive keeps its signal-0 semantics.
func processZombie(pid int) bool {
	text, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	idx := strings.LastIndexByte(string(text), ')')
	if idx < 0 || idx+2 >= len(text) {
		return false
	}
	state := text[idx+2]
	return state == 'Z' || state == 'X'
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
