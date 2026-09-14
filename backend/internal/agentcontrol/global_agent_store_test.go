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
		AgentID:                  "child-agent",
		RootSessionID:            "root-session",
		ParentAgentID:            root.AgentID,
		ParentSessionID:          "root-session",
		SessionID:                "child-session",
		AgentPath:                "/root/child-agent",
		Depth:                    1,
		AgentType:                AgentTypeChild,
		Nickname:                 "worker",
		Workflow:                 WorkflowSpawnAgent,
		Provider:                 "remote",
		Model:                    "strong-model",
		ReasoningEffort:          "high",
		Difficulty:               "hard",
		DifficultySource:         "explicit",
		RouteSource:              "difficulty_level",
		RouteWarnings:            []string{"provider_fallback_parent"},
		FallbackUsed:             true,
		FallbackReason:           "provider_unresolved_parent",
		RequestedProvider:        "requested-provider",
		EffectiveProvider:        "remote",
		RequestedModel:           "requested-model",
		EffectiveModel:           "strong-model",
		RequestedReasoningEffort: "xhigh",
		EffectiveReasoningEffort: "high",
		RequestedPermissionMode:  "plan",
		EffectivePermissionMode:  "bypass_permissions",
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
	require.Equal(t, "remote", records[1].Provider)
	require.Equal(t, "strong-model", records[1].Model)
	require.Equal(t, "high", records[1].ReasoningEffort)
	require.Equal(t, "hard", records[1].Difficulty)
	require.Equal(t, "explicit", records[1].DifficultySource)
	require.Equal(t, "difficulty_level", records[1].RouteSource)
	require.Equal(t, []string{"provider_fallback_parent"}, records[1].RouteWarnings)
	require.True(t, records[1].FallbackUsed)
	require.Equal(t, "provider_unresolved_parent", records[1].FallbackReason)
	require.Equal(t, "requested-provider", records[1].RequestedProvider)
	require.Equal(t, "remote", records[1].EffectiveProvider)
	require.Equal(t, "requested-model", records[1].RequestedModel)
	require.Equal(t, "strong-model", records[1].EffectiveModel)
	require.Equal(t, "xhigh", records[1].RequestedReasoningEffort)
	require.Equal(t, "high", records[1].EffectiveReasoningEffort)
	require.Equal(t, "plan", records[1].RequestedPermissionMode)
	require.Equal(t, "bypass_permissions", records[1].EffectivePermissionMode)

	records, err = store.ListAgentControlAgents(ctx, AgentFilter{
		RootSessionID: "root-session",
		PathPrefix:    "/root/child-agent",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "child-session", records[0].SessionID)
}

func TestSQLiteGlobalAgentRegistryStorePathPrefixMatchesOnlySubtree(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	for _, record := range []AgentRecord{
		{AgentID: "child-1", RootSessionID: "root-session", SessionID: "session-1", AgentPath: "/root/child"},
		{AgentID: "nested", RootSessionID: "root-session", SessionID: "session-nested", AgentPath: "/root/child/nested"},
		{AgentID: "child-10", RootSessionID: "root-session", SessionID: "session-10", AgentPath: "/root/child-10"},
	} {
		_, err := store.UpsertAgentControlAgent(ctx, record)
		require.NoError(t, err)
	}

	records, err := store.ListAgentControlAgents(ctx, AgentFilter{
		RootSessionID: "root-session",
		PathPrefix:    "/root/child",
	})
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, []string{"child-1", "nested"}, []string{records[0].AgentID, records[1].AgentID})
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

