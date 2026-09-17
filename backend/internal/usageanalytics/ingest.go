package usageanalytics

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// 事件类型：实际发布为点号格式（internal/agent/loop.go）；
// 下划线常量（internal/chat/events.go）为消费端兼容别名，一并订阅。
const (
	EventLLMRequestStarted       = "llm.request.started"
	EventLLMRequestFinished      = "llm.request.finished"
	EventLLMRequestStartedAlias  = "llm_request_started"
	EventLLMRequestFinishedAlias = "llm_request_finished"
	EventAssistantMessage        = "assistant_message"
	EventSessionStart            = "session_start"
	EventSessionEnd              = "session_end"
	EventSessionInterrupted      = "session_interrupted"
	// schema v2 输入（方案 §5.3）：工具生命周期与子代理完成。
	EventToolRequested     = "tool.requested"
	EventToolCompleted     = "tool.completed"
	EventSubagentCompleted = "subagent.completed"
)

// SessionStatusCompleted / SessionStatusInterrupted 会话终态。
const (
	SessionStatusCompleted   = "completed"
	SessionStatusInterrupted = "interrupted"
)

// SessionMeta 是补齐 usage_sessions 元数据所需的最小字段集
// （best-effort：查不到就保留已有值）。
type SessionMeta struct {
	Title            string
	ProjectPath      string
	WorkingDirectory string
	Provider         string
	Model            string
	Protocol         string
	Status           string
}

// SessionMetaLookup 由调用方注入的会话元数据来源
// （runtime server: chat.SessionManager；aicli 本地: host.SessionStore）。
type SessionMetaLookup interface {
	// SessionMeta 返回会话元数据；ok=false 表示未知（保留库中已有值）。
	SessionMeta(sessionID string) (SessionMeta, bool)
}

// inflightRequest 已开始未终态的请求登记（会话终止时兜底为 interrupted）。
type inflightRequest struct {
	sessionID         string
	traceID           string
	turnID            string
	step              int
	provider          string
	model             string
	stream            bool
	startedAt         time.Time
	cacheEpoch        int
	promptCacheKey    string
	promptFingerprint string
}

// collector 订阅 runtime EventBus 并实时 UPSERT 分析库。
type collector struct {
	store  *Store
	lookup SessionMetaLookup
	now    func() time.Time

	mu       sync.Mutex
	inflight map[string]*inflightRequest
	unsubs   []func()
	// turnToolStats：回合内工具失败/恢复计数（会话终止时落 usage_turns）。
	turnToolStats map[string]*turnToolStats

	// writeFailureCount：分析库写入失败此前被完全静默（行丢失且无痕迹）。
	// 首次失败提示一次，之后每 100 次再提示一次，既留痕又不刷屏。
	writeFailureCount atomic.Int64
}

func newCollector(store *Store, lookup SessionMetaLookup, now func() time.Time) *collector {
	if now == nil {
		now = time.Now
	}
	return &collector{
		store:    store,
		lookup:   lookup,
		now:      now,
		inflight: make(map[string]*inflightRequest),
	}
}

// subscribe 订阅全部输入事件（幂等：重复调用只订阅一次由调用方保证）。
func (c *collector) subscribe(bus *runtimeevents.Bus) {
	if c == nil || bus == nil {
		return
	}
	for _, eventType := range []string{
		EventLLMRequestStarted, EventLLMRequestFinished,
		EventLLMRequestStartedAlias, EventLLMRequestFinishedAlias,
		EventAssistantMessage, EventSessionStart, EventSessionEnd, EventSessionInterrupted,
		EventToolRequested, EventToolCompleted, EventSubagentCompleted,
	} {
		c.unsubs = append(c.unsubs, bus.SubscribeCancelable(eventType, c.handleEvent))
	}
}

