//go:build windows

package background

import (
	"fmt"

	"golang.org/x/sys/windows"
)

type processHealth struct {
	Running  bool
	Zombie   bool
	Identity string
}

// inspectProcess uses the Windows process API instead of spawning PowerShell
// for every watchdog tick. The creation time is persisted to prevent PID reuse
// from being mistaken for the original detached job.
func inspectProcess(pid int) processHealth {
	if pid <= 0 {
		return processHealth{}
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return processHealth{}
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	const stillActive = 259
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil || exitCode != stillActive {
		return processHealth{}
	}

	var creation, exit, kernel, user windows.Filetime
	identity := ""
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err == nil {
		identity = fmt.Sprintf("%d:%d", creation.HighDateTime, creation.LowDateTime)
	}
	return processHealth{Running: true, Identity: identity}
}
