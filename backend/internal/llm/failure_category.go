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
	case strings.Contains(normalized, "UPSTREAM") || strings.Contains(normalized, "PROVIDER"):
		return FailureCategoryProviderError
	}
	return FailureCategoryUnknown
}
