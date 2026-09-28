//go:build windows

package background

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processTree groups a job's whole process tree in a Windows Job Object so the
// runner and every child it spawns can be terminated as one unit (2026-09-28,
// P2: the 8791-port orphans came from tree members surviving a single-pid
// kill).
//
// killOnClose is false for detached jobs on purpose: they must survive the
// runtime process that launched them, because another instance adopts them
// after a restart. Direct-exec jobs are strict children of the manager and use
// kill-on-close so nothing can outlive the manager unnoticed.
type processTree struct {
	job  windows.Handle
	root int
}

func attachProcessTree(pid int, killOnClose bool) (*processTree, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("invalid pid %d", pid)
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	if killOnClose {
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			_ = windows.CloseHandle(job)
			return nil, fmt.Errorf("configure job object: %w", err)
		}
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("open process %d for job assignment: %w", pid, err)
	}
	err = windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("assign process %d to job object: %w", pid, err)
	}
	return &processTree{job: job, root: pid}, nil
}

// Terminate kills every process still in the tree.
func (t *processTree) Terminate() error {
	if t == nil || t.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(t.job, 1)
}

// Release drops the job handle. Detached trees keep running (no kill-on-close
// limit); the handle is only our kill lever while this instance is alive.
func (t *processTree) Release() {
	if t == nil || t.job == 0 {
		return
	}
	_ = windows.CloseHandle(t.job)
	t.job = 0
}

// GroupID reports the tree identity used for observability (the root pid).
func (t *processTree) GroupID() int {
	if t == nil {
		return 0
	}
	return t.root
}

// applyProcessGroup is a no-op on Windows: tree containment happens through the
// Job Object right after Start (see attachProcessTree).
func applyProcessGroup(cmd *exec.Cmd) {}
