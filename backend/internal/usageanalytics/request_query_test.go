package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

func TestListRequestsFiltersLowCacheTodayBeforePagination(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	collector := newCollector(store, nil, nil)
	today := time.Date(2026, 11, 4, 8, 0, 0, 0, time.Local)
	yesterday := today.AddDate(0, 0, -1)
	finished := today.Add(time.Minute)
	low, high, unknown := 0.2, 0.8, 0.1
	records := []cacheanalytics.CacheRequestRecord{
		{
			LLMRequestID:  "low-today",
			SessionID:     "session-low",
			Provider:      "openai",
			Model:         "gpt-5",
			Status:        cacheanalytics.RequestStatusSuccess,
			CacheStatus:   cacheanalytics.CacheStatusHit,
			CacheHitRatio: &low,
			StartedAt:     today,
			FinishedAt:    &finished,
			Usage: &cacheanalytics.CacheUsage{
				PromptTokens:      100,
				CompletionTokens:  20,
				TotalTokens:       120,
				CacheReadTokens:   20,
				InputTotalTokens:  100,
				CacheReadReported: true,
			},
		},
		{
			LLMRequestID:  "high-today",
			SessionID:     "session-high",
			Provider:      "openai",
			Model:         "gpt-5",
			Status:        cacheanalytics.RequestStatusSuccess,
			CacheStatus:   cacheanalytics.CacheStatusHit,
			CacheHitRatio: &high,
			StartedAt:     today.Add(time.Hour),
			FinishedAt:    &finished,
			Usage: &cacheanalytics.CacheUsage{
				PromptTokens:      100,
				CacheReadTokens:   80,
				InputTotalTokens:  100,
				CacheReadReported: true,
			},
		},
		{
			LLMRequestID:  "low-yesterday",
			SessionID:     "session-old",
			Provider:      "openai",
			Model:         "gpt-5",
			Status:        cacheanalytics.RequestStatusSuccess,
			CacheStatus:   cacheanalytics.CacheStatusHit,
			CacheHitRatio: &low,
			StartedAt:     yesterday,
			FinishedAt:    &finished,
		},
		{
			LLMRequestID:  "not-reported",
			SessionID:     "session-unknown",
			Provider:      "openai",
			Model:         "gpt-5",
			Status:        cacheanalytics.RequestStatusSuccess,
			CacheStatus:   cacheanalytics.CacheStatusNotReported,
			CacheHitRatio: &unknown,
			StartedAt:     today.Add(2 * time.Hour),
			FinishedAt:    &finished,
		},
		{
			LLMRequestID:  "cache-error",
			SessionID:     "session-error",
			Provider:      "openai",
			Model:         "gpt-5",
			Status:        cacheanalytics.RequestStatusError,
			CacheStatus:   cacheanalytics.CacheStatusError,
			CacheHitRatio: &unknown,
			StartedAt:     today.Add(3 * time.Hour),
			FinishedAt:    &finished,
		},
	}
	for _, record := range records {
		collector.upsertRequest(record)
	}
	var storedRatio float64
	if err := store.readDB().QueryRow(
		`SELECT cache_hit_ratio FROM usage_requests WHERE llm_request_id = 'low-today'`,
	).Scan(&storedRatio); err != nil {
		t.Fatalf("read promoted cache ratio: %v", err)
	}
	if storedRatio != low {
		t.Fatalf("promoted cache ratio = %v, want %v", storedRatio, low)
	}

	below := 0.5
	result, err := store.ListRequests(RequestQuery{
		From:               today,
		To:                 today.AddDate(0, 0, 1),
		CacheHitRatioBelow: &below,
		Limit:              1,
	})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	if result.Total != 1 || len(result.Requests) != 1 {
		t.Fatalf("low-cache today = total %d rows %d, want 1/1", result.Total, len(result.Requests))
	}
	if result.Requests[0].LLMRequestID != "low-today" {
		t.Fatalf("request = %q, want low-today", result.Requests[0].LLMRequestID)
	}
	if result.Summary.CacheReported != 1 || result.Summary.CacheStatusDistribution.Hit != 1 {
		t.Fatalf("summary cache counts = %+v", result.Summary)
	}
	if result.Summary.AverageCacheHitRatio == nil || *result.Summary.AverageCacheHitRatio != low {
		t.Fatalf("average ratio = %v, want %v", result.Summary.AverageCacheHitRatio, low)
	}
}

func TestListRequestsUnknownCacheNeverMatchesRatioBoundary(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	collector := newCollector(store, nil, nil)
	ratio := 0.0
	collector.upsertRequest(cacheanalytics.CacheRequestRecord{
		LLMRequestID:  "unknown",
		SessionID:     "session",
		Status:        cacheanalytics.RequestStatusSuccess,
		CacheStatus:   cacheanalytics.CacheStatusNotReported,
		CacheHitRatio: &ratio,
		StartedAt:     time.Now(),
	})

	below := 0.5
	result, err := store.ListRequests(RequestQuery{CacheHitRatioBelow: &below, Limit: 50})
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	if result.Total != 0 || len(result.Requests) != 0 {
		t.Fatalf("unknown cache matched ratio filter: %+v", result)
	}
}

func TestListRequestsFiltersInclusiveCacheRatioRange(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	collector := newCollector(store, nil, nil)
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	for index, ratio := range []float64{0.2, 0.5, 0.8} {
		value := ratio
		collector.upsertRequest(cacheanalytics.CacheRequestRecord{
			LLMRequestID:  "range-" + string(rune('a'+index)),
			SessionID:     "session-range",
			Status:        cacheanalytics.RequestStatusSuccess,
			CacheStatus:   cacheanalytics.CacheStatusHit,
			CacheHitRatio: &value,
			StartedAt:     now.Add(time.Duration(index) * time.Minute),
		})
	}

	min, max := 0.5, 0.8
	result, err := store.ListRequests(RequestQuery{
		CacheHitRatioMin: &min,
		CacheHitRatioMax: &max,
		Limit:            50,
	})
	require.NoError(t, err)
	require.Equal(t, 2, result.Total)
	require.Len(t, result.Requests, 2)
}

func TestListRequestsOrderUsesCompositeTimeIndex(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	plan := explainPlanText(t, store,
		`SELECT r.record_json FROM usage_requests r
		 WHERE r.started_at_unix_nano >= ? AND r.started_at_unix_nano < ?
		 ORDER BY r.started_at_unix_nano DESC, r.step DESC, r.llm_request_id DESC
		 LIMIT 200 OFFSET 0`,
		int64(1), int64(2))
	require.Contains(t, plan, "idx_usage_requests_time_step_id")
	require.NotContains(t, plan, "TEMP B-TREE")
}

func TestRequestListFromOnlyJoinsSessionsForSessionFilters(t *testing.T) {
	require.NotContains(t, requestListFrom(RequestQuery{Provider: "openai"}), "usage_sessions")
	require.Contains(t, requestListFrom(RequestQuery{Directory: "/repo"}), "usage_sessions")
	require.Contains(t, requestListFrom(RequestQuery{Project: "runtime"}), "usage_sessions")
	require.Contains(t, requestListFrom(RequestQuery{SessionStatus: "completed"}), "usage_sessions")
	require.Contains(t, requestListFrom(RequestQuery{Query: "needle"}), "usage_sessions")
}
