package team

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/migrate"
	"github.com/google/uuid"

	_ "github.com/mattn/go-sqlite3"
)

// SQLiteStore persists team data in SQLite.
type SQLiteStore struct {
	db *sql.DB
}

const globalMailReceiptAgent = "*"

// NewSQLiteStore opens a SQLite-backed team store.
func NewSQLiteStore(cfg *StoreConfig) (*SQLiteStore, error) {
	if cfg == nil {
		cfg = &StoreConfig{}
	}
	dsn, err := resolveDSN(cfg)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open team db: %w", err)
	}
	store := &SQLiteStore{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the underlying database.
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// WithImmediateTx executes fn inside a SQLite IMMEDIATE transaction.
// The DSN is configured with _txlock=immediate, so BeginTx automatically uses IMMEDIATE mode.
func (s *SQLiteStore) WithImmediateTx(ctx context.Context, fn func(*sql.Tx) error) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if fn == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin immediate tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit immediate tx: %w", err)
	}
	committed = true
	return nil
}

// CreateTeam inserts a new team record.
func (s *SQLiteStore) CreateTeam(ctx context.Context, team Team) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("team store is not initialized")
	}
	if team.ID == "" {
		team.ID = "team_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	now := time.Now().UTC()
	if team.CreatedAt.IsZero() {
		team.CreatedAt = now
	}
	if team.UpdatedAt.IsZero() {
		team.UpdatedAt = now
	}
	if team.Status == "" {
		team.Status = TeamStatusActive
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO teams (
			id, workspace_id, lead_session_id, status, strategy, max_teammates, max_writers, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, team.ID, team.WorkspaceID, team.LeadSessionID, string(team.Status), team.Strategy, team.MaxTeammates, team.MaxWriters, formatTime(team.CreatedAt), formatTime(team.UpdatedAt))
	if err != nil {
		return "", fmt.Errorf("insert team: %w", err)
	}
	return team.ID, nil
}

// GetTeam loads a team by id.
func (s *SQLiteStore) GetTeam(ctx context.Context, id string) (*Team, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, lead_session_id, status, strategy, max_teammates, max_writers, created_at, updated_at
		FROM teams
		WHERE id = ?
	`, id)
	var (
		team         Team
		status       string
		createdAtRaw string
		updatedAtRaw string
	)
	if err := row.Scan(&team.ID, &team.WorkspaceID, &team.LeadSessionID, &status, &team.Strategy, &team.MaxTeammates, &team.MaxWriters, &createdAtRaw, &updatedAtRaw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load team: %w", err)
	}
	team.Status = TeamStatus(status)
	team.CreatedAt = parseTime(createdAtRaw)
	team.UpdatedAt = parseTime(updatedAtRaw)
	return &team, nil
}

// ListTeams returns teams matching the filter.
func (s *SQLiteStore) ListTeams(ctx context.Context, filter TeamFilter) ([]Team, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	query := strings.Builder{}
	query.WriteString(`
		SELECT id, workspace_id, lead_session_id, status, strategy, max_teammates, max_writers, created_at, updated_at
		FROM teams
	`)
	clauses := make([]string, 0)
	args := make([]interface{}, 0)
	if strings.TrimSpace(filter.WorkspaceID) != "" {
		clauses = append(clauses, "workspace_id = ?")
		args = append(args, filter.WorkspaceID)
	}
	if filter.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, string(filter.Status))
	}
	if len(clauses) > 0 {
		query.WriteString(" WHERE ")
		query.WriteString(strings.Join(clauses, " AND "))
	}
	query.WriteString(" ORDER BY created_at DESC")
	if filter.Limit > 0 {
		query.WriteString(" LIMIT ?")
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()

	teams := make([]Team, 0)
	for rows.Next() {
		var (
			team         Team
			status       string
			createdAtRaw string
			updatedAtRaw string
		)
		if err := rows.Scan(&team.ID, &team.WorkspaceID, &team.LeadSessionID, &status, &team.Strategy, &team.MaxTeammates, &team.MaxWriters, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		team.Status = TeamStatus(status)
		team.CreatedAt = parseTime(createdAtRaw)
		team.UpdatedAt = parseTime(updatedAtRaw)
		teams = append(teams, team)
	}
	return teams, rows.Err()
}

// UpdateTeam updates a team record.
func (s *SQLiteStore) UpdateTeam(ctx context.Context, team Team) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if team.ID == "" {
		return fmt.Errorf("team id is required")
	}
	team.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE teams
		SET workspace_id = ?, lead_session_id = ?, status = ?, strategy = ?, max_teammates = ?, max_writers = ?, updated_at = ?
		WHERE id = ?
	`, team.WorkspaceID, team.LeadSessionID, string(team.Status), team.Strategy, team.MaxTeammates, team.MaxWriters, formatTime(team.UpdatedAt), team.ID)
	if err != nil {
		return fmt.Errorf("update team: %w", err)
	}
	return nil
}

// UpdateTeamStatus updates a team's status field.
func (s *SQLiteStore) UpdateTeamStatus(ctx context.Context, id string, status TeamStatus) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if id == "" {
		return fmt.Errorf("team id is required")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE teams SET status = ?, updated_at = ? WHERE id = ?
	`, string(status), formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("update team status: %w", err)
	}
	return nil
}

// DeleteTeam deletes a team record.
func (s *SQLiteStore) DeleteTeam(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if id == "" {
		return fmt.Errorf("team id is required")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM teams WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete team: %w", err)
	}
	return nil
}

// ListTeamIDs returns all team ids.
func (s *SQLiteStore) ListTeamIDs(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM teams ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list team ids: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan team id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpsertTeammate inserts or updates a teammate record.
func (s *SQLiteStore) UpsertTeammate(ctx context.Context, teammate Teammate) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("team store is not initialized")
	}
	if teammate.ID == "" {
		teammate.ID = "mate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	now := time.Now().UTC()
	if teammate.CreatedAt.IsZero() {
		teammate.CreatedAt = now
	}
	if teammate.UpdatedAt.IsZero() {
		teammate.UpdatedAt = now
	}
	if teammate.State == "" {
		teammate.State = TeammateStateIdle
	}
	capabilitiesJSON, err := encodeStringSlice(teammate.Capabilities)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO teammates (
			id, team_id, name, profile, session_id, state, last_heartbeat, capabilities_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			team_id = excluded.team_id,
			name = excluded.name,
			profile = excluded.profile,
			session_id = excluded.session_id,
			state = excluded.state,
			last_heartbeat = excluded.last_heartbeat,
			capabilities_json = excluded.capabilities_json,
			updated_at = excluded.updated_at
	`, teammate.ID, teammate.TeamID, teammate.Name, teammate.Profile, teammate.SessionID, string(teammate.State), formatTime(teammate.LastHeartbeat), capabilitiesJSON, formatTime(teammate.CreatedAt), formatTime(teammate.UpdatedAt))
	if err != nil {
		return "", fmt.Errorf("upsert teammate: %w", err)
	}
	return teammate.ID, nil
}

