package commands

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

const restoredPendingTestSessionID = "session-restored-pending"

// newRestoredPendingHarness 搭出与生产同形的“恢复态”现场：已恢复的 runtime
// session + durable RuntimeState + 固定底部 surface + UI actor。事件日志走
// bridge.eventLogPathOverride（缺省不存在），因此不会误触重放。
func newRestoredPendingHarness(t *testing.T, state *runtimechat.RuntimeState) (*ChatSession, *chatRuntimeEventBridge, *chatInteractionCoordinator) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 120, 30
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = restoredPendingTestSessionID
	session := &ChatSession{
		Surface:        surface,
		InputBox:       ui.NewInputBox(nil),
		RuntimeSession: runtimeSession,
		SessionUserID:  "tester",
	}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	coord.SetSurface(surface)
	coord.SetWriter(os.Stdout)
	if actor := coord.ensureUIActor(); actor != nil {
		actor.Post(ui.Resize{Width: width, Height: height, Generation: 1})
		coord.waitUIActorIdle()
	}

	store := runtimechat.NewInMemoryRuntimeStore(8)
	if state != nil {
		state.SessionID = restoredPendingTestSessionID
		if state.UpdatedAt.IsZero() {
			state.UpdatedAt = time.Now().UTC()
		}
		if err := store.SaveState(context.Background(), state); err != nil {
			t.Fatalf("SaveState: %v", err)
		}
	}
	session.LocalRuntimeHost = &localChatRuntimeHost{RuntimeStore: store}

	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(string) {}
	session.RuntimeEventBridge = bridge
	return session, bridge, coord
}

