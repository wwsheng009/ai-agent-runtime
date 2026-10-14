package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSendMessage_AutoContinuationUnlimitedByDefault(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	// 默认 0 = 不限制（对齐 Codex 的 continue_if_idle）：由模型在若干轮后完成
	// goal 才停止，而不是被固定次数截断。
	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			fake := session.ChatExecutor.(*fakeChatExecutor)
			if fake.continuations < 8 {
				return "working", nil
			}
			if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
				"status":  string(runtimegoal.StatusComplete),
				"summary": "completed after eight unlimited continuations",
			}); err != nil {
				return "", err
			}
			return "done", nil
		},
	}
	session.ChatExecutor = executor

	response, err := sendMessage(session, "start")
	if err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if response != "initial response" {
		t.Fatalf("unexpected response: %q", response)
	}
	if len(executor.prompts) != 1 || executor.prompts[0] != "start" {
		t.Fatalf("expected only the initial prompt as user prompt, got %#v", executor.prompts)
	}
	if executor.continuations != 8 {
		t.Fatalf("expected 8 hidden continuations until completion, got %d", executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusComplete)
}

func TestSendMessage_AutoContinuationRespectsConfiguredLimit(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalMaxContinuations = 8

	executor := &fakeChatExecutor{output: "initial response"}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if executor.continuations != 8 {
		t.Fatalf("expected configured safety cap of 8 continuations, got %d", executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusActive)
}

func TestSendMessage_AutoContinuationStopsAfterModelMarksGoalComplete(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
				"status":  string(runtimegoal.StatusComplete),
				"summary": "all requested work is complete",
			}); err != nil {
				return "", err
			}
			return "ok", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if executor.continuations != 1 {
		t.Fatalf("expected continuation to run once, got %d", executor.continuations)
	}
	goal, ok, err := runtimegoal.NewMetadataStore().Get(session.RuntimeSession)
	if err != nil || !ok || goal == nil {
		t.Fatalf("expected goal metadata, ok=%v err=%v", ok, err)
	}
	if goal.Status != runtimegoal.StatusComplete || goal.CompletedBy != "model" {
		t.Fatalf("expected model-completed goal, got %#v", goal)
	}
}

func TestSendMessage_AutoContinuesActiveGoalAfterInitialError(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		err: context.DeadlineExceeded,
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
				"status":  string(runtimegoal.StatusComplete),
				"summary": "goal completed after retry",
			}); err != nil {
				return "", err
			}
			return "ok", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("expected active goal continuation to recover initial error, got %v", err)
	}
	if executor.continuations != 1 {
		t.Fatalf("expected one continuation after initial error, got %d", executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusComplete)
}

func TestSendMessage_AutoContinuationRetriesErrorsUntilGoalComplete(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			if ctx == nil {
				t.Fatal("expected continuation context")
			}
			if session.ChatExecutor.(*fakeChatExecutor).continuations < 3 {
				return "", errors.New("transient continuation failure")
			}
			if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
				"status":  string(runtimegoal.StatusComplete),
				"summary": "goal completed after retrying continuation errors",
			}); err != nil {
				return "", err
			}
			return "ok", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if executor.continuations != 3 {
		t.Fatalf("expected continuation to retry until third attempt, got %d", executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusComplete)
}

func TestSendMessage_DoesNotAutoContinueInactiveGoal(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusPaused)
	defer cleanup()

	executor := &fakeChatExecutor{output: "initial response"}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if len(executor.prompts) != 1 {
		t.Fatalf("expected no continuation for paused goal, got %#v", executor.prompts)
	}
	if executor.continued {
		t.Fatal("expected no hidden continuation for paused goal")
	}
}