// GetTeammate loads a teammate by id.
func (s *SQLiteStore) GetTeammate(ctx context.Context, id string) (*Teammate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, team_id, name, profile, session_id, state, last_heartbeat, capabilities_json, created_at, updated_at
		FROM teammates WHERE id = ?
	`, id)
	var (
		teammate     Teammate
		state        string
		heartbeatRaw string
		capabilities string
		createdAtRaw string
		updatedAtRaw string
	)
	if err := row.Scan(&teammate.ID, &teammate.TeamID, &teammate.Name, &teammate.Profile, &teammate.SessionID, &state, &heartbeatRaw, &capabilities, &createdAtRaw, &updatedAtRaw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load teammate: %w", err)
	}
	teammate.State = TeammateState(state)
	teammate.LastHeartbeat = parseTime(heartbeatRaw)
	teammate.Capabilities = decodeStringSlice(capabilities)
	teammate.CreatedAt = parseTime(createdAtRaw)
	teammate.UpdatedAt = parseTime(updatedAtRaw)
	return &teammate, nil
}

// ListTeammates returns all teammates within a team.
func (s *SQLiteStore) ListTeammates(ctx context.Context, teamID string) ([]Teammate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, team_id, name, profile, session_id, state, last_heartbeat, capabilities_json, created_at, updated_at
		FROM teammates WHERE team_id = ?
		ORDER BY created_at ASC
	`, teamID)
	if err != nil {
		return nil, fmt.Errorf("list teammates: %w", err)
	}
	defer rows.Close()

	teammates := make([]Teammate, 0)
	for rows.Next() {
		var (
			teammate     Teammate
			state        string
			heartbeatRaw string
			capabilities string
			createdAtRaw string
			updatedAtRaw string
		)
		if err := rows.Scan(&teammate.ID, &teammate.TeamID, &teammate.Name, &teammate.Profile, &teammate.SessionID, &state, &heartbeatRaw, &capabilities, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan teammate: %w", err)
		}
		teammate.State = TeammateState(state)
		teammate.LastHeartbeat = parseTime(heartbeatRaw)
		teammate.Capabilities = decodeStringSlice(capabilities)
		teammate.CreatedAt = parseTime(createdAtRaw)
		teammate.UpdatedAt = parseTime(updatedAtRaw)
		teammates = append(teammates, teammate)
	}
	return teammates, rows.Err()
}

// UpdateTeammateState updates the state for a teammate.
func (s *SQLiteStore) UpdateTeammateState(ctx context.Context, id string, state TeammateState) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE teammates SET state = ?, updated_at = ? WHERE id = ?
	`, string(state), formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("update teammate state: %w", err)
	}
	return nil
}

// UpdateTeammateHeartbeat updates a teammate heartbeat timestamp.
func (s *SQLiteStore) UpdateTeammateHeartbeat(ctx context.Context, id string, heartbeat time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE teammates SET last_heartbeat = ?, updated_at = ? WHERE id = ?
	`, formatTime(heartbeat), formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("update teammate heartbeat: %w", err)
	}
	return nil
}

// CreateTask inserts a new task record.
func (s *SQLiteStore) CreateTask(ctx context.Context, task Task) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("team store is not initialized")
	}
	if task.ID == "" {
		task.ID = "task_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if task.Status == "" {
		task.Status = TaskStatusPending
	}
	now := time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = now
	}
	if task.Version == 0 {
		task.Version = 1
	}
	readJSON, err := encodeStringSlice(task.ReadPaths)
	if err != nil {
		return "", err
	}
	inputsJSON, err := encodeStringSlice(task.Inputs)
	if err != nil {
		return "", err
	}
	writeJSON, err := encodeStringSlice(task.WritePaths)
	if err != nil {
		return "", err
	}
	deliverableJSON, err := encodeStringSlice(task.Deliverables)
	if err != nil {
		return "", err
	}
	assignee := nullableString(task.Assignee)
	leaseUntil := nullableTime(task.LeaseUntil)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO team_tasks (
			id, team_id, parent_task_id, title, goal, status, priority, assignee, lease_until, retry_count,
			inputs_json, read_paths_json, write_paths_json, deliverables_json, summary, result_ref, version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, task.ID, task.TeamID, nullableString(task.ParentTaskID), task.Title, task.Goal, string(task.Status), task.Priority, assignee, leaseUntil, task.RetryCount, inputsJSON, readJSON, writeJSON, deliverableJSON, task.Summary, nullableString(task.ResultRef), task.Version, formatTime(task.CreatedAt), formatTime(task.UpdatedAt))
	if err != nil {
		return "", fmt.Errorf("insert task: %w", err)
	}
	return task.ID, nil
}

