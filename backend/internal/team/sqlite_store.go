package team

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// SQLiteStore persists team data in SQLite.
type SQLiteStore struct {
	db *sql.DB

	globalMailboxWriterMu sync.RWMutex
	globalMailboxWriter   agentcontrol.GlobalMailboxWriter

	mailboxWatchMu     sync.Mutex
	nextMailboxWatchID int64
	mailboxWatchers    map[int64]mailboxWatcher

	agentControlMailboxWatchMu     sync.Mutex
	nextAgentControlMailboxWatchID int64
	agentControlMailboxWatchers    map[int64]mailboxWatcher

	agentControlWakeWatchMu     sync.Mutex
	nextAgentControlWakeWatchID int64
	agentControlWakeWatchers    map[int64]agentControlWakeWatcher

	taskSignalWatchMu     sync.Mutex
	nextTaskSignalWatchID int64
	taskSignalWatchers    map[int64]taskSignalWatcher

	agentControlTaskSignalWatchMu     sync.Mutex
	nextAgentControlTaskSignalWatchID int64
	agentControlTaskSignalWatchers    map[int64]taskSignalWatcher
}

// SetGlobalMailboxWriter configures an optional write-through target for the
// durable cross-workflow AgentControl mailbox registry.
func (s *SQLiteStore) SetGlobalMailboxWriter(writer agentcontrol.GlobalMailboxWriter) {
	if s == nil {
		return
	}
	s.globalMailboxWriterMu.Lock()
	s.globalMailboxWriter = writer
	s.globalMailboxWriterMu.Unlock()
}

// AgentControlMailboxProjectionStatus reports the team store's local to global
// AgentControl mailbox projection semantics.
func (s *SQLiteStore) AgentControlMailboxProjectionStatus() agentcontrol.MailboxProjectionStatus {
	status := agentcontrol.MailboxProjectionStatus{Store: "team_sqlite"}
	if s == nil || s.db == nil {
		status.Mode = agentcontrol.MailboxProjectionModeLocalOnly
		status.Reason = "store_not_configured"
		return status.Normalize()
	}
	s.globalMailboxWriterMu.RLock()
	writer := s.globalMailboxWriter
	s.globalMailboxWriterMu.RUnlock()
	if writer == nil {
		status.Mode = agentcontrol.MailboxProjectionModeLocalOnly
		status.Reason = "global_writer_not_configured"
		return status.Normalize()
	}
	if txWriter, ok := writer.(agentcontrol.GlobalMailboxSQLiteTxWriter); ok && txWriter != nil {
		if _, ok := txWriter.GlobalMailboxAttachDSN(); ok {
			status.Mode = agentcontrol.MailboxProjectionModeTransactional
			status.Reason = "global_registry_attachable"
			return status.Normalize()
		}
	}
	status.Mode = agentcontrol.MailboxProjectionModeWriteThrough
	status.Reason = "global_registry_not_attachable"
	return status.Normalize()
}

const globalMailReceiptAgent = "*"

type mailboxWatcher struct {
	workflow string
	teamID   string
	ch       chan MailMessage
}

type taskSignalWatcher struct {
	workflow string
	teamID   string
	ch       chan TaskSignal
}

type agentControlWakeWatcher struct {
	workflow string
	kind     string
	teamID   string
	ch       chan agentcontrol.WakeEvent
}

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
	if isSQLiteMemoryDSN(dsn) {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
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
	if err := s.refreshAgentControlTaskAssigneeProjection(ctx, teammate.TeamID, teammate.ID); err != nil {
		return "", err
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
	return s.CreateAgentControlTaskRecord(ctx, task)
}

// CreateAgentControlTaskRecord inserts a task through the AgentControl task
// graph table. Legacy team_tasks is migration-only and is no longer written.
func (s *SQLiteStore) CreateAgentControlTaskRecord(ctx context.Context, task Task) (string, error) {
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
	record, err := s.agentControlTaskRecordForTask(ctx, task)
	if err != nil {
		return "", err
	}
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		return s.insertAgentControlTaskRecordTx(ctx, tx, task, record, false)
	}); err != nil {
		return "", err
	}
	if task.Status != TaskStatusPending {
		_ = s.appendTaskSignal(ctx, TaskSignal{
			TeamID: task.TeamID,
			TaskID: task.ID,
			Kind:   TaskSignalTaskCreated,
			Status: task.Status,
		})
	}
	return task.ID, nil
}

// GetTask loads a task by id.
func (s *SQLiteStore) GetTask(ctx context.Context, id string) (*Task, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if task, err := s.getAgentControlTask(ctx, id); err != nil {
		return nil, err
	} else if task != nil {
		return task, nil
	}
	return nil, nil
}

func (s *SQLiteStore) getAgentControlTask(ctx context.Context, id string) (*Task, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT task_id, team_id, parent_task_id, title, goal, status, priority, assignee, lease_until, retry_count,
			inputs_json, read_paths_json, write_paths_json, deliverables_json, summary, result_ref, version, created_at, updated_at
		FROM agent_control_task_records
		WHERE workflow = ? AND task_id = ?
	`, agentcontrol.WorkflowSpawnTeam, id)
	task, err := scanTaskRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load agent control task: %w", err)
	}
	return task, nil
}

type taskRowScanner interface {
	Scan(dest ...interface{}) error
}

func scanTaskRow(row taskRowScanner) (*Task, error) {
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
		return nil, err
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
	return s.listAgentControlTasks(ctx, filter)
}

func (s *SQLiteStore) listAgentControlTasks(ctx context.Context, filter TaskFilter) ([]Task, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	query := strings.Builder{}
	query.WriteString(`
		SELECT task_id, team_id, parent_task_id, title, goal, status, priority, assignee, lease_until, retry_count,
			inputs_json, read_paths_json, write_paths_json, deliverables_json, summary, result_ref, version, created_at, updated_at
		FROM agent_control_task_records
	`)
	clauses := []string{"workflow = ?"}
	args := []interface{}{agentcontrol.WorkflowSpawnTeam}
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
	query.WriteString(" WHERE ")
	query.WriteString(strings.Join(clauses, " AND "))
	query.WriteString(" ORDER BY priority DESC, created_at ASC")
	if filter.Limit > 0 {
		query.WriteString(" LIMIT ?")
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list agent control tasks: %w", err)
	}
	defer rows.Close()
	tasks := make([]Task, 0)
	for rows.Next() {
		task, err := scanTaskRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent control task: %w", err)
		}
		tasks = append(tasks, *task)
	}
	return tasks, rows.Err()
}

// UpdateTask updates a task record.
func (s *SQLiteStore) UpdateTask(ctx context.Context, task Task) error {
	return s.UpdateAgentControlTaskRecord(ctx, task)
}

// UpdateAgentControlTaskRecord updates the AgentControl task table. Legacy
// team_tasks is migration-only and is no longer synchronized.
func (s *SQLiteStore) UpdateAgentControlTaskRecord(ctx context.Context, task Task) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if strings.TrimSpace(task.ID) == "" {
		return fmt.Errorf("task id is required")
	}
	if task.Status == "" {
		task.Status = TaskStatusPending
	}
	task.UpdatedAt = time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = task.UpdatedAt
	}
	if task.Version == 0 {
		task.Version = 1
	}
	record, err := s.agentControlTaskRecordForTask(ctx, task)
	if err != nil {
		return err
	}
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		return s.insertAgentControlTaskRecordTx(ctx, tx, task, record, true)
	}); err != nil {
		return err
	}
	_ = s.appendTaskSignal(ctx, TaskSignal{
		TeamID: task.TeamID,
		TaskID: task.ID,
		Kind:   TaskSignalTaskUpdated,
		Status: task.Status,
	})
	return nil
}

// UpdateTaskStatus updates a task status and summary.
func (s *SQLiteStore) UpdateTaskStatus(ctx context.Context, id string, status TaskStatus, summary string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	now := time.Now().UTC()
	changed := false
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_control_task_records
			SET status = ?, summary = ?, updated_at = ?
			WHERE workflow = ? AND task_id = ?
		`, string(status), summary, formatTime(now), agentcontrol.WorkflowSpawnTeam, id)
		if err != nil {
			return fmt.Errorf("update agent control task status: %w", err)
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return nil
		}
		changed = true
		return nil
	}); err != nil {
		return err
	}
	if changed {
		_ = s.appendTaskSignalForTask(ctx, id, TaskSignalTaskStatus, status)
	}
	return nil
}

// IncrementTaskRetry increments the retry counter for a task.
func (s *SQLiteStore) IncrementTaskRetry(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	now := time.Now().UTC()
	changed := false
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_control_task_records
			SET retry_count = retry_count + 1, updated_at = ?
			WHERE workflow = ? AND task_id = ?
		`, formatTime(now), agentcontrol.WorkflowSpawnTeam, id)
		if err != nil {
			return fmt.Errorf("increment agent control task retry: %w", err)
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return nil
		}
		changed = true
		return nil
	}); err != nil {
		return err
	}
	if changed {
		_ = s.appendTaskSignalForTask(ctx, id, TaskSignalTaskRetry, "")
	}
	return nil
}

// MarkReadyTasks promotes pending tasks whose dependencies are satisfied.
func (s *SQLiteStore) MarkReadyTasks(ctx context.Context, teamID string) (int64, error) {
	return s.MarkAgentControlTaskRecordsReady(ctx, teamID)
}

// MarkAgentControlTaskRecordsReady promotes dependency-satisfied AgentControl
// task records.
func (s *SQLiteStore) MarkAgentControlTaskRecordsReady(ctx context.Context, teamID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return 0, fmt.Errorf("team id is required")
	}
	now := time.Now().UTC()
	taskIDs := make([]string, 0)
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT task_id
			FROM agent_control_task_records
			WHERE workflow = ? AND team_id = ? AND status = ?
			  AND NOT EXISTS (
				SELECT 1
				FROM agent_control_task_dependencies d
				JOIN agent_control_task_records dep
				  ON dep.workflow = d.workflow AND dep.task_id = d.depends_on_id
				WHERE d.workflow = agent_control_task_records.workflow
				  AND d.task_id = agent_control_task_records.task_id
				  AND dep.status != ?
			  )
			ORDER BY priority DESC, created_at ASC, task_id ASC
		`, agentcontrol.WorkflowSpawnTeam, teamID, string(TaskStatusPending), string(TaskStatusDone))
		if err != nil {
			return fmt.Errorf("select agent control ready tasks: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var taskID string
			if err := rows.Scan(&taskID); err != nil {
				return fmt.Errorf("scan agent control ready task: %w", err)
			}
			taskID = strings.TrimSpace(taskID)
			if taskID != "" {
				taskIDs = append(taskIDs, taskID)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, taskID := range taskIDs {
			if _, err := tx.ExecContext(ctx, `
				UPDATE agent_control_task_records
				SET status = ?, updated_at = ?
				WHERE workflow = ? AND task_id = ?
			`, string(TaskStatusReady), formatTime(now), agentcontrol.WorkflowSpawnTeam, taskID); err != nil {
				return fmt.Errorf("mark agent control task ready: %w", err)
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if len(taskIDs) > 0 {
		_ = s.appendTaskSignal(ctx, TaskSignal{
			TeamID: teamID,
			Kind:   TaskSignalTasksMarkedReady,
			Status: TaskStatusReady,
		})
	}
	return int64(len(taskIDs)), nil
}

// ClaimTask attempts to claim a ready task.
func (s *SQLiteStore) ClaimTask(ctx context.Context, id string, assignee string, leaseUntil time.Time, expectedVersion int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("team store is not initialized")
	}
	id = strings.TrimSpace(id)
	assignee = strings.TrimSpace(assignee)
	if id == "" || assignee == "" {
		return false, fmt.Errorf("task id and assignee are required")
	}
	if leaseUntil.IsZero() {
		leaseUntil = time.Now().UTC().Add(5 * time.Minute)
	}
	existing, err := s.getAgentControlTask(ctx, id)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	mate, err := s.GetTeammate(ctx, assignee)
	if err != nil {
		return false, err
	}
	sessionID, agentPath := agentControlTaskAssigneeProjection(existing.TeamID, assignee, mate)
	now := time.Now().UTC()
	claimed := false
	query := `
		UPDATE agent_control_task_records
		SET status = ?, assignee = ?, session_id = ?, agent_path = ?, lease_until = ?, version = version + 1, updated_at = ?
		WHERE workflow = ? AND task_id = ? AND status = ?
	`
	args := []interface{}{
		string(TaskStatusRunning),
		assignee,
		nullablePlainString(sessionID),
		nullablePlainString(agentPath),
		formatTime(leaseUntil),
		formatTime(now),
		agentcontrol.WorkflowSpawnTeam,
		id,
		string(TaskStatusReady),
	}
	if expectedVersion > 0 {
		query += " AND version = ?"
		args = append(args, expectedVersion)
	}
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("claim agent control task: %w", err)
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return nil
		}
		claimed = true
		return nil
	}); err != nil {
		return false, err
	}
	if claimed {
		_ = s.appendTaskSignalForTask(ctx, id, TaskSignalTaskClaimed, TaskStatusRunning)
	}
	return claimed, nil
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
	mate, err := s.GetTeammate(ctx, assignee)
	if err != nil {
		return false, err
	}
	sessionID, agentPath := agentControlTaskAssigneeProjection(teamID, assignee, mate)
	now := time.Now().UTC()
	claimed := false

	err = s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
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
			UPDATE agent_control_task_records
			SET status = ?, assignee = ?, session_id = ?, agent_path = ?, lease_until = ?, version = version + 1, updated_at = ?
			WHERE workflow = ? AND task_id = ? AND team_id = ? AND status = ?
		`
		args := []interface{}{
			string(TaskStatusRunning),
			assignee,
			nullablePlainString(sessionID),
			nullablePlainString(agentPath),
			formatTime(leaseUntil),
			formatTime(now),
			agentcontrol.WorkflowSpawnTeam,
			taskID,
			teamID,
			string(TaskStatusReady),
		}
		if task.Version > 0 {
			query += " AND version = ?"
			args = append(args, task.Version)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("claim agent control task with path claims: %w", err)
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
	if claimed {
		_ = s.appendTaskSignal(ctx, TaskSignal{
			TeamID: teamID,
			TaskID: taskID,
			Kind:   TaskSignalTaskClaimed,
			Status: TaskStatusRunning,
		})
	}
	return claimed, nil
}

// RenewTaskLease extends a running task lease.
func (s *SQLiteStore) RenewTaskLease(ctx context.Context, id string, leaseUntil time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	now := time.Now().UTC()
	changed := false
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_control_task_records
			SET lease_until = ?, updated_at = ?
			WHERE workflow = ? AND task_id = ?
		`, formatTime(leaseUntil), formatTime(now), agentcontrol.WorkflowSpawnTeam, id)
		if err != nil {
			return fmt.Errorf("renew agent control task lease: %w", err)
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return nil
		}
		changed = true
		return nil
	}); err != nil {
		return err
	}
	if changed {
		_ = s.appendTaskSignalForTask(ctx, id, TaskSignalTaskLeaseRenewed, TaskStatusRunning)
	}
	return nil
}

