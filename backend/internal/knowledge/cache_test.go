package knowledge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Phase 6 切片 2：compile 层缓存（cache_entries）。窄接口 + 假体 + 真库两套。

// fakeCacheStore 是 CompileCacheStore 的内存假体。
type fakeCacheStore struct {
	entries map[string]CacheEntry
	getErr  error
	putErr  error
	deletes int
}

func newFakeCacheStore() *fakeCacheStore {
	return &fakeCacheStore{entries: map[string]CacheEntry{}}
}

func (f *fakeCacheStore) key(workspaceID, cacheType, cacheKey string) string {
	return workspaceID + "\x00" + cacheType + "\x00" + cacheKey
}

func (f *fakeCacheStore) GetCacheEntry(_ context.Context, workspaceID, cacheType, cacheKey string) (CacheEntry, bool, error) {
	if f.getErr != nil {
		return CacheEntry{}, false, f.getErr
	}
	entry, ok := f.entries[f.key(workspaceID, cacheType, cacheKey)]
	return entry, ok, nil
}

func (f *fakeCacheStore) PutCacheEntry(_ context.Context, entry CacheEntry) error {
	if f.putErr != nil {
		return f.putErr
	}
	if entry.ID == "" {
		entry.ID = CacheEntryID(entry.WorkspaceID, entry.CacheType, entry.CacheKey)
	}
	f.entries[f.key(entry.WorkspaceID, entry.CacheType, entry.CacheKey)] = entry
	return nil
}

func (f *fakeCacheStore) DeleteCacheEntry(_ context.Context, workspaceID, cacheType, cacheKey string) error {
	f.deletes++
	delete(f.entries, f.key(workspaceID, cacheType, cacheKey))
	return nil
}

func (f *fakeCacheStore) PurgeExpiredCacheEntries(_ context.Context, now time.Time, limit int) (int, error) {
	removed := 0
	for key, entry := range f.entries {
		if limit > 0 && removed >= limit {
			break
		}
		if !entry.ExpiresAt.IsZero() && !now.Before(entry.ExpiresAt) {
			delete(f.entries, key)
			removed++
		}
	}
	return removed, nil
}

// 缓存键：确定性 + 对每个输入维度敏感（含知识版本与编译器版本）。
func TestCompileCacheKeyDeterministicAndSensitive(t *testing.T) {
	base := CompileCacheKeyInput{
		WorkspaceID:      "w_1",
		TaskID:           "t_1",
		SessionID:        "s_1",
		Query:            "explain auth flow",
		Scope:            "task",
		Write:            false,
		Mode:             "broad",
		TokenBudget:      800,
		ConfidenceFloor:  0.5,
		KnowledgeVersion: "wv1:abc",
	}
	first := CompileCacheKey(base)
	require.Equal(t, first, CompileCacheKey(base), "同输入必须同键")
	require.Equal(t, first, CompileCacheKey(CompileCacheKeyInput{
		WorkspaceID: " w_1 ", TaskID: " t_1 ", SessionID: " s_1 ",
		Query: " explain auth flow ", Scope: " TASK ", Mode: " BROAD ",
		TokenBudget: 800, ConfidenceFloor: 0.5, KnowledgeVersion: "wv1:abc",
	}), "归一化后同键（trim + lower）")

	mutations := map[string]func(*CompileCacheKeyInput){
		"workspace": func(in *CompileCacheKeyInput) { in.WorkspaceID = "w_2" },
		"task":      func(in *CompileCacheKeyInput) { in.TaskID = "t_2" },
		"session":   func(in *CompileCacheKeyInput) { in.SessionID = "s_2" },
		"query":     func(in *CompileCacheKeyInput) { in.Query = "explain other flow" },
		"scope":     func(in *CompileCacheKeyInput) { in.Scope = "cross_task" },
		"write":     func(in *CompileCacheKeyInput) { in.Write = true },
		"mode":      func(in *CompileCacheKeyInput) { in.Mode = "signals" },
		"budget":    func(in *CompileCacheKeyInput) { in.TokenBudget = 400 },
		"floor":     func(in *CompileCacheKeyInput) { in.ConfidenceFloor = 0.8 },
		"version":   func(in *CompileCacheKeyInput) { in.KnowledgeVersion = "wv1:def" },
		"pending":   func(in *CompileCacheKeyInput) { in.KnowledgeVersion = "wv1:abc#pending1" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			in := base
			mutate(&in)
			require.NotEqual(t, first, CompileCacheKey(in), "%s 变化必须改变键", name)
		})
	}
}

