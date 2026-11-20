package memoryruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

var errCaptureReceiptGenerationMismatch = errors.New("memoryruntime: capture receipt belongs to a different session generation")

// MaterializeTurnCaptureReceipts drains the canonical capture outbox without
// invoking the extractor. Each receipt is acknowledged only after the memory
// transaction containing both digest and phase-1 job commits.
func (r *Runtime) MaterializeTurnCaptureReceipts(ctx context.Context, limit int) (int, error) {
	if r == nil || r.Store == nil || !r.enabled() {
		return 0, nil
	}
	source, ok := r.Source.(TurnCaptureReceiptSource)
	if !ok {
		return 0, nil
	}
	if limit <= 0 {
		limit = r.Config.TurnDigestMaxDigestsPerRun
	}
	if limit <= 0 {
		limit = 100
	}
	receipts, err := source.ListTurnCaptureReceipts(ctx, limit)
	if err != nil {
		return 0, err
	}
	materialized := 0
	for _, receipt := range receipts {
		if err := ctx.Err(); err != nil {
			return materialized, err
		}
		if receipt.SessionGeneration <= 0 {
			continue
		}
		ok, err := r.materializeTurnCaptureReceipt(ctx, source, receipt)
		if errors.Is(err, errCaptureReceiptGenerationMismatch) {
			continue
		}
		if err != nil {
			return materialized, err
		}
		if ok {
			materialized++
		}
	}
	return materialized, nil
}

// MaterializeTurnCaptureReceiptsForSession is the bounded session-end flush.
// Canonical stores with scoped listing support avoid starving a session behind
// unrelated receipts in the global page.
func (r *Runtime) MaterializeTurnCaptureReceiptsForSession(ctx context.Context, sessionID string, limit int) (int, error) {
	return r.materializeTurnCaptureReceiptsForSession(ctx, sessionID, nil, limit)
}

// MaterializeTurnCaptureReceiptsForSessionGeneration avoids allowing an old
// incarnation's deferred receipts to occupy the current session-end flush.
func (r *Runtime) MaterializeTurnCaptureReceiptsForSessionGeneration(
	ctx context.Context, sessionID string, generation int64, limit int,
) (int, error) {
	if generation <= 0 {
		// Generation zero is legacy data with no trustworthy incarnation.
		return 0, nil
	}
	return r.materializeTurnCaptureReceiptsForSession(ctx, sessionID, &generation, limit)
}

func (r *Runtime) materializeTurnCaptureReceiptsForSession(
	ctx context.Context, sessionID string, generation *int64, limit int,
) (int, error) {
	if r == nil || r.Store == nil || !r.enabled() {
		return 0, nil
	}
	source, ok := r.Source.(TurnCaptureReceiptSource)
	if !ok {
		return 0, nil
	}
	if limit <= 0 {
		limit = r.Config.TurnDigestMaxDigestsPerRun
	}
	if limit <= 0 {
		limit = 100
	}
	var receipts []chat.TurnCaptureReceipt
	if generation != nil {
		if scoped, ok := r.Source.(SessionTurnCaptureReceiptGenerationSource); ok {
			var err error
			receipts, err = scoped.ListTurnCaptureReceiptsForSessionGeneration(
				ctx, strings.TrimSpace(sessionID), *generation, limit,
			)
			if err != nil {
				return 0, err
			}
		} else {
			return 0, fmt.Errorf("memoryruntime: generation-scoped capture receipt listing unavailable")
		}
	} else if scoped, ok := r.Source.(SessionTurnCaptureReceiptSource); ok {
		var err error
		receipts, err = scoped.ListTurnCaptureReceiptsForSession(ctx, strings.TrimSpace(sessionID), limit)
		if err != nil {
			return 0, err
		}
	} else {
		var err error
		receipts, err = source.ListTurnCaptureReceipts(ctx, limit)
		if err != nil {
			return 0, err
		}
	}
	materialized := 0
	for _, receipt := range receipts {
		if strings.TrimSpace(receipt.SessionID) != strings.TrimSpace(sessionID) {
			continue
		}
		if receipt.SessionGeneration <= 0 {
			continue
		}
		if generation != nil && receipt.SessionGeneration != *generation {
			continue
		}
		if err := ctx.Err(); err != nil {
			return materialized, err
		}
		ok, err := r.materializeTurnCaptureReceipt(ctx, source, receipt)
		if errors.Is(err, errCaptureReceiptGenerationMismatch) {
			continue
		}
		if err != nil {
			return materialized, err
		}
		if ok {
			materialized++
		}
	}
	return materialized, nil
}

