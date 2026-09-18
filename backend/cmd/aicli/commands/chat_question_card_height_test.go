package commands

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

// 回归：提问卡片的正文上界必须是“终端当前真实可用行数”，而不是固定预算。
// 只要放得下，卡片就要把全部问题与选项逐行显示出来（例如一次 6 个问题）；
// 放不下时收缩也必须保持卡片整块可见：顶边框不被裁掉、Running/Waiting 行
// 不得插进卡片中间（它们必须排在卡片下方）。
func TestChatQuestionCardShowsEveryBodyLine(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	// 6 个问题 × 4 个选项：正文 31 行（含标题），加回答提示共 32 行。
	body := []string{"[提问] Agent 需要你的补充信息"}
	for index := 1; index <= 6; index++ {
		body = append(body, fmt.Sprintf("[提问] 问题：【测试问题 %d/6 · 单选】你希望本次交互面板测试的重点是什么？", index))
		for option := 1; option <= 4; option++ {
			body = append(body, fmt.Sprintf("[提问] %d. 第 %d 个选项的说明文本", option, option))
		}
	}
	band := "Running [broker] ask_user_question prompt=【测试问题 1/6】… required=true suggestions=[4]"

	t.Run("tall terminal keeps every line", func(t *testing.T) {
		rows := renderQuestionCardRows(t, 156, 44, body, band)
		card := popupRows(rows)
		if len(card) == 0 || !strings.Contains(card[0].Text, "┌") || !strings.Contains(card[len(card)-1].Text, "└") {
			t.Fatalf("question card is not a bordered box: %#v", card)
		}
		if len(card)-2 != len(body)+1 {
			t.Fatalf("card content rows = %d, want all %d body lines (+answer prompt)\n%s",
				len(card)-2, len(body), dumpQuestionRows(rows))
		}
		for index := 1; index <= 6; index++ {
			marker := fmt.Sprintf("【测试问题 %d/6", index)
			if !rowsContain(card, marker) {
				t.Fatalf("card dropped question %d\n%s", index, dumpQuestionRows(rows))
			}
		}
		assertQuestionCardChromeBelow(t, rows, card)
	})

	t.Run("short terminal keeps the card intact", func(t *testing.T) {
		rows := renderQuestionCardRows(t, 100, 24, body, band)
		card := popupRows(rows)
		if len(card) == 0 {
			t.Fatalf("question card missing\n%s", dumpQuestionRows(rows))
		}
		if !strings.Contains(card[0].Text, "┌") {
			t.Fatalf("question card lost its top border: %#v\n%s", card[0], dumpQuestionRows(rows))
		}
		if !strings.Contains(card[len(card)-1].Text, "└") {
			t.Fatalf("question card lost its bottom border: %#v\n%s", card[len(card)-1], dumpQuestionRows(rows))
		}
		for index, row := range card {
			if index > 0 && row.Row != card[index-1].Row+1 {
				t.Fatalf("question card is split at row %d\n%s", row.Row, dumpQuestionRows(rows))
			}
			if row.Owner != renderengine.RowOwnerPopup {
				t.Fatalf("card row %d owner = %s, want popup\n%s", row.Row, row.Owner, dumpQuestionRows(rows))
			}
		}
		assertQuestionCardChromeBelow(t, rows, card)
	})

	t.Run("very short terminal keeps the card intact", func(t *testing.T) {
		rows := renderQuestionCardRows(t, 80, 14, body, band)
		card := popupRows(rows)
		if len(card) < 3 {
			t.Fatalf("question card painted %d row(s)\n%s", len(card), dumpQuestionRows(rows))
		}
		if !strings.Contains(card[0].Text, "┌") {
			t.Fatalf("question card lost its top border: %#v\n%s", card[0], dumpQuestionRows(rows))
		}
		assertQuestionCardChromeBelow(t, rows, card)
	})
}

func renderQuestionCardRows(t *testing.T, width, height int, body []string, band string) []ui.AppScreenRow {
	t.Helper()
	rows, _ := renderQuestionCardFrame(t, width, height, body, band)
	return rows
}

