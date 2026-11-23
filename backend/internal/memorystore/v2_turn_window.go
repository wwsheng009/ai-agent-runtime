package memorystore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// TurnWindowStatus is the lifecycle of one L1 window summary. L0 digests are
// only marked rolled up after a window reaches done.
type TurnWindowStatus string

const (
	TurnWindowPending     TurnWindowStatus = "pending"
	TurnWindowProcessing  TurnWindowStatus = "processing"
	TurnWindowDone        TurnWindowStatus = "done"
	TurnWindowFailed      TurnWindowStatus = "failed"
	TurnWindowInvalidated TurnWindowStatus = "invalidated"
)

// TurnWindow is the durable L1 summary for a consecutive range of L0 turn
// digests. It is derived data: the canonical transcript remains the source of
// truth for extraction and memory sources.
type TurnWindow struct {
	WindowSeq         int64
	ID                string
	Scope             Scope
	ProjectKey        string
	SessionID         string
	SessionGeneration int64
	WindowIndex       int
	StartDigestSeq    int64
	EndDigestSeq      int64
	StartMessageSeq   int64
	EndMessageSeq     int64
	TurnCount         int
	SummaryText       string
	SummaryHash       string
	SummaryRevision   int64
	SourceHash        string
	SourceRevision    int64
	Status            TurnWindowStatus
	Model             string
	UsageJSON         string
	MetadataJSON      string
	LastError         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ProcessedAt       time.Time
}

// TurnWindowSummary is the bounded output of a window summarizer.
type TurnWindowSummary struct {
	Text         string
	Model        string
	UsageJSON    string
	MetadataJSON string
}

// TurnWindowPurgeResult reports a manual L1 cleanup.
type TurnWindowPurgeResult struct {
	Windows int64
	Digests int64
}

// TurnWindowSourceWindow returns the stable source_window label used by the
// derived L2 candidate for an L1 window.
func TurnWindowSourceWindow(startDigestSeq, endDigestSeq int64) string {
	return fmt.Sprintf("turn_window:%d-%d", startDigestSeq, endDigestSeq)
}

// TurnWindowSourceWindowGeneration returns a source identity that cannot be
// reused by a different canonical incarnation of the same session.
func TurnWindowSourceWindowGeneration(generation, startDigestSeq, endDigestSeq int64) string {
	return fmt.Sprintf("turn_window:g%d:%d-%d", generation, startDigestSeq, endDigestSeq)
}

