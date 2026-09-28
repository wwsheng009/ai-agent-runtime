package runtimeapi

import (
	"context"
	stderrors "errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// agentChatKeepaliveInterval 是 /api/agent/chat SSE 的存活注释帧周期。
// 与 /runtime/stream 的 15s 同源：前端读侧 45s 静默看门狗据此判定连接健康。
const agentChatKeepaliveInterval = 15 * time.Second

// agentChatStream 是 /api/agent/chat 流式请求的「请求级单写者」。
//
// 背景（2026-09-28 故障）：此前 SSE 响应头与首帧要等 session 获取、profile 解析、
// **会话租约排队**（同进程冲突最长 5 分钟）、工作区扫描、MCP 冷启动等前置步骤
// 全部走完才写出；前端 15s 连接守卫量的是「首帧」而不是「排队完成」，排队一旦
// 超过预算就被误判为 runtime 不可达（用户看到「连接 runtime 超时」）。
//
// 现在的契约：
//   - 入口即开流并立刻下发 `: open` 注释帧（首帧），随后每 15s 一条
//     `: keepalive`（仅在静默 ≥15s 时补发，活跃流不额外写入）；
//   - 前置步骤失败不再回 4xx/5xx JSON，而是走 SSE error 帧（payload 形状与
//     writeError 对齐，含 code/context/message），前端 onErrorEvent 直接消费；
//   - 整个流式请求只有这一个写出器：ReAct 分支、streamStaticResult、
//     streamLLMChat 全部复用它（同 session 单写者），避免多写者字节交错。
type agentChatStream struct {
	raw    http.ResponseWriter
	writer *pacedFlushWriter

	openedAt    time.Time
	firstFrame  atomic.Int64 // unix nano；首个出站字节（`: open`）
	lastWriteAt atomic.Int64 // unix nano；最近一次写入（任意帧）

	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
	closeOnce sync.Once
	started   atomic.Bool
}

func newAgentChatStream(w http.ResponseWriter, flushInterval time.Duration) *agentChatStream {
	flusher, _ := w.(http.Flusher)
	return &agentChatStream{
		raw:      w,
		writer:   newPacedFlushWriter(w, flusher, streamWriteBufferSize, flushInterval),
		openedAt: time.Now(),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Write/Header/WriteHeader/Flush/FlushNow 让 agentChatStream 直接充当
// http.ResponseWriter + http.Flusher：所有下游写者（轨迹发射器、静态结果、
// LLM 流）都拿到同一个它，从而共享单写者与活动时间戳。
func (s *agentChatStream) Write(p []byte) (int, error) {
	now := time.Now().UnixNano()
	s.lastWriteAt.Store(now)
	s.firstFrame.CompareAndSwap(0, now)
	return s.writer.Write(p)
}

func (s *agentChatStream) Header() http.Header        { return s.writer.Header() }
func (s *agentChatStream) WriteHeader(statusCode int) { s.writer.WriteHeader(statusCode) }
func (s *agentChatStream) Flush()                     { s.writer.Flush() }
func (s *agentChatStream) FlushNow() error            { return s.writer.FlushNow() }

// open 写下首帧（`: open` 注释帧）并启动 keepalive ticker。
// 首个 flush 不受合并窗口节流（pacedFlushWriter 的 lastFlush 为零 → 立即出站），
// 因此前端的「首帧」证据从这个字节开始计时。
func (s *agentChatStream) open() {
	writeSSEComment(s, "open")
	s.startKeepalive()
}

func (s *agentChatStream) startKeepalive() {
	if !s.started.CompareAndSwap(false, true) {
		return
	}
	go s.runKeepalive()
}

func (s *agentChatStream) runKeepalive() {
	defer close(s.done)
	ticker := time.NewTicker(agentChatKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			last := s.lastWriteAt.Load()
			if last != 0 && time.Since(time.Unix(0, last)) < agentChatKeepaliveInterval {
				// 流本来就活跃：不补无谓的注释帧，也避开与业务写者的交错窗口。
				continue
			}
			writeSSEComment(s, "keepalive")
		}
	}
}

// stopKeepalive 幂等停掉 ticker 并等待 goroutine 退出：返回后不再有任何
// 并发写者，收尾 Close 才安全。
func (s *agentChatStream) stopKeepalive() {
	if !s.started.Load() {
		return
	}
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
}

// Close 停止 keepalive 并推出缓冲余量。幂等；handler 返回前必须调用。
func (s *agentChatStream) Close() {
	s.closeOnce.Do(func() {
		s.stopKeepalive()
		_ = s.writer.Close()
	})
}

// agentChatStreamFrom 从已包装的 ResponseWriter 取回流句柄；非流式请求返回 nil。
func agentChatStreamFrom(w http.ResponseWriter) *agentChatStream {
	stream, _ := w.(*agentChatStream)
	return stream
}

// FirstFrameDelay 返回从开流到首帧出站的耗时（未出站时为 0）。
func (s *agentChatStream) FirstFrameDelay() time.Duration {
	ns := s.firstFrame.Load()
	if ns <= 0 {
		return 0
	}
	return time.Unix(0, ns).Sub(s.openedAt)
}

// EmitPayload 把任意 payload 作为 error 帧写出（连接已开，不能回 JSON）。
func (s *agentChatStream) EmitPayload(statusCode int, payload map[string]interface{}) {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	if _, ok := payload["message"]; !ok {
		if msg, ok := payload["error"].(string); ok {
			payload["message"] = msg
		}
	}
	if _, ok := payload["source"]; !ok {
		payload["source"] = "agent_chat"
	}
	writeSSEEventFrame(s, "error", payload, 0, 0)
	_ = s.FlushNow()
}

// EmitError 把一次错误转成 SSE error 帧；payload 与 writeError 对齐。
func (s *agentChatStream) EmitError(statusCode int, err error, turnID string, traceIDs ...string) {
	if err == nil {
		return
	}
	payload := buildErrorPayload(s.raw, err)
	if len(traceIDs) > 0 && strings.TrimSpace(traceIDs[0]) != "" {
		payload["trace_id"] = strings.TrimSpace(traceIDs[0])
	}
	if strings.TrimSpace(turnID) != "" {
		payload["turn_id"] = strings.TrimSpace(turnID)
	}
	// runtimeErr 的 context 里带着对前端有用的判定键（error_type/retryable/
	// suggested_action/lease），平铺到顶层，onErrorEvent 不需要再拆一层。
	if ctx, ok := payload["context"].(map[string]interface{}); ok {
		for key, value := range ctx {
			if _, exists := payload[key]; !exists {
				payload[key] = value
			}
		}
	}
	s.EmitPayload(statusCode, payload)
	logger.Warn("agent chat stream error frame emitted",
		logger.Status(statusCode),
		logger.Err(err),
	)
}

// EmitExecutionError 是 writeAgentChatExecutionError 的流式版本：仍走
// prompt-preflight 的修正/落库逻辑，只是把最终错误写进 SSE error 帧
// （响应头已发出，不能再改 X-Trace-ID，因此把 trace 放进帧内）。
func (s *agentChatStream) EmitExecutionError(ctx context.Context, h *Handler, statusCode int, err error, session *chat.Session, traceID, turnID string) {
	if err == nil {
		return
	}
	preparedErr, resolvedTraceID := h.prepareAgentChatExecutionError(ctx, err, session, traceID)
	s.EmitError(statusCode, preparedErr, turnID, resolvedTraceID)
}

// openAgentChatStream 在流式请求入口打开 SSE：先设响应头，再写 `: open`
// 首帧并启动 keepalive。非流式请求返回 (nil, nil)，语义完全不变。
func (h *Handler) openAgentChatStream(w http.ResponseWriter, r *http.Request, streaming bool) (*agentChatStream, error) {
	if !streaming {
		return nil, nil
	}
	flushInterval, err := resolveStreamFlushInterval(r)
	if err != nil {
		return nil, err
	}
	h.prepareSSEHeaders(w)
	stream := newAgentChatStream(w, flushInterval)
	stream.open()
	return stream, nil
}

// buildErrorPayload 构造与 writeError 相同的 JSON payload（不含 HTTP 写出），
// 让「JSON 错误」与「SSE error 帧」共享同一形状，前端两条路径都能渲染。
func buildErrorPayload(w http.ResponseWriter, err error) map[string]interface{} {
	response := map[string]interface{}{
		"error":   err.Error(),
		"message": err.Error(),
	}
	if requestID := strings.TrimSpace(w.Header().Get("X-Request-ID")); requestID != "" {
		response["request_id"] = requestID
	}
	if traceID := strings.TrimSpace(w.Header().Get("X-Trace-ID")); traceID != "" {
		response["trace_id"] = traceID
	}

	var runtimeErr *errors.RuntimeError
	if stderrors.As(err, &runtimeErr) {
		response["code"] = runtimeErr.Code
		response["context"] = runtimeErr.GetContext()
	}
	if preflightErr, ok := agent.AsPromptPreflightError(err); ok && preflightErr != nil {
		response["error_type"] = "prompt_preflight"
		for key, value := range preflightErr.Metadata() {
			response[key] = value
		}
	}
	return response
}

// sessionStoreErrorPayload 将会话存储错误映射为 (statusCode, payload)，
// 与 writeSessionStoreError 同源，供 JSON 与 SSE 两条出口复用。
func sessionStoreErrorPayload(w http.ResponseWriter, err error) (int, map[string]interface{}) {
	statusCode := http.StatusServiceUnavailable
	code := "STORE_UNAVAILABLE"
	message := err.Error()

	// 会话不存在是客户端语义（404），不是"存储不可用"（503）。此前统一
	// 落 503，前端把 503 当作可重试的服务故障（退避重试 + 降级横幅），
	// 已删除/失效会话的读取因此会在控制台反复报错，且掩盖了真正的存储
	// 故障。仅当错误确实是 not-found 时才降级为 404。
	if stderrors.Is(err, chat.ErrSessionNotFound) ||
		strings.Contains(strings.ToLower(message), "session not found") {
		code = "SESSION_NOT_FOUND"
		statusCode = http.StatusNotFound
	} else if strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database locked") {
		code = "STORE_LOCKED"
		message = "会话存储被其他进程（aicli CLI）锁定，请稍后重试。"
	} else if strings.Contains(message, "context deadline exceeded") ||
		strings.Contains(message, "deadline exceeded") {
		code = "STORE_TIMEOUT"
		message = "会话存储查询超时（共享数据库被并发写入占用），请稍后重试。"
	} else if strings.Contains(message, "database is busy") ||
		strings.Contains(message, "database busy") {
		code = "STORE_BUSY"
		message = "会话存储正忙，请稍后重试。"
	} else if strings.Contains(message, "context canceled") {
		code = "STORE_CANCELED"
		message = "会话存储请求被取消。"
		statusCode = http.StatusGatewayTimeout
	}

	response := map[string]interface{}{
		"error":   message,
		"message": message,
		"code":    code,
	}
	if w != nil {
		if requestID := strings.TrimSpace(w.Header().Get("X-Request-ID")); requestID != "" {
			response["request_id"] = requestID
		}
	}
	return statusCode, response
}

// sessionLeaseConflictRuntimeErr 把租约冲突错误规范化为 RuntimeError
// （error_type/retryable/suggested_action/lease 进 context），JSON 与 SSE
// 两条错误出口共用；非冲突错误返回 nil。
func sessionLeaseConflictRuntimeErr(err error) error {
	var conflict *chat.LeaseConflictError
	if !stderrors.As(err, &conflict) {
		return nil
	}
	runtimeErr := errors.New(errors.ErrSessionLeaseConflict, sessionLeaseConflictMessage(conflict))
	if conflict != nil && conflict.Lease != nil {
		runtimeErr = runtimeErr.
			WithContext("lease", conflict.Lease).
			WithContext("retryable", true).
			WithContext("error_type", "session_lease_conflict").
			WithContext("suggested_action", sessionLeaseConflictSuggestedAction(conflict.Lease))
	}
	return runtimeErr
}

// agentChatTiming 记录 /api/agent/chat 的分段耗时（item 4 可观测性）：
// 一次请求一条汇总日志，包含 session_get/lease/workspace/agent_build 等分段、
// 首帧延迟与总量。下次故障不再靠猜。
type agentChatTiming struct {
	start     time.Time
	last      time.Time
	phases    []string
	streaming bool
}

func newAgentChatTiming() *agentChatTiming {
	now := time.Now()
	return &agentChatTiming{start: now, last: now}
}

func (t *agentChatTiming) mark(name string) {
	if t == nil {
		return
	}
	now := time.Now()
	t.phases = append(t.phases, name+"="+strconv.FormatInt(now.Sub(t.last).Milliseconds(), 10)+"ms")
	t.last = now
}

func (t *agentChatTiming) log(sessionIDValue, turnID string, clientAborted bool, firstFrame time.Duration) {
	if t == nil {
		return
	}
	outcome := "ok"
	if clientAborted {
		outcome = "client_aborted"
	}
	firstFrameValue := "n/a"
	if firstFrame > 0 {
		firstFrameValue = firstFrame.String()
	}
	logger.Info("agent chat timing",
		logger.String("session", sessionIDValue),
		logger.String("turn", turnID),
		logger.String("stream", strconv.FormatBool(t.streaming)),
		logger.String("phases", strings.Join(t.phases, " ")),
		logger.String("first_frame", firstFrameValue),
		logger.String("outcome", outcome),
		logger.String("total", time.Since(t.start).String()),
	)
}
