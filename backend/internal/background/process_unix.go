//go:build !windows && !linux

package background

import (
	"os"
	"syscall"
)

type processHealth struct {
	Running  bool
	Zombie   bool
	Identity string
}

func inspectProcess(pid int) processHealth {
	if pid <= 0 {
		return processHealth{}
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return processHealth{}
	}
	err = process.Signal(syscall.Signal(0))
	if err != nil && err != syscall.EPERM {
		return processHealth{}
	}
	return processHealth{Running: true}
}
