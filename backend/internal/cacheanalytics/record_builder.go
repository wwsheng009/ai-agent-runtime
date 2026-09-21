package cacheanalytics

import "time"

// ============================================================================
// 终态记录归一化（纯函数，供 Collector 与外部 ingest 共用）。
//
// 统一 /usage 与 /usage/cache 数据源后，usageanalytics（SQLite ingest）需要
// 产出与 cacheanalytics.CacheRequestRecord 完全一致的记录，才能保证缓存端点
// （数据库回放）与用量分析（SQL 聚合）读到同一份事实。这里把原先内联在
// Collector.onRequestFinished 的归一化逻辑抽为公开纯函数，两处共用一份实现。
// ============================================================================

// TerminalRecordInput 汇总构造一条终态请求记录所需的输入。
type TerminalRecordInput struct {
	LLMRequestID string
	SessionID    string
	TraceID      string
	TurnID       string
	Step         int
	Provider     string
	Model        string
	Stream       bool
	// Attempt 重试次数；Collector 终态路径置 1，会话终止兜底路径保持 0（与 v1 一致）。
	Attempt int
	// StartedAt 来自 llm.request.started 事件（缺失时回退到终态时刻）。
	StartedAt time.Time
	// FinishedAt 终态时刻。
	FinishedAt        time.Time
	CacheEpoch        int
	PromptCacheKey    string
	PromptFingerprint string
	// Payload 是 llm.request.finished 的事件载荷（success/error_code/usage_*）。
	Payload map[string]interface{}
	// Interrupted 标记会话/进程终止时仍未终态的 in-flight 请求（§16.1 边界 3）。
	Interrupted bool
}

// BuildTerminalRecord 把终态输入归一化为不可变的 CacheRequestRecord。
func BuildTerminalRecord(in TerminalRecordInput) CacheRequestRecord {
	finishedAt := in.FinishedAt
	record := CacheRequestRecord{
		SchemaVersion:     SchemaVersion,
		LLMRequestID:      in.LLMRequestID,
		SessionID:         in.SessionID,
		TraceID:           in.TraceID,
		TurnID:            in.TurnID,
		Step:              in.Step,
		Provider:          in.Provider,
		Model:             in.Model,
		Stream:            in.Stream,
		Attempt:           in.Attempt,
		StartedAt:         in.StartedAt,
		FinishedAt:        &finishedAt,
		DurationMS:        finishedAt.Sub(in.StartedAt).Milliseconds(),
		CacheEpoch:        in.CacheEpoch,
		PromptCacheKey:    in.PromptCacheKey,
		PromptFingerprint: in.PromptFingerprint,
	}
	// 上下文事实先于错误分支提取：失败请求（如上下文超限被拒）同样携带窗口与预算，
	// 前端仍能显示"已用 / 窗口 / 预算"。interrupted 兜底路径没有载荷，保持 0。
	payload := in.Payload
	record.ContextPromptTokens = payloadInt(payload, "context_prompt_tokens")
	record.ContextWindowTokens = payloadInt(payload, "context_window_tokens")
	record.PromptBudget = payloadInt(payload, "prompt_budget")
	// 首字时间与上下文事实同层：失败请求（首字后中断/超时）同样保留该观测。
	record.FirstTokenMS = payloadInt64OrZero(payload, "first_token_ms")

	if in.Interrupted {
		record.Status = RequestStatusError
		record.ErrorCategory = errorCategoryInterrupted
		record.CacheStatus = CacheStatusError
		return record
	}
	if !payloadBool(payload, "success") {
		record.Status = RequestStatusError
		record.ErrorCategory = payloadString(payload, "error_code")
		if record.ErrorCategory == "" {
			record.ErrorCategory = "error"
		}
		record.CacheStatus = CacheStatusError
		return record
	}
	record.Status = RequestStatusSuccess
	usage := buildUsage(payload)
	if usage == nil {
		record.CacheStatus = CacheStatusNotReported
		return record
	}
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
	return record
}
