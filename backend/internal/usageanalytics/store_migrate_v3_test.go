package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// downgradeToV2 把当前 v3 库还原成 v2 形态（删预聚合列 + 删 turn 键表 + 版本号回 2），
// 用于模拟"既有 v2 旧库"迁移前状态。
func downgradeToV2(t *testing.T, store *Store) {
	t.Helper()
	for _, column := range statsColumnDefs {
		if _, err := store.db.Exec("ALTER TABLE usage_sessions DROP COLUMN " + column.name); err != nil {
			t.Fatalf("drop column %s: %v", column.name, err)
		}
	}
	if _, err := store.db.Exec("DROP TABLE IF EXISTS " + statsTurnKeysTable); err != nil {
		t.Fatalf("drop turn keys: %v", err)
	}
	if _, err := store.db.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatalf("reset user_version: %v", err)
	}
}

// seedV2FixtureRows 写入 v2 时代的原始行（绕过增量维护，模拟历史数据）。
func seedV2FixtureRows(t *testing.T, store *Store) {
	t.Helper()
	base := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC).UnixNano()
	statements := []struct {
		sql  string
		args []interface{}
	}{
		{`INSERT INTO usage_sessions (session_id, title, provider, model, status, started_at_unix_nano, updated_at_unix_nano)
		  VALUES ('s1', 'S1', 'acme', 'm1', 'completed', ?, ?)`, []interface{}{base, base + int64(2*time.Hour)}},
		{`INSERT INTO usage_sessions (session_id, title, provider, model, status, started_at_unix_nano, updated_at_unix_nano)
		  VALUES ('s2', 'S2', 'beta', 'm2', 'completed', ?, ?)`, []interface{}{base + int64(time.Hour), base + int64(3*time.Hour)}},
		{`INSERT INTO usage_requests (llm_request_id, session_id, trace_id, turn_id, step, provider, model, status,
		    success, started_at_unix_nano, duration_ms, prompt_tokens, completion_tokens, total_tokens,
		    cache_read_tokens, reasoning_tokens, usage_available)
		  VALUES ('r1', 's1', 't1', 'turn-1', 1, 'acme', 'm1', 'success', 1, ?, 1000, 10, 2, 12, 0, 0, 1)`,
			[]interface{}{base}},
		{`INSERT INTO usage_requests (llm_request_id, session_id, trace_id, turn_id, step, provider, model, status,
		    success, started_at_unix_nano, duration_ms, prompt_tokens, completion_tokens, total_tokens,
		    cache_read_tokens, reasoning_tokens, usage_available)
		  VALUES ('r2', 's1', 't1', 'turn-1', 2, 'acme', 'm1', 'error', 0, ?, 3000, 0, 0, 0, 0, 0, 0)`,
			[]interface{}{base + int64(time.Minute)}},
		{`INSERT INTO usage_requests (llm_request_id, session_id, trace_id, turn_id, step, provider, model, status,
		    success, started_at_unix_nano, duration_ms, prompt_tokens, completion_tokens, total_tokens,
		    cache_read_tokens, reasoning_tokens, usage_available)
		  VALUES ('r3', 's1', 't2', 'turn-2', 1, 'acme', 'm1', 'success', 1, ?, 0, 5, 1, 6, 2, 0, 1)`,
			[]interface{}{base + int64(2*time.Minute)}},
	}
	for _, statement := range statements {
		if _, err := store.db.Exec(statement.sql, statement.args...); err != nil {
			t.Fatalf("seed v2 fixture: %v", err)
		}
	}
}

// TestStoreMigratesV3AndBackfills 锁定 §6.1/§6.2：v2 库打开时单事务迁移到 v3，
// 历史数据一次性回填正确；重复 Open 幂等。
func TestStoreMigratesV3AndBackfills(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	store, err := Open(Config{Path: path})
	require.NoError(t, err)
	seedV2FixtureRows(t, store)
	downgradeToV2(t, store)
	require.NoError(t, store.Close())

	reopened, err := Open(Config{Path: path})
	require.NoError(t, err)
	defer func() { _ = reopened.Close() }()

	version, err := reopened.schemaVersion()
	require.NoError(t, err)
	require.Equal(t, statsSchemaVersion, version)
	require.True(t, reopened.statsReady())

	stats := readStoredStats(t, reopened, "s1")
	require.Equal(t, 3, stats.totalRequests)
	require.Equal(t, 2, stats.successes)
	require.Equal(t, 1, stats.errors)
	require.Equal(t, 2, stats.withUsage)
	require.Equal(t, 18, stats.totalTokens)
	require.Equal(t, 15, stats.promptTokens)
	require.Equal(t, 3, stats.completionTokens)
	require.Equal(t, 2, stats.cachedTokens)
	require.EqualValues(t, 4000, stats.totalDuration)
	require.EqualValues(t, 2, stats.durationSamples)
	require.Equal(t, 2, stats.turnCount)
	require.Equal(t, 1, stats.failedTurns)
	require.Equal(t, time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC).UnixNano(), stats.firstStarted)
	require.Equal(t, time.Date(2026, 9, 10, 9, 2, 0, 0, time.UTC).UnixNano(), stats.lastStarted)

	// s2 无请求：回填后计数保持 0。
	statsS2 := readStoredStats(t, reopened, "s2")
	require.Equal(t, 0, statsS2.totalRequests)
	require.Equal(t, 0, statsS2.turnCount)

	// 重复 Open：迁移幂等，回填不重复累加。
	require.NoError(t, reopened.Close())
	again, err := Open(Config{Path: path})
	require.NoError(t, err)
	defer func() { _ = again.Close() }()
	require.Equal(t, 3, readStoredStats(t, again, "s1").totalRequests)
	require.Equal(t, 2, readStoredStats(t, again, "s1").turnCount)
}

// TestReadOnlyV2FallsBackToLegacySelect 锁定 §5.4：只读打开不迁移，
// v2 旧库走旧聚合路径且结果正确；库文件版本保持 2。
func TestReadOnlyV2FallsBackToLegacySelect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	store, err := Open(Config{Path: path})
	require.NoError(t, err)
	seedV2FixtureRows(t, store)
	downgradeToV2(t, store)
	require.NoError(t, store.Close())

	readOnly := openReadOnly(path, 0)
	defer func() { _ = readOnly.Close() }()
	require.False(t, readOnly.Empty())
	require.False(t, readOnly.statsReady(), "v2 库必须回退旧读路径")
	require.False(t, readOnly.statsColumnsReady())

	list, err := readOnly.ListSessions(Query{})
	require.NoError(t, err)
	require.Equal(t, 2, list.Total)
	require.Len(t, list.Sessions, 2)
	var s1 *SessionRollup
	for i := range list.Sessions {
		if list.Sessions[i].SessionID == "s1" {
			s1 = &list.Sessions[i]
		}
	}
	require.NotNil(t, s1)
	require.Equal(t, 3, s1.TotalRequests)
	require.Equal(t, 2, s1.LLMSuccesses)
	require.Equal(t, 1, s1.LLMErrors)
	require.Equal(t, 2, s1.TurnCount)
	require.Equal(t, 1, s1.FailedTurns)
	require.Equal(t, 18, s1.TotalTokens)
	require.EqualValues(t, 2000, s1.AverageResponseTimeMs)

	version, err := readOnly.schemaVersion()
	require.NoError(t, err)
	require.Equal(t, 2, version, "只读打开不得执行迁移")
}
