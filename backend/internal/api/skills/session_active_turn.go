package skills

import (
	"strings"
	"sync"
	"time"
)

const (
	// agentChatActiveTurnSource 标识 web 直连（POST /api/agent/chat）的在途回合来源。
	agentChatActiveTurnSource = "agent_chat_stream"
	// defaultDetachedAgentChatRunTimeout 是未配置 Agent.Timeout 时，detached 回合
	// 的兜底上限（防止上游挂死留下常驻 goroutine）。
	defaultDetachedAgentChatRunTimeout = 30 * time.Minute
	// detachedAgentChatRunGrace 是兜底超时相对 Agent.Timeout 的宽限：让 ReAct 循环
	// 自身的 MaxRunDuration 先收敛，ctx 超时只作最后熔断。
	detachedAgentChatRunGrace = 30 * time.Second
)

// 会话在途回合注册表（P4-刷新续传）。
//
// 背景：workspace 的实时轨迹由 `GET /runtime/stream` 长轮询承载，刷新页面后
// 该流会按游标自动重连（use-session-runtime-stream 的常驻重连循环），但**回合
// 本体**（POST /api/agent/chat 的 ReAct 运行）此前与请求上下文同生命周期：
// 浏览器刷新即 abort 请求 → `r.Context()` 取消 → 在途回合被中止（handler.go
// 的 PersistFinal 注释就写明「含客户端断开导致的 ctx 取消」）。于是刷新后
// 页面虽然重新连上了流，却已经没有任何事件可续——用户看到的就是「刷新后
// 整个 SSE live 断开」。
//
// 修复分两半：
//  1. `resume_on_disconnect` 请求开关：客户端断开后 run 继续跑（见 handler.go
//     AgentChat 的 detached 分支），历史与增量照常落库；
//  2. 本注册表：把「该会话此刻有没有在途回合」暴露给刷新后的新页面
//     （GET /api/runtime/sessions/{id}/runtime 的 `active_turn` 字段），
//     前端据此重新挂载在途回合身份，让 runtime/stream 上落库的增量帧
//     继续渲染到同一条 streaming 消息里（renderLiveDeltas 门控才会打开）。
//
// 语义约束：
//   - 进程内状态，只表达「本进程此刻在跑哪个 turn」；重启即失效，不落库
//     （durable actor 会话的在途状态仍以 runtime store 为准）。
//   - 键为 normalized session id；同一会话同一时刻只登记一个在途回合
//     （会话租约保证了这一点），后来的 begin 覆盖旧条目，旧条目的 release
//     因身份不匹配而不会误删新条目。
//   - release 幂等：重复调用无副作用（defer + 显式调用都安全）。
type activeTurnRegistry struct {
	mu    sync.RWMutex
	turns map[string]activeTurnSnapshot
}

// activeTurnSnapshot 是 `GET /runtime` 里 `active_turn` 字段的载荷形状。
type activeTurnSnapshot struct {
	SessionID string    `json:"session_id"`
	TurnID    string    `json:"turn_id"`
	// Source 标明回合来源（"agent_chat_stream" 等），便于前端区分 web 直连
	// 回合与 durable actor 回合。
	Source string `json:"source"`
	// Detached 为 true 表示该回合在客户端断开后继续执行（刷新可续传）。
	Detached  bool      `json:"detached"`
	StartedAt time.Time `json:"started_at"`
}

func newActiveTurnRegistry() *activeTurnRegistry {
	return &activeTurnRegistry{turns: make(map[string]activeTurnSnapshot)}
}

// begin 登记一个在途回合，返回幂等的释放函数。
func (r *activeTurnRegistry) begin(sessionID, turnID, source string, detached bool) func() {
	sessionID = strings.TrimSpace(sessionID)
	turnID = strings.TrimSpace(turnID)
	if r == nil || sessionID == "" || turnID == "" {
		return func() {}
	}
	entry := activeTurnSnapshot{
		SessionID: sessionID,
		TurnID:    turnID,
		Source:    strings.TrimSpace(source),
		Detached:  detached,
		StartedAt: time.Now().UTC(),
	}

	r.mu.Lock()
	if r.turns == nil {
		r.turns = make(map[string]activeTurnSnapshot)
	}
	r.turns[sessionID] = entry
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			// 身份不匹配说明该条目已被更晚的回合覆盖：不删，避免误清在途回合。
			if current, ok := r.turns[sessionID]; ok && current.TurnID == entry.TurnID {
				delete(r.turns, sessionID)
			}
		})
	}
}

// get 读取会话当前在途回合；无在途回合时返回 (零值, false)。
func (r *activeTurnRegistry) get(sessionID string) (activeTurnSnapshot, bool) {
	if r == nil {
		return activeTurnSnapshot{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return activeTurnSnapshot{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.turns[sessionID]
	return entry, ok
}

// getActiveTurnRegistry 懒初始化（Handler 零值可用：测试里直接构造 &Handler{}
// 也能走到注册表，与 attachRuntimeEventBridge 的 sync.Once 口径一致）。
func (h *Handler) getActiveTurnRegistry() *activeTurnRegistry {
	if h == nil {
		return nil
	}
	h.activeTurnsOnce.Do(func() {
		h.activeTurns = newActiveTurnRegistry()
	})
	return h.activeTurns
}
