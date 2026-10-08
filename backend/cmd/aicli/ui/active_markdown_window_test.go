package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

func resetActiveMarkdownWindows() {
	activeMarkdownWindows.mu.Lock()
	defer activeMarkdownWindows.mu.Unlock()
	activeMarkdownWindows.entries = map[scene.CellID]*activeMarkdownWindowEntry{}
}

// activeBandFullTail 返回全量渲染的最后 maxRows 行——窗口路径的期望输出。
// 仅适用于“看起来像 markdown”的源（否则投影走 plain 路径）。
func activeBandFullTail(t *testing.T, source string, width, maxRows int) []render.Line {
	t.Helper()
	if !markdown.LooksLikeMarkdown(source) {
		t.Fatalf("test source must look like markdown")
	}
	full := activeMarkdownBandLines(markdown.Render(source, activeBandMarkdownOptions(width, style.ThemeContext{}, newActiveBandHighlighter())))
	if len(full) > maxRows {
		full = full[len(full)-maxRows:]
	}
	return full
}

type countingBandHighlighter struct {
	calls int
}

func (c *countingBandHighlighter) Highlight(req syntax.HighlightRequest) ([]render.Line, syntax.HighlightMeta) {
	c.calls++
	return nil, syntax.HighlightMeta{}
}

// 逐步追加（含跨块拆分的栅栏/表格/setext/引用式定义与同段落追加），每一步
// 窗口化投影都必须与全量渲染的尾部逐行一致。
func TestActiveMarkdownWindowStreamingMatchesFullRender(t *testing.T) {
	resetActiveMarkdownWindows()
	chunks := []string{
		"## 流式小节\n\n", "第一段文字。\n\n",
		"- 列表项一\n", "- 列表项二\n\n",
		"```go\n", "func f() error {\n", "\treturn nil\n", "}\n", "```\n\n",
		"| 列一 | 列二 |\n", "|---|---|\n", "| a | b |\n",
		"\n> 引用\n> 续行\n\n",
		"段落标题\n", "===\n\n",
		"[链接][ref] 与定义。\n\n[ref]: https://example.com\n\n",
		"长段落开始，",
		"继续在同一段落里追加文字，",
		"直到它超过一行宽度。\n\n",
		"a\r\nb\r\n\r\nc\r\n",
		"结尾。\n",
	}
	geometry := GeometryState{Width: 60, Height: 24}
	maxRows := ActiveBandRows(geometry.Height)
	var source strings.Builder
	for step, chunk := range chunks {
		source.WriteString(chunk)
		active := ActiveCellState{
			CellID:   71,
			Revision: uint64(step + 1),
			Kind:     scene.KindAssistant,
			Phase:    ActiveCellMutable,
			Source:   source.String(),
			Stable:   SourceRange{Start: 0, End: source.Len()},
		}
		projection := ProjectActiveCellBand(active, geometry)
		if !projection.Valid() {
			t.Fatalf("step %d: projection invalid", step)
		}
		want := activeBandFullTail(t, source.String(), 60, maxRows)
		if !render.LinesEqual(projection.Lines, want) {
			t.Fatalf("step %d: windowed projection diverges from the full render tail", step)
		}
	}
}

// 维护路径不得重渲染窗口之前的块（头部栅栏用计数高亮器钉住）。
func TestActiveMarkdownWindowMaintenanceSkipsLeadingBlocks(t *testing.T) {
	resetActiveMarkdownWindows()
	head := "```go\nfunc main() {}\n```\n\n" + strings.Repeat("头部段落内容，用于撑起窗口边界。\n\n", 6)
	source := head + strings.Repeat("尾部段落，不含高亮需求。\n\n", 4)

	if lines, ok := activeMarkdownWindowedTailLines(72, source, 80, style.ThemeContext{}, newActiveBandHighlighter(), 4); !ok || len(lines) == 0 {
		t.Fatalf("window establishment failed")
	}
	// 维护路径：追加纯段落增量，计数高亮器必须零调用（栅栏在窗口之外）。
	counter := &countingBandHighlighter{}
	appended := source + "追加的纯文本段落。\n\n"
	lines, ok := activeMarkdownWindowedTailLines(72, appended, 80, style.ThemeContext{}, counter, 4)
	if !ok || len(lines) == 0 {
		t.Fatalf("window maintenance failed")
	}
	if counter.calls != 0 {
		t.Fatalf("maintenance re-rendered leading fenced blocks: %d highlighter calls", counter.calls)
	}
}

