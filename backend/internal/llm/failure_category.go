package llm

import "strings"

// FailureCategory 是跨侧统一的失败分类枚举（方案 D5）。
//
// 全仓只保留这一处"错误码 → 失败分类"的映射：usageanalytics 的 ingest、
// agent 的 subagent 恢复建议、以及查询层诊断都消费同一函数，避免三处各写
// 一套分类导致统计口径漂移。
const (
	FailureCategoryProviderError   = "provider_error"
	FailureCategoryRateLimited     = "rate_limited"
	FailureCategoryTimeout         = "timeout"
	FailureCategoryContextOverflow = "context_overflow"
	FailureCategoryToolError       = "tool_error"
	FailureCategoryBudgetExceeded  = "budget_exceeded"
	FailureCategoryCancelled       = "cancelled"
	FailureCategoryInterrupted     = "interrupted"
	FailureCategoryUnknown         = "unknown"
)

// NormalizeFailureCategory 把调用方显式给出的分类归一化到 D5 枚举。
// 接受枚举别名（rate_limit→rate_limited、context_length→context_overflow 等），
// 无法识别时返回 unknown（调用方不应据此覆盖已有更精确分类，除非显式要求）。
func NormalizeFailureCategory(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "":
		return ""
	case FailureCategoryProviderError, FailureCategoryRateLimited, FailureCategoryTimeout,
		FailureCategoryContextOverflow, FailureCategoryToolError, FailureCategoryBudgetExceeded,
		FailureCategoryCancelled, FailureCategoryInterrupted, FailureCategoryUnknown:
		return normalized
	case "rate_limit", "rate_limited_exceeded", "throttled":
		return FailureCategoryRateLimited
	case "context_length", "context_window", "context_overflow_exceeded", "token_limit":
		return FailureCategoryContextOverflow
	case "budget", "token_budget", "cost_budget", "step_limit", "tool_call_limit":
		return FailureCategoryBudgetExceeded
	case "canceled", "user_cancelled", "user_canceled":
		return FailureCategoryCancelled
	case "stream_interrupted", "connection_reset", "aborted":
		return FailureCategoryInterrupted
	case "tool", "tool_failure":
		return FailureCategoryToolError
	case "upstream", "provider", "upstream_error":
		return FailureCategoryProviderError
	case "timeout_error", "deadline_exceeded":
		return FailureCategoryTimeout
	}
	return FailureCategoryUnknown
}

