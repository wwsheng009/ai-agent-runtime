package scene

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
)

// layoutCells 从 Scene 快照派生 Layout 行（等价 LayoutTranscript 的便捷入口）。
func layoutScene(t *testing.T, s *TuiScene, policyVersion uint64) []LayoutRow {
	t.Helper()
	return LayoutTranscript(s.Cells(), policyVersion)
}

func TestLayoutFirstCellNoLeadingGap(t *testing.T) {
	// 规则表：无 -> 任意首 cell -> 0（transcript 不以空行开头）。
	s := New()
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(1, KindUser, "hi")}); err != nil {
		t.Fatal(err)
	}
	rows := layoutScene(t, s, 1)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Boundary != nil {
		t.Fatalf("first row must not be boundary, got %+v", rows[0])
	}
}

func TestLayoutTopLevelCellsGetGapRow(t *testing.T) {
	// 规则表：user -> assistant -> 1。
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(1, KindUser, "u")})
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(2, KindAssistant, "a")})
	rows := layoutScene(t, s, 1)
	// 期望：u 行, gap row, a 行。
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (u, gap, a)", len(rows))
	}
	if rows[1].Boundary == nil {
		t.Fatalf("row 1 must be boundary row, got %+v", rows[1])
	}
	if rows[1].Boundary.PrevCellID != 1 || rows[1].Boundary.NextCellID != 2 {
		t.Fatalf("boundary key = %+v, want 1->2", rows[1].Boundary)
	}
	if rows[1].Boundary.PolicyVersion != 1 {
		t.Fatalf("policy version = %d, want 1", rows[1].Boundary.PolicyVersion)
	}
	if rows[1].Gap != boundary.GapOne {
		t.Fatalf("gap = %d, want 1", rows[1].Gap)
	}
	// CellID 归属后继 cell（§7.4）。
	if rows[1].CellID != 2 {
		t.Fatalf("boundary row cell = %d, want 2", rows[1].CellID)
	}
}

func TestLayoutSameRequestReasoningAssistantHasNoGhostGap(t *testing.T) {
	s := New()
	reasoning := newTestCell(1, KindSupplement, "end reasoning")
	reasoning.BoundaryGroupKey = "request-1"
	assistant := newTestCell(2, KindAssistant, "Hello")
	assistant.BoundaryGroupKey = "request-1"
	mustSubmit(t, s, &AppendCell{Cell: reasoning})
	mustSubmit(t, s, &AppendCell{Cell: assistant})

	rows := layoutScene(t, s, 1)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want adjacent reasoning and assistant without a boundary row", rows)
	}
	if rows[0].Text != "end reasoning" || rows[1].Text != "Hello" {
		t.Fatalf("unexpected dense rows: %+v", rows)
	}
	for _, row := range rows {
		if row.Boundary != nil {
			t.Fatalf("same-request sections gained ghost gap: %+v", rows)
		}
	}
}

func TestLayoutDifferentRequestReasoningAssistantKeepsTurnGap(t *testing.T) {
	s := New()
	reasoning := newTestCell(1, KindSupplement, "end reasoning")
	reasoning.BoundaryGroupKey = "request-1"
	assistant := newTestCell(2, KindAssistant, "Hello")
	assistant.BoundaryGroupKey = "request-2"
	mustSubmit(t, s, &AppendCell{Cell: reasoning})
	mustSubmit(t, s, &AppendCell{Cell: assistant})

	rows := layoutScene(t, s, 1)
	if len(rows) != 3 || rows[1].Boundary == nil || rows[1].Gap != boundary.GapOne {
		t.Fatalf("different requests lost their boundary: %+v", rows)
	}
}

func TestLayoutToolChainDense(t *testing.T) {
	// 规则表：同一 tool-chain cell 内的 tool events -> 0（链内事件合并进链首 cell，
	// 内容行之间无 boundary 行；§7.3）。
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: chainCell(1, "chain-1", "tool start")})
	mustSubmit(t, s, &UpdateCell{ID: 1, Revision: 1, Source: "tool start\ntool out"})
	rows := layoutScene(t, s, 1)
	// 期望：tool start 行, tool out 行（链内稠密，无 gap 行）。
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (dense chain content, no gap)", len(rows))
	}
	for _, r := range rows {
		if r.Boundary != nil {
			t.Fatalf("unexpected boundary row inside chain cell: %+v", r)
		}
	}
}

func TestLayoutIndependentToolCellsGap(t *testing.T) {
	// 规则表：独立 final tool/event cell -> 下一独立 final cell -> 1。
	s := New()
	a := chainCell(1, "chain-1", "tool a")
	b := chainCell(2, "chain-2", "tool b")
	// 不同 chain 是独立 final cell。
	mustSubmit(t, s, &AppendCell{Cell: a})
	mustSubmit(t, s, &AppendCell{Cell: b})
	rows := layoutScene(t, s, 1)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (tool a, gap, tool b)", len(rows))
	}
	if rows[1].Boundary == nil {
		t.Fatalf("expected gap between independent chains")
	}
}

