//go:build windows

package commands

import (
	"errors"

	"golang.org/x/sys/windows"
)

func localChatProcessRunning(pid int) (running bool, known bool) {
	if pid <= 0 {
		return false, true
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, true
		}
		return false, false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false, false
	}
	const stillActive = 259
	return exitCode == stillActive, true
}
