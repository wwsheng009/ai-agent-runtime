package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestEnsureAgentResultContractLinksFindingsErrorsAndEvidence(t *testing.T) {
	success := types.NewObservation("step-1", "go_test").WithOutput("tests passed").WithMetric("artifact_refs", []string{"artifact:test-log"}).MarkSuccess()
	failure := types.NewObservation("step-2", "build").WithMetric("error_code", "BUILD_FAILED").WithMetric("retryable", true).MarkFailure("compile failed")
	result := &Result{
		Success: false, Output: "partial result", Error: "compile failed", TraceID: "trace-1",
		Observations: []types.Observation{*success, *failure},
	}
	contract := ensureAgentResultContract(result, "finish build")
	require.Equal(t, agentresult.StatusPartiallyCompleted, contract.Status)
	require.Equal(t, "trace-1", contract.TraceID)
	require.Len(t, contract.Findings, 1)
	require.Equal(t, "artifact:test-log", contract.Evidence[0].Ref)
	require.Contains(t, contract.RemainingWork, "finish build")
	require.True(t, contract.Errors[len(contract.Errors)-1].Retryable)
}

func TestEnsureSubagentResultContractKeepsChangesSeparateFromFindings(t *testing.T) {
	report := SubagentResult{
		Success: false, Summary: "implementation partial", Error: "verification timed out",
		Findings: []string{"root cause identified"},
		Patches:  []FilePatch{{Path: "main.go", Summary: "added guard", ApplyStatus: "applied", ArtifactRefs: []string{"artifact:patch"}}},
	}
	contract := ensureSubagentResultContract(&report, SubagentTask{Goal: "verify fix"})
	require.Equal(t, agentresult.StatusPartiallyCompleted, contract.Status)
	require.Len(t, contract.Findings, 1)
	require.Len(t, contract.Changes, 1)
	require.Equal(t, "artifact:patch", contract.Evidence[0].Ref)
}
