package chat

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// TestSQLiteRuntimeStoreCacheRequestsRoundTrip 验证 cache_requests 镜像表
// 全链路：写入幂等、字段保真回读、损坏行跳过、跨进程重开（迁移幂等）、
// DeleteState 级联清理（方案 §375 Phase 3）。
func TestSQLiteRuntimeStoreCacheRequestsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_runtime.sqlite")

	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2026, 3, 15, 10, 30, 0, 123456789, time.UTC)
	finished := base.Add(1500 * time.Millisecond)
	ratio := 0.8
	writeRatio := 0.05
	record := cacheanalytics.CacheRequestRecord{
		SchemaVersion: cacheanalytics.SchemaVersion,
		LLMRequestID:  "req-1",
		SessionID:     "sess-1",
		TraceID:       "trace-1",
		TurnID:        "turn-1",
		Step:          2,
		Provider:      "openai",
		Model:         "gpt-4o",
		Stream:        true,
		Status:        cacheanalytics.RequestStatusSuccess,
		Attempt:       1,
		StartedAt:     base,
		FinishedAt:    &finished,
		DurationMS:    1500,
		Usage: &cacheanalytics.CacheUsage{
			PromptTokens:      1000,
			CompletionTokens:  50,
			TotalTokens:       1050,
			CachedTokens:      800,
			CacheReadTokens:   800,
			CacheReadReported: true,
		},
		CacheHitRatio:     &ratio,
		CacheWriteRatio:   &writeRatio,
		CacheStatus:       cacheanalytics.CacheStatusHit,
		CacheEpoch:        3,
		PromptCacheKey:    "ck-1",
		PromptFingerprint: "fp-1",
	}
	require.NoError(t, store.SaveRequest(record))

	// 幂等：重复保存同一 llm_request_id 不产生重复行。
	require.NoError(t, store.SaveRequest(record))

	// 更早的一条，验证 started_at 升序。
	earlier := record
	earlier.LLMRequestID = "req-0"
	earlier.StartedAt = base.Add(-time.Second)
	earlier.FinishedAt = nil
	earlier.Usage = nil
	earlier.Status = cacheanalytics.RequestStatusError
	earlier.CacheStatus = cacheanalytics.CacheStatusError
	earlier.ErrorCategory = "rate_limited"
	require.NoError(t, store.SaveRequest(earlier))

	loaded, err := store.LoadSessionRequests("sess-1")
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	require.Equal(t, "req-0", loaded[0].LLMRequestID)
	require.Equal(t, "req-1", loaded[1].LLMRequestID)

	got := loaded[1]
	require.Equal(t, record.SchemaVersion, got.SchemaVersion)
	require.Equal(t, record.SessionID, got.SessionID)
	require.Equal(t, record.TraceID, got.TraceID)
	require.Equal(t, record.TurnID, got.TurnID)
	require.Equal(t, record.Step, got.Step)
	require.Equal(t, record.Provider, got.Provider)
	require.Equal(t, record.Model, got.Model)
	require.Equal(t, record.Stream, got.Stream)
	require.Equal(t, record.Status, got.Status)
	require.Equal(t, record.Attempt, got.Attempt)
	require.True(t, got.StartedAt.Equal(base), "started_at nanos must survive round-trip")
	require.NotNil(t, got.FinishedAt)
	require.True(t, got.FinishedAt.Equal(finished))
	require.Equal(t, record.DurationMS, got.DurationMS)
	require.NotNil(t, got.Usage)
	require.Equal(t, *record.Usage, *got.Usage)
	require.NotNil(t, got.CacheHitRatio)
	require.Equal(t, ratio, *got.CacheHitRatio)
	require.NotNil(t, got.CacheWriteRatio)
	require.Equal(t, writeRatio, *got.CacheWriteRatio)
	require.Equal(t, record.CacheStatus, got.CacheStatus)
	require.Equal(t, record.CacheEpoch, got.CacheEpoch)
	require.Equal(t, record.PromptCacheKey, got.PromptCacheKey)
	require.Equal(t, record.PromptFingerprint, got.PromptFingerprint)

	// 未知会话 → 空切片，不报错。
	empty, err := store.LoadSessionRequests("missing")
	require.NoError(t, err)
	require.Empty(t, empty)

	// 单行损坏跳过，不拖垮整体回放。
	_, err = store.db.Exec(`UPDATE cache_requests SET record_json = '{broken' WHERE llm_request_id = 'req-1'`)
	require.NoError(t, err)
	loaded, err = store.LoadSessionRequests("sess-1")
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, "req-0", loaded[0].LLMRequestID)

	// 跨进程重开：迁移幂等，数据仍在。
	require.NoError(t, store.Close())
	reopened, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	loaded, err = reopened.LoadSessionRequests("sess-1")
	require.NoError(t, err)
	require.Len(t, loaded, 1)

	// DeleteState 级联清理镜像行。
	require.NoError(t, reopened.DeleteState(context.Background(), "sess-1"))
	loaded, err = reopened.LoadSessionRequests("sess-1")
	require.NoError(t, err)
	require.Empty(t, loaded)
}

// TestSQLiteRuntimeStoreCacheRequestsValidation 校验入参防线。
func TestSQLiteRuntimeStoreCacheRequestsValidation(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "runtime.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.Error(t, store.SaveRequest(cacheanalytics.CacheRequestRecord{SessionID: "s1"}))
	require.Error(t, store.SaveRequest(cacheanalytics.CacheRequestRecord{LLMRequestID: "req-1"}))
}
