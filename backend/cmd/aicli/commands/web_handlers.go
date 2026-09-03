package commands

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// ---------------------------------------------------------------------------
// 全局互斥锁（§4.2.4 — 并发安全）
// ---------------------------------------------------------------------------

// webInputMu 防止多个 Web 客户端同时注入输入。
var webInputMu sync.Mutex

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// chatWebSession 返回当前活动会话，供 SSE 端点每次事件时重新解析（§8.5）。
func chatWebSession() *ChatSession {
	return chatDebugDisplaySession()
}

// chatWebSessionActor 返回当前会话对应的 SessionActor。
func chatWebSessionActor(session *ChatSession) *runtimechat.SessionActor {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return nil
	}
	sessionID := currentRuntimeSessionID(session)
	if sessionID == "" {
		return nil
	}
	actor, ok := session.LocalRuntimeHost.SessionHub.Get(sessionID)
	if !ok {
		return nil
	}
	return actor
}

// writeWebAPIJSON 写入 JSON 响应并设置 Content-Type。
func writeWebAPIJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}

// ---------------------------------------------------------------------------
// HTTP 处理函数
// ---------------------------------------------------------------------------

// HandleChatWebAPIScreen 返回当前屏幕合成帧（§4.2.2）。
//   - ?format=text（默认）：纯文本面板内容
//   - ?format=json：结构化 JSON 快照
func HandleChatWebAPIScreen(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("format") == "json" {
		body, err := MarshalChatDebugScreenJSON()
		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(BuildChatDebugScreenText()))
}

// HandleChatWebAPIStatus 返回当前渲染器状态快照（§4.2.6）。
//   - ?format=json（默认）：结构化 JSON 快照
//   - ?format=text：纯文本摘要
func HandleChatWebAPIStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(BuildChatDebugDisplayText()))
		return
	}
	body, err := MarshalChatDebugDisplayJSON()
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}

// HandleChatWebAPIEventsSchema 返回 SSE 事件类型定义文档（§4.2.5）。
func HandleChatWebAPIEventsSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(chatWebSSESchema())
}

// ---------------------------------------------------------------------------
// SSE 发射器（§4.2.3 — 带互斥锁）
// ---------------------------------------------------------------------------

// chatWebSSEStream 是受互斥锁保护的 SSE 写入器。
type chatWebSSEStream struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
	seq     int64
	lastAt  time.Time
}

// newChatWebSSEStream 创建 SSE 发射器。
func newChatWebSSEStream(w http.ResponseWriter, flusher http.Flusher) *chatWebSSEStream {
	return &chatWebSSEStream{w: w, flusher: flusher, lastAt: time.Now()}
}

// writeEvent 写入一个 SSE 事件（event: + data: 两行），并递增连接内序列号。
// data 会被套上 _event 信封（sequence / schema_version / timestamp / source_event）。
func (s *chatWebSSEStream) writeEvent(event string, data map[string]interface{}, sourceEvent string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	data["_event"] = map[string]interface{}{
		"sequence":       s.seq,
		"schema_version": chatWebSchemaVersion,
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"source_event":   sourceEvent,
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	var sb strings.Builder
	sb.WriteString("event: ")
	sb.WriteString(event)
	sb.WriteString("\n")
	sb.WriteString("data: ")
	sb.WriteString(string(payload))
	sb.WriteString("\n\n")
	_, _ = s.w.Write([]byte(sb.String()))
	s.flusher.Flush()
	s.lastAt = time.Now()
}

// keepalive 写入一行 SSE 注释（`: keepalive`），维持连接存活。
func (s *chatWebSSEStream) keepalive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write([]byte(": keepalive\n\n"))
	s.flusher.Flush()
	s.lastAt = time.Now()
}

// lastEventAge 返回距最近一次写入的时间。
func (s *chatWebSSEStream) lastEventAge() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastAt)
}

// ---------------------------------------------------------------------------
// GET /web/api/events — SSE（§4.2.3）
// ---------------------------------------------------------------------------

// chatWebEventKeepaliveInterval 是无事件时发送 `: keepalive` 注释的周期。
const chatWebEventKeepaliveInterval = 15 * time.Second

// chatWebEventHeartbeatInterval 是无事件超过该时长后发送 heartbeat 事件的阈值。
const chatWebEventHeartbeatInterval = 30 * time.Second

