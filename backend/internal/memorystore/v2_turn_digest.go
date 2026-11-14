package memorystore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TurnDigestStatus is the lifecycle of a runtime turn digest row.
type TurnDigestStatus string

const (
	TurnDigestPending     TurnDigestStatus = "pending"
	TurnDigestProcessing  TurnDigestStatus = "processing"
	TurnDigestDone        TurnDigestStatus = "done"
	TurnDigestFailed      TurnDigestStatus = "failed"
	TurnDigestSkipped     TurnDigestStatus = "skipped"
	TurnDigestDeferred    TurnDigestStatus = "deferred"
	TurnDigestInvalidated TurnDigestStatus = "invalidated"
)

// TurnDigestRollupStatus tracks whether an L0 digest has been folded into an
// L1 window summary. Extraction status and rollup status are deliberately
// independent: a done digest remains unprunable until rollup finishes.
type TurnDigestRollupStatus string

const (
	TurnDigestRollupPending TurnDigestRollupStatus = "pending"
	TurnDigestRollupDone    TurnDigestRollupStatus = "done"
	TurnDigestRollupFailed  TurnDigestRollupStatus = "failed"
	TurnDigestRollupSkipped TurnDigestRollupStatus = "skipped"
)

// TurnDigest is the durable runtime outbox row for one completed turn.
//
// Phase 1 keeps canonical transcript as the memory extraction source of truth.
// This row only records that a turn is waiting for extraction and carries
// bounded, redacted text/message references for recovery and diagnostics.
type TurnDigest struct {
	DigestSeq          int64
	ID                 string
	Scope              Scope
	ProjectKey         string
	SessionID          string
	TurnID             string
	TraceID            string
	ParentSessionID    string
	AgentType          string
	UserMessageID      string
	AssistantMessageID string
	MessageSeqEnd      int64
	UserText           string
	AssistantText      string
	TextTruncated      bool
	Outcome            string
	Steps              int
	UsageJSON          string
	MetadataJSON       string
	DigestHash         string
	DigestRevision     int64
	Status             TurnDigestStatus
	ProcessJobID       string
	LastError          string
	RollupStatus       TurnDigestRollupStatus
	RollupWindowID     string
	RolledUpAt         time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ProcessedAt        time.Time
}

// TurnDigestSession summarizes pending digests for one session.
type TurnDigestSession struct {
	ProjectKey string
	SessionID  string
	Pending    int
	MinSeq     int64
	MaxSeq     int64
}

