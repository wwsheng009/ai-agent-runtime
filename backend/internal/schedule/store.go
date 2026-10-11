package schedule

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// instantLayout 是持久化的 UTC 毫秒时点格式（四位数年份，以字面量 Z 结尾）。
const instantLayout = "2006-01-02T15:04:05.000Z"

func formatInstant(t time.Time) string { return t.UTC().Format(instantLayout) }

func parseInstant(raw string) (time.Time, error) {
	return time.Parse(instantLayout, strings.TrimSpace(raw))
}

// Store 是 Schedule 的持久化接口。
type Store interface {
	// Close 关闭底层存储。
	Close() error
	// List 返回全部任务（catalog 用），按 scheduled_at 升序、id 字典序。
	List() ([]Task, error)
	// ListActiveBySession 返回某会话的活动任务。
	ListActiveBySession(sessionID string) ([]Task, error)
	// ListActiveDue 返回 status=active 且 scheduled_at <= now 的任务。
	ListActiveDue(now time.Time) ([]Task, error)
	// Get 读取一条任务。
	Get(id string) (Task, bool, error)
	// Put 插入一条新任务（已存在则失败）。
	Put(task Task) error
	// UpdateCAS 以 state_version 比较并更新；返回是否生效。
	UpdateCAS(task Task, expectedVersion int64) (bool, error)
	// Delete 硬删除任务及其历史；返回是否存在。
	Delete(id string) (bool, error)
}

// SQLiteStore 是基于 SQLite 的 Store 实现。
type SQLiteStore struct {
	db *sql.DB
}

var _ Store = (*SQLiteStore)(nil)

// OpenStore 打开（或创建）schedule.sqlite 并应用迁移。
func OpenStore(path string) (*SQLiteStore, error) {
	db, err := sqliteutil.OpenFile(path, true)
	if err != nil {
		return nil, fmt.Errorf("open schedule db: %w", err)
	}
	store := &SQLiteStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// NewMemoryStore 打开一个进程内内存库（测试用）。
func NewMemoryStore() (*SQLiteStore, error) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open in-memory schedule db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	migrations := []migrate.Migration{{
		Version: 1,
		Name:    "schedule_tasks",
		UpSQL: `
CREATE TABLE IF NOT EXISTS schedule_tasks (
    id                TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL,
    kind              TEXT NOT NULL,
    title             TEXT NOT NULL,
    prompt            TEXT NOT NULL,
    scheduled_at      TEXT NOT NULL,
    after_seconds     INTEGER,
    every_seconds     INTEGER,
    local_time        TEXT,
    time_zone         TEXT,
    weekdays_json     TEXT,
    expression        TEXT,
    status            TEXT NOT NULL,
    last_scheduled_at TEXT,
    last_delivered_at TEXT,
    last_message_id   TEXT,
    state_version     INTEGER NOT NULL DEFAULT 0,
    owner_instance_id TEXT,
    lease_expires_at  TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_schedule_due ON schedule_tasks(status, scheduled_at);
CREATE INDEX IF NOT EXISTS idx_schedule_session ON schedule_tasks(session_id, status);
CREATE INDEX IF NOT EXISTS idx_schedule_lease ON schedule_tasks(lease_expires_at);
CREATE TABLE IF NOT EXISTS schedule_delivery_history (
    schedule_id  TEXT NOT NULL,
    seq          INTEGER NOT NULL,
    scheduled_at TEXT NOT NULL,
    delivered_at TEXT NOT NULL,
    message_id   TEXT NOT NULL,
    prompt       TEXT,
    PRIMARY KEY (schedule_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_schedule_history_task_seq ON schedule_delivery_history(schedule_id, seq);
`,
	}}
	return migrate.Apply(ctx, s.db, migrations)
}

// Close 关闭底层数据库。
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB 暴露底层句柄（供历史查询等同包扩展使用）。
func (s *SQLiteStore) DB() *sql.DB { return s.db }

const taskColumns = `id, session_id, kind, title, prompt, scheduled_at, after_seconds, every_seconds,
    local_time, time_zone, weekdays_json, expression, status,
    last_scheduled_at, last_delivered_at, last_message_id, state_version`

func scanTask(scanner interface{ Scan(...any) error }) (Task, error) {
	var (
		task        Task
		id          string
		kind        string
		scheduledAt string
		afterRaw    sql.NullInt64
		everyRaw    sql.NullInt64
		localTime   sql.NullString
		timeZone    sql.NullString
		weekdaysRaw sql.NullString
		expression  sql.NullString
		status      string
		lastSched   sql.NullString
		lastDeliv   sql.NullString
		lastMsgID   sql.NullString
		version     int64
	)
	if err := scanner.Scan(&id, &task.SessionID, &kind, &task.Record.Title, &task.Record.Prompt, &scheduledAt,
		&afterRaw, &everyRaw, &localTime, &timeZone, &weekdaysRaw, &expression, &status,
		&lastSched, &lastDeliv, &lastMsgID, &version); err != nil {
		return Task{}, err
	}
	target, err := parseInstant(scheduledAt)
	if err != nil {
		return Task{}, fmt.Errorf("parse scheduled_at for %s: %w", id, err)
	}
	task.Record.ID = id
	task.Record.Kind = Kind(kind)
	task.Record.ScheduledAt = target
	task.Record.AfterSeconds = afterRaw.Int64
	task.Record.EverySeconds = everyRaw.Int64
	task.Record.Time = localTime.String
	task.Record.TimeZone = timeZone.String
	task.Record.Expression = expression.String
	task.Status = Status(status)
	task.StateVersion = version
	if lastSched.Valid && lastDeliv.Valid && lastMsgID.Valid {
		delivered, derr := parseInstant(lastDeliv.String)
		if derr != nil {
			return Task{}, fmt.Errorf("parse last_delivered_at for %s: %w", id, derr)
		}
		scheduled, serr := parseInstant(lastSched.String)
		if serr != nil {
			return Task{}, fmt.Errorf("parse last_scheduled_at for %s: %w", id, serr)
		}
		task.LastDelivery = &DeliveryReceipt{ScheduledAt: scheduled, DeliveredAt: delivered, MessageID: lastMsgID.String}
	}
	return task, nil
}

