package chat

import (
	"strings"
	"sync"
)

// 会话级写锁（方案 §3.4 M11 / §4.4）。
//
// 会话路由覆盖是「读-改-写」：先读当前 override，合并补丁，再整份写回会话记录。
// 若两次写入并发执行，后写者会带着同一份旧快照落盘，静默覆盖前者的字段（丢更新，
// 方案 §12 R4）。因此写入方必须在同一把会话锁内完成「读 → 改 → 写」。
//
// 锁按会话 ID 注册，**进程内共享**：aicli TUI 的本地写入
// （chatRoutingCommitOverride → SessionManager.Update）与内嵌 runtime-server 的
// API handler（PATCH /api/runtime/sessions/{id}/routing）取的是同一把锁。
// 跨进程并发不在本锁范围内，仍由存储层的原子写（FileStorage 的写锁 / SQLite 事务）
// 保证单次写入不撕裂。
type sessionWriteLockEntry struct {
	mu   sync.Mutex
	refs int
}

var (
	sessionWriteLocksMu sync.Mutex
	sessionWriteLocks   = map[string]*sessionWriteLockEntry{}
)

// LockSessionWrite 获取会话级写锁，返回释放函数；释放函数可安全重复调用。
// 会话 ID 为空时返回 no-op 释放函数（调用方无需为此单独分支）。
//
// 用法：
//
//	unlock := LockSessionWrite(session.ID)
//	defer unlock()
//	// 读-改-写
func LockSessionWrite(sessionID string) func() {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return func() {}
	}

	sessionWriteLocksMu.Lock()
	entry := sessionWriteLocks[id]
	if entry == nil {
		entry = &sessionWriteLockEntry{}
		sessionWriteLocks[id] = entry
	}
	// 先加引用再等锁：等待者持有引用时条目不会被回收，避免「释放者删表 → 等待者
	// 拿到孤儿锁」。
	entry.refs++
	sessionWriteLocksMu.Unlock()

	entry.mu.Lock()

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.mu.Unlock()
			sessionWriteLocksMu.Lock()
			entry.refs--
			if entry.refs <= 0 {
				delete(sessionWriteLocks, id)
			}
			sessionWriteLocksMu.Unlock()
		})
	}
}
