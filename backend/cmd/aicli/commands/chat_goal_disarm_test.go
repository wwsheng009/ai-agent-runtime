package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

// G4 rearm 闸（DSH goal 借鉴，docs/analysis/dsh-goal-domain-vs-aar-goal-20261017.md
// §2.3/G4）：恢复含 active goal 的会话后，用户任意消息不得触发自动续跑；
// /goal resume（或显式继续意图）重新武装。
func TestGoalDisarmGateBlocksContinuationAfterResume(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.ChatExecutor = &aicliSharedChatExecutor{}
	session.goalContinuationDisarmed.Store(true)

	decision, err := shouldAutoContinueActiveGoalDecision(session)
	require.NoError(t, err)
	require.False(t, decision.Continue, "disarmed session must not auto-continue")
	require.Equal(t, "goal_disarmed", decision.Reason)

	// rearm 后同一 active goal 恢复可续跑（其余判定路径不变）。
	session.goalContinuationDisarmed.Store(false)
	decision, err = shouldAutoContinueActiveGoalDecision(session)
	require.NoError(t, err)
	require.True(t, decision.Continue, "re-armed session with active goal must auto-continue")
	require.Equal(t, "goal_active", decision.Reason)
}

func TestDisarmGoalContinuationAfterRestore(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalContinuationDisarmed.Store(true)

	// active goal：restore 后保持 disarmed。
	disarmGoalContinuationAfterRestore(session)
	require.True(t, session.goalContinuationDisarmed.Load(), "restore with an active goal keeps the gate set")

	// 非 active goal（如 complete）：restore 不置闸。
	session.goalContinuationDisarmed.Store(false)
	goal, ok, err := currentSessionGoal(session)
	require.NoError(t, err)
	require.True(t, ok)
	goal.Status = runtimegoal.StatusComplete
	goal.CompletedBy = "user"
	goal.UpdatedAt = time.Now()
	if err := runtimegoal.NewMetadataStore().Put(session.RuntimeSession, *goal); err != nil {
		t.Fatalf("mark goal complete: %v", err)
	}
	disarmGoalContinuationAfterRestore(session)
	require.False(t, session.goalContinuationDisarmed.Load(), "restore without an active goal must not disarm")
}

func TestGoalResumeCommandRearmsContinuation(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusPaused)
	defer cleanup()
	session.goalContinuationDisarmed.Store(true)

	output := captureStdout(t, func() {
		handleGoalCommand(session, "/goal resume")
	})
	require.Contains(t, output, "Goal 已恢复")
	require.False(t, session.goalContinuationDisarmed.Load(), "/goal resume must re-arm the continuation gate")
}

func TestGoalStatusShowsDisarmedHint(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalContinuationDisarmed.Store(true)

	output := captureStdout(t, func() {
		printGoalStatus(session, false)
	})
	require.Contains(t, output, goalDisarmedStatusHint, "disarmed active goal must surface the hint")

	// armed（默认）时不出现提示。
	session.goalContinuationDisarmed.Store(false)
	output = captureStdout(t, func() {
		printGoalStatus(session, false)
	})
	require.NotContains(t, output, goalDisarmedStatusHint)
}
