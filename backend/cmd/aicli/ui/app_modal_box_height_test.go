package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// 回归：提问卡片的正文上界必须是“终端当前真实可用行数”，而不是固定预算。
// 正文再长（例如一次 6 个问题 × 4 个选项）也要逐行保留，直到终端放不下；
// 放不下需要收缩时，盒子仍必须完整可见：顶边框留在屏内，且 Running band /
// Waiting 动态状态行 / 回答输入行全部排在盒子下方，不得插进卡片中间。
func TestModalBoxLinesFillsAvailableHeightWithoutClipping(t *testing.T) {
	body := []string{"[提问] Agent 需要你的补充信息"}
	for question := 1; question <= 6; question++ {
		body = append(body, fmt.Sprintf("[提问] 问题：【测试问题 %d/6 · 单选】你希望本次交互面板测试的重点是什么？", question))
		for option := 1; option <= 4; option++ {
			body = append(body, fmt.Sprintf("[提问] %d. 第 %d 个选项的说明文本", option, option))
		}
	}
	body = append(body, "[提问] 请输入回答，可输入建议编号（必答）：")

	stateFor := func() BottomPaneState {
		return BottomPaneState{
			StatusModel:        &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
			PromptLine:         "> ",
			PromptVisible:      true,
			PromptReservedRows: 1,
			PromptCursorKnown:  true,
			ActiveBandLines:    []string{"Running [broker] ask_user_question prompt=… required=true"},
			DynamicStatusModel: &style.StatusLineModel{State: style.RunStreaming, StateText: "Waiting for answer"},
			PopupLines:         body,
			PopupOwner:         modalBoxTestOwner,
		}
	}

	t.Run("tall terminal keeps every body line", func(t *testing.T) {
		geometry := GeometryState{Width: 156, Height: 44}
		derived := DeriveBottomPaneState(stateFor(), geometry)
		box := modalBoxLines(derived, geometry.Height, geometry.Width)
		if len(box) != len(body)+2 {
			t.Fatalf("box rows = %d, want %d (all body lines + borders)\n%s",
				len(box), len(body)+2, strings.Join(box, "\n"))
		}
		for index, line := range body {
			if !strings.Contains(box[index+1], strings.TrimSpace(line)) {
				t.Fatalf("box dropped line %d (%q)\n%s", index, line, strings.Join(box, "\n"))
			}
		}
	})

	t.Run("short terminal shrinks instead of clipping", func(t *testing.T) {
		geometry := GeometryState{Width: 100, Height: 24}
		derived := DeriveBottomPaneState(stateFor(), geometry)
		box := modalBoxLines(derived, geometry.Height, geometry.Width)
		if len(box) < 3 {
			t.Fatalf("expected a bordered box, got %#v", box)
		}
		if !strings.HasPrefix(strings.TrimLeft(box[0], " "), "┌") {
			t.Fatalf("box top border missing: %q", box[0])
		}
		if !strings.HasPrefix(strings.TrimLeft(box[len(box)-1], " "), "└") {
			t.Fatalf("box bottom border missing: %q", box[len(box)-1])
		}
		gap := derived.popupBottomGapRowCount()
		if len(box) > geometry.Height-2-gap {
			t.Fatalf("box rows %d exceed the space above the prompt area (height=%d gap=%d)",
				len(box), geometry.Height, gap)
		}
		if maxRows := ModalBoxMaxRows(geometry.Height); len(box) > maxRows {
			t.Fatalf("box rows %d exceed terminal ceiling %d", len(box), maxRows)
		}
		assertBoxPrecedesPromptArea(t, stateFor(), geometry)
	})

	t.Run("very short terminal shrinks instead of clipping", func(t *testing.T) {
		geometry := GeometryState{Width: 80, Height: 14}
		derived := DeriveBottomPaneState(stateFor(), geometry)
		box := modalBoxLines(derived, geometry.Height, geometry.Width)
		if len(box) < 3 {
			t.Fatalf("expected a bordered box, got %#v", box)
		}
		if !strings.HasPrefix(strings.TrimLeft(box[0], " "), "┌") {
			t.Fatalf("box top border missing: %q", box[0])
		}
		assertBoxPrecedesPromptArea(t, stateFor(), geometry)
	})
}

// assertBoxPrecedesPromptArea 断言盒子整块位于置顶位置可见，且其下方依次是
// band / 动态状态行 / prompt 输入行：任何一行落进盒子区间即为回归。
func assertBoxPrecedesPromptArea(t *testing.T, state BottomPaneState, geometry GeometryState) {
	t.Helper()
	plan := LayoutBottomPaneRows(state, geometry)
	dump := func() string {
		var builder strings.Builder
		for _, row := range plan.Rows {
			fmt.Fprintf(&builder, "row %2d owner=%-8s text=%q\n", row.Row, row.Owner, row.Text)
		}
		return builder.String()
	}

	popupRows := make([]BottomPaneRow, 0, len(plan.Rows))
	for _, row := range plan.Rows {
		if row.Owner == renderengine.RowOwnerPopup {
			popupRows = append(popupRows, row)
		}
	}
	if len(popupRows) < 3 {
		t.Fatalf("popup block painted %d row(s)\n%s", len(popupRows), dump())
	}
	for index, row := range popupRows {
		if index > 0 && row.Row != popupRows[index-1].Row+1 {
			t.Fatalf("popup block split at row %d\n%s", row.Row, dump())
		}
	}
	boxTop := popupRows[0]
	if boxTop.Row < 1 {
		t.Fatalf("box top row %d is off-screen\n%s", boxTop.Row, dump())
	}
	boxEnd := popupRows[len(popupRows)-1].Row

	find := func(pred func(BottomPaneRow) bool) (BottomPaneRow, bool) {
		for _, row := range plan.Rows {
			if pred(row) {
				return row, true
			}
		}
		return BottomPaneRow{}, false
	}
	bandRow, ok := find(func(row BottomPaneRow) bool {
		return row.Owner == renderengine.RowOwnerBand && strings.Contains(row.Text, "ask_user_question")
	})
	if !ok {
		t.Fatalf("band row missing\n%s", dump())
	}
	if bandRow.Row <= boxEnd {
		t.Fatalf("band row %d is not below the box (ends %d)\n%s", bandRow.Row, boxEnd, dump())
	}
	if waitRow, ok := find(func(row BottomPaneRow) bool {
		return row.Owner == renderengine.RowOwnerStatus && strings.Contains(row.Text, "Waiting for answer")
	}); ok && waitRow.Row <= bandRow.Row {
		t.Fatalf("waiting row %d is not below the band row %d\n%s", waitRow.Row, bandRow.Row, dump())
	}
	if promptRow, ok := find(func(row BottomPaneRow) bool {
		return row.Owner == renderengine.RowOwnerPrompt && strings.TrimSpace(row.Text) == ">"
	}); ok && promptRow.Row <= boxEnd {
		t.Fatalf("prompt row %d is not below the box (ends %d)\n%s", promptRow.Row, boxEnd, dump())
	}
}
