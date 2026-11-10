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

type UsageKind string

const (
	UsageCited        UsageKind = "cited"
	UsageRead         UsageKind = "read"
	UsageInjectedOnly UsageKind = "injected_only"
)

type UsageRecord struct {
	ID        string    `json:"id"`
	MemoryID  string    `json:"memory_id"`
	SessionID string    `json:"session_id"`
	TurnID    string    `json:"turn_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	UsedAt    time.Time `json:"used_at"`
	UseKind   UsageKind `json:"use_kind"`
}

type UsageFilter struct {
	MemoryID  string
	SessionID string
	TurnID    string
	UseKind   UsageKind
	Limit     int
}

type Exposure struct {
	MemoryIDs []string
	SessionID string
	TurnID    string
	MessageID string
	UseKind   UsageKind
	UsedAt    time.Time
}

type ExposureResult struct {
	Recorded   int
	Skipped    int
	UseCounted int
}

// RecordExposure writes usage rows for a bounded id set. Only cited/read
// events update use_count and last_used_at; injected_only remains an exposure
// signal and never inflates the usage counter.
func (s *Store) RecordExposure(ctx context.Context, exposure Exposure) (ExposureResult, error) {
	var result ExposureResult
	db, err := s.ensureV2()
	if err != nil {
		return result, err
	}
	kind := UsageKind(strings.ToLower(strings.TrimSpace(string(exposure.UseKind))))
	if kind == "" {
		kind = UsageInjectedOnly
	}
	if kind != UsageCited && kind != UsageRead && kind != UsageInjectedOnly {
		return result, fmt.Errorf("memorystore: invalid usage kind %q", kind)
	}
	sessionID := strings.TrimSpace(exposure.SessionID)
	if sessionID == "" {
		return result, fmt.Errorf("memorystore: usage session id is required")
	}
	ids := uniqueStrings(exposure.MemoryIDs)
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
	useCounts := kind == UsageCited || kind == UsageRead
	for _, id := range ids {
		var exists int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_entries WHERE id = ? AND project_key = ?
`, id, s.cfg.ProjectKey).Scan(&exists); err != nil {
			return result, err
		}
		if exists == 0 {
			result.Skipped++
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_usage (id, memory_id, session_id, turn_id, message_id, used_at, use_kind)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, "usage_"+uuid.NewString(), id, sessionID, nullableString(exposure.TurnID),
			nullableString(exposure.MessageID), usedAt.Format(time.RFC3339Nano), string(kind)); err != nil {
			return result, err
		}
		result.Recorded++
		if useCounts {
			if _, err := tx.ExecContext(ctx, `
UPDATE memory_entries SET use_count = use_count + 1, last_used_at = ?
WHERE id = ? AND project_key = ?
`, usedAt.Format(time.RFC3339Nano), id, s.cfg.ProjectKey); err != nil {
				return result, err
			}
			result.UseCounted++
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) ListUsage(ctx context.Context, filter UsageFilter) ([]UsageRecord, error) {
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
	where := []string{"e.project_key = ?"}
	args := []interface{}{s.cfg.ProjectKey}
	if id := strings.TrimSpace(filter.MemoryID); id != "" {
		where = append(where, "u.memory_id = ?")
		args = append(args, id)
	}
	if id := strings.TrimSpace(filter.SessionID); id != "" {
		where = append(where, "u.session_id = ?")
		args = append(args, id)
	}
	if id := strings.TrimSpace(filter.TurnID); id != "" {
		where = append(where, "u.turn_id = ?")
		args = append(args, id)
	}
	if kind := strings.TrimSpace(string(filter.UseKind)); kind != "" {
		where = append(where, "u.use_kind = ?")
		args = append(args, kind)
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `
SELECT u.id, u.memory_id, u.session_id, COALESCE(u.turn_id, ''),
       COALESCE(u.message_id, ''), u.used_at, u.use_kind
FROM memory_usage u
JOIN memory_entries e ON e.id = u.memory_id
WHERE `+strings.Join(where, " AND ")+`
ORDER BY u.used_at DESC LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UsageRecord, 0, limit)
	for rows.Next() {
		var (
			record                               UsageRecord
			id, memoryID, session, turn, message sql.NullString
			usedAt, kind                         sql.NullString
		)
		if err := rows.Scan(&id, &memoryID, &session, &turn, &message, &usedAt, &kind); err != nil {
			return nil, err
		}
		record = UsageRecord{
			ID: id.String, MemoryID: memoryID.String, SessionID: session.String,
			TurnID: turn.String, MessageID: message.String,
			UsedAt: parseTime(usedAt.String), UseKind: UsageKind(kind.String),
		}
		out = append(out, record)
	}
	return out, rows.Err()
}
