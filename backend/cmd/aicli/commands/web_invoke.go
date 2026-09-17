package commands

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// ============================================================================
// POST /web/api/invoke — 同步远程调用（远程控制 aicli chat TUI）
//
// 与 /web/api/input 的分工：
//   - /web/api/input 是异步注入：立即返回 {"status":"queued"}，调用方通过
//     /web/api/events（SSE）或轮询 /web/api/screen 获取后续输出；
//   - /web/api/invoke 是同步调用：一次 HTTP 请求内完成"注入 prompt → 等待
//     turn 结束 → 返回最终状态与界面渲染"，适合脚本 / 外部 Agent 远程调用。
//
// 请求体（application/json；非 JSON body 时整个 body 视为 prompt 文本）：
//
//	{"prompt": "修复构建错误", "timeout_ms": 120000}
//
// 响应（HTTP 200，status 字段表达结果）：
//
//	{
//	  "status": "completed"|"settled"|"timeout"|"interrupted"|
//	            "requires_approval"|"requires_answer"|"rejected"|"error",
//	  "session_id": "...", "turn_id": "...", "elapsed_ms": 1234,
//	  "queued": true, "busy": false, "pending_inputs": 0, "llm_observed": true,
//	  "assistant": {"role": "assistant", "content": "..."},
//	  "screen": {"available": true, "width": 120, "height": 40,
//	             "lines": ["..."], "text": "..."}
//	}
//
// 等待结束的判定（EventBus 订阅 + 运行时状态轮询双通道）：
//  1. prompt 被主循环消费（InputQueue 计数回到 0）；
//  2. 观察到 turn 忙碌（busy 采样或 llm_request_started）；
//  3. turn 结束：会话空闲、队列为空、事件静默超过 quiet window。
//
// 若等待期间会话进入审批/提问等待，提前返回 requires_approval /
// requires_answer（调用方用 POST /web/api/input 回答后可再次 invoke 续跑）；
// 超过 timeout_ms 返回 timeout，并附带当前渲染与运行状态。
// ============================================================================

const (
	// chatWebInvokeDefaultTimeoutMs 是未显式传 timeout_ms 时的等待上限。
	chatWebInvokeDefaultTimeoutMs = 120000
	// chatWebInvokeMinTimeoutMs / chatWebInvokeMaxTimeoutMs 是 timeout_ms 的钳制范围。
	chatWebInvokeMinTimeoutMs = 1000
	chatWebInvokeMaxTimeoutMs = 600000
	// chatWebInvokeBodyLimitBytes 是请求体读取上限（与 input 端点一致）。
	chatWebInvokeBodyLimitBytes = 1 << 20
	// chatWebInvokePollInterval 是等待循环的采样间隔。
	chatWebInvokePollInterval = 150 * time.Millisecond
	// chatWebInvokeQuietWindow 是判定 turn 结束所需的"无事件静默"窗口。
	chatWebInvokeQuietWindow = 800 * time.Millisecond
	// chatWebInvokeNoLLMGrace 是无 LLM turn（斜杠命令等）路径在输入被消费后
	// 的确认宽限期：超过它仍无 turn 事件，按 settled 返回（附当前渲染）。
	chatWebInvokeNoLLMGrace = 2 * time.Second
	// chatWebInvokeAttentionTicks 是审批/提问状态连续命中多少拍后才提前返回，
	// 避免瞬时状态误报。
	chatWebInvokeAttentionTicks = 2
	// chatWebInvokeMaxRequestIDLen 是 client_request_id 的最大长度。
	chatWebInvokeMaxRequestIDLen = 128
	// chatWebInvokeIdemTTL 是幂等结果回放的保留窗口。
	chatWebInvokeIdemTTL = 10 * time.Minute
	// chatWebInvokeIdemMax 是幂等表上限（超出后淘汰最旧条目）。
	chatWebInvokeIdemMax = 256
)

// chatWebInvokeIdemEntry 保存一次已完成 invoke 的响应快照。
type chatWebInvokeIdemEntry struct {
	at   time.Time
	resp *chatWebInvokeResponse
}

var chatWebInvokeIdem = struct {
	mu sync.Mutex
	m  map[string]chatWebInvokeIdemEntry
}{m: map[string]chatWebInvokeIdemEntry{}}