// 命中路径不再执行编译内核；结果语义等价且标记 CacheHit。
func TestCompileCacheHitAvoidsRecompile(t *testing.T) {
	ctx := context.Background()
	store := newFakeCacheStore()
	cache := NewCompileCache(store)
	calls := 0
	cache.Compile = func(req CompileRequest) CompileResult {
		calls++
		return CompilePlan(req)
	}
	key := CompileCacheKeyInput{WorkspaceID: "w_1", Query: "explain auth flow", Mode: "broad", KnowledgeVersion: "wv1:abc"}
	req := CompileRequest{Plan: Plan{
		Reuse:  []ReuseItem{reuseFixture("n1", "pkg/a.go", 0.9, "wv1:abc", ReuseReasonOK)},
		Reason: PlanReasonOK,
	}}

	first := cache.Do(ctx, key, req)
	require.False(t, first.CacheHit, "首次必须未命中")
	require.Equal(t, 1, calls)
	require.Len(t, first.Items, 1)

	second := cache.Do(ctx, key, req)
	require.True(t, second.CacheHit, "第二次必须命中")
	require.Equal(t, 1, calls, "命中不得再次调用编译内核")
	require.Equal(t, first.Items, second.Items, "命中结果语义等价")
	require.Equal(t, first.Reason, second.Reason)

	snapshot := cache.Metrics.Snapshot()
	require.Equal(t, int64(1), snapshot.Hits)
	require.Equal(t, int64(1), snapshot.Misses)
	require.Equal(t, int64(0), snapshot.Errors)
	require.InDelta(t, 0.5, snapshot.HitRate, 0.0001)
}

// 缓存故障一律降级直算（Degrade-Not-Fail），绝不冒泡错误。
func TestCompileCacheDegradesOnStoreFailure(t *testing.T) {
	ctx := context.Background()
	req := CompileRequest{Plan: Plan{Reuse: []ReuseItem{reuseFixture("n1", "pkg/a.go", 0.9, "wv1", ReuseReasonOK)}}}
	key := CompileCacheKeyInput{WorkspaceID: "w_1", Query: "q", KnowledgeVersion: "wv1"}

	t.Run("get_error", func(t *testing.T) {
		store := newFakeCacheStore()
		store.getErr = errors.New("boom")
		cache := NewCompileCache(store)
		result := cache.Do(ctx, key, req)
		require.Len(t, result.Items, 1, "读失败仍必须直算产出")
		require.False(t, result.CacheHit)
		require.Equal(t, int64(1), cache.Metrics.Snapshot().Errors)
	})

	t.Run("put_error", func(t *testing.T) {
		store := newFakeCacheStore()
		store.putErr = errors.New("boom")
		cache := NewCompileCache(store)
		result := cache.Do(ctx, key, req)
		require.Len(t, result.Items, 1, "写失败仍必须返回结果")
		require.Equal(t, int64(1), cache.Metrics.Snapshot().Errors)
	})

	t.Run("corrupt_payload", func(t *testing.T) {
		store := newFakeCacheStore()
		cache := NewCompileCache(store)
		cacheKey := CompileCacheKey(key)
		require.NoError(t, store.PutCacheEntry(ctx, CacheEntry{
			WorkspaceID: "w_1", CacheKey: cacheKey, CacheType: CacheTypeCompile,
			PayloadJSON: "{not json", KnowledgeVersion: "wv1",
		}))
		result := cache.Do(ctx, key, req)
		require.Len(t, result.Items, 1)
		require.False(t, result.CacheHit)
		require.Equal(t, int64(1), cache.Metrics.Snapshot().Errors)
	})

	t.Run("nil_store_and_empty_workspace", func(t *testing.T) {
		cache := NewCompileCache(nil)
		result := cache.Do(ctx, key, req)
		require.Len(t, result.Items, 1)
		require.False(t, result.CacheHit)

		store := newFakeCacheStore()
		other := NewCompileCache(store)
		result = other.Do(ctx, CompileCacheKeyInput{Query: "q"}, req)
		require.Len(t, result.Items, 1)
		snapshot := other.Metrics.Snapshot()
		require.Zero(t, snapshot.Hits+snapshot.Misses, "无缓存可用时不污染命中率口径")
	})
}

