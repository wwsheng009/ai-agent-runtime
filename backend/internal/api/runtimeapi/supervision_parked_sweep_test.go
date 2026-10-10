package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestSweepSettledParkedTurnsClearsOrphanAndResumes 是 CLI
// TestSweepSettledParkedTurnsClearsOrphanAndResumes 的 API 侧对等：对账清扫必须
// 清掉义务已全终态的孤儿挂起记录、保留运行中记录，并留下结算 wake（父会话不可
// 运行时保持 durable）。
func TestSweepSettledParkedTurnsClearsOrphanAndResumes(t *testing.T) {
	handler, store := newAPISupervisionBudgetTestHandler(t, "api-parked-sweep")
	batches := newAPISupervisionBatchStore(t)
	handler.SetSubagentBatchStore(batches)
	ctx := context.Background()
	now := time.Now().UTC()

	const sessionID = "sess_parent"
	_, err := batches.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         "batch_orphan",
		RootScopeID:     sessionID,
		ParentSessionID: sessionID,
		ParentTurnID:    "turn_orphan",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchCompleted,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}, nil)
	require.NoError(t, err)
	require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn_orphan",
		SessionID:     sessionID,
		RootScopeID:   sessionID,
		ObligationIDs: []string{"batch_orphan"},
		ResumeQueue:   []string{"batch_orphan"},
		ParkedAt:      now,
	}))

	_, err = batches.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         "batch_live",
		RootScopeID:     sessionID,
		ParentSessionID: sessionID,
		ParentTurnID:    "turn_live",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}, nil)
	require.NoError(t, err)
	require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn_live",
		SessionID:     sessionID,
		RootScopeID:   sessionID,
		ObligationIDs: []string{"batch_live"},
		ResumeQueue:   []string{"batch_live"},
		ParkedAt:      now,
	}))

	outcome, err := handler.sweepSettledParkedTurns(ctx, now)
	require.NoError(t, err)
	require.True(t, outcome.Supported)
	require.Equal(t, 2, outcome.Scanned)
	require.Equal(t, 1, outcome.Cleared)
	require.Zero(t, outcome.Failed)

	_, orphanOK, err := batches.GetTurnSuspension(ctx, sessionID, "turn_orphan")
	require.NoError(t, err)
	require.False(t, orphanOK, "a terminal orphan record must be swept")
	_, liveOK, err := batches.GetTurnSuspension(ctx, sessionID, "turn_live")
	require.NoError(t, err)
	require.True(t, liveOK, "a still-running obligation keeps its record")

	wakes, err := store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           sessionID,
		TargetParentSessionID: sessionID,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	settled := false
	for _, wake := range wakes {
		if wake.WakeReason == supervision.WakeReasonObligationSettled {
			settled = true
		}
	}
	require.True(t, settled, "the sweep must leave a durable settlement wake")
}
