//go:build windows

package commands

import (
	"errors"
	"fmt"
	"syscall"
)

// Windows 错误码常量（syscall 包未导出 ERROR_SHARING_VIOLATION/LOCK_VIOLATION）。
const (
	errnoSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
	errnoLockViolation    = syscall.Errno(33) // ERROR_LOCK_VIOLATION
)

// storageExclusiveProbe 以零共享方式打开主库文件：只要还有任何其它句柄
// （SQLite 的空闲连接也会持续持有主库句柄），CreateFile 就会失败。
func storageExclusiveProbe(path string) error {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("解析路径失败：%w", err)
	}
	handle, err := syscall.CreateFile(
		name,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, // 不共享：任何既有句柄都会让本次打开失败
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		if errors.Is(err, errnoSharingViolation) || errors.Is(err, errnoLockViolation) {
			return errStorageExclusiveBusy
		}
		return fmt.Errorf("独占探测打开失败：%w", err)
	}
	return syscall.CloseHandle(handle)
}
