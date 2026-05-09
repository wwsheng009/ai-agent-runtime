package agentcontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// GlobalMailboxStoreConfig controls the durable global AgentControl mailbox
// registry store.
type GlobalMailboxStoreConfig struct {
	Path string
	DSN  string
}

// SQLiteGlobalMailboxRegistryStore persists mailbox rows from local runtime
// and workflow sources into one durable global row-id space.
type SQLiteGlobalMailboxRegistryStore struct {
	db     *sql.DB
	dsn    string
	ownsDB bool

	mailboxWakeMu          sync.Mutex
	nextMailboxWakeWatchID int64
	mailboxWakeWatchers    map[int64]globalMailboxWakeWatcher
}

var _ GlobalMailboxRegistryStore = (*SQLiteGlobalMailboxRegistryStore)(nil)
var _ MailboxWakeSource = (*SQLiteGlobalMailboxRegistryStore)(nil)

// NewSQLiteGlobalMailboxRegistryStore opens a SQLite-backed global mailbox
// registry store.
func NewSQLiteGlobalMailboxRegistryStore(cfg *GlobalMailboxStoreConfig) (*SQLiteGlobalMailboxRegistryStore, error) {
	if cfg == nil {
		cfg = &GlobalMailboxStoreConfig{}
	}
	dsn, err := resolveGlobalMailboxDSN(cfg)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open agent control mailbox registry db: %w", err)
	}
	if isGlobalMailboxMemoryDSN(dsn) {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
	if err := configureAgentControlSQLiteDB(context.Background(), db, dsn); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &SQLiteGlobalMailboxRegistryStore{db: db, dsn: dsn, ownsDB: true}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func newSQLiteGlobalMailboxRegistryStoreWithDB(ctx context.Context, db *sql.DB, dsn string) (*SQLiteGlobalMailboxRegistryStore, error) {
	if db == nil {
		return nil, fmt.Errorf("agent control mailbox registry db is not initialized")
	}
	store := &SQLiteGlobalMailboxRegistryStore{db: db, dsn: strings.TrimSpace(dsn)}
	if err := store.init(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// Close closes the underlying database.
func (s *SQLiteGlobalMailboxRegistryStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.closeMailboxWakeWatchers()
	if !s.ownsDB {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteGlobalMailboxRegistryStore) closeMailboxWakeWatchers() {
	if s == nil {
		return
	}
	s.mailboxWakeMu.Lock()
	watchers := s.mailboxWakeWatchers
	s.mailboxWakeWatchers = nil
	s.mailboxWakeMu.Unlock()
	for _, watcher := range watchers {
		if watcher.unwatch != nil {
			watcher.unwatch()
		}
		if watcher.ch == nil {
			continue
		}
		close(watcher.ch)
	}
}

// AppendGlobalMailboxRecord writes or refreshes one source-local mailbox row in
// the durable global registry. The returned sequence is the global row id.
func (s *SQLiteGlobalMailboxRegistryStore) AppendGlobalMailboxRecord(ctx context.Context, source string, record MailboxRecord) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("agent control mailbox registry store is not initialized")
	}
	record = record.Normalize()
	source = strings.TrimSpace(source)
	if source == "" {
		source = record.Source
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return 0, fmt.Errorf("mailbox source is required")
	}
	sourceSeq := record.SourceSeq
	if sourceSeq <= 0 {
		sourceSeq = record.Seq
	}
	if sourceSeq <= 0 {
		return 0, fmt.Errorf("mailbox source sequence is required")
	}
	sourceScope := strings.TrimSpace(record.Scope)
	if sourceScope == "" {
		return 0, fmt.Errorf("mailbox scope is required")
	}
	if record.Workflow == "" {
		record.Workflow = MetadataString(record.Metadata, MetadataKeyWorkflow)
	}
	sourceID := globalMailboxSourceID(record)
	messageID := strings.TrimSpace(record.MessageID)
	if messageID == "" {
		messageID = fmt.Sprintf("%s:%s:%d", source, sourceID, sourceSeq)
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	metadataJSON, err := marshalMailboxMetadata(record.Metadata)
	if err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_control_global_mailbox_records (
			source, source_scope, source_id, source_seq, workflow, scope, session_id, session_mailbox_seq,
			team_id, team_seq, message_id, from_agent, to_agent, task_id, kind, body, metadata_json,
			created_at, acked_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source, source_scope, source_id, source_seq) DO NOTHING
	`, source, sourceScope, sourceID, sourceSeq, nullMailboxString(record.Workflow), sourceScope,
		nullMailboxString(record.SessionID), record.SessionMailboxSeq, nullMailboxString(record.TeamID), record.TeamSeq,
		messageID, nullMailboxString(record.FromAgent), nullMailboxString(record.ToAgent), nullMailboxString(record.TaskID),
		nullMailboxString(record.Kind), record.Body, metadataJSON, formatMailboxTime(createdAt),
		nullableMailboxTime(record.AckedAt), formatMailboxTime(time.Now().UTC()))
	if err != nil {
		return 0, fmt.Errorf("append global mailbox record: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read global mailbox append result: %w", err)
	}
	created := rowsAffected > 0
	if !created {
		_, err = s.db.ExecContext(ctx, `
			UPDATE agent_control_global_mailbox_records SET
				workflow = ?,
				scope = ?,
				session_id = ?,
				session_mailbox_seq = ?,
				team_id = ?,
				team_seq = ?,
				message_id = ?,
				from_agent = ?,
				to_agent = ?,
				task_id = ?,
				kind = ?,
				body = ?,
				metadata_json = ?,
				created_at = ?,
				acked_at = ?,
				updated_at = ?
			WHERE source = ? AND source_scope = ? AND source_id = ? AND source_seq = ?
		`, nullMailboxString(record.Workflow), sourceScope, nullMailboxString(record.SessionID), record.SessionMailboxSeq,
			nullMailboxString(record.TeamID), record.TeamSeq, messageID, nullMailboxString(record.FromAgent),
			nullMailboxString(record.ToAgent), nullMailboxString(record.TaskID), nullMailboxString(record.Kind),
			record.Body, metadataJSON, formatMailboxTime(createdAt), nullableMailboxTime(record.AckedAt),
			formatMailboxTime(time.Now().UTC()), source, sourceScope, sourceID, sourceSeq)
		if err != nil {
			return 0, fmt.Errorf("refresh global mailbox record: %w", err)
		}
	}
	var seq int64
	err = s.db.QueryRowContext(ctx, `
		SELECT id
		FROM agent_control_global_mailbox_records
		WHERE source = ? AND source_scope = ? AND source_id = ? AND source_seq = ?
	`, source, sourceScope, sourceID, sourceSeq).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("read global mailbox record id: %w", err)
	}
	if created {
		record.Source = source
		record.SourceSeq = sourceSeq
		record.Seq = seq
		record.Scope = sourceScope
		record.MessageID = messageID
		record.CreatedAt = createdAt
		s.notifyMailboxWake(mailboxWakeEventFromRecord(record))
	}
	return seq, nil
}

// AppendPrimaryGlobalMailboxRecord writes the canonical AgentControl mailbox
// record before any local runtime/team compatibility projection is created. It
// is idempotent by workflow/scope/source id/message id and returns the durable
// global row.
func (s *SQLiteGlobalMailboxRegistryStore) AppendPrimaryGlobalMailboxRecord(ctx context.Context, record MailboxRecord) (MailboxRecord, error) {
	if s == nil || s.db == nil {
		return MailboxRecord{}, fmt.Errorf("agent control mailbox registry store is not initialized")
	}
	appended, created, err := appendPrimaryGlobalMailboxRecordSQL(ctx, s.db, "", record)
	if err != nil {
		return MailboxRecord{}, err
	}
	if created {
		s.notifyMailboxWake(mailboxWakeEventFromRecord(appended))
	}
	return appended, nil
}

// AppendPrimaryGlobalMailboxRecordTx writes the canonical row through an
// existing SQLite transaction. schema may name an attached database; an empty
// schema writes to the transaction's main database.
func (s *SQLiteGlobalMailboxRegistryStore) AppendPrimaryGlobalMailboxRecordTx(ctx context.Context, tx *sql.Tx, schema string, record MailboxRecord) (MailboxRecord, error) {
	if tx == nil {
		return MailboxRecord{}, fmt.Errorf("agent control mailbox transaction is not initialized")
	}
	appended, _, err := appendPrimaryGlobalMailboxRecordSQL(ctx, tx, schema, record)
	return appended, err
}

// GlobalMailboxAttachDSN returns the SQLite DSN that local stores can attach
// when they need a same-transaction global mailbox projection commit.
func (s *SQLiteGlobalMailboxRegistryStore) GlobalMailboxAttachDSN() (string, bool) {
	if s == nil {
		return "", false
	}
	dsn := strings.TrimSpace(s.dsn)
	if dsn == "" || strings.EqualFold(dsn, ":memory:") {
		return "", false
	}
	return dsn, true
}

// NotifyGlobalMailboxWake publishes an in-process wake notification for a row
// written through AppendPrimaryGlobalMailboxRecordTx after the caller commits.
func (s *SQLiteGlobalMailboxRegistryStore) NotifyGlobalMailboxWake(record MailboxRecord) {
	if s == nil {
		return
	}
	s.notifyMailboxWake(mailboxWakeEventFromRecord(record))
}

type globalMailboxSQLRunner interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func appendPrimaryGlobalMailboxRecordSQL(ctx context.Context, runner globalMailboxSQLRunner, schema string, record MailboxRecord) (MailboxRecord, bool, error) {
	if runner == nil {
		return MailboxRecord{}, false, fmt.Errorf("agent control mailbox registry runner is not initialized")
	}
	record = record.Normalize()
	if record.Workflow == "" {
		record.Workflow = MetadataString(record.Metadata, MetadataKeyWorkflow)
	}
	if record.Scope == "" {
		return MailboxRecord{}, false, fmt.Errorf("mailbox scope is required")
	}
	sourceID := globalMailboxSourceID(record)
	if sourceID == "" {
		return MailboxRecord{}, false, fmt.Errorf("mailbox source id is required")
	}
	if record.MessageID == "" {
		return MailboxRecord{}, false, fmt.Errorf("mailbox message id is required")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	primaryKey := globalMailboxPrimaryKey(record, sourceID)
	metadataJSON, err := marshalMailboxMetadata(record.Metadata)
	if err != nil {
		return MailboxRecord{}, false, err
	}
	now := time.Now().UTC()
	record.Source = MailboxSourceGlobal
	tableName, err := qualifiedGlobalMailboxTable(schema)
	if err != nil {
		return MailboxRecord{}, false, err
	}
	result, err := runner.ExecContext(ctx, `
		INSERT INTO `+tableName+` (
			source, source_scope, source_id, source_seq, workflow, scope, session_id, session_mailbox_seq,
			team_id, team_seq, message_id, from_agent, to_agent, task_id, kind, body, metadata_json,
			created_at, acked_at, updated_at, primary_key
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(primary_key) DO NOTHING
	`, MailboxSourceGlobal, record.Scope, sourceID, temporaryGlobalPrimarySourceSeq(primaryKey), nullMailboxString(record.Workflow), record.Scope,
		nullMailboxString(record.SessionID), record.SessionMailboxSeq, nullMailboxString(record.TeamID), record.TeamSeq,
		record.MessageID, nullMailboxString(record.FromAgent), nullMailboxString(record.ToAgent), nullMailboxString(record.TaskID),
		nullMailboxString(record.Kind), record.Body, metadataJSON, formatMailboxTime(record.CreatedAt),
		nullableMailboxTime(record.AckedAt), formatMailboxTime(now), primaryKey)
	if err != nil {
		return MailboxRecord{}, false, fmt.Errorf("append primary global mailbox record: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return MailboxRecord{}, false, fmt.Errorf("read primary global mailbox append result: %w", err)
	}
	created := rowsAffected > 0
	var seq int64
	if created {
		seq, err = result.LastInsertId()
		if err != nil {
			return MailboxRecord{}, false, fmt.Errorf("primary global mailbox record id: %w", err)
		}
		if _, err := runner.ExecContext(ctx, `
			UPDATE `+tableName+`
			SET source_seq = ?
			WHERE id = ?
		`, seq, seq); err != nil {
			return MailboxRecord{}, false, fmt.Errorf("finalize primary global mailbox source sequence: %w", err)
		}
		appended, err := getGlobalMailboxRecordSQL(ctx, runner, tableName, seq)
		return appended, true, err
	}
	if _, err := runner.ExecContext(ctx, `
		UPDATE `+tableName+` SET
			workflow = ?,
			scope = ?,
			session_id = ?,
			session_mailbox_seq = ?,
			team_id = ?,
			team_seq = ?,
			message_id = ?,
			from_agent = ?,
			to_agent = ?,
			task_id = ?,
			kind = ?,
			body = ?,
			metadata_json = ?,
			created_at = ?,
			acked_at = ?,
			updated_at = ?
		WHERE primary_key = ?
	`, nullMailboxString(record.Workflow), record.Scope, nullMailboxString(record.SessionID), record.SessionMailboxSeq,
		nullMailboxString(record.TeamID), record.TeamSeq, record.MessageID, nullMailboxString(record.FromAgent),
		nullMailboxString(record.ToAgent), nullMailboxString(record.TaskID), nullMailboxString(record.Kind),
		record.Body, metadataJSON, formatMailboxTime(record.CreatedAt), nullableMailboxTime(record.AckedAt),
		formatMailboxTime(now), primaryKey); err != nil {
		return MailboxRecord{}, false, fmt.Errorf("refresh primary global mailbox record: %w", err)
	}
	appended, err := getGlobalMailboxRecordByPrimaryKeySQL(ctx, runner, tableName, primaryKey)
	return appended, false, err
}

// MaterializeMailboxRecords copies matching local source rows into the durable
// global registry. It is idempotent by source/scope/source id/source seq.
func (s *SQLiteGlobalMailboxRegistryStore) MaterializeMailboxRecords(ctx context.Context, sources []NamedMailboxRegistrySource, filter MailboxRecordFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("agent control mailbox registry store is not initialized")
	}
	filter = filter.Normalize()
	filter.AfterSeq = 0
	filter.Limit = 0
	var count int64
	for index, source := range sources {
		source.Name = strings.TrimSpace(source.Name)
		if source.Source == nil {
			continue
		}
		if source.Name == "" {
			source.Name = fmt.Sprintf("source_%d", index+1)
		}
		records, err := source.Source.ListAgentControlMailboxRecords(ctx, filter)
		if err != nil {
			return count, err
		}
		for _, record := range records {
			if record.GlobalSeq > 0 {
				count++
				continue
			}
			if _, err := s.AppendGlobalMailboxRecord(ctx, source.Name, record); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

// ListAgentControlMailboxRecords returns records from the durable global row-id
// space.
func (s *SQLiteGlobalMailboxRegistryStore) ListAgentControlMailboxRecords(ctx context.Context, filter MailboxRecordFilter) ([]MailboxRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("agent control mailbox registry store is not initialized")
	}
	filter = filter.Normalize()
	clauses := []string{"id > ?"}
	args := []interface{}{filter.AfterSeq}
	if filter.Workflow != "" {
		clauses = append(clauses, "workflow = ?")
		args = append(args, filter.Workflow)
	}
	if filter.Scope != "" {
		clauses = append(clauses, "scope = ?")
		args = append(args, filter.Scope)
	}
	if filter.SessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, filter.SessionID)
	}
	if filter.TeamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID)
	}
	query := `
		SELECT id, source, source_seq, workflow, scope, session_id, session_mailbox_seq, team_id, team_seq,
			message_id, from_agent, to_agent, task_id, kind, body, metadata_json, created_at, acked_at
		FROM agent_control_global_mailbox_records
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY id ASC
	`
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list global mailbox records: %w", err)
	}
	defer rows.Close()

	records := make([]MailboxRecord, 0)
	for rows.Next() {
		record, err := scanGlobalMailboxRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// LastAgentControlMailboxRecordSeq returns the durable global high-water mark.
func (s *SQLiteGlobalMailboxRegistryStore) LastAgentControlMailboxRecordSeq(ctx context.Context, filter MailboxRecordFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("agent control mailbox registry store is not initialized")
	}
	filter = filter.Normalize()
	clauses := make([]string, 0)
	args := make([]interface{}, 0)
	if filter.Workflow != "" {
		clauses = append(clauses, "workflow = ?")
		args = append(args, filter.Workflow)
	}
	if filter.Scope != "" {
		clauses = append(clauses, "scope = ?")
		args = append(args, filter.Scope)
	}
	if filter.SessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, filter.SessionID)
	}
	if filter.TeamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID)
	}
	query := "SELECT COALESCE(MAX(id), 0) FROM agent_control_global_mailbox_records"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	var seq int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&seq); err != nil {
		return 0, fmt.Errorf("last global mailbox record sequence: %w", err)
	}
	return seq, nil
}

func (s *SQLiteGlobalMailboxRegistryStore) getGlobalMailboxRecord(ctx context.Context, seq int64) (MailboxRecord, error) {
	if seq <= 0 {
		return MailboxRecord{}, fmt.Errorf("global mailbox sequence is required")
	}
	return getGlobalMailboxRecordSQL(ctx, s.db, "agent_control_global_mailbox_records", seq)
}

func getGlobalMailboxRecordSQL(ctx context.Context, runner globalMailboxSQLRunner, tableName string, seq int64) (MailboxRecord, error) {
	if seq <= 0 {
		return MailboxRecord{}, fmt.Errorf("global mailbox sequence is required")
	}
	row := runner.QueryRowContext(ctx, `
		SELECT id, source, source_seq, workflow, scope, session_id, session_mailbox_seq, team_id, team_seq,
			message_id, from_agent, to_agent, task_id, kind, body, metadata_json, created_at, acked_at
		FROM `+tableName+`
		WHERE id = ?
	`, seq)
	record, err := scanGlobalMailboxRecord(row)
	if err != nil {
		return MailboxRecord{}, err
	}
	return record, nil
}

func (s *SQLiteGlobalMailboxRegistryStore) getGlobalMailboxRecordByPrimaryKey(ctx context.Context, primaryKey string) (MailboxRecord, error) {
	primaryKey = strings.TrimSpace(primaryKey)
	if primaryKey == "" {
		return MailboxRecord{}, fmt.Errorf("global mailbox primary key is required")
	}
	return getGlobalMailboxRecordByPrimaryKeySQL(ctx, s.db, "agent_control_global_mailbox_records", primaryKey)
}

func getGlobalMailboxRecordByPrimaryKeySQL(ctx context.Context, runner globalMailboxSQLRunner, tableName string, primaryKey string) (MailboxRecord, error) {
	primaryKey = strings.TrimSpace(primaryKey)
	if primaryKey == "" {
		return MailboxRecord{}, fmt.Errorf("global mailbox primary key is required")
	}
	row := runner.QueryRowContext(ctx, `
		SELECT id, source, source_seq, workflow, scope, session_id, session_mailbox_seq, team_id, team_seq,
			message_id, from_agent, to_agent, task_id, kind, body, metadata_json, created_at, acked_at
		FROM `+tableName+`
		WHERE primary_key = ?
	`, primaryKey)
	record, err := scanGlobalMailboxRecord(row)
	if err != nil {
		return MailboxRecord{}, err
	}
	return record, nil
}

// WatchAgentControlMailboxWake subscribes to newly inserted durable global
// mailbox rows. Existing rows are consumed through LastAgentControlMailboxWakeSeq.
func (s *SQLiteGlobalMailboxRegistryStore) WatchAgentControlMailboxWake(ctx context.Context, filter MailboxWakeFilter) (<-chan MailboxWakeEvent, func()) {
	out := make(chan MailboxWakeEvent, 1)
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.db == nil {
		return out, func() {}
	}
	filter = filter.Normalize()
	done := make(chan struct{})
	var once sync.Once
	s.mailboxWakeMu.Lock()
	if s.mailboxWakeWatchers == nil {
		s.mailboxWakeWatchers = make(map[int64]globalMailboxWakeWatcher)
	}
	s.nextMailboxWakeWatchID++
	watchID := s.nextMailboxWakeWatchID
	unwatch := func() {
		once.Do(func() {
			s.mailboxWakeMu.Lock()
			delete(s.mailboxWakeWatchers, watchID)
			s.mailboxWakeMu.Unlock()
			close(done)
		})
	}
	s.mailboxWakeWatchers[watchID] = globalMailboxWakeWatcher{
		filter:  filter,
		ch:      out,
		unwatch: unwatch,
	}
	s.mailboxWakeMu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			unwatch()
		case <-done:
		}
	}()
	return out, unwatch
}

// LastAgentControlMailboxWakeSeq returns the durable global mailbox row-id
// high-water mark for the requested wake stream.
func (s *SQLiteGlobalMailboxRegistryStore) LastAgentControlMailboxWakeSeq(ctx context.Context, filter MailboxWakeFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("agent control mailbox registry store is not initialized")
	}
	filter = filter.Normalize()
	clauses := make([]string, 0)
	args := make([]interface{}, 0)
	if filter.Workflow != "" {
		clauses = append(clauses, "workflow = ?")
		args = append(args, filter.Workflow)
	}
	if filter.TeamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID)
	}
	if filter.SessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, filter.SessionID)
	}
	query := "SELECT COALESCE(MAX(id), 0) FROM agent_control_global_mailbox_records"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	var seq int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&seq); err != nil {
		return 0, fmt.Errorf("last global mailbox wake sequence: %w", err)
	}
	return seq, nil
}

func (s *SQLiteGlobalMailboxRegistryStore) notifyMailboxWake(event MailboxWakeEvent) {
	event = event.Normalize()
	if event.Seq <= 0 {
		return
	}
	s.mailboxWakeMu.Lock()
	defer s.mailboxWakeMu.Unlock()
	for _, watcher := range s.mailboxWakeWatchers {
		if !mailboxWakeMatchesFilter(event, watcher.filter) {
			continue
		}
		select {
		case watcher.ch <- event:
		default:
		}
	}
}

func (s *SQLiteGlobalMailboxRegistryStore) init(ctx context.Context) error {
	migrations := []migrate.Migration{
		{
			Version: 1,
			Name:    "create_agent_control_global_mailbox_records",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS agent_control_global_mailbox_records (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					source TEXT NOT NULL,
					source_scope TEXT NOT NULL,
					source_id TEXT NOT NULL DEFAULT '',
					source_seq INTEGER NOT NULL,
					workflow TEXT,
					scope TEXT NOT NULL,
					session_id TEXT,
					session_mailbox_seq INTEGER NOT NULL DEFAULT 0,
					team_id TEXT,
					team_seq INTEGER NOT NULL DEFAULT 0,
					message_id TEXT NOT NULL,
					from_agent TEXT,
					to_agent TEXT,
					task_id TEXT,
					kind TEXT,
					body TEXT NOT NULL DEFAULT '',
					metadata_json TEXT NOT NULL DEFAULT '{}',
					created_at TEXT NOT NULL,
					acked_at TEXT,
					updated_at TEXT NOT NULL,
					UNIQUE(source, source_scope, source_id, source_seq)
				);
				CREATE INDEX IF NOT EXISTS idx_agent_control_global_mailbox_filter
					ON agent_control_global_mailbox_records(workflow, scope, session_id, team_id, id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_global_mailbox_source
					ON agent_control_global_mailbox_records(source, source_scope, source_id, source_seq);
			`,
		},
		{
			Version: 2,
			Name:    "primary_global_mailbox_key",
			UpSQL: `
				ALTER TABLE agent_control_global_mailbox_records ADD COLUMN primary_key TEXT;
				CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_control_global_mailbox_primary_key
					ON agent_control_global_mailbox_records(primary_key);
			`,
		},
	}
	return migrate.Apply(ctx, s.db, migrations)
}

type globalMailboxScanner interface {
	Scan(dest ...interface{}) error
}

type globalMailboxWakeWatcher struct {
	filter  MailboxWakeFilter
	ch      chan MailboxWakeEvent
	unwatch func()
}

func scanGlobalMailboxRecord(scanner globalMailboxScanner) (MailboxRecord, error) {
	var (
		record      MailboxRecord
		workflowRaw sql.NullString
		scopeRaw    sql.NullString
		sessionID   sql.NullString
		teamID      sql.NullString
		fromAgent   sql.NullString
		toAgent     sql.NullString
		taskID      sql.NullString
		kind        sql.NullString
		metadataRaw string
		createdRaw  string
		ackedRaw    sql.NullString
	)
	if err := scanner.Scan(&record.Seq, &record.Source, &record.SourceSeq, &workflowRaw, &scopeRaw, &sessionID,
		&record.SessionMailboxSeq, &teamID, &record.TeamSeq, &record.MessageID, &fromAgent, &toAgent,
		&taskID, &kind, &record.Body, &metadataRaw, &createdRaw, &ackedRaw); err != nil {
		return MailboxRecord{}, fmt.Errorf("scan global mailbox record: %w", err)
	}
	record.Workflow = workflowRaw.String
	record.Scope = scopeRaw.String
	record.SessionID = sessionID.String
	record.TeamID = teamID.String
	record.FromAgent = fromAgent.String
	record.ToAgent = toAgent.String
	record.TaskID = taskID.String
	record.Kind = kind.String
	record.Metadata = unmarshalMailboxMetadata(metadataRaw)
	if record.Metadata == nil {
		record.Metadata = map[string]interface{}{}
	}
	record.CreatedAt = parseMailboxTime(createdRaw)
	if ackedRaw.Valid {
		ackedAt := parseMailboxTime(ackedRaw.String)
		record.AckedAt = &ackedAt
	}
	record.GlobalSeq = record.Seq
	return record.Normalize(), nil
}

func mailboxWakeEventFromRecord(record MailboxRecord) MailboxWakeEvent {
	return MailboxWakeEvent{
		Seq:       record.Seq,
		Workflow:  record.Workflow,
		TeamID:    record.TeamID,
		SessionID: record.SessionID,
		MessageID: record.MessageID,
		Kind:      record.Kind,
		FromAgent: record.FromAgent,
		ToAgent:   record.ToAgent,
		TaskID:    record.TaskID,
		CreatedAt: record.CreatedAt,
	}.Normalize()
}

func mailboxWakeMatchesFilter(event MailboxWakeEvent, filter MailboxWakeFilter) bool {
	filter = filter.Normalize()
	if filter.Workflow != "" && !strings.EqualFold(event.Workflow, filter.Workflow) {
		return false
	}
	if filter.TeamID != "" && !strings.EqualFold(event.TeamID, filter.TeamID) {
		return false
	}
	if filter.SessionID != "" && !strings.EqualFold(event.SessionID, filter.SessionID) {
		return false
	}
	return true
}

func globalMailboxSourceID(record MailboxRecord) string {
	switch strings.TrimSpace(record.Scope) {
	case MailboxScopeSession:
		return strings.TrimSpace(record.SessionID)
	case MailboxScopeTeam:
		return strings.TrimSpace(record.TeamID)
	default:
		return firstNonEmpty(record.SessionID, record.TeamID, record.MessageID)
	}
}

func globalMailboxPrimaryKey(record MailboxRecord, sourceID string) string {
	return strings.Join([]string{
		strings.TrimSpace(record.Workflow),
		strings.TrimSpace(record.Scope),
		strings.TrimSpace(sourceID),
		strings.TrimSpace(record.MessageID),
	}, "\x1f")
}

func temporaryGlobalPrimarySourceSeq(primaryKey string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.TrimSpace(primaryKey)))
	value := int64(h.Sum64() & ((uint64(1) << 63) - 1))
	if value == 0 {
		value = 1
	}
	return -value
}

