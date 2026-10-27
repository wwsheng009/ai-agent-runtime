package cacheanalytics

import (
	"testing"
	"time"
)

// TestBuildTerminalRecordClassifiesConfirmedMiss 锁定 §6.4：provider 报告
// cached=0 时区分 reported_zero 与 confirmed_miss。判定依据是 provider 显式
// 给出的 miss 计数（如 DeepSeek prompt_cache_miss_tokens）；缺省零值不得被
// 伪装成真实 miss，写入/命中优先级高于 miss 计数。
func TestBuildTerminalRecordClassifiesConfirmedMiss(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		payload map[string]interface{}
		want    string
	}{
		{
			name: "explicit miss counted",
			payload: map[string]interface{}{
				"success":                   true,
				"usage_prompt_tokens":       1000,
				"usage_cache_read_reported": true,
				"usage_cache_miss_reported": true,
				"usage_cache_miss_tokens":   1000,
			},
			want: CacheStatusConfirmedMiss,
		},
		{
			name: "miss field present but zero stays reported_zero",
			payload: map[string]interface{}{
				"success":                   true,
				"usage_prompt_tokens":       1000,
				"usage_cache_read_reported": true,
				"usage_cache_miss_reported": true,
			},
			want: CacheStatusReportedZero,
		},
		{
			name: "miss counted without read report stays not_reported",
			payload: map[string]interface{}{
				"success":                   true,
				"usage_prompt_tokens":       1000,
				"usage_cache_miss_reported": true,
				"usage_cache_miss_tokens":   1000,
			},
			want: CacheStatusNotReported,
		},
		{
			name: "hit wins over miss count",
			payload: map[string]interface{}{
				"success":                   true,
				"usage_prompt_tokens":       1000,
				"usage_cache_read_tokens":   900,
				"usage_cache_read_reported": true,
				"usage_cache_miss_reported": true,
				"usage_cache_miss_tokens":   100,
			},
			want: CacheStatusHit,
		},
		{
			name: "write wins over miss count",
			payload: map[string]interface{}{
				"success":                     true,
				"usage_prompt_tokens":         1000,
				"usage_cache_creation_tokens": 1000,
				"usage_cache_read_reported":   true,
				"usage_cache_miss_reported":   true,
				"usage_cache_miss_tokens":     1000,
			},
			want: CacheStatusWrite,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := BuildTerminalRecord(TerminalRecordInput{
				LLMRequestID: "req-miss",
				SessionID:    "s-miss",
				StartedAt:    now.Add(-time.Second),
				FinishedAt:   now,
				Payload:      tc.payload,
			})
			if record.CacheStatus != tc.want {
				t.Fatalf("cache_status = %q, want %q", record.CacheStatus, tc.want)
			}
		})
	}
}

// TestCacheStatusDistributionAccountsConfirmedMiss 锁定 confirmed_miss 在
// add/remove/merge 三处都计入分布：漏掉任一处会导致 overview 计数或环形缓冲
// 淘汰后的分布出现漂移。
func TestCacheStatusDistributionAccountsConfirmedMiss(t *testing.T) {
	d := CacheStatusDistribution{}
	d.add(CacheStatusConfirmedMiss)
	d.add(CacheStatusConfirmedMiss)
	d.add(CacheStatusReportedZero)
	if d.ConfirmedMiss != 2 || d.ReportedZero != 1 {
		t.Fatalf("after add = %+v", d)
	}
	d.remove(CacheStatusConfirmedMiss)
	if d.ConfirmedMiss != 1 {
		t.Fatalf("after remove = %+v", d)
	}
	agg := projectorAggregates{}
	agg.dist.add(CacheStatusConfirmedMiss)
	other := projectorAggregates{}
	other.dist.ConfirmedMiss = 3
	agg.merge(other)
	if agg.dist.ConfirmedMiss != 4 {
		t.Fatalf("after merge = %+v", agg.dist)
	}
}
