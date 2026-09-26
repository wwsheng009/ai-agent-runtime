//go:build win7compat

package chat

// Windows 7 兼容构建的 SQLite 日志模式策略：禁用 WAL，退回回滚日志。
//
// 为什么必须禁用（而不是“升级驱动”）：
//   - win7compat 构建 = Go 1.21.4 + go.win7.mod，被工具链钉在 ncruces
//     go-sqlite3 v0.22.x；上游修复 issue #404 需要 v0.35.3+，而 v0.35.3+ 的
//     go.mod 要求 Go >= 1.26（v0.25.2 起要求 Go 1.23+），在 Go 1.21 下无法构建；
//     并且该修复依赖 Win10 1803+ 的 placeholder API，Windows 7 永远拿不到。
//   - v0.22.x 的 Windows VFS 用 “按锁边界拷贝” 的伪共享内存实现 WAL-index
//     （vfs/shm_copy.go）。上游 issue #404 / PR #405 已确认：SQLite 会在
//     xShmLock 调用之间读写 wal-index，checkpointer 因此会回填陈旧页版本并截断
//     未回填的 WAL 帧，直接撕裂数据库文件（表现为 disk image is malformed /
//     file is not a database / bad ptr map entry）。该缺陷与业务用法无关，任何
//     多连接或多进程的 WAL 写都会触发。
//   - 回滚日志（journal_mode=DELETE）的跨进程互斥完全依赖数据库文件自身的
//     POSIX/Windows 字节范围锁，不经过 -shm，因此不受该缺陷影响；代价是写并发
//     与“读不阻塞写”不如 WAL，但功能完整、多进程可用。
//
// 该构建下：
//   - 不再设置 journal_mode=WAL / wal_autocheckpoint / journal_size_limit；
//   - Close 不再做 wal_checkpoint（非 WAL 下无意义）；
//   - 已存在的 WAL 库会在首次打开时被 PRAGMA journal_mode=DELETE 转换（需要
//     独占锁，由既有锁重试循环处理）。
const runtimeJournalModeDelete = "DELETE"

func runtimeJournalMode() string { return runtimeJournalModeDelete }

func runtimeJournalUsesWAL() bool { return false }