// MaterializeTurnCaptureReceipt is the bounded post-commit fast path used by
// the turn-end sink. Failures leave the canonical receipt retryable; maintenance
// repeats the same idempotent cross-database handoff.
func (r *Runtime) MaterializeTurnCaptureReceipt(ctx context.Context, receiptID string) error {
	if r == nil || r.Store == nil || !r.enabled() {
		return nil
	}
	source, ok := r.Source.(TurnCaptureReceiptSource)
	if !ok {
		return fmt.Errorf("memoryruntime: canonical capture receipt source unavailable")
	}
	receipt, err := source.GetTurnCaptureReceipt(ctx, strings.TrimSpace(receiptID))
	if err != nil {
		return fmt.Errorf("memoryruntime: read capture receipt %s: %w", receiptID, err)
	}
	if receipt.SessionGeneration <= 0 {
		return fmt.Errorf("memoryruntime: capture receipt %s has no positive session generation", receiptID)
	}
	_, err = r.materializeTurnCaptureReceipt(ctx, source, receipt)
	return err
}

func (r *Runtime) materializeTurnCaptureReceipt(
	ctx context.Context,
	source TurnCaptureReceiptSource,
	receipt chat.TurnCaptureReceipt,
) (bool, error) {
	if receipt.State == "materialized" {
		return false, nil
	}
	if receipt.SessionGeneration <= 0 {
		return false, fmt.Errorf("memoryruntime: capture receipt %s has no positive session generation", receipt.ID)
	}
	session, err := r.findSession(ctx, receipt.SessionID)
	if err != nil {
		return false, fmt.Errorf("memoryruntime: read current session generation for capture receipt %s: %w", receipt.ID, err)
	}
	if session.SessionGeneration <= 0 || session.SessionGeneration != receipt.SessionGeneration {
		return false, errCaptureReceiptGenerationMismatch
	}
	if err := source.ValidateTurnCaptureReceipt(ctx, receipt); err != nil {
		retryAt := r.now().Add(captureRetryDelay(receipt.AttemptCount))
		if deferErr := source.DeferTurnCaptureReceipt(ctx, receipt.ID, "canonical validation: "+err.Error(), retryAt); deferErr != nil {
			return false, fmt.Errorf("memoryruntime: defer invalid capture receipt %s: %w", receipt.ID, deferErr)
		}
		return false, nil
	}
	projectKey := strings.TrimSpace(receipt.ProjectKey)
	if projectKey == "" {
		projectKey = strings.TrimSpace(r.Config.ProjectKey)
	}
	_, err = r.Store.MaterializeTurnCapture(ctx, memorystore.TurnCapture{
		ID:                 receipt.ID,
		ProjectKey:         projectKey,
		SessionID:          receipt.SessionID,
		SessionGeneration:  receipt.SessionGeneration,
		TurnID:             receipt.TurnID,
		MessageSeqStart:    receipt.MessageSeqStart,
		MessageSeqEnd:      receipt.MessageSeqEnd,
		CanonicalRevision:  receipt.CanonicalRevision,
		RangeHash:          receipt.RangeHash,
		TraceID:            receipt.TraceID,
		UserMessageID:      receipt.UserMessageID,
		AssistantMessageID: receipt.AssistantMessageID,
		Outcome:            receipt.Outcome,
		Steps:              receipt.Steps,
		UsageJSON:          receipt.UsageJSON,
		CreatedAt:          receipt.CreatedAt,
	})
	if err != nil {
		retryAt := r.now().Add(captureRetryDelay(receipt.AttemptCount))
		if deferErr := source.DeferTurnCaptureReceipt(ctx, receipt.ID, err.Error(), retryAt); deferErr != nil {
			return false, fmt.Errorf("memoryruntime: defer capture receipt %s: %w (materialize: %v)", receipt.ID, deferErr, err)
		}
		return false, nil
	}
	if err := source.MarkTurnCaptureReceiptMaterialized(ctx, receipt.ID); err != nil {
		// The memory transaction already committed. Leave the canonical row
		// retryable; the next pass finds the same digest/job identity.
		return false, fmt.Errorf("memoryruntime: acknowledge capture receipt %s: %w", receipt.ID, err)
	}
	return true, nil
}

func captureRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Second
	for i := 0; i < attempt && delay < time.Minute; i++ {
		delay *= 2
	}
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}
