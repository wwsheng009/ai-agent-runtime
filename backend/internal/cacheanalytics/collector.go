package cacheanalytics

import (
	"sync"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// 事件类型：实际发布为点号格式（internal/agent/loop.go）；
// 下划线常量（internal/chat/events.go）为消费端兼容别名，一并订阅。
const (
	eventLLMRequestStarted       = "llm.request.started"
	eventLLMRequestFinished      = "llm.request.finished"
	eventLLMRequestStartedAlias  = "llm_request_started"
	eventLLMRequestFinishedAlias = "llm_request_finished"
	eventAssistantMessage        = "assistant_message"
	eventSessionEnd              = "session_end"
	eventSessionInterrupted      = "session_interrupted"
)

// EventCacheRequestFinished 是 Collector 在请求记录终态落盘后发布的
// EventBus 事件类型（§6.3 SSE 增量）：载荷为 RecordPayload 投影，
// aicli web SSE（/web/api/events）据此增量刷新缓存页签。
// Collector 自身不订阅该类型，无回环；Bus.Publish 锁外调用 handler，
// 在订阅回调内再发布可重入安全。
const EventCacheRequestFinished = "cache_request_finished"

// errorCategoryInterrupted 会话结束时仍未终态的 in-flight 请求归类（§16.1 边界 3）。
const errorCategoryInterrupted = "interrupted"

// DefaultMaxRequestsPerSession 每 session 环形缓冲上限（§11 Phase 1：1000 条）。
const DefaultMaxRequestsPerSession = 1000

// inflightRequest 已开始未终态的请求登记（§6.2 输入事件）。
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

// Collector 订阅 runtime EventBus，把 llm.request.* 事件投影为
// CacheRequestRecord（每请求一行，终态后不可变）。
//
// 采集与聚合只做一次：HTTP 契约（httpapi.Mount）与 TUI（/usage）都消费
// 同一个 Service.Source()，不重复解析事件流。
type Collector struct {
	mu          sync.Mutex
	bus         *runtimeevents.Bus
	projector   *Projector
	correlation *correlationTable
	inflight    map[string]*inflightRequest
	unsubs      []runtimeevents.Unsubscribe
	now         func() time.Time
	closed      bool
	// store 终态记录持久化镜像（Phase 3）；nil 时纯内存（v1 行为不变）。
	store RequestStore
}

// Options Collector 构建选项。
type Options struct {
	// MaxRequestsPerSession 每 session 记录上限；<=0 时取默认 1000。
	MaxRequestsPerSession int
	// Now 时间源（测试注入）；nil 取 time.Now。
	Now func() time.Time
	// SupportsSSE 覆盖能力发现的是否支持 SSE 增量（§4.4）。nil 时默认
	// true（两种挂载形态的事件流均承载 cache_request_finished）。
	SupportsSSE *bool
	// Store 终态记录持久化镜像（Phase 3：session_runtime.sqlite 镜像表
	// cache_requests）；nil 时纯内存（v1 行为不变）。非 nil 时：
	//   - 终态记录同步落库（best-effort，失败不影响在线投影）；
	//   - 查询期按会话惰性回放镜像记录（重启/恢复会话后明细不丢）；
	//   - Projector 不再淘汰（消除环形上限 partial 降级，§710）。
	Store RequestStore
}

// Attach 把 Collector 挂到事件总线上并返回服务实例。
// bus 为 nil 时返回 nil（调用方无需判空即可安全调用 Close）。
func Attach(bus *runtimeevents.Bus, opts Options, history HistoryLookup) *Service {
	if bus == nil {
		return nil
	}
	if opts.MaxRequestsPerSession <= 0 {
		opts.MaxRequestsPerSession = DefaultMaxRequestsPerSession
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	projector := newProjector(opts.MaxRequestsPerSession)
	projector.setPersisted(opts.Store != nil)
	correlation := newCorrelationTable()
	collector := &Collector{
		bus:         bus,
		projector:   projector,
		correlation: correlation,
		inflight:    make(map[string]*inflightRequest),
		now:         opts.Now,
		store:       opts.Store,
	}
	for _, eventType := range []string{
		eventLLMRequestStarted, eventLLMRequestFinished,
		eventLLMRequestStartedAlias, eventLLMRequestFinishedAlias,
		eventAssistantMessage, eventSessionEnd, eventSessionInterrupted,
	} {
		collector.unsubs = append(collector.unsubs, bus.SubscribeCancelable(eventType, collector.handleEvent))
	}
	// SupportsSSE 能力如实上报（§4.4）：aicli /web/api/events 与 runtime
	// 事件流均承载 cache_request_finished，默认 true；Options 可覆盖。
	supportsSSE := opts.SupportsSSE == nil || *opts.SupportsSSE
	return &Service{
		collector: collector,
		source:    newLiveSource(projector, correlation, history, opts.MaxRequestsPerSession, supportsSSE, opts.Store),
	}
}

// Service Collector 与其只读查询视图的绑定。
type Service struct {
	collector *Collector
	source    *LiveSource
}

// Source 返回统一查询接口（HTTP 与 TUI 共用）。
func (s *Service) Source() Source {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source
}

// Close 退订全部事件并释放引用；幂等。
func (s *Service) Close() {
	if s == nil || s.collector == nil {
		return
	}
	s.collector.close()
}

func (c *Collector) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	for _, unsub := range c.unsubs {
		if unsub != nil {
			unsub()
		}
	}
	c.unsubs = nil
	c.inflight = make(map[string]*inflightRequest)
}

// handleEvent 事件总入口（同步发布，单 handler 内自旋锁保护）。
func (c *Collector) handleEvent(event runtimeevents.Event) {
	switch event.Type {
	case eventLLMRequestStarted, eventLLMRequestStartedAlias:
		c.onRequestStarted(event)
	case eventLLMRequestFinished, eventLLMRequestFinishedAlias:
		c.onRequestFinished(event)
	case eventAssistantMessage:
		c.onAssistantMessage(event)
	case eventSessionEnd, eventSessionInterrupted:
		c.onSessionTerminal(event)
	}
}

func (c *Collector) onRequestStarted(event runtimeevents.Event) {
	payload := event.Payload
	llmRequestID := payloadString(payload, "llm_request_id")
	if llmRequestID == "" {
		return
	}
	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = payloadString(payload, "session_id")
	}
	traceID := event.TraceID
	if traceID == "" {
		traceID = payloadString(payload, "trace_id")
	}
	turnID := payloadString(payload, "logical_turn_id", "turn_id")
	startedAt := event.Timestamp
	if startedAt.IsZero() {
		startedAt = c.now()
	}
	inflight := &inflightRequest{
		sessionID:         sessionID,
		traceID:           traceID,
		turnID:            turnID,
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
	c.correlation.registerStarted(sessionID, llmRequestID, traceID, turnID)
}

func (c *Collector) onRequestFinished(event runtimeevents.Event) {
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

	success := payloadBool(payload, "success")
	record := &CacheRequestRecord{
		SchemaVersion:     SchemaVersion,
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
		CacheEpoch:        inflight.cacheEpoch,
		PromptCacheKey:    inflight.promptCacheKey,
		PromptFingerprint: inflight.promptFingerprint,
	}
	finishedAt := now
	record.FinishedAt = &finishedAt
	record.DurationMS = finishedAt.Sub(inflight.startedAt).Milliseconds()
	if !success {
		record.Status = RequestStatusError
		record.ErrorCategory = payloadString(payload, "error_code")
		if record.ErrorCategory == "" {
			record.ErrorCategory = "error"
		}
		record.CacheStatus = CacheStatusError
		c.projector.append(record)
		c.publishRecordFinished(record)
		c.persistRecord(record)
		return
	}
	record.Status = RequestStatusSuccess

	usage := buildUsage(payload)
	if usage != nil {
		record.Usage = usage
		record.CacheStatus = classifyCacheStatus(usage, payloadString(payload, "usage_source"))
		if ratio, ok := payloadFloat(payload, "usage_cache_hit_ratio"); ok && usage.CacheReadReported && usage.PromptTokens > 0 {
			record.CacheHitRatio = &ratio
		} else if usage.CacheReadReported && usage.PromptTokens > 0 {
			ratio := float64(usage.CacheReadTokens) / float64(usage.PromptTokens)
			record.CacheHitRatio = &ratio
		}
		if usage.CacheCreationReported && usage.PromptTokens > 0 {
			ratio := float64(usage.CacheCreationTokens) / float64(usage.PromptTokens)
			record.CacheWriteRatio = &ratio
		}
	} else {
		record.CacheStatus = CacheStatusNotReported
	}
	c.projector.append(record)
	c.publishRecordFinished(record)
	c.persistRecord(record)
}

// persistRecord 终态记录落库（Phase 3 镜像表，best-effort）：
// 在线投影与 SSE 发布已先行完成（慢写不拖慢增量推送，§9 采集开销约束）；
// 写入失败静默忽略——仅丢失镜像行，不影响本次进程内的查询与 SSE 增量，
// 镜像行可由该请求下次终态事件或下次会话回放修复。
func (c *Collector) persistRecord(record *CacheRequestRecord) {
	if c == nil || c.store == nil || record == nil {
		return
	}
	_ = c.store.SaveRequest(*record)
}

// publishRecordFinished 向总线发布 cache_request_finished（§6.3）：
// 载荷 = CacheRequestRecord 投影（与 HTTP 契约字段一致，单帧 < 2KB）。
func (c *Collector) publishRecordFinished(record *CacheRequestRecord) {
	if c == nil || c.bus == nil || record == nil {
		return
	}
	c.bus.Publish(runtimeevents.Event{
		Type:      EventCacheRequestFinished,
		SessionID: record.SessionID,
		TraceID:   record.TraceID,
		Payload:   RecordPayload(*record),
	})
}

// buildUsage 从 finished 载荷归一化 usage；无 usage 字段时返回 nil。
// 载荷特征（loop.go:1834-1870）：usage_prompt/completion/total 与
// usage_cache_read_reported 在 Usage!=nil 时必写（零值也写）；
// cache_read/creation/cached/reasoning 仅在 >0 时写；无 creation_reported 字段。
func buildUsage(payload map[string]interface{}) *CacheUsage {
	promptPresent := false
	if _, ok := payloadInt64(payload, "usage_prompt_tokens"); ok {
		promptPresent = true
	}
	_, readReportedPresent := payloadBoolValue(payload, "usage_cache_read_reported")
	if !promptPresent && !readReportedPresent {
		return nil
	}
	usage := &CacheUsage{
		UsageSource:      payloadString(payload, "usage_source"),
		PromptTokens:     payloadInt64OrZero(payload, "usage_prompt_tokens"),
		CompletionTokens: payloadInt64OrZero(payload, "usage_completion_tokens"),
		TotalTokens:      payloadInt64OrZero(payload, "usage_total_tokens"),
		CachedTokens:     payloadInt64OrZero(payload, "usage_cached_tokens"),
		ReasoningTokens:  payloadInt64OrZero(payload, "usage_reasoning_tokens"),
	}
	usage.CacheReadTokens = payloadInt64OrZero(payload, "usage_cache_read_tokens")
	if usage.CacheReadTokens == 0 {
		usage.CacheReadTokens = usage.CachedTokens
	}
	usage.CacheCreationTokens = payloadInt64OrZero(payload, "usage_cache_creation_tokens")
	usage.CacheReadReported = payloadBool(payload, "usage_cache_read_reported")
	// 载荷无 creation_reported 字段：tokens>0 即视为已上报（零值省略语义）。
	usage.CacheCreationReported = usage.CacheCreationTokens > 0
	return usage
}

// classifyCacheStatus 缓存状态归类（§4.2 顺序：error→hit→write→reported_zero→not_reported）。
func classifyCacheStatus(usage *CacheUsage, usageSource string) string {
	switch {
	case usage.CacheReadTokens > 0:
		return CacheStatusHit
	case usage.CacheCreationTokens > 0:
		return CacheStatusWrite
	case usage.CacheReadReported:
		return CacheStatusReportedZero
	default:
		// 本地估算（usage_source=local_estimate）v1 不视为缓存上报，保留 usage_source。
		return CacheStatusNotReported
	}
}

func (c *Collector) onAssistantMessage(event runtimeevents.Event) {
	payload := event.Payload
	turnID := payloadString(payload, "turn_id", "logical_turn_id")
	traceID := event.TraceID
	if traceID == "" {
		traceID = payloadString(payload, "trace_id")
	}
	// v1 载荷无 message_id（消息身份在持久化时分配）；经 HistoryLookup 查询期回填。
	// 若未来载荷携带 message_id，则直接登记产出关联（§5.2 两级策略 path 1）。
	if messageID := payloadString(payload, "message_id", "assistant_message_id"); messageID != "" {
		c.correlation.registerAssistantMessage(turnID, traceID, messageID)
	}
}

// onSessionTerminal 终结该 session 全部悬挂 in-flight（C5：status=error,
// error_category=interrupted），避免记录凭空消失。
func (c *Collector) onSessionTerminal(event runtimeevents.Event) {
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
		record := &CacheRequestRecord{
			SchemaVersion:     SchemaVersion,
			LLMRequestID:      orphan.id,
			SessionID:         inflight.sessionID,
			TraceID:           inflight.traceID,
			TurnID:            inflight.turnID,
			Step:              inflight.step,
			Provider:          inflight.provider,
			Model:             inflight.model,
			Stream:            inflight.stream,
			StartedAt:         inflight.startedAt,
			FinishedAt:        &finishedAt,
			DurationMS:        finishedAt.Sub(inflight.startedAt).Milliseconds(),
			Status:            RequestStatusError,
			ErrorCategory:     errorCategoryInterrupted,
			CacheStatus:       CacheStatusError,
			CacheEpoch:        inflight.cacheEpoch,
			PromptCacheKey:    inflight.promptCacheKey,
			PromptFingerprint: inflight.promptFingerprint,
		}
		c.projector.append(record)
		c.persistRecord(record)
	}
}

// payloadInt64OrZero 容错取 int64。
func payloadInt64OrZero(payload map[string]interface{}, key string) int64 {
	value, _ := payloadInt64(payload, key)
	return value
}

// payloadBoolValue 带存在性的 bool 提取。
func payloadBoolValue(payload map[string]interface{}, key string) (bool, bool) {
	if payload == nil {
		return false, false
	}
	raw, ok := payload[key]
	if !ok || raw == nil {
		return false, false
	}
	switch value := raw.(type) {
	case bool:
		return value, true
	case string:
		if value == "true" {
			return true, true
		}
		if value == "false" {
			return false, true
		}
	case float64:
		return value != 0, true
	}
	return false, false
}