func TestSQLiteGlobalAgentRegistryStoreMarksSubtreeStale(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	for _, record := range []AgentRecord{
		{AgentID: "root", RootSessionID: "root-session", SessionID: "root-session", AgentPath: "/root", AgentType: AgentTypeRoot},
		{AgentID: "worker", RootSessionID: "root-session", ParentAgentID: "root", SessionID: "worker-session", AgentPath: "/root/worker", AgentType: AgentTypeChild},
		{AgentID: "nested", RootSessionID: "root-session", ParentAgentID: "worker", SessionID: "nested-session", AgentPath: "/root/worker/nested", AgentType: AgentTypeChild},
	} {
		_, err := store.UpsertAgentControlAgent(ctx, record)
		require.NoError(t, err)
	}

	count, err := store.MarkAgentControlAgentSubtreeStale(ctx, "root-session", "/root/worker", time.Unix(40, 0).UTC())
	require.NoError(t, err)
	require.Equal(t, int64(2), count)

	active, err := store.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: "root-session"})
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "root", active[0].AgentID)

	all, err := store.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: "root-session", IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Equal(t, AgentStatusStale, all[1].Status)
	require.Equal(t, AgentStatusStale, all[2].Status)
	require.True(t, all[1].Closed())
	require.NotNil(t, all[1].ClosedAt)
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
		AgentID:         "child-10",
		RootSessionID:   "root-session",
		ParentAgentID:   "root",
		ParentSessionID: "root-session",
		SessionID:       "child-10-session",
		AgentPath:       "/root/child-10",
		Depth:           1,
		AgentType:       AgentTypeChild,
	})
	require.NoError(t, err)
	select {
	case event := <-wake:
		t.Fatalf("sibling prefix row should not match child subtree wake filter: %#v", event)
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

// TestSQLiteGlobalAgentRegistryStoreReleaseSpawnFreesThreadSlot covers the P0-3
// compensation path: a reservation that never became runnable must not keep
// counting against max_threads.
func TestSQLiteGlobalAgentRegistryStoreReleaseSpawnFreesThreadSlot(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	root := AgentRecord{
		AgentID:       "root",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
	}
	child := AgentRecord{
		AgentID:         "child-1",
		RootSessionID:   "root-session",
		ParentAgentID:   "root",
		ParentSessionID: "root-session",
		SessionID:       "child-session-1",
		AgentPath:       "/root/child-1",
		Depth:           1,
		AgentType:       AgentTypeChild,
		Workflow:        WorkflowSpawnAgent,
	}
	reserved, err := store.ReserveAgentControlAgentSpawn(ctx, root, child, 1)
	require.NoError(t, err)
	require.Equal(t, AgentStatusActive, reserved.Status)

	blocked := child
	blocked.AgentID = "child-2"
	blocked.SessionID = "child-session-2"
	blocked.AgentPath = "/root/child-2"
	_, err = store.ReserveAgentControlAgentSpawn(ctx, root, blocked, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "agent spawn thread limit reached")

	released, err := store.ReleaseAgentControlAgentSpawn(ctx, child.AgentID, "queue child prompt: actor refused")
	require.NoError(t, err)
	require.Equal(t, AgentStatusStale, released.Status)
	require.True(t, released.Closed())

	// The slot must be available again once the reservation is released.
	retry := child
	retry.AgentID = "child-3"
	retry.SessionID = "child-session-3"
	retry.AgentPath = "/root/child-3"
	_, err = store.ReserveAgentControlAgentSpawn(ctx, root, retry, 1)
	require.NoError(t, err)

	// Releasing an already terminal row stays a no-op, and unknown ids are
	// tolerated so the caller never fails while rolling back.
	again, err := store.ReleaseAgentControlAgentSpawn(ctx, child.AgentID, "second release")
	require.NoError(t, err)
	require.Equal(t, AgentStatusStale, again.Status)
	_, err = store.ReleaseAgentControlAgentSpawn(ctx, "missing-agent", "noop")
	require.NoError(t, err)
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

// 终态行不可被宿主投影复活：投影按会话存储重写 registry，若一次扫描读到释放前的
// 旧快照，就会把刚释放的 spawn 预约写回 active，继续占用 agents.maxThreads。
// 守卫在 upsert SQL 内判定；重新绑定仍走预约事务，不受影响。
func TestUpsertAgentControlAgentKeepsTerminalStateAgainstProjection(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	record := AgentRecord{
		AgentID:       "resurrect-child",
		RootSessionID: "session-resurrect",
		SessionID:     "resurrect-child",
		AgentPath:     "/root/resurrect-child",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	}
	_, err := store.UpsertAgentControlAgent(ctx, record)
	require.NoError(t, err)

	_, err = store.MarkAgentControlAgentSubtreeStale(ctx, record.RootSessionID, record.AgentPath, time.Now().UTC())
	require.NoError(t, err)

	projected := record
	projected.Status = AgentStatusActive
	projected.Model = "projection-model"
	_, err = store.UpsertAgentControlAgent(ctx, projected)
	require.NoError(t, err)

	rows, err := store.ListAgentControlAgents(ctx, AgentFilter{AgentID: record.AgentID, IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Closed(), "active 投影不得把终态行写回 active")
	require.Equal(t, AgentStatusStale, rows[0].Status)
	require.NotNil(t, rows[0].ClosedAt)
	require.Empty(t, rows[0].Model, "被守卫挡下的投影不应改写终态行")

	// 预约路径（事务 upsert）仍可重新绑定同一身份：守卫只约束非事务投影写入。
	_, err = store.ReserveAgentControlAgentSpawn(ctx, AgentRecord{
		AgentID:       "root:session-resurrect",
		RootSessionID: record.RootSessionID,
		SessionID:     record.RootSessionID,
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	}, projected, 8)
	require.NoError(t, err)

	rows, err = store.ListAgentControlAgents(ctx, AgentFilter{AgentID: record.AgentID, IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.False(t, rows[0].Closed(), "预约重新绑定必须仍能激活同一身份")
}
