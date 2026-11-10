package memoryruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type fakeSource struct {
	sessions    []SessionRef
	transcripts map[string]Transcript
	listCalls   int
	readCalls   int
	readErr     error
}

func (s *fakeSource) ListSessions(context.Context, string, int) ([]SessionRef, error) {
	s.listCalls++
	return append([]SessionRef(nil), s.sessions...), nil
}

func (s *fakeSource) ReadTranscript(_ context.Context, session SessionRef, _ int) (Transcript, error) {
	s.readCalls++
	if s.readErr != nil {
		return Transcript{}, s.readErr
	}
	return s.transcripts[session.ID], nil
}

type fakeExtractor struct {
	result ExtractionResult
	err    error
	calls  int
}

func (e *fakeExtractor) Extract(context.Context, SessionRef, Transcript) (ExtractionResult, error) {
	e.calls++
	return e.result, e.err
}

type fakeModes map[string]memorystore.SessionMode

func (m fakeModes) GetSessionMode(_ context.Context, sessionID string) (memorystore.SessionMode, error) {
	if mode, ok := m[sessionID]; ok {
		return mode, nil
	}
	return memorystore.SessionModeEnabled, nil
}

func newRuntimeForTest(t *testing.T, mode string, source *fakeSource, extractor *fakeExtractor) (*Runtime, *memorystore.Store) {
	t.Helper()
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("memorystore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runtime := New(store, source, extractor, fakeModes{}, Config{
		Mode: mode, Workspace: "/workspace", ProjectKey: store.ProjectKey(),
		MinIdle: time.Hour, MaxSessionsPerRun: 5, MaxSourceMessages: 20,
		MaxCandidatesPerSession: 20, MaxCandidateTextRunes: 4000,
	})
	runtime.Clock = func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) }
	return runtime, store
}

func validTranscript(sessionID, hash string) Transcript {
	return Transcript{
		SessionID: sessionID, ProjectKey: "", Hash: hash, Revision: 3,
		UpdatedAt: time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC),
		Messages: []TranscriptMessage{
			{ID: "msg-1", Seq: 1, Role: "user", Content: "remember this project decision"},
			{ID: "msg-2", Seq: 2, Role: "assistant", Content: "use append-only memory writes"},
		},
	}
}

func TestRuntimeOffMakesNoCalls(t *testing.T) {
	source := &fakeSource{}
	extractor := &fakeExtractor{}
	runtime, _ := newRuntimeForTest(t, "off", source, extractor)
	result, err := runtime.Run(context.Background(), TriggerIdle)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Sessions != 0 || source.listCalls != 0 || extractor.calls != 0 {
		t.Fatalf("off made calls: result=%+v list=%d extract=%d", result, source.listCalls, extractor.calls)
	}
}

func TestRuntimeGenerationDisabledBlocksIdleButAllowsManual(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	transcript := validTranscript("s1", "hash-a")
	source := &fakeSource{
		sessions:    []SessionRef{{ID: "s1", WorkspacePath: "/workspace", LastActivityAt: now.Add(-2 * time.Hour)}},
		transcripts: map[string]Transcript{"s1": transcript},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "manual extraction remains available", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, _ := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Config.GenerationDisabled = true
	if _, err := runtime.Run(context.Background(), TriggerIdle); err != nil {
		t.Fatalf("idle Run: %v", err)
	}
	if extractor.calls != 0 {
		t.Fatalf("generation-disabled idle triggered extractor %d times", extractor.calls)
	}
	result, err := runtime.RunSession(context.Background(), TriggerManual, "s1")
	if err != nil || result.Processed != 1 {
		t.Fatalf("manual run = %+v, %v", result, err)
	}
}

func TestRuntimeSkipsSubagentEphemeralWorkspaceAndMode(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{
			{ID: "idle", WorkspacePath: "/workspace", LastActivityAt: now.Add(-2 * time.Hour)},
			{ID: "subagent", WorkspacePath: "/workspace", IsSubagent: true, LastActivityAt: now.Add(-2 * time.Hour)},
			{ID: "ephemeral", WorkspacePath: "/workspace", Ephemeral: true, LastActivityAt: now.Add(-2 * time.Hour)},
			{ID: "other", WorkspacePath: "/other", LastActivityAt: now.Add(-2 * time.Hour)},
			{ID: "fresh", WorkspacePath: "/workspace", LastActivityAt: now.Add(-10 * time.Minute)},
		},
		transcripts: map[string]Transcript{"idle": validTranscript("idle", "hash-idle")},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "Use append-only memory writes.", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, _ := newRuntimeForTest(t, "shadow", source, extractor)
	runtime.Modes = fakeModes{"idle": memorystore.SessionModeEnabled, "subagent": memorystore.SessionModeEnabled}
	result, err := runtime.Run(context.Background(), TriggerIdle)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Processed != 1 || extractor.calls != 1 {
		t.Fatalf("expected only idle session processed, result=%+v calls=%d", result, extractor.calls)
	}
}

func TestRuntimeWritesCandidatesAndSkipsSecretSourceLess(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions:    []SessionRef{{ID: "s1", WorkspacePath: "/workspace", LastActivityAt: now.Add(-2 * time.Hour)}},
		transcripts: map[string]Transcript{"s1": validTranscript("s1", "hash-a")},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{
		{Kind: "decision", Text: "Use append-only memory writes.", Confidence: 0.9, SourceMessageIDs: []string{"msg-2"}},
		{Kind: "fact", Text: "token sk-1234567890abcdef", Confidence: 0.9, SourceMessageIDs: []string{"msg-1"}},
		{Kind: "reference", Text: "source-less candidate", Confidence: 0.9},
		{Kind: "not-a-kind", Text: "bad kind", Confidence: 0.9, SourceMessageIDs: []string{"msg-1"}},
	}}}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	result, err := runtime.RunSession(context.Background(), TriggerManual, "s1")
	if err != nil {
		t.Fatalf("RunSession: %v", err)
	}
	if result.Processed != 1 || result.Candidates != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	foundSecret := false
	for _, reason := range result.Ignored {
		if strings.HasPrefix(reason, "secret:") {
			foundSecret = true
		}
	}
	if !foundSecret {
		t.Fatalf("expected secret ignore reason, got %+v", result.Ignored)
	}
	page, err := store.ListRecords(context.Background(), memorystore.RecordFilter{Status: memorystore.StatusCandidate, Limit: 10})
	if err != nil || page.Count != 1 {
		t.Fatalf("candidate records = %d, %v", page.Count, err)
	}
	if page.Items[0].SourceKind != "session_extract" || page.Items[0].Status != memorystore.StatusCandidate {
		t.Fatalf("unexpected candidate: %+v", page.Items[0])
	}
}