func renderQuestionCardFrame(t *testing.T, width, height int, body []string, band string) ([]ui.AppScreenRow, string) {
	t.Helper()
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)

	session := &ChatSession{Surface: surface, InputBox: ui.NewInputBox(nil)}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	coord.SetSurface(surface)
	coord.SetWriter(os.Stdout)
	t.Cleanup(coord.Shutdown)
	if actor := coord.ensureUIActor(); actor != nil {
		actor.Post(ui.Resize{Width: width, Height: height, Generation: 1})
		coord.waitUIActorIdle()
	}

	lines := append(append([]string(nil), body...), answerPromptBodyLines(questionAnswerPrompt(true, true))...)
	coord.SetToolAgentStageDisplay("call-question", "ask_user_question", band)
	restoreStage := pushChatComposerAgentStage(session, chatAgentStageAwaitingAnswer)
	defer restoreStage()
	if !coord.ShowAnswerPrompt() {
		t.Fatal("expected answer prompt on the fixed surface")
	}
	coord.SetPromptInput("")
	cleanup, ok := showChatRuntimePriorityPromptBody(session, lines)
	if !ok {
		t.Fatal("expected question popup body")
	}
	defer cleanup()
	if !surface.SetActiveBand([]string{band}) {
		t.Fatal("expected the live tool band to be accepted")
	}
	coord.waitUIActorIdle()

	state := coord.uiActor.AppState()
	layout := ui.LayoutAppState(state).Bottom
	diagnostics := fmt.Sprintf("geometry=%dx%d popupRows=%d visiblePopup=%d promptRows=%d statusRows=%d bandRows=%d dynamicRows=%d noticeRows=%d planRows=%d outputBottom=%d promptStart=%d statusRow=%d",
		width, height, layout.PopupRows, len(layout.VisiblePopupLines), layout.PromptRows, layout.StatusRows,
		layout.ActiveBandRows, layout.DynamicStatusRows, layout.PromptNoticeRows,
		len(layout.RowPlan.Rows), layout.RowPlan.OutputBottomRow, layout.RowPlan.PromptInputStartRow, layout.RowPlan.StatusRow)
	t.Log(diagnostics)
	return ui.ComposeAppTextLayout(state).Rows, diagnostics
}

func popupRows(rows []ui.AppScreenRow) []ui.AppScreenRow {
	card := make([]ui.AppScreenRow, 0, len(rows))
	for _, row := range rows {
		if row.Owner == renderengine.RowOwnerPopup {
			card = append(card, row)
		}
	}
	return card
}

func rowsContain(rows []ui.AppScreenRow, marker string) bool {
	for _, row := range rows {
		if strings.Contains(row.Text, marker) {
			return true
		}
	}
	return false
}

// assertQuestionCardChromeBelow 断言 Running band / Waiting 状态行 / 回答输入行
// 都排在卡片下方，且卡片区间没有任何非 popup 行（被 band 或状态行切开即为回归）。
func assertQuestionCardChromeBelow(t *testing.T, rows []ui.AppScreenRow, card []ui.AppScreenRow) {
	t.Helper()
	cardStart, cardEnd := card[0].Row, card[len(card)-1].Row
	rowOf := func(pred func(ui.AppScreenRow) bool) (ui.AppScreenRow, bool) {
		for _, row := range rows {
			if pred(row) {
				return row, true
			}
		}
		return ui.AppScreenRow{}, false
	}
	bandRow, ok := rowOf(func(row ui.AppScreenRow) bool {
		return row.Owner == renderengine.RowOwnerBand && strings.Contains(row.Text, "ask_user_question")
	})
	if !ok {
		t.Fatalf("Running band row missing\n%s", dumpQuestionRows(rows))
	}
	if bandRow.Row <= cardEnd {
		t.Fatalf("Running band row %d is not below the card (ends %d)\n%s",
			bandRow.Row, cardEnd, dumpQuestionRows(rows))
	}
	if waitRow, ok := rowOf(func(row ui.AppScreenRow) bool {
		return row.Owner == renderengine.RowOwnerStatus && strings.Contains(row.Text, "Waiting for answer")
	}); ok && waitRow.Row <= bandRow.Row {
		t.Fatalf("Waiting row %d is not below the band row %d\n%s", waitRow.Row, bandRow.Row, dumpQuestionRows(rows))
	}
	for _, row := range rows {
		if row.Row < cardStart || row.Row > cardEnd {
			continue
		}
		if row.Owner != renderengine.RowOwnerPopup {
			t.Fatalf("row %d inside the card is owned by %s (%q)\n%s",
				row.Row, row.Owner, row.Text, dumpQuestionRows(rows))
		}
	}
	if promptRow, ok := rowOf(func(row ui.AppScreenRow) bool {
		return row.Owner == renderengine.RowOwnerPrompt && strings.TrimSpace(row.Text) == ">"
	}); ok && promptRow.Row <= cardEnd {
		t.Fatalf("prompt row %d is not below the card (ends %d)\n%s", promptRow.Row, cardEnd, dumpQuestionRows(rows))
	}
}

func dumpQuestionRows(rows []ui.AppScreenRow) string {
	var builder strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&builder, "row %2d owner=%-8s text=%q\n", row.Row, row.Owner, row.Text)
	}
	return builder.String()
}