// close 取消订阅并清空 in-flight（幂等）。
func (c *collector) close() {
	if c == nil {
		return
	}
	for _, unsub := range c.unsubs {
		if unsub != nil {
			unsub()
		}
	}
	c.unsubs = nil
	c.mu.Lock()
	c.inflight = make(map[string]*inflightRequest)
	c.turnToolStats = make(map[string]*turnToolStats)
	c.mu.Unlock()
}

func (c *collector) handleEvent(event runtimeevents.Event) {
	switch event.Type {
	case EventLLMRequestStarted, EventLLMRequestStartedAlias:
		c.onRequestStarted(event)
	case EventLLMRequestFinished, EventLLMRequestFinishedAlias:
		c.onRequestFinished(event)
	case EventAssistantMessage:
		c.onAssistantMessage(event)
	case EventSessionStart:
		c.onSessionStart(event)
	case EventSessionEnd, EventSessionInterrupted:
		c.onSessionTerminal(event)
	case EventToolRequested:
		c.onToolRequested(event)
	case EventToolCompleted:
		c.onToolCompleted(event)
	case EventSubagentCompleted:
		c.onSubagentCompleted(event)
	}
}

func (c *collector) onRequestStarted(event runtimeevents.Event) {
	payload := event.Payload
	llmRequestID := payloadString(payload, "llm_request_id")
	if llmRequestID == "" {
		return
	}
	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = payloadString(payload, "session_id")
	}
	if sessionID == "" {
		return
	}
	traceID := event.TraceID
	if traceID == "" {
		traceID = payloadString(payload, "trace_id")
	}
	startedAt := event.Timestamp
	if startedAt.IsZero() {
		startedAt = c.now()
	}
	inflight := &inflightRequest{
		sessionID:         sessionID,
		traceID:           traceID,
		turnID:            payloadString(payload, "logical_turn_id", "turn_id"),
		step:              payloadInt(payload, "step"),
		provider:          payloadString(payload, "provider"),
		model:             payloadString(payload, "model"),
		stream:            payloadString(payload, "stream_id") != "",
		startedAt:         startedAt,
		cacheEpoch:        payloadInt(payload, "prompt_cache_epoch"),
		promptCacheKey:    payloadString(payload, "prompt_cache_key"),
		promptFingerprint: payloadString(payload, "prompt_fingerprint"),
	}
	c.mu.Lock()
	c.inflight[llmRequestID] = inflight
	c.mu.Unlock()

	c.upsertSession(sessionID, SessionMeta{
		Provider: inflight.provider,
		Model:    inflight.model,
	}, startedAt, time.Time{})
}

func (c *collector) onRequestFinished(event runtimeevents.Event) {
	payload := event.Payload
	llmRequestID := payloadString(payload, "llm_request_id")
	if llmRequestID == "" {
		return
	}
	c.mu.Lock()
	inflight := c.inflight[llmRequestID]
	delete(c.inflight, llmRequestID)
	now := c.now()
	c.mu.Unlock()

	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = payloadString(payload, "session_id")
	}
	if inflight == nil {
		inflight = &inflightRequest{}
	}
	if inflight.sessionID == "" {
		inflight.sessionID = sessionID
	}
	if inflight.traceID == "" {
		inflight.traceID = event.TraceID
		if inflight.traceID == "" {
			inflight.traceID = payloadString(payload, "trace_id")
		}
	}
	if inflight.turnID == "" {
		inflight.turnID = payloadString(payload, "logical_turn_id", "turn_id")
	}
	if inflight.provider == "" {
		inflight.provider = payloadString(payload, "provider")
	}
	if inflight.model == "" {
		inflight.model = payloadString(payload, "model")
	}
	if inflight.startedAt.IsZero() {
		inflight.startedAt = now
	}
	if inflight.sessionID == "" {
		// 无会话归属的请求不进入分析库（无法归组）。
		return
	}

	finishedAt := now
	record := cacheanalytics.BuildTerminalRecord(cacheanalytics.TerminalRecordInput{
		LLMRequestID:      llmRequestID,
		SessionID:         inflight.sessionID,
		TraceID:           inflight.traceID,
		TurnID:            inflight.turnID,
		Step:              inflight.step,
		Provider:          inflight.provider,
		Model:             inflight.model,
		Stream:            inflight.stream,
		Attempt:           1,
		StartedAt:         inflight.startedAt,
		FinishedAt:        finishedAt,
		CacheEpoch:        inflight.cacheEpoch,
		PromptCacheKey:    inflight.promptCacheKey,
		PromptFingerprint: inflight.promptFingerprint,
		Payload:           payload,
	})
	c.persistRequestTerminal(record, SessionMeta{
		Provider: record.Provider,
		Model:    record.Model,
	}, record.StartedAt, finishedAt)
}

