package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

func TestActiveCellProjectionUsesRestrictedHighlighter(t *testing.T) {
	h, ok := newActiveBandHighlighter().(*syntax.ChromaHighlighter)
	if !ok {
		t.Fatalf("active band highlighter = %T, want *syntax.ChromaHighlighter", newActiveBandHighlighter())
	}
	if h.Budget != 80*time.Millisecond {
		t.Fatalf("active band budget = %v, want 80ms", h.Budget)
	}
	want := syntax.Limits{MaxBytes: 64 * 1024, MaxLines: 2000}
	if h.Limits != want {
		t.Fatalf("active band limits = %+v, want %+v", h.Limits, want)
	}
}
func TestProjectActiveCellBandUsesOnlyUnacknowledgedSemanticSuffix(t *testing.T) {
	const acknowledged = "already handed off\n"
	active := ActiveCellState{
		CellID:   17,
		Revision: 4,
		Kind:     scene.KindAssistant,
		Phase:    ActiveCellMutable,
		Source:   acknowledged + "still live\nfinal tail",
		Acked:    SourceRange{Start: 0, End: len(acknowledged)},
	}

	projection := ProjectActiveCellBand(active, GeometryState{Width: 80, Height: 24})
	if !projection.Valid() {
		t.Fatalf("projection = %+v, want valid source-backed tail", projection)
	}
	if projection.CellID != active.CellID || projection.Revision != active.Revision || projection.Kind != scene.KindAssistant {
		t.Fatalf("projection identity = %+v, want active identity", projection)
	}
	if projection.SourceRange != (SourceRange{Start: len(acknowledged), End: len(active.Source)}) {
		t.Fatalf("source range = %+v, want unacknowledged suffix", projection.SourceRange)
	}
	plain := render.PlainBackend{}.Render(render.LinesDoc(projection.Lines...))
	if strings.Contains(plain, "already handed off") || plain != "still live\nfinal tail" {
		t.Fatalf("projected plain tail = %q, want only unacknowledged source", plain)
	}
	for _, line := range projection.Lines {
		if len(line.Spans) != 1 || line.Spans[0].Style.Role != string(style.RoleAssistant) {
			t.Fatalf("line role = %#v, want assistant semantic role", line)
		}
	}
}

func TestProjectActiveCellBandUsesViewportTailAndRejectsUnsafeRangeBoundary(t *testing.T) {
	active := ActiveCellState{
		CellID:   19,
		Revision: 2,
		Kind:     scene.KindToolChain,
		Phase:    ActiveCellFinalizing,
		Source:   "one\ntwo\nthree\nfour\nfive\nsix\nseven",
	}
	projection := ProjectActiveCellBand(active, GeometryState{Width: 80, Height: 12})
	if got, want := len(projection.Lines), ActiveBandRows(12); got != want {
		t.Fatalf("line count = %d, want active-band budget %d", got, want)
	}
	plain := render.PlainBackend{}.Render(render.LinesDoc(projection.Lines...))
	if plain != "two\nthree\nfour\nfive\nsix\nseven" {
		t.Fatalf("viewport tail = %q, want latest bounded rows", plain)
	}
	if projection.Lines[0].Spans[0].Style.Role != string(style.RoleTool) {
		t.Fatalf("tool projection role = %#v", projection.Lines[0])
	}

	// SourceRange uses byte offsets. An offset inside a UTF-8 rune must not be
	// rounded because either direction would corrupt exact handoff ownership.
	unsafe := ActiveCellState{
		CellID: 20, Revision: 1, Kind: scene.KindAssistant, Phase: ActiveCellMutable,
		Source: "你好", Acked: SourceRange{Start: 0, End: 1},
	}
	if got := ProjectActiveCellBand(unsafe, GeometryState{Width: 80, Height: 24}); got.Valid() || len(got.Lines) != 0 {
		t.Fatalf("unsafe UTF-8 range produced projection: %+v", got)
	}
	nonPrefix := active
	nonPrefix.Acked = SourceRange{Start: 1, End: 4}
	if got := ProjectActiveCellBand(nonPrefix, GeometryState{Width: 80, Height: 24}); got.Valid() || len(got.Lines) != 0 {
		t.Fatalf("non-prefix acknowledged range produced projection: %+v", got)
	}
}

