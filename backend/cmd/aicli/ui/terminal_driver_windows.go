//go:build windows

package ui

import (
	"os"

	"golang.org/x/sys/windows"
)

const (
	enableProcessedOutput           uint32 = 0x0001
	enableVirtualTerminalProcessing uint32 = 0x0004
)

func platformTerminalSupportsANSI(stdout *os.File) bool {
	if stdout == nil {
		return false
	}
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(stdout.Fd()), &mode) == nil
}

func platformEnableVirtualTerminalProcessing(stdout *os.File) bool {
	if stdout == nil {
		return false
	}
	handle := windows.Handle(stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return false
	}
	next := mode | enableProcessedOutput | enableVirtualTerminalProcessing
	if next == mode {
		return true
	}
	return windows.SetConsoleMode(handle, next) == nil
}
