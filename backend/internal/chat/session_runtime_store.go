package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	runtimeevents "github.com/ai-gateway/ai-agent-runtime/internal/events"
	"github.com/ai-gateway/ai-agent-runtime/internal/migrate"
	"github.com/ai-gateway/ai-agent-runtime/internal/team"
	"github.com/google/uuid"

	_ "github.com/mattn/go-sqlite3"
)

// RuntimeStateStore persists per-session runtime state.
type RuntimeStateStore interface {
	LoadState(ctx context.Context, sessionID string) (*RuntimeState, error)
	SaveState(ctx context.Context, state *RuntimeState) error
	DeleteState(ctx context.Context, sessionID string) error
}

// ToolReceiptStore persists replayable tool results across crashes.
type ToolReceiptStore interface {
	SaveToolReceipt(ctx context.Context, receipt ToolExecutionReceipt) error
	GetToolReceipt(ctx context.Context, sessionID, toolCallID string) (*ToolExecutionReceipt, error)
	DeleteToolReceipt(ctx context.Context, sessionID, toolCallID string) error
	ListToolReceipts(ctx context.Context, sessionID string, limit int) ([]ToolExecutionReceipt, error)
}

// EventStore persists runtime events for a session.
type EventStore interface {
	AppendEvent(ctx context.Context, event runtimeevents.Event) (int64, error)
	ListEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]runtimeevents.Event, error)
}

// RuntimeStoreConfig configures the sqlite-backed runtime store.
type RuntimeStoreConfig struct {
	Path string
	DSN  string
}

// InMemoryRuntimeStore stores state and events in memory.
type InMemoryRuntimeStore struct {
	mu        sync.RWMutex
	states    map[string]*RuntimeState
	events    map[string][]storedEvent
	receipts  map[string]map[string]ToolExecutionReceipt
	seq       map[string]int64
	retention int
}

type storedEvent struct {
	Seq   int64
	Event runtimeevents.Event
}

// NewInMemoryRuntimeStore creates a memory-backed runtime store.
func NewInMemoryRuntimeStore(retention int) *InMemoryRuntimeStore {
	if retention < 0 {
		retention = 0
	}
	return &InMemoryRuntimeStore{
		states:    make(map[string]*RuntimeState),
		events:    make(map[string][]storedEvent),
		receipts:  make(map[string]map[string]ToolExecutionReceipt),
		seq:       make(map[string]int64),
		retention: retention,
	}
}

// LoadState returns the runtime state for a session.
func (s *InMemoryRuntimeStore) LoadState(ctx context.Context, sessionID string) (*RuntimeState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.states[sessionID]
	if state == nil {
		return nil, nil
	}
	return state.Clone(), nil
}

