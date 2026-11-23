package memorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// SessionRangeInvalidation identifies the canonical message range removed by a
// backtrack. CutoffMessageSeq is the last retained canonical seq; rows with seq
// greater than it (and <= BeforeMessageSeq when known) were removed.
type SessionRangeInvalidation struct {
	CutoffMessageSeq int64
	BeforeMessageSeq int64
	MessageIDs       []string
	TurnIDs          []string
	// SessionGeneration scopes invalidation to one canonical session
	// incarnation. It must be positive; generationless range invalidation is
	// unsafe when a canonical session ID can be reused.
	SessionGeneration *int64
	// CutoffAt is the event time used to avoid invalidating new rows that
	// re-used a canonical seq after the rewind. Zero disables the time bound.
	CutoffAt time.Time
}

// SessionRangeInvalidationResult reports progressive invalidation counts.
type SessionRangeInvalidationResult struct {
	Digests  int64
	Windows  int64
	Memories int
}

type invalidationDigestRef struct {
	digestSeq          int64
	turnID             string
	userMessageID      string
	assistantMessageID string
	messageSeqEnd      int64
	createdAt          time.Time
}

type invalidationWindowRef struct {
	windowSeq       int64
	startDigestSeq  int64
	endDigestSeq    int64
	startMessageSeq int64
	endMessageSeq   int64
	createdAt       time.Time
}

type invalidationSourceRef struct {
	memoryID     string
	revision     int64
	sourceKind   string
	turnID       string
	messageIDs   []string
	sourceWindow string
	createdAt    time.Time
}

