//go:build windows

package mesh

import (
	"errors"
	"syscall"
)

// errInvalidParameter is Windows' "no such process" errno (ERROR_INVALID_PARAMETER).
//
// It is spelled out instead of using syscall.ERROR_INVALID_PARAMETER because
// that constant's visibility has been unstable across Go releases, while 87 is
// a stable Win32 contract.
const errInvalidParameter syscall.Errno = 87

// processQueryLimitedInformation is PROCESS_QUERY_LIMITED_INFORMATION (0x1000).
// The syscall package does not export it (x/sys/windows does); internal/mesh
// stays standard-library only on purpose, so the value is written out.
const processQueryLimitedInformation = 0x1000

// processAlive reports whether pid is still running.
//
// Conservative on purpose (same policy as internal/knowledge): only "PID does
// not exist" counts as dead. Access-denied and every other error are treated as
// alive, because a false "alive" merely makes the reader wait for the TTL,
// while a false "dead" would let two processes believe they own one session.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err == nil {
		_ = syscall.CloseHandle(handle)
		return true
	}
	return !errors.Is(err, errInvalidParameter)
}
