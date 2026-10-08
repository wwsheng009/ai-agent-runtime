package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// 长 Markdown 源（远超视口）在 start==0 流式路径下必须投影出与全量渲染完全
// 一致的最后 maxRows 行（尾部块窗口是性能路径，不得改变可见语义）。
func TestProjectActiveCellBandMarkdownTailMatchesFullRender(t *testing.T) {
	source := strings.Repeat("## 小节标题\n\n段落文字，含 `inline code` 与 **强调**。\n\n```go\nfunc f() error { return nil }\n```\n\n", 24)
	active := ActiveCellState{
		CellID:   41,
		Revision: 5,
		Kind:     scene.KindAssistant,
		Phase:    ActiveCellMutable,
		Source:   source,
		Stable:   SourceRange{Start: 0, End: len(source)},
	}
	geometry := GeometryState{Width: 80, Height: 24}
	projection := ProjectActiveCellBand(active, geometry)
	if !projection.Valid() {
		t.Fatalf("projection invalid: %+v", projection)
	}
	maxRows := ActiveBandRows(geometry.Height)
	full := activeMarkdownBandLines(markdown.Render(source, activeBandMarkdownOptions(80, style.ThemeContext{}, newActiveBandHighlighter())))
	if len(full) < maxRows {
		t.Fatalf("full render lines = %d, want >= %d", len(full), maxRows)
	}
	want := full[len(full)-maxRows:]
	if !render.LinesEqual(projection.Lines, want) {
		t.Fatalf("markdown tail projection diverges from the full render tail: proj=%d lines, want=%d lines",
			len(projection.Lines), len(want))
	}
}

// reasoning 尾部有界路径与全量字面投影的尾部一致（含 opening divider 的
// 视口裁剪语义）。
func TestProjectActiveCellBandReasoningTailMatchesFullRender(t *testing.T) {
	source := strings.Repeat("思考行一，包含一些解释。\n思考行二，继续推理。\n", 40)
	active := ActiveCellState{
		CellID:   42,
		Revision: 2,
		Kind:     scene.KindReasoning,
		Phase:    ActiveCellMutable,
		Source:   source,
		Stable:   SourceRange{Start: 0, End: len(source)},
	}
	geometry := GeometryState{Width: 80, Height: 24}
	projection := ProjectActiveCellBand(active, geometry)
	if !projection.Valid() {
		t.Fatalf("projection invalid: %+v", projection)
	}
	maxRows := ActiveBandRows(geometry.Height)
	full := activeReasoningBandLines(source, 80, style.ThemeContext{}, newActiveBandHighlighter())
	if len(full) < maxRows {
		t.Fatalf("full reasoning lines = %d, want >= %d", len(full), maxRows)
	}
	want := full[len(full)-maxRows:]
	if !render.LinesEqual(projection.Lines, want) {
		t.Fatalf("reasoning tail projection diverges from the full projection tail: proj=%d lines, want=%d lines",
			len(projection.Lines), len(want))
	}
}
