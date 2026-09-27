package toolbroker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The 2026-09-27 incident: a tool_progress read whose raw window was entirely
// reasoning deltas returned count=0 with "36 events returned" guidance, and the
// parent then re-read an older cursor to find evidence. The projection must scan
// bounded raw pages until it can show tool/lifecycle events or prove there are
// none, and its guidance must always point forward.
func TestReadAgentEventsWithView_ScansPastReasoningPages(t *testing.T) {
	controller := newScriptedReadController(
		AgentEventsResult{
			SessionID: "child-1", LatestSeq: 10, HasMore: true, UnreadCount: 2,
			Events: []AgentEventItem{
				{Seq: 9, Type: "assistant.reasoning"},
				{Seq: 10, Type: "assistant.reasoning"},
			},
		},
		AgentEventsResult{
			SessionID: "child-1", LatestSeq: 11,
			Events: []AgentEventItem{{Seq: 11, Type: "tool.completed", ToolName: "view"}},
		},
	)
	broker := &Broker{AgentSessions: controller}

	result, _ := executeReadAgentEvents(t, broker, "parent-session", map[string]interface{}{
		"id":        "child-1",
		"after_seq": 8,
		"limit":     2,
		"view":      "tool_progress",
	})

	require.Equal(t, "tool_progress", result.View)
	require.Equal(t, 1, result.Count, "the tool event from the second raw page must be visible")
	require.Equal(t, 2, result.Filtered, "both reasoning deltas were dropped")
	require.False(t, result.HasMore)
	require.Equal(t, int64(11), result.LatestSeq)
	require.Len(t, result.Events, 1)
	require.Equal(t, "tool.completed", result.Events[0].Type)
	require.Equal(t, 2, controller.calls, "the scan must continue past the invisible first page")
	require.Contains(t, result.NextAction, "after_seq=11")
}

func TestReadAgentEventsWithView_EmptyProjectionKeepsForwardCursor(t *testing.T) {
	windows := make([]AgentEventsResult, 0, 17)
	for index := 0; index < 16; index++ {
		windows = append(windows, AgentEventsResult{
			SessionID: "child-1", LatestSeq: int64(100 + index), HasMore: true, UnreadCount: 3,
			Events: []AgentEventItem{{Seq: int64(100 + index), Type: "assistant.reasoning"}},
		})
	}
	windows = append(windows, AgentEventsResult{
		SessionID: "child-1", LatestSeq: 200,
		Events: []AgentEventItem{{Seq: 200, Type: "tool.completed", ToolName: "view"}},
	})
	controller := newScriptedReadController(windows...)
	broker := &Broker{AgentSessions: controller}

	result, _ := executeReadAgentEvents(t, broker, "parent-session", map[string]interface{}{
		"id":        "child-1",
		"after_seq": 99,
		"limit":     1,
		"view":      "tool_progress",
	})

	require.Zero(t, result.Count, "a fully hidden raw window must not claim visible events")
	require.True(t, result.HasMore)
	require.Equal(t, agentEventsViewScanMaxPages, controller.calls, "the scan is bounded by pages")
	require.Equal(t, 16, result.Filtered)
	require.Contains(t, result.NextAction, "continue_tool_progress_scan")
	require.Contains(t, result.NextAction, "after_seq=115",
		"guidance must advance to the raw high-water cursor, never back to the caller's original cursor")
	require.NotContains(t, result.NextAction, "after_seq=99")
}

func TestReadAgentEventsWithView_AllViewStaysRawAndSinglePage(t *testing.T) {
	controller := newScriptedReadController(AgentEventsResult{
		SessionID: "child-1", Count: 2, LatestSeq: 7,
		Events: []AgentEventItem{
			{Seq: 6, Type: "assistant.reasoning"},
			{Seq: 7, Type: "tool.completed"},
		},
	})
	broker := &Broker{AgentSessions: controller}

	result, _ := executeReadAgentEvents(t, broker, "parent-session", map[string]interface{}{
		"id":        "child-1",
		"after_seq": 5,
		"limit":     20,
	})

	require.Empty(t, result.View, "no view must preserve the raw result shape")
	require.Equal(t, 2, result.Count)
	require.Zero(t, result.Filtered, "raw reads must never be filtered")
	require.Equal(t, 1, controller.calls, "raw reads must not scan extra pages")
}

func TestReadAgentEventsWithView_UnknownViewDegradesToAll(t *testing.T) {
	controller := newScriptedReadController(AgentEventsResult{
		SessionID: "child-1", Count: 1, LatestSeq: 3,
		Events: []AgentEventItem{{Seq: 3, Type: "assistant.reasoning"}},
	})
	broker := &Broker{AgentSessions: controller}

	result, _ := executeReadAgentEvents(t, broker, "parent-session", map[string]interface{}{
		"id":        "child-1",
		"after_seq": 0,
		"limit":     20,
		"view":      "typo",
	})

	require.Empty(t, result.View)
	require.Equal(t, 1, result.Count)
	require.Equal(t, 1, controller.calls)
}

func TestReadAgentEventsWithView_ApprovalStopsScan(t *testing.T) {
	controller := newScriptedReadController(
		AgentEventsResult{
			SessionID: "child-1", LatestSeq: 4, HasMore: true, UnreadCount: 9,
			Events: []AgentEventItem{
				{Seq: 3, Type: "assistant.reasoning"},
				{Seq: 4, Type: "approval_requested", Payload: map[string]interface{}{"request_id": "req-1"}},
			},
		},
		AgentEventsResult{
			SessionID: "child-1", LatestSeq: 5,
			Events: []AgentEventItem{{Seq: 5, Type: "tool.completed"}},
		},
	)
	broker := &Broker{AgentSessions: controller}

	result, _ := executeReadAgentEvents(t, broker, "parent-session", map[string]interface{}{
		"id":        "child-1",
		"after_seq": 2,
		"limit":     5,
		"view":      "tool_progress",
	})

	require.Equal(t, 1, controller.calls, "an approval must surface without scanning further pages")
	require.Equal(t, 1, result.Count)
	require.Equal(t, "approval_requested", result.Events[0].Type)
	require.Contains(t, result.NextAction, "resolve_pending_approval")
}

// Guards the constant the guidance depends on: a misconfigured scan bound would
// either loop forever or return an empty page without forward guidance.
func TestAgentEventsViewScanBoundsAreSane(t *testing.T) {
	require.Greater(t, agentEventsViewScanMaxPages, 1)
	require.Greater(t, agentEventsViewScanMaxRawEvents, 1)
	require.Equal(t, 20, agentEventsDefaultReadLimit)
}
