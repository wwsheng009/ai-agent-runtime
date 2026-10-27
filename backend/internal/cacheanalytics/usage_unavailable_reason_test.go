package cacheanalytics

import (
	"testing"
	"time"
)

// TestUsageUnavailableReasonStages 锁定 §8.2：请求没有 usage 时必须显式说明
// 为什么——request_error（error/interrupted）与 usage_not_reported（成功但
// provider 未上报）；有完整 usage 的成功请求保持空串（未观测）。
func TestUsageUnavailableReasonStages(t *testing.T) {
	finishedAt := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	base := TerminalRecordInput{
		LLMRequestID: "req-uu-1",
		SessionID:    "sess-uu",
		StartedAt:    finishedAt.Add(-time.Second),
		FinishedAt:   finishedAt,
		Payload:      map[string]interface{}{},
	}

	// error：请求失败 → request_error。
	errPayload := map[string]interface{}{"success": false, "error_code": "UPSTREAM_UNAVAILABLE"}
	errorRecord := BuildTerminalRecord(mergeTerminalPayload(base, errPayload))
	if errorRecord.UsageUnavailableReason != "request_error" {
		t.Fatalf("error request reason = %q, want request_error", errorRecord.UsageUnavailableReason)
	}
	if errorRecord.CacheStatus != CacheStatusError {
		t.Fatalf("error request cache status = %q", errorRecord.CacheStatus)
	}

	// interrupted：会话终止兜底 → request_error。
	interrupted := base
	interrupted.Interrupted = true
	interruptedRecord := BuildTerminalRecord(interrupted)
	if interruptedRecord.UsageUnavailableReason != "request_error" {
		t.Fatalf("interrupted reason = %q, want request_error", interruptedRecord.UsageUnavailableReason)
	}

	// 成功但无 usage 明细 → usage_not_reported。
	successNoUsage := base
	successNoUsage.Payload = map[string]interface{}{"success": true}
	noUsageRecord := BuildTerminalRecord(successNoUsage)
	if noUsageRecord.UsageUnavailableReason != "usage_not_reported" {
		t.Fatalf("success without usage reason = %q, want usage_not_reported", noUsageRecord.UsageUnavailableReason)
	}
	if noUsageRecord.CacheStatus != CacheStatusNotReported {
		t.Fatalf("success without usage cache status = %q", noUsageRecord.CacheStatus)
	}

	// 成功且带 usage → 空串（有完整用量，无"不可用"语义）。
	successWithUsage := base
	successWithUsage.Payload = map[string]interface{}{
		"success":             true,
		"usage_total_tokens":  42,
		"usage_prompt_tokens": 40,
		"usage_source":        "provider",
	}
	withUsageRecord := BuildTerminalRecord(successWithUsage)
	if withUsageRecord.UsageUnavailableReason != "" {
		t.Fatalf("success with usage must not carry a reason, got %q", withUsageRecord.UsageUnavailableReason)
	}
}

func mergeTerminalPayload(base TerminalRecordInput, payload map[string]interface{}) TerminalRecordInput {
	merged := base
	merged.Payload = payload
	return merged
}