// chatWebEventResubscribeInterval 是重新解析当前会话 EventBus 的周期（§8.5）。
const chatWebEventResubscribeInterval = 2 * time.Second

// HandleChatWebAPIEvents 提供 SSE 事件流。
//
// 实现要点（§4.2.3 + §8.5）：
//  1. 先发送 connected 事件（session_active/session_id/session_busy/turn_id/
//     last_sequence/server_version，以及可选的 pending_approval/pending_question）；
//  2. 订阅 session.LocalRuntimeHost.EventBus（host 级总线），事件经 §5.1 映射后转发；
//  3. 每次事件处理时重新解析 chatDebugDisplaySession()，若 EventBus 实例变化
//     （会话切换/重建）则重新订阅并取消旧订阅；
//  4. 定期发送 `: keepalive` 注释；无事件超过 30s 时发送 heartbeat 事件；
//  5. 连接断开（r.Context() 取消）时 Unsubscribe 并停止定时器，避免泄漏。
func HandleChatWebAPIEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	stream := newChatWebSSEStream(w, flusher)

	// 1. connected 首事件
	stream.writeEvent("connected", chatWebConnectedPayload(chatWebSession()), "connected")

	// 订阅管理（§8.5）：curBus 与 unsub 只在本 handler 的 goroutine 间通过 subMu 访问。
	var (
		subMu  sync.Mutex
		curBus *runtimeevents.Bus
		unsub  runtimeevents.Unsubscribe = func() {}
	)

	// 事件转发 handler：由 EventBus.Publish 调用（发布者 goroutine）。
	onEvent := func(ev runtimeevents.Event) {
		// 每次事件时重新解析当前会话（§8.5），按会话过滤。
		sessionID := ""
		if session := chatWebSession(); session != nil {
			sessionID = currentRuntimeSessionID(session)
		}
		if sessionID != "" && ev.SessionID != "" && ev.SessionID != sessionID {
			return
		}
		name, _ := chatWebSSEEventName(ev.Type)
		data := chatWebSSEDataForEvent(ev)
		stream.writeEvent(name, data, ev.Type)
		// 方法二屏幕刷新辅助：关键低频事件后附带 screen_refresh 提示（§8.6）。
		switch name {
		case "turn_end", "session_end", "session_interrupted", "error":
			stream.writeEvent("screen_refresh", map[string]interface{}{
				"reason": name,
			}, "screen_refresh")
		}
	}

	// 重新订阅循环（§8.5）：周期检查当前会话 EventBus 是否变化。
	go func() {
		ticker := time.NewTicker(chatWebEventResubscribeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var bus *runtimeevents.Bus
				if session := chatWebSession(); session != nil && session.LocalRuntimeHost != nil {
					bus = session.LocalRuntimeHost.EventBus
				}
				subMu.Lock()
				if bus != curBus {
					unsub()
					unsub = func() {}
					if bus != nil {
						unsub = bus.SubscribeCancelable("", onEvent)
					}
					curBus = bus
				}
				subMu.Unlock()
			}
		}
	}()

	// 主循环：keepalive 注释 + heartbeat。
	ticker := time.NewTicker(chatWebEventKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			subMu.Lock()
			unsub()
			subMu.Unlock()
			return
		case <-ticker.C:
			if stream.lastEventAge() >= chatWebEventHeartbeatInterval {
				stream.writeEvent("heartbeat", map[string]interface{}{
					"timestamp": time.Now().UTC().Format(time.RFC3339),
				}, "heartbeat")
				continue
			}
			stream.keepalive()
		}
	}
}

// ---------------------------------------------------------------------------
// POST /web/api/input — 注入输入（§4.2.4）
// ---------------------------------------------------------------------------

// chatWebInputRequest 是 POST /web/api/input 的请求体。
// type 缺省时按普通 prompt 处理；type=approval / type=question_answer 走反向交互通道。
type chatWebInputRequest struct {
	Type       string `json:"type"`
	Prompt     string `json:"prompt"`
	RequestID  string `json:"request_id"`
	Allow      bool   `json:"allow"`
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
}