func TestLayoutMutableUpdateNoNewBoundary(t *testing.T) {
	// INV-GAP-04：mutable update 不改变既有 cell boundary。
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(1, KindUser, "u")})
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(2, KindAssistant, "a1")})
	before := layoutScene(t, s, 1)
	// mutable update 同一 cell。
	mustSubmit(t, s, &UpdateCell{ID: 2, Revision: 1, Source: "a2"})
	after := layoutScene(t, s, 1)
	if len(before) != len(after) {
		t.Fatalf("update changed row count: before=%d after=%d", len(before), len(after))
	}
	for i := range before {
		if (before[i].Boundary == nil) != (after[i].Boundary == nil) {
			t.Fatalf("boundary structure changed at row %d", i)
		}
	}
}

func TestLayoutFinalizeNoNewBoundary(t *testing.T) {
	// INV-GAP-04：finalize（replace/commit）不新增 gap。
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(1, KindUser, "u")})
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(2, KindAssistant, "a1")})
	before := layoutScene(t, s, 1)
	mustSubmit(t, s, &FinalizeCell{ID: 2, Revision: 1, Source: "a-final"})
	after := layoutScene(t, s, 1)
	if len(before) != len(after) {
		t.Fatalf("finalize changed row count: before=%d after=%d", len(before), len(after))
	}
}

func TestLayoutEmptySourceCellSkipsRows(t *testing.T) {
	// INV-GAP-05：空 block/无可见内容不推进 boundary state。
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(1, KindUser, "u")})
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(2, KindSystem, "")}) // 空 source
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(3, KindAssistant, "a")})
	rows := layoutScene(t, s, 1)
	// 期望：u 行, gap(1->2? 不，2 空 source 不产生行但 gap 计算仍推进), ...
	// 注意：空 source 的 cell 仍是有效 cell，gap 计算基于 cell 元数据，
	// 但空 cell 不产生语义行。这里断言：空 cell 不产生行。
	textRows := 0
	for _, r := range rows {
		if r.Boundary == nil {
			textRows++
		}
	}
	if textRows != 2 {
		t.Fatalf("text rows = %d, want 2 (u, a)", textRows)
	}
	// 空 cell 与相邻 cell 之间仍按规则产生 gap（boundary state 由 cell 元数据推进）。
	// 行结构：u, gap(1->2), a 之间 gap(2->3) 需要按元数据判定：
	// 1(user) -> 2(system) 为 1，2(system) -> 3(assistant) 为 1。
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4 (u, gap, gap, a)", len(rows))
	}
}

func TestLayoutReplayEqualsLive(t *testing.T) {
	// 规则表：replay 与 live 相同（禁止 replay 特例）——无状态纯函数契约。
	s1 := New()
	s2 := New()
	events := []CellMutation{
		&AppendCell{Cell: newTestCell(1, KindUser, "u")},
		&AppendCell{Cell: newTestCell(2, KindAssistant, "a")},
	}
	for _, m := range events {
		mustSubmit(t, s1, m)
		mustSubmit(t, s2, m)
	}
	r1 := layoutScene(t, s1, 1)
	r2 := layoutScene(t, s2, 1)
	if len(r1) != len(r2) {
		t.Fatalf("replay/live row count mismatch")
	}
	for i := range r1 {
		if (r1[i].Boundary == nil) != (r2[i].Boundary == nil) {
			t.Fatalf("boundary structure differs at %d", i)
		}
	}
}

func TestLayoutSourceInternalBlankPreserved(t *testing.T) {
	// §7.2：markdown/code/preformatted 内部空行必须保留（不 TrimSpace）。
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(1, KindAssistant, "line1\n\nline3")})
	rows := layoutScene(t, s, 1)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (line1, blank, line3)", len(rows))
	}
	if rows[1].Text != "" || rows[1].Boundary != nil {
		t.Fatalf("internal blank must be content row, got %+v", rows[1])
	}
}

// mustSubmit 便捷提交。
func mustSubmit(t *testing.T, s *TuiScene, m CellMutation) {
	t.Helper()
	if _, err := s.ApplyCellMutation(m); err != nil {
		t.Fatalf("apply %T: %v", m, err)
	}
}

// chainCell 构造 tool-chain 成员 cell。
func chainCell(id CellID, chain, source string) TranscriptCell {
	c := newTestCell(id, KindToolChain, source)
	c.ChainKey = chain
	return c
}
