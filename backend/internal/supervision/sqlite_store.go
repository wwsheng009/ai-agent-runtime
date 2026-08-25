package supervision

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

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// SQLiteSupervisionStore persists the supervision control plane in SQLite.
// The database is opened lazily on first use so bootstrap can wire the store
// without paying the open/migrate cost up front (same pattern as the team
// store).
type SQLiteSupervisionStore struct {
	cfg  StoreConfig
	path string

	openOnce sync.Once
	openMu   sync.RWMutex
	openErr  error
	closed   bool

	db     *sql.DB
	dsn    string
	ownsDB bool
}

var _ Store = (*SQLiteSupervisionStore)(nil)

// NewSQLiteSupervisionStore creates a SQLite-backed supervision store.
func NewSQLiteSupervisionStore(cfg *StoreConfig) (*SQLiteSupervisionStore, error) {
	if cfg == nil {
		cfg = &StoreConfig{}
	}
	cfgCopy := *cfg
	dsn, path, err := resolveSupervisionDSN(&cfgCopy)
	if err != nil {
		return nil, err
	}
	store := &SQLiteSupervisionStore{
		cfg:  cfgCopy,
		path: path,
		dsn:  dsn,
	}
	if path == "" || supervisionMemoryDSN(dsn) {
		if err := store.ensure(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// Opened reports whether the durable backend has been opened.
func (s *SQLiteSupervisionStore) Opened() bool {
	if s == nil {
		return false
	}
	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return s.db != nil
}

// Path returns the configured on-disk path when one is used.
func (s *SQLiteSupervisionStore) Path() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.path)
}

// Close closes the underlying database when owned by this store.
func (s *SQLiteSupervisionStore) Close() error {
	if s == nil {
		return nil
	}
	s.openMu.Lock()
	s.closed = true
	db := s.db
	s.db = nil
	s.openMu.Unlock()
	if db != nil && s.ownsDB {
		return db.Close()
	}
	return nil
}

func (s *SQLiteSupervisionStore) ensure() error {
	if s == nil {
		return fmt.Errorf("supervision store is not initialized")
	}
	s.openMu.RLock()
	if s.closed {
		s.openMu.RUnlock()
		return fmt.Errorf("supervision store is closed")
	}
	if s.db != nil || s.openErr != nil {
		err := s.openErr
		s.openMu.RUnlock()
		return err
	}
	s.openMu.RUnlock()

	s.openOnce.Do(func() {
		if pathKey := strings.TrimSpace(s.path); pathKey != "" {
			if err := ensureSupervisionStoreDirectory(pathKey); err != nil {
				s.openErr = err
				return
			}
		}
		db, err := sql.Open("sqlite3", s.dsn)
		if err != nil {
			s.openErr = fmt.Errorf("open supervision db: %w", err)
			return
		}
		if supervisionMemoryDSN(s.dsn) {
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
		}
		s.openMu.Lock()
		if s.closed {
			s.openMu.Unlock()
			_ = db.Close()
			return
		}
		s.db = db
		s.ownsDB = true
		s.openMu.Unlock()
		if err := s.init(context.Background()); err != nil {
			s.openErr = err
			return
		}
	})

	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return s.openErr
}

func (s *SQLiteSupervisionStore) dbOrErr() (*sql.DB, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	s.openMu.RLock()
	defer s.openMu.RUnlock()
	if s.db == nil {
		return nil, fmt.Errorf("supervision store is not open")
	}
	return s.db, nil
}

func (s *SQLiteSupervisionStore) init(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("supervision store is not initialized")
	}
	migrations := []migrate.Migration{
		{
			Version: 1,
			Name:    "supervision_control_plane",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS supervision_lifecycle_notifications (
					notification_id TEXT PRIMARY KEY,
					root_scope_id TEXT NOT NULL,
					target_parent_session_id TEXT,
					target_parent_team_id TEXT,
					subject_kind TEXT NOT NULL,
					subject_id TEXT NOT NULL,
					subject_version INTEGER NOT NULL DEFAULT 0,
					event_seq INTEGER NOT NULL DEFAULT 0,
					event_type TEXT NOT NULL,
					severity TEXT NOT NULL,
					supervision_state TEXT NOT NULL,
					reason TEXT NOT NULL DEFAULT '',
					diagnostic_ref TEXT NOT NULL DEFAULT '',
					recommended_action TEXT NOT NULL DEFAULT '',
					allowed_actions_json TEXT NOT NULL DEFAULT '[]',
					auto_action_id TEXT NOT NULL DEFAULT '',
					delivery_state TEXT NOT NULL DEFAULT 'pending',
					decision_state TEXT NOT NULL DEFAULT 'unacknowledged',
					resolution_state TEXT NOT NULL DEFAULT 'unresolved',
					defer_until TEXT,
					delivered_at TEXT,
					seen_at TEXT,
					acknowledged_at TEXT,
					resolved_at TEXT,
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL,
					version INTEGER NOT NULL DEFAULT 1,
					UNIQUE(root_scope_id, subject_kind, subject_id, subject_version, event_type)
				);
				CREATE INDEX IF NOT EXISTS idx_supervision_notifications_scope
					ON supervision_lifecycle_notifications(root_scope_id, event_seq);
				CREATE INDEX IF NOT EXISTS idx_supervision_notifications_parent
					ON supervision_lifecycle_notifications(target_parent_session_id, resolution_state, decision_state);
				CREATE TABLE IF NOT EXISTS supervision_actions (
					action_id TEXT PRIMARY KEY,
					root_scope_id TEXT NOT NULL,
					requested_by_kind TEXT NOT NULL,
					requested_by_id TEXT NOT NULL,
					target_kind TEXT NOT NULL,
					target_id TEXT NOT NULL,
					action TEXT NOT NULL,
					cascade_mode TEXT NOT NULL DEFAULT 'none',
					reason TEXT NOT NULL DEFAULT '',
					expected_version INTEGER NOT NULL DEFAULT 0,
					expected_fencing_token TEXT NOT NULL DEFAULT '',
					status TEXT NOT NULL DEFAULT 'requested',
					result TEXT NOT NULL DEFAULT '',
					result_detail TEXT NOT NULL DEFAULT '',
					created_at TEXT NOT NULL,
					started_at TEXT,
					finished_at TEXT,
					version INTEGER NOT NULL DEFAULT 1
				);
				CREATE INDEX IF NOT EXISTS idx_supervision_actions_scope
					ON supervision_actions(root_scope_id, status);
				CREATE INDEX IF NOT EXISTS idx_supervision_actions_target
					ON supervision_actions(target_kind, target_id);
				CREATE TABLE IF NOT EXISTS supervision_wake_pending (
					wake_id TEXT PRIMARY KEY,
					root_scope_id TEXT NOT NULL,
					target_parent_session_id TEXT,
					target_parent_team_id TEXT,
					wake_reason TEXT NOT NULL DEFAULT '',
					notification_seq INTEGER NOT NULL DEFAULT 0,
					dedup_key TEXT NOT NULL,
					created_at TEXT NOT NULL,
					claimed_at TEXT,
					claimed_by TEXT NOT NULL DEFAULT '',
					UNIQUE(dedup_key)
				);
				CREATE INDEX IF NOT EXISTS idx_supervision_wake_scope
					ON supervision_wake_pending(root_scope_id, target_parent_session_id);
				CREATE TABLE IF NOT EXISTS supervision_team_edges (
					edge_id TEXT PRIMARY KEY,
					root_scope_id TEXT NOT NULL,
					root_team_id TEXT NOT NULL,
					parent_team_id TEXT NOT NULL,
					parent_kind TEXT NOT NULL DEFAULT '',
					parent_id TEXT NOT NULL DEFAULT '',
					child_team_id TEXT NOT NULL,
					relation TEXT NOT NULL DEFAULT 'spawned',
					created_by TEXT NOT NULL DEFAULT '',
					created_at TEXT NOT NULL,
					closed_at TEXT,
					status TEXT NOT NULL DEFAULT 'active',
					version INTEGER NOT NULL DEFAULT 1,
					UNIQUE(parent_team_id, child_team_id)
				);
				CREATE INDEX IF NOT EXISTS idx_supervision_team_edges_child
					ON supervision_team_edges(child_team_id, status);
				CREATE INDEX IF NOT EXISTS idx_supervision_team_edges_root
					ON supervision_team_edges(root_team_id, status);
			`,
		},
		{
			Version: 2,
			Name:    "execution_runs_and_completion_outbox",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS supervision_execution_runs (
					run_id TEXT PRIMARY KEY,
					kind TEXT NOT NULL,
					workflow TEXT NOT NULL DEFAULT '',
					root_session_id TEXT NOT NULL DEFAULT '',
					parent_session_id TEXT NOT NULL DEFAULT '',
					parent_run_id TEXT NOT NULL DEFAULT '',
					session_id TEXT NOT NULL DEFAULT '',
					agent_id TEXT NOT NULL DEFAULT '',
					attempt INTEGER NOT NULL DEFAULT 1,
					status TEXT NOT NULL,
					owner_id TEXT NOT NULL DEFAULT '',
					owner_lease_until TEXT,
					started_at TEXT,
					last_heartbeat_at TEXT,
					last_progress_at TEXT,
					progress_seq INTEGER NOT NULL DEFAULT 0,
					execution_deadline_at TEXT,
					progress_deadline_at TEXT,
					approval_deadline_at TEXT,
					cancel_requested_at TEXT,
					cancel_deadline_at TEXT,
					cancel_source TEXT NOT NULL DEFAULT '',
					finished_at TEXT,
					max_attempts INTEGER NOT NULL DEFAULT 1,
					fencing_token INTEGER NOT NULL DEFAULT 0,
					result_ref TEXT NOT NULL DEFAULT '',
					error_code TEXT NOT NULL DEFAULT '',
					version INTEGER NOT NULL DEFAULT 1,
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL
				);
				CREATE INDEX IF NOT EXISTS idx_supervision_runs_active
					ON supervision_execution_runs(status, execution_deadline_at);
				CREATE INDEX IF NOT EXISTS idx_supervision_runs_session
					ON supervision_execution_runs(session_id, created_at);
				CREATE INDEX IF NOT EXISTS idx_supervision_runs_root
					ON supervision_execution_runs(root_session_id, status);
				CREATE TABLE IF NOT EXISTS supervision_completion_outbox (
					outbox_id TEXT PRIMARY KEY,
					run_id TEXT NOT NULL,
					session_id TEXT NOT NULL DEFAULT '',
					parent_session_id TEXT NOT NULL DEFAULT '',
					root_session_id TEXT NOT NULL DEFAULT '',
					status TEXT NOT NULL,
					idempotency_key TEXT NOT NULL UNIQUE,
					payload_json TEXT NOT NULL DEFAULT '{}',
					attempts INTEGER NOT NULL DEFAULT 0,
					last_error TEXT NOT NULL DEFAULT '',
					delivered_at TEXT,
					parent_mailbox_seq INTEGER NOT NULL DEFAULT 0,
					created_at TEXT NOT NULL
				);
				CREATE INDEX IF NOT EXISTS idx_supervision_outbox_pending
					ON supervision_completion_outbox(delivered_at, created_at);
			`,
		},
	}
	return migrate.Apply(ctx, s.db, migrations)
}

