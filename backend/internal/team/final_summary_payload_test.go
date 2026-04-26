package team

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildFinalSummaryFailurePayloadMergesSummaryAndSessionExecutionMetadata(t *testing.T) {
	summaryResult := &FinalSummaryResult{
		Summary:        "fallback summary",
		SummarySource:  FinalSummarySourceFallback,
		UsedFallback:   true,
		FallbackReason: FinalSummaryFallbackLeadSessionError,
		TraceID:        "trace-summary-result",
		ErrorType:      "prompt_preflight",
		ErrorMetadata:  map[string]interface{}{"failure_reason_code": "prompt_still_exceeds_budget_after_compaction"},
		SessionError:   assert.AnError,
	}
	err := WrapSessionExecutionError(assert.AnError, &SessionResult{
		TraceID:   "trace-session-error",
		ErrorType: "prompt_preflight",
		ErrorMetadata: map[string]interface{}{
			"failure_reason_code":         "prompt_still_exceeds_budget_after_compaction",
			"replacement_history_applied": true,
		},
	})

	payload := BuildFinalSummaryFailurePayload(summaryResult, err)
	require.NotNil(t, payload)

	assert.Equal(t, "fallback", payload["summary_source"])
	assert.Equal(t, true, payload["used_fallback"])
	assert.Equal(t, "lead_session_error", payload["fallback_reason"])
	assert.Equal(t, "trace-session-error", payload["trace_id"])
	assert.Equal(t, "prompt_preflight", payload["error_type"])
	assert.Equal(t, "prompt_still_exceeds_budget_after_compaction", payload["failure_reason_code"])
	assert.Equal(t, true, payload["replacement_history_applied"])
	assert.Equal(t, assert.AnError.Error(), payload["error"])
}
