//go:build windows

package executor

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// platformGuard tracks a Windows command tree with a Job Object. The job is
// created with KILL_ON_JOB_CLOSE so that even a crashed runtime tears the tree
// down, and TerminateJobObject kills every descendant in one call.
type platformGuard struct {
	job      windows.Handle
	assigned bool
	degraded bool
}

type jobProcessIDList struct {
	NumberOfAssignedProcesses uint32
	NumberOfProcessIdsInList  uint32
	ProcessIdList             [1]uintptr
}

func newPlatformGuard() platformGuard {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return platformGuard{degraded: true}
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	// KILL_ON_JOB_CLOSE guarantees crash-safe teardown. BREAKAWAY_OK is the
	// controlled escape hatch: a descendant still stays in the job by default,
	// but a launch that explicitly passes CREATE_BREAKAWAY_FROM_JOB (see
	// DetachedLaunch) may leave it on purpose instead of being captured.
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
		windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return platformGuard{degraded: true}
	}
	return platformGuard{job: job}
}

func (p *platformGuard) bind(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// A dedicated process group keeps our tree isolated from the runtime
	// console; termination itself goes through the job object.
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	return nil
}

func (p *platformGuard) attach(proc *os.Process) error {
	if p.job == 0 || proc == nil {
		return nil
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(proc.Pid),
	)
	if err != nil {
		return fmt.Errorf("job object: open process %d: %w", proc.Pid, err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(p.job, handle); err != nil {
		return fmt.Errorf("job object: assign process %d: %w", proc.Pid, err)
	}
	p.assigned = true
	return nil
}

func (p *platformGuard) terminate(pid int) TerminationReport {
	rep := TerminationReport{}
	if p.job != 0 && p.assigned {
		rep.Killed = p.livePIDs()
		if err := windows.TerminateJobObject(p.job, 1); err == nil {
			rep.TreeKill = true
			rep.Mode = "job_object"
			return rep
		} else {
			rep.Err = err.Error()
		}
	}
	return terminateWindowsFallback(pid, rep)
}

// terminateWindowsFallback kills the tree via taskkill, then the direct child.
func terminateWindowsFallback(pid int, rep TerminationReport) TerminationReport {
	if pid <= 0 {
		return rep
	}
	if out, err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).CombinedOutput(); err == nil {
		rep.TreeKill = true
		rep.Mode = "taskkill"
		rep.Killed = appendPIDUnique(rep.Killed, pid)
		return rep
	} else {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		rep.Err = strings.TrimSpace(strings.Join([]string{rep.Err, detail}, "; "))
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

func (p *platformGuard) livePIDs() []int {
	if p.job == 0 {
		return nil
	}
	buf := make([]byte, 4096)
	var ret uint32
	if err := windows.QueryInformationJobObject(
		p.job,
		windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&buf[0])),
		uint32(len(buf)),
		&ret,
	); err != nil {
		return nil
	}
	list := (*jobProcessIDList)(unsafe.Pointer(&buf[0]))
	count := int(list.NumberOfProcessIdsInList)
	if count <= 0 {
		return nil
	}
	header := uintptr(8) // 2 * uint32
	entrySize := unsafe.Sizeof(uintptr(0))
	max := (len(buf) - int(header)) / int(entrySize)
	if count > max {
		count = max
	}
	pids := make([]int, 0, count)
	for i := 0; i < count; i++ {
		pid := *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(&buf[0])) + header + uintptr(i)*entrySize))
		if pid > 0 {
			pids = append(pids, int(pid))
		}
	}
	return pids
}

func (p *platformGuard) close(preserveLeftovers bool) {
	if p.job == 0 {
		return
	}
	if preserveLeftovers {
		// Clear KILL_ON_JOB_CLOSE so descendants that intentionally outlive the
		// command (daemons the user asked for) are not killed at Close.
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = 0
		_, _ = windows.SetInformationJobObject(
			p.job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		)
	}
	windows.CloseHandle(p.job)
	p.job = 0
	p.assigned = false
}
