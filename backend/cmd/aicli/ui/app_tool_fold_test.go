package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestTranscriptFoldsOversizedToolResult 锁定折叠契约：已提交的工具链单元格
// 在 transcript 中按显示预算折叠（head/tail + 仅显示标记），而不是把整段正文
// 原样铺开。折叠是显示层的选择，cell.Source 保留完整正文，因此 pager（Ctrl+T）
// 仍能看到被折叠隐藏的部分。
func TestTranscriptFoldsOversizedToolResult(t *testing.T) {
	const bodyLines = 60
	lines := make([]string, 0, bodyLines+1)
	lines = append(lines, "• Completed shell in 12ms")
	for i := 0; i < bodyLines; i++ {
		lines = append(lines, fmt.Sprintf("  │  row-%02d", i))
	}
	source := strings.Join(lines, "\n")
	middle := fmt.Sprintf("row-%02d", bodyLines/2)

	state := AppState{
		Revision:         7,
		LayoutGeneration: 1,
		Geometry:         GeometryState{Width: 80, Height: 40, Generation: 1},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{
				ID: 1, Sequence: 1, Kind: scene.KindToolChain, Source: source,
				Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal,
			},
		}}),
	}

	layout := LayoutAppScreen(state)
	texts := make([]string, 0, len(layout.Rows))
	for _, row := range layout.Rows {
		if row.CellID == 1 && !row.TranscriptGap {
			texts = append(texts, row.Text)
		}
	}
	joined := strings.Join(texts, "\n")

	if len(texts) >= bodyLines {
		t.Fatalf("tool cell rendered %d rows, want a folded projection (< %d):\n%s",
			len(texts), bodyLines, joined)
	}
	if !strings.Contains(joined, "omitted from this preview (display only)") {
		t.Fatalf("fold marker missing from the collapsed tool cell:\n%s", joined)
	}
	// 终端较窄时标记会被软折行拆开（标记贴在尾行末尾，行本身超宽），折行只是
	// 空白，提示本身必须完整可读。
	if flattened := strings.Join(strings.Fields(joined), " "); !strings.Contains(flattened, toolFoldHint) {
		t.Fatalf("fold marker must name the recovery keybinding %q:\n%s", toolFoldHint, joined)
	}
	if !strings.Contains(joined, "row-00") || !strings.Contains(joined, fmt.Sprintf("row-%02d", bodyLines-1)) {
		t.Fatalf("fold must keep head and tail rows:\n%s", joined)
	}
	if strings.Contains(joined, middle) {
		t.Fatalf("collapsed projection leaked the middle of the result (fold did not engage):\n%s", joined)
	}

	// pager 侧：整份 source 仍然可见，标记不会成为结果的唯一副本。
	pager := NewTranscriptPagerModel(TranscriptPagerSnapshot{Transcript: state.Transcript})
	pagerText := make([]string, 0)
	for _, row := range pager.Rows(80) {
		pagerText = append(pagerText, row.Text)
	}
	pagerJoined := strings.Join(pagerText, "\n")
	if !strings.Contains(pagerJoined, middle) {
		t.Fatalf("pager dropped %q from the full tool result:\n%s", middle, pagerJoined)
	}
	if strings.Contains(pagerJoined, "display only") {
		t.Fatalf("pager rendered a fold marker instead of the full text:\n%s", pagerJoined)
	}
}