// InvalidateSessionRange invalidates only the L0 digests, L1 windows and
// canonical-derived memory whose evidence overlaps the removed range. It is the
// precise counterpart to the session-wide conservative invalidation used for
// legacy events that do not carry a canonical range.
func (s *Store) InvalidateSessionRange(ctx context.Context, sessionID string, inv SessionRangeInvalidation, reason string) (SessionRangeInvalidationResult, error) {
	result := SessionRangeInvalidationResult{}
	if s == nil {
		return result, fmt.Errorf("memorystore: store is nil")
	}
	db, err := s.ensureV2()
	if err != nil {
		return result, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return result, nil
	}
	if inv.SessionGeneration == nil || *inv.SessionGeneration <= 0 {
		return result, fmt.Errorf("memorystore: positive session generation required for range invalidation")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "source_range_invalidated"
	}
	inv.MessageIDs = uniqueStrings(inv.MessageIDs)
	inv.TurnIDs = uniqueStrings(inv.TurnIDs)
	cutoff := inv.CutoffMessageSeq
	before := inv.BeforeMessageSeq
	if cutoff <= 0 && len(inv.MessageIDs) == 0 && len(inv.TurnIDs) == 0 {
		return result, nil
	}
	if cutoff > 0 && before > 0 && before < cutoff {
		before = 0
	}
	criteria, criteriaArgs := buildRangeDigestCriteria(cutoff, before, inv.MessageIDs, inv.TurnIDs)

	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()

	digestRefs, err := queryInvalidationDigests(ctx, tx, s.cfg.ProjectKey, sessionID, inv.SessionGeneration, criteria, criteriaArgs)
	if err != nil {
		return result, err
	}
	affectedDigestSeqs := make(map[int64]struct{}, len(digestRefs))
	affectedMessageIDs := make(map[string]struct{}, len(inv.MessageIDs)+len(digestRefs)*2)
	affectedTurnIDs := make(map[string]struct{}, len(inv.TurnIDs)+len(digestRefs))
	for _, id := range inv.MessageIDs {
		affectedMessageIDs[id] = struct{}{}
	}
	for _, id := range inv.TurnIDs {
		affectedTurnIDs[id] = struct{}{}
	}
	for _, ref := range digestRefs {
		if !inv.CutoffAt.IsZero() && ref.createdAt.After(inv.CutoffAt) {
			continue
		}
		affectedDigestSeqs[ref.digestSeq] = struct{}{}
		if ref.turnID != "" {
			affectedTurnIDs[ref.turnID] = struct{}{}
		}
		if ref.userMessageID != "" {
			affectedMessageIDs[ref.userMessageID] = struct{}{}
		}
		if ref.assistantMessageID != "" {
			affectedMessageIDs[ref.assistantMessageID] = struct{}{}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	affectedDigestList := make([]int64, 0, len(affectedDigestSeqs))
	for seq := range affectedDigestSeqs {
		affectedDigestList = append(affectedDigestList, seq)
	}
	result.Digests, err = invalidateDigestSeqs(ctx, tx, s.cfg.ProjectKey, sessionID, inv.SessionGeneration, affectedDigestList, reason, now)
	if err != nil {
		return result, err
	}

	windows, err := queryInvalidationWindows(ctx, tx, s.cfg.ProjectKey, sessionID, inv.SessionGeneration)
	if err != nil {
		return result, err
	}
	affectedWindowIDs := make(map[string]struct{})
	affectedWindowSeqs := make([]int64, 0)
	for _, window := range windows {
		overlaps, strong := windowOverlapsInvalidation(window, cutoff, before, affectedDigestSeqs)
		if !overlaps {
			continue
		}
		if !strong && !inv.CutoffAt.IsZero() && window.createdAt.After(inv.CutoffAt) {
			continue
		}
		sourceWindow := TurnWindowSourceWindow(window.startDigestSeq, window.endDigestSeq)
		if inv.SessionGeneration != nil {
			sourceWindow = TurnWindowSourceWindowGeneration(*inv.SessionGeneration, window.startDigestSeq, window.endDigestSeq)
		}
		affectedWindowIDs[sourceWindow] = struct{}{}
		affectedWindowSeqs = append(affectedWindowSeqs, window.windowSeq)
	}
	if len(affectedWindowSeqs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(affectedWindowSeqs)), ",")
		args := make([]interface{}, 0, 3+len(affectedWindowSeqs))
		args = append(args, reason, now, now)
		for _, seq := range affectedWindowSeqs {
			args = append(args, seq)
		}
		windowScope := ""
		if inv.SessionGeneration != nil {
			windowScope = ` AND session_generation = ?`
			args = append(args, *inv.SessionGeneration)
		}
		res, err := tx.ExecContext(ctx, `
UPDATE memory_turn_windows
SET status = 'invalidated', last_error = ?,
    processed_at = COALESCE(processed_at, ?), updated_at = ?
WHERE window_seq IN (`+placeholders+`) AND status != 'invalidated'`+windowScope+`
`, args...)
		if err != nil {
			return result, fmt.Errorf("memorystore: invalidate window range: %w", err)
		}
		result.Windows, _ = res.RowsAffected()
	}

	sourceRefs, err := queryInvalidationSources(ctx, tx, s.cfg.ProjectKey, sessionID, inv.SessionGeneration)
	if err != nil {
		return result, err
	}
	affectedMemories := make(map[string]int64)
	for _, source := range sourceRefs {
		if _, already := affectedMemories[source.memoryID]; already {
			continue
		}
		overlaps, strong := sourceOverlapsInvalidation(source, cutoff, before, affectedMessageIDs, affectedTurnIDs, affectedWindowIDs)
		if !overlaps {
			continue
		}
		if !strong && !inv.CutoffAt.IsZero() && source.createdAt.After(inv.CutoffAt) {
			continue
		}
		affectedMemories[source.memoryID] = source.revision
	}
	for memoryID, revision := range affectedMemories {
		res, err := tx.ExecContext(ctx, `
UPDATE memory_entries SET freshness_status = 'stale', stale_reason = ?,
    revision = revision + 1, updated_at = ?
 WHERE id = ? AND project_key = ? AND revision = ?
`, reason, now, memoryID, s.cfg.ProjectKey, revision)
		if err != nil {
			return result, err
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			continue
		}
		result.Memories++
		eventID, err := insertEventIDTx(ctx, tx, s.cfg.ProjectKey, memoryID, "stale_marked", "system", sessionID, revision+1, map[string]interface{}{
			"reason":                 reason,
			"canonical_cutoff_seq":   cutoff,
			"canonical_before_seq":   before,
			"affected_message_count": len(affectedMessageIDs),
		})
		if err != nil {
			return result, err
		}
		if err := appendCurrentRecordVersionTx(ctx, tx, memoryID, s.cfg.ProjectKey,
			"system:invalidation", reason, "update", eventID); err != nil {
			return result, err
		}
		if err := s.markSummariesStaleForRecordTx(ctx, tx, memoryID, reason); err != nil {
			return result, err
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func buildRangeDigestCriteria(cutoff, before int64, messageIDs, turnIDs []string) (string, []interface{}) {
	args := make([]interface{}, 0)
	clauses := make([]string, 0, 3)
	if cutoff > 0 {
		if before > cutoff {
			clauses = append(clauses, "(message_seq_end > ? AND message_seq_end <= ?)")
			args = append(args, cutoff, before)
		} else {
			clauses = append(clauses, "message_seq_end > ?")
			args = append(args, cutoff)
		}
	}
	if len(messageIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(messageIDs)), ",")
		clauses = append(clauses, "(user_message_id IN ("+placeholders+") OR assistant_message_id IN ("+placeholders+"))")
		for _, id := range messageIDs {
			args = append(args, id)
		}
		for _, id := range messageIDs {
			args = append(args, id)
		}
	}
	if len(turnIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(turnIDs)), ",")
		clauses = append(clauses, "turn_id IN ("+placeholders+")")
		for _, id := range turnIDs {
			args = append(args, id)
		}
	}
	if len(clauses) == 0 {
		return "1=0", nil
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

func queryInvalidationDigests(ctx context.Context, tx *sql.Tx, projectKey, sessionID string, generation *int64, criteria string, criteriaArgs []interface{}) ([]invalidationDigestRef, error) {
	query := `
SELECT digest_seq, COALESCE(turn_id, ''), COALESCE(user_message_id, ''),
       COALESCE(assistant_message_id, ''), message_seq_end, created_at
FROM memory_turn_digests
WHERE project_key = ? AND session_id = ?`
	args := make([]interface{}, 0, 3+len(criteriaArgs))
	args = append(args, projectKey, sessionID)
	if generation != nil {
		query += ` AND session_generation = ?`
		args = append(args, *generation)
	}
	args = append(args, criteriaArgs...)
	query += ` AND ` + criteria + `
ORDER BY digest_seq ASC
`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memorystore: query digest invalidation range: %w", err)
	}
	defer rows.Close()
	out := make([]invalidationDigestRef, 0)
	for rows.Next() {
		var ref invalidationDigestRef
		var createdAt string
		if err := rows.Scan(&ref.digestSeq, &ref.turnID, &ref.userMessageID, &ref.assistantMessageID, &ref.messageSeqEnd, &createdAt); err != nil {
			return nil, err
		}
		ref.createdAt = parseTime(createdAt)
		out = append(out, ref)
	}
	return out, rows.Err()
}

