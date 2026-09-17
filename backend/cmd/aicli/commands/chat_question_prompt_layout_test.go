package commands

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

// 回归：ask_user_question 等待回答时，问题卡片（body-only popup）必须整块连续
// 渲染在底部 prompt 区域之上。此前同一工具调用的 ActiveBand Running 行与
// “Waiting for answer” 动态状态行会插入卡片中间，并把卡片尾部两行覆盖掉，
// 用户看到的是“卡片上段 / Running / Waiting / 卡片最后一条建议 / >”。
// 卡片现在由边框盒子承载：盒子高度随正文自动扩展（每条问题一行、超宽就地
// 折行），只受终端“放得下”的上界约束（ui.ModalBoxMaxRows：正文 + 边框不挤掉
// 底部 prompt 输入行与状态行）。
func TestChatQuestionPriorityPromptKeepsCardContiguous(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 120, 30
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)

	session := &ChatSession{Surface: surface, InputBox: ui.NewInputBox(nil)}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetSurface(surface)
	coord.SetWriter(os.Stdout)
	t.Cleanup(coord.Shutdown)
	if actor := coord.ensureUIActor(); actor != nil {
		actor.Post(ui.Resize{Width: width, Height: height, Generation: 1})
		coord.waitUIActorIdle()
	}

	prompt := "这两个「会话用户」卡片希望怎么处理？（它们目前的作用是切换会话列表按哪个 runtime 用户过滤）"
	suggestions := []string{
		"保留现状：继续按 runtime 用户过滤会话列表",
		"增加说明：卡片上标注过滤范围",
		"增加可移除入口：卡片上提供隐藏/删除，并持久化到本地",
	}
	body := append(
		append([]string(nil), questionPriorityPromptLines(prompt, suggestions)...),
		answerPromptBodyLines(questionAnswerPrompt(true, len(suggestions) > 0))...,
	)

	// 生产实测：进入提问等待时 coordinator 会清空 ActiveBand（syncAgentStageActiveBandLocked
	// 的 default 分支），正常时序的等待帧并不含 Running 行。但该清理与 tool.requested
	// 事件存在竞态（tool 事件晚到、或 legacy 绘制路径仍持有 band），用户报告的截图正是
	// band 行仍存活的那一帧：当时它插进卡片中间并覆盖卡片尾部。这里刻意保留该 band 行，
	// 锁定“无论事件顺序如何，问题卡片都必须整块连续”的不变量。
	band := "Running [broker] ask_user_question prompt=" + prompt + " required=true"
	coord.SetToolAgentStageDisplay("call-question", "ask_user_question", band)
	restoreStage := pushChatComposerAgentStage(session, chatAgentStageAwaitingAnswer)
	defer restoreStage()
	if !coord.ShowAnswerPrompt() {
		t.Fatal("expected answer prompt on the fixed surface")
	}
	coord.SetPromptInput("")
	cleanup, ok := showChatRuntimePriorityPromptBody(session, body)
	if !ok {
		t.Fatal("expected question popup body")
	}
	defer cleanup()
	if !surface.SetActiveBand([]string{band}) {
		t.Fatal("expected the live tool band to be accepted")
	}
	coord.waitUIActorIdle()

	state := coord.uiActor.AppState()
	plan := ui.LayoutAppState(state).Bottom.RowPlan
	frame := ui.ComposeAppTextLayout(state)
	debug := func() string {
		var b strings.Builder
		fmt.Fprintf(&b, "plan: promptInputStart=%d rows=%d statusRow=%d outputBottom=%d\n",
			plan.PromptInputStartRow, plan.PromptInputRows, plan.StatusRow, plan.OutputBottomRow)
		for _, row := range frame.Rows {
			fmt.Fprintf(&b, "row %2d owner=%-8s text=%q\n", row.Row, row.Owner, row.Text)
		}
		return b.String()
	}

	// 1) 问题卡片整块连续，并由边框盒子承载：高度随正文自动扩展（每条问题
	// 一行、超宽就地折行），且不超过终端上界 ModalBoxMaxRows（正文 + 边框
	// 不挤掉底部 prompt 输入行与状态行）。首末行是盒子边框，内容行仍携带
	// 问题摘要与回答提示，卡片尾部不被覆盖。
	card := make([]ui.BottomPaneRow, 0, len(body))
	for _, row := range plan.Rows {
		if row.Owner == renderengine.RowOwnerPopup {
			card = append(card, row)
		}
	}
	if len(card) < 3 {
		t.Fatalf("question card painted %d row(s), want a bordered box\n%s", len(card), debug())
	}
	if maxRows := ui.ModalBoxMaxRows(height); maxRows > 0 && len(card) > maxRows {
		t.Fatalf("question card rows = %d, want <= %d (box must not crowd out the prompt/status rows)\n%s",
			len(card), maxRows, debug())
	}
	for index, row := range card {
		if index > 0 && row.Row != card[index-1].Row+1 {
			t.Fatalf("question card is split at row %d\n%s", row.Row, debug())
		}
		if strings.TrimSpace(row.Text) == "" {
			t.Fatalf("card row %d is blank\n%s", row.Row, debug())
		}
	}
	if !strings.Contains(card[0].Text, "┌") || !strings.Contains(card[len(card)-1].Text, "└") {
		t.Fatalf("question card is not a bordered box: %#v\n%s", card, debug())
	}
	if !strings.Contains(card[1].Text, "[提问]") || !strings.Contains(card[len(card)-2].Text, "请输入回答") {
		t.Fatalf("question card lost its question summary or answer prompt: %#v\n%s", card, debug())
	}
	cardStart, cardEnd := card[0].Row, card[len(card)-1].Row

	// 2) 卡片范围内的每一行都必须归 popup 所有：Running 行、Waiting 行或
	// 输入行插进这段区间即为本次回归。
	for _, row := range frame.Rows {
		if row.Row < cardStart || row.Row > cardEnd {
			continue
		}
		if row.Owner != renderengine.RowOwnerPopup {
			t.Fatalf("row %d inside the question card is owned by %s (%q)\n%s",
				row.Row, row.Owner, row.Text, debug())
		}
	}

	// 3) band / 动态状态 / 回答输入行都排在卡片下方，并保持既有相对顺序。
	rowOf := func(pred func(ui.AppScreenRow) bool) ui.AppScreenRow {
		for _, row := range frame.Rows {
			if pred(row) {
				return row
			}
		}
		t.Fatalf("expected row not found in frame\n%s", debug())
		return ui.AppScreenRow{}
	}
	bandRow := rowOf(func(row ui.AppScreenRow) bool {
		return row.Owner == renderengine.RowOwnerBand && strings.Contains(row.Text, "ask_user_question")
	})
	waitRow := rowOf(func(row ui.AppScreenRow) bool {
		return row.Owner == renderengine.RowOwnerStatus && strings.Contains(row.Text, "Waiting for answer")
	})
	promptRow := rowOf(func(row ui.AppScreenRow) bool {
		return row.Owner == renderengine.RowOwnerPrompt && strings.TrimSpace(row.Text) == ">"
	})
	if !(bandRow.Row > cardEnd && waitRow.Row > bandRow.Row && promptRow.Row > waitRow.Row) {
		t.Fatalf("bottom rows out of order: card=%d..%d band=%d waiting=%d prompt=%d\n%s",
			cardStart, cardEnd, bandRow.Row, waitRow.Row, promptRow.Row, debug())
	}
	if plan.PromptInputStartRow != promptRow.Row {
		t.Fatalf("plan prompt row = %d, frame prompt row = %d\n%s", plan.PromptInputStartRow, promptRow.Row, debug())
	}
}
