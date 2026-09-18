package executor

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultProcessWaitDelay bounds how long Wait may block after the command
	// process exits (or its context is canceled) while stdout/stderr pipes are
	// still held open by surviving descendants (e.g. a daemon started by the
	// command, or a grandchild that inherited the pipe). Without it a tool call
	// can stay pending forever even though the shell itself already exited.
	DefaultProcessWaitDelay = 5 * time.Second

	processWaitDelayEnv  = "AICLI_SHELL_WAIT_DELAY"
	killOrphansOnExitEnv = "AICLI_SHELL_KILL_ORPHANS_ON_EXIT"
)

// TerminationReport describes how (and whether) a guarded command tree was
// terminated, plus any leftover descendants observed at close time.
type TerminationReport struct {
	TreeKill  bool   `json:"tree_kill"`
	Mode      string `json:"mode,omitempty"`
	Killed    []int  `json:"killed_pids,omitempty"`
	Leftovers []int  `json:"leftover_pids,omitempty"`
	AttachErr string `json:"attach_error,omitempty"`
	Err       string `json:"error,omitempty"`
}

// ProcessGuard tracks a started command process together with its descendants.
//
// It provides three properties the plain exec.CommandContext path lacks:
//   - WaitDelay: Wait can no longer block forever on pipes kept open by
//     descendants after the direct child exited.
//   - tree termination on timeout/cancel (Windows Job Object, Unix process
//     group) instead of killing only the direct child.
//   - leftover-descendant visibility so the runtime can report/reap orphans
//     that outlive the command.
type ProcessGuard struct {
	mu        sync.Mutex
	waitDelay time.Duration
	platform  platformGuard
	pid       int
	report    TerminationReport
	closed    bool
}

// NewProcessGuard creates a guard. Bind must be called before the command is
// started; Attach right after Start.
func NewProcessGuard() *ProcessGuard {
	return &ProcessGuard{
		waitDelay: ResolveProcessWaitDelay(),
		platform:  newPlatformGuard(),
	}
}

// ResolveProcessWaitDelay returns the bounded post-exit wait for command
// processes. AICLI_SHELL_WAIT_DELAY accepts a Go duration (e.g. "5s", "1500ms");
// zero or negative disables the bound (not recommended).
func ResolveProcessWaitDelay() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(processWaitDelayEnv)); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			if parsed <= 0 {
				return 0
			}
			return parsed
		}
	}
	return DefaultProcessWaitDelay
}

func resolveKillOrphansOnExit() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(killOrphansOnExitEnv)))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// Bind configures cmd before Start: bounded Wait plus platform-specific
// process-tree tracking (Windows: Job Object; Unix: process group).
func (g *ProcessGuard) Bind(cmd *exec.Cmd) error {
	if g == nil || cmd == nil {
		return fmt.Errorf("process guard: nil command")
	}
	if g.waitDelay > 0 {
		cmd.WaitDelay = g.waitDelay
	}
	return g.platform.bind(cmd)
}

// Attach registers the started process with the platform process-tree handle.
// Errors are recorded but non-fatal: termination then degrades to a fallback
// (taskkill / direct kill) instead of failing the command.
func (g *ProcessGuard) Attach(proc *os.Process) error {
	if g == nil || proc == nil {
		return nil
	}
	g.mu.Lock()
	g.pid = proc.Pid
	g.mu.Unlock()
	if err := g.platform.attach(proc); err != nil {
		g.NoteAttachError(err)
		return err
	}
	return nil
}

// NoteAttachError records a degraded tree-tracking attachment.
func (g *ProcessGuard) NoteAttachError(err error) {
	if g == nil || err == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.report.AttachErr == "" {
		g.report.AttachErr = err.Error()
	}
}

// Terminate kills the whole process tree. Safe to call multiple times and from
// concurrent goroutines (e.g. context watchdog plus interrupt path).
func (g *ProcessGuard) Terminate() TerminationReport {
	if g == nil {
		return TerminationReport{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.report.TreeKill {
		return g.report
	}
	rep := g.platform.terminate(g.pid)
	if rep.AttachErr == "" {
		rep.AttachErr = g.report.AttachErr
	}
	g.report = rep
	return g.report
}

// Leftovers returns live descendant PIDs still tracked at call time, excluding
// the command root process.
func (g *ProcessGuard) Leftovers() []int {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.leftoverPIDsLocked()
}

// leftoverPIDsLocked must be called with g.mu held: it queries the platform
// process-tree handle, which is also touched by Terminate/Close.
func (g *ProcessGuard) leftoverPIDsLocked() []int {
	pids := g.platform.livePIDs()
	out := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid > 0 && pid != g.pid {
			out = append(out, pid)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Report returns the current termination report.
func (g *ProcessGuard) Report() TerminationReport {
	if g == nil {
		return TerminationReport{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	rep := g.report
	rep.Killed = append([]int(nil), g.report.Killed...)
	return rep
}

// WaitDelay returns the configured bounded post-exit wait.
func (g *ProcessGuard) WaitDelay() time.Duration {
	if g == nil {
		return 0
	}
	return g.waitDelay
}

// PID returns the tracked root process id (0 before Attach).
func (g *ProcessGuard) PID() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pid
}

// Close releases platform resources. By default surviving descendants are left
// running (they are only reported); set AICLI_SHELL_KILL_ORPHANS_ON_EXIT=1 to
// terminate leftovers at close time as well.
func (g *ProcessGuard) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.closed = true

	killOrphans := resolveKillOrphansOnExit()
	leftovers := g.leftoverPIDsLocked()
	if killOrphans && len(leftovers) > 0 {
		g.platform.terminate(g.pid)
	}
	g.report.Leftovers = leftovers
	// Preserve descendants that intentionally outlive the command unless the
	// caller opted into orphan cleanup.
	g.platform.close(!killOrphans)
}

func appendPIDUnique(pids []int, pid int) []int {
	if pid <= 0 {
		return pids
	}
	for _, existing := range pids {
		if existing == pid {
			return pids
		}
	}
	return append(pids, pid)
}
