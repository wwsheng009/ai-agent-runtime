package memoryruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// DigestReplayReport is a bounded operator-facing dry-run summary. It contains
// stable identities and safe classifications only; raw runtime/provider errors
// and transcript content are never included.
type DigestReplayReport struct {
	GeneratedAt time.Time                                      `json:"generated_at"`
	Limit       int                                            `json:"limit"`
	Count       int                                            `json:"count"`
	ByClass     map[memorystore.DigestReplayClassification]int `json:"by_class"`
	Items       []memorystore.DigestReplayResult               `json:"items"`
}

// PreviewDigestReplay verifies the durable canonical receipt and current
// session incarnation, then returns the memory-store replay classification
// without writing jobs, digests, or audit rows.
func (r *Runtime) PreviewDigestReplay(
	ctx context.Context,
	digestID string,
) (memorystore.DigestReplayResult, error) {
	digest, capture, result, proceed, err := r.validateDigestReplaySource(ctx, digestID, true)
	if err != nil || !proceed {
		return result, err
	}
	_ = digest
	return r.Store.ReplayDigestDryRun(ctx, digestID, capture)
}

// PreviewDigestReplayRecovery classifies a bounded oldest-first digest backlog
// without modifying the memory store or acknowledging canonical receipts.
func (r *Runtime) PreviewDigestReplayRecovery(
	ctx context.Context,
	limit int,
) (DigestReplayReport, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	report := DigestReplayReport{
		GeneratedAt: time.Now().UTC(),
		Limit:       limit,
		ByClass:     make(map[memorystore.DigestReplayClassification]int),
		Items:       make([]memorystore.DigestReplayResult, 0),
	}
	if r != nil {
		report.GeneratedAt = r.now()
	}
	if r == nil || r.Store == nil || !r.enabled() {
		return report, nil
	}
	ids, err := r.Store.ListDigestReplayCandidateIDs(ctx, limit)
	if err != nil {
		return report, err
	}
	for _, digestID := range ids {
		item, err := r.PreviewDigestReplay(ctx, digestID)
		if err != nil {
			return report, fmt.Errorf("memoryruntime: classify digest recovery backlog: %w", err)
		}
		report.Items = append(report.Items, item)
		report.ByClass[item.Classification]++
	}
	report.Count = len(report.Items)
	return report, nil
}

// ReplayDigest requeues only a receipt-backed orphan after checking that the
// canonical closed range still exists on the same session generation.
func (r *Runtime) ReplayDigest(
	ctx context.Context,
	digestID, reason string,
) (memorystore.DigestReplayResult, error) {
	return r.ReplayDigestWithLease(ctx, digestID, reason)
}

func (r *Runtime) replayDigestWithContext(
	ctx context.Context,
	digestID, reason string,
) (memorystore.DigestReplayResult, error) {
	digest, capture, result, proceed, err := r.validateDigestReplaySource(ctx, digestID, false)
	if err != nil || !proceed {
		return result, err
	}
	_ = digest
	return r.Store.ReplayDigestWithResult(ctx, digestID, capture, reason)
}

