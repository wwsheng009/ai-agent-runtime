package memoryruntime

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// TestAutomaticMemoryEndToEndWithoutManualSeed deliberately never calls
// Append/manual memory. It proves the normal path is:
// session transcript -> candidate -> Consolidation -> active summary -> recall.
func TestAutomaticMemoryEndToEndWithoutManualSeed(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	store, err := memorystore.New(memorystore.Config{
		Root: t.TempDir(), Mode: "on", InjectionMode: "hybrid",
		PinnedBudgetRatio: 0.2, RecallTraceEnabled: true, RecallTraceSampleRate: 1,
	})
	if err != nil {
		t.Fatalf("memorystore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "session-auto", WorkspacePath: "/workspace",
			LastActivityAt: now.Add(-2 * time.Hour),
		}},
		transcripts: map[string]Transcript{
			"session-auto": validTranscript("session-auto", "hash-auto"),
		},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "Automatically preserve append-only memory decisions.",
		Confidence: 0.92, SourceMessageIDs: []string{"msg-2"},
	}}}}
	cfg := Config{
		Mode: "on", Workspace: "/workspace", ProjectKey: store.ProjectKey(),
		MinIdle: time.Hour, MaxSessionsPerRun: 5, MaxSourceMessages: 20,
		MaxCandidatesPerSession: 20, MaxCandidateTextRunes: 4000,
		MaxConsolidationRecords: 20, SummaryTokenBudget: 400,
	}
	runtime := New(store, source, extractor, fakeModes{}, cfg)
	runtime.Clock = func() time.Time { return now }

	result, err := runtime.RunSession(ctx, TriggerSessionEnd, "session-auto")
	if err != nil {
		t.Fatalf("RunSession(session_end): %v", err)
	}
	if result.Processed != 1 || result.Candidates != 1 || extractor.calls != 1 {
		t.Fatalf("automatic extraction result = %+v, extractor calls=%d", result, extractor.calls)
	}
	candidates, err := store.ListRecords(ctx, memorystore.RecordFilter{
		Status: memorystore.StatusCandidate, Limit: 10,
	})
	if err != nil || candidates.Count != 1 {
		t.Fatalf("candidate records = %d, %v", candidates.Count, err)
	}
	if candidates.Items[0].SourceKind != "session_extract" {
		t.Fatalf("candidate source = %q", candidates.Items[0].SourceKind)
	}

	consolidator := NewConsolidator(store, cfg)
	consolidator.Clock = func() time.Time { return now }
	consolidated, err := consolidator.Run(ctx)
	if err != nil {
		t.Fatalf("Consolidator.Run: %v", err)
	}
	if consolidated.Promoted != 1 || consolidated.SummaryID == "" {
		t.Fatalf("consolidation result = %+v", consolidated)
	}
	active, err := store.ListRecords(ctx, memorystore.RecordFilter{
		Status: memorystore.StatusActive, Limit: 10,
	})
	if err != nil || active.Count != 1 || active.Items[0].SourceKind != "session_extract" {
		t.Fatalf("active records = %+v, %v", active, err)
	}

	selection, err := store.SelectForInjectV2(memorystore.InjectOptions{
		Query: "append-only memory", Limit: 5, TokenBudget: 900, SessionID: "next-session",
	})
	if err != nil {
		t.Fatalf("SelectForInjectV2: %v", err)
	}
	if selection.Summary == nil || selection.Summary.Revision == 0 {
		t.Fatalf("automatic summary was not selected: %+v", selection.Summary)
	}
	found := false
	for _, id := range selection.IDs {
		if id == active.Items[0].ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("automatic record missing from selection IDs: %+v", selection.IDs)
	}
	traces, err := store.ListRecallTraces(ctx, "next-session", 10)
	if err != nil || len(traces) != 1 || !traces[0].Injected {
		t.Fatalf("recall trace = %+v, %v", traces, err)
	}
}
