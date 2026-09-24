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
