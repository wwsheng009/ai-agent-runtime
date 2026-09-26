package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestRenderTurnSuspensionTimelineEvents 钉住 G3 在 CLI 时间线上的表达（设计 §6.8：
// 挂起期必须让用户看见"托管中"，否则"等待子任务"与"卡死"在对话流里不可区分；
// 挂起期仍会收到 agent.turn.finished，那是"本次 run 结束"，不是托管 turn 结束）。
func TestRenderTurnSuspensionTimelineEvents(t *testing.T) {
	suspended := renderChatRuntimeTimelineEvent(runtimeevents.Event{
		Type: runtimeevents.EventTurnSuspended,
		Payload: map[string]interface{}{
			"turn_id":          "turn-park-1",
			"batch_id":         "batch-1",
			"obligation_count": 3,
		},
	})
	require.Contains(t, suspended.Line, "托管挂起")
	require.Contains(t, suspended.Line, "3 个义务")
	require.Contains(t, suspended.Line, "batch-1")
	require.NotNil(t, suspended.Timeline)
	require.Equal(t, "batch-1", suspended.DedupKey, "同一 batch 的重复投影不得在时间线上刷屏")

	// 终局 resume：terminal=true ⇒ success 状态 + 明确的收尾指引。
	resumedTerminal := renderChatRuntimeTimelineEvent(runtimeevents.Event{
		Type: runtimeevents.EventTurnResumed,
		Payload: map[string]interface{}{
			"turn_id":       "turn-park-1",
			"trigger":       supervision.ResumeTriggerTerminal,
			"pending_count": 0,
			"terminal":      true,
		},
	})
	require.Contains(t, resumedTerminal.Line, "托管恢复")
	require.Contains(t, resumedTerminal.Line, supervision.ResumeTriggerTerminal)
	require.Contains(t, resumedTerminal.Line, "pending=0")
	require.Contains(t, resumedTerminal.Line, "终局报告")
	require.NotNil(t, resumedTerminal.Timeline)

	// 非终局 resume（progress/approval）：信息态，不宣告"可收尾"。
	resumedProgress := renderChatRuntimeTimelineEvent(runtimeevents.Event{
		Type: runtimeevents.EventTurnResumed,
		Payload: map[string]interface{}{
			"trigger":       supervision.ResumeTriggerProgress,
			"pending_count": 2,
		},
	})
	require.Contains(t, resumedProgress.Line, "托管恢复")
	require.Contains(t, resumedProgress.Line, "pending=2")
	require.NotContains(t, resumedProgress.Line, "终局报告")

	// 数字键缺失时降级文案不能假装 0（未知 ≠ 零义务）。
	missingCount := renderChatRuntimeTimelineEvent(runtimeevents.Event{
		Type:    runtimeevents.EventTurnSuspended,
		Payload: map[string]interface{}{"turn_id": "turn-park-2"},
	})
	require.Contains(t, missingCount.Line, "托管挂起")
	require.NotContains(t, missingCount.Line, "0 个义务")

	// 事件载荷经 JSON 回放后数字是 float64/json.Number：必须仍能读出计数。
	replayed := renderChatRuntimeTimelineEvent(runtimeevents.Event{
		Type: runtimeevents.EventTurnSuspended,
		Payload: map[string]interface{}{
			"obligation_count": float64(4),
		},
	})
	require.Contains(t, replayed.Line, "4 个义务")
}
