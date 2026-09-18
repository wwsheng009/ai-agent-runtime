//go:build !windows

package executor

import (
	"os"
	"os/exec"
	"syscall"
)

// platformGuard tracks a Unix command tree via its own process group: the shell
// is started with Setpgid so every descendant stays in that group and can be
// killed with kill(-pgid).
type platformGuard struct {
	group bool
}

func newPlatformGuard() platformGuard {
	return platformGuard{}
}

func (p *platformGuard) bind(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	p.group = true
	return nil
}

func (p *platformGuard) attach(*os.Process) error { return nil }

func (p *platformGuard) terminate(pid int) TerminationReport {
	if pid <= 0 {
		return TerminationReport{}
	}
	rep := TerminationReport{}
	if p.group {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
			rep.TreeKill = true
			rep.Mode = "process_group"
			rep.Killed = appendPIDUnique(rep.Killed, pid)
			return rep
		} else {
			rep.Err = err.Error()
		}
	}
	if proc, err := os.FindProcess(pid); err == nil {
		if killErr := proc.Kill(); killErr == nil {
			rep.TreeKill = true
			rep.Mode = "direct_kill"
			rep.Killed = appendPIDUnique(rep.Killed, pid)
			return rep
		}
	}
	return rep
}

func (p *platformGuard) livePIDs() []int { return nil }

func (p *platformGuard) close(bool) {}
