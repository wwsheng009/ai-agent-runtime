package memoryruntime

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestTurnDigestAcknowledgementDoesNotExceedCanonicalReceipt(t *testing.T) {
	for _, entrypoint := range []string{"session_end", "recovery", "mark_pending"} {
		t.Run(entrypoint, func(t *testing.T) {
			ctx := context.Background()
			rt, source, store := newRecoveryRuntime(t, 4, 4)
			// Metadata and the readable canonical page can lag a captured
			// digest. Successful extraction of 1-4 cannot acknowledge 5-6.
			for _, end := range []int64{2, 4, 6} {
				_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
					SessionID: source.session.ID, TurnID: fmt.Sprintf("turn-%d", end),
					MessageSeqEnd: end, Outcome: "success", AssistantText: "answer",
				})
				require.NoError(t, err)
			}
			switch entrypoint {
			case "session_end":
				result, err := rt.RunSessionEnd(ctx, source.session.ID)
				require.NoError(t, err)
				require.Empty(t, result.Errors)
			case "recovery":
				result, err := rt.RunPendingTurnDigestsForSession(ctx, TriggerStartup, source.session.ID)
				require.NoError(t, err)
				require.Empty(t, result.Errors)
			case "mark_pending":
				_, err := rt.MarkPendingTurnDigestsProcessed(ctx, source.session.ID)
				require.NoError(t, err)
			}
			for _, end := range []int64{2, 4, 6} {
				digest, err := store.GetTurnDigestByTurn(ctx, source.session.ID, fmt.Sprintf("turn-%d", end))
				require.NoError(t, err)
				expected := memorystore.TurnDigestDone
				if end == 6 {
					expected = memorystore.TurnDigestPending
				}
				require.Equal(t, expected, digest.Status, "canonical endpoint %d", end)
			}

			// Once the remaining evidence is visible, recovery consumes just
			// that suffix rather than repeating already-persisted extraction.
			for seq := 5; seq <= 6; seq++ {
				source.messages = append(source.messages, TranscriptMessage{
					ID: fmt.Sprintf("message-%d", seq), Seq: seq, Role: "assistant", Content: "remaining evidence",
				})
			}
			source.session.CanonicalCount = 6
			result, err := rt.RunPendingTurnDigestsForSession(ctx, TriggerStartup, source.session.ID)
			require.NoError(t, err)
			require.Empty(t, result.Errors)
			require.Equal(t, 1, source.readCalls)
			require.Equal(t, 1, source.windowCalls)
			pending, err := store.ListPendingTurnDigests(ctx, source.session.ID, 10)
			require.NoError(t, err)
			require.Empty(t, pending)
		})
	}
}

func TestCoveredTurnDigestSeqRequiresCoveredPrefix(t *testing.T) {
	session := SessionRef{CanonicalCount: 4}
	digests := []memorystore.TurnDigest{
		{DigestSeq: 10, MessageSeqEnd: 2},
		{DigestSeq: 20, MessageSeqEnd: 6},
		{DigestSeq: 30, MessageSeqEnd: 4},
		{DigestSeq: 40}, // A legacy row cannot authorize skipping the gap.
	}
	for _, tt := range []struct {
		name    string
		receipt RunResult
		wantSeq int64
	}{
		{"covered prefix", RunResult{Completed: true, SourceStartSeq: 1, SourceEndSeq: 4}, 10},
		{"no persisted completion", RunResult{SourceStartSeq: 1, SourceEndSeq: 4}, 0},
		{"tail only", RunResult{Completed: true, SourceStartSeq: 3, SourceEndSeq: 6}, 0},
		{"incomplete session", RunResult{Completed: true, SourceStartSeq: 1, SourceEndSeq: 2}, 0},
		{"complete range", RunResult{Completed: true, SourceStartSeq: 1, SourceEndSeq: 6}, 40},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantSeq, coveredTurnDigestSeq(tt.receipt, session, digests))
		})
	}
}