// ReleaseTask releases a task lease and updates its status.
func (s *SQLiteStore) ReleaseTask(ctx context.Context, id string, status TaskStatus) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	now := time.Now().UTC()
	changed := false
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_control_task_records
			SET status = ?, assignee = NULL, session_id = NULL, agent_path = NULL, lease_until = NULL, updated_at = ?
			WHERE workflow = ? AND task_id = ?
		`, string(status), formatTime(now), agentcontrol.WorkflowSpawnTeam, id)
		if err != nil {
			return fmt.Errorf("release agent control task: %w", err)
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return nil
		}
		changed = true
		return nil
	}); err != nil {
		return err
	}
	if changed {
		_ = s.appendTaskSignalForTask(ctx, id, TaskSignalTaskReleased, status)
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
	now := time.Now().UTC()
	summary = strings.TrimSpace(summary)
	changed := false
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_control_task_records
			SET status = ?, summary = ?, lease_until = NULL, version = version + 1, updated_at = ?
			WHERE workflow = ? AND task_id = ?
		`, string(TaskStatusBlocked), summary, formatTime(now), agentcontrol.WorkflowSpawnTeam, id)
		if err != nil {
			return fmt.Errorf("block agent control task: %w", err)
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return nil
		}
		changed = true
		return nil
	}); err != nil {
		return err
	}
	if changed {
		_ = s.appendTaskSignalForTask(ctx, id, TaskSignalTaskBlocked, TaskStatusBlocked)
	}
	return nil
}

func (s *SQLiteStore) refreshAgentControlTaskAssigneeProjection(ctx context.Context, teamID, assignee string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	teamID = strings.TrimSpace(teamID)
	assignee = strings.TrimSpace(assignee)
	if teamID == "" || assignee == "" {
		return nil
	}
	records, err := s.ListAgentControlTaskRecords(ctx, agentcontrol.TaskFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
		Assignee: assignee,
	})
	if err != nil {
		return err
	}
	for _, record := range records {
		task, err := s.GetTask(ctx, record.ID)
		if err != nil {
			return err
		}
		if task == nil {
			continue
		}
		var mate *Teammate
		if assignee := taskAssigneeID(*task); assignee != "" {
			if teammate, err := s.GetTeammate(ctx, assignee); err == nil && teammate != nil {
				mate = teammate
			} else if err != nil {
				return err
			}
		}
		if err := s.upsertAgentControlTaskRecordValue(ctx, *task, AgentControlTaskRecord(*task, mate)); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) upsertAgentControlTaskRecordValue(ctx context.Context, task Task, record agentcontrol.TaskRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	if task.Status == "" {
		task.Status = TaskStatusPending
	}
	if task.Version == 0 {
		task.Version = 1
	}
	record = record.Normalize()
	if record.ID == "" {
		return nil
	}
	if record.Workflow == "" {
		record.Workflow = agentcontrol.WorkflowSpawnTeam
	}
	return s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		return s.insertAgentControlTaskRecordTx(ctx, tx, task, record, true)
	})
}

func (s *SQLiteStore) insertAgentControlTaskRecordTx(ctx context.Context, tx *sql.Tx, task Task, record agentcontrol.TaskRecord, upsert bool) error {
	record = record.Normalize()
	if record.ID == "" {
		return nil
	}
	inputsJSON, err := encodeStringSlice(task.Inputs)
	if err != nil {
		return err
	}
	readJSON, err := encodeStringSlice(task.ReadPaths)
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
	if task.Status == "" {
		task.Status = TaskStatusPending
	}
	if task.Version == 0 {
		task.Version = 1
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = task.CreatedAt
	}
	query := `
		INSERT INTO agent_control_task_records (
			workflow, task_id, team_id, parent_task_id, assignee, session_id, agent_path,
			title, goal, inputs_json, summary, status, priority, lease_until, retry_count,
			read_paths_json, write_paths_json, deliverables_json, result_ref, version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	if upsert {
		query += `
		ON CONFLICT(workflow, task_id) DO UPDATE SET
			team_id = excluded.team_id,
			parent_task_id = excluded.parent_task_id,
			assignee = excluded.assignee,
			session_id = excluded.session_id,
			agent_path = excluded.agent_path,
			title = excluded.title,
			goal = excluded.goal,
			inputs_json = excluded.inputs_json,
			summary = excluded.summary,
			status = excluded.status,
			priority = excluded.priority,
			lease_until = excluded.lease_until,
			retry_count = excluded.retry_count,
			read_paths_json = excluded.read_paths_json,
			write_paths_json = excluded.write_paths_json,
			deliverables_json = excluded.deliverables_json,
			result_ref = excluded.result_ref,
			version = excluded.version,
			updated_at = excluded.updated_at
	`
	}
	_, err = tx.ExecContext(ctx, query,
		record.Workflow,
		record.ID,
		record.TeamID,
		nullableString(task.ParentTaskID),
		nullableString(task.Assignee),
		nullablePlainString(record.SessionID),
		nullablePlainString(record.Path),
		task.Title,
		task.Goal,
		inputsJSON,
		task.Summary,
		string(task.Status),
		task.Priority,
		nullableTime(task.LeaseUntil),
		task.RetryCount,
		readJSON,
		writeJSON,
		deliverableJSON,
		nullableString(task.ResultRef),
		task.Version,
		formatTime(task.CreatedAt),
		formatTime(task.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert agent control task record: %w", err)
	}
	return nil
}

func (s *SQLiteStore) agentControlTaskRecordForTask(ctx context.Context, task Task) (agentcontrol.TaskRecord, error) {
	var mate *Teammate
	if assignee := taskAssigneeID(task); assignee != "" {
		record, err := s.GetTeammate(ctx, assignee)
		if err != nil {
			return agentcontrol.TaskRecord{}, err
		}
		mate = record
	}
	return AgentControlTaskRecord(task, mate), nil
}

func agentControlTaskAssigneeProjection(teamID, assignee string, teammate *Teammate) (string, string) {
	teamID = strings.TrimSpace(teamID)
	assignee = strings.TrimSpace(assignee)
	if teammate != nil {
		sessionID := strings.TrimSpace(teammate.SessionID)
		return sessionID, agentcontrol.TeamTeammatePath(teamID, teammate.ID, teammate.Name, teammate.SessionID)
	}
	if assignee == "" {
		return "", ""
	}
	return "", agentcontrol.TeamTeammatePath(teamID, assignee, "", "")
}

// AddTaskDependency registers a dependency edge.
func (s *SQLiteStore) AddTaskDependency(ctx context.Context, taskID, dependsOnID string) error {
	return s.CreateAgentControlTaskDependencyRecord(ctx, "", taskID, dependsOnID)
}

// CreateAgentControlTaskDependencyRecord inserts a dependency edge through the
// AgentControl task graph. Legacy team_task_dependencies is migration-only.
func (s *SQLiteStore) CreateAgentControlTaskDependencyRecord(ctx context.Context, teamID, taskID, dependsOnID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	teamID = strings.TrimSpace(teamID)
	taskID = strings.TrimSpace(taskID)
	dependsOnID = strings.TrimSpace(dependsOnID)
	if taskID == "" || dependsOnID == "" {
		return fmt.Errorf("task dependency ids are required")
	}
	if teamID == "" {
		task, err := s.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if task != nil {
			teamID = strings.TrimSpace(task.TeamID)
		}
	}
	if teamID == "" {
		return fmt.Errorf("team id is required")
	}
	dependencyID := "dep_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	createdAt := time.Now().UTC()
	created := false
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO agent_control_task_dependencies (
				workflow, dependency_id, team_id, task_id, depends_on_id, created_at
			) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(workflow, task_id, depends_on_id) DO NOTHING
		`, agentcontrol.WorkflowSpawnTeam, dependencyID, teamID, taskID, dependsOnID, formatTime(createdAt))
		if err != nil {
			return fmt.Errorf("insert agent control task dependency: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			created = true
		} else {
			row := tx.QueryRowContext(ctx, `
				SELECT dependency_id, created_at
				FROM agent_control_task_dependencies
				WHERE workflow = ? AND task_id = ? AND depends_on_id = ?
			`, agentcontrol.WorkflowSpawnTeam, taskID, dependsOnID)
			var createdRaw string
			if err := row.Scan(&dependencyID, &createdRaw); err != nil {
				return fmt.Errorf("load agent control task dependency: %w", err)
			}
			createdAt = parseTime(createdRaw)
		}
		return nil
	}); err != nil {
		return err
	}
	if created {
		_ = s.appendTaskDependencyCreatedEvent(ctx, dependencyID, taskID, dependsOnID)
	}
	return nil
}

