package usageanalytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAnalyticsQueryCacheTTLAndGeneration 锁定 §8.2 缓存语义：
// TTL 内命中、写入代数变化立即失效、过期失效。
func TestAnalyticsQueryCacheTTLAndGeneration(t *testing.T) {
	clock := time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC)
	cache := newAnalyticsQueryCache(func() time.Time { return clock })
	key := analyticsCacheKey{op: "sessions", q: Query{Limit: 50}}

	cache.put(key, 0, "v1")
	value, ok := cache.get(key, 0)
	require.True(t, ok)
	require.Equal(t, "v1", value)

	// 写入代数变化（本进程有新请求终态落库）：立即失效。
	_, ok = cache.get(key, 1)
	require.False(t, ok, "generation 变化必须失效")

	cache.put(key, 1, "v2")
	clock = clock.Add(analyticsCacheTTL - time.Millisecond)
	value, ok = cache.get(key, 1)
	require.True(t, ok, "TTL 内应命中")
	require.Equal(t, "v2", value)

	clock = clock.Add(2 * time.Millisecond)
	_, ok = cache.get(key, 1)
	require.False(t, ok, "TTL 过期必须失效")
}

// TestServiceQueryCacheReturnsFreshAfterWrite 锁定服务层缓存不会吞掉新写入：
// 同一查询在写入前后必须返回不同结果（测试时钟固定，只能靠代数失效）。
func TestServiceQueryCacheReturnsFreshAfterWrite(t *testing.T) {
	now := time.Date(2026, 9, 17, 18, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()

	startedAt := now.Add(-2 * time.Second)
	publishRequestStarted(bus, "sess-cache", "req-c1", "trace-c1", "turn-c1", 1, startedAt, "acme", "m1")
	publishRequestFinished(bus, "sess-cache", "req-c1", usagePayload(10, 1, 11, 0))

	first, err := service.ListSessions(Query{})
	require.NoError(t, err)
	require.Len(t, first.Sessions, 1)
	require.Equal(t, 1, first.Sessions[0].TotalRequests)

	// 命中缓存：直接篡改列值也不应影响结果（证明第二次查询未落库）。
	if _, err := store.exec(`UPDATE usage_sessions SET c_total_requests = 9 WHERE session_id = ?`, "sess-cache"); err != nil {
		t.Fatalf("构造缓存探针失败: %v", err)
	}
	cached, err := service.ListSessions(Query{})
	require.NoError(t, err)
	require.Equal(t, 1, cached.Sessions[0].TotalRequests, "TTL 内应命中缓存")

	// 新请求终态写入 → statsGen 自增 → 缓存立即失效（新值 = 篡改值 9 + delta 1）。
	publishRequestStarted(bus, "sess-cache", "req-c2", "trace-c2", "turn-c2", 1, startedAt, "acme", "m1")
	publishRequestFinished(bus, "sess-cache", "req-c2", usagePayload(20, 2, 22, 0))
	fresh, err := service.ListSessions(Query{})
	require.NoError(t, err)
	require.Equal(t, 10, fresh.Sessions[0].TotalRequests, "写入后必须失效并读到最新值")
}
