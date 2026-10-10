package commands

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestSweepSettledParkedTurnsClearsOrphanAndResumes 钉住第三层自愈（真机
// 2026-10-10）：义务已全终态、但终态 wake 与下一个 run 双缺的孤儿挂起记录，必须
// 在对账节拍里被清掉并补发 turn.resumed；仍欠运行中义务的记录保持不动；清理前
// 先排一笔结算 wake 并投递给可运行的父会话。
func TestSweepSettledParkedTurnsClearsOrphanAndResumes(t *testing.T) {
	host, store, deliveries := newWakeConsumerTestHost(t, "aicli-parked-sweep")
	ctx := context.Background()
	attachSettledTurnWakeFixture(t, host, store, "parked-sweep-batches")

	const sessionID = "root-session"
	now := time.Now().UTC()
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: sessionID,
		Status:    runtimechat.SessionIdle,
		UpdatedAt: now,
	}))

	var mu sync.Mutex
	var resumed []runtimeevents.Event
	host.EventBus.Subscribe(runtimeevents.EventTurnResumed, func(event runtimeevents.Event) {
		mu.Lock()
		defer mu.Unlock()
		resumed = append(resumed, event)
	})

	_, err := host.SubagentBatches.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         "batch-orphan",
		RootScopeID:     sessionID,
		ParentSessionID: sessionID,
		ParentTurnID:    "turn-orphan",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchCompleted,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}, nil)
	require.NoError(t, err)
	require.NoError(t, host.SubagentBatches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-orphan",
		SessionID:     sessionID,
		RootScopeID:   sessionID,
		ObligationIDs: []string{"batch-orphan", "task-1"},
		ResumeQueue:   []string{"batch-orphan"},
		ParkedAt:      now,
	}))

	_, err = host.SubagentBatches.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         "batch-live",
		RootScopeID:     sessionID,
		ParentSessionID: sessionID,
		ParentTurnID:    "turn-live",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}, nil)
	require.NoError(t, err)
	require.NoError(t, host.SubagentBatches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-live",
		SessionID:     sessionID,
		RootScopeID:   sessionID,
		ObligationIDs: []string{"batch-live"},
		ResumeQueue:   []string{"batch-live"},
		ParkedAt:      now,
	}))

	// 真实链路里批次终态投影会先落一条 lifecycle 通知；唤醒投递要求 digest 有
	// 内容（digestDeliverable），没有这条通知时 drain 会把它按"空转 turn"释放。
	_, err = supervision.ProjectLifecycle(ctx, store, host.supervisionWake.Wakes, supervision.LifecycleProjection{
		RootScopeID:           sessionID,
		TargetParentSessionID: sessionID,
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "batch-orphan",
		SubjectVersion:        1,
		EventType:             "subagent.batch.completed",
		Severity:              supervision.SeverityInfo,
		SupervisionState:      supervision.SupervisionTerminated,
		Reason:                "subagent batch batch-orphan finished with status completed",
		RecommendedAction:     string(supervision.ActionClose),
		AllowedActions:        []string{string(supervision.ActionInspect)},
		ResolutionState:       supervision.ResolutionClosed,
	})
	require.NoError(t, err)

	outcome, err := host.sweepSettledParkedTurns(ctx, now)
	require.NoError(t, err)
	require.True(t, outcome.Supported, "durable batch store must report supported")
	require.Equal(t, 2, outcome.Scanned)
	require.Equal(t, 1, outcome.Cleared)
	require.Zero(t, outcome.Failed)

	_, orphanOK, err := host.SubagentBatches.GetTurnSuspension(ctx, sessionID, "turn-orphan")
	require.NoError(t, err)
	require.False(t, orphanOK, "a terminal orphan record must be swept")
	_, liveOK, err := host.SubagentBatches.GetTurnSuspension(ctx, sessionID, "turn-live")
	require.NoError(t, err)
	require.True(t, liveOK, "a still-running obligation keeps its record")

	pendingWakes, pendingErr := store.ListWakePending(ctx, supervision.WakeFilter{RootScopeID: sessionID, UnclaimedOnly: true})
	t.Logf("after sweep: deliveries=%d pending_wakes=%d err=%v", deliveries.count(), len(pendingWakes), pendingErr)
	deliveries.wait(t, 3*time.Second)
	require.Equal(t, 1, deliveries.count(), "the sweep must wake the runnable parent exactly once")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, resumed, 1, "the sweep must announce the settled closure")
	require.Equal(t, "turn-orphan", resumed[0].Payload["turn_id"])
	require.Equal(t, runtimeevents.TurnResumedTriggerSettled, resumed[0].Payload["trigger"])
	require.Equal(t, true, resumed[0].Payload["sweep"])
}