// restoredPendingPopupText 汇总底部 popup 行（问题/审批卡片正文）。
func restoredPendingPopupText(state ui.AppState) string {
	frame := ui.ComposeAppTextLayout(state)
	var builder strings.Builder
	for _, row := range frame.Rows {
		if row.Owner == renderengine.RowOwnerPopup {
			builder.WriteString(row.Text)
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func TestEnsureRestoredPendingInteractivePromptProjectsQuestionCard(t *testing.T) {
	session, _, coord := newRestoredPendingHarness(t, &runtimechat.RuntimeState{
		Status:        runtimechat.SessionWaitingInput,
		CurrentTurnID: "turn-restored",
		PendingQuestion: &runtimechat.UserQuestionRequest{
			ID:          "q-restored",
			SessionID:   restoredPendingTestSessionID,
			Prompt:      "继续吗？",
			Suggestions: []string{"继续", "停止"},
			Required:    true,
		},
	})

	if !ensureRestoredPendingInteractivePrompt(session) {
		t.Fatal("expected the restored pending question to be projected onto the input face")
	}
	pending := currentRestoredPendingPrompt(session)
	if pending == nil || pending.kind != chatRestoredPendingQuestion || pending.questionID != "q-restored" {
		t.Fatalf("unexpected restored pending prompt: %#v", pending)
	}
	if got := session.Interaction.InputMode(); got != chatInputModeAnswer {
		t.Fatalf("input mode = %q, want %q", got, chatInputModeAnswer)
	}

	coord.waitUIActorIdle()
	card := restoredPendingPopupText(coord.uiActor.AppState())
	if !strings.Contains(card, "[提问]") || !strings.Contains(card, "继续吗") {
		t.Fatalf("question card was not painted: %q", card)
	}
	if !strings.Contains(card, "必答") {
		t.Fatalf("question card lost its required-answer hint: %q", card)
	}
}

func TestEnsureRestoredPendingInteractivePromptIsIdempotent(t *testing.T) {
	session, _, _ := newRestoredPendingHarness(t, &runtimechat.RuntimeState{
		Status: runtimechat.SessionWaitingInput,
		PendingQuestion: &runtimechat.UserQuestionRequest{
			ID: "q-once", SessionID: restoredPendingTestSessionID, Prompt: "唯一吗？", Required: false,
		},
	})

	first := currentRestoredPendingPromptAfterEnsure(t, session)
	second := currentRestoredPendingPromptAfterEnsure(t, session)
	if first != second {
		t.Fatal("repeated ensure must keep the same projection (no duplicate cards)")
	}
}

func currentRestoredPendingPromptAfterEnsure(t *testing.T, session *ChatSession) *chatRestoredPendingPrompt {
	t.Helper()
	ensureRestoredPendingInteractivePrompt(session)
	pending := currentRestoredPendingPrompt(session)
	if pending == nil {
		t.Fatal("expected a registered restored pending prompt")
	}
	return pending
}

func TestRestoredPendingQuestionAnswerRoutesToActorAndClears(t *testing.T) {
	session, bridge, _ := newRestoredPendingHarness(t, &runtimechat.RuntimeState{
		Status: runtimechat.SessionWaitingInput,
		PendingQuestion: &runtimechat.UserQuestionRequest{
			ID:          "q-route",
			SessionID:   restoredPendingTestSessionID,
			Prompt:      "选择哪个？",
			Suggestions: []string{"甲方案", "乙方案"},
			Required:    true,
		},
	})

	type answerCall struct{ sessionID, questionID, answer string }
	calls := make(chan answerCall, 1)
	bridge.answerQuestion = func(_ context.Context, sessionID, questionID, answer string) error {
		calls <- answerCall{sessionID: sessionID, questionID: questionID, answer: answer}
		return nil
	}

	if !ensureRestoredPendingInteractivePrompt(session) {
		t.Fatal("expected the restored pending question to be projected")
	}
	if !handleRestoredPendingAnswerLine(session, "2") {
		t.Fatal("the answer line must be consumed by the restored pending question")
	}
	select {
	case call := <-calls:
		if call.sessionID != restoredPendingTestSessionID || call.questionID != "q-route" || call.answer != "乙方案" {
			t.Fatalf("unexpected answer routing: %#v", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answer was not routed to the actor")
	}
	if currentRestoredPendingPrompt(session) != nil {
		t.Fatal("the projection must be cleared once answered")
	}
	if got := session.Interaction.InputMode(); got != chatInputModeChat {
		t.Fatalf("input mode = %q, want restored to chat", got)
	}
}

func TestRestoredPendingRequiredQuestionRejectsEmptyAnswer(t *testing.T) {
	session, bridge, _ := newRestoredPendingHarness(t, &runtimechat.RuntimeState{
		Status: runtimechat.SessionWaitingInput,
		PendingQuestion: &runtimechat.UserQuestionRequest{
			ID: "q-required", SessionID: restoredPendingTestSessionID, Prompt: "必答吗？", Required: true,
		},
	})
	called := false
	bridge.answerQuestion = func(context.Context, string, string, string) error {
		called = true
		return nil
	}
	if !ensureRestoredPendingInteractivePrompt(session) {
		t.Fatal("expected the restored pending question to be projected")
	}
	if !handleRestoredPendingAnswerLine(session, "   ") {
		t.Fatal("an empty answer to a required question must stay in the answer mode")
	}
	if called {
		t.Fatal("an empty answer must not be routed to the actor")
	}
	if currentRestoredPendingPrompt(session) == nil {
		t.Fatal("the projection must stay while the required answer is missing")
	}
}

// TestReplayEventLogDoesNotProjectRestoredPendingPrompt 锁定重放的零交互副作用：
// 事件日志里的 question_asked 只重建数据面，绝不弹卡片（否则每次 /history 重放
// 都会重新进入作答态）。
func TestReplayEventLogDoesNotProjectRestoredPendingPrompt(t *testing.T) {
	session, bridge, _ := newRestoredPendingHarness(t, nil)
	logPath := writeTrimReplayLog(t, []runtimeevents.Event{
		{
			Type:      runtimechat.EventQuestionAsked,
			SessionID: restoredPendingTestSessionID,
			Payload: map[string]interface{}{
				"question_id": "q-replayed",
				"prompt":      "重放的问题？",
			},
		},
	})
	bridge.eventLogPathOverride = logPath

	prompted := 0
	bridge.askQuestion = func(string, []string, bool) (string, error) {
		prompted++
		return "", nil
	}
	if _, err := bridge.replayEventLog(); err != nil {
		t.Fatalf("replayEventLog: %v", err)
	}
	if prompted != 0 {
		t.Fatalf("replay must not prompt the console, got %d prompt(s)", prompted)
	}
	if currentRestoredPendingPrompt(session) != nil {
		t.Fatal("replay must not register a restored pending prompt")
	}
}

func TestRestoredPendingApprovalRoutesDecisionAndClears(t *testing.T) {
	session, bridge, _ := newRestoredPendingHarness(t, &runtimechat.RuntimeState{
		Status: runtimechat.SessionWaitingApproval,
		PendingApproval: &runtimechat.ApprovalRequest{
			ID:        "apr-restored",
			SessionID: restoredPendingTestSessionID,
			ToolName:  "shell",
			Reason:    "需要执行命令",
		},
	})

	type approvalCall struct {
		requestID string
		allow     bool
	}
	calls := make(chan approvalCall, 1)
	bridge.approveTool = func(_ context.Context, _, requestID string, allow bool) error {
		calls <- approvalCall{requestID: requestID, allow: allow}
		return nil
	}

	if !ensureRestoredPendingInteractivePrompt(session) {
		t.Fatal("expected the restored pending approval to be projected")
	}
	if got := session.Interaction.InputMode(); got != chatInputModeApproval {
		t.Fatalf("input mode = %q, want %q", got, chatInputModeApproval)
	}
	if !handleRestoredPendingAnswerLine(session, "1") {
		t.Fatal("the approval decision must be consumed by the restored pending approval")
	}
	select {
	case call := <-calls:
		if call.requestID != "apr-restored" || !call.allow {
			t.Fatalf("unexpected approval routing: %#v", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approval decision was not routed to the actor")
	}
	if currentRestoredPendingPrompt(session) != nil {
		t.Fatal("the approval projection must be cleared once resolved")
	}
}

// TestRestoredPendingApprovalAutoResolvesUnderYolo 与 ACP 恢复审批同口径：
// yolo 下审批本不该出现，直接放行而不是重新问一遍。
func TestRestoredPendingApprovalAutoResolvesUnderYolo(t *testing.T) {
	session, bridge, _ := newRestoredPendingHarness(t, &runtimechat.RuntimeState{
		Status: runtimechat.SessionWaitingApproval,
		PendingApproval: &runtimechat.ApprovalRequest{
			ID: "apr-yolo", SessionID: restoredPendingTestSessionID, ToolName: "shell",
		},
	})
	session.PermissionMode = runtimepolicy.ModeBypassPermissions

	allowed := make(chan bool, 1)
	bridge.approveTool = func(_ context.Context, _, _ string, allow bool) error {
		allowed <- allow
		return nil
	}

	if ensureRestoredPendingInteractivePrompt(session) {
		t.Fatal("a yolo session must not re-prompt for a restored approval")
	}
	select {
	case allow := <-allowed:
		if !allow {
			t.Fatal("yolo must auto-allow the restored approval")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the auto-allowed approval was not resolved")
	}
	if currentRestoredPendingPrompt(session) != nil {
		t.Fatal("no projection may be registered for an auto-resolved approval")
	}
}
