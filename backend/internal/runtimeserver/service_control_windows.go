//go:build windows

package runtimeserver

import (
	"fmt"
	"golang.org/x/sys/windows"
)

// processRunningOS 用 Windows API OpenProcess 探测进程是否存在，
// 不依赖外部 PowerShell。此前用 powershell.exe -Command "if (Get-Process ...)"
// 检查：在无 PowerShell（或被裁剪/禁用）的 Win7 工控机上恒失败，导致
// start 命令在 serve 进程已写好 PID 文件的正常启动情况下空转 30s，
// 误报"未写入 PID 文件"。
func processRunningOS(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Vista+ 语义：进程不存在返回 ERROR_INVALID_PARAMETER；
		// 其他错误（如 ERROR_ACCESS_DENIED）表示进程存在但权限不足，
		// 此时按"存在"处理，避免误判。
		return err != windows.ERROR_INVALID_PARAMETER
	}
	_ = windows.CloseHandle(h)
	return true
}

// processImagePathOS 返回进程可执行文件路径：
// QueryFullProcessImageNameW（Vista+，Win7 可用，PROCESS_QUERY_LIMITED_INFORMATION 权限）。
func processImagePathOS(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid pid %d", pid)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var buf [1024]uint16
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}