// HandleChatWebAPIInput 注入用户输入（prompt / 审批决议 / 提问回答）。
// 全部路径由 webInputMu 互斥保护，防止多个 Web 客户端同时注入。
func HandleChatWebAPIInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "rejected",
			"reason": "method not allowed",
		})
		return
	}

	webInputMu.Lock()
	defer webInputMu.Unlock()

	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": "no active chat session",
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "read body: " + err.Error(),
		})
		return
	}

	var req chatWebInputRequest
	if jsonErr := json.Unmarshal(body, &req); jsonErr != nil {
		// 兼容 text/plain：整个 body 视为 prompt。
		req.Prompt = strings.TrimSpace(string(body))
	}

	switch strings.ToLower(strings.TrimSpace(req.Type)) {
	case "approval":
		handleWebApproval(w, session, req.RequestID, req.Allow)
	case "question_answer":
		handleWebQuestionAnswer(w, session, req.QuestionID, req.Answer)
	case "interrupt":
		handleWebInterrupt(w, session)
	default:
		handleWebPrompt(w, session, req.Prompt)
	}
}

// handleWebInterrupt 中断当前正在执行的 turn（§4.2.4 扩展）。
//
// 语义与终端运行期 Esc 一致，但走 ChatSession.Interrupt()（丢弃排队输入，
// 由 Web 端停止按钮使用）：幂等，无运行中 turn 时也安全（仅置中断标记并
// 触发一次清理，随后由前端收到的 session_interrupted/turn_end 事件复位）。
func handleWebInterrupt(w http.ResponseWriter, session *ChatSession) {
	session.Interrupt()
	writeWebAPIJSON(w, http.StatusOK, map[string]string{"status": "interrupted"})
}

// handleWebPrompt 路由普通 prompt 到 InputQueue（§4.2.4 步骤 3-5）。
func handleWebPrompt(w http.ResponseWriter, session *ChatSession, prompt string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "empty prompt",
		})
		return
	}
	queue := ensureChatBufferedInputQueue(session)
	if queue == nil {
		writeWebAPIJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "error",
			"reason": "input queue unavailable",
		})
		return
	}
	// Web 页面需要打字机效果：确保本轮及后续轮次 LLM 走流式输出，
	// 从而实时产生 assistant_delta / reasoning_delta 增量事件（§5.1）。
	session.Stream = true
	if actor := chatWebSessionActor(session); actor != nil {
		actor.EnableStreaming()
	}
	// 若队列未处于外部捕获模式，挂起 KeyHandler，避免与 Web 输入竞争（§4.2.4 步骤 4）。
	if !queue.hasExternalInputCaptureActive() {
		queue.setExternalInputCaptureActive(true)
	}
	result := queue.routeInputText(prompt)
	switch {
	case result.queued():
		// 交互式 TTY 模式下主循环阻塞在 composer 读取中；唤醒它，
		// 使其在下一轮循环中优先消费输入队列里的 Web 输入（§8.3 竞态修复）。
		// 非 TTY 场景（管道/PTY/普通文件重定向）同样阻塞在可唤醒的读取中，
		// 统一通过 composerWakeCancel 唤醒，下一轮 chatInteractiveReadLine
		// 会优先检查输入队列。
		session.wakeComposerRead()
		writeWebAPIJSON(w, http.StatusOK, map[string]string{"status": "queued"})
	case result.rejected():
		writeWebAPIJSON(w, http.StatusOK, map[string]string{
			"status": "rejected",
			"reason": "input rejected by command gate",
		})
	default:
		writeWebAPIJSON(w, http.StatusOK, map[string]string{"status": "queued"})
	}
}

