package usageanalytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// storedStats 是 usage_sessions 预聚合列的测试快照。
type storedStats struct {
	totalRequests    int
	successes        int
	errors           int
	withUsage        int
	totalTokens      int
	promptTokens     int
	completionTokens int
	cachedTokens     int
	reasoningTokens  int
	totalDuration    int64
	durationSamples  int64
	turnCount        int
	failedTurns      int
	firstStarted     int64
	lastStarted      int64
}

func readStoredStats(t *testing.T, store *Store, sessionID string) storedStats {
	t.Helper()
	var stats storedStats
	err := store.db.QueryRow(`SELECT
  c_total_requests, c_llm_successes, c_llm_errors, c_requests_with_usage,
  c_total_tokens, c_prompt_tokens, c_completion_tokens, c_cached_tokens, c_reasoning_tokens,
  c_total_duration_ms, c_duration_samples, c_turn_count, c_failed_turns,
  c_first_started_at, c_last_started_at
FROM usage_sessions WHERE session_id = ?`, sessionID).Scan(
		&stats.totalRequests, &stats.successes, &stats.errors, &stats.withUsage,
		&stats.totalTokens, &stats.promptTokens, &stats.completionTokens, &stats.cachedTokens, &stats.reasoningTokens,
		&stats.totalDuration, &stats.durationSamples, &stats.turnCount, &stats.failedTurns,
		&stats.firstStarted, &stats.lastStarted,
	)
	require.NoError(t, err)
	return stats
}

// TestStatsIncrementalCountersTrackRequests 锁定 §5.3：请求终态在同一事务内
// 增量维护全部计数列；重复终态事件按 delta 覆盖而不是累加。
func TestStatsIncrementalCountersTrackRequests(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()
	require.True(t, store.StatsReady(), "v3 库应启用预聚合读路径")

	startedAt := now.Add(-3 * time.Second)
	// turn-1：成功请求 + 失败请求（failure 载荷不解析 usage）。
	publishRequestStarted(bus, "sess-1", "req-1", "trace-1", "turn-1", 1, startedAt, "acme", "m1")
	publishRequestFinished(bus, "sess-1", "req-1", usagePayload(100, 20, 120, 40))
	publishRequestStarted(bus, "sess-1", "req-2", "trace-1", "turn-1", 2, startedAt, "acme", "m1")
	publishRequestFinished(bus, "sess-1", "req-2", map[string]interface{}{"success": false, "error_code": "upstream"})
	// turn-2：成功请求。
	publishRequestStarted(bus, "sess-1", "req-3", "trace-2", "turn-2", 1, startedAt, "acme", "m1")
	publishRequestFinished(bus, "sess-1", "req-3", usagePayload(50, 5, 55, 0))

	stats := readStoredStats(t, store, "sess-1")
	require.Equal(t, 3, stats.totalRequests)
	require.Equal(t, 2, stats.successes)
	require.Equal(t, 1, stats.errors)
	require.Equal(t, 2, stats.withUsage)
	require.Equal(t, 175, stats.totalTokens)
	require.Equal(t, 150, stats.promptTokens)
	require.Equal(t, 25, stats.completionTokens)
	require.Equal(t, 40, stats.cachedTokens)
	require.Equal(t, 2, stats.turnCount)
	require.Equal(t, 1, stats.failedTurns)
	require.EqualValues(t, 9000, stats.totalDuration)
	require.EqualValues(t, 3, stats.durationSamples)
	require.Equal(t, startedAt.UnixNano(), stats.firstStarted)
	require.Equal(t, startedAt.UnixNano(), stats.lastStarted)

	// 重复终态事件（同 llm_request_id 覆盖写，trace/turn 不变）：计数按新值替换，不重复累加。
	replay := usagePayload(50, 5, 55, 0)
	replay["trace_id"] = "trace-1"
	replay["turn_id"] = "turn-1"
	publishRequestFinished(bus, "sess-1", "req-1", replay)
	stats = readStoredStats(t, store, "sess-1")
	require.Equal(t, 3, stats.totalRequests, "重复终态不得重复计请求数")
	require.Equal(t, 2, stats.successes)
	require.Equal(t, 1, stats.errors)
	require.Equal(t, 2, stats.withUsage)
	require.Equal(t, 110, stats.totalTokens)
	require.Equal(t, 100, stats.promptTokens)
	require.Equal(t, 10, stats.completionTokens)
	require.Equal(t, 0, stats.cachedTokens)
	require.Equal(t, 2, stats.turnCount, "同一 turn 覆盖写不得新增 turn")
	require.Equal(t, 1, stats.failedTurns)

	// 公开读路径与列值一致。
	list, err := store.ListSessions(Query{})
	require.NoError(t, err)
	require.Len(t, list.Sessions, 1)
	require.Equal(t, 3, list.Sessions[0].TotalRequests)
	require.Equal(t, 110, list.Sessions[0].TotalTokens)
	require.Equal(t, 2, list.Sessions[0].TurnCount)
	require.Equal(t, 1, list.Sessions[0].FailedTurns)
}

