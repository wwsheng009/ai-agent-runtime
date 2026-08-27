//go:build windows

package consolehost

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformSupported() bool {
	return true
}

func platformHasConsole() bool {
	return isConsoleHandle(os.Stdin) && isConsoleHandle(os.Stdout)
}

func isConsoleHandle(file *os.File) bool {
	if file == nil {
		return false
	}
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(file.Fd()), &mode) == nil
}

// platformRunInNewConsole deliberately calls CreateProcessW directly instead
// of os/exec with SysProcAttr.CreationFlags. Go's Windows os.StartProcess path
// always sets STARTF_USESTDHANDLES; that would pass the MobaXterm/Cygwin pipe
// (or NUL) into the child and defeat CREATE_NEW_CONSOLE. Leaving
// STARTF_USESTDHANDLES clear lets Windows initialize stdin/stdout/stderr from
// the new console's CONIN$/CONOUT$ handles, including on Windows 7.
func platformRunInNewConsole(executable string, args []string) (int, error) {
	appName, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return 1, fmt.Errorf("encode executable path: %w", err)
	}
	commandLine, err := windows.UTF16PtrFromString(buildWindowsCommandLine(executable, args))
	if err != nil {
		return 1, fmt.Errorf("encode command line: %w", err)
	}

	startupInfo := &windows.StartupInfo{
		Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})),
	}
	var processInfo windows.ProcessInformation
	if err := windows.CreateProcess(
		appName,
		commandLine,
		nil,
		nil,
		false, // do not inherit the parent PTY/pipe handles
		windows.CREATE_NEW_CONSOLE|windows.CREATE_UNICODE_ENVIRONMENT,
		nil, // inherit the complete parent environment
		nil, // inherit the current working directory
		startupInfo,
		&processInfo,
	); err != nil {
		return 1, err
	}
	defer windows.CloseHandle(processInfo.Process)
	defer windows.CloseHandle(processInfo.Thread)

	waitResult, err := windows.WaitForSingleObject(processInfo.Process, windows.INFINITE)
	if err != nil {
		return 1, fmt.Errorf("wait for console process: %w", err)
	}
	if waitResult != windows.WAIT_OBJECT_0 {
		return 1, fmt.Errorf("wait for console process returned status 0x%x", waitResult)
	}

	var exitCode uint32
	if err := windows.GetExitCodeProcess(processInfo.Process, &exitCode); err != nil {
		return 1, fmt.Errorf("read console process exit code: %w", err)
	}
	return int(exitCode), nil
}

func buildWindowsCommandLine(executable string, args []string) string {
	escaped := make([]string, 0, len(args)+1)
	escaped = append(escaped, syscall.EscapeArg(executable))
	for _, arg := range args {
		escaped = append(escaped, syscall.EscapeArg(arg))
	}
	return strings.Join(escaped, " ")
}