// SaveState persists the runtime state.
func (s *InMemoryRuntimeStore) SaveState(ctx context.Context, state *RuntimeState) error {
	if state == nil || strings.TrimSpace(state.SessionID) == "" {
		return fmt.Errorf("runtime state requires session id")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cloned := state.Clone()
	if cloned.UpdatedAt.IsZero() {
		cloned.UpdatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[state.SessionID] = cloned
	return nil
}

// DeleteState removes a session runtime state.
func (s *InMemoryRuntimeStore) DeleteState(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, sessionID)
	delete(s.events, sessionID)
	delete(s.receipts, sessionID)
	delete(s.seq, sessionID)
	return nil
}

// SaveToolReceipt persists a replayable tool result.
func (s *InMemoryRuntimeStore) SaveToolReceipt(ctx context.Context, receipt ToolExecutionReceipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	receipt.SessionID = strings.TrimSpace(receipt.SessionID)
	receipt.ToolCallID = strings.TrimSpace(receipt.ToolCallID)
	if receipt.SessionID == "" || receipt.ToolCallID == "" {
		return fmt.Errorf("tool receipt requires session id and tool call id")
	}
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	}
	if len(receipt.MessageJSON) > 0 {
		receipt.MessageJSON = append(json.RawMessage(nil), receipt.MessageJSON...)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receipts[receipt.SessionID] == nil {
		s.receipts[receipt.SessionID] = make(map[string]ToolExecutionReceipt)
	}
	s.receipts[receipt.SessionID][receipt.ToolCallID] = receipt
	return nil
}

// GetToolReceipt loads a persisted tool receipt.
func (s *InMemoryRuntimeStore) GetToolReceipt(ctx context.Context, sessionID, toolCallID string) (*ToolExecutionReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	toolCallID = strings.TrimSpace(toolCallID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	bySession := s.receipts[sessionID]
	if bySession == nil {
		return nil, nil
	}
	receipt, ok := bySession[toolCallID]
	if !ok {
		return nil, nil
	}
	if len(receipt.MessageJSON) > 0 {
		receipt.MessageJSON = append(json.RawMessage(nil), receipt.MessageJSON...)
	}
	return &receipt, nil
}

// DeleteToolReceipt removes a persisted tool receipt.
func (s *InMemoryRuntimeStore) DeleteToolReceipt(ctx context.Context, sessionID, toolCallID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	toolCallID = strings.TrimSpace(toolCallID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if bySession := s.receipts[sessionID]; bySession != nil {
		delete(bySession, toolCallID)
		if len(bySession) == 0 {
			delete(s.receipts, sessionID)
		}
	}
	return nil
}

// ListToolReceipts returns stored tool receipts for a session.
func (s *InMemoryRuntimeStore) ListToolReceipts(ctx context.Context, sessionID string, limit int) ([]ToolExecutionReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	bySession := s.receipts[sessionID]
	if len(bySession) == 0 {
		return nil, nil
	}
	results := make([]ToolExecutionReceipt, 0, len(bySession))
	for _, receipt := range bySession {
		if len(receipt.MessageJSON) > 0 {
			receipt.MessageJSON = append(json.RawMessage(nil), receipt.MessageJSON...)
		}
		results = append(results, receipt)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].CreatedAt.Equal(results[j].CreatedAt) {
			return results[i].ToolCallID < results[j].ToolCallID
		}
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// AppendEvent stores a runtime event and returns its sequence.
func (s *InMemoryRuntimeStore) AppendEvent(ctx context.Context, event runtimeevents.Event) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(event.SessionID) == "" {
		return 0, fmt.Errorf("event requires session id")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq[event.SessionID]++
	seq := s.seq[event.SessionID]
	entry := storedEvent{Seq: seq, Event: cloneRuntimeEvent(event)}
	list := append(s.events[event.SessionID], entry)
	if s.retention > 0 && len(list) > s.retention {
		list = list[len(list)-s.retention:]
	}
	s.events[event.SessionID] = list
	return seq, nil
}

// ListEvents returns events after a given sequence.
func (s *InMemoryRuntimeStore) ListEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]runtimeevents.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.events[sessionID]
	if len(list) == 0 {
		return nil, nil
	}
	result := make([]runtimeevents.Event, 0, len(list))
	for _, entry := range list {
		if entry.Seq <= afterSeq {
			continue
		}
		event := cloneRuntimeEvent(entry.Event)
		if event.Payload == nil {
			event.Payload = map[string]interface{}{}
		}
		event.Payload["seq"] = entry.Seq
		result = append(result, event)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// SQLiteRuntimeStore persists runtime data in sqlite.
type SQLiteRuntimeStore struct {
	db *sql.DB
}

// NewSQLiteRuntimeStore opens a sqlite-backed runtime store.
func NewSQLiteRuntimeStore(cfg *RuntimeStoreConfig) (*SQLiteRuntimeStore, error) {
	if cfg == nil {
		cfg = &RuntimeStoreConfig{}
	}
	dsn, err := resolveRuntimeDSN(cfg)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open runtime db: %w", err)
	}
	store := &SQLiteRuntimeStore{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the underlying database.
func (s *SQLiteRuntimeStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// LoadState loads runtime state for a session.
func (s *SQLiteRuntimeStore) LoadState(ctx context.Context, sessionID string) (*RuntimeState, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT session_id, status, current_turn_id, current_checkpoint_id, current_run_meta_json, ambient_run_meta_json,
		       pending_tool_json, pending_approval_json, pending_question_json, head_offset, active_job_ids_json, updated_at
		FROM session_runtime_state
		WHERE session_id = ?
	`, sessionID)

	var (
		state                RuntimeState
		statusRaw            string
		currentRunMetaRaw    sql.NullString
		ambientRunMetaRaw    sql.NullString
		pendingToolRaw       sql.NullString
		pendingApprovalRaw   sql.NullString
		pendingQuestionRaw   sql.NullString
		activeJobsRaw        sql.NullString
		updatedAtRaw         string
		currentTurnIDRaw     sql.NullString
		currentCheckpointRaw sql.NullString
	)
	if err := row.Scan(
		&state.SessionID,
		&statusRaw,
		&currentTurnIDRaw,
		&currentCheckpointRaw,
		&currentRunMetaRaw,
		&ambientRunMetaRaw,
		&pendingToolRaw,
		&pendingApprovalRaw,
		&pendingQuestionRaw,
		&state.HeadOffset,
		&activeJobsRaw,
		&updatedAtRaw,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load runtime state: %w", err)
	}
	state.Status = SessionStatus(statusRaw)
	if currentTurnIDRaw.Valid {
		state.CurrentTurnID = currentTurnIDRaw.String
	}
	if currentCheckpointRaw.Valid {
		state.CurrentCheckpointID = currentCheckpointRaw.String
	}
	if currentRunMetaRaw.Valid && strings.TrimSpace(currentRunMetaRaw.String) != "" {
		var runMeta team.RunMeta
		if err := json.Unmarshal([]byte(currentRunMetaRaw.String), &runMeta); err == nil {
			state.CurrentRunMeta = runMeta.Clone()
		}
	}
	if ambientRunMetaRaw.Valid && strings.TrimSpace(ambientRunMetaRaw.String) != "" {
		var runMeta team.RunMeta
		if err := json.Unmarshal([]byte(ambientRunMetaRaw.String), &runMeta); err == nil {
			state.AmbientRunMeta = runMeta.Clone()
		}
	}
	if pendingToolRaw.Valid && strings.TrimSpace(pendingToolRaw.String) != "" {
		var pendingTool PendingToolInvocation
		if err := json.Unmarshal([]byte(pendingToolRaw.String), &pendingTool); err == nil {
			state.PendingTool = &pendingTool
		}
	}
	if pendingApprovalRaw.Valid && strings.TrimSpace(pendingApprovalRaw.String) != "" {
		var approval ApprovalRequest
		if err := json.Unmarshal([]byte(pendingApprovalRaw.String), &approval); err == nil {
			state.PendingApproval = &approval
		}
	}
	if pendingQuestionRaw.Valid && strings.TrimSpace(pendingQuestionRaw.String) != "" {
		var question UserQuestionRequest
		if err := json.Unmarshal([]byte(pendingQuestionRaw.String), &question); err == nil {
			state.PendingQuestion = &question
		}
	}
	if activeJobsRaw.Valid && strings.TrimSpace(activeJobsRaw.String) != "" {
		_ = json.Unmarshal([]byte(activeJobsRaw.String), &state.ActiveJobIDs)
	}
	if updatedAtRaw != "" {
		state.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtRaw)
	}
	return &state, nil
}

// SaveState upserts the runtime state.
func (s *SQLiteRuntimeStore) SaveState(ctx context.Context, state *RuntimeState) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runtime store is not initialized")
	}
	if state == nil || strings.TrimSpace(state.SessionID) == "" {
		return fmt.Errorf("runtime state requires session id")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	currentRunMetaJSON := ""
	if state.CurrentRunMeta != nil {
		payload, err := json.Marshal(state.CurrentRunMeta)
		if err != nil {
			return fmt.Errorf("marshal current run meta: %w", err)
		}
		currentRunMetaJSON = string(payload)
	}
	ambientRunMetaJSON := ""
	if state.AmbientRunMeta != nil {
		payload, err := json.Marshal(state.AmbientRunMeta)
		if err != nil {
			return fmt.Errorf("marshal ambient run meta: %w", err)
		}
		ambientRunMetaJSON = string(payload)
	}
	pendingToolJSON := ""
	if state.PendingTool != nil {
		payload, err := json.Marshal(state.PendingTool)
		if err != nil {
			return fmt.Errorf("marshal pending tool: %w", err)
		}
		pendingToolJSON = string(payload)
	}
	pendingApprovalJSON := ""
	if state.PendingApproval != nil {
		payload, err := json.Marshal(state.PendingApproval)
		if err != nil {
			return fmt.Errorf("marshal pending approval: %w", err)
		}
		pendingApprovalJSON = string(payload)
	}
	pendingQuestionJSON := ""
	if state.PendingQuestion != nil {
		payload, err := json.Marshal(state.PendingQuestion)
		if err != nil {
			return fmt.Errorf("marshal pending question: %w", err)
		}
		pendingQuestionJSON = string(payload)
	}
	activeJobsJSON := "[]"
	if len(state.ActiveJobIDs) > 0 {
		payload, err := json.Marshal(state.ActiveJobIDs)
		if err != nil {
			return fmt.Errorf("marshal active job ids: %w", err)
		}
		activeJobsJSON = string(payload)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_runtime_state (
			session_id, status, current_turn_id, current_checkpoint_id, current_run_meta_json, ambient_run_meta_json, pending_tool_json, pending_approval_json,
			pending_question_json, head_offset, active_job_ids_json, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			status = excluded.status,
			current_turn_id = excluded.current_turn_id,
			current_checkpoint_id = excluded.current_checkpoint_id,
			current_run_meta_json = excluded.current_run_meta_json,
			ambient_run_meta_json = excluded.ambient_run_meta_json,
			pending_tool_json = excluded.pending_tool_json,
			pending_approval_json = excluded.pending_approval_json,
			pending_question_json = excluded.pending_question_json,
			head_offset = excluded.head_offset,
			active_job_ids_json = excluded.active_job_ids_json,
			updated_at = excluded.updated_at
	`, state.SessionID, string(state.Status), nullIfEmpty(state.CurrentTurnID), nullIfEmpty(state.CurrentCheckpointID),
		nullIfEmpty(currentRunMetaJSON), nullIfEmpty(ambientRunMetaJSON), nullIfEmpty(pendingToolJSON), pendingApprovalJSON, pendingQuestionJSON, state.HeadOffset, activeJobsJSON, state.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save runtime state: %w", err)
	}
	return nil
}

// DeleteState removes runtime state for a session.
func (s *SQLiteRuntimeStore) DeleteState(ctx context.Context, sessionID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runtime store is not initialized")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM session_tool_receipts WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("delete tool receipts: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM session_runtime_state WHERE session_id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("delete runtime state: %w", err)
	}
	return nil
}

// SaveToolReceipt persists a replayable tool result.
func (s *SQLiteRuntimeStore) SaveToolReceipt(ctx context.Context, receipt ToolExecutionReceipt) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runtime store is not initialized")
	}
	receipt.SessionID = strings.TrimSpace(receipt.SessionID)
	receipt.ToolCallID = strings.TrimSpace(receipt.ToolCallID)
	if receipt.SessionID == "" || receipt.ToolCallID == "" {
		return fmt.Errorf("tool receipt requires session id and tool call id")
	}
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	}
	createdAt := receipt.CreatedAt.UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_tool_receipts (session_id, tool_call_id, tool_name, message_json, created_at, created_at_unix_nano)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, tool_call_id) DO UPDATE SET
			tool_name = excluded.tool_name,
			message_json = excluded.message_json,
			created_at = excluded.created_at,
			created_at_unix_nano = excluded.created_at_unix_nano
	`, receipt.SessionID, receipt.ToolCallID, nullIfEmpty(strings.TrimSpace(receipt.ToolName)), string(receipt.MessageJSON), createdAt.Format(time.RFC3339Nano), createdAt.UnixNano())
	if err != nil {
		return fmt.Errorf("save tool receipt: %w", err)
	}
	return nil
}

// GetToolReceipt loads a persisted tool receipt.
func (s *SQLiteRuntimeStore) GetToolReceipt(ctx context.Context, sessionID, toolCallID string) (*ToolExecutionReceipt, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT session_id, tool_call_id, tool_name, message_json, created_at, created_at_unix_nano
		FROM session_tool_receipts
		WHERE session_id = ? AND tool_call_id = ?
	`, strings.TrimSpace(sessionID), strings.TrimSpace(toolCallID))
	var (
		receipt           ToolExecutionReceipt
		toolName          sql.NullString
		messageJSON       string
		createdAtRaw      string
		createdAtUnixNano sql.NullInt64
	)
	if err := row.Scan(&receipt.SessionID, &receipt.ToolCallID, &toolName, &messageJSON, &createdAtRaw, &createdAtUnixNano); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load tool receipt: %w", err)
	}
	if toolName.Valid {
		receipt.ToolName = toolName.String
	}
	receipt.MessageJSON = json.RawMessage(messageJSON)
	receipt.CreatedAt = parseStoredUnixOrRFC3339Time(createdAtUnixNano, createdAtRaw)
	return &receipt, nil
}