func ensureSupervisionStoreDirectory(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if strings.TrimSpace(dir) == "" || dir == "." {
		return nil
	}
	info, err := os.Stat(dir)
	if err == nil && info.IsDir() {
		return nil
	}
	if os.IsNotExist(err) {
		return os.MkdirAll(dir, 0o755)
	}
	return err
}

func supervisionMemoryDSN(dsn string) bool {
	dsn = strings.ToLower(strings.TrimSpace(dsn))
	return strings.Contains(dsn, "mode=memory")
}

func resolveSupervisionDSN(cfg *StoreConfig) (string, string, error) {
	if cfg == nil {
		cfg = &StoreConfig{}
	}
	if path := strings.TrimSpace(cfg.Path); path != "" {
		return supervisionDSNOptions(path), path, nil
	}
	if dsn := strings.TrimSpace(cfg.DSN); dsn != "" {
		return supervisionDSNOptions(dsn), "", nil
	}
	return supervisionDSNOptions(fmt.Sprintf("file:supervision-%d?mode=memory&cache=shared", time.Now().UnixNano())), "", nil
}

func supervisionDSNOptions(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return dsn
	}
	if strings.HasPrefix(strings.ToLower(dsn), "file:") || strings.Contains(dsn, "?") {
		return supervisionAppendOption(dsn, "_txlock", "immediate")
	}
	return supervisionAppendOption("file:"+dsn, "_txlock", "immediate")
}

