package ui

import (
	"strconv"
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
	c := &cellRowsCache{lru: newCellLayoutLRU[[]AppScreenRow](4, cellRowsCacheMaxBytes)}
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
	c := &cellRowsCache{lru: newCellLayoutLRU[[]AppScreenRow](2, cellRowsCacheMaxBytes)}
	fp := "dark|1|github|{0}|false"
	for i := 0; i < 3; i++ {
		source := "cell-" + string(rune('a'+i))
		c.put(cellLayoutKeyFor(testCell(source, scene.PresentationPlain), 40, fp),
			[]AppScreenRow{{Owner: 1, Text: source}})
	}
	if _, _, _, entries, _ := c.stats(); entries != 2 {
		t.Fatalf("expected 2 entries after eviction, got %d", entries)
	}
	// 最早的 cell-a 被逐出，cell-b/c 仍在
	if got := c.get(cellLayoutKeyFor(testCell("cell-a", scene.PresentationPlain), 40, fp)); got != nil {
		t.Fatal("expected oldest entry evicted")
	}
	if got := c.get(cellLayoutKeyFor(testCell("cell-c", scene.PresentationPlain), 40, fp)); got == nil {
		t.Fatal("expected newest entry retained")
	}
}

// TestCellRowsCacheCoversWorkingSet 是「容量必须覆盖工作集」这一不变量的回归
// 测试。生产故障的形状是：LayoutAppScreen 与 planEligibleHistoryCommits 每次都
// 按稳定的 transcript 顺序遍历全部 cell，而容量 1024 低于真实会话的 3951 个
// cell —— 每个条目都在被再次复用之前就遭逐出，稳态命中率退化为 0，于是每次
// 布局都对每个 diff/代码单元格重跑一遍语法高亮。
//
// 这里断言修好之后必须成立的性质：工作集装得下时，预热后的重复扫描 100% 命中、
// 零逐出（缓存不再参与成本，布局真正变成 O(Δ)）。
func TestCellRowsCacheCoversWorkingSet(t *testing.T) {
	const workingSet = 64
	c := &cellRowsCache{lru: newCellLayoutLRU[[]AppScreenRow](workingSet, cellRowsCacheMaxBytes)}
	fp := "dark|1|github|{0}|false"
	keys := make([]cellLayoutKey, 0, workingSet)
	for i := 0; i < workingSet; i++ {
		source := "cell-" + strconv.Itoa(i)
		key := cellLayoutKeyFor(testCell(source, scene.PresentationPlain), 40, fp)
		keys = append(keys, key)
		c.put(key, []AppScreenRow{{Owner: 1, Text: source}})
	}
	// 预热阶段不应发生任何逐出：容量等于工作集。
	if _, _, evictions, entries, _ := c.stats(); entries != workingSet || evictions != 0 {
		t.Fatalf("warm-up: entries=%d evictions=%d, want %d/0", entries, evictions, workingSet)
	}
	hitsBefore, missesBefore, _, _, _ := c.stats()
	const rounds = 4
	for round := 0; round < rounds; round++ {
		for index, key := range keys {
			if got := c.get(key); got == nil {
				t.Fatalf("round %d: cell %d unexpectedly missed", round, index)
			}
		}
	}
	hits, misses, evictions, _, _ := c.stats()
	if misses != missesBefore {
		t.Fatalf("repeated scan over a resident working set must not miss, got %d new misses", misses-missesBefore)
	}
	if hits != hitsBefore+rounds*workingSet {
		t.Fatalf("hits = %d, want %d", hits, hitsBefore+rounds*workingSet)
	}
	if evictions != 0 {
		t.Fatalf("evictions = %d, want 0 once the working set is resident", evictions)
	}
}

// TestCellRowsCacheThrashesBelowWorkingSet 把「容量小于工作集」的退化固定成
// 可执行的文档：纯循环扫描下命中率精确为 0，无论逐出策略是 FIFO 还是 LRU ——
// LRU 只能利用时间局部性，救不了真正超过容量的工作集。这就是为什么修复必须
// 落在容量上（cellRowsCacheMax >= 会话 cell 数），而不是落在逐出策略上。
func TestCellRowsCacheThrashesBelowWorkingSet(t *testing.T) {
	c := &cellRowsCache{lru: newCellLayoutLRU[[]AppScreenRow](2, cellRowsCacheMaxBytes)}
	fp := "dark|1|github|{0}|false"
	sources := []string{"scan-a", "scan-b", "scan-c"}
	keys := make([]cellLayoutKey, 0, len(sources))
	for _, source := range sources {
		key := cellLayoutKeyFor(testCell(source, scene.PresentationPlain), 40, fp)
		keys = append(keys, key)
		c.put(key, []AppScreenRow{{Owner: 1, Text: source}})
	}
	for round := 0; round < 2; round++ {
		for _, key := range keys {
			if c.get(key) == nil {
				// 真实调用方在这里会重新布局并 put，模拟同一步。
				c.put(key, []AppScreenRow{{Owner: 1, Text: key.source}})
			}
		}
	}
	hits, _, _, _, _ := c.stats()
	if hits != 0 {
		t.Fatalf("working set above capacity must thrash, got %d hits", hits)
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