// DeleteToolReceipt removes a persisted tool receipt.
func (s *SQLiteRuntimeStore) DeleteToolReceipt(ctx context.Context, sessionID, toolCallID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runtime store is not initialized")
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM session_tool_receipts WHERE session_id = ? AND tool_call_id = ?
	`, strings.TrimSpace(sessionID), strings.TrimSpace(toolCallID))
	if err != nil {
		return fmt.Errorf("delete tool receipt: %w", err)
	}
	return nil
}

// ListToolReceipts returns stored tool receipts for a session ordered by recency.
func (s *SQLiteRuntimeStore) ListToolReceipts(ctx context.Context, sessionID string, limit int) ([]ToolExecutionReceipt, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	query := `
		SELECT session_id, tool_call_id, tool_name, message_json, created_at, created_at_unix_nano
		FROM session_tool_receipts
		WHERE session_id = ?
		ORDER BY created_at_unix_nano DESC, created_at DESC, tool_call_id ASC
	`
	args := []interface{}{strings.TrimSpace(sessionID)}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tool receipts: %w", err)
	}
	defer rows.Close()

	results := make([]ToolExecutionReceipt, 0)
	for rows.Next() {
		var (
			receipt           ToolExecutionReceipt
			toolNameRaw       sql.NullString
			messageJSON       string
			createdAtRaw      string
			createdAtUnixNano sql.NullInt64
		)
		if err := rows.Scan(&receipt.SessionID, &receipt.ToolCallID, &toolNameRaw, &messageJSON, &createdAtRaw, &createdAtUnixNano); err != nil {
			return nil, fmt.Errorf("scan tool receipt: %w", err)
		}
		if toolNameRaw.Valid {
			receipt.ToolName = toolNameRaw.String
		}
		receipt.MessageJSON = json.RawMessage(messageJSON)
		receipt.CreatedAt = parseStoredUnixOrRFC3339Time(createdAtUnixNano, createdAtRaw)
		results = append(results, receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// AppendEvent stores a runtime event and returns its sequence.
func (s *SQLiteRuntimeStore) AppendEvent(ctx context.Context, event runtimeevents.Event) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("runtime store is not initialized")
	}
	if strings.TrimSpace(event.SessionID) == "" {
		return 0, fmt.Errorf("event requires session id")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	payloadJSON, err := json.Marshal(event.Payload)
	if err != nil {
		return 0, fmt.Errorf("marshal event payload: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin event tx: %w", err)
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1 FROM session_events WHERE session_id = ?
	`, event.SessionID).Scan(&seq); err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("next event seq: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_events (
			session_id, seq, type, trace_id, agent_name, tool_name, payload_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, event.SessionID, seq, event.Type, nullIfEmpty(event.TraceID), nullIfEmpty(event.AgentName),
		nullIfEmpty(event.ToolName), string(payloadJSON), event.Timestamp.Format(time.RFC3339Nano))
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("insert session event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit session event: %w", err)
	}
	return seq, nil
}

