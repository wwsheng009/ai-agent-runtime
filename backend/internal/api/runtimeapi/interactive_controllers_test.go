package runtimeapi

// 交互通道控制器单测（§21）：ask_user_question 挂起/唤醒、plan_enter/exit 的
// durable 会话状态落库与权限模式元数据。真实 turn 的模型选择不可强制，
// 这里锁住控制器的同步语义（与 SessionActor 同名实现逐项对齐）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func newInteractiveTestHandler(t *testing.T) (*Handler, *chat.Session) {
	t.Helper()
	handler := NewHandler(nil, nil, nil)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(context.Background(), "interactive-user")
	require.NoError(t, err)
	session.SetContext(sessionmeta.WorkspacePath, t.TempDir())
	require.NoError(t, sessionManager.Update(context.Background(), session))
	return handler, session
}

func TestAskUserQuestionRegistersPendingAndWakesOnAnswer(t *testing.T) {
	handler, session := newInteractiveTestHandler(t)
	controllers := handler.interactiveControllers()
	require.NotNil(t, controllers)

	// 异步发起提问（会挂起等待）。
	type askResult struct {
		answer string
		err    error
	}
	done := make(chan askResult, 1)
	go func() {
		answer, err := controllers.AskUserQuestion(context.Background(), toolbroker.UserQuestionRequest{
			ID:        "question_test_1",
			SessionID: session.ID,
			Prompt:    "which db?",
			Required:  true,
		})
		done <- askResult{answer: answer, err: err}
	}()

	// 挂起可见（/runtime 快照语义：durable state + 会话上下文）。
	require.Eventually(t, func() bool {
		state := handler.inlineRuntimeState(session.ID)
		return state != nil && state.PendingQuestion != nil && state.PendingQuestion.ID == "question_test_1"
	}, 3*time.Second, 50*time.Millisecond, "pending question must be observable via runtime state")

	// 用户回答（answer_question 的 inline 分支）。
	require.True(t, handler.AnswerInlineQuestion("question_test_1", "postgres"))

	result := <-done
	require.NoError(t, result.err)
	require.Equal(t, "postgres", result.answer)

	// 唤醒后挂起状态清除。
	require.False(t, handler.AnswerInlineQuestion("question_test_1", "duplicate"),
		"waiter must be unregistered after the first answer")
	require.Eventually(t, func() bool {
		state := handler.inlineRuntimeState(session.ID)
		return state != nil && state.PendingQuestion == nil
	}, 3*time.Second, 50*time.Millisecond, "pending question must be cleared after resume")
}

func TestAskUserQuestionCtxCancelClearsPending(t *testing.T) {
	handler, session := newInteractiveTestHandler(t)
	controllers := handler.interactiveControllers()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = controllers.AskUserQuestion(ctx, toolbroker.UserQuestionRequest{
			ID:        "question_cancel",
			SessionID: session.ID,
			Prompt:    "any?",
		})
	}()
	require.Eventually(t, func() bool {
		state := handler.inlineRuntimeState(session.ID)
		return state != nil && state.PendingQuestion != nil
	}, 3*time.Second, 50*time.Millisecond)
	cancel()
	require.Eventually(t, func() bool {
		state := handler.inlineRuntimeState(session.ID)
		return state != nil && state.PendingQuestion == nil
	}, 3*time.Second, 50*time.Millisecond, "ctx cancel must clear the pending question")
}

func TestPlanEnterExitRoundTripOnDurableSession(t *testing.T) {
	handler, session := newInteractiveTestHandler(t)
	controllers := handler.interactiveControllers()

	entered, err := controllers.EnterPlanMode(context.Background(), session.ID, toolbroker.EnterPlanModeArgs{
		PlanPath: "plan.md",
	})
	require.NoError(t, err)
	require.True(t, entered.Active)
	require.Equal(t, string(runtimepolicyModePlan()), entered.PermissionMode)
	require.Equal(t, "plan.md", entered.PlanPath)

	// durable 会话状态：planmode 激活 + 权限模式元数据落库（下一 turn 生效）。
	reloaded, err := handler.sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	require.True(t, planmode.IsActive(planmode.Load(reloaded)))
	require.Equal(t, string(runtimepolicyModePlan()), sessionmetaPlanPermissionMode(reloaded))

	// request_changes：保持 active 并记录轮次/笔记。
	requestChanges, err := controllers.ExitPlanMode(context.Background(), session.ID, toolbroker.ExitPlanModeArgs{
		Decision: "request_changes",
		Notes:    "add rollback section",
		Source:   "user",
	})
	require.NoError(t, err)
	require.True(t, requestChanges.Active)
	require.Equal(t, "request_changes", requestChanges.ExitDecision)
	require.Equal(t, 1, requestChanges.ReviewRound)
	require.Contains(t, requestChanges.PendingReviewNotes, "rollback")

	// approve：退出 plan，权限还原。
	approved, err := controllers.ExitPlanMode(context.Background(), session.ID, toolbroker.ExitPlanModeArgs{
		Decision: "approve",
		Source:   "user",
	})
	require.NoError(t, err)
	require.False(t, approved.Active)
	require.Equal(t, "approve", approved.ExitDecision)

	reloaded, err = handler.sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	require.False(t, planmode.IsActive(planmode.Load(reloaded)))
}

// runtimepolicyModePlan 是 ModePlan 的字符串形态（与 runtimepolicy.ModePlan 同值；
// 测试避免直接依赖 policy 包的类型常量，保持断言可读）。
func runtimepolicyModePlan() string { return "plan" }

func TestPlanExitWithoutPlanModeRejected(t *testing.T) {
	handler, session := newInteractiveTestHandler(t)
	controllers := handler.interactiveControllers()
	_, err := controllers.ExitPlanMode(context.Background(), session.ID, toolbroker.ExitPlanModeArgs{
		Decision: "approve",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in plan mode")
}

func TestModelPlanExitRequiresUserConfirmation(t *testing.T) {
	handler, session := newInteractiveTestHandler(t)
	controllers := handler.interactiveControllers()
	_, err := controllers.EnterPlanMode(context.Background(), session.ID, toolbroker.EnterPlanModeArgs{})
	require.NoError(t, err)
	// 模型来源的 approve 在 web 面须走 pending approval 卡片确认（与 actor 同门控）。
	_, err = controllers.ExitPlanMode(context.Background(), session.ID, toolbroker.ExitPlanModeArgs{
		Decision: "approve",
		Source:   "model",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires user confirmation")
}