// GetTask loads a task by id.
func (s *SQLiteStore) GetTask(ctx context.Context, id string) (*Task, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, team_id, parent_task_id, title, goal, status, priority, assignee, lease_until, retry_count,
			inputs_json, read_paths_json, write_paths_json, deliverables_json, summary, result_ref, version, created_at, updated_at
		FROM team_tasks WHERE id = ?
	`, id)
	var (
		task            Task
		status          string
		assigneeRaw     sql.NullString
		leaseUntilRaw   sql.NullString
		inputsJSON      string
		readJSON        string
		writeJSON       string
		deliverableJSON string
		resultRefRaw    sql.NullString
		parentID        sql.NullString
		createdAtRaw    string
		updatedAtRaw    string
	)
	if err := row.Scan(&task.ID, &task.TeamID, &parentID, &task.Title, &task.Goal, &status, &task.Priority, &assigneeRaw, &leaseUntilRaw, &task.RetryCount, &inputsJSON, &readJSON, &writeJSON, &deliverableJSON, &task.Summary, &resultRefRaw, &task.Version, &createdAtRaw, &updatedAtRaw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load task: %w", err)
	}
	if parentID.Valid {
		parent := parentID.String
		task.ParentTaskID = &parent
	}
	task.Status = TaskStatus(status)
	if assigneeRaw.Valid {
		assignee := assigneeRaw.String
		task.Assignee = &assignee
	}
	if leaseUntilRaw.Valid {
		lease := parseTime(leaseUntilRaw.String)
		task.LeaseUntil = &lease
	}
	if resultRefRaw.Valid {
		resultRef := resultRefRaw.String
		task.ResultRef = &resultRef
	}
	task.Inputs = decodeStringSlice(inputsJSON)
	task.ReadPaths = decodeStringSlice(readJSON)
	task.WritePaths = decodeStringSlice(writeJSON)
	task.Deliverables = decodeStringSlice(deliverableJSON)
	task.CreatedAt = parseTime(createdAtRaw)
	task.UpdatedAt = parseTime(updatedAtRaw)
	return &task, nil
}

// ListTasks returns tasks matching the filter.
func (s *SQLiteStore) ListTasks(ctx context.Context, filter TaskFilter) ([]Task, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	query := strings.Builder{}
	query.WriteString(`
		SELECT id, team_id, parent_task_id, title, goal, status, priority, assignee, lease_until, retry_count,
			inputs_json, read_paths_json, write_paths_json, deliverables_json, summary, result_ref, version, created_at, updated_at
		FROM team_tasks
	`)
	clauses := make([]string, 0)
	args := make([]interface{}, 0)
	if strings.TrimSpace(filter.TeamID) != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID)
	}
	if len(filter.Status) > 0 {
		placeholders := make([]string, 0, len(filter.Status))
		for _, status := range filter.Status {
			placeholders = append(placeholders, "?")
			args = append(args, string(status))
		}
		clauses = append(clauses, fmt.Sprintf("status IN (%s)", strings.Join(placeholders, ",")))
	}
	if filter.Assignee != nil {
		clauses = append(clauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}
	if filter.ParentTaskID != nil {
		parentID := strings.TrimSpace(*filter.ParentTaskID)
		if parentID == "" {
			clauses = append(clauses, "parent_task_id IS NULL")
		} else {
			clauses = append(clauses, "parent_task_id = ?")
			args = append(args, parentID)
		}
	}
	if len(clauses) > 0 {
		query.WriteString(" WHERE ")
		query.WriteString(strings.Join(clauses, " AND "))
	}
	query.WriteString(" ORDER BY priority DESC, created_at ASC")
	if filter.Limit > 0 {
		query.WriteString(" LIMIT ?")
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]Task, 0)
	for rows.Next() {
		var (
			task            Task
			status          string
			assigneeRaw     sql.NullString
			leaseUntilRaw   sql.NullString
			inputsJSON      string
			readJSON        string
			writeJSON       string
			deliverableJSON string
			resultRefRaw    sql.NullString
			parentID        sql.NullString
			createdAtRaw    string
			updatedAtRaw    string
		)
		if err := rows.Scan(&task.ID, &task.TeamID, &parentID, &task.Title, &task.Goal, &status, &task.Priority, &assigneeRaw, &leaseUntilRaw, &task.RetryCount, &inputsJSON, &readJSON, &writeJSON, &deliverableJSON, &task.Summary, &resultRefRaw, &task.Version, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		if parentID.Valid {
			parent := parentID.String
			task.ParentTaskID = &parent
		}
		task.Status = TaskStatus(status)
		if assigneeRaw.Valid {
			assignee := assigneeRaw.String
			task.Assignee = &assignee
		}
		if leaseUntilRaw.Valid {
			lease := parseTime(leaseUntilRaw.String)
			task.LeaseUntil = &lease
		}
		if resultRefRaw.Valid {
			resultRef := resultRefRaw.String
			task.ResultRef = &resultRef
		}
		task.Inputs = decodeStringSlice(inputsJSON)
		task.ReadPaths = decodeStringSlice(readJSON)
		task.WritePaths = decodeStringSlice(writeJSON)
		task.Deliverables = decodeStringSlice(deliverableJSON)
		task.CreatedAt = parseTime(createdAtRaw)
		task.UpdatedAt = parseTime(updatedAtRaw)
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// UpdateTask updates a task record.
func (s *SQLiteStore) UpdateTask(ctx context.Context, task Task) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if task.ID == "" {
		return fmt.Errorf("task id is required")
	}
	readJSON, err := encodeStringSlice(task.ReadPaths)
	if err != nil {
		return err
	}
	inputsJSON, err := encodeStringSlice(task.Inputs)
	if err != nil {
		return err
	}
	writeJSON, err := encodeStringSlice(task.WritePaths)
	if err != nil {
		return err
	}
	deliverableJSON, err := encodeStringSlice(task.Deliverables)
	if err != nil {
		return err
	}
	task.UpdatedAt = time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		UPDATE team_tasks
		SET team_id = ?, parent_task_id = ?, title = ?, goal = ?, status = ?, priority = ?, assignee = ?, lease_until = ?, retry_count = ?,
			inputs_json = ?, read_paths_json = ?, write_paths_json = ?, deliverables_json = ?, summary = ?, result_ref = ?, version = ?, updated_at = ?
		WHERE id = ?
	`, task.TeamID, nullableString(task.ParentTaskID), task.Title, task.Goal, string(task.Status), task.Priority, nullableString(task.Assignee), nullableTime(task.LeaseUntil), task.RetryCount, inputsJSON, readJSON, writeJSON, deliverableJSON, task.Summary, nullableString(task.ResultRef), task.Version, formatTime(task.UpdatedAt), task.ID)
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	return nil
}

