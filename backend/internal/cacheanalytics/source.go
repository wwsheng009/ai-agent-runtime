package cacheanalytics

import "sync"

// LiveSource 在线投影查询视图：内存 Projector + 关联表 + 会话历史兜底。
// 实现 Source 接口，HTTP（httpapi.Mount）与 TUI（/usage）共用。
// 挂载持久化镜像表（Phase 3）时，首次查询某会话前先从镜像表惰性回放，
// 进程重启/会话恢复后 per-request 明细不丢（方案 §375/§711）。
type LiveSource struct {
	projector   *Projector
	correlation *correlationTable
	history     HistoryLookup
	maxRequests int
	supportsSSE bool
	// store 持久化镜像（nil 时纯内存）；loadedSessions 记录已回放
	//（或确认无镜像行）的会话，避免重复查库。
	store          RequestStore
	loadedMu       sync.Mutex
	loadedSessions map[string]bool
}

func newLiveSource(projector *Projector, correlation *correlationTable, history HistoryLookup, maxRequests int, supportsSSE bool, store RequestStore) *LiveSource {
	if maxRequests <= 0 {
		maxRequests = DefaultMaxRequestsPerSession
	}
	return &LiveSource{
		projector:      projector,
		correlation:    correlation,
		history:        history,
		maxRequests:    maxRequests,
		supportsSSE:    supportsSSE,
		store:          store,
		loadedSessions: make(map[string]bool),
	}
}

// ensureSessionLoaded 查询前把该会话的镜像记录回放进 Projector（幂等：
// projector.append 按 llm_request_id 去重，与在线事件并发安全）。
// 加载失败降级为纯内存视图，不重试（下次进程重启再回放）。
//
// 持锁加载：前端打开页签时会并行拉取 overview+requests（SSE 刷新亦然），
// 若"先标记后加载"，并发查询会读到回放中途的部分投影，且前端按会话缓存
// 不会自动重拉 → 持 loadedMu 贯穿加载+回放，让并发查询阻塞到回放完成。
// 锁序固定为 loadedMu → projector.mu（无反向路径），无死锁风险。
func (s *LiveSource) ensureSessionLoaded(sessionID string) {
	if s == nil || s.store == nil || sessionID == "" {
		return
	}
	s.loadedMu.Lock()
	defer s.loadedMu.Unlock()
	if s.loadedSessions[sessionID] {
		return
	}
	// 先标记再加载：同一会话只回放一次；失败视为已回放（降级）。
	s.loadedSessions[sessionID] = true
	records, err := s.store.LoadSessionRequests(sessionID)
	if err != nil {
		return
	}
	for i := range records {
		s.projector.append(&records[i])
	}
	// 回放期间在线事件可能交错落位（collector 独立取 projector.mu）：
	// 恢复该会话记录的时间序，保证 overview 窗口（records[0]/last）与
	// requestsAfter 的顺序语义。
	s.projector.sortSessionRecords(sessionID)
}

// Capabilities 能力发现（§7.1：前端启动探测，不支持时优雅降级）。
func (s *LiveSource) Capabilities() Capabilities {
	capabilities := Capabilities{
		SchemaVersion:         SchemaVersion,
		DataSource:            DataSourceLive,
		MaxRequestsPerSession: s.maxRequests,
		SupportsSSE:           s.supportsSSE,
		Persisted:             s.store != nil,
	}
	if s.supportsSSE {
		capabilities.SupportedEvents = []string{EventCacheRequestFinished}
	}
	return capabilities
}

// Overview 会话总览。会话不存在（历史存储确认无此会话且无记录）返回 ErrSessionNotFound。
func (s *LiveSource) Overview(sessionID string) (CacheOverview, error) {
	if sessionID == "" {
		return CacheOverview{}, ErrInvalidRequest
	}
	s.ensureSessionLoaded(sessionID)
	if s.sessionMissing(sessionID) {
		return CacheOverview{}, ErrSessionNotFound
	}
	return s.projector.overview(sessionID), nil
}

// Requests 明细分页（过滤条件见 RequestQuery）。
func (s *LiveSource) Requests(sessionID string, q RequestQuery) (RequestListResponse, error) {
	if sessionID == "" {
		return RequestListResponse{}, ErrInvalidRequest
	}
	s.ensureSessionLoaded(sessionID)
	if s.sessionMissing(sessionID) {
		return RequestListResponse{}, ErrSessionNotFound
	}
	if q.Limit < 0 || q.Offset < 0 {
		return RequestListResponse{}, ErrInvalidRequest
	}
	return s.projector.requests(sessionID, q), nil
}

// Request 单请求详情。
func (s *LiveSource) Request(sessionID, llmRequestID string) (CacheRequestRecord, error) {
	if sessionID == "" || llmRequestID == "" {
		return CacheRequestRecord{}, ErrInvalidRequest
	}
	s.ensureSessionLoaded(sessionID)
	record, ok := s.projector.request(sessionID, llmRequestID)
	if !ok {
		return CacheRequestRecord{}, ErrNotFound
	}
	return record, nil
}

