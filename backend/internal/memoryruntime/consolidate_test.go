package memoryruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func newConsolidatorForTest(t *testing.T) (*Consolidator, *memorystore.Store) {
	t.Helper()
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on", InjectionMode: "summary"})
	if err != nil {
		t.Fatalf("memorystore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c := NewConsolidator(store, Config{Mode: "shadow", MaxConsolidationRecords: 20, SummaryTokenBudget: 400})
	c.Clock = func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) }
	return c, store
}

func TestConsolidatorPromotesCandidatesAndCompilesSummary(t *testing.T) {
	c, store := newConsolidatorForTest(t)
	if _, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "decision", Text: "Use append-only memory writes.",
		Confidence: 0.9, SourceHash: "hash-a", SourceRevision: 1,
	}); err != nil {
		t.Fatalf("InsertCandidate: %v", err)
	}
	result, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Consolidator.Run: %v", err)
	}
	if result.Promoted != 1 || result.SummaryID == "" || result.Revision != 1 {
		t.Fatalf("unexpected consolidation result: %+v", result)
	}
	if !strings.Contains(result.SummaryText, "Use append-only memory writes.") {
		t.Fatalf("summary missing candidate: %q", result.SummaryText)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), "memory_summary.md")); err != nil {
		t.Fatalf("memory_summary.md missing: %v", err)
	}
	active, err := store.ListRecords(context.Background(), memorystore.RecordFilter{Status: memorystore.StatusActive, Limit: 10})
	if err != nil || active.Count != 1 {
		t.Fatalf("active records = %d, %v", active.Count, err)
	}
	summaries, err := store.ListSummaries(context.Background(), 10)
	if err != nil || len(summaries) != 1 || summaries[0].Status != "active" {
		t.Fatalf("summaries = %+v, %v", summaries, err)
	}
	selection, err := store.SelectForInjectV2(memorystore.InjectOptions{Query: "memory", Limit: 5, TokenBudget: 900})
	if err != nil {
		t.Fatalf("SelectForInjectV2: %v", err)
	}
	if selection.Summary == nil || selection.Summary.Revision != 1 {
		t.Fatalf("active summary not injectable: %+v", selection.Summary)
	}
}

func TestConsolidatorKeepsLowConfidenceCandidatesOutOfActive(t *testing.T) {
	c, store := newConsolidatorForTest(t)
	candidate, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "decision", Text: "Low confidence inference.",
		Confidence: 0.2, SourceHash: "hash-low", SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("InsertCandidate: %v", err)
	}
	result, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Consolidator.Run: %v", err)
	}
	if result.Promoted != 0 {
		t.Fatalf("low-confidence candidate promoted: %+v", result)
	}
	record, err := store.GetRecord(context.Background(), candidate.ID)
	if err != nil || record.Status != memorystore.StatusCandidate {
		t.Fatalf("record = %+v, %v", record, err)
	}
}

func TestConsolidatorManualBeatsDerivedConflict(t *testing.T) {
	c, store := newConsolidatorForTest(t)
	manual, err := store.Append(memorystore.AppendNoteOptions{
		Text: "Use append-only memory writes.", Kind: "decision",
	})
	if err != nil {
		t.Fatalf("Append manual: %v", err)
	}
	candidate, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "fact", Text: "Use append-only memory writes.",
		Confidence: 0.9, SourceHash: "hash-a", SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("InsertCandidate: %v", err)
	}
	result, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Consolidator.Run: %v", err)
	}
	if result.Promoted != 1 || result.Superseded != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	manualRecord, err := store.GetRecord(context.Background(), manual.ID)
	if err != nil || manualRecord.Status != memorystore.StatusActive {
		t.Fatalf("manual status = %+v, %v", manualRecord, err)
	}
	if manualRecord.SupersedesID != candidate.ID {
		t.Fatalf("manual winner supersedes_id = %q, want %q", manualRecord.SupersedesID, candidate.ID)
	}
	derived, err := store.GetRecord(context.Background(), candidate.ID)
	if err != nil || derived.Status != memorystore.StatusSuperseded {
		t.Fatalf("derived status = %+v, %v", derived, err)
	}
	events, err := store.ListEvents(context.Background(), 50)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	foundSupersedeEvent := false
	for _, event := range events {
		if event.MemoryID == candidate.ID && event.EventType == "superseded" {
			foundSupersedeEvent = true
			if event.Actor != "consolidation" || event.Payload["superseded_by"] != manual.ID {
				t.Fatalf("unexpected supersede event: %+v", event)
			}
		}
	}
	if !foundSupersedeEvent {
		t.Fatal("missing consolidation supersede event")
	}
	summaries, err := store.ListSummaries(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("manual-only consolidation must not compile a duplicate summary: %+v", summaries)
	}
}

