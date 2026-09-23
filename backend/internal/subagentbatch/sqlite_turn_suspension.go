package subagentbatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// IsDurable reports whether the store is backed by an on-disk database and
// therefore survives a process restart. The I9 gate (design §6.13) probes this
// before granting supervised suspension; in-memory stores answer false so the
// dispatch path degrades to the legacy synchronous semantics instead of
// promising a handle whose state dies with the process.
func (s *sqliteBatchStore) IsDurable() bool {
	if s == nil {
		return false
	}
	if batchDSNIsInMemory(s.dsn) {
		return false
	}
	// A file-backed store is reached either through Path (the documented durable
	// form) or through an explicit file: DSN. Anything else is process-local.
	if strings.TrimSpace(s.path) != "" {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.dsn)), "file:")
}

// batchDSNIsInMemory recognises every DSN form the store treats as
// process-local: the empty DSN, `mode=memory`, and `:memory:` spellings.
func batchDSNIsInMemory(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	if lower == "" {
		return true
	}
	if batchMemoryDSN(lower) {
		return true
	}
	return strings.Contains(lower, ":memory:")
}

// ParkTurnSuspension upserts the §6.12 parked-turn record. It is idempotent per
// (session_id, turn_id): re-parking the same turn overwrites the row instead of
// accumulating duplicates, which is what a retried dispatch after a crash needs.
func (s *sqliteBatchStore) ParkTurnSuspension(ctx context.Context, record *TurnSuspension) error {
	if record == nil {
		return fmt.Errorf("subagentbatch: turn suspension record is required")
	}
	record.normalize()
	if record.SessionID == "" || record.TurnID == "" {
		return fmt.Errorf("subagentbatch: turn suspension requires session_id and turn_id")
	}
	obligations, err := json.Marshal(jsonIDList(record.ObligationIDs))
	if err != nil {
		return fmt.Errorf("subagentbatch: encode obligation ids: %w", err)
	}
	resumeQueue, err := json.Marshal(jsonIDList(record.ResumeQueue))
	if err != nil {
		return fmt.Errorf("subagentbatch: encode resume queue: %w", err)
	}
	parkedAt := record.ParkedAt
	if parkedAt.IsZero() {
		parkedAt = Now()
	}
	var decisionWindow interface{}
	if !record.DecisionWindowUntil.IsZero() {
		decisionWindow = record.DecisionWindowUntil.UTC().Format(time.RFC3339Nano)
	}

	db, err := s.dbc(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO turn_suspensions (
	session_id, turn_id, root_scope_id, obligation_ids_json, parked_at,
	decision_window_until, resume_queue_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, turn_id) DO UPDATE SET
	root_scope_id = excluded.root_scope_id,
	obligation_ids_json = excluded.obligation_ids_json,
	parked_at = excluded.parked_at,
	decision_window_until = excluded.decision_window_until,
	resume_queue_json = excluded.resume_queue_json,
	updated_at = excluded.updated_at
`, record.SessionID, record.TurnID, record.RootScopeID, string(obligations),
		parkedAt.UTC().Format(time.RFC3339Nano), decisionWindow, string(resumeQueue),
		Now().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("subagentbatch: park turn suspension: %w", err)
	}
	return nil
}

// GetTurnSuspension returns one parked-turn record. ok=false means the turn is
// not parked (never parked, already resumed, or the record was cleared).
func (s *sqliteBatchStore) GetTurnSuspension(ctx context.Context, sessionID, turnID string) (*TurnSuspension, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	turnID = strings.TrimSpace(turnID)
	if sessionID == "" || turnID == "" {
		return nil, false, nil
	}
	db, err := s.dbc(ctx)
	if err != nil {
		return nil, false, err
	}
	var (
		record        TurnSuspension
		obligations   string
		resumeQueue   string
		parkedAt      sql.NullString
		decisionUntil sql.NullString
	)
	err = db.QueryRowContext(ctx, `
SELECT session_id, turn_id, root_scope_id, obligation_ids_json, parked_at,
	decision_window_until, resume_queue_json
FROM turn_suspensions WHERE session_id = ? AND turn_id = ?`, sessionID, turnID).
		Scan(&record.SessionID, &record.TurnID, &record.RootScopeID, &obligations,
			&parkedAt, &decisionUntil, &resumeQueue)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("subagentbatch: read turn suspension: %w", err)
	}
	record.ObligationIDs = decodeJSONIDList(obligations)
	record.ResumeQueue = decodeJSONIDList(resumeQueue)
	record.ParkedAt = parseBatchTime(parkedAt.String)
	if until := parseNullableBatchTime(decisionUntil); until != nil {
		record.DecisionWindowUntil = *until
	}
	return &record, true, nil
}

// ClearTurnSuspension removes a parked-turn record once the turn resumes or is
// abandoned. Removing an absent record is not an error.
func (s *sqliteBatchStore) ClearTurnSuspension(ctx context.Context, sessionID, turnID string) error {
	sessionID = strings.TrimSpace(sessionID)
	turnID = strings.TrimSpace(turnID)
	if sessionID == "" || turnID == "" {
		return nil
	}
	db, err := s.dbc(ctx)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM turn_suspensions WHERE session_id = ? AND turn_id = ?`, sessionID, turnID); err != nil {
		return fmt.Errorf("subagentbatch: clear turn suspension: %w", err)
	}
	return nil
}

// jsonIDList keeps the persisted payload a JSON array even for nil input, so a
// record with no obligations/queue round-trips as [] and not as null.
func jsonIDList(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func decodeJSONIDList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	if len(values) == 0 {
		return nil
	}
	return values
}
