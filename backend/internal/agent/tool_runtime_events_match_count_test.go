package agent

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// TestSummarizeToolMetadataMatchCountScope 覆盖 match_count 口径标注的渲染：
// 带 observed_before_stop 标注时以下界（"108+ matches"）呈现，未标注时保持
// 旧格式，避免把「停止前观察到的行数」当成查询总数复述。
func TestSummarizeToolMetadataMatchCountScope(t *testing.T) {
	cases := []struct {
		name     string
		metadata map[string]interface{}
		want     string
	}{
		{
			name:     "complete result reports plain count",
			metadata: map[string]interface{}{"match_count": 3},
			want:     "3 matches",
		},
		{
			name: "observed scope renders lower bound",
			metadata: map[string]interface{}{
				"match_count":                         108,
				"truncated":                           true,
				toolresult.MetadataMatchCountScopeKey: toolresult.MatchCountScopeObservedBeforeStop,
			},
			want: "108+ matches, truncated",
		},
		{
			name: "legacy truncated count without scope stays unchanged",
			metadata: map[string]interface{}{
				"match_count": 80000,
				"truncated":   true,
			},
			want: "80000 matches, truncated",
		},
		{
			name: "single observed match",
			metadata: map[string]interface{}{
				"match_count":                         1,
				toolresult.MetadataMatchCountScopeKey: toolresult.MatchCountScopeObservedBeforeStop,
			},
			want: "1+ match",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeToolMetadata(tc.metadata); got != tc.want {
				t.Fatalf("summarizeToolMetadata(%v) = %q, want %q", tc.metadata, got, tc.want)
			}
		})
	}
}