func TestProjectActiveCellBandRendersToolRunningRow(t *testing.T) {
	// 工具执行中（tool chain 处于 mutable 阶段）：ActiveBand 必须投影一行
	// "• Running <命令摘要>"，即使整个 Source 已 Acked（短命令常被立即
	// 确认）——真实 TUI（semantic active-cell projection）里 Running 行
	// 依赖这条路径重新可见。
	active := ActiveCellState{
		CellID:   23,
		Revision: 7,
		Kind:     scene.KindToolChain,
		Phase:    ActiveCellMutable,
		Source:   "ping -n 4 127.0.0.1 >nul & echo hello",
		Acked:    SourceRange{Start: 0, End: len("ping -n 4 127.0.0.1 >nul & echo hello")},
	}
	projection := ProjectActiveCellBand(active, GeometryState{Width: 80, Height: 24})
	if !projection.Valid() {
		t.Fatalf("projection = %+v, want valid tool Running row", projection)
	}
	if projection.CellID != active.CellID || projection.Revision != active.Revision || projection.Kind != scene.KindToolChain {
		t.Fatalf("projection identity = %+v, want active identity", projection)
	}
	plain := render.PlainBackend{}.Render(render.LinesDoc(projection.Lines...))
	if plain != "• Running ping -n 4 127.0.0.1 >nul & echo hello" {
		t.Fatalf("projected plain = %q, want tool Running row", plain)
	}
	if len(projection.Lines) != 1 || len(projection.Lines[0].Spans) != 1 || !projection.Lines[0].Spans[0].Style.Bold {
		t.Fatalf("running row style = %#v, want single bold span", projection.Lines)
	}
}

func TestProjectActiveCellBandRendersToolRunningRowTruncatesHead(t *testing.T) {
	active := ActiveCellState{
		CellID:   24,
		Revision: 1,
		Kind:     scene.KindToolChain,
		Phase:    ActiveCellMutable,
		Source:   "ping -n 4 127.0.0.1 >nul & echo hello",
	}
	projection := ProjectActiveCellBand(active, GeometryState{Width: 24, Height: 24})
	if !projection.Valid() {
		t.Fatalf("projection = %+v, want truncated tool Running row", projection)
	}
	plain := render.PlainBackend{}.Render(render.LinesDoc(projection.Lines...))
	if len(plain) >= len("• Running ping -n 4 127.0.0.1 >nul & echo hello") {
		t.Fatalf("projected plain = %q, want truncated to geometry width", plain)
	}
	if !strings.HasPrefix(plain, "• Running ") {
		t.Fatalf("projected plain = %q, want Running prefix preserved", plain)
	}
}

func TestProjectActiveCellBandRendersAssistantMarkdown(t *testing.T) {
	active := ActiveCellState{
		CellID:   27,
		Revision: 1,
		Kind:     scene.KindAssistant,
		Phase:    ActiveCellMutable,
		Source:   "# Live heading\n\n- **one**\n- `two`",
	}
	projection := ProjectActiveCellBand(active, GeometryState{Width: 40, Height: 16})
	if !projection.Valid() {
		t.Fatalf("markdown projection = %+v", projection)
	}
	plain := render.PlainBackend{}.Render(render.LinesDoc(projection.Lines...))
	for _, want := range []string{"Live heading", "one", "two"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("active markdown projection missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "# Live heading") || strings.Contains(plain, "**one**") {
		t.Fatalf("active projection retained raw markdown: %q", plain)
	}
}

func TestProjectActiveCellBandKeepsMarkdownLookingReasoningLiteral(t *testing.T) {
	active := ActiveCellState{
		CellID:   28,
		Revision: 2,
		Kind:     scene.KindReasoning,
		Phase:    ActiveCellMutable,
		Source:   "# Heading\n\n- **one**\n- `two`",
	}
	projection := ProjectActiveCellBand(active, GeometryState{Width: 40, Height: 16})
	if !projection.Valid() {
		t.Fatalf("reasoning projection = %+v", projection)
	}
	plain := render.PlainBackend{}.Render(render.LinesDoc(projection.Lines...))
	for _, want := range []string{"# Heading", "- **one**", "- `two`"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("source-faithful reasoning projection missing %q: %q", want, plain)
		}
	}
}

