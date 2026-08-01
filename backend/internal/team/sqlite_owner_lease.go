package team

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AcquireOrchestratorLease atomically acquires (or re-acquires) the
// orchestrator owner lease for a team. It succeeds when no valid lease exists
// (fresh insert), when the existing lease has expired (takeover, restart
// counter incremented), or when the caller already owns the current lease
// (idempotent re-acquire that extends the same fencing token). It fails when
// another instance holds a valid lease.
func (s *SQLiteStore) AcquireOrchestratorLease(ctx context.Context, lease OrchestratorLease, leaseTTL time.Duration) (*OrchestratorLease, bool, error) {
	if s == nil {
		return nil, false, fmt.Errorf("team store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return nil, false, err
	}
	lease.TeamID = strings.TrimSpace(lease.TeamID)
	lease.OwnerID = strings.TrimSpace(lease.OwnerID)
	if lease.TeamID == "" {
		return nil, false, fmt.Errorf("team id is required")
	}
	if lease.OwnerID == "" {
		return nil, false, fmt.Errorf("owner id is required")
	}
	if leaseTTL <= 0 {
		leaseTTL = DefaultOrchestratorLeaseTTL
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(leaseTTL)
	var (
		acquired *OrchestratorLease
		claimed  bool
	)
	err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		var (
			existingOwner     string
			existingToken     string
			existingLeaseText string
			existingRestarts  int
		)
		err := tx.QueryRowContext(ctx, `
			SELECT owner_id, fencing_token, lease_until, restart_count
			FROM orchestrator_owner_leases
			WHERE team_id = ?
		`, lease.TeamID).Scan(&existingOwner, &existingToken, &existingLeaseText, &existingRestarts)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read orchestrator owner lease: %w", err)
		}
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO orchestrator_owner_leases (
					team_id, owner_id, owner_instance, lease_until, fencing_token,
					heartbeat_at, restart_count, last_error, created_at, updated_at
				) VALUES (?, ?, ?, ?, lower(hex(randomblob(16))), ?, 0, '', ?, ?)
			`, lease.TeamID, lease.OwnerID, lease.OwnerInstance, formatTime(leaseUntil), formatTime(now), formatTime(now), formatTime(now)); err != nil {
				return fmt.Errorf("insert orchestrator owner lease: %w", err)
			}
			var token string
			if err := tx.QueryRowContext(ctx, `
				SELECT fencing_token FROM orchestrator_owner_leases WHERE team_id = ?
			`, lease.TeamID).Scan(&token); err != nil {
				return fmt.Errorf("read minted orchestrator owner token: %w", err)
			}
			acquired = &OrchestratorLease{
				TeamID:        lease.TeamID,
				OwnerID:       lease.OwnerID,
				OwnerInstance: lease.OwnerInstance,
				LeaseUntil:    leaseUntil,
				FencingToken:  token,
				HeartbeatAt:   now,
				RestartCount:  0,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			claimed = true
			return nil
		}
		existingLeaseUntil := parseTime(existingLeaseText)
		if existingLeaseUntil.IsZero() || existingLeaseUntil.Before(now) {
			// Takeover: the previous owner's lease expired.
			if _, err := tx.ExecContext(ctx, `
				UPDATE orchestrator_owner_leases
				SET owner_id = ?, owner_instance = ?, lease_until = ?,
					fencing_token = lower(hex(randomblob(16))), heartbeat_at = ?,
					restart_count = restart_count + 1, last_error = '', updated_at = ?
				WHERE team_id = ?
			`, lease.OwnerID, lease.OwnerInstance, formatTime(leaseUntil), formatTime(now), formatTime(now), lease.TeamID); err != nil {
				return fmt.Errorf("takeover orchestrator owner lease: %w", err)
			}
			var token string
			if err := tx.QueryRowContext(ctx, `
				SELECT fencing_token FROM orchestrator_owner_leases WHERE team_id = ?
			`, lease.TeamID).Scan(&token); err != nil {
				return fmt.Errorf("read minted orchestrator owner token: %w", err)
			}
			acquired = &OrchestratorLease{
				TeamID:        lease.TeamID,
				OwnerID:       lease.OwnerID,
				OwnerInstance: lease.OwnerInstance,
				LeaseUntil:    leaseUntil,
				FencingToken:  token,
				HeartbeatAt:   now,
				RestartCount:  existingRestarts + 1,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			claimed = true
			return nil
		}
		if existingOwner == lease.OwnerID && existingToken != "" && existingToken == lease.FencingToken {
			// Same owner re-acquiring while the lease is still valid: keep the
			// fencing token stable so in-flight claims stay fenced to us.
			if _, err := tx.ExecContext(ctx, `
				UPDATE orchestrator_owner_leases
				SET lease_until = ?, heartbeat_at = ?, updated_at = ?
				WHERE team_id = ?
			`, formatTime(leaseUntil), formatTime(now), formatTime(now), lease.TeamID); err != nil {
				return fmt.Errorf("extend orchestrator owner lease: %w", err)
			}
			acquired = &OrchestratorLease{
				TeamID:        lease.TeamID,
				OwnerID:       existingOwner,
				OwnerInstance: lease.OwnerInstance,
				LeaseUntil:    leaseUntil,
				FencingToken:  existingToken,
				HeartbeatAt:   now,
				RestartCount:  existingRestarts,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			claimed = true
			return nil
		}
		// Someone else holds a valid lease.
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return acquired, claimed, nil
}

// RenewOrchestratorLease extends the lease_until for the current owner and
// fencing token. It reports false when the lease was lost (token mismatch) or
// already expired (taken over by another instance).
func (s *SQLiteStore) RenewOrchestratorLease(ctx context.Context, teamID, ownerID, token string, leaseTTL time.Duration) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("team store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return false, err
	}
	if leaseTTL <= 0 {
		leaseTTL = DefaultOrchestratorLeaseTTL
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(leaseTTL)
	result, err := s.db.ExecContext(ctx, `
		UPDATE orchestrator_owner_leases
		SET lease_until = ?, heartbeat_at = ?, last_tick_at = ?, updated_at = ?
		WHERE team_id = ? AND owner_id = ? AND fencing_token = ? AND lease_until >= ?
	`, formatTime(leaseUntil), formatTime(now), formatTime(now), formatTime(now),
		strings.TrimSpace(teamID), strings.TrimSpace(ownerID), strings.TrimSpace(token), formatTime(now))
	if err != nil {
		return false, fmt.Errorf("renew orchestrator owner lease: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("renew orchestrator owner lease rows: %w", err)
	}
	return affected > 0, nil
}

// MarkOrchestratorTickSuccess records a successful orchestrator tick under the
// current fencing token. It fails with ErrOrchestratorLeaseLost when the
// caller no longer owns the team.
func (s *SQLiteStore) MarkOrchestratorTickSuccess(ctx context.Context, teamID, token string) error {
	if s == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE orchestrator_owner_leases
		SET last_successful_tick_at = ?, updated_at = ?
		WHERE team_id = ? AND fencing_token = ? AND lease_until >= ?
	`, formatTime(now), formatTime(now), strings.TrimSpace(teamID), strings.TrimSpace(token), formatTime(now))
	if err != nil {
		return fmt.Errorf("mark orchestrator tick success: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark orchestrator tick success rows: %w", err)
	}
	if affected == 0 {
		return ErrOrchestratorLeaseLost
	}
	return nil
}

