package agentcontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// GlobalAgentStoreConfig controls the durable AgentControl identity registry
// store. Path and DSN follow the same rules as the global mailbox registry.
type GlobalAgentStoreConfig struct {
	Path string
	DSN  string
}

// SQLiteGlobalAgentRegistryStore persists the AgentControl identity graph in a
// single durable row-id space. It is deliberately scoped to identity data; chat
// sessions remain execution state projections.
// Path-backed construction is cheap: the durable database is opened on first
// use so chat bootstrap can wire AgentControl without creating
// agent_control.sqlite early.
type SQLiteGlobalAgentRegistryStore struct {
	cfg  GlobalAgentStoreConfig
	path string

	openOnce   sync.Once
	openMu     sync.RWMutex
	openErr    error
	closed     bool
	sharedOpen func() error

	db     *sql.DB
	dsn    string
	ownsDB bool

	agentWakeMu          sync.Mutex
	nextAgentWakeWatchID int64
	agentWakeWatchers    map[int64]globalAgentWakeWatcher
}

var _ AgentRegistryStore = (*SQLiteGlobalAgentRegistryStore)(nil)
var _ AgentSpawnReservationStore = (*SQLiteGlobalAgentRegistryStore)(nil)
var _ AgentWakeSource = (*SQLiteGlobalAgentRegistryStore)(nil)

type globalAgentWakeWatcher struct {
	filter  AgentWakeFilter
	ch      chan<- AgentWakeEvent
	unwatch func()
}

