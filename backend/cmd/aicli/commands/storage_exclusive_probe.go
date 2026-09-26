package commands

import "errors"

// ============================================================================
// 句柄级独占探测（--require-exclusive 的实现基础）。
//
// 默认的"被占用"判定只依赖 SQLite 锁冲突（BUSY）：其它进程打开着库但空闲时
// 不一定触发。离线压缩需要更强的保证，因此在打开库之前用操作系统句柄语义探测：
//
//   - Windows：以零共享模式打开主库文件，任何其它进程/连接持有句柄（包括空闲
//     连接）都会返回 sharing violation → in_use；
//   - 其它平台：SQLite 的 POSIX 锁在空闲连接上不持锁，没有等价的可靠探测，
//     显式报"不支持"，而不是给出虚假的独占保证。
// ============================================================================

var (
	// errStorageExclusiveBusy 表示探测到其它进程仍持有数据库文件句柄。
	errStorageExclusiveBusy = errors.New("database file is held by another process")
	// errStorageExclusiveUnsupported 表示当前平台没有句柄级独占探测实现。
	errStorageExclusiveUnsupported = errors.New("exclusive handle probe is not implemented on this platform")
)
