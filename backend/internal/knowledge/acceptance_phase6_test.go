package knowledge

// 本文件把 04 §7.3 的 Phase 6 **硬门槛**变成可复现的自动化用例：
//
//  1. `stale_item_injected = 0`：记录器只记注入条目，`context_items` 中
//     stale=1 的行数必须为 0（表内口径，04 §7.3）；
//  2. `knowledge_version_mismatch_count = 0`：注入条目的版本必须可用且与快照
//     知识版本一致——空版本 / `#pendingN` 在编译期即被判 stale 丢弃；
//  3. 快照写入时延（切片 5 新增的注入路径 IO）：p95 < 50ms（本机进程内近似）。
//
// 缓存命中/未命中 p95 门槛见 cache_test.go `TestCompileCacheLatencyGate`（切片 2），
// 此处不重复。

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func acceptanceReuseItem(nodeID, target, version string, confidence float64) ReuseItem {
	return ReuseItem{
		NodeID:           nodeID,
		NodeType:         NodeTypeFile,
		Target:           target,
		Summary:          "remembered exploration summary for " + target,
		Confidence:       confidence,
		KnowledgeVersion: version,
		Scope:            ReuseScopeTask,
		Reason:           ReuseReasonOK,
	}
}

func TestAcceptancePhase6StaleRowsAndVersionMismatchAreZero(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "knowledge.db"), false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	recorder := NewContextRecorder(store.(ContextSnapshotStore))
	reader := store.(ContextSnapshotReader)

	// 注入候选里混入三类必须被编译期丢弃的条目：不可用版本（#pending）、
	// 空版本、低置信。
	result := CompilePlan(CompileRequest{
		Plan: Plan{
			Reuse: []ReuseItem{
				acceptanceReuseItem("en_ok", "backend/ok.go", "wv1", 0.95),
				acceptanceReuseItem("en_pending", "backend/pending.go", "#pending3", 0.95),
				acceptanceReuseItem("en_empty", "backend/empty.go", "", 0.95),
				acceptanceReuseItem("en_floor", "backend/floor.go", "wv1", 0.10),
			},
			Reason: PlanReasonOK,
		},
		TokenBudget:     800,
		ConfidenceFloor: 0.60,
	})
	require.Len(t, result.Items, 1, "只有版本可用且过置信下限的条目可注入：%#v", result.Items)

	dropped := map[string]int{}
	for _, item := range result.Dropped {
		dropped[item.DropReason]++
	}
	require.Equal(t, 2, dropped[CompileReasonStale], "空版本与 #pending 必须判 stale：%#v", dropped)
	require.Equal(t, 1, dropped[CompileReasonBelowFloor], "低置信必须被下限拦截：%#v", dropped)

	written, err := recorder.Record(ctx, ContextSnapshotMeta{
		SessionID:        "acceptance-session",
		KnowledgeVersion: "wv1",
		BudgetJSON:       `{"mode":"broad"}`,
	}, result)
	require.NoError(t, err)
	require.True(t, written)

	// 门槛 1：表内 stale=1 行数 = 0。
	snapshots, err := reader.ContextSnapshotsBySession(ctx, "acceptance-session", 10)
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	items, err := reader.ContextItemsBySnapshot(ctx, snapshots[0].ID)
	require.NoError(t, err)
	require.Len(t, items, 1, "只记注入条目")
	staleRows := 0
	mismatched := 0
	for _, item := range items {
		if item.Stale {
			staleRows++
		}
		if item.Version != snapshots[0].KnowledgeVersion {
			mismatched++
		}
	}
	require.Equal(t, 0, staleRows, "stale_item_injected 必须为 0（04 §7.3 硬门槛）")
	require.Equal(t, 0, mismatched, "knowledge_version_mismatch_count 必须为 0（04 §7.3 硬门槛）")
}

func TestAcceptancePhase6SnapshotWriteLatency(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "knowledge.db"), false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	recorder := NewContextRecorder(store.(ContextSnapshotStore))

	const samples = 50
	latencies := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		result := CompileResult{Items: []CompiledItem{{
			ItemType:   "exploration",
			RefID:      fmt.Sprintf("en_%d", i),
			Target:     fmt.Sprintf("backend/file_%d.go", i),
			Source:     SourceClassMemory,
			Trust:      TrustCodeIntelligence,
			Version:    "wv1",
			Confidence: 0.95,
			Reason:     ReuseReasonOK,
			Tokens:     120,
			Content:    "remembered exploration summary",
		}}}
		start := time.Now()
		written, err := recorder.Record(ctx, ContextSnapshotMeta{
			SessionID:        fmt.Sprintf("acceptance-session-%d", i),
			KnowledgeVersion: "wv1",
			BudgetJSON:       `{"mode":"broad"}`,
		}, result)
		elapsed := time.Since(start)
		require.NoError(t, err)
		require.True(t, written)
		latencies = append(latencies, elapsed)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[len(latencies)/2]
	p95 := latencies[(len(latencies)*95)/100]
	t.Logf("snapshot write latency: p50=%v p95=%v (n=%d)", p50, p95, samples)
	require.Less(t, p95, 50*time.Millisecond, "快照写入 p95 必须 < 50ms（与 04 §7.4 缓存门槛同量级）")
	require.EqualValues(t, samples, recorder.Metrics().Recorded)
}