// RecordTurnDigest inserts or updates a turn digest. A changed digest hash
// resets the row to pending; identical content preserves done/processing/
// invalidated state so retries do not re-open a completed turn.
func (s *Store) RecordTurnDigest(ctx context.Context, digest TurnDigest) (TurnDigest, error) {
	if s == nil {
		return TurnDigest{}, fmt.Errorf("memorystore: store is nil")
	}
	db, err := s.ensureV2()
	if err != nil {
		return TurnDigest{}, err
	}
	now := time.Now().UTC()
	if digest.Scope == "" {
		digest.Scope = ScopeProject
	}
	if strings.TrimSpace(digest.ProjectKey) == "" {
		digest.ProjectKey = s.cfg.ProjectKey
	}
	digest.ProjectKey = strings.TrimSpace(digest.ProjectKey)
	digest.SessionID = strings.TrimSpace(digest.SessionID)
	digest.TurnID = strings.TrimSpace(digest.TurnID)
	if digest.ProjectKey == "" || digest.SessionID == "" || digest.TurnID == "" {
		return TurnDigest{}, fmt.Errorf("memorystore: turn digest requires project_key, session_id and turn_id")
	}
	if digest.ID == "" {
		digest.ID = "td_" + uuid.NewString()
	}
	if digest.Status == "" {
		digest.Status = TurnDigestPending
	}
	if digest.RollupStatus == "" {
		digest.RollupStatus = TurnDigestRollupPending
	}
	if digest.Outcome == "" {
		digest.Outcome = "success"
	}
	if digest.CreatedAt.IsZero() {
		digest.CreatedAt = now
	}
	if digest.UpdatedAt.IsZero() {
		digest.UpdatedAt = now
	}
	if digest.DigestHash == "" {
		digest.DigestHash = TurnDigestHash(digest)
	}
	if digest.DigestRevision <= 0 {
		digest.DigestRevision = 1
	}
	if digest.UsageJSON == "" {
		digest.UsageJSON = "{}"
	}
	if digest.MetadataJSON == "" {
		digest.MetadataJSON = "{}"
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO memory_turn_digests (
    id, scope, project_key, session_id, turn_id, trace_id,
    parent_session_id, agent_type, user_message_id, assistant_message_id, message_seq_end,
    user_text, assistant_text, text_truncated, outcome, steps,
    usage_json, metadata_json, digest_hash, digest_revision, status,
    process_job_id, last_error, rollup_status, rollup_window_id, rolled_up_at,
    created_at, updated_at, processed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(project_key, session_id, turn_id) DO UPDATE SET
    trace_id = excluded.trace_id,
    parent_session_id = excluded.parent_session_id,
    agent_type = excluded.agent_type,
    user_message_id = excluded.user_message_id,
    assistant_message_id = excluded.assistant_message_id,
    message_seq_end = excluded.message_seq_end,
    user_text = excluded.user_text,
    assistant_text = excluded.assistant_text,
    text_truncated = excluded.text_truncated,
    outcome = excluded.outcome,
    steps = excluded.steps,
    usage_json = excluded.usage_json,
    metadata_json = excluded.metadata_json,
    digest_hash = excluded.digest_hash,
    digest_revision = excluded.digest_revision,
    status = CASE
        WHEN memory_turn_digests.digest_hash = excluded.digest_hash
          AND memory_turn_digests.status IN ('done', 'processing', 'invalidated', 'deferred')
        THEN memory_turn_digests.status
        ELSE 'pending'
    END,
    process_job_id = CASE
        WHEN memory_turn_digests.digest_hash = excluded.digest_hash
          AND memory_turn_digests.status IN ('done', 'processing', 'deferred')
        THEN memory_turn_digests.process_job_id
        ELSE NULL
    END,
    last_error = CASE
        WHEN memory_turn_digests.digest_hash = excluded.digest_hash
          AND memory_turn_digests.status IN ('done', 'processing', 'invalidated', 'deferred')
        THEN memory_turn_digests.last_error
        ELSE NULL
    END,
    processed_at = CASE
        WHEN memory_turn_digests.digest_hash = excluded.digest_hash
          AND memory_turn_digests.status IN ('done', 'invalidated')
        THEN memory_turn_digests.processed_at
        ELSE NULL
    END,
    rollup_status = CASE
        WHEN memory_turn_digests.digest_hash = excluded.digest_hash
          AND memory_turn_digests.status IN ('done', 'processing', 'deferred', 'invalidated')
        THEN memory_turn_digests.rollup_status
        ELSE 'pending'
    END,
    rollup_window_id = CASE
        WHEN memory_turn_digests.digest_hash = excluded.digest_hash
          AND memory_turn_digests.status IN ('done', 'processing', 'deferred', 'invalidated')
        THEN memory_turn_digests.rollup_window_id
        ELSE NULL
    END,
    rolled_up_at = CASE
        WHEN memory_turn_digests.digest_hash = excluded.digest_hash
          AND memory_turn_digests.status IN ('done', 'processing', 'deferred', 'invalidated')
        THEN memory_turn_digests.rolled_up_at
        ELSE NULL
    END,
    updated_at = excluded.updated_at
`, digest.ID, string(digest.Scope), digest.ProjectKey, digest.SessionID, digest.TurnID,
		nullableString(digest.TraceID), nullableString(digest.ParentSessionID),
		nullableString(digest.AgentType), nullableString(digest.UserMessageID),
		nullableString(digest.AssistantMessageID), digest.MessageSeqEnd, digest.UserText, digest.AssistantText,
		boolInt(digest.TextTruncated), digest.Outcome, digest.Steps, digest.UsageJSON,
		digest.MetadataJSON, digest.DigestHash, digest.DigestRevision, string(digest.Status),
		nullableString(digest.ProcessJobID), nullableString(digest.LastError),
		string(digest.RollupStatus), nullableString(digest.RollupWindowID),
		nullableTime(digest.RolledUpAt),
		digest.CreatedAt.UTC().Format(time.RFC3339Nano), digest.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return TurnDigest{}, fmt.Errorf("memorystore: record turn digest: %w", err)
	}
	return s.getTurnDigestByTurn(ctx, digest.ProjectKey, digest.SessionID, digest.TurnID)
}

// TurnDigestHash computes a stable hash for a digest row.
func TurnDigestHash(digest TurnDigest) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(digest.ProjectKey),
		strings.TrimSpace(digest.SessionID),
		strings.TrimSpace(digest.TurnID),
		strings.TrimSpace(digest.UserMessageID),
		strings.TrimSpace(digest.AssistantMessageID),
		strings.TrimSpace(digest.Outcome),
		fmt.Sprintf("%d", digest.MessageSeqEnd),
		digest.UserText,
		digest.AssistantText,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (s *Store) getTurnDigestByTurn(ctx context.Context, projectKey, sessionID, turnID string) (TurnDigest, error) {
	db, err := s.ensureV2()
	if err != nil {
		return TurnDigest{}, err
	}
	row := db.QueryRowContext(ctx, `
SELECT digest_seq, id, scope, project_key, session_id, turn_id,
       COALESCE(trace_id, ''), COALESCE(parent_session_id, ''), COALESCE(agent_type, ''),
       COALESCE(user_message_id, ''), COALESCE(assistant_message_id, ''), message_seq_end,
       user_text, assistant_text, text_truncated, outcome, steps,
       usage_json, metadata_json, digest_hash, digest_revision, status,
       COALESCE(process_job_id, ''), COALESCE(last_error, ''),
       rollup_status, COALESCE(rollup_window_id, ''),
       COALESCE(rolled_up_at, ''), created_at, updated_at, COALESCE(processed_at, '')
FROM memory_turn_digests
WHERE project_key = ? AND session_id = ? AND turn_id = ?
`, projectKey, sessionID, turnID)
	return scanTurnDigest(row)
}

// GetTurnDigestByTurn returns the current digest state for diagnostics and
// event-handler tests.
func (s *Store) GetTurnDigestByTurn(ctx context.Context, sessionID, turnID string) (TurnDigest, error) {
	return s.getTurnDigestByTurn(ctx, s.cfg.ProjectKey, strings.TrimSpace(sessionID), strings.TrimSpace(turnID))
}

// ListPendingTurnDigestSessions returns sessions with pending digest rows,
// oldest digest first.
func (s *Store) ListPendingTurnDigestSessions(ctx context.Context, projectKey string, limit int) ([]TurnDigestSession, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(projectKey) == "" {
		projectKey = s.cfg.ProjectKey
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `
SELECT project_key, session_id, COUNT(1), MIN(digest_seq), MAX(digest_seq)
FROM memory_turn_digests
WHERE project_key = ? AND status = 'pending'
GROUP BY project_key, session_id
ORDER BY MIN(digest_seq) ASC
LIMIT ?
`, strings.TrimSpace(projectKey), limit)
	if err != nil {
		return nil, fmt.Errorf("memorystore: list pending turn digest sessions: %w", err)
	}
	defer rows.Close()
	out := make([]TurnDigestSession, 0, limit)
	for rows.Next() {
		var item TurnDigestSession
		if err := rows.Scan(&item.ProjectKey, &item.SessionID, &item.Pending, &item.MinSeq, &item.MaxSeq); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListPendingTurnDigests returns pending rows for one session in seq order.
func (s *Store) ListPendingTurnDigests(ctx context.Context, sessionID string, limit int) ([]TurnDigest, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := db.QueryContext(ctx, `
SELECT digest_seq, id, scope, project_key, session_id, turn_id,
       COALESCE(trace_id, ''), COALESCE(parent_session_id, ''), COALESCE(agent_type, ''),
       COALESCE(user_message_id, ''), COALESCE(assistant_message_id, ''), message_seq_end,
       user_text, assistant_text, text_truncated, outcome, steps,
       usage_json, metadata_json, digest_hash, digest_revision, status,
       COALESCE(process_job_id, ''), COALESCE(last_error, ''),
       rollup_status, COALESCE(rollup_window_id, ''),
       COALESCE(rolled_up_at, ''), created_at, updated_at, COALESCE(processed_at, '')
FROM memory_turn_digests
WHERE project_key = ? AND session_id = ? AND status = 'pending'
ORDER BY digest_seq ASC
LIMIT ?
`, s.cfg.ProjectKey, strings.TrimSpace(sessionID), limit)
	if err != nil {
		return nil, fmt.Errorf("memorystore: list pending turn digests: %w", err)
	}
	defer rows.Close()
	out := make([]TurnDigest, 0, limit)
	for rows.Next() {
		item, scanErr := scanTurnDigest(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// PreviousTurnDigestMessageSeqEnd returns the highest canonical message seq
// recorded by an earlier, non-invalidated digest for the same session. Pending
// and deferred rows still delimit valid canonical coverage; invalidated rows
// belong to a removed branch and must not advance the next extraction window.
func (s *Store) PreviousTurnDigestMessageSeqEnd(ctx context.Context, sessionID string, beforeDigestSeq int64) (int64, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	var seq int64
	err = db.QueryRowContext(ctx, `
SELECT COALESCE(MAX(message_seq_end), 0)
FROM memory_turn_digests
WHERE project_key = ? AND session_id = ? AND digest_seq < ? AND message_seq_end > 0
  AND status != 'invalidated'
`, s.cfg.ProjectKey, strings.TrimSpace(sessionID), beforeDigestSeq).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("memorystore: previous turn digest seq: %w", err)
	}
	return seq, nil
}

// MarkTurnDigestsProcessed marks pending rows up to maxSeq as done.
func (s *Store) MarkTurnDigestsProcessed(ctx context.Context, sessionID string, maxSeq int64, jobID string) (int64, error) {
	return s.updateTurnDigestStatus(ctx, sessionID, maxSeq, TurnDigestDone, jobID, "")
}

// MarkTurnDigestsSkipped marks rows as skipped (e.g. oversized session waiting
// for the Phase 1.5 window reader).
func (s *Store) MarkTurnDigestsSkipped(ctx context.Context, sessionID string, maxSeq int64, jobID, reason string) (int64, error) {
	return s.updateTurnDigestStatus(ctx, sessionID, maxSeq, TurnDigestSkipped, jobID, reason)
}

// MarkTurnDigestsDeferred keeps rows for the Phase 1.5 windowed reader without
// retrying a known-oversized session on every sweep.
func (s *Store) MarkTurnDigestsDeferred(ctx context.Context, sessionID string, maxSeq int64, jobID, reason string) (int64, error) {
	return s.updateTurnDigestStatus(ctx, sessionID, maxSeq, TurnDigestDeferred, jobID, reason)
}

// MarkTurnDigestsFailed marks rows as failed for operator visibility.
func (s *Store) MarkTurnDigestsFailed(ctx context.Context, sessionID string, maxSeq int64, jobID, reason string) (int64, error) {
	return s.updateTurnDigestStatus(ctx, sessionID, maxSeq, TurnDigestFailed, jobID, reason)
}

// ListTurnDigestsForRollup returns extraction-done rows that still need an L1
// window summary. Rows with rollup skipped already have no required rollup.
func (s *Store) ListTurnDigestsForRollup(ctx context.Context, sessionID string, limit int) ([]TurnDigest, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := db.QueryContext(ctx, `
SELECT digest_seq, id, scope, project_key, session_id, turn_id,
       COALESCE(trace_id, ''), COALESCE(parent_session_id, ''), COALESCE(agent_type, ''),
       COALESCE(user_message_id, ''), COALESCE(assistant_message_id, ''), message_seq_end,
       user_text, assistant_text, text_truncated, outcome, steps,
       usage_json, metadata_json, digest_hash, digest_revision, status,
       COALESCE(process_job_id, ''), COALESCE(last_error, ''),
       rollup_status, COALESCE(rollup_window_id, ''),
       COALESCE(rolled_up_at, ''), created_at, updated_at, COALESCE(processed_at, '')
FROM memory_turn_digests
WHERE project_key = ? AND session_id = ? AND status = 'done'
  AND rollup_status IN ('pending', 'failed')
ORDER BY digest_seq ASC
LIMIT ?
`, s.cfg.ProjectKey, strings.TrimSpace(sessionID), limit)
	if err != nil {
		return nil, fmt.Errorf("memorystore: list turn digests for rollup: %w", err)
	}
	defer rows.Close()
	out := make([]TurnDigest, 0, limit)
	for rows.Next() {
		item, scanErr := scanTurnDigest(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// MarkTurnDigestsRollupSkipped releases done rows from retention when window
// summaries are disabled by configuration.
func (s *Store) MarkTurnDigestsRollupSkipped(ctx context.Context, sessionID string, maxSeq int64) (int64, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	if maxSeq <= 0 {
		return 0, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := db.ExecContext(ctx, `
UPDATE memory_turn_digests
SET rollup_status = 'skipped', rollup_window_id = NULL, rolled_up_at = NULL, updated_at = ?
WHERE project_key = ? AND session_id = ? AND digest_seq <= ?
  AND status = 'done' AND rollup_status IN ('pending', 'failed')
`, now, s.cfg.ProjectKey, strings.TrimSpace(sessionID), maxSeq)
	if err != nil {
		return 0, fmt.Errorf("memorystore: skip turn digest rollup: %w", err)
	}
	return res.RowsAffected()
}

// MarkTurnDigestsRolledUp records that the digest range is now covered by a
// durable L1 window. This must only run after the L1 summary has been stored.
func (s *Store) MarkTurnDigestsRolledUp(ctx context.Context, sessionID string, startSeq, endSeq int64, windowID string) (int64, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	if startSeq <= 0 || endSeq < startSeq || strings.TrimSpace(windowID) == "" {
		return 0, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := db.ExecContext(ctx, `
UPDATE memory_turn_digests
SET rollup_status = 'done', rollup_window_id = ?, rolled_up_at = ?, updated_at = ?
WHERE project_key = ? AND session_id = ? AND digest_seq BETWEEN ? AND ?
  AND status = 'done' AND rollup_status IN ('pending', 'failed')
`, strings.TrimSpace(windowID), now, now,
		s.cfg.ProjectKey, strings.TrimSpace(sessionID), startSeq, endSeq)
	if err != nil {
		return 0, fmt.Errorf("memorystore: mark turn digests rolled up: %w", err)
	}
	return res.RowsAffected()
}

func (s *Store) updateTurnDigestStatus(ctx context.Context, sessionID string, maxSeq int64, status TurnDigestStatus, jobID, reason string) (int64, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	if maxSeq <= 0 {
		return 0, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var processedAt interface{}
	if status == TurnDigestDone {
		processedAt = now
	}
	if status == TurnDigestInvalidated {
		processedAt = now
	}
	res, err := db.ExecContext(ctx, `
UPDATE memory_turn_digests
SET status = ?, process_job_id = ?, last_error = ?,
    processed_at = CASE
        WHEN ? IN ('done', 'invalidated') THEN ?
        ELSE processed_at
    END,
    rollup_status = CASE
        WHEN ? = 'done' THEN 'pending'
        WHEN ? IN ('skipped', 'invalidated') THEN 'skipped'
        ELSE rollup_status
    END,
    rollup_window_id = CASE
        WHEN ? IN ('done', 'skipped', 'invalidated') THEN NULL
        ELSE rollup_window_id
    END,
    rolled_up_at = CASE
        WHEN ? IN ('done', 'skipped', 'invalidated') THEN NULL
        ELSE rolled_up_at
    END,
    updated_at = ?
WHERE project_key = ? AND session_id = ? AND digest_seq <= ?
  AND status IN ('pending', 'processing', 'failed')
`, string(status), nullableString(jobID), nullableString(reason),
		string(status), processedAt,
		string(status), string(status),
		string(status), string(status),
		now, s.cfg.ProjectKey, strings.TrimSpace(sessionID), maxSeq)
	if err != nil {
		return 0, fmt.Errorf("memorystore: update turn digest status: %w", err)
	}
	return res.RowsAffected()
}

// CountPendingTurnDigests returns the pending backlog for a project.
func (s *Store) CountPendingTurnDigests(ctx context.Context, projectKey string) (int, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(projectKey) == "" {
		projectKey = s.cfg.ProjectKey
	}
	var count int
	err = db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_turn_digests WHERE project_key = ? AND status = 'pending'
`, strings.TrimSpace(projectKey)).Scan(&count)
	return count, err
}

// PruneTurnDigests deletes terminal digest rows older than before for the
// configured project, once their rollup is done or skipped.
func (s *Store) PruneTurnDigests(ctx context.Context, before time.Time) (int64, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	if before.IsZero() {
		before = time.Now().UTC()
	}
	res, err := db.ExecContext(ctx, `
DELETE FROM memory_turn_digests
WHERE project_key = ? AND status IN ('done', 'skipped', 'invalidated')
  AND rollup_status IN ('done', 'skipped')
  AND created_at < ?
`, s.cfg.ProjectKey, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("memorystore: prune turn digests: %w", err)
	}
	return res.RowsAffected()
}

// DeleteTurnDigestsForSession removes all digest rows for a deleted session.
func (s *Store) DeleteTurnDigestsForSession(ctx context.Context, sessionID string) (int64, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `
DELETE FROM memory_turn_digests WHERE project_key = ? AND session_id = ?
`, s.cfg.ProjectKey, strings.TrimSpace(sessionID))
	if err != nil {
		return 0, fmt.Errorf("memorystore: delete turn digests: %w", err)
	}
	return res.RowsAffected()
}

