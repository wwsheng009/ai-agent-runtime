package chat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// insertTurnCaptureReceiptTx is called only after the canonical session update
// has been applied to the same SQLite transaction. The receipt therefore cannot
// exist without the terminal transcript snapshot it describes.
func (s *SQLiteSessionStorage) insertTurnCaptureReceiptTx(ctx context.Context, tx *sql.Tx, receipt TurnCaptureReceipt) (TurnCaptureReceipt, error) {
	if s == nil || tx == nil {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: canonical capture transaction is nil")
	}
	receipt.SessionID = strings.TrimSpace(receipt.SessionID)
	receipt.TurnID = strings.TrimSpace(receipt.TurnID)
	if receipt.SessionID == "" || receipt.TurnID == "" {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: capture receipt requires session and turn identity")
	}
	rows, err := tx.QueryContext(ctx, `
SELECT seq, sha256
FROM session_messages WHERE session_id = ? AND turn_id = ? ORDER BY seq ASC
`, receipt.SessionID, receipt.TurnID)
	if err != nil {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: read canonical range: %w", err)
	}
	defer rows.Close()
	type canonicalRow struct {
		seq    int
		digest string
	}
	rangeRows := make([]canonicalRow, 0, 8)
	for rows.Next() {
		var seq int
		var digest string
		if err := rows.Scan(&seq, &digest); err != nil {
			return TurnCaptureReceipt{}, fmt.Errorf("chat: scan canonical range: %w", err)
		}
		rangeRows = append(rangeRows, canonicalRow{seq: seq, digest: digest})
	}
	if err := rows.Err(); err != nil {
		return TurnCaptureReceipt{}, err
	}

	if len(rangeRows) == 0 {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: canonical turn range is unavailable for %s", receipt.TurnID)
	}
	start, end := rangeRows[0].seq, rangeRows[len(rangeRows)-1].seq
	if end-start+1 != len(rangeRows) {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: canonical turn range has a sequence gap for %s", receipt.TurnID)
	}
	if receipt.RangeHash == "" {
		hash := sha256.New()
		for _, row := range rangeRows {
			_, _ = fmt.Fprintf(hash, "%d\x00%s\x00", row.seq, row.digest)
		}
		receipt.RangeHash = hex.EncodeToString(hash.Sum(nil))
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM session_canonical_revisions WHERE session_id = ?`, receipt.SessionID).Scan(&revision); err == sql.ErrNoRows {
		revision = 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_canonical_revisions(session_id, revision) VALUES (?, ?)`, receipt.SessionID, revision); err != nil {
			return TurnCaptureReceipt{}, fmt.Errorf("chat: initialize canonical revision: %w", err)
		}
	} else if err != nil {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: read canonical revision: %w", err)
	}
	var sessionGeneration int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(session_generation, 0) FROM sessions
		WHERE id = ? AND COALESCE(purge_state, 'active') = 'active'
	`, receipt.SessionID).Scan(&sessionGeneration); err != nil {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: read session generation: %w", err)
	}
	if sessionGeneration <= 0 {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: canonical session generation is invalid")
	}
	existing, err := scanTurnCaptureReceipt(tx.QueryRowContext(ctx, `
 SELECT id, session_id, session_generation, turn_id, message_seq_start, message_seq_end,
       canonical_revision, range_hash, trace_id, user_message_id,
       assistant_message_id, outcome, steps, usage_json, state, created_at,
       COALESCE(materialized_at, ''), COALESCE(last_error, ''), attempt_count,
       COALESCE(retry_at, '')
