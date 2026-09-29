//go:build windows

package background

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// startDetachedProcess starts exe in its own process group without a console
// window and returns the child PID immediately. It intentionally never waits:
// the detached runner must outlive this runtime process.
//
// The previous implementation launched a Windows PowerShell wrapper per job
// (`Start-Process ... -PassThru` and waited for the wrapper's stdout), which
// cost 8-14s per job on a loaded machine. That launch latency fed the
// scheduler watchdog's "scheduler stuck" misdiagnosis (2026-09-29).
func startDetachedProcess(exe string, args []string) (int, error) {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	// Release the process handle without reaping so the runner keeps running
	// after this runtime process exits.
	_ = cmd.Process.Release()
	return pid, nil
}
