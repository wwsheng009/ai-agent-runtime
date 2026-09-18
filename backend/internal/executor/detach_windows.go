//go:build windows

package executor

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// startDetached creates the process in a new console via CreateProcessW without
// STARTF_USESTDHANDLES. Two properties matter:
//
//  1. The child gets the new console's real standard handles instead of the
//     runtime's captured output pipes. That keeps tool calls free of WaitDelay
//     stalls and lets interactive TUIs attach to a genuine terminal.
//  2. The process is never assigned to the per-command Job Object, so timeouts,
//     Esc cancels and job close cannot kill it. CREATE_BREAKAWAY_FROM_JOB is
//     attempted so the launch can also leave an outer job when that job allows
//     breakaway; a denied breakaway degrades to a plain new-console launch and
//     is reported in the result.
func startDetached(req DetachedLaunch) (*DetachedProcess, error) {
	appName, err := windows.UTF16PtrFromString(req.Path)
	if err != nil {
		return nil, fmt.Errorf("detach: encode executable path: %w", err)
	}
	cmdLine, err := windows.UTF16PtrFromString(buildWindowsCommandLine(req.Path, req.Args))
	if err != nil {
		return nil, fmt.Errorf("detach: encode command line: %w", err)
	}

	var dirPtr *uint16
	if strings.TrimSpace(req.Dir) != "" {
		if dirPtr, err = windows.UTF16PtrFromString(req.Dir); err != nil {
			return nil, fmt.Errorf("detach: encode workdir: %w", err)
		}
	}

	envPtr, envKeep, envErr := buildWindowsEnvBlock(req.Env)
	if envErr != nil {
		return nil, envErr
	}

	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	pi := windows.ProcessInformation{}

	flags := uint32(windows.CREATE_NEW_CONSOLE | windows.CREATE_BREAKAWAY_FROM_JOB)
	if envPtr != nil {
		// A UTF-16 environment block is only valid when CreateProcessW is told
		// about the encoding; without this flag Windows rejects it as an
		// invalid parameter instead of silently degrading.
		flags |= windows.CREATE_UNICODE_ENVIRONMENT
	}
	proc := &DetachedProcess{Mode: DetachModeNewConsole, Breakaway: true}
	err = windows.CreateProcess(appName, cmdLine, nil, nil, false, flags, envPtr, dirPtr, &si, &pi)
	if err != nil && isAccessDenied(err) {
		// An outer job without JOB_OBJECT_LIMIT_BREAKAWAY_OK rejects the
		// breakaway flag; the launch itself is still independent of the
		// per-command guard job, so degrade instead of failing.
		proc.Breakaway = false
		proc.Warning = "outer job does not allow breakaway: launched without CREATE_BREAKAWAY_FROM_JOB"
		err = windows.CreateProcess(appName, cmdLine, nil, nil, false,
			flags&^uint32(windows.CREATE_BREAKAWAY_FROM_JOB), envPtr, dirPtr, &si, &pi)
	}
	if err != nil {
		return nil, fmt.Errorf("detach: CreateProcess: %w", err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)

	runtime.KeepAlive(envKeep)
	proc.PID = int(pi.ProcessId)
	return proc, nil
}

func isAccessDenied(err error) bool {
	return err == windows.ERROR_ACCESS_DENIED || errorsIsAccessDenied(err)
}

// errorsIsAccessDenied keeps the check working when the error is wrapped.
func errorsIsAccessDenied(err error) bool {
	for err != nil {
		if err == windows.ERROR_ACCESS_DENIED {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// buildWindowsCommandLine renders argv the same way os/exec does.
func buildWindowsCommandLine(path string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, syscall.EscapeArg(path))
	for _, arg := range args {
		parts = append(parts, syscall.EscapeArg(arg))
	}
	return strings.Join(parts, " ")
}

// buildWindowsEnvBlock converts "KEY=VALUE" entries into a CreateProcessW
// environment block. Duplicate keys keep the last value (case-insensitive), the
// block is NUL-separated and double-NUL terminated. A nil block means "inherit
// the parent environment".
func buildWindowsEnvBlock(env []string) (*uint16, []uint16, error) {
	if len(env) == 0 {
		return nil, nil, nil
	}
	order := make([]string, 0, len(env))
	values := make(map[string]string, len(env))
	for _, entry := range env {
		idx := strings.IndexByte(entry, '=')
		if idx <= 0 {
			continue
		}
		key := strings.ToUpper(entry[:idx])
		if _, seen := values[key]; !seen {
			order = append(order, key)
		}
		values[key] = entry
	}
	block := make([]uint16, 0, len(env)*32)
	for _, key := range order {
		encoded, err := windows.UTF16FromString(values[key])
		if err != nil {
			return nil, nil, fmt.Errorf("detach: encode env %s: %w", key, err)
		}
		block = append(block, encoded...)
	}
	if len(block) == 0 {
		return nil, nil, nil
	}
	block = append(block, 0)
	return &block[0], block, nil
}