// NewSQLiteGlobalAgentRegistryStore creates a SQLite-backed AgentControl
// identity registry store. Path-backed stores open lazily on first use.
func NewSQLiteGlobalAgentRegistryStore(cfg *GlobalAgentStoreConfig) (*SQLiteGlobalAgentRegistryStore, error) {
	if cfg == nil {
		cfg = &GlobalAgentStoreConfig{}
	}
	cfgCopy := *cfg
	dsn, path, err := resolveLazyGlobalAgentDSN(&cfgCopy)
	if err != nil {
		return nil, err
	}
	store := &SQLiteGlobalAgentRegistryStore{
		cfg:    cfgCopy,
		path:   path,
		dsn:    dsn,
		ownsDB: true,
	}
	if path == "" || isGlobalMailboxMemoryDSN(dsn) {
		if err := store.ensure(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func newSQLiteGlobalAgentRegistryStoreWithDB(ctx context.Context, db *sql.DB, dsn string) (*SQLiteGlobalAgentRegistryStore, error) {
	if db == nil {
		return nil, fmt.Errorf("agent control agent registry db is not initialized")
	}
	store := &SQLiteGlobalAgentRegistryStore{db: db, dsn: strings.TrimSpace(dsn)}
	if err := store.init(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// Opened reports whether the durable backend has been opened.
func (s *SQLiteGlobalAgentRegistryStore) Opened() bool {
	if s == nil {
		return false
	}
	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return s.db != nil
}

// Path returns the configured on-disk path when one is used.
func (s *SQLiteGlobalAgentRegistryStore) Path() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.path)
}

func (s *SQLiteGlobalAgentRegistryStore) ensure() error {
	if s == nil {
		return fmt.Errorf("agent control agent registry store is not initialized")
	}
	if s.sharedOpen != nil {
		return s.sharedOpen()
	}
	s.openMu.RLock()
	if s.closed {
		s.openMu.RUnlock()
		return fmt.Errorf("agent control agent registry store is closed")
	}
	if s.db != nil || s.openErr != nil {
		err := s.openErr
		s.openMu.RUnlock()
		return err
	}
	s.openMu.RUnlock()

	s.openOnce.Do(func() {
		pathKey := strings.TrimSpace(s.path)
		if pathKey != "" {
			agentControlPathOpenMu.Lock()
			defer agentControlPathOpenMu.Unlock()
		}
		s.openMu.Lock()
		defer s.openMu.Unlock()
		if s.closed {
			s.openErr = fmt.Errorf("agent control agent registry store is closed")
			return
		}
		if s.db != nil || s.openErr != nil {
			return
		}
		if err := ensureAgentControlStoreDirectory(s.path); err != nil {
			s.openErr = err
			return
		}
		db, err := sql.Open("sqlite3", s.dsn)
		if err != nil {
			s.openErr = fmt.Errorf("open agent control agent registry db: %w", err)
			return
		}
		// 单写者连接池（含文件模式）：避免同库多连接自锁。
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if err := configureAgentControlSQLiteDB(context.Background(), db, s.dsn); err != nil {
			_ = db.Close()
			s.openErr = err
			return
		}
		store := &SQLiteGlobalAgentRegistryStore{db: db, dsn: s.dsn}
		if err := store.init(context.Background()); err != nil {
			_ = db.Close()
			s.openErr = err
			return
		}
		s.db = db
		s.ownsDB = true
	})

	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return s.openErr
}

func (s *SQLiteGlobalAgentRegistryStore) durableFileExists() bool {
	if s == nil {
		return false
	}
	path := strings.TrimSpace(s.path)
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func (s *SQLiteGlobalAgentRegistryStore) ensureForRead() (skipEmpty bool, err error) {
	if s == nil {
		return false, fmt.Errorf("agent control agent registry store is not initialized")
	}
	if s.Opened() || s.durableFileExists() {
		return false, s.ensure()
	}
	if strings.TrimSpace(s.path) == "" {
		return false, s.ensure()
	}
	return true, nil
}

// Close closes the underlying database if it was opened and owned.
func (s *SQLiteGlobalAgentRegistryStore) Close() error {
	if s == nil {
		return nil
	}
	s.openMu.Lock()
	defer s.openMu.Unlock()
	s.closed = true
	s.closeAgentWakeWatchers()
	if s.db == nil || !s.ownsDB {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *SQLiteGlobalAgentRegistryStore) closeAgentWakeWatchers() {
	if s == nil {
		return
	}
	s.agentWakeMu.Lock()
	watchers := s.agentWakeWatchers
	s.agentWakeWatchers = nil
	s.agentWakeMu.Unlock()
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

// UpsertAgentControlAgent inserts or refreshes one durable AgentControl
// identity row. The row is idempotent by AgentID; root/path uniqueness remains
// enforced separately so conflicting identity projections are visible errors.
func (s *SQLiteGlobalAgentRegistryStore) UpsertAgentControlAgent(ctx context.Context, record AgentRecord) (AgentRecord, error) {
	if s == nil {
		return AgentRecord{}, fmt.Errorf("agent control agent registry store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return AgentRecord{}, err
	}
	record = record.Normalize()
	if record.AgentID == "" {
		return AgentRecord{}, fmt.Errorf("agent id is required")
	}
	if record.RootSessionID == "" {
		return AgentRecord{}, fmt.Errorf("root session id is required")
	}
	if record.AgentPath == "" {
		return AgentRecord{}, fmt.Errorf("agent path is required")
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_control_agents (
			agent_id, root_session_id, parent_agent_id, parent_session_id, session_id, agent_path, depth,
			agent_type, nickname, workflow, team_id, teammate_id, provider, model, reasoning_effort,
			difficulty, difficulty_source, difficulty_rationale, route_source, route_warnings_json,
			fallback_used, fallback_reason, requested_provider, effective_provider, requested_model,
			effective_model, requested_reasoning_effort, effective_reasoning_effort,
			requested_permission_mode, effective_permission_mode, status, created_at, updated_at, closed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			root_session_id = excluded.root_session_id,
			parent_agent_id = excluded.parent_agent_id,
			parent_session_id = excluded.parent_session_id,
			session_id = excluded.session_id,
			agent_path = excluded.agent_path,
			depth = excluded.depth,
			agent_type = excluded.agent_type,
			nickname = excluded.nickname,
			workflow = excluded.workflow,
			team_id = excluded.team_id,
			teammate_id = excluded.teammate_id,
			provider = excluded.provider,
			model = excluded.model,
			reasoning_effort = excluded.reasoning_effort,
			difficulty = excluded.difficulty,
			difficulty_source = excluded.difficulty_source,
			difficulty_rationale = excluded.difficulty_rationale,
			route_source = excluded.route_source,
			route_warnings_json = excluded.route_warnings_json,
			fallback_used = excluded.fallback_used,
			fallback_reason = excluded.fallback_reason,
			requested_provider = excluded.requested_provider,
			effective_provider = excluded.effective_provider,
			requested_model = excluded.requested_model,
			effective_model = excluded.effective_model,
			requested_reasoning_effort = excluded.requested_reasoning_effort,
			effective_reasoning_effort = excluded.effective_reasoning_effort,
			requested_permission_mode = excluded.requested_permission_mode,
			effective_permission_mode = excluded.effective_permission_mode,
			status = excluded.status,
			updated_at = excluded.updated_at,
			closed_at = excluded.closed_at
		ON CONFLICT(root_session_id, agent_path) DO UPDATE SET
			agent_id = excluded.agent_id,
			parent_agent_id = excluded.parent_agent_id,
			parent_session_id = excluded.parent_session_id,
			session_id = excluded.session_id,
			depth = excluded.depth,
			agent_type = excluded.agent_type,
			nickname = excluded.nickname,
			workflow = excluded.workflow,
			team_id = excluded.team_id,
			teammate_id = excluded.teammate_id,
			provider = excluded.provider,
			model = excluded.model,
			reasoning_effort = excluded.reasoning_effort,
			difficulty = excluded.difficulty,
			difficulty_source = excluded.difficulty_source,
			difficulty_rationale = excluded.difficulty_rationale,
			route_source = excluded.route_source,
			route_warnings_json = excluded.route_warnings_json,
			fallback_used = excluded.fallback_used,
			fallback_reason = excluded.fallback_reason,
			requested_provider = excluded.requested_provider,
			effective_provider = excluded.effective_provider,
			requested_model = excluded.requested_model,
			effective_model = excluded.effective_model,
			requested_reasoning_effort = excluded.requested_reasoning_effort,
			effective_reasoning_effort = excluded.effective_reasoning_effort,
			requested_permission_mode = excluded.requested_permission_mode,
			effective_permission_mode = excluded.effective_permission_mode,
			status = excluded.status,
			updated_at = excluded.updated_at,
			closed_at = excluded.closed_at
	`, record.AgentID, record.RootSessionID, nullAgentString(record.ParentAgentID), nullAgentString(record.ParentSessionID),
		nullAgentString(record.SessionID), record.AgentPath, record.Depth, nullAgentString(record.AgentType),
		nullAgentString(record.Nickname), nullAgentString(record.Workflow), nullAgentString(record.TeamID),
		nullAgentString(record.TeammateID), nullAgentString(record.Provider), nullAgentString(record.Model),
		nullAgentString(record.ReasoningEffort), nullAgentString(record.Difficulty), nullAgentString(record.DifficultySource),
		nullAgentString(record.DifficultyRationale), nullAgentString(record.RouteSource), routeWarningsJSON(record.RouteWarnings),
		record.FallbackUsed, nullAgentString(record.FallbackReason),
		nullAgentString(record.RequestedProvider), nullAgentString(record.EffectiveProvider),
		nullAgentString(record.RequestedModel), nullAgentString(record.EffectiveModel),
		nullAgentString(record.RequestedReasoningEffort), nullAgentString(record.EffectiveReasoningEffort),
		nullAgentString(record.RequestedPermissionMode), nullAgentString(record.EffectivePermissionMode),
		record.Status, formatAgentTime(record.CreatedAt), formatAgentTime(record.UpdatedAt),
		nullableAgentTime(record.ClosedAt))
	if err != nil {
		return AgentRecord{}, fmt.Errorf("upsert agent control agent: %w", err)
	}
	stored, err := s.getAgentControlAgentByID(ctx, record.AgentID)
	if err != nil {
		return AgentRecord{}, err
	}
	wake, err := s.appendAgentWakeEvent(ctx, stored, "upsert")
	if err != nil {
		return AgentRecord{}, err
	}
	s.notifyAgentWake(wake)
	return stored, nil
}

// ReserveAgentControlAgentSpawn atomically upserts the root row, checks the
// active child count, and creates the child row. This is the registry-first
// cross-process guard for spawn_agent thread limits.
func (s *SQLiteGlobalAgentRegistryStore) ReserveAgentControlAgentSpawn(ctx context.Context, root AgentRecord, child AgentRecord, maxThreads int) (AgentRecord, error) {
	if s == nil {
		return AgentRecord{}, fmt.Errorf("agent control agent registry store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return AgentRecord{}, err
	}
	root = root.Normalize()
	child = child.Normalize()
	if root.AgentID == "" {
		return AgentRecord{}, fmt.Errorf("root agent id is required")
	}
	if root.RootSessionID == "" {
		return AgentRecord{}, fmt.Errorf("root session id is required")
	}
	if root.AgentPath == "" {
		return AgentRecord{}, fmt.Errorf("root agent path is required")
	}
	if child.AgentID == "" {
		return AgentRecord{}, fmt.Errorf("child agent id is required")
	}
	if child.RootSessionID == "" {
		child.RootSessionID = root.RootSessionID
	}
	if child.RootSessionID != root.RootSessionID {
		return AgentRecord{}, fmt.Errorf("child root session id must match root")
	}
	if child.AgentPath == "" {
		return AgentRecord{}, fmt.Errorf("child agent path is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentRecord{}, fmt.Errorf("begin agent spawn reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := upsertAgentControlAgentTx(ctx, tx, root); err != nil {
		return AgentRecord{}, err
	}
	if maxThreads > 0 {
		var activeCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM agent_control_agents
			WHERE root_session_id = ?
				AND agent_path <> '/root'
				AND closed_at IS NULL
				AND status NOT IN (?, ?)
		`, root.RootSessionID, AgentStatusClosed, AgentStatusStale).Scan(&activeCount); err != nil {
			return AgentRecord{}, fmt.Errorf("count active agent registry children: %w", err)
		}
		if activeCount >= maxThreads {
			return AgentRecord{}, fmt.Errorf("agent spawn thread limit reached: max_threads=%d active_children=%d", maxThreads, activeCount)
		}
	}
	if _, err := upsertAgentControlAgentTx(ctx, tx, child); err != nil {
		return AgentRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentRecord{}, fmt.Errorf("commit agent spawn reservation: %w", err)
	}
	stored, err := s.getAgentControlAgentByID(ctx, child.AgentID)
	if err != nil {
		return AgentRecord{}, err
	}
	wake, err := s.appendAgentWakeEvent(ctx, stored, "spawn_reserved")
	if err != nil {
		return AgentRecord{}, err
	}
	s.notifyAgentWake(wake)
	return stored, nil
}

// ListAgentControlAgents returns durable AgentControl identity rows matching
// filter, ordered by the global agent registry row id.
func (s *SQLiteGlobalAgentRegistryStore) ListAgentControlAgents(ctx context.Context, filter AgentFilter) ([]AgentRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("agent control agent registry store is not initialized")
	}
	if skip, err := s.ensureForRead(); err != nil {
		return nil, err
	} else if skip {
		return nil, nil
	}
	filter = filter.Normalize()
	clauses, args := agentFilterClauses(filter)
	query := `
		SELECT id, agent_id, root_session_id, parent_agent_id, parent_session_id, session_id, agent_path, depth,
			agent_type, nickname, workflow, team_id, teammate_id, provider, model, reasoning_effort,
			difficulty, difficulty_source, difficulty_rationale, route_source, route_warnings_json,
			fallback_used, fallback_reason, requested_provider, effective_provider, requested_model,
			effective_model, requested_reasoning_effort, effective_reasoning_effort,
			requested_permission_mode, effective_permission_mode, status, created_at, updated_at, closed_at
		FROM agent_control_agents
	`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY id ASC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list agent control agents: %w", err)
	}
	defer rows.Close()

	records := make([]AgentRecord, 0)
	for rows.Next() {
		record, err := scanAgentRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record.Normalize())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func agentFilterClauses(filter AgentFilter) ([]string, []interface{}) {
	filter = filter.Normalize()
	clauses := make([]string, 0)
	args := make([]interface{}, 0)
	if filter.AgentID != "" {
		clauses = append(clauses, "agent_id = ?")
		args = append(args, filter.AgentID)
	}
	if filter.RootSessionID != "" {
		clauses = append(clauses, "root_session_id = ?")
		args = append(args, filter.RootSessionID)
	}
	if filter.ParentAgentID != "" {
		clauses = append(clauses, "parent_agent_id = ?")
		args = append(args, filter.ParentAgentID)
	}
	if filter.ParentSessionID != "" {
		clauses = append(clauses, "parent_session_id = ?")
		args = append(args, filter.ParentSessionID)
	}
	if filter.SessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, filter.SessionID)
	}
	if filter.AgentPath != "" {
		clauses = append(clauses, "agent_path = ?")
		args = append(args, filter.AgentPath)
	}
	if filter.PathPrefix != "" {
		pathPrefix := strings.TrimRight(filter.PathPrefix, "/")
		clauses = append(clauses, "(agent_path = ? OR agent_path LIKE ?)")
		args = append(args, pathPrefix, pathPrefix+"/%")
	}
	if filter.Workflow != "" {
		clauses = append(clauses, "workflow = ?")
		args = append(args, filter.Workflow)
	}
	if filter.TeamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID)
	}
	if filter.TeammateID != "" {
		clauses = append(clauses, "teammate_id = ?")
		args = append(args, filter.TeammateID)
	}
	if !filter.IncludeClosed {
		clauses = append(clauses, "closed_at IS NULL")
		clauses = append(clauses, "status NOT IN (?, ?)")
		args = append(args, AgentStatusClosed, AgentStatusStale)
	}
	if filter.AfterSeq > 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, filter.AfterSeq)
	}
	return clauses, args
}

