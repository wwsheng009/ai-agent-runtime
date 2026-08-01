package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// zeroAlertConfig returns a config with every threshold set to trigger
// immediately so tests only need minimal fixture data.
func zeroAlertConfig() AlertConfig {
	return AlertConfig{
		OutboxBacklogMin: 1,
		OutboxStaleAge:   0,
		// Duration thresholds: store forces created_at=now, so a tiny
		// positive age threshold plus a short sleep makes "now" stale.
		CriticalStaleAge: time.Millisecond,
		WakeStaleAge:     time.Millisecond,
		MaxAlerts:        20,
	}
}

func TestEvaluateAlerts_HealthyStoreNoAlerts(t *testing.T) {
	store := testExecutionRunStore(t, "alerts-healthy")
	ctx := context.Background()

	alerts, err := EvaluateAlerts(ctx, store, "", DefaultAlertConfig())
	require.NoError(t, err)
	require.Empty(t, alerts)
}

func TestEvaluateAlerts_OutboxBacklog(t *testing.T) {
	store := testExecutionRunStore(t, "alerts-outbox")
	ctx := context.Background()
	now := time.Now().UTC()

	payload, err := MarshalOutboxPayloadJSON(map[string]interface{}{"status": "succeeded"})
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		runID := "run_ob_" + string(rune('a'+i))
		created, err := store.EnqueueCompletionOutbox(ctx, CompletionOutboxEntry{
			OutboxID:       "outbox_" + runID,
			RunID:          runID,
			SessionID:      "child-1",
			ParentSessionID: "parent-session",
			RootSessionID:  "root-session",
			Status:         RunStatusSucceeded,
			IdempotencyKey: "subagent_completion:" + runID + ":1",
			PayloadJSON:    payload,
			CreatedAt:      now,
		})
		require.NoError(t, err)
		require.True(t, created)
	}

	alerts, err := EvaluateAlerts(ctx, store, "", zeroAlertConfig())
	require.NoError(t, err)
	found := false
	for _, a := range alerts {
		if a.Code == AlertOutboxBacklog {
			found = true
			require.Equal(t, SeverityWarning, a.Severity)
			require.Equal(t, 3, a.Count)
			require.Contains(t, a.Message, "completion outbox backlog")
		}
	}
	require.True(t, found, "expected outbox backlog alert, got %+v", alerts)
}

func TestEvaluateAlerts_CriticalNotificationStale(t *testing.T) {
	store := testExecutionRunStore(t, "alerts-critical")
	ctx := context.Background()

	_, err := store.UpsertNotification(ctx, testNotification("agent-alert-1", 5))
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)

	alerts, err := EvaluateAlerts(ctx, store, "root-session-1", zeroAlertConfig())
	require.NoError(t, err)
	found := false
	for _, a := range alerts {
		if a.Code == AlertCriticalStale {
			found = true
			require.Equal(t, SeverityCritical, a.Severity)
			require.Equal(t, "agent-alert-1", a.SubjectID)
			require.Contains(t, a.Message, "critical notification(s) unresolved")
		}
	}
	require.True(t, found, "expected critical stale alert, got %+v", alerts)
}

func TestEvaluateAlerts_RunStalledAndOrphan(t *testing.T) {
	store := testExecutionRunStore(t, "alerts-runs")
	ctx := context.Background()
	now := time.Now().UTC()
	pastProgress := now.Add(-10 * time.Minute)
	pastLease := now.Add(-5 * time.Minute)
	future := now.Add(30 * time.Minute)

	// Stalled: progress deadline passed, lease still fresh.
	stalled := sampleExecutionRun("run_stalled")
	stalled.ProgressDeadlineAt = &pastProgress
	stalled.OwnerLeaseUntil = &future
	created, err := store.CreateExecutionRun(ctx, stalled)
	require.NoError(t, err)
	require.True(t, created)

	// Orphan: lease expired but progress deadline still fresh.
	orphan := sampleExecutionRun("run_orphan")
	orphan.ProgressDeadlineAt = &future
	orphan.OwnerLeaseUntil = &pastLease
	created, err = store.CreateExecutionRun(ctx, orphan)
	require.NoError(t, err)
	require.True(t, created)

	alerts, err := EvaluateAlerts(ctx, store, "", zeroAlertConfig())
	require.NoError(t, err)

	var stallAlert, orphanAlert *Alert
	for i := range alerts {
		switch alerts[i].Code {
		case AlertRunProgressStalled:
			stallAlert = &alerts[i]
		case AlertRunOrphanSuspected:
			orphanAlert = &alerts[i]
		}
	}
	require.NotNil(t, stallAlert, "expected progress stalled alert, got %+v", alerts)
	require.Equal(t, "run_stalled", stallAlert.SubjectID)
	require.Equal(t, SeverityWarning, stallAlert.Severity)
	require.NotNil(t, orphanAlert, "expected orphan suspected alert, got %+v", alerts)
	require.Equal(t, "run_orphan", orphanAlert.SubjectID)
	require.Equal(t, SeverityCritical, orphanAlert.Severity)
}

func TestEvaluateAlerts_WakePendingStale(t *testing.T) {
	store := testExecutionRunStore(t, "alerts-wake")
	ctx := context.Background()

	err := store.InsertWakePending(ctx, WakePending{
		WakeID:                "wake_1",
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            "child stalled",
		DedupKey:              "dedup:1",
		CreatedAt:             time.Now().UTC().Add(-10 * time.Minute),
	})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)

	alerts, err := EvaluateAlerts(ctx, store, "", zeroAlertConfig())
	require.NoError(t, err)
	found := false
	for _, a := range alerts {
		if a.Code == AlertWakePendingStale {
			found = true
			require.Equal(t, SeverityWarning, a.Severity)
			require.Contains(t, a.Message, "wake_pending row(s) unclaimed")
		}
	}
	require.True(t, found, "expected wake pending stale alert, got %+v", alerts)
}

func TestEvaluateAlerts_CriticalFirstInOrder(t *testing.T) {
	store := testExecutionRunStore(t, "alerts-order")
	ctx := context.Background()

	// One warning (outbox) and one critical (orphan).
	payload, err := MarshalOutboxPayloadJSON(map[string]interface{}{"status": "succeeded"})
	require.NoError(t, err)
	created, err := store.EnqueueCompletionOutbox(ctx, CompletionOutboxEntry{
		OutboxID:        "outbox_order",
		RunID:           "run_order",
		SessionID:       "child-1",
		ParentSessionID: "parent-session",
		RootSessionID:   "root-session",
		Status:          RunStatusSucceeded,
		IdempotencyKey:  "subagent_completion:run_order:1",
		PayloadJSON:     payload,
		CreatedAt:       time.Now().UTC(),
	})
	require.NoError(t, err)
	require.True(t, created)

	now := time.Now().UTC()
	pastLease := now.Add(-5 * time.Minute)
	orphan := sampleExecutionRun("run_order_orphan")
	orphan.OwnerLeaseUntil = &pastLease
	created, err = store.CreateExecutionRun(ctx, orphan)
	require.NoError(t, err)
	require.True(t, created)

	alerts, err := EvaluateAlerts(ctx, store, "", zeroAlertConfig())
	require.NoError(t, err)
	require.NotEmpty(t, alerts)
	require.Equal(t, SeverityCritical, alerts[0].Severity, "critical alerts must sort first, got %+v", alerts)
}
