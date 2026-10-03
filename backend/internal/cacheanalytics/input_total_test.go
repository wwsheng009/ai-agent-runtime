package cacheanalytics

import (
	"math"
	"testing"
	"time"
)

// TestBuildTerminalRecordHitRatioUsesInputTotal 锁定不含式口径（Anthropic /
// DeepSeek-Anthropic）的命中率分母：input_tokens=155 只是新增输入，
// cache_read=34432 是命中缓存，正确命中率 = 34432/(155+34432) ≈ 99.55%，
// 而不是把 prompt 当分母算出的 22214%（用户线上数据的真实形态）。
func TestBuildTerminalRecordHitRatioUsesInputTotal(t *testing.T) {
	finished := time.Now()
	record := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID: "req-anthropic",
		SessionID:    "s1",
		FinishedAt:   finished,
		Payload: map[string]interface{}{
			"success":                     true,
			"usage_prompt_tokens":         155,
			"usage_completion_tokens":     83,
			"usage_total_tokens":          34670,
			"usage_cache_read_tokens":     34432,
			"usage_cache_read_reported":   true,
			"usage_uncached_input_tokens": 155,
			"usage_input_total_tokens":    34587,
		},
	})
	if record.Usage == nil {
		t.Fatal("usage missing")
	}
	if record.Usage.InputTotal() != 34587 {
		t.Fatalf("InputTotal = %d, want 34587", record.Usage.InputTotal())
	}
	if record.CacheHitRatio == nil {
		t.Fatal("cache_hit_ratio missing")
	}
	want := 34432.0 / 34587.0
	if diff := math.Abs(*record.CacheHitRatio - want); diff > 1e-9 {
		t.Fatalf("cache_hit_ratio = %v, want %v（分母不能用 prompt_token）", *record.CacheHitRatio, want)
	}
}

// TestBuildTerminalRecordHitRatioLegacyFallback 旧载荷（无 usage_input_total_tokens）
// 用 uncached + cache_read 推导分母，历史记录同样不再出现 >100% 命中率。
func TestBuildTerminalRecordHitRatioLegacyFallback(t *testing.T) {
	finished := time.Now()
	record := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID: "req-legacy",
		SessionID:    "s1",
		FinishedAt:   finished,
		Payload: map[string]interface{}{
			"success":                     true,
			"usage_prompt_tokens":         155,
			"usage_completion_tokens":     83,
			"usage_total_tokens":          34670,
			"usage_cache_read_tokens":     34432,
			"usage_cache_read_reported":   true,
			"usage_uncached_input_tokens": 155,
		},
	})
	if record.CacheHitRatio == nil {
		t.Fatal("cache_hit_ratio missing")
	}
	want := 34432.0 / 34587.0
	if diff := math.Abs(*record.CacheHitRatio - want); diff > 1e-9 {
		t.Fatalf("legacy cache_hit_ratio = %v, want %v", *record.CacheHitRatio, want)
	}
}

// TestBuildTerminalRecordHitRatioInclusiveUnchanged 包含式口径（OpenAI 系）
// 维持 prompt 分母语义：prompt 已含缓存命中。
func TestBuildTerminalRecordHitRatioInclusiveUnchanged(t *testing.T) {
	finished := time.Now()
	record := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID: "req-openai",
		SessionID:    "s1",
		FinishedAt:   finished,
		Payload: map[string]interface{}{
			"success":                     true,
			"usage_prompt_tokens":         2000,
			"usage_completion_tokens":     100,
			"usage_total_tokens":          2100,
			"usage_cache_read_tokens":     1500,
			"usage_cache_read_reported":   true,
			"usage_uncached_input_tokens": 500,
			"usage_input_total_tokens":    2000,
		},
	})
	if record.CacheHitRatio == nil {
		t.Fatal("cache_hit_ratio missing")
	}
	if want := 0.75; math.Abs(*record.CacheHitRatio-want) > 1e-9 {
		t.Fatalf("inclusive cache_hit_ratio = %v, want %v", *record.CacheHitRatio, want)
	}
}

// TestProjectorOverviewHitRatioUsesInputTotal 总览命中率同样以输入总量为分母。
func TestProjectorOverviewHitRatioUsesInputTotal(t *testing.T) {
	bus, service := attachTest(t, 100)
	publishStarted(bus, "s-anthropic", "req-1", nil)
	publishFinished(bus, "s-anthropic", "req-1", true, map[string]interface{}{
		"usage_prompt_tokens":         155,
		"usage_completion_tokens":     83,
		"usage_total_tokens":          34670,
		"usage_cache_read_tokens":     34432,
		"usage_cache_read_reported":   true,
		"usage_uncached_input_tokens": 155,
		"usage_input_total_tokens":    34587,
	})

	overview, err := service.Source().Overview("s-anthropic")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.CacheHitRatio == nil {
		t.Fatal("overview cache_hit_ratio missing")
	}
	want := 34432.0 / 34587.0
	if diff := math.Abs(*overview.CacheHitRatio - want); diff > 1e-9 {
		t.Fatalf("overview cache_hit_ratio = %v, want %v", *overview.CacheHitRatio, want)
	}
}
