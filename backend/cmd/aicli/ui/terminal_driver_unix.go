//go:build !windows

package ui

import "os"

func platformTerminalSupportsANSI(stdout *os.File) bool {
	if stdout == nil {
		return false
	}
	termName := os.Getenv("TERM")
	if termName == "" || termName == "dumb" {
		return false
	}
	return true
}

func platformEnableVirtualTerminalProcessing(stdout *os.File) bool {
	return stdout != nil
}
