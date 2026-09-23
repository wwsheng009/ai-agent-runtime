package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 尾部窗口（layoutTranscriptTailScreenRows）必须与「全量布局后截取末 N 行」逐行
// 一致。逐帧路径只在视口里显示最后 OutputBottomRow 行，但一致性不能靠「反正看不
// 见」来豁免：窗口切错会把某个 cell 的连续语义行切成半截，plain 路径按残缺行集
// 合 wrap，输出与全量布局不同 —— 表现为历史正文被软折成与真实宽度无关的形状。

// resetSharedCellRows 清空共享布局缓存，让对等性测试同时覆盖冷缓存路径（容量
// 修复前，冷路径就是每一帧都会走的那条）。
func resetSharedCellRows() {
	sharedCellRows.mu.Lock()
	defer sharedCellRows.mu.Unlock()
	sharedCellRows.lru = newCellLayoutLRU[[]AppScreenRow](cellRowsCacheMax, cellRowsCacheMaxBytes)
}

// tailParityFixtureState 覆盖尾部窗口必须正确处理的全部形态：跨 cell 的 gap
// row、结构化投影（markdown/chroma）、折叠工具链（foldTarget 提示行）、纯文本
// wrap、reasoning 分隔线，以及末尾的未提交 cell（整块排除）。
func tailParityFixtureState() AppState {
	cells := []scene.TranscriptCell{
		{ID: 1, Revision: 1, Sequence: 1, Kind: scene.KindUser, Source: "请解释一下这段 diff 的语义", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		{ID: 2, Revision: 1, Sequence: 2, Kind: scene.KindAssistant, Source: "# 标题\n\n这是**加粗**的说明，后面跟一段足够长的中文正文用来触发软折行。\n\n```go\nfunc main() { println(\"hi\") }\n```", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		committedToolCell(3, numberedToolLines("row", 60)),
		{ID: 4, Revision: 1, Sequence: 4, Kind: scene.KindUser, Source: "再试一次", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		committedToolCell(5, numberedToolLines("last", 60)),
		{ID: 6, Revision: 1, Sequence: 6, Kind: scene.KindReasoning, Source: "先看 diff，再看测试覆盖", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		{ID: 7, Revision: 1, Sequence: 7, Kind: scene.KindAssistant, Source: "普通纯文本回复，没有 markdown 标记，走 plain 路径并参与 wrap。", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		{ID: 8, Revision: 1, Sequence: 8, Kind: scene.KindAssistant, Source: "正在生成中", Phase: scene.CellMutable, Boundary: boundary.BoundaryNormal},
	}
	refs := make([]*scene.TranscriptCell, 0, len(cells))
	for index := range cells {
		refs = append(refs, &cells[index])
	}
	return AppState{
		Revision:         3,
		LayoutGeneration: 1,
		Geometry:         GeometryState{Width: 90, Height: 40, Generation: 1},
		Transcript:       NewTranscriptState(&scene.Snapshot{Cells: refs}),
	}
}

// assertAppScreenRowsEqual 比较两段布局行。Text/CellID/Owner/TranscriptGap/
// UserMessage 是布局与光标计算的语义字段；RenderLine 的 span 数一并比较，确保
// 结构化投影也没有因为窗口而被替换成另一种渲染结果。
func assertAppScreenRowsEqual(t *testing.T, context string, want, got []AppScreenRow) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: 行数不一致 want=%d got=%d", context, len(want), len(got))
	}
	for index := range want {
		w, g := want[index], got[index]
		if w.Text != g.Text || w.CellID != g.CellID || w.Owner != g.Owner ||
			w.TranscriptGap != g.TranscriptGap || w.UserMessage != g.UserMessage ||
			len(w.RenderLine.Spans) != len(g.RenderLine.Spans) {
			t.Fatalf("%s: 第 %d 行不一致\nwant cell=%d owner=%d gap=%t user=%t spans=%d text=%q\ngot  cell=%d owner=%d gap=%t user=%t spans=%d text=%q",
				context, index,
				w.CellID, w.Owner, w.TranscriptGap, w.UserMessage, len(w.RenderLine.Spans), w.Text,
				g.CellID, g.Owner, g.TranscriptGap, g.UserMessage, len(g.RenderLine.Spans), g.Text)
		}
	}
}

// tailWindowSizes 覆盖几个关键区间：小于单个 cell、正好跨 cell 边界、远大于
// 全部内容（触发 start == 0 的全量回退），以及需要几何扩张重试的中间值。
func tailWindowSizes(full int) []int {
	sizes := []int{1, 2, 3, 5, 8, 13, 21, 34, 55, 89, 144, 233}
	for _, extra := range []int{0, 1, 7} {
		if full+extra > 0 {
			sizes = append(sizes, full+extra)
		}
	}
	if full-1 > 0 {
		sizes = append(sizes, full-1)
	}
	return sizes
}

func TestLayoutTranscriptTailScreenRowsMatchesFullLayout(t *testing.T) {
	state := tailParityFixtureState()
	width := state.Geometry.Width
	rows := state.Transcript.LayoutRows(state.LayoutGeneration)
	byID := transcriptCellsByID(state.Transcript)
	mutable := transcriptSuffixCellIDsFromFirstMutable(state.Transcript)

	resetSharedCellRows()
	full := layoutTranscriptScreenRows(rows, byID, mutable, width, state.Theme)
	if len(full) == 0 {
		t.Fatal("fixture 布局为空，测试失去意义")
	}

	for _, maxRows := range tailWindowSizes(len(full)) {
		// 冷缓存：尾部窗口先跑，缓存里没有任何该 cell 的条目。
		resetSharedCellRows()
		cold := layoutTranscriptTailScreenRows(rows, byID, mutable, width, maxRows, state.Theme)
		assertAppScreenRowsEqual(t, "cold", expectedTail(full, maxRows), cold)

		// 热缓存：全量布局预热后，尾部窗口全部走缓存命中路径。
		resetSharedCellRows()
		warm := layoutTranscriptScreenRows(rows, byID, mutable, width, state.Theme)
		tail := layoutTranscriptTailScreenRows(rows, byID, mutable, width, maxRows, state.Theme)
		assertAppScreenRowsEqual(t, "warm", expectedTail(warm, maxRows), tail)
	}
}

func expectedTail(full []AppScreenRow, maxRows int) []AppScreenRow {
	if maxRows <= 0 || len(full) == 0 {
		return nil
	}
	if len(full) <= maxRows {
		return full
	}
	return full[len(full)-maxRows:]
}

// TestTranscriptTailStartIndexAlignsToCellBoundary 锁定窗口起点的对齐契约：起点
// 必须是某个 cell 非 gap 连续段的第一行（或 gap row），不能落在 cell 中间。
func TestTranscriptTailStartIndexAlignsToCellBoundary(t *testing.T) {
	state := tailParityFixtureState()
	rows := state.Transcript.LayoutRows(state.LayoutGeneration)
	if len(rows) < 2 {
		t.Fatal("fixture 布局行过少")
	}
	for maxRows := 1; maxRows <= len(rows)+3; maxRows++ {
		start := transcriptTailStartIndex(rows, nil, maxRows)
		if start < 0 || start > len(rows) {
			t.Fatalf("maxRows=%d: 起点越界 %d", maxRows, start)
		}
		if start == 0 || start == len(rows) {
			continue
		}
		if rows[start-1].CellID == rows[start].CellID && rows[start-1].Gap == 0 {
			t.Fatalf("maxRows=%d: 起点 %d 落在 cell %d 的连续段中间", maxRows, start, rows[start].CellID)
		}
	}
}

// TestLayoutAppScreenTailWindowPreservesFrameRows 是端到端护栏：LayoutAppScreen
// 走尾部窗口之后，整帧行内容必须与「全量布局 + 截断」的语义一致 —— 视口内的
// transcript 行来自 transcript，视口外的行仍是空白 gap。
func TestLayoutAppScreenTailWindowPreservesFrameRows(t *testing.T) {
	state := tailParityFixtureState()
	resetSharedCellRows()
	layout := LayoutAppScreen(state)
	if len(layout.Rows) != state.Geometry.Height {
		t.Fatalf("帧行数=%d, want %d", len(layout.Rows), state.Geometry.Height)
	}
	if layout.OutputBottomRow <= 0 || layout.OutputBottomRow > state.Geometry.Height {
		t.Fatalf("OutputBottomRow=%d 超出 [1,%d]", layout.OutputBottomRow, state.Geometry.Height)
	}
	// 尾部窗口只应填充输出区底部：输出边界之外（固定状态保留区）绝不能出现
	// transcript 正文，也不能出现历史正文被错误地推到输出区顶部。
	transcriptRows := 0
	for _, row := range layout.Rows {
		if row.Owner == renderengine.RowOwnerTranscript {
			transcriptRows++
			if row.Row > layout.OutputBottomRow {
				t.Fatalf("第 %d 行是 transcript 行，但输出边界只有 %d", row.Row, layout.OutputBottomRow)
			}
		}
	}
	if transcriptRows == 0 {
		t.Fatal("帧里没有任何 transcript 行")
	}
	if transcriptRows > layout.OutputBottomRow {
		t.Fatalf("transcript 行数 %d 超过 OutputBottomRow %d", transcriptRows, layout.OutputBottomRow)
	}
	// 末行必须是 transcript 的最后一个 cell（尾部窗口对齐到输出区底部）。
	last := layout.Rows[layout.OutputBottomRow-1]
	if last.Owner != renderengine.RowOwnerTranscript {
		t.Fatalf("输出区末行 owner=%d，want transcript：尾部窗口没有贴底", last.Owner)
	}
}