func TestSendMessage_MissingGoalDoesNotMaskInitialTurnError(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	updated, err := runtimegoal.NewMetadataStore().ClearPersistent(context.Background(), session.SessionManager.GetStorage(), session.RuntimeSession.ID)
	if err != nil {
		t.Fatalf("ClearPersistent: %v", err)
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		t.Fatalf("restoreChatStateFromRuntimeSession: %v", err)
	}

	executor := &fakeChatExecutor{err: context.DeadlineExceeded}
	session.ChatExecutor = executor
	_, err = sendMessage(session, "start")
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("expected original turn error to remain visible, got %v", err)
	}
	if executor.continuations != 0 {
		t.Fatalf("expected missing goal to suppress continuation, got %d", executor.continuations)
	}
}

func TestSendMessage_DoesNotAutoContinueWhenInputQueued(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.InputQueue = newChatInputQueue(nil)
	session.InputQueue.routeLine(chatQueuedInput{Text: "new user input"})

	executor := &fakeChatExecutor{output: "initial response"}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if len(executor.prompts) != 1 {
		t.Fatalf("expected queued input to suppress continuation, got %#v", executor.prompts)
	}
	if executor.continued {
		t.Fatal("expected queued input to suppress hidden continuation")
	}
}

func TestSendMessage_DoesNotAutoContinueNonInteractive(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.NoInteractive = true

	executor := &fakeChatExecutor{output: "initial response"}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if executor.continued {
		t.Fatal("expected non-interactive mode to suppress hidden continuation")
	}
}

func TestShouldAutoContinueActiveGoalAllowsActorWhenGoalToolAvailable(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.ChatExecutor = &aicliActorChatExecutor{}
	session.LocalRuntimeHost = &localChatRuntimeHost{
		ToolSurface: wrapGoalToolSurface(session, nil),
	}

	shouldContinue, err := shouldAutoContinueActiveGoal(session)
	if err != nil {
		t.Fatalf("shouldAutoContinueActiveGoal failed: %v", err)
	}
	if !shouldContinue {
		t.Fatal("expected actor-first session to auto-continue when update_goal is exposed")
	}
}

func TestSendMessage_AutoContinuationErrorDoesNotFailInitialResponse(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			return "", context.Canceled
		},
	}
	session.ChatExecutor = executor

	response, err := sendMessage(session, "start")
	if err != nil {
		t.Fatalf("expected initial response to remain successful, got error: %v", err)
	}
	if response != "initial response" {
		t.Fatalf("unexpected response: %q", response)
	}
	// 真实取消（context.Canceled）立即停止续跑，不计入错误容忍。
	if executor.continuations != 1 {
		t.Fatalf("expected canceled continuation to stop after one attempt, got %d", executor.continuations)
	}
}

func TestSendMessage_AutoContinuationStopsAfterThreeConsecutiveErrors(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			return "", errors.New("persistent continuation failure")
		},
	}
	session.ChatExecutor = executor

	response, err := sendMessage(session, "start")
	if err != nil {
		t.Fatalf("expected initial response to remain successful, got error: %v", err)
	}
	if response != "initial response" {
		t.Fatalf("unexpected response: %q", response)
	}
	if executor.continuations != goalAutoContinuationErrorStrikeLimit {
		t.Fatalf("expected continuation to stop at %d consecutive errors, got %d",
			goalAutoContinuationErrorStrikeLimit, executor.continuations)
	}
	// 对齐 Codex：连续失败停止并把目标置为 blocked（/goal resume 解除）。
	assertGoalStatus(t, session, runtimegoal.StatusBlocked)
}

func TestSendMessage_AutoContinuationBlocksAfterThreeNoProgressRounds(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			return "", nil
		},
	}
	session.ChatExecutor = executor

	response, err := sendMessage(session, "start")
	if err != nil {
		t.Fatalf("expected initial response to remain successful, got error: %v", err)
	}
	if response != "initial response" {
		t.Fatalf("unexpected response: %q", response)
	}
	if executor.continuations != goalAutoContinuationNoProgressStrikeLimit {
		t.Fatalf("expected continuation to stop at %d no-progress rounds, got %d",
			goalAutoContinuationNoProgressStrikeLimit, executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusBlocked)
}

