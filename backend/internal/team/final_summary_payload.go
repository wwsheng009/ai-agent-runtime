package team

import "strings"

// AppendFinalSummaryMetadata copies structured final-summary source / fallback /
// session error metadata into target without altering any existing summary text.
func AppendFinalSummaryMetadata(target map[string]interface{}, summaryResult *FinalSummaryResult) {
	if target == nil || summaryResult == nil {
		return
	}
	if source := strings.TrimSpace(summaryResult.SummarySource); source != "" {
		target["summary_source"] = source
	}
	if summaryResult.UsedFallback {
		target["used_fallback"] = true
	}
	if reason := strings.TrimSpace(summaryResult.FallbackReason); reason != "" {
		target["fallback_reason"] = reason
	}
	if traceID := strings.TrimSpace(summaryResult.TraceID); traceID != "" {
		target["trace_id"] = traceID
	}
	if errorType := strings.TrimSpace(summaryResult.ErrorType); errorType != "" {
		target["error_type"] = errorType
	}
	for key, value := range summaryResult.CloneErrorMetadata() {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		target[key] = value
	}
}

// BuildFinalSummaryEventPayload builds a persisted/runtime payload for a final
// summary event, including the summary text itself plus structured source and
// fallback metadata when available.
func BuildFinalSummaryEventPayload(summaryResult *FinalSummaryResult) map[string]interface{} {
	if summaryResult == nil {
		return nil
	}
	payload := map[string]interface{}{
		"summary": strings.TrimSpace(summaryResult.Summary),
	}
	AppendFinalSummaryMetadata(payload, summaryResult)
	return payload
}

// BuildFinalSummaryFailurePayload builds a structured failure payload for final
// summary generation, preserving any fallback/session execution metadata.
func BuildFinalSummaryFailurePayload(summaryResult *FinalSummaryResult, err error) map[string]interface{} {
	if err == nil && summaryResult == nil {
		return nil
	}
	payload := map[string]interface{}{}
	AppendFinalSummaryMetadata(payload, summaryResult)
	if err != nil {
		if text := strings.TrimSpace(err.Error()); text != "" {
			payload["error"] = text
		}
	}
	if sessionErr, ok := AsSessionExecutionError(err); ok && sessionErr != nil {
		if traceID := strings.TrimSpace(sessionErr.TraceID); traceID != "" {
			payload["trace_id"] = traceID
		}
		if errorType := strings.TrimSpace(sessionErr.ErrorType); errorType != "" {
			payload["error_type"] = errorType
		}
		for key, value := range sessionErr.CloneMetadata() {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if _, exists := payload[key]; exists {
				continue
			}
			payload[key] = value
		}
	}
	if len(payload) == 0 {
		return nil
	}
	return payload
}
