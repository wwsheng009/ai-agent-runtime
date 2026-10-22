//go:build windows

package remotetools

import (
	"os/exec"
	"strconv"
	"syscall"
)

// configureProcessGroup creates a new process group on Windows as well, so the
// timeout path can kill the tree instead of only the shell.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// killProcessGroup terminates the process tree of cmd.
//
// taskkill /T is used instead of Process.Kill because the shell's children are
// not guaranteed to be in a Windows Job Object here (P2 targets Linux; this path
// exists so a Windows target does not silently leak children).
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	_ = exec.Command("taskkill", "/T", "/F", "/PID", pid).Run()
}

// terminateProcessGroup on Windows cannot deliver a graceful signal to a console
// process group without a shared console, so it falls back to the same tree kill.
// A graceful stop is a POSIX-only guarantee in P3.
func terminateProcessGroup(cmd *exec.Cmd) {
	killProcessGroup(cmd)
}
