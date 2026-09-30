package knowledge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestShadowPatternTokens 钉住 regex → 字面 token 的候选映射规则。
func TestShadowPatternTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"ShadowObserver", []string{"ShadowObserver"}},
		{"Alpha|Beta", []string{"Alpha", "Beta"}},
		{`func \(h \*localChatRuntimeHost\)|ProfileReference`, []string{"func", "localChatRuntimeHost", "ProfileReference"}},
		{`session\.ToolPolicy`, []string{"session", "ToolPolicy"}},
		{`a|bb|ccc`, []string{"ccc"}}, // 长度 < 3 的片段丢弃
		{`\|`, nil},                   // 转义竖线不是交替分隔
		{`.*`, nil},                   // 无可用 token
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, shadowPatternTokens(tc.in), "pattern=%q", tc.in)
	}
}

// TestShadowObserver_GrepExpandsRegexTokensAndDedupes 钉住候选查询映射：
// 交替被拆成多次检索，同一 (path,line) 只计一次。
func TestShadowObserver_GrepExpandsRegexTokensAndDedupes(t *testing.T) {
	index := &capturingIndex{fakeShadowIndex: fakeShadowIndex{hits: []SearchHit{
		{Path: "backend/a.go", Line: 10, Name: "A"},
	}}}
	obs := NewShadowObserver(ShadowConfig{Mode: ModeShadow, Workspace: "", Index: index})

	rec, err := obs.Observe(context.Background(), ObservedCall{
		Tool:   "grep",
		Args:   map[string]any{"pattern": "Alpha|Beta"},
		Output: "backend/a.go:10:func A",
	})
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Len(t, index.searches, 2)
	require.Equal(t, "Alpha", index.searches[0].Text)
	require.Equal(t, "Beta", index.searches[1].Text)
	require.Equal(t, 1, rec.CandidateN, "两条 token 命中同一行必须去重")
	require.Equal(t, 1, rec.OverlapN)
	require.NotNil(t, rec.Coverage)
	require.InDelta(t, 1.0, *rec.Coverage, 1e-9)
}
