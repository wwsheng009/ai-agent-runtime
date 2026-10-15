package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// newGoalGuidanceTestHandler 构造带内存会话存储的 handler 与一个已落库会话
// （runtimeServerGoalToolsAvailable 要求会话在存储中可见）。
func newGoalGuidanceTestHandler(t *testing.T) (*Handler, *chat.SessionManager, *chat.Session) {
	t.Helper()
	ctx := context.Background()
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler := NewHandler(nil, nil, nil)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(ctx, "user-goal-guidance")
	require.NoError(t, err)
	return handler, sessionManager, session
}

func persistGoalForGuidanceTest(t *testing.T, sessionManager *chat.SessionManager, session *chat.Session, goal runtimegoal.SessionGoal) {
	t.Helper()
	require.NoError(t, runtimegoal.NewMetadataStore().Put(session, goal))
	require.NoError(t, sessionManager.Update(context.Background(), session))
}

func newGuidanceTestAgent() *agent.Agent {
	return agent.NewAgent(&agent.Config{Name: "goal-guidance-test", Options: map[string]interface{}{}}, nil)
}

// TestApplyRuntimeServerGoalGuidanceInjectsActiveGoal：active goal 在 run 前被
// 渲染进 agent options（与 CLI 本地路径同一渲染器 + 同一键）。
func TestApplyRuntimeServerGoalGuidanceInjectsActiveGoal(t *testing.T) {
	handler, sessionManager, session := newGoalGuidanceTestHandler(t)
	goal, err := runtimegoal.NewSessionGoal(session.ID, "finish the migration", time.Now())
	require.NoError(t, err)
	persistGoalForGuidanceTest(t, sessionManager, session, goal)

	apiAgent := newGuidanceTestAgent()
	handler.applyRuntimeServerGoalGuidance(apiAgent, session)

	raw, ok := apiAgent.GetConfig().Options[runtimegoal.GuidanceOptionKey]
	require.True(t, ok, "active goal must inject %s", runtimegoal.GuidanceOptionKey)
	guidance, _ := raw.(string)
	require.Contains(t, guidance, "finish the migration")
	require.Contains(t, guidance, "completion audit")
	require.Contains(t, guidance, "call update_goal")
}

// TestApplyRuntimeServerGoalGuidanceClearsWhenGoalInactive：pause/complete 后
// 指引必须被清除（同一 actor 无需重建即生效）。
func TestApplyRuntimeServerGoalGuidanceClearsWhenGoalInactive(t *testing.T) {
	handler, sessionManager, session := newGoalGuidanceTestHandler(t)
	goal, err := runtimegoal.NewSessionGoal(session.ID, "finish the migration", time.Now())
	require.NoError(t, err)
	persistGoalForGuidanceTest(t, sessionManager, session, goal)

	apiAgent := newGuidanceTestAgent()
	handler.applyRuntimeServerGoalGuidance(apiAgent, session)
	require.Contains(t, apiAgent.GetConfig().Options, runtimegoal.GuidanceOptionKey)

	goal.Status = runtimegoal.StatusPaused
	persistGoalForGuidanceTest(t, sessionManager, session, goal)
	handler.applyRuntimeServerGoalGuidance(apiAgent, session)
	require.NotContains(t, apiAgent.GetConfig().Options, runtimegoal.GuidanceOptionKey)
}

// TestApplyRuntimeServerGoalGuidanceClearsStaleOptionWithoutGoal：会话无 goal
// 时必须清掉上一轮遗留的指引，不能把旧目标继续喂给模型。
func TestApplyRuntimeServerGoalGuidanceClearsStaleOptionWithoutGoal(t *testing.T) {
	handler, _, session := newGoalGuidanceTestHandler(t)
	apiAgent := newGuidanceTestAgent()
	apiAgent.GetConfig().Options[runtimegoal.GuidanceOptionKey] = "stale guidance"

	handler.applyRuntimeServerGoalGuidance(apiAgent, session)
	require.NotContains(t, apiAgent.GetConfig().Options, runtimegoal.GuidanceOptionKey)
}

// TestApplyRuntimeServerGoalGuidanceNilInputsAreNoops：nil agent / nil session
// 不得 panic（钩子在 actor 生命周期早期也可能被调用）。
func TestApplyRuntimeServerGoalGuidanceNilInputsAreNoops(t *testing.T) {
	handler, _, session := newGoalGuidanceTestHandler(t)
	handler.applyRuntimeServerGoalGuidance(nil, session)
	handler.applyRuntimeServerGoalGuidance(newGuidanceTestAgent(), nil)
}

// TestSessionDepsWiresRunPrepareGoalHook：SessionDeps 必须装配 run 级 goal 指引
// 钩子（缺了它服务端 turn 就没有逐轮指引）。
func TestSessionDepsWiresRunPrepareGoalHook(t *testing.T) {
	handler, _, session := newGoalGuidanceTestHandler(t)
	deps := handler.SessionDeps(session.ID)
	require.NotNil(t, deps.PrepareAgent)
	require.NotNil(t, deps.OnRunPrepare)

	// 闭包本身可安全调用（preparedAgent 未装配时 applyRuntimeServerGoalGuidance
	// 走 nil 早返回，不得 panic）。
	require.NoError(t, deps.OnRunPrepare(context.Background(), session, false))
}

// TestSessionActor_InjectsActiveGoalGuidancePerTurn 是 gap-1 的端到端验证：
// runtime-server actor（buildSessionActor → SubmitPrompt）在每次 run 前把会话
// active goal 渲染进 turn 上下文；同一 actor 无需重建，goal 变更在下一轮生效。
func TestSessionActor_InjectsActiveGoalGuidancePerTurn(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)

	provider := &testLLMProvider{name: "goal-guidance-model", content: "ok"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "goal-guidance-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("goal-guidance-model", provider))
	handler.SetLLMRuntime(runtime)

	session, err := sessionManager.Create(ctx, "user-1")
	require.NoError(t, err)
	goal, err := runtimegoal.NewSessionGoal(session.ID, "finish gap one", time.Now())
	require.NoError(t, err)
	require.NoError(t, runtimegoal.NewMetadataStore().Put(session, goal))
	require.NoError(t, sessionManager.Update(ctx, session))

	actor, err := handler.buildSessionActor(session.ID)
	require.NoError(t, err)
	defer actor.Stop()

	_, err = actor.SubmitPrompt(ctx, "hi", nil)
	require.NoError(t, err)
	require.NotEmpty(t, provider.requests)
	first := provider.requests[0]
	require.True(t, messageListContainsText(first.Messages, "finish gap one"),
		"server turn must carry the active goal objective")
	require.True(t, messageListContainsText(first.Messages, "completion audit"),
		"server turn must carry the completion-audit guidance")
	require.True(t, messageListContainsText(first.Messages, "call update_goal"),
		"server turn must allow update_goal when the session surface exposes it")

	// 第二轮：改目标后同一 actor 再次 run，run 前钩子必须按实时会话重新渲染
	// （不需要重建 actor；这正是 gap-1 修复的行为）。
	stored, err := sessionManager.Get(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	goal.Objective = "finish gap two"
	require.NoError(t, runtimegoal.NewMetadataStore().Put(stored, goal))
	require.NoError(t, sessionManager.Update(ctx, stored))

	_, err = actor.SubmitPrompt(ctx, "again", nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(provider.requests), 2)
	second := provider.requests[1]
	require.True(t, messageListContainsText(second.Messages, "finish gap two"),
		"second server turn must carry the refreshed goal objective")
}
