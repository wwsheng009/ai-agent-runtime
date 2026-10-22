//go:build !windows

package remotetools

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup starts the command in its own process group so a timeout
// can terminate the whole tree.
//
// Without Setpgid a shell command that spawns children leaves them running after
// the parent is killed; on a shared target that turns a timed-out build into an
// orphan that keeps consuming CPU.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup terminates the whole process group of cmd.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Negative pid targets the group created by Setpgid.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// terminateProcessGroup asks the whole group to stop, giving it a chance to
// flush state before killProcessGroup escalates.
func terminateProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}