// MarkAgentControlAgentSubtreeStale removes one abandoned path subtree from
// active routing while retaining a distinct diagnostic terminal state.
func (s *SQLiteGlobalAgentRegistryStore) MarkAgentControlAgentSubtreeStale(ctx context.Context, rootSessionID string, agentPath string, staleAt time.Time) (int64, error) {
	return s.markAgentControlAgentSubtreeTerminal(ctx, rootSessionID, agentPath, AgentStatusStale, "stale", staleAt)
}

func upsertAgentControlAgentTx(ctx context.Context, tx *sql.Tx, record AgentRecord) (AgentRecord, error) {
	if tx == nil {
		return AgentRecord{}, fmt.Errorf("agent control agent transaction is not initialized")
	}
	record = record.Normalize()
	if record.AgentID == "" {
		return AgentRecord{}, fmt.Errorf("agent id is required")
	}
	if record.RootSessionID == "" {
		return AgentRecord{}, fmt.Errorf("root session id is required")
	}
	if record.AgentPath == "" {
		return AgentRecord{}, fmt.Errorf("agent path is required")
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	_, err := tx.ExecContext(ctx, `
		INSERT INTO agent_control_agents (
			agent_id, root_session_id, parent_agent_id, parent_session_id, session_id, agent_path, depth,
			agent_type, nickname, workflow, team_id, teammate_id, provider, model, reasoning_effort,
			difficulty, difficulty_source, difficulty_rationale, route_source, route_warnings_json,
			fallback_used, fallback_reason, requested_provider, effective_provider, requested_model,
			effective_model, requested_reasoning_effort, effective_reasoning_effort,
			requested_permission_mode, effective_permission_mode, status, created_at, updated_at, closed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			root_session_id = excluded.root_session_id,
			parent_agent_id = excluded.parent_agent_id,
			parent_session_id = excluded.parent_session_id,
			session_id = excluded.session_id,
			agent_path = excluded.agent_path,
			depth = excluded.depth,
			agent_type = excluded.agent_type,
			nickname = excluded.nickname,
			workflow = excluded.workflow,
			team_id = excluded.team_id,
			teammate_id = excluded.teammate_id,
			provider = excluded.provider,
			model = excluded.model,
			reasoning_effort = excluded.reasoning_effort,
			difficulty = excluded.difficulty,
			difficulty_source = excluded.difficulty_source,
			difficulty_rationale = excluded.difficulty_rationale,
			route_source = excluded.route_source,
			route_warnings_json = excluded.route_warnings_json,
			fallback_used = excluded.fallback_used,
			fallback_reason = excluded.fallback_reason,
			requested_provider = excluded.requested_provider,
			effective_provider = excluded.effective_provider,
			requested_model = excluded.requested_model,
			effective_model = excluded.effective_model,
			requested_reasoning_effort = excluded.requested_reasoning_effort,
			effective_reasoning_effort = excluded.effective_reasoning_effort,
			requested_permission_mode = excluded.requested_permission_mode,
			effective_permission_mode = excluded.effective_permission_mode,
			status = excluded.status,
			updated_at = excluded.updated_at,
			closed_at = excluded.closed_at
		ON CONFLICT(root_session_id, agent_path) DO UPDATE SET
			agent_id = excluded.agent_id,
			parent_agent_id = excluded.parent_agent_id,
			parent_session_id = excluded.parent_session_id,
			session_id = excluded.session_id,
			depth = excluded.depth,
			agent_type = excluded.agent_type,
			nickname = excluded.nickname,
			workflow = excluded.workflow,
			team_id = excluded.team_id,
			teammate_id = excluded.teammate_id,
			provider = excluded.provider,
			model = excluded.model,
			reasoning_effort = excluded.reasoning_effort,
			difficulty = excluded.difficulty,
			difficulty_source = excluded.difficulty_source,
			difficulty_rationale = excluded.difficulty_rationale,
			route_source = excluded.route_source,
			route_warnings_json = excluded.route_warnings_json,
			fallback_used = excluded.fallback_used,
			fallback_reason = excluded.fallback_reason,
			requested_provider = excluded.requested_provider,
			effective_provider = excluded.effective_provider,
			requested_model = excluded.requested_model,
			effective_model = excluded.effective_model,
			requested_reasoning_effort = excluded.requested_reasoning_effort,
			effective_reasoning_effort = excluded.effective_reasoning_effort,
			requested_permission_mode = excluded.requested_permission_mode,
			effective_permission_mode = excluded.effective_permission_mode,
			status = excluded.status,
			updated_at = excluded.updated_at,
			closed_at = excluded.closed_at
	`, record.AgentID, record.RootSessionID, nullAgentString(record.ParentAgentID), nullAgentString(record.ParentSessionID),
		nullAgentString(record.SessionID), record.AgentPath, record.Depth, nullAgentString(record.AgentType),
		nullAgentString(record.Nickname), nullAgentString(record.Workflow), nullAgentString(record.TeamID),
		nullAgentString(record.TeammateID), nullAgentString(record.Provider), nullAgentString(record.Model),
		nullAgentString(record.ReasoningEffort), nullAgentString(record.Difficulty), nullAgentString(record.DifficultySource),
		nullAgentString(record.DifficultyRationale), nullAgentString(record.RouteSource), routeWarningsJSON(record.RouteWarnings),
		record.FallbackUsed, nullAgentString(record.FallbackReason),
		nullAgentString(record.RequestedProvider), nullAgentString(record.EffectiveProvider),
		nullAgentString(record.RequestedModel), nullAgentString(record.EffectiveModel),
		nullAgentString(record.RequestedReasoningEffort), nullAgentString(record.EffectiveReasoningEffort),
		nullAgentString(record.RequestedPermissionMode), nullAgentString(record.EffectivePermissionMode),
		record.Status, formatAgentTime(record.CreatedAt), formatAgentTime(record.UpdatedAt),
		nullableAgentTime(record.ClosedAt))
	if err != nil {
		return AgentRecord{}, fmt.Errorf("upsert agent control agent: %w", err)
	}
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record, nil
}

// CloseAgentControlAgentSubtree marks one path and all descendants closed.
func (s *SQLiteGlobalAgentRegistryStore) CloseAgentControlAgentSubtree(ctx context.Context, rootSessionID string, agentPath string, closedAt time.Time) (int64, error) {
	return s.markAgentControlAgentSubtreeTerminal(ctx, rootSessionID, agentPath, AgentStatusClosed, "closed", closedAt)
}

func (s *SQLiteGlobalAgentRegistryStore) markAgentControlAgentSubtreeTerminal(ctx context.Context, rootSessionID string, agentPath, status, eventKind string, terminalAt time.Time) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("agent control agent registry store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return 0, err
	}
	rootSessionID = strings.TrimSpace(rootSessionID)
	agentPath = normalizeAgentPath(agentPath)
	if rootSessionID == "" {
		return 0, fmt.Errorf("root session id is required")
	}
	if agentPath == "" {
		return 0, fmt.Errorf("agent path is required")
	}
	if terminalAt.IsZero() {
		terminalAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_control_agents
		SET status = ?, closed_at = ?, updated_at = ?
		WHERE root_session_id = ?
			AND (agent_path = ? OR agent_path LIKE ?)
			AND closed_at IS NULL
	`, status, formatAgentTime(terminalAt), formatAgentTime(time.Now().UTC()), rootSessionID, agentPath, strings.TrimRight(agentPath, "/")+"/%")
	if err != nil {
		return 0, fmt.Errorf("mark agent control agent subtree %s: %w", status, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read closed agent subtree count: %w", err)
	}
	if count > 0 {
		records, listErr := s.ListAgentControlAgents(ctx, AgentFilter{
			RootSessionID: rootSessionID,
			PathPrefix:    agentPath,
			IncludeClosed: true,
		})
		if listErr != nil {
			return 0, listErr
		}
		for _, record := range records {
			if !record.Closed() {
				continue
			}
			wake, wakeErr := s.appendAgentWakeEvent(ctx, record, eventKind)
			if wakeErr != nil {
				return 0, wakeErr
			}
			s.notifyAgentWake(wake)
		}
	}
	return count, nil
}

func (s *SQLiteGlobalAgentRegistryStore) getAgentControlAgentByID(ctx context.Context, agentID string) (AgentRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, agent_id, root_session_id, parent_agent_id, parent_session_id, session_id, agent_path, depth,
			agent_type, nickname, workflow, team_id, teammate_id, provider, model, reasoning_effort,
			difficulty, difficulty_source, difficulty_rationale, route_source, route_warnings_json,
			fallback_used, fallback_reason, requested_provider, effective_provider, requested_model,
			effective_model, requested_reasoning_effort, effective_reasoning_effort,
			requested_permission_mode, effective_permission_mode, status, created_at, updated_at, closed_at
		FROM agent_control_agents
		WHERE agent_id = ?
	`, strings.TrimSpace(agentID))
	record, err := scanAgentRecord(row)
	if err != nil {
		return AgentRecord{}, fmt.Errorf("read agent control agent: %w", err)
	}
	return record.Normalize(), nil
}