// 版本守卫与过期守卫：不符/过期一律未命中，并尽力清理。
func TestCompileCacheVersionGuardAndExpiry(t *testing.T) {
	ctx := context.Background()
	req := CompileRequest{Plan: Plan{Reuse: []ReuseItem{reuseFixture("n1", "pkg/a.go", 0.9, "wv2", ReuseReasonOK)}}}

	t.Run("version_mismatch", func(t *testing.T) {
		store := newFakeCacheStore()
		cache := NewCompileCache(store)
		key := CompileCacheKeyInput{WorkspaceID: "w_1", Query: "q", KnowledgeVersion: "wv1"}
		require.NoError(t, store.PutCacheEntry(ctx, CacheEntry{
			WorkspaceID: "w_1", CacheKey: CompileCacheKey(key), CacheType: CacheTypeCompile,
			PayloadJSON: `{"reason":"ok"}`, KnowledgeVersion: "wv0",
		}))
		result := cache.Do(ctx, key, req)
		require.False(t, result.CacheHit, "条目版本不符必须未命中")
		require.GreaterOrEqual(t, store.deletes, 1, "不符条目应被清理")
	})

	t.Run("expired", func(t *testing.T) {
		store := newFakeCacheStore()
		cache := NewCompileCache(store)
		now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		cache.Now = func() time.Time { return now }
		key := CompileCacheKeyInput{WorkspaceID: "w_1", Query: "q", KnowledgeVersion: "wv1"}
		require.NoError(t, store.PutCacheEntry(ctx, CacheEntry{
			WorkspaceID: "w_1", CacheKey: CompileCacheKey(key), CacheType: CacheTypeCompile,
			PayloadJSON: `{"reason":"ok"}`, KnowledgeVersion: "wv1",
			ExpiresAt: now.Add(-time.Second),
		}))
		result := cache.Do(ctx, key, req)
		require.False(t, result.CacheHit, "过期条目必须未命中")
		require.GreaterOrEqual(t, store.deletes, 1, "过期条目应被清理")
	})
}

// 指标：计数、命中率、最近邻分位。
func TestCompileCacheMetricsPercentiles(t *testing.T) {
	metrics := NewCompileCacheMetrics()
	metrics.recordHit(10 * time.Millisecond)
	metrics.recordHit(20 * time.Millisecond)
	metrics.recordHit(30 * time.Millisecond)
	metrics.recordMiss(100 * time.Millisecond)
	metrics.RecordError()

	snapshot := metrics.Snapshot()
	require.Equal(t, int64(3), snapshot.Hits)
	require.Equal(t, int64(1), snapshot.Misses)
	require.Equal(t, int64(1), snapshot.Errors)
	require.InDelta(t, 0.75, snapshot.HitRate, 0.0001)
	require.InDelta(t, 20.0, snapshot.HitP50MS, 0.0001)
	require.InDelta(t, 30.0, snapshot.HitP95MS, 0.0001)
	require.InDelta(t, 100.0, snapshot.MissP95MS, 0.0001)

	empty := NewCompileCacheMetrics().Snapshot()
	require.Zero(t, empty.HitRate)
	require.Zero(t, empty.HitP95MS)
}