func (s *SQLiteStore) upsertAgentControlTaskDependencyRecord(ctx context.Context, dependencyID, taskID, dependsOnID string, createdAt time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	dependencyID = strings.TrimSpace(dependencyID)
	taskID = strings.TrimSpace(taskID)
	dependsOnID = strings.TrimSpace(dependsOnID)
	if dependencyID == "" || taskID == "" || dependsOnID == "" {
		return nil
	}
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil || strings.TrimSpace(task.TeamID) == "" {
		return nil
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO agent_control_task_dependencies (
			workflow, dependency_id, team_id, task_id, depends_on_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(workflow, task_id, depends_on_id) DO UPDATE SET
			dependency_id = excluded.dependency_id,
			team_id = excluded.team_id,
			created_at = excluded.created_at
	`, agentcontrol.WorkflowSpawnTeam, dependencyID, strings.TrimSpace(task.TeamID), taskID, dependsOnID, formatTime(createdAt))
	if err != nil {
		return fmt.Errorf("upsert agent control task dependency: %w", err)
	}
	return nil
}

func (s *SQLiteStore) appendTaskDependencyCreatedEvent(ctx context.Context, dependencyID, taskID, dependsOnID string) error {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil || strings.TrimSpace(task.TeamID) == "" {
		return nil
	}
	payload := map[string]interface{}{
		"dependency_id": dependencyID,
		"task_id":       strings.TrimSpace(taskID),
		"depends_on_id": strings.TrimSpace(dependsOnID),
		"team_id":       strings.TrimSpace(task.TeamID),
	}
	if dependsOn, err := s.GetTask(ctx, dependsOnID); err == nil && dependsOn != nil {
		payload["depends_on_team_id"] = strings.TrimSpace(dependsOn.TeamID)
	}
	_, err = s.AppendTeamEvent(ctx, TeamEvent{
		Type:      TaskDependencyCreatedEvent,
		TeamID:    strings.TrimSpace(task.TeamID),
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	})
	return err
}

// ListTaskDependencies returns dependency ids for a task.
func (s *SQLiteStore) ListTaskDependencies(ctx context.Context, taskID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT depends_on_id
		FROM agent_control_task_dependencies
		WHERE workflow = ? AND task_id = ?
		ORDER BY created_at ASC, depends_on_id ASC
	`, agentcontrol.WorkflowSpawnTeam, taskID)
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
		SELECT task_id
		FROM agent_control_task_dependencies
		WHERE workflow = ? AND depends_on_id = ?
		ORDER BY created_at ASC, task_id ASC
	`, agentcontrol.WorkflowSpawnTeam, taskID)
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

// ListTaskDependencyRecords returns full dependency edge records.
func (s *SQLiteStore) ListTaskDependencyRecords(ctx context.Context, filter TaskDependencyFilter) ([]TaskDependency, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	taskID := strings.TrimSpace(filter.TaskID)
	dependsOnID := strings.TrimSpace(filter.DependsOnID)
	if taskID == "" && dependsOnID == "" {
		return nil, fmt.Errorf("task_id or depends_on_id is required")
	}
	clauses := []string{"workflow = ?"}
	args := []interface{}{agentcontrol.WorkflowSpawnTeam}
	if taskID != "" {
		clauses = append(clauses, "task_id = ?")
		args = append(args, taskID)
	}
	if dependsOnID != "" {
		clauses = append(clauses, "depends_on_id = ?")
		args = append(args, dependsOnID)
	}
	query := `
		SELECT dependency_id, task_id, depends_on_id, created_at
		FROM agent_control_task_dependencies
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY created_at ASC, task_id ASC, depends_on_id ASC
	`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list dependency records: %w", err)
	}
	defer rows.Close()
	records := make([]TaskDependency, 0)
	for rows.Next() {
		var record TaskDependency
		var createdAt string
		if err := rows.Scan(&record.ID, &record.TaskID, &record.DependsOnID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan dependency record: %w", err)
		}
		record.CreatedAt = parseTime(createdAt)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if filter.IncludeDependents && taskID != "" {
		dependentRecords, err := s.ListTaskDependencyRecords(ctx, TaskDependencyFilter{DependsOnID: taskID})
		if err != nil {
			return nil, err
		}
		records = appendUniqueTaskDependencies(records, dependentRecords...)
	}
	return records, nil
}

func appendUniqueTaskDependencies(records []TaskDependency, extra ...TaskDependency) []TaskDependency {
	seen := make(map[string]struct{}, len(records)+len(extra))
	for _, record := range records {
		key := strings.ToLower(strings.TrimSpace(record.TaskID)) + "\x00" + strings.ToLower(strings.TrimSpace(record.DependsOnID))
		seen[key] = struct{}{}
	}
	for _, record := range extra {
		key := strings.ToLower(strings.TrimSpace(record.TaskID)) + "\x00" + strings.ToLower(strings.TrimSpace(record.DependsOnID))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		records = append(records, record)
	}
	return records
}

// ListTaskGraphEvents returns task-related events from the AgentControl task
// graph mirror using a store-global cursor while preserving each team's event
// sequence separately.
func (s *SQLiteStore) ListTaskGraphEvents(ctx context.Context, filter TaskGraphEventFilter) ([]TaskGraphEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	afterSeq := filter.AfterSeq
	if afterSeq < 0 {
		afterSeq = 0
	}
	workflow := strings.TrimSpace(filter.Workflow)
	if workflow == "" {
		workflow = "spawn_team"
	}
	clauses := []string{"id > ?", "workflow = ?"}
	args := []interface{}{afterSeq, workflow}
	if teamID := strings.TrimSpace(filter.TeamID); teamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, teamID)
	}
	clauses = append(clauses, "type LIKE ?")
	args = append(args, "task.%")
	eventType := strings.TrimSpace(filter.EventType)
	if eventType != "" && strings.Contains(eventType, "*") {
		clauses = append(clauses, "type LIKE ?")
		args = append(args, strings.ReplaceAll(eventType, "*", "%"))
	} else if eventType != "" {
		clauses = append(clauses, "type = ?")
		args = append(args, eventType)
	}

	query := `
		SELECT id, workflow, team_id, team_seq, type, payload_json, created_at
		FROM agent_control_task_graph_events
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY id ASC
	`
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list task graph events: %w", err)
	}
	defer rows.Close()

	events := make([]TaskGraphEvent, 0)
	for rows.Next() {
		var (
			event      TaskGraphEvent
			payloadRaw string
			createdRaw string
		)
		if err := rows.Scan(&event.Seq, &event.Workflow, &event.TeamID, &event.TeamSeq, &event.Type, &payloadRaw, &createdRaw); err != nil {
			return nil, fmt.Errorf("scan task graph event: %w", err)
		}
		event.Payload = decodeMetadata(payloadRaw)
		event.CreatedAt = parseTime(createdRaw)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// ListAgentControlTaskRecords reads the AgentControl task graph table.
func (s *SQLiteStore) ListAgentControlTaskRecords(ctx context.Context, filter agentcontrol.TaskFilter) ([]agentcontrol.TaskRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	workflow := strings.TrimSpace(filter.Workflow)
	if workflow == "" {
		workflow = agentcontrol.WorkflowSpawnTeam
	}
	if workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, nil
	}
	clauses := []string{"workflow = ?"}
	args := []interface{}{workflow}
	if teamID := strings.TrimSpace(filter.TeamID); teamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, teamID)
	}
	if assignee := strings.TrimSpace(filter.Assignee); assignee != "" {
		clauses = append(clauses, "assignee = ?")
		args = append(args, assignee)
	}
	if pathPrefix := strings.TrimRight(strings.TrimSpace(filter.PathPrefix), "/"); pathPrefix != "" {
		clauses = append(clauses, "(agent_path = ? OR agent_path LIKE ?)")
		args = append(args, pathPrefix, pathPrefix+"/%")
	}
	if len(filter.Status) > 0 {
		placeholders := make([]string, 0, len(filter.Status))
		for _, status := range filter.Status {
			status = strings.TrimSpace(status)
			if status == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		if len(placeholders) > 0 {
			clauses = append(clauses, fmt.Sprintf("status IN (%s)", strings.Join(placeholders, ",")))
		}
	}
	query := `
		SELECT task_id, workflow, team_id, parent_task_id, assignee, session_id, agent_path,
			title, summary, status, priority, created_at, updated_at
		FROM agent_control_task_records
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY priority DESC, created_at ASC, task_id ASC
	`
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list agent control task records: %w", err)
	}
	defer rows.Close()

	records := make([]agentcontrol.TaskRecord, 0)
	for rows.Next() {
		var (
			record       agentcontrol.TaskRecord
			parentRaw    sql.NullString
			assigneeRaw  sql.NullString
			sessionRaw   sql.NullString
			pathRaw      sql.NullString
			titleRaw     sql.NullString
			summaryRaw   sql.NullString
			statusRaw    sql.NullString
			createdAtRaw string
			updatedAtRaw string
		)
		if err := rows.Scan(&record.ID, &record.Workflow, &record.TeamID, &parentRaw, &assigneeRaw, &sessionRaw, &pathRaw, &titleRaw, &summaryRaw, &statusRaw, &record.Priority, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan agent control task record: %w", err)
		}
		if parentRaw.Valid {
			record.ParentTaskID = parentRaw.String
		}
		if assigneeRaw.Valid {
			record.Assignee = assigneeRaw.String
		}
		if sessionRaw.Valid {
			record.SessionID = sessionRaw.String
		}
		if pathRaw.Valid {
			record.Path = pathRaw.String
		}
		if titleRaw.Valid {
			record.Title = titleRaw.String
		}
		if summaryRaw.Valid {
			record.Summary = summaryRaw.String
		}
		if statusRaw.Valid {
			record.Status = statusRaw.String
		}
		record.CreatedAt = parseTime(createdAtRaw)
		record.UpdatedAt = parseTime(updatedAtRaw)
		records = append(records, record.Normalize())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// InsertMail inserts a mailbox message.
func (s *SQLiteStore) InsertMail(ctx context.Context, message MailMessage) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("team store is not initialized")
	}
	message.TeamID = strings.TrimSpace(message.TeamID)
	if message.TeamID == "" {
		return "", fmt.Errorf("team id is required")
	}
	if message.ID == "" {
		message.ID = "mail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	if id, ok, err := s.insertMailSameTx(ctx, message); ok {
		return id, err
	}
	var globalPrimaryUsed bool
	var err error
	message, globalPrimaryUsed, err = s.appendPrimaryGlobalMailboxRecord(ctx, message)
	if err != nil {
		return "", err
	}
	metadataJSON, err := encodeMetadata(message.Metadata)
	if err != nil {
		return "", err
	}
	controlMessage := message
	controlMessage.ControlSeq = 0
	wakeEvent := agentcontrol.WakeEvent{}
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		seq, err := nextAgentControlMailboxTeamSeqTx(ctx, tx, message.TeamID)
		if err != nil {
			return err
		}
		message.Seq = seq
		result, err := tx.ExecContext(ctx, `
			INSERT INTO agent_control_mailbox_records (
				scope, global_seq, workflow, session_id, session_mailbox_seq, team_id, team_seq, message_id, from_agent, to_agent, task_id,
				kind, body, metadata_json, created_at, acked_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, agentcontrol.MailboxScopeTeam, message.GlobalSeq, agentcontrol.WorkflowSpawnTeam, nil, 0, message.TeamID, message.Seq, message.ID,
			message.FromAgent, message.ToAgent, nullableString(message.TaskID), message.Kind, message.Body, metadataJSON,
			formatTime(message.CreatedAt), nullableTime(message.AckedAt))
		if err != nil {
			return fmt.Errorf("insert agent control mailbox record: %w", err)
		}
		recordSeq, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("agent control mailbox record id: %w", err)
		}
		result, err = tx.ExecContext(ctx, `
			INSERT INTO agent_control_wake_events (
				workflow, kind, team_id, team_seq, message_id, from_agent, to_agent, task_id, event_kind, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, agentcontrol.WorkflowSpawnTeam, agentcontrol.WakeKindMailbox, message.TeamID, message.Seq, message.ID, message.FromAgent, message.ToAgent, nullableString(message.TaskID), message.Kind, formatTime(message.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert agent control wake event: %w", err)
		}
		wakeSeq, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("agent control wake event id: %w", err)
		}
		taskID := ""
		if message.TaskID != nil {
			taskID = strings.TrimSpace(*message.TaskID)
		}
		wakeEvent = agentcontrol.WakeEvent{
			Seq:       wakeSeq,
			Workflow:  agentcontrol.WorkflowSpawnTeam,
			Kind:      agentcontrol.WakeKindMailbox,
			TeamID:    message.TeamID,
			TeamSeq:   message.Seq,
			MessageID: message.ID,
			TaskID:    taskID,
			EventKind: message.Kind,
			FromAgent: message.FromAgent,
			ToAgent:   message.ToAgent,
			CreatedAt: message.CreatedAt,
		}.Normalize()
		controlMessage = message
		controlMessage.ControlSeq = recordSeq
		return nil
	}); err != nil {
		return "", err
	}
	if globalPrimaryUsed {
		if refreshed, _, err := s.appendPrimaryGlobalMailboxRecord(ctx, controlMessage); err != nil {
			return "", err
		} else if refreshed.GlobalSeq > 0 {
			message.GlobalSeq = refreshed.GlobalSeq
			controlMessage.GlobalSeq = refreshed.GlobalSeq
		}
	} else if globalSeq, _ := s.appendGlobalMailboxRecord(ctx, controlMessage); globalSeq > 0 {
		message.GlobalSeq = globalSeq
		controlMessage.GlobalSeq = globalSeq
	}
	s.notifyMailboxWatchers(message)
	s.notifyAgentControlWakeWatchers(wakeEvent)
	s.notifyAgentControlMailboxWatchers(controlMessage)
	return message.ID, nil
}

func (s *SQLiteStore) insertMailSameTx(ctx context.Context, message MailMessage) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, nil
	}
	s.globalMailboxWriterMu.RLock()
	writer := s.globalMailboxWriter
	s.globalMailboxWriterMu.RUnlock()
	txWriter, ok := writer.(agentcontrol.GlobalMailboxSQLiteTxWriter)
	if !ok || txWriter == nil {
		return "", false, nil
	}
	if _, ok := txWriter.GlobalMailboxAttachDSN(); !ok {
		return "", false, nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return "", false, nil
	}
	defer conn.Close()
	txWriter, schema, detach, attached, err := agentcontrol.AttachGlobalMailboxSQLiteTx(ctx, conn, writer)
	if err != nil || !attached {
		return "", false, nil
	}
	defer detach()

	metadataJSON, err := encodeMetadata(message.Metadata)
	if err != nil {
		return "", true, err
	}
	controlMessage := message
	controlMessage.ControlSeq = 0
	wakeEvent := agentcontrol.WakeEvent{}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return "", true, fmt.Errorf("begin insert mail tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	appended, err := txWriter.AppendPrimaryGlobalMailboxRecordTx(ctx, tx, schema, AgentControlMailboxRecord(message))
	if err != nil {
		return "", true, fmt.Errorf("append primary global team mailbox record: %w", err)
	}
	if appended.GlobalSeq > 0 {
		message.GlobalSeq = appended.GlobalSeq
	}
	if strings.TrimSpace(appended.MessageID) != "" {
		message.ID = appended.MessageID
	}
	seq, err := nextAgentControlMailboxTeamSeqTx(ctx, tx, message.TeamID)
	if err != nil {
		return "", true, err
	}
	message.Seq = seq
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_control_mailbox_records (
			scope, global_seq, workflow, session_id, session_mailbox_seq, team_id, team_seq, message_id, from_agent, to_agent, task_id,
			kind, body, metadata_json, created_at, acked_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, agentcontrol.MailboxScopeTeam, message.GlobalSeq, agentcontrol.WorkflowSpawnTeam, nil, 0, message.TeamID, message.Seq, message.ID,
		message.FromAgent, message.ToAgent, nullableString(message.TaskID), message.Kind, message.Body, metadataJSON,
		formatTime(message.CreatedAt), nullableTime(message.AckedAt))
	if err != nil {
		return "", true, fmt.Errorf("insert agent control mailbox record: %w", err)
	}
	recordSeq, err := result.LastInsertId()
	if err != nil {
		return "", true, fmt.Errorf("agent control mailbox record id: %w", err)
	}
	result, err = tx.ExecContext(ctx, `
		INSERT INTO agent_control_wake_events (
			workflow, kind, team_id, team_seq, message_id, from_agent, to_agent, task_id, event_kind, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, agentcontrol.WorkflowSpawnTeam, agentcontrol.WakeKindMailbox, message.TeamID, message.Seq, message.ID, message.FromAgent, message.ToAgent, nullableString(message.TaskID), message.Kind, formatTime(message.CreatedAt))
	if err != nil {
		return "", true, fmt.Errorf("insert agent control wake event: %w", err)
	}
	wakeSeq, err := result.LastInsertId()
	if err != nil {
		return "", true, fmt.Errorf("agent control wake event id: %w", err)
	}
	taskID := ""
	if message.TaskID != nil {
		taskID = strings.TrimSpace(*message.TaskID)
	}
	wakeEvent = agentcontrol.WakeEvent{
		Seq:       wakeSeq,
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		Kind:      agentcontrol.WakeKindMailbox,
		TeamID:    message.TeamID,
		TeamSeq:   message.Seq,
		MessageID: message.ID,
		TaskID:    taskID,
		EventKind: message.Kind,
		FromAgent: message.FromAgent,
		ToAgent:   message.ToAgent,
		CreatedAt: message.CreatedAt,
	}.Normalize()
	controlMessage = message
	controlMessage.ControlSeq = recordSeq
	refreshed, err := txWriter.AppendPrimaryGlobalMailboxRecordTx(ctx, tx, schema, AgentControlMailboxRecord(controlMessage))
	if err != nil {
		return "", true, fmt.Errorf("refresh primary global team mailbox record: %w", err)
	}
	if refreshed.GlobalSeq > 0 {
		appended = refreshed
		message.GlobalSeq = refreshed.GlobalSeq
		controlMessage.GlobalSeq = refreshed.GlobalSeq
	}
	if err := tx.Commit(); err != nil {
		return "", true, fmt.Errorf("commit insert mail tx: %w", err)
	}
	committed = true

	if notifier, ok := txWriter.(agentcontrol.GlobalMailboxWakeNotifier); ok {
		notifier.NotifyGlobalMailboxWake(appended)
	}
	s.notifyMailboxWatchers(message)
	s.notifyAgentControlWakeWatchers(wakeEvent)
	s.notifyAgentControlMailboxWatchers(controlMessage)
	return message.ID, true, nil
}

func (s *SQLiteStore) appendGlobalMailboxRecord(ctx context.Context, message MailMessage) (int64, error) {
	if s == nil || message.ControlSeq <= 0 {
		return 0, nil
	}
	s.globalMailboxWriterMu.RLock()
	writer := s.globalMailboxWriter
	s.globalMailboxWriterMu.RUnlock()
	if writer == nil {
		return 0, nil
	}
	record := AgentControlMailboxRecord(message)
	record.Seq = message.ControlSeq
	record.SourceSeq = message.ControlSeq
	globalSeq, err := writer.AppendGlobalMailboxRecord(ctx, agentcontrol.MailboxSourceTeams, record)
	if err != nil {
		return 0, fmt.Errorf("append global team mailbox record: %w", err)
	}
	if globalSeq > 0 {
		if err := s.updateTeamMailboxGlobalSeq(ctx, message.ControlSeq, globalSeq); err != nil {
			return globalSeq, err
		}
	}
	return globalSeq, nil
}

func (s *SQLiteStore) appendPrimaryGlobalMailboxRecord(ctx context.Context, message MailMessage) (MailMessage, bool, error) {
	if s == nil {
		return message, false, nil
	}
	s.globalMailboxWriterMu.RLock()
	writer := s.globalMailboxWriter
	s.globalMailboxWriterMu.RUnlock()
	primary, ok := writer.(agentcontrol.GlobalMailboxPrimaryWriter)
	if !ok || primary == nil {
		return message, false, nil
	}
	record := AgentControlMailboxRecord(message)
	appended, err := primary.AppendPrimaryGlobalMailboxRecord(ctx, record)
	if err != nil {
		return message, false, fmt.Errorf("append primary global team mailbox record: %w", err)
	}
	if appended.GlobalSeq > 0 {
		message.GlobalSeq = appended.GlobalSeq
	}
	if strings.TrimSpace(appended.MessageID) != "" {
		message.ID = appended.MessageID
	}
	return message, true, nil
}

func (s *SQLiteStore) updateTeamMailboxGlobalSeq(ctx context.Context, controlSeq int64, globalSeq int64) error {
	if s == nil || s.db == nil || controlSeq <= 0 || globalSeq <= 0 {
		return nil
	}
	return s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE agent_control_mailbox_records
			SET global_seq = ?
			WHERE scope = ? AND id = ?
		`, globalSeq, agentcontrol.MailboxScopeTeam, controlSeq)
		if err != nil {
			return fmt.Errorf("update team mailbox global sequence: %w", err)
		}
		return nil
	})
}

// RepairAgentControlMailboxProjection materializes local AgentControl mailbox
// rows that are missing a global registry backlink, then records the returned
// global sequence on each repaired local row.
func (s *SQLiteStore) RepairAgentControlMailboxProjection(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	s.globalMailboxWriterMu.RLock()
	writer := s.globalMailboxWriter
	s.globalMailboxWriterMu.RUnlock()
	if writer == nil {
		return 0, nil
	}
	records, err := s.listUnprojectedTeamMailboxRecords(ctx, filter)
	if err != nil {
		return 0, err
	}
	var repaired int64
	for _, record := range records {
		record.SourceSeq = record.Seq
		globalSeq, err := writer.AppendGlobalMailboxRecord(ctx, agentcontrol.MailboxSourceTeams, record)
		if err != nil {
			return repaired, fmt.Errorf("repair global team mailbox record: %w", err)
		}
		if globalSeq <= 0 {
			continue
		}
		if err := s.updateTeamMailboxGlobalSeq(ctx, record.Seq, globalSeq); err != nil {
			return repaired, err
		}
		repaired++
	}
	return repaired, nil
}

func (s *SQLiteStore) listUnprojectedTeamMailboxRecords(ctx context.Context, filter agentcontrol.MailboxRecordFilter) ([]agentcontrol.MailboxRecord, error) {
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeTeam {
		return nil, nil
	}
	workflow := normalizeAgentControlTaskWorkflow(filter.Workflow)
	if workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, nil
	}
	clauses := []string{"scope = ?", "workflow = ?", "global_seq = 0"}
	args := []interface{}{agentcontrol.MailboxScopeTeam, workflow}
	if filter.TeamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID)
	}
	if filter.AfterSeq > 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, filter.AfterSeq)
	}
	query := `
		SELECT id, global_seq, workflow, scope, session_id, session_mailbox_seq, team_id, team_seq, message_id,
			from_agent, to_agent, task_id, kind, body, metadata_json, created_at, acked_at
		FROM agent_control_mailbox_records
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY id ASC
	`
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list unprojected team mailbox records: %w", err)
	}
	defer rows.Close()

	records := make([]agentcontrol.MailboxRecord, 0)
	for rows.Next() {
		var (
			record      agentcontrol.MailboxRecord
			metadataRaw string
			createdRaw  string
			ackedRaw    sql.NullString
			workflowRaw sql.NullString
			scopeRaw    sql.NullString
			sessionID   sql.NullString
			teamID      sql.NullString
			fromAgent   sql.NullString
			toAgent     sql.NullString
			taskIDRaw   sql.NullString
			kind        sql.NullString
		)
		if err := rows.Scan(&record.Seq, &record.GlobalSeq, &workflowRaw, &scopeRaw, &sessionID, &record.SessionMailboxSeq, &teamID, &record.TeamSeq, &record.MessageID,
			&fromAgent, &toAgent, &taskIDRaw, &kind, &record.Body, &metadataRaw, &createdRaw, &ackedRaw); err != nil {
			return nil, fmt.Errorf("scan unprojected team mailbox record: %w", err)
		}
		record.Workflow = workflowRaw.String
		record.Scope = scopeRaw.String
		record.SessionID = sessionID.String
		record.TeamID = teamID.String
		record.FromAgent = fromAgent.String
		record.ToAgent = toAgent.String
		record.Kind = kind.String
		if taskIDRaw.Valid {
			record.TaskID = taskIDRaw.String
		}
		record.Metadata = decodeMetadata(metadataRaw)
		if record.Metadata == nil {
			record.Metadata = map[string]interface{}{}
		}
		record.CreatedAt = parseTime(createdRaw)
		if ackedRaw.Valid {
			ackedAt := parseTime(ackedRaw.String)
			record.AckedAt = &ackedAt
		}
		records = append(records, record.Normalize())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// RepairAgentControlMailboxLocalProjection backfills missing local team mailbox
// projection rows from the durable global registry.
func (s *SQLiteStore) RepairAgentControlMailboxLocalProjection(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	s.globalMailboxWriterMu.RLock()
	writer := s.globalMailboxWriter
	s.globalMailboxWriterMu.RUnlock()
	reader, ok := writer.(agentcontrol.MailboxRegistryReader)
	if !ok || reader == nil {
		return 0, nil
	}
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeTeam {
		return 0, nil
	}
	filter.Scope = agentcontrol.MailboxScopeTeam
	records, err := reader.ListAgentControlMailboxRecords(ctx, filter)
	if err != nil {
		return 0, err
	}
	var repaired int64
	for _, record := range records {
		record = record.Normalize()
		if record.Scope != agentcontrol.MailboxScopeTeam || record.TeamID == "" {
			continue
		}
		if record.GlobalSeq <= 0 {
			record.GlobalSeq = record.Seq
		}
		ok, err := s.repairTeamMailboxLocalProjectionRecord(ctx, record)
		if err != nil {
			return repaired, err
		}
		if ok {
			repaired++
		}
	}
	return repaired, nil
}

func (s *SQLiteStore) repairTeamMailboxLocalProjectionRecord(ctx context.Context, record agentcontrol.MailboxRecord) (bool, error) {
	if record.GlobalSeq <= 0 {
		return false, nil
	}
	inserted := false
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		var existing int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM agent_control_mailbox_records
			WHERE scope = ? AND global_seq = ?
		`, agentcontrol.MailboxScopeTeam, record.GlobalSeq).Scan(&existing); err != nil {
			return fmt.Errorf("check team mailbox projection by global seq: %w", err)
		}
		if existing > 0 {
			return nil
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_control_mailbox_records
			SET global_seq = ?
			WHERE scope = ? AND team_id = ? AND message_id = ? AND global_seq = 0
		`, record.GlobalSeq, agentcontrol.MailboxScopeTeam, record.TeamID, record.MessageID)
		if err != nil {
			return fmt.Errorf("repair team mailbox projection backlink: %w", err)
		}
		if rows, _ := result.RowsAffected(); rows > 0 {
			inserted = true
			return nil
		}
		teamSeq, err := nextTeamMailboxProjectionSeqTx(ctx, tx, record.TeamID, record.TeamSeq)
		if err != nil {
			return err
		}
		message := teamMessageFromMailboxRecord(record, teamSeq)
		metadataJSON, err := encodeMetadata(message.Metadata)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO agent_control_mailbox_records (
				scope, global_seq, workflow, session_id, session_mailbox_seq, team_id, team_seq, message_id, from_agent, to_agent, task_id,
				kind, body, metadata_json, created_at, acked_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, agentcontrol.MailboxScopeTeam, record.GlobalSeq, agentcontrol.WorkflowSpawnTeam, nil, 0, message.TeamID, message.Seq, message.ID,
			message.FromAgent, message.ToAgent, nullableString(message.TaskID), message.Kind, message.Body, metadataJSON,
			formatTime(message.CreatedAt), nullableTime(message.AckedAt))
		if err != nil {
			return fmt.Errorf("insert team mailbox local projection: %w", err)
		}
		inserted = true
		return nil
	}); err != nil {
		return false, err
	}
	return inserted, nil
}

func nextTeamMailboxProjectionSeqTx(ctx context.Context, tx *sql.Tx, teamID string, preferred int64) (int64, error) {
	if preferred > 0 {
		var used int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM agent_control_mailbox_records
			WHERE scope = ? AND team_id = ? AND team_seq = ?
		`, agentcontrol.MailboxScopeTeam, teamID, preferred).Scan(&used); err != nil {
			return 0, fmt.Errorf("check team mailbox projection sequence: %w", err)
		}
		if used == 0 {
			return preferred, nil
		}
	}
	return nextAgentControlMailboxTeamSeqTx(ctx, tx, teamID)
}

