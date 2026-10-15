// turn_activity.go 是「IM 来源回合进行中」会话登记器（docs/bots/13 §4.2 L3）
// 的宿主共享实现：宿主在入站提交（Backend.Handle / runner DispatchTurn）进入时
// Begin、返回时 End；内容镜像订阅用它跳过 IM 回合——其正文已由回复路径投递，
// 避免同一文本对同一 chat 双发。aicli 与 runtime-server 共用本实现。
package connectors

import (
	"strings"
	"sync"
)

// TurnActivityTracker 登记进行中的 IM 来源回合（按会话计数，支持嵌套/并发）。
// 零值不可用；用 NewTurnActivityTracker 构造。方法对 nil 接收者安全。
type TurnActivityTracker struct {
	mu     sync.Mutex
	active map[string]int
}

// NewTurnActivityTracker 构造登记器。
func NewTurnActivityTracker() *TurnActivityTracker {
	return &TurnActivityTracker{active: make(map[string]int)}
}

// Begin 登记一次进行中的回合，返回注销函数（可重入计数；返回函数可重复调用）。
func (t *TurnActivityTracker) Begin(sessionID string) func() {
	if t == nil {
		return func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return func() {}
	}
	t.mu.Lock()
	if t.active == nil {
		t.active = make(map[string]int)
	}
	t.active[sessionID]++
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		if count := t.active[sessionID]; count <= 1 {
			delete(t.active, sessionID)
		} else {
			t.active[sessionID] = count - 1
		}
		t.mu.Unlock()
	}
}

// Active 报告该会话是否有 IM 来源回合进行中（内容镜像的 L3 判据）。
func (t *TurnActivityTracker) Active(sessionID string) bool {
	if t == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active[sessionID] > 0
}
