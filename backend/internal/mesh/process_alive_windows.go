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
//
// Windows keeps the process object — and therefore the PID — alive as long as
// any handle to it is open, so OpenProcess keeps succeeding for a process that
// has already exited. Parents that start a child and hold on to it (PowerShell
// Start-Process, os.Process, the S9 spawn path, supervising scripts) are the
// normal case, not an edge case: without the exit-code check below a killed
// chat process stayed `live` forever, so the view never flipped to `stale` and
// gc could not reclaim it (E2E-DEBUG-03 M6/M9 caught exactly that).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return !errors.Is(err, errInvalidParameter)
	}
	defer func() { _ = syscall.CloseHandle(handle) }()
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		// 退出码都问不到：按保守口径当活（宁可让读者等 TTL）。
		return true
	}
	return code == stillActive
}

// stillActive 是 STILL_ACTIVE（STATUS_PENDING，259）：GetExitCodeProcess 对
// 「尚未退出」的进程返回该值；其余值（含 0）都说明进程已经结束。
const stillActive = 259

// processTerminate is PROCESS_TERMINATE (0x0001): the right to call
// TerminateProcess on the handle (same reason as processQueryLimitedInformation,
// the value is written out instead of pulled from x/sys/windows).
const processTerminate = 0x0001

// terminateProcess 强制结束 pid（Stop --force 的唯一系统调用）。
//
// 「进程已经不在」不是错误：OpenProcess/TerminateProcess 回
// ERROR_INVALID_PARAMETER 时按成功处理——目标是幂等的，调用方只关心它最终
// 不在运行。其余错误（访问被拒等）如实返回，由调用方决定是否回 error。
func terminateProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	handle, err := syscall.OpenProcess(processTerminate, false, uint32(pid))
	if err != nil {
		if errors.Is(err, errInvalidParameter) {
			return nil
		}
		return err
	}
	defer func() { _ = syscall.CloseHandle(handle) }()
	if err := syscall.TerminateProcess(handle, 1); err != nil {
		if errors.Is(err, errInvalidParameter) {
			return nil
		}
		return err
	}
	return nil
}
