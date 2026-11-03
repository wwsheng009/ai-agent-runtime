package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The common semantic validator must apply the same recovery as the OpenAI
// adapter: a non-empty answer mislabeled as a tool-call finish is final text,
// not a reason to replay the request and duplicate the answer.
func TestValidateAssistantMessageSemantics_NormalizesContentOnlyToolCallsFinish(t *testing.T) {
	msg := map[string]interface{}{
		"finish_reason": "tool_calls",
		"content":       "final answer",
	}

	require.NoError(t, validateAssistantMessageSemantics(msg))
	assert.Equal(t, "stop", msg["finish_reason"])
}

// An empty tool-call finish has no user-visible answer to preserve; it remains
// a retryable protocol error.
func TestValidateAssistantMessageSemantics_RejectsEmptyToolCallsFinish(t *testing.T) {
	err := validateAssistantMessageSemantics(map[string]interface{}{
		"finish_reason": "tool_calls",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing_tool_call")
}
