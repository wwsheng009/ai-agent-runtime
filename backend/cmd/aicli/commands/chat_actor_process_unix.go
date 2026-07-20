//go:build !windows

package commands

import (
	"errors"
	"os"
	"syscall"
)

func localChatProcessRunning(pid int) (running bool, known bool) {
	if pid <= 0 {
		return false, true
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, false
	}
	err = process.Signal(syscall.Signal(0))
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, true
	case errors.Is(err, syscall.ESRCH):
		return false, true
	default:
		return false, false
	}
}