// ListEvents returns session events after a given sequence.
func (s *SQLiteRuntimeStore) ListEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]runtimeevents.Event, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	query := `
		SELECT seq, type, trace_id, agent_name, tool_name, payload_json, created_at
		FROM session_events
		WHERE session_id = ? AND seq > ?
		ORDER BY seq ASC
	`
	args := []interface{}{sessionID, afterSeq}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list session events: %w", err)
	}
	defer rows.Close()

	events := make([]runtimeevents.Event, 0)
	for rows.Next() {
		var (
			seq         int64
			eventType   string
			traceID     sql.NullString
			agentName   sql.NullString
			toolName    sql.NullString
			payloadJSON string
			createdRaw  string
		)
		if err := rows.Scan(&seq, &eventType, &traceID, &agentName, &toolName, &payloadJSON, &createdRaw); err != nil {
			return nil, fmt.Errorf("scan session event: %w", err)
		}
		ev := runtimeevents.Event{
			Type:      eventType,
			SessionID: sessionID,
			Payload:   map[string]interface{}{},
		}
		if traceID.Valid {
			ev.TraceID = traceID.String
		}
		if agentName.Valid {
			ev.AgentName = agentName.String
		}
		if toolName.Valid {
			ev.ToolName = toolName.String
		}
		if payloadJSON != "" {
			_ = json.Unmarshal([]byte(payloadJSON), &ev.Payload)
		}
		if ev.Payload == nil {
			ev.Payload = map[string]interface{}{}
		}
		ev.Payload["seq"] = seq
		if createdRaw != "" {
			ev.Timestamp, _ = time.Parse(time.RFC3339Nano, createdRaw)
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (s *SQLiteRuntimeStore) init(ctx context.Context) error {
	migrations := []migrate.Migration{
		{
			Version: 1,
			Name:    "session_runtime_state",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS session_runtime_state (
					session_id TEXT PRIMARY KEY,
					status TEXT NOT NULL,
					current_turn_id TEXT,
					current_checkpoint_id TEXT,
					pending_approval_json BLOB,
					pending_question_json BLOB,
					head_offset INTEGER NOT NULL DEFAULT 0,
					active_job_ids_json BLOB NOT NULL DEFAULT '[]',
					updated_at TEXT NOT NULL
				);
			`,
		},
		{
			Version: 2,
			Name:    "session_events",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS session_events (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					session_id TEXT NOT NULL,
					seq INTEGER NOT NULL,
					type TEXT NOT NULL,
					trace_id TEXT,
					agent_name TEXT,
					tool_name TEXT,
					payload_json BLOB NOT NULL,
					created_at TEXT NOT NULL,
					UNIQUE(session_id, seq)
				);
				CREATE INDEX IF NOT EXISTS idx_session_events_session_seq
				ON session_events(session_id, seq);
			`,
		},
		{
			Version: 3,
			Name:    "session_runtime_state_current_run_meta",
			UpSQL: `
				ALTER TABLE session_runtime_state ADD COLUMN current_run_meta_json BLOB;
			`,
		},
		{
			Version: 4,
			Name:    "session_runtime_state_pending_tool",
			UpSQL: `
				ALTER TABLE session_runtime_state ADD COLUMN pending_tool_json BLOB;
			`,
		},
		{
			Version: 5,
			Name:    "session_tool_receipts",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS session_tool_receipts (
					session_id TEXT NOT NULL,
					tool_call_id TEXT NOT NULL,
					tool_name TEXT,
					message_json BLOB NOT NULL,
					created_at TEXT NOT NULL,
					PRIMARY KEY (session_id, tool_call_id)
				);
			`,
		},
		{
			Version: 6,
			Name:    "session_tool_receipts_created_at_unix_nano",
			UpSQL: `
				ALTER TABLE session_tool_receipts ADD COLUMN created_at_unix_nano INTEGER NOT NULL DEFAULT 0;
				UPDATE session_tool_receipts
				SET created_at_unix_nano = COALESCE(
					CAST(ROUND((julianday(created_at) - 2440587.5) * 86400000000000.0) AS INTEGER),
					0
				)
				WHERE created_at_unix_nano = 0;
				CREATE INDEX IF NOT EXISTS idx_session_tool_receipts_session_created_at
				ON session_tool_receipts(session_id, created_at_unix_nano DESC, tool_call_id ASC);
			`,
		},
		{
			Version: 7,
			Name:    "session_runtime_state_ambient_run_meta",
			UpSQL: `
				ALTER TABLE session_runtime_state ADD COLUMN ambient_run_meta_json BLOB;
			`,
		},
	}
	return migrate.Apply(ctx, s.db, migrations)
}

func resolveRuntimeDSN(cfg *RuntimeStoreConfig) (string, error) {
	if cfg.Path != "" {
		dir := filepath.Dir(cfg.Path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("create runtime store directory: %w", err)
			}
		}
		return cfg.Path, nil
	}
	if cfg.DSN != "" {
		return cfg.DSN, nil
	}
	return fmt.Sprintf("file:runtime-store-%s?mode=memory&cache=shared", uuid.NewString()), nil
}

func nullIfEmpty(value string) interface{} {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func parseStoredUnixOrRFC3339Time(unixNano sql.NullInt64, raw string) time.Time {
	if unixNano.Valid && unixNano.Int64 != 0 {
		return time.Unix(0, unixNano.Int64).UTC()
	}
	if strings.TrimSpace(raw) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func cloneRuntimeEvent(event runtimeevents.Event) runtimeevents.Event {
	cloned := event
	if len(event.Payload) > 0 {
		cloned.Payload = make(map[string]interface{}, len(event.Payload))
		for key, value := range event.Payload {
			cloned.Payload[key] = value
		}
	}
	return cloned
}