func (s *SQLiteStore) queryTasks(ctx context.Context, query string, args ...any) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// List 返回全部任务，按 scheduled_at 升序、id 字典序。
func (s *SQLiteStore) List() ([]Task, error) {
	return s.queryTasks(context.Background(),
		`SELECT `+taskColumns+` FROM schedule_tasks ORDER BY scheduled_at ASC, id ASC`)
}

// ListActiveBySession 返回某会话的活动任务。
func (s *SQLiteStore) ListActiveBySession(sessionID string) ([]Task, error) {
	return s.queryTasks(context.Background(),
		`SELECT `+taskColumns+` FROM schedule_tasks WHERE session_id = ? AND status = 'active' ORDER BY scheduled_at ASC, id ASC`,
		sessionID)
}

// ListActiveDue 返回 status=active 且 scheduled_at <= now 的任务。
func (s *SQLiteStore) ListActiveDue(now time.Time) ([]Task, error) {
	return s.queryTasks(context.Background(),
		`SELECT `+taskColumns+` FROM schedule_tasks WHERE status = 'active' AND scheduled_at <= ? ORDER BY scheduled_at ASC, id ASC`,
		formatInstant(now))
}

// Get 读取一条任务。
func (s *SQLiteStore) Get(id string) (Task, bool, error) {
	row := s.db.QueryRow(`SELECT `+taskColumns+` FROM schedule_tasks WHERE id = ?`, id)
	task, err := scanTask(row)
	if err == sql.ErrNoRows {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	return task, true, nil
}

func recordArgs(task Task) []any {
	return []any{
		task.Record.ID, task.SessionID, string(task.Record.Kind), task.Record.Title, task.Record.Prompt,
		formatInstant(task.Record.ScheduledAt), nullableInt(task.Record.AfterSeconds), nullableInt(task.Record.EverySeconds),
		nullableString(task.Record.Time), nullableString(task.Record.TimeZone), nullableString(""), nullableString(task.Record.Expression),
		string(task.Status),
	}
}

func nullableInt(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// Put 插入一条新任务；已存在同 id 时返回错误。
func (s *SQLiteStore) Put(task Task) error {
	now := formatInstant(time.Now())
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO schedule_tasks (`+taskColumns+`, owner_instance_id, lease_expires_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, NULL, NULL, ?, ?)`,
		append(recordArgs(task), task.StateVersion, now, now)...)
	if err != nil {
		return fmt.Errorf("insert schedule task %s: %w", task.Record.ID, err)
	}
	return nil
}

// UpdateCAS 以 state_version 比较并更新任务；返回是否生效。
func (s *SQLiteStore) UpdateCAS(task Task, expectedVersion int64) (bool, error) {
	now := formatInstant(time.Now())
	res, err := s.db.ExecContext(context.Background(), `
UPDATE schedule_tasks SET
    session_id = ?, kind = ?, title = ?, prompt = ?, scheduled_at = ?,
    after_seconds = ?, every_seconds = ?, local_time = ?, time_zone = ?, expression = ?,
    status = ?, last_scheduled_at = ?, last_delivered_at = ?, last_message_id = ?,
    state_version = state_version + 1, updated_at = ?
WHERE id = ? AND state_version = ?`,
		task.SessionID, string(task.Record.Kind), task.Record.Title, task.Record.Prompt, formatInstant(task.Record.ScheduledAt),
		nullableInt(task.Record.AfterSeconds), nullableInt(task.Record.EverySeconds), nullableString(task.Record.Time),
		nullableString(task.Record.TimeZone), nullableString(task.Record.Expression),
		string(task.Status), lastScheduled(task), lastDelivered(task), lastMessageID(task),
		now, task.Record.ID, expectedVersion)
	if err != nil {
		return false, fmt.Errorf("update schedule task %s: %w", task.Record.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func lastScheduled(task Task) any {
	if task.LastDelivery == nil {
		return nil
	}
	return formatInstant(task.LastDelivery.ScheduledAt)
}

func lastDelivered(task Task) any {
	if task.LastDelivery == nil {
		return nil
	}
	return formatInstant(task.LastDelivery.DeliveredAt)
}

func lastMessageID(task Task) any {
	if task.LastDelivery == nil {
		return nil
	}
	return nullableString(task.LastDelivery.MessageID)
}

// Delete 硬删除任务及其历史；返回是否存在。
func (s *SQLiteStore) Delete(id string) (bool, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_delivery_history WHERE schedule_id = ?`, id); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM schedule_tasks WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return affected > 0, nil
}