// ReplayDigestWithLease takes a short-lived maintenance lease before validating
// or linking any recovery job, so an HTTP/CLI replay cannot race the periodic
// maintenance owner. ReplayDigest delegates here; the internal helper performs
// the fenced operation after the lease has been attached to the context.
func (r *Runtime) ReplayDigestWithLease(
	ctx context.Context,
	digestID, reason string,
) (memorystore.DigestReplayResult, error) {
	result := memorystore.DigestReplayResult{
		DigestID: strings.TrimSpace(digestID),
	}
	if r == nil || r.Store == nil || !r.enabled() {
		result.Classification = memorystore.DigestReplayNotOrphaned
		result.Reason = "runtime_disabled"
		return result, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerID := "memory-replay-" + uuid.NewString()
	lease, acquired, err := r.Store.AcquireMaintenanceLease(
		ctx, ownerID, "operator", 2*time.Minute,
	)
	if err != nil {
		return result, err
	}
	if !acquired {
		result.Classification = memorystore.DigestReplayBlockedMaintenance
		result.Reason = "maintenance_lease"
		return result, memorystore.ErrMaintenanceLeaseBusy
	}
	ownedCtx := memorystore.WithMaintenanceLease(ctx, lease)
	result, err = r.replayDigestWithContext(ownedCtx, digestID, reason)
	releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	releaseErr := r.Store.ReleaseMaintenanceLease(releaseCtx, lease)
	cancel()
	if err == nil && releaseErr != nil {
		err = releaseErr
	}
	return result, err
}

func (r *Runtime) validateDigestReplaySource(
	ctx context.Context,
	digestID string,
	dryRun bool,
) (memorystore.TurnDigest, memorystore.TurnCapture, memorystore.DigestReplayResult, bool, error) {
	digestID = strings.TrimSpace(digestID)
	result := memorystore.DigestReplayResult{DigestID: digestID, DryRun: dryRun}
	if r == nil || r.Store == nil || !r.enabled() {
		result.Classification = memorystore.DigestReplayNotOrphaned
		result.Reason = "runtime_disabled"
		return memorystore.TurnDigest{}, memorystore.TurnCapture{}, result, false, nil
	}
	digest, err := r.Store.GetTurnDigestByID(ctx, digestID)
	if err != nil {
		return digest, memorystore.TurnCapture{}, result, false, err
	}
	result.SessionID = digest.SessionID
	result.SessionGeneration = digest.SessionGeneration
	result.CaptureReceiptID = digest.CaptureReceiptID
	if digest.Status == memorystore.TurnDigestDone ||
		digest.Status == memorystore.TurnDigestSkipped ||
		digest.Status == memorystore.TurnDigestInvalidated {
		result.Classification = memorystore.DigestReplayAlreadyProcessed
		result.Reason = "digest_terminal"
		return digest, memorystore.TurnCapture{}, result, false, nil
	}
	if strings.TrimSpace(digest.CaptureReceiptID) == "" {
		result.Classification = memorystore.DigestReplayMissingReceipt
		result.Reason = "capture_receipt"
		return digest, memorystore.TurnCapture{}, result, false, nil
	}
	source, ok := r.Source.(TurnCaptureReceiptSource)
	if !ok {
		result.Classification = memorystore.DigestReplayMissingReceipt
		result.Reason = "canonical_receipt_source"
		return digest, memorystore.TurnCapture{}, result, false, nil
	}
	receipt, err := source.GetTurnCaptureReceipt(ctx, digest.CaptureReceiptID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			result.Classification = memorystore.DigestReplayMissingReceipt
			result.Reason = "canonical_receipt"
			return digest, memorystore.TurnCapture{}, result, false, nil
		}
		return digest, memorystore.TurnCapture{}, result, false,
			fmt.Errorf("memoryruntime: read canonical capture receipt: %w", err)
	}
	defaultProject := strings.TrimSpace(r.Config.ProjectKey)
	if defaultProject == "" {
		defaultProject = r.Store.ProjectKey()
	}
	capture := turnCaptureFromCanonicalReceipt(receipt, defaultProject)
	if receipt.State != "materialized" {
		result.Classification = memorystore.DigestReplayMissingReceipt
		result.Reason = "receipt_not_materialized"
		return digest, capture, result, false, nil
	}
	if !sameTurnCaptureIdentity(digest, capture) {
		result.Classification = memorystore.DigestReplayInvalidIdentity
		result.Reason = "canonical_receipt_identity"
		return digest, capture, result, false, nil
	}
	session, err := r.findSession(ctx, digest.SessionID)
	if err != nil {
		return digest, capture, result, false,
			fmt.Errorf("memoryruntime: read session for digest replay: %w", err)
	}
	if session.SessionGeneration <= 0 || session.SessionGeneration != digest.SessionGeneration {
		result.Classification = memorystore.DigestReplayStaleGeneration
		result.Reason = "current_session_generation"
		return digest, capture, result, false, nil
	}
	if err := source.ValidateTurnCaptureReceipt(ctx, receipt); err != nil {
		result.Classification = memorystore.DigestReplayInvalidIdentity
		result.Reason = "canonical_range_validation"
		return digest, capture, result, false, nil
	}
	return digest, capture, result, true, nil
}

func turnCaptureFromCanonicalReceipt(receipt chat.TurnCaptureReceipt, defaultProject string) memorystore.TurnCapture {
	projectKey := strings.TrimSpace(receipt.ProjectKey)
	if projectKey == "" {
		projectKey = strings.TrimSpace(defaultProject)
	}
	return memorystore.TurnCapture{
		ID:                 strings.TrimSpace(receipt.ID),
		ProjectKey:         projectKey,
		SessionID:          strings.TrimSpace(receipt.SessionID),
		SessionGeneration:  receipt.SessionGeneration,
		TurnID:             strings.TrimSpace(receipt.TurnID),
		MessageSeqStart:    receipt.MessageSeqStart,
		MessageSeqEnd:      receipt.MessageSeqEnd,
		CanonicalRevision:  receipt.CanonicalRevision,
		RangeHash:          strings.TrimSpace(receipt.RangeHash),
		TraceID:            strings.TrimSpace(receipt.TraceID),
		UserMessageID:      strings.TrimSpace(receipt.UserMessageID),
		AssistantMessageID: strings.TrimSpace(receipt.AssistantMessageID),
		Outcome:            strings.TrimSpace(receipt.Outcome),
		Steps:              receipt.Steps,
		UsageJSON:          receipt.UsageJSON,
		CreatedAt:          receipt.CreatedAt,
	}
}

func sameTurnCaptureIdentity(digest memorystore.TurnDigest, capture memorystore.TurnCapture) bool {
	return capture.ID == digest.CaptureReceiptID &&
		capture.ProjectKey == digest.ProjectKey &&
		capture.SessionID == digest.SessionID &&
		capture.SessionGeneration == digest.SessionGeneration &&
		capture.TurnID == digest.TurnID &&
		capture.MessageSeqStart == digest.MessageSeqStart &&
		capture.MessageSeqEnd == digest.MessageSeqEnd &&
		capture.CanonicalRevision == digest.SourceRevision &&
		capture.RangeHash == digest.RangeHash &&
		capture.SessionGeneration > 0 &&
		capture.MessageSeqStart > 0 &&
		capture.MessageSeqEnd >= capture.MessageSeqStart &&
		capture.CanonicalRevision > 0 &&
		capture.RangeHash != ""
}