// UpdateTaskStatus updates a task status and summary.
func (s *SQLiteStore) UpdateTaskStatus(ctx context.Context, id string, status TaskStatus, summary string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE team_tasks SET status = ?, summary = ?, updated_at = ? WHERE id = ?
	`, string(status), summary, formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("update task status: %w", err)
	}
	return nil
}

// IncrementTaskRetry increments the retry counter for a task.
func (s *SQLiteStore) IncrementTaskRetry(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE team_tasks SET retry_count = retry_count + 1, updated_at = ? WHERE id = ?
	`, formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("increment task retry: %w", err)
	}
	return nil
}

// MarkReadyTasks promotes pending tasks whose dependencies are satisfied.
func (s *SQLiteStore) MarkReadyTasks(ctx context.Context, teamID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE team_tasks
		SET status = ?, updated_at = ?
		WHERE team_id = ? AND status = ?
		  AND NOT EXISTS (
			SELECT 1 FROM team_task_dependencies d
			JOIN team_tasks t ON t.id = d.depends_on_id
			WHERE d.task_id = team_tasks.id AND t.status != ?
		  )
	`, string(TaskStatusReady), formatTime(time.Now().UTC()), teamID, string(TaskStatusPending), string(TaskStatusDone))
	if err != nil {
		return 0, fmt.Errorf("mark ready tasks: %w", err)
	}
	rows, _ := result.RowsAffected()
	return rows, nil
}

// ClaimTask attempts to claim a ready task.
func (s *SQLiteStore) ClaimTask(ctx context.Context, id string, assignee string, leaseUntil time.Time, expectedVersion int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("team store is not initialized")
	}
	if id == "" || assignee == "" {
		return false, fmt.Errorf("task id and assignee are required")
	}
	query := `
		UPDATE team_tasks
		SET status = ?, assignee = ?, lease_until = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND status = ?
	`
	args := []interface{}{string(TaskStatusRunning), assignee, formatTime(leaseUntil), formatTime(time.Now().UTC()), id, string(TaskStatusReady)}
	if expectedVersion > 0 {
		query += " AND version = ?"
		args = append(args, expectedVersion)
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("claim task: %w", err)
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

// ClaimTaskWithPathClaims atomically claims a task and records any path claims.
func (s *SQLiteStore) ClaimTaskWithPathClaims(ctx context.Context, task Task, assignee string, leaseUntil time.Time, workspaceRoot string) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("team store is not initialized")
	}
	taskID := strings.TrimSpace(task.ID)
	teamID := strings.TrimSpace(task.TeamID)
	assignee = strings.TrimSpace(assignee)
	if taskID == "" || teamID == "" || assignee == "" {
		return false, fmt.Errorf("task id, team id, and assignee are required")
	}
	if leaseUntil.IsZero() {
		leaseUntil = time.Now().UTC().Add(5 * time.Minute)
	}
	requestedReads := normalizePaths(workspaceRoot, task.ReadPaths)
	requestedWrites := normalizePaths(workspaceRoot, task.WritePaths)
	now := time.Now().UTC()
	claimed := false

	err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		// Drop expired claims before checking overlap so stale rows do not block assignment.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM team_path_claims
			WHERE team_id = ? AND lease_until < ?
		`, teamID, formatTime(now)); err != nil {
			return fmt.Errorf("delete expired path claims: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM team_path_claims
			WHERE task_id = ?
		`, taskID); err != nil {
			return fmt.Errorf("delete existing task path claims: %w", err)
		}

		existingClaims, err := listPathClaimsTx(ctx, tx, teamID)
		if err != nil {
			return err
		}
		if hasPathClaimConflict(existingClaims, workspaceRoot, requestedReads, requestedWrites) {
			return nil
		}

		query := `
			UPDATE team_tasks
			SET status = ?, assignee = ?, lease_until = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND status = ?
		`
		args := []interface{}{string(TaskStatusRunning), assignee, formatTime(leaseUntil), formatTime(now), taskID, string(TaskStatusReady)}
		if task.Version > 0 {
			query += " AND version = ?"
			args = append(args, task.Version)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("claim task with path claims: %w", err)
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return nil
		}
		claimed = true

		if _, err := tx.ExecContext(ctx, `
			UPDATE teammates SET state = ?, updated_at = ? WHERE id = ?
		`, string(TeammateStateBusy), formatTime(now), assignee); err != nil {
			return fmt.Errorf("update teammate state: %w", err)
		}

		claims := buildPathClaims(teamID, taskID, assignee, requestedReads, requestedWrites, leaseUntil)
		for _, claim := range claims {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO team_path_claims (
					id, team_id, task_id, owner_agent_id, path, mode, lease_until
				) VALUES (?, ?, ?, ?, ?, ?, ?)
			`, claim.ID, claim.TeamID, claim.TaskID, claim.OwnerAgentID, claim.Path, string(claim.Mode), formatTime(claim.LeaseUntil)); err != nil {
				return fmt.Errorf("insert path claim: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return claimed, nil
}

// RenewTaskLease extends a running task lease.
func (s *SQLiteStore) RenewTaskLease(ctx context.Context, id string, leaseUntil time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE team_tasks SET lease_until = ?, updated_at = ? WHERE id = ?
	`, formatTime(leaseUntil), formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("renew task lease: %w", err)
	}
	return nil
}

// ReleaseTask releases a task lease and updates its status.
func (s *SQLiteStore) ReleaseTask(ctx context.Context, id string, status TaskStatus) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE team_tasks SET status = ?, assignee = NULL, lease_until = NULL, updated_at = ? WHERE id = ?
	`, string(status), formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("release task: %w", err)
	}
	return nil
}

// BlockTask marks a task as blocked while preserving ownership context.
func (s *SQLiteStore) BlockTask(ctx context.Context, id string, summary string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("task id is required")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE team_tasks
		SET status = ?, summary = ?, lease_until = NULL, version = version + 1, updated_at = ?
		WHERE id = ?
	`, string(TaskStatusBlocked), strings.TrimSpace(summary), formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("block task: %w", err)
	}
	return nil
}

// AddTaskDependency registers a dependency edge.
func (s *SQLiteStore) AddTaskDependency(ctx context.Context, taskID, dependsOnID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if taskID == "" || dependsOnID == "" {
		return fmt.Errorf("task dependency ids are required")
	}
	id := "dep_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO team_task_dependencies (id, task_id, depends_on_id, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(task_id, depends_on_id) DO NOTHING
	`, id, taskID, dependsOnID, formatTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("insert dependency: %w", err)
	}
	return nil
}