FROM session_turn_capture_receipts
WHERE session_id = ? AND turn_id = ? AND canonical_revision = ? AND range_hash = ?
`, receipt.SessionID, receipt.TurnID, revision, receipt.RangeHash))
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: read existing capture receipt: %w", err)
	}
	receipt.ID = strings.TrimSpace(receipt.ID)
	if receipt.ID == "" {
		receipt.ID = "capture_" + uuid.NewString()
	}
	receipt.MessageSeqStart = int64(start)
	receipt.MessageSeqEnd = int64(end)
	receipt.CanonicalRevision = revision
	receipt.SessionGeneration = sessionGeneration
	if receipt.State == "" {
		receipt.State = "captured"
	}
	if receipt.Outcome == "" {
		receipt.Outcome = "success"
	}
	if receipt.UsageJSON == "" {
		receipt.UsageJSON = "{}"
	}
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `
 INSERT INTO session_turn_capture_receipts(
 id, session_id, session_generation, turn_id, message_seq_start, message_seq_end,
 canonical_revision, range_hash, trace_id, user_message_id,
 assistant_message_id, outcome, steps, usage_json, state, created_at,
 materialized_at, last_error, attempt_count, retry_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, NULL)
	`, receipt.ID, receipt.SessionID, receipt.SessionGeneration, receipt.TurnID, receipt.MessageSeqStart, receipt.MessageSeqEnd,
		receipt.CanonicalRevision, receipt.RangeHash, receipt.TraceID, receipt.UserMessageID,
		receipt.AssistantMessageID, receipt.Outcome, receipt.Steps, receipt.UsageJSON,
		receipt.State, receipt.CreatedAt.UTC().Format(time.RFC3339Nano), receipt.AttemptCount)
	if err != nil {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: insert capture receipt: %w", err)
	}
	return receipt, nil
}

func scanTurnCaptureReceipt(scanner interface{ Scan(...interface{}) error }) (TurnCaptureReceipt, error) {
	var receipt TurnCaptureReceipt
	var created, materialized, retry string
	if err := scanner.Scan(
		&receipt.ID, &receipt.SessionID, &receipt.SessionGeneration, &receipt.TurnID,
		&receipt.MessageSeqStart, &receipt.MessageSeqEnd, &receipt.CanonicalRevision,
		&receipt.RangeHash, &receipt.TraceID, &receipt.UserMessageID,
		&receipt.AssistantMessageID, &receipt.Outcome, &receipt.Steps, &receipt.UsageJSON,
		&receipt.State, &created, &materialized, &receipt.LastError,
		&receipt.AttemptCount, &retry,
	); err != nil {
		return TurnCaptureReceipt{}, err
	}
	receipt.CreatedAt = parseCaptureTime(created)
	receipt.MaterializedAt = parseCaptureTime(materialized)
	receipt.RetryAt = parseCaptureTime(retry)
	return receipt, nil
}

func parseCaptureTime(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func (s *SQLiteSessionStorage) ListTurnCaptureReceipts(ctx context.Context, limit int) ([]TurnCaptureReceipt, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, session_id, session_generation, turn_id, message_seq_start, message_seq_end,
       canonical_revision, range_hash, trace_id, user_message_id,
       assistant_message_id, outcome, steps, usage_json, state, created_at,
       COALESCE(materialized_at, ''), COALESCE(last_error, ''), attempt_count,
       COALESCE(retry_at, '')
FROM session_turn_capture_receipts
WHERE state IN ('captured', 'materializing', 'deferred')
  AND (retry_at IS NULL OR retry_at = '' OR retry_at <= ?)
ORDER BY created_at ASC LIMIT ?
`, time.Now().UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TurnCaptureReceipt, 0, limit)
	for rows.Next() {
		item, err := scanTurnCaptureReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteSessionStorage) ListTurnCaptureReceiptsForSession(ctx context.Context, sessionID string, limit int) ([]TurnCaptureReceipt, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, session_id, session_generation, turn_id, message_seq_start, message_seq_end,
       canonical_revision, range_hash, trace_id, user_message_id,
       assistant_message_id, outcome, steps, usage_json, state, created_at,
       COALESCE(materialized_at, ''), COALESCE(last_error, ''), attempt_count,
       COALESCE(retry_at, '')
FROM session_turn_capture_receipts
WHERE session_id = ? AND state IN ('captured', 'materializing', 'deferred')
  AND (retry_at IS NULL OR retry_at = '' OR retry_at <= ?)
ORDER BY created_at ASC LIMIT ?
`, strings.TrimSpace(sessionID), time.Now().UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TurnCaptureReceipt, 0, limit)
	for rows.Next() {
		item, err := scanTurnCaptureReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteSessionStorage) ListTurnCaptureReceiptsForSessionGeneration(
	ctx context.Context, sessionID string, generation int64, limit int,
) ([]TurnCaptureReceipt, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, session_id, session_generation, turn_id, message_seq_start, message_seq_end,
       canonical_revision, range_hash, trace_id, user_message_id,
       assistant_message_id, outcome, steps, usage_json, state, created_at,
       COALESCE(materialized_at, ''), COALESCE(last_error, ''), attempt_count,
       COALESCE(retry_at, '')
FROM session_turn_capture_receipts
WHERE session_id = ? AND session_generation = ?
  AND state IN ('captured', 'materializing', 'deferred')
  AND (retry_at IS NULL OR retry_at = '' OR retry_at <= ?)
ORDER BY created_at ASC LIMIT ?
`, strings.TrimSpace(sessionID), generation, time.Now().UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TurnCaptureReceipt, 0, limit)
	for rows.Next() {
		item, err := scanTurnCaptureReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteSessionStorage) ListMaterializedTurnCaptureReceipts(ctx context.Context, limit int) ([]TurnCaptureReceipt, error) {
	return s.ListMaterializedTurnCaptureReceiptsPage(ctx, nil, limit)
}

