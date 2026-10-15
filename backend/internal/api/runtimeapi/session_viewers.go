package runtimeapi

import (
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

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

// presence 适配 notify.Presence（docs/bots/12 §4 R1）：runtime-server 是单宿主，
// nodeID 恒为本机标识——内核门的定位键要求两段都非空，这里忽略 nodeID、按
// sessionID 判定。nil 接收者恒 false（内核门对 nil 权威放行）。
func (v *sessionViewers) presence(nodeID, sessionID string) bool {
	if v == nil {
		return false
	}
	return v.Active(strings.TrimSpace(sessionID))
}

// SessionPresence 返回 web SSE 订阅面的 notify.Presence 权威（供宿主内
// 通知消费方经内核 Dispatcher 门禁判定"web 端正在看"；生产消费方随
// runtime-server 自有推送面切片接入，当前先固化契约与测试）。
func (h *Handler) SessionPresence() notify.Presence {
	if h == nil {
		return nil
	}
	return presenceFuncAdapter{v: &h.sessionViewers}
}

// presenceFuncAdapter 把方法值适配为 Presence（避免 Handler 直接实现接口）。
type presenceFuncAdapter struct{ v *sessionViewers }

func (a presenceFuncAdapter) Active(nodeID, sessionID string) bool {
	return a.v.presence(nodeID, sessionID)
}
