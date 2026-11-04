package supervision

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ClaimResume inserts the settlement claim once. Release->claim is the only
// retryable transition; a claimed or submitted row is never silently restarted.
func (s *SQLiteSupervisionStore) ClaimResume(ctx context.Context, claim ResumeClaim) (ResumeClaim, bool, error) {
	if s == nil {
		return ResumeClaim{}, false, fmt.Errorf("supervision store is nil")
	}
	claim.ClaimKey = strings.TrimSpace(claim.ClaimKey)
	if claim.ClaimKey == "" {
		return ResumeClaim{}, false, fmt.Errorf("resume claim key is required")
	}
	if claim.State == "" {
		claim.State = ResumeClaimStateClaimed
	}
	if claim.State != ResumeClaimStateClaimed {
		return ResumeClaim{}, false, fmt.Errorf("resume claim must start in claimed state")
	}
	if claim.ClaimedAt.IsZero() {
		claim.ClaimedAt = time.Now().UTC()
	}
	db, err := s.dbOrErr()
	if err != nil {
		return ResumeClaim{}, false, err
	}
	result, err := db.ExecContext(ctx, `
		INSERT INTO supervision_resume_claims (
			claim_key, root_scope_id, parent_session_id, parent_turn_id,
			parent_episode_id, settlement_id, settlement_cohort_id,
			state, claimed_at, delivery_attempt, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'claimed', ?, 1, 1)
		ON CONFLICT(claim_key) DO NOTHING
	`,
		claim.ClaimKey,
		strings.TrimSpace(claim.RootScopeID),
		strings.TrimSpace(claim.ParentSessionID),
		strings.TrimSpace(claim.ParentTurnID),
		strings.TrimSpace(claim.ParentEpisodeID),
		strings.TrimSpace(claim.SettlementID),
		strings.TrimSpace(claim.SettlementCohortID),
		formatSupervisionTime(claim.ClaimedAt),
	)
	if err != nil {
		return ResumeClaim{}, false, fmt.Errorf("claim resume: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		claim.Version = 1
		claim.DeliveryAttempt = 1
		return claim, true, nil
	}

	existing, err := s.GetResumeClaim(ctx, claim.ClaimKey)
	if err != nil {
		return ResumeClaim{}, false, err
	}
	if existing == nil {
		return ResumeClaim{}, false, fmt.Errorf("resume claim disappeared after conflict")
	}
	if existing.State != ResumeClaimStateReleased {
		return *existing, false, nil
	}
	// A released claim is an explicit retryable decision (for example a failed
	// delivery). Reclaim it atomically; a concurrent retry loses the CAS.
	updated := existing.ClaimedAt.UTC()
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	result, err = db.ExecContext(ctx, `
		UPDATE supervision_resume_claims
		SET state = 'claimed', claimed_at = ?, submitted_at = NULL,
			model_turn_id = '', delivery_attempt = delivery_attempt + 1,
			version = version + 1
		WHERE claim_key = ? AND state = 'released' AND version = ?
	`, formatSupervisionTime(updated), claim.ClaimKey, existing.Version)
	if err != nil {
		return ResumeClaim{}, false, fmt.Errorf("reclaim resume: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		latest, getErr := s.GetResumeClaim(ctx, claim.ClaimKey)
		if getErr != nil {
			return ResumeClaim{}, false, getErr
		}
		if latest == nil {
			return ResumeClaim{}, false, fmt.Errorf("resume claim disappeared during reclaim")
		}
		return *latest, false, nil
	}
	latest, err := s.GetResumeClaim(ctx, claim.ClaimKey)
	if err != nil {
		return ResumeClaim{}, false, err
	}
	if latest == nil {
		return ResumeClaim{}, false, fmt.Errorf("resume claim disappeared after reclaim")
	}
	return *latest, true, nil
}

func (s *SQLiteSupervisionStore) GetResumeClaim(ctx context.Context, claimKey string) (*ResumeClaim, error) {
	claimKey = strings.TrimSpace(claimKey)
	if claimKey == "" {
		return nil, nil
	}
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	var (
		claim       ResumeClaim
		state       string
		claimedAt   sql.NullString
		submittedAt sql.NullString
	)
	err = db.QueryRowContext(ctx, `
		SELECT claim_key, root_scope_id, parent_session_id, parent_turn_id,
			parent_episode_id, settlement_id, settlement_cohort_id,
			state, claimed_at, submitted_at, model_turn_id, delivery_attempt, version
		FROM supervision_resume_claims WHERE claim_key = ?
	`, claimKey).Scan(
		&claim.ClaimKey, &claim.RootScopeID, &claim.ParentSessionID, &claim.ParentTurnID,
		&claim.ParentEpisodeID, &claim.SettlementID, &claim.SettlementCohortID,
		&state, &claimedAt, &submittedAt, &claim.ModelTurnID, &claim.DeliveryAttempt, &claim.Version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get resume claim: %w", err)
	}
	claim.State = ResumeClaimState(state)
	claim.ClaimedAt = parseSupervisionTime(claimedAt)
	claim.SubmittedAt = parseNullableSupervisionTime(submittedAt)
	return &claim, nil
}

func (s *SQLiteSupervisionStore) MarkResumeSubmitted(ctx context.Context, claimKey, modelTurnID string, at time.Time, expectedVersion int64) (bool, error) {
	claimKey = strings.TrimSpace(claimKey)
	if claimKey == "" {
		return false, fmt.Errorf("resume claim key is required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `
		UPDATE supervision_resume_claims
		SET state = 'submitted', submitted_at = ?, model_turn_id = ?, version = version + 1
		WHERE claim_key = ? AND state = 'claimed' AND version = ?
	`, formatSupervisionTime(at), strings.TrimSpace(modelTurnID), claimKey, expectedVersion)
	if err != nil {
		return false, fmt.Errorf("mark resume submitted: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// RenewResumeClaim refreshes claimed_at for the live lease holder. Version is
// deliberately unchanged so the holder's later CAS transitions remain valid.
func (s *SQLiteSupervisionStore) RenewResumeClaim(ctx context.Context, claimKey string, at time.Time, expectedVersion int64) (bool, error) {
	claimKey = strings.TrimSpace(claimKey)
	if claimKey == "" {
		return false, nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `
		UPDATE supervision_resume_claims
		SET claimed_at = ?
		WHERE claim_key = ? AND state = 'claimed' AND version = ?
	`, formatSupervisionTime(at), claimKey, expectedVersion)
	if err != nil {
		return false, fmt.Errorf("renew resume claim: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (s *SQLiteSupervisionStore) ReleaseResumeClaim(ctx context.Context, claimKey string, expectedVersion int64) (bool, error) {
	claimKey = strings.TrimSpace(claimKey)
	if claimKey == "" {
		return false, nil
	}
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `
		UPDATE supervision_resume_claims
		SET state = 'released', version = version + 1
		WHERE claim_key = ? AND state = 'claimed' AND version = ?
	`, claimKey, expectedVersion)
	if err != nil {
		return false, fmt.Errorf("release resume claim: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// RecoverStaleResumeClaims is the crash-recovery half of the exactly-once
// boundary. A claim that stayed `claimed` past the cutoff means the owner may
// have submitted a model turn and died before recording the receipt. We
// converge it to `submitted` with a synthetic model-turn id rather than retry:
// a missing automatic resume is recoverable by the user/notification, a
// duplicate model turn is not.
func (s *SQLiteSupervisionStore) RecoverStaleResumeClaims(ctx context.Context, claimedBefore time.Time) (int64, error) {
	if s == nil {
		return 0, nil
	}
	if claimedBefore.IsZero() {
		return 0, nil
	}
	db, err := s.dbOrErr()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	result, err := db.ExecContext(ctx, `
		UPDATE supervision_resume_claims
		SET state = 'submitted', submitted_at = ?, model_turn_id = 'recovered_unknown',
			version = version + 1
		WHERE state = 'claimed' AND claimed_at < ?
	`, formatSupervisionTime(now), formatSupervisionTime(claimedBefore))
	if err != nil {
		return 0, fmt.Errorf("recover stale resume claims: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// PruneResumeClaims removes terminal claim rows after the retention window.
// Claimed rows stay forever until recovery/submission; deleting them could
// re-open a duplicate automatic resume.
func (s *SQLiteSupervisionStore) PruneResumeClaims(ctx context.Context, before time.Time) (int64, error) {
	if s == nil {
		return 0, nil
	}
	if before.IsZero() {
		return 0, nil
	}
	db, err := s.dbOrErr()
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(ctx, `
		DELETE FROM supervision_resume_claims
		WHERE state IN ('submitted', 'released')
			AND COALESCE(submitted_at, claimed_at) < ?
	`, formatSupervisionTime(before.UTC()))
	if err != nil {
		return 0, fmt.Errorf("prune resume claims: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return affected, nil
}
