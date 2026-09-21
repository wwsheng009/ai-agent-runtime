//go:build windows

package transport

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// windowsStillActive 是 GetExitCodeProcess 对「进程尚未退出」的返回值 (STILL_ACTIVE)。
const windowsStillActive = 259

// processExitStatus 探测进程是否已退出及退出码。
//
// 这里刻意不调用 cmd.Wait（那是 SDK 的职责，重复 Wait 会报错），而是用
// OpenProcess + GetExitCodeProcess 做无副作用探测：
//   - known=false 表示无法判断（权限不足等）；
//   - exited=true && hasCode=false 表示进程对象已回收、退出码不可得；
//   - exited=true && hasCode=true 时可用于诊断「启动即失败」还是「握手超时」。
func processExitStatus(pid int) (exited bool, code int, hasCode bool, known bool) {
	if pid <= 0 {
		return false, 0, false, false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// ERROR_INVALID_PARAMETER：进程对象已消失（已退出且句柄全部关闭）。
		if errors.Is(err, syscall.Errno(windows.ERROR_INVALID_PARAMETER)) {
			return true, 0, false, true
		}
		return false, 0, false, false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false, 0, false, false
	}
	if exitCode == windowsStillActive {
		return false, 0, false, true
	}
	return true, int(exitCode), true, true
}
