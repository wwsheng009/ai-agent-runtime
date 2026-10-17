package chat

import (
	"context"
	"sync"
)

// P2 单写者收敛（session-message-identity-protocol-20261017 §3 INV-2）：
// run 生命周期内，loop 持有的 session 对象是唯一真身。actor 的 pending/审批/
// 回执路径需要在 run 中改历史时，必须通过本文件提供的受控入口在真身上操作，
// 禁止绕经 store 做 loadSession-persist 往返（副本写回会覆盖 loop 对真身的并发
// 写入——2026-10-17 实弹缺陷的根因通道）。
//
// 真身引用经 run control 携带：run 接管 session 时挂引用，终结时解除；恢复路径
// （actor 命令循环 goroutine）通过 RunLiveSession 取真身。真身的并发安全由 actor
// 既有模型保证：恢复只发生在 loop 挂起等待恢复决议的窗口
// （PendingTool/PendingApproval/PendingQuestion），此刻 loop goroutine 不在写历史。

// liveSessionRef 是挂进 run control 的真身引用（独立小结构，避免给
// sessionRunControl 增加锁字段——引用的挂载/解除各自串行于 run 生命周期）。
type liveSessionRef struct {
	mu    sync.RWMutex
	_live *Session
}

func (r *liveSessionRef) attach(session *Session) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r._live = session
}

func (r *liveSessionRef) detach() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r._live = nil
}

func (r *liveSessionRef) get() (*Session, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r._live == nil {
		return nil, false
	}
	return r._live, true
}

// attachLiveSession 在 run 接管 session 时挂真身引用（startSessionRun 调用）。
func (r *sessionRunControl) attachLiveSession(session *Session) {
	if r == nil {
		return
	}
	r.live.attach(session)
}

// detachLiveSession 在 run 终结时解除真身引用，保证 run 结束后不会有路径继续
// 借用已过期的对象。
func (r *sessionRunControl) detachLiveSession() {
	if r == nil {
		return
	}
	r.live.detach()
}

// liveSession 返回 run 真身；第二个返回值表示是否存在（false = 调用方必须回退
// loadSession 语义，run 外路径照旧）。
func (r *sessionRunControl) liveSession() (*Session, bool) {
	if r == nil {
		return nil, false
	}
	return r.live.get()
}

// RunLiveSession 是 actor 恢复路径取真身的入口：优先返回 run 真身（INV-2 主
// 路径）；run 未接管或已终结时返回 nil，调用方回退 loadSession（run 外语义）。
func (a *SessionActor) RunLiveSession(ctx context.Context) *Session {
	if a == nil || ctx == nil {
		return nil
	}
	run, ok := sessionRunControlFromContext(ctx)
	if !ok || run == nil {
		return nil
	}
	session, ok := run.liveSession()
	if !ok {
		return nil
	}
	return session
}
