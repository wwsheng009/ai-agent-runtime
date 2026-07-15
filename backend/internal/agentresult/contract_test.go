package agentresult

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFromLegacyProducesValidContract(t *testing.T) {
	result := FromLegacy(false, "", "TOOL_TIMEOUT", "tool exceeded timeout", Usage{ToolCalls: 2})
	require.Equal(t, StatusFailed, result.Status)
	require.Equal(t, "tool exceeded timeout", result.Summary)
	require.Equal(t, "TOOL_TIMEOUT", result.Errors[0].Code)
	require.NoError(t, result.Validate())
}

func TestMergeEvidenceDeduplicatesAndClassifiesReferences(t *testing.T) {
	result := &Result{Status: StatusSucceeded, Summary: "done"}
	MergeEvidence(result, "artifact:one", "event:two", "artifact:one")
	require.Len(t, result.Evidence, 2)
	require.Equal(t, "artifact", result.Evidence[0].Kind)
	require.Equal(t, "execution_event", result.Evidence[1].Kind)
	require.NoError(t, result.Validate())
}