func invalidateDigestSeqs(ctx context.Context, tx *sql.Tx, projectKey, sessionID string, generation *int64, digestSeqs []int64, reason, now string) (int64, error) {
	if len(digestSeqs) == 0 {
		return 0, nil
	}
	var total int64
	for start := 0; start < len(digestSeqs); start += 500 {
		end := start + 500
		if end > len(digestSeqs) {
			end = len(digestSeqs)
		}
		batch := digestSeqs[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]interface{}, 0, 6+len(batch))
		args = append(args, now, reason, now, projectKey, sessionID)
		generationFilter := ""
		if generation != nil {
			generationFilter = ` AND session_generation = ?`
			args = append(args, *generation)
		}
		for _, seq := range batch {
			args = append(args, seq)
		}
		res, err := tx.ExecContext(ctx, `
UPDATE memory_turn_digests
SET status = 'invalidated', rollup_status = 'skipped',
    rollup_window_id = NULL, rolled_up_at = NULL,
    processed_at = ?, last_error = ?, updated_at = ?
WHERE project_key = ? AND session_id = ? AND status != 'invalidated'
  `+generationFilter+`
  AND digest_seq IN (`+placeholders+`)
`, args...)
		if err != nil {
			return total, fmt.Errorf("memorystore: invalidate digest range: %w", err)
		}
		affected, _ := res.RowsAffected()
		total += affected
	}
	return total, nil
}

