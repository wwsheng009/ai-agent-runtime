package scene

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
)

// layout_window_test.go —— T8b-3 窗口起点计数契约：TailLayoutWindowStart 的
// 结果必须与 LayoutTranscript 实际产出的行数一致（gap row 归属后继 cell、
// 空 source 不产生行、dense 链无 gap），否则窗口化布局的起点会与全量布局漂移。

func windowFixtureCells() []*TranscriptCell {
	kinds := []CellKind{KindUser, KindAssistant, KindToolChain, KindAssistant}
	sources := []string{
		"用户问题",
		"助手回答第一行\n助手回答第二行",
		"",
		"链内工具输出\n第二行",
		"独立工具输出\n第二行\n第三行",
		"补充说明",
	}
	cells := make([]*TranscriptCell, 0, len(sources))
	for index, source := range sources {
		metadata := boundary.BoundaryNormal
		if index == 2 {
			metadata = boundary.BoundaryDense
		}
		chainKey := ""
		if index == 3 {
			chainKey = "chain-a"
		}
		cells = append(cells, &TranscriptCell{
			ID:       CellID(index + 1),
			Sequence: uint64(index + 1),
			Kind:     kinds[index%len(kinds)],
			Source:   source,
			Revision: 1,
			Phase:    CellCommitted,
			Boundary: metadata,
			ChainKey: chainKey,
		})
	}
	return cells
}

// windowFixtureValues 把 fixture 投影为值切片：TailLayoutWindowStart 的输入口径
// 与 ui.TranscriptState.Cells（[]TranscriptCell）一致。
func windowFixtureValues(cells []*TranscriptCell) []TranscriptCell {
	values := make([]TranscriptCell, 0, len(cells))
	for _, cell := range cells {
		values = append(values, *cell)
	}
	return values
}

// rowsForCellRange 统计归属 cells[start:] 的布局行数（gap 行按 Gap 计，非 gap
// 行计 1；excluded 中的 cell 及其 gap 行计 0），作为 TailLayoutWindowStart 的
// 独立地面真值。
func rowsForCellRange(rows []LayoutRow, cells []*TranscriptCell, start int, excluded map[CellID]struct{}) int {
	owned := make(map[CellID]struct{}, len(cells))
	for index := start; index < len(cells); index++ {
		if _, skip := excluded[cells[index].ID]; skip {
			continue
		}
		owned[cells[index].ID] = struct{}{}
	}
	total := 0
	for _, row := range rows {
		if _, ok := owned[row.CellID]; !ok {
			continue
		}
		if row.Gap > 0 {
			total += int(row.Gap)
		} else {
			total++
		}
	}
	return total
}

func TestTailLayoutWindowStartMatchesLayoutRows(t *testing.T) {
	cells := windowFixtureCells()
	values := windowFixtureValues(cells)
	rows := LayoutTranscript(cells, 1)
	if len(rows) == 0 {
		t.Fatal("fixture 未产生布局行")
	}
	excluded := map[CellID]struct{}{CellID(6): {}}
	minRowsCases := []int{1, 2, 3, 5, 8, 13}
	for _, minRows := range minRowsCases {
		start := TailLayoutWindowStart(values, excluded, minRows)
		if start < 0 || start > len(cells) {
			t.Fatalf("minRows=%d：起点越界 %d", minRows, start)
		}
		if start == 0 {
			if got := rowsForCellRange(rows, cells, 0, excluded); got >= minRows {
				t.Fatalf("minRows=%d：全部行已够 %d 却返回 0", minRows, got)
			}
			continue
		}
		if got := rowsForCellRange(rows, cells, start, excluded); got < minRows {
			t.Fatalf("minRows=%d：窗口行数不足 got=%d start=%d", minRows, got, start)
		}
		if got := rowsForCellRange(rows, cells, start+1, excluded); got >= minRows {
			t.Fatalf("minRows=%d：起点可再前移（start+1 仍有 %d 行）", minRows, got)
		}
	}
}

func TestTailLayoutWindowStartDegenerateInputs(t *testing.T) {
	if got := TailLayoutWindowStart(nil, nil, 3); got != 0 {
		t.Fatalf("空切片起点 = %d，期望 0", got)
	}
	values := windowFixtureValues(windowFixtureCells())
	if got := TailLayoutWindowStart(values, nil, 0); got != len(values) {
		t.Fatalf("minRows<=0 起点 = %d，期望 %d（无窗口）", got, len(values))
	}
	// 全部 excluded：无可见行，起点只能回到 0。
	all := map[CellID]struct{}{}
	for index := range values {
		all[values[index].ID] = struct{}{}
	}
	if got := TailLayoutWindowStart(values, all, 5); got != 0 {
		t.Fatalf("全 excluded 起点 = %d，期望 0", got)
	}
}

// TestLayoutWindowRowHelpersMatchLayoutTranscript 直接对拍窗口行生成：窗口行序列
// 必须是全量 LayoutTranscript 行的连续后缀（含前驱上下文 cell 决定的首个 gap）。
func TestLayoutWindowRowHelpersMatchLayoutTranscript(t *testing.T) {
	cells := windowFixtureCells()
	pointers := make([]*TranscriptCell, 0, len(cells))
	for index := range cells {
		pointers = append(pointers, cells[index])
	}
	full := LayoutTranscript(pointers, 1)
	for start := 1; start < len(cells); start++ {
		// 窗口输入从 start-1（前驱上下文 cell）开始：上下文 cell 在输入里没有
		// 前驱，因此它的行只有自身语义行（不含它的 leading gap row）。这部分是
		// 窗口行序列的前缀，切片后剩下的必须等于全量行中「归属 cells[start:]」
		// 的后缀（首个 gap row 归属后继 cell）。
		contextRows := LayoutCellRowCount(cells[start-1])
		window := make([]*TranscriptCell, 0, len(cells)-start+1)
		for index := start - 1; index < len(cells); index++ {
			window = append(window, cells[index])
		}
		windowRows := LayoutTranscript(window, 1)[contextRows:]

		// 全量行的后缀起点 = 第一行归属 cells[start:] 的行（gap row 归属后继 cell）。
		ownedFromStart := make(map[CellID]struct{}, len(cells)-start)
		for index := start; index < len(cells); index++ {
			ownedFromStart[cells[index].ID] = struct{}{}
		}
		offset := 0
		for offset < len(full) {
			if _, ok := ownedFromStart[full[offset].CellID]; ok {
				break
			}
			offset++
		}
		suffix := full[offset:]
		if len(windowRows) != len(suffix) {
			t.Fatalf("start=%d：窗口行数 %d != 后缀行数 %d", start, len(windowRows), len(suffix))
		}
		for index := range suffix {
			if windowRows[index].CellID != suffix[index].CellID || windowRows[index].Gap != suffix[index].Gap || windowRows[index].Text != suffix[index].Text {
				t.Fatalf("start=%d 第 %d 行不一致：got=%+v want=%+v", start, index, windowRows[index], suffix[index])
			}
		}
	}
}
