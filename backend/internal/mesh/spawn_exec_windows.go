//go:build windows

package mesh

import "syscall"

// Windows process-creation flags for a detached child. Written out because
// internal/mesh stays standard-library only (x/sys/windows exports them):
//
//	DETACHED_PROCESS         0x00000008  no console at all
//	CREATE_NEW_PROCESS_GROUP 0x00000200  its own group (Ctrl+C never reaches us)
//
// The pair is the documented combination (DETACHED_PROCESS cannot be combined
// with CREATE_NEW_CONSOLE, but it can with CREATE_NEW_PROCESS_GROUP).
const (
	createDetachedProcess = 0x00000008
	createNewProcessGroup = 0x00000200
)

// spawnSysProcAttr detaches the spawned node from the requesting process.
func spawnSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createDetachedProcess | createNewProcessGroup,
	}
}
