package knowledge

// Phase 6 切片 5：快照映射与记录器（纯单元；真库路径见 store_sqlite_context_test.go）。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeContextSnapshotStore struct {
	calls   int
	records []ContextSnapshotRecord
	err     error
}

func (f *fakeContextSnapshotStore) RecordContextSnapshot(_ context.Context, rec ContextSnapshotRecord) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.records = append(f.records, rec)
	return nil
}

func compiledItemFixture(ref, target string, verify bool) CompiledItem {
	return CompiledItem{
		ItemType:    "exploration",
		RefID:       ref,
		Target:      target,
		Source:      SourceClassMemory,
		Trust:       TrustCodeIntelligence,
		Version:     "wv1",
		Confidence:  0.95,
		Reason:      ReuseReasonOK,
		Tokens:      120,
		Content:     "remembered exploration summary",
		Explanation: "reused: verified within task scope",
		Verify:      verify,
	}
}

func TestBuildContextSnapshotRecordMapping(t *testing.T) {
	meta := ContextSnapshotMeta{
		SessionID:        "session-1",
		TaskID:           "task-1",
		WorkspaceID:      "workspace-1",
		KnowledgeVersion: "wv1",
		BudgetJSON:       `{"mode":"broad","injected":2}`,
		Now:              time.UnixMilli(1_700_000_000_000),
	}
	result := CompileResult{
		Items: []CompiledItem{
			compiledItemFixture("en_hot", "backend/hot.go", false),
			compiledItemFixture("en_warm", "backend/warm.go", true),
		},
		Reason: PlanReasonOK,
	}

	record := BuildContextSnapshotRecord(meta, "", result)
	require.Equal(t, "session-1", record.SessionID)
	require.Equal(t, "task-1", record.TaskID)
	require.Equal(t, "workspace-1", record.WorkspaceID)
	require.Equal(t, "wv1", record.KnowledgeVersion)
	require.Equal(t, CompileCacheVersion, record.CompilerVersion, "编译器版本缺省取 CompileCacheVersion")
	require.Equal(t, meta.Now, record.CreatedAt)
	require.Len(t, record.Items, 2)
	require.NotEmpty(t, record.ID)
	require.True(t, strings.HasPrefix(record.ID, "cs_"))

	// 每个 item 的可解释性四件套（source/version/trust/reason）+ tier/stale。
	require.Equal(t, "memory", record.Items[0].Source)
	require.Equal(t, "wv1", record.Items[0].Version)
	require.Equal(t, "CODE_INTELLIGENCE", record.Items[0].Trust)
	require.Equal(t, ReuseReasonOK, record.Items[0].Reason)
	require.False(t, record.Items[0].Stale)
	require.Equal(t, ContextItemTierHot, record.Items[0].Tier)
	require.Equal(t, ContextItemTierWarm, record.Items[1].Tier)
	require.Equal(t, "reused: verified within task scope", record.Items[0].Explanation)
	require.True(t, strings.HasPrefix(record.Items[0].ID, "ci_"))

	// 确定性：同内容复算得到同一 id；内容变化（tokens）则变化。
	same := BuildContextSnapshotRecord(meta, "", result)
	require.Equal(t, record.ID, same.ID, "同内容快照 id 必须稳定")
	changed := result
	changed.Items = []CompiledItem{compiledItemFixture("en_hot", "backend/hot.go", false)}
	require.NotEqual(t, record.ID, BuildContextSnapshotRecord(meta, "", changed).ID)
}

func TestContextRecorderSkipsFailAndReadOnly(t *testing.T) {
	ctx := context.Background()
	result := CompileResult{Items: []CompiledItem{compiledItemFixture("en_hot", "backend/hot.go", false)}}
	meta := ContextSnapshotMeta{SessionID: "session-1"}

	t.Run("nil_store_and_zero_items_skip", func(t *testing.T) {
		recorder := NewContextRecorder(nil)
		written, err := recorder.Record(ctx, meta, result)
		require.NoError(t, err)
		require.False(t, written)
		require.EqualValues(t, 1, recorder.Metrics().Skipped)

		store := &fakeContextSnapshotStore{}
		recorder = NewContextRecorder(store)
		written, err = recorder.Record(ctx, meta, CompileResult{})
		require.NoError(t, err)
		require.False(t, written)
		require.Zero(t, store.calls, "无注入条目不得触库")
	})

	t.Run("writes_once_and_is_idempotent_at_store_level", func(t *testing.T) {
		store := &fakeContextSnapshotStore{}
		recorder := NewContextRecorder(store)
		written, err := recorder.Record(ctx, meta, result)
		require.NoError(t, err)
		require.True(t, written)
		require.Equal(t, 1, store.calls)
		require.Len(t, store.records, 1)
		require.EqualValues(t, 1, recorder.Metrics().Recorded)
	})

	t.Run("failure_is_reported_not_swallowed", func(t *testing.T) {
		store := &fakeContextSnapshotStore{err: errors.New("disk full")}
		recorder := NewContextRecorder(store)
		written, err := recorder.Record(ctx, meta, result)
		require.Error(t, err)
		require.False(t, written)
		metrics := recorder.Metrics()
		require.EqualValues(t, 1, metrics.Failed)
		require.Contains(t, metrics.LastError, "disk full")
	})

	t.Run("read_only_disables_sticky", func(t *testing.T) {
		store := &fakeContextSnapshotStore{err: ErrReadOnlyStore}
		recorder := NewContextRecorder(store)
		written, err := recorder.Record(ctx, meta, result)
		require.NoError(t, err, "reader 角色必须静默降级")
		require.False(t, written)
		require.True(t, recorder.Metrics().ReadOnly)

		// 第二次不再触库（不再产生失败噪声）。
		written, err = recorder.Record(ctx, meta, result)
		require.NoError(t, err)
		require.False(t, written)
		require.Equal(t, 1, store.calls, "reader 停用后不得重试")
		require.Zero(t, recorder.Metrics().Failed)
	})
}
