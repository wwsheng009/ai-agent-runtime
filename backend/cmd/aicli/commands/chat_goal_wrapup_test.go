package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

// G3 终局 wrapup（DSH goal 借鉴，docs/analysis/dsh-goal-domain-vs-aar-goal-20261017.md
// §1.3/§3.1）：模型终态后请求一次收尾轮，消费一次性，非终态不请求。
func TestRequestGoalTerminalWrapUp(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	// 非 model 终态语义由调用方（reconciler 只传 completed 的 goal）保证；
	// 这里验证状态→提示词映射与一次性消费。
	requestGoalTerminalWrapUp(session, &runtimegoal.SessionGoal{Status: runtimegoal.StatusComplete})
	got := consumeGoalTerminalWrapUp(session)
	require.Equal(t, runtimegoal.CompleteWrapUpPrompt, got)
	require.Empty(t, consumeGoalTerminalWrapUp(session), "wrap-up must be consumed exactly once")

	requestGoalTerminalWrapUp(session, &runtimegoal.SessionGoal{Status: runtimegoal.StatusBlocked})
	got = consumeGoalTerminalWrapUp(session)
	require.Equal(t, runtimegoal.BlockedWrapUpPrompt, got)

	// active / budget_limited 不请求收尾。
	requestGoalTerminalWrapUp(session, &runtimegoal.SessionGoal{Status: runtimegoal.StatusActive})
	require.Empty(t, consumeGoalTerminalWrapUp(session))
}

// TestRequestGoalTerminalWrapUpDeduplicatesSameTerminalState 回归 2026-10-18
// 真机复现：reconcile 增量扫描在 goal 保持终态期间会反复命中同一条 complete
// 记录，旧实现每轮都重新 Store → goal 完成后每次新的前台轮收尾都会再补一个
// 收尾轮（每发一条消息模型多输出一段）。现按“goal_id|status|updated_at”
// 去重；同一 goal 经 resume 再次终态（updated_at 前移）时允许再次收尾。
func TestRequestGoalTerminalWrapUpDeduplicatesSameTerminalState(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()

	completedAt := time.Date(2026, 10, 18, 23, 37, 0, 0, time.UTC)
	goal := &runtimegoal.SessionGoal{
		GoalID:    "goal-dedupe",
		Status:    runtimegoal.StatusComplete,
		UpdatedAt: completedAt,
	}
	requestGoalTerminalWrapUp(session, goal)
	require.Equal(t, runtimegoal.CompleteWrapUpPrompt, consumeGoalTerminalWrapUp(session))

	// 同一终态重复命中（reconcile 每轮重扫）：不得再次入队。
	requestGoalTerminalWrapUp(session, goal)
	require.Empty(t, consumeGoalTerminalWrapUp(session),
		"the same terminal state must not re-arm wrap-up")

	// resume 后再次终态（updated_at 前移）：允许再次收尾。
	recompleted := *goal
	recompleted.UpdatedAt = completedAt.Add(10 * time.Minute)
	requestGoalTerminalWrapUp(session, &recompleted)
	require.Equal(t, runtimegoal.CompleteWrapUpPrompt, consumeGoalTerminalWrapUp(session),
		"a re-terminalized goal (new updated_at) must re-arm wrap-up")
}

func TestWrapUpPromptsCarryGroundingDiscipline(t *testing.T) {
	for _, prompt := range []string{runtimegoal.CompleteWrapUpPrompt, runtimegoal.BlockedWrapUpPrompt} {
		require.Contains(t, prompt, "do not call any more tools", "wrap-up must forbid further tools")
		require.Contains(t, prompt, "instead of inventing it", "wrap-up must carry the grounding discipline")
	}
	require.Contains(t, runtimegoal.CompleteWrapUpPrompt, "how it was verified")
	require.Contains(t, runtimegoal.BlockedWrapUpPrompt, "blocking condition")
	// 与既有三段提示词同层的共享常量：不得为空、不得以空白结尾差异形式出现。
	for _, prompt := range []string{
		runtimegoal.ContinuationPrompt,
		runtimegoal.BudgetWrapUpPrompt,
		runtimegoal.IterateEscalationPrompt,
		runtimegoal.CompleteWrapUpPrompt,
		runtimegoal.BlockedWrapUpPrompt,
	} {
		require.NotEmpty(t, strings.TrimSpace(prompt))
	}
}
