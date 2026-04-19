//go:build !windows

package runtimeserver

import (
	"os/exec"
	"syscall"
)

func applyDetachedProcessAttrs(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
}
