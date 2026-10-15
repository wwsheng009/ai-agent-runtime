package runtimeapi

import "sync"

// sessionViewers tracks live SSE viewers per session（docs/bots/10 §4）：该会话
// 有 web 订阅者时，gateway 会话摘要上报 Viewing=true，网关据此抑制外部推送。
// 计数为连接粒度、best-effort；零值可用（懒初始化）。
type sessionViewers struct {
	mu     sync.Mutex
	counts map[string]int
}

// Add 增计一个订阅者，返回是否为 0→1 跃迁（调用方据此触发摘要刷新）。
func (v *sessionViewers) Add(sessionID string) bool {
	if v == nil || sessionID == "" {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.counts == nil {
		v.counts = make(map[string]int, 4)
	}
	prev := v.counts[sessionID]
	v.counts[sessionID] = prev + 1
	return prev == 0
}

// Remove 减计一个订阅者，返回是否为 1→0 跃迁。
func (v *sessionViewers) Remove(sessionID string) bool {
	if v == nil || sessionID == "" {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	prev := v.counts[sessionID]
	if prev <= 1 {
		delete(v.counts, sessionID)
		return prev == 1
	}
	v.counts[sessionID] = prev - 1
	return false
}

// Active 报告该会话当前是否存在 web 订阅者。
func (v *sessionViewers) Active(sessionID string) bool {
	if v == nil || sessionID == "" {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.counts[sessionID] > 0
}