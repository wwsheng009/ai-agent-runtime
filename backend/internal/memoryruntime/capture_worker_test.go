package memoryruntime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type materializedCaptureSource struct {
	*fakeWindowSource
	receipt            chat.TurnCaptureReceipt
	validationCalls    int
	failValidationCall int
	markCalls          int
	failMarkCall       int
}

type paginatedMaterializedCaptureSource struct {
	*materializedCaptureSource
	receipts []chat.TurnCaptureReceipt
}

func (s *paginatedMaterializedCaptureSource) ListMaterializedTurnCaptureReceiptsPage(
	_ context.Context,
	after *chat.TurnCaptureReceipt,
	limit int,
) ([]chat.TurnCaptureReceipt, error) {
	return s.page(after, limit, "")
}

func (s *paginatedMaterializedCaptureSource) ListMaterializedTurnCaptureReceiptsForSessionPage(
	_ context.Context,
	sessionID string,
	after *chat.TurnCaptureReceipt,
	limit int,
) ([]chat.TurnCaptureReceipt, error) {
	return s.page(after, limit, sessionID)
}

func (s *paginatedMaterializedCaptureSource) page(
	after *chat.TurnCaptureReceipt,
	limit int,
	sessionID string,
) ([]chat.TurnCaptureReceipt, error) {
	if limit <= 0 {
		limit = 100
	}
	page := make([]chat.TurnCaptureReceipt, 0, limit)
	for _, receipt := range s.receipts {
		if receipt.State != "materialized" || (sessionID != "" && receipt.SessionID != sessionID) {
			continue
		}
		if after != nil && !captureReceiptCursorAfter(receipt, *after) {
			continue
		}
		page = append(page, receipt)
		if len(page) == limit {
			break
		}
	}
	return page, nil
}

func (s *materializedCaptureSource) ListTurnCaptureReceipts(_ context.Context, limit int) ([]chat.TurnCaptureReceipt, error) {
	if s.receipt.State == "captured" || s.receipt.State == "materializing" || s.receipt.State == "deferred" {
		return []chat.TurnCaptureReceipt{s.receipt}, nil
	}
	return nil, nil
}

func (s *materializedCaptureSource) ListTurnCaptureReceiptsForSession(_ context.Context, sessionID string, limit int) ([]chat.TurnCaptureReceipt, error) {
	if s.receipt.SessionID == sessionID &&
		(s.receipt.State == "captured" || s.receipt.State == "materializing" || s.receipt.State == "deferred") {
		return []chat.TurnCaptureReceipt{s.receipt}, nil
	}
	return nil, nil
}

func (s *materializedCaptureSource) ListMaterializedTurnCaptureReceipts(context.Context, int) ([]chat.TurnCaptureReceipt, error) {
	if s.receipt.State != "materialized" {
		return nil, nil
	}
	return []chat.TurnCaptureReceipt{s.receipt}, nil
}

func (s *materializedCaptureSource) ListMaterializedTurnCaptureReceiptsForSession(_ context.Context, sessionID string, limit int) ([]chat.TurnCaptureReceipt, error) {
	if s.receipt.State != "materialized" || s.receipt.SessionID != sessionID {
		return nil, nil
	}
	return []chat.TurnCaptureReceipt{s.receipt}, nil
}

func (s *materializedCaptureSource) ListMaterializedTurnCaptureReceiptsForSessionGenerationPage(
	_ context.Context, sessionID string, generation int64, after *chat.TurnCaptureReceipt, limit int,
) ([]chat.TurnCaptureReceipt, error) {
	receipt := s.receipt
	if receipt.State != "materialized" || receipt.SessionID != sessionID ||
		receipt.SessionGeneration != generation ||
		(after != nil && after.ID != "" && !captureReceiptCursorAfter(receipt, *after)) {
		return nil, nil
	}
	return []chat.TurnCaptureReceipt{receipt}, nil
}

func (s *materializedCaptureSource) HasTurnCaptureReceiptsForSession(_ context.Context, sessionID string) (bool, error) {
	return s.receipt.ID != "" && s.receipt.SessionID == sessionID, nil
}

func (s *materializedCaptureSource) ListTurnCaptureReceiptsForSessionGeneration(
	_ context.Context, sessionID string, generation int64, _ int,
) ([]chat.TurnCaptureReceipt, error) {
	receipt := s.receipt
	if receipt.SessionID != sessionID || receipt.SessionGeneration != generation ||
		(receipt.State != "captured" && receipt.State != "materializing" && receipt.State != "deferred") {
		return nil, nil
	}
	return []chat.TurnCaptureReceipt{receipt}, nil
}

