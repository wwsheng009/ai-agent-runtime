package render

import (
	"fmt"
	"testing"
)

// BenchmarkWidthCacheWarmHistoryFrame measures a repeated history-sized frame
// whose working set fits in the cache. One benchmark operation is one complete
// pass, not one row with a synthetic "lines/cycle" label.
func BenchmarkWidthCacheWarmHistoryFrame(b *testing.B) {
	const linesPerFrame = 15000
	m := newTestWidthCache(defaultWidthCacheEntries)
	keys := benchmarkWidthCacheKeys(linesPerFrame)
	for _, key := range keys {
		m.store(key, 20)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for frame := 0; frame < b.N; frame++ {
		for _, key := range keys {
			if _, ok := m.get(key); !ok {
				b.Fatalf("warm key unexpectedly evicted: %q", key)
			}
		}
	}
	b.ReportMetric(linesPerFrame, "keys/frame")
}

// BenchmarkWidthCacheThrashingHistoryFrame exercises the failure mode that the
// old implementation handled pathologically: a sequential full-history pass
// whose working set is larger than the cache. After warm-up, every lookup is a
// miss and every store evicts one entry. Keys are prebuilt so the timing does
// not include fmt.Sprintf allocations.
func BenchmarkWidthCacheThrashingHistoryFrame(b *testing.B) {
	const (
		capacity      = 2048
		linesPerFrame = 15000
	)
	m := newTestWidthCache(capacity)
	keys := benchmarkWidthCacheKeys(linesPerFrame)
	for _, key := range keys[:capacity] {
		m.store(key, 20)
	}
	// One untimed pass puts the cache into the same steady sequential-thrash
	// state used by every measured pass.
	for _, key := range keys {
		if _, ok := m.get(key); !ok {
			m.store(key, 20)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for frame := 0; frame < b.N; frame++ {
		for _, key := range keys {
			if _, ok := m.get(key); !ok {
				m.store(key, 20)
			}
		}
	}
	b.ReportMetric(linesPerFrame, "keys/frame")
}

func benchmarkWidthCacheKeys(count int) []string {
	keys := make([]string, count)
	for i := range keys {
		keys[i] = fmt.Sprintf("第 %05d 行 render 测试文本 width-%05d", i, i)
	}
	return keys
}
