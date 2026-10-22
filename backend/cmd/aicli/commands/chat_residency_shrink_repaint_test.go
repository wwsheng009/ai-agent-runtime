package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// chat_residency_shrink_repaint_test.go —— T9 R1/R4 的最小复现：
// 生产逐出后 bridge 快照会**变短**，下一次 ReplaceTranscriptAction 会让内联
// 视口从「满屏」缩到「窗口」。底部状态行/提示行必须被重新画出，否则用户会
// 看到状态行消失（真实 Windows Terminal fixture 在冷前缀模式下观察到此现象）。
func TestTranscriptSnapshotShrinkRepaintsBottomPane(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 100, 20
	controller := ui.NewUIController(ui.UIControllerConfig{}, nil, nil)
	go controller.Run()
	var physical bytes.Buffer
	executor := ui.NewTerminalSessionExecutor(controller, ui.NewTerminalSession(&physical))
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})

	deliver := func(snapshot *scene.Snapshot) {
		t.Helper()
		for _, action := range []ui.UIAction{ui.ReplaceTranscriptAction{Snapshot: snapshot}} {
			if !controller.Post(action) {
				t.Fatalf("post %T", action)
			}
		}
		controller.WaitIdle()
		executor.Request()
		executor.WaitIdle()
		controller.WaitIdle()
	}

	for _, action := range []ui.UIAction{
		ui.Resize{Width: width, Height: height, Generation: 1},
		ui.SetStatusModelAction{Status: style.StatusLineModel{State: style.RunReady, StateText: "SHRINK-STATUS-SENTINEL"}},
		ui.ShowPromptAction{Line: "SHRINK-PROMPT-SENTINEL> "},
	} {
		if !controller.Post(action) {
			t.Fatalf("post %T", action)
		}
	}
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	full := shrinkFixtureSnapshot(40)
	deliver(full)
	if !strings.Contains(physical.String(), "SHRINK-STATUS-SENTINEL") {
		t.Fatal("前置条件失败：满屏快照下状态行未绘制")
	}

	// 逐出语义：快照变短到窗口大小（生产桥接在写穿裁剪后就是这样）。
	short := shrinkFixtureSnapshot(8)
	short.Revision = full.Revision + 1
	deliver(short)

	// 重新投影屏幕：状态行与提示行必须在**增量写入之后**仍然可见。
	screen := vt.NewScreen(width, height)
	screen.Feed(physical.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	if !strings.Contains(projected, "SHRINK-STATUS-SENTINEL") {
		t.Fatalf("快照缩短后状态行从终端投影中消失（这是用户可见缺陷）\n--- physical tail ---\n%s\n--- screen ---\n%s",
			tailString(physical.String(), 2000), screen.Dump())
	}
	if !strings.Contains(projected, "SHRINK-PROMPT-SENTINEL") {
		t.Fatalf("快照缩短后提示行从终端投影中消失\n--- screen ---\n%s", screen.Dump())
	}
	// 窗口内容必须完整可见。
	for index := 33; index < 40; index++ {
		marker := shrinkMarker(index)
		if !strings.Contains(projected, marker) {
			t.Fatalf("窗口内 marker %q 缺失（缩短后重绘不完整）\n--- screen ---\n%s", marker, screen.Dump())
		}
	}
}

// TestTranscriptSnapshotShrinkToWindowKeepsStatus 复刻真实 fixture 的比例
// （73 cells → 24 cells，100×24 终端）：逐出后状态行/提示行必须仍在。
func TestTranscriptSnapshotShrinkToWindowKeepsStatus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 100, 24
	controller := ui.NewUIController(ui.UIControllerConfig{}, nil, nil)
	go controller.Run()
	var physical bytes.Buffer
	executor := ui.NewTerminalSessionExecutor(controller, ui.NewTerminalSession(&physical))
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})
	deliver := func(snapshot *scene.Snapshot) {
		t.Helper()
		if !controller.Post(ui.ReplaceTranscriptAction{Snapshot: snapshot}) {
			t.Fatal("post transcript")
		}
		controller.WaitIdle()
		executor.Request()
		executor.WaitIdle()
		controller.WaitIdle()
	}
	for _, action := range []ui.UIAction{
		ui.Resize{Width: width, Height: height, Generation: 1},
		ui.SetStatusModelAction{Status: style.StatusLineModel{State: style.RunReady, StateText: "SHRINK-STATUS-BIG"}},
		ui.ShowPromptAction{Line: "SHRINK-PROMPT-BIG> "},
	} {
		if !controller.Post(action) {
			t.Fatalf("post %T", action)
		}
	}
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	deliver(shrinkFixtureSnapshot(73))
	deliver(shrinkFixtureSnapshot(24))

	state := controller.State()
	if state.Bottom.StatusModel == nil || state.Bottom.StatusModel.StateText != "SHRINK-STATUS-BIG" {
		t.Fatalf("缩小快照后 StatusModel 丢失：%+v", state.Bottom.StatusModel)
	}
	screen := vt.NewScreen(width, height)
	screen.Feed(physical.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	if !strings.Contains(projected, "SHRINK-STATUS-BIG") {
		t.Fatalf("缩小到窗口后状态行从终端投影消失\n--- screen ---\n%s", screen.Dump())
	}
	if !strings.Contains(projected, "SHRINK-PROMPT-BIG") {
		t.Fatalf("缩小到窗口后提示行从终端投影消失\n--- screen ---\n%s", screen.Dump())
	}
}

func shrinkMarker(index int) string {
	return "SHRINK-CELL-" + string(rune('A'+index/26)) + string(rune('a'+index%26))
}

func shrinkFixtureSnapshot(count int) *scene.Snapshot {
	cells := make([]*scene.TranscriptCell, 0, count)
	for index := 0; index < count; index++ {
		cells = append(cells, &scene.TranscriptCell{
			ID:       scene.CellID(index + 1),
			Sequence: uint64(index + 1),
			Revision: 1,
			Kind:     scene.KindAssistant,
			Source:   shrinkMarker(index),
			Phase:    scene.CellCommitted,
		})
	}
	return &scene.Snapshot{Revision: 1, Cells: cells}
}
