package memorystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

var ErrMaintenanceLeaseLost = errors.New("memorystore: maintenance lease is no longer owned")
var ErrMaintenanceLeaseBusy = errors.New("memorystore: maintenance lease is owned by another runtime")

// MaintenanceLease identifies one project-maintenance owner incarnation.
// FencingToken is monotonically increased whenever an expired lease is taken
// over; owner IDs are diagnostic and are not sufficient fencing by themselves.
type MaintenanceLease struct {
	ProjectKey   string
	OwnerID      string
	OwnerKind    string
	FencingToken int64
	AcquiredAt   time.Time
	HeartbeatAt  time.Time
	ExpiresAt    time.Time
}

type maintenanceLeaseContextKey struct{}

// WithMaintenanceLease attaches the project owner fence to downstream memory
// operations. Write transactions that support maintenance fencing validate it
// against the current database row before changing state.
func WithMaintenanceLease(ctx context.Context, lease MaintenanceLease) context.Context {
	return context.WithValue(ctx, maintenanceLeaseContextKey{}, lease)
}

func maintenanceLeaseFromContext(ctx context.Context) (MaintenanceLease, bool) {
	if ctx == nil {
		return MaintenanceLease{}, false
	}
	lease, ok := ctx.Value(maintenanceLeaseContextKey{}).(MaintenanceLease)
	return lease, ok && lease.OwnerID != "" && lease.FencingToken > 0
}

// AcquireMaintenanceLease creates the persistent project lease or takes it
// over after expiry. A live lease held by another owner is returned with
// acquired=false. The lease row is never deleted, preserving token generation.
func (s *Store) AcquireMaintenanceLease(
	ctx context.Context,
	ownerID, ownerKind string,
	ttl time.Duration,
) (lease MaintenanceLease, acquired bool, err error) {
	err = sqliteutil.RetryWriteTx(ctx, nil, func(attemptCtx context.Context) error {
		lease, acquired, err = s.acquireMaintenanceLeaseAttempt(attemptCtx, ownerID, ownerKind, ttl)
		return err
	})
	return lease, acquired, err
}

