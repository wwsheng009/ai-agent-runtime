package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestChatRuntimeEventBridge_PendingPredicatesTreatClearedStateAsResolved pins
// the 2026-10-12 live bug (micro web client answer + stuck TUI):
//
// The web client answered a pending ask_user_question (the actor cleared
// PendingQuestion and the model continued), but the console reader stayed
// parked. questionStillPending/approvalStillPending treated a nil pending slot
// as "still pending"; the sentinel retry loop therefore considered the wake
// signal stale, re-entered the blocking priority read, and hung the TUI
// forever while no further resolve signal could arrive.
//
// Contracts pinned here:
//   - no pending item in the actor ⇒ false (previously true, the inversion);
//   - a different id ⇒ false;
//   - the actually parked question ⇒ true until the answer clears it.
func TestChatRuntimeEventBridge_PendingPredicatesTreatClearedStateAsResolved(t *testing.T) {
	hub := buildTestSessionHub(t)
	defer hub.StopAll()
	session := &ChatSession{LocalRuntimeHost: &localChatRuntimeHost{SessionHub: hub}}
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(string) {}

	// buildTestSessionHub 的会话存储中登记的是 session-1：actor 的
	// recordPendingToolCall 会按 actor id 回读会话并回写挂起工具调用。
	const sessionID = "session-1"
	actor, err := hub.GetOrCreate(sessionID)
	require.NoError(t, err)

	// 空挂起槽位不是 pending：旧实现把 nil 判成 true，使 console 在答案已被
	// 其它入口（web client）解决后继续询问/重新挂起读取。
	require.False(t, bridge.questionStillPending(sessionID, "q-none"),
		"a nil pending question must not read as pending")
	require.False(t, bridge.approvalStillPending(sessionID, "r-none"),
		"a nil pending approval must not read as pending")

	askResult := make(chan error, 1)
	go func() {
		_, askErr := actor.AskUserQuestion(context.Background(), toolbroker.UserQuestionRequest{
			ID:         "q-pending-predicate",
			ToolCallID: "call-pending-predicate",
			Prompt:     "继续吗？",
			Required:   true,
		})
		askResult <- askErr
	}()

	parked := false
	deadline := time.After(5 * time.Second)
	for !parked {
		select {
		case askErr := <-askResult:
			t.Fatalf("AskUserQuestion returned before parking: %v", askErr)
		case <-deadline:
			t.Fatal("question was not parked in the actor")
		case <-time.After(10 * time.Millisecond):
			state := actor.State()
			parked = state != nil && state.PendingQuestion != nil && state.PendingQuestion.ID == "q-pending-predicate"
		}
	}

	require.True(t, bridge.questionStillPending(sessionID, "q-pending-predicate"),
		"a parked question must read as pending")
	require.False(t, bridge.questionStillPending(sessionID, "q-other"),
		"another question id must not read as this pending question")

	require.NoError(t, actor.AnswerQuestion(context.Background(), "q-pending-predicate", "继续"))
	select {
	case askErr := <-askResult:
		require.NoError(t, askErr, "AskUserQuestion must return nil after the answer")
	case <-time.After(5 * time.Second):
		t.Fatal("AskUserQuestion did not return after the answer")
	}
	require.Eventually(t, func() bool {
		state := actor.State()
		return state != nil && state.PendingQuestion == nil
	}, 5*time.Second, 10*time.Millisecond, "pending question was not cleared")

	// 关键回归断言：答案已送达并清空后必须判 false；旧实现返回 true，使哨兵
	// 重试循环重新进入阻塞读取（TUI 永久堵塞）。
	require.False(t, bridge.questionStillPending(sessionID, "q-pending-predicate"),
		"a cleared question must not read as pending")
}