func teamMessageFromMailboxRecord(record agentcontrol.MailboxRecord, teamSeq int64) MailMessage {
	metadata := map[string]interface{}{}
	for key, value := range record.Metadata {
		metadata[key] = value
	}
	var taskID *string
	if strings.TrimSpace(record.TaskID) != "" {
		value := strings.TrimSpace(record.TaskID)
		taskID = &value
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return MailMessage{
		ID:        record.MessageID,
		Seq:       teamSeq,
		GlobalSeq: record.GlobalSeq,
		TeamID:    record.TeamID,
		FromAgent: record.FromAgent,
		ToAgent:   record.ToAgent,
		TaskID:    taskID,
		Kind:      record.Kind,
		Body:      record.Body,
		Metadata:  metadata,
		CreatedAt: createdAt,
		AckedAt:   cloneMailboxAckedAt(record.AckedAt),
	}
}

// ListMail returns mailbox messages matching the filter.
func (s *SQLiteStore) ListMail(ctx context.Context, filter MailFilter) ([]MailMessage, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	return s.listAgentControlMail(ctx, filter)
}

func (s *SQLiteStore) listAgentControlMail(ctx context.Context, filter MailFilter) ([]MailMessage, error) {
	query := strings.Builder{}
	query.WriteString(`
		SELECT id, global_seq, team_seq, message_id, team_id, from_agent, to_agent, task_id, kind, body, metadata_json, created_at, acked_at
		FROM agent_control_mailbox_records
	`)
	clauses := make([]string, 0)
	args := make([]interface{}, 0)
	clauses = append(clauses, "scope = ?")
	args = append(args, agentcontrol.MailboxScopeTeam)
	clauses = append(clauses, "workflow = ?")
	args = append(args, agentcontrol.WorkflowSpawnTeam)
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
	if filter.AfterSeq > 0 {
		clauses = append(clauses, "team_seq > ?")
		args = append(args, filter.AfterSeq)
	}
	if filter.UnreadOnly {
		if toAgent := strings.TrimSpace(filter.ToAgent); toAgent != "" {
			clauses = append(clauses, `
				NOT EXISTS (
					SELECT 1
					FROM team_mailbox_receipts receipts
					WHERE receipts.team_id = agent_control_mailbox_records.team_id
					  AND receipts.message_id = agent_control_mailbox_records.message_id
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
	if filter.AfterSeq > 0 {
		query.WriteString(" ORDER BY team_seq ASC")
	} else {
		query.WriteString(" ORDER BY created_at DESC, team_seq DESC")
	}
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
			controlSeq  int64
			globalSeq   int64
			teamSeq     int64
			metadataRaw string
			createdRaw  string
			ackedRaw    sql.NullString
			taskIDRaw   sql.NullString
		)
		if err := rows.Scan(&controlSeq, &globalSeq, &teamSeq, &msg.ID, &msg.TeamID, &msg.FromAgent, &msg.ToAgent, &taskIDRaw, &msg.Kind, &msg.Body, &metadataRaw, &createdRaw, &ackedRaw); err != nil {
			return nil, fmt.Errorf("scan mail: %w", err)
		}
		msg.Seq = teamSeq
		msg.ControlSeq = controlSeq
		msg.GlobalSeq = globalSeq
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

// ListAgentControlMailboxRecords projects team mailbox rows into the shared
// AgentControl mailbox registry read model.
func (s *SQLiteStore) ListAgentControlMailboxRecords(ctx context.Context, filter agentcontrol.MailboxRecordFilter) ([]agentcontrol.MailboxRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeTeam {
		return nil, nil
	}
	workflow := normalizeAgentControlTaskWorkflow(filter.Workflow)
	if workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, nil
	}
	clauses := []string{"scope = ?", "workflow = ?", "id > ?"}
	args := []interface{}{agentcontrol.MailboxScopeTeam, workflow, filter.AfterSeq}
	if filter.TeamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID)
	}
	query := `
		SELECT id, global_seq, workflow, scope, session_id, session_mailbox_seq, team_id, team_seq, message_id,
			from_agent, to_agent, task_id, kind, body, metadata_json, created_at, acked_at
		FROM agent_control_mailbox_records
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY id ASC
	`
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list agent control mailbox records: %w", err)
	}
	defer rows.Close()

	records := make([]agentcontrol.MailboxRecord, 0)
	for rows.Next() {
		var (
			record      agentcontrol.MailboxRecord
			metadataRaw string
			createdRaw  string
			ackedRaw    sql.NullString
			workflowRaw sql.NullString
			scopeRaw    sql.NullString
			sessionID   sql.NullString
			teamID      sql.NullString
			fromAgent   sql.NullString
			toAgent     sql.NullString
			taskIDRaw   sql.NullString
			kind        sql.NullString
		)
		if err := rows.Scan(&record.Seq, &record.GlobalSeq, &workflowRaw, &scopeRaw, &sessionID, &record.SessionMailboxSeq, &teamID, &record.TeamSeq, &record.MessageID,
			&fromAgent, &toAgent, &taskIDRaw, &kind, &record.Body, &metadataRaw, &createdRaw, &ackedRaw); err != nil {
			return nil, fmt.Errorf("scan agent control mailbox record: %w", err)
		}
		record.Workflow = workflowRaw.String
		record.Scope = scopeRaw.String
		record.SessionID = sessionID.String
		record.TeamID = teamID.String
		record.FromAgent = fromAgent.String
		record.ToAgent = toAgent.String
		record.Kind = kind.String
		if taskIDRaw.Valid {
			record.TaskID = taskIDRaw.String
		}
		record.Metadata = decodeMetadata(metadataRaw)
		if record.Metadata == nil {
			record.Metadata = map[string]interface{}{}
		}
		record.CreatedAt = parseTime(createdRaw)
		if ackedRaw.Valid {
			ackedAt := parseTime(ackedRaw.String)
			record.AckedAt = &ackedAt
		}
		records = append(records, record.Normalize())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// WatchMail registers an in-process notification channel for mailbox inserts.
// Callers must still use ListMail with AfterSeq for durable catch-up; this
// channel is only a low-latency wake source and intentionally drops duplicate
// wake signals when the receiver is already behind.
func (s *SQLiteStore) WatchMail(ctx context.Context, teamID string) (<-chan MailMessage, func()) {
	ch := make(chan MailMessage, 1)
	if s == nil {
		return ch, func() {}
	}
	teamID = strings.TrimSpace(teamID)
	s.mailboxWatchMu.Lock()
	if s.mailboxWatchers == nil {
		s.mailboxWatchers = make(map[int64]mailboxWatcher)
	}
	s.nextMailboxWatchID++
	watchID := s.nextMailboxWatchID
	s.mailboxWatchers[watchID] = mailboxWatcher{
		teamID: teamID,
		ch:     ch,
	}
	s.mailboxWatchMu.Unlock()

	cancel := func() {
		s.mailboxWatchMu.Lock()
		if s.mailboxWatchers != nil {
			delete(s.mailboxWatchers, watchID)
		}
		s.mailboxWatchMu.Unlock()
	}
	if ctx != nil {
		done := ctx.Done()
		if done == nil {
			return ch, cancel
		}
		go func() {
			<-done
			cancel()
		}()
	}
	return ch, cancel
}

// LastMailSeq returns the current durable mailbox high-water mark for a team.
func (s *SQLiteStore) LastMailSeq(ctx context.Context, teamID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(team_seq), 0)
		FROM agent_control_mailbox_records
		WHERE scope = ? AND workflow = ? AND team_id = ?
	`, agentcontrol.MailboxScopeTeam, agentcontrol.WorkflowSpawnTeam, strings.TrimSpace(teamID)).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("last mail sequence: %w", err)
	}
	return seq, nil
}

// LastAgentControlMailboxRecordSeq returns the shared AgentControl mailbox
// registry high-water mark for team-scoped mailbox rows.
func (s *SQLiteStore) LastAgentControlMailboxRecordSeq(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeTeam {
		return 0, nil
	}
	workflow := normalizeAgentControlTaskWorkflow(filter.Workflow)
	if workflow != agentcontrol.WorkflowSpawnTeam {
		return 0, nil
	}
	clauses := []string{"scope = ?", "workflow = ?"}
	args := []interface{}{agentcontrol.MailboxScopeTeam, workflow}
	if filter.TeamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID)
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(id), 0)
		FROM agent_control_mailbox_records
		WHERE `+strings.Join(clauses, " AND ")+`
	`, args...).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("last agent control mailbox record sequence: %w", err)
	}
	return seq, nil
}

// WatchAgentControlWake registers an in-process notification channel for the
// unified AgentControl wake registry.
func (s *SQLiteStore) WatchAgentControlWake(ctx context.Context, filter agentcontrol.WakeFilter) (<-chan agentcontrol.WakeEvent, func()) {
	ch := make(chan agentcontrol.WakeEvent, 1)
	if s == nil {
		return ch, func() {}
	}
	filter = filter.Normalize()
	filter.Workflow = normalizeAgentControlTaskWorkflow(filter.Workflow)
	s.agentControlWakeWatchMu.Lock()
	if s.agentControlWakeWatchers == nil {
		s.agentControlWakeWatchers = make(map[int64]agentControlWakeWatcher)
	}
	s.nextAgentControlWakeWatchID++
	watchID := s.nextAgentControlWakeWatchID
	s.agentControlWakeWatchers[watchID] = agentControlWakeWatcher{
		workflow: filter.Workflow,
		kind:     filter.Kind,
		teamID:   filter.TeamID,
		ch:       ch,
	}
	s.agentControlWakeWatchMu.Unlock()

	cancel := func() {
		s.agentControlWakeWatchMu.Lock()
		if s.agentControlWakeWatchers != nil {
			delete(s.agentControlWakeWatchers, watchID)
		}
		s.agentControlWakeWatchMu.Unlock()
	}
	if ctx != nil {
		done := ctx.Done()
		if done == nil {
			return ch, cancel
		}
		go func() {
			<-done
			cancel()
		}()
	}
	return ch, cancel
}

// LastAgentControlWakeSeq returns the unified AgentControl wake high-water mark
// for the workflow/kind/team filter.
func (s *SQLiteStore) LastAgentControlWakeSeq(ctx context.Context, filter agentcontrol.WakeFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	filter = filter.Normalize()
	filter.Workflow = normalizeAgentControlTaskWorkflow(filter.Workflow)
	clauses := []string{"workflow = ?"}
	args := []interface{}{filter.Workflow}
	if filter.Kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, filter.Kind)
	}
	if filter.TeamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID)
	}
	if filter.SessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, filter.SessionID)
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(id), 0)
		FROM agent_control_wake_events
		WHERE `+strings.Join(clauses, " AND ")+`
	`, args...).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("last agent control wake sequence: %w", err)
	}
	return seq, nil
}

