package cacheanalytics

import "testing"

// TestCollectorCarriesCacheLineageMode 锁定 §8.4 窄读法：started 载荷的
// cache_lineage_mode=resume 随 inflight 进入终态记录；未携带时保持空串
// （未观测），不会被 finished 载荷缺省或状态分类覆盖。
func TestCollectorCarriesCacheLineageMode(t *testing.T) {
	bus, service := attachTest(t, 100)
	src := service.Source()

	publishStarted(bus, "s1", "req-resume", map[string]interface{}{"cache_lineage_mode": "resume"})
	publishFinished(bus, "s1", "req-resume", true, map[string]interface{}{"usage_prompt_tokens": 100})
	publishStarted(bus, "s1", "req-plain", nil)
	publishFinished(bus, "s1", "req-plain", true, map[string]interface{}{"usage_prompt_tokens": 100})

	record, err := src.Request("s1", "req-resume")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if record.CacheLineageMode != "resume" {
		t.Fatalf("cache_lineage_mode = %q, want resume", record.CacheLineageMode)
	}

	plain, err := src.Request("s1", "req-plain")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if plain.CacheLineageMode != "" {
		t.Fatalf("unmarked request must stay unobserved, got %q", plain.CacheLineageMode)
	}
}