func (s *Store) acquireMaintenanceLeaseAttempt(
	ctx context.Context,
	ownerID, ownerKind string,
	ttl time.Duration,
) (lease MaintenanceLease, acquired bool, err error) {
	db, err := s.ensureV2()
	if err != nil {
		return MaintenanceLease{}, false, err
	}
	ownerID = strings.TrimSpace(ownerID)
	ownerKind = strings.TrimSpace(ownerKind)
	projectKey := strings.TrimSpace(s.cfg.ProjectKey)
	if ownerID == "" || ownerKind == "" || ttl <= 0 {
		return MaintenanceLease{}, false, fmt.Errorf("memorystore: maintenance owner, kind and positive ttl are required")
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return MaintenanceLease{}, false, fmt.Errorf("memorystore: begin maintenance lease acquire: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	nowMS, err := maintenanceDBNowMS(ctx, tx)
	if err != nil {
		return MaintenanceLease{}, false, err
	}
	var current MaintenanceLease
	var acquiredMS, heartbeatMS, expiresMS int64
	err = tx.QueryRowContext(ctx, `
SELECT project_key, owner_id, owner_kind, fencing_token,
       acquired_at_ms, heartbeat_at_ms, expires_at_ms
FROM memory_maintenance_leases WHERE project_key = ?
`, projectKey).Scan(
		&current.ProjectKey, &current.OwnerID, &current.OwnerKind, &current.FencingToken,
		&acquiredMS, &heartbeatMS, &expiresMS,
	)
	if err == nil {
		current.AcquiredAt = time.UnixMilli(acquiredMS).UTC()
		current.HeartbeatAt = time.UnixMilli(heartbeatMS).UTC()
		current.ExpiresAt = time.UnixMilli(expiresMS).UTC()
		return s.acquireExistingMaintenanceLease(ctx, tx, current, ownerID, ownerKind, ttl, nowMS)
	}
	if err != sql.ErrNoRows {
		return MaintenanceLease{}, false, fmt.Errorf("memorystore: read maintenance lease: %w", err)
	}

	expiresMS = nowMS + ttl.Milliseconds()
	if expiresMS <= nowMS {
		expiresMS = nowMS + 1
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_maintenance_leases (
    project_key, owner_id, owner_kind, acquired_at_ms, heartbeat_at_ms, expires_at_ms, fencing_token
) VALUES (?, ?, ?, ?, ?, ?, 1)
`, projectKey, ownerID, ownerKind, nowMS, nowMS, expiresMS); err != nil {
		return MaintenanceLease{}, false, fmt.Errorf("memorystore: insert maintenance lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return MaintenanceLease{}, false, fmt.Errorf("memorystore: commit maintenance lease acquire: %w", err)
	}
	return maintenanceLeaseFromMS(projectKey, ownerID, ownerKind, 1, nowMS, nowMS, expiresMS), true, nil
}

func (s *Store) acquireExistingMaintenanceLease(
	ctx context.Context,
	tx *sql.Tx,
	current MaintenanceLease,
	ownerID, ownerKind string,
	ttl time.Duration,
	nowMS int64,
) (MaintenanceLease, bool, error) {
	var acquiredMS, heartbeatMS, expiresMS int64
	err := tx.QueryRowContext(ctx, `
SELECT acquired_at_ms, heartbeat_at_ms, expires_at_ms
FROM memory_maintenance_leases WHERE project_key = ?
`, current.ProjectKey).Scan(&acquiredMS, &heartbeatMS, &expiresMS)
	if err != nil {
		return MaintenanceLease{}, false, fmt.Errorf("memorystore: read maintenance lease timestamps: %w", err)
	}
	if expiresMS > nowMS && current.OwnerID != ownerID {
		if err := tx.Commit(); err != nil {
			return MaintenanceLease{}, false, fmt.Errorf("memorystore: finish maintenance lease observation: %w", err)
		}
		current.AcquiredAt = time.UnixMilli(acquiredMS).UTC()
		current.HeartbeatAt = time.UnixMilli(heartbeatMS).UTC()
		current.ExpiresAt = time.UnixMilli(expiresMS).UTC()
		return current, false, nil
	}
	newExpiresMS := nowMS + ttl.Milliseconds()
	if newExpiresMS <= nowMS {
		newExpiresMS = nowMS + 1
	}
	newToken := current.FencingToken
	newAcquiredMS := acquiredMS
	if expiresMS <= nowMS {
		newToken++
		newAcquiredMS = nowMS
	}
	res, err := tx.ExecContext(ctx, `
UPDATE memory_maintenance_leases
SET owner_id = ?, owner_kind = ?, acquired_at_ms = ?, heartbeat_at_ms = ?,
    expires_at_ms = ?, fencing_token = ?
WHERE project_key = ? AND fencing_token = ? AND expires_at_ms = ?
`, ownerID, ownerKind, newAcquiredMS, nowMS, newExpiresMS, newToken,
		current.ProjectKey, current.FencingToken, expiresMS)
	if err != nil {
		return MaintenanceLease{}, false, fmt.Errorf("memorystore: update maintenance lease: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return MaintenanceLease{}, false, err
	}
	if affected != 1 {
		return MaintenanceLease{}, false, ErrMaintenanceLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return MaintenanceLease{}, false, fmt.Errorf("memorystore: commit maintenance lease takeover: %w", err)
	}
	return maintenanceLeaseFromMS(current.ProjectKey, ownerID, ownerKind, newToken,
		newAcquiredMS, nowMS, newExpiresMS), true, nil
}

// RenewMaintenanceLease extends an unexpired lease only when both the owner
// incarnation and fencing token still match.
func (s *Store) RenewMaintenanceLease(
	ctx context.Context,
	lease MaintenanceLease,
	ttl time.Duration,
) (MaintenanceLease, error) {
	db, err := s.ensureV2()
	if err != nil {
		return MaintenanceLease{}, err
	}
	lease.ProjectKey = strings.TrimSpace(lease.ProjectKey)
	lease.OwnerID = strings.TrimSpace(lease.OwnerID)
	if lease.ProjectKey != strings.TrimSpace(s.cfg.ProjectKey) || lease.OwnerID == "" ||
		lease.FencingToken <= 0 || ttl <= 0 {
		return MaintenanceLease{}, ErrMaintenanceLeaseLost
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return MaintenanceLease{}, fmt.Errorf("memorystore: begin maintenance lease renewal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	nowMS, err := maintenanceDBNowMS(ctx, tx)
	if err != nil {
		return MaintenanceLease{}, err
	}
	expiresMS := nowMS + ttl.Milliseconds()
	if expiresMS <= nowMS {
		expiresMS = nowMS + 1
	}
	res, err := tx.ExecContext(ctx, `
UPDATE memory_maintenance_leases
SET heartbeat_at_ms = ?, expires_at_ms = ?
WHERE project_key = ? AND owner_id = ? AND fencing_token = ? AND expires_at_ms > ?
`, nowMS, expiresMS, lease.ProjectKey, lease.OwnerID, lease.FencingToken, nowMS)
	if err != nil {
		return MaintenanceLease{}, fmt.Errorf("memorystore: renew maintenance lease: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return MaintenanceLease{}, err
	}
	if affected != 1 {
		return MaintenanceLease{}, ErrMaintenanceLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return MaintenanceLease{}, fmt.Errorf("memorystore: commit maintenance lease renewal: %w", err)
	}
	lease.HeartbeatAt = time.UnixMilli(nowMS).UTC()
	lease.ExpiresAt = time.UnixMilli(expiresMS).UTC()
	return lease, nil
}

// ReleaseMaintenanceLease expires the row without deleting it or resetting its
// fencing generation, allowing immediate takeover by another owner.
func (s *Store) ReleaseMaintenanceLease(ctx context.Context, lease MaintenanceLease) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	if strings.TrimSpace(lease.ProjectKey) != strings.TrimSpace(s.cfg.ProjectKey) ||
		strings.TrimSpace(lease.OwnerID) == "" || lease.FencingToken <= 0 {
		return ErrMaintenanceLeaseLost
	}
	nowMS, err := maintenanceDBNowMS(ctx, db)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `
UPDATE memory_maintenance_leases
SET heartbeat_at_ms = ?, expires_at_ms = ?
WHERE project_key = ? AND owner_id = ? AND fencing_token = ?
`, nowMS, nowMS, strings.TrimSpace(lease.ProjectKey), strings.TrimSpace(lease.OwnerID), lease.FencingToken)
	if err != nil {
		return fmt.Errorf("memorystore: release maintenance lease: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrMaintenanceLeaseLost
	}
	return nil
}

// CheckMaintenanceLease reports whether this exact owner fence is still live.
func (s *Store) CheckMaintenanceLease(ctx context.Context, lease MaintenanceLease) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	if strings.TrimSpace(lease.ProjectKey) != strings.TrimSpace(s.cfg.ProjectKey) ||
		strings.TrimSpace(lease.OwnerID) == "" || lease.FencingToken <= 0 {
		return ErrMaintenanceLeaseLost
	}
	var ownerID string
	var token, expiresMS, nowMS int64
	err = db.QueryRowContext(ctx, `
SELECT owner_id, fencing_token, expires_at_ms,
       CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)
FROM memory_maintenance_leases WHERE project_key = ?
`, strings.TrimSpace(lease.ProjectKey)).Scan(&ownerID, &token, &expiresMS, &nowMS)
	if err != nil {
		if err == sql.ErrNoRows {
			return ErrMaintenanceLeaseLost
		}
		return err
	}
	if ownerID != strings.TrimSpace(lease.OwnerID) || token != lease.FencingToken || expiresMS <= nowMS {
		return ErrMaintenanceLeaseLost
	}
	return nil
}

// ValidateMaintenanceLeaseContext validates the optional owner fence carried
// by ctx. Contexts without a maintenance lease remain valid for turn-local
// writes, while maintenance-owned writes fail closed after takeover/expiry.
func (s *Store) ValidateMaintenanceLeaseContext(ctx context.Context) error {
	lease, ok := maintenanceLeaseFromContext(ctx)
	if !ok {
		return nil
	}
	return s.CheckMaintenanceLease(ctx, lease)
}

func maintenanceDBNowMS(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}) (int64, error) {
	var nowMS int64
	err := queryer.QueryRowContext(ctx,
		`SELECT CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)`).Scan(&nowMS)
	return nowMS, err
}

func maintenanceLeaseFromMS(projectKey, ownerID, ownerKind string, token, acquiredMS, heartbeatMS, expiresMS int64) MaintenanceLease {
	return MaintenanceLease{
		ProjectKey: projectKey, OwnerID: ownerID, OwnerKind: ownerKind, FencingToken: token,
		AcquiredAt: time.UnixMilli(acquiredMS).UTC(), HeartbeatAt: time.UnixMilli(heartbeatMS).UTC(),
		ExpiresAt: time.UnixMilli(expiresMS).UTC(),
	}
}

func validateMaintenanceLeaseTx(ctx context.Context, tx *sql.Tx, projectKey string) error {
	lease, ok := maintenanceLeaseFromContext(ctx)
	if !ok {
		return nil
	}
	if strings.TrimSpace(projectKey) != strings.TrimSpace(lease.ProjectKey) {
		return ErrMaintenanceLeaseLost
	}
	var ownerID string
	var token, expiresMS, nowMS int64
	err := tx.QueryRowContext(ctx, `
SELECT owner_id, fencing_token, expires_at_ms,
       CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)
FROM memory_maintenance_leases WHERE project_key = ?
`, strings.TrimSpace(projectKey)).Scan(&ownerID, &token, &expiresMS, &nowMS)
	if err != nil {
		if err == sql.ErrNoRows {
			return ErrMaintenanceLeaseLost
		}
		return fmt.Errorf("memorystore: validate maintenance lease: %w", err)
	}
	if ownerID != strings.TrimSpace(lease.OwnerID) || token != lease.FencingToken || expiresMS <= nowMS {
		return ErrMaintenanceLeaseLost
	}
	return nil
}
