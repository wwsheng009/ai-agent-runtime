package team

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentresult"
)

func TestTerminalTeamPayloadUsesSharedResultContractAndPreservesEvidence(t *testing.T) {
	resultRef := "artifact:task-1"
	payload := map[string]interface{}{"summary": "one task succeeded and one failed"}
	appendTerminalOutcomePayload(payload, TeamStatusPartiallyCompleted, []Task{
		{ID: "done", Title: "inspect", Status: TaskStatusDone, Summary: "found the cause", ResultRef: &resultRef},
		{ID: "failed", Title: "fix", Status: TaskStatusFailed, Summary: "build failed"},
	})
	contract, ok := payload["result_contract"].(*agentresult.Result)
	require.True(t, ok)
	require.Equal(t, agentresult.StatusPartiallyCompleted, contract.Status)
	require.Len(t, contract.Findings, 1)
	require.Len(t, contract.Errors, 1)
	require.Equal(t, "artifact:task-1", contract.Evidence[0].Ref)
	require.Equal(t, []string{"fix"}, contract.RemainingWork)
	require.NoError(t, contract.Validate())
}

func TestTaskOutcomeRuntimeFillsSharedResultContract(t *testing.T) {
	outcome, err := ValidateTaskOutcomeContract(TaskOutcomeContract{
		Status: TaskOutcomeBlocked, Summary: "review pending", Blocker: "approval required",
	})
	require.NoError(t, err)
	require.NotNil(t, outcome.ResultContract)
	require.Equal(t, agentresult.StatusBlocked, outcome.ResultContract.Status)
	require.Equal(t, "review pending", outcome.ResultContract.Summary)
	require.Equal(t, "approval required", outcome.ResultContract.Errors[0].Message)
}