// WatchAgentControlAgentWake subscribes to in-process notifications for newly
// upserted or closed durable agent identity rows. Existing rows are consumed
// through LastAgentControlAgentWakeSeq plus ListAgentControlAgents(AfterSeq).
func (s *SQLiteGlobalAgentRegistryStore) WatchAgentControlAgentWake(ctx context.Context, filter AgentWakeFilter) (<-chan AgentWakeEvent, func()) {
	out := make(chan AgentWakeEvent, 1)
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return out, func() {}
	}
	filter = filter.Normalize()
	done := make(chan struct{})
	var once sync.Once
	s.agentWakeMu.Lock()
	if s.agentWakeWatchers == nil {
		s.agentWakeWatchers = make(map[int64]globalAgentWakeWatcher)
	}
	s.nextAgentWakeWatchID++
	watchID := s.nextAgentWakeWatchID
	unwatch := func() {
		once.Do(func() {
			s.agentWakeMu.Lock()
			delete(s.agentWakeWatchers, watchID)
			s.agentWakeMu.Unlock()
			close(done)
		})
	}
	s.agentWakeWatchers[watchID] = globalAgentWakeWatcher{
		filter:  filter,
		ch:      out,
		unwatch: unwatch,
	}
	s.agentWakeMu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			unwatch()
		case <-done:
		}
	}()
	return out, unwatch
}

