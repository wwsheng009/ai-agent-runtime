package usageanalytics

import (
	"testing"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// TestSubagentCompletedPayloadNormalized 锁定方案 §2.1 / D4：两个生产者的载荷
// 经共享归一化后字段同构；读取侧对历史（仅 status / 两者皆缺）兼容。
func TestSubagentCompletedPayloadNormalized(t *testing.T) {
	t.Run("scheduler_style_success_only", func(t *testing.T) {
		payload := map[string]interface{}{
			"subagent_id":       "child-1",
			"parent_session_id": "session-1",
			"read_only":         true,
			"success":           false,
			"error_code":        "UPSTREAM_RATE_LIMITED",
			"source":            "scheduler",
			"attempt":           2,
			"max_attempts":      2,
		}
		NormalizeSubagentCompletionPayload(payload, "")
		if payload["status"] != "failed" {
			t.Fatalf("success=false 应补 status=failed，实际 %v", payload["status"])
		}
		if payload["completion_reason"] != "failed" {
			t.Fatalf("completion_reason 不符：%v", payload["completion_reason"])
		}
		if payload["failure_category"] != llm.FailureCategoryRateLimited {
			t.Fatalf("error_code 应推导 rate_limited，实际 %v", payload["failure_category"])
		}
		if payload["attempt"] != 2 || payload["max_attempts"] != 2 {
			t.Fatalf("attempt 字段不得被覆盖：%v", payload)
		}
		if payload["source"] != "scheduler" {
			t.Fatalf("生产者显式 source 不得被覆盖：%v", payload["source"])
		}
	})

	t.Run("controller_style_status_only", func(t *testing.T) {
		payload := map[string]interface{}{
			"agent_id":          "child-2",
			"parent_session_id": "session-1",
			"status":            "idle",
			"control_action":    "close",
		}
		NormalizeSubagentCompletionPayload(payload, "")
		if payload["success"] != true {
			t.Fatalf("status=idle 应映射 success=true，实际 %v", payload["success"])
		}
		if payload["completion_reason"] != SubagentCompletionCompleted {
			t.Fatalf("completion_reason 应为 completed，实际 %v", payload["completion_reason"])
		}
		if _, exists := payload["failure_category"]; exists {
			t.Fatalf("成功结果不得写 failure_category：%v", payload["failure_category"])
		}
		if payload["attempt"] != 1 || payload["max_attempts"] != 1 {
			t.Fatalf("缺省 attempt 应为 1/1：%v", payload)
		}
	})

	t.Run("read_side_matches_write_side", func(t *testing.T) {
		payload := map[string]interface{}{
			"subagent_id":       "child-3",
			"parent_session_id": "session-1",
			"success":           false,
			"error_code":        "TOOL_TIMEOUT",
			"attempt":           2,
			"max_attempts":      2,
		}
		NormalizeSubagentCompletionPayload(payload, "")
		normalized, ok := NormalizeSubagentCompletion(runtimeevents.Event{
			Type:      EventSubagentCompleted,
			SessionID: "session-1",
			Payload:   payload,
		})
		if !ok {
			t.Fatal("归一化后的载荷必须可被读取侧识别")
		}
		if normalized.Success == nil || *normalized.Success {
			t.Fatalf("读取侧 success 应为 false：%v", normalized.Success)
		}
		if normalized.CompletionReason != SubagentCompletionFailed {
			t.Fatalf("读取侧 completion_reason 不符：%q", normalized.CompletionReason)
		}
		if normalized.FailureCategory != llm.FailureCategoryTimeout {
			t.Fatalf("读取侧 failure_category 应为 timeout：%q", normalized.FailureCategory)
		}
		if normalized.Attempt != 2 || normalized.MaxAttempts != 2 {
			t.Fatalf("读取侧 attempt 不符：%d/%d", normalized.Attempt, normalized.MaxAttempts)
		}
	})

	t.Run("history_without_fields_stays_unknown", func(t *testing.T) {
		// 历史 26 行：两个字段都缺 → unknown，且不得计入失败。
		normalized, ok := NormalizeSubagentCompletion(runtimeevents.Event{
			Type:      EventSubagentCompleted,
			SessionID: "session-legacy",
			Payload: map[string]interface{}{
				"agent_id":          "child-legacy",
				"parent_session_id": "session-legacy",
			},
		})
		if !ok {
			t.Fatal("带 id 的历史行应可识别")
		}
		if normalized.Success != nil {
			t.Fatalf("缺字段历史行 success 应为 nil，实际 %v", *normalized.Success)
		}
		if normalized.CompletionReason != SubagentCompletionUnknown {
			t.Fatalf("缺字段历史行应为 unknown，实际 %q", normalized.CompletionReason)
		}
		if normalized.FailureCategory != "" {
			t.Fatalf("unknown 行不得写 failure_category，实际 %q", normalized.FailureCategory)
		}
	})

	t.Run("conflict_prefers_success", func(t *testing.T) {
		payload := map[string]interface{}{
			"subagent_id":       "child-conflict",
			"parent_session_id": "session-1",
			"success":           true,
			"status":            "failed",
		}
		NormalizeSubagentCompletionPayload(payload, "")
		normalized, ok := NormalizeSubagentCompletion(runtimeevents.Event{
			Type:      EventSubagentCompleted,
			SessionID: "session-1",
			Payload:   payload,
		})
		if !ok {
			t.Fatal("冲突载荷应可识别")
		}
		if normalized.Success == nil || !*normalized.Success {
			t.Fatalf("冲突时应以 success 为准，实际 %v", normalized.Success)
		}
		if !normalized.Conflict {
			t.Fatal("success 与 status 冲突必须被标记（conflict_count）")
		}
	})
}