func (s *SQLiteSessionStorage) ListMaterializedTurnCaptureReceiptsPage(
	ctx context.Context,
	after *TurnCaptureReceipt,
	limit int,
) ([]TurnCaptureReceipt, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
SELECT id, session_id, session_generation, turn_id, message_seq_start, message_seq_end,
       canonical_revision, range_hash, trace_id, user_message_id,
       assistant_message_id, outcome, steps, usage_json, state, created_at,
       COALESCE(materialized_at, ''), COALESCE(last_error, ''), attempt_count,
       COALESCE(retry_at, '')
FROM session_turn_capture_receipts
WHERE state = 'materialized'
`
	args := make([]interface{}, 0, 6)
	if after != nil && after.ID != "" {
		query += ` AND (
    COALESCE(materialized_at, '') > ? OR
    (COALESCE(materialized_at, '') = ? AND (
      created_at > ? OR (created_at = ? AND id > ?)
    ))
 )`
		materializedAt := captureReceiptTimeKey(after.MaterializedAt)
		createdAt := captureReceiptTimeKey(after.CreatedAt)
		args = append(args, materializedAt, materializedAt, createdAt, createdAt, after.ID)
	}
	query += ` ORDER BY COALESCE(materialized_at, '') ASC, created_at ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TurnCaptureReceipt, 0, limit)
	for rows.Next() {
		item, err := scanTurnCaptureReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteSessionStorage) ListMaterializedTurnCaptureReceiptsForSession(ctx context.Context, sessionID string, limit int) ([]TurnCaptureReceipt, error) {
	return s.ListMaterializedTurnCaptureReceiptsForSessionPage(ctx, sessionID, nil, limit)
}