// handleWebApproval 提交审批决议（§4.2.4 步骤 6）。
func handleWebApproval(w http.ResponseWriter, session *ChatSession, requestID string, allow bool) {
	if strings.TrimSpace(requestID) == "" {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "request_id is required",
		})
		return
	}
	actor := chatWebSessionActor(session)
	if actor == nil {
		writeWebAPIJSON(w, http.StatusOK, map[string]string{
			"status": "not_found",
			"reason": "session actor not available",
		})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := actor.ApproveTool(ctx, requestID, allow); err != nil {
		writeWebAPIJSON(w, http.StatusOK, map[string]string{
			"status": "error",
			"reason": err.Error(),
		})
		return
	}
	// 双入口一致性：审批已被 web 端解决，唤醒 console 端可能挂起的优先级读取，
	// 使其通过哨兵错误跳过本次提示（见 chat_input_queue.go）。
	chatSignalPriorityResolvedElsewhere(session)
	writeWebAPIJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// handleWebQuestionAnswer 提交提问回答（§4.2.4 步骤 6）。
func handleWebQuestionAnswer(w http.ResponseWriter, session *ChatSession, questionID, answer string) {
	if strings.TrimSpace(questionID) == "" {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "question_id is required",
		})
		return
	}
	actor := chatWebSessionActor(session)
	if actor == nil {
		writeWebAPIJSON(w, http.StatusOK, map[string]string{
			"status": "not_found",
			"reason": "session actor not available",
		})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := actor.AnswerQuestion(ctx, questionID, answer); err != nil {
		writeWebAPIJSON(w, http.StatusOK, map[string]string{
			"status": "error",
			"reason": err.Error(),
		})
		return
	}
	// 双入口一致性：问题已被 web 端回答，唤醒 console 端可能挂起的优先级读取。
	chatSignalPriorityResolvedElsewhere(session)
	writeWebAPIJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// ---------------------------------------------------------------------------
// GET /web/api/sessions — 会话列表（§4.2.8）
// ---------------------------------------------------------------------------

// chatWebSessionListItem 是 GET /web/api/sessions 响应的单个会话条目。
type chatWebSessionListItem struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Summary      string    `json:"summary,omitempty"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Current      bool      `json:"current,omitempty"`
}

// HandleChatWebAPISessions 返回可恢复的历史会话列表（含当前会话，带 current 标记）。
//
// 响应结构：
//
//	{
//	  "sessions": [ {id,title,summary,message_count,created_at,updated_at,current}, ... ],
//	  "current_session_id": "<当前会话 ID，无会话时为空>"
//	}
//
// 列表顺序按 sort 参数决定：
//   - ?sort=created_at（默认）→ 按 CreatedAt 降序，当前会话带 current 标记但不置顶
//   - ?sort=updated_at      → 按 UpdatedAt 降序
// 列表项来自 listResumeCandidateChatSessions（已排除当前会话并过滤无对话的空会话），
// 与 TTY /resume 选择器的候选集一致。
func HandleChatWebAPISessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "error",
			"reason": "method not allowed",
		})
		return
	}

	session := chatWebSession()
	if session == nil || session.SessionManager == nil {
		writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{
			"sessions":           []chatWebSessionListItem{},
			"current_session_id": "",
		})
		return
	}

	currentID := currentRuntimeSessionID(session)
	candidates, err := listResumeCandidateChatSessions(
		session.SessionManager,
		session.SessionUserID,
		session.SessionFilter,
		currentID,
	)
	if err != nil {
		writeWebAPIJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "error",
			"reason": err.Error(),
		})
		return
	}

	items := make([]chatWebSessionListItem, 0, len(candidates)+1)
	if current := session.RuntimeSession; current != nil {
		items = append(items, buildChatWebSessionListItem(current, true))
	}
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		items = append(items, buildChatWebSessionListItem(candidate, false))
	}

	// 解析排序参数，默认按创建时间降序。
	sortByUpdatedAt := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort"))) == "updated_at"
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		lt, rt := left.CreatedAt, right.CreatedAt
		if sortByUpdatedAt {
			lt, rt = left.UpdatedAt, right.UpdatedAt
		}
		if !lt.Equal(rt) {
			return lt.After(rt)
		}
		return strings.TrimSpace(left.ID) < strings.TrimSpace(right.ID)
	})

	writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{
		"sessions":           items,
		"current_session_id": currentID,
	})
}

// buildChatWebSessionListItem 从 runtimechat.Session 构建列表条目。
// 标题为空时回退为 "(untitled)"，保持列表可扫描。
func buildChatWebSessionListItem(s *runtimechat.Session, current bool) chatWebSessionListItem {
	item := chatWebSessionListItem{
		ID:      s.ID,
		Current: current,
	}
	if preview := s.BuildPreview(); preview != nil {
		item.Title = strings.TrimSpace(preview.Title)
		item.Summary = strings.TrimSpace(preview.Summary)
		item.MessageCount = preview.MessageCount
		item.CreatedAt = preview.CreatedAt
		item.UpdatedAt = preview.UpdatedAt
	}
	if item.Title == "" {
		item.Title = "(untitled)"
	}
	return item
}

// ---------------------------------------------------------------------------
// POST /web/api/sessions/resume — 恢复历史会话（§4.2.9）
// ---------------------------------------------------------------------------

// chatWebSessionsResumeRequest 是 POST /web/api/sessions/resume 的请求体。
type chatWebSessionsResumeRequest struct {
	SessionID string `json:"session_id"`
}

// HandleChatWebAPISessionsResume 将 "/resume <session-id>" 注入输入队列，
// 由主循环安全地执行会话切换。
//
// 与 POST /web/api/input 的 prompt 注入共用同一机制（输入队列 +
// wakeComposerRead），保证会话状态只被主循环单写者修改，避免 HTTP
// goroutine 与正在运行的 turn 竞态。注入成功后 SSE 会继续投递
// session_end/session_start/screen_refresh，前端据此刷新屏幕。
func HandleChatWebAPISessionsResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "error",
			"reason": "method not allowed",
		})
		return
	}

	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": "no active chat session",
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "read body: " + err.Error(),
		})
		return
	}

	var req chatWebSessionsResumeRequest
	if jsonErr := json.Unmarshal(body, &req); jsonErr != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "invalid JSON: " + jsonErr.Error(),
		})
		return
	}
	targetID := strings.TrimSpace(req.SessionID)
	if targetID == "" {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "session_id is required",
		})
		return
	}

	// 校验目标会话存在且属于当前用户，避免注入无效命令。
	manager := session.SessionManager
	if manager == nil {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": "session manager unavailable",
		})
		return
	}
	target, err := manager.Get(context.Background(), targetID)
	if err != nil {
		writeWebAPIJSON(w, http.StatusNotFound, map[string]string{
			"status": "error",
			"reason": "session not found",
		})
		return
	}
	if target.UserID != session.SessionUserID {
		writeWebAPIJSON(w, http.StatusForbidden, map[string]string{
			"status": "error",
			"reason": "session belongs to another user",
		})
		return
	}
	if currentID := currentRuntimeSessionID(session); currentID != "" && strings.EqualFold(currentID, targetID) {
		writeWebAPIJSON(w, http.StatusOK, map[string]string{
			"status":     "already_current",
			"session_id": targetID,
		})
		return
	}

	queue := ensureChatBufferedInputQueue(session)
	if queue == nil {
		writeWebAPIJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "error",
			"reason": "input queue unavailable",
		})
		return
	}
	result := queue.routeInputText("/resume " + targetID)
	switch {
	case result.queued():
		session.wakeComposerRead()
		writeWebAPIJSON(w, http.StatusOK, map[string]string{
			"status":     "queued",
			"session_id": targetID,
		})
	case result.rejected():
		writeWebAPIJSON(w, http.StatusOK, map[string]string{
			"status": "rejected",
			"reason": "input rejected by command gate",
		})
	default:
		writeWebAPIJSON(w, http.StatusOK, map[string]string{
			"status":     "queued",
			"session_id": targetID,
		})
	}
}

// ---------------------------------------------------------------------------
// POST /web/api/sessions/delete — 删除历史会话（§3.5）
// ---------------------------------------------------------------------------

// chatWebSessionsDeleteRequest 是 POST /web/api/sessions/delete 的请求体。
type chatWebSessionsDeleteRequest struct {
	SessionID string `json:"session_id"`
}

// HandleChatWebAPISessionsDelete 删除一个非当前历史会话。
//
// 安全约束与 resume 一致：会话必须存在且属于当前用户；当前活动会话
// 不可删除（主循环持有其引用，删除会导致会话状态与列表不一致）。
// 删除成功只影响存储层与会话列表，SSE 流不受影响。
func HandleChatWebAPISessionsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "error",
			"reason": "method not allowed",
		})
		return
	}

	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": "no active chat session",
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "read body: " + err.Error(),
		})
		return
	}

	var req chatWebSessionsDeleteRequest
	if jsonErr := json.Unmarshal(body, &req); jsonErr != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "invalid JSON: " + jsonErr.Error(),
		})
		return
	}
	targetID := strings.TrimSpace(req.SessionID)
	if targetID == "" {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "session_id is required",
		})
		return
	}

	manager := session.SessionManager
	if manager == nil {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": "session manager unavailable",
		})
		return
	}
	target, err := manager.Get(context.Background(), targetID)
	if err != nil {
		writeWebAPIJSON(w, http.StatusNotFound, map[string]string{
			"status": "error",
			"reason": "session not found",
		})
		return
	}
	if target.UserID != session.SessionUserID {
		writeWebAPIJSON(w, http.StatusForbidden, map[string]string{
			"status": "error",
			"reason": "session belongs to another user",
		})
		return
	}
	if currentID := currentRuntimeSessionID(session); currentID != "" && strings.EqualFold(currentID, targetID) {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": "cannot delete current session",
		})
		return
	}

	if err := manager.Delete(context.Background(), targetID); err != nil {
		writeWebAPIJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "error",
			"reason": "delete failed: " + err.Error(),
		})
		return
	}

	writeWebAPIJSON(w, http.StatusOK, map[string]string{
		"status":     "ok",
		"session_id": targetID,
	})
}

// ---------------------------------------------------------------------------
// POST /web/api/sessions/rename — 重命名会话（§3.5）
// ---------------------------------------------------------------------------

// chatWebSessionsRenameRequest 是 POST /web/api/sessions/rename 的请求体。
type chatWebSessionsRenameRequest struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
}

// chatWebSessionTitleMaxRunes 是会话标题允许的最大字符数（按 rune 计）。
const chatWebSessionTitleMaxRunes = 100

// HandleChatWebAPISessionsRename 重命名一个会话。
//
// 通过 manager.SetTitle 持久化到存储层；若目标是当前活动会话，同时更新
// 内存中 RuntimeSession 的 Metadata，保证列表与屏幕状态一致（主循环
// 单写者原则下，这里只改元数据标题，不触碰对话内容）。
func HandleChatWebAPISessionsRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "error",
			"reason": "method not allowed",
		})
		return
	}

	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": "no active chat session",
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "read body: " + err.Error(),
		})
		return
	}

	var req chatWebSessionsRenameRequest
	if jsonErr := json.Unmarshal(body, &req); jsonErr != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "invalid JSON: " + jsonErr.Error(),
		})
		return
	}
	targetID := strings.TrimSpace(req.SessionID)
	if targetID == "" {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "session_id is required",
		})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "title is required",
		})
		return
	}
	if utf8.RuneCountInString(title) > chatWebSessionTitleMaxRunes {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "title too long (max 100 characters)",
		})
		return
	}

	manager := session.SessionManager
	if manager == nil {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": "session manager unavailable",
		})
		return
	}
	target, err := manager.Get(context.Background(), targetID)
	if err != nil {
		writeWebAPIJSON(w, http.StatusNotFound, map[string]string{
			"status": "error",
			"reason": "session not found",
		})
		return
	}
	if target.UserID != session.SessionUserID {
		writeWebAPIJSON(w, http.StatusForbidden, map[string]string{
			"status": "error",
			"reason": "session belongs to another user",
		})
		return
	}

	if err := manager.SetTitle(context.Background(), targetID, title); err != nil {
		writeWebAPIJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "error",
			"reason": "rename failed: " + err.Error(),
		})
		return
	}

	// 当前会话：同步内存中的 RuntimeSession 元数据标题。
	if currentID := currentRuntimeSessionID(session); currentID != "" &&
		strings.EqualFold(currentID, targetID) && session.RuntimeSession != nil {
		session.RuntimeSession.Metadata.Title = title
	}

	writeWebAPIJSON(w, http.StatusOK, map[string]string{
		"status":     "ok",
		"session_id": targetID,
		"title":      title,
	})
}

// ---------------------------------------------------------------------------
// SSE 事件 schema 文档（§4.2.5）
// ---------------------------------------------------------------------------

// webSSEFieldSpec 描述一个 SSE data 字段。
type webSSEFieldSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// webSSEEventSpec 描述一个 SSE 事件定义。
type webSSEEventSpec struct {
	Event       string            `json:"event"`
	Description string            `json:"description"`
	SourceEvent string            `json:"source_event"`
	Fields      []webSSEFieldSpec `json:"fields"`
	Example     string            `json:"example"`
}

// chatWebSSESchema 返回静态事件类型文档（与 §5.1 映射表保持同步）。
func chatWebSSESchema() []webSSEEventSpec {
	specs := []webSSEEventSpec{
		{
			Event:       "connected",
			Description: "连接建立时的初始状态同步",
			SourceEvent: "",
			Fields: []webSSEFieldSpec{
				{Name: "session_active", Type: "boolean", Description: "是否存在活动会话"},
				{Name: "session_id", Type: "string", Description: "活动会话 ID"},
				{Name: "session_busy", Type: "boolean", Description: "会话是否忙碌"},
				{Name: "turn_id", Type: "string", Description: "当前 turn 标识"},
				{Name: "last_sequence", Type: "integer", Description: "连接内已确认的最后序列号"},
				{Name: "server_version", Type: "string", Description: "服务端版本"},
				{Name: "pending_approval", Type: "object", Description: "可选的待处理审批请求"},
				{Name: "pending_question", Type: "object", Description: "可选的待处理提问"},
			},
			Example: `{"session_active":true,"session_id":"sess_abc","session_busy":false,"last_sequence":0,"server_version":"aicli/1.0.0"}`,
		},
		{
			Event:       "heartbeat",
			Description: "无事件超过 30s 时发送，保持连接存活并通知序列号状态",
			SourceEvent: "",
			Fields: []webSSEFieldSpec{
				{Name: "timestamp", Type: "string (RFC3339)", Description: "心跳时间"},
			},
			Example: `{"timestamp":"2026-09-01T12:00:30Z"}`,
		},
		{
			Event:       "screen_refresh",
			Description: "屏幕内容已变化，前端应重新拉取 /web/api/screen",
			SourceEvent: "",
			Fields: []webSSEFieldSpec{
				{Name: "reason", Type: "string", Description: "触发原因（turn_end/session_end 等）"},
			},
			Example: `{"reason":"turn_end"}`,
		},
		{
			Event:       "error",
			Description: "订阅或处理异常",
			SourceEvent: "",
			Fields: []webSSEFieldSpec{
				{Name: "code", Type: "string", Description: "错误码"},
				{Name: "message", Type: "string", Description: "错误信息"},
				{Name: "turn_id", Type: "string", Description: "当前 turn 标识"},
			},
			Example: `{"code":"subscribe_failed","message":"event bus unavailable","turn_id":"turn_abc"}`,
		},
	}

	for _, m := range chatWebSSEMappings {
		specs = append(specs, webSSEEventSpec{
			Event:       m.SSEEvent,
			Description: m.Desc,
			SourceEvent: m.BusEvent,
			Fields:      chatWebSSEFieldsFor(m.BusEvent),
			Example:     chatWebSSEExampleFor(m.BusEvent),
		})
	}
	return specs
}

// chatWebSSEFieldsFor 返回指定 EventBus 事件的 data 字段定义。
func chatWebSSEFieldsFor(busEvent string) []webSSEFieldSpec {
	fields := []webSSEFieldSpec{
		{Name: "session_id", Type: "string", Description: "会话 ID"},
		{Name: "turn_id", Type: "string", Description: "当前 turn 标识"},
	}
	switch busEvent {
	case runtimechat.EventLLMRequestStarted:
		fields = append(fields,
			webSSEFieldSpec{Name: "request_id", Type: "string", Description: "LLM 请求标识"},
			webSSEFieldSpec{Name: "model", Type: "string", Description: "使用的模型名称"},
			webSSEFieldSpec{Name: "timestamp", Type: "string (RFC3339)", Description: "事件时间戳"},
		)
	case runtimechat.EventAssistantDelta:
		fields = append(fields,
			webSSEFieldSpec{Name: "stream_id", Type: "string", Description: "流式块标识"},
			webSSEFieldSpec{Name: "sequence", Type: "integer", Description: "流内序号"},
			webSSEFieldSpec{Name: "text", Type: "string", Description: "文本增量"},
		)
	case runtimechat.EventAssistantReasoningDelta, runtimechat.EventAssistantReasoning:
		fields = append(fields,
			webSSEFieldSpec{Name: "stream_id", Type: "string", Description: "流式块标识"},
			webSSEFieldSpec{Name: "sequence", Type: "integer", Description: "流内序号"},
			webSSEFieldSpec{Name: "content", Type: "string", Description: "推理增量内容"},
		)
	case runtimechat.EventLLMRequestFinished:
		fields = append(fields,
			webSSEFieldSpec{Name: "request_id", Type: "string", Description: "LLM 请求标识"},
			webSSEFieldSpec{Name: "finish_reason", Type: "string", Description: "结束原因"},
			webSSEFieldSpec{Name: "usage", Type: "object", Description: "token 用量"},
		)
	case runtimechat.EventToolStarted:
		fields = append(fields,
			webSSEFieldSpec{Name: "tool_name", Type: "string", Description: "工具名称"},
			webSSEFieldSpec{Name: "tool_call_id", Type: "string", Description: "工具调用标识"},
			webSSEFieldSpec{Name: "arguments", Type: "object", Description: "工具参数"},
		)
	case runtimechat.EventToolFinished:
		fields = append(fields,
			webSSEFieldSpec{Name: "tool_name", Type: "string", Description: "工具名称"},
			webSSEFieldSpec{Name: "tool_call_id", Type: "string", Description: "工具调用标识"},
			webSSEFieldSpec{Name: "result_summary", Type: "string", Description: "结果摘要"},
		)
	case runtimechat.EventApprovalRequested:
		fields = append(fields,
			webSSEFieldSpec{Name: "request_id", Type: "string", Description: "审批请求标识"},
			webSSEFieldSpec{Name: "tool_name", Type: "string", Description: "工具名称"},
			webSSEFieldSpec{Name: "prompt", Type: "string", Description: "审批提示"},
		)
	case runtimechat.EventApprovalResolved:
		fields = append(fields,
			webSSEFieldSpec{Name: "request_id", Type: "string", Description: "审批请求标识"},
			webSSEFieldSpec{Name: "allowed", Type: "boolean", Description: "是否允许"},
		)
	case runtimechat.EventQuestionAsked:
		fields = append(fields,
			webSSEFieldSpec{Name: "question_id", Type: "string", Description: "提问标识"},
			webSSEFieldSpec{Name: "prompt", Type: "string", Description: "提问内容"},
			webSSEFieldSpec{Name: "suggestions", Type: "array", Description: "建议答案"},
		)
	case runtimechat.EventQuestionAnswered:
		fields = append(fields,
			webSSEFieldSpec{Name: "question_id", Type: "string", Description: "提问标识"},
			webSSEFieldSpec{Name: "answer", Type: "string", Description: "用户回答"},
		)
	case runtimechat.EventCheckpointCreated:
		fields = append(fields,
			webSSEFieldSpec{Name: "checkpoint_id", Type: "string", Description: "checkpoint 标识"},
		)
	}
	return fields
}

// chatWebSSEExampleFor 返回指定 EventBus 事件的 data 示例。
func chatWebSSEExampleFor(busEvent string) string {
	switch busEvent {
	case runtimechat.EventLLMRequestStarted:
		return `{"turn_id":"turn_abc","request_id":"req_123","model":"gpt-4","timestamp":"2026-09-01T12:00:00Z"}`
	case runtimechat.EventAssistantDelta:
		return `{"turn_id":"turn_abc","stream_id":"s1","sequence":1,"text":"你好"}`
	case runtimechat.EventAssistantReasoningDelta, runtimechat.EventAssistantReasoning:
		return `{"turn_id":"turn_abc","stream_id":"s1","sequence":1,"content":"思考中"}`
	case runtimechat.EventLLMRequestFinished:
		return `{"turn_id":"turn_abc","request_id":"req_123","finish_reason":"stop","usage":{"input_tokens":10,"output_tokens":20}}`
	case runtimechat.EventToolStarted:
		return `{"turn_id":"turn_abc","tool_name":"grep","tool_call_id":"call_1","arguments":{"pattern":"foo"}}`
	case runtimechat.EventToolFinished:
		return `{"turn_id":"turn_abc","tool_name":"grep","tool_call_id":"call_1","result_summary":"3 matches"}`
	case runtimechat.EventApprovalRequested:
		return `{"turn_id":"turn_abc","request_id":"req_1","tool_name":"bash","prompt":"允许执行?"}`
	case runtimechat.EventApprovalResolved:
		return `{"turn_id":"turn_abc","request_id":"req_1","allowed":true}`
	case runtimechat.EventQuestionAsked:
		return `{"turn_id":"turn_abc","question_id":"q_1","prompt":"继续吗?","suggestions":["yes","no"]}`
	case runtimechat.EventQuestionAnswered:
		return `{"turn_id":"turn_abc","question_id":"q_1","answer":"yes"}`
	case runtimechat.EventCheckpointCreated:
		return `{"turn_id":"turn_abc","checkpoint_id":"cp_1"}`
	default:
		return `{"turn_id":"turn_abc"}`
	}
}

// HandleChatWebPage 定义于 web_page.go（go:embed 嵌入 web/index.html）。