// TestStatsTurnFailureToSuccessTransition 锁定 §5.2 失败 turn 的降级路径：
// turn 内唯一失败请求变为成功后，failed_turns 必须回退。
func TestStatsTurnFailureToSuccessTransition(t *testing.T) {
	now := time.Date(2026, 9, 17, 11, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()

	startedAt := now.Add(-time.Second)
	publishRequestStarted(bus, "sess-t", "req-a", "trace-1", "turn-1", 1, startedAt, "acme", "m1")
	publishRequestFinished(bus, "sess-t", "req-a", map[string]interface{}{"success": false})
	require.Equal(t, 1, readStoredStats(t, store, "sess-t").failedTurns)

	publishRequestFinished(bus, "sess-t", "req-a", usagePayload(10, 2, 12, 0))
	stats := readStoredStats(t, store, "sess-t")
	require.Equal(t, 1, stats.totalRequests)
	require.Equal(t, 1, stats.successes)
	require.Equal(t, 0, stats.errors)
	require.Equal(t, 1, stats.turnCount)
	require.Equal(t, 0, stats.failedTurns, "唯一失败请求恢复成功后 failed_turns 应回退")

	// 同一 turn 内仍有其它失败请求时保持失败态。
	publishRequestStarted(bus, "sess-t", "req-b", "trace-1", "turn-1", 2, startedAt, "acme", "m1")
	publishRequestFinished(bus, "sess-t", "req-b", map[string]interface{}{"success": false})
	require.Equal(t, 1, readStoredStats(t, store, "sess-t").failedTurns)
	publishRequestFinished(bus, "sess-t", "req-a", usagePayload(20, 4, 24, 0))
	require.Equal(t, 1, readStoredStats(t, store, "sess-t").failedTurns, "其它失败请求仍在时不得回退")
}

// TestStatsDurationSamplesExcludeZero 锁定 §5.1：duration_ms=0 不进入
// c_duration_samples，AVG 语义与旧表达式一致（0 样本 → 0）。
func TestStatsDurationSamplesExcludeZero(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()

	// started_at == now → duration 0。
	publishRequestStarted(bus, "sess-zero", "req-z", "", "", 1, now, "acme", "m1")
	publishRequestFinished(bus, "sess-zero", "req-z", usagePayload(10, 1, 11, 0))

	stats := readStoredStats(t, store, "sess-zero")
	require.EqualValues(t, 0, stats.totalDuration)
	require.EqualValues(t, 0, stats.durationSamples)

	list, err := store.ListSessions(Query{})
	require.NoError(t, err)
	require.Len(t, list.Sessions, 1)
	require.EqualValues(t, 0, list.Sessions[0].AverageResponseTimeMs)
}

// TestStatsPruneAndRebuildNotRequiredForBasicDrift 锁定对账入口可修复人为漂移：
// 直接篡改计数列后，RebuildSessionStats 恢复与原始表一致。
func TestStatsRebuildRepairsDrift(t *testing.T) {
	now := time.Date(2026, 9, 17, 13, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()

	startedAt := now.Add(-2 * time.Second)
	publishRequestStarted(bus, "sess-r", "req-1", "trace-1", "turn-1", 1, startedAt, "acme", "m1")
	publishRequestFinished(bus, "sess-r", "req-1", usagePayload(70, 7, 77, 0))

	if _, err := store.exec(`UPDATE usage_sessions SET c_total_requests = 99, c_turn_count = 42, c_failed_turns = 7 WHERE session_id = ?`, "sess-r"); err != nil {
		t.Fatalf("构造漂移失败: %v", err)
	}
	require.NoError(t, store.RebuildSessionStats("sess-r"))
	stats := readStoredStats(t, store, "sess-r")
	require.Equal(t, 1, stats.totalRequests)
	require.Equal(t, 1, stats.turnCount)
	require.Equal(t, 0, stats.failedTurns)
	require.Equal(t, 77, stats.totalTokens)
}

// TestStatsDriftSamplingDetectsAndRebuildClears 锁定 §6.3：健康快照抽样对账
// 能发现人为漂移，rebuild 后归零。
func TestStatsDriftSamplingDetectsAndRebuildClears(t *testing.T) {
	now := time.Date(2026, 9, 17, 16, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()

	startedAt := now.Add(-time.Second)
	publishRequestStarted(bus, "sess-d", "req-d", "trace-d", "turn-d", 1, startedAt, "acme", "m1")
	publishRequestFinished(bus, "sess-d", "req-d", usagePayload(30, 3, 33, 0))

	health := store.AnalyticsHealth()
	require.True(t, health.StatsReady)
	require.NotNil(t, health.StatsDrift)
	require.Equal(t, 1, health.StatsDrift.Checked)
	require.Equal(t, 0, health.StatsDrift.Drifted)

	if _, err := store.exec(`UPDATE usage_sessions SET c_total_tokens = 1 WHERE session_id = ?`, "sess-d"); err != nil {
		t.Fatalf("构造漂移失败: %v", err)
	}
	health = store.AnalyticsHealth()
	require.Equal(t, 1, health.StatsDrift.Drifted)
	require.NotEmpty(t, health.StatsDrift.Samples)
	require.Equal(t, "c_total_tokens", health.StatsDrift.Samples[0].Field)
	require.EqualValues(t, 1, health.StatsDrift.Samples[0].Stored)
	require.EqualValues(t, 33, health.StatsDrift.Samples[0].Live)

	require.NoError(t, store.RebuildAllSessionStats())
	health = store.AnalyticsHealth()
	require.Equal(t, 0, health.StatsDrift.Drifted)
	require.Empty(t, health.StatsDrift.Samples)
}
