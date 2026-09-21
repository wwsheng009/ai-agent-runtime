//go:build !windows

package knowledge

import (
	"errors"
	"syscall"
)

// processAlive 报告 pid 是否仍存活。
//
// signal 0 不发送信号，只做权限与存在性检查：EPERM 表示进程存在但不属于
// 当前用户（视为存活），ESRCH 才表示进程不存在。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
