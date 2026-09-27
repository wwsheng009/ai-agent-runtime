package llm

import "testing"

// TestFailureCategoryFromErrorCode_StaleContextIsNotContextOverflow 锁定
// 2026-09-27 的关键误分类：STALE_CONTEXT 是编辑/补丁上下文失配，
// 不是模型窗口超限；它必须归类为工具侧失败，而不是 context_overflow。
func TestFailureCategoryFromErrorCode_StaleContextIsNotContextOverflow(t *testing.T) {
	cases := map[string]string{
		"STALE_CONTEXT":          FailureCategoryToolError,
		"TOOL_PATH_NOT_FOUND":    FailureCategoryToolError,
		"AGENT_READ_ONLY":        FailureCategoryToolError,
		"SESSION_LEASE_CONFLICT": FailureCategoryToolError,
		// 真正的窗口超限必须保持原语义。
		"CONTEXT_BUDGET_EXCEEDED": FailureCategoryContextOverflow,
		"CONTEXT_OVERFLOW":        FailureCategoryContextOverflow,
		// 超时/取消/上游分类不受影响。
		"TOOL_TIMEOUT":              FailureCategoryTimeout,
		"USER_CANCELLED":            FailureCategoryCancelled,
		"UPSTREAM_INVALID_RESPONSE": FailureCategoryProviderError,
		"STREAM_INTERRUPTED":        FailureCategoryInterrupted,
		"TOOL_INVALID_ARGS":         FailureCategoryToolError,
		// 未知码不猜测。
		"SOMETHING_NEW": FailureCategoryUnknown,
	}
	for code, want := range cases {
		if got := FailureCategoryFromErrorCode(code); got != want {
			t.Errorf("FailureCategoryFromErrorCode(%q) = %q, want %q", code, got, want)
		}
	}
}

// TestNormalizeFailureCategory_AcceptsMappedOutput 保证面板/查询层拿到的
// D5 分类再归一化时保持稳定（幂等），用于 requests 来源统一映射。
func TestNormalizeFailureCategory_AcceptsMappedOutput(t *testing.T) {
	for _, code := range []string{
		"UPSTREAM_INVALID_RESPONSE",
		"STALE_CONTEXT",
		"AGENT_READ_ONLY",
		"USER_CANCELLED",
		"TOOL_TIMEOUT",
	} {
		category := FailureCategoryFromErrorCode(code)
		if got := NormalizeFailureCategory(category); got != category {
			t.Errorf("NormalizeFailureCategory(%q)=%q, want idempotent %q", category, got, category)
		}
	}
}