// chatWebInvokeIdemKey 组合会话与调用方幂等键（按会话隔离，避免跨会话回放）。
func chatWebInvokeIdemKey(sessionID, requestID string) string {
	if requestID == "" {
		return ""
	}
	return sessionID + "\x00" + requestID
}

// lookupChatWebInvokeIdem 查询未过期的历史结果；命中时返回 duplicate=true 副本。
func lookupChatWebInvokeIdem(key string) (*chatWebInvokeResponse, bool) {
	if key == "" {
		return nil, false
	}
	chatWebInvokeIdem.mu.Lock()
	defer chatWebInvokeIdem.mu.Unlock()
	entry, ok := chatWebInvokeIdem.m[key]
	if !ok {
		return nil, false
	}
	if time.Since(entry.at) > chatWebInvokeIdemTTL {
		delete(chatWebInvokeIdem.m, key)
		return nil, false
	}
	clone := *entry.resp
	clone.Duplicate = true
	return &clone, true
}

// storeChatWebInvokeIdem 记录可回放的终态结果：只有 prompt 已注入（或
// wait_only 已开始等待）的状态才回放，避免把 rejected 等无副作用结果固化。
func storeChatWebInvokeIdem(key string, resp *chatWebInvokeResponse) {
	if key == "" || resp == nil {
		return
	}
	switch resp.Status {
	case "completed", "settled", "timeout", "interrupted",
		"requires_approval", "requires_answer", "error":
	default:
		return
	}
	clone := *resp
	clone.Duplicate = false
	chatWebInvokeIdem.mu.Lock()
	defer chatWebInvokeIdem.mu.Unlock()
	if len(chatWebInvokeIdem.m) >= chatWebInvokeIdemMax {
		oldestKey := ""
		var oldest time.Time
		for k, v := range chatWebInvokeIdem.m {
			if oldestKey == "" || v.at.Before(oldest) {
				oldestKey, oldest = k, v.at
			}
		}
		if oldestKey != "" {
			delete(chatWebInvokeIdem.m, oldestKey)
		}
	}
	chatWebInvokeIdem.m[key] = chatWebInvokeIdemEntry{at: time.Now(), resp: &clone}
}