func (c *collector) onAssistantMessage(event runtimeevents.Event) {
	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = payloadString(event.Payload, "session_id")
	}
	if sessionID == "" {
		return
	}
	c.upsertSession(sessionID, SessionMeta{}, time.Time{}, c.now())
}

// onSessionTerminal 终结该会话全部悬挂 in-flight（interrupted），
// 并把会话状态推进到终态（幂等：同 id 重复事件覆盖为同一状态）。
func (c *collector) onSessionTerminal(event runtimeevents.Event) {
	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = payloadString(event.Payload, "session_id")
	}
	if sessionID == "" {
		return
	}
	now := c.now()
	c.mu.Lock()
	type orphanPair struct {
		id       string
		inflight *inflightRequest
	}
	var orphans []orphanPair
	for id, inflight := range c.inflight {
		if inflight.sessionID == sessionID {
			orphans = append(orphans, orphanPair{id: id, inflight: inflight})
		}
	}
	for _, orphan := range orphans {
		delete(c.inflight, orphan.id)
	}
	c.mu.Unlock()

	for _, orphan := range orphans {
		inflight := orphan.inflight
		finishedAt := now
		record := cacheanalytics.BuildTerminalRecord(cacheanalytics.TerminalRecordInput{
			LLMRequestID:      orphan.id,
			SessionID:         inflight.sessionID,
			TraceID:           inflight.traceID,
			TurnID:            inflight.turnID,
			Step:              inflight.step,
			Provider:          inflight.provider,
			Model:             inflight.model,
			Stream:            inflight.stream,
			StartedAt:         inflight.startedAt,
			FinishedAt:        finishedAt,
			CacheEpoch:        inflight.cacheEpoch,
			PromptCacheKey:    inflight.promptCacheKey,
			PromptFingerprint: inflight.promptFingerprint,
			Interrupted:       true,
		})
		c.persistRequestTerminal(record, SessionMeta{}, record.StartedAt, finishedAt)
	}

	status := SessionStatusCompleted
	if event.Type == EventSessionInterrupted {
		status = SessionStatusInterrupted
	}
	c.upsertSession(sessionID, SessionMeta{Status: status}, time.Time{}, now)
	// schema v2：回合级终值（含工具失败/恢复计数），session_end 为权威。
	c.upsertTurnTerminal(sessionID, event)
}

// upsertRequest 幂等写入一条请求终态行（同 llm_request_id 覆盖）。
func (c *collector) upsertRequest(record cacheanalytics.CacheRequestRecord) {
	if c == nil || c.store == nil {
		return
	}
	statement, args := c.requestUpsertStatement(record)
	if err := c.store.execWithLockRetry(statement, args...); err != nil {
		c.reportWriteFailure("usage_requests", err)
	}
}

