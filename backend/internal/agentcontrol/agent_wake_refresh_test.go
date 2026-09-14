package agentcontrol

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Host projections rewrite every identity on every materialize pass, so an
// unchanged refresh must keep the updated_at heartbeat (reclaim reads it as its
// idle fallback) while staying invisible to the wake log: the durable event
// stream is a change feed, not a pass counter.
func TestUpsertAgentControlAgentRefreshKeepsHeartbeatWithoutWakeEvent(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	record := AgentRecord{
		AgentID:         "refresh-child",
		RootSessionID:   "refresh-root",
		ParentAgentID:   "refresh-root-root",
		ParentSessionID: "refresh-root",
		SessionID:       "refresh-child-session",
		AgentPath:       "/root/refresh-child",
		Depth:           1,
		AgentType:       AgentTypeChild,
		Workflow:        WorkflowSpawnAgent,
		Provider:        "openai",
		RequestedModel:  "gpt-5",
		EffectiveModel:  "gpt-5",
		RouteWarnings:   []string{"difficulty routing kept the requested model"},
		Status:          AgentStatusActive,
	}
	first, err := store.UpsertAgentControlAgent(ctx, record)
	require.NoError(t, err)
	firstSeq, err := store.LastAgentControlAgentWakeSeq(ctx, AgentWakeFilter{RootSessionID: record.RootSessionID})
	require.NoError(t, err)
	require.Equal(t, int64(1), firstSeq, "a new identity wakes its watchers")

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake, unwatch := store.WatchAgentControlAgentWake(watchCtx, AgentWakeFilter{RootSessionID: record.RootSessionID})
	defer unwatch()

	time.Sleep(20 * time.Millisecond)
	refreshed, err := store.UpsertAgentControlAgent(ctx, record)
	require.NoError(t, err)
	require.True(t, refreshed.UpdatedAt.After(first.UpdatedAt), "the heartbeat write must still land")
	require.True(t, agentRecordsShareWakeState(first, refreshed))

	refreshedSeq, err := store.LastAgentControlAgentWakeSeq(ctx, AgentWakeFilter{RootSessionID: record.RootSessionID})
	require.NoError(t, err)
	require.Equal(t, firstSeq, refreshedSeq, "an unchanged refresh must not append a wake event")
	select {
	case event := <-wake:
		t.Fatalf("an unchanged refresh must not notify watchers: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}

	// The other half must not regress: a real change still wakes its watchers.
	changed := record
	changed.EffectiveModel = "gpt-5-mini"
	changed.RouteWarnings = []string{"fallback switched the effective model"}
	_, err = store.UpsertAgentControlAgent(ctx, changed)
	require.NoError(t, err)
	changedSeq, err := store.LastAgentControlAgentWakeSeq(ctx, AgentWakeFilter{RootSessionID: record.RootSessionID})
	require.NoError(t, err)
	require.Equal(t, firstSeq+1, changedSeq)
	select {
	case event := <-wake:
		require.Equal(t, "upsert", event.EventKind)
		require.Equal(t, record.AgentID, event.AgentID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the change wake")
	}
}

// TestAgentRecordsShareWakeStateTracksPersistedShape pins the comparison the
// no-op suppression relies on: only updated_at/created_at/row sequence are
// allowed to move without producing a wake event.
func TestAgentRecordsShareWakeStateTracksPersistedShape(t *testing.T) {
	base := AgentRecord{
		AgentID:       "child",
		RootSessionID: "root-session",
		SessionID:     "child-session",
		AgentPath:     "/root/child",
		Depth:         1,
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
		RouteWarnings: []string{"kept"},
	}
	require.True(t, agentRecordsShareWakeState(base, base))

	heartbeatOnly := base
	heartbeatOnly.CreatedAt = base.CreatedAt.Add(time.Minute)
	heartbeatOnly.UpdatedAt = base.UpdatedAt.Add(time.Minute)
	heartbeatOnly.Seq = 42
	require.True(t, agentRecordsShareWakeState(base, heartbeatOnly))
	require.True(t, agentRecordsShareWakeState(heartbeatOnly, base))

	zeroClosedAt := time.Time{}
	require.True(t, agentRecordsShareWakeState(
		AgentRecord{AgentPath: "/root/child", ClosedAt: &zeroClosedAt},
		AgentRecord{AgentPath: "/root/child"},
	), "a zero closed_at persists as NULL, so it is not a change")
	require.True(t, agentRecordsShareWakeState(
		AgentRecord{AgentPath: "/root/child"},
		AgentRecord{AgentPath: "/root/child", RouteWarnings: []string{}},
	), "nil and empty route warnings are the same persisted state")

	closedAt := time.Now().UTC()
	mutations := map[string]func(*AgentRecord){
		"status":          func(r *AgentRecord) { r.Status = AgentStatusClosed },
		"closed_at":       func(r *AgentRecord) { r.ClosedAt = &closedAt },
		"effective_model": func(r *AgentRecord) { r.EffectiveModel = "gpt-5-mini" },
		"route_warnings":  func(r *AgentRecord) { r.RouteWarnings = []string{"switched"} },
		"agent_path":      func(r *AgentRecord) { r.AgentPath = "/root/child-2" },
		"depth":           func(r *AgentRecord) { r.Depth = 2 },
		"fallback_used":   func(r *AgentRecord) { r.FallbackUsed = true },
		"parent_session":  func(r *AgentRecord) { r.ParentSessionID = "root-session" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			require.False(t, agentRecordsShareWakeState(base, changed))
			require.False(t, agentRecordsShareWakeState(changed, base))
		})
	}
}