// WatchAgentControlMailboxSignals registers an in-process notification channel
// for AgentControl mailbox wake mirror rows.
func (s *SQLiteStore) WatchAgentControlMailboxSignals(ctx context.Context, workflow, teamID string) (<-chan MailMessage, func()) {
	ch := make(chan MailMessage, 1)
	if s == nil {
		return ch, func() {}
	}
	workflow = normalizeAgentControlTaskWorkflow(workflow)
	teamID = strings.TrimSpace(teamID)
	s.agentControlMailboxWatchMu.Lock()
	if s.agentControlMailboxWatchers == nil {
		s.agentControlMailboxWatchers = make(map[int64]mailboxWatcher)
	}
	s.nextAgentControlMailboxWatchID++
	watchID := s.nextAgentControlMailboxWatchID
	s.agentControlMailboxWatchers[watchID] = mailboxWatcher{
		workflow: workflow,
		teamID:   teamID,
		ch:       ch,
	}
	s.agentControlMailboxWatchMu.Unlock()

	cancel := func() {
		s.agentControlMailboxWatchMu.Lock()
		if s.agentControlMailboxWatchers != nil {
			delete(s.agentControlMailboxWatchers, watchID)
		}
		s.agentControlMailboxWatchMu.Unlock()
	}
	if ctx != nil {
		done := ctx.Done()
		if done == nil {
			return ch, cancel
		}
		go func() {
			<-done
			cancel()
		}()
	}
	return ch, cancel
}

