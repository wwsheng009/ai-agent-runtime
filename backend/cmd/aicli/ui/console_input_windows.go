//go:build windows

package ui

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 本文件为低版本 x/sys（Go 1.20 兼容的 v0.30 等）提供
// GetNumberOfConsoleInputEvents / KEY_EVENT 的等价实现。
// 这些符号直到 x/sys v0.40（要求 go 1.24）才加入，而 Windows 上
// kernel32.GetNumberOfConsoleInputEvents 自 Windows 2000 即存在。
// 实现与 x/sys 的 syscall 封装语义一致，双工具链行为无差异。

var procGetNumberOfConsoleInputEvents = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetNumberOfConsoleInputEvents")

// consoleKeyEventType 即 INPUT_RECORD 的 KEY_EVENT 事件类型（0x0001）。
const consoleKeyEventType uint16 = 0x0001

func getNumberOfConsoleInputEvents(handle windows.Handle, numEvents *uint32) error {
	ret, _, callErr := procGetNumberOfConsoleInputEvents.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(numEvents)),
	)
	if ret == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return windows.GetLastError()
	}
	return nil
}