func TestConsolidatorSameHourJobIsIdempotent(t *testing.T) {
	c, store := newConsolidatorForTest(t)
	if _, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "workflow", Text: "Run tests before merging.",
		Confidence: 0.8, SourceHash: "hash-a", SourceRevision: 1,
	}); err != nil {
		t.Fatalf("InsertCandidate: %v", err)
	}
	first, err := c.Run(context.Background())
	if err != nil || first.SummaryID == "" {
		t.Fatalf("first Run = %+v, %v", first, err)
	}
	second, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second.SummaryID != "" || second.Promoted != 0 {
		t.Fatalf("same-hour consolidation should be idempotent: %+v", second)
	}
	summaries, err := store.ListSummaries(context.Background(), 10)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summaries = %+v, %v", summaries, err)
	}
}

func TestConsolidatorRebuildsStaleSummaryIdempotently(t *testing.T) {
	c, store := newConsolidatorForTest(t)
	candidate, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "decision", Text: "Use append-only memory writes.",
		Confidence: 0.9, SourceHash: "hash-a", SourceRevision: 1,
	})
	if err != nil {
		t.Fatalf("InsertCandidate: %v", err)
	}
	first, err := c.Run(context.Background())
	if err != nil || first.Revision != 1 {
		t.Fatalf("first Run = %+v, %v", first, err)
	}
	corrected := "Use revisioned memory writes."
	if _, err := store.UpdateRecord(context.Background(), candidate.ID, memorystore.RecordPatch{Text: &corrected}); err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	active, err := store.ActiveSummary(context.Background())
	if err != nil || active == nil || active.Freshness != memorystore.FreshnessStale {
		t.Fatalf("active summary after mutation = %+v, %v", active, err)
	}
	second, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second.Rebuilt != 1 || second.Revision != 2 {
		t.Fatalf("expected one summary rebuild to revision 2: %+v", second)
	}
	selection, err := store.SelectForInjectV2(memorystore.InjectOptions{Query: "memory", Limit: 5, TokenBudget: 900})
	if err != nil || selection.Summary == nil || selection.Summary.Revision != 2 {
		t.Fatalf("rebuilt summary not injectable: %+v, %v", selection.Summary, err)
	}
	third, err := c.Run(context.Background())
	if err != nil || third.Rebuilt != 0 {
		t.Fatalf("repeat rebuild should be idempotent: %+v, %v", third, err)
	}
	summaries, err := store.ListSummaries(context.Background(), 10)
	if err != nil || len(summaries) != 2 {
		t.Fatalf("summaries = %+v, %v", summaries, err)
	}
	if summaries[0].Revision != 2 || summaries[0].Status != "active" || summaries[1].Status != "superseded" {
		t.Fatalf("unexpected summary revisions: %+v", summaries)
	}
}

func TestConsolidatorPreservesSamePriorityAmbiguity(t *testing.T) {
	c, store := newConsolidatorForTest(t)
	for _, kind := range []string{"pitfall", "workflow"} {
		if _, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
			SessionID: "s1", Kind: kind, Text: "Use the canonical bootstrap path.",
			Confidence: 0.9, SourceHash: "hash-" + kind, SourceRevision: 1,
		}); err != nil {
			t.Fatalf("InsertCandidate(%s): %v", kind, err)
		}
	}
	result, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Consolidator.Run: %v", err)
	}
	if result.OpenQuestions != 2 || result.Superseded != 0 {
		t.Fatalf("ambiguous conflict must be preserved: %+v", result)
	}
	active, err := store.ListRecords(context.Background(), memorystore.RecordFilter{Status: memorystore.StatusActive, Limit: 10})
	if err != nil || active.Count != 2 {
		t.Fatalf("active records = %+v, %v", active, err)
	}
}
