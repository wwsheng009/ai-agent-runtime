package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestResumeEpisode_DoesNotTouchRunLedger pins C3-7 / AC-P2-7c（§6.15）：steer 骑在
// resume episode 上（同 turn_id），它是一次投递观测，不是进度事件，也不是父决策。
// wake 被投递并消费后，目标 obligation 的账本必须逐字段不变：progress_seq 不变、
// 延长计数（ExtensionCount / ExtendedTotal）不变、escalate-first 决策窗口不被改写。
func TestResumeEpisode_DoesNotTouchRunLedger(t *testing.T) {
	store := newTestStore(t, "steer-resume-no-count")
	ctx := context.Background()
	now := time.Now().UTC()
	progressDeadline := now.Add(30 * time.Minute)
	decisionWindow := now.Add(5 * time.Minute)

	seeded := ExecutionRun{
		RunID:               "child-steer",
		Kind:                RunKindAgentRun,
		Workflow:            RunWorkflowSpawnAgent,
		RootSessionID:       "root-steer",
		ParentSessionID:     "root-steer",
		SessionID:           "child-steer",
		TurnID:              "turn-steer",
		Status:              RunStatusRunning,
		ProgressSeq:         7,
		ExtensionCount:      2,
		ExtendedTotal:       90 * time.Second,
		ProgressDeadlineAt:  &progressDeadline,
		DecisionWindowUntil: &decisionWindow,
		StartedAt:           now.Add(-time.Minute),
		LastHeartbeatAt:     now.Add(-time.Second),
		LastProgressAt:      now.Add(-time.Second),
		CreatedAt:           now.Add(-time.Minute),
		UpdatedAt:           now,
	}
	created, err := store.CreateExecutionRun(ctx, seeded)
	require.NoError(t, err)
	require.True(t, created)
	before, err := store.GetExecutionRun(ctx, "child-steer")
	require.NoError(t, err)
	require.NotNil(t, before)

	scheduler := NewWakeScheduler(store, unlimitedWakeConfig())
	delivered := 0
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(context.Context, string, string, string) bool { return true },
		Deliver: func(_ context.Context, parentSessionID, rootScopeID string, _ *Digest, wakeIDs []string) error {
			delivered++
			return nil
		},
	}

	// 投影形状沿用既有 A6 resume 用例（critical/timed_out），确保 wake 一定被调度；
	// 本用例断言的是"投递不改账本"，与事件类型无关。
	_, err = ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-steer",
		TargetParentSessionID: "root-steer",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-steer",
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
	})
	require.NoError(t, err)
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-steer", "", "root-steer"))
	require.Equal(t, 1, delivered, "resume 必须照常开回合：steer 的载体是恢复回合，而不是新的进度事件")

	after, err := store.GetExecutionRun(ctx, "child-steer")
	require.NoError(t, err)
	require.NotNil(t, after)
	require.Equal(t, before.ProgressSeq, after.ProgressSeq, "AC-P2-7c: steer/resume 不得改变 progress_seq")
	require.Equal(t, before.ExtensionCount, after.ExtensionCount, "AC-P2-7c: steer 不计入延长次数")
	require.Equal(t, before.ExtendedTotal, after.ExtendedTotal)
	require.Equal(t, before.LastProgressAt, after.LastProgressAt, "steer 不是进度事件：last_progress_at 不得前移")
	require.NotNil(t, after.DecisionWindowUntil)
	require.True(t, after.DecisionWindowUntil.Equal(decisionWindow), "steer 不是父决策：决策窗口不得被改写")
	require.Equal(t, RunStatusRunning, after.Status)
}