func (s *SQLiteSessionStorage) ListMaterializedTurnCaptureReceiptsForSessionPage(
	ctx context.Context,
	sessionID string,
	after *TurnCaptureReceipt,
	limit int,
) ([]TurnCaptureReceipt, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
SELECT id, session_id, session_generation, turn_id, message_seq_start, message_seq_end,
       canonical_revision, range_hash, trace_id, user_message_id,
       assistant_message_id, outcome, steps, usage_json, state, created_at,
       COALESCE(materialized_at, ''), COALESCE(last_error, ''), attempt_count,
       COALESCE(retry_at, '')
FROM session_turn_capture_receipts
WHERE session_id = ? AND state = 'materialized'
`
	args := []interface{}{strings.TrimSpace(sessionID)}
	if after != nil && after.ID != "" {
		query += ` AND (
    COALESCE(materialized_at, '') > ? OR
    (COALESCE(materialized_at, '') = ? AND (
      created_at > ? OR (created_at = ? AND id > ?)
    ))
 )`
		materializedAt := captureReceiptTimeKey(after.MaterializedAt)
		createdAt := captureReceiptTimeKey(after.CreatedAt)
		args = append(args, materializedAt, materializedAt, createdAt, createdAt, after.ID)
	}
	query += ` ORDER BY COALESCE(materialized_at, '') ASC, created_at ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TurnCaptureReceipt, 0, limit)
	for rows.Next() {
		item, err := scanTurnCaptureReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteSessionStorage) ListMaterializedTurnCaptureReceiptsForSessionGenerationPage(
	ctx context.Context,
	sessionID string,
	generation int64,
	after *TurnCaptureReceipt,
	limit int,
) ([]TurnCaptureReceipt, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
SELECT id, session_id, session_generation, turn_id, message_seq_start, message_seq_end,
       canonical_revision, range_hash, trace_id, user_message_id,
       assistant_message_id, outcome, steps, usage_json, state, created_at,
       COALESCE(materialized_at, ''), COALESCE(last_error, ''), attempt_count,
       COALESCE(retry_at, '')
FROM session_turn_capture_receipts
WHERE session_id = ? AND session_generation = ? AND state = 'materialized'
`
	args := []interface{}{strings.TrimSpace(sessionID), generation}
	if after != nil && after.ID != "" {
		query += ` AND (
    COALESCE(materialized_at, '') > ? OR
    (COALESCE(materialized_at, '') = ? AND (
      created_at > ? OR (created_at = ? AND id > ?)
    ))
  )`
		materializedAt := captureReceiptTimeKey(after.MaterializedAt)
		createdAt := captureReceiptTimeKey(after.CreatedAt)
		args = append(args, materializedAt, materializedAt, createdAt, createdAt, after.ID)
	}
	query += ` ORDER BY COALESCE(materialized_at, '') ASC, created_at ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TurnCaptureReceipt, 0, limit)
	for rows.Next() {
		item, err := scanTurnCaptureReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteSessionStorage) HasTurnCaptureReceiptsForSession(ctx context.Context, sessionID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM session_turn_capture_receipts WHERE session_id = ?)
`, strings.TrimSpace(sessionID)).Scan(&exists)
	return exists, err
}

func (s *SQLiteSessionStorage) HasTurnCaptureReceiptsForSessionGeneration(
	ctx context.Context, sessionID string, generation int64,
) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM session_turn_capture_receipts
  WHERE session_id = ? AND session_generation = ?
)
`, strings.TrimSpace(sessionID), generation).Scan(&exists)
	return exists, err
}

func (s *SQLiteSessionStorage) GetTurnCaptureReceipt(ctx context.Context, id string) (TurnCaptureReceipt, error) {
	return scanTurnCaptureReceipt(s.db.QueryRowContext(ctx, `
SELECT id, session_id, session_generation, turn_id, message_seq_start, message_seq_end,
       canonical_revision, range_hash, trace_id, user_message_id,
       assistant_message_id, outcome, steps, usage_json, state, created_at,
       COALESCE(materialized_at, ''), COALESCE(last_error, ''), attempt_count,
       COALESCE(retry_at, '')
FROM session_turn_capture_receipts WHERE id = ?
	`, strings.TrimSpace(id)))
}