// Live reasoning and committed reasoning share one body-only projection.
// Provider-owned leading blank lines remain visible in both projections.
func TestProjectActiveCellBandReasoningMatchesCommittedRows(t *testing.T) {
	active := ActiveCellState{
		CellID:   30,
		Revision: 3,
		Kind:     scene.KindReasoning,
		Phase:    ActiveCellMutable,
		Source:   "\n# Heading\n\n- **one**\n- `two`",
	}
	geometry := GeometryState{Width: 40, Height: 40}
	projection := ProjectActiveCellBand(active, geometry)
	if !projection.Valid() {
		t.Fatalf("supplement projection = %+v", projection)
	}
	if len(projection.Lines) < 2 {
		t.Fatalf("supplement projection rows = %d, want >= 2", len(projection.Lines))
	}
	// The derived opening divider owns the reasoning role.
	firstDivider := -1
	for index, line := range projection.Lines {
		if strings.Contains(lineText(line), "reasoning") {
			firstDivider = index
			break
		}
	}
	if firstDivider < 0 {
		t.Fatalf("no opening divider in projection: %+v", projection.Lines)
	}
	first := projection.Lines[firstDivider]
	if len(first.Spans) == 0 || first.Spans[0].Style.Role != string(style.RoleReasoning) {
		t.Fatalf("divider row style = %+v, want reasoning role", first.Spans)
	}
	// The body starts with a provider newline; it must not be stripped merely
	// because it follows presentation chrome.
	bodyStart := len(reasoningDividerBandLines(reasoningChromeLine("reasoning"), geometry.Width))
	if bodyStart >= len(projection.Lines) || lineText(projection.Lines[bodyStart]) != "" {
		t.Fatalf("provider leading blank row was not preserved: %+v", projection.Lines)
	}
	// live band 行必须与提交后投影逐行（含样式）完全一致。
	theme := style.ThemeContext{}
	committed := activeReasoningBandLines(active.Source, geometry.Width, theme, newActiveBandHighlighter())
	if !render.LinesEqual(projection.Lines, committed) {
		t.Fatalf("live band rows diverge from committed rows:\nband:     %+v\ncommitted: %+v", projection.Lines, committed)
	}
}

func lineText(line render.Line) string {
	var b strings.Builder
	for _, span := range line.Spans {
		b.WriteString(span.Text)
	}
	return b.String()
}

func TestProjectActiveCellBandKeepsReasoningPlainTextWithoutMarkdown(t *testing.T) {
	active := ActiveCellState{
		CellID:   29,
		Revision: 1,
		Kind:     scene.KindReasoning,
		Phase:    ActiveCellMutable,
		Source:   "plain thinking text",
	}
	projection := ProjectActiveCellBand(active, GeometryState{Width: 40, Height: 16})
	if !projection.Valid() {
		t.Fatalf("supplement plain projection = %+v", projection)
	}
	plain := render.PlainBackend{}.Render(render.LinesDoc(projection.Lines...))
	if !strings.Contains(plain, "plain thinking text") {
		t.Fatalf("supplement plain projection missing body: %q", plain)
	}
	if !strings.Contains(plain, "reasoning") {
		t.Fatalf("supplement plain projection missing divider marker: %q", plain)
	}
}

func TestLayoutAppStateUsesActiveProjectionOnlyWithoutLegacyBand(t *testing.T) {
	state := AppState{
		Geometry: GeometryState{Width: 40, Height: 16},
		Active: ActiveCellState{
			CellID: 31, Revision: 5, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: "semantic live tail",
		},
		Bottom: BottomPaneState{
			StatusModel: &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		},
	}

	layout := LayoutAppState(state)
	if !layout.ActiveBand.Valid() || layout.Bottom.LegacyBandProjection {
		t.Fatalf("active/legacy projection state = %+v / %+v", layout.ActiveBand, layout.Bottom)
	}
	if got := (render.PlainBackend{}).Render(render.LinesDoc(layout.Bottom.State.ActiveBandStyled...)); got != "semantic live tail" {
		t.Fatalf("derived active-band text = %q", got)
	}
	if len(state.Bottom.ActiveBandLines) != 0 || len(state.Bottom.ActiveBandStyled) != 0 {
		t.Fatalf("pure layout mutated caller bottom state: %+v", state.Bottom)
	}

	state.Bottom.ActiveBandLines = []string{"legacy active projection"}
	layout = LayoutAppState(state)
	if !layout.Bottom.LegacyBandProjection {
		t.Fatal("legacy facade input must remain selected during migration")
	}
	if got := strings.Join(layout.Bottom.State.ActiveBandLines, "\n"); got != "legacy active projection" {
		t.Fatalf("legacy active-band text = %q, want legacy facade projection", got)
	}
}
func TestSuffixProjectorDefaultsToRestrictedHighlighter(t *testing.T) {
	proj := newSuffixProjector("```go", 0, 80, style.ThemeContext{}, false, nil)
	if proj.highlighter == nil {
		t.Fatal("suffix projector did not receive a restricted highlighter")
	}
	hl, ok := proj.highlighter.(*syntax.ChromaHighlighter)
	if !ok || hl.Budget != 80*time.Millisecond {
		t.Fatalf("suffix projector highlighter = %#v, want restricted active band highlighter", proj.highlighter)
	}
}