// LastAgentControlMailboxSignalSeq returns the AgentControl mailbox wake
// high-water mark for the workflow/team filter.
func (s *SQLiteStore) LastAgentControlMailboxSignalSeq(ctx context.Context, workflow, teamID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	workflow = normalizeAgentControlTaskWorkflow(workflow)
	teamID = strings.TrimSpace(teamID)
	clauses := []string{"workflow = ?", "kind = ?"}
	args := []interface{}{workflow, agentcontrol.WakeKindMailbox}
	if teamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, teamID)
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(id), 0)
		FROM agent_control_wake_events
		WHERE `+strings.Join(clauses, " AND ")+`
	`, args...).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("last agent control mailbox wake sequence: %w", err)
	}
	return seq, nil
}

// WatchTaskSignals registers an in-process notification channel for task
// lifecycle signal inserts. Callers must use LastTaskSignalSeq for durable
// catch-up; this channel is only a low-latency wake source.
func (s *SQLiteStore) WatchTaskSignals(ctx context.Context, teamID string) (<-chan TaskSignal, func()) {
	ch := make(chan TaskSignal, 1)
	if s == nil {
		return ch, func() {}
	}
	teamID = strings.TrimSpace(teamID)
	s.taskSignalWatchMu.Lock()
	if s.taskSignalWatchers == nil {
		s.taskSignalWatchers = make(map[int64]taskSignalWatcher)
	}
	s.nextTaskSignalWatchID++
	watchID := s.nextTaskSignalWatchID
	s.taskSignalWatchers[watchID] = taskSignalWatcher{
		teamID: teamID,
		ch:     ch,
	}
	s.taskSignalWatchMu.Unlock()

	cancel := func() {
		s.taskSignalWatchMu.Lock()
		if s.taskSignalWatchers != nil {
			delete(s.taskSignalWatchers, watchID)
		}
		s.taskSignalWatchMu.Unlock()
	}
	if ctx != nil {
		done := ctx.Done()
		if done == nil {
			return ch, cancel
		}
		go func() {
			<-done
			cancel()
		}()
	}
	return ch, cancel
}

// LastTaskSignalSeq returns the current durable task signal high-water mark for a team.
func (s *SQLiteStore) LastTaskSignalSeq(ctx context.Context, teamID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0)
		FROM team_task_signals
		WHERE team_id = ?
	`, strings.TrimSpace(teamID)).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("last task signal sequence: %w", err)
	}
	return seq, nil
}

// WatchAgentControlTaskSignals registers an in-process notification channel
// for AgentControl task wake mirror rows.
func (s *SQLiteStore) WatchAgentControlTaskSignals(ctx context.Context, workflow, teamID string) (<-chan TaskSignal, func()) {
	ch := make(chan TaskSignal, 1)
	if s == nil {
		return ch, func() {}
	}
	workflow = normalizeAgentControlTaskWorkflow(workflow)
	teamID = strings.TrimSpace(teamID)
	s.agentControlTaskSignalWatchMu.Lock()
	if s.agentControlTaskSignalWatchers == nil {
		s.agentControlTaskSignalWatchers = make(map[int64]taskSignalWatcher)
	}
	s.nextAgentControlTaskSignalWatchID++
	watchID := s.nextAgentControlTaskSignalWatchID
	s.agentControlTaskSignalWatchers[watchID] = taskSignalWatcher{
		workflow: workflow,
		teamID:   teamID,
		ch:       ch,
	}
	s.agentControlTaskSignalWatchMu.Unlock()

	cancel := func() {
		s.agentControlTaskSignalWatchMu.Lock()
		if s.agentControlTaskSignalWatchers != nil {
			delete(s.agentControlTaskSignalWatchers, watchID)
		}
		s.agentControlTaskSignalWatchMu.Unlock()
	}
	if ctx != nil {
		done := ctx.Done()
		if done == nil {
			return ch, cancel
		}
		go func() {
			<-done
			cancel()
		}()
	}
	return ch, cancel
}

// LastAgentControlTaskSignalSeq returns the AgentControl task wake high-water
// mark for the workflow/team filter.
func (s *SQLiteStore) LastAgentControlTaskSignalSeq(ctx context.Context, workflow, teamID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	workflow = normalizeAgentControlTaskWorkflow(workflow)
	teamID = strings.TrimSpace(teamID)
	clauses := []string{"workflow = ?", "kind = ?"}
	args := []interface{}{workflow, agentcontrol.WakeKindTask}
	if teamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, teamID)
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(id), 0)
		FROM agent_control_wake_events
		WHERE `+strings.Join(clauses, " AND ")+`
	`, args...).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("last agent control task signal sequence: %w", err)
	}
	return seq, nil
}

func nextAgentControlMailboxTeamSeqTx(ctx context.Context, tx *sql.Tx, teamID string) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(team_seq), 0) + 1
		FROM agent_control_mailbox_records
		WHERE scope = ? AND workflow = ? AND team_id = ?
	`, agentcontrol.MailboxScopeTeam, agentcontrol.WorkflowSpawnTeam, strings.TrimSpace(teamID)).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("next agent control mailbox team sequence: %w", err)
	}
	return seq, nil
}

func (s *SQLiteStore) appendTaskSignalForTask(ctx context.Context, taskID string, kind string, status TaskStatus) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	task, err := s.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return err
	}
	if status == "" {
		status = task.Status
	}
	return s.appendTaskSignal(ctx, TaskSignal{
		TeamID: task.TeamID,
		TaskID: task.ID,
		Kind:   kind,
		Status: status,
	})
}

func (s *SQLiteStore) appendTaskSignal(ctx context.Context, signal TaskSignal) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("team store is not initialized")
	}
	workflow := normalizeAgentControlTaskWorkflow(signal.Workflow)
	signal.TeamID = strings.TrimSpace(signal.TeamID)
	if signal.TeamID == "" {
		return nil
	}
	signal.TaskID = strings.TrimSpace(signal.TaskID)
	signal.Kind = strings.TrimSpace(signal.Kind)
	if signal.Kind == "" {
		signal.Kind = TaskSignalTaskUpdated
	}
	if signal.CreatedAt.IsZero() {
		signal.CreatedAt = time.Now().UTC()
	}
	agentSignal := signal
	agentSignal.Workflow = workflow
	wakeEvent := agentcontrol.WakeEvent{}
	if err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		seq, err := nextTaskSignalSeqTx(ctx, tx, signal.TeamID)
		if err != nil {
			return err
		}
		signal.Seq = seq
		agentSignal.TeamSeq = seq
		_, err = tx.ExecContext(ctx, `
			INSERT INTO team_task_signals (
				seq, team_id, task_id, kind, status, created_at
			) VALUES (?, ?, ?, ?, ?, ?)
		`, signal.Seq, signal.TeamID, nullablePlainString(signal.TaskID), signal.Kind, string(signal.Status), formatTime(signal.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert task signal: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO agent_control_wake_events (
				workflow, kind, team_id, team_seq, task_id, event_kind, status, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, workflow, agentcontrol.WakeKindTask, signal.TeamID, signal.Seq, nullablePlainString(signal.TaskID), signal.Kind, string(signal.Status), formatTime(signal.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert agent control wake event: %w", err)
		}
		wakeSeq, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("agent control wake event id: %w", err)
		}
		wakeEvent = agentcontrol.WakeEvent{
			Seq:       wakeSeq,
			Workflow:  workflow,
			Kind:      agentcontrol.WakeKindTask,
			TeamID:    signal.TeamID,
			TeamSeq:   signal.Seq,
			TaskID:    signal.TaskID,
			EventKind: signal.Kind,
			Status:    string(signal.Status),
			CreatedAt: signal.CreatedAt,
		}.Normalize()
		agentSignal.Seq = wakeSeq
		return nil
	}); err != nil {
		return err
	}
	s.notifyTaskSignalWatchers(signal)
	s.notifyAgentControlWakeWatchers(wakeEvent)
	s.notifyAgentControlTaskSignalWatchers(agentSignal)
	return nil
}

