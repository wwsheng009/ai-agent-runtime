package usageanalytics

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Raw tool payloads can contain 16 KiB+ display output, full protocol results
// and artifact bodies. usage_tool_calls only needs compact diagnostic fields:
// aggregates use promoted columns and LSP baselines read the two output-byte
// counters. Keeping the full transcript payload here duplicated the runtime
// store and grew one analytics table past 1.6 GiB in a 43-day sample.
const maxAnalyticsToolRecordBytes = 16 * 1024

var analyticsToolRecordKeys = map[string]struct{}{
	"tool_call_id":               {},
	"logical_tool":               {},
	"tool_name":                  {},
	"session_id":                 {},
	"trace_id":                   {},
	"turn_id":                    {},
	"step":                       {},
	"source":                     {},
	"kind":                       {},
	"outcome":                    {},
	"ok":                         {},
	"empty_result":               {},
	"error_code":                 {},
	"retryable":                  {},
	"failed_count":               {},
	"succeeded_count":            {},
	"duration_ms":                {},
	"arg_preview":                {},
	"command_text":               {},
	"directory":                  {},
	"file_path":                  {},
	"summary":                    {},
	"output_kind":                {},
	"output_original_bytes":      {},
	"output_model_visible_bytes": {},
	"artifact_archived":          {},
	"artifact_skipped":           {},
	"artifact_id":                {},
	"error":                      {},
	"error_message":              {},
	"next_action":                {},
	"malformed_arguments":        {},
	"not_executed":               {},
}

func analyticsToolRecordJSON(payload map[string]interface{}) string {
	thin := make(map[string]interface{}, len(analyticsToolRecordKeys))
	for key := range analyticsToolRecordKeys {
		if value, ok := payload[key]; ok && value != nil {
			thin[key] = value
		}
	}
	encoded, err := json.Marshal(thin)
	if err != nil {
		return ""
	}
	if len(encoded) <= maxAnalyticsToolRecordBytes {
		return string(encoded)
	}
	// Unknown call sites may still attach an oversized scalar to a known key.
	// Fall back to identifiers plus bounded summaries rather than bypassing the
	// size cap and reintroducing unbounded growth.
	fallback := make(map[string]interface{}, 8)
	for _, key := range []string{
		"tool_call_id", "logical_tool", "session_id", "trace_id", "turn_id",
		"outcome", "error_code", "output_model_visible_bytes",
		"output_original_bytes",
	} {
		if value, ok := thin[key]; ok {
			fallback[key] = value
		}
	}
	for _, key := range []string{"summary", "arg_preview", "error"} {
		if value, ok := thin[key].(string); ok {
			fallback[key] = truncateAnalyticsString(value, 512)
		}
	}
	fallback["analytics_record_truncated"] = true
	encoded, err = json.Marshal(fallback)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func truncateAnalyticsString(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	value = strings.ToValidUTF8(value, "")
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
