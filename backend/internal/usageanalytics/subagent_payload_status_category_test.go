package usageanalytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// TestNormalizeSubagentCompletionPayloadStatusCategory 锁定 §13.7 观察收敛：
// 轻量子代理的"控制面/用户停止"不再落 unknown（避免把主动关闭混进真实失败），
// 但无细节的 failed 仍是 unknown（不猜测）；显式 category 与 error_code 优先。
func TestNormalizeSubagentCompletionPayloadStatusCategory(t *testing.T) {
	cases := []struct {
		name         string
		payload      map[string]interface{}
		statusHint   string
		wantCategory string
		wantSuccess  bool
	}{
		{
			name:         "stopped control stop -> cancelled",
			payload:      map[string]interface{}{"subagent_id": "sa-1", "status": "stopped", "success": false},
			wantCategory: llm.FailureCategoryCancelled,
		},
		{
			name:         "interrupted alias -> cancelled",
			payload:      map[string]interface{}{"subagent_id": "sa-2", "status": "interrupted", "success": false},
			wantCategory: llm.FailureCategoryCancelled,
		},
		{
			// 只给 statusHint（无 status/success）：派生 success=false + cancelled。
			name:         "statusHint cancelled derives cancelled",
			payload:      map[string]interface{}{"subagent_id": "sa-3"},
			statusHint:   "cancelled",
			wantCategory: llm.FailureCategoryCancelled,
		},
		{
			name:         "timeout status -> timeout",
			payload:      map[string]interface{}{"subagent_id": "sa-4", "status": "timeout", "success": false},
			wantCategory: llm.FailureCategoryTimeout,
		},
		{
			name:         "opaque failed stays unknown",
			payload:      map[string]interface{}{"subagent_id": "sa-5", "status": "failed", "success": false},
			wantCategory: llm.FailureCategoryUnknown,
		},
		{
			name: "error_code wins over stopped status",
			payload: map[string]interface{}{
				"subagent_id": "sa-6", "status": "stopped", "success": false, "error_code": "UPSTREAM_UNAVAILABLE",
			},
			wantCategory: llm.FailureCategoryProviderError,
		},
		{
			name: "explicit category preserved",
			payload: map[string]interface{}{
				"subagent_id": "sa-7", "status": "stopped", "success": false, "failure_category": "tool_error",
			},
			wantCategory: llm.FailureCategoryToolError,
		},
		{
			name:         "success keeps category empty",
			payload:      map[string]interface{}{"subagent_id": "sa-8", "status": "completed", "success": true},
			wantCategory: "",
			wantSuccess:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := NormalizeSubagentCompletionPayload(tc.payload, tc.statusHint)
			if tc.wantCategory != "" {
				require.Equal(t, tc.wantCategory, out["failure_category"])
			} else {
				require.Empty(t, out["failure_category"], "分类为空时不得写键")
			}
			require.Equal(t, tc.wantSuccess, out["success"])
		})
	}
}

// TestNormalizeSubagentCompletionStatusCategory 是读取侧同规则（历史/原始载荷
// 直接进入 ingest 时也得到同一分类）。
func TestNormalizeSubagentCompletionStatusCategory(t *testing.T) {
	event := func(payload map[string]interface{}) runtimeevents.Event {
		return runtimeevents.Event{
			Type:      EventSubagentCompleted,
			SessionID: "session-status-cat",
			Timestamp: time.Now(),
			Payload:   payload,
		}
	}

	stopped, ok := NormalizeSubagentCompletion(event(map[string]interface{}{
		"subagent_id": "sa-read-1", "parent_session_id": "session-status-cat",
		"success": false, "status": "stopped",
	}))
	require.True(t, ok)
	// success 是权威字段：显式 success=false 时 CompletionReason 归 failed（现有
	// 契约不变）；关键改进在分类——不再落 unknown。
	require.Equal(t, SubagentCompletionFailed, stopped.CompletionReason)
	require.Equal(t, llm.FailureCategoryCancelled, stopped.FailureCategory)

	opaque, ok := NormalizeSubagentCompletion(event(map[string]interface{}{
		"subagent_id": "sa-read-2", "parent_session_id": "session-status-cat",
		"success": false, "status": "failed",
	}))
	require.True(t, ok)
	require.Equal(t, llm.FailureCategoryUnknown, opaque.FailureCategory,
		"无细节的 failed 不得被猜测成具体分类")

	timedOut, ok := NormalizeSubagentCompletion(event(map[string]interface{}{
		"subagent_id": "sa-read-3", "parent_session_id": "session-status-cat",
		"success": false, "status": "timed_out",
	}))
	require.True(t, ok)
	require.Equal(t, llm.FailureCategoryTimeout, timedOut.FailureCategory)
}
