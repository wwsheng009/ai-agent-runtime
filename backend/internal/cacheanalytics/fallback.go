package cacheanalytics

// SessionFallbackSource 组合两个数据源：优先 primary，primary 查不到该会话时
// 回退 secondary。
//
// 背景（回归事故）：微型 Web 缓存页优先读统一用量库（usage_analytics.sqlite
// usage_requests），回退才用进程内实时投影 + runtime 镜像（session_runtime.sqlite
// cache_requests）。但"服务存在"不等于"库里有这个会话的行"——升级前/未挂载
// 用量采集时 usage_requests 可能为空，此时查询命中 primary 的空结果，页面
// 表现为"会话恢复后缓存历史没有加载"，而镜像里其实有完整明细。此组合把
// "优先哪个源"与"这个源到底有没有数据"解耦：空结果与会话不存在都回退。
//
// 语义（逐方法、按会话判定）：
//   - Capabilities 取 primary（Persisted 取两者并集，避免镜像可用却上报未持久化）；
//   - Overview/Requests 在 primary 报错或该会话 0 条记录时改用 secondary；
//   - Request/MessageTrace 在 primary 返回 ErrNotFound/ErrSessionNotFound 时改用 secondary；
//   - secondary 也没有数据时返回 primary 的原结果（保持既有错误码与空语义）。
//
// 已知边界：primary 有部分行（非空但不完整）时不合并两者，只返回 primary；
// 跨源补齐/合并留待后续按需实现。
type SessionFallbackSource struct {
	primary   Source
	secondary Source
}

// NewSessionFallbackSource 构造回退数据源。任一为 nil 时退化为单源；两者
// 都为 nil 时返回 nil（调用方保持"服务不可用"的既有处理）。
func NewSessionFallbackSource(primary, secondary Source) Source {
	switch {
	case primary == nil:
		return secondary
	case secondary == nil:
		return primary
	default:
		return &SessionFallbackSource{primary: primary, secondary: secondary}
	}
}

// Capabilities 以 primary 为准，Persisted 取并集。
func (s *SessionFallbackSource) Capabilities() Capabilities {
	capabilities := s.primary.Capabilities()
	if secondary := s.secondary.Capabilities(); secondary.Persisted {
		capabilities.Persisted = true
	}
	return capabilities
}

// Overview 会话总览：primary 无该会话记录时回退 secondary。
func (s *SessionFallbackSource) Overview(sessionID string) (CacheOverview, error) {
	primary, primaryErr := s.primary.Overview(sessionID)
	if primaryErr == nil && overviewHasRecords(primary) {
		return primary, nil
	}
	secondary, secondaryErr := s.secondary.Overview(sessionID)
	if secondaryErr == nil && overviewHasRecords(secondary) {
		return secondary, nil
	}
	if primaryErr != nil {
		if secondaryErr != nil {
			return primary, primaryErr
		}
		return secondary, nil
	}
	return primary, nil
}

// Requests 明细分页：primary 该会话 0 条命中时回退 secondary。
func (s *SessionFallbackSource) Requests(sessionID string, q RequestQuery) (RequestListResponse, error) {
	primary, primaryErr := s.primary.Requests(sessionID, q)
	if primaryErr == nil && (primary.Total > 0 || len(primary.Requests) > 0) {
		return primary, nil
	}
	secondary, secondaryErr := s.secondary.Requests(sessionID, q)
	if secondaryErr == nil && (secondary.Total > 0 || len(secondary.Requests) > 0) {
		return secondary, nil
	}
	if primaryErr != nil {
		if secondaryErr != nil {
			return primary, primaryErr
		}
		return secondary, nil
	}
	return primary, nil
}

// Request 单请求详情：primary 找不到该请求时回退 secondary。
func (s *SessionFallbackSource) Request(sessionID, llmRequestID string) (CacheRequestRecord, error) {
	primary, primaryErr := s.primary.Request(sessionID, llmRequestID)
	if primaryErr == nil {
		return primary, nil
	}
	secondary, secondaryErr := s.secondary.Request(sessionID, llmRequestID)
	if secondaryErr == nil {
		return secondary, nil
	}
	return primary, primaryErr
}

// MessageTrace 消息追溯：primary 找不到时回退 secondary。
func (s *SessionFallbackSource) MessageTrace(sessionID, messageID string) (MessageTrace, error) {
	primary, primaryErr := s.primary.MessageTrace(sessionID, messageID)
	if primaryErr == nil && traceHasContent(primary) {
		return primary, nil
	}
	secondary, secondaryErr := s.secondary.MessageTrace(sessionID, messageID)
	if secondaryErr == nil && traceHasContent(secondary) {
		return secondary, nil
	}
	if primaryErr != nil {
		if secondaryErr != nil {
			return primary, primaryErr
		}
		return secondary, nil
	}
	return primary, nil
}

// overviewHasRecords 总览是否真的带有记录（RequestsTotal 为 0 即视为空）。
func overviewHasRecords(overview CacheOverview) bool {
	return overview.RequestsTotal > 0
}

// traceHasContent 追溯是否有产出/消费记录（仅有 HistoryAvailable 不算命中）。
func traceHasContent(trace MessageTrace) bool {
	return trace.ProducedBy != nil || len(trace.ConsumedBy) > 0
}
