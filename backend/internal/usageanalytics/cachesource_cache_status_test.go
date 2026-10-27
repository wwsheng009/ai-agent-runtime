package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// TestCacheSourceOverviewCacheStatusDistributionAlignsWithSQL 锁定分布式查询的
// SELECT/Scan 列序与 cache_status 枚举一一对应：六档（含 §6.4 新增的
// confirmed_miss）各写 1 条，总览必须各自读回 1；若列序错位会立即读串。
func TestCacheSourceOverviewCacheStatusDistributionAlignsWithSQL(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	collector := newCollector(store, nil, nil)
	finished := time.Now()
	statuses := []string{
		cacheanalytics.CacheStatusHit,
		cacheanalytics.CacheStatusWrite,
		cacheanalytics.CacheStatusConfirmedMiss,
		cacheanalytics.CacheStatusReportedZero,
		cacheanalytics.CacheStatusNotReported,
		cacheanalytics.CacheStatusError,
	}
	for i, status := range statuses {
		collector.upsertRequest(cacheanalytics.CacheRequestRecord{
			LLMRequestID: "req-status-" + status,
			SessionID:    "s-status",
			Status:       cacheanalytics.RequestStatusSuccess,
			CacheStatus:  status,
			StartedAt:    finished.Add(-time.Duration(i+1) * time.Second),
			FinishedAt:   &finished,
			Usage: &cacheanalytics.CacheUsage{
				PromptTokens:     100,
				CompletionTokens: 10,
				TotalTokens:      110,
			},
		})
	}

	source := NewCacheSource(store, nil, false)
	if source == nil {
		t.Fatal("NewCacheSource 返回 nil")
	}
	overview, err := source.Overview("s-status")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	dist := overview.CacheStatusDistribution
	if dist.Hit != 1 || dist.Write != 1 || dist.ConfirmedMiss != 1 ||
		dist.ReportedZero != 1 || dist.NotReported != 1 || dist.Error != 1 {
		t.Fatalf("distribution = %+v, want one of each", dist)
	}
}