func supervisionAppendOption(dsn, key, value string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	lower := strings.ToLower(dsn)
	if strings.Contains(lower, strings.ToLower(key)+"=") {
		return dsn
	}
	return dsn + sep + key + "=" + value
}

// --- time / json helpers ---

func formatSupervisionTime(value time.Time) string {
	if value.IsZero() {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func formatNullableSupervisionTime(value *time.Time) interface{} {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseNullableSupervisionTime(raw sql.NullString) *time.Time {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func supervisionJSONList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func parseSupervisionJSONList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return values
}

// --- Notifications ---

// UpsertNotification inserts or refreshes a notification by idempotency key
// (doc 6.3 rule 2). A refresh keeps the original notification_id and
// created_at, updates state fields and bumps version so CAS readers observe
// the change.
func (s *SQLiteSupervisionStore) UpsertNotification(ctx context.Context, n Notification) (Notification, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return n, err
	}
	n.NotificationID = strings.TrimSpace(n.NotificationID)
	if n.NotificationID == "" {
		n.NotificationID = notificationIDFor(n)
	}
	now := time.Now().UTC()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return n, fmt.Errorf("begin upsert notification: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingID string
	var existingVersion int64
	var existingDecisionState string
	var existingResolutionState string
	err = tx.QueryRowContext(ctx, `
		SELECT notification_id, version, decision_state, resolution_state
		FROM supervision_lifecycle_notifications
		WHERE root_scope_id = ? AND subject_kind = ? AND subject_id = ? AND subject_version = ? AND event_type = ?
	`, n.RootScopeID, string(n.SubjectKind), n.SubjectID, n.SubjectVersion, n.EventType).Scan(
		&existingID,
		&existingVersion,
		&existingDecisionState,
		&existingResolutionState,
	)
	switch {
	case err == nil:
		// Refresh existing row, keeping original id and created_at.
		n.NotificationID = existingID
		n.Version = existingVersion + 1
		n.CreatedAt = parseExistingCreatedAt(ctx, tx, existingID, n.CreatedAt)
		n.UpdatedAt = now
		// An at-least-once lifecycle replay must not regress decisions or
		// resolutions already made by the parent. Reopening requires a new
		// subject version/event idempotency key, not a refresh of the same row.
		if n.DecisionState == "" {
			n.DecisionState = DecisionState(existingDecisionState)
		} else if n.DecisionState == DecisionUnacknowledged &&
			DecisionState(existingDecisionState) != "" &&
			DecisionState(existingDecisionState) != DecisionUnacknowledged {
			n.DecisionState = DecisionState(existingDecisionState)
		}
		if n.ResolutionState == ResolutionUnresolved &&
			ResolutionState(existingResolutionState) != "" &&
			ResolutionState(existingResolutionState) != ResolutionUnresolved {
			n.ResolutionState = ResolutionState(existingResolutionState)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE supervision_lifecycle_notifications SET
				target_parent_session_id = ?, target_parent_team_id = ?,
				event_seq = ?, severity = ?, supervision_state = ?, reason = ?,
				diagnostic_ref = ?, recommended_action = ?, allowed_actions_json = ?,
				auto_action_id = ?, defer_until = ?,
				delivery_state = COALESCE(NULLIF(?, ''), delivery_state),
				decision_state = COALESCE(NULLIF(?, ''), decision_state),
				resolution_state = COALESCE(NULLIF(?, ''), resolution_state),
				delivered_at = COALESCE(NULLIF(?, ''), delivered_at),
				seen_at = COALESCE(NULLIF(?, ''), seen_at),
				acknowledged_at = COALESCE(NULLIF(?, ''), acknowledged_at),
				resolved_at = COALESCE(NULLIF(?, ''), resolved_at),
				updated_at = ?, version = ?
			WHERE notification_id = ?
		`,
			nullSupervisionString(n.TargetParentSessionID),
			nullSupervisionString(n.TargetParentTeamID),
			n.EventSeq,
			string(n.Severity),
			string(n.SupervisionState),
			n.Reason,
			n.DiagnosticRef,
			n.RecommendedAction,
			supervisionJSONList(n.AllowedActions),
			n.AutoActionID,
			formatNullableSupervisionTime(n.DeferUntil),
			string(n.DeliveryState),
			string(n.DecisionState),
			string(n.ResolutionState),
			formatNullableSupervisionTime(n.DeliveredAt),
			formatNullableSupervisionTime(n.SeenAt),
			formatNullableSupervisionTime(n.AcknowledgedAt),
			formatNullableSupervisionTime(n.ResolvedAt),
			formatSupervisionTime(now),
			n.Version,
			existingID,
		); err != nil {
			return n, fmt.Errorf("refresh notification: %w", err)
		}
	case err == sql.ErrNoRows:
		n.CreatedAt = now
		n.UpdatedAt = now
		if n.Version <= 0 {
			n.Version = 1
		}
		if n.DeliveryState == "" {
			n.DeliveryState = DeliveryPending
		}
		if n.DecisionState == "" {
			n.DecisionState = DecisionUnacknowledged
		}
		if n.ResolutionState == "" {
			n.ResolutionState = ResolutionUnresolved
		}
		if n.EventSeq <= 0 {
			var lastSeq int64
			if err := tx.QueryRowContext(ctx, `
				SELECT COALESCE(MAX(event_seq), 0)
				FROM supervision_lifecycle_notifications
				WHERE root_scope_id = ?
			`, n.RootScopeID).Scan(&lastSeq); err != nil {
				return n, fmt.Errorf("allocate notification event sequence: %w", err)
			}
			n.EventSeq = lastSeq + 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO supervision_lifecycle_notifications (
				notification_id, root_scope_id, target_parent_session_id, target_parent_team_id,
				subject_kind, subject_id, subject_version, event_seq, event_type, severity,
				supervision_state, reason, diagnostic_ref, recommended_action, allowed_actions_json,
				auto_action_id, delivery_state, decision_state, resolution_state, defer_until,
				delivered_at, seen_at, acknowledged_at, resolved_at, created_at, updated_at, version
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			n.NotificationID,
			n.RootScopeID,
			nullSupervisionString(n.TargetParentSessionID),
			nullSupervisionString(n.TargetParentTeamID),
			string(n.SubjectKind),
			n.SubjectID,
			n.SubjectVersion,
			n.EventSeq,
			n.EventType,
			string(n.Severity),
			string(n.SupervisionState),
			n.Reason,
			n.DiagnosticRef,
			n.RecommendedAction,
			supervisionJSONList(n.AllowedActions),
			n.AutoActionID,
			string(n.DeliveryState),
			string(n.DecisionState),
			string(n.ResolutionState),
			formatNullableSupervisionTime(n.DeferUntil),
			formatNullableSupervisionTime(n.DeliveredAt),
			formatNullableSupervisionTime(n.SeenAt),
			formatNullableSupervisionTime(n.AcknowledgedAt),
			formatNullableSupervisionTime(n.ResolvedAt),
			formatSupervisionTime(n.CreatedAt),
			formatSupervisionTime(n.UpdatedAt),
			n.Version,
		); err != nil {
			return n, fmt.Errorf("insert notification: %w", err)
		}
	default:
		return n, fmt.Errorf("lookup notification: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return n, fmt.Errorf("commit upsert notification: %w", err)
	}
	return n, nil
}

func parseExistingCreatedAt(ctx context.Context, tx *sql.Tx, notificationID string, fallback time.Time) time.Time {
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT created_at FROM supervision_lifecycle_notifications WHERE notification_id = ?`, notificationID).Scan(&raw); err == nil && raw.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, raw.String); err == nil {
			return parsed
		}
	}
	if fallback.IsZero() {
		return time.Now().UTC()
	}
	return fallback
}

func notificationIDFor(n Notification) string {
	sum := hashString(n.IdempotencyKey())
	return fmt.Sprintf("n-%s-%x", sanitizeID(n.SubjectID), sum)
}

func (s *SQLiteSupervisionStore) GetNotification(ctx context.Context, notificationID string) (*Notification, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	row := db.QueryRowContext(ctx, `
		SELECT notification_id, root_scope_id, target_parent_session_id, target_parent_team_id,
			subject_kind, subject_id, subject_version, event_seq, event_type, severity,
			supervision_state, reason, diagnostic_ref, recommended_action, allowed_actions_json,
			auto_action_id, delivery_state, decision_state, resolution_state, defer_until,
			delivered_at, seen_at, acknowledged_at, resolved_at, created_at, updated_at, version
		FROM supervision_lifecycle_notifications WHERE notification_id = ?
	`, notificationID)
	record, err := scanNotification(row)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *SQLiteSupervisionStore) ListNotifications(ctx context.Context, filter NotificationFilter) ([]Notification, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	clauses := []string{"1=1"}
	args := []interface{}{}
	if v := strings.TrimSpace(filter.RootScopeID); v != "" {
		clauses = append(clauses, "root_scope_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(filter.TargetParentSessionID); v != "" {
		clauses = append(clauses, "target_parent_session_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(filter.TargetParentTeamID); v != "" {
		clauses = append(clauses, "target_parent_team_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(string(filter.SubjectKind)); v != "" {
		clauses = append(clauses, "subject_kind = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(filter.SubjectID); v != "" {
		clauses = append(clauses, "subject_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(string(filter.Severity)); v != "" {
		clauses = append(clauses, "severity = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(string(filter.DecisionState)); v != "" {
		clauses = append(clauses, "decision_state = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(string(filter.ResolutionState)); v != "" {
		clauses = append(clauses, "resolution_state = ?")
		args = append(args, v)
	}
	if !filter.IncludeResolved {
		clauses = append(clauses, "resolution_state = ?")
		args = append(args, string(ResolutionUnresolved))
	}
	if filter.AfterSeq > 0 {
		clauses = append(clauses, "event_seq > ?")
		args = append(args, filter.AfterSeq)
	}
	if filter.ActionRequiredOnly {
		clauses = append(clauses, "decision_state IN (?, ?)")
		args = append(args, string(DecisionUnacknowledged), string(DecisionDeferred))
		clauses = append(clauses, "resolution_state = ?")
		args = append(args, string(ResolutionUnresolved))
	}
	order := " ORDER BY event_seq ASC, created_at ASC"
	if filter.Limit > 0 {
		order += " LIMIT " + fmt.Sprintf("%d", filter.Limit)
	}
	query := "SELECT notification_id, root_scope_id, target_parent_session_id, target_parent_team_id, subject_kind, subject_id, subject_version, event_seq, event_type, severity, supervision_state, reason, diagnostic_ref, recommended_action, allowed_actions_json, auto_action_id, delivery_state, decision_state, resolution_state, defer_until, delivered_at, seen_at, acknowledged_at, resolved_at, created_at, updated_at, version FROM supervision_lifecycle_notifications WHERE " + strings.Join(clauses, " AND ") + order
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()
	var records []Notification
	for rows.Next() {
		record, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *SQLiteSupervisionStore) LastNotificationSeq(ctx context.Context, rootScopeID string) (int64, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return 0, err
	}
	var seq int64
	err = db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(event_seq), 0) FROM supervision_lifecycle_notifications WHERE root_scope_id = ?
	`, rootScopeID).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("last notification seq: %w", err)
	}
	return seq, nil
}

func (s *SQLiteSupervisionStore) MarkNotificationDelivered(ctx context.Context, notificationID string, at time.Time) error {
	return s.updateNotificationTimestamp(ctx, notificationID, "delivery_state", string(DeliveryDelivered), "delivered_at", at)
}

func (s *SQLiteSupervisionStore) MarkNotificationSeen(ctx context.Context, notificationID string, at time.Time) error {
	return s.updateNotificationTimestamp(ctx, notificationID, "delivery_state", string(DeliverySeen), "seen_at", at)
}

func (s *SQLiteSupervisionStore) updateNotificationTimestamp(ctx context.Context, notificationID, stateCol, stateValue, atCol string, at time.Time) error {
	db, err := s.dbOrErr()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE supervision_lifecycle_notifications
		SET `+stateCol+` = ?, `+atCol+` = ?, updated_at = ?, version = version + 1
		WHERE notification_id = ?
	`, stateValue, formatSupervisionTime(at), formatSupervisionTime(at), notificationID)
	if err != nil {
		return fmt.Errorf("update notification %s: %w", atCol, err)
	}
	return nil
}

func (s *SQLiteSupervisionStore) AcknowledgeNotification(ctx context.Context, notificationID string, at time.Time, expectedVersion int64) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `
		UPDATE supervision_lifecycle_notifications
		SET decision_state = ?, acknowledged_at = ?, updated_at = ?, version = version + 1
		WHERE notification_id = ? AND version = ?
	`, string(DecisionAcknowledged), formatSupervisionTime(at), formatSupervisionTime(at), notificationID, expectedVersion)
	if err != nil {
		return false, fmt.Errorf("acknowledge notification: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (s *SQLiteSupervisionStore) DeferNotification(ctx context.Context, notificationID string, until time.Time, reason string, expectedVersion int64) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `
		UPDATE supervision_lifecycle_notifications
		SET decision_state = ?, defer_until = ?, reason = ?, updated_at = ?, version = version + 1
		WHERE notification_id = ? AND version = ?
	`, string(DecisionDeferred), formatNullableSupervisionTime(&until), reason, formatSupervisionTime(time.Now().UTC()), notificationID, expectedVersion)
	if err != nil {
		return false, fmt.Errorf("defer notification: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (s *SQLiteSupervisionStore) ResolveNotification(ctx context.Context, notificationID string, resolution ResolutionState, at time.Time, expectedVersion int64) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	resolution = ResolutionState(strings.TrimSpace(string(resolution)))
	if resolution == "" {
		resolution = ResolutionRecovered
	}
	result, err := db.ExecContext(ctx, `
		UPDATE supervision_lifecycle_notifications
		SET resolution_state = ?, resolved_at = ?, updated_at = ?, version = version + 1
		WHERE notification_id = ? AND version = ?
	`, string(resolution), formatSupervisionTime(at), formatSupervisionTime(at), notificationID, expectedVersion)
	if err != nil {
		return false, fmt.Errorf("resolve notification: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

type notificationScanner interface {
	Scan(dest ...interface{}) error
}

func scanNotification(scanner notificationScanner) (Notification, error) {
	var n Notification
	var allowedJSON, targetParentSession, targetParentTeam sql.NullString
	var deferUntil, deliveredAt, seenAt, ackedAt, resolvedAt sql.NullString
	var createdAt, updatedAt sql.NullString
	err := scanner.Scan(
		&n.NotificationID, &n.RootScopeID, &targetParentSession, &targetParentTeam,
		&n.SubjectKind, &n.SubjectID, &n.SubjectVersion, &n.EventSeq, &n.EventType, &n.Severity,
		&n.SupervisionState, &n.Reason, &n.DiagnosticRef, &n.RecommendedAction, &allowedJSON,
		&n.AutoActionID, &n.DeliveryState, &n.DecisionState, &n.ResolutionState, &deferUntil,
		&deliveredAt, &seenAt, &ackedAt, &resolvedAt, &createdAt, &updatedAt, &n.Version,
	)
	if err != nil {
		return n, err
	}
	n.TargetParentSessionID = targetParentSession.String
	n.TargetParentTeamID = targetParentTeam.String
	n.AllowedActions = parseSupervisionJSONList(allowedJSON.String)
	n.DeferUntil = parseNullableSupervisionTime(deferUntil)
	n.DeliveredAt = parseNullableSupervisionTime(deliveredAt)
	n.SeenAt = parseNullableSupervisionTime(seenAt)
	n.AcknowledgedAt = parseNullableSupervisionTime(ackedAt)
	n.ResolvedAt = parseNullableSupervisionTime(resolvedAt)
	n.CreatedAt = parseSupervisionTime(createdAt)
	n.UpdatedAt = parseSupervisionTime(updatedAt)
	return n, nil
}

func parseSupervisionTime(raw sql.NullString) time.Time {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func nullSupervisionString(value string) interface{} {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

// --- Actions ---

func (s *SQLiteSupervisionStore) CreateAction(ctx context.Context, a ActionRecord) (ActionRecord, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return a, err
	}
	a.ActionID = strings.TrimSpace(a.ActionID)
	if a.ActionID == "" {
		a.ActionID = fmt.Sprintf("act-%d", time.Now().UnixNano())
	}
	a.CreatedAt = time.Now().UTC()
	if a.Status == "" {
		a.Status = ActionRequested
	}
	if a.Version <= 0 {
		a.Version = 1
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO supervision_actions (
			action_id, root_scope_id, requested_by_kind, requested_by_id,
			target_kind, target_id, action, cascade_mode, reason,
			expected_version, expected_fencing_token, status, result, result_detail,
			created_at, started_at, finished_at, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		a.ActionID,
		a.RootScopeID,
		a.RequestedByKind,
		a.RequestedByID,
		string(a.TargetKind),
		a.TargetID,
		string(a.Action),
		string(a.CascadeMode),
		a.Reason,
		a.ExpectedVersion,
		a.ExpectedFencingToken,
		string(a.Status),
		a.Result,
		a.ResultDetail,
		formatSupervisionTime(a.CreatedAt),
		formatNullableSupervisionTime(a.StartedAt),
		formatNullableSupervisionTime(a.FinishedAt),
		a.Version,
	)
	if err != nil {
		return a, fmt.Errorf("create action: %w", err)
	}
	return a, nil
}

func (s *SQLiteSupervisionStore) GetAction(ctx context.Context, actionID string) (*ActionRecord, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	row := db.QueryRowContext(ctx, `
		SELECT action_id, root_scope_id, requested_by_kind, requested_by_id,
			target_kind, target_id, action, cascade_mode, reason,
			expected_version, expected_fencing_token, status, result, result_detail,
			created_at, started_at, finished_at, version
		FROM supervision_actions WHERE action_id = ?
	`, actionID)
	record, err := scanAction(row)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *SQLiteSupervisionStore) ListActions(ctx context.Context, filter ActionFilter) ([]ActionRecord, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	clauses := []string{"1=1"}
	args := []interface{}{}
	if v := strings.TrimSpace(filter.RootScopeID); v != "" {
		clauses = append(clauses, "root_scope_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(string(filter.TargetKind)); v != "" {
		clauses = append(clauses, "target_kind = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(filter.TargetID); v != "" {
		clauses = append(clauses, "target_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(string(filter.Action)); v != "" {
		clauses = append(clauses, "action = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(string(filter.Status)); v != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, v)
	}
	order := " ORDER BY created_at DESC"
	if filter.Limit > 0 {
		order += " LIMIT " + fmt.Sprintf("%d", filter.Limit)
	}
	query := "SELECT action_id, root_scope_id, requested_by_kind, requested_by_id, target_kind, target_id, action, cascade_mode, reason, expected_version, expected_fencing_token, status, result, result_detail, created_at, started_at, finished_at, version FROM supervision_actions WHERE " + strings.Join(clauses, " AND ") + order
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list actions: %w", err)
	}
	defer rows.Close()
	var records []ActionRecord
	for rows.Next() {
		record, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *SQLiteSupervisionStore) UpdateActionStatus(ctx context.Context, a ActionRecord, expectedVersion int64) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `
		UPDATE supervision_actions
		SET status = ?, result = ?, result_detail = ?, started_at = ?, finished_at = ?, version = version + 1
		WHERE action_id = ? AND version = ?
	`,
		string(a.Status),
		a.Result,
		a.ResultDetail,
		formatNullableSupervisionTime(a.StartedAt),
		formatNullableSupervisionTime(a.FinishedAt),
		a.ActionID,
		expectedVersion,
	)
	if err != nil {
		return false, fmt.Errorf("update action status: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

type actionScanner interface {
	Scan(dest ...interface{}) error
}

func scanAction(scanner actionScanner) (ActionRecord, error) {
	var a ActionRecord
	var startedAt, finishedAt, createdAt sql.NullString
	err := scanner.Scan(
		&a.ActionID, &a.RootScopeID, &a.RequestedByKind, &a.RequestedByID,
		&a.TargetKind, &a.TargetID, &a.Action, &a.CascadeMode, &a.Reason,
		&a.ExpectedVersion, &a.ExpectedFencingToken, &a.Status, &a.Result, &a.ResultDetail,
		&createdAt, &startedAt, &finishedAt, &a.Version,
	)
	if err != nil {
		return a, err
	}
	a.CreatedAt = parseSupervisionTime(createdAt)
	a.StartedAt = parseNullableSupervisionTime(startedAt)
	a.FinishedAt = parseNullableSupervisionTime(finishedAt)
	return a, nil
}

// --- Wake pending ---

func (s *SQLiteSupervisionStore) InsertWakePending(ctx context.Context, w WakePending) error {
	db, err := s.dbOrErr()
	if err != nil {
		return err
	}
	w.WakeID = strings.TrimSpace(w.WakeID)
	if w.WakeID == "" {
		w.WakeID = fmt.Sprintf("wake-%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(w.DedupKey) == "" {
		w.DedupKey = strings.Join([]string{
			strings.TrimSpace(w.RootScopeID),
			strings.TrimSpace(w.TargetParentSessionID),
			strings.TrimSpace(w.WakeReason),
		}, "|")
	}
	w.CreatedAt = time.Now().UTC()
	_, err = db.ExecContext(ctx, `
		INSERT OR IGNORE INTO supervision_wake_pending (
			wake_id, root_scope_id, target_parent_session_id, target_parent_team_id,
			wake_reason, notification_seq, dedup_key, created_at, claimed_at, claimed_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		w.WakeID,
		w.RootScopeID,
		nullSupervisionString(w.TargetParentSessionID),
		nullSupervisionString(w.TargetParentTeamID),
		w.WakeReason,
		w.NotificationSeq,
		w.DedupKey,
		formatSupervisionTime(w.CreatedAt),
		formatNullableSupervisionTime(w.ClaimedAt),
		w.ClaimedBy,
	)
	if err != nil {
		return fmt.Errorf("insert wake pending: %w", err)
	}
	return nil
}

func (s *SQLiteSupervisionStore) ListWakePending(ctx context.Context, filter WakeFilter) ([]WakePending, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	clauses := []string{"1=1"}
	args := []interface{}{}
	if v := strings.TrimSpace(filter.RootScopeID); v != "" {
		clauses = append(clauses, "root_scope_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(filter.TargetParentSessionID); v != "" {
		clauses = append(clauses, "target_parent_session_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(filter.TargetParentTeamID); v != "" {
		clauses = append(clauses, "target_parent_team_id = ?")
		args = append(args, v)
	}
	if filter.UnclaimedOnly {
		clauses = append(clauses, "claimed_at IS NULL")
	}
	query := "SELECT wake_id, root_scope_id, target_parent_session_id, target_parent_team_id, wake_reason, notification_seq, dedup_key, created_at, claimed_at, claimed_by FROM supervision_wake_pending WHERE " + strings.Join(clauses, " AND ") + " ORDER BY created_at ASC"
	if filter.Limit > 0 {
		query += " LIMIT " + fmt.Sprintf("%d", filter.Limit)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list wake pending: %w", err)
	}
	defer rows.Close()
	var records []WakePending
	for rows.Next() {
		var w WakePending
		var createdAt, claimedAt sql.NullString
		var targetParentSession, targetParentTeam sql.NullString
		if err := rows.Scan(&w.WakeID, &w.RootScopeID, &targetParentSession, &targetParentTeam, &w.WakeReason, &w.NotificationSeq, &w.DedupKey, &createdAt, &claimedAt, &w.ClaimedBy); err != nil {
			return nil, err
		}
		w.TargetParentSessionID = targetParentSession.String
		w.TargetParentTeamID = targetParentTeam.String
		w.CreatedAt = parseSupervisionTime(createdAt)
		w.ClaimedAt = parseNullableSupervisionTime(claimedAt)
		records = append(records, w)
	}
	return records, rows.Err()
}

func (s *SQLiteSupervisionStore) ClaimWakePending(ctx context.Context, wakeID, claimedBy string, at time.Time) (bool, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `
		UPDATE supervision_wake_pending
		SET claimed_at = ?, claimed_by = ?
		WHERE wake_id = ? AND claimed_at IS NULL
	`, formatSupervisionTime(at), claimedBy, wakeID)
	if err != nil {
		return false, fmt.Errorf("claim wake pending: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (s *SQLiteSupervisionStore) ResolveWakePending(ctx context.Context, wakeID string) error {
	db, err := s.dbOrErr()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM supervision_wake_pending WHERE wake_id = ?`, wakeID); err != nil {
		return fmt.Errorf("resolve wake pending: %w", err)
	}
	return nil
}

// --- Team parent edges ---

func (s *SQLiteSupervisionStore) UpsertTeamEdge(ctx context.Context, edge TeamEdge) (TeamEdge, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return edge, err
	}
	edge.EdgeID = strings.TrimSpace(edge.EdgeID)
	if edge.EdgeID == "" {
		edge.EdgeID = fmt.Sprintf("edge-%x", hashString(edge.ParentTeamID+"|"+edge.ChildTeamID))
	}
	if edge.Status == "" {
		edge.Status = TeamEdgeStatusActive
	}
	edge.CreatedAt = time.Now().UTC()
	if edge.Version <= 0 {
		edge.Version = 1
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO supervision_team_edges (
			edge_id, root_scope_id, root_team_id, parent_team_id, parent_kind, parent_id,
			child_team_id, relation, created_by, created_at, closed_at, status, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(parent_team_id, child_team_id) DO UPDATE SET
			root_scope_id = excluded.root_scope_id,
			root_team_id = excluded.root_team_id,
			parent_kind = excluded.parent_kind,
			parent_id = excluded.parent_id,
			relation = excluded.relation,
			created_by = excluded.created_by,
			status = excluded.status,
			closed_at = excluded.closed_at,
			version = supervision_team_edges.version + 1
	`,
		edge.EdgeID,
		edge.RootScopeID,
		edge.RootTeamID,
		edge.ParentTeamID,
		edge.ParentKind,
		edge.ParentID,
		edge.ChildTeamID,
		edge.Relation,
		edge.CreatedBy,
		formatSupervisionTime(edge.CreatedAt),
		formatNullableSupervisionTime(edge.ClosedAt),
		edge.Status,
		edge.Version,
	)
	if err != nil {
		return edge, fmt.Errorf("upsert team edge: %w", err)
	}
	return edge, nil
}

func (s *SQLiteSupervisionStore) GetTeamEdge(ctx context.Context, edgeID string) (*TeamEdge, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	row := db.QueryRowContext(ctx, `
		SELECT edge_id, root_scope_id, root_team_id, parent_team_id, parent_kind, parent_id,
			child_team_id, relation, created_by, created_at, closed_at, status, version
		FROM supervision_team_edges WHERE edge_id = ?
	`, edgeID)
	edge, err := scanTeamEdge(row)
	if err != nil {
		return nil, err
	}
	return &edge, nil
}

func (s *SQLiteSupervisionStore) ListChildTeams(ctx context.Context, parentTeamID string) ([]TeamEdge, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT edge_id, root_scope_id, root_team_id, parent_team_id, parent_kind, parent_id,
			child_team_id, relation, created_by, created_at, closed_at, status, version
		FROM supervision_team_edges
		WHERE parent_team_id = ? AND status = ?
		ORDER BY created_at ASC
	`, parentTeamID, TeamEdgeStatusActive)
	if err != nil {
		return nil, fmt.Errorf("list child teams: %w", err)
	}
	defer rows.Close()
	var edges []TeamEdge
	for rows.Next() {
		edge, err := scanTeamEdge(rows)
		if err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

// ListTeamAncestors walks parent edges from childTeamID up to the root and
// returns the chain root-first. Cycles are rejected after maxTeamEdgeDepth
// hops.
func (s *SQLiteSupervisionStore) ListTeamAncestors(ctx context.Context, childTeamID string) ([]TeamEdge, error) {
	db, err := s.dbOrErr()
	if err != nil {
		return nil, err
	}
	var chain []TeamEdge
	seen := map[string]bool{}
	current := strings.TrimSpace(childTeamID)
	for hop := 0; hop < maxTeamEdgeDepth && current != ""; hop++ {
		if seen[current] {
			return nil, fmt.Errorf("team edge cycle detected at %q", current)
		}
		seen[current] = true
		edge, err := s.parentTeamEdge(ctx, db, current)
		if err != nil {
			return nil, err
		}
		if edge == nil {
			break
		}
		chain = append([]TeamEdge{*edge}, chain...)
		current = strings.TrimSpace(edge.ParentTeamID)
	}
	return chain, nil
}

const maxTeamEdgeDepth = 64

func (s *SQLiteSupervisionStore) parentTeamEdge(ctx context.Context, db *sql.DB, childTeamID string) (*TeamEdge, error) {
	row := db.QueryRowContext(ctx, `
		SELECT edge_id, root_scope_id, root_team_id, parent_team_id, parent_kind, parent_id,
			child_team_id, relation, created_by, created_at, closed_at, status, version
		FROM supervision_team_edges
		WHERE child_team_id = ? AND status = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, childTeamID, TeamEdgeStatusActive)
	edge, err := scanTeamEdge(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &edge, nil
}

func (s *SQLiteSupervisionStore) CloseTeamEdge(ctx context.Context, edgeID string, at time.Time) error {
	db, err := s.dbOrErr()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE supervision_team_edges
		SET status = ?, closed_at = ?, version = version + 1
		WHERE edge_id = ?
	`, TeamEdgeStatusClosed, formatSupervisionTime(at), edgeID)
	if err != nil {
		return fmt.Errorf("close team edge: %w", err)
	}
	return nil
}

type teamEdgeScanner interface {
	Scan(dest ...interface{}) error
}

func scanTeamEdge(scanner teamEdgeScanner) (TeamEdge, error) {
	var e TeamEdge
	var createdAt, closedAt sql.NullString
	err := scanner.Scan(
		&e.EdgeID, &e.RootScopeID, &e.RootTeamID, &e.ParentTeamID, &e.ParentKind, &e.ParentID,
		&e.ChildTeamID, &e.Relation, &e.CreatedBy, &createdAt, &closedAt, &e.Status, &e.Version,
	)
	if err != nil {
		return e, err
	}
	e.CreatedAt = parseSupervisionTime(createdAt)
	e.ClosedAt = parseNullableSupervisionTime(closedAt)
	return e, nil
}

// hashString is a small stable FNV-1a hash used for derived ids.
func hashString(value string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	hash := uint64(offset64)
	for i := 0; i < len(value); i++ {
		hash ^= uint64(value[i])
		hash *= prime64
	}
	return hash
}

func sanitizeID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "unknown"
	}
	if len(result) > 48 {
		result = result[:48]
	}
	return result
}
