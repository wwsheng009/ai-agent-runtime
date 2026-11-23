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

type thresholdCaptureSource struct {
	*fakeSource
	receipts    []chat.TurnCaptureReceipt
	windows     map[string]Transcript
	windowCalls int
}

func (s *thresholdCaptureSource) ListTurnCaptureReceipts(_ context.Context, limit int) ([]chat.TurnCaptureReceipt, error) {
	return s.list(false, "", 0, limit), nil
}

func (s *thresholdCaptureSource) ListMaterializedTurnCaptureReceipts(_ context.Context, limit int) ([]chat.TurnCaptureReceipt, error) {
	return s.list(true, "", 0, limit), nil
}

func (s *thresholdCaptureSource) ListMaterializedTurnCaptureReceiptsForSessionGenerationPage(
	_ context.Context, sessionID string, generation int64, after *chat.TurnCaptureReceipt, limit int,
) ([]chat.TurnCaptureReceipt, error) {
	out := make([]chat.TurnCaptureReceipt, 0, limit)
	for _, receipt := range s.receipts {
		if receipt.State != "materialized" || receipt.SessionID != sessionID ||
			receipt.SessionGeneration != generation ||
			(after != nil && !captureReceiptCursorAfter(receipt, *after)) {
			continue
		}
		out = append(out, receipt)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *thresholdCaptureSource) GetTurnCaptureReceipt(_ context.Context, id string) (chat.TurnCaptureReceipt, error) {
	for _, receipt := range s.receipts {
		if receipt.ID == id {
			return receipt, nil
		}
	}
	return chat.TurnCaptureReceipt{}, fmt.Errorf("receipt %s not found", id)
}

func (s *thresholdCaptureSource) ValidateTurnCaptureReceipt(_ context.Context, receipt chat.TurnCaptureReceipt) error {
	if receipt.MessageSeqStart <= 0 || receipt.MessageSeqEnd < receipt.MessageSeqStart {
		return fmt.Errorf("invalid frozen range")
	}
	return nil
}

func (s *thresholdCaptureSource) MarkTurnCaptureReceiptMaterialized(_ context.Context, id string) error {
	for index := range s.receipts {
		if s.receipts[index].ID == id {
			s.receipts[index].State = "materialized"
			return nil
		}
	}
	return fmt.Errorf("receipt %s not found", id)
}

func (s *thresholdCaptureSource) DeferTurnCaptureReceipt(_ context.Context, id, reason string, retryAt time.Time) error {
	for index := range s.receipts {
		if s.receipts[index].ID == id {
			s.receipts[index].State = "deferred"
			s.receipts[index].LastError = reason
			s.receipts[index].RetryAt = retryAt
			return nil
		}
	}
	return fmt.Errorf("receipt %s not found", id)
}

func (s *thresholdCaptureSource) ReadTranscriptWindow(
	_ context.Context, session SessionRef, startSeq, endSeq, limit int,
) (Transcript, error) {
	s.windowCalls++
	source := s.windows[session.ID]
	out := source
	out.Messages = nil
	for _, message := range source.Messages {
		if message.Seq >= startSeq && message.Seq <= endSeq &&
			(limit <= 0 || len(out.Messages) < limit) {
			out.Messages = append(out.Messages, message)
		}
	}
	out.Hash = TranscriptHash(out.Messages)
	return out, nil
}

func (s *thresholdCaptureSource) list(materialized bool, sessionID string, generation int64, limit int) []chat.TurnCaptureReceipt {
	out := make([]chat.TurnCaptureReceipt, 0, limit)
	for _, receipt := range s.receipts {
		if materialized && receipt.State != "materialized" {
			continue
		}
		if !materialized && receipt.State == "materialized" {
			continue
		}
		if sessionID != "" && receipt.SessionID != sessionID {
			continue
		}
		if generation > 0 && receipt.SessionGeneration != generation {
			continue
		}
		out = append(out, receipt)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func newThresholdCaptureRuntime(t *testing.T, pending int) (*Runtime, *thresholdCaptureSource, *fakeExtractor) {
	t.Helper()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	base := &fakeSource{sessions: []SessionRef{{
		ID: "threshold-session", SessionGeneration: 1, WorkspacePath: "/workspace",
		State: "active", LastActivityAt: now, CanonicalCount: pending * 2,
	}}}
	source := &thresholdCaptureSource{
		fakeSource: base,
		windows:    make(map[string]Transcript),
	}
	source.windows[base.sessions[0].ID] = Transcript{
		SessionID: base.sessions[0].ID,
		Revision:  int64(pending * 2),
	}
	for index := 0; index < pending; index++ {
		turnID := fmt.Sprintf("threshold-turn-%d", index+1)
		receipt := chat.TurnCaptureReceipt{
			ProjectKey: "", ID: "threshold-receipt-" + fmt.Sprint(index+1),
			SessionID: base.sessions[0].ID, SessionGeneration: 1, TurnID: turnID,
			MessageSeqStart: int64(index*2 + 1), MessageSeqEnd: int64(index*2 + 2),
			CanonicalRevision: int64((index + 1) * 2), RangeHash: "range-" + fmt.Sprint(index+1),
			State: "materialized", Outcome: "success", UsageJSON: "{}",
			CreatedAt:      now.Add(time.Duration(index) * time.Second),
			MaterializedAt: now.Add(time.Duration(index) * time.Second),
		}
		source.receipts = append(source.receipts, receipt)
		window := source.windows[base.sessions[0].ID]
		window.Messages = append(window.Messages,
			TranscriptMessage{ID: fmt.Sprintf("threshold-user-%d", index+1), Seq: index*2 + 1, Role: "user", Content: "frozen user"},
			TranscriptMessage{ID: fmt.Sprintf("threshold-assistant-%d", index+1), Seq: index*2 + 2, Role: "assistant", Content: "frozen assistant"},
		)
		source.windows[base.sessions[0].ID] = window
	}
	extractor := &fakeExtractor{}
	runtime, _ := newRuntimeForTest(t, "shadow", base, extractor)
	runtime.Source = source
	runtime.Clock = func() time.Time { return time.Now().UTC().Add(time.Minute) }
	runtime.Config.GenerationTrigger = string(TriggerTurnThreshold)
	runtime.Config.TurnDigestEnabled = true
	runtime.Config.TurnDigestExtractEveryNTurns = 2
	runtime.Config.TurnDigestMaxDigestsPerRun = 20
	return runtime, source, extractor
}

func materializeThresholdReceipts(t *testing.T, runtime *Runtime, source *thresholdCaptureSource) {
	t.Helper()
	for _, receipt := range source.receipts {
		_, err := runtime.Store.MaterializeTurnCapture(context.Background(), memorystore.TurnCapture{
			ID: receipt.ID, ProjectKey: runtime.Store.ProjectKey(), SessionID: receipt.SessionID,
			SessionGeneration: receipt.SessionGeneration, TurnID: receipt.TurnID,
			MessageSeqStart: receipt.MessageSeqStart, MessageSeqEnd: receipt.MessageSeqEnd,
			CanonicalRevision: receipt.CanonicalRevision, RangeHash: receipt.RangeHash,
			Outcome: receipt.Outcome, UsageJSON: receipt.UsageJSON, CreatedAt: receipt.CreatedAt,
		})
		require.NoError(t, err)
	}
}

func TestRunTurnThresholdDoesNotExtractBeforeThreshold(t *testing.T) {
	runtime, source, extractor := newThresholdCaptureRuntime(t, 1)
	materializeThresholdReceipts(t, runtime, source)

	result, err := runtime.RunTurnThreshold(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, TriggerTurnThreshold, result.Trigger)
	require.Zero(t, result.Processed)
	require.Zero(t, extractor.calls)
	require.Zero(t, source.windowCalls)
}

func TestRunTurnThresholdExtractsFrozenRangesAndIsIdempotent(t *testing.T) {
	runtime, source, extractor := newThresholdCaptureRuntime(t, 2)
	materializeThresholdReceipts(t, runtime, source)

	result, err := runtime.RunTurnThreshold(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, 2, result.Processed, "threshold result=%+v", result)
	require.Equal(t, 2, extractor.calls, "threshold result=%+v", result)
	require.Equal(t, 2, source.windowCalls)
	require.Zero(t, source.readCalls, "threshold extraction must not read an unfrozen full transcript")

	status, err := runtime.Store.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, status.Jobs[string(memorystore.JobStatusSucceeded)])

	result, err = runtime.RunTurnThreshold(context.Background(), 5)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Equal(t, 2, extractor.calls, "repeating the threshold hook must not re-extract completed receipts")
}

func TestRunTurnThresholdRejectsGenerationMismatch(t *testing.T) {
	runtime, source, extractor := newThresholdCaptureRuntime(t, 2)
	materializeThresholdReceipts(t, runtime, source)
	source.sessions[0].SessionGeneration = 2

	result, err := runtime.RunTurnThreshold(context.Background(), 5)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Zero(t, extractor.calls)
	require.Zero(t, source.windowCalls)
}