func TestSendMessage_NoProgressStrikesResetOnToolActivity(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalMaxContinuations = 5

	executor := &fakeChatExecutor{
		output: "initial response",
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			fake := session.ChatExecutor.(*fakeChatExecutor)
			if fake.continuations == 3 {
				// 第 3 轮有工具执行（计数增长）→ 空输出也不算空转，计数清零。
				recordChatTurnToolExecution(session)
			}
			return "", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	// 轮次：1/2 空转各记 1 strike；3 有工具执行清零；4/5 再各记 1；到 cap=5 停下。
	if executor.continuations != 5 {
		t.Fatalf("expected tool activity to reset no-progress strikes (cap 5), got %d", executor.continuations)
	}
	assertGoalStatus(t, session, runtimegoal.StatusActive)
}

func TestSendMessage_AccountsGoalUsageAndStopsAtBudget(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	// 预算 250：初始 turn 计 100，续跑每轮计 100 → 第 2 轮后 300 触发收尾。
	goal, ok, err := runtimegoal.NewMetadataStore().Get(session.RuntimeSession)
	if err != nil || !ok || goal == nil {
		t.Fatalf("expected goal metadata, ok=%v err=%v", ok, err)
	}
	goal.TokenBudget = 250
	if err := runtimegoal.NewMetadataStore().Put(session.RuntimeSession, *goal); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	resetGoalAccountingBaseline(session)

	executor := &fakeChatExecutor{
		output: "initial response",
		onCall: func(ctx context.Context, session *ChatSession, prompt string) (string, error) {
			session.TokenCount += 100
			return "initial response", nil
		},
		onContinue: func(ctx context.Context, session *ChatSession) (string, error) {
			session.TokenCount += 100
			return "working", nil
		},
	}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "start"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}

	updated, ok, err := runtimegoal.NewMetadataStore().Get(session.RuntimeSession)
	if err != nil || !ok || updated == nil {
		t.Fatalf("expected updated goal, ok=%v err=%v", ok, err)
	}
	if updated.Status != runtimegoal.StatusBudgetLimited {
		t.Fatalf("expected budget_limited, got %s (used=%d budget=%d)", updated.Status, updated.TokensUsed, updated.TokenBudget)
	}
	if updated.TokensUsed < 250 {
		t.Fatalf("expected accounted usage >= budget, got %d", updated.TokensUsed)
	}
	if executor.lastContinuationPrompt != goalBudgetWrapUpPrompt {
		t.Fatalf("expected the wrap-up prompt as the last continuation, got %q", executor.lastContinuationPrompt)
	}
	// 2 轮正常续跑 + 1 轮预算收尾。
	if executor.continuations != 3 {
		t.Fatalf("expected 3 continuation calls (2 + wrap-up), got %d", executor.continuations)
	}
}

func TestGoalAutoContinuationLimitEnvFallback(t *testing.T) {
	t.Setenv(goalMaxContinuationsEnv, "2")
	if got := goalAutoContinuationLimit(nil); got != 2 {
		t.Fatalf("expected env fallback 2, got %d", got)
	}
	if got := goalAutoContinuationLimit(&ChatSession{}); got != 2 {
		t.Fatalf("expected env fallback for unset session, got %d", got)
	}
	if got := goalAutoContinuationLimit(&ChatSession{goalMaxContinuations: 5}); got != 5 {
		t.Fatalf("explicit session config must win over env, got %d", got)
	}
	t.Setenv(goalMaxContinuationsEnv, "-3")
	if got := goalAutoContinuationLimit(&ChatSession{}); got != 0 {
		t.Fatalf("invalid env must fall back to unlimited, got %d", got)
	}
}

func TestRequestAndConsumeGoalKickRunsContinuation(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalMaxContinuations = 2

	executor := &fakeChatExecutor{output: "kick response"}
	session.ChatExecutor = executor

	if !requestGoalContinuationKick(session) {
		t.Fatal("expected kick request to be accepted for interactive active goal")
	}
	if !session.goalKickPending.Load() {
		t.Fatal("expected kick flag to be set")
	}
	maybeConsumeDeferredGoalKick(session)
	if session.goalKickPending.Load() {
		t.Fatal("expected kick flag to be consumed")
	}
	if executor.continuations != 2 {
		t.Fatalf("expected kick to run the continuation loop (2 capped rounds), got %d", executor.continuations)
	}
}

