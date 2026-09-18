//go:build !windows

package executor

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// startDetached launches the process in a new session with the null device as
// stdin/stdout/stderr. Setsid makes the child independent of the tool call's
// process group, and the null device guarantees it never holds the runtime's
// output pipes open.
func startDetached(req DetachedLaunch) (*DetachedProcess, error) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("detach: open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	argv := append([]string{req.Path}, req.Args...)
	proc, err := os.StartProcess(req.Path, argv, &os.ProcAttr{
		Dir:   strings.TrimSpace(req.Dir),
		Env:   req.Env,
		Files: []*os.File{devNull, devNull, devNull},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("detach: start %s: %w", req.Path, err)
	}
	pid := proc.Pid
	if err := proc.Release(); err != nil {
		return nil, fmt.Errorf("detach: release %d: %w", pid, err)
	}
	return &DetachedProcess{PID: pid, Mode: DetachModeSetsid, Breakaway: true}, nil
}
