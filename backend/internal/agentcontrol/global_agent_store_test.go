package agentcontrol

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSQLiteGlobalAgentRegistryStoreUpsertAndList(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	root, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "root-agent",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
		CreatedAt:     time.Unix(1, 0).UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), root.Seq)
	require.Equal(t, "/root", root.AgentPath)

	child, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:         "child-agent",
		RootSessionID:   "root-session",
		ParentAgentID:   root.AgentID,
		ParentSessionID: "root-session",
		SessionID:       "child-session",
		AgentPath:       "/root/child-agent",
		Depth:           1,
		AgentType:       AgentTypeChild,
		Nickname:        "worker",
		Workflow:        WorkflowSpawnAgent,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), child.Seq)
	require.Equal(t, AgentStatusActive, child.Status)

	records, err := store.ListAgentControlAgents(ctx, AgentFilter{
		RootSessionID: "root-session",
		PathPrefix:    "/root",
	})
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "root-agent", records[0].AgentID)
	require.Equal(t, "child-agent", records[1].AgentID)

	records, err = store.ListAgentControlAgents(ctx, AgentFilter{
		RootSessionID: "root-session",
		PathPrefix:    "/root/child-agent",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "child-session", records[0].SessionID)
}

func TestSQLiteGlobalAgentRegistryStoreCanonicalizesDuplicateRootPath(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-1",
		RootSessionID: "root-session",
		SessionID:     "session-1",
		AgentPath:     "/root/worker",
	})
	require.NoError(t, err)

	updated, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-2",
		RootSessionID: "root-session",
		SessionID:     "session-2",
		AgentPath:     "/root/worker",
		Workflow:      WorkflowSpawnTeam,
	})
	require.NoError(t, err)
	require.Equal(t, "agent-2", updated.AgentID)
	require.Equal(t, "session-2", updated.SessionID)
	require.Equal(t, WorkflowSpawnTeam, updated.Workflow)

	records, err := store.ListAgentControlAgents(ctx, AgentFilter{
		RootSessionID: "root-session",
		PathPrefix:    "/root/worker",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "agent-2", records[0].AgentID)
}

func TestSQLiteGlobalAgentRegistryStoreRejectsDuplicateActiveSessionBinding(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-1",
		RootSessionID: "root-session",
		SessionID:     "shared-session",
		AgentPath:     "/root/one",
	})
	require.NoError(t, err)

	_, err = store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-2",
		RootSessionID: "root-session",
		SessionID:     "shared-session",
		AgentPath:     "/root/two",
	})
	require.Error(t, err)

	closed, err := store.CloseAgentControlAgentSubtree(ctx, "root-session", "/root/one", time.Unix(20, 0).UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), closed)
	_, err = store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-2",
		RootSessionID: "root-session",
		SessionID:     "shared-session",
		AgentPath:     "/root/two",
	})
	require.NoError(t, err)
}

func TestSQLiteGlobalAgentRegistryStoreClosesSubtree(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	for _, record := range []AgentRecord{
		{AgentID: "root", RootSessionID: "root-session", SessionID: "root-session", AgentPath: "/root", AgentType: AgentTypeRoot},
		{AgentID: "worker", RootSessionID: "root-session", ParentAgentID: "root", SessionID: "worker-session", AgentPath: "/root/worker", AgentType: AgentTypeChild},
		{AgentID: "nested", RootSessionID: "root-session", ParentAgentID: "worker", SessionID: "nested-session", AgentPath: "/root/worker/nested", AgentType: AgentTypeChild},
		{AgentID: "sibling", RootSessionID: "root-session", ParentAgentID: "root", SessionID: "sibling-session", AgentPath: "/root/sibling", AgentType: AgentTypeChild},
	} {
		_, err := store.UpsertAgentControlAgent(ctx, record)
		require.NoError(t, err)
	}

	closed, err := store.CloseAgentControlAgentSubtree(ctx, "root-session", "/root/worker", time.Unix(30, 0).UTC())
	require.NoError(t, err)
	require.Equal(t, int64(2), closed)

	active, err := store.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: "root-session"})
	require.NoError(t, err)
	require.Len(t, active, 2)
	require.Equal(t, []string{"root", "sibling"}, []string{active[0].AgentID, active[1].AgentID})

	all, err := store.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: "root-session", IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, all, 4)
	require.True(t, all[1].Closed())
	require.True(t, all[2].Closed())
	require.False(t, all[3].Closed())
}

