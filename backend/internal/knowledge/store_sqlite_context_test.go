package knowledge

// Phase 6 切片 5：context_snapshots / context_items 真库读写。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestContextSnapshotStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "knowledge.db")
	store, err := OpenStore(ctx, path, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	snapshotStore, ok := store.(ContextSnapshotStore)
	require.True(t, ok, "sqliteStore 必须满足 ContextSnapshotStore")

	result := CompileResult{
		Items: []CompiledItem{
			compiledItemFixture("en_hot", "backend/hot.go", false),
			compiledItemFixture("en_warm", "backend/warm.go", true),
		},
		Reason: PlanReasonOK,
	}
	meta := ContextSnapshotMeta{
		SessionID:        "session-1",
		TaskID:           "task-1",
		WorkspaceID:      "workspace-1",
		KnowledgeVersion: "wv1",
		BudgetJSON:       `{"mode":"broad","injected":2,"dropped":{"below_floor":1}}`,
		Now:              time.UnixMilli(1_700_000_000_000),
	}
	record := BuildContextSnapshotRecord(meta, CompileCacheVersion, result)
	require.NoError(t, snapshotStore.RecordContextSnapshot(ctx, record))

	// 幂等：重复记录同一次编译不产生重复行。
	require.NoError(t, snapshotStore.RecordContextSnapshot(ctx, record))

	snapshots, err := store.(*sqliteStore).ContextSnapshotsBySession(ctx, "session-1", 10)
	require.NoError(t, err)
	require.Len(t, snapshots, 1, "同内容快照必须幂等")
	require.Equal(t, record.ID, snapshots[0].ID)
	require.Equal(t, "task-1", snapshots[0].TaskID)
	require.Equal(t, "workspace-1", snapshots[0].WorkspaceID)
	require.Equal(t, "wv1", snapshots[0].KnowledgeVersion)
	require.Equal(t, CompileCacheVersion, snapshots[0].CompilerVersion)
	require.Contains(t, snapshots[0].BudgetJSON, "below_floor")
	require.Equal(t, meta.Now, snapshots[0].CreatedAt)

	items, err := store.(*sqliteStore).ContextItemsBySnapshot(ctx, record.ID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "memory", items[0].Source)
	require.Equal(t, "wv1", items[0].Version)
	require.Equal(t, "CODE_INTELLIGENCE", items[0].Trust)
	require.Equal(t, ReuseReasonOK, items[0].Reason)
	require.False(t, items[0].Stale, "注入条目 stale 必须为 0（stale_item_injected=0 的表内口径）")
	require.Equal(t, ContextItemTierHot, items[0].Tier)
	require.InDelta(t, 0.95, items[0].Confidence, 1e-9)
	require.NotEmpty(t, items[0].Explanation)
	warm := items[1]
	require.Equal(t, ContextItemTierWarm, warm.Tier)
	require.True(t, warm.Provisional == false)
	require.NotEmpty(t, warm.RefID)
}

func TestContextSnapshotStoreReadOnlyAndSessionScoping(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "knowledge.db")
	store, err := OpenStore(ctx, path, false)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	// owner 关闭后以 reader 打开：读可用、写硬失败。
	reader, err := OpenStore(ctx, path, true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	readerStore, ok := reader.(*sqliteStore)
	require.True(t, ok)

	// 先由 owner 写两条（不同会话），再验证会话隔离。
	owner, err := OpenStore(ctx, path, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = owner.Close() })
	ownerStore := owner.(*sqliteStore)
	writeSnapshot := func(sessionID string, now time.Time) ContextSnapshotRecord {
		meta := ContextSnapshotMeta{SessionID: sessionID, KnowledgeVersion: "wv1", Now: now}
		rec := BuildContextSnapshotRecord(meta, CompileCacheVersion, CompileResult{
			Items: []CompiledItem{compiledItemFixture("en_"+sessionID, "backend/"+sessionID+".go", false)},
		})
		require.NoError(t, ownerStore.RecordContextSnapshot(ctx, rec))
		return rec
	}
	first := writeSnapshot("session-a", time.UnixMilli(1_700_000_000_000))
	writeSnapshot("session-b", time.UnixMilli(1_700_000_001_000))

	// reader 可读。
	snapshots, err := readerStore.ContextSnapshotsBySession(ctx, "session-a", 10)
	require.NoError(t, err)
	require.Len(t, snapshots, 1, "会话隔离：只返回本会话快照")
	require.Equal(t, first.ID, snapshots[0].ID)
	items, err := readerStore.ContextItemsBySnapshot(ctx, first.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)

	// reader 写硬失败（ContextRecorder 会据此静默停用）。
	meta := ContextSnapshotMeta{SessionID: "session-a", KnowledgeVersion: "wv1"}
	rec := BuildContextSnapshotRecord(meta, CompileCacheVersion, CompileResult{
		Items: []CompiledItem{compiledItemFixture("en_x", "backend/x.go", false)},
	})
	err = readerStore.RecordContextSnapshot(ctx, rec)
	require.ErrorIs(t, err, ErrReadOnlyStore)
}