// InvalidateTurnDigestsForSession prevents pending rows from being processed
// after a backtrack/compaction or an explicit session invalidation.
func (s *Store) InvalidateTurnDigestsForSession(ctx context.Context, sessionID string) (int64, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := db.ExecContext(ctx, `
UPDATE memory_turn_digests
SET status = 'invalidated', rollup_status = 'skipped',
    rollup_window_id = NULL, rolled_up_at = NULL,
    processed_at = ?, updated_at = ?
WHERE project_key = ? AND session_id = ?
  AND status IN ('pending', 'processing', 'done', 'deferred', 'failed')
  AND rollup_status IN ('pending', 'failed')
`, now, now, s.cfg.ProjectKey, strings.TrimSpace(sessionID))
	if err != nil {
		return 0, fmt.Errorf("memorystore: invalidate turn digests: %w", err)
	}
	return res.RowsAffected()
}

type turnDigestScanner interface {
	Scan(dest ...interface{}) error
}

func scanTurnDigest(scanner turnDigestScanner) (TurnDigest, error) {
	var item TurnDigest
	var scope, status, rollupStatus, rolledUpAt, createdAt, updatedAt, processedAt string
	var truncated int
	if err := scanner.Scan(
		&item.DigestSeq, &item.ID, &scope, &item.ProjectKey, &item.SessionID, &item.TurnID,
		&item.TraceID, &item.ParentSessionID, &item.AgentType,
		&item.UserMessageID, &item.AssistantMessageID, &item.MessageSeqEnd,
		&item.UserText, &item.AssistantText, &truncated, &item.Outcome, &item.Steps,
		&item.UsageJSON, &item.MetadataJSON, &item.DigestHash, &item.DigestRevision, &status,
		&item.ProcessJobID, &item.LastError, &rollupStatus, &item.RollupWindowID,
		&rolledUpAt, &createdAt, &updatedAt, &processedAt,
	); err != nil {
		return TurnDigest{}, err
	}
	item.Scope = Scope(scope)
	item.Status = TurnDigestStatus(status)
	item.RollupStatus = TurnDigestRollupStatus(rollupStatus)
	item.TextTruncated = truncated != 0
	item.RolledUpAt = parseTime(rolledUpAt)
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	item.ProcessedAt = parseTime(processedAt)
	return item, nil
}
