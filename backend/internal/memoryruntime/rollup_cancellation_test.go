package memoryruntime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type cancelledWindowSummarizer struct{ cancel context.CancelFunc }

func (s cancelledWindowSummarizer) SummarizeWindow(ctx context.Context, _ WindowSummaryInput) (WindowSummary, error) {
	s.cancel()
	return WindowSummary{}, ctx.Err()
}

func TestRollupCancellationPersistsFailureAndRecoversWithoutExtraction(t *testing.T) {
	rt, source, store := newRecoveryRuntime(t, 2, 20)
	rt.Config.TurnDigestWindowSummaryEnabled = true
	rt.Config.TurnDigestWindowTurns = 1
	extractions := 0
	rt.Extractor = recoveryExtractorFunc(func(context.Context, SessionRef, Transcript) (ExtractionResult, error) {
		extractions++
		return ExtractionResult{}, nil
	})
	_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: source.session.ID, TurnID: "rollup-cancel", Outcome: "success", MessageSeqEnd: 2,
		UserText: "preserve evidence", AssistantText: "canonical evidence preserved",
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.WindowSummarizer = cancelledWindowSummarizer{cancel: cancel}
	result, _ := rt.RunSessionEnd(ctx, source.session.ID)
	require.Equal(t, 1, result.RollupFailed)
	require.NotEmpty(t, result.Errors)
	db := recoverySQL(t, store)
	var state string
	require.NoError(t, db.QueryRow(`SELECT status FROM memory_jobs WHERE kind='phase1_rollup'`).Scan(&state))
	require.Equal(t, "pending", state, "a canceled context must still release the rollup lease for retry")
	pending, err := store.ListTurnDigestsForRollup(context.Background(), source.session.ID, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	now := rt.now().Add(time.Minute)
	rt.Clock = func() time.Time { return now }
	rt.WindowSummarizer = &fakeWindowSummarizer{result: WindowSummary{Text: "Preserve canonical evidence.", Model: "offline"}}
	recovered, err := rt.RunPendingTurnDigests(context.Background(), TriggerStartup)
	require.NoError(t, err)
	require.Empty(t, recovered.Errors)
	require.Equal(t, 1, recovered.RolledUp)
	require.Equal(t, 1, extractions)
	pending, err = store.ListTurnDigestsForRollup(context.Background(), source.session.ID, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}
