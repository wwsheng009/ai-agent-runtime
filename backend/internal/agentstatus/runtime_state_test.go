package agentstatus

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestApplyRuntimeStateProjectsEveryPendingDecision(t *testing.T) {
	result := &toolbroker.AgentStatusResult{}
	ApplyRuntimeState(result, chat.RuntimeStateSummary{
		Status:                   chat.SessionWaitingApproval,
		CurrentTurnID:            " turn-1 ",
		PendingApproval:          true,
		PendingApprovalID:        " approval-1 ",
		PendingApprovalReason:    " writes outside workspace ",
		PendingApprovalRiskLevel: " high ",
		PendingQuestion:          true,
		PendingTool:              true,
		PendingToolName:          " shell ",
		PendingToolCallID:        " call-1 ",
	})

	require.Equal(t, string(chat.SessionWaitingApproval), result.Status)
	require.Equal(t, "turn-1", result.CurrentTurnID)
	require.True(t, result.PendingApproval)
	require.Equal(t, "approval-1", result.PendingApprovalID)
	require.Equal(t, "writes outside workspace", result.PendingApprovalReason)
	require.Equal(t, "high", result.PendingApprovalRiskLevel)
	require.True(t, result.PendingQuestion)
	require.Equal(t, "shell", result.PendingToolName)
	require.Equal(t, "call-1", result.PendingToolCallID)
}

func TestApplyRuntimeStateIgnoresNilResult(t *testing.T) {
	ApplyRuntimeState(nil, chat.RuntimeStateSummary{Status: chat.SessionRunning})
}