func (s *materializedCaptureSource) HasTurnCaptureReceiptsForSessionGeneration(
	_ context.Context, sessionID string, generation int64,
) (bool, error) {
	return s.receipt.ID != "" && s.receipt.SessionID == sessionID &&
		s.receipt.SessionGeneration == generation, nil
}

func (s *materializedCaptureSource) GetTurnCaptureReceipt(context.Context, string) (chat.TurnCaptureReceipt, error) {
	return s.receipt, nil
}

func (s *materializedCaptureSource) ValidateTurnCaptureReceipt(context.Context, chat.TurnCaptureReceipt) error {
	s.validationCalls++
	if s.failValidationCall > 0 && s.validationCalls == s.failValidationCall {
		return fmt.Errorf("canonical range changed")
	}
	return nil
}

func (s *materializedCaptureSource) MarkTurnCaptureReceiptMaterialized(context.Context, string) error {
	s.markCalls++
	if s.failMarkCall > 0 && s.markCalls == s.failMarkCall {
		return fmt.Errorf("canonical acknowledgement unavailable")
	}
	s.receipt.State = "materialized"
	return nil
}

func (s *materializedCaptureSource) DeferTurnCaptureReceipt(_ context.Context, _ string, reason string, retryAt time.Time) error {
	s.receipt.State = "deferred"
	s.receipt.LastError = reason
	s.receipt.RetryAt = retryAt
	return nil
}

func newMaterializedCaptureRuntime(t *testing.T, failValidationCall int, materialize bool) (*Runtime, *memorystore.Store, *materializedCaptureSource, *fakeExtractor, memorystore.TurnCapture) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	transcript := Transcript{
		SessionID: "capture-session", Revision: 3,
		Messages: []TranscriptMessage{
			{ID: "capture-user", Seq: 1, Role: "user", Content: "remember the decision"},
			{ID: "capture-assistant", Seq: 2, Role: "assistant", Content: "use the durable capture path"},
		},
	}
	base := &fakeSource{sessions: []SessionRef{{
		ID: "capture-session", SessionGeneration: 1, WorkspacePath: "/workspace", State: "active",
		LastActivityAt: now, CanonicalCount: 2,
	}}}
	source := &materializedCaptureSource{
		fakeWindowSource:   &fakeWindowSource{fakeSource: base, window: transcript},
		failValidationCall: failValidationCall,
		receipt: chat.TurnCaptureReceipt{
			ProjectKey: "", ID: "capture-worker", SessionID: "capture-session", SessionGeneration: 1, TurnID: "turn-1",
			MessageSeqStart: 1, MessageSeqEnd: 2, CanonicalRevision: 3, RangeHash: "canonical-range",
			State: "materialized", Outcome: "success", UsageJSON: "{}", CreatedAt: now,
		},
	}
	extractor := &fakeExtractor{result: ExtractionResult{Model: "test-model", Candidates: []Candidate{{
		Kind: "decision", Text: "use the durable capture path", Confidence: 0.95,
		SourceMessageIDs: []string{"capture-assistant"},
	}}}}
	runtime, store := newRuntimeForTest(t, "shadow", base, extractor)
	runtime.Source = source
	runtime.Config.TurnDigestEnabled = true
	runtime.Config.TurnDigestRecoveryOnStartup = true
	runtime.Clock = time.Now
	capture := memorystore.TurnCapture{
		ID: source.receipt.ID, ProjectKey: store.ProjectKey(), SessionID: source.receipt.SessionID,
		SessionGeneration: source.receipt.SessionGeneration,
		TurnID:            source.receipt.TurnID, MessageSeqStart: source.receipt.MessageSeqStart,
		MessageSeqEnd: source.receipt.MessageSeqEnd, CanonicalRevision: source.receipt.CanonicalRevision,
		RangeHash: source.receipt.RangeHash, CreatedAt: now,
	}
	if materialize {
		_, err := store.MaterializeTurnCapture(ctx, capture)
		require.NoError(t, err)
	}
	return runtime, store, source, extractor, capture
}

func TestMaterializeTurnCaptureReceiptsSkipsReusedSessionGeneration(t *testing.T) {
	ctx := context.Background()
	runtime, store, source, _, _ := newMaterializedCaptureRuntime(t, 0, false)
	source.receipt.State = "captured"
	source.fakeSource.sessions[0].SessionGeneration = 2

	materialized, err := runtime.MaterializeTurnCaptureReceipts(ctx, 10)
	require.NoError(t, err)
	require.Zero(t, materialized)
	require.Equal(t, 0, source.markCalls, "a stale incarnation must not be acknowledged")
	require.Equal(t, "captured", source.receipt.State)

	status, err := store.Status(ctx)
	require.NoError(t, err)
	require.Empty(t, status.Jobs)
	require.Empty(t, status.TurnDigests)
}

