package memoryruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

type fakeProjectSummarizer struct {
	output ProjectSummaryOutput
	err    error
	calls  int
}

func (f *fakeProjectSummarizer) SummarizeProject(context.Context, ProjectSummaryInput) (ProjectSummaryOutput, error) {
	f.calls++
	return f.output, f.err
}

func TestConsolidatorUsesProjectSummaryGenerator(t *testing.T) {
	c, store := newConsolidatorForTest(t)
	_, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "decision", Text: "Use append-only memory writes.",
		Confidence: 0.9, SourceHash: "hash-llm-summary", SourceRevision: 1,
	})
	require.NoError(t, err)
	summarizer := &fakeProjectSummarizer{output: ProjectSummaryOutput{
		Text:  "## Project decisions\n- Use append-only memory writes. (id=seed)",
		Model: "custom-summary-model",
		Usage: TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}}
	c.Config.Summarizer = summarizer

	result, err := c.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, summarizer.calls)
	require.Equal(t, summarizer.output.Text, result.SummaryText)
	require.Equal(t, "custom-summary-model", result.SummaryModel)

	summary, err := store.ActiveSummary(context.Background())
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.Equal(t, "llm_v1", summary.Metadata["compiler"])
	require.Equal(t, "custom-summary-model", summary.Model)
}

func TestConsolidatorFallsBackWhenProjectSummaryGeneratorFails(t *testing.T) {
	c, store := newConsolidatorForTest(t)
	_, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "decision", Text: "Use deterministic fallback.",
		Confidence: 0.9, SourceHash: "hash-fallback-summary", SourceRevision: 1,
	})
	require.NoError(t, err)
	c.Config.Summarizer = &fakeProjectSummarizer{err: context.DeadlineExceeded}

	result, err := c.Run(context.Background())
	require.NoError(t, err)
	require.Contains(t, result.SummaryText, "Use deterministic fallback.")
	summary, err := store.ActiveSummary(context.Background())
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.Equal(t, "deterministic_v1", summary.Metadata["compiler"])
	require.Contains(t, summary.Metadata["fallback_reason"], "deadline")
}

func TestConsolidatorFallsBackOnInvalidLLMSummaryStructure(t *testing.T) {
	c, store := newConsolidatorForTest(t)
	_, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "decision", Text: "Use structured summary headings.",
		Confidence: 0.9, SourceHash: "hash-invalid-summary", SourceRevision: 1,
	})
	require.NoError(t, err)
	c.Config.Summarizer = &fakeProjectSummarizer{output: ProjectSummaryOutput{
		Text: "plain summary without headings or source ids", Model: "bad-model",
	}}

	result, err := c.Run(context.Background())
	require.NoError(t, err)
	require.Contains(t, result.SummaryText, "Use structured summary headings.")
	summary, err := store.ActiveSummary(context.Background())
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.Equal(t, "deterministic_v1", summary.Metadata["compiler"])
	require.Equal(t, "missing_headings", summary.Metadata["fallback_reason"])
}

func TestDeterministicSummaryDeduplicatesCaseAndWhitespace(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	records := []memorystore.Record{
		{
			ID: "mem-1", Kind: "decision", Text: "Use Stable Summary.",
			SourceKind: "session_extract", Confidence: 0.9, UpdatedAt: now,
		},
		{
			ID: "mem-2", Kind: "decision", Text: "use stable summary.",
			SourceKind: "session_extract", Confidence: 0.8, UpdatedAt: now,
		},
	}
	text, ids, _ := deterministicSummary(records, 400)
	require.Len(t, ids, 1)
	require.Equal(t, "mem-1", ids[0])
	require.Equal(t, 1, strings.Count(strings.ToLower(text), "use stable summary"))
}

func TestDeterministicSummaryPreventsKindStarvation(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	records := make([]memorystore.Record, 0, 21)
	for i := 0; i < 20; i++ {
		records = append(records, memorystore.Record{
			ID: "pref-" + string(rune('a'+i)), Kind: "preference",
			Text:       "Prefer a very specific interaction detail " + string(rune('a'+i)),
			SourceKind: "session_extract", Confidence: 0.9, UpdatedAt: now,
		})
	}
	records = append(records, memorystore.Record{
		ID: "fact-1", Kind: "fact", Text: "The canonical bootstrap path is stable.",
		SourceKind: "session_extract", Confidence: 0.9, UpdatedAt: now,
	})

	text, ids, tokens := deterministicSummary(records, 220)
	require.Contains(t, text, "## Facts")
	require.Contains(t, text, "canonical bootstrap path")
	require.Contains(t, ids, "fact-1")
	require.LessOrEqual(t, tokens, 220)
}

func TestResolveConflictsGroupsDifferentValuesBySubject(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	old := memorystore.Record{
		ID: "old", Kind: "fact", Text: "API timeout is 30s", SourceKind: "session_extract",
		Confidence: 0.6, UpdatedAt: now.Add(-time.Hour),
	}
	newer := memorystore.Record{
		ID: "new", Kind: "fact", Text: "API timeout is 60s", SourceKind: "session_extract",
		Confidence: 0.9, UpdatedAt: now,
	}
	require.Equal(t, conflictSubjectKey(old), conflictSubjectKey(newer))

	active, supersessions, ambiguous := resolveConflicts([]memorystore.Record{old, newer})
	require.Len(t, active, 1)
	require.Equal(t, "new", active[0].ID)
	require.Empty(t, ambiguous)
	require.Len(t, supersessions, 1)
	require.Equal(t, "old", supersessions[0].loserID)
	require.Equal(t, "new", supersessions[0].winnerID)
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
