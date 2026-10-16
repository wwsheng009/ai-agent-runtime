package ui

import (
	"strings"
	"testing"
)

// 模型 picker 的「刷新模型列表」/「添加模型」是动作行，不是目录项：它们既
// 不显示序号，也不消耗序号，所以排在它们下面的模型行仍然从 [1] 开始（而不是
// 被两个动作行顶到 [3]）。Disabled 行的 [·] 也是同一套「不占号」语义。
func TestFullScreenListItemNoIndexSkipsRankAndKeepsCatalogFromOne(t *testing.T) {
	options := FullScreenListOptions{
		Title: "选择模型",
		Items: []FullScreenListItem{
			{Title: "⟳ 刷新模型列表（重新拉取本 provider）", Detail: "model", NoIndex: true},
			{Title: "＋ 添加模型…", Detail: "model", NoIndex: true},
			{Title: "gpt-5.2", Detail: "model"},
			{Title: "gpt-5.2-mini", Detail: "model"},
		},
	}
	matches := fullScreenListMatches(options.Items, "")
	if len(matches) != 4 {
		t.Fatalf("matches = %v, want all four rows", matches)
	}

	for visibleIndex, want := range map[int]string{0: "", 1: "", 2: "[1]", 3: "[2]"} {
		if got := fullScreenListItemNumber(options.Items, matches, visibleIndex); got != want {
			t.Fatalf("row %d label = %q, want %q", visibleIndex, got, want)
		}
	}

	frame := renderTestFullScreenListFrame(options, fullScreenListState{selected: 0}, matches, 80, 16)
	for _, forbidden := range []string{"[1] ⟳", "[2] ＋", "[·] ⟳", "[·] ＋"} {
		if strings.Contains(frame, forbidden) {
			t.Fatalf("action rows must not carry a rank label, got %q in frame %q", forbidden, frame)
		}
	}
	for _, expected := range []string{"> ⟳ 刷新模型列表", "[1] gpt-5.2", "[2] gpt-5.2-mini"} {
		if !strings.Contains(frame, expected) {
			t.Fatalf("expected frame to contain %q, got %q", expected, frame)
		}
	}

	// 高亮动作行时也保持无序号（紧凑帧走的是另一条渲染分支）。
	compact := renderCompactFullScreenListFrame(options, fullScreenListState{selected: 1}, matches, 80, 8)
	for _, forbidden := range []string{"[2] ＋", "[·] ＋"} {
		if strings.Contains(compact, forbidden) {
			t.Fatalf("compact frame must not number the add row, got %q in %q", forbidden, compact)
		}
	}
	if !strings.Contains(compact, "> ＋ 添加模型") {
		t.Fatalf("compact frame must still show the highlighted add row, got %q", compact)
	}
}
