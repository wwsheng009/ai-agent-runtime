package background

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	_ "github.com/mattn/go-sqlite3"
)

// StoreConfig configures background job persistence.
type StoreConfig struct {
	Path string
	DSN  string
}

// JobEvent captures a background job event.
type JobEvent struct {
	Seq       int64                  `json:"seq"`
	JobID     string                 `json:"job_id"`
	Type      string                 `json:"type"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
}

// Store defines persistence for background jobs.
type Store interface {
	SaveJob(ctx context.Context, job Job) error
	UpdateJob(ctx context.Context, job Job) error
	GetJob(ctx context.Context, jobID string) (*Job, error)
}

// JobLister lists background jobs.
type JobLister interface {
	ListJobs(ctx context.Context, filter JobFilter) ([]Job, error)
}

// EventWriter appends background job events.
type EventWriter interface {
	AppendEvent(ctx context.Context, jobID, eventType string, payload map[string]interface{}) error
}

// EventReader reads background job events.
type EventReader interface {
	ListEvents(ctx context.Context, jobID string, afterSeq int64, limit int) ([]JobEvent, error)
}

// SQLiteStore persists background jobs in sqlite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates a sqlite-backed background store.
func NewSQLiteStore(cfg *StoreConfig) (*SQLiteStore, error) {
	if cfg == nil {
		cfg = &StoreConfig{}
	}
	dsn, err := resolveBackgroundDSN(cfg)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open background db: %w", err)
	}
	store := &SQLiteStore{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the store.
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// UpsertJob upserts a background job record.
func (s *SQLiteStore) SaveJob(ctx context.Context, job Job) error {
	return s.upsertJob(ctx, job)
}

// UpdateJob updates a background job record.
func (s *SQLiteStore) UpdateJob(ctx context.Context, job Job) error {
	return s.upsertJob(ctx, job)
}

func (s *SQLiteStore) upsertJob(ctx context.Context, job Job) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("background store is not initialized")
	}
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("job id is required")
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	metadataJSON := "{}"
	if len(job.Metadata) > 0 {
		payload, err := json.Marshal(job.Metadata)
		if err != nil {
			return fmt.Errorf("marshal job metadata: %w", err)
		}
		metadataJSON = string(payload)
	}
	kind := strings.TrimSpace(job.Kind)
	if kind == "" {
		kind = "unknown"
	}
	sessionID := strings.TrimSpace(job.SessionID)
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO background_jobs (
            id, session_id, kind, status, message, command, cwd, priority, created_at,
            started_at, finished_at, exit_code, log_path, metadata_json
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            session_id = excluded.session_id,
            kind = excluded.kind,
            status = excluded.status,
            message = excluded.message,
            command = excluded.command,
            cwd = excluded.cwd,
            priority = excluded.priority,
            created_at = excluded.created_at,
            started_at = excluded.started_at,
            finished_at = excluded.finished_at,
            exit_code = excluded.exit_code,
            log_path = excluded.log_path,
            metadata_json = excluded.metadata_json
    `,
		job.ID,
		sessionID,
		kind,
		string(job.Status),
		nullIfEmpty(job.Message),
		nullIfEmpty(job.Command),
		nullIfEmpty(job.Cwd),
		job.Priority,
		job.CreatedAt.Format(time.RFC3339Nano),
		formatTimePtr(job.StartedAt),
		formatTimePtr(job.FinishedAt),
		job.ExitCode,
		nullIfEmpty(job.LogPath),
		metadataJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert background job: %w", err)
	}
	return nil
}

// GetJob returns a background job.
func (s *SQLiteStore) GetJob(ctx context.Context, jobID string) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("background store is not initialized")
	}
	row := s.db.QueryRowContext(ctx, `
        SELECT id, session_id, kind, status, message, command, cwd, priority, created_at,
               started_at, finished_at, exit_code, log_path, metadata_json
        FROM background_jobs
        WHERE id = ?
    `, jobID)

	var (
		job           Job
		statusRaw     string
		messageRaw    sql.NullString
		createdAtRaw  string
		startedAtRaw  sql.NullString
		finishedAtRaw sql.NullString
		sessionIDRaw  sql.NullString
		kindRaw       sql.NullString
		commandRaw    sql.NullString
		cwdRaw        sql.NullString
		logPathRaw    sql.NullString
		metadataJSON  string
		exitCodeRaw   sql.NullInt64
	)
	if err := row.Scan(
		&job.ID,
		&sessionIDRaw,
		&kindRaw,
		&statusRaw,
		&messageRaw,
		&commandRaw,
		&cwdRaw,
		&job.Priority,
		&createdAtRaw,
		&startedAtRaw,
		&finishedAtRaw,
		&exitCodeRaw,
		&logPathRaw,
		&metadataJSON,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get background job: %w", err)
	}

	job.Status = JobStatus(statusRaw)
	if messageRaw.Valid {
		job.Message = messageRaw.String
	}
	if sessionIDRaw.Valid {
		job.SessionID = sessionIDRaw.String
	}
	if kindRaw.Valid {
		job.Kind = kindRaw.String
	}
	if commandRaw.Valid {
		job.Command = commandRaw.String
	}
	if cwdRaw.Valid {
		job.Cwd = cwdRaw.String
	}
	if logPathRaw.Valid {
		job.LogPath = logPathRaw.String
	}
	if createdAtRaw != "" {
		job.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtRaw)
	}
	if startedAtRaw.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, startedAtRaw.String); err == nil {
			job.StartedAt = &parsed
		}
	}
	if finishedAtRaw.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, finishedAtRaw.String); err == nil {
			job.FinishedAt = &parsed
		}
	}
	if exitCodeRaw.Valid {
		code := int(exitCodeRaw.Int64)
		job.ExitCode = &code
	}
	if metadataJSON != "" {
		_ = json.Unmarshal([]byte(metadataJSON), &job.Metadata)
	}
	job.RestartPolicy = normalizeLoadedRestartPolicy(job)

	return &job, nil
}

