package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DetachAllowedEnv is the runtime-wide kill switch for detached process
// launches (shell tool `detach=true`). Unset/empty means enabled: the launch is
// already opt-in per call, and the model-visible result always reports the PID.
// Set to 0/false/off/no/disabled to refuse detached launches (locked-down
// environments that require every child to stay inside the per-command job).
const DetachAllowedEnv = "AICLI_SHELL_ALLOW_DETACH"

// Detach isolation modes reported in DetachedProcess.Mode.
const (
	// DetachModeNewConsole is a Windows launch that owns a brand-new console
	// (real TTY handles for TUIs, no inheritance of the runtime's pipes).
	DetachModeNewConsole = "new_console"
	// DetachModeSetsid is a Unix launch in its own session with the null
	// device as stdio.
	DetachModeSetsid = "setsid"
)

// DetachedLaunch describes one fire-and-forget process launch. The process is
// started outside the per-command Job Object and without inheriting the
// runtime's output pipes, so it can outlive the tool call (daemons, a second
// interactive TUI, long-running helpers).
type DetachedLaunch struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

// DetachedProcess reports the identity of a launched detached process.
type DetachedProcess struct {
	PID int
	// Mode is the platform isolation used for the launch:
	// windows: "new_console"; unix: "setsid".
	Mode string
	// Breakaway reports whether CREATE_BREAKAWAY_FROM_JOB was honored
	// (Windows only). When false the process was still created outside the
	// per-command job, but an outer job object may still track it.
	Breakaway bool
	// Warning carries non-fatal launch degradations.
	Warning string
}

// ResolveDetachAllowed reports whether detached launches are enabled.
func ResolveDetachAllowed() bool {
	raw := strings.TrimSpace(os.Getenv(DetachAllowedEnv))
	if raw == "" {
		return true
	}
	switch strings.ToLower(raw) {
	case "0", "false", "off", "no", "disabled":
		return false
	}
	return true
}

// StartDetached launches req as an independent process and returns its PID
// immediately. The child never shares the caller's stdout/stderr pipes: a TUI
// needs its own console, and a daemon must not hold a tool call open.
func StartDetached(req DetachedLaunch) (*DetachedProcess, error) {
	path := strings.TrimSpace(req.Path)
	if path == "" {
		return nil, fmt.Errorf("detach: empty executable path")
	}
	if resolved, err := exec.LookPath(path); err == nil {
		path = resolved
	} else if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("detach: executable not found: %s", req.Path)
	}
	req.Path = path
	proc, err := startDetached(req)
	if err != nil {
		return nil, err
	}
	if proc == nil {
		return nil, fmt.Errorf("detach: launcher returned no process")
	}
	if proc.PID <= 0 {
		return nil, fmt.Errorf("detach: launcher returned invalid pid %d", proc.PID)
	}
	return proc, nil
}
