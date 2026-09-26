package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// TestBottomPanePlanReservesDynamicStatusRowAbovePrompt 固化统一渲染面的底部保留区
// 不变量：只要动态状态行在布局里占一行（它画在输入行上方），这一行就必须同时计入
// 保留区（RowPlan.OutputBottomRow 之上）。
//
// 反例（本测试要防的回归）：保留区只数了输入行与状态行，动态行被排到
// OutputBottomRow 之外——setRow 会把它静默丢弃，而历史区域恰好覆盖该行，用户看到
// 的就是「历史消息渲染覆盖动态状态栏的位置」。
func TestBottomPanePlanReservesDynamicStatusRowAbovePrompt(t *testing.T) {
	dynamicText := "◦ 恢复历史会话 200/250 (12s)"
	bottom := BottomPaneState{
		PromptVisible:      true,
		PromptReservedRows: 1,
		PromptLine:         "> ",
		DynamicStatusModel: &style.StatusLineModel{State: style.RunStreaming, StateText: dynamicText},
	}
	geometry := GeometryState{Width: 80, Height: 24}

	plan := LayoutBottomPaneRows(bottom, geometry)
	if len(plan.Rows) == 0 {
		t.Fatal("bottom pane plan is empty")
	}
	for _, row := range plan.Rows {
		t.Logf("plan row=%d owner=%s text=%q", row.Row, row.Owner, row.Text)
	}
	t.Logf("plan outputBottom=%d statusRow=%d promptInputStart=%d", plan.OutputBottomRow, plan.StatusRow, plan.PromptInputStartRow)

	dynamicRow := 0
	statusRowCount := 0
	for _, row := range plan.Rows {
		if strings.Contains(row.Text, "恢复历史会话") {
			dynamicRow = row.Row
		}
		if row.Owner == renderengine.RowOwnerStatus {
			statusRowCount++
		}
	}
	if dynamicRow == 0 {
		t.Fatalf("dynamic status row missing from the plan entirely: rows=%+v", plan.Rows)
	}
	if dynamicRow <= plan.OutputBottomRow {
		t.Fatalf("dynamic row %d must be reserved above the history region (outputBottom=%d); history would cover it",
			dynamicRow, plan.OutputBottomRow)
	}
	if dynamicRow >= plan.StatusRow {
		t.Fatalf("dynamic row %d must sit above the persistent status row %d", dynamicRow, plan.StatusRow)
	}
	// 输入行也要真的在保留区里（同一保留区必须同时容纳输入行与动态行）。
	if plan.PromptInputStartRow < 1 || plan.PromptInputStartRow <= plan.OutputBottomRow {
		t.Fatalf("prompt input row %d must be reserved as well (outputBottom=%d)", plan.PromptInputStartRow, plan.OutputBottomRow)
	}
}
