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

// ErrSessionPurgeGenerationMismatch means a memory purge or extraction
// commit belongs to an older session incarnation than the durable tombstone.
var ErrSessionPurgeGenerationMismatch = errors.New("memory session purge generation mismatch")

// checkSessionPurgeGenerationTx rejects work from an incarnation that has
// already been fenced. A generation newer than a completed tombstone
// represents a recreated canonical session and is allowed to materialize.
// While a purge is still fenced, no work is accepted: the canonical side has
// not yet completed the cross-database delete protocol.
func checkSessionPurgeGenerationTx(
	ctx context.Context,
	tx *sql.Tx,
	projectKey, sessionID string,
	generation int64,
) error {
	var state string
	var fencedGeneration int64
	err := tx.QueryRowContext(ctx, `
		SELECT state, COALESCE(fenced_session_generation, generation)
		FROM memory_session_purges
		WHERE project_key = ? AND session_id = ?
	`, projectKey, sessionID).Scan(&state, &fencedGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memorystore: read session purge fence: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(state), "fenced") || generation <= fencedGeneration {
		return fmt.Errorf("%w: session=%s generation=%d stored=%d",
			ErrSessionPurgeGenerationMismatch, sessionID, generation, fencedGeneration)
	}
	return nil
}

// PurgeSession durably fences one session incarnation in the memory database,
// removes all session-scoped projections and keeps a tombstone for stale
// workers. The operation is idempotent for the same generation and monotonic
// across recreated sessions. It is retained as a compatibility wrapper; new
// callers should pass the canonical incarnation and purge generations
// separately through PurgeSessionWithGenerations.
func (s *Store) PurgeSession(ctx context.Context, sessionID string, generation int64) (int64, error) {
	return s.PurgeSessionWithGenerations(ctx, sessionID, generation, generation)
}

