package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestDoomLoopTracker_WarnsAtThresholdWithoutStoppingWhenHardStopDisabled(t *testing.T) {
	tracker := NewDoomLoopTracker(0) // hard stop off (product default)
	call := []types.ToolCall{{
		ID:   "c1",
		Name: "view",
		Args: map[string]interface{}{"file_path": "a.go"},
	}}

	var last DoomLoopObservation
	for i := 1; i <= DoomLoopWarningThreshold; i++ {
		last = tracker.ObserveSemanticToolBatch(call)
		require.Equal(t, i, last.RepeatCount)
		require.False(t, last.ShouldStop)
		if i == DoomLoopWarningThreshold {
			require.True(t, last.EmitWarning)
		} else {
			require.False(t, last.EmitWarning)
		}
	}
	// Crossing threshold again must not re-emit warning for the same fingerprint streak.
	again := tracker.ObserveSemanticToolBatch(call)
	require.Equal(t, DoomLoopWarningThreshold+1, again.RepeatCount)
	require.False(t, again.EmitWarning)
	require.False(t, again.ShouldStop)
	require.NotEmpty(t, again.Advisory)
}

func TestDoomLoopTracker_HardStopAtConfiguredLimit(t *testing.T) {
	tracker := NewDoomLoopTracker(3)
	call := []types.ToolCall{{
		Name: "read_logs",
		Args: map[string]interface{}{"path": "app.log"},
	}}
	require.False(t, tracker.ObserveSemanticToolBatch(call).ShouldStop)
	require.False(t, tracker.ObserveSemanticToolBatch(call).ShouldStop)
	obs := tracker.ObserveSemanticToolBatch(call)
	require.True(t, obs.ShouldStop)
	require.Equal(t, 3, obs.StopLimit)
	require.Equal(t, "repeated_tool_calls", obs.LimitReason)
	require.Equal(t, 3, obs.RepeatCount)
}

func TestDoomLoopTracker_ResetsOnDifferentArgs(t *testing.T) {
	tracker := NewDoomLoopTracker(0)
	a := []types.ToolCall{{Name: "view", Args: map[string]interface{}{"file_path": "a.go"}}}
	b := []types.ToolCall{{Name: "view", Args: map[string]interface{}{"file_path": "b.go"}}}
	require.Equal(t, 1, tracker.ObserveSemanticToolBatch(a).RepeatCount)
	require.Equal(t, 2, tracker.ObserveSemanticToolBatch(a).RepeatCount)
	obs := tracker.ObserveSemanticToolBatch(b)
	require.Equal(t, 1, obs.RepeatCount)
	require.False(t, obs.EmitWarning)
	require.NotEqual(t, "", obs.Fingerprint)
}

func TestDoomLoopTracker_ExemptsPollingTools(t *testing.T) {
	tracker := NewDoomLoopTracker(2)
	// Seed a real streak first.
	seed := []types.ToolCall{{Name: "view", Args: map[string]interface{}{"file_path": "x.go"}}}
	require.Equal(t, 1, tracker.ObserveSemanticToolBatch(seed).RepeatCount)
	// Polling tools clear the tracker and never count.
	poll := []types.ToolCall{{Name: "wait_agent", Args: map[string]interface{}{"id": "child-1"}}}
	obs := tracker.ObserveSemanticToolBatch(poll)
	require.Empty(t, obs.Fingerprint)
	require.Zero(t, obs.RepeatCount)
	require.False(t, obs.ShouldStop)
	// After exempt batch, a new real call starts at 1.
	require.Equal(t, 1, tracker.ObserveSemanticToolBatch(seed).RepeatCount)
}

func TestSemanticToolCallFingerprintStableAndExempt(t *testing.T) {
	a := semanticToolCallFingerprint([]types.ToolCall{{
		Name: "View",
		Args: map[string]interface{}{"file_path": "a.go"},
	}})
	b := semanticToolCallFingerprint([]types.ToolCall{{
		Name: "view",
		Args: map[string]interface{}{"file_path": "a.go"},
	}})
	require.NotEmpty(t, a)
	require.Equal(t, a, b)
	require.Empty(t, semanticToolCallFingerprint([]types.ToolCall{{
		Name: "background_task",
		Args: map[string]interface{}{"task_id": "t1"},
	}}))
}

func TestDoomLoopEventPayloads(t *testing.T) {
	obs := DoomLoopObservation{
		Fingerprint:   "abc",
		RepeatCount:   4,
		ToolCallCount: 1,
		StopLimit:     8,
		LimitReason:   "repeated_tool_calls",
	}
	warn := DoomLoopWarningPayload("tr", 3, obs)
	require.Equal(t, "warning", warn["phase"])
	require.Equal(t, "semantic_tool_repeat", warn["kind"])
	require.Equal(t, DoomLoopWarningThreshold, warn["warning_threshold"])
	require.Equal(t, 4, warn["repeat_count"])

	term := DoomLoopTerminationPayload("tr", 5, obs)
	require.Equal(t, "terminated", term["phase"])
	require.Equal(t, 8, term["stop_limit"])
	require.Equal(t, "repeated_tool_calls", term["limit_reason"])
}
