//go:build windows

package winconsole

import (
	"os"

	"golang.org/x/sys/windows"
)

const (
	enableVirtualTerminalProcessing uint32 = 0x0004
	codePageUTF8                    uint32 = 65001
)

// EnsureConsoleUTF8Output 在无 VT 的 Windows 控制台（Win7 及更早 conhost 不支持
// ENABLE_VIRTUAL_TERMINAL_PROCESSING）上，把控制台输出代码页切到 65001(UTF-8)。
// 支持 VT 的控制台、管道/文件重定向、非 Windows 平台均为空操作。
// 返回值：仅在确实切换过代码页时非 nil；调用者应 defer 它在进程正常退出时恢复
// 原代码页，避免污染同一 console 上后续命令的显示。
func EnsureConsoleUTF8Output() func() {
	stdout := os.Stdout
	if stdout == nil {
		return nil
	}
	handle := windows.Handle(stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return nil // 非 console（重定向/管道），不改全局代码页
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return nil // VT 控制台已按 UTF-8 处理输出流
	}
	original, err := windows.GetConsoleOutputCP()
	if err != nil || original == codePageUTF8 {
		return nil
	}
	if err := windows.SetConsoleOutputCP(codePageUTF8); err != nil {
		return nil
	}
	return func() { _ = windows.SetConsoleOutputCP(original) }
}