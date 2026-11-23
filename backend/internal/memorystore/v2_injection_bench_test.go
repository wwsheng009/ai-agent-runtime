package memorystore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

func BenchmarkSelectForInjectV2BoundedCandidates(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("records-%d", count), func(b *testing.B) {
			store, err := New(Config{Root: b.TempDir(), Mode: "on"})
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			b.StopTimer()
			seedBenchInjectionRecords(b, store, count)
			b.ResetTimer()
			start := time.Now()
			for i := 0; i < b.N; i++ {
				selection, err := store.SelectForInjectV2(InjectOptions{
					Query: "E_BENCH_TARGET", Limit: 5, TokenBudget: 4000, SessionID: "bench",
				})
				if err != nil {
					b.Fatal(err)
				}
				if selection.CandidateCount > 300 {
					b.Fatalf("candidate count = %d, want <= 300", selection.CandidateCount)
				}
			}
			b.ReportMetric(float64(time.Since(start).Nanoseconds())/float64(b.N), "wall-ns/op")
		})
	}
}

func seedBenchInjectionRecords(b *testing.B, store *Store, count int) {
	b.Helper()
	ctx := context.Background()
	db, err := store.ensureV2()
	if err != nil {
		b.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	for i := 0; i < count; i++ {
		text := fmt.Sprintf("benchmark filler memory %06d", i)
		if i == count-1 {
			text = "unique benchmark needle E_BENCH_TARGET"
		}
		record := Record{
			ID:              fmt.Sprintf("bench-%06d", i),
			Scope:           ScopeProject,
			ProjectKey:      store.ProjectKey(),
			Kind:            "fact",
			Status:          StatusActive,
			Text:            text,
			NormalizedText:  normalizeMemoryText(text),
			Confidence:      0.9,
			SourceKind:      "session_extract",
			SourceSessionID: "bench-session",
			SourceRefs:      []string{"bench"},
			ContentHash:     memoryContentHash(string(ScopeProject), store.ProjectKey(), "fact", text),
			Revision:        1,
			CreatedAt:       now,
			UpdatedAt:       now,
			Freshness:       FreshnessFresh,
			LastVerifiedAt:  now,
			ValidFrom:       now,
		}
		if err := store.insertRecordSQLTxWithOptions(ctx, tx, record, "bench", "bench-session", recordInsertOptions{}); err != nil {
			b.Fatalf("seed record %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}