// LastAgentControlAgentWakeSeq returns the durable agent wake event row-id
// high-water mark for the requested identity graph stream.
func (s *SQLiteGlobalAgentRegistryStore) LastAgentControlAgentWakeSeq(ctx context.Context, filter AgentWakeFilter) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("agent control agent registry store is not initialized")
	}
	if skip, err := s.ensureForRead(); err != nil {
		return 0, err
	} else if skip {
		return 0, nil
	}
	filter = filter.Normalize()
	clauses, args := agentWakeFilterClauses(filter)
	query := "SELECT COALESCE(MAX(id), 0) FROM agent_control_agent_wake_events"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	var seq int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&seq); err != nil {
		return 0, fmt.Errorf("last agent control agent wake sequence: %w", err)
	}
	return seq, nil
}

func (s *SQLiteGlobalAgentRegistryStore) appendAgentWakeEvent(ctx context.Context, record AgentRecord, eventKind string) (AgentWakeEvent, error) {
	if s == nil {
		return AgentWakeEvent{}, fmt.Errorf("agent control agent registry store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return AgentWakeEvent{}, err
	}
	event := agentRecordWakeEvent(record, eventKind)
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_control_agent_wake_events (
			agent_row_id, agent_id, root_session_id, parent_agent_id, parent_session_id,
			session_id, agent_path, depth, agent_type, workflow, team_id, teammate_id,
			status, event_kind, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, nullInt64(event.Seq), event.AgentID, event.RootSessionID, nullAgentString(event.ParentAgentID),
		nullAgentString(event.ParentSessionID), nullAgentString(event.SessionID), event.AgentPath, event.Depth,
		nullAgentString(event.AgentType), nullAgentString(event.Workflow), nullAgentString(event.TeamID),
		nullAgentString(event.TeammateID), nullAgentString(event.Status), nullAgentString(event.EventKind),
		formatAgentTime(event.CreatedAt))
	if err != nil {
		return AgentWakeEvent{}, fmt.Errorf("append agent control agent wake event: %w", err)
	}
	seq, err := result.LastInsertId()
	if err != nil {
		return AgentWakeEvent{}, fmt.Errorf("read agent control agent wake sequence: %w", err)
	}
	event.Seq = seq
	return event.Normalize(), nil
}

func (s *SQLiteGlobalAgentRegistryStore) notifyAgentWake(event AgentWakeEvent) {
	event = event.Normalize()
	if event.Seq <= 0 {
		return
	}
	s.agentWakeMu.Lock()
	defer s.agentWakeMu.Unlock()
	for _, watcher := range s.agentWakeWatchers {
		if !agentWakeMatchesFilter(event, watcher.filter) {
			continue
		}
		select {
		case watcher.ch <- event:
		default:
		}
	}
}

func agentRecordWakeEvent(record AgentRecord, eventKind string) AgentWakeEvent {
	record = record.Normalize()
	return AgentWakeEvent{
		Seq:             record.Seq,
		AgentID:         record.AgentID,
		RootSessionID:   record.RootSessionID,
		ParentAgentID:   record.ParentAgentID,
		ParentSessionID: record.ParentSessionID,
		SessionID:       record.SessionID,
		AgentPath:       record.AgentPath,
		Depth:           record.Depth,
		AgentType:       record.AgentType,
		Workflow:        record.Workflow,
		TeamID:          record.TeamID,
		TeammateID:      record.TeammateID,
		Status:          record.Status,
		EventKind:       eventKind,
		CreatedAt:       time.Now().UTC(),
	}
}

func agentWakeMatchesFilter(event AgentWakeEvent, filter AgentWakeFilter) bool {
	event = event.Normalize()
	filter = filter.Normalize()
	if filter.RootSessionID != "" && !strings.EqualFold(event.RootSessionID, filter.RootSessionID) {
		return false
	}
	if filter.ParentAgentID != "" && !strings.EqualFold(event.ParentAgentID, filter.ParentAgentID) {
		return false
	}
	if filter.SessionID != "" && !strings.EqualFold(event.SessionID, filter.SessionID) {
		return false
	}
	if filter.AgentPath != "" && !strings.EqualFold(event.AgentPath, filter.AgentPath) {
		return false
	}
	if filter.PathPrefix != "" && !agentPathMatchesPrefix(event.AgentPath, filter.PathPrefix) {
		return false
	}
	if filter.Workflow != "" && !strings.EqualFold(event.Workflow, filter.Workflow) {
		return false
	}
	if filter.TeamID != "" && !strings.EqualFold(event.TeamID, filter.TeamID) {
		return false
	}
	if filter.TeammateID != "" && !strings.EqualFold(event.TeammateID, filter.TeammateID) {
		return false
	}
	return true
}

func agentWakeFilterClauses(filter AgentWakeFilter) ([]string, []interface{}) {
	filter = filter.Normalize()
	clauses := make([]string, 0)
	args := make([]interface{}, 0)
	if filter.RootSessionID != "" {
		clauses = append(clauses, "root_session_id = ?")
		args = append(args, filter.RootSessionID)
	}
	if filter.ParentAgentID != "" {
		clauses = append(clauses, "parent_agent_id = ?")
		args = append(args, filter.ParentAgentID)
	}
	if filter.SessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, filter.SessionID)
	}
	if filter.AgentPath != "" {
		clauses = append(clauses, "agent_path = ?")
		args = append(args, filter.AgentPath)
	}
	if filter.PathPrefix != "" {
		pathPrefix := strings.TrimRight(filter.PathPrefix, "/")
		clauses = append(clauses, "(agent_path = ? OR agent_path LIKE ?)")
		args = append(args, pathPrefix, pathPrefix+"/%")
	}
	if filter.Workflow != "" {
		clauses = append(clauses, "workflow = ?")
		args = append(args, filter.Workflow)
	}
	if filter.TeamID != "" {
		clauses = append(clauses, "team_id = ?")
		args = append(args, filter.TeamID)
	}
	if filter.TeammateID != "" {
		clauses = append(clauses, "teammate_id = ?")
		args = append(args, filter.TeammateID)
	}
	return clauses, args
}