// ListTaskDependencies returns dependency ids for a task.
func (s *SQLiteStore) ListTaskDependencies(ctx context.Context, taskID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT depends_on_id FROM team_task_dependencies WHERE task_id = ?
	`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list dependencies: %w", err)
	}
	defer rows.Close()
	deps := make([]string, 0)
	for rows.Next() {
		var dep string
		if err := rows.Scan(&dep); err != nil {
			return nil, fmt.Errorf("scan dependency: %w", err)
		}
		deps = append(deps, dep)
	}
	return deps, rows.Err()
}

// ListTaskDependents returns task ids that depend on the given task.
func (s *SQLiteStore) ListTaskDependents(ctx context.Context, taskID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id FROM team_task_dependencies WHERE depends_on_id = ?
	`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list dependents: %w", err)
	}
	defer rows.Close()
	dependents := make([]string, 0)
	for rows.Next() {
		var dependent string
		if err := rows.Scan(&dependent); err != nil {
			return nil, fmt.Errorf("scan dependent: %w", err)
		}
		dependents = append(dependents, dependent)
	}
	return dependents, rows.Err()
}

// InsertMail inserts a mailbox message.
func (s *SQLiteStore) InsertMail(ctx context.Context, message MailMessage) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("team store is not initialized")
	}
	if message.ID == "" {
		message.ID = "mail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	metadataJSON, err := encodeMetadata(message.Metadata)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO team_mailbox_messages (
			id, team_id, from_agent, to_agent, task_id, kind, body, metadata_json, created_at, acked_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, message.ID, message.TeamID, message.FromAgent, message.ToAgent, nullableString(message.TaskID), message.Kind, message.Body, metadataJSON, formatTime(message.CreatedAt), nullableTime(message.AckedAt))
	if err != nil {
		return "", fmt.Errorf("insert mail: %w", err)
	}
	return message.ID, nil
}

// ListMail returns mailbox messages matching the filter.
func (s *SQLiteStore) ListMail(ctx context.Context, filter MailFilter) ([]MailMessage, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	query := strings.Builder{}
	query.WriteString(`
		SELECT id, team_id, from_agent, to_agent, task_id, kind, body, metadata_json, created_at, acked_at
		FROM team_mailbox_messages
	`)
	clauses := make([]string, 0)
	args := make([]interface{}, 0)
	if strings.TrimSpace(filter.TeamID) != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID)
	}
	if strings.TrimSpace(filter.FromAgent) != "" {
		clauses = append(clauses, "from_agent = ?")
		args = append(args, strings.TrimSpace(filter.FromAgent))
	}
	if strings.TrimSpace(filter.ToAgent) != "" {
		toAgent := strings.TrimSpace(filter.ToAgent)
		if filter.IncludeBroadcast {
			clauses = append(clauses, "(to_agent = ? OR to_agent = ?)")
			args = append(args, toAgent, "*")
		} else {
			clauses = append(clauses, "to_agent = ?")
			args = append(args, toAgent)
		}
	}
	if strings.TrimSpace(filter.TaskID) != "" {
		clauses = append(clauses, "task_id = ?")
		args = append(args, strings.TrimSpace(filter.TaskID))
	}
	if strings.TrimSpace(filter.Kind) != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, strings.TrimSpace(filter.Kind))
	}
	if filter.UnreadOnly {
		if toAgent := strings.TrimSpace(filter.ToAgent); toAgent != "" {
			clauses = append(clauses, `
				NOT EXISTS (
					SELECT 1
					FROM team_mailbox_receipts receipts
					WHERE receipts.team_id = team_mailbox_messages.team_id
					  AND receipts.message_id = team_mailbox_messages.id
					  AND receipts.agent_id IN (?, ?)
				)
			`)
			args = append(args, toAgent, globalMailReceiptAgent)
		} else {
			clauses = append(clauses, "acked_at IS NULL")
		}
	}
	if filter.Since != nil {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, formatTime(*filter.Since))
	}
	if len(clauses) > 0 {
		query.WriteString(" WHERE ")
		query.WriteString(strings.Join(clauses, " AND "))
	}
	query.WriteString(" ORDER BY created_at DESC")
	if filter.Limit > 0 {
		query.WriteString(" LIMIT ?")
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list mail: %w", err)
	}
	defer rows.Close()

	messages := make([]MailMessage, 0)
	for rows.Next() {
		var (
			msg         MailMessage
			metadataRaw string
			createdRaw  string
			ackedRaw    sql.NullString
			taskIDRaw   sql.NullString
		)
		if err := rows.Scan(&msg.ID, &msg.TeamID, &msg.FromAgent, &msg.ToAgent, &taskIDRaw, &msg.Kind, &msg.Body, &metadataRaw, &createdRaw, &ackedRaw); err != nil {
			return nil, fmt.Errorf("scan mail: %w", err)
		}
		if taskIDRaw.Valid {
			value := taskIDRaw.String
			msg.TaskID = &value
		}
		msg.Metadata = decodeMetadata(metadataRaw)
		msg.CreatedAt = parseTime(createdRaw)
		if ackedRaw.Valid {
			ackedAt := parseTime(ackedRaw.String)
			msg.AckedAt = &ackedAt
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

// AckMail marks a mailbox message as acknowledged.
func (s *SQLiteStore) AckMail(ctx context.Context, teamID, messageID string, ackedAt time.Time) error {
	return s.RecordMailReceipt(ctx, MailReceipt{
		MessageID: strings.TrimSpace(messageID),
		TeamID:    strings.TrimSpace(teamID),
		AgentID:   globalMailReceiptAgent,
		AckedAt:   ackedAt,
	})
}

// RecordMailReceipt marks a mailbox message as acknowledged for a specific agent.
func (s *SQLiteStore) RecordMailReceipt(ctx context.Context, receipt MailReceipt) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	receipt.TeamID = strings.TrimSpace(receipt.TeamID)
	receipt.MessageID = strings.TrimSpace(receipt.MessageID)
	receipt.AgentID = strings.TrimSpace(receipt.AgentID)
	if receipt.TeamID == "" || receipt.MessageID == "" {
		return fmt.Errorf("team id and message id are required")
	}
	if receipt.AgentID == "" {
		receipt.AgentID = globalMailReceiptAgent
	}
	if receipt.AckedAt.IsZero() {
		receipt.AckedAt = time.Now().UTC()
	}
	return s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		ackValue := interface{}(nil)
		if receipt.AgentID == globalMailReceiptAgent {
			ackValue = formatTime(receipt.AckedAt)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE team_mailbox_messages
			SET acked_at = COALESCE(?, acked_at)
			WHERE id = ? AND team_id = ?
		`, ackValue, receipt.MessageID, receipt.TeamID)
		if err != nil {
			return fmt.Errorf("update mail acked_at: %w", err)
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return fmt.Errorf("mail not found")
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO team_mailbox_receipts (message_id, team_id, agent_id, acked_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(team_id, message_id, agent_id) DO UPDATE SET
				acked_at = excluded.acked_at
		`, receipt.MessageID, receipt.TeamID, receipt.AgentID, formatTime(receipt.AckedAt))
		if err != nil {
			return fmt.Errorf("record mail receipt: %w", err)
		}
		return nil
	})
}

