//go:build windows

package agentconfig

import (
	"sync"

	"golang.org/x/sys/windows"
)

// 整文件范围的字节区间锁：LockFileEx/UnlockFileEx 的 0xFFFFFFFF/0xFFFFFFFF。
const (
	routingFileOSLockBytesLow  = 0xFFFFFFFF
	routingFileOSLockBytesHigh = 0xFFFFFFFF
)

// routingFileOSLockSupported 标记本平台有真正的跨进程锁实现（测试用来区分
// 「实现存在但被降级」与「平台本身没有实现」）。
const routingFileOSLockSupported = true

// acquireRoutingFileOSLock 在 Windows 上用 LockFileEx 取跨进程排他锁（整文件范围）。
//
// 阻塞语义的两个必要条件（缺一即退化成「立即失败 → 静默降级」或忙等）：
//   - 句柄以**同步**方式打开：CreateFile 不传 FILE_FLAG_OVERLAPPED（本实现传
//     FILE_ATTRIBUTE_NORMAL）。同步句柄上 LockFileEx 在锁被别的进程占用时阻塞等待，
//     而不是返回 ERROR_IO_PENDING / ERROR_LOCK_VIOLATION；
//   - flags 只带 LOCKFILE_EXCLUSIVE_LOCK，**不带 LOCKFILE_FAIL_IMMEDIATELY**：带上
//     就变成非阻塞尝试，锁被占用时立刻失败。
//
// 共享模式放开 READ|WRITE|DELETE：多个进程必须能同时打开同一个锁文件才能排队。
// 锁文件用 OPEN_ALWAYS 按需创建（0 字节，只做锁载体，不写入内容），且**从不删除**：
// 删除会与另一个进程的「打开 → 加锁」形成竞态（见 routing_file_write_lock.go）。
//
// 降级（静默，返回 ok=false）：目录不存在、权限/只读盘、网络盘不支持字节区间锁等。
// 此时调用方退化为仅进程内锁并继续写入流程，绝不 panic、绝不让写入失败。
func acquireRoutingFileOSLock(lockBasePath string) (func(), bool) {
	lockPath := lockBasePath + routingFileOSLockSuffix
	pathPtr, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, false
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL, // 同步句柄（不带 FILE_FLAG_OVERLAPPED）→ LockFileEx 阻塞
		0,
	)
	if err != nil {
		return nil, false
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(
		handle,
		windows.LOCKFILE_EXCLUSIVE_LOCK, // 不带 LOCKFILE_FAIL_IMMEDIATELY → 阻塞等待
		0,
		routingFileOSLockBytesLow,
		routingFileOSLockBytesHigh,
		overlapped,
	); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, false
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			// 释放失败也无害：CloseHandle 会释放该句柄持有的所有字节区间锁。
			_ = windows.UnlockFileEx(handle, 0, routingFileOSLockBytesLow, routingFileOSLockBytesHigh, overlapped)
			_ = windows.CloseHandle(handle)
			// 不删除锁文件（见 routing_file_write_lock.go：删除会引入竞态）。
		})
	}, true
}