func TestSQLiteGlobalAgentRegistryStoreReserveSpawnEnforcesLimit(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	root := AgentRecord{
		AgentID:       "root",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
	}
	child, err := store.ReserveAgentControlAgentSpawn(ctx, root, AgentRecord{
		AgentID:         "child-1",
		RootSessionID:   "root-session",
		ParentAgentID:   "root",
		ParentSessionID: "root-session",
		SessionID:       "child-session-1",
		AgentPath:       "/root/child-1",
		Depth:           1,
		AgentType:       AgentTypeChild,
		Workflow:        WorkflowSpawnAgent,
	}, 1)
	require.NoError(t, err)
	require.Equal(t, "child-1", child.AgentID)

	_, err = store.ReserveAgentControlAgentSpawn(ctx, root, AgentRecord{
		AgentID:         "child-2",
		RootSessionID:   "root-session",
		ParentAgentID:   "root",
		ParentSessionID: "root-session",
		SessionID:       "child-session-2",
		AgentPath:       "/root/child-2",
		Depth:           1,
		AgentType:       AgentTypeChild,
		Workflow:        WorkflowSpawnAgent,
	}, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "agent spawn thread limit reached")

	records, err := store.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: "root-session", IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, records, 2)
}

func TestSQLiteGlobalAgentRegistryStoreWatchesAgentWake(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake, unwatch := store.WatchAgentControlAgentWake(watchCtx, AgentWakeFilter{
		RootSessionID: "root-session",
		PathPrefix:    "/root/child",
	})
	defer unwatch()

	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "root",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
	})
	require.NoError(t, err)
	select {
	case event := <-wake:
		t.Fatalf("root row should not match child prefix wake filter: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}

	_, err = store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:         "child",
		RootSessionID:   "root-session",
		ParentAgentID:   "root",
		ParentSessionID: "root-session",
		SessionID:       "child-session",
		AgentPath:       "/root/child",
		Depth:           1,
		AgentType:       AgentTypeChild,
		Workflow:        WorkflowSpawnAgent,
	})
	require.NoError(t, err)
	select {
	case event := <-wake:
		require.Greater(t, event.Seq, int64(0))
		require.Equal(t, "child-session", event.SessionID)
		require.Equal(t, "upsert", event.EventKind)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent wake")
	}
}

func TestSQLiteGlobalAgentRegistryStoreAgentWakeSequenceAndClose(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	root := AgentRecord{
		AgentID:       "root",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
	}
	_, err := store.ReserveAgentControlAgentSpawn(ctx, root, AgentRecord{
		AgentID:         "worker",
		RootSessionID:   "root-session",
		ParentAgentID:   "root",
		ParentSessionID: "root-session",
		SessionID:       "worker-session",
		AgentPath:       "/root/worker",
		Depth:           1,
		AgentType:       AgentTypeChild,
		Workflow:        WorkflowSpawnAgent,
	}, 3)
	require.NoError(t, err)

	seq, err := store.LastAgentControlAgentWakeSeq(ctx, AgentWakeFilter{RootSessionID: "root-session"})
	require.NoError(t, err)
	require.Equal(t, int64(1), seq)

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake, unwatch := store.WatchAgentControlAgentWake(watchCtx, AgentWakeFilter{
		RootSessionID: "root-session",
		AgentPath:     "/root/worker",
	})
	defer unwatch()
	closed, err := store.CloseAgentControlAgentSubtree(ctx, "root-session", "/root/worker", time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), closed)
	select {
	case event := <-wake:
		require.Greater(t, event.Seq, seq)
		require.Equal(t, AgentStatusClosed, event.Status)
		require.Equal(t, "closed", event.EventKind)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for close wake")
	}

	closedSeq, err := store.LastAgentControlAgentWakeSeq(ctx, AgentWakeFilter{
		RootSessionID: "root-session",
		AgentPath:     "/root/worker",
	})
	require.NoError(t, err)
	require.Equal(t, seq+1, closedSeq)
}

func newTestGlobalAgentRegistryStore(t *testing.T) *SQLiteGlobalAgentRegistryStore {
	t.Helper()
	store, err := NewSQLiteGlobalAgentRegistryStore(&GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent-registry.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}