// MessageTrace 按消息 id 追溯（§4.3）。
//
// 两级策略（§5.2）：事件流登记的 message_id 直接关联；v1 事件载荷无
// message_id，主路径为 HistoryLookup 兜底——由消息 role/turn 反查请求。
func (s *LiveSource) MessageTrace(sessionID, messageID string) (MessageTrace, error) {
	if sessionID == "" || messageID == "" {
		return MessageTrace{}, ErrInvalidRequest
	}
	// 与 Overview/Requests 一致：会话确认不存在（历史存储无此会话且无记录）
	// 返回 ErrSessionNotFound，与未知消息 id 的 ErrNotFound 区分（§4.3）。
	s.ensureSessionLoaded(sessionID)
	if s.sessionMissing(sessionID) {
		return MessageTrace{}, ErrSessionNotFound
	}
	trace := MessageTrace{
		SchemaVersion: SchemaVersion,
		SessionID:     sessionID,
		MessageID:     messageID,
		ConsumedBy:    []ConsumedBy{},
	}
	// path 1：事件流登记（未来载荷携带 message_id 时生效）。
	if producerID, ok := s.correlation.producerOfMessage(messageID); ok {
		if record, ok := s.projector.request(sessionID, producerID); ok {
			trace.ProducedBy = producedByOf(&record)
			trace.TurnID = record.TurnID
		}
	}
	// path 2：会话历史兜底。
	if s.history != nil {
		if ctx, ok := s.history.MessageContext(sessionID, messageID); ok {
			trace.HistoryAvailable = true
			trace.CorrelationSource = CorrelationSourceHistory
			trace.MessageRole = ctx.Role
			trace.Neighbors = MessageNeighbors{
				PrevMessageID: ctx.PrevMessageID,
				NextMessageID: ctx.NextMessageID,
			}
			if trace.TurnID == "" {
				trace.TurnID = ctx.TurnID
			}
			s.enrichTraceFromHistory(&trace, ctx)
		}
	}
	// consumed_by（§4.3）：产出之后开始的请求（后续 turn 复用上下文）。
	// path 1 命中时 enrichTraceFromHistory 未执行，在此统一补齐；
	// path 2 已填充（user/assistant 分支）时跳过。
	if trace.ProducedBy != nil && len(trace.ConsumedBy) == 0 && trace.ProducedBy.StartedAt != nil {
		trace.ConsumedBy = consumedByOf(s.projector.requestsAfter(trace.SessionID, *trace.ProducedBy.StartedAt, 50))
	}
	if trace.ProducedBy == nil && len(trace.ConsumedBy) == 0 && !trace.HistoryAvailable {
		return MessageTrace{}, ErrNotFound
	}
	return trace, nil
}

// enrichTraceFromHistory 由历史消息的 role/turn 反查产出/消费请求。
func (s *LiveSource) enrichTraceFromHistory(trace *MessageTrace, ctx MessageContext) {
	turnRequests := s.projector.turnSuccessfulRequests(trace.SessionID, ctx.TurnID)
	switch ctx.Role {
	case "assistant":
		// 产出：turn 内最后一个成功请求（ReAct 终步）。
		if trace.ProducedBy == nil && len(turnRequests) > 0 {
			trace.ProducedBy = producedByOf(turnRequests[len(turnRequests)-1])
			if s.history != nil {
				if assistantID, ok := s.history.AssistantMessageIDByTurn(trace.SessionID, ctx.TurnID); ok && assistantID == trace.MessageID {
					s.projector.backfillMessageID(trace.SessionID, trace.ProducedBy.LLMRequestID, "", trace.MessageID)
				}
			}
		}
		// 消费：产出之后开始的请求（后续 turn 复用上下文）。
		if trace.ProducedBy != nil && trace.ProducedBy.StartedAt != nil {
			trace.ConsumedBy = consumedByOf(s.projector.requestsAfter(trace.SessionID, *trace.ProducedBy.StartedAt, 50))
		}
	case "user":
		// user 消息触发 turn：该 turn 及其后的请求都消费了它。
		if s.history != nil {
			if userID, ok := s.history.UserMessageIDByTurn(trace.SessionID, ctx.TurnID); ok && userID == trace.MessageID {
				for _, record := range turnRequests {
					s.projector.backfillMessageID(trace.SessionID, record.LLMRequestID, trace.MessageID, "")
				}
			}
		}
		trace.ConsumedBy = consumedByOf(turnRequests)
		if len(trace.ConsumedBy) == 0 {
			return
		}
		// 追加后续 turn 的请求（截断到 50 条）。
		after := trace.ConsumedBy[len(trace.ConsumedBy)-1]
		if after.StartedAt != nil {
			extra := consumedByOf(s.projector.requestsAfter(trace.SessionID, *after.StartedAt, 50-len(trace.ConsumedBy)))
			trace.ConsumedBy = append(trace.ConsumedBy, extra...)
		}
	}
	sortConsumers(trace.ConsumedBy)
}

// sessionMissing 会话是否确认不存在：有历史存储且明确无此会话，且投影无记录。
func (s *LiveSource) sessionMissing(sessionID string) bool {
	if s.history == nil {
		return false
	}
	if s.projector.sessionRecordCount(sessionID) > 0 {
		return false
	}
	return !s.history.SessionExists(sessionID)
}

func producedByOf(record *CacheRequestRecord) *ProducedBy {
	if record == nil {
		return nil
	}
	produced := &ProducedBy{
		LLMRequestID:  record.LLMRequestID,
		Usage:         record.Usage,
		CacheHitRatio: record.CacheHitRatio,
		CacheStatus:   record.CacheStatus,
		TraceID:       record.TraceID,
		TurnID:        record.TurnID,
	}
	if !record.StartedAt.IsZero() {
		started := record.StartedAt
		produced.StartedAt = &started
	}
	return produced
}

func consumedByOf(records []*CacheRequestRecord) []ConsumedBy {
	consumers := make([]ConsumedBy, 0, len(records))
	for _, record := range records {
		consumer := ConsumedBy{
			LLMRequestID:  record.LLMRequestID,
			Step:          record.Step,
			TurnID:        record.TurnID,
			CacheHitRatio: record.CacheHitRatio,
			CacheStatus:   record.CacheStatus,
		}
		if !record.StartedAt.IsZero() {
			started := record.StartedAt
			consumer.StartedAt = &started
		}
		consumers = append(consumers, consumer)
	}
	sortConsumers(consumers)
	return consumers
}