func TestRequestGoalKickRejectedForPausedOrNonInteractive(t *testing.T) {
	paused, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusPaused)
	defer cleanup()
	if requestGoalContinuationKick(paused) {
		t.Fatal("paused goal must not accept a kick")
	}

	active, cleanupActive := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanupActive()
	active.NoInteractive = true
	if requestGoalContinuationKick(active) {
		t.Fatal("non-interactive session must not accept a kick")
	}
}

func TestAICLISharedChatExecutor_GoalContinuationDoesNotPersistInstructionPrompt(t *testing.T) {
	originalExecute := executeToolLoop
	originalCompact := autoCompactSharedChatHistory
	defer func() {
		executeToolLoop = originalExecute
		autoCompactSharedChatHistory = originalCompact
	}()

	autoCompactSharedChatHistory = func(ctx context.Context, session *ChatSession, history []types.Message) ([]types.Message, *sharedChatAutoCompactReport, error) {
		t.Fatal("goal continuation should not run pre-turn auto compaction")
		return history, nil, nil
	}

	var captured runtimechatcore.ToolLoopRequest
	executeToolLoop = func(ctx context.Context, req runtimechatcore.ToolLoopRequest) (*runtimechatcore.ToolLoopResult, error) {
		captured = req
		return &runtimechatcore.ToolLoopResult{
			Response: &runtimechatcore.ChatResult{Output: "audit done"},
			History: append(append([]types.Message(nil), req.History...),
				*types.NewAssistantMessage("audit done"),
			),
		}, nil
	}

	session := &ChatSession{
		Messages: []types.Message{
			*types.NewSystemMessage("system"),
			*types.NewUserMessage("start"),
			*types.NewAssistantMessage("initial response"),
		},
	}
	output, err := (&aicliSharedChatExecutor{}).ContinueGoal(context.Background(), session)
	if err != nil {
		t.Fatalf("ContinueGoal failed: %v", err)
	}
	if output != "audit done" {
		t.Fatalf("unexpected output: %q", output)
	}
	if captured.Prompt != "" {
		t.Fatalf("expected empty visible prompt for hidden continuation, got %q", captured.Prompt)
	}
	if len(captured.History) == 0 || !strings.Contains(captured.History[len(captured.History)-1].Content, "Continue working toward") {
		t.Fatalf("expected provider history to include hidden continuation instruction, got %#v", captured.History)
	}
	for _, message := range session.Messages {
		if strings.Contains(message.Content, "Continue working toward") {
			t.Fatalf("did not expect continuation instruction to persist in session history: %#v", session.Messages)
		}
		if message.Metadata != nil {
			if value, ok := message.Metadata.Get(goalContinuationMetadataKey); ok {
				t.Fatalf("did not expect hidden continuation metadata to persist, got %v in %#v", value, session.Messages)
			}
		}
	}
	if got := session.Messages[len(session.Messages)-1].Content; got != "audit done" {
		t.Fatalf("expected continuation assistant response to persist, got %q", got)
	}
}

func newGoalAutoContinueTestSession(t *testing.T, status runtimegoal.Status) (*ChatSession, func()) {
	t.Helper()
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), nil)
	runtimeSession, err := manager.Create(context.Background(), "test-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	goal, err := runtimegoal.NewSessionGoal(runtimeSession.ID, "finish the requested implementation", runtimeSession.CreatedAt)
	if err != nil {
		t.Fatalf("NewSessionGoal: %v", err)
	}
	goal.Status = status
	if err := runtimegoal.NewMetadataStore().Put(runtimeSession, goal); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	if err := manager.Update(context.Background(), runtimeSession); err != nil {
		t.Fatalf("manager.Update: %v", err)
	}
	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  "test-user",
		cancelCtx:      context.Background(),
	}
	registerGoalFunctions(session)
	return session, manager.Stop
}
