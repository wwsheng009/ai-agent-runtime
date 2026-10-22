package ui

import (
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// app_screen_layout_window_test.go —— T8b-3 窗口化布局输入的等价性与分配量契约。
//
// 参考实现 referenceFullLayoutAppScreen 复刻 T8b-3 之前的 LayoutAppScreen 本体
// （全量 LayoutRows + 全量 cells map + 行级尾部窗口），用于逐字节对照；不得在
// 生产路径使用。

func referenceFullLayoutAppScreen(state AppState) AppScreenLayout {
	layout := LayoutAppState(state)
	height := layout.Geometry.Height
	result := AppScreenLayout{
		Revision:                layout.Revision,
		LayoutGeneration:        layout.LayoutGeneration,
		Geometry:                layout.Geometry,
		Lease:                   layout.Lease,
		CursorFocus:             layout.Bottom.CursorFocus,
		Active:                  layout.Active.Clone(),
		ActiveBand:              layout.ActiveBand.Clone(),
		LegacyBandProjection:    layout.Bottom.LegacyBandProjection,
		ActiveProjectionPending: layout.Active.Phase != ActiveCellInactive,
		bottom:                  layout.Bottom,
	}
	if height < 1 {
		return result
	}
	width := layout.Geometry.Width
	if width < 1 {
		width = 80
	}
	result.Rows = makeAppScreenRows(height)
	result.OutputBottomRow = height
	for _, bottom := range layout.Bottom.RowPlan.Rows {
		if bottom.Row < 1 || bottom.Row > height {
			continue
		}
		result.Rows[bottom.Row-1] = AppScreenRow{
			Row:   bottom.Row,
			Owner: bottom.Owner,
			Text:  bottom.Text,
		}
	}
	result.OutputBottomRow = layout.Bottom.RowPlan.OutputBottomRow
	if result.OutputBottomRow < 0 {
		result.OutputBottomRow = 0
	}
	if result.OutputBottomRow > height {
		result.OutputBottomRow = height
	}
	excluded := transcriptSuffixCellIDsFromFirstMutable(state.Transcript)
	transcript := layoutTranscriptTailScreenRows(layout.Transcript, transcriptCellsByID(state.Transcript), excluded, width, result.OutputBottomRow, state.Theme)
	if len(transcript) > result.OutputBottomRow {
		transcript = transcript[len(transcript)-result.OutputBottomRow:]
	}
	startRow := result.OutputBottomRow - len(transcript) + 1
	for index, row := range transcript {
		row.Row = startRow + index
		if row.Row < 1 || row.Row > result.OutputBottomRow {
			continue
		}
		result.Rows[row.Row-1] = row
	}
	return result
}

// t8bWindowFixture 生成确定性的混合 transcript：覆盖 gap/dense 边界、空 source、
// wrap 长行、markdown/结构化投影、折叠工具链（多个，检验「最近一次折叠」）、
// reasoning 与尾部未提交屏障（excluded）。count 为 cell 数量，mutableTail 为尾部
// mutable cell 数量。
func t8bWindowFixture(seed int64, count, mutableTail int) AppState {
	rng := rand.New(rand.NewSource(seed))
	cells := make([]*scene.TranscriptCell, 0, count)
	for i := 1; i <= count; i++ {
		kind := scene.KindAssistant
		switch rng.Intn(8) {
		case 0, 1:
			kind = scene.KindUser
		case 2, 3:
			kind = scene.KindToolChain
		case 4:
			kind = scene.KindSupplement
		case 5:
			kind = scene.KindReasoning
		case 6:
			kind = scene.KindDiagnostic
		}
		var source string
		switch rng.Intn(8) {
		case 0:
			source = ""
		case 1:
			source = fmt.Sprintf("cell %d 单行内容", i)
		case 2:
			source = strings.Repeat(fmt.Sprintf("cell %d 多行内容\n", i), 1+rng.Intn(5))
		case 3:
			source = "长行片段 " + strings.Repeat("宽字符内容", 30) + fmt.Sprintf(" 尾部 %d", i)
		case 4:
			source = "## 标题\n\n正文段落 " + strings.Repeat("说明文字 ", 18) + "\n\n- 项一\n- 项二"
		case 5:
			source = strings.Repeat("工具输出行\n", 40) + fmt.Sprintf("结束 %d", i)
		case 6:
			source = fmt.Sprintf("推理片段 %d\n\n补充说明 ", i) + strings.Repeat("推理文字 ", 12)
		case 7:
			source = fmt.Sprintf("短 %d", i)
		}
		phase := scene.CellCommitted
		if i > count-mutableTail {
			phase = scene.CellMutable
		}
		boundaryClass := boundary.BoundaryNormal
		if rng.Intn(3) == 0 {
			boundaryClass = boundary.BoundaryDense
		}
		chainKey := ""
		if kind == scene.KindToolChain && rng.Intn(2) == 0 {
			chainKey = fmt.Sprintf("chain-%d", i/3)
		}
		groupKey := ""
		if rng.Intn(4) == 0 {
			groupKey = fmt.Sprintf("group-%d", i/2)
		}
		cells = append(cells, &scene.TranscriptCell{
			ID:               scene.CellID(i),
			Sequence:         uint64(i),
			Kind:             kind,
			Source:           source,
			Revision:         1,
			Phase:            phase,
			Boundary:         boundaryClass,
			ChainKey:         chainKey,
			BoundaryGroupKey: groupKey,
		})
	}
	return AppState{
		Revision:   11,
		Geometry:   GeometryState{Width: 40, Height: 12, Generation: 3},
		Transcript: NewTranscriptState(&scene.Snapshot{SceneID: 9, Cells: cells}),
		Bottom: BottomPaneState{
			StatusModel: &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		},
	}
}

func assertSameScreenRows(t *testing.T, label string, got, want AppScreenLayout) {
	t.Helper()
	if len(got.Rows) != len(want.Rows) {
		t.Fatalf("%s：行数不一致 got=%d want=%d", label, len(got.Rows), len(want.Rows))
	}
	for index := range got.Rows {
		if !reflect.DeepEqual(got.Rows[index], want.Rows[index]) {
			t.Fatalf("%s：第 %d 行不一致（OutputBottomRow got=%d want=%d）\ngot =%+v\nwant=%+v",
				label, index, got.OutputBottomRow, want.OutputBottomRow, got.Rows[index], want.Rows[index])
		}
	}
}

func TestLayoutAppScreenWindowedMatchesFullLayout(t *testing.T) {
	widths := []int{20, 45, 100}
	heights := []int{1, 3, 8, 21, 40}
	cases := []struct {
		name        string
		seed        int64
		cells       int
		mutableTail int
	}{
		{name: "简单 transcript", seed: 1, cells: 240, mutableTail: 0},
		{name: "混合内容（含折叠工具链）", seed: 7, cells: 180, mutableTail: 0},
		{name: "尾部未提交屏障", seed: 13, cells: 200, mutableTail: 3},
		{name: "短 transcript", seed: 21, cells: 6, mutableTail: 1},
		{name: "空/稠密混合", seed: 34, cells: 120, mutableTail: 2},
	}
	for _, testCase := range cases {
		state := t8bWindowFixture(testCase.seed, testCase.cells, testCase.mutableTail)
		for _, width := range widths {
			for _, height := range heights {
				state.Geometry.Width = width
				state.Geometry.Height = height
				label := fmt.Sprintf("%s/%dx%d", testCase.name, width, height)
				want := referenceFullLayoutAppScreen(state)
				got := LayoutAppScreen(state)
				assertSameScreenRows(t, label, got, want)
				// 缓存顺序不应影响结果：先参考后被测、再反过来各跑一遍。
				reversed := LayoutAppScreen(state)
				assertSameScreenRows(t, label+"（重跑）", reversed, want)
			}
		}
	}
}

func TestLayoutAppScreenWindowedFixtureHasFoldTarget(t *testing.T) {
	state := t8bWindowFixture(7, 180, 0)
	excluded := transcriptSuffixCellIDsFromFirstMutable(state.Transcript)
	if target := transcriptGlobalFoldTarget(state.Transcript, excluded); target == 0 {
		t.Fatal("fixture 未覆盖折叠路径：期望至少一个折叠工具链 cell")
	}
}

// TestLayoutAppScreenWindowedTrimBoundariesSweep 逐一扫过每个视口高度：尾部裁剪
// 的边界（laid 恰好等于 maxRows、差一行、多一行）是「首个 gap row 是否进入窗口」
// 最容易出错的地方。
func TestLayoutAppScreenWindowedTrimBoundariesSweep(t *testing.T) {
	cases := []struct {
		name        string
		seed        int64
		cells       int
		mutableTail int
	}{
		{name: "折叠与多行混合", seed: 7, cells: 90, mutableTail: 0},
		{name: "尾部屏障", seed: 13, cells: 60, mutableTail: 2},
		{name: "空 source 与稠密边界", seed: 34, cells: 48, mutableTail: 1},
	}
	for _, testCase := range cases {
		state := t8bWindowFixture(testCase.seed, testCase.cells, testCase.mutableTail)
		for _, width := range []int{20, 40} {
			state.Geometry.Width = width
			for height := 1; height <= 64; height++ {
				state.Geometry.Height = height
				label := fmt.Sprintf("%s/%dx%d", testCase.name, width, height)
				got := LayoutAppScreen(state)
				want := referenceFullLayoutAppScreen(state)
				assertSameScreenRows(t, label, got, want)
			}
		}
	}
}

func TestLayoutAppScreenWindowedMatchesFullLayoutAtScale(t *testing.T) {
	state := t7FrameState(5000)
	state.Geometry.Width = 40
	state.Geometry.Height = 12
	want := referenceFullLayoutAppScreen(state)
	got := LayoutAppScreen(state)
	assertSameScreenRows(t, "5000 cells", got, want)
}

// TestLayoutAppScreenWindowedAllocationsDoNotScaleWithHistory 固定视口下单帧分配量
// 必须与历史长度基本无关（窗口化布局输入的直接可观测契约）。
func TestLayoutAppScreenWindowedAllocationsDoNotScaleWithHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过分配量对照")
	}
	small := t7FrameState(200)
	big := t7FrameState(5000)
	small.Geometry = GeometryState{Width: 40, Height: 12, Generation: 2}
	big.Geometry = GeometryState{Width: 40, Height: 12, Generation: 2}
	_ = LayoutAppScreen(small)
	_ = LayoutAppScreen(big)

	const iterations = 20
	measure := func(state AppState) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := 0; i < iterations; i++ {
			_ = LayoutAppScreen(state)
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	smallBytes := measure(small)
	bigBytes := measure(big)
	t.Logf("LayoutAppScreen 分配量（%d 次）：200 cells=%dB 5000 cells=%dB（比值 %.3f）",
		iterations, smallBytes, bigBytes, float64(bigBytes)/float64(smallBytes))
	// 窗口内 cell 数量差异（尾部可达 maxRows+predecessor 个 cell）会带来小幅差别，
	// 但不得随历史长度线性增长：允许 200 cells 的 4 倍 + 固定余量。
	if bigBytes > smallBytes*4+256*1024 {
		t.Fatalf("单帧分配量随历史长度增长：200 cells=%dB 5000 cells=%dB", smallBytes, bigBytes)
	}
}
