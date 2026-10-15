package schedule

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
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
	return migrate.Apply(ctx, s.db, storeMigrations())
}

// storeMigrations 是 schedule store 全部迁移的唯一事实源（migrate 与
// SupportedSchemaVersion 共用，避免版本号两处漂移）。
func storeMigrations() []migrate.Migration {
	return []migrate.Migration{{
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
	}, {
		Version: 2,
		Name:    "schedule_pending_messages",
		UpSQL: `
CREATE TABLE IF NOT EXISTS schedule_pending_messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    schedule_id  TEXT NOT NULL,
    session_id   TEXT NOT NULL,
    prompt       TEXT NOT NULL,
    reason       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    consumed_at  TEXT
);
CREATE INDEX IF NOT EXISTS idx_schedule_pending_session
ON schedule_pending_messages(session_id, consumed_at);
`,
	}, {
		Version: 3,
		Name:    "schedule_tasks_origin",
		UpSQL: `
ALTER TABLE schedule_tasks ADD COLUMN origin TEXT NOT NULL DEFAULT 'model';
`,
	}, {
		Version: 4,
		Name:    "schedule_tasks_job_fields",
		// J0：作业通用化字段（goal/plan + 执行上下文 target）。
		// 全部带默认值，旧行零迁移（target_kind 默认 'session'）。
		UpSQL: `
ALTER TABLE schedule_tasks ADD COLUMN goal TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_tasks ADD COLUMN plan_json TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_tasks ADD COLUMN target_kind TEXT NOT NULL DEFAULT 'session';
ALTER TABLE schedule_tasks ADD COLUMN target_workspace TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_tasks ADD COLUMN target_profile TEXT NOT NULL DEFAULT '';
`,
	}, {
		Version: 5,
		Name:    "schedule_job_runs",
		// J3：创建者会话 + 运行历史。旧行零迁移（created_by_session_id 默认空）。
		UpSQL: `
ALTER TABLE schedule_tasks ADD COLUMN created_by_session_id TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS schedule_job_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    schedule_id  TEXT NOT NULL,
    status       TEXT NOT NULL,
    output       TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT '',
    session_id   TEXT NOT NULL DEFAULT '',
    started_at   TEXT NOT NULL,
    finished_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_schedule_job_runs_task ON schedule_job_runs(schedule_id, id);
`,
	}, {
		Version: 6,
		Name:    "schedule_delivery_intents",
		// S2（§12.1）：交付账本——接收侧按 occurrence_key 去重，收敛
		// "先投递、后 commit" 崩溃窗口（leader 失租重扫同一 occurrence）。
		// 当前只写 state='delivered'；'intent' 状态留给 S4 的任务级 claim 细化。
		UpSQL: `
CREATE TABLE IF NOT EXISTS schedule_delivery_intents (
    occurrence_key TEXT PRIMARY KEY,
    schedule_id    TEXT NOT NULL,
    state          TEXT NOT NULL DEFAULT 'delivered',
    owner          TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_schedule_delivery_intents_task ON schedule_delivery_intents(schedule_id);
`,
	}, {
		Version: 7,
		Name:    "schedule_task_claims_attempts",
		UpSQL: `
ALTER TABLE schedule_tasks ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE schedule_tasks ADD COLUMN last_attempt_at TEXT;
ALTER TABLE schedule_tasks ADD COLUMN dead_lettered_at TEXT;
`,
	}}
}

// SupportedSchemaVersion 返回本二进制支持的最新 store schema 版本
// （= 迁移集最高版本）。用于在岗可见性创建检查（§4.1）与版本握手（§4.2）：
// 能力位标记 scheduler:store-v<N> 中的 N 即此值。
func SupportedSchemaVersion() int {
	return migrate.LatestVersion(storeMigrations())
}

// SchemaVersion 读取底层库当前已应用的最高迁移版本（读不到返回错误；
// 空库无迁移记录时为 0——调用方按"低于支持版本"处理即可）。
func (s *SQLiteStore) SchemaVersion(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("schedule: store is not open")
	}
	var version int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("schedule: read schema version: %w", err)
	}
	return version, nil
}

