package scene

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
)

// TestRenderTextEmptyScene 空 Scene 投影为空（不产生行）。
func TestRenderTextEmptyScene(t *testing.T) {
	if got := RenderText(New().Cells(), 1); got != nil {
		t.Fatalf("RenderText(empty) = %v, want nil", got)
	}
}

// TestRenderTextFirstCellNoLeadingGap 首 cell 前无 gap 行（transcript 不以空行开头）。
func TestRenderTextFirstCellNoLeadingGap(t *testing.T) {
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(1, KindUser, "hi")})
	lines := RenderText(s.Cells(), 1)
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	if lines[0] != "hi" {
		t.Fatalf("line[0] = %q, want %q", lines[0], "hi")
	}
}

// TestRenderTextTopLevelGapRow 相邻 top-level cell 之间投影为空行 gap。
func TestRenderTextTopLevelGapRow(t *testing.T) {
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(1, KindUser, "u")})
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(2, KindAssistant, "a")})
	lines := RenderText(s.Cells(), 1)
	// 期望：["u", "", "a"]。
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3 (u, gap, a): %q", len(lines), lines)
	}
	if lines[0] != "u" || lines[1] != "" || lines[2] != "a" {
		t.Fatalf("lines = %q, want [u, '', a]", lines)
	}
}

// TestRenderTextMatchesLayoutRows RenderText 与 LayoutTranscript 行结构与
// 数量完全一致（投影层不改变布局）：gap 行数 == boundary 行数，内容行
// 数 == 非 boundary 行数，且 gap 行全部投影为空字符串。
func TestRenderTextMatchesLayoutRows(t *testing.T) {
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(1, KindUser, "u1")})
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(2, KindAssistant, "a1")})
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(3, KindAssistant, "a2")})

	rows := LayoutTranscript(s.Cells(), 1)
	lines := RenderText(s.Cells(), 1)
	if len(rows) != len(lines) {
		t.Fatalf("row count: layout=%d text=%d", len(rows), len(lines))
	}
	gaps, content := 0, 0
	for i, row := range rows {
		if row.Gap > 0 {
			gaps++
			if lines[i] != "" {
				t.Fatalf("row %d: layout gap but text %q (want empty)", i, lines[i])
			}
			if row.Boundary == nil || row.Boundary.PrevCellID == 0 {
				t.Fatalf("row %d: gap row without boundary key: %+v", i, row)
			}
			continue
		}
		content++
		if lines[i] != row.Text {
			t.Fatalf("row %d: text %q != layout %q", i, lines[i], row.Text)
		}
	}
	if gaps != 2 || content != 3 {
		t.Fatalf("gaps=%d content=%d, want 2/3", gaps, content)
	}
}

// TestRenderTextInternalBlankPreserved cell 内部空行保留为内容行（§7.2），
// 不 TrimSpace、不并入 gap 语义。
func TestRenderTextInternalBlankPreserved(t *testing.T) {
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(1, KindAssistant, "line1\n\nline3")})
	lines := RenderText(s.Cells(), 1)
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
	if lines[0] != "line1" || lines[1] != "" || lines[2] != "line3" {
		t.Fatalf("lines = %q, want [line1, '', line3]", lines)
	}
	// 内部空行不是 boundary 行：Layout 行结构与投影一致。
	rows := LayoutTranscript(s.Cells(), 1)
	if rows[1].Boundary != nil || rows[1].Gap != 0 {
		t.Fatalf("internal blank misclassified as boundary: %+v", rows[1])
	}
}

// TestRenderTextDenseChainNoGap tool-chain 链内稠密（§7.3）：合并进链首
// cell 的多个 source 行之间无 gap 行。
func TestRenderTextDenseChainNoGap(t *testing.T) {
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: chainCell(1, "chain-1", "tool start")})
	mustSubmit(t, s, &UpdateCell{ID: 1, Revision: 1, Source: "tool start\ntool out"})
	lines := RenderText(s.Cells(), 1)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (dense)", len(lines))
	}
	if lines[0] != "tool start" || lines[1] != "tool out" {
		t.Fatalf("lines = %q, want [tool start, tool out]", lines)
	}
}

// TestRenderTextGapOneFromBoundaryPolicy gap 值来自 boundary.ResolveGap 规则表
// （INV-GAP-03），本投影只把 gap 映射为空行，不做任何特例。
func TestRenderTextGapOneFromBoundaryPolicy(t *testing.T) {
	s := New()
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(1, KindUser, "u")})
	mustSubmit(t, s, &AppendCell{Cell: newTestCell(2, KindAssistant, "a")})
	rows := LayoutTranscript(s.Cells(), 1)
	if rows[1].Gap != boundary.GapOne {
		t.Fatalf("gap = %d, want GapOne (boundary.ResolveGap rule table)", rows[1].Gap)
	}
}