// PurgeSessionWithGenerations fences and removes one canonical session
// incarnation. sessionGeneration identifies the incarnation being deleted;
// purgeGeneration identifies this deletion operation and is independently
// monotonic for idempotent cross-database recovery.
func (s *Store) PurgeSessionWithGenerations(
	ctx context.Context,
	sessionID string,
	sessionGeneration int64,
	purgeGeneration int64,
) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("memorystore: store is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	projectKey := strings.TrimSpace(s.cfg.ProjectKey)
	if sessionID == "" || projectKey == "" || sessionGeneration <= 0 || purgeGeneration <= 0 {
		return 0, fmt.Errorf("%w: session, project and positive generation are required",
			ErrSessionPurgeGenerationMismatch)
	}
	db, err := s.ensureV2()
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return 0, fmt.Errorf("memorystore: begin session purge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	var storedFence, storedPurge int64
	var storedState string
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(fenced_session_generation, generation),
		       COALESCE(purge_generation, generation),
		       state
		FROM memory_session_purges
		WHERE project_key = ? AND session_id = ?
	`, projectKey, sessionID).Scan(&storedFence, &storedPurge, &storedState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("memorystore: read session purge generation: %w", err)
	}
	if err == nil && (storedFence > sessionGeneration || storedPurge > purgeGeneration) {
		return 0, fmt.Errorf("%w: session=%s generation=%d stored=%d",
			ErrSessionPurgeGenerationMismatch, sessionID, sessionGeneration, storedFence)
	}
	// The purge transaction commits its tombstone and all generation-qualified
	// deletes atomically. A retry for an already-purged incarnation must not
	// rerun session-ID-only cleanup after a newer incarnation has been created.
	if err == nil && storedFence == sessionGeneration &&
		strings.EqualFold(strings.TrimSpace(storedState), "purged") {
		if purgeGeneration > storedPurge {
			if _, err := tx.ExecContext(ctx, `
				UPDATE memory_session_purges
				SET purge_generation = ?, updated_at = ?
				WHERE project_key = ? AND session_id = ?
			`, purgeGeneration, now, projectKey, sessionID); err != nil {
				return 0, fmt.Errorf("memorystore: advance completed session purge generation: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("memorystore: commit idempotent session purge: %w", err)
		}
		if err := s.clearSummaryArtifactsIfNoFreshActiveSummary(ctx, projectKey); err != nil {
			return 0, fmt.Errorf("memorystore: clear summary artifacts after session purge: %w", err)
		}
		return 0, nil
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO memory_session_purges(
			project_key, session_id, generation, fenced_session_generation,
			purge_generation, state, reason, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 'purged', 'canonical_session_delete', ?, ?)
		ON CONFLICT(project_key, session_id) DO UPDATE SET
			generation = CASE
				WHEN excluded.fenced_session_generation > memory_session_purges.fenced_session_generation
				THEN excluded.fenced_session_generation ELSE memory_session_purges.generation END,
			fenced_session_generation = CASE
				WHEN excluded.fenced_session_generation > memory_session_purges.fenced_session_generation
				THEN excluded.fenced_session_generation ELSE memory_session_purges.fenced_session_generation END,
			purge_generation = CASE
				WHEN excluded.purge_generation > memory_session_purges.purge_generation
				THEN excluded.purge_generation ELSE memory_session_purges.purge_generation END,
			state = 'purged',
			updated_at = excluded.updated_at
	`, projectKey, sessionID, sessionGeneration, sessionGeneration, purgeGeneration, now, now)
	if err != nil {
		return 0, fmt.Errorf("memorystore: write session purge fence: %w", err)
	}

	// Delete dependent projections before entries so foreign-key enforcement
	// remains valid even when a candidate was already promoted or referenced by
	// a summary.
	statements := []struct {
		query string
		args  []interface{}
	}{
		// A project summary can contain both this session's records and
		// records from other sessions. Keep the project summary row for the
		// latter, but invalidate and enqueue a rebuild before removing its
		// summary items. This makes the purge's summary watermark durable
		// instead of waiting for a later gated read to discover missing items.
		{`UPDATE memory_summaries
		  SET freshness_status = 'stale'
		  WHERE project_key = ? AND status = 'active'
		    AND id IN (
		      SELECT DISTINCT i.summary_id
		      FROM memory_summary_items i
		      JOIN memory_entries e ON e.id = i.memory_id
		      WHERE e.project_key = ? AND e.source_session_id = ?
		        AND e.source_session_generation = ?
		    )`, []interface{}{projectKey, projectKey, sessionID, sessionGeneration}},
		{`DELETE FROM memory_summary_items
		  WHERE memory_id IN (
		    SELECT id FROM memory_entries
		    WHERE project_key = ? AND source_session_id = ?
		      AND source_session_generation = ?
		  )`, []interface{}{projectKey, sessionID, sessionGeneration}},
		{`DELETE FROM memory_sources
		  WHERE memory_id IN (
		    SELECT id FROM memory_entries
		    WHERE project_key = ? AND source_session_id = ?
		      AND source_session_generation = ?
		  )`, []interface{}{
			projectKey, sessionID, sessionGeneration,
		}},
		{`DELETE FROM memory_events
		  WHERE project_key = ? AND memory_id IN (
		    SELECT id FROM memory_entries
		    WHERE project_key = ? AND source_session_id = ?
		      AND source_session_generation = ?
		  )`, []interface{}{
			projectKey, projectKey, sessionID, sessionGeneration,
		}},
		{`DELETE FROM memory_usage
		  WHERE project_key = ? AND memory_id IN (
		    SELECT id FROM memory_entries
		    WHERE project_key = ? AND source_session_id = ?
		      AND source_session_generation = ?
		  )`, []interface{}{
			projectKey, projectKey, sessionID, sessionGeneration,
		}},
		// Recall traces and session modes predate incarnation identity. Keep
		// them until their schema carries a generation; matching only the
		// reusable session ID could erase state from a later incarnation.
		{`DELETE FROM memory_turn_output_receipts
		  WHERE project_key = ? AND session_id = ?
		    AND EXISTS (
		      SELECT 1 FROM memory_turn_capture_generations g
		      WHERE g.project_key = memory_turn_output_receipts.project_key
		        AND g.capture_receipt_id = memory_turn_output_receipts.capture_receipt_id
		        AND g.generation = ?
		    )`, []interface{}{projectKey, sessionID, sessionGeneration}},
		{`DELETE FROM memory_turn_capture_generations
		  WHERE project_key = ? AND session_id = ? AND generation = ?`,
			[]interface{}{projectKey, sessionID, sessionGeneration}},
		{`DELETE FROM memory_turn_windows
		  WHERE project_key = ? AND session_id = ?
		    AND session_generation = ?`, []interface{}{projectKey, sessionID, sessionGeneration}},
		{`DELETE FROM memory_turn_digests
		  WHERE project_key = ? AND session_id = ? AND session_generation = ?`,
			[]interface{}{projectKey, sessionID, sessionGeneration}},
		// Jobs with a generation can be deleted exactly. Legacy jobs whose
		// identity exists only in job_key remain quarantined by the claim fence:
		// without a generation, a prefix match cannot distinguish incarnations.
		{`DELETE FROM memory_jobs
		  WHERE project_key = ?
		    AND (
		      (
		        kind = 'phase1_extract/v2' AND session_id = ?
		        AND session_generation = ?
		      )
		      OR (
		        session_generation = ? AND (
		          substr(job_key, 1, length(?)) = ?
		          OR substr(job_key, 1, length(?)) = ?
		        )
		      )
		    )`, []interface{}{
			projectKey,
			sessionID, sessionGeneration,
			sessionGeneration,
			"session:" + sessionID + ":", "session:" + sessionID + ":",
			"project:" + projectKey + ":session:" + sessionID + ":",
			"project:" + projectKey + ":session:" + sessionID + ":",
		}},
		{`DELETE FROM memory_entries
		  WHERE project_key = ? AND source_session_id = ?
		    AND source_session_generation = ?`,
			[]interface{}{projectKey, sessionID, sessionGeneration}},
	}
	if err := enqueueSessionPurgeSummaryRebuilds(ctx, tx, projectKey, sessionID, sessionGeneration); err != nil {
		return 0, fmt.Errorf("memorystore: enqueue session summary rebuild: %w", err)
	}
	if err := purgeRecordVersionsTx(ctx, tx, projectKey, `
DELETE FROM memory_versions
WHERE project_key = ? AND memory_id IN (
  SELECT id FROM memory_entries
  WHERE project_key = ? AND source_session_id = ?
    AND source_session_generation = ?
)
`, projectKey, projectKey, sessionID, sessionGeneration); err != nil {
		return 0, fmt.Errorf("memorystore: purge session versions: %w", err)
	}
	var deleted int64
	for _, statement := range statements {
		res, execErr := tx.ExecContext(ctx, statement.query, statement.args...)
		if execErr != nil {
			return 0, fmt.Errorf("memorystore: purge session %s: %w", sessionID, execErr)
		}
		if affected, affectedErr := res.RowsAffected(); affectedErr == nil {
			deleted += affected
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("memorystore: commit session purge: %w", err)
	}
	if err := s.clearSummaryArtifactsIfNoFreshActiveSummary(ctx, projectKey); err != nil {
		return deleted, fmt.Errorf("memorystore: clear summary artifacts after session purge: %w", err)
	}
	return deleted, nil
}

func enqueueSessionPurgeSummaryRebuilds(
	ctx context.Context,
	tx *sql.Tx,
	projectKey, sessionID string,
	sessionGeneration int64,
) error {
	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT s.id, COALESCE(s.input_watermark, ''), s.scope
FROM memory_summaries s
JOIN memory_summary_items i ON i.summary_id = s.id
JOIN memory_entries e ON e.id = i.memory_id
WHERE s.project_key = ? AND s.status = 'active'
  AND e.project_key = ? AND e.source_session_id = ?
  AND e.source_session_generation = ?
`, projectKey, projectKey, sessionID, sessionGeneration)
	if err != nil {
		return err
	}
	defer rows.Close()
	type summaryRef struct {
		id, watermark, scope string
	}
	var summaries []summaryRef
	for rows.Next() {
		var summary summaryRef
		if err := rows.Scan(&summary.id, &summary.watermark, &summary.scope); err != nil {
			return err
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, summary := range summaries {
		if _, err := enqueueSummaryRebuild(ctx, tx,
			summary.id+":"+summary.watermark, summary.scope, projectKey, summary.watermark); err != nil {
			return err
		}
	}
	return nil
}
