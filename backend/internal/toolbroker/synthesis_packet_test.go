package toolbroker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttachSynthesisPacketBuildsTerminalPacket(t *testing.T) {
	result := &AgentWaitResult{
		Agents: []AgentStatusResult{
			{
				ID:                "child-a",
				SessionID:         "sess-a",
				CurrentTaskID:     "A",
				CurrentTaskStatus: "succeeded",
				Output:            "A completed",
				RouteWarnings:     []string{"reasoning_effort_unsupported_ignored"},
			},
			{
				ID:                "child-b",
				SessionID:         "sess-b",
				CurrentTaskID:     "B",
				CurrentTaskStatus: "failed",
				Error:             "boom",
			},
		},
		PendingCount: 0,
		NextAction:   "finalize",
		Obligations: []AgentWaitObligation{{
			ObligationID: "batch_1",
			SubjectKind:  "batch",
			SubjectID:    "batch_1",
			Terminal:     true,
		}},
	}

	got := AttachSynthesisPacket(result)
	require.NotNil(t, got)
	require.NotNil(t, got.Synthesis)
	require.True(t, got.FinalizationRequired)
	require.Equal(t, "batch_1", got.Synthesis.BatchID)
	require.Equal(t, 2, got.Synthesis.TaskCount)
	require.Equal(t, 1, got.Synthesis.CompletedCount)
	require.Equal(t, 1, got.Synthesis.FailedCount)
	require.Equal(t, []string{"A", "B"}, got.Synthesis.CompletionOrder)
	require.Equal(t, "synthesize_and_finalize", got.Synthesis.NextAction)
	require.Equal(t, "A completed", got.Synthesis.Children[0].Summary)
	require.Equal(t, []string{"reasoning_effort_unsupported_ignored"}, got.Synthesis.Children[0].RouteWarnings)
	require.Equal(t, "boom", got.Synthesis.Children[1].Error)
}

func TestAttachSynthesisPacketStaysOpenWhilePending(t *testing.T) {
	result := &AgentWaitResult{
		Agents: []AgentStatusResult{{
			ID:                "child-a",
			SessionID:         "sess-a",
			CurrentTaskID:     "A",
			CurrentTaskStatus: "running",
		}},
		PendingCount: 1,
		NextAction:   "continue_wait",
		Obligations: []AgentWaitObligation{{
			ObligationID: "batch_1",
			SubjectKind:  "batch",
			SubjectID:    "batch_1",
		}},
	}

	got := AttachSynthesisPacket(result)
	require.NotNil(t, got.Synthesis)
	require.False(t, got.FinalizationRequired)
	require.Equal(t, "continue_wait", got.Synthesis.NextAction)
}

func TestSynthesisPacketBoundsChildren(t *testing.T) {
	result := &AgentWaitResult{}
	for i := 0; i < SynthesisChildLimit+3; i++ {
		result.Agents = append(result.Agents, AgentStatusResult{
			ID:                "child",
			CurrentTaskID:     "task",
			CurrentTaskStatus: "succeeded",
		})
	}
	packet := BuildSynthesisPacketFromWaitResult(result)
	require.NotNil(t, packet)
	require.Len(t, packet.Children, SynthesisChildLimit)
	require.True(t, packet.Truncated)
	require.Equal(t, SynthesisChildLimit+3, packet.TaskCount)
}

func TestBuildSubagentBatchTerminalMailboxCopiesSynthesisPacket(t *testing.T) {
	packet := &SynthesisPacket{
		BatchID:        "batch_1",
		Status:         "completed",
		TaskCount:      1,
		CompletedCount: 1,
		NextAction:     "synthesize_and_finalize",
		Children: []SynthesisChildResult{{
			TaskID:  "A",
			Status:  "succeeded",
			Summary: "done",
		}},
	}
	message := BuildSubagentBatchTerminalMailboxMessage(
		"parent-1",
		"batch_1",
		"subagent.batch.completed",
		"delivery-1",
		map[string]interface{}{
			"status":           "completed",
			"synthesis_packet": packet,
			"next_action":      "synthesize_and_finalize",
		},
	)
	require.Equal(t, packet, message.Metadata["synthesis_packet"])
	require.Equal(t, "synthesize_and_finalize", message.Metadata["next_action"])
	require.Contains(t, message.Body, "SynthesisPacket attached")
}
