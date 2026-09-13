package commands

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// 审批面板必须与提问回答共用同一条合并输入通道：面板正文（工具/原因/风险/
// 参数摘要/操作提示）只作为 popup body 呈现，不再拥有独立的 popup 输入行，
// 因此面板既不会覆盖也不会顶掉用户的底部 prompt 区；审批答案直接在 composer
// 区输入，光标跟随答案文本。
func TestChatApprovalAnswerMergedIntoBottomPromptReadsThroughPromptRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)

	restoreStdio := withTransientStdio(t, "1\n")
	defer restoreStdio()

	session := &ChatSession{
		Surface:     surface,
		InputBox:    ui.NewInputBox(nil),
		InputReader: bufio.NewReader(strings.NewReader("stale\n")),
	}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetSurface(surface)
	coord.SetWriter(os.Stdout)
	// 生产路径由 presenter 上报终端几何；这里显式投递一次 Resize，便于从
	// actor AppState 派生可比较的文本帧与光标。
	if actor := coord.ensureUIActor(); actor != nil {
		actor.Post(ui.Resize{Width: 80, Height: 24, Generation: 1})
		coord.waitUIActorIdle()
	}
	coord.SetPromptInput("未提交草稿")

	if !chatMergedAnswerPromptSupported(session) {
		t.Fatal("expected fixed surface session to support merged answer input")
	}

	approval := &runtimechat.ApprovalRequest{
		ID:         "appr_write",
		ToolCallID: "call_write",
		ToolName:   "write",
		ArgsJSON:   []byte(`{"content":"package main"}`),
		Reason:     "permission_mode_requires_approval",
		RiskLevel:  "high",
	}
	promptLine := approvalDecisionPromptWithReuse("")
	body := append(
		approvalPriorityPromptLines(approval, []string{"permission_mode=default"}),
		answerPromptBodyLines(promptLine)...,
	)

	cleanup, ok := showChatRuntimePriorityPromptBody(session, body)
	if !ok {
		t.Fatal("expected approval popup body without dedicated input row")
	}
	// 合并路径不得占用 popup 输入通道：审批答案属于底部 prompt，面板只保留正文。
	if session.priorityPopupHandle.Valid() {
		t.Fatal("merged approval prompt must not claim the popup input (ComposerLine) channel")
	}

	// 审批答案并入底部 composer 区（与 chatMergedPromptComposer 的顺序一致）。
	coord.SetPromptInput("")
	if !coord.ShowAnswerPrompt() {
		t.Fatal("expected approval answer prompt to reuse the fixed bottom prompt row")
	}
	coord.SetPromptInput("1")
	coord.waitUIActorIdle()
	if coord.uiActor == nil {
		t.Fatal("expected coordinator UI actor for surface projection")
	}
	state := coord.uiActor.AppState()
	if state.Bottom.ComposerLine != "" {
		t.Fatalf("approval panel must not publish a popup input row, got %q", state.Bottom.ComposerLine)
	}
	popupText := strings.Join(state.Bottom.PopupLines, "\n")
	for _, want := range []string{
		"[审批] Agent 请求执行需要授权的操作",
		"[审批] 工具：write",
		"[审批] 风险等级：高（high）",
		"[审批] 上下文：permission_mode=default",
		"[审批] 请选择 [1] 仅本次允许  [2] 拒绝  [3] 查看完整参数（兼容 y/n）：",
	} {
		if !strings.Contains(popupText, want) {
			t.Fatalf("expected approval panel to carry %q in popup rows: %#v", want, state.Bottom.PopupLines)
		}
	}
	if state.Bottom.PromptInput != "1" {
		t.Fatalf("expected the approval answer to be rendered on the bottom prompt, got %q", state.Bottom.PromptInput)
	}
	if state.Bottom.Focus != ui.BottomFocusPrompt {
		t.Fatalf("focus = %v, want BottomFocusPrompt while typing the approval answer", state.Bottom.Focus)
	}

	// 面板不得覆盖底部 prompt 区：所有 popup 行必须整体落在 prompt 输入行之上。
	plan := ui.LayoutAppState(state).Bottom.RowPlan
	for _, row := range plan.Rows {
		if row.Owner == renderengine.RowOwnerPopup && row.Row >= plan.PromptInputStartRow {
			t.Fatalf("approval panel row %d (%q) overlaps the bottom prompt rows starting at %d (rows=%#v)",
				row.Row, row.Text, plan.PromptInputStartRow, plan.Rows)
		}
	}

	// 光标必须停在底部 prompt 输入行并跟随已输入答案，而不是停在面板正文行。
	frame := ui.ComposeAppTextLayout(state)
	if frame.Cursor == nil || frame.Cursor.Focus != ui.BottomFocusPrompt {
		t.Fatalf("cursor = %+v, want BottomFocusPrompt (geometry=%+v bottom=%+v plan=%+v)",
			frame.Cursor, state.Geometry, state.Bottom, plan)
	}
	if frame.Cursor.Row < plan.PromptInputStartRow || frame.Cursor.Row > plan.PromptInputStartRow+plan.PromptInputRows-1 {
		t.Fatalf("cursor row %d outside prompt input rows %d..%d",
			frame.Cursor.Row, plan.PromptInputStartRow, plan.PromptInputStartRow+plan.PromptInputRows-1)
	}
	answerCol := frame.Cursor.Col
	coord.SetPromptInput("12")
	coord.waitUIActorIdle()
	typedFrame := ui.ComposeAppTextLayout(coord.uiActor.AppState())
	if typedFrame.Cursor == nil || typedFrame.Cursor.Col != answerCol+1 || typedFrame.Cursor.Row != frame.Cursor.Row {
		t.Fatalf("cursor %+v did not follow the typed approval answer from %+v", typedFrame.Cursor, frame.Cursor)
	}
	for _, row := range typedFrame.Rows {
		if row.Row == typedFrame.Cursor.Row && row.Owner == renderengine.RowOwnerPopup {
			t.Fatalf("cursor row %d still owned by the approval panel: %q", row.Row, row.Text)
		}
	}

	// 恢复未提交草稿后再验证合并读取路径：审批决策通过底部 prompt 读取。
	// 生产路径每次读取都会重新打开面板并在读取结束后关闭，因此这里先释放
	// 上面用于可视断言的 popup（priorityPromptMu 不可重入）。
	cleanup()
	if session.priorityPopupHandle.Valid() {
		t.Fatal("expected the approval panel handle to be released before the merged read")
	}
	coord.SetPromptInput("未提交草稿")
	draftBefore := coord.PromptInputSnapshot().Text

	line, merged, err := readChatRuntimeApprovalAnswer(
		session,
		approvalPriorityPromptLines(approval, []string{"permission_mode=default"}),
		promptLine,
	)
	afterRead := coord.PromptInputSnapshot().Text
	afterCleanup := coord.PromptInputSnapshot().Text
	if err != nil {
		t.Fatalf("merged approval read: %v", err)
	}
	if !merged {
		t.Fatal("expected the approval decision to be read through the bottom prompt")
	}
	if got := strings.TrimSpace(line); got != "1" {
		t.Fatalf("approval answer = %q, want %q", got, "1")
	}
	// 审批结束后把用户未提交草稿归还，审批答案不得覆盖草稿。
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "未提交草稿" {
		t.Fatalf("expected parked draft to be restored, got %#v (draftBefore=%q afterRead=%q afterCleanup=%q line=%q)",
			snapshot, draftBefore, afterRead, afterCleanup, line)
	}
	// 审批读取不得消费主循环的共享 reader。
	if next, readErr := session.InputReader.ReadString('\n'); readErr != nil || next != "stale\n" {
		t.Fatalf("expected shared reader to remain untouched, got %q err=%v", next, readErr)
	}
}

