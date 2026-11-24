package memorystore

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

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

const (
	maxEpisodeSourceIDs = 32
	maxEpisodeTextRunes = 4000
	maxEpisodeActions   = 32
)

func (s *Store) CreateEpisode(ctx context.Context, input EpisodeInput) (Episode, error) {
	db, err := s.ensureV2()
	if err != nil {
		return Episode{}, err
	}
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return Episode{}, err
	}
	input, err = normalizeEpisodeInput(input, scope)
	if err != nil {
		return Episode{}, err
	}
	episodeID := strings.TrimSpace(input.ID)
	if episodeID == "" {
		episodeID = deterministicEpisodeID(input)
	}
	existing, err := s.getEpisodeRaw(ctx, episodeID, input.ProjectKey)
	if err == nil {
		return s.GetEpisode(ctx, existing.ID)
	}
	if err != sql.ErrNoRows {
		return Episode{}, err
	}
	memoryID := "mem_episode_" + uuid.NewString()
	now := time.Now().UTC()
	record := episodeRecord(input, memoryID, episodeID, now)
	actionsJSON, _ := json.Marshal(input.Actions)
	sourceIDsJSON, _ := json.Marshal(input.SourceEventIDs)

	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return Episode{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.insertRecordSQLTxWithOptions(ctx, tx, record, "task_completion", input.SessionID,
		recordInsertOptions{eventType: "episode_created", changeType: "create"}); err != nil {
		return Episode{}, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO memory_episodes (
    id, memory_id, tenant_id, project_key, task_id, session_id, problem,
    actions_json, result, failure_reason, outcome, started_at, ended_at,
    source_event_ids_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, episodeID, memoryID, input.TenantID, input.ProjectKey, input.TaskID, input.SessionID,
		input.Problem, string(actionsJSON), input.Result, input.FailureReason, string(input.Outcome),
		nullableTime(input.StartedAt), nullableTime(input.EndedAt), string(sourceIDsJSON),
		now.Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueConstraint(err) {
			existing, lookupErr := s.getEpisodeRaw(ctx, episodeID, input.ProjectKey)
			if lookupErr == nil {
				return s.GetEpisode(ctx, existing.ID)
			}
		}
		return Episode{}, fmt.Errorf("memorystore: insert episode: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Episode{}, err
	}
	return s.GetEpisode(ctx, episodeID)
}

func (s *Store) GetEpisode(ctx context.Context, id string) (Episode, error) {
	scope, err := s.resolveScopeContext(ctx)
	if err != nil {
		return Episode{}, err
	}
	episode, err := s.getEpisodeRaw(ctx, strings.TrimSpace(id), scope.ProjectKey)
	if err != nil {
		return Episode{}, err
	}
	if _, err := s.GetRecord(ctx, episode.MemoryID); err != nil {
		return Episode{}, err
	}
	return episode, nil
}

func (s *Store) ListEpisodes(ctx context.Context, filter EpisodeFilter) ([]Episode, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	scopePredicate, scopeArgs, err := s.memoryScopeVisibility(ctx, "m", ScopePermissionRead, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	where := []string{scopePredicate}
	args := append([]interface{}(nil), scopeArgs...)
	if value := strings.TrimSpace(filter.TaskID); value != "" {
		where = append(where, "e.task_id = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.SessionID); value != "" {
		where = append(where, "e.session_id = ?")
		args = append(args, value)
	}
	if filter.Outcome != "" {
		where = append(where, "e.outcome = ?")
		args = append(args, string(filter.Outcome))
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `
SELECT e.id, e.memory_id, e.tenant_id, e.project_key, e.task_id, e.session_id,
       e.problem, e.actions_json, e.result, e.failure_reason, e.outcome,
       COALESCE(e.started_at, ''), COALESCE(e.ended_at, ''),
       e.source_event_ids_json, e.created_at
FROM memory_episodes e
JOIN memory_entries m ON m.id = e.memory_id
WHERE `+strings.Join(where, " AND ")+`
ORDER BY e.created_at DESC, e.id DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	episodes := make([]Episode, 0, limit)
	for rows.Next() {
		episode, scanErr := scanEpisode(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		episodes = append(episodes, episode)
	}
	return episodes, rows.Err()
}

func (s *Store) PromoteEpisode(ctx context.Context, id string) (Episode, error) {
	episode, err := s.GetEpisode(ctx, id)
	if err != nil {
		return Episode{}, err
	}
	if len(episode.SourceEventIDs) == 0 {
		return Episode{}, fmt.Errorf("memorystore: episode without durable source events cannot be promoted")
	}
	if _, err := s.PromoteCandidate(ctx, episode.MemoryID); err != nil {
		return Episode{}, err
	}
	return s.GetEpisode(ctx, id)
}

func (s *Store) getEpisodeRaw(ctx context.Context, id, projectKey string) (Episode, error) {
	db, err := s.ensureV2()
	if err != nil {
		return Episode{}, err
	}
	row := db.QueryRowContext(ctx, `
SELECT id, memory_id, tenant_id, project_key, task_id, session_id, problem,
       actions_json, result, failure_reason, outcome, COALESCE(started_at, ''),
       COALESCE(ended_at, ''), source_event_ids_json, created_at
FROM memory_episodes
WHERE id = ? AND project_key = ?
`, strings.TrimSpace(id), strings.TrimSpace(projectKey))
	return scanEpisode(row)
}

type episodeScanner interface {
	Scan(dest ...interface{}) error
}

func scanEpisode(row episodeScanner) (Episode, error) {
	var (
		episode                  Episode
		id, memoryID, tenantID   sql.NullString
		projectKey, taskID       sql.NullString
		sessionID, problem       sql.NullString
		actionsJSON, result      sql.NullString
		failureReason, outcome   sql.NullString
		startedAt, endedAt       sql.NullString
		sourceIDsJSON, createdAt sql.NullString
	)
	if err := row.Scan(
		&id, &memoryID, &tenantID, &projectKey, &taskID, &sessionID, &problem,
		&actionsJSON, &result, &failureReason, &outcome, &startedAt, &endedAt,
		&sourceIDsJSON, &createdAt,
	); err != nil {
		return Episode{}, err
	}
	episode = Episode{
		ID: id.String, MemoryID: memoryID.String, TenantID: tenantID.String,
		ProjectKey: projectKey.String, TaskID: taskID.String, SessionID: sessionID.String,
		Problem: problem.String, Result: result.String, FailureReason: failureReason.String,
		Outcome: EpisodeOutcome(outcome.String), StartedAt: parseTime(startedAt.String),
		EndedAt: parseTime(endedAt.String), CreatedAt: parseTime(createdAt.String),
	}
	_ = json.Unmarshal([]byte(actionsJSON.String), &episode.Actions)
	_ = json.Unmarshal([]byte(sourceIDsJSON.String), &episode.SourceEventIDs)
	return episode, nil
}

func normalizeEpisodeInput(input EpisodeInput, scope ScopeContext) (EpisodeInput, error) {
	input.ProjectKey = firstNonEmpty(input.ProjectKey, scope.ProjectKey)
	input.TenantID = firstNonEmpty(input.TenantID, scope.TenantID)
	if input.ProjectKey == "" || input.ProjectKey != scope.ProjectKey {
		return EpisodeInput{}, fmt.Errorf("memorystore: episode project identity mismatch")
	}
	if scope.TenantID != "" && input.TenantID != scope.TenantID {
		return EpisodeInput{}, fmt.Errorf("memorystore: episode tenant identity mismatch")
	}
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	if input.TaskID == "" || input.SessionID == "" {
		return EpisodeInput{}, fmt.Errorf("memorystore: episode task and session are required")
	}
	input.Outcome = EpisodeOutcome(strings.ToLower(strings.TrimSpace(string(input.Outcome))))
	switch input.Outcome {
	case EpisodeOutcomeSuccess, EpisodeOutcomeFailed, EpisodeOutcomePartial,
		EpisodeOutcomeBlocked, EpisodeOutcomeCancelled:
	default:
		return EpisodeInput{}, fmt.Errorf("memorystore: invalid episode outcome %q", input.Outcome)
	}
	input.Problem = sanitizeEpisodeText(input.Problem)
	input.Result = sanitizeEpisodeText(input.Result)
	input.FailureReason = sanitizeEpisodeText(input.FailureReason)
	if input.Problem == "" {
		return EpisodeInput{}, fmt.Errorf("memorystore: episode problem is required")
	}
	if len(input.Actions) > maxEpisodeActions {
		input.Actions = input.Actions[:maxEpisodeActions]
	}
	for index, action := range input.Actions {
		input.Actions[index] = sanitizeEpisodeText(action)
	}
	input.SourceEventIDs = normalizeEpisodeSourceIDs(input.SourceEventIDs)
	if len(input.SourceEventIDs) == 0 {
		return EpisodeInput{}, fmt.Errorf("memorystore: episode requires durable source event ids")
	}
	if input.EndedAt.IsZero() {
		input.EndedAt = time.Now().UTC()
	}
	input.EndedAt = input.EndedAt.UTC()
	if !input.StartedAt.IsZero() {
		input.StartedAt = input.StartedAt.UTC()
		if input.StartedAt.After(input.EndedAt) {
			return EpisodeInput{}, fmt.Errorf("memorystore: episode interval is invalid")
		}
	}
	return input, nil
}

func sanitizeEpisodeText(text string) string {
	text = strings.TrimSpace(text)
	text, _ = memoryredact.Redact(text)
	if runes := []rune(text); len(runes) > maxEpisodeTextRunes {
		text = string(runes[:maxEpisodeTextRunes])
	}
	if memoryredact.DetectSecret(text) != "" {
		return ""
	}
	return text
}

func normalizeEpisodeSourceIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 256 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) >= maxEpisodeSourceIDs {
			break
		}
	}
	return out
}

func deterministicEpisodeID(input EpisodeInput) string {
	sourceIDs := append([]string(nil), input.SourceEventIDs...)
	sort.Strings(sourceIDs)
	payload := strings.Join([]string{
		input.TenantID, input.ProjectKey, input.TaskID, input.SessionID,
		string(input.Outcome), strings.Join(sourceIDs, "\x00"),
	}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return "episode_" + hex.EncodeToString(sum[:16])
}

func episodeRecord(input EpisodeInput, memoryID, episodeID string, now time.Time) Record {
	actions := append([]string(nil), input.Actions...)
	textBuilder := strings.Builder{}
	textBuilder.WriteString("Episode outcome: ")
	textBuilder.WriteString(string(input.Outcome))
	textBuilder.WriteString("\nProblem: ")
	textBuilder.WriteString(input.Problem)
	if len(actions) > 0 {
		textBuilder.WriteString("\nActions: ")
		textBuilder.WriteString(strings.Join(actions, " | "))
	}
	if input.Result != "" {
		textBuilder.WriteString("\nResult: ")
		textBuilder.WriteString(input.Result)
	}
	if input.FailureReason != "" {
		textBuilder.WriteString("\nFailure: ")
		textBuilder.WriteString(input.FailureReason)
	}
	text := textBuilder.String()
	sourceRefs := make([]string, 0, len(input.SourceEventIDs))
	for _, sourceID := range input.SourceEventIDs {
		sourceRefs = append(sourceRefs, "event:"+sourceID)
	}
	metadata := map[string]interface{}{
		"episode_id":       episodeID,
		"episode_outcome":  string(input.Outcome),
		"task_id":          input.TaskID,
		"source_event_ids": append([]string(nil), input.SourceEventIDs...),
	}
	return Record{
		ID: memoryID, Scope: ScopeProject, ScopeKind: ScopeProject,
		ScopeKey: input.ProjectKey, TenantID: input.TenantID,
		ProjectKey: input.ProjectKey, TaskID: input.TaskID,
		Kind: "episodic", Status: StatusCandidate, Text: text,
		Title:          "Episode: " + string(input.Outcome),
		Summary:        truncateTextByRunes(input.Problem, 500),
		NormalizedText: normalizeMemoryText(text),
		Tags:           []string{"episode", string(input.Outcome)},
		Confidence:     1, Sensitivity: "internal", Pinned: false,
		RetentionClass: "standard", SourceKind: "task_completion",
		SourceType: "task_completion", SourceTrust: "agent_inference",
		SourceSessionID: input.SessionID, SourceRefs: sourceRefs,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
		Freshness: FreshnessFresh, LastVerifiedAt: now, Metadata: metadata,
	}
}

func truncateTextByRunes(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}
