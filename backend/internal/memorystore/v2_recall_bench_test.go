package memorystore

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func benchmarkRecallCandidates(recordCount int) ([]Record, []recallCandidate) {
	records := make([]Record, 0, recordCount)
	candidates := make([]recallCandidate, 0, recordCount)
	kinds := []string{"decision", "fact", "preference", "workflow"}
	for i := 0; i < recordCount; i++ {
		record := Record{
			ID:   "memory-" + strconv.Itoa(i),
			Kind: kinds[i%len(kinds)],
			Text: "shared retrieval benchmark topic " + strconv.Itoa(i),
		}
		records = append(records, record)
		candidates = append(candidates, recallCandidate{
			recordIndex: i, score: float64(recordCount - i),
		})
	}
	return records, candidates
}

// diversifyRecallOrderLinearBaseline preserves the pre-index lookup shape so
// the benchmark can quantify the removed O(candidates*records) scan.
func diversifyRecallOrderLinearBaseline(
	candidates []recallCandidate,
	records []Record,
	limit int,
) []recallCandidate {
	if limit <= 1 || len(candidates) < 2 {
		return candidates
	}
	perKindCap := limit / 2
	if perKindCap < 1 {
		perKindCap = 1
	}
	counts := make(map[string]int)
	ordered := make([]recallCandidate, 0, len(candidates))
	deferred := make([]recallCandidate, 0, len(candidates))
	for _, hit := range candidates {
		id := records[hit.recordIndex].ID
		record := Record{}
		for _, candidate := range records {
			if candidate.ID == id {
				record = candidate
				break
			}
		}
		kind := strings.ToLower(strings.TrimSpace(record.Kind))
		if kind == "" {
			kind = "memory"
		}
		if counts[kind] < perKindCap {
			ordered = append(ordered, hit)
			counts[kind]++
			continue
		}
		deferred = append(deferred, hit)
	}
	return append(ordered, deferred...)
}

func BenchmarkDiversifyRecallOrderLookup(b *testing.B) {
	records, candidates := benchmarkRecallCandidates(2048)
	b.Run("indexed", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := diversifyRecallOrder(candidates, records, 10); len(got) != len(candidates) {
				b.Fatalf("indexed order returned %d candidates, want %d", len(got), len(candidates))
			}
		}
	})
	b.Run("linear", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := diversifyRecallOrderLinearBaseline(candidates, records, 10); len(got) != len(candidates) {
				b.Fatalf("linear order returned %d candidates, want %d", len(got), len(candidates))
			}
		}
	})
}

func BenchmarkSelectForInjectV2LargeActiveSet(b *testing.B) {
	for _, recordCount := range []int{512, 2048} {
		b.Run(strconv.Itoa(recordCount), func(b *testing.B) {
			ctx := context.Background()
			store, err := New(Config{
				ProjectRoot: b.TempDir(), Mode: "on", InjectionMode: "notes",
				PinnedBudgetRatio: 0.2,
			})
			if err != nil {
				b.Fatalf("New: %v", err)
			}
			b.Cleanup(func() { _ = store.Close() })
			kinds := []string{"decision", "fact", "preference", "workflow"}
			for i := 0; i < recordCount; i++ {
				record, insertErr := store.InsertCandidate(ctx, Candidate{
					SessionID: "bench-session", Kind: kinds[i%len(kinds)],
					Text:       "shared retrieval benchmark topic " + strconv.Itoa(i),
					Confidence: 0.9, SourceHash: "bench-hash-" + strconv.Itoa(i),
					SourceRevision: int64(i + 1), RetentionClass: "permanent",
				})
				if insertErr != nil {
					b.Fatalf("InsertCandidate: %v", insertErr)
				}
				if _, insertErr = store.PromoteCandidate(ctx, record.ID); insertErr != nil {
					b.Fatalf("PromoteCandidate: %v", insertErr)
				}
			}
			now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				selection, selectErr := store.SelectForInjectV2(InjectOptions{
					Query: "shared retrieval benchmark topic", Limit: 10,
					TokenBudget: 600, SkipTrace: true, Now: now,
				})
				if selectErr != nil {
					b.Fatalf("SelectForInjectV2: %v", selectErr)
				}
				if len(selection.Recall) == 0 {
					b.Fatal("expected recall candidates")
				}
			}
		})
	}
}
