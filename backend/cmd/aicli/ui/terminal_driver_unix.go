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

// platformEnsureConsoleUTF8Output 在非 Windows 平台为空操作：控制台本身
// 以 UTF-8 为原生编码，无需切换代码页。
func platformEnsureConsoleUTF8Output(stdout *os.File) func() { return nil }
