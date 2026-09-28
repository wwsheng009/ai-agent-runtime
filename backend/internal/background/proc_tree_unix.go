//go:build unix

package background

import (
	"fmt"
	"os/exec"
	"syscall"
)

// processTree tracks the process group of a job. Jobs are spawned with
// Setpgid (pgid == root pid) so the whole tree shares one group and can be
// signalled as a unit (2026-09-28, P2).
type processTree struct {
	pgid int
}

func attachProcessTree(pid int, killOnClose bool) (*processTree, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("invalid pid %d", pid)
	}
	return &processTree{pgid: pid}, nil
}

// Terminate signals the whole process group, escalating from TERM to KILL.
func (t *processTree) Terminate() error {
	if t == nil || t.pgid <= 0 {
		return nil
	}
	script := fmt.Sprintf("(kill -TERM -%d 2>/dev/null || kill -TERM %d 2>/dev/null || true); sleep 0.2; (kill -KILL -%d 2>/dev/null || kill -KILL %d 2>/dev/null || true)", t.pgid, t.pgid, t.pgid, t.pgid)
	return exec.Command("/bin/sh", "-c", script).Run()
}

func (t *processTree) Release() {}

// GroupID reports the process group id (the root pid).
func (t *processTree) GroupID() int {
	if t == nil {
		return 0
	}
	return t.pgid
}

// applyProcessGroup starts the child in its own process group so Terminate can
// signal the whole tree.
func applyProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
