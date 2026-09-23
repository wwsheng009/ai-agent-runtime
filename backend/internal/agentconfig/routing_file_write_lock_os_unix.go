//go:build unix && !aix && !solaris

package agentconfig

import (
	"errors"
	"os"
	"sync"
	"syscall"
)

// routingFileOSLockSupported 标记本平台有真正的跨进程锁实现（测试用来区分
// 「实现存在但被降级」与「平台本身没有实现」）。
const routingFileOSLockSupported = true

// acquireRoutingFileOSLock 在 Unix 上用 flock(2) 取跨进程排他锁。
//
//   - syscall.LOCK_EX 是**阻塞式**：锁被别的进程占用时 flock 睡眠等待，不忙等空转；
//   - 被信号打断（EINTR）时重试：否则信号频繁的宿主上会把「被打断」误判成「不支持」
//     而静默降级，跨进程互斥会悄悄失效；
//   - 锁文件用 O_CREATE|O_RDWR 按需创建（0 字节，只做锁载体，不写入内容），且
//     **从不删除**：删除会与另一个进程的「打开 → 加锁」形成竞态（见
//     routing_file_write_lock.go）。0 字节残留无害。
//
// 降级（静默，返回 ok=false）：目录不存在、权限/只读盘、文件系统不支持 flock
// （部分网络盘）等。此时调用方退化为仅进程内锁并继续写入流程，绝不 panic、绝不让
// 写入失败。
//
// 构建约束说明：aix/solaris 的标准库没有 syscall.Flock（走 fcntl 记录锁），由
// routing_file_write_lock_os_fallback.go 兜底为 no-op。
func acquireRoutingFileOSLock(lockBasePath string) (func(), bool) {
	lockPath := lockBasePath + routingFileOSLockSuffix
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	if err := flockRoutingFileExclusive(int(file.Fd())); err != nil {
		_ = file.Close()
		return nil, false
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			// 显式 LOCK_UN 只是语义清晰；关闭 fd 本身也会释放 flock。
			_ = flockRoutingFileUnlock(int(file.Fd()))
			_ = file.Close()
			// 不删除锁文件（见 routing_file_write_lock.go：删除会引入竞态）。
		})
	}, true
}

// flockRoutingFileExclusive 阻塞取排他锁，EINTR 重试。
func flockRoutingFileExclusive(fd int) error {
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX)
		if err == nil {
			return nil
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		return err
	}
}

// flockRoutingFileUnlock 释放排他锁，EINTR 重试；失败无害（Close 也会释放）。
func flockRoutingFileUnlock(fd int) error {
	for {
		err := syscall.Flock(fd, syscall.LOCK_UN)
		if err == nil {
			return nil
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		return err
	}
}