func qualifiedGlobalMailboxTable(schema string) (string, error) {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		return "agent_control_global_mailbox_records", nil
	}
	for _, r := range schema {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return "", fmt.Errorf("invalid global mailbox schema name: %q", schema)
	}
	return schema + ".agent_control_global_mailbox_records", nil
}

func marshalMailboxMetadata(metadata map[string]interface{}) (string, error) {
	if len(metadata) == 0 {
		return "{}", nil
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("marshal mailbox metadata: %w", err)
	}
	return string(payload), nil
}

func unmarshalMailboxMetadata(raw string) map[string]interface{} {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return nil
	}
	return metadata
}

func nullMailboxString(value string) interface{} {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func nullableMailboxTime(value *time.Time) interface{} {
	if value == nil || value.IsZero() {
		return nil
	}
	return formatMailboxTime(*value)
}

func formatMailboxTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseMailboxTime(raw string) time.Time {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func resolveGlobalMailboxDSN(cfg *GlobalMailboxStoreConfig) (string, error) {
	if cfg.Path != "" {
		dir := filepath.Dir(cfg.Path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("create agent control mailbox registry directory: %w", err)
			}
		}
		return ensureGlobalMailboxDSNOptions(cfg.Path), nil
	}
	if cfg.DSN != "" {
		return ensureGlobalMailboxDSNOptions(cfg.DSN), nil
	}
	return ensureGlobalMailboxDSNOptions(fmt.Sprintf("file:agent-control-mailbox-registry-%d?mode=memory&cache=shared", time.Now().UnixNano())), nil
}

func ensureGlobalMailboxDSNOptions(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return dsn
	}
	dsn = normalizeGlobalMailboxDSN(dsn)
	dsn = ensureGlobalMailboxDSNOption(dsn, "_txlock", "immediate")
	dsn = ensureGlobalMailboxDSNOption(dsn, "_busy_timeout", "5000")
	return dsn
}

func normalizeGlobalMailboxDSN(dsn string) string {
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

func ensureGlobalMailboxDSNOption(dsn, key, value string) string {
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

func isGlobalMailboxMemoryDSN(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	return lower == ":memory:" || strings.Contains(lower, "mode=memory")
}