// ReleaseOrchestratorLease releases a still-valid lease when the caller owns
// it (matching owner id, fencing token, and unexpired lease_until). An
// expired or already-taken-over lease is a no-op so diagnostic history and the
// restart counter survive a crashed owner.
func (s *SQLiteStore) ReleaseOrchestratorLease(ctx context.Context, teamID, ownerID, token string) error {
	if s == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM orchestrator_owner_leases
		WHERE team_id = ? AND owner_id = ? AND fencing_token = ? AND lease_until >= ?
	`, strings.TrimSpace(teamID), strings.TrimSpace(ownerID), strings.TrimSpace(token), formatTime(time.Now().UTC())); err != nil {
		return fmt.Errorf("release orchestrator owner lease: %w", err)
	}
	return nil
}

// GetOrchestratorLease returns the current owner lease record for a team, or
// nil when no lease exists.
func (s *SQLiteStore) GetOrchestratorLease(ctx context.Context, teamID string) (*OrchestratorLease, error) {
	if s == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT team_id, owner_id, owner_instance, lease_until, fencing_token, heartbeat_at,
			last_tick_at, last_successful_tick_at, restart_count, last_error, created_at, updated_at
		FROM orchestrator_owner_leases
		WHERE team_id = ?
	`, strings.TrimSpace(teamID))
	var (
		record       OrchestratorLease
		leaseText    string
		heartbeat    string
		lastTick     string
		lastSuccess  string
		createdText  string
		updatedText  string
	)
	err := row.Scan(&record.TeamID, &record.OwnerID, &record.OwnerInstance, &leaseText, &record.FencingToken,
		&heartbeat, &lastTick, &lastSuccess, &record.RestartCount, &record.LastError, &createdText, &updatedText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read orchestrator owner lease: %w", err)
	}
	record.LeaseUntil = parseTime(leaseText)
	record.HeartbeatAt = parseTime(heartbeat)
	if value := parseTime(lastTick); !value.IsZero() {
		record.LastTickAt = &value
	}
	if value := parseTime(lastSuccess); !value.IsZero() {
		record.LastSuccessfulTickAt = &value
	}
	record.CreatedAt = parseTime(createdText)
	record.UpdatedAt = parseTime(updatedText)
	return &record, nil
}

// ValidateOrchestratorOwner reports whether the given owner/token pair still
// holds a valid, unexpired orchestrator lease for the team.
func (s *SQLiteStore) ValidateOrchestratorOwner(ctx context.Context, teamID, ownerID, token string) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("team store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return false, err
	}
	var one int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM orchestrator_owner_leases
		WHERE team_id = ? AND owner_id = ? AND fencing_token = ? AND lease_until >= ?
	`, strings.TrimSpace(teamID), strings.TrimSpace(ownerID), strings.TrimSpace(token), formatTime(time.Now().UTC())).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("validate orchestrator owner: %w", err)
	}
	return true, nil
}