// 面板正文必须携带独立的决策提示行：popup 不再拥有输入行后，用户只能从正文
// 看到可接受的选项，提示不能随输入行一起消失。
func TestApprovalDecisionPromptStaysVisibleInPanelBody(t *testing.T) {
	body := answerPromptBodyLines(approvalDecisionPromptWithReuse(""))
	if len(body) != 1 {
		t.Fatalf("approval decision hint body lines = %#v", body)
	}
	for _, want := range []string{"[1] 仅本次允许", "[2] 拒绝", "[3] 查看完整参数"} {
		if !strings.Contains(body[0], want) {
			t.Fatalf("approval decision hint %q missing %q", body[0], want)
		}
	}

	reuseBody := answerPromptBodyLines(approvalDecisionPromptWithReuse("session_readonly_shell"))
	if len(reuseBody) != 1 || !strings.Contains(reuseBody[0], "[4]") {
		t.Fatalf("reuse approval decision hint body lines = %#v", reuseBody)
	}
}

func newBusyCaptureApprovalSession(t *testing.T) (*ChatSession, *chatInteractionCoordinator) {
	t.Helper()
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)

	session := &ChatSession{
		Surface:     surface,
		InputBox:    ui.NewInputBox(nil),
		InputReader: bufio.NewReader(strings.NewReader("stale\n")),
	}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetSurface(surface)
	coord.SetWriter(os.Stdout)
	if actor := coord.ensureUIActor(); actor != nil {
		actor.Post(ui.Resize{Width: 80, Height: 24, Generation: 1})
		coord.waitUIActorIdle()
	}
	session.InputQueue = newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	return session, coord
}

