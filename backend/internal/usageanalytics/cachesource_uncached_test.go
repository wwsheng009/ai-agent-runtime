package usageanalytics

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// TestCacheSourceOverviewSumsUncachedInputTokens 锁定「未缓存输入」增量列在
// 写入(usage_requests INSERT) → 总览(SQL SUM) 全链路上的可用性：两种协议口径
// （OpenAI 包含式：prompt 含缓存；Anthropic 不含式：input 本身即未缓存）都由
// 解析端归一化后写入该列，总览不做 prompt - cache_read 反推。
func TestCacheSourceOverviewSumsUncachedInputTokens(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	collector := newCollector(store, nil, nil)
	finished := time.Now()
	records := []cacheanalytics.CacheRequestRecord{
		{
			LLMRequestID: "req-openai",
			SessionID:    "s-uncached",
			Status:       cacheanalytics.RequestStatusSuccess,
			CacheStatus:  cacheanalytics.CacheStatusHit,
			FinishedAt:   &finished,
			Usage: &cacheanalytics.CacheUsage{
				PromptTokens:        2000,
				CompletionTokens:    100,
				TotalTokens:         2100,
				CachedTokens:        1500,
				CacheReadTokens:     1500,
				UncachedInputTokens: 500,
				InputTotalTokens:    2000,
				CacheReadReported:   true,
			},
		},
		{
			LLMRequestID: "req-anthropic",
			SessionID:    "s-uncached",
			Status:       cacheanalytics.RequestStatusSuccess,
			CacheStatus:  cacheanalytics.CacheStatusHit,
			FinishedAt:   &finished,
			Usage: &cacheanalytics.CacheUsage{
				PromptTokens:     800,
				CompletionTokens: 50,
				TotalTokens:      1350,
				CachedTokens:     500,
				CacheReadTokens:  500,
				// 不含式口径：input 本身即未缓存，不能被 cache_read 再减一次。
				UncachedInputTokens: 800,
				InputTotalTokens:    1300,
				CacheReadReported:   true,
			},
		},
	}
	for _, record := range records {
		collector.upsertRequest(record)
	}

	source := NewCacheSource(store, nil, false)
	if source == nil {
		t.Fatal("NewCacheSource 返回 nil")
	}
	overview, err := source.Overview("s-uncached")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.Tokens.UncachedInputTokens != 1300 {
		t.Fatalf("uncached_input_tokens 汇总 = %d, want 1300", overview.Tokens.UncachedInputTokens)
	}
	if overview.Tokens.PromptTokens != 2800 {
		t.Fatalf("prompt_tokens 汇总 = %d, want 2800", overview.Tokens.PromptTokens)
	}
	// 命中率分母是输入总量（2000 + 1300），不是 prompt（2800 中的 800 只是新增输入）。
	if overview.CacheHitRatio == nil {
		t.Fatal("cache_hit_ratio missing")
	}
	if want := 2000.0 / 3300.0; math.Abs(*overview.CacheHitRatio-want) > 1e-9 {
		t.Fatalf("cache_hit_ratio = %v, want %v", *overview.CacheHitRatio, want)
	}
}

// TestCacheSourceOverviewHitRatioLegacyFallback 旧行无 input_total_tokens 列值时，
// 用 uncached + cache_read 推导分母，历史 Anthropic 行不再出现 >100% 命中率。
func TestCacheSourceOverviewHitRatioLegacyFallback(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	collector := newCollector(store, nil, nil)
	finished := time.Now()
	collector.upsertRequest(cacheanalytics.CacheRequestRecord{
		LLMRequestID: "req-legacy-anthropic",
		SessionID:    "s-legacy",
		Status:       cacheanalytics.RequestStatusSuccess,
		CacheStatus:  cacheanalytics.CacheStatusHit,
		FinishedAt:   &finished,
		Usage: &cacheanalytics.CacheUsage{
			PromptTokens:        155,
			CompletionTokens:    83,
			TotalTokens:         34670,
			CachedTokens:        34432,
			CacheReadTokens:     34432,
			UncachedInputTokens: 155,
			CacheReadReported:   true,
		},
	})

	overview, err := NewCacheSource(store, nil, false).Overview("s-legacy")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.CacheHitRatio == nil {
		t.Fatal("cache_hit_ratio missing")
	}
	if want := 34432.0 / 34587.0; math.Abs(*overview.CacheHitRatio-want) > 1e-9 {
		t.Fatalf("legacy cache_hit_ratio = %v, want %v", *overview.CacheHitRatio, want)
	}
}
