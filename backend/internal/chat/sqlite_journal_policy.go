//go:build !win7compat

package chat

// 主线构建的 SQLite 日志模式策略。
//
// 为什么主线可以用（且应该用）WAL：
//   - ncruces/go-sqlite3 >= v0.35.3 在 Windows 10 1803+ / Server 2019+ 上把
//     WAL-index（-shm）通过 VirtualAlloc2 + MapViewOfFile3 直接映射进 wasm 线性
//     内存（上游 PR #413，修复 issue #404），多连接/多进程并发写 WAL 不再有
//     “按锁边界拷贝伪共享内存”导致的回填陈旧页、B 树撕裂问题；
//   - WAL 允许读写并发，是本仓库多进程 CLI + Web 的既有功能基线，不能关。
//
// win7compat 构建的对应策略见 sqlite_journal_policy_win7.go：那条工具链被
// Go 1.21.4 钉在 ncruces v0.22.x，拿不到该修复，所以必须退回回滚日志。
const runtimeJournalModeWAL = "WAL"

// runtimeJournalMode 返回当前构建对文件型 SQLite 使用的 journal_mode。
func runtimeJournalMode() string { return runtimeJournalModeWAL }

// runtimeJournalUsesWAL 报告当前构建是否使用 WAL（WAL 专属 PRAGMA 与
// checkpoint 只在 WAL 下执行）。
func runtimeJournalUsesWAL() bool { return true }