// ListMailReceipts returns recorded receipts for a message.
func (s *SQLiteStore) ListMailReceipts(ctx context.Context, teamID, messageID string) ([]MailReceipt, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT message_id, team_id, agent_id, acked_at
		FROM team_mailbox_receipts
		WHERE team_id = ? AND message_id = ?
		ORDER BY acked_at ASC, agent_id ASC
	`, strings.TrimSpace(teamID), strings.TrimSpace(messageID))
	if err != nil {
		return nil, fmt.Errorf("list mail receipts: %w", err)
	}
	defer rows.Close()

	receipts := make([]MailReceipt, 0)
	for rows.Next() {
		var (
			receipt  MailReceipt
			ackedRaw string
		)
		if err := rows.Scan(&receipt.MessageID, &receipt.TeamID, &receipt.AgentID, &ackedRaw); err != nil {
			return nil, fmt.Errorf("scan mail receipt: %w", err)
		}
		receipt.AckedAt = parseTime(ackedRaw)
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}

// ListPathClaims returns path claims for a team.
func (s *SQLiteStore) ListPathClaims(ctx context.Context, teamID string) ([]PathClaim, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, team_id, task_id, owner_agent_id, path, mode, lease_until
		FROM team_path_claims WHERE team_id = ?
	`, teamID)
	if err != nil {
		return nil, fmt.Errorf("list path claims: %w", err)
	}
	defer rows.Close()

	claims := make([]PathClaim, 0)
	for rows.Next() {
		var (
			claim    PathClaim
			mode     string
			leaseRaw string
		)
		if err := rows.Scan(&claim.ID, &claim.TeamID, &claim.TaskID, &claim.OwnerAgentID, &claim.Path, &mode, &leaseRaw); err != nil {
			return nil, fmt.Errorf("scan path claim: %w", err)
		}
		claim.Mode = PathClaimMode(mode)
		claim.LeaseUntil = parseTime(leaseRaw)
		claims = append(claims, claim)
	}
	return claims, rows.Err()
}

// CreatePathClaims stores path claims in a single transaction.
func (s *SQLiteStore) CreatePathClaims(ctx context.Context, claims []PathClaim) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if len(claims) == 0 {
		return nil
	}
	return s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		for i := range claims {
			claim := claims[i]
			if claim.ID == "" {
				claim.ID = "claim_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			}
			if claim.LeaseUntil.IsZero() {
				claim.LeaseUntil = time.Now().UTC().Add(5 * time.Minute)
			}
			_, err := tx.ExecContext(ctx, `
				INSERT INTO team_path_claims (
					id, team_id, task_id, owner_agent_id, path, mode, lease_until
				) VALUES (?, ?, ?, ?, ?, ?, ?)
			`, claim.ID, claim.TeamID, claim.TaskID, claim.OwnerAgentID, claim.Path, string(claim.Mode), formatTime(claim.LeaseUntil))
			if err != nil {
				return fmt.Errorf("insert path claim: %w", err)
			}
			claims[i] = claim
		}
		return nil
	})
}

// ReleasePathClaimsByTask deletes claims for a task.
func (s *SQLiteStore) ReleasePathClaimsByTask(ctx context.Context, taskID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM team_path_claims WHERE task_id = ?`, taskID)
	if err != nil {
		return fmt.Errorf("release path claims: %w", err)
	}
	return nil
}

// RenewPathClaimsByTask extends leases for a task's claims.
func (s *SQLiteStore) RenewPathClaimsByTask(ctx context.Context, taskID string, leaseUntil time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE team_path_claims SET lease_until = ? WHERE task_id = ?
	`, formatTime(leaseUntil), taskID)
	if err != nil {
		return fmt.Errorf("renew path claims: %w", err)
	}
	return nil
}

