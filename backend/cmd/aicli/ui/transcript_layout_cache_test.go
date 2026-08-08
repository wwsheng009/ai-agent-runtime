package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func testCell(source string, kind scene.PresentationKind) scene.TranscriptCell {
	return scene.TranscriptCell{
		ID:     1,
		Kind:   scene.KindUser,
		Source: source,
		Presentation: scene.TranscriptPresentation{
			Kind: kind,
		},
	}
}

func TestCellRowsCacheHitAndInvalidation(t *testing.T) {
	c := &cellRowsCache{entries: make(map[cellLayoutKey]cachedCellRows), max: 4, maxBytes: cellRowsCacheMaxBytes}
	fp := "dark|1|github|{0}|false"
	cell := testCell("hello\nworld", scene.PresentationPlain)
	key := cellLayoutKeyFor(cell, 40, fp)
	rows := []AppScreenRow{{Owner: 1, Text: "hello"}, {Owner: 1, Text: "world"}}
	c.put(key, rows)

	if got := c.get(key); got == nil || len(got) != 2 || got[0].Text != "hello" {
		t.Fatalf("hit failed: got %#v", got)
	}

	// source 变化 → miss
	if got := c.get(cellLayoutKeyFor(testCell("hello\nthere", scene.PresentationPlain), 40, fp)); got != nil {
		t.Fatalf("expected miss after source change, got %#v", got)
	}
	// kind 变化 → miss
	if got := c.get(cellLayoutKeyFor(testCell("hello\nworld", scene.PresentationAssistantMarkdown), 40, fp)); got != nil {
		t.Fatalf("expected miss after kind change, got %#v", got)
	}
	assistant := testCell("hello\nworld", scene.PresentationPlain)
	assistant.Kind = scene.KindAssistant
	if got := c.get(cellLayoutKeyFor(assistant, 40, fp)); got != nil {
		t.Fatalf("expected miss after cell kind change, got %#v", got)
	}
	// width 变化 → miss
	if got := c.get(cellLayoutKeyFor(testCell("hello\nworld", scene.PresentationPlain), 80, fp)); got != nil {
		t.Fatalf("expected miss after width change, got %#v", got)
	}
	// theme 变化 → miss
	if got := c.get(cellLayoutKeyFor(testCell("hello\nworld", scene.PresentationPlain), 40, "light|0|basic|{0}|false")); got != nil {
		t.Fatalf("expected miss after theme change, got %#v", got)
	}
	// 原键仍可命中（内容寻址不互相污染）
	if got := c.get(key); got == nil {
		t.Fatal("original key should still hit")
	}
}

func TestCellRowsCacheKeyIncludesPresentationDocument(t *testing.T) {
	first := testCell("same canonical source", scene.PresentationDocument)
	first.Presentation.Document = render.SingleLineDoc(render.TextSpan("document one"))
	second := first
	second.Presentation.Document = render.SingleLineDoc(render.TextSpan("document two"))
	if cellLayoutKeyFor(first, 40, "theme") == cellLayoutKeyFor(second, 40, "theme") {
		t.Fatal("different presentation documents produced the same layout key")
	}
}

func TestCellRowsCacheEviction(t *testing.T) {
	c := &cellRowsCache{entries: make(map[cellLayoutKey]cachedCellRows), max: 2, maxBytes: cellRowsCacheMaxBytes}
	fp := "dark|1|github|{0}|false"
	for i := 0; i < 3; i++ {
		source := "cell-" + string(rune('a'+i))
		c.put(cellLayoutKeyFor(testCell(source, scene.PresentationPlain), 40, fp),
			[]AppScreenRow{{Owner: 1, Text: source}})
	}
	if len(c.entries) != 2 {
		t.Fatalf("expected 2 entries after eviction, got %d", len(c.entries))
	}
	// 最早的 cell-a 被逐出，cell-b/c 仍在
	if got := c.get(cellLayoutKeyFor(testCell("cell-a", scene.PresentationPlain), 40, fp)); got != nil {
		t.Fatal("expected oldest entry evicted")
	}
	if got := c.get(cellLayoutKeyFor(testCell("cell-c", scene.PresentationPlain), 40, fp)); got == nil {
		t.Fatal("expected newest entry retained")
	}
}

