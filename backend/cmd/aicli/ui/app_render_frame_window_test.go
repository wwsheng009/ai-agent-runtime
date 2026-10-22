package ui

import (
	"fmt"
	"reflect"
	"runtime"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// app_render_frame_window_test.go —— T7 帧合成窗口化契约：
// 逐字节等价 + 分配量对照（大会话下 ≥60% 降幅）。

func t7FrameState(cells int) AppState {
	snapshotCells := make([]*scene.TranscriptCell, 0, cells)
	for i := 1; i <= cells; i++ {
		kind := scene.KindAssistant
		switch i % 4 {
		case 0:
			kind = scene.KindUser
		case 2:
			kind = scene.KindToolChain
		}
		snapshotCells = append(snapshotCells, &scene.TranscriptCell{
			ID:       scene.CellID(i),
			Sequence: uint64(i),
			Kind:     kind,
			Source:   fmt.Sprintf("cell %d 内容行", i),
			Revision: 1,
			Phase:    scene.CellCommitted,
			Boundary: boundary.BoundaryNormal,
		})
	}
	return AppState{
		Revision:   7,
		Geometry:   GeometryState{Width: 40, Height: 12, Generation: 2},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: snapshotCells}),
		Bottom: BottomPaneState{
			StatusModel: &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		},
	}
}

// referenceComposeAppRenderFrameFullMap 复刻 T7/T8b-3 之前的实现：全量
// LayoutAppScreen（LayoutAppState 全量行 + 全量 cells map）+ 全量 frames map。
// 仅用于等价性与分配量对照（不得在生产路径使用）。
func referenceComposeAppRenderFrameFullMap(state AppState) AppRenderFrame {
	screen := referenceFullLayoutAppScreen(state)
	text := composeAppTextLayoutFromScreen(screen)
	transcript := make(map[scene.CellID]scene.TranscriptCell, len(state.Transcript.Cells))
	for _, cell := range state.Transcript.Cells {
		transcript[cell.ID] = cell
	}
	bottomLines := appBottomRenderLines(screen.bottom, screen.Geometry.Width)
	frame := AppRenderFrame{
		Revision:         screen.Revision,
		LayoutGeneration: screen.LayoutGeneration,
		Geometry:         screen.Geometry,
		Lease:            screen.Lease,
		OutputBottomRow:  screen.OutputBottomRow,
		Rows:             make([]AppRenderRow, len(text.Rows)),
		Cursor:           cloneAppRenderCursor(text.Cursor),
	}
	for index, row := range text.Rows {
		line := appPlainRenderLine(row.Text)
		switch row.Owner {
		case renderengine.RowOwnerTranscript:
			line = appTranscriptRenderLine(row, transcript)
		case renderengine.RowOwnerBand, renderengine.RowOwnerStatus:
			if styled, ok := bottomLines[row.Row]; ok {
				line = styled
			}
		}
		frame.Rows[index] = AppRenderRow{
			Screen: row,
			Line:   cloneAppRenderLine(line),
		}
	}
	return frame
}

func TestComposeAppRenderFrameWindowedIndexIsEquivalent(t *testing.T) {
	state := t7FrameState(5000)
	got := ComposeAppRenderFrame(state)
	want := referenceComposeAppRenderFrameFullMap(state)
	if len(got.Rows) != len(want.Rows) {
		t.Fatalf("行数不一致：got=%d want=%d", len(got.Rows), len(want.Rows))
	}
	if !reflect.DeepEqual(got, want) {
		for index := range got.Rows {
			if !reflect.DeepEqual(got.Rows[index], want.Rows[index]) {
				t.Fatalf("第 %d 行不一致：\ngot =%+v\nwant=%+v", index, got.Rows[index], want.Rows[index])
			}
		}
		t.Fatal("帧输出与全量 map 实现不一致")
	}
}

func TestComposeAppRenderFrameWindowedIndexCutsAllocations(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过分配量对照")
	}
	// 规模对齐真实长会话（5,500 cells 量级）；帧合成前先跑一次预热布局缓存，
	// 使对照只反映「全量 map vs 帧内按需 map」的差异。
	state := t7FrameState(5000)
	_ = ComposeAppRenderFrame(state)
	const iterations = 20

	measure := func(compose func(AppState) AppRenderFrame) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := 0; i < iterations; i++ {
			_ = compose(state)
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}

	oldBytes := measure(referenceComposeAppRenderFrameFullMap)
	newBytes := measure(ComposeAppRenderFrame)
	ratio := float64(newBytes) / float64(oldBytes)
	t.Logf("frame alloc 对照（%d cells × %d 次，old=全量布局+全量 map，new=窗口化布局+帧内按需 map）：old=%dB new=%dB ratio=%.4f（降幅 %.1f%%）",
		5000, iterations, oldBytes, newBytes, ratio, (1-ratio)*100)
	// T8b-3 门禁：窗口化布局输入（LayoutAppScreen 不再物化全量行/cells map）后，
	// 单帧分配量相对全量实现必须下降 ≥60%。
	if newBytes*5 > oldBytes*2 {
		t.Fatalf("帧分配降幅不足 60%%：old=%d new=%d ratio=%.4f", oldBytes, newBytes, ratio)
	}
}

// TestComposeAppRenderFrameTranscriptIndexWindowedCutsAllocations 隔离度量
// T7 真正改动的分量：全量 cell map vs 帧内按需 map（两者都不含布局/文本帧开销）。
func TestComposeAppRenderFrameTranscriptIndexWindowedCutsAllocations(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过分配量对照")
	}
	state := t7FrameState(5000)
	neededIDs := make([]scene.CellID, 0, 16)
	for i := 0; i < 16 && i < len(state.Transcript.Cells); i++ {
		neededIDs = append(neededIDs, state.Transcript.Cells[i].ID)
	}
	const iterations = 50

	buildFullMap := func() map[scene.CellID]scene.TranscriptCell {
		m := make(map[scene.CellID]scene.TranscriptCell, len(state.Transcript.Cells))
		for index := range state.Transcript.Cells {
			cell := state.Transcript.Cells[index]
			m[cell.ID] = cell
		}
		return m
	}
	buildWindowedMap := func() map[scene.CellID]scene.TranscriptCell {
		m := make(map[scene.CellID]scene.TranscriptCell, len(neededIDs))
		for _, id := range neededIDs {
			m[id] = scene.TranscriptCell{}
		}
		for index := range state.Transcript.Cells {
			cell := state.Transcript.Cells[index]
			if _, wanted := m[cell.ID]; wanted {
				m[cell.ID] = cell
			}
		}
		return m
	}

	measure := func(build func() map[scene.CellID]scene.TranscriptCell) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := 0; i < iterations; i++ {
			_ = build()
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}

	oldBytes := measure(buildFullMap)
	newBytes := measure(buildWindowedMap)
	ratio := float64(newBytes) / float64(oldBytes)
	t.Logf("transcript index alloc 对照（5000 cells → 16 需用 × %d 次）：old=%dB new=%dB ratio=%.4f（降幅 %.1f%%）",
		iterations, oldBytes, newBytes, ratio, (1-ratio)*100)
	if newBytes*5 > oldBytes*2 {
		t.Fatalf("map 分量降幅不足 60%%：old=%d new=%d ratio=%.4f", oldBytes, newBytes, ratio)
	}
}