// ValidateTurnCaptureReceipt fails closed if a receipt no longer describes the
// current canonical revision and exact contiguous range.
func (s *SQLiteSessionStorage) ValidateTurnCaptureReceipt(ctx context.Context, receipt TurnCaptureReceipt) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("chat: canonical session storage is closed")
	}
	var revision int64
	if err := s.db.QueryRowContext(ctx, `
SELECT revision FROM session_canonical_revisions WHERE session_id = ?
`, strings.TrimSpace(receipt.SessionID)).Scan(&revision); err != nil {
		return fmt.Errorf("chat: read canonical revision: %w", err)
	}
	if revision != receipt.CanonicalRevision {
		return fmt.Errorf("chat: capture receipt %s invalidated by canonical revision %d (expected %d)",
			receipt.ID, revision, receipt.CanonicalRevision)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT seq, sha256, turn_id
FROM session_messages
WHERE session_id = ? AND seq >= ? AND seq <= ?
ORDER BY seq ASC
`, strings.TrimSpace(receipt.SessionID), receipt.MessageSeqStart, receipt.MessageSeqEnd)
	if err != nil {
		return fmt.Errorf("chat: read receipt range: %w", err)
	}
	defer rows.Close()
	hash := sha256.New()
	count := int64(0)
	for rows.Next() {
		var seq int64
		var digest, turnID string
		if err := rows.Scan(&seq, &digest, &turnID); err != nil {
			return err
		}
		if strings.TrimSpace(turnID) != strings.TrimSpace(receipt.TurnID) {
			return fmt.Errorf("chat: capture receipt %s range contains a different turn", receipt.ID)
		}
		_, _ = fmt.Fprintf(hash, "%d\x00%s\x00", seq, digest)
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != receipt.MessageSeqEnd-receipt.MessageSeqStart+1 {
		return fmt.Errorf("chat: capture receipt %s canonical range is incomplete", receipt.ID)
	}
	if hex.EncodeToString(hash.Sum(nil)) != receipt.RangeHash {
		return fmt.Errorf("chat: capture receipt %s canonical range hash mismatch", receipt.ID)
	}
	return nil
}

func (s *SQLiteSessionStorage) MarkTurnCaptureReceiptMaterialized(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
UPDATE session_turn_capture_receipts
SET state = 'materialized', materialized_at = ?, last_error = NULL, retry_at = NULL
WHERE id = ? AND state IN ('captured', 'materializing', 'deferred')
`, now, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("chat: capture receipt %s was not materialized", strings.TrimSpace(id))
	}
	return nil
}