// TestLayoutTranscriptScreenRowsCacheConsistency 验证两次布局输出逐字节一致
// （第二次应全部缓存命中），且内容寻址缓存不改变布局语义。
func TestLayoutTranscriptScreenRowsCacheConsistency(t *testing.T) {
	width := 40
	rows := []scene.LayoutRow{
		{CellID: 1, Text: "plain cell one"},
		{CellID: 2, Text: "structured cell"},
		{CellID: 3, Text: "plain cell two"},
	}
	cells := map[scene.CellID]scene.TranscriptCell{
		1: testCell("plain cell one", scene.PresentationPlain),
		2: testCell("structured cell", scene.PresentationAssistantMarkdown),
		3: testCell("plain cell two", scene.PresentationPlain),
	}
	mutable := map[scene.CellID]struct{}{}
	theme := style.ThemeContext{}

	first := layoutTranscriptScreenRows(rows, cells, mutable, width, theme)
	second := layoutTranscriptScreenRows(rows, cells, mutable, width, theme)
	if len(first) != len(second) {
		t.Fatalf("row count mismatch: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Text != second[i].Text || first[i].CellID != second[i].CellID {
			t.Fatalf("row %d mismatch:\n first %+v\nsecond %+v", i, first[i], second[i])
		}
	}
}

func TestLayoutTranscriptScreenRowsCacheKeepsCellOwnership(t *testing.T) {
	const source = "identical source"
	rows := []scene.LayoutRow{
		{CellID: 11, Text: source},
		{CellID: 12, Text: source},
	}
	cells := map[scene.CellID]scene.TranscriptCell{
		11: {ID: 11, Kind: scene.KindUser, Source: source},
		12: {ID: 12, Kind: scene.KindUser, Source: source},
	}
	got := layoutTranscriptScreenRows(rows, cells, nil, 40, style.ThemeContext{})
	if len(got) != 2 || got[0].CellID != 11 || got[1].CellID != 12 {
		t.Fatalf("cached row ownership = %#v, want cell 11 then 12", got)
	}
}

func TestLayoutTranscriptScreenRowsCacheSeparatesPlainAndMarkdownPaths(t *testing.T) {
	const source = "## heading"
	rows := []scene.LayoutRow{
		{CellID: 21, Text: source},
		{CellID: 22, Text: source},
	}
	cells := map[scene.CellID]scene.TranscriptCell{
		21: {ID: 21, Kind: scene.KindUser, Source: source},
		22: {ID: 22, Kind: scene.KindAssistant, Source: source},
	}
	got := layoutTranscriptScreenRows(rows, cells, nil, 40, style.ThemeContext{})
	var userStructured, assistantStructured bool
	for _, row := range got {
		switch row.CellID {
		case 21:
			userStructured = userStructured || len(row.RenderLine.Spans) > 0
		case 22:
			assistantStructured = assistantStructured || len(row.RenderLine.Spans) > 0
		}
	}
	if userStructured || !assistantStructured {
		t.Fatalf("plain/markdown cache paths crossed: rows=%#v", got)
	}
}

func TestLayoutTranscriptScreenRowsCacheSeparatesDocumentsWithSameSource(t *testing.T) {
	const source = "same canonical source"
	first := scene.TranscriptCell{
		ID: 31, Kind: scene.KindCommand, Source: source,
		Presentation: scene.TranscriptPresentation{
			Kind:     scene.PresentationDocument,
			Document: render.SingleLineDoc(render.TextSpan("document one")),
		},
	}
	second := first
	second.ID = 32
	second.Presentation.Document = render.SingleLineDoc(render.TextSpan("document two"))
	got := layoutTranscriptScreenRows(
		[]scene.LayoutRow{{CellID: 31, Text: source}, {CellID: 32, Text: source}},
		map[scene.CellID]scene.TranscriptCell{31: first, 32: second}, nil, 40, style.ThemeContext{},
	)
	if len(got) != 2 || got[0].CellID != 31 || got[0].Text != "document one" ||
		got[1].CellID != 32 || got[1].Text != "document two" {
		t.Fatalf("document cache returned stale rows: %#v", got)
	}
}

func TestThemeFingerprintUsesColorValuesInsteadOfPointerAddresses(t *testing.T) {
	fg := style.RGB{R: 1, G: 2, B: 3}
	bg := style.RGB{R: 4, G: 5, B: 6}
	theme := style.ThemeContext{Terminal: style.ColorProfile{DefaultFG: &fg, DefaultBG: &bg}}
	clone := cloneThemeContext(theme)
	if themeFingerprint(theme) != themeFingerprint(clone) {
		t.Fatalf("equivalent cloned themes produced different fingerprints: %q != %q", themeFingerprint(theme), themeFingerprint(clone))
	}
}