// 真库往返：upsert / 删除 / 过期清理 / reader 角色写入拒绝。
func TestSQLiteCompileCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "knowledge.db")
	store, err := OpenStore(ctx, path, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	cacheStore, ok := store.(CompileCacheStore)
	require.True(t, ok, "*sqliteStore 必须实现 CompileCacheStore")

	entry := CacheEntry{
		WorkspaceID:      "w_1",
		CacheKey:         "key-1",
		CacheType:        CacheTypeCompile,
		PayloadJSON:      `{"reason":"ok","items":[]}`,
		KnowledgeVersion: "wv1:abc",
		CreatedAt:        time.Now(),
		ExpiresAt:        time.Now().Add(time.Minute),
	}
	require.NoError(t, cacheStore.PutCacheEntry(ctx, entry))

	got, found, err := cacheStore.GetCacheEntry(ctx, "w_1", CacheTypeCompile, "key-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, CacheEntryID("w_1", CacheTypeCompile, "key-1"), got.ID)
	require.Equal(t, entry.PayloadJSON, got.PayloadJSON)
	require.Equal(t, "wv1:abc", got.KnowledgeVersion)
	require.WithinDuration(t, entry.ExpiresAt, got.ExpiresAt, time.Second)

	// upsert：同键覆盖载荷。
	entry.PayloadJSON = `{"reason":"updated","items":[]}`
	require.NoError(t, cacheStore.PutCacheEntry(ctx, entry))
	got, found, err = cacheStore.GetCacheEntry(ctx, "w_1", CacheTypeCompile, "key-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, `{"reason":"updated","items":[]}`, got.PayloadJSON)

	// 未命中：不存在的键不是错误。
	_, found, err = cacheStore.GetCacheEntry(ctx, "w_1", CacheTypeCompile, "missing")
	require.NoError(t, err)
	require.False(t, found)

	// 过期清理：过期条目被清，NULL 过期与未过期条目保留。
	require.NoError(t, cacheStore.PutCacheEntry(ctx, CacheEntry{
		WorkspaceID: "w_1", CacheKey: "key-expired", CacheType: CacheTypeCompile,
		PayloadJSON: `{"reason":"old"}`, KnowledgeVersion: "wv1",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(-time.Minute),
	}))
	require.NoError(t, cacheStore.PutCacheEntry(ctx, CacheEntry{
		WorkspaceID: "w_1", CacheKey: "key-forever", CacheType: CacheTypeCompile,
		PayloadJSON: `{"reason":"forever"}`, KnowledgeVersion: "wv1",
		CreatedAt: time.Now(),
	}))
	removed, err := cacheStore.PurgeExpiredCacheEntries(ctx, time.Now(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	_, found, err = cacheStore.GetCacheEntry(ctx, "w_1", CacheTypeCompile, "key-expired")
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = cacheStore.GetCacheEntry(ctx, "w_1", CacheTypeCompile, "key-forever")
	require.NoError(t, err)
	require.True(t, found, "NULL 过期时间 = 永不过期")
	_, found, err = cacheStore.GetCacheEntry(ctx, "w_1", CacheTypeCompile, "key-1")
	require.NoError(t, err)
	require.True(t, found)

	// 删除幂等。
	require.NoError(t, cacheStore.DeleteCacheEntry(ctx, "w_1", CacheTypeCompile, "key-1"))
	require.NoError(t, cacheStore.DeleteCacheEntry(ctx, "w_1", CacheTypeCompile, "key-1"))

	// reader 角色：读可用、写硬失败（不静默）。
	reader, err := OpenStore(ctx, path, true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	readerCache, ok := reader.(CompileCacheStore)
	require.True(t, ok)
	_, found, err = readerCache.GetCacheEntry(ctx, "w_1", CacheTypeCompile, "key-forever")
	require.NoError(t, err)
	require.True(t, found, "reader 可读缓存")
	err = readerCache.PutCacheEntry(ctx, entry)
	require.ErrorIs(t, err, ErrReadOnlyStore, "reader 写缓存必须硬失败")
}

// 门槛复现（04 §5 Phase 6）：真库 + 真编译，命中 p95 < 50ms、未命中 p95 < 200ms。
func TestCompileCacheLatencyGate(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "knowledge.db"), false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	cache := NewCompileCache(store.(CompileCacheStore))
	key := CompileCacheKeyInput{WorkspaceID: "w_gate", Query: "explain auth flow", Mode: "broad", KnowledgeVersion: "wv1:abc"}
	req := CompileRequest{Plan: Plan{
		Reuse: []ReuseItem{
			reuseFixture("n1", "pkg/a.go", 0.93, "wv1:abc", ReuseReasonOK),
			reuseFixture("n2", "pkg/b.go", 0.88, "wv1:abc", ReuseReasonCrossTaskVerify),
		},
		Reason: PlanReasonOK,
	}}

	const iterations = 200
	var first CompileResult
	for i := 0; i < iterations; i++ {
		result := cache.Do(ctx, key, req)
		if i == 0 {
			first = result
			require.False(t, result.CacheHit)
			continue
		}
		require.True(t, result.CacheHit, "第 %d 次必须命中", i)
		require.Equal(t, first.Items, result.Items)
	}

	snapshot := cache.Metrics.Snapshot()
	require.Equal(t, int64(iterations-1), snapshot.Hits)
	require.Equal(t, int64(1), snapshot.Misses)
	require.Less(t, snapshot.HitP95MS, 50.0, "命中 p95 必须 < 50ms（04 §5 Phase 6 门槛）")
	require.Less(t, snapshot.MissP95MS, 200.0, "未命中 p95 必须 < 200ms（04 §5 Phase 6 门槛）")
	require.GreaterOrEqual(t, snapshot.HitRate, 0.5, "04 §7.2 compile 层命中率 ≥ 50%")
	t.Logf("compile 缓存：hits=%d misses=%d hit_p95=%.2fms miss_p95=%.2fms hit_rate=%.3f",
		snapshot.Hits, snapshot.Misses, snapshot.HitP95MS, snapshot.MissP95MS, snapshot.HitRate)
}

// CacheEntry.Validate 的边界（缺字段 / 非法类型）。
func TestCacheEntryValidate(t *testing.T) {
	valid := CacheEntry{WorkspaceID: "w", CacheKey: "k", CacheType: CacheTypeCompile, PayloadJSON: "{}"}
	require.NoError(t, valid.Validate())

	require.Error(t, (CacheEntry{CacheKey: "k", CacheType: CacheTypeCompile, PayloadJSON: "{}"}).Validate())
	require.Error(t, (CacheEntry{WorkspaceID: "w", CacheType: CacheTypeCompile, PayloadJSON: "{}"}).Validate())
	require.Error(t, (CacheEntry{WorkspaceID: "w", CacheKey: "k", CacheType: "bogus", PayloadJSON: "{}"}).Validate())
	require.Error(t, (CacheEntry{WorkspaceID: "w", CacheKey: "k", CacheType: CacheTypeCompile}).Validate())
	require.True(t, ValidCacheType(CacheTypeSummary))
	require.False(t, ValidCacheType(""))
	require.Equal(t, "ce_"+digest("w", CacheTypeCompile, "k"), CacheEntryID("w", CacheTypeCompile, "k"))
	require.NotEqual(t, CacheEntryID("w", CacheTypeCompile, "k"), CacheEntryID("w", CacheTypeCompile, "k2"))
	require.Contains(t, fmt.Sprintf("%v", valid), "w", "结构体打印不 panic")
}
