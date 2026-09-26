package runtimeapi

import (
	"strings"
	"sync"
	"time"
)

const (
	// agentChatActiveTurnSource 标识 web 直连（POST /api/agent/chat）的在途回合来源。
	agentChatActiveTurnSource = "agent_chat_stream"
	// activeTurnCancelSourceUserInterrupt 是「接口显式取消」写入 cancel_source 的取值。
	// 与 durable actor 路径（chat/actor.go 的 sessionRunCancelSource）保持同一字面量：
	// 前端/trajectory 用同一个字符串判定「这是用户主动停止」，而不是失败。
	activeTurnCancelSourceUserInterrupt = "user_interrupt"
	// defaultDetachedAgentChatRunTimeout 是未配置 Agent.Timeout 时，detached 回合
	// 的兜底上限（防止上游挂死留下常驻 goroutine）。
	defaultDetachedAgentChatRunTimeout = 30 * time.Minute
	// detachedAgentChatRunGrace 是兜底超时相对 Agent.Timeout 的宽限：让 ReAct 循环
	// 自身的 MaxRunDuration 先收敛，ctx 超时只作最后熔断。
	detachedAgentChatRunGrace = 30 * time.Second
)

// 会话在途回合注册表（P4-刷新续传 / 建议 3-后端 cancel 契约）。
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
// 建议 3 追加第三半：detach 之后**没有任何人能让它停下来**。runCtx 的
// CancelFunc 只活在 handler 局部作用域，客户端 abort 不再取消执行（这正是
// detached 的定义），于是「停止」按钮退化成只停本页接收、服务端继续跑。
// 因此注册表同时持有取消句柄（activeTurnCancel）：POST
// /api/runtime/sessions/{id}/runtime/commands 的 `interrupt` 命令据此真正
// 中止在途回合，并在响应里如实报告「取消到了谁 / 是否真的触发了取消」。
//
// 语义约束：
//   - 进程内状态，只表达「本进程此刻在跑哪个 turn」；重启即失效，不落库
//     （durable actor 会话的在途状态仍以 runtime store 为准）。
//   - 键为 normalized session id；同一会话同一时刻只登记一个在途回合
//     （会话租约保证了这一点），后来的 begin 覆盖旧条目，旧条目的 release
//     因身份不匹配而不会误删新条目。
//   - release 幂等：重复调用无副作用（defer + 显式调用都安全）。
//   - 取消只影响被登记的那一个回合，且由 run 自己 release；「已取消、仍在收尾」
//     是可见状态（CancelSource 非空），不是残留。
type activeTurnRegistry struct {
	mu    sync.RWMutex
	turns map[string]activeTurnEntry
}

// activeTurnSnapshot 是 `GET /runtime` 里 `active_turn` 字段的载荷形状。
type activeTurnSnapshot struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	// Source 标明回合来源（"agent_chat_stream" 等），便于前端区分 web 直连
	// 回合与 durable actor 回合。
	Source string `json:"source"`
	// Detached 为 true 表示该回合在客户端断开后继续执行（刷新可续传）。
	Detached  bool      `json:"detached"`
	StartedAt time.Time `json:"started_at"`
	// CancelSource 非空表示该回合已被显式取消（值即取消来源，如 user_interrupt），
	// 在 run 真正返回并 release 之前一直可见。刷新后的页面据此区分
	// 「正在跑、可续传」与「已请求停止、等收尾」。
	CancelSource string `json:"cancel_source,omitempty"`
}

// activeTurnCancel 是一个在途回合的取消句柄（建议 3）。
//
// 为什么必须持有句柄而不是只读快照：detached 回合的 runCtx 是
// `context.WithTimeout(context.WithoutCancel(r.Context()), …)`，它的 CancelFunc
// 只存在于 handler 的局部作用域。刷新/重连后的新页面（以及 stop 按钮）能触达的
// 只有进程内注册表，所以取消句柄必须随回合一起登记，否则「停止」只能停本页接收。
//
// 语义：
//   - once 保证同一回合只触发一次真实取消（重复 stop 幂等、不报错）；
//   - source 记录取消来源，写入 active_turn 快照与 chat 错误帧的 cancel_source；
//   - 句柄不负责清理注册表条目：run 返回时由 release 清理，因此
//     「已取消但仍在收尾」这一窗口对观察者是可解释的。
type activeTurnCancel struct {
	once sync.Once
	// cancel 由 handler 提供：取消 runCtx，返回 true 表示确实触发了取消。
	cancel func(source string) bool

	mu     sync.Mutex
	source string
}

func newActiveTurnCancel(cancel func(source string) bool) *activeTurnCancel {
	if cancel == nil {
		return nil
	}
	return &activeTurnCancel{cancel: cancel}
}