// chatWebInvokeUsageFrom 采集当前会话的结构化 token 用量；无数据时返回 nil。
func chatWebInvokeUsageFrom(session *ChatSession) *chatWebInvokeUsage {
	if session == nil {
		return nil
	}
	usage := &chatWebInvokeUsage{
		InputTokens:         session.InputTokenCount,
		OutputTokens:        session.OutputTokenCount,
		TotalTokens:         session.TokenCount,
		ContextTokens:       session.ContextTokenCount,
		ContextWindowTokens: session.ContextWindowTokenCount,
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 &&
		usage.TotalTokens == 0 && usage.ContextTokens == 0 {
		return nil
	}
	return usage
}

// chatWebInvokeAcceptsSSE 判断调用方是否请求流式响应（Accept: text/event-stream）。
func chatWebInvokeAcceptsSSE(r *http.Request) bool {
	if r == nil {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}

// webInvokeMu 保证同一时刻至多一个同步 invoke 在等待（TryLock 冲突时返回 409），
// 避免多个远程调用方互相等待、把 HTTP 连接与调用方超时全部拖长。
var webInvokeMu sync.Mutex

// chatWebInvokeProbeFn 是运行状态探测的间接层：生产路径为 chatWebInvokeProbe，
// 单元测试可临时替换它以驱动等待循环的状态迁移（busy / 审批 / 提问）。
var chatWebInvokeProbeFn = chatWebInvokeProbe

// chatWebInvokeRequest 是 POST /web/api/invoke 的请求体。
type chatWebInvokeRequest struct {
	Prompt    string `json:"prompt"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
	// WaitOnly=true 时不注入 prompt，只等待当前 turn 结束（审批/提问决议后
	// 继续等待、或外部编排等待既有 turn 时使用）；此时 Prompt 可省略。
	WaitOnly bool `json:"wait_only,omitempty"`
	// ClientRequestID 是调用方幂等键（≤128 字符）：同一会话内重复提交相同
	// id 直接回放首次结果（duplicate=true），不会重复注入 prompt。
	ClientRequestID string `json:"client_request_id,omitempty"`
	// SessionID 可选：非空且与当前活动会话不一致时返回 409，避免 prompt
	// 误投递到切换后的会话（调用方应先 resume 再 invoke）。
	SessionID string `json:"session_id,omitempty"`
}

// chatWebInvokeUsage 是 invoke 响应中的结构化 token 用量（与 TUI /status
// 同源：本会话累计输入/输出与最近一次上下文占用）。
type chatWebInvokeUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	TotalTokens         int `json:"total_tokens"`
	ContextTokens       int `json:"context_tokens"`
	ContextWindowTokens int `json:"context_window_tokens"`
}

// chatWebInvokeResponse 是 POST /web/api/invoke 的响应体。
type chatWebInvokeResponse struct {
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	TurnID      string `json:"turn_id,omitempty"`
	ElapsedMs   int64  `json:"elapsed_ms"`
	Queued      bool   `json:"queued"`
	Busy        bool   `json:"busy"`
	Pending     int    `json:"pending_inputs"`
	LLMObserved bool   `json:"llm_observed"`
	// Duplicate=true 表示本次响应回放了相同 client_request_id 的首次结果。
	Duplicate bool                     `json:"duplicate,omitempty"`
	Usage     *chatWebInvokeUsage      `json:"usage,omitempty"`
	Assistant *chatWebScreenMessage    `json:"assistant,omitempty"`
	Screen    *chatDebugScreenSnapshot `json:"screen,omitempty"`
	// PendingApproval / PendingQuestion 仅在 requires_approval /
	// requires_answer（或等待期间出现等待）时携带，结构与
	// /web/api/events 的 connected 事件一致。
	PendingApproval map[string]interface{} `json:"pending_approval,omitempty"`
	PendingQuestion map[string]interface{} `json:"pending_question,omitempty"`
}

// HandleChatWebAPIInvoke 处理同步远程调用：注入 prompt、等待 turn 结束，
// 并在同一响应中返回最终 assistant 回复、运行状态与 TUI 渲染快照。
func HandleChatWebAPIInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "rejected",
			"reason": "method not allowed",
		})
		return
	}
	if !webInvokeMu.TryLock() {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "busy",
			"reason": "another /web/api/invoke is already in progress",
		})
		return
	}
	defer webInvokeMu.Unlock()

	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": "no active chat session",
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, chatWebInvokeBodyLimitBytes))
	if err != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "read body: " + err.Error(),
		})
		return
	}
	var req chatWebInvokeRequest
	if jsonErr := json.Unmarshal(body, &req); jsonErr != nil {
		// 兼容 text/plain：整个 body 视为 prompt。
		req.Prompt = strings.TrimSpace(string(body))
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" && !req.WaitOnly {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "empty prompt",
		})
		return
	}
	clientRequestID := strings.TrimSpace(req.ClientRequestID)
	if len(clientRequestID) > chatWebInvokeMaxRequestIDLen {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "client_request_id too long",
		})
		return
	}
	currentSessionID := currentRuntimeSessionID(session)
	if target := strings.TrimSpace(req.SessionID); target != "" &&
		currentSessionID != "" && target != currentSessionID {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status":     "error",
			"reason":     "session_id mismatch: current session is " + currentSessionID + " (resume it first)",
			"session_id": currentSessionID,
		})
		return
	}
	idemKey := chatWebInvokeIdemKey(currentSessionID, clientRequestID)
	if cached, ok := lookupChatWebInvokeIdem(idemKey); ok {
		writeWebAPIJSON(w, http.StatusOK, cached)
		return
	}
	// 安装 turn 记录器：invoke 覆盖的 turn 也能被 /web/api/turn 后验查询。
	ensureChatWebTurnRecorder(session)

	started := time.Now()
	baselineAssistant := chatWebInvokeAssistantContent(session)
	if req.WaitOnly {
		// wait-only：不注入 prompt，等待结束时回传最新 assistant 回复。
		baselineAssistant = ""
	}
	watch := newChatWebInvokeWatch()
	stream := startChatWebInvokeStream(w, r, currentSessionID)
	if stream != nil {
		// handler 返回前必须先停掉 writer goroutine：ResponseWriter 在 handler
		// 返回后由 net/http 收尾，并发 Write/Flush 会数据竞争/panic。
		defer func() {
			stream.Close()
			<-stream.writerDone
		}()
		watch.onStream = stream.writeEvent
	}
	unsubscribe := subscribeChatWebInvokeWatch(session, watch)
	defer unsubscribe()

	if !req.WaitOnly {
		result, ok := injectChatWebPrompt(session, prompt)
		if !ok {
			writeChatWebInvokeResult(w, stream, http.StatusInternalServerError, &chatWebInvokeResponse{
				Status:    "error",
				Reason:    "input queue unavailable",
				SessionID: currentSessionID,
				ElapsedMs: time.Since(started).Milliseconds(),
			})
			return
		}
		if result.rejected() {
			writeChatWebInvokeResult(w, stream, http.StatusOK, &chatWebInvokeResponse{
				Status:    "rejected",
				Reason:    "input rejected by command gate",
				SessionID: currentSessionID,
				ElapsedMs: time.Since(started).Milliseconds(),
			})
			return
		}
	}

	deadline := time.Now().Add(chatWebInvokeTimeout(req.TimeoutMs))
	resp := chatWebInvokeWait(r.Context(), session, watch, baselineAssistant, deadline)
	resp.ElapsedMs = time.Since(started).Milliseconds()
	resp.Usage = chatWebInvokeUsageFrom(session)
	if req.WaitOnly {
		resp.Queued = false
	}
	storeChatWebInvokeIdem(idemKey, resp)
	writeChatWebInvokeResult(w, stream, http.StatusOK, resp)
}

// startChatWebInvokeStream 在调用方声明 Accept: text/event-stream 时建立
// SSE 流：先发 start 帧，随后由观察器转发 delta/tool 事件，最后发 result 帧。
// 未声明流式（或 ResponseWriter 不支持 Flush）时返回 nil，调用方回退 JSON。
func startChatWebInvokeStream(w http.ResponseWriter, r *http.Request, sessionID string) *chatWebSSEStream {
	if !chatWebInvokeAcceptsSSE(r) {
		return nil
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	stream := newChatWebSSEStream(w, flusher)
	stream.start(nil)
	stream.writeEvent("start", map[string]interface{}{
		"status":     "waiting",
		"session_id": sessionID,
	}, "invoke.start")
	return stream
}

// writeChatWebInvokeResult 输出最终结果：流式模式写 result 帧并等待其落盘
// （客户端断开/写超时直接放弃），非流式写 JSON。
func writeChatWebInvokeResult(w http.ResponseWriter, stream *chatWebSSEStream, statusCode int, resp *chatWebInvokeResponse) {
	if stream == nil {
		writeWebAPIJSON(w, statusCode, resp)
		return
	}
	payload, err := chatWebInvokeResponseMap(resp)
	if err != nil {
		payload = map[string]interface{}{"status": resp.Status, "reason": resp.Reason}
	}
	payload["http_status"] = statusCode
	stream.writeEvent("result", payload, "invoke.result")
	stream.flush(3 * time.Second)
}

// chatWebInvokeResponseMap 把响应结构体转换为 SSE 帧可用的 map。
func chatWebInvokeResponseMap(resp *chatWebInvokeResponse) (map[string]interface{}, error) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// chatWebInvokeTimeout 把请求的 timeout_ms 钳制到 [min, max]；<=0 用默认值。
func chatWebInvokeTimeout(ms int) time.Duration {
	if ms <= 0 {
		return chatWebInvokeDefaultTimeoutMs * time.Millisecond
	}
	d := time.Duration(ms) * time.Millisecond
	if d < chatWebInvokeMinTimeoutMs*time.Millisecond {
		return chatWebInvokeMinTimeoutMs * time.Millisecond
	}
	if d > chatWebInvokeMaxTimeoutMs*time.Millisecond {
		return chatWebInvokeMaxTimeoutMs * time.Millisecond
	}
	return d
}

// ---------------------------------------------------------------------------
// EventBus 观察器（turn 活动信号）
// ---------------------------------------------------------------------------

// chatWebInvokeWatch 记录注入后 EventBus 上的 turn 活动信号。
// EventBus.Publish 在发布者 goroutine 内同步调用 handler，因此 observe 必须
// 只做加锁计数，不能阻塞。
type chatWebInvokeWatch struct {
	mu           sync.Mutex
	sessionID    string // 绑定的会话 ID；非空时忽略其他会话的事件
	lastActivity time.Time
	starts       int
	finishes     int
	interrupted  bool
	assistant    string
	// onStream 是流式 invoke 的事件转发回调（非空时按需转发 delta/tool 事件）；
	// 回调实现必须非阻塞（writeEvent 只做入队）。
	onStream func(event string, data map[string]interface{}, sourceEvent string)
}

func newChatWebInvokeWatch() *chatWebInvokeWatch {
	return &chatWebInvokeWatch{lastActivity: time.Now()}
}

func (w *chatWebInvokeWatch) observe(event runtimeevents.Event) {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.sessionID != "" && event.SessionID != "" && event.SessionID != w.sessionID {
		w.mu.Unlock()
		return
	}
	streamEvent := ""
	streamPayload := map[string]interface{}{}
	switch event.Type {
	case runtimechat.EventLLMRequestStarted, "llm.request.started":
		w.starts++
	case runtimechat.EventLLMRequestFinished, "llm.request.finished":
		w.finishes++
	case runtimechat.EventSessionInterrupted:
		w.interrupted = true
	case runtimechat.EventAssistantMessage, "assistant.message":
		if content := strings.TrimSpace(payloadStringValue(event.Payload["content"])); content != "" {
			w.assistant = content
		}
	case runtimechat.EventAssistantDelta, "assistant.delta":
		if text := streamEventText(event); text != "" {
			streamEvent = "assistant_delta"
			streamPayload["delta"] = text
		}
	case runtimechat.EventAssistantReasoning, runtimechat.EventAssistantReasoningDelta:
		if text := streamEventText(event); text != "" {
			streamEvent = "assistant_reasoning_delta"
			streamPayload["delta"] = text
		}
	case runtimechat.EventToolStarted:
		if name := strings.TrimSpace(payloadStringValue(event.Payload["name"])); name != "" {
			streamEvent = "tool_started"
			streamPayload["name"] = name
		}
	case runtimechat.EventToolFinished, "tool.completed":
		if name := strings.TrimSpace(payloadStringValue(event.Payload["name"])); name != "" {
			streamEvent = "tool_finished"
			streamPayload["name"] = name
		}
	}
	w.lastActivity = time.Now()
	onStream := w.onStream
	w.mu.Unlock()
	if onStream != nil && streamEvent != "" {
		onStream(streamEvent, streamPayload, streamEvent)
	}
}

// chatWebInvokeWatchState 是观察器的并发安全快照。
type chatWebInvokeWatchState struct {
	LastActivity time.Time
	Starts       int
	Finishes     int
	Interrupted  bool
	Assistant    string
}

func (w *chatWebInvokeWatch) snapshot() chatWebInvokeWatchState {
	if w == nil {
		return chatWebInvokeWatchState{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return chatWebInvokeWatchState{
		LastActivity: w.lastActivity,
		Starts:       w.starts,
		Finishes:     w.finishes,
		Interrupted:  w.interrupted,
		Assistant:    w.assistant,
	}
}

// subscribeChatWebInvokeWatch 订阅会话 host 级 EventBus（与 SSE 端点同源）。
// 无 host / 无 bus（测试或降级形态）时返回空 Unsubscribe，等待循环仍可通过
// 运行时状态轮询完成判定。
func subscribeChatWebInvokeWatch(session *ChatSession, watch *chatWebInvokeWatch) runtimeevents.Unsubscribe {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.EventBus == nil || watch == nil {
		return func() {}
	}
	watch.sessionID = currentRuntimeSessionID(session)
	return session.LocalRuntimeHost.EventBus.SubscribeCancelable("", watch.observe)
}

// ---------------------------------------------------------------------------
// 等待循环与结束判定
// ---------------------------------------------------------------------------

// chatWebInvokeSample 是等待循环的一拍采样，交给纯函数 chatWebInvokeDecide
// 判定是否可以结束等待（便于单元测试覆盖各分支，不依赖真实时钟/会话）。
type chatWebInvokeSample struct {
	Busy             bool
	Pending          int
	Consumed         bool // InputQueue 计数已回到 0（输入被主循环消费）
	BusyAfterConsume bool // 消费后至少采样到一次 busy
	Starts           int
	Finishes         int
	Interrupted      bool
	PendingApproval  bool
	PendingQuestion  bool
	AttentionStable  bool // 审批/提问状态连续命中 chatWebInvokeAttentionTicks 拍
	SinceActivity    time.Duration
	SinceConsumed    time.Duration
	Quiet            time.Duration
	NoLLMGrace       time.Duration
}

// chatWebInvokeDecide 根据一拍采样判定等待是否结束。
// 返回 (status, done)；done=false 表示继续等待。
//
//	completed  —— 观察到 turn 忙碌且 LLM 请求完成、会话空闲、队列为空且事件静默；
//	settled    —— 输入已被消费但未观察到 LLM turn（斜杠命令等），宽限期后按就绪返回；
//	interrupted—— 会话被中断且已空闲；
//	requires_approval / requires_answer —— 会话停在审批/提问等待，需要调用方决策。
func chatWebInvokeDecide(s chatWebInvokeSample) (string, bool) {
	if s.Interrupted && !s.Busy && s.Pending == 0 {
		return "interrupted", true
	}
	if s.PendingApproval && s.AttentionStable {
		return "requires_approval", true
	}
	if s.PendingQuestion && s.AttentionStable {
		return "requires_answer", true
	}
	if s.BusyAfterConsume && s.Finishes > 0 && !s.Busy && s.Pending == 0 && s.SinceActivity >= s.Quiet {
		return "completed", true
	}
	if s.Consumed && !s.BusyAfterConsume && !s.Busy && s.Pending == 0 &&
		s.SinceConsumed >= s.NoLLMGrace && s.SinceActivity >= s.Quiet {
		return "settled", true
	}
	return "", false
}

// chatWebInvokeWait 轮询等待注入的 turn 结束，返回最终响应。
// ctx 由 HTTP 请求派生：调用方断开时立即停止等待（不再注入/订阅）。
func chatWebInvokeWait(ctx context.Context, session *ChatSession, watch *chatWebInvokeWatch, baselineAssistant string, deadline time.Time) *chatWebInvokeResponse {
	resp := &chatWebInvokeResponse{Status: "timeout", Queued: true}
	pinnedSessionID := currentRuntimeSessionID(session)
	var (
		consumed         bool
		consumedAt       time.Time
		busyAfterConsume bool
		attentionTicks   int
	)
	ticker := time.NewTicker(chatWebInvokePollInterval)
	defer ticker.Stop()
	for {
		if time.Now().After(deadline) {
			return chatWebInvokeFinalize(resp, chatWebSession(), watch, baselineAssistant,
				"timeout", "deadline exceeded while waiting for turn to finish")
		}
		select {
		case <-ctx.Done():
			return chatWebInvokeFinalize(resp, chatWebSession(), watch, baselineAssistant,
				"error", "client disconnected")
		case <-ticker.C:
		}

		current := chatWebSession()
		if current == nil {
			return chatWebInvokeFinalize(resp, session, watch, baselineAssistant,
				"error", "session closed while waiting")
		}
		if pinnedSessionID != "" && currentRuntimeSessionID(current) != pinnedSessionID {
			return chatWebInvokeFinalize(resp, current, watch, baselineAssistant,
				"error", "session switched while waiting for turn to finish")
		}
		_, _, busy, approval, question := chatWebInvokeProbeFn(current)
		pending := 0
		if current.InputQueue != nil {
			pending = current.InputQueue.queuedSubmissionCount()
		}
		if pending == 0 && !consumed {
			consumed = true
			consumedAt = time.Now()
		}
		if consumed && busy {
			busyAfterConsume = true
		}
		if approval != nil || question != nil {
			attentionTicks++
		} else {
			attentionTicks = 0
		}
		ws := watch.snapshot()
		sample := chatWebInvokeSample{
			Busy:             busy,
			Pending:          pending,
			Consumed:         consumed,
			BusyAfterConsume: busyAfterConsume,
			Starts:           ws.Starts,
			Finishes:         ws.Finishes,
			Interrupted:      ws.Interrupted,
			PendingApproval:  approval != nil,
			PendingQuestion:  question != nil,
			AttentionStable:  attentionTicks >= chatWebInvokeAttentionTicks,
			Quiet:            chatWebInvokeQuietWindow,
			NoLLMGrace:       chatWebInvokeNoLLMGrace,
		}
		if !ws.LastActivity.IsZero() {
			sample.SinceActivity = time.Since(ws.LastActivity)
		} else {
			sample.SinceActivity = chatWebInvokeQuietWindow
		}
		if consumed && !consumedAt.IsZero() {
			sample.SinceConsumed = time.Since(consumedAt)
		}
		if status, done := chatWebInvokeDecide(sample); done {
			return chatWebInvokeFinalize(resp, current, watch, baselineAssistant, status, "")
		}
	}
}

// chatWebInvokeProbe 解析当前会话的运行状态：session/turn 标识、是否忙碌、
// 待处理审批/提问（结构与 SSE connected 事件保持一致）。
func chatWebInvokeProbe(session *ChatSession) (
	sessionID string,
	turnID string,
	busy bool,
	pendingApproval map[string]interface{},
	pendingQuestion map[string]interface{},
) {
	if session == nil {
		return "", "", false, nil, nil
	}
	sessionID = currentRuntimeSessionID(session)
	actor := chatWebSessionActor(session)
	if actor == nil {
		return sessionID, "", false, nil, nil
	}
	state := actor.State()
	if state == nil {
		return sessionID, "", false, nil, nil
	}
	turnID = state.CurrentTurnID
	busy = state.Summary().Busy()
	if state.PendingApproval != nil {
		pendingApproval = map[string]interface{}{
			"request_id": state.PendingApproval.ID,
			"tool_name":  state.PendingApproval.ToolName,
			"prompt":     state.PendingApproval.Reason,
		}
	}
	if state.PendingQuestion != nil {
		pendingQuestion = map[string]interface{}{
			"question_id": state.PendingQuestion.ID,
			"prompt":      state.PendingQuestion.Prompt,
			"suggestions": state.PendingQuestion.Suggestions,
		}
	}
	return sessionID, turnID, busy, pendingApproval, pendingQuestion
}

// chatWebInvokeFinalize 填充最终响应：状态、运行快照、assistant 回复与
// TUI 渲染快照（screen 与 /debug/chat/screen、/web/api/screen?view=tui 同源）。
func chatWebInvokeFinalize(resp *chatWebInvokeResponse, session *ChatSession, watch *chatWebInvokeWatch, baselineAssistant, status, reason string) *chatWebInvokeResponse {
	if resp == nil {
		resp = &chatWebInvokeResponse{}
	}
	resp.Status = status
	resp.Reason = reason
	if session != nil {
		sessionID, turnID, busy, approval, question := chatWebInvokeProbeFn(session)
		if resp.SessionID == "" {
			resp.SessionID = sessionID
		}
		resp.TurnID = turnID
		resp.Busy = busy
		resp.PendingApproval = approval
		resp.PendingQuestion = question
		if session.InputQueue != nil {
			resp.Pending = session.InputQueue.queuedSubmissionCount()
		}
	}
	ws := watch.snapshot()
	resp.LLMObserved = ws.Starts > 0 || ws.Finishes > 0
	assistant := strings.TrimSpace(ws.Assistant)
	if assistant == "" && session != nil {
		assistant = chatWebInvokeAssistantContent(session)
	}
	if assistant != "" && assistant != baselineAssistant {
		resp.Assistant = &chatWebScreenMessage{Role: "assistant", Content: assistant}
	}
	resp.Screen = BuildChatDebugScreenSnapshot()
	return resp
}

// chatWebInvokeAssistantContent 返回当前会话最后一条 assistant 消息正文；
// 用于在 EventBus assistant_message 事件缺失时回退提取本轮回复。
func chatWebInvokeAssistantContent(session *ChatSession) string {
	if session == nil {
		return ""
	}
	messages := session.Messages
	if len(messages) == 0 && session.RuntimeSession != nil {
		messages = session.RuntimeSession.History
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		if content := strings.TrimSpace(messages[i].Content); content != "" {
			return content
		}
	}
	return ""
}