// StoreStats 是运维对账快照（S4b，§4 ⑥）：任务状态计数 + pending 兜底消息 +
// 交付账本条目，供 daemon /status 与面板展示。
type StoreStats struct {
	ActiveTasks     int `json:"activeTasks"`
	InactiveTasks   int `json:"inactiveTasks"`
	PendingMessages int `json:"pendingMessages"`
	DeliveryIntents int `json:"deliveryIntents"`
}

// StoreStats 汇总对账计数（只读单语句；不参与驱动路径的锁域）。
func (s *SQLiteStore) StoreStats() (StoreStats, error) {
	if s == nil || s.db == nil {
		return StoreStats{}, fmt.Errorf("schedule: store is not open")
	}
	ctx := context.Background()
	var stats StoreStats
	if err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status = 'inactive' THEN 1 ELSE 0 END), 0)
FROM schedule_tasks`).Scan(&stats.ActiveTasks, &stats.InactiveTasks); err != nil {
		return StoreStats{}, fmt.Errorf("schedule: count tasks: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schedule_pending_messages`).Scan(&stats.PendingMessages); err != nil {
		return StoreStats{}, fmt.Errorf("schedule: count pending messages: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schedule_delivery_intents`).Scan(&stats.DeliveryIntents); err != nil {
		return StoreStats{}, fmt.Errorf("schedule: count delivery intents: %w", err)
	}
	return stats, nil
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

// MarkOccurrencesDelivered 把 occurrence 键幂等落账（交付账本的"送达"侧，§12.1）。
// 重复写只刷新 updated_at，created_at 保持首次值。
func (s *SQLiteStore) MarkOccurrencesDelivered(keys []string, now time.Time) error {
	cleaned := cleanOccurrenceKeys(keys)
	if len(cleaned) == 0 {
		return nil
	}
	now = now.UTC()
	stamp := formatInstant(now)
	for _, key := range cleaned {
		if _, err := s.db.ExecContext(context.Background(), `
INSERT INTO schedule_delivery_intents (occurrence_key, schedule_id, state, owner, created_at, updated_at)
VALUES (?, ?, 'delivered', '', ?, ?)
ON CONFLICT(occurrence_key) DO UPDATE SET state = 'delivered', updated_at = excluded.updated_at`,
			key, ScheduleIDFromOccurrenceKey(key), stamp, stamp); err != nil {
			return fmt.Errorf("mark occurrence delivered %s: %w", key, err)
		}
	}
	return nil
}

// AllOccurrencesDelivered 报告 keys 是否全部已送达（空列表恒 false：无键不做去重）。
func (s *SQLiteStore) AllOccurrencesDelivered(keys []string) (bool, error) {
	cleaned := cleanOccurrenceKeys(keys)
	if len(cleaned) == 0 {
		return false, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cleaned)), ",")
	args := make([]any, 0, len(cleaned))
	for _, key := range cleaned {
		args = append(args, key)
	}
	var count int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM schedule_delivery_intents WHERE state = 'delivered' AND occurrence_key IN (`+placeholders+`)`,
		args...).Scan(&count); err != nil {
		return false, fmt.Errorf("count delivered occurrences: %w", err)
	}
	return count == len(cleaned), nil
}

// ExecRaw 执行一条原始 SQL（测试用：构造持久层边界数据）。
func (s *SQLiteStore) ExecRaw(query string, args ...any) error {
	_, err := s.db.ExecContext(context.Background(), query, args...)
	return err
}

const taskColumns = `id, session_id, kind, title, prompt, scheduled_at, after_seconds, every_seconds,
    local_time, time_zone, weekdays_json, expression, status,
    last_scheduled_at, last_delivered_at, last_message_id, state_version, origin,
    goal, plan_json, target_kind, target_workspace, target_profile, created_by_session_id,
    attempt_count, last_attempt_at, dead_lettered_at`

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
		origin      string
		goal        string
		planJSON    string
		targetKind  string
		targetWS    string
		targetProf  string
		createdBy   string
		attempts    int64
		lastAttempt sql.NullString
		deadLetter  sql.NullString
	)
	if err := scanner.Scan(&id, &task.SessionID, &kind, &task.Record.Title, &task.Record.Prompt, &scheduledAt,
		&afterRaw, &everyRaw, &localTime, &timeZone, &weekdaysRaw, &expression, &status,
		&lastSched, &lastDeliv, &lastMsgID, &version, &origin,
		&goal, &planJSON, &targetKind, &targetWS, &targetProf, &createdBy,
		&attempts, &lastAttempt, &deadLetter); err != nil {
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
	task.Record.Weekdays = decodeWeekdays(weekdaysRaw)
	// J0：作业字段（旧行默认空 → 回落 session / 无计划）。
	task.Record.Goal = goal
	task.Record.Plan = decodePlan(planJSON)
	task.TargetKind = NormalizeTargetKind(targetKind)
	task.TargetWorkspace = targetWS
	task.TargetProfile = targetProf
	task.CreatedBySessionID = createdBy
	// S4：attempts/死信为簿记字段——坏值降级为 0/nil，不让整行读取失败。
	task.AttemptCount = int(attempts)
	if lastAttempt.Valid {
		if parsed, perr := parseInstant(lastAttempt.String); perr == nil {
			task.LastAttemptAt = &parsed
		}
	}
	if deadLetter.Valid {
		if parsed, perr := parseInstant(deadLetter.String); perr == nil {
			task.DeadLetteredAt = &parsed
		}
	}
	task.Status = Status(status)
	task.StateVersion = version
	// 未知/空来源回落 model：旧数据与外部写入零迁移语义。
	if origin == string(OriginManual) {
		task.Origin = OriginManual
	} else {
		task.Origin = OriginModel
	}
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
	// J2：到期选择包含全部目标；非 session 目标由 Runtime 走 headless 分支
	// （无 headless 执行能力时延后，不空转）。
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
		nullableString(task.Record.Time), nullableString(task.Record.TimeZone), encodeWeekdays(task.Record.Weekdays), nullableString(task.Record.Expression),
		string(task.Status),
	}
}

// originArg 把任务来源归一化为可存储值（空/未知 → model）。
func originArg(task Task) string {
	if task.Origin == OriginManual {
		return string(OriginManual)
	}
	return string(OriginModel)
}

// encodePlan 把执行步骤编码为 JSON 文本（空计划存空串）。
func encodePlan(plan []string) string {
	if len(plan) == 0 {
		return ""
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// decodePlan 解析 plan_json：空/非法一律回落为空计划（绝不因坏数据拒绝整行）。
func decodePlan(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var plan []string
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil
	}
	return plan
}

// encodeWeekdays 把星期集合编码为 JSON 文本（空集合存 NULL）。
func encodeWeekdays(days []int) any {
	if len(days) == 0 {
		return nil
	}
	encoded, err := json.Marshal(days)
	if err != nil {
		return nil
	}
	return string(encoded)
}

// decodeWeekdays 解码已存储的星期集合；损坏返回 nil（由 ValidateRecord 兜底）。
func decodeWeekdays(raw sql.NullString) []int {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var days []int
	if err := json.Unmarshal([]byte(raw.String), &days); err != nil {
		return nil
	}
	return days
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

// nullableInstant 把可选时间写成可存储值（nil → NULL）。
func nullableInstant(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatInstant(*value)
}

// Put 插入一条新任务；已存在同 id 时返回错误。
func (s *SQLiteStore) Put(task Task) error {
	now := formatInstant(time.Now())
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO schedule_tasks (`+taskColumns+`, owner_instance_id, lease_expires_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, ?,
        ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		append(recordArgs(task), task.StateVersion, originArg(task),
			task.Record.Goal, encodePlan(task.Record.Plan), string(NormalizeTargetKind(string(task.TargetKind))),
			task.TargetWorkspace, task.TargetProfile, task.CreatedBySessionID,
			int64(task.AttemptCount), nullableInstant(task.LastAttemptAt), nullableInstant(task.DeadLetteredAt),
			now, now)...)
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
    after_seconds = ?, every_seconds = ?, local_time = ?, time_zone = ?, weekdays_json = ?, expression = ?,
    goal = ?, plan_json = ?, target_kind = ?, target_workspace = ?, target_profile = ?,
    created_by_session_id = ?,
    status = ?, last_scheduled_at = ?, last_delivered_at = ?, last_message_id = ?,
    state_version = state_version + 1, updated_at = ?
WHERE id = ? AND state_version = ?`,
		task.SessionID, string(task.Record.Kind), task.Record.Title, task.Record.Prompt, formatInstant(task.Record.ScheduledAt),
		nullableInt(task.Record.AfterSeconds), nullableInt(task.Record.EverySeconds), nullableString(task.Record.Time),
		nullableString(task.Record.TimeZone), encodeWeekdays(task.Record.Weekdays), nullableString(task.Record.Expression),
		task.Record.Goal, encodePlan(task.Record.Plan), string(NormalizeTargetKind(string(task.TargetKind))),
		task.TargetWorkspace, task.TargetProfile, task.CreatedBySessionID,
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

