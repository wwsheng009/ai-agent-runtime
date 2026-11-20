package memoryruntime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestRecoverPendingPhase1ProcessesMatchingPendingJob(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", WorkspacePath: "/workspace",
			State: "active", LastActivityAt: now.Add(-2 * time.Hour),
		}},
		transcripts: map[string]Transcript{"s1": validTranscript("s1", "hash-a")},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "recover stranded phase1 jobs",
		Confidence: 0.9, SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, store := newRuntimeForTest(t, "on", source, extractor)

	_, err := store.EnsureJob(context.Background(), memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: "session:s1:hash-a",
		ProjectKey: store.ProjectKey(), MaxAttempts: 3,
	})
	require.NoError(t, err)

	result, err := runtime.RecoverPendingPhase1(context.Background(), TriggerStartup)
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Processed)
	require.Equal(t, 1, extractor.calls)

	page, err := store.ListRecords(context.Background(), memorystore.RecordFilter{
		Status: memorystore.StatusCandidate, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
}

func TestRecoverPendingPhase1RespectsIdleGate(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", WorkspacePath: "/workspace",
			State: "active", LastActivityAt: now.Add(-10 * time.Minute),
		}},
		transcripts: map[string]Transcript{"s1": validTranscript("s1", "hash-a")},
	}
	extractor := &fakeExtractor{}
	runtime, store := newRuntimeForTest(t, "on", source, extractor)

	_, err := store.EnsureJob(context.Background(), memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: "session:s1:hash-a",
		ProjectKey: store.ProjectKey(), MaxAttempts: 3,
	})
	require.NoError(t, err)

	result, err := runtime.RecoverPendingPhase1(context.Background(), TriggerStartup)
	require.NoError(t, err)
	require.EqualValues(t, 0, result.Processed)
	require.Equal(t, 0, extractor.calls)
}

func TestRecoverPendingPhase1CancelsStaleBaseJob(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		sessions: []SessionRef{{
			ID: "s1", WorkspacePath: "/workspace",
			State: "active", LastActivityAt: now.Add(-2 * time.Hour),
		}},
		transcripts: map[string]Transcript{"s1": validTranscript("s1", "hash-b")},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: "new transcript hash wins",
		Confidence: 0.9, SourceMessageIDs: []string{"msg-2"},
	}}}}
	runtime, store := newRuntimeForTest(t, "on", source, extractor)

	_, err := store.EnsureJob(context.Background(), memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: "session:s1:hash-a",
		ProjectKey: store.ProjectKey(), MaxAttempts: 3,
	})
	require.NoError(t, err)

	result, err := runtime.RecoverPendingPhase1(context.Background(), TriggerStartup)
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Processed)
	require.Equal(t, 1, extractor.calls)

	jobs, err := store.ListJobs(context.Background(), 10)
	require.NoError(t, err)
	statuses := make(map[string]memorystore.JobStatus)
	for _, job := range jobs {
		statuses[job.JobKey] = job.Status
	}
	require.Equal(t, memorystore.JobStatusCancelled, statuses["session:s1:hash-a"])
	require.Equal(t, memorystore.JobStatusSucceeded, statuses["session:s1:hash-b"])
}

func TestPhase1JobSessionID(t *testing.T) {
	cases := map[string]string{
		"session:s1:hash-a":                                     "s1",
		"session:s1:window:1-20:hash-a":                         "s1",
		"session:s1:byte-pages-v1:1-20:source-hash:0:page-hash": "s1",
		"project:pk:202611170000":                               "",
		"":                                                      "",
	}
	for key, want := range cases {
		require.Equal(t, want, phase1JobSessionID(key), "job key %q", key)
	}
}
