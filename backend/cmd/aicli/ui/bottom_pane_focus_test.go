package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

// 提问正文等“只有正文、没有输入行”的 popup 不得接管光标：回答输入已并入
// 底部 prompt，光标必须跟随 bottom prompt 的输入（否则 compose 阶段取最后
// 一条 popup 行宽度 + 1，会把光标钉在静态提示语之后、答案文本之前）。
func TestBottomFocusForPopupOwnership(t *testing.T) {
	bodyOnly := PopupLayer{Owner: "priority_prompt", Lines: []string{"[提问] 问题：怎么处理？"}}
	withInput := PopupLayer{Owner: "priority_prompt", Lines: []string{"[提问] 问题：怎么处理？"}, ComposerLine: "请输入回答："}

	cases := []struct {
		name          string
		layer         PopupLayer
		promptVisible bool
		want          BottomFocus
	}{
		{"body-only popup keeps prompt focus", bodyOnly, true, BottomFocusPrompt},
		{"popup with input row owns focus", withInput, true, BottomFocusPopup},
		{"body-only popup without prompt keeps popup focus", bodyOnly, false, BottomFocusPopup},
		{"no popup keeps prompt focus", PopupLayer{}, true, BottomFocusPrompt},
		{"no popup without prompt clears focus", PopupLayer{}, false, BottomFocusNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bottomFocusForPopup(tc.layer, tc.promptVisible); got != tc.want {
				t.Fatalf("bottomFocusForPopup(%+v, promptVisible=%v) = %v, want %v", tc.layer, tc.promptVisible, got, tc.want)
			}
		})
	}
}

func TestSetActivePopupLayerKeepsPromptFocusForQuestionBody(t *testing.T) {
	state := BottomPaneState{PromptVisible: true, PromptLine: "> ", PromptReservedRows: 1}
	state.setActivePopupLayer(PopupLayer{
		Owner: "priority_prompt",
		Lines: []string{"[提问] 问题：怎么处理？", "[提问] 1. 继续"},
	})
	if state.ComposerLine != "" {
		t.Fatalf("body-only popup must not carry a composer input row, got %q", state.ComposerLine)
	}
	if state.Focus != BottomFocusPrompt {
		t.Fatalf("focus = %v, want BottomFocusPrompt for body-only popup", state.Focus)
	}

	// popup 真正拥有输入行时仍然接管光标。
	state.setActivePopupLayer(PopupLayer{
		Owner:        "priority_prompt",
		Lines:        []string{"[提问] 问题：怎么处理？"},
		ComposerLine: "请输入回答：",
	})
	if state.Focus != BottomFocusPopup {
		t.Fatalf("focus = %v, want BottomFocusPopup for popup with input row", state.Focus)
	}
}

func TestSurfaceQuestionBodyPopupKeepsPromptCursorOwner(t *testing.T) {
	surface := NewFixedBottomSurface(NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)

	surface.ShowPrompt("> ")
	surface.SetPromptInputStateVersioned("> ", "2", 1, 0, 3, 0)

	lines := []string{"[提问] 问题：需要执行哪些文档改动？"}
	surface.BeginPopupInputForOwnerWithViewport(lines, "", "priority_prompt", PopupViewportSpec{})

	state := surface.bottomPaneStateLocked()
	if strings.TrimSpace(state.ComposerLine) != "" {
		t.Fatalf("question body popup must not publish a composer input row, got %q", state.ComposerLine)
	}
	if !state.PromptVisible {
		t.Fatal("expected bottom prompt to stay visible under the question body popup")
	}
	if state.Focus != BottomFocusPrompt {
		t.Fatalf("focus = %v, want BottomFocusPrompt", state.Focus)
	}
}

// 光标必须留在底部 prompt 输入行上并跟随答案文本，而不是停在 popup 正文行。
func TestComposeAppTextLayoutKeepsAnswerCursorOnPromptRow(t *testing.T) {
	state := composeFixtureState()
	state.Bottom.Focus = BottomFocusPrompt
	state.Bottom.PromptLine = "> "
	state.Bottom.PromptVisible = true
	state.Bottom.PromptReservedRows = 1
	state.Bottom.PromptCursorKnown = true
	state.Bottom.PopupOwner = "priority_prompt"
	state.Bottom.PopupLines = []string{"[提问] 问题：怎么处理？", "[提问] 1. 继续"}
	state.Bottom.ComposerLine = "" // 回答输入不再占用 popup 输入行

	cursorFor := func(input string) *AppCursor {
		state.Bottom.PromptInput = input
		state.Bottom.PromptCursor = DisplayWidth(input)
		state.Bottom.PromptCursorCol = DisplayWidth(state.Bottom.PromptLine) + DisplayWidth(input)
		frame := ComposeAppTextLayout(state)
		if frame.Cursor == nil {
			t.Fatalf("cursor suppressed for input %q", input)
		}
		return frame.Cursor
	}

	empty := cursorFor("")
	typed := cursorFor("2")
	if empty.Focus != BottomFocusPrompt || typed.Focus != BottomFocusPrompt {
		t.Fatalf("cursor focus = %v/%v, want BottomFocusPrompt", empty.Focus, typed.Focus)
	}
	if typed.Row != empty.Row {
		t.Fatalf("typing moved cursor row from %d to %d, want same prompt row", empty.Row, typed.Row)
	}
	if typed.Col != empty.Col+DisplayWidth("2") {
		t.Fatalf("cursor col = %d, want %d so the cursor follows the typed answer", typed.Col, empty.Col+DisplayWidth("2"))
	}

	plan := LayoutAppState(state).Bottom.RowPlan
	if typed.Row < plan.PromptInputStartRow || typed.Row > plan.PromptInputStartRow+plan.PromptInputRows-1 {
		t.Fatalf("cursor row %d outside prompt input rows %d..%d",
			typed.Row, plan.PromptInputStartRow, plan.PromptInputStartRow+plan.PromptInputRows-1)
	}
	// 光标不得落在 popup 正文行上。
	for _, row := range ComposeAppTextLayout(state).Rows {
		if row.Owner == renderengine.RowOwnerPopup && row.Row == typed.Row {
			t.Fatalf("cursor row %d still owned by popup body: %q", typed.Row, row.Text)
		}
	}
}
