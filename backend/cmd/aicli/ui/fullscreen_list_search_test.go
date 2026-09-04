package ui

import "testing"

// TestFullScreenListSearchMuseRegression guards the reported false positive:
// searching "muse" used to match "stepfun-ai/step-3.5-flash" etc. via split
// subsequence matching. Without an explicit '*' wildcard a token must now
// appear as a contiguous substring.
func TestFullScreenListSearchMuseRegression(t *testing.T) {
	items := []FullScreenListItem{
		{Title: "muse-spark-1.3-contributor-free", SearchText: "model muse-spark-1.3-contributor-free"},
		{Title: "muse-spark-1.2", SearchText: "model muse-spark-1.2"},
		{Title: "stepfun-ai/step-3.5-flash", SearchText: "model stepfun-ai/step-3.5-flash"},
		{Title: "stepfun-ai/step-3.7-flash", SearchText: "model stepfun-ai/step-3.7-flash"},
		{Title: "stepaudio-2.5-asr-stream", SearchText: "model stepaudio-2.5-asr-stream"},
	}

	// 只有真实含连续 "muse" 的两个模型命中，step* 假命中全部消失。
	matches := fullScreenListMatches(items, "muse")
	if len(matches) != 2 || matches[0]+matches[1] != 1 {
		t.Fatalf("expected only the 2 muse models (order by name length), got %v", matches)
	}

	// 显式通配符允许模糊：m*spark* 命中 muse 两兄弟（含 spark 且以 m 开头）。
	matches = fullScreenListMatches(items, "m*spark*")
	if len(matches) != 2 || matches[0]+matches[1] != 1 {
		t.Fatalf("expected wildcard m*spark* to match the 2 muse models, got %v", matches)
	}

	// 无通配符的拆分（m 与 spark 不连续）不允许。
	matches = fullScreenListMatches(items, "mspark")
	if len(matches) != 0 {
		t.Fatalf("expected no split match for mspark, got %v", matches)
	}

	// 前缀语义不受影响：搜索 "model"（SearchText 前缀）命中全部。
	matches = fullScreenListMatches(items, "model")
	if len(matches) != len(items) {
		t.Fatalf("expected prefix match to return all items, got %v", matches)
	}

	// 通配符整体匹配：*free 命中 contributor-free。
	matches = fullScreenListMatches(items, "*free")
	if len(matches) != 1 || matches[0] != 0 {
		t.Fatalf("expected *free -> [0], got %v", matches)
	}
}

// TestGlobMatch exercises the '*' wildcard matching primitives.
func TestGlobMatch(t *testing.T) {
	cases := []struct {
		s, pattern string
		want       bool
	}{
		{"muse-spark-1.2", "muse*", true},
		{"muse-spark-1.2", "*spark*", true},
		{"muse-spark-1.2", "m*spark*", true},
		{"muse-spark-1.2", "m*2", true},
		{"muse-spark-1.2", "muse", false},
		{"muse-spark-1.2", "*", true},
		{"muse-spark-1.2", "", false},
		{"", "", true},
		{"stepfun-ai/step-3.5-flash", "m*spark*", false},
		{"stepfun-ai/step-3.5-flash", "*step*", true},
		{"muse-spark-1.3-contributor-free", "*free", true},
	}
	for _, c := range cases {
		if got := globMatch(c.s, c.pattern); got != c.want {
			t.Fatalf("globMatch(%q, %q) = %v, want %v", c.s, c.pattern, got, c.want)
		}
	}
}