// requestUpsertStatement 构造 usage_requests 幂等 UPSERT（ON CONFLICT 语义不变）。
func (c *collector) requestUpsertStatement(record cacheanalytics.CacheRequestRecord) (string, []interface{}) {
	startedAt := record.StartedAt
	if startedAt.IsZero() && record.FinishedAt != nil {
		startedAt = *record.FinishedAt
	}
	payload, err := json.Marshal(record)
	if err != nil {
		payload = []byte{}
	}
	var promptTokens, completionTokens, totalTokens, cacheRead, cacheCreation, reasoning int64
	if record.Usage != nil {
		promptTokens = record.Usage.PromptTokens
		completionTokens = record.Usage.CompletionTokens
		totalTokens = record.Usage.TotalTokens
		cacheRead = record.Usage.CacheReadTokens
		cacheCreation = record.Usage.CacheCreationTokens
		reasoning = record.Usage.ReasoningTokens
	}
	success := 0
	if record.Status == cacheanalytics.RequestStatusSuccess {
		success = 1
	}
	usageAvailable := 0
	if record.Usage != nil {
		usageAvailable = 1
	}
	const statement = `
INSERT INTO usage_requests (
  llm_request_id, session_id, trace_id, turn_id, step, provider, model, status, cache_status,
  success, error_category, started_at_unix_nano, duration_ms, prompt_tokens, completion_tokens,
  cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, usage_available, record_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(llm_request_id) DO UPDATE SET
  session_id = excluded.session_id,
  trace_id = excluded.trace_id,
  turn_id = excluded.turn_id,
  step = excluded.step,
  provider = CASE WHEN excluded.provider <> '' THEN excluded.provider ELSE usage_requests.provider END,
  model = CASE WHEN excluded.model <> '' THEN excluded.model ELSE usage_requests.model END,
  status = excluded.status,
  cache_status = excluded.cache_status,
  success = excluded.success,
  error_category = excluded.error_category,
  started_at_unix_nano = CASE WHEN usage_requests.started_at_unix_nano = 0 THEN excluded.started_at_unix_nano ELSE usage_requests.started_at_unix_nano END,
  duration_ms = excluded.duration_ms,
  prompt_tokens = excluded.prompt_tokens,
  completion_tokens = excluded.completion_tokens,
  cache_read_tokens = excluded.cache_read_tokens,
  cache_creation_tokens = excluded.cache_creation_tokens,
  reasoning_tokens = excluded.reasoning_tokens,
  total_tokens = excluded.total_tokens,
  usage_available = excluded.usage_available,
  record_json = excluded.record_json`
	return statement, []interface{}{
		record.LLMRequestID,
		record.SessionID,
		record.TraceID,
		record.TurnID,
		record.Step,
		record.Provider,
		record.Model,
		record.Status,
		record.CacheStatus,
		success,
		record.ErrorCategory,
		startedAt.UnixNano(),
		record.DurationMS,
		promptTokens,
		completionTokens,
		cacheRead,
		cacheCreation,
		reasoning,
		totalTokens,
		usageAvailable,
		string(payload),
	}
}

// reportWriteFailure 记录一次分析库写入失败（此前完全静默：行丢失且无痕迹）。
func (c *collector) reportWriteFailure(table string, err error) {
	if c == nil || err == nil {
		return
	}
	failures := c.writeFailureCount.Add(1)
	if failures == 1 || failures%100 == 0 {
		degradeWarn("写入 %s 失败（累计 %d 次，本行已丢失）：%v", table, failures, err)
	}
}

// upsertSession 幂等合并会话元数据：空值不覆盖已有值；查找成功时补齐
// title/project_path/working_directory/provider/model/protocol/status。
func (c *collector) upsertSession(sessionID string, meta SessionMeta, startedAt, endedAt time.Time) {
	if c == nil || c.store == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	statement, args := c.sessionUpsertStatement(sessionID, meta, startedAt, endedAt)
	if err := c.store.execWithLockRetry(statement, args...); err != nil {
		c.reportWriteFailure("usage_sessions", err)
	}
}

