package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// TestBottomPaneDynamicStatusRowReservationIsStableAcrossModelStates 固化 composer
// 的稳定性前提：开启 ReserveDynamicStatusRow 后，band 高度——以及由此决定的
// OutputBottomRow 与历史容量——必须在「无模型 / blank 模型 / 活跃模型」三种状态
// 下完全一致。
//
// 反例（真实现场捕获到的回归）：预留跟随模型内容变化，动态行出现/消失会让
// OutputBottomRow 在 38/39 之间漂移，历史滚动区的底边随之占用动态行所在行，
// 用户看到「动态状态栏被历史消息覆盖冲刷」，且同一会话内两次运行的 band 高度
// 都不一致（capture 中 run1=39..44、run2=40..44）。
func TestBottomPaneDynamicStatusRowReservationIsStableAcrossModelStates(t *testing.T) {
	geometry := GeometryState{Width: 100, Height: 44}
	base := BottomPaneState{
		StatusModel:             &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		SessionIDLine:           "会话 session_20260926101217_Ol9LoKuj",
		PromptVisible:           true,
		PromptReservedRows:      1,
		PromptLine:              "> ",
		ReserveDynamicStatusRow: true,
	}

	variants := []struct {
		name   string
		model  *style.StatusLineModel
		stable bool
	}{
		{name: "no-model", model: nil, stable: true},
		{name: "blank-model", model: &style.StatusLineModel{State: style.RunReady}, stable: true},
		{name: "live-model", model: &style.StatusLineModel{State: style.RunStreaming, StateText: "◦ 恢复历史会话 200/713 (1s)"}, stable: true},
	}

	pinnedBottom, pinnedBandRows := 0, 0
	for _, variant := range variants {
		state := base
		state.DynamicStatusModel = variant.model
		plan := LayoutBottomPaneRows(state, geometry)
		bandRows := geometry.Height - plan.OutputBottomRow
		t.Logf("%s: outputBottom=%d bandRows=%d statusRow=%d promptInputStart=%d",
			variant.name, plan.OutputBottomRow, bandRows, plan.StatusRow, plan.PromptInputStartRow)
		if pinnedBottom == 0 {
			pinnedBottom, pinnedBandRows = plan.OutputBottomRow, bandRows
			continue
		}
		if plan.OutputBottomRow != pinnedBottom || bandRows != pinnedBandRows {
			t.Fatalf("%s 改变了 band 高度：outputBottom=%d bandRows=%d，期望恒定 %d/%d（历史容量必须与瞬时模型解耦）",
				variant.name, plan.OutputBottomRow, bandRows, pinnedBottom, pinnedBandRows)
		}
	}
	if pinnedBandRows < 4 {
		t.Fatalf("保留区行数 %d 过少，动态行/输入行/状态行不可能同时容纳", pinnedBandRows)
	}

	// 关闭常驻预留（legacy surface 语义）时允许漂移——这正是统一 composer 必须
	// 显式开启预留的原因，也让本测试的“稳定性”断言有实际约束力。
	legacyFree := base
	legacyFree.ReserveDynamicStatusRow = false
	legacyFree.DynamicStatusModel = nil
	legacyPlan := LayoutBottomPaneRows(legacyFree, geometry)
	legacyLive := legacyFree
	legacyLive.DynamicStatusModel = &style.StatusLineModel{State: style.RunStreaming, StateText: "◦ 恢复历史会话 200/713 (1s)"}
	legacyLivePlan := LayoutBottomPaneRows(legacyLive, geometry)
	if legacyPlan.OutputBottomRow == legacyLivePlan.OutputBottomRow {
		t.Fatalf("期望 legacy（未开启预留）在动态行出现时改变 OutputBottomRow，实际都是 %d——该断言失去约束力",
			legacyPlan.OutputBottomRow)
	}
}
