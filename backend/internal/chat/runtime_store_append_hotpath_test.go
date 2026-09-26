package chat

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// P1.6 热路径回归：RETURNING 单语句路径 + prune/vacuum 拆分 + 维护任务。

func newHotpathTestStore(t *testing.T, cfg *RuntimeStoreConfig) *SQLiteRuntimeStore {
	t.Helper()
	if cfg == nil {
		cfg = &RuntimeStoreConfig{}
	}
	if cfg.Path == "" && cfg.DSN == "" {
		cfg.Path = filepath.Join(t.TempDir(), "runtime-hotpath.sqlite")
	}
	store, err := NewSQLiteRuntimeStore(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestAppendEventReturningMatchesLegacySeq(t *testing.T) {
	ctx := context.Background()
	returningStore := newHotpathTestStore(t, nil)
	legacyStore := newHotpathTestStore(t, &RuntimeStoreConfig{DisableSQLiteReturning: true})
	require.NoError(t, returningStore.ensure())
	require.NoError(t, legacyStore.ensure())
	require.True(t, returningStore.SupportsReturning())
	require.False(t, legacyStore.SupportsReturning())

	sessionIDs := []string{"seq-session-a", "seq-session-b"}
	collect := func(store *SQLiteRuntimeStore) map[string][]int64 {
		result := map[string][]int64{}
		for index := 1; index <= 100; index++ {
			sessionID := sessionIDs[index%2]
			seq, err := store.AppendEvent(ctx, runtimeevents.Event{
				Type:      "seq-check",
				SessionID: sessionID,
				Payload:   map[string]interface{}{"index": index},
			})
			require.NoError(t, err)
			result[sessionID] = append(result[sessionID], seq)
		}
		return result
	}

	returningSequences := collect(returningStore)
	legacySequences := collect(legacyStore)
	require.Equal(t, returningSequences, legacySequences, "RETURNING 与 legacy 路径的 seq 序列必须一致")
	for _, sessionID := range sessionIDs {
		sequences := returningSequences[sessionID]
		require.Len(t, sequences, 50)
		for index, seq := range sequences {
			require.Equal(t, int64(index+1), seq, "seq 必须从 1 连续递增")
		}
	}
}

func TestAppendEventReturningConcurrentInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-concurrent.sqlite")
	first := newHotpathTestStore(t, &RuntimeStoreConfig{Path: path})
	second := newHotpathTestStore(t, &RuntimeStoreConfig{Path: path})
	// 先让第一个实例完成迁移，再并发写入（避免两个首次打开同时迁移）。
	require.NoError(t, first.ensure())

	const perStore = 40
	seqs := make(chan int64, perStore*2)
	errs := make(chan error, perStore*2)
	var wait sync.WaitGroup
	for _, store := range []*SQLiteRuntimeStore{first, second} {
		wait.Add(1)
		go func(store *SQLiteRuntimeStore) {
			defer wait.Done()
			for index := 0; index < perStore; index++ {
				seq, err := store.AppendEvent(context.Background(), runtimeevents.Event{
					Type:      "concurrent",
					SessionID: "concurrent-session",
					Payload:   map[string]interface{}{"index": index},
				})
				if err != nil {
					errs <- err
					return
				}
				seqs <- seq
			}
		}(store)
	}
	wait.Wait()
	close(errs)
	close(seqs)
	for err := range errs {
		require.NoError(t, err)
	}
	seen := map[int64]bool{}
	for seq := range seqs {
		require.False(t, seen[seq], "duplicate seq %d across instances", seq)
		seen[seq] = true
	}
	require.Len(t, seen, perStore*2)
	require.Greater(t, first.AppendTimingStats().ReturningUsed+second.AppendTimingStats().ReturningUsed, int64(0))
}

func TestAppendEventReturningProbeFallback(t *testing.T) {
	ctx := context.Background()
	store := newHotpathTestStore(t, nil)
	require.NoError(t, store.ensure())
	require.True(t, store.SupportsReturning())

	// 伪造探测失败：必须自动走 legacy 且计数。
	store.supportsReturning = false
	for index := 1; index <= 3; index++ {
		seq, err := store.AppendEvent(ctx, runtimeevents.Event{
			Type:      "fallback",
			SessionID: "fallback-session",
			Payload:   map[string]interface{}{"index": index},
		})
		require.NoError(t, err)
		require.Equal(t, int64(index), seq)
	}
	stats := store.AppendTimingStats()
	require.Equal(t, int64(3), stats.LegacyUsed)
	require.Zero(t, stats.ReturningUsed)
}

func TestSQLiteVersionAtLeast(t *testing.T) {
	require.True(t, sqliteVersionAtLeast("3.51.3", "3.35.0"))
	require.True(t, sqliteVersionAtLeast("3.35.0", "3.35.0"))
	require.False(t, sqliteVersionAtLeast("3.34.9", "3.35.0"))
	require.False(t, sqliteVersionAtLeast("2.9.9", "3.35.0"))
	require.True(t, sqliteVersionAtLeast("3.51.3-dev", "3.35.0"))
	require.False(t, sqliteVersionAtLeast("garbage", "3.35.0"))
}

func TestPruneRuntimeRowsKeepsDeleteInTransaction(t *testing.T) {
	ctx := context.Background()
	store := newHotpathTestStore(t, &RuntimeStoreConfig{
		EventRetention: 2,
		PruneInterval:  2,
	})
	for index := 1; index <= 4; index++ {
		seq, err := store.AppendEvent(ctx, runtimeevents.Event{
			Type:      "prune",
			SessionID: "prune-session",
			Payload:   map[string]interface{}{"index": index},
		})
		require.NoError(t, err)
		require.Equal(t, int64(index), seq)
	}

	stats := store.AppendTimingStats()
	require.GreaterOrEqual(t, stats.PruneRuns, int64(1))
	// 页回收（incremental_vacuum）已整体移除：prune 只做 DELETE，写事务内
	// 不再有任何在线页回收，删掉的行留在 freelist 由离线 compaction 回收。
	var remaining int64
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_events WHERE session_id = ?`, "prune-session").Scan(&remaining))
	require.Equal(t, int64(2), remaining)
}

func TestAppendTimingStats(t *testing.T) {
	ctx := context.Background()
	store := newHotpathTestStore(t, nil)
	for index := 0; index < 5; index++ {
		_, err := store.AppendEvent(ctx, runtimeevents.Event{Type: "stats", SessionID: "stats-session"})
		require.NoError(t, err)
	}
	stats := store.AppendTimingStats()
	require.Equal(t, int64(5), stats.Appends)
	require.Equal(t, int64(5), stats.ReturningUsed)
	require.Zero(t, stats.LegacyUsed)
	require.Greater(t, stats.TotalNs, int64(0))
	require.Greater(t, stats.MaxNs, int64(0))
	require.Greater(t, stats.LockHoldNs, int64(0))
	require.True(t, stats.SupportsReturning)
	require.NotEmpty(t, stats.SQLiteVersion)
}

func TestAppendSQLPathGolden(t *testing.T) {
	ctx := context.Background()
	var (
		mu    sync.Mutex
		paths []string
	)
	runtimeAppendSQLHook = func(path string) {
		mu.Lock()
		paths = append(paths, path)
		mu.Unlock()
	}
	t.Cleanup(func() { runtimeAppendSQLHook = nil })

	returningStore := newHotpathTestStore(t, nil)
	_, err := returningStore.AppendEvent(ctx, runtimeevents.Event{Type: "golden", SessionID: "golden-returning"})
	require.NoError(t, err)
	mu.Lock()
	require.Equal(t, []string{"returning"}, paths)
	paths = nil
	mu.Unlock()

	legacyStore := newHotpathTestStore(t, &RuntimeStoreConfig{DisableSQLiteReturning: true})
	_, err = legacyStore.AppendEvent(ctx, runtimeevents.Event{Type: "golden", SessionID: "golden-legacy"})
	require.NoError(t, err)
	mu.Lock()
	require.Equal(t, []string{"legacy-select", "legacy-insert"}, paths)
	mu.Unlock()
}

func BenchmarkAppendEventReturning(b *testing.B) {
	benchmarkAppendEventPath(b, false)
}

func BenchmarkAppendEventLegacy(b *testing.B) {
	benchmarkAppendEventPath(b, true)
}

func benchmarkAppendEventPath(b *testing.B, legacy bool) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		Path:                         filepath.Join(b.TempDir(), "runtime-bench.sqlite"),
		DisableBackgroundMaintenance: true,
		DisableSQLiteReturning:       legacy,
	})
	if err != nil {
		b.Fatalf("create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	var statements atomic.Int64
	runtimeAppendSQLHook = func(string) { statements.Add(1) }
	defer func() { runtimeAppendSQLHook = nil }()

	ctx := context.Background()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := store.AppendEvent(ctx, runtimeevents.Event{
			Type:      "bench",
			SessionID: "bench-session",
			Payload:   map[string]interface{}{"index": index},
		}); err != nil {
			b.Fatalf("append event: %v", err)
		}
	}
	b.StopTimer()
	stats := store.AppendTimingStats()
	b.ReportMetric(float64(stats.LockHoldNs)/float64(b.N), "lock_hold_ns/op")
	b.ReportMetric(float64(statements.Load())/float64(b.N), "stmts/op")
}
