package memorystore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// CoreUsageRecord is the usage ledger row for a synthetic Core identity.
// Core rows intentionally do not enter memory_usage because Core is not a
// memory_entries record and must not be made to look like one.
type CoreUsageRecord struct {
	ID          string    `json:"id"`
	ProjectKey  string    `json:"project_key"`
	CoreID      string    `json:"core_id"`
	CoreVersion int64     `json:"core_version"`
	SessionID   string    `json:"session_id"`
	TurnID      string    `json:"turn_id,omitempty"`
	MessageID   string    `json:"message_id,omitempty"`
	UsedAt      time.Time `json:"used_at"`
	UseKind     UsageKind `json:"use_kind"`
}

type CoreUsageFilter struct {
	CoreID    string
	SessionID string
	TurnID    string
	UseKind   UsageKind
	Limit     int
}

type CoreExposure struct {
	CoreIDs   []string
	SessionID string
	TurnID    string
	MessageID string
	UseKind   UsageKind
	UsedAt    time.Time
}

type CoreExposureResult struct {
	Recorded int
	Skipped  int
}

// RecordCoreExposure writes usage rows for Core IDs without touching the
// ordinary memory_entries usage counters. It accepts any retained Core status
// so a response that races with explicit activation/archive still has an
// auditable identity.
func (s *Store) RecordCoreExposure(ctx context.Context, exposure CoreExposure) (CoreExposureResult, error) {
	var result CoreExposureResult
	db, err := s.ensureV2()
	if err != nil {
		return result, err
	}
	kind := UsageKind(strings.ToLower(strings.TrimSpace(string(exposure.UseKind))))
	if kind == "" {
		kind = UsageInjectedOnly
	}
	if kind != UsageCited && kind != UsageRead && kind != UsageInjectedOnly {
		return result, fmt.Errorf("memorystore: invalid Core usage kind %q", kind)
	}
	sessionID := strings.TrimSpace(exposure.SessionID)
	if sessionID == "" {
		return result, fmt.Errorf("memorystore: Core usage session id is required")
	}
	ids := uniqueStrings(exposure.CoreIDs)
	if len(ids) == 0 {
		return result, nil
	}
	usedAt := exposure.UsedAt.UTC()
	if usedAt.IsZero() {
		usedAt = time.Now().UTC()
	}

	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, coreID := range ids {
		var version int64
		err := tx.QueryRowContext(ctx, `
SELECT version
FROM memory_core
WHERE id = ? AND tenant_id = ? AND project_key = ?
`, coreID, s.tenantID(), s.cfg.ProjectKey).Scan(&version)
		if err == sql.ErrNoRows {
			result.Skipped++
			continue
		}
		if err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_core_usage (
    id, tenant_id, project_key, core_id, core_version, session_id, turn_id, message_id,
    used_at, use_kind
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, "core_usage_"+uuid.NewString(), s.tenantID(), s.cfg.ProjectKey, coreID, version, sessionID,
			nullableString(exposure.TurnID), nullableString(exposure.MessageID),
			usedAt.Format(time.RFC3339Nano), string(kind)); err != nil {
			return result, err
		}
		result.Recorded++
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) ListCoreUsage(ctx context.Context, filter CoreUsageFilter) ([]CoreUsageRecord, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	where := []string{"u.tenant_id = ?", "u.project_key = ?"}
	args := []interface{}{s.tenantID(), s.cfg.ProjectKey}
	if coreID := strings.TrimSpace(filter.CoreID); coreID != "" {
		where = append(where, "u.core_id = ?")
		args = append(args, coreID)
	}
	if sessionID := strings.TrimSpace(filter.SessionID); sessionID != "" {
		where = append(where, "u.session_id = ?")
		args = append(args, sessionID)
	}
	if turnID := strings.TrimSpace(filter.TurnID); turnID != "" {
		where = append(where, "u.turn_id = ?")
		args = append(args, turnID)
	}
	if kind := strings.TrimSpace(string(filter.UseKind)); kind != "" {
		args = append(args, kind)
		where = append(where, "u.use_kind = ?")
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `
SELECT u.id, u.project_key, u.core_id, u.core_version, u.session_id,
       COALESCE(u.turn_id, ''), COALESCE(u.message_id, ''), u.used_at, u.use_kind
FROM memory_core_usage u
WHERE `+strings.Join(where, " AND ")+`
ORDER BY u.used_at DESC, u.id DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]CoreUsageRecord, 0, limit)
	for rows.Next() {
		var (
			record                         CoreUsageRecord
			id, project, core, session     sql.NullString
			turn, message, usedAt, useKind sql.NullString
			version                        sql.NullInt64
		)
		if err := rows.Scan(&id, &project, &core, &version, &session,
			&turn, &message, &usedAt, &useKind); err != nil {
			return nil, err
		}
		record.ID = id.String
		record.ProjectKey = project.String
		record.CoreID = core.String
		record.CoreVersion = version.Int64
		record.SessionID = session.String
		record.TurnID = turn.String
		record.MessageID = message.String
		record.UsedAt = parseTime(usedAt.String)
		record.UseKind = UsageKind(useKind.String)
		out = append(out, record)
	}
	return out, rows.Err()
}
