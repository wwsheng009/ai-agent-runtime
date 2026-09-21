//go:build windows

package knowledge

import (
	"errors"
	"syscall"
)

// errInvalidParameter 是 Windows 上"PID 不存在"的 errno（ERROR_INVALID_PARAMETER）。
//
// 不引用 syscall.ERROR_INVALID_PARAMETER：该常量在各 Go 版本中的可见性不稳定，
// 而 87 是 Win32 的稳定契约。
const errInvalidParameter syscall.Errno = 87

// processQueryLimitedInformation 是 PROCESS_QUERY_LIMITED_INFORMATION 的数值
// （Win32 稳定契约）。syscall 包不导出该常量，x/sys/windows 才导出——为了不引入
// 新依赖，这里使用数值。
const processQueryLimitedInformation = 0x1000

// processAlive 报告 pid 是否仍存活。
//
// 保守策略：只有"PID 不存在"才判定为死亡；拒绝访问等其他错误一律视为存活，
// 因为误判存活只会让本进程降级为 reader，而误判死亡会破坏单写者不变量。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err == nil {
		_ = syscall.CloseHandle(handle)
		return true
	}
	return !errors.Is(err, errInvalidParameter)
}
