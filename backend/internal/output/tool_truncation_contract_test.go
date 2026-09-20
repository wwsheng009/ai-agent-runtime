package output

import "testing"

// TestToolTruncatedUpstreamVocabulary pins the "controlled tool decides
// truncation" vocabulary: once a controlled tool reports that it truncated its
// own output, downstream layers must treat that output as final.
func TestToolTruncatedUpstreamVocabulary(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]interface{}
		want bool
	}{
		{"nil", nil, false},
		{"empty", map[string]interface{}{}, false},
		{"plain", map[string]interface{}{"exit_code": float64(0)}, false},
		{"results_truncated/true", map[string]interface{}{"results_truncated": true}, true},
		{"output_truncated/true", map[string]interface{}{"output_truncated": true}, true},
		{"truncated/true", map[string]interface{}{"truncated": true}, true},
		{"results_truncated/false", map[string]interface{}{"results_truncated": false}, false},
		{"output_truncated/false", map[string]interface{}{"output_truncated": false}, false},
		{"truncated/false", map[string]interface{}{"truncated": false}, false},
		{"truncated/zero", map[string]interface{}{"truncated": float64(0)}, false},
		{"truncated/string-true", map[string]interface{}{"truncated": "true"}, true},
		{"truncated/nonempty-string", map[string]interface{}{"truncated": "yes"}, true},
		{"is_truncated/true", map[string]interface{}{"is_truncated": true}, true},
		{"skip_render_truncation/true", map[string]interface{}{"skip_render_truncation": true}, true},
		{"skip_render_truncation/false", map[string]interface{}{"skip_render_truncation": false}, false},
		{
			"skip_render_truncation/nested_tool_metadata",
			map[string]interface{}{
				"tool_metadata": map[string]interface{}{"skip_render_truncation": true},
			},
			true,
		},
	}
	for _, tc := range cases {
		if got := toolTruncatedUpstream(tc.meta); got != tc.want {
			t.Errorf("%s: toolTruncatedUpstream=%v want %v", tc.name, got, tc.want)
		}
	}
}