// AppendDeliveryRecord 追加一条投递历史（seq 单调递增）。
func (s *SQLiteStore) AppendDeliveryRecord(scheduleID string, record DeliveryRecord) error {
	ctx := context.Background()
	// 读后写（MAX(seq) → INSERT）：必须 IMMEDIATE，否则 WAL 下并发写者会顶成
	// SQLITE_BUSY_SNAPSHOT 且重试永不成功（P0.5b 写事务门禁，2026-10-14）。
	tx, err := s.db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var maxSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM schedule_delivery_history WHERE schedule_id = ?`, scheduleID).Scan(&maxSeq); err != nil {
		return err
	}
	seq := int64(1)
	if maxSeq.Valid {
		seq = maxSeq.Int64 + 1
	}
	var prompt any
	if record.Prompt != "" {
		prompt = record.Prompt
	}
	const insertHistory = `INSERT INTO schedule_delivery_history
(schedule_id, seq, scheduled_at, delivered_at, message_id, prompt)
VALUES (?, ?, ?, ?, ?, ?)`
	if _, err := tx.ExecContext(ctx, insertHistory,
		scheduleID, seq, formatInstant(record.ScheduledAt), formatInstant(record.DeliveredAt),
		record.MessageID, prompt); err != nil {
		return err
	}
	return tx.Commit()
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
	// 读后写（存在性检查 → DELETE）：与 AppendDeliveryRecord 同一口径。
	tx, err := s.db.BeginTx(ctx, sqliteutil.WriteTxOptions)
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

// ListHistoryPage 读取某任务的一段历史：追加序逆序、beforeSeq 为排他 seq 游标。
func (s *SQLiteStore) ListHistoryPage(id string, beforeSeq int64, limit int) ([]DeliveryRecord, error) {
	query := `SELECT scheduled_at, delivered_at, message_id, prompt
FROM schedule_delivery_history WHERE schedule_id = ?`
	args := []any{id}
	if beforeSeq > 0 {
		query += ` AND seq < ?`
		args = append(args, beforeSeq)
	}
	query += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []DeliveryRecord{}
	for rows.Next() {
		var scheduledAt, deliveredAt, messageID string
		var prompt sql.NullString
		if err := rows.Scan(&scheduledAt, &deliveredAt, &messageID, &prompt); err != nil {
			return nil, err
		}
		scheduled, err := parseInstant(scheduledAt)
		if err != nil {
			return nil, fmt.Errorf("parse history scheduled_at: %w", err)
		}
		delivered, err := parseInstant(deliveredAt)
		if err != nil {
			return nil, fmt.Errorf("parse history delivered_at: %w", err)
		}
		record := DeliveryRecord{
			DeliveryReceipt: DeliveryReceipt{ScheduledAt: scheduled, DeliveredAt: delivered, MessageID: messageID},
		}
		if prompt.Valid {
			record.Prompt = prompt.String
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// MaxHistorySeq 返回某任务的最大追加序号（ok=false 表示无历史）。
func (s *SQLiteStore) MaxHistorySeq(id string) (int64, bool, error) {
	var maxSeq sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(seq) FROM schedule_delivery_history WHERE schedule_id = ?`, id).Scan(&maxSeq)
	if err != nil {
		return 0, false, err
	}
	if !maxSeq.Valid {
		return 0, false, nil
	}
	return maxSeq.Int64, true, nil
}