func waitForApprovalTestCondition(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func promptRowPainted(coord *chatInteractionCoordinator) bool {
	if coord == nil {
		return false
	}
	coord.mu.Lock()
	defer coord.mu.Unlock()
	return coord.promptVisible && coord.promptRenderedOnSurface
}

// 答案读完后 agent 回到执行状态：底部 prompt 行必须仍然画在屏幕上。运行期没有
// Ready 态的 PrintPrompt 再补画一次，所以释放答案时不能把行一起撤掉，否则用户在
// 整个执行状态里都看不到 "> " 输入区。
func TestMergedAnswerReleaseKeepsBottomPromptRowPainted(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session, coord := newBusyCaptureApprovalSession(t)
	session.InputQueue.setExternalInputCaptureActive(true)

	coord.SetPromptInput("")
	capture := newChatBusyComposerCapture(session, formatSessionUserPrompt(session), true, true)
	capture.beginAnswerPrompt()
	if !promptRowPainted(coord) {
		t.Fatal("expected the merged answer to own the painted bottom prompt row")
	}
	capture.onChange(ui.LineEditorSnapshot{Text: "1", Cursor: 1})

	capture.releaseAnswerPrompt()

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected the answer text to leave the prompt row, got %#v", snapshot)
	}
	if !promptRowPainted(coord) {
		t.Fatal("bottom prompt row disappeared after the answer: the running turn never repaints it")
	}
}

// 运行期（agent 正在跑）stdin 由 busy capture 独占：读取方无法自己读 stdin，合并
// 输入必须由 capture 把答案画进底部 prompt 行。回归断言：答案进入底部 prompt
// 状态、草稿在答案结束后归还。
func TestBusyCapturePaintsMergedApprovalAnswerIntoPromptRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session, coord := newBusyCaptureApprovalSession(t)
	session.InputQueue.setExternalInputCaptureActive(true)
	if !chatMergedAnswerPromptRenderable(session) {
		t.Fatal("expected a running turn with busy input capture to render the merged answer row")
	}

	coord.SetPromptInput("未提交草稿")
	capture := newChatBusyComposerCapture(session, formatSessionUserPrompt(session), true, true)
	if !capture.answerPrompt {
		t.Fatal("expected the merged answer capture to own the bottom prompt row")
	}
	if got := capture.hooks().InitialText; got != "" {
		t.Fatalf("expected the merged answer to start empty, got %q", got)
	}

	capture.beginAnswerPrompt()
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected the parked draft to leave the prompt row, got %#v", snapshot)
	}
	capture.onChange(ui.LineEditorSnapshot{Text: "1", Cursor: 1})
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "1" {
		t.Fatalf("expected the typed approval answer in the bottom prompt row, got %#v", snapshot)
	}
	capture.ClearPrompt()
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "未提交草稿" {
		t.Fatalf("expected the parked draft back after the answer, got %#v", snapshot)
	}
}

// 运行期审批读取：读取方经 queue 等行，面板必须保持 body-only（不占用 popup 输入
// 行），并且要把合并标记传给 busy capture。
func TestMergedApprovalReadDuringRunningTurnSkipsPopupInputRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session, _ := newBusyCaptureApprovalSession(t)
	queue := session.InputQueue
	queue.setExternalInputCaptureActive(true)

	approval := &runtimechat.ApprovalRequest{
		ID:         "appr_shell",
		ToolCallID: "call_shell",
		ToolName:   "shell",
		ArgsJSON:   []byte(`{"command":"New-Item -Path .\\approval_probe.tmp -ItemType File -Force"}`),
		Reason:     "permission_mode_requires_approval",
		RiskLevel:  "high",
	}
	lines := approvalPriorityPromptLines(approval, []string{"permission_mode=default"})
	promptLine := approvalDecisionPromptWithReuse("")

	type outcome struct {
		text   string
		merged bool
		err    error
	}
	outcomes := make(chan outcome, 1)
	go func() {
		text, merged, err := readChatRuntimeApprovalAnswer(session, lines, promptLine)
		outcomes <- outcome{text: text, merged: merged, err: err}
	}()

	waitForApprovalTestCondition(t, 5*time.Second, "merged priority capture", func() bool {
		return queue.isPriorityMode() && queue.priorityAnswerMergedPrompt()
	})
	// 面板在读取期间由 priorityPromptMu 独占，测试不能在此处取同一把锁。
	queue.routeInputText("1\n")

	select {
	case got := <-outcomes:
		if got.err != nil {
			t.Fatalf("merged approval read: %v", got.err)
		}
		if !got.merged {
			t.Fatal("expected the approval answer to use the merged bottom prompt row")
		}
		if strings.TrimSpace(got.text) != "1" {
			t.Fatalf("approval answer = %q, want %q", got.text, "1")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the approval decision")
	}
	if session.priorityPopupHandle.Valid() {
		t.Fatal("approval panel must not claim the popup input (ComposerLine) row during a running turn")
	}
}