func TestRuntimeSameSourceIsIdempotent(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	transcript := validTranscript("s1", "hash-a")
	source := &fakeSource{
		sessions:    []SessionRef{{ID: "s1", WorkspacePath: "/workspace", LastActivityAt: now.Add(-2 * time.Hour)}},
		transcripts: map[string]Transcript{"s1": transcript},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "Use append-only memory writes.", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, _ := newRuntimeForTest(t, "shadow", source, extractor)
	if _, err := runtime.RunSession(context.Background(), TriggerManual, "s1"); err != nil {
		t.Fatalf("first RunSession: %v", err)
	}
	result, err := runtime.RunSession(context.Background(), TriggerManual, "s1")
	if err != nil {
		t.Fatalf("second RunSession: %v", err)
	}
	if extractor.calls != 1 {
		t.Fatalf("same source extracted %d times", extractor.calls)
	}
	if result.Skipped != 1 {
		t.Fatalf("expected second run skipped, got %+v", result)
	}
}

func TestRuntimeSourceChangeMarksOldCandidateStale(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	first := validTranscript("s1", "hash-a")
	second := validTranscript("s1", "hash-b")
	second.Messages[1].Content = "a materially changed decision"
	source := &fakeSource{
		sessions:    []SessionRef{{ID: "s1", WorkspacePath: "/workspace", LastActivityAt: now.Add(-2 * time.Hour)}},
		transcripts: map[string]Transcript{"s1": first},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "Use append-only memory writes.", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	if _, err := runtime.RunSession(context.Background(), TriggerManual, "s1"); err != nil {
		t.Fatalf("first RunSession: %v", err)
	}
	page, err := store.ListRecords(context.Background(), memorystore.RecordFilter{Status: memorystore.StatusCandidate, Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("first candidate count=%d err=%v", len(page.Items), err)
	}
	oldID := page.Items[0].ID
	source.transcripts["s1"] = second
	extractor.result.Candidates[0].Text = "Use a newer memory decision."
	if _, err := runtime.RunSession(context.Background(), TriggerManual, "s1"); err != nil {
		t.Fatalf("second RunSession: %v", err)
	}
	oldRecord, err := store.GetRecord(context.Background(), oldID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if oldRecord.Freshness != memorystore.FreshnessStale || oldRecord.StaleReason != "source_changed" {
		t.Fatalf("old candidate not stale: %+v", oldRecord)
	}
}

func TestRuntimeExtractionErrorRetriesJob(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions:    []SessionRef{{ID: "s1", WorkspacePath: "/workspace", LastActivityAt: now.Add(-2 * time.Hour)}},
		transcripts: map[string]Transcript{"s1": validTranscript("s1", "hash-a")},
	}
	extractor := &fakeExtractor{err: errors.New("invalid json")}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	if _, err := runtime.RunSession(context.Background(), TriggerManual, "s1"); err == nil {
		t.Fatal("expected extraction error")
	}
	jobs, err := store.ListJobs(context.Background(), 10)
	if err != nil || len(jobs) != 1 || jobs[0].Status != memorystore.JobStatusPending || jobs[0].Attempt != 1 {
		t.Fatalf("job after failed extraction: %+v, %v", jobs, err)
	}
}

func TestRuntimeSameContentSourceChangeRevivesCandidate(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	first := validTranscript("s1", "hash-a")
	second := validTranscript("s1", "hash-b")
	source := &fakeSource{
		sessions:    []SessionRef{{ID: "s1", WorkspacePath: "/workspace", LastActivityAt: now.Add(-2 * time.Hour)}},
		transcripts: map[string]Transcript{"s1": first},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "Use append-only memory writes.", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, store := newRuntimeForTest(t, "shadow", source, extractor)
	if _, err := runtime.RunSession(context.Background(), TriggerManual, "s1"); err != nil {
		t.Fatalf("first RunSession: %v", err)
	}
	page, err := store.ListRecords(context.Background(), memorystore.RecordFilter{Status: memorystore.StatusCandidate, Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("first candidate count=%d err=%v", len(page.Items), err)
	}
	id := page.Items[0].ID
	source.transcripts["s1"] = second
	if _, err := runtime.RunSession(context.Background(), TriggerManual, "s1"); err != nil {
		t.Fatalf("second RunSession: %v", err)
	}
	record, err := store.GetRecord(context.Background(), id)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if record.Freshness != memorystore.FreshnessFresh || record.Revision < 2 {
		t.Fatalf("same-content re-extract should revive candidate: %+v", record)
	}
}
