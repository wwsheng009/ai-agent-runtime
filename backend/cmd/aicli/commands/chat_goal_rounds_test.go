package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

// G2 轮数上限 CLI 侧集成（DSH goal 借鉴）：
//   - admit 递增 durable 轮数并在达到 MaxRounds 时报告上限；
//   - markActiveGoalBlockedRoundLimit 落库 blocked（round-limit）且 /goal resume 可解除。
func TestAdmitGoalRoundForSessionReachesConfiguredCap(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.ChatExecutor = &aicliSharedChatExecutor{}

	goal, ok, err := currentSessionGoal(session)
	require.NoError(t, err)
	require.True(t, ok)
	goal.MaxRounds = 2
	require.NoError(t, runtimegoal.NewMetadataStore().Put(session.RuntimeSession, *goal))

	reached, err := admitGoalRoundForSession(session)
	require.NoError(t, err)
	require.False(t, reached, "first admitted round must not reach the cap")

	reached, err = admitGoalRoundForSession(session)
	require.NoError(t, err)
	require.True(t, reached, "second admitted round must reach the cap of 2")

	stored, ok, err := currentSessionGoal(session)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 2, stored.RoundsStarted, "durable round count must persist")

	require.True(t, markActiveGoalBlockedRoundLimit(session), "round-limit blocked must persist")
	stored, _, err = currentSessionGoal(session)
	require.NoError(t, err)
	require.Equal(t, runtimegoal.StatusBlocked, stored.Status)
	require.Equal(t, runtimegoal.BlockedCodeRoundLimit, stored.BlockedCode)

	// /goal resume 解除 blocked（用户主权出口），且 rearm 清除 G4 闸。
	session.goalContinuationDisarmed.Store(true)
	output := captureStdout(t, func() {
		handleGoalCommand(session, "/goal resume")
	})
	require.Contains(t, output, "Goal 已恢复")
	restored, _, err := currentSessionGoal(session)
	require.NoError(t, err)
	require.Equal(t, runtimegoal.StatusActive, restored.Status)
	require.False(t, session.goalContinuationDisarmed.Load(), "/goal resume must re-arm")
}
