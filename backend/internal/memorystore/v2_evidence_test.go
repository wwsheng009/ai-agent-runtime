package memorystore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecordSourceEvidenceIsDistinctFromMemoryText(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	defer store.Close()

	record, err := store.InsertCandidate(context.Background(), Candidate{
		SessionID:        "session-evidence",
		Kind:             "decision",
		Text:             "Use append-only memory writes.",
		Confidence:       0.9,
		SourceKind:       "session_extract",
		SourceType:       "transcript_message",
		SourceTrust:      "canonical_transcript",
		SourceQuote:      "[msg-1] The project decision was to keep memory writes append-only.",
		SourceMessageIDs: []string{"msg-1"},
		SourceHash:       "evidence-hash",
		SourceRevision:   3,
	})
	require.NoError(t, err)

	sources, err := store.ListRecordSources(context.Background(), record.ID)
	require.NoError(t, err)
	require.Len(t, sources, 1)
	require.Equal(t, "transcript_message", sources[0].SourceType)
	require.Equal(t, "canonical_transcript", sources[0].SourceTrust)
	require.Equal(t, "[msg-1] The project decision was to keep memory writes append-only.", sources[0].Quote)
	require.NotEqual(t, record.Text, sources[0].Quote)
	require.Equal(t, []string{"msg-1"}, sources[0].MessageIDs)
}

func TestRecordSourceRejectsSecretEvidence(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	defer store.Close()

	_, err = store.InsertCandidate(context.Background(), Candidate{
		SessionID:   "session-secret-evidence",
		Kind:        "fact",
		Text:        "A safe memory claim.",
		Confidence:  0.9,
		SourceQuote: "api_key=supersecretvalue123",
	})
	require.ErrorContains(t, err, "secret-shaped source evidence")
}

func TestExtractionDiagnosticsAreBoundedIdempotentAndProjectScoped(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on", ProjectKey: "project-a"})
	require.NoError(t, err)
	defer store.Close()

	diagnostic := ExtractionDiagnostic{
		ProjectKey: "project-a",
		JobID:      "job-evidence",
		SessionID:  "session-evidence",
		Reason:     "secret:api_key",
		Summary:    "ignored because api_key=supersecretvalue123 was present",
		SourceIDs:  []string{"msg-1", "msg-1"},
		CreatedAt:  time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC),
	}
	require.NoError(t, store.RecordExtractionDiagnostics(context.Background(), []ExtractionDiagnostic{
		diagnostic, diagnostic,
	}))

	rows, err := store.ListExtractionDiagnostics(context.Background(), "job-evidence", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "secret:api_key", rows[0].Reason)
	require.Equal(t, []string{"msg-1"}, rows[0].SourceIDs)
	require.NotContains(t, rows[0].Summary, "supersecretvalue123")

	err = store.RecordExtractionDiagnostics(context.Background(), []ExtractionDiagnostic{{
		ProjectKey: "project-b",
		JobID:      "job-evidence",
		Reason:     "kind",
	}})
	require.ErrorContains(t, err, "project scope mismatch")

	allRows, err := store.ListExtractionDiagnostics(context.Background(), "", 10)
	require.NoError(t, err)
	require.Len(t, allRows, 1)
	require.True(t, strings.HasPrefix(allRows[0].ID, "diag_"))
}
