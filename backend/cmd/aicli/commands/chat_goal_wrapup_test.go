package commands

import (
	"strings"
	"testing"

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
