package usageanalytics

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// ============================================================================
// 性能基准（方案 §11.3）：
//   - BenchmarkRequestTerminalStatsWrite：ingest 写入吞吐（阈值 ≥ 基线 −10% 由
//     CI 留档对比，不在代码内硬断言）；
//   - BenchmarkListSessions / BenchmarkSummarize / BenchmarkDimensions：
//     首屏读路径在中等规模 fixture（200 会话 × 50 请求 = 1 万行）下的单次耗时。
// ============================================================================

func BenchmarkRequestTerminalStatsWrite(b *testing.B) {
	store, err := Open(Config{Path: filepath.Join(b.TempDir(), DefaultDBFileName)})
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()
	collector := newCollector(store, nil, nil)
	finished := time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)
	started := finished.Add(-time.Second)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		record := cacheanalytics.CacheRequestRecord{
			LLMRequestID: fmt.Sprintf("req-%d", i),
			SessionID:    "sess-bench",
			TraceID:      fmt.Sprintf("trace-%d", i),
			Status:       cacheanalytics.RequestStatusSuccess,
			StartedAt:    started,
			FinishedAt:   &finished,
		}
		collector.persistRequestTerminal(record, SessionMeta{}, started, finished)
	}
}

// seedStatsBenchmarkFixture 批量造 nSessions×nRequests 行（含预聚合维护）。
func seedStatsBenchmarkFixture(b *testing.B, nSessions, nRequests int) *Store {
	b.Helper()
	store, err := Open(Config{Path: filepath.Join(b.TempDir(), DefaultDBFileName)})
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	collector := newCollector(store, nil, nil)
	base := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	for s := 0; s < nSessions; s++ {
		sessionID := fmt.Sprintf("sess-%d", s)
		for r := 0; r < nRequests; r++ {
			started := base.Add(time.Duration(s*nRequests+r) * time.Second)
			finished := started.Add(time.Duration(100+r) * time.Millisecond)
			record := cacheanalytics.CacheRequestRecord{
				LLMRequestID: fmt.Sprintf("req-%d-%d", s, r),
				SessionID:    sessionID,
				TraceID:      fmt.Sprintf("trace-%d-%d", s, r/5),
				Status:       cacheanalytics.RequestStatusSuccess,
				StartedAt:    started,
				FinishedAt:   &finished,
				Usage: &cacheanalytics.CacheUsage{
					PromptTokens:     int64(100 + r),
					CompletionTokens: int64(10 + r),
					TotalTokens:      int64(110 + 2*r),
				},
			}
			collector.persistRequestTerminal(record, SessionMeta{
				Provider: "acme",
				Model:    fmt.Sprintf("m%d", s%4),
			}, started, finished)
		}
	}
	return store
}

func BenchmarkListSessions(b *testing.B) {
	store := seedStatsBenchmarkFixture(b, 200, 50)
	defer func() { _ = store.Close() }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.ListSessions(Query{Limit: 50}); err != nil {
			b.Fatalf("list: %v", err)
		}
	}
}

func BenchmarkSummarize(b *testing.B) {
	store := seedStatsBenchmarkFixture(b, 200, 50)
	defer func() { _ = store.Close() }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Summarize(Query{GroupBy: "day"}); err != nil {
			b.Fatalf("summarize: %v", err)
		}
	}
}

func BenchmarkDimensions(b *testing.B) {
	store := seedStatsBenchmarkFixture(b, 200, 50)
	defer func() { _ = store.Close() }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Dimensions(Query{}); err != nil {
			b.Fatalf("dimensions: %v", err)
		}
	}
}

// BenchmarkRequestTerminalLegacyWrite 对照基准：改造前的两条独立 UPSERT
// （不做预聚合维护），用于量化 §11.3 的写放大阈值（新路径 ≥ 旧路径 −10%）。
func BenchmarkRequestTerminalLegacyWrite(b *testing.B) {
	store, err := Open(Config{Path: filepath.Join(b.TempDir(), DefaultDBFileName)})
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()
	collector := newCollector(store, nil, nil)
	finished := time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)
	started := finished.Add(-time.Second)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		record := cacheanalytics.CacheRequestRecord{
			LLMRequestID: fmt.Sprintf("req-%d", i),
			SessionID:    "sess-bench",
			TraceID:      fmt.Sprintf("trace-%d", i),
			Status:       cacheanalytics.RequestStatusSuccess,
			StartedAt:    started,
			FinishedAt:   &finished,
		}
		collector.upsertRequest(record)
		collector.upsertSession("sess-bench", SessionMeta{}, started, finished)
	}
}

// openLegacyDSNStore 复刻改造前的打开方式（普通路径 DSN / 默认 deferred 事务），
// 仅用于 ingest 基线对照：新路径必须相对它满足 ≥ 基线 −10%（§11.3）。
func openLegacyDSNStore(b *testing.B, path string) *Store {
	b.Helper()
	store := &Store{path: path}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		b.Fatalf("open legacy dsn: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store.db = db
	if err := store.migrate(0); err != nil {
		b.Fatalf("migrate legacy dsn: %v", err)
	}
	return store
}

// BenchmarkRequestTerminalLegacyBaseline 改造前基线：普通 DSN + 两条独立 UPSERT。
func BenchmarkRequestTerminalLegacyBaseline(b *testing.B) {
	store := openLegacyDSNStore(b, filepath.Join(b.TempDir(), DefaultDBFileName))
	defer func() { _ = store.Close() }()
	collector := newCollector(store, nil, nil)
	finished := time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)
	started := finished.Add(-time.Second)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		record := cacheanalytics.CacheRequestRecord{
			LLMRequestID: fmt.Sprintf("req-%d", i),
			SessionID:    "sess-bench",
			TraceID:      fmt.Sprintf("trace-%d", i),
			Status:       cacheanalytics.RequestStatusSuccess,
			StartedAt:    started,
			FinishedAt:   &finished,
		}
		collector.upsertRequest(record)
		collector.upsertSession("sess-bench", SessionMeta{}, started, finished)
	}
}
