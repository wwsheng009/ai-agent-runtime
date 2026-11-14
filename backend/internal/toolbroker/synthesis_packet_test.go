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
	require.True(t, got.ChildrenSettled)
	require.False(t, got.FinalizationRequired)
	require.Equal(t, ContinueParentWorkNextAction, got.NextAction)
	require.Equal(t, "batch_1", got.Synthesis.BatchID)
	require.Equal(t, 2, got.Synthesis.TaskCount)
	require.Equal(t, 1, got.Synthesis.CompletedCount)
	require.Equal(t, 1, got.Synthesis.FailedCount)
	require.Equal(t, []string{"A", "B"}, got.Synthesis.CompletionOrder)
	require.Equal(t, ContinueParentWorkNextAction, got.Synthesis.NextAction)
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
	require.False(t, got.ChildrenSettled)
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
		NextAction:     ContinueParentWorkNextAction,
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
			"next_action":      ContinueParentWorkNextAction,
		},
	)
	require.Equal(t, packet, message.Metadata["synthesis_packet"])
	require.Equal(t, ContinueParentWorkNextAction, message.Metadata["next_action"])
	require.Contains(t, message.Body, "SynthesisPacket attached")
	require.Contains(t, message.Body, "continue the parent task")
	require.NotContains(t, message.Body, "synthesize and finalize")
}

func TestAttachSynthesisPacketRecomputesSettlementAndClearsLegacyFinalization(t *testing.T) {
	result := &AgentWaitResult{
		Agents:               []AgentStatusResult{{ID: "child", CurrentTaskStatus: "succeeded"}},
		Obligations:          []AgentWaitObligation{{ObligationID: "batch", Terminal: true}},
		FinalizationRequired: true,
		NextAction:           "synthesize_and_finalize",
	}
	AttachSynthesisPacket(result)
	require.True(t, result.ChildrenSettled)
	require.False(t, result.FinalizationRequired)
	require.Equal(t, ContinueParentWorkNextAction, result.NextAction)

	// A subsequent phase may add obligations. The previous completion fact
	// must not survive reattachment, or clear the new phase's parked state.
	result.Obligations = append(result.Obligations, AgentWaitObligation{ObligationID: "next-batch"})
	AttachSynthesisPacket(result)
	require.False(t, result.ChildrenSettled)
	require.False(t, result.FinalizationRequired)

	result.Agents = nil
	result.FinalizationRequired = true
	AttachSynthesisPacket(result)
	require.Nil(t, result.Synthesis)
	require.False(t, result.ChildrenSettled)
	require.False(t, result.FinalizationRequired)
}

func TestBuildSynthesisPacketNeverPropagatesLegacyFinalization(t *testing.T) {
	for _, status := range []string{"running", "succeeded", "failed", "canceled", "timed_out"} {
		for _, action := range []string{"finalize", "synthesize_and_finalize"} {
			packet := BuildSynthesisPacketFromWaitResult(&AgentWaitResult{
				Agents:     []AgentStatusResult{{ID: "child", CurrentTaskStatus: status}},
				NextAction: action,
			})
			require.NotNil(t, packet)
			require.Equal(t, ContinueParentWorkNextAction, packet.NextAction)
		}
	}
}

func TestTerminalMailboxNormalizesLegacyPacketWithoutMutatingSource(t *testing.T) {
	for _, packet := range []interface{}{
		&SynthesisPacket{NextAction: "synthesize_and_finalize"},
		SynthesisPacket{NextAction: "synthesize_and_finalize"},
		map[string]interface{}{"next_action": "synthesize_and_finalize"},
	} {
		payload := map[string]interface{}{
			"status": "completed", "synthesis_packet": packet, "next_action": "synthesize_and_finalize",
		}
		message := BuildSubagentBatchTerminalMailboxMessage("parent", "batch", "subagent.batch.completed", "delivery", payload)
		require.Equal(t, ContinueParentWorkNextAction, message.Metadata["next_action"])
		switch value := message.Metadata["synthesis_packet"].(type) {
		case *SynthesisPacket:
			require.Equal(t, ContinueParentWorkNextAction, value.NextAction)
			require.Equal(t, "synthesize_and_finalize", packet.(*SynthesisPacket).NextAction)
		case SynthesisPacket:
			require.Equal(t, ContinueParentWorkNextAction, value.NextAction)
		case map[string]interface{}:
			require.Equal(t, ContinueParentWorkNextAction, value["next_action"])
			require.Equal(t, "synthesize_and_finalize", packet.(map[string]interface{})["next_action"])
		default:
			t.Fatalf("unexpected packet type %T", value)
		}
		require.Equal(t, "synthesize_and_finalize", payload["next_action"])
	}
}