// sessionUpsertStatement 构造 usage_sessions 元数据合并 UPSERT（空值不覆盖）。
func (c *collector) sessionUpsertStatement(sessionID string, meta SessionMeta, startedAt, endedAt time.Time) (string, []interface{}) {
	now := c.now()
	if c.lookup != nil {
		if looked, ok := c.lookup.SessionMeta(sessionID); ok {
			meta = mergeSessionMeta(meta, looked)
		}
	}
	var started, ended int64
	if !startedAt.IsZero() {
		started = startedAt.UnixNano()
	}
	if !endedAt.IsZero() {
		ended = endedAt.UnixNano()
	}
	const statement = `
INSERT INTO usage_sessions (
  session_id, title, project_path, working_directory, provider, model, protocol, status,
  started_at_unix_nano, ended_at_unix_nano, updated_at_unix_nano, meta_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,NULL)
ON CONFLICT(session_id) DO UPDATE SET
  title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE usage_sessions.title END,
  project_path = CASE WHEN excluded.project_path <> '' THEN excluded.project_path ELSE usage_sessions.project_path END,
  working_directory = CASE WHEN excluded.working_directory <> '' THEN excluded.working_directory ELSE usage_sessions.working_directory END,
  provider = CASE WHEN excluded.provider <> '' THEN excluded.provider ELSE usage_sessions.provider END,
  model = CASE WHEN excluded.model <> '' THEN excluded.model ELSE usage_sessions.model END,
  protocol = CASE WHEN excluded.protocol <> '' THEN excluded.protocol ELSE usage_sessions.protocol END,
  status = CASE WHEN excluded.status <> '' THEN excluded.status ELSE usage_sessions.status END,
  started_at_unix_nano = CASE
    WHEN excluded.started_at_unix_nano = 0 THEN usage_sessions.started_at_unix_nano
    WHEN usage_sessions.started_at_unix_nano = 0 THEN excluded.started_at_unix_nano
    ELSE MIN(usage_sessions.started_at_unix_nano, excluded.started_at_unix_nano) END,
  ended_at_unix_nano = MAX(usage_sessions.ended_at_unix_nano, excluded.ended_at_unix_nano),
  updated_at_unix_nano = MAX(usage_sessions.updated_at_unix_nano, excluded.updated_at_unix_nano)`
	return statement, []interface{}{
		sessionID,
		meta.Title,
		meta.ProjectPath,
		meta.WorkingDirectory,
		meta.Provider,
		meta.Model,
		meta.Protocol,
		meta.Status,
		started,
		ended,
		now.UnixNano(),
	}
}

// mergeSessionMeta 事件元数据 + 查找元数据（查找值补齐事件缺失字段）。
func mergeSessionMeta(primary, fallback SessionMeta) SessionMeta {
	merged := primary
	if merged.Title == "" {
		merged.Title = fallback.Title
	}
	if merged.ProjectPath == "" {
		merged.ProjectPath = fallback.ProjectPath
	}
	if merged.WorkingDirectory == "" {
		merged.WorkingDirectory = fallback.WorkingDirectory
	}
	if merged.Provider == "" {
		merged.Provider = fallback.Provider
	}
	if merged.Model == "" {
		merged.Model = fallback.Model
	}
	if merged.Protocol == "" {
		merged.Protocol = fallback.Protocol
	}
	if merged.Status == "" {
		merged.Status = fallback.Status
	}
	return merged
}

// ---------------------------------------------------------------------------
// 载荷读取辅助（与 cacheanalytics 同语义：缺失/类型不符返回零值）。
// ---------------------------------------------------------------------------

func payloadString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if payload == nil {
			return ""
		}
		raw, ok := payload[key]
		if !ok || raw == nil {
			continue
		}
		if value, ok := raw.(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
			continue
		}
		return strings.TrimSpace(fmt.Sprintf("%v", raw))
	}
	return ""
}

func payloadInt(payload map[string]interface{}, key string) int {
	if payload == nil {
		return 0
	}
	switch value := payload[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	default:
		return 0
	}
}
