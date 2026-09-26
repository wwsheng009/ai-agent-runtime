//go:build !windows

package commands

// storageExclusiveProbe 在非 Windows 平台没有可靠的句柄级独占探测实现：
// SQLite 的空闲连接在 POSIX 上不持有文件锁，无法与"无进程使用"区分。
func storageExclusiveProbe(path string) error {
	return errStorageExclusiveUnsupported
}