func TestRunPendingTurnCaptureReceiptsCommitsOneTurnAtomically(t *testing.T) {
	ctx := context.Background()
	runtime, store, source, extractor, capture := newMaterializedCaptureRuntime(t, 0, true)
	result, err := runtime.RunPendingTurnCaptureReceipts(ctx, TriggerStartup)
	require.NoError(t, err)
	require.Equal(t, 1, result.Processed,
		"startup may recover an already materialized, exact turn range from an active session")
	require.Equal(t, 1, extractor.calls)

	result, err = runtime.RunPendingTurnCaptureReceipts(ctx, TriggerSessionEnd)
	require.NoError(t, err)
	require.Zero(t, result.Processed, "session-end must not replay a receipt completed during startup")
	require.Zero(t, result.Candidates)
	require.Equal(t, 1, extractor.calls)

	digest, err := store.GetTurnDigestByTurn(ctx, capture.SessionID, capture.TurnID)
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestDone, digest.Status)
	status, err := store.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, status.ByStatus[string(memorystore.StatusCandidate)])
	require.Equal(t, 1, status.Jobs[string(memorystore.JobStatusSucceeded)])
	require.Zero(t, status.TurnDigests[string(memorystore.TurnDigestPending)])

	result, err = runtime.RunPendingTurnCaptureReceipts(ctx, TriggerSessionEnd)
	require.NoError(t, err)
	require.Zero(t, result.Processed, "replay should skip the completed per-turn receipt")
	require.Equal(t, 1, extractor.calls, "replay must not invoke the extractor again")

	_, err = runtime.RunSessionEnd(ctx, capture.SessionID)
	require.NoError(t, err)
	require.Zero(t, source.readCalls, "session-end must not fall back to full-session extraction after canonical capture")
	require.Equal(t, 1, extractor.calls, "session-end must not re-extract the canonical turn")

	runtime.Config.TurnDigestEnabled = false
	_, err = runtime.RunSessionEnd(ctx, capture.SessionID)
	require.NoError(t, err)
	require.Zero(t, source.readCalls, "disabling the turn digest feature must not make captured canonical turns eligible for fallback")

	require.Equal(t, 1, status.Jobs[string(memorystore.JobStatusSucceeded)])
}

func TestRunPendingTurnCaptureReceiptsPaginatesPastCompletedReceipts(t *testing.T) {
	ctx := context.Background()
	runtime, store, source, extractor, firstCapture := newMaterializedCaptureRuntime(t, 0, true)
	runtime.Config.TurnDigestMaxDigestsPerRun = 2
	extractor.result.Candidates = nil

	receipts := []chat.TurnCaptureReceipt{source.receipt}
	captures := []memorystore.TurnCapture{firstCapture}
	for index := 2; index <= 3; index++ {
		receipt := source.receipt
		receipt.ID = fmt.Sprintf("capture-worker-%d", index)
		receipt.TurnID = fmt.Sprintf("turn-%d", index)
		receipt.RangeHash = fmt.Sprintf("canonical-range-%d", index)
		receipt.CreatedAt = receipt.CreatedAt.Add(time.Duration(index) * time.Second)
		receipts = append(receipts, receipt)
		captures = append(captures, memorystore.TurnCapture{
			ID: receipt.ID, ProjectKey: store.ProjectKey(), SessionID: receipt.SessionID,
			SessionGeneration: receipt.SessionGeneration,
			TurnID:            receipt.TurnID, MessageSeqStart: receipt.MessageSeqStart,
			MessageSeqEnd: receipt.MessageSeqEnd, CanonicalRevision: receipt.CanonicalRevision,
			RangeHash: receipt.RangeHash, CreatedAt: receipt.CreatedAt,
		})
	}
	for _, capture := range captures[1:] {
		_, err := store.MaterializeTurnCapture(ctx, capture)
		require.NoError(t, err)
	}
	runtime.Source = &paginatedMaterializedCaptureSource{
		materializedCaptureSource: source,
		receipts:                  receipts,
	}

	firstRun, err := runtime.RunPendingTurnCaptureReceipts(ctx, TriggerStartup)
	require.NoError(t, err)
	require.Equal(t, 2, firstRun.Processed, "first recovery run: %+v", firstRun)
	require.Equal(t, 2, extractor.calls)

	secondRun, err := runtime.RunPendingTurnCaptureReceipts(ctx, TriggerIdle)
	require.NoError(t, err)
	require.Equal(t, 1, secondRun.Processed,
		"recovery must continue beyond the first page of already-completed receipts")
	require.Equal(t, 3, extractor.calls)
	for _, capture := range captures {
		digest, err := store.GetTurnDigestByTurn(ctx, capture.SessionID, capture.TurnID)
		require.NoError(t, err)
		require.Equal(t, memorystore.TurnDigestDone, digest.Status)
	}
}

