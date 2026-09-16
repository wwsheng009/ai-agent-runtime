package usageledger

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// 背景：`skills_runtime.usage_ledger_enabled` 默认关闭；是否改成默认开启，
// 取决于「每个请求一次 INSERT（auto-commit，每次 commit 一次 fsync）」的真实开销。
// 本文件的 benchmark 就是为了给出这个数字，而不是靠猜。

func benchmarkRecord(now time.Time) *entity.TokenUsageHistory {
	return &entity.TokenUsageHistory{
		ID:           uuid.NewString(),
		RequestID:    uuid.NewString(),
		ModelID:      "mimo-v2.5-pro",
		ProviderID:   "mimo_anthropic",
		InputTokens:  1200,
		OutputTokens: 300,
		TotalTokens:  1500,
		MessageCount: 4,
		MaxTokens:    8192,
		Success:      true,
		StatusCode:   0,
		Metadata: entity.JSONMap{
			"tenant_id":     "tenant-a",
			"project_id":    "project-a",
			"user_id":       "user-a",
			"entrypoint":    "skills_execute",
			"skill":         "demo-skill",
			"resolved_from": "user",
		},
		CreatedAt: entity.Time(now),
	}
}

func newBenchStore(tb testing.TB) *SQLiteStore {
	tb.Helper()
	dsn := filepath.Join(tb.TempDir(), "ledger.db")
	store, err := NewSQLiteStore(&Config{Driver: "sqlite", DSN: dsn})
	if err != nil {
		tb.Fatalf("open store: %v", err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	return store
}

// 串行写入：等价于「请求串行到达」时的单次落库成本（含 fsync）。
func BenchmarkSQLiteStoreCreate(b *testing.B) {
	store := newBenchStore(b)
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Create(benchmarkRecord(now)); err != nil {
			b.Fatalf("create: %v", err)
		}
	}
}

// 并发写入：真实服务会并发落库；这里同时暴露「写冲突被丢弃」的规模。
func BenchmarkSQLiteStoreCreateParallel(b *testing.B) {
	store := newBenchStore(b)
	now := time.Now()
	var failures int64

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := store.Create(benchmarkRecord(now)); err != nil {
				atomic.AddInt64(&failures, 1)
			}
		}
	})
	b.StopTimer()
	if failures > 0 {
		b.Logf("并发写失败（会被 handler 记为 warn 并丢弃）: %d/%d", failures, b.N)
	}
}

// TestSQLiteStoreGrowthPerRecord 量的是「默认开启后数据库会涨多快」。
func TestSQLiteStoreGrowthPerRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping growth measurement in short mode")
	}
	dir := t.TempDir()
	dsn := filepath.Join(dir, "ledger.db")
	store, err := NewSQLiteStore(&Config{Driver: "sqlite", DSN: dsn})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	const records = 2000
	sizeOf := func() int64 {
		var total int64
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir: %v", err)
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				t.Fatalf("stat %s: %v", entry.Name(), err)
			}
			total += info.Size()
		}
		return total
	}

	before := sizeOf()
	now := time.Now()
	for i := 0; i < records; i++ {
		if err := store.Create(benchmarkRecord(now)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	after := sizeOf()

	perRecord := float64(after-before) / float64(records)
	t.Logf("文件体积: %.2f MiB -> %.2f MiB（%d 条）", float64(before)/1024/1024, float64(after)/1024/1024, records)
	t.Logf("平均每条记录: %.0f bytes（含索引）", perRecord)
	t.Logf("推算: 10 万条 ≈ %.1f MiB, 100 万条 ≈ %.1f MiB", perRecord*1e5/1024/1024, perRecord*1e6/1024/1024)

	if perRecord <= 0 {
		t.Fatalf("expected positive growth per record, got %.2f", perRecord)
	}
}

// TestSQLiteStoreConcurrentWriteFailures 用固定并发量探测「写冲突丢记录」的实际规模，
// 因为实现里落库失败只 warn（相当于静默丢账）。
func TestSQLiteStoreConcurrentWriteFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency probe in short mode")
	}
	store := newBenchStore(t)

	const writers = 8
	const perWriter = 50
	now := time.Now()

	var (
		wg       sync.WaitGroup
		failures int64
	)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := store.Create(benchmarkRecord(now)); err != nil {
					atomic.AddInt64(&failures, 1)
				}
			}
		}()
	}
	wg.Wait()

	records, err := store.GetSince(time.Time{}, writers*perWriter)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	t.Logf("并发写入 %d 次，失败 %d 次，实际落库 %d 条", writers*perWriter, failures, len(records))

	if failures > 0 {
		t.Logf("注意：%d 次写入失败会被 handler 降级为 warn 并丢弃", failures)
	}
}