// ListJobs returns background jobs matching the filter.
func (s *SQLiteStore) ListJobs(ctx context.Context, filter JobFilter) ([]Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("background store is not initialized")
	}

	clauses := make([]string, 0, 2)
	args := make([]interface{}, 0, 4)
	if strings.TrimSpace(filter.SessionID) != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, strings.TrimSpace(filter.SessionID))
	}
	if len(filter.Status) > 0 {
		statuses := make([]string, 0, len(filter.Status))
		for _, status := range filter.Status {
			if strings.TrimSpace(string(status)) == "" {
				continue
			}
			statuses = append(statuses, string(status))
		}
		if len(statuses) > 0 {
			placeholders := make([]string, len(statuses))
			for i, value := range statuses {
				placeholders[i] = "?"
				args = append(args, value)
			}
			clauses = append(clauses, "status IN ("+strings.Join(placeholders, ",")+")")
		}
	}

	query := `
		SELECT id, session_id, kind, status, message, command, cwd, priority, created_at,
		       started_at, finished_at, exit_code, log_path, metadata_json
		FROM background_jobs
	`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
		if filter.Offset > 0 {
			query += " OFFSET ?"
			args = append(args, filter.Offset)
		}
	} else if filter.Offset > 0 {
		query += " LIMIT -1 OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list background jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]Job, 0)
	for rows.Next() {
		var (
			job           Job
			statusRaw     string
			messageRaw    sql.NullString
			createdAtRaw  string
			startedAtRaw  sql.NullString
			finishedAtRaw sql.NullString
			sessionIDRaw  sql.NullString
			kindRaw       sql.NullString
			commandRaw    sql.NullString
			cwdRaw        sql.NullString
			logPathRaw    sql.NullString
			metadataJSON  string
			exitCodeRaw   sql.NullInt64
		)
		if err := rows.Scan(
			&job.ID,
			&sessionIDRaw,
			&kindRaw,
			&statusRaw,
			&messageRaw,
			&commandRaw,
			&cwdRaw,
			&job.Priority,
			&createdAtRaw,
			&startedAtRaw,
			&finishedAtRaw,
			&exitCodeRaw,
			&logPathRaw,
			&metadataJSON,
		); err != nil {
			return nil, fmt.Errorf("scan background job: %w", err)
		}
		job.Status = JobStatus(statusRaw)
		if messageRaw.Valid {
			job.Message = messageRaw.String
		}
		if sessionIDRaw.Valid {
			job.SessionID = sessionIDRaw.String
		}
		if kindRaw.Valid {
			job.Kind = kindRaw.String
		}
		if commandRaw.Valid {
			job.Command = commandRaw.String
		}
		if cwdRaw.Valid {
			job.Cwd = cwdRaw.String
		}
		if logPathRaw.Valid {
			job.LogPath = logPathRaw.String
		}
		if createdAtRaw != "" {
			job.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtRaw)
		}
		if startedAtRaw.Valid {
			if parsed, err := time.Parse(time.RFC3339Nano, startedAtRaw.String); err == nil {
				job.StartedAt = &parsed
			}
		}
		if finishedAtRaw.Valid {
			if parsed, err := time.Parse(time.RFC3339Nano, finishedAtRaw.String); err == nil {
				job.FinishedAt = &parsed
			}
		}
		if exitCodeRaw.Valid {
			code := int(exitCodeRaw.Int64)
			job.ExitCode = &code
		}
		if metadataJSON != "" {
			_ = json.Unmarshal([]byte(metadataJSON), &job.Metadata)
		}
		job.RestartPolicy = normalizeLoadedRestartPolicy(job)
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// AppendEvent stores a job event and returns its sequence.
func (s *SQLiteStore) AppendEvent(ctx context.Context, jobID, eventType string, payload map[string]interface{}) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("background store is not initialized")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job id is required")
	}
	if strings.TrimSpace(eventType) == "" {
		return fmt.Errorf("event type is required")
	}
	if payload == nil {
		payload = map[string]interface{}{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal job event payload: %w", err)
	}
	createdAt := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin job event tx: %w", err)
	}

	var seq int64
	if err := tx.QueryRowContext(ctx, `
        SELECT COALESCE(MAX(seq), 0) + 1 FROM background_job_events WHERE job_id = ?
    `, jobID).Scan(&seq); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("next job event seq: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
        INSERT INTO background_job_events (
            job_id, seq, type, payload_json, created_at
        ) VALUES (?, ?, ?, ?, ?)
    `, jobID, seq, strings.TrimSpace(eventType), string(payloadJSON), createdAt.Format(time.RFC3339Nano))
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("insert job event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit job event: %w", err)
	}
	return nil
}

// ListEvents returns job events after the specified sequence.
func (s *SQLiteStore) ListEvents(ctx context.Context, jobID string, afterSeq int64, limit int) ([]JobEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("background store is not initialized")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job id is required")
	}
	query := `
        SELECT seq, type, payload_json, created_at
        FROM background_job_events
        WHERE job_id = ? AND seq > ?
        ORDER BY seq ASC
    `
	args := []interface{}{jobID, afterSeq}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list job events: %w", err)
	}
	defer rows.Close()

	events := make([]JobEvent, 0)
	for rows.Next() {
		var (
			seq        int64
			eventType  string
			payloadRaw string
			createdRaw string
		)
		if err := rows.Scan(&seq, &eventType, &payloadRaw, &createdRaw); err != nil {
			return nil, fmt.Errorf("scan job event: %w", err)
		}
		event := JobEvent{Seq: seq, JobID: jobID, Type: eventType, Payload: map[string]interface{}{}}
		if payloadRaw != "" {
			_ = json.Unmarshal([]byte(payloadRaw), &event.Payload)
		}
		if createdRaw != "" {
			event.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (s *SQLiteStore) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS background_jobs (
            id TEXT PRIMARY KEY,
            session_id TEXT NOT NULL,
            kind TEXT NOT NULL,
            status TEXT NOT NULL,
            message TEXT,
            command TEXT,
            cwd TEXT,
            priority INTEGER NOT NULL DEFAULT 0,
            created_at TEXT NOT NULL,
            started_at TEXT,
            finished_at TEXT,
            exit_code INTEGER,
            log_path TEXT,
            metadata_json BLOB
        )
    `); err != nil {
		return fmt.Errorf("create background_jobs table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS background_job_events (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            job_id TEXT NOT NULL,
            seq INTEGER NOT NULL,
            type TEXT NOT NULL,
            payload_json BLOB NOT NULL,
            created_at TEXT NOT NULL,
            UNIQUE(job_id, seq)
        )
    `); err != nil {
		return fmt.Errorf("create background_job_events table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
        CREATE INDEX IF NOT EXISTS idx_background_job_events_job_seq
        ON background_job_events(job_id, seq)
    `); err != nil {
		return fmt.Errorf("create background_job_events index: %w", err)
	}
	if err := s.ensureBackgroundJobsColumn(ctx, "message", "TEXT"); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStore) ensureBackgroundJobsColumn(ctx context.Context, columnName, columnType string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("background store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(background_jobs)`)
	if err != nil {
		return fmt.Errorf("inspect background_jobs schema: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			typ        string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); err != nil {
			return fmt.Errorf("scan background_jobs schema: %w", err)
		}
		if strings.EqualFold(name, columnName) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate background_jobs schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE background_jobs ADD COLUMN %s %s`, columnName, columnType)); err != nil {
		return fmt.Errorf("alter background_jobs add %s: %w", columnName, err)
	}
	return nil
}

func resolveBackgroundDSN(cfg *StoreConfig) (string, error) {
	if cfg.Path != "" {
		dir := filepath.Dir(cfg.Path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("create background store directory: %w", err)
			}
		}
		return cfg.Path, nil
	}
	if cfg.DSN != "" {
		return cfg.DSN, nil
	}
	return fmt.Sprintf("file:background-store-%s?mode=memory&cache=shared", uuid.NewString()), nil
}

func nullIfEmpty(value string) interface{} {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func formatTimePtr(value *time.Time) interface{} {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func normalizeLoadedRestartPolicy(job Job) RestartPolicy {
	if normalized := normalizeRestartPolicy(job.RestartPolicy); normalized != RestartPolicyFail {
		return normalized
	}
	if text, ok := stringMetadataValue(job.Metadata, "restart_policy"); ok {
		return normalizeRestartPolicy(RestartPolicy(text))
	}
	return RestartPolicyFail
}
