package ui

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_residency_t9_test.go —— T9 的进程内门禁：
//   - R6：foldTarget 落在冷区（窗口外）时，窗口内不得出现提示；落在窗口内时
//     提示只出现在它自己身上（全局唯一，§14.7）；
//   - R8 的进程内分量：窗口化帧/布局的分配量与驻留窗口不随历史长度增长。

// t9FoldFixture 构造 count 个 cell，其中 folded 里的下标是折叠工具链 cell。
func t9FoldFixture(count int, folded map[int]bool) AppState {
	cells := make([]*scene.TranscriptCell, 0, count)
	for index := 1; index <= count; index++ {
		kind := scene.KindAssistant
		source := fmt.Sprintf("cell %d 内容行", index)
		if folded[index-1] {
			kind = scene.KindToolChain
			// 超过显示预算 → toolFoldOmits 为真（真实折叠，不是空判定）。
			source = ""
			for line := 0; line < 60; line++ {
				source += fmt.Sprintf("工具输出行 %d\n", line)
			}
		}
		cells = append(cells, &scene.TranscriptCell{
			ID:       scene.CellID(index),
			Sequence: uint64(index),
			Kind:     kind,
			Source:   source,
			Revision: 1,
			Phase:    scene.CellCommitted,
			Boundary: boundary.BoundaryNormal,
		})
	}
	return AppState{
		Revision:   3,
		Geometry:   GeometryState{Width: 60, Height: 12, Generation: 1},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: cells}),
	}
}

func t9FoldHintCount(rows []AppScreenRow) int {
	for _, row := range rows {
		// 提示拼在折叠投影的尾行行尾（不是独立 span），两种投影都要查。
		if row.Text != "" && containsToolFoldHint(row.Text) {
			return 1
		}
		var rendered string
		for _, span := range row.RenderLine.Spans {
			rendered += span.Text
		}
		if rendered != "" && containsToolFoldHint(rendered) {
			return 1
		}
	}
	return 0
}

func containsToolFoldHint(text string) bool {
	return len(text) >= len(toolFoldHint) && (func() bool {
		for i := 0; i+len(toolFoldHint) <= len(text); i++ {
			if text[i:i+len(toolFoldHint)] == toolFoldHint {
				return true
			}
		}
		return false
	})()
}

// TestWindowedLayoutFoldTargetColdRegion R6：最近一次折叠在冷区（窗口外）时，
// 窗口内不得出现 Ctrl+T 提示；把它移进窗口后，提示只出现在它自己身上。
func TestWindowedLayoutFoldTargetColdRegion(t *testing.T) {
	// 40 cells，折叠发生在 6 与 12（都在冷区），16 处的折叠更近但同样在窗口外。
	state := t9FoldFixture(40, map[int]bool{5: true, 11: true, 15: true})
	excluded := transcriptSuffixCellIDsFromFirstMutable(state.Transcript)
	target := transcriptGlobalFoldTarget(state.Transcript, excluded)
	windowStart := 20 // 尾部窗口只覆盖 20..40
	if target == 0 {
		t.Fatal("fixture 未产生折叠目标")
	}
	if int(target) > windowStart {
		t.Fatalf("fixture 目标 %d 不在冷区（窗口起点 %d）", target, windowStart)
	}

	windowCells := state.Transcript.Cells[windowStart:]
	windowState := state
	windowState.Transcript = TranscriptState{Cells: windowCells}
	screen := LayoutAppScreen(windowState)
	if got := t9FoldHintCount(screen.Rows); got != 0 {
		t.Fatalf("foldTarget 在冷区时窗口内不得出现折叠提示，实际 %d 处", got)
	}
	// 全量路径（足够高的视口，保证目标行不被视口裁掉）应恰有 1 处提示，且它在
	// 窗口外的目标 cell 上——证明窗口化路径的 0 处不是"提示根本不存在"。
	tallState := state
	tallState.Geometry.Height = 400
	full := LayoutAppScreen(tallState)
	if got := t9FoldHintCount(full.Rows); got != 1 {
		t.Fatalf("全量布局（高视口）应恰有 1 处提示，实际 %d", got)
	}
	hintCell := scene.CellID(0)
	for _, row := range full.Rows {
		if containsToolFoldHint(row.Text) {
			hintCell = row.CellID
			break
		}
		var rendered string
		for _, span := range row.RenderLine.Spans {
			rendered += span.Text
		}
		if containsToolFoldHint(rendered) {
			hintCell = row.CellID
			break
		}
	}
	if hintCell != target {
		t.Fatalf("全量提示落在 cell %d，期望 foldTarget=%d", hintCell, target)
	}

	// 把目标移到窗口内（cell 36）：提示必须只出现在它自己身上。
	nearState := t9FoldFixture(40, map[int]bool{5: true, 11: true, 35: true})
	nearTarget := transcriptGlobalFoldTarget(nearState.Transcript, excluded)
	if int(nearTarget) != 36 {
		t.Fatalf("fixture 目标=%d，期望 36", nearTarget)
	}
	nearWindow := nearState
	nearWindow.Transcript = TranscriptState{Cells: nearState.Transcript.Cells[windowStart:]}
	nearScreen := LayoutAppScreen(nearWindow)
	if got := t9FoldHintCount(nearScreen.Rows); got != 1 {
		t.Fatalf("目标在窗口内时应恰有 1 处提示，实际 %d", got)
	}
	// 且必须落在目标 cell 的行上。
	found := false
	for _, row := range nearScreen.Rows {
		if row.CellID != nearTarget {
			continue
		}
		if containsToolFoldHint(row.Text) {
			found = true
		}
		var rendered string
		for _, span := range row.RenderLine.Spans {
			rendered += span.Text
		}
		if containsToolFoldHint(rendered) {
			found = true
		}
	}
	if !found {
		t.Fatal("提示未落在 foldTarget 对应的 cell 行上")
	}
}

// TestWindowedFrameAllocationsFlatUnderLongHistory 是 R8 的进程内分量：固定视口
// 下，单帧分配量不得随历史长度增长（与 T8b-3 的合同一致，这里用更大的规模比）。
func TestWindowedFrameAllocationsFlatUnderLongHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过分配量对照")
	}
	small := t7FrameState(500)
	large := t7FrameState(20000)
	small.Geometry = GeometryState{Width: 40, Height: 12, Generation: 2}
	large.Geometry = GeometryState{Width: 40, Height: 12, Generation: 2}
	_ = ComposeAppRenderFrame(small)
	_ = ComposeAppRenderFrame(large)

	const iterations = 10
	measure := func(state AppState) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := 0; i < iterations; i++ {
			_ = ComposeAppRenderFrame(state)
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	smallBytes := measure(small)
	largeBytes := measure(large)
	ratio := float64(largeBytes) / float64(smallBytes)
	t.Logf("窗口化帧分配（%d 次）：500 cells=%dB 20000 cells=%dB ratio=%.3f",
		iterations, smallBytes, largeBytes, ratio)
	// 40x 历史增长下，单帧分配量必须基本不变（允许 3× 的系统噪声余量）。
	if largeBytes > smallBytes*3 {
		t.Fatalf("单帧分配随历史增长：500 cells=%dB 20000 cells=%dB ratio=%.3f",
			smallBytes, largeBytes, ratio)
	}
}
