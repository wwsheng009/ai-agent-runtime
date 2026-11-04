package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// TestCacheSourceRequestsFilterByCacheHitRatio 锁定数据库源与内存投影源的
// cache_hit_ratio_min/max 同口径：新行按 token 列计算，旧行回退 record_json，
// 且 not_reported/error 的未知比率不参与命中。
func TestCacheSourceRequestsFilterByCacheHitRatio(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	collector := newCollector(store, nil, nil)
	finished := time.Now()
	lowRatio, midRatio, legacyRatio := 0.1, 0.6, 0.8
	records := []cacheanalytics.CacheRequestRecord{
		{
			LLMRequestID:  "current-low",
			SessionID:     "s-ratio",
			Status:        cacheanalytics.RequestStatusSuccess,
			CacheStatus:   cacheanalytics.CacheStatusHit,
			CacheHitRatio: &lowRatio,
			FinishedAt:    &finished,
			Usage: &cacheanalytics.CacheUsage{
				PromptTokens:      100,
				CacheReadTokens:   10,
				InputTotalTokens:  100,
				CacheReadReported: true,
			},
		},
		{
			LLMRequestID:  "current-mid",
			SessionID:     "s-ratio",
			Status:        cacheanalytics.RequestStatusSuccess,
			CacheStatus:   cacheanalytics.CacheStatusHit,
			CacheHitRatio: &midRatio,
			FinishedAt:    &finished,
			Usage: &cacheanalytics.CacheUsage{
				PromptTokens:      100,
				CacheReadTokens:   60,
				InputTotalTokens:  100,
				CacheReadReported: true,
			},
		},
	}
	records = append(records,
		cacheanalytics.CacheRequestRecord{
			LLMRequestID:  "legacy",
			SessionID:     "s-ratio",
			Status:        cacheanalytics.RequestStatusSuccess,
			CacheStatus:   cacheanalytics.CacheStatusHit,
			CacheHitRatio: &legacyRatio,
			FinishedAt:    &finished,
		},
		cacheanalytics.CacheRequestRecord{
			LLMRequestID:  "unknown",
			SessionID:     "s-ratio",
			Status:        cacheanalytics.RequestStatusSuccess,
			CacheStatus:   cacheanalytics.CacheStatusNotReported,
			CacheHitRatio: &legacyRatio,
			FinishedAt:    &finished,
		},
	)
	for _, record := range records {
		collector.upsertRequest(record)
	}

	source := NewCacheSource(store, nil, false)
	min, max := 0.05, 0.65
	page, err := source.Requests("s-ratio", cacheanalytics.RequestQuery{
		CacheHitRatioMin: &min,
		CacheHitRatioMax: &max,
		Limit:            50,
	})
	if err != nil {
		t.Fatalf("requests by ratio range: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("ratio range total = %d, want 2; requests=%+v", page.Total, page.Requests)
	}

	legacyMin := 0.7
	page, err = source.Requests("s-ratio", cacheanalytics.RequestQuery{
		CacheHitRatioMin: &legacyMin,
		Limit:            50,
	})
	if err != nil {
		t.Fatalf("requests by legacy ratio: %v", err)
	}
	if page.Total != 1 || page.Requests[0].LLMRequestID != "legacy" {
		t.Fatalf("legacy ratio fallback mismatch: %+v", page)
	}
}