func queryInvalidationWindows(ctx context.Context, tx *sql.Tx, projectKey, sessionID string, generation *int64) ([]invalidationWindowRef, error) {
	query := `
SELECT window_seq, start_digest_seq, end_digest_seq,
       start_message_seq, end_message_seq, created_at
FROM memory_turn_windows
WHERE project_key = ? AND session_id = ?
`
	args := []interface{}{projectKey, sessionID}
	if generation != nil {
		query += ` AND session_generation = ?`
		args = append(args, *generation)
	}
	query += `
ORDER BY window_seq ASC
`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memorystore: query window invalidation range: %w", err)
	}
	defer rows.Close()
	out := make([]invalidationWindowRef, 0)
	for rows.Next() {
		var ref invalidationWindowRef
		var createdAt string
		if err := rows.Scan(&ref.windowSeq, &ref.startDigestSeq, &ref.endDigestSeq, &ref.startMessageSeq, &ref.endMessageSeq, &createdAt); err != nil {
			return nil, err
		}
		ref.createdAt = parseTime(createdAt)
		out = append(out, ref)
	}
	return out, rows.Err()
}

func windowOverlapsInvalidation(window invalidationWindowRef, cutoff, before int64, affectedDigests map[int64]struct{}) (bool, bool) {
	for seq := range affectedDigests {
		if seq >= window.startDigestSeq && seq <= window.endDigestSeq {
			return true, true
		}
	}
	if cutoff <= 0 || window.endMessageSeq <= cutoff {
		return false, false
	}
	if before > cutoff && window.startMessageSeq > before && window.startMessageSeq > 0 {
		return false, false
	}
	if window.startMessageSeq == 0 {
		return true, false
	}
	return window.startMessageSeq <= cutoff || window.startMessageSeq <= before, false
}

func queryInvalidationSources(ctx context.Context, tx *sql.Tx, projectKey, sessionID string, generation *int64) ([]invalidationSourceRef, error) {
	query := `
SELECT e.id, e.revision, e.source_kind,
       COALESCE(src.turn_id, ''), COALESCE(src.message_ids_json, '[]'),
       COALESCE(src.source_window, ''), e.created_at
FROM memory_entries e
JOIN memory_sources src ON src.memory_id = e.id
WHERE e.project_key = ? AND e.source_session_id = ?
  AND e.source_kind IN ('session_extract', 'turn_window')
  AND e.status IN ('candidate', 'active')
  AND e.freshness_status != 'stale'
`
	args := []interface{}{projectKey, sessionID}
	if generation != nil {
		query += ` AND e.source_session_generation = ? AND src.source_session_generation = ?`
		args = append(args, *generation, *generation)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memorystore: query source invalidation range: %w", err)
	}
	defer rows.Close()
	out := make([]invalidationSourceRef, 0)
	for rows.Next() {
		var ref invalidationSourceRef
		var messageIDsJSON string
		var createdAt string
		if err := rows.Scan(&ref.memoryID, &ref.revision, &ref.sourceKind, &ref.turnID, &messageIDsJSON, &ref.sourceWindow, &createdAt); err != nil {
			return nil, err
		}
		ref.createdAt = parseTime(createdAt)
		var messageIDs []string
		if json.Unmarshal([]byte(messageIDsJSON), &messageIDs) == nil {
			ref.messageIDs = uniqueStrings(messageIDs)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

func sourceOverlapsInvalidation(source invalidationSourceRef, cutoff, before int64, affectedMessageIDs, affectedTurnIDs, affectedWindowIDs map[string]struct{}) (bool, bool) {
	if strings.EqualFold(strings.TrimSpace(source.sourceKind), "turn_window") {
		if _, ok := affectedWindowIDs[strings.TrimSpace(source.sourceWindow)]; ok {
			return true, true
		}
		// Fall through to message/turn evidence. A window row may have been
		// pruned while its derived candidate still carries source IDs.
	}
	if id := strings.TrimSpace(source.turnID); id != "" {
		if _, ok := affectedTurnIDs[id]; ok {
			return true, true
		}
	}
	for _, id := range source.messageIDs {
		if _, ok := affectedMessageIDs[strings.TrimSpace(id)]; ok {
			return true, true
		}
	}
	if cutoff > 0 && strings.TrimSpace(source.sourceWindow) != "" {
		start, end, ok := parseCanonicalWindow(source.sourceWindow)
		if ok && end > cutoff {
			if before <= 0 || start == 0 || start <= before {
				return true, false
			}
		}
	}
	return false, false
}

func parseCanonicalWindow(raw string) (start, end int64, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "turn_window:") {
		return 0, 0, false
	}
	parts := strings.SplitN(raw, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, errStart := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	end, errEnd := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if errStart != nil || errEnd != nil || start <= 0 || end < start {
		return 0, 0, false
	}
	return start, end, true
}
