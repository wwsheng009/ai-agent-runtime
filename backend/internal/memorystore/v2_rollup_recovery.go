package memorystore

import (
	"context"
	"fmt"
	"strings"
)

// HasTurnDigestsForSession distinguishes legacy history with no outbox from a
// session whose outbox was already consumed, skipped, or deferred. Pending-only
// discovery cannot make that distinction when choosing the extraction path.
func (s *Store) HasTurnDigestsForSession(ctx context.Context, sessionID string) (bool, error) {
	db, err := s.ensureV2()
	if err != nil {
		return false, err
	}
	var exists bool
	err = db.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM memory_turn_digests WHERE tenant_id = ? AND project_key = ? AND session_id = ?
)
`, s.tenantID(), s.cfg.ProjectKey, strings.TrimSpace(sessionID)).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("memorystore: check turn digest coverage: %w", err)
	}
	return exists, nil
}

// ListTurnDigestRollupSessions discovers L1 work independently of the
// extraction outbox. afterSessionID is an exclusive keyset cursor; callers can
// advance past ineligible or backoff sessions without loading the full queue.
func (s *Store) ListTurnDigestRollupSessions(ctx context.Context, projectKey, afterSessionID string, limit int) ([]TurnDigestSession, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		projectKey = s.cfg.ProjectKey
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := db.QueryContext(ctx, `
SELECT project_key, session_id, COUNT(1), MIN(digest_seq), MAX(digest_seq)
FROM memory_turn_digests
WHERE tenant_id = ? AND project_key = ? AND session_id > ? AND status = 'done'
  AND rollup_status IN ('pending', 'failed')
GROUP BY project_key, session_id
ORDER BY session_id ASC
LIMIT ?
`, s.tenantID(), projectKey, strings.TrimSpace(afterSessionID), limit)
	if err != nil {
		return nil, fmt.Errorf("memorystore: list rollup recovery sessions: %w", err)
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