func normalizeAgentControlTaskWorkflow(workflow string) string {
	workflow = strings.TrimSpace(workflow)
	if workflow == "" {
		return "spawn_team"
	}
	return workflow
}

func nextTaskSignalSeqTx(ctx context.Context, tx *sql.Tx, teamID string) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1
		FROM team_task_signals
		WHERE team_id = ?
	`, strings.TrimSpace(teamID)).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("next task signal sequence: %w", err)
	}
	return seq, nil
}

func (s *SQLiteStore) notifyMailboxWatchers(message MailMessage) {
	if s == nil {
		return
	}
	teamID := strings.TrimSpace(message.TeamID)
	s.mailboxWatchMu.Lock()
	watchers := make([]mailboxWatcher, 0, len(s.mailboxWatchers))
	for _, watcher := range s.mailboxWatchers {
		if watcher.ch == nil {
			continue
		}
		if watcher.teamID != "" && !strings.EqualFold(watcher.teamID, teamID) {
			continue
		}
		watchers = append(watchers, watcher)
	}
	s.mailboxWatchMu.Unlock()
	for _, watcher := range watchers {
		select {
		case watcher.ch <- message:
		default:
		}
	}
}

func (s *SQLiteStore) notifyAgentControlMailboxWatchers(message MailMessage) {
	if s == nil {
		return
	}
	workflow := "spawn_team"
	teamID := strings.TrimSpace(message.TeamID)
	s.agentControlMailboxWatchMu.Lock()
	watchers := make([]mailboxWatcher, 0, len(s.agentControlMailboxWatchers))
	for _, watcher := range s.agentControlMailboxWatchers {
		if watcher.ch == nil {
			continue
		}
		if watcher.workflow != "" && !strings.EqualFold(watcher.workflow, workflow) {
			continue
		}
		if watcher.teamID != "" && !strings.EqualFold(watcher.teamID, teamID) {
			continue
		}
		watchers = append(watchers, watcher)
	}
	s.agentControlMailboxWatchMu.Unlock()
	for _, watcher := range watchers {
		select {
		case watcher.ch <- message:
		default:
		}
	}
}

func (s *SQLiteStore) notifyAgentControlWakeWatchers(event agentcontrol.WakeEvent) {
	if s == nil {
		return
	}
	event = event.Normalize()
	workflow := normalizeAgentControlTaskWorkflow(event.Workflow)
	s.agentControlWakeWatchMu.Lock()
	watchers := make([]agentControlWakeWatcher, 0, len(s.agentControlWakeWatchers))
	for _, watcher := range s.agentControlWakeWatchers {
		if watcher.ch == nil {
			continue
		}
		if watcher.workflow != "" && !strings.EqualFold(watcher.workflow, workflow) {
			continue
		}
		if watcher.kind != "" && !strings.EqualFold(watcher.kind, event.Kind) {
			continue
		}
		if watcher.teamID != "" && !strings.EqualFold(watcher.teamID, event.TeamID) {
			continue
		}
		watchers = append(watchers, watcher)
	}
	s.agentControlWakeWatchMu.Unlock()
	event.Workflow = workflow
	for _, watcher := range watchers {
		select {
		case watcher.ch <- event:
		default:
		}
	}
}

func (s *SQLiteStore) notifyTaskSignalWatchers(signal TaskSignal) {
	if s == nil {
		return
	}
	teamID := strings.TrimSpace(signal.TeamID)
	s.taskSignalWatchMu.Lock()
	watchers := make([]taskSignalWatcher, 0, len(s.taskSignalWatchers))
	for _, watcher := range s.taskSignalWatchers {
		if watcher.ch == nil {
			continue
		}
		if watcher.teamID != "" && !strings.EqualFold(watcher.teamID, teamID) {
			continue
		}
		watchers = append(watchers, watcher)
	}
	s.taskSignalWatchMu.Unlock()
	for _, watcher := range watchers {
		select {
		case watcher.ch <- signal:
		default:
		}
	}
}

func (s *SQLiteStore) notifyAgentControlTaskSignalWatchers(signal TaskSignal) {
	if s == nil {
		return
	}
	workflow := normalizeAgentControlTaskWorkflow(signal.Workflow)
	teamID := strings.TrimSpace(signal.TeamID)
	s.agentControlTaskSignalWatchMu.Lock()
	watchers := make([]taskSignalWatcher, 0, len(s.agentControlTaskSignalWatchers))
	for _, watcher := range s.agentControlTaskSignalWatchers {
		if watcher.ch == nil {
			continue
		}
		if watcher.workflow != "" && !strings.EqualFold(watcher.workflow, workflow) {
			continue
		}
		if watcher.teamID != "" && !strings.EqualFold(watcher.teamID, teamID) {
			continue
		}
		watchers = append(watchers, watcher)
	}
	s.agentControlTaskSignalWatchMu.Unlock()
	for _, watcher := range watchers {
		select {
		case watcher.ch <- signal:
		default:
		}
	}
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
			UPDATE agent_control_mailbox_records
			SET acked_at = COALESCE(?, acked_at)
			WHERE scope = ? AND workflow = ? AND team_id = ? AND message_id = ?
		`, ackValue, agentcontrol.MailboxScopeTeam, agentcontrol.WorkflowSpawnTeam, receipt.TeamID, receipt.MessageID)
		if err != nil {
			return fmt.Errorf("update agent control mailbox record acked_at: %w", err)
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
	createdAt := formatTime(event.Timestamp)

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
		`, teamID, seq, eventType, payloadJSON, createdAt); err != nil {
			return fmt.Errorf("insert team event: %w", err)
		}
		if strings.HasPrefix(eventType, "task.") {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO agent_control_task_graph_events (
					workflow, team_id, team_seq, type, payload_json, created_at
				) VALUES (?, ?, ?, ?, ?, ?)
			`, "spawn_team", teamID, seq, eventType, payloadJSON, createdAt); err != nil {
				return fmt.Errorf("insert agent control task graph event: %w", err)
			}
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