// 源被整体替换（非前缀）时必须重建窗口并保持尾部正确。
func TestActiveMarkdownWindowSourceReplacement(t *testing.T) {
	resetActiveMarkdownWindows()
	geometry := GeometryState{Width: 72, Height: 24}
	project := func(source string) []render.Line {
		active := ActiveCellState{
			CellID: 73, Revision: 1, Kind: scene.KindAssistant, Phase: ActiveCellMutable,
			Source: source, Stable: SourceRange{Start: 0, End: len(source)},
		}
		projection := ProjectActiveCellBand(active, geometry)
		if !projection.Valid() {
			t.Fatalf("projection invalid for replacement source")
		}
		return projection.Lines
	}
	_ = project(strings.Repeat("第一段内容，含 `code`。\n\n", 24))
	replacement := strings.Repeat("完全不同的第二段内容，含 `code`。\n\n", 24)
	if got, want := project(replacement), activeBandFullTail(t, replacement, 72, ActiveBandRows(24)); !render.LinesEqual(got, want) {
		t.Fatalf("replacement source diverges from full render tail")
	}
}

// 窗口随流前移：长流之后窗口长度必须有界（≈预算行），不得随源增长。
func TestActiveMarkdownWindowAdvancesWithStream(t *testing.T) {
	resetActiveMarkdownWindows()
	geometry := GeometryState{Width: 72, Height: 24}
	var source strings.Builder
	for step := 0; step < 60; step++ {
		source.WriteString("追加段落内容，含 `code`，用于推动窗口前移并保持尾部可见。\n\n")
		active := ActiveCellState{
			CellID: 74, Revision: uint64(step + 1), Kind: scene.KindAssistant, Phase: ActiveCellMutable,
			Source: source.String(), Stable: SourceRange{Start: 0, End: source.Len()},
		}
		projection := ProjectActiveCellBand(active, geometry)
		if !projection.Valid() {
			t.Fatalf("step %d: projection invalid", step)
		}
		want := activeBandFullTail(t, source.String(), 72, ActiveBandRows(24))
		if !render.LinesEqual(projection.Lines, want) {
			t.Fatalf("step %d: windowed projection diverges from full render tail", step)
		}
	}
	_, cut, _, ok := activeMarkdownWindows.get(74)
	if !ok || cut <= 0 {
		t.Fatalf("no window established after streaming")
	}
	if windowLen := source.Len() - cut; windowLen > 2048 {
		t.Fatalf("window length = %d bytes, want bounded (≈视口预算行)", windowLen)
	}
}

// 无块边界的退化源（单段落无空行）也必须保持尾部正确。
func TestActiveMarkdownWindowSingleParagraphStaysCorrect(t *testing.T) {
	resetActiveMarkdownWindows()
	geometry := GeometryState{Width: 72, Height: 24}
	var source strings.Builder
	for step := 0; step < 40; step++ {
		source.WriteString("同一个段落持续追加文字，带 `code`，不插入空行。")
		active := ActiveCellState{
			CellID: 75, Revision: uint64(step + 1), Kind: scene.KindAssistant, Phase: ActiveCellMutable,
			Source: source.String(), Stable: SourceRange{Start: 0, End: source.Len()},
		}
		projection := ProjectActiveCellBand(active, geometry)
		if !projection.Valid() {
			t.Fatalf("step %d: projection invalid", step)
		}
		want := activeBandFullTail(t, source.String(), 72, ActiveBandRows(24))
		if !render.LinesEqual(projection.Lines, want) {
			t.Fatalf("step %d: single-paragraph windowed projection diverges", step)
		}
	}
}

func TestActiveMarkdownWindowCacheBounded(t *testing.T) {
	resetActiveMarkdownWindows()
	for id := scene.CellID(1); id <= 12; id++ {
		activeMarkdownWindows.put(id, "src", 0, 80)
	}
	if got := len(activeMarkdownWindows.entries); got > activeMarkdownWindowEntries {
		t.Fatalf("cache entries = %d, want <= %d", got, activeMarkdownWindowEntries)
	}
}