// FindHistorySeqByMessage 把 message_id 游标解析为排他 seq（从新到旧第一处）。
func (s *SQLiteStore) FindHistorySeqByMessage(id, messageID string) (int64, bool, error) {
	var seq int64
	err := s.db.QueryRow(
		`SELECT seq FROM schedule_delivery_history WHERE schedule_id = ? AND message_id = ? ORDER BY seq DESC LIMIT 1`,
		id, messageID).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return seq, true, nil
}

// PendingMessage 是一次投递失败后落库的兜底消息（宿主恢复后消费）。
type PendingMessage struct {
	ID         int64
	ScheduleID string
	SessionID  string
	Prompt     string
	Reason     string
	CreatedAt  time.Time
}

// AppendPendingMessage 落库一条兜底消息。
func (s *SQLiteStore) AppendPendingMessage(scheduleID, sessionID, prompt, reason string, now time.Time) error {
	_, err := s.db.Exec(`
INSERT INTO schedule_pending_messages (schedule_id, session_id, prompt, reason, created_at)
VALUES (?, ?, ?, ?, ?)`,
		scheduleID, sessionID, prompt, reason, formatInstant(now))
	if err != nil {
		return fmt.Errorf("append pending message: %w", err)
	}
	return nil
}

// ListPendingMessages 返回某会话未消费的兜底消息（旧到新）。
func (s *SQLiteStore) ListPendingMessages(sessionID string, limit int) ([]PendingMessage, error) {
	rows, err := s.db.Query(`
SELECT id, schedule_id, session_id, prompt, reason, created_at
FROM schedule_pending_messages
WHERE session_id = ? AND consumed_at IS NULL
ORDER BY id ASC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []PendingMessage{}
	for rows.Next() {
		var message PendingMessage
		var createdRaw string
		if err := rows.Scan(&message.ID, &message.ScheduleID, &message.SessionID, &message.Prompt,
			&message.Reason, &createdRaw); err != nil {
			return nil, err
		}
		message.CreatedAt, _ = parseInstant(createdRaw)
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

// MarkPendingMessageConsumed 标记一条兜底消息已消费。
func (s *SQLiteStore) MarkPendingMessageConsumed(id int64, now time.Time) error {
	_, err := s.db.Exec(
		`UPDATE schedule_pending_messages SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL`,
		formatInstant(now), id)
	return err
}

// MarkPendingConsumedBySchedule 标记某任务的全部未消费兜底消息为已消费。
// 任务经正常路径（delegate/内嵌）投递成功提交后调用：该提醒已经送达，其
// 历史失败留下的 pending 行不应再被宿主 drain 一次（避免重复投递）。
func (s *SQLiteStore) MarkPendingConsumedBySchedule(scheduleID string, now time.Time) error {
	_, err := s.db.Exec(
		`UPDATE schedule_pending_messages SET consumed_at = ? WHERE schedule_id = ? AND consumed_at IS NULL`,
		formatInstant(now), scheduleID)
	if err != nil {
		return fmt.Errorf("mark pending consumed by schedule: %w", err)
	}
	return nil
}

// AppendJobRun 追加一条作业运行记录（J3：运行历史）。
func (s *SQLiteStore) AppendJobRun(run JobRun) error {
	_, err := s.db.Exec(`
INSERT INTO schedule_job_runs (schedule_id, status, output, error, session_id, started_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.ScheduleID, string(run.Status), run.Output, run.Error, run.SessionID,
		formatInstant(run.StartedAt), formatInstant(run.FinishedAt))
	if err != nil {
		return fmt.Errorf("append job run: %w", err)
	}
	return nil
}

// ListJobRuns 返回某作业的运行历史（新到旧，最多 limit 条；limit<=0 取 20）。
func (s *SQLiteStore) ListJobRuns(scheduleID string, limit int) ([]JobRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
SELECT id, schedule_id, status, output, error, session_id, started_at, finished_at
FROM schedule_job_runs WHERE schedule_id = ? ORDER BY id DESC LIMIT ?`, scheduleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []JobRun
	for rows.Next() {
		var (
			run         JobRun
			status      string
			startedRaw  string
			finishedRaw string
		)
		if err := rows.Scan(&run.ID, &run.ScheduleID, &status, &run.Output, &run.Error,
			&run.SessionID, &startedRaw, &finishedRaw); err != nil {
			return nil, err
		}
		run.Status = JobRunStatus(status)
		run.StartedAt, _ = parseInstant(startedRaw)
		run.FinishedAt, _ = parseInstant(finishedRaw)
		runs = append(runs, run)
	}
	return runs, rows.Err()
}
