package commands

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestChatRuntimeEventBridge_RequiredQuestionConsoleFailureStaysPending 钉住
// 2026-10-17 真机（QQ 绑定会话）的交互 bug：
//
// question_asked 15:47:49.12242 与 question_answered 15:47:49.19702（answer=""）
// 相隔 75ms —— 用户从未作答。旧桥接层在控制台读取失败时用空串 resolve，
// agent 带着 answer="" 继续，表现为"问题被自动放行"。
//
// 契约：空串不是回答。必答问题在控制台读取失败时必须保持挂起，等待真实入口
// （控制台恢复态作答 / Web pending_question / IM `/answer`）送达回答。
func TestChatRuntimeEventBridge_RequiredQuestionConsoleFailureStaysPending(t *testing.T) {
	hub := buildTestSessionHub(t)
	defer hub.StopAll()
	const sessionID = "session-1"
	session := &ChatSession{
		Stream:           true,
		RuntimeSession:   &runtimechat.Session{ID: sessionID},
		LocalRuntimeHost: &localChatRuntimeHost{SessionHub: hub},
	}
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	t.Cleanup(session.Interaction.Shutdown)
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(string) {}
	askCalls := 0
	bridge.askQuestion = func(string, []string, bool) (string, error) {
		askCalls++
		return "", fmt.Errorf("console reader failed")
	}

	actor, err := hub.GetOrCreate(sessionID)
	require.NoError(t, err)

	// 真实地挂起一个必答问题（注册 waiter，等价于 run 内 ask_user_question 阻塞）。
	answerCh := make(chan string, 1)
	go func() {
		answer, _ := actor.AskUserQuestion(context.Background(), toolbroker.UserQuestionRequest{
			ID:       "q-required-no-answer",
			Prompt:   "必须回答的问题",
			Required: true,
		})
		answerCh <- answer
	}()
	require.Eventually(t, func() bool {
		state := actor.State()
		return state != nil && state.PendingQuestion != nil && state.PendingQuestion.ID == "q-required-no-answer"
	}, 5*time.Second, 10*time.Millisecond, "question was not parked")

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: sessionID,
		Payload:   map[string]interface{}{"turn_id": "turn-required-question"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventQuestionAsked,
		SessionID: sessionID,
		Payload: map[string]interface{}{
			"question_id": "q-required-no-answer",
			"prompt":      "必须回答的问题",
			"required":    true,
		},
	})
	require.Equal(t, 1, askCalls, "the console ask path must have been attempted")

	// 控制台读取失败不得伪造空答案：工具继续等待真实回答。
	select {
	case answer := <-answerCh:
		t.Fatalf("required question must not be auto-answered, got %q", answer)
	case <-time.After(200 * time.Millisecond):
	}
	state := actor.State()
	require.NotNil(t, state)
	require.NotNil(t, state.PendingQuestion, "required question must stay pending after a console read failure")
	require.Equal(t, "q-required-no-answer", state.PendingQuestion.ID)
	require.Equal(t, runtimechat.SessionWaitingInput, state.Status)

	// 其它入口（Web / IM `/answer` / 恢复态控制台）仍能送达真实回答。
	require.NoError(t, actor.AnswerQuestion(context.Background(), "q-required-no-answer", "答案：A"))
	select {
	case answer := <-answerCh:
		require.Equal(t, "答案：A", answer)
	case <-time.After(5 * time.Second):
		t.Fatal("real answer was not delivered to the parked question")
	}
}
