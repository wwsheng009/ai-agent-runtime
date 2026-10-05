package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// resumeParityFixtureState 构造跨多轮布局采样所需的长历史：
//   - cell 1 是 3000 行纯文本（plain 路径整段消费，采样只在 cell 起点命中）；
//   - cell 2 是结构化 markdown（renderedStructured 整块 append）；
//   - cell 3 是 6000 行折叠工具链，其语义行跨度必然覆盖多个采样点，使截断
//     命中在该 cell 中部——这是 layoutResumeRow 必须前移游标的场景；
//   - 末尾 cell 保持 mutable，作为 frontier 屏障（布局豁免）。
func resumeParityFixtureState() AppState {
	cells := []scene.TranscriptCell{
		{
			ID: 1, Revision: 1, Sequence: 1, Kind: scene.KindUser,
			Source: strings.Repeat("alpha line\n", 3000),
			Phase:  scene.CellCommitted, Boundary: boundary.BoundaryNormal,
		},
		{
			ID: 2, Revision: 1, Sequence: 2, Kind: scene.KindAssistant,
			Source: "# 大块\n\n" + strings.Repeat("markdown 段落行\n", 200),
			Phase:  scene.CellCommitted, Boundary: boundary.BoundaryNormal,
		},
		committedToolCell(3, numberedToolLines("resume", 6000)),
		{
			ID: 4, Revision: 1, Sequence: 4, Kind: scene.KindAssistant,
			Source: "仍在生成", Phase: scene.CellMutable, Boundary: boundary.BoundaryNormal,
		},
	}
	refs := make([]*scene.TranscriptCell, 0, len(cells))
	for index := range cells {
		refs = append(refs, &cells[index])
	}
	return AppState{
		Revision:         4,
		LayoutGeneration: 1,
		Geometry:         GeometryState{Width: 90, Height: 40, Generation: 1},
		Transcript:       NewTranscriptState(&scene.Snapshot{Cells: refs}),
	}
}

// TestLayoutTranscriptScreenRowsFromResumesAtCellBoundaries 锁定续跑布局的
// 核心不变量：以已过期 deadline 逐轮续跑得到的行序列，必须与一次性全量布局
// 逐行一致（无缺失、无重复、结构化 cell 不被重复 append），且游标严格前进、
// 直到 complete。Stage 2/3 的规划游标就建立在这条性质上。
func TestLayoutTranscriptScreenRowsFromResumesAtCellBoundaries(t *testing.T) {
	state := resumeParityFixtureState()
	width := state.Geometry.Width
	rows := state.Transcript.LayoutRows(state.LayoutGeneration)
	byID := transcriptCellsByID(state.Transcript)
	mutable := transcriptSuffixCellIDsFromFirstMutable(state.Transcript)
	// 至少跨两个采样点（第一轮在 4096 处截断、第二轮在 8192 处再截断）才覆盖
	// 多轮续跑；两次截断都落在 6000 行的工具链 cell 内部，正好验证
	// layoutResumeRow 的前移逻辑。
	if len(rows) < layoutBudgetCheckRows*2 {
		t.Fatalf("fixture 语义行数 %d 不足以覆盖多轮采样", len(rows))
	}

	resetSharedCellRows()
	full, completeFull := layoutTranscriptScreenRowsWithin(rows, byID, mutable, width, time.Time{}, state.Theme)
	if !completeFull || len(full) == 0 {
		t.Fatalf("全量布局非法：complete=%t rows=%d", completeFull, len(full))
	}

	// 冷缓存 + 已过期 deadline：每轮在采样点截断（len(result)>0 保护保证至少
	// 产出一个采样块），因此必然多轮。
	resetSharedCellRows()
	expired := time.Now().Add(-time.Second)
	var resumed []AppScreenRow
	cursor := 0
	passes := 0
	for {
		passes++
		if passes > 64 {
			t.Fatalf("续跑轮数异常：cursor=%d / %d", cursor, len(rows))
		}
		chunk, complete, next := layoutTranscriptScreenRowsFrom(rows, byID, mutable, width, expired, state.Theme, cursor)
		if next <= cursor && !complete {
			t.Fatalf("续跑未前进：pass=%d cursor=%d next=%d complete=%t", passes, cursor, next, complete)
		}
		if next > len(rows) {
			t.Fatalf("续跑游标越界：pass=%d next=%d len=%d", passes, next, len(rows))
		}
		if next > cursor && next < len(rows) && rows[next].Gap == 0 &&
			rows[next-1].CellID == rows[next].CellID && rows[next-1].Gap == 0 {
			// 游标必须落在 cell 边界：要么是 gap 行，要么是其 cell 非 gap 连续段
			// 的第一个语义行。落在 cell 中部会让下一轮重复 append 该 cell。
			t.Fatalf("游标落在 cell %d 中部：cursor=%d next=%d", rows[next].CellID, cursor, next)
		}
		resumed = append(resumed, chunk...)
		cursor = next
		if complete {
			break
		}
	}
	if passes < 2 {
		t.Fatalf("过期 deadline 下只有 %d 轮续跑，未覆盖多轮路径", passes)
	}
	assertAppScreenRowsEqual(t, "resume", full, resumed)
}