// LastTeamEventSeq returns the current durable team event high-water mark.
func (s *SQLiteStore) LastTeamEventSeq(ctx context.Context, teamID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("team store is not initialized")
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0)
		FROM team_events
		WHERE team_id = ?
	`, strings.TrimSpace(teamID)).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("last team event sequence: %w", err)
	}
	return seq, nil
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
		{
			Version: 7,
			Name:    "team_mailbox_sequence",
			UpSQL: `
				ALTER TABLE team_mailbox_messages ADD COLUMN seq INTEGER NOT NULL DEFAULT 0;
				WITH ordered AS (
					SELECT id, ROW_NUMBER() OVER (PARTITION BY team_id ORDER BY created_at ASC, id ASC) AS rn
					FROM team_mailbox_messages
				)
				UPDATE team_mailbox_messages
				SET seq = (
					SELECT rn FROM ordered WHERE ordered.id = team_mailbox_messages.id
				)
				WHERE seq = 0;
				CREATE UNIQUE INDEX IF NOT EXISTS idx_mailbox_team_seq
				ON team_mailbox_messages(team_id, seq);
				CREATE INDEX IF NOT EXISTS idx_mailbox_team_after_seq
				ON team_mailbox_messages(team_id, seq);
			`,
		},
		{
			Version: 8,
			Name:    "team_task_signals",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS team_task_signals (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					team_id TEXT NOT NULL,
					seq INTEGER NOT NULL,
					task_id TEXT,
					kind TEXT NOT NULL,
					status TEXT,
					created_at TEXT NOT NULL,
					UNIQUE(team_id, seq)
				);
				CREATE INDEX IF NOT EXISTS idx_team_task_signals_team_seq
				ON team_task_signals(team_id, seq);
				CREATE INDEX IF NOT EXISTS idx_team_task_signals_team_created
				ON team_task_signals(team_id, created_at);
			`,
		},
		{
			Version: 9,
			Name:    "agent_control_task_graph_events",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS agent_control_task_graph_events (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					workflow TEXT NOT NULL,
					team_id TEXT NOT NULL,
					team_seq INTEGER NOT NULL,
					type TEXT NOT NULL,
					payload_json TEXT NOT NULL DEFAULT '{}',
					created_at TEXT NOT NULL,
					UNIQUE(workflow, team_id, team_seq)
				);
				CREATE INDEX IF NOT EXISTS idx_agent_control_task_graph_workflow_id
				ON agent_control_task_graph_events(workflow, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_task_graph_team_id
				ON agent_control_task_graph_events(workflow, team_id, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_task_graph_type_id
				ON agent_control_task_graph_events(workflow, type, id);
				INSERT OR IGNORE INTO agent_control_task_graph_events (
					workflow, team_id, team_seq, type, payload_json, created_at
				)
				SELECT 'spawn_team', team_id, seq, type, payload_json, created_at
				FROM team_events
				WHERE type LIKE 'task.%'
				ORDER BY id ASC;
			`,
		},
		{
			Version: 10,
			Name:    "agent_control_task_wake_signals",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS agent_control_task_wake_signals (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					workflow TEXT NOT NULL,
					team_id TEXT NOT NULL,
					team_seq INTEGER NOT NULL,
					task_id TEXT,
					kind TEXT NOT NULL,
					status TEXT,
					created_at TEXT NOT NULL,
					UNIQUE(workflow, team_id, team_seq)
				);
				CREATE INDEX IF NOT EXISTS idx_agent_control_task_wake_workflow_id
				ON agent_control_task_wake_signals(workflow, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_task_wake_team_id
				ON agent_control_task_wake_signals(workflow, team_id, id);
				INSERT OR IGNORE INTO agent_control_task_wake_signals (
					workflow, team_id, team_seq, task_id, kind, status, created_at
				)
				SELECT 'spawn_team', team_id, seq, task_id, kind, status, created_at
				FROM team_task_signals
				ORDER BY id ASC;
			`,
		},
		{
			Version: 11,
			Name:    "agent_control_mailbox_wake_messages",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS agent_control_mailbox_wake_messages (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					workflow TEXT NOT NULL,
					team_id TEXT NOT NULL,
					team_seq INTEGER NOT NULL,
					message_id TEXT NOT NULL,
					from_agent TEXT,
					to_agent TEXT,
					task_id TEXT,
					kind TEXT,
					created_at TEXT NOT NULL,
					UNIQUE(workflow, team_id, team_seq)
				);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_wake_workflow_id
				ON agent_control_mailbox_wake_messages(workflow, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_wake_team_id
				ON agent_control_mailbox_wake_messages(workflow, team_id, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_wake_message
				ON agent_control_mailbox_wake_messages(message_id);
				INSERT OR IGNORE INTO agent_control_mailbox_wake_messages (
					workflow, team_id, team_seq, message_id, from_agent, to_agent, task_id, kind, created_at
				)
				SELECT 'spawn_team', team_id, seq, id, from_agent, to_agent, task_id, kind, created_at
				FROM team_mailbox_messages
				ORDER BY seq ASC, id ASC;
			`,
		},
		{
			Version: 12,
			Name:    "agent_control_task_records",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS agent_control_task_records (
					workflow TEXT NOT NULL,
					task_id TEXT NOT NULL,
					team_id TEXT NOT NULL,
					parent_task_id TEXT,
					assignee TEXT,
					session_id TEXT,
					agent_path TEXT,
					title TEXT,
					summary TEXT,
					status TEXT,
					priority INTEGER NOT NULL DEFAULT 0,
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL,
					PRIMARY KEY (workflow, task_id)
				);
				CREATE INDEX IF NOT EXISTS idx_agent_control_task_records_team
				ON agent_control_task_records(workflow, team_id, status, priority DESC);
				CREATE INDEX IF NOT EXISTS idx_agent_control_task_records_assignee
				ON agent_control_task_records(workflow, team_id, assignee);
				CREATE INDEX IF NOT EXISTS idx_agent_control_task_records_path
				ON agent_control_task_records(workflow, agent_path);
				INSERT OR IGNORE INTO agent_control_task_records (
					workflow, task_id, team_id, parent_task_id, assignee, session_id, agent_path,
					title, summary, status, priority, created_at, updated_at
				)
				SELECT
					'spawn_team',
					t.id,
					t.team_id,
					t.parent_task_id,
					t.assignee,
					m.session_id,
					CASE
						WHEN t.assignee IS NULL OR TRIM(t.assignee) = '' THEN NULL
						ELSE '/root/teams/' || t.team_id || '/' || t.assignee
					END,
					t.title,
					t.summary,
					t.status,
					t.priority,
					t.created_at,
					t.updated_at
				FROM team_tasks t
				LEFT JOIN teammates m ON m.team_id = t.team_id AND m.id = t.assignee;
			`,
		},
		{
			Version: 13,
			Name:    "agent_control_task_dependencies",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS agent_control_task_dependencies (
					workflow TEXT NOT NULL,
					dependency_id TEXT NOT NULL,
					team_id TEXT NOT NULL,
					task_id TEXT NOT NULL,
					depends_on_id TEXT NOT NULL,
					created_at TEXT NOT NULL,
					PRIMARY KEY (workflow, task_id, depends_on_id)
				);
				CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_control_task_dependencies_id
				ON agent_control_task_dependencies(workflow, dependency_id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_task_dependencies_task
				ON agent_control_task_dependencies(workflow, task_id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_task_dependencies_depends_on
				ON agent_control_task_dependencies(workflow, depends_on_id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_task_dependencies_team
				ON agent_control_task_dependencies(workflow, team_id);
				INSERT OR IGNORE INTO agent_control_task_dependencies (
					workflow, dependency_id, team_id, task_id, depends_on_id, created_at
				)
				SELECT
					'spawn_team',
					d.id,
					t.team_id,
					d.task_id,
					d.depends_on_id,
					d.created_at
				FROM team_task_dependencies d
				JOIN team_tasks t ON t.id = d.task_id;
			`,
		},
		{
			Version: 14,
			Name:    "agent_control_task_records_full_fields",
			UpSQL: `
				ALTER TABLE agent_control_task_records ADD COLUMN goal TEXT;
				ALTER TABLE agent_control_task_records ADD COLUMN inputs_json TEXT NOT NULL DEFAULT '[]';
				ALTER TABLE agent_control_task_records ADD COLUMN read_paths_json TEXT NOT NULL DEFAULT '[]';
				ALTER TABLE agent_control_task_records ADD COLUMN write_paths_json TEXT NOT NULL DEFAULT '[]';
				ALTER TABLE agent_control_task_records ADD COLUMN deliverables_json TEXT NOT NULL DEFAULT '[]';
				ALTER TABLE agent_control_task_records ADD COLUMN lease_until TEXT;
				ALTER TABLE agent_control_task_records ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
				ALTER TABLE agent_control_task_records ADD COLUMN result_ref TEXT;
				ALTER TABLE agent_control_task_records ADD COLUMN version INTEGER NOT NULL DEFAULT 1;
				UPDATE agent_control_task_records
				SET
					goal = (SELECT t.goal FROM team_tasks t WHERE t.id = agent_control_task_records.task_id),
					inputs_json = COALESCE((SELECT t.inputs_json FROM team_tasks t WHERE t.id = agent_control_task_records.task_id), '[]'),
					read_paths_json = COALESCE((SELECT t.read_paths_json FROM team_tasks t WHERE t.id = agent_control_task_records.task_id), '[]'),
					write_paths_json = COALESCE((SELECT t.write_paths_json FROM team_tasks t WHERE t.id = agent_control_task_records.task_id), '[]'),
					deliverables_json = COALESCE((SELECT t.deliverables_json FROM team_tasks t WHERE t.id = agent_control_task_records.task_id), '[]'),
					lease_until = (SELECT t.lease_until FROM team_tasks t WHERE t.id = agent_control_task_records.task_id),
					retry_count = COALESCE((SELECT t.retry_count FROM team_tasks t WHERE t.id = agent_control_task_records.task_id), 0),
					result_ref = (SELECT t.result_ref FROM team_tasks t WHERE t.id = agent_control_task_records.task_id),
					version = COALESCE((SELECT t.version FROM team_tasks t WHERE t.id = agent_control_task_records.task_id), 1);
			`,
		},
		{
			Version: 15,
			Name:    "agent_control_mailbox_messages",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS agent_control_mailbox_messages (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					workflow TEXT NOT NULL,
					team_id TEXT NOT NULL,
					team_seq INTEGER NOT NULL,
					message_id TEXT NOT NULL,
					from_agent TEXT,
					to_agent TEXT,
					task_id TEXT,
					kind TEXT,
					body TEXT NOT NULL,
					metadata_json TEXT NOT NULL DEFAULT '{}',
					created_at TEXT NOT NULL,
					acked_at TEXT,
					UNIQUE(workflow, team_id, team_seq)
				);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_messages_workflow_id
				ON agent_control_mailbox_messages(workflow, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_messages_team_seq
				ON agent_control_mailbox_messages(workflow, team_id, team_seq);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_messages_message
				ON agent_control_mailbox_messages(message_id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_messages_team_to
				ON agent_control_mailbox_messages(workflow, team_id, to_agent, created_at DESC);
				INSERT OR IGNORE INTO agent_control_mailbox_messages (
					workflow, team_id, team_seq, message_id, from_agent, to_agent, task_id, kind, body, metadata_json, created_at, acked_at
				)
				SELECT
					'spawn_team',
					team_id,
					seq,
					id,
					from_agent,
					to_agent,
					task_id,
					kind,
					body,
					metadata_json,
					created_at,
					acked_at
				FROM team_mailbox_messages
				ORDER BY seq ASC, id ASC;
			`,
		},
		{
			Version: 16,
			Name:    "agent_control_wake_events",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS agent_control_wake_events (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					workflow TEXT NOT NULL,
					kind TEXT NOT NULL,
					team_id TEXT,
					team_seq INTEGER NOT NULL DEFAULT 0,
					session_id TEXT,
					message_id TEXT,
					task_id TEXT,
					event_kind TEXT,
					status TEXT,
					from_agent TEXT,
					to_agent TEXT,
					created_at TEXT NOT NULL
				);
				CREATE INDEX IF NOT EXISTS idx_agent_control_wake_events_workflow_kind_id
				ON agent_control_wake_events(workflow, kind, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_wake_events_team_kind_id
				ON agent_control_wake_events(workflow, kind, team_id, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_wake_events_message
				ON agent_control_wake_events(message_id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_wake_events_task
				ON agent_control_wake_events(task_id);
				INSERT INTO agent_control_wake_events (
					workflow, kind, team_id, team_seq, session_id, message_id, task_id, event_kind, status, from_agent, to_agent, created_at
				)
				SELECT workflow, kind, team_id, team_seq, session_id, message_id, task_id, event_kind, status, from_agent, to_agent, created_at
				FROM (
					SELECT
						workflow,
						'task' AS kind,
						team_id,
						team_seq,
						NULL AS session_id,
						NULL AS message_id,
						task_id,
						kind AS event_kind,
						status,
						NULL AS from_agent,
						NULL AS to_agent,
						created_at
					FROM agent_control_task_wake_signals
					UNION ALL
					SELECT
						workflow,
						'mailbox' AS kind,
						team_id,
						team_seq,
						NULL AS session_id,
						message_id,
						task_id,
						kind AS event_kind,
						NULL AS status,
						from_agent,
						to_agent,
						created_at
					FROM agent_control_mailbox_wake_messages
				)
				ORDER BY created_at ASC, kind ASC, team_seq ASC;
			`,
		},
		{
			Version: 17,
			Name:    "agent_control_mailbox_records",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS agent_control_mailbox_records (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					scope TEXT NOT NULL,
					workflow TEXT,
					session_id TEXT,
					session_mailbox_seq INTEGER NOT NULL DEFAULT 0,
					team_id TEXT,
					team_seq INTEGER NOT NULL DEFAULT 0,
					message_id TEXT NOT NULL,
					from_agent TEXT,
					to_agent TEXT,
					task_id TEXT,
					kind TEXT,
					body TEXT NOT NULL,
					metadata_json TEXT NOT NULL DEFAULT '{}',
					created_at TEXT NOT NULL,
					acked_at TEXT
				);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_records_scope_id
				ON agent_control_mailbox_records(scope, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_records_session_id
				ON agent_control_mailbox_records(scope, session_id, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_records_team_id
				ON agent_control_mailbox_records(scope, team_id, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_records_message
				ON agent_control_mailbox_records(message_id);
				INSERT INTO agent_control_mailbox_records (
					scope, workflow, session_id, session_mailbox_seq, team_id, team_seq, message_id,
					from_agent, to_agent, task_id, kind, body, metadata_json, created_at, acked_at
				)
				SELECT
					'team',
					workflow,
					NULL,
					0,
					team_id,
					team_seq,
					message_id,
					from_agent,
					to_agent,
					task_id,
					kind,
					body,
					metadata_json,
					created_at,
					acked_at
				FROM agent_control_mailbox_messages
				ORDER BY workflow ASC, team_id ASC, team_seq ASC, id ASC;
			`,
		},
		{
			Version: 18,
			Name:    "drop_legacy_agent_control_mirrors",
			UpSQL: `
				DROP TABLE IF EXISTS agent_control_mailbox_messages;
				DROP TABLE IF EXISTS team_mailbox_messages;
				DROP TABLE IF EXISTS agent_control_mailbox_wake_messages;
				DROP TABLE IF EXISTS agent_control_task_wake_signals;
			`,
		},
		{
			Version: 19,
			Name:    "drop_legacy_task_mirrors",
			UpSQL: `
				DROP TABLE IF EXISTS team_task_dependencies;
				DROP TABLE IF EXISTS team_tasks;
			`,
		},
		{
			Version: 20,
			Name:    "agent_control_mailbox_global_projection",
			UpSQL: `
				ALTER TABLE agent_control_mailbox_records ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_records_global_seq
				ON agent_control_mailbox_records(global_seq);
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
	dsn = normalizeSQLiteDSN(dsn)
	dsn = ensureSQLiteDSNOption(dsn, "_txlock", "immediate")
	dsn = ensureSQLiteDSNOption(dsn, "_busy_timeout", "5000")
	return dsn
}

func normalizeSQLiteDSN(dsn string) string {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	if lower == "" || lower == ":memory:" || strings.HasPrefix(lower, "file:") {
		return dsn
	}
	pathPart, queryPart, hasQuery := strings.Cut(dsn, "?")
	uri := "file:" + filepath.ToSlash(filepath.Clean(pathPart))
	if hasQuery {
		return uri + "?" + queryPart
	}
	return uri
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

func isSQLiteMemoryDSN(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	if lower == ":memory:" {
		return true
	}
	return strings.Contains(lower, "mode=memory")
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

func nullablePlainString(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
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
