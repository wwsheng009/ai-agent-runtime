package usageanalytics

import (
	"strings"
	"testing"
)

func TestAnalyticsToolRecordJSONDropsTranscriptPayloads(t *testing.T) {
	payload := map[string]interface{}{
		"tool_call_id":               "call-1",
		"logical_tool":               "view",
		"output":                     strings.Repeat("x", 100000),
		"render_output":              strings.Repeat("y", 100000),
		"protocol_result":            map[string]interface{}{"content": strings.Repeat("z", 50000)},
		"output_original_bytes":      100000,
		"output_model_visible_bytes": 1024,
		"summary":                    "viewed file",
	}
	encoded := analyticsToolRecordJSON(payload)
	if len(encoded) >= maxAnalyticsToolRecordBytes {
		t.Fatalf("encoded analytics record = %d bytes, want <%d", len(encoded), maxAnalyticsToolRecordBytes)
	}
	for _, omitted := range []string{`"output":`, `"render_output":`, `"protocol_result":`} {
		if strings.Contains(encoded, omitted) {
			t.Fatalf("analytics record retained %s: %s", omitted, encoded)
		}
	}
	for _, kept := range []string{"output_original_bytes", "output_model_visible_bytes", "summary"} {
		if !strings.Contains(encoded, `"`+kept+`"`) {
			t.Fatalf("analytics record lost %s: %s", kept, encoded)
		}
	}
}

func TestTruncateAnalyticsStringKeepsValidUTF8(t *testing.T) {
	if got := truncateAnalyticsString("a界bc", 3); got != "a" {
		t.Fatalf("truncated multibyte string = %q, want %q", got, "a")
	}
	if got := truncateAnalyticsString(string([]byte{'a', 0xff, 'b'}), 3); got != "ab" {
		t.Fatalf("sanitized invalid UTF-8 = %q, want %q", got, "ab")
	}
}