func agentPathMatchesPrefix(path string, prefix string) bool {
	return AgentPathMatchesPrefix(path, prefix)
}

func (s *SQLiteGlobalAgentRegistryStore) init(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("agent control agent registry store is not initialized")
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS agent_control_agents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL UNIQUE,
			root_session_id TEXT NOT NULL,
			parent_agent_id TEXT,
			parent_session_id TEXT,
			session_id TEXT,
			agent_path TEXT NOT NULL,
			depth INTEGER NOT NULL DEFAULT 0,
			agent_type TEXT,
			nickname TEXT,
			workflow TEXT,
			team_id TEXT,
			teammate_id TEXT,
			provider TEXT,
			model TEXT,
			reasoning_effort TEXT,
			difficulty TEXT,
			difficulty_source TEXT,
			difficulty_rationale TEXT,
			route_source TEXT,
			route_warnings_json TEXT NOT NULL DEFAULT '[]',
			fallback_used INTEGER NOT NULL DEFAULT 0,
			fallback_reason TEXT,
			requested_provider TEXT,
			effective_provider TEXT,
			requested_model TEXT,
			effective_model TEXT,
			requested_reasoning_effort TEXT,
			effective_reasoning_effort TEXT,
			requested_permission_mode TEXT,
			effective_permission_mode TEXT,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			UNIQUE(root_session_id, agent_path)
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_control_agents_active_session
			ON agent_control_agents(session_id)
			WHERE session_id IS NOT NULL AND session_id <> '' AND closed_at IS NULL;`,
		`CREATE INDEX IF NOT EXISTS idx_agent_control_agents_root_path ON agent_control_agents(root_session_id, agent_path);`,
		`CREATE INDEX IF NOT EXISTS idx_agent_control_agents_parent ON agent_control_agents(parent_agent_id);`,
		`CREATE INDEX IF NOT EXISTS idx_agent_control_agents_team ON agent_control_agents(workflow, team_id, teammate_id);`,
		`CREATE TABLE IF NOT EXISTS agent_control_agent_wake_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_row_id INTEGER,
			agent_id TEXT NOT NULL,
			root_session_id TEXT NOT NULL,
			parent_agent_id TEXT,
			parent_session_id TEXT,
			session_id TEXT,
			agent_path TEXT NOT NULL,
			depth INTEGER NOT NULL DEFAULT 0,
			agent_type TEXT,
			workflow TEXT,
			team_id TEXT,
			teammate_id TEXT,
			status TEXT,
			event_kind TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_agent_control_agent_wake_root_path
			ON agent_control_agent_wake_events(root_session_id, agent_path, id);`,
		`CREATE INDEX IF NOT EXISTS idx_agent_control_agent_wake_session
			ON agent_control_agent_wake_events(session_id, id);`,
		`CREATE INDEX IF NOT EXISTS idx_agent_control_agent_wake_team
			ON agent_control_agent_wake_events(workflow, team_id, teammate_id, id);`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize agent control agent registry: %w", err)
		}
	}
	if err := s.ensureAgentControlAgentRouteColumns(ctx); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteGlobalAgentRegistryStore) ensureAgentControlAgentRouteColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(agent_control_agents)")
	if err != nil {
		return fmt.Errorf("inspect agent control agent columns: %w", err)
	}
	defer rows.Close()
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("scan agent control agent column: %w", err)
		}
		existing[strings.TrimSpace(name)] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	columns := []struct {
		Name string
		SQL  string
	}{
		{Name: "provider", SQL: "ALTER TABLE agent_control_agents ADD COLUMN provider TEXT"},
		{Name: "model", SQL: "ALTER TABLE agent_control_agents ADD COLUMN model TEXT"},
		{Name: "reasoning_effort", SQL: "ALTER TABLE agent_control_agents ADD COLUMN reasoning_effort TEXT"},
		{Name: "difficulty", SQL: "ALTER TABLE agent_control_agents ADD COLUMN difficulty TEXT"},
		{Name: "difficulty_source", SQL: "ALTER TABLE agent_control_agents ADD COLUMN difficulty_source TEXT"},
		{Name: "difficulty_rationale", SQL: "ALTER TABLE agent_control_agents ADD COLUMN difficulty_rationale TEXT"},
		{Name: "route_source", SQL: "ALTER TABLE agent_control_agents ADD COLUMN route_source TEXT"},
		{Name: "route_warnings_json", SQL: "ALTER TABLE agent_control_agents ADD COLUMN route_warnings_json TEXT NOT NULL DEFAULT '[]'"},
		{Name: "fallback_used", SQL: "ALTER TABLE agent_control_agents ADD COLUMN fallback_used INTEGER NOT NULL DEFAULT 0"},
		{Name: "fallback_reason", SQL: "ALTER TABLE agent_control_agents ADD COLUMN fallback_reason TEXT"},
		{Name: "requested_provider", SQL: "ALTER TABLE agent_control_agents ADD COLUMN requested_provider TEXT"},
		{Name: "effective_provider", SQL: "ALTER TABLE agent_control_agents ADD COLUMN effective_provider TEXT"},
		{Name: "requested_model", SQL: "ALTER TABLE agent_control_agents ADD COLUMN requested_model TEXT"},
		{Name: "effective_model", SQL: "ALTER TABLE agent_control_agents ADD COLUMN effective_model TEXT"},
		{Name: "requested_reasoning_effort", SQL: "ALTER TABLE agent_control_agents ADD COLUMN requested_reasoning_effort TEXT"},
		{Name: "effective_reasoning_effort", SQL: "ALTER TABLE agent_control_agents ADD COLUMN effective_reasoning_effort TEXT"},
		{Name: "requested_permission_mode", SQL: "ALTER TABLE agent_control_agents ADD COLUMN requested_permission_mode TEXT"},
		{Name: "effective_permission_mode", SQL: "ALTER TABLE agent_control_agents ADD COLUMN effective_permission_mode TEXT"},
	}
	for _, column := range columns {
		if existing[column.Name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, column.SQL); err != nil {
			return fmt.Errorf("add agent control agent column %s: %w", column.Name, err)
		}
	}
	return nil
}

type agentRecordScanner interface {
	Scan(dest ...interface{}) error
}

func scanAgentRecord(scanner agentRecordScanner) (AgentRecord, error) {
	var (
		record                   AgentRecord
		parentAgentID            sql.NullString
		parentSessionID          sql.NullString
		sessionID                sql.NullString
		agentType                sql.NullString
		nickname                 sql.NullString
		workflow                 sql.NullString
		teamID                   sql.NullString
		teammateID               sql.NullString
		provider                 sql.NullString
		model                    sql.NullString
		reasoningEffort          sql.NullString
		difficulty               sql.NullString
		difficultySource         sql.NullString
		difficultyRationale      sql.NullString
		routeSource              sql.NullString
		routeWarningsRaw         string
		fallbackUsed             int
		fallbackReason           sql.NullString
		requestedProvider        sql.NullString
		effectiveProvider        sql.NullString
		requestedModel           sql.NullString
		effectiveModel           sql.NullString
		requestedReasoningEffort sql.NullString
		effectiveReasoningEffort sql.NullString
		requestedPermissionMode  sql.NullString
		effectivePermissionMode  sql.NullString
		createdRaw               string
		updatedRaw               string
		closedRaw                sql.NullString
	)
	if err := scanner.Scan(&record.Seq, &record.AgentID, &record.RootSessionID, &parentAgentID, &parentSessionID,
		&sessionID, &record.AgentPath, &record.Depth, &agentType, &nickname, &workflow, &teamID,
		&teammateID, &provider, &model, &reasoningEffort, &difficulty, &difficultySource,
		&difficultyRationale, &routeSource, &routeWarningsRaw, &fallbackUsed, &fallbackReason,
		&requestedProvider, &effectiveProvider, &requestedModel, &effectiveModel,
		&requestedReasoningEffort, &effectiveReasoningEffort,
		&requestedPermissionMode, &effectivePermissionMode,
		&record.Status, &createdRaw, &updatedRaw, &closedRaw); err != nil {
		return AgentRecord{}, err
	}
	record.ParentAgentID = parentAgentID.String
	record.ParentSessionID = parentSessionID.String
	record.SessionID = sessionID.String
	record.AgentType = agentType.String
	record.Nickname = nickname.String
	record.Workflow = workflow.String
	record.TeamID = teamID.String
	record.TeammateID = teammateID.String
	record.Provider = provider.String
	record.Model = model.String
	record.ReasoningEffort = reasoningEffort.String
	record.Difficulty = difficulty.String
	record.DifficultySource = difficultySource.String
	record.DifficultyRationale = difficultyRationale.String
	record.RouteSource = routeSource.String
	record.RouteWarnings = parseRouteWarningsJSON(routeWarningsRaw)
	record.FallbackUsed = fallbackUsed != 0
	record.FallbackReason = fallbackReason.String
	record.RequestedProvider = requestedProvider.String
	record.EffectiveProvider = effectiveProvider.String
	record.RequestedModel = requestedModel.String
	record.EffectiveModel = effectiveModel.String
	record.RequestedReasoningEffort = requestedReasoningEffort.String
	record.EffectiveReasoningEffort = effectiveReasoningEffort.String
	record.RequestedPermissionMode = requestedPermissionMode.String
	record.EffectivePermissionMode = effectivePermissionMode.String
	if createdRaw != "" {
		record.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
	}
	if updatedRaw != "" {
		record.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
	}
	if closedRaw.Valid && strings.TrimSpace(closedRaw.String) != "" {
		closedAt, _ := time.Parse(time.RFC3339Nano, closedRaw.String)
		record.ClosedAt = &closedAt
	}
	return record, nil
}

func resolveLazyGlobalAgentDSN(cfg *GlobalAgentStoreConfig) (string, string, error) {
	if cfg == nil {
		cfg = &GlobalAgentStoreConfig{}
	}
	if path := strings.TrimSpace(cfg.Path); path != "" {
		return ensureGlobalMailboxDSNOptions(path), path, nil
	}
	if dsn := strings.TrimSpace(cfg.DSN); dsn != "" {
		return ensureGlobalMailboxDSNOptions(dsn), "", nil
	}
	return ensureGlobalMailboxDSNOptions(fmt.Sprintf("file:agent-control-agent-registry-%d?mode=memory&cache=shared", time.Now().UnixNano())), "", nil
}

// resolveGlobalAgentDSN keeps the historical helper for callers/tests that only
// need a DSN. Directory creation is deferred until ensure().
func resolveGlobalAgentDSN(cfg *GlobalAgentStoreConfig) (string, error) {
	dsn, _, err := resolveLazyGlobalAgentDSN(cfg)
	return dsn, err
}

func nullAgentString(value string) interface{} {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func routeWarningsJSON(values []string) string {
	values = trimAgentRecordStrings(values)
	if len(values) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func parseRouteWarningsJSON(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return trimAgentRecordStrings(values)
}

func nullInt64(value int64) interface{} {
	if value <= 0 {
		return nil
	}
	return value
}

func formatAgentTime(value time.Time) string {
	if value.IsZero() {
		value = time.Now().UTC()
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableAgentTime(value *time.Time) interface{} {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