// FailureCategoryFromErrorCode 把 ClassifyFailureCode 的稳定错误码（UPPER_SNAKE）
// 或工具/子代理侧的错误码映射到 D5 枚举。无法判定时返回 unknown，绝不猜测为
// 具体类别（否则诊断 Top-N 会出现假阳性）。
func FailureCategoryFromErrorCode(code string) string {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if normalized == "" {
		return FailureCategoryUnknown
	}
	switch normalized {
	case "USER_CANCELLED", "CANCELLED", "CANCELED", "CONTEXT_CANCELED":
		return FailureCategoryCancelled
	case "CONTEXT_BUDGET_EXCEEDED", "CONTEXT_OVERFLOW", "CONTEXT_LENGTH_EXCEEDED":
		return FailureCategoryContextOverflow
	case "BUDGET_EXCEEDED", "TOKEN_BUDGET_EXCEEDED", "COST_BUDGET_EXCEEDED", "STEP_LIMIT_REACHED", "TOOL_CALL_LIMIT_REACHED":
		return FailureCategoryBudgetExceeded
	case "UPSTREAM_RATE_LIMITED", "UPSTREAM_QUOTA_EXHAUSTED", "RATE_LIMITED":
		return FailureCategoryRateLimited
	case "UPSTREAM_UNAVAILABLE", "UPSTREAM_INVALID_REQUEST", "UPSTREAM_INVALID_RESPONSE",
		"UPSTREAM_ERROR", "CONTENT_FILTERED", "PROVIDER_ERROR":
		return FailureCategoryProviderError
	case "STREAM_INTERRUPTED", "INTERRUPTED", "CONNECTION_RESET":
		return FailureCategoryInterrupted
	case "PERMISSION_DENIED", "TOOL_ERROR", "TOOL_DENIED", "HOOK_BLOCKED":
		return FailureCategoryToolError
	// STALE_CONTEXT 是编辑/补丁的旧内容失配（apply_patch/edit 的 old_string、
	// @@ 上下文不再匹配），与「模型窗口超限」是两件不同的事。它必须先于下面
	// 的 CONTEXT 子串兜底命中，否则会被误报成 context_overflow，把排查方向
	// 引向「压缩对话/扩大窗口」。（2026-09-27 证据：139 条 STALE_CONTEXT 被
	// 展示为上下文超限。）
	case "STALE_CONTEXT", "STALE_EDIT", "STALE_PATCH_CONTEXT":
		return FailureCategoryToolError
	// AGENT_READ_ONLY 是只读执行边界的策略拒绝（不可被审批/bypass 覆盖）。
	// 它既不是未知错误，也不是模型上下文问题；归类为工具侧失败，具体语义由
	// 错误码本身与 next_action 表达。
	case "AGENT_READ_ONLY", "AGENT_PERMISSION_DENIED":
		return FailureCategoryToolError
	// 会话租约/状态冲突是监督状态机语义，不是「未知」。
	case "SESSION_LEASE_CONFLICT", "AGENT_RUN_SUPERSEDED", "AGENT_BUSY":
		return FailureCategoryToolError
	}
	// 子串兜底：错误码来自多个包（llm / toolresult / runtimeerrors），
	// 前缀不统一，但语义关键词是稳定的。
	switch {
	case strings.Contains(normalized, "TIMEOUT") || strings.Contains(normalized, "DEADLINE"):
		return FailureCategoryTimeout
	case strings.Contains(normalized, "RATE_LIMIT") || strings.Contains(normalized, "THROTTL"):
		return FailureCategoryRateLimited
	case strings.Contains(normalized, "CONTEXT"):
		return FailureCategoryContextOverflow
	case strings.Contains(normalized, "BUDGET") || strings.Contains(normalized, "LIMIT_REACHED"):
		return FailureCategoryBudgetExceeded
	case strings.Contains(normalized, "CANCEL"):
		return FailureCategoryCancelled
	case strings.Contains(normalized, "INTERRUPT"):
		return FailureCategoryInterrupted
	case strings.Contains(normalized, "PERMISSION") || strings.Contains(normalized, "TOOL_") || strings.Contains(normalized, "HOOK"):
		return FailureCategoryToolError
	// 2026-09-30 真机：子代理任务因工具面问题终局时，错误原文是
	// "tool not found: commands"（模型给出不存在的工具名）与
	// "policy:capability not allowed by execution policy: exec_shell"
	// （执行策略拒绝）。两者都是确定性的工具侧失败，此前落到 unknown：
	// 父模型拿到空建议后只能反复重派新子代理（该会话 10 次失败、单次烧掉
	// 数百万 token）。归类为 tool_error 后 retry_advice 给出
	// complete_locally_or_change_tool，父模型可立即改道。
	case strings.Contains(normalized, "TOOL NOT FOUND") || strings.Contains(normalized, "UNKNOWN TOOL"):
		return FailureCategoryToolError
	case strings.Contains(normalized, "NOT ALLOWED BY EXECUTION POLICY") || strings.Contains(normalized, "CAPABILITY NOT ALLOWED"):
		return FailureCategoryToolError
	// 2026-10-02 真机：spawn_subagents 只给子代理发 tools_whitelist:["shell"]
	// 时，子代理调用 glob/ls/grep 被 agent/loop.go 的白名单闸门拒绝，终局错误
	// 原文是 "tool not allowed for this agent: glob; ..."。这是上一条同类的
	// 确定性工具侧拒绝，但措辞不同，同样漏在兜底之外：ClassifyFailureCode 先把
	// 它压成笼统的 UPSTREAM_ERROR，本函数的文本兜底又认不出这句话，于是
	// provider_error 留下、Retryable=true、retry_advice=retry_with_changed_inputs
	// ——重试绝不可能成功，父代理却被引导去重试。
	case strings.Contains(normalized, "NOT ALLOWED FOR THIS AGENT"):
		return FailureCategoryToolError
	case strings.Contains(normalized, "UPSTREAM") || strings.Contains(normalized, "PROVIDER"):
		return FailureCategoryProviderError
	}
	return FailureCategoryUnknown
}