// fire 触发一次取消；返回 (本次调用是否真的触发了取消, 取消来源)。
func (c *activeTurnCancel) fire(source string) (bool, string) {
	if c == nil || c.cancel == nil {
		return false, ""
	}
	source = strings.TrimSpace(source)
	fired := false
	c.once.Do(func() {
		c.mu.Lock()
		c.source = source
		c.mu.Unlock()
		fired = c.cancel(source)
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	return fired, c.source
}

// cancelSource 返回已记录的取消来源；未取消时为空串。
func (c *activeTurnCancel) cancelSource() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.source
}

// activeTurnEntry 是注册表里的一个条目：对外快照 + 可选取消句柄。
type activeTurnEntry struct {
	snapshot activeTurnSnapshot
	cancel   *activeTurnCancel
}

// activeTurnCancelReason 是取消结果的机读 reason（写进 /runtime/commands 响应）。
//
// 之所以要区分四种「没能取消」，是因为调用方的下一步动作不同：
//   - no_active_turn / not_cancelable：本注册表帮不上忙，回退 durable actor 路径；
//   - already_cancelled：同一个回合的重复 stop，语义上仍是「已取消」，不该报错；
//   - turn_mismatch：客户端拿着过期回合身份打到了新回合上，必须拒绝（409），
//     否则一次迟到的 stop 会误杀刚启动的回合。
type activeTurnCancelReason string

const (
	activeTurnCancelReasonCancelled        activeTurnCancelReason = "cancelled"
	activeTurnCancelReasonAlreadyCancelled activeTurnCancelReason = "already_cancelled"
	activeTurnCancelReasonNoActiveTurn     activeTurnCancelReason = "no_active_turn"
	activeTurnCancelReasonTurnMismatch     activeTurnCancelReason = "turn_mismatch"
	activeTurnCancelReasonNotCancelable    activeTurnCancelReason = "not_cancelable"
)

// activeTurnCancelResult 是 activeTurnRegistry.cancel 的结果。
type activeTurnCancelResult struct {
	// Turn 是当前登记的回合（无在途回合时为零值）。
	Turn activeTurnSnapshot
	// Cancelled 为 true 表示本次调用真的发出了取消（重复 stop 为 false）。
	Cancelled bool
	// Reason 见 activeTurnCancelReason*。
	Reason activeTurnCancelReason
}

func newActiveTurnRegistry() *activeTurnRegistry {
	return &activeTurnRegistry{turns: make(map[string]activeTurnEntry)}
}

// begin 登记一个在途回合，返回幂等的释放函数。
func (r *activeTurnRegistry) begin(sessionID, turnID, source string, detached bool) func() {
	return r.beginCancelable(sessionID, turnID, source, detached, nil)
}

// beginCancelable 登记一个在途回合并持有它的取消句柄（建议 3）。
//
// cancel 为 nil 时等价于 begin（只登记、不可取消）；handler 传入的 cancel 在
// 「接口显式取消」时被调用一次，参数是取消来源。
func (r *activeTurnRegistry) beginCancelable(sessionID, turnID, source string, detached bool, cancel func(source string) bool) func() {
	sessionID = strings.TrimSpace(sessionID)
	turnID = strings.TrimSpace(turnID)
	if r == nil || sessionID == "" || turnID == "" {
		return func() {}
	}
	snapshot := activeTurnSnapshot{
		SessionID: sessionID,
		TurnID:    turnID,
		Source:    strings.TrimSpace(source),
		Detached:  detached,
		StartedAt: time.Now().UTC(),
	}
	handle := newActiveTurnCancel(cancel)

	r.mu.Lock()
	if r.turns == nil {
		r.turns = make(map[string]activeTurnEntry)
	}
	r.turns[sessionID] = activeTurnEntry{snapshot: snapshot, cancel: handle}
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			// 身份不匹配说明该条目已被更晚的回合覆盖：不删，避免误清在途回合。
			if current, ok := r.turns[sessionID]; ok && current.snapshot.TurnID == snapshot.TurnID {
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
	entry, ok := r.turns[sessionID]
	r.mu.RUnlock()
	if !ok {
		return activeTurnSnapshot{}, false
	}
	snapshot := entry.snapshot
	// 取消来源由句柄持有：只读路径也要能看到「已请求停止」。
	if source := entry.cancel.cancelSource(); source != "" {
		snapshot.CancelSource = source
	}
	return snapshot, true
}

// cancel 取消会话当前在途回合（建议 3：后端 cancel 契约的唯一入口）。
//
// 契约（与 SubmitSessionRuntimeCommand 的 interrupt 分支一一对应）：
//   - 没有在途回合 → no_active_turn，调用方回退 durable actor 路径；
//   - turnID 非空且与当前在途回合不一致 → turn_mismatch，调用方必须拒绝：
//     一次迟到的 stop 绝不能打到新回合上；
//   - 登记时没有取消句柄 → not_cancelable（历史 begin 调用方），同样回退；
//   - 首次取消 → cancelled；同一回合重复取消 → already_cancelled（幂等，
//     调用方不必把重复 stop 当错误）。
func (r *activeTurnRegistry) cancel(sessionID, turnID, source string) activeTurnCancelResult {
	if r == nil {
		return activeTurnCancelResult{Reason: activeTurnCancelReasonNoActiveTurn}
	}
	sessionID = strings.TrimSpace(sessionID)
	turnID = strings.TrimSpace(turnID)
	if sessionID == "" {
		return activeTurnCancelResult{Reason: activeTurnCancelReasonNoActiveTurn}
	}

	r.mu.RLock()
	entry, ok := r.turns[sessionID]
	r.mu.RUnlock()
	if !ok {
		return activeTurnCancelResult{Reason: activeTurnCancelReasonNoActiveTurn}
	}
	turn := entry.snapshot
	if turnID != "" && turnID != turn.TurnID {
		turn.CancelSource = entry.cancel.cancelSource()
		return activeTurnCancelResult{Turn: turn, Reason: activeTurnCancelReasonTurnMismatch}
	}
	if entry.cancel == nil {
		return activeTurnCancelResult{Turn: turn, Reason: activeTurnCancelReasonNotCancelable}
	}
	fired, appliedSource := entry.cancel.fire(source)
	turn.CancelSource = appliedSource
	if !fired {
		return activeTurnCancelResult{Turn: turn, Reason: activeTurnCancelReasonAlreadyCancelled}
	}
	return activeTurnCancelResult{Turn: turn, Cancelled: true, Reason: activeTurnCancelReasonCancelled}
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
