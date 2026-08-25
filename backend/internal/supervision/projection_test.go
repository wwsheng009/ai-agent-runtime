package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProjectLifecycleReplayDoesNotReopenResolvedDecision(t *testing.T) {
	store := newTestStore(t, "supervision-projection-replay-monotonic")
	ctx := context.Background()
	event := LifecycleProjection{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "batch-1",
		SubjectVersion:        3,
		EventType:             "subagent.batch.failed",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionBlocked,
		Reason:                "provider unavailable",
		ResolutionState:       ResolutionUnresolved,
	}

	first, err := ProjectLifecycle(ctx, store, nil, event)
	require.NoError(t, err)
	acknowledged, err := store.AcknowledgeNotification(ctx, first.NotificationID, time.Now().UTC(), first.Version)
	require.NoError(t, err)
	require.True(t, acknowledged)

	afterAck, err := store.GetNotification(ctx, first.NotificationID)
	require.NoError(t, err)
	resolved, err := store.ResolveNotification(ctx, first.NotificationID, ResolutionClosed, time.Now().UTC(), afterAck.Version)
	require.NoError(t, err)
	require.True(t, resolved)

	replayed, err := ProjectLifecycle(ctx, store, nil, event)
	require.NoError(t, err)
	require.Equal(t, first.NotificationID, replayed.NotificationID)
	require.Equal(t, DecisionAcknowledged, replayed.DecisionState)
	require.Equal(t, ResolutionClosed, replayed.ResolutionState)
}