func (s *SQLiteSessionStorage) DeferTurnCaptureReceipt(ctx context.Context, id, reason string, retryAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE session_turn_capture_receipts
SET state = 'deferred', last_error = ?, retry_at = ?, attempt_count = attempt_count + 1
WHERE id = ? AND state IN ('captured', 'materializing', 'deferred')
`, strings.TrimSpace(reason), nullableCaptureTime(retryAt), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("chat: capture receipt %s was not deferred", strings.TrimSpace(id))
	}
	return nil
}

func nullableCaptureTime(value time.Time) interface{} {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// The in-memory backend is used by actor/unit tests. It implements the same
// receipt contract so tests exercise the handoff rather than silently falling
// back to a best-effort digest.
func (s *InMemoryStorage) SaveWithTurnCapture(ctx context.Context, session *Session, receipt TurnCaptureReceipt) (TurnCaptureReceipt, error) {
	return s.commitInMemoryTurnCapture(ctx, session, receipt, false)
}

func (s *InMemoryStorage) UpdateWithTurnCapture(ctx context.Context, session *Session, receipt TurnCaptureReceipt) (TurnCaptureReceipt, error) {
	return s.commitInMemoryTurnCapture(ctx, session, receipt, true)
}

func (s *InMemoryStorage) commitInMemoryTurnCapture(ctx context.Context, session *Session, receipt TurnCaptureReceipt, requireExisting bool) (TurnCaptureReceipt, error) {
	if session == nil {
		return TurnCaptureReceipt{}, ErrInvalidSession
	}
	if err := ctx.Err(); err != nil {
		return TurnCaptureReceipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[string]*Session)
	}
	if s.receipts == nil {
		s.receipts = make(map[string]TurnCaptureReceipt)
	}
	if requireExisting {
		if _, ok := s.sessions[session.ID]; !ok {
			return TurnCaptureReceipt{}, ErrSessionNotFound
		}
	}
	if session.ID == "" {
		session.ID = generateSessionID()
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}
	if !session.PreserveUpdatedAt {
		session.UpdatedAt = time.Now().UTC()
	}
	stored := session.Clone()
	if stored == nil {
		return TurnCaptureReceipt{}, ErrInvalidSession
	}
	if old, ok := s.receiptForTurnLocked(session.ID, receipt.TurnID); ok {
		return old, nil
	}
	committed, err := captureReceiptFromSession(stored, receipt, len(s.receiptsForSessionLocked(session.ID))+1)
	if err != nil {
		return TurnCaptureReceipt{}, err
	}
	s.sessions[stored.ID] = stored
	s.updateTagIndex(stored)
	s.receipts[committed.ID] = committed
	return committed, nil
}

func (s *InMemoryStorage) receiptsForSessionLocked(sessionID string) []TurnCaptureReceipt {
	out := make([]TurnCaptureReceipt, 0)
	for _, receipt := range s.receipts {
		if receipt.SessionID == sessionID {
			out = append(out, receipt)
		}
	}
	return out
}

func (s *InMemoryStorage) receiptForTurnLocked(sessionID, turnID string) (TurnCaptureReceipt, bool) {
	for _, receipt := range s.receipts {
		if receipt.SessionID == sessionID && receipt.TurnID == turnID {
			return receipt, true
		}
	}
	return TurnCaptureReceipt{}, false
}

func (s *InMemoryStorage) ListTurnCaptureReceipts(ctx context.Context, limit int) ([]TurnCaptureReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TurnCaptureReceipt, 0, limit)
	for _, receipt := range s.receipts {
		if receipt.State != "captured" && receipt.State != "materializing" && receipt.State != "deferred" {
			continue
		}
		if !receipt.RetryAt.IsZero() && receipt.RetryAt.After(time.Now()) {
			continue
		}
		out = append(out, receipt)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *InMemoryStorage) ListTurnCaptureReceiptsForSession(ctx context.Context, sessionID string, limit int) ([]TurnCaptureReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	sessionID = strings.TrimSpace(sessionID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TurnCaptureReceipt, 0, limit)
	for _, receipt := range s.receipts {
		if receipt.SessionID != sessionID ||
			(receipt.State != "captured" && receipt.State != "materializing" && receipt.State != "deferred") ||
			(!receipt.RetryAt.IsZero() && receipt.RetryAt.After(time.Now())) {
			continue
		}
		out = append(out, receipt)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *InMemoryStorage) ListTurnCaptureReceiptsForSessionGeneration(
	ctx context.Context, sessionID string, generation int64, limit int,
) ([]TurnCaptureReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	sessionID = strings.TrimSpace(sessionID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TurnCaptureReceipt, 0, limit)
	for _, receipt := range s.receipts {
		if receipt.SessionID != sessionID || receipt.SessionGeneration != generation ||
			(receipt.State != "captured" && receipt.State != "materializing" && receipt.State != "deferred") ||
			(!receipt.RetryAt.IsZero() && receipt.RetryAt.After(time.Now())) {
			continue
		}
		out = append(out, receipt)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *InMemoryStorage) ListMaterializedTurnCaptureReceipts(ctx context.Context, limit int) ([]TurnCaptureReceipt, error) {
	return s.ListMaterializedTurnCaptureReceiptsPage(ctx, nil, limit)
}

func (s *InMemoryStorage) ListMaterializedTurnCaptureReceiptsPage(
	ctx context.Context,
	after *TurnCaptureReceipt,
	limit int,
) ([]TurnCaptureReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TurnCaptureReceipt, 0, limit)
	for _, receipt := range s.receipts {
		if receipt.State != "materialized" {
			continue
		}
		if after != nil && after.ID != "" && !captureReceiptAfter(receipt, *after) {
			continue
		}
		out = append(out, receipt)
	}
	sort.Slice(out, func(i, j int) bool {
		return captureReceiptBefore(out[i], out[j])
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *InMemoryStorage) ListMaterializedTurnCaptureReceiptsForSession(ctx context.Context, sessionID string, limit int) ([]TurnCaptureReceipt, error) {
	return s.ListMaterializedTurnCaptureReceiptsForSessionPage(ctx, sessionID, nil, limit)
}

func (s *InMemoryStorage) ListMaterializedTurnCaptureReceiptsForSessionPage(
	ctx context.Context,
	sessionID string,
	after *TurnCaptureReceipt,
	limit int,
) ([]TurnCaptureReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	sessionID = strings.TrimSpace(sessionID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TurnCaptureReceipt, 0, limit)
	for _, receipt := range s.receipts {
		if receipt.SessionID != sessionID || receipt.State != "materialized" {
			continue
		}
		if after != nil && after.ID != "" && !captureReceiptAfter(receipt, *after) {
			continue
		}
		out = append(out, receipt)
	}
	sort.Slice(out, func(i, j int) bool {
		return captureReceiptBefore(out[i], out[j])
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *InMemoryStorage) ListMaterializedTurnCaptureReceiptsForSessionGenerationPage(
	ctx context.Context,
	sessionID string,
	generation int64,
	after *TurnCaptureReceipt,
	limit int,
) ([]TurnCaptureReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	sessionID = strings.TrimSpace(sessionID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TurnCaptureReceipt, 0, limit)
	for _, receipt := range s.receipts {
		if receipt.SessionID != sessionID || receipt.SessionGeneration != generation || receipt.State != "materialized" {
			continue
		}
		if after != nil && after.ID != "" && !captureReceiptAfter(receipt, *after) {
			continue
		}
		out = append(out, receipt)
	}
	sort.Slice(out, func(i, j int) bool {
		return captureReceiptBefore(out[i], out[j])
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func captureReceiptBefore(left, right TurnCaptureReceipt) bool {
	if !left.MaterializedAt.Equal(right.MaterializedAt) {
		return left.MaterializedAt.Before(right.MaterializedAt)
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.ID < right.ID
}

func captureReceiptAfter(receipt, cursor TurnCaptureReceipt) bool {
	return captureReceiptBefore(cursor, receipt)
}

func captureReceiptTimeKey(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (s *InMemoryStorage) HasTurnCaptureReceiptsForSession(ctx context.Context, sessionID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.receiptsForSessionLocked(strings.TrimSpace(sessionID))) > 0, nil
}

func (s *InMemoryStorage) HasTurnCaptureReceiptsForSessionGeneration(
	ctx context.Context, sessionID string, generation int64,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	sessionID = strings.TrimSpace(sessionID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, receipt := range s.receipts {
		if receipt.SessionID == sessionID && receipt.SessionGeneration == generation {
			return true, nil
		}
	}
	return false, nil
}

func (s *InMemoryStorage) GetTurnCaptureReceipt(ctx context.Context, id string) (TurnCaptureReceipt, error) {
	if err := ctx.Err(); err != nil {
		return TurnCaptureReceipt{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	receipt, ok := s.receipts[strings.TrimSpace(id)]
	if !ok {
		return TurnCaptureReceipt{}, sql.ErrNoRows
	}
	return receipt, nil
}

func (s *InMemoryStorage) ValidateTurnCaptureReceipt(ctx context.Context, receipt TurnCaptureReceipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	session := s.sessions[strings.TrimSpace(receipt.SessionID)]
	if session == nil {
		return fmt.Errorf("chat: capture receipt session is missing")
	}
	current, ok := s.receipts[strings.TrimSpace(receipt.ID)]
	if !ok || current.CanonicalRevision != receipt.CanonicalRevision ||
		current.RangeHash != receipt.RangeHash || current.TurnID != receipt.TurnID {
		return fmt.Errorf("chat: capture receipt is invalid or changed")
	}
	computed, err := captureReceiptFromSession(session, current, int(current.CanonicalRevision))
	if err != nil {
		return err
	}
	if computed.MessageSeqStart != receipt.MessageSeqStart ||
		computed.MessageSeqEnd != receipt.MessageSeqEnd || computed.RangeHash != receipt.RangeHash {
		return fmt.Errorf("chat: capture receipt canonical range mismatch")
	}
	return nil
}

func (s *InMemoryStorage) MarkTurnCaptureReceiptMaterialized(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.receipts[strings.TrimSpace(id)]
	if !ok {
		return sql.ErrNoRows
	}
	receipt.State = "materialized"
	receipt.MaterializedAt = time.Now().UTC()
	receipt.LastError = ""
	receipt.RetryAt = time.Time{}
	s.receipts[receipt.ID] = receipt
	return nil
}

func (s *InMemoryStorage) DeferTurnCaptureReceipt(ctx context.Context, id, reason string, retryAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.receipts[strings.TrimSpace(id)]
	if !ok {
		return sql.ErrNoRows
	}
	receipt.State = "deferred"
	receipt.LastError = strings.TrimSpace(reason)
	receipt.RetryAt = retryAt
	receipt.AttemptCount++
	s.receipts[receipt.ID] = receipt
	return nil
}

func captureReceiptFromSession(session *Session, receipt TurnCaptureReceipt, revision int) (TurnCaptureReceipt, error) {
	if session == nil || strings.TrimSpace(session.ID) == "" || strings.TrimSpace(receipt.TurnID) == "" {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: invalid in-memory capture receipt")
	}
	start, end := 0, 0
	for index, message := range session.History {
		if strings.TrimSpace(types.TurnID(message)) != strings.TrimSpace(receipt.TurnID) {
			continue
		}
		if start == 0 {
			start = index + 1
		}
		end = index + 1
	}
	if end == 0 {
		end = int(receipt.MessageSeqEnd)
		if end <= 0 {
			end = len(session.History)
		}
		start = end
	}
	if start <= 0 || end < start {
		return TurnCaptureReceipt{}, fmt.Errorf("chat: in-memory canonical turn range is unavailable")
	}
	hash := sha256.New()
	for index := start - 1; index < end && index < len(session.History); index++ {
		payload, _ := json.Marshal(session.History[index])
		_, _ = hash.Write(payload)
	}
	receipt.ID = strings.TrimSpace(receipt.ID)
	if receipt.ID == "" {
		receipt.ID = "capture_" + uuid.NewString()
	}
	receipt.SessionID = session.ID
	receipt.MessageSeqStart = int64(start)
	receipt.MessageSeqEnd = int64(end)
	receipt.CanonicalRevision = int64(revision)
	receipt.RangeHash = hex.EncodeToString(hash.Sum(nil))
	if receipt.State == "" {
		receipt.State = "captured"
	}
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	}
	return receipt, nil
}

func ensureCanonicalRevisionTx(ctx context.Context, tx *sql.Tx, sessionID string) error {
	var revision int
	err := tx.QueryRowContext(ctx, `SELECT revision FROM session_canonical_revisions WHERE session_id = ?`, sessionID).Scan(&revision)
	if err == sql.ErrNoRows {
		_, err = tx.ExecContext(ctx, `INSERT INTO session_canonical_revisions(session_id, revision) VALUES (?, 1)`, sessionID)
	}
	return err
}
