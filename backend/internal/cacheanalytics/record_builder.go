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
	// ParentSessionID / RootSessionID / SubagentID 是子代理请求的父链归属
	// （可显式传入；为空时回退到 Payload 同名键）。RootSessionID 为空时
	// 由 SessionID/ParentSessionID 兜底推导，保证新记录总有 root 值。
	ParentSessionID string
	RootSessionID   string
	SubagentID      string
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
	payload := in.Payload
	parentSessionID := firstNonEmptyString(in.ParentSessionID, payloadString(payload, "parent_session_id"))
	rootSessionID := firstNonEmptyString(in.RootSessionID, payloadString(payload, "root_session_id"))
	if rootSessionID == "" {
		// 兜底：有父链（1 级）时父即根；无父链时根即自身。深层嵌套由采集侧
		// 显式传入 root，不做猜测式回溯。
		rootSessionID = firstNonEmptyString(parentSessionID, in.SessionID)
	}
	record := CacheRequestRecord{
		SchemaVersion:     SchemaVersion,
		LLMRequestID:      in.LLMRequestID,
		SessionID:         in.SessionID,
		ParentSessionID:   parentSessionID,
		RootSessionID:     rootSessionID,
		SubagentID:        firstNonEmptyString(in.SubagentID, payloadString(payload, "subagent_id")),
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
	record.ContextPromptTokens = payloadInt(payload, "context_prompt_tokens")
	record.ContextWindowTokens = payloadInt(payload, "context_window_tokens")
	record.PromptBudget = payloadInt(payload, "prompt_budget")
	// 首字时间与上下文事实同层：失败请求（首字后中断/超时）同样保留该观测。
	record.FirstTokenMS = payloadInt64OrZero(payload, "first_token_ms")
	// P0-2 证据列：终结信号、参数错误分类与取消归因来自 llm.request.finished 载荷；
	// 缺省零值表示未观测（历史记录 / 载荷未带该字段）。
	record.TerminalSeen = payloadBool(payload, "terminal_seen")
	record.ArgErrorClass = payloadString(payload, "arg_error_class")
	record.CancelSource = payloadString(payload, "cancel_source")
	record.CancelCause = payloadString(payload, "cancel_cause")
	record.CancelReason = payloadString(payload, "cancel_reason")

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
	// 比率分母用完整输入总量（不含式口径下 prompt 只是新增输入，用它会得到
	// >100% 的命中率）。载荷里的 usage_cache_hit_ratio 可能来自旧进程的
	// prompt 口径，故只要输入明细可用就本地重算，仅在缺失时退回载荷值。
	inputTotal := usage.InputTotal()
	if usage.CacheReadReported && inputTotal > 0 {
		ratio := float64(usage.CacheReadTokens) / float64(inputTotal)
		record.CacheHitRatio = &ratio
	} else if ratio, ok := payloadFloat(payload, "usage_cache_hit_ratio"); ok && usage.CacheReadReported {
		record.CacheHitRatio = &ratio
	}
	if usage.CacheCreationReported && inputTotal > 0 {
		ratio := float64(usage.CacheCreationTokens) / float64(inputTotal)
		record.CacheWriteRatio = &ratio
	}
	return record
}
