package memorystore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

func (s *Store) RecordFeedback(ctx context.Context, input FeedbackInput) (MemoryFeedback, error) {
	record, err := s.GetRecord(ctx, input.MemoryID)
	if err != nil {
		return MemoryFeedback{}, err
	}
	kind := MemoryFeedbackKind(strings.ToLower(strings.TrimSpace(string(input.Kind))))
	delta := 0.0
	stale := false
	switch kind {
	case FeedbackHelpful:
		delta = 1
	case FeedbackIrrelevant:
		delta = -1
	case FeedbackStale:
		delta = -1
		stale = true
	case FeedbackIncorrect:
		delta = -2
		stale = true
	default:
		return MemoryFeedback{}, fmt.Errorf("memorystore: invalid feedback kind %q", kind)
	}
	comment := strings.TrimSpace(input.Comment)
	comment, _ = memoryredact.Redact(comment)
	if memoryredact.DetectSecret(comment) != "" {
		return MemoryFeedback{}, fmt.Errorf("memorystore: feedback comment contains secret-shaped data")
	}
	if runes := []rune(comment); len(runes) > 1000 {
		comment = string(runes[:1000])
	}
	actor := strings.TrimSpace(input.Actor)
	if actor == "" {
		actor = "user"
	}
	now := time.Now().UTC()
	feedback := MemoryFeedback{
		ID: "feedback_" + uuid.NewString(), MemoryID: record.ID,
		TenantID: record.TenantID, ProjectKey: record.ProjectKey,
		SessionID: strings.TrimSpace(input.SessionID), TurnID: strings.TrimSpace(input.TurnID),
		Kind: kind, Comment: comment, Actor: actor, CreatedAt: now,
	}
	db, err := s.ensureV2()
	if err != nil {
		return MemoryFeedback{}, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return MemoryFeedback{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET feedback_score = MAX(-10.0, MIN(10.0, feedback_score + ?)),
    freshness_status = CASE WHEN ? THEN 'stale' ELSE freshness_status END,
    revalidate_after = CASE WHEN ? THEN ? ELSE revalidate_after END,
    stale_reason = CASE WHEN ? THEN ? ELSE stale_reason END,
    revision = revision + CASE WHEN ? THEN 1 ELSE 0 END,
    updated_at = CASE WHEN ? THEN ? ELSE updated_at END
WHERE id = ? AND project_key = ?
`, delta, boolInt(stale), boolInt(stale), nullableString(now.Format(time.RFC3339Nano)),
		boolInt(stale), "feedback:"+string(kind), boolInt(stale), boolInt(stale),
		now.Format(time.RFC3339Nano), record.ID, record.ProjectKey); err != nil {
		return MemoryFeedback{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_feedback (
    id, memory_id, tenant_id, project_key, session_id, turn_id,
    kind, comment, actor, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, feedback.ID, feedback.MemoryID, feedback.TenantID, feedback.ProjectKey,
		feedback.SessionID, feedback.TurnID, string(feedback.Kind), feedback.Comment,
		feedback.Actor, feedback.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return MemoryFeedback{}, err
	}
	eventType := "feedback_" + string(kind)
	eventID, err := insertEventIDTx(ctx, tx, record.ProjectKey, record.ID, eventType, actor,
		feedback.SessionID, record.Revision+1, map[string]interface{}{
			"feedback_kind": string(kind), "feedback_score_delta": delta,
		})
	if err != nil {
		return MemoryFeedback{}, err
	}
	if stale {
		if err := s.appendCurrentRecordVersionTx(ctx, tx, record.ID, record.ProjectKey,
			actor, "feedback:"+string(kind), "feedback", eventID); err != nil {
			return MemoryFeedback{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return MemoryFeedback{}, err
	}
	return feedback, nil
}

func (s *Store) ListFeedback(ctx context.Context, memoryID string, limit int) ([]MemoryFeedback, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	where := []string{"tenant_id = ?", "project_key = ?"}
	args := []interface{}{scope.TenantID, scope.ProjectKey}
	if id := strings.TrimSpace(memoryID); id != "" {
		where = append(where, "memory_id = ?")
		args = append(args, id)
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `
SELECT id, memory_id, tenant_id, project_key, session_id, turn_id,
       kind, comment, actor, created_at
FROM memory_feedback
WHERE `+strings.Join(where, " AND ")+`
ORDER BY created_at DESC, id DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]MemoryFeedback, 0, limit)
	for rows.Next() {
		var (
			item                                    MemoryFeedback
			id, memoryID, tenantID, projectKey      sql.NullString
			sessionID, turnID, kind, comment, actor sql.NullString
			createdAt                               sql.NullString
		)
		if err := rows.Scan(&id, &memoryID, &tenantID, &projectKey, &sessionID, &turnID,
			&kind, &comment, &actor, &createdAt); err != nil {
			return nil, err
		}
		item = MemoryFeedback{
			ID: id.String, MemoryID: memoryID.String, TenantID: tenantID.String,
			ProjectKey: projectKey.String, SessionID: sessionID.String, TurnID: turnID.String,
			Kind: MemoryFeedbackKind(kind.String), Comment: comment.String,
			Actor: actor.String, CreatedAt: parseTime(createdAt.String),
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