// DeleteExpiredPathClaims removes claims older than the provided timestamp.
func (s *SQLiteStore) DeleteExpiredPathClaims(ctx context.Context, teamID string, asOf time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	if strings.TrimSpace(teamID) == "" {
		return 0, fmt.Errorf("team id is required")
	}
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM team_path_claims
		WHERE team_id = ? AND lease_until < ?
	`, teamID, formatTime(asOf))
	if err != nil {
		return 0, fmt.Errorf("delete expired path claims: %w", err)
	}
	affected, _ := result.RowsAffected()
	return affected, nil
}

// AppendTeamEvent stores a team event and returns its sequence number.
func (s *SQLiteStore) AppendTeamEvent(ctx context.Context, event TeamEvent) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	teamID := strings.TrimSpace(event.TeamID)
	if teamID == "" {
		return 0, fmt.Errorf("team id is required")
	}
	eventType := strings.TrimSpace(event.Type)
	if eventType == "" {
		return 0, fmt.Errorf("event type is required")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	payloadJSON, err := encodeMetadata(event.Payload)
	if err != nil {
		return 0, err
	}

	var seq int64
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(seq), 0) + 1 FROM team_events WHERE team_id = ?
		`, teamID).Scan(&seq); err != nil {
			return fmt.Errorf("next team event seq: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_events (team_id, seq, type, payload_json, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, teamID, seq, eventType, payloadJSON, formatTime(event.Timestamp)); err != nil {
			return fmt.Errorf("insert team event: %w", err)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return seq, nil
}

// ListTeamEvents returns persisted team events after the given sequence.
func (s *SQLiteStore) ListTeamEvents(ctx context.Context, filter TeamEventFilter) ([]TeamEventRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	teamID := strings.TrimSpace(filter.TeamID)
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	afterSeq := filter.AfterSeq
	if afterSeq < 0 {
		afterSeq = 0
	}

	clauses := []string{"team_id = ?", "seq > ?"}
	args := []interface{}{teamID, afterSeq}
	if eventType := strings.TrimSpace(filter.EventType); eventType != "" {
		if strings.Contains(eventType, "*") {
			pattern := strings.ReplaceAll(eventType, "*", "%")
			clauses = append(clauses, "type LIKE ?")
			args = append(args, pattern)
		} else {
			clauses = append(clauses, "type = ?")
			args = append(args, eventType)
		}
	}
	if filter.Since != nil && !filter.Since.IsZero() {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, formatTime(*filter.Since))
	}
	if filter.Until != nil && !filter.Until.IsZero() {
		clauses = append(clauses, "created_at <= ?")
		args = append(args, formatTime(*filter.Until))
	}

	query := `
		SELECT seq, type, payload_json, created_at
		FROM team_events
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY seq ASC
	`
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list team events: %w", err)
	}
	defer rows.Close()

	events := make([]TeamEventRecord, 0)
	for rows.Next() {
		var (
			seq        int64
			eventType  string
			payloadRaw string
			createdRaw string
		)
		if err := rows.Scan(&seq, &eventType, &payloadRaw, &createdRaw); err != nil {
			return nil, fmt.Errorf("scan team event: %w", err)
		}
		events = append(events, TeamEventRecord{
			Seq: seq,
			TeamEvent: TeamEvent{
				Type:      eventType,
				TeamID:    teamID,
				Payload:   decodeMetadata(payloadRaw),
				Timestamp: parseTime(createdRaw),
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (s *SQLiteStore) init(ctx context.Context) error {
	migrations := []migrate.Migration{
		{
			Version: 1,
			Name:    "team_core",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS teams (
					id TEXT PRIMARY KEY,
					workspace_id TEXT,
					lead_session_id TEXT,
					status TEXT NOT NULL,
					strategy TEXT,
					max_teammates INTEGER NOT NULL DEFAULT 0,
					max_writers INTEGER NOT NULL DEFAULT 0,
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL
				);
				CREATE TABLE IF NOT EXISTS teammates (
					id TEXT PRIMARY KEY,
					team_id TEXT NOT NULL,
					name TEXT,
					profile TEXT,
					session_id TEXT,
					state TEXT NOT NULL,
					last_heartbeat TEXT,
					capabilities_json TEXT NOT NULL DEFAULT '[]',
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL
				);
				CREATE TABLE IF NOT EXISTS team_tasks (
					id TEXT PRIMARY KEY,
					team_id TEXT NOT NULL,
					parent_task_id TEXT,
					title TEXT,
					goal TEXT,
					status TEXT NOT NULL,
					priority INTEGER NOT NULL DEFAULT 0,
					assignee TEXT,
					lease_until TEXT,
					retry_count INTEGER NOT NULL DEFAULT 0,
					read_paths_json TEXT NOT NULL DEFAULT '[]',
					write_paths_json TEXT NOT NULL DEFAULT '[]',
					deliverables_json TEXT NOT NULL DEFAULT '[]',
					summary TEXT,
					version INTEGER NOT NULL DEFAULT 1,
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL
				);
				CREATE TABLE IF NOT EXISTS team_task_dependencies (
					id TEXT PRIMARY KEY,
					task_id TEXT NOT NULL,
					depends_on_id TEXT NOT NULL,
					created_at TEXT NOT NULL,
					UNIQUE(task_id, depends_on_id)
				);
				CREATE TABLE IF NOT EXISTS team_mailbox_messages (
					id TEXT PRIMARY KEY,
					team_id TEXT NOT NULL,
					from_agent TEXT,
					to_agent TEXT,
					task_id TEXT,
					kind TEXT,
					body TEXT NOT NULL,
					metadata_json TEXT NOT NULL DEFAULT '{}',
					created_at TEXT NOT NULL,
					acked_at TEXT
				);
				CREATE TABLE IF NOT EXISTS team_path_claims (
					id TEXT PRIMARY KEY,
					team_id TEXT NOT NULL,
					task_id TEXT NOT NULL,
					owner_agent_id TEXT NOT NULL,
					path TEXT NOT NULL,
					mode TEXT NOT NULL,
					lease_until TEXT NOT NULL
				);
			`,
		},
		{
			Version: 2,
			Name:    "team_indexes",
			UpSQL: `
				CREATE INDEX IF NOT EXISTS idx_team_tasks_status ON team_tasks(team_id, status, priority DESC);
				CREATE INDEX IF NOT EXISTS idx_teammates_team_state ON teammates(team_id, state);
				CREATE INDEX IF NOT EXISTS idx_mailbox_team_to ON team_mailbox_messages(team_id, to_agent, created_at DESC);
				CREATE INDEX IF NOT EXISTS idx_path_claims_team ON team_path_claims(team_id, lease_until);
				CREATE INDEX IF NOT EXISTS idx_task_dependencies_task ON team_task_dependencies(task_id);
			`,
		},
		{
			Version: 3,
			Name:    "team_events",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS team_events (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					team_id TEXT NOT NULL,
					seq INTEGER NOT NULL,
					type TEXT NOT NULL,
					payload_json TEXT NOT NULL DEFAULT '{}',
					created_at TEXT NOT NULL,
					UNIQUE(team_id, seq)
				);
				CREATE INDEX IF NOT EXISTS idx_team_events_team_seq
				ON team_events(team_id, seq);
			`,
		},
		{
			Version: 4,
			Name:    "team_task_result_ref",
			UpSQL: `
				ALTER TABLE team_tasks ADD COLUMN result_ref TEXT;
			`,
		},
		{
			Version: 5,
			Name:    "team_task_inputs",
			UpSQL: `
				ALTER TABLE team_tasks ADD COLUMN inputs_json TEXT NOT NULL DEFAULT '[]';
			`,
		},
		{
			Version: 6,
			Name:    "team_mailbox_receipts",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS team_mailbox_receipts (
					message_id TEXT NOT NULL,
					team_id TEXT NOT NULL,
					agent_id TEXT NOT NULL,
					acked_at TEXT NOT NULL,
					PRIMARY KEY (team_id, message_id, agent_id)
				);
				CREATE INDEX IF NOT EXISTS idx_mailbox_receipts_message
				ON team_mailbox_receipts(team_id, message_id, acked_at);
			`,
		},
	}
	return migrate.Apply(ctx, s.db, migrations)
}

func resolveDSN(cfg *StoreConfig) (string, error) {
	if cfg.Path != "" {
		dir := filepath.Dir(cfg.Path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("create team store directory: %w", err)
			}
		}
		return ensureSQLiteDSNOptions(cfg.Path), nil
	}
	if cfg.DSN != "" {
		return ensureSQLiteDSNOptions(cfg.DSN), nil
	}
	return ensureSQLiteDSNOptions(fmt.Sprintf("file:team-store-%s?mode=memory&cache=shared", uuid.NewString())), nil
}

func ensureSQLiteDSNOptions(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return dsn
	}
	dsn = ensureSQLiteDSNOption(dsn, "_txlock", "immediate")
	dsn = ensureSQLiteDSNOption(dsn, "_busy_timeout", "5000")
	return dsn
}

func ensureSQLiteDSNOption(dsn, key, value string) string {
	lower := strings.ToLower(dsn)
	token := strings.ToLower(key) + "="
	if strings.Contains(lower, token) {
		return dsn
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + key + "=" + value
}

func listPathClaimsTx(ctx context.Context, tx *sql.Tx, teamID string) ([]PathClaim, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, team_id, task_id, owner_agent_id, path, mode, lease_until
		FROM team_path_claims
		WHERE team_id = ?
	`, teamID)
	if err != nil {
		return nil, fmt.Errorf("list path claims: %w", err)
	}
	defer rows.Close()

	claims := make([]PathClaim, 0)
	for rows.Next() {
		var (
			claim    PathClaim
			mode     string
			leaseRaw string
		)
		if err := rows.Scan(&claim.ID, &claim.TeamID, &claim.TaskID, &claim.OwnerAgentID, &claim.Path, &mode, &leaseRaw); err != nil {
			return nil, fmt.Errorf("scan path claim: %w", err)
		}
		claim.Mode = PathClaimMode(mode)
		claim.LeaseUntil = parseTime(leaseRaw)
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return claims, nil
}

func hasPathClaimConflict(existingClaims []PathClaim, workspaceRoot string, requestedReads, requestedWrites []string) bool {
	if len(existingClaims) == 0 || (len(requestedReads) == 0 && len(requestedWrites) == 0) {
		return false
	}
	now := time.Now().UTC()
	for _, claim := range existingClaims {
		if !claim.LeaseUntil.IsZero() && claim.LeaseUntil.Before(now) {
			continue
		}
		existingPath := normalizePath(workspaceRoot, claim.Path)
		if existingPath == "" {
			continue
		}
		for _, path := range requestedReads {
			if path == "" || !pathsOverlap(path, existingPath) {
				continue
			}
			if claim.Mode == PathClaimWrite {
				return true
			}
		}
		for _, path := range requestedWrites {
			if path == "" || !pathsOverlap(path, existingPath) {
				continue
			}
			return true
		}
	}
	return false
}

func buildPathClaims(teamID, taskID, ownerAgentID string, readPaths, writePaths []string, leaseUntil time.Time) []PathClaim {
	claims := make([]PathClaim, 0, len(readPaths)+len(writePaths))
	for _, path := range readPaths {
		if path == "" {
			continue
		}
		claims = append(claims, PathClaim{
			ID:           "claim_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			TeamID:       teamID,
			TaskID:       taskID,
			OwnerAgentID: ownerAgentID,
			Path:         path,
			Mode:         PathClaimRead,
			LeaseUntil:   leaseUntil,
		})
	}
	for _, path := range writePaths {
		if path == "" {
			continue
		}
		claims = append(claims, PathClaim{
			ID:           "claim_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			TeamID:       teamID,
			TaskID:       taskID,
			OwnerAgentID: ownerAgentID,
			Path:         path,
			Mode:         PathClaimWrite,
			LeaseUntil:   leaseUntil,
		})
	}
	return claims
}

func encodeStringSlice(values []string) (string, error) {
	if len(values) == 0 {
		return "[]", nil
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("marshal string slice: %w", err)
	}
	return string(payload), nil
}

func decodeStringSlice(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return values
}

func encodeMetadata(metadata map[string]interface{}) (string, error) {
	if len(metadata) == 0 {
		return "{}", nil
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("marshal metadata: %w", err)
	}
	return string(payload), nil
}

func decodeMetadata(raw string) map[string]interface{} {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return nil
	}
	return metadata
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(raw string) time.Time {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func nullableString(value *string) interface{} {
	if value == nil {
		return nil
	}
	if strings.TrimSpace(*value) == "" {
		return nil
	}
	return *value
}

func nullableTime(value *time.Time) interface{} {
	if value == nil {
		return nil
	}
	if value.IsZero() {
		return nil
	}
	return formatTime(*value)
}