func TestRunPendingTurnCaptureReceiptsRevalidatesCanonicalRange(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failCall      int
		extractorRuns int
	}{
		{name: "after_range_read", failCall: 2, extractorRuns: 0},
		{name: "before_commit", failCall: 3, extractorRuns: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime, store, _, extractor, capture := newMaterializedCaptureRuntime(t, tc.failCall, true)
			result, err := runtime.RunPendingTurnCaptureReceipts(context.Background(), TriggerSessionEnd)
			require.NoError(t, err)
			require.Equal(t, 1, result.Failed)
			require.Zero(t, result.Processed)
			require.Equal(t, tc.extractorRuns, extractor.calls)

			digest, err := store.GetTurnDigestByTurn(context.Background(), capture.SessionID, capture.TurnID)
			require.NoError(t, err)
			require.Equal(t, memorystore.TurnDigestPending, digest.Status)
			status, err := store.Status(context.Background())
			require.NoError(t, err)
			require.Zero(t, status.ByStatus[string(memorystore.StatusCandidate)],
				"changed canonical source must not commit extracted candidates")
			require.EqualValues(t, 1, status.Jobs[string(memorystore.JobStatusPending)])
		})
	}
}

func TestMaterializeTurnCaptureReceiptRetriesCanonicalAcknowledgement(t *testing.T) {
	runtime, store, source, _, capture := newMaterializedCaptureRuntime(t, 0, true)
	source.receipt.State = "captured"
	source.failMarkCall = 1

	err := runtime.MaterializeTurnCaptureReceipt(context.Background(), capture.ID)
	require.Error(t, err)
	require.Equal(t, 1, source.markCalls)
	require.Equal(t, "captured", source.receipt.State,
		"failed acknowledgement must leave the canonical receipt retryable")

	source.failMarkCall = 0
	err = runtime.MaterializeTurnCaptureReceipt(context.Background(), capture.ID)
	require.NoError(t, err)
	require.Equal(t, 2, source.markCalls)
	require.Equal(t, "materialized", source.receipt.State)

	status, err := store.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Jobs[string(memorystore.JobStatusPending)],
		"ack retry must reuse the existing idempotent job")
	require.Zero(t, status.Jobs[string(memorystore.JobStatusSucceeded)])
}

func TestMaterializeTurnCaptureReceiptDefersCanonicalValidationFailure(t *testing.T) {
	runtime, store, source, _, capture := newMaterializedCaptureRuntime(t, 0, false)
	source.receipt.State = "captured"
	source.failValidationCall = 1

	err := runtime.MaterializeTurnCaptureReceipt(context.Background(), capture.ID)
	require.NoError(t, err)
	require.Equal(t, "deferred", source.receipt.State)
	require.NotEmpty(t, source.receipt.LastError)
	require.False(t, source.receipt.RetryAt.IsZero())

	status, err := store.Status(context.Background())
	require.NoError(t, err)
	require.Zero(t, status.Jobs[string(memorystore.JobStatusPending)])
	require.Zero(t, status.Jobs[string(memorystore.JobStatusSucceeded)])
}

func TestRunSessionEndMaterializesCaptureWhenGenerationDisabled(t *testing.T) {
	runtime, store, source, extractor, _ := newMaterializedCaptureRuntime(t, 0, false)
	source.receipt.State = "captured"
	runtime.Config.GenerationDisabled = true

	_, err := runtime.RunSessionEnd(context.Background(), source.receipt.SessionID)
	require.NoError(t, err)
	require.Equal(t, "materialized", source.receipt.State,
		"capture handoff must progress independently of the LLM generation gate")
	require.Zero(t, extractor.calls)

	status, err := store.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Jobs[string(memorystore.JobStatusPending)])
	require.Zero(t, status.Jobs[string(memorystore.JobStatusSucceeded)])
	require.Zero(t, status.ByStatus[string(memorystore.StatusCandidate)])
}
