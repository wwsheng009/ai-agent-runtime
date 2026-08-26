//go:build windows

package ui

import (
	"os"

	"golang.org/x/sys/windows"
)

const (
	enableProcessedOutput           uint32 = 0x0001
	enableVirtualTerminalProcessing uint32 = 0x0004
	codePageUTF8                    uint32 = 65001
)

// platformEnsureConsoleUTF8Output 在无 VT 处理的 Windows 控制台（Win7 及更早
// 的 conhost 不支持 ENABLE_VIRTUAL_TERMINAL_PROCESSING）上，把控制台输出
// 代码页切到 65001(UTF-8)。Go 程序始终以 UTF-8 字节写 stdout，conhost 默认
// 按 OEM 代码页（中文系统为 936/GBK）解码，无 VT 时所有中文输出都会显示为
// 乱码。支持 VT 的控制台（Win10 1607+）在 VT 模式下由 conhost 按 UTF-8
// 解释输出流，无需切换；stdout 重定向到文件/管道时也不应改动全局代码页。
// 返回恢复函数：仅在确实切换过代码页时非 nil，调用者可 defer 在退出时
// 恢复原代码页，避免污染同一 console 上后续命令的显示。
func platformEnsureConsoleUTF8Output(stdout *os.File) func() {
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