// TurnWindowHash computes a stable hash for the L0 input covered by an L1
// window. It deliberately uses digest hashes, not the canonical transcript
// hash, so a window can be retried idempotently.
func TurnWindowHash(digests []TurnDigest) string {
	h := sha256.New()
	for _, digest := range digests {
		_, _ = fmt.Fprintf(h, "%d\x00%s\x00%s\x00%s\x00%d\x00",
			digest.DigestSeq, digest.ID, digest.TurnID, digest.DigestHash, digest.MessageSeqEnd)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// EnsureTurnWindow creates or refreshes the identity of a digest range. The
// returned bool reports that the L0 input changed and any previous L1 summary
// was reset, allowing the caller to stale the old derived candidate.
//
// Callers processing canonical session data should use
// EnsureTurnWindowForGeneration. This legacy entrypoint remains available for
// migration/diagnostic callers and may create a generation-zero quarantined
// row when SessionGeneration is omitted.
func (s *Store) EnsureTurnWindow(ctx context.Context, window TurnWindow) (TurnWindow, bool, error) {
	return s.ensureTurnWindow(ctx, window, false)
}

// EnsureTurnWindowForGeneration is the fail-closed entrypoint for canonical
// session data. A positive generation is mandatory so a reused session ID
// cannot refresh a window belonging to another incarnation.
func (s *Store) EnsureTurnWindowForGeneration(ctx context.Context, window TurnWindow) (TurnWindow, bool, error) {
	return s.ensureTurnWindow(ctx, window, true)
}

func (s *Store) ensureTurnWindow(ctx context.Context, window TurnWindow, requireGeneration bool) (TurnWindow, bool, error) {
	db, err := s.ensureV2()
	if err != nil {
		return TurnWindow{}, false, err
	}
	if strings.TrimSpace(window.ProjectKey) == "" {
		window.ProjectKey = s.cfg.ProjectKey
	}
	window.ProjectKey = strings.TrimSpace(window.ProjectKey)
	window.SessionID = strings.TrimSpace(window.SessionID)
	window.SourceHash = strings.TrimSpace(window.SourceHash)
	if window.ProjectKey == "" || window.SessionID == "" ||
		window.StartDigestSeq <= 0 || window.EndDigestSeq < window.StartDigestSeq {
		return TurnWindow{}, false, fmt.Errorf("memorystore: invalid turn window identity")
	}
	if requireGeneration {
		if window.SessionGeneration <= 0 {
			return TurnWindow{}, false, fmt.Errorf("memorystore: positive session generation required for turn window")
		}
		if window.SourceHash == "" {
			return TurnWindow{}, false, fmt.Errorf("memorystore: turn window source hash required")
		}
	}
	if window.Scope == "" {
		window.Scope = ScopeProject
	}
	if window.ID == "" {
		window.ID = "tw_" + uuid.NewString()
	}
	now := time.Now().UTC()
	if window.CreatedAt.IsZero() {
		window.CreatedAt = now
	}
	window.UpdatedAt = now
	if window.Status == "" {
		window.Status = TurnWindowPending
	}
	if window.UsageJSON == "" {
		window.UsageJSON = "{}"
	}
	if window.MetadataJSON == "" {
		window.MetadataJSON = "{}"
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return TurnWindow{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, window.ProjectKey); err != nil {
		return TurnWindow{}, false, err
	}
	if err := checkTurnWindowIdentityPurgeFenceTx(ctx, tx,
		window.ProjectKey, window.SessionID, window.SessionGeneration); err != nil {
		return TurnWindow{}, false, err
	}
	existing, getErr := scanTurnWindow(tx.QueryRowContext(ctx, `SELECT `+turnWindowColumns+`
FROM memory_turn_windows
WHERE project_key = ? AND session_id = ? AND session_generation = ?
  AND start_digest_seq = ? AND end_digest_seq = ?`,
		window.ProjectKey, window.SessionID, window.SessionGeneration, window.StartDigestSeq, window.EndDigestSeq))
	if getErr != nil && getErr != sql.ErrNoRows {
		return TurnWindow{}, false, getErr
	}
	if getErr == nil {
		window.ID = existing.ID
		window.CreatedAt = existing.CreatedAt
		window.SummaryRevision = existing.SummaryRevision
		sourceChanged := strings.TrimSpace(existing.SourceHash) != "" &&
			existing.SourceHash != window.SourceHash
		if requireGeneration && existing.SourceHash == "" {
			// A positive-generation window without a source hash cannot prove
			// which input produced its summary. Treat it as changed instead of
			// reusing unverified derived state.
			sourceChanged = true
		}
		if !sourceChanged {
			window.Status = existing.Status
			window.SummaryText = existing.SummaryText
			window.SummaryHash = existing.SummaryHash
			window.SummaryRevision = existing.SummaryRevision
			window.Model = existing.Model
			window.UsageJSON = existing.UsageJSON
			window.MetadataJSON = existing.MetadataJSON
			window.LastError = existing.LastError
			window.ProcessedAt = existing.ProcessedAt
		}
		_, err = tx.ExecContext(ctx, `
UPDATE memory_turn_windows
SET window_index = ?, start_message_seq = ?, end_message_seq = ?, turn_count = ?,
    source_hash = ?, source_revision = ?, status = ?,
    summary_text = ?, summary_hash = ?, summary_revision = ?,
    model = ?, usage_json = ?, metadata_json = ?, last_error = ?,
    updated_at = ?, processed_at = ?
WHERE id = ?
`, window.WindowIndex, window.StartMessageSeq, window.EndMessageSeq, window.TurnCount,
			window.SourceHash, window.SourceRevision, string(window.Status),
			window.SummaryText, window.SummaryHash, window.SummaryRevision,
			nullableString(window.Model), window.UsageJSON, window.MetadataJSON,
			nullableString(window.LastError), now.Format(time.RFC3339Nano),
			nullableTime(window.ProcessedAt), window.ID)
		if err != nil {
			return TurnWindow{}, false, fmt.Errorf("memorystore: refresh turn window: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return TurnWindow{}, false, err
		}
		refreshed, err := s.getTurnWindowByID(ctx, window.ID)
		return refreshed, sourceChanged, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO memory_turn_windows (
    id, scope, project_key, session_id, session_generation, window_index,
    start_digest_seq, end_digest_seq, start_message_seq, end_message_seq, turn_count,
    summary_text, summary_hash, summary_revision, source_hash, source_revision,
    status, model, usage_json, metadata_json, last_error,
    created_at, updated_at, processed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, window.ID, string(window.Scope), window.ProjectKey, window.SessionID, window.SessionGeneration, window.WindowIndex,
		window.StartDigestSeq, window.EndDigestSeq, window.StartMessageSeq, window.EndMessageSeq, window.TurnCount,
		window.SummaryText, window.SummaryHash, window.SummaryRevision, window.SourceHash, window.SourceRevision,
		string(window.Status), nullableString(window.Model), window.UsageJSON, window.MetadataJSON,
		nullableString(window.LastError), window.CreatedAt.Format(time.RFC3339Nano),
		window.UpdatedAt.Format(time.RFC3339Nano), nullableTime(window.ProcessedAt))
	if err != nil {
		return TurnWindow{}, false, fmt.Errorf("memorystore: insert turn window: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TurnWindow{}, false, err
	}
	created, err := s.getTurnWindowByID(ctx, window.ID)
	return created, false, err
}

// GetTurnWindow loads one window by id.
func (s *Store) GetTurnWindow(ctx context.Context, id string) (TurnWindow, error) {
	return s.getTurnWindowByID(ctx, strings.TrimSpace(id))
}

// CountTurnWindows returns the number of windows for one session.
func (s *Store) CountTurnWindows(ctx context.Context, sessionID string) (int, error) {
	return s.CountTurnWindowsForGeneration(ctx, sessionID, nil)
}

// CountTurnWindowsForGeneration counts windows for one session incarnation.
// A nil generation preserves the legacy diagnostic behavior.
func (s *Store) CountTurnWindowsForGeneration(ctx context.Context, sessionID string, generation *int64) (int, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	var count int
	query := `SELECT COUNT(1) FROM memory_turn_windows WHERE project_key = ? AND session_id = ?`
	args := []interface{}{s.cfg.ProjectKey, strings.TrimSpace(sessionID)}
	if generation != nil {
		query += ` AND session_generation = ?`
		args = append(args, *generation)
	}
	err = db.QueryRowContext(ctx, query, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("memorystore: count turn windows: %w", err)
	}
	return count, nil
}

// ListTurnWindows returns windows newest-first for diagnostics and tests.
func (s *Store) ListTurnWindows(ctx context.Context, sessionID string, limit int) ([]TurnWindow, error) {
	return s.listTurnWindows(ctx, sessionID, nil, limit)
}

// ListTurnWindowsForGeneration returns only windows owned by one canonical
// session incarnation.
func (s *Store) ListTurnWindowsForGeneration(ctx context.Context, sessionID string, generation int64, limit int) ([]TurnWindow, error) {
	return s.listTurnWindows(ctx, sessionID, &generation, limit)
}

func (s *Store) listTurnWindows(ctx context.Context, sessionID string, generation *int64, limit int) ([]TurnWindow, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	query := `
	SELECT ` + turnWindowColumns + `
 FROM memory_turn_windows
 WHERE project_key = ? AND session_id = ?`
	args := []interface{}{s.cfg.ProjectKey, strings.TrimSpace(sessionID)}
	if generation != nil {
		query += ` AND session_generation = ?`
		args = append(args, *generation)
	}
	query += `
 ORDER BY window_seq DESC LIMIT ?
	`
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memorystore: list turn windows: %w", err)
	}
	defer rows.Close()
	out := make([]TurnWindow, 0, limit)
	for rows.Next() {
		item, scanErr := scanTurnWindow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// CompleteTurnWindow stores the L1 summary. It never marks L0 digests rolled
// up; that is a separate step after the derived L2 candidate is durable.
func (s *Store) CompleteTurnWindow(ctx context.Context, id string, summary TurnWindowSummary) (TurnWindow, error) {
	return s.completeTurnWindow(ctx, id, nil, nil, summary)
}

// CompleteTurnWindowForGeneration stores a summary only when the window
// belongs to the expected canonical session incarnation.
func (s *Store) CompleteTurnWindowForGeneration(ctx context.Context, id string, generation int64, sourceHash string, summary TurnWindowSummary) (TurnWindow, error) {
	if generation <= 0 {
		return TurnWindow{}, fmt.Errorf("memorystore: positive session generation required for turn window")
	}
	sourceHash = strings.TrimSpace(sourceHash)
	if sourceHash == "" {
		return TurnWindow{}, fmt.Errorf("memorystore: turn window source hash required")
	}
	return s.completeTurnWindow(ctx, id, &generation, &sourceHash, summary)
}

func (s *Store) completeTurnWindow(ctx context.Context, id string, generation *int64, sourceHash *string, summary TurnWindowSummary) (TurnWindow, error) {
	db, err := s.ensureV2()
	if err != nil {
		return TurnWindow{}, err
	}
	text := strings.TrimSpace(summary.Text)
	if text == "" {
		return TurnWindow{}, fmt.Errorf("memorystore: turn window summary text is required")
	}
	if rule := memoryredact.DetectSecret(text); rule != "" {
		return TurnWindow{}, fmt.Errorf("memorystore: refusing to store secret-shaped window summary (%s)", rule)
	}
	now := time.Now().UTC()
	sum := sha256.Sum256([]byte(text))
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return TurnWindow{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return TurnWindow{}, err
	}
	if err := checkTurnWindowPurgeFenceTx(ctx, tx, id); err != nil {
		return TurnWindow{}, err
	}
	where := "id = ? AND status != 'invalidated'"
	whereArgs := []interface{}{strings.TrimSpace(id)}
	if generation != nil {
		where += " AND session_generation = ?"
		whereArgs = append(whereArgs, *generation)
	}
	if sourceHash != nil {
		where += " AND source_hash = ?"
		whereArgs = append(whereArgs, *sourceHash)
	}
	args := []interface{}{
		text, hex.EncodeToString(sum[:]), nullableString(summary.Model),
		firstNonEmpty(summary.UsageJSON, "{}"), firstNonEmpty(summary.MetadataJSON, "{}"),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	}
	args = append(args, whereArgs...)
	res, err := tx.ExecContext(ctx, `
UPDATE memory_turn_windows
SET summary_text = ?, summary_hash = ?, summary_revision = summary_revision + 1,
    status = 'done', model = ?, usage_json = ?, metadata_json = ?,
    last_error = NULL, processed_at = ?, updated_at = ?
WHERE `+where, args...)
	if err != nil {
		return TurnWindow{}, fmt.Errorf("memorystore: complete turn window: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return TurnWindow{}, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return TurnWindow{}, err
	}
	return s.getTurnWindowByID(ctx, id)
}

// FailTurnWindow records a summarizer failure. L0 rows remain pending.
func (s *Store) FailTurnWindow(ctx context.Context, id, reason string) error {
	return s.failTurnWindow(ctx, id, nil, nil, reason)
}

// FailTurnWindowForGeneration marks a window failed only if both its canonical
// generation and source hash still match the worker's input.
func (s *Store) FailTurnWindowForGeneration(ctx context.Context, id string, generation int64, sourceHash, reason string) error {
	if generation <= 0 {
		return fmt.Errorf("memorystore: positive session generation required for turn window")
	}
	sourceHash = strings.TrimSpace(sourceHash)
	if sourceHash == "" {
		return fmt.Errorf("memorystore: turn window source hash required")
	}
	return s.failTurnWindow(ctx, id, &generation, &sourceHash, reason)
}

func (s *Store) failTurnWindow(ctx context.Context, id string, generation *int64, sourceHash *string, reason string) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return err
	}
	if err := checkTurnWindowPurgeFenceTx(ctx, tx, id); err != nil {
		return err
	}
	where := "id = ? AND status != 'done' AND status != 'invalidated'"
	args := []interface{}{nullableString(reason), now, strings.TrimSpace(id)}
	if generation != nil {
		where += " AND session_generation = ?"
		args = append(args, *generation)
	}
	if sourceHash != nil {
		where += " AND source_hash = ?"
		args = append(args, *sourceHash)
	}
	res, err := tx.ExecContext(ctx, `
UPDATE memory_turn_windows
SET status = 'failed', last_error = ?, updated_at = ?
WHERE `+where, args...)
	if err != nil {
		return fmt.Errorf("memorystore: fail turn window: %w", err)
	}
	if generation != nil || sourceHash != nil {
		affected, affectedErr := res.RowsAffected()
		if affectedErr != nil {
			return affectedErr
		}
		if affected == 0 {
			return sql.ErrNoRows
		}
	}
	return tx.Commit()
}

// MarkTurnWindowProcessing records that a summarizer claim is active.
func (s *Store) MarkTurnWindowProcessing(ctx context.Context, id string) error {
	return s.markTurnWindowProcessing(ctx, id, nil, nil)
}

// MarkTurnWindowProcessingForGeneration claims a window only if its canonical
// generation and source hash still match the worker's input.
func (s *Store) MarkTurnWindowProcessingForGeneration(ctx context.Context, id string, generation int64, sourceHash string) error {
	if generation <= 0 {
		return fmt.Errorf("memorystore: positive session generation required for turn window")
	}
	sourceHash = strings.TrimSpace(sourceHash)
	if sourceHash == "" {
		return fmt.Errorf("memorystore: turn window source hash required")
	}
	return s.markTurnWindowProcessing(ctx, id, &generation, &sourceHash)
}

func (s *Store) markTurnWindowProcessing(ctx context.Context, id string, generation *int64, sourceHash *string) error {
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return err
	}
	if err := checkTurnWindowPurgeFenceTx(ctx, tx, id); err != nil {
		return err
	}
	where := "id = ? AND status IN ('pending', 'failed', 'processing')"
	args := []interface{}{now, strings.TrimSpace(id)}
	if generation != nil {
		where += " AND session_generation = ?"
		args = append(args, *generation)
	}
	if sourceHash != nil {
		where += " AND source_hash = ?"
		args = append(args, *sourceHash)
	}
	res, err := tx.ExecContext(ctx, `
UPDATE memory_turn_windows
SET status = 'processing', last_error = NULL, updated_at = ?
WHERE `+where, args...)
	if err != nil {
		return fmt.Errorf("memorystore: mark turn window processing: %w", err)
	}
	if generation != nil || sourceHash != nil {
		affected, affectedErr := res.RowsAffected()
		if affectedErr != nil {
			return affectedErr
		}
		if affected == 0 {
			return sql.ErrNoRows
		}
	}
	return tx.Commit()
}

// checkTurnWindowPurgeFenceTx resolves the stored incarnation before a state
// transition. The window row can outlive its canonical session only when it
// has no trustworthy generation; such legacy rows must nevertheless stop being
// mutated once any tombstone exists for the reusable session ID.
func checkTurnWindowPurgeFenceTx(ctx context.Context, tx *sql.Tx, id string) error {
	var projectKey, sessionID string
	var generation int64
	err := tx.QueryRowContext(ctx, `
SELECT project_key, session_id, session_generation
FROM memory_turn_windows
WHERE id = ?
`, strings.TrimSpace(id)).Scan(&projectKey, &sessionID, &generation)
	if err == sql.ErrNoRows {
		// Preserve the existing conditional-update contract: a missing row
		// still yields zero affected rows and sql.ErrNoRows at the caller.
		return nil
	}
	if err != nil {
		return fmt.Errorf("memorystore: read turn window purge identity: %w", err)
	}
	return checkTurnWindowIdentityPurgeFenceTx(ctx, tx, projectKey, sessionID, generation)
}

func checkTurnWindowIdentityPurgeFenceTx(
	ctx context.Context,
	tx *sql.Tx,
	projectKey, sessionID string,
	generation int64,
) error {
	projectKey = strings.TrimSpace(projectKey)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("%w: turn window lacks a session identity",
			ErrSessionPurgeGenerationMismatch)
	}
	if generation > 0 {
		return checkSessionPurgeGenerationTx(ctx, tx, projectKey, sessionID, generation)
	}
	return checkLegacySessionPurgeFenceTx(ctx, tx, projectKey, sessionID)
}

// InvalidateTurnWindowsForSession prevents old L1 summaries from being reused
// after compaction/backtrack. Candidate stale handling remains session-wide and
// conservative until precise window ranges are available.
func (s *Store) InvalidateTurnWindowsForSession(ctx context.Context, sessionID, reason string) (int64, error) {
	return s.invalidateTurnWindowsForSession(ctx, sessionID, nil, reason)
}

// InvalidateTurnWindowsForSessionGeneration invalidates windows associated
// with one canonical session incarnation through their L0 digest range.
func (s *Store) InvalidateTurnWindowsForSessionGeneration(ctx context.Context, sessionID string, generation int64, reason string) (int64, error) {
	return s.invalidateTurnWindowsForSession(ctx, sessionID, &generation, reason)
}

func (s *Store) invalidateTurnWindowsForSession(ctx context.Context, sessionID string, generation *int64, reason string) (int64, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return 0, err
	}
	query := `
UPDATE memory_turn_windows
SET status = 'invalidated', last_error = ?, processed_at = COALESCE(processed_at, ?), updated_at = ?
WHERE project_key = ? AND session_id = ? AND status != 'invalidated'
`
	args := []interface{}{nullableString(reason), now, now, s.cfg.ProjectKey, strings.TrimSpace(sessionID)}
	if generation != nil {
		query += ` AND session_generation = ?`
		args = append(args, *generation)
	}
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("memorystore: invalidate turn windows: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}

// DeleteTurnWindowsForSession removes L1 rows for an explicitly deleted
// session.
func (s *Store) DeleteTurnWindowsForSession(ctx context.Context, sessionID string) (int64, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `
DELETE FROM memory_turn_windows WHERE project_key = ? AND session_id = ?
`, s.cfg.ProjectKey, strings.TrimSpace(sessionID))
	if err != nil {
		return 0, fmt.Errorf("memorystore: delete turn windows: %w", err)
	}
	return res.RowsAffected()
}

// PruneTurnWindows deletes terminal L1 rows after their independent retention
// window. Pending/processing/failed windows are retained because they may still
// be needed for rollup retry or operator diagnosis.
func (s *Store) PruneTurnWindows(ctx context.Context, before time.Time) (int64, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	if before.IsZero() {
		before = time.Now().UTC()
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return 0, fmt.Errorf("memorystore: begin prune turn windows: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT window_seq, created_at
FROM memory_turn_windows
WHERE project_key = ? AND status IN ('done', 'invalidated')
`, s.cfg.ProjectKey)
	if err != nil {
		return 0, fmt.Errorf("memorystore: list turn windows for prune: %w", err)
	}
	var seqs []int64
	for rows.Next() {
		var seq int64
		var createdAt string
		if err := rows.Scan(&seq, &createdAt); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if parseTime(createdAt).Before(before) {
			seqs = append(seqs, seq)
		}
	}
	_ = rows.Close()
	if len(seqs) == 0 {
		return 0, nil
	}
	var deleted int64
	for start := 0; start < len(seqs); start += 500 {
		end := start + 500
		if end > len(seqs) {
			end = len(seqs)
		}
		batch := seqs[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]interface{}, 0, len(batch))
		for _, seq := range batch {
			args = append(args, seq)
		}
		res, err := tx.ExecContext(ctx, `
DELETE FROM memory_turn_windows
WHERE project_key = ? AND status IN ('done', 'invalidated')
  AND window_seq IN (`+placeholders+`)
`, append([]interface{}{s.cfg.ProjectKey}, args...)...)
		if err != nil {
			return deleted, fmt.Errorf("memorystore: prune turn windows: %w", err)
		}
		affected, _ := res.RowsAffected()
		deleted += affected
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("memorystore: commit prune turn windows: %w", err)
	}
	return deleted, nil
}

// PurgeTurnWindows manually removes L1 rows for one session or the whole
// project. Rolled-up L0 rows are released to rollup_status=skipped so their
// independent retention can finish. L2 memory is intentionally not deleted.
func (s *Store) PurgeTurnWindows(ctx context.Context, sessionID string) (TurnWindowPurgeResult, error) {
	result := TurnWindowPurgeResult{}
	db, err := s.ensureV2()
	if err != nil {
		return result, err
	}
	sessionID = strings.TrimSpace(sessionID)
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return result, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	digestWhere := "project_key = ? AND rollup_status = 'done' AND rollup_window_id IS NOT NULL"
	digestArgs := []interface{}{now, s.cfg.ProjectKey}
	if sessionID != "" {
		digestWhere += " AND session_id = ?"
		digestArgs = append(digestArgs, sessionID)
	}
	res, err := tx.ExecContext(ctx, `
UPDATE memory_turn_digests
SET rollup_status = 'skipped', rollup_window_id = NULL, rolled_up_at = NULL, updated_at = ?
WHERE `+digestWhere, digestArgs...)
	if err != nil {
		return result, fmt.Errorf("memorystore: purge turn window digest watermarks: %w", err)
	}
	result.Digests, _ = res.RowsAffected()

	windowWhere := "project_key = ?"
	windowArgs := []interface{}{s.cfg.ProjectKey}
	if sessionID != "" {
		windowWhere += " AND session_id = ?"
		windowArgs = append(windowArgs, sessionID)
	}
	res, err = tx.ExecContext(ctx, `DELETE FROM memory_turn_windows WHERE `+windowWhere, windowArgs...)
	if err != nil {
		return result, fmt.Errorf("memorystore: purge turn windows: %w", err)
	}
	result.Windows, _ = res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

// MarkTurnWindowCandidatesStale marks the derived L2 seed candidate for a
// window stale when its L0 input changed.
func (s *Store) MarkTurnWindowCandidatesStale(ctx context.Context, sessionID, sourceWindow, reason string) (int, error) {
	return s.markTurnWindowCandidatesStale(ctx, sessionID, nil, sourceWindow, reason)
}

// MarkTurnWindowCandidatesStaleForGeneration scopes stale marking to one
// canonical incarnation. The source_window is generation-qualified by the
// caller, so a reused digest range cannot stale a later candidate.
func (s *Store) MarkTurnWindowCandidatesStaleForGeneration(ctx context.Context, sessionID string, generation int64, sourceWindow, reason string) (int, error) {
	return s.markTurnWindowCandidatesStale(ctx, sessionID, &generation, sourceWindow, reason)
}

func (s *Store) markTurnWindowCandidatesStale(ctx context.Context, sessionID string, generation *int64, sourceWindow, reason string) (int, error) {
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	sourceWindow = strings.TrimSpace(sourceWindow)
	if sessionID == "" || sourceWindow == "" {
		return 0, nil
	}
	reason = firstNonEmpty(reason, "turn_window_source_changed")
	query := `
SELECT DISTINCT e.id, e.revision
FROM memory_entries e
JOIN memory_sources src ON src.memory_id = e.id
WHERE e.project_key = ? AND e.source_session_id = ? AND e.source_kind = 'turn_window'
  AND src.source_window = ? AND e.status IN ('candidate', 'active')
  AND e.freshness_status != 'stale'
`
	args := []interface{}{s.cfg.ProjectKey, sessionID, sourceWindow}
	if generation != nil {
		query += ` AND e.source_session_generation = ? AND src.source_session_generation = ?`
		args = append(args, *generation, *generation)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	type staleRecord struct {
		id       string
		revision int64
	}
	var stale []staleRecord
	for rows.Next() {
		var item staleRecord
		if err := rows.Scan(&item.id, &item.revision); err != nil {
			_ = rows.Close()
			return 0, err
		}
		stale = append(stale, item)
	}
	_ = rows.Close()
	if len(stale) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, item := range stale {
		res, err := tx.ExecContext(ctx, `
UPDATE memory_entries SET freshness_status = 'stale', stale_reason = ?,
    revision = revision + 1, updated_at = ?
 WHERE id = ? AND project_key = ? AND revision = ?
`, reason, now.Format(time.RFC3339Nano), item.id, s.cfg.ProjectKey, item.revision)
		if err != nil {
			return 0, err
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			continue
		}
		eventID, err := insertEventIDTx(ctx, tx, s.cfg.ProjectKey, item.id, "stale_marked", "system", sessionID, item.revision+1, map[string]interface{}{
			"reason":        reason,
			"source_window": sourceWindow,
		})
		if err != nil {
			return 0, err
		}
		if err := appendCurrentRecordVersionTx(ctx, tx, item.id, s.cfg.ProjectKey,
			"system:turn_window", reason, "update", eventID); err != nil {
			return 0, err
		}
		if err := s.markSummariesStaleForRecordTx(ctx, tx, item.id, reason); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(stale), nil
}

// CountTurnWindowsByStatus returns the project-level L1 status counts.
func (s *Store) CountTurnWindowsByStatus(ctx context.Context, projectKey string) (map[string]int, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(projectKey) == "" {
		projectKey = s.cfg.ProjectKey
	}
	rows, err := db.QueryContext(ctx, `
SELECT status, COUNT(1) FROM memory_turn_windows WHERE project_key = ? GROUP BY status
`, strings.TrimSpace(projectKey))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
	}
	return out, rows.Err()
}

const turnWindowColumns = `
window_seq, id, scope, project_key, session_id, session_generation, window_index,
start_digest_seq, end_digest_seq, start_message_seq, end_message_seq, turn_count,
summary_text, summary_hash, summary_revision, source_hash, source_revision,
status, COALESCE(model, ''), usage_json, metadata_json, COALESCE(last_error, ''),
created_at, updated_at, COALESCE(processed_at, '')
`

func (s *Store) getTurnWindowByID(ctx context.Context, id string) (TurnWindow, error) {
	db, err := s.ensureV2()
	if err != nil {
		return TurnWindow{}, err
	}
	row := db.QueryRowContext(ctx, `SELECT `+turnWindowColumns+` FROM memory_turn_windows WHERE id = ?`, strings.TrimSpace(id))
	return scanTurnWindow(row)
}

func scanTurnWindow(scanner turnDigestScanner) (TurnWindow, error) {
	var item TurnWindow
	var scope, status, createdAt, updatedAt, processedAt string
	if err := scanner.Scan(
		&item.WindowSeq, &item.ID, &scope, &item.ProjectKey, &item.SessionID, &item.SessionGeneration, &item.WindowIndex,
		&item.StartDigestSeq, &item.EndDigestSeq, &item.StartMessageSeq, &item.EndMessageSeq, &item.TurnCount,
		&item.SummaryText, &item.SummaryHash, &item.SummaryRevision, &item.SourceHash, &item.SourceRevision,
		&status, &item.Model, &item.UsageJSON, &item.MetadataJSON, &item.LastError,
		&createdAt, &updatedAt, &processedAt,
	); err != nil {
		return TurnWindow{}, err
	}
	item.Scope = Scope(scope)
	item.Status = TurnWindowStatus(status)
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	item.ProcessedAt = parseTime(processedAt)
	return item, nil
}

// EncodeTurnWindowMetadata serializes bounded metadata for a window row.
func EncodeTurnWindowMetadata(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
