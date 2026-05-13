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

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
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

// SessionLeaseStore coordinates cross-process ownership for mutating session execution.
type SessionLeaseStore interface {
	AcquireLease(ctx context.Context, req LeaseRequest) (*SessionLease, error)
	RenewLease(ctx context.Context, sessionID, ownerID string, ttl time.Duration) error
	ReleaseLease(ctx context.Context, sessionID, ownerID string) error
	GetLease(ctx context.Context, sessionID string) (*SessionLease, error)
}

// LeaseRequest describes a requested session execution lease.
type LeaseRequest struct {
	SessionID string
	OwnerID   string
	OwnerKind string
	PID       int
	Hostname  string
	TTL       time.Duration
	Now       time.Time
}

// SessionLease describes the current owner of a session execution lease.
type SessionLease struct {
	SessionID   string    `json:"session_id"`
	OwnerID     string    `json:"owner_id"`
	OwnerKind   string    `json:"owner_kind"`
	PID         int       `json:"pid,omitempty"`
	Hostname    string    `json:"hostname,omitempty"`
	AcquiredAt  time.Time `json:"acquired_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
}

// LeaseConflictError is returned when another live owner holds a lease.
type LeaseConflictError struct {
	Lease *SessionLease
}

func (e *LeaseConflictError) Error() string {
	if e == nil || e.Lease == nil {
		return "session lease conflict"
	}
	return fmt.Sprintf("session %s is already owned by %s until %s", e.Lease.SessionID, e.Lease.OwnerID, e.Lease.ExpiresAt.Format(time.RFC3339Nano))
}

const defaultSessionLeaseTTL = 2 * time.Minute

// SessionLeaseHandle owns a live session lease and renews it until released.
type SessionLeaseHandle struct {
	store SessionLeaseStore
	lease *SessionLease
	ttl   time.Duration

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// AcquireSessionLease acquires a lease and starts periodic renewal for it.
func AcquireSessionLease(ctx context.Context, store SessionLeaseStore, req LeaseRequest) (*SessionLeaseHandle, error) {
	if store == nil {
		return nil, fmt.Errorf("session lease store is not configured")
	}
	req.TTL = normalizeSessionLeaseTTL(req.TTL)
	lease, err := store.AcquireLease(ctx, req)
	if err != nil {
		return nil, err
	}
	handle := &SessionLeaseHandle{
		store: store,
		lease: cloneSessionLease(lease),
		ttl:   req.TTL,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go handle.renewLoop()
	return handle, nil
}

// Lease returns a copy of the currently held lease.
func (h *SessionLeaseHandle) Lease() *SessionLease {
	if h == nil {
		return nil
	}
	return cloneSessionLease(h.lease)
}

// Release stops renewal and releases the lease. It is safe to call more than once.
func (h *SessionLeaseHandle) Release(ctx context.Context) error {
	if h == nil || h.store == nil || h.lease == nil {
		return nil
	}
	h.stopOnce.Do(func() {
		close(h.stop)
		<-h.done
	})
	if ctx == nil {
		ctx = context.Background()
	}
	return h.store.ReleaseLease(ctx, h.lease.SessionID, h.lease.OwnerID)
}

func (h *SessionLeaseHandle) renewLoop() {
	defer close(h.done)
	if h == nil || h.store == nil || h.lease == nil {
		return
	}
	interval := sessionLeaseHeartbeatInterval(h.ttl)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-timer.C:
			err := h.store.RenewLease(context.Background(), h.lease.SessionID, h.lease.OwnerID, h.ttl)
			if err != nil {
				return
			}
			timer.Reset(interval)
		}
	}
}

func normalizeSessionLeaseTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return defaultSessionLeaseTTL
	}
	return ttl
}

func sessionLeaseHeartbeatInterval(ttl time.Duration) time.Duration {
	ttl = normalizeSessionLeaseTTL(ttl)
	interval := ttl / 3
	if interval <= 0 {
		return time.Second
	}
	if interval < time.Second && ttl >= time.Second {
		return time.Second
	}
	return interval
}

// EventWatcherStore exposes in-process wake notifications for session events.
// Callers must still use ListEvents with AfterSeq for durable catch-up.
type EventWatcherStore interface {
	WatchEvents(ctx context.Context, sessionID string) (<-chan runtimeevents.Event, func())
}

// EventSequenceStore exposes the durable high-water mark for session events.
type EventSequenceStore interface {
	LastEventSeq(ctx context.Context, sessionID string) (int64, error)
}

// RuntimeStoreConfig configures the sqlite-backed runtime store.
type RuntimeStoreConfig struct {
	Path string
	DSN  string
}

// InMemoryRuntimeStore stores state and events in memory.
type InMemoryRuntimeStore struct {
	mu             sync.RWMutex
	states         map[string]*RuntimeState
	events         map[string][]storedEvent
	mailbox        map[string][]team.MailMessage
	controlMailbox map[string][]team.MailMessage
	receipts       map[string]map[string]ToolExecutionReceipt
	seq            map[string]int64
	mailboxSeq     map[string]int64
	controlSeq     map[string]int64
	retention      int
	leases         map[string]*SessionLease

	globalMailboxWriter agentcontrol.GlobalMailboxWriter

	watchMu         sync.Mutex
	nextWatchID     int64
	eventWatchers   map[int64]eventWatcher
	mailWatchers    map[int64]mailboxWatcher
	controlWatchers map[int64]mailboxWatcher
}

// SetGlobalMailboxWriter configures an optional write-through target for the
// durable cross-workflow AgentControl mailbox registry.
func (s *InMemoryRuntimeStore) SetGlobalMailboxWriter(writer agentcontrol.GlobalMailboxWriter) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.globalMailboxWriter = writer
	s.mu.Unlock()
}

// AgentControlMailboxProjectionStatus reports the runtime store's local to
// global AgentControl mailbox projection semantics.
func (s *InMemoryRuntimeStore) AgentControlMailboxProjectionStatus() agentcontrol.MailboxProjectionStatus {
	status := agentcontrol.MailboxProjectionStatus{Store: "runtime_in_memory"}
	if s == nil {
		status.Mode = agentcontrol.MailboxProjectionModeLocalOnly
		status.Reason = "store_not_configured"
		return status.Normalize()
	}
	s.mu.RLock()
	writer := s.globalMailboxWriter
	s.mu.RUnlock()
	if writer == nil {
		status.Mode = agentcontrol.MailboxProjectionModeLocalOnly
		status.Reason = "global_writer_not_configured"
		return status.Normalize()
	}
	status.Mode = agentcontrol.MailboxProjectionModeWriteThrough
	status.Reason = "in_memory_store_cannot_share_sqlite_transaction"
	return status.Normalize()
}

type storedEvent struct {
	Seq   int64
	Event runtimeevents.Event
}

type eventWatcher struct {
	sessionID string
	ch        chan runtimeevents.Event
}

type mailboxWatcher struct {
	sessionID string
	ch        chan team.MailMessage
}

// NewInMemoryRuntimeStore creates a memory-backed runtime store.
func NewInMemoryRuntimeStore(retention int) *InMemoryRuntimeStore {
	if retention < 0 {
		retention = 0
	}
	return &InMemoryRuntimeStore{
		states:         make(map[string]*RuntimeState),
		events:         make(map[string][]storedEvent),
		mailbox:        make(map[string][]team.MailMessage),
		controlMailbox: make(map[string][]team.MailMessage),
		receipts:       make(map[string]map[string]ToolExecutionReceipt),
		seq:            make(map[string]int64),
		mailboxSeq:     make(map[string]int64),
		controlSeq:     make(map[string]int64),
		leases:         make(map[string]*SessionLease),
		retention:      retention,
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
	delete(s.mailbox, sessionID)
	delete(s.controlMailbox, sessionID)
	delete(s.receipts, sessionID)
	delete(s.seq, sessionID)
	delete(s.mailboxSeq, sessionID)
	delete(s.controlSeq, sessionID)
	delete(s.leases, sessionID)
	return nil
}

func (s *InMemoryRuntimeStore) AcquireLease(ctx context.Context, req LeaseRequest) (*SessionLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lease, err := buildLeaseFromRequest(req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.leases[lease.SessionID]; existing != nil && !leaseExpired(existing, lease.HeartbeatAt) && existing.OwnerID != lease.OwnerID {
		return nil, &LeaseConflictError{Lease: cloneSessionLease(existing)}
	}
	s.leases[lease.SessionID] = cloneSessionLease(lease)
	return cloneSessionLease(lease), nil
}

func (s *InMemoryRuntimeStore) RenewLease(ctx context.Context, sessionID, ownerID string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	ownerID = strings.TrimSpace(ownerID)
	if sessionID == "" || ownerID == "" {
		return fmt.Errorf("session id and owner id are required")
	}
	if ttl <= 0 {
		ttl = defaultSessionLeaseTTL
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.leases[sessionID]
	if existing == nil {
		return fmt.Errorf("session lease not found")
	}
	if existing.OwnerID != ownerID {
		return &LeaseConflictError{Lease: cloneSessionLease(existing)}
	}
	existing.HeartbeatAt = now
	existing.ExpiresAt = now.Add(ttl)
	return nil
}

func (s *InMemoryRuntimeStore) ReleaseLease(ctx context.Context, sessionID, ownerID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	ownerID = strings.TrimSpace(ownerID)
	if sessionID == "" || ownerID == "" {
		return fmt.Errorf("session id and owner id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.leases[sessionID]; existing != nil && existing.OwnerID == ownerID {
		delete(s.leases, sessionID)
	}
	return nil
}

func (s *InMemoryRuntimeStore) GetLease(ctx context.Context, sessionID string) (*SessionLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSessionLease(s.leases[sessionID]), nil
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
	s.seq[event.SessionID]++
	seq := s.seq[event.SessionID]
	entry := storedEvent{Seq: seq, Event: cloneRuntimeEvent(event)}
	list := append(s.events[event.SessionID], entry)
	if s.retention > 0 && len(list) > s.retention {
		list = list[len(list)-s.retention:]
	}
	s.events[event.SessionID] = list
	s.mu.Unlock()
	s.notifyEventWatchers(seq, event)
	return seq, nil
}

// AppendMailbox stores an agent mailbox message through the runtime store's
// mailbox substrate.
func (s *InMemoryRuntimeStore) AppendMailbox(ctx context.Context, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, error) {
	if err := ctx.Err(); err != nil {
		return runtimeevents.Event{}, 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return runtimeevents.Event{}, 0, fmt.Errorf("session id is required")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	globalPrimaryUsed := false
	var err error
	if IsAgentControlMailboxMessage(message) {
		message, globalPrimaryUsed, err = s.appendPrimaryGlobalMailboxRecord(ctx, sessionID, message)
		if err != nil {
			return runtimeevents.Event{}, 0, err
		}
	}
	s.mu.Lock()
	s.mailboxSeq[sessionID]++
	mailboxSeq := s.mailboxSeq[sessionID]
	message.Seq = mailboxSeq
	message.SessionMailboxSeq = mailboxSeq
	if strings.TrimSpace(message.ID) == "" {
		message.ID = fmt.Sprintf("mailbox_%d", mailboxSeq)
	}
	var controlMessage team.MailMessage
	if IsAgentControlMailboxMessage(message) {
		s.controlSeq[sessionID]++
		controlSeq := s.controlSeq[sessionID]
		controlMessage = cloneTeamMailMessage(message)
		controlMessage.Seq = controlSeq
		controlMessage.ControlSeq = controlSeq
		controlMessage.SessionMailboxSeq = mailboxSeq
		s.controlMailbox[sessionID] = append(s.controlMailbox[sessionID], controlMessage)
	}
	s.mailbox[sessionID] = append(s.mailbox[sessionID], cloneTeamMailMessage(message))
	s.mu.Unlock()
	if controlMessage.ControlSeq > 0 {
		if globalPrimaryUsed {
			if refreshed, _, err := s.appendPrimaryGlobalMailboxRecord(ctx, sessionID, controlMessage); err != nil {
				return runtimeevents.Event{}, mailboxSeq, err
			} else if refreshed.GlobalSeq > 0 {
				controlMessage.GlobalSeq = refreshed.GlobalSeq
				message.GlobalSeq = refreshed.GlobalSeq
				s.setInMemoryControlMailboxGlobalSeq(sessionID, controlMessage.ControlSeq, refreshed.GlobalSeq)
			}
		} else if globalSeq, _ := s.appendGlobalMailboxRecord(ctx, sessionID, controlMessage); globalSeq > 0 {
			controlMessage.GlobalSeq = globalSeq
			message.GlobalSeq = globalSeq
			s.setInMemoryControlMailboxGlobalSeq(sessionID, controlMessage.ControlSeq, globalSeq)
		}
	}
	s.notifyMailboxWatchers(sessionID, message)
	if controlMessage.ControlSeq > 0 {
		s.notifyAgentControlMailboxWatchers(sessionID, controlMessage)
	}

	event := NewMailboxReceivedEvent(sessionID, message)
	seq, err := s.AppendEvent(ctx, event)
	if err != nil {
		return event, mailboxSeq, err
	}
	if event.Payload == nil {
		event.Payload = map[string]interface{}{}
	}
	event.Payload["seq"] = seq
	event.Payload["mailbox_seq"] = mailboxSeq
	return event, mailboxSeq, nil
}

// AppendAgentControlMailbox stores an AgentControl envelope mailbox message
// through the runtime store's canonical control-plane write surface, then keeps
// the legacy session mailbox/event rows as compatibility mirrors.
func (s *InMemoryRuntimeStore) AppendAgentControlMailbox(ctx context.Context, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, error) {
	if err := validateAgentControlMailboxMessage(message); err != nil {
		return runtimeevents.Event{}, 0, err
	}
	if err := ctx.Err(); err != nil {
		return runtimeevents.Event{}, 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return runtimeevents.Event{}, 0, fmt.Errorf("session id is required")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	var globalPrimaryUsed bool
	var err error
	message, globalPrimaryUsed, err = s.appendPrimaryGlobalMailboxRecord(ctx, sessionID, message)
	if err != nil {
		return runtimeevents.Event{}, 0, err
	}
	s.mu.Lock()
	s.mailboxSeq[sessionID]++
	mailboxSeq := s.mailboxSeq[sessionID]
	s.controlSeq[sessionID]++
	controlSeq := s.controlSeq[sessionID]
	message.Seq = mailboxSeq
	message.SessionMailboxSeq = mailboxSeq
	if strings.TrimSpace(message.ID) == "" {
		message.ID = fmt.Sprintf("mailbox_%d", mailboxSeq)
	}
	controlMessage := cloneTeamMailMessage(message)
	controlMessage.Seq = controlSeq
	controlMessage.ControlSeq = controlSeq
	controlMessage.SessionMailboxSeq = mailboxSeq
	s.controlMailbox[sessionID] = append(s.controlMailbox[sessionID], controlMessage)
	s.mailbox[sessionID] = append(s.mailbox[sessionID], cloneTeamMailMessage(message))
	s.mu.Unlock()
	if globalPrimaryUsed {
		if refreshed, _, err := s.appendPrimaryGlobalMailboxRecord(ctx, sessionID, controlMessage); err != nil {
			return runtimeevents.Event{}, mailboxSeq, err
		} else if refreshed.GlobalSeq > 0 {
			controlMessage.GlobalSeq = refreshed.GlobalSeq
			message.GlobalSeq = refreshed.GlobalSeq
			s.setInMemoryControlMailboxGlobalSeq(sessionID, controlMessage.ControlSeq, refreshed.GlobalSeq)
		}
	} else if globalSeq, _ := s.appendGlobalMailboxRecord(ctx, sessionID, controlMessage); globalSeq > 0 {
		controlMessage.GlobalSeq = globalSeq
		message.GlobalSeq = globalSeq
		s.setInMemoryControlMailboxGlobalSeq(sessionID, controlMessage.ControlSeq, globalSeq)
	}
	s.notifyAgentControlMailboxWatchers(sessionID, controlMessage)
	s.notifyMailboxWatchers(sessionID, message)

	event := NewMailboxReceivedEvent(sessionID, message)
	seq, err := s.AppendEvent(ctx, event)
	if err != nil {
		return event, mailboxSeq, err
	}
	if event.Payload == nil {
		event.Payload = map[string]interface{}{}
	}
	event.Payload["seq"] = seq
	event.Payload["mailbox_seq"] = mailboxSeq
	return event, mailboxSeq, nil
}

func (s *InMemoryRuntimeStore) appendGlobalMailboxRecord(ctx context.Context, sessionID string, message team.MailMessage) (int64, error) {
	if s == nil || message.ControlSeq <= 0 {
		return 0, nil
	}
	s.mu.RLock()
	writer := s.globalMailboxWriter
	s.mu.RUnlock()
	if writer == nil {
		return 0, nil
	}
	record := mailboxRecordFromRuntimeMessage(sessionID, message)
	record.Seq = message.ControlSeq
	record.SourceSeq = message.ControlSeq
	globalSeq, err := writer.AppendGlobalMailboxRecord(ctx, agentcontrol.MailboxSourceRuntimeSessions, record)
	if err != nil {
		return 0, fmt.Errorf("append global runtime mailbox record: %w", err)
	}
	return globalSeq, nil
}

func (s *InMemoryRuntimeStore) appendPrimaryGlobalMailboxRecord(ctx context.Context, sessionID string, message team.MailMessage) (team.MailMessage, bool, error) {
	if s == nil {
		return message, false, nil
	}
	s.mu.RLock()
	writer := s.globalMailboxWriter
	s.mu.RUnlock()
	primary, ok := writer.(agentcontrol.GlobalMailboxPrimaryWriter)
	if !ok || primary == nil {
		return message, false, nil
	}
	if strings.TrimSpace(message.ID) == "" {
		message.ID = "mailbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	record := mailboxRecordFromRuntimeMessage(sessionID, message)
	appended, err := primary.AppendPrimaryGlobalMailboxRecord(ctx, record)
	if err != nil {
		return message, false, fmt.Errorf("append primary global runtime mailbox record: %w", err)
	}
	if appended.GlobalSeq > 0 {
		message.GlobalSeq = appended.GlobalSeq
	}
	if strings.TrimSpace(appended.MessageID) != "" {
		message.ID = appended.MessageID
	}
	return message, true, nil
}

func (s *InMemoryRuntimeStore) setInMemoryControlMailboxGlobalSeq(sessionID string, controlSeq int64, globalSeq int64) {
	if s == nil || controlSeq <= 0 || globalSeq <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionMailboxSeq := int64(0)
	messageID := ""
	for index := range s.controlMailbox[sessionID] {
		if s.controlMailbox[sessionID][index].ControlSeq == controlSeq {
			s.controlMailbox[sessionID][index].GlobalSeq = globalSeq
			sessionMailboxSeq = s.controlMailbox[sessionID][index].SessionMailboxSeq
			messageID = strings.TrimSpace(s.controlMailbox[sessionID][index].ID)
			break
		}
	}
	for index := range s.mailbox[sessionID] {
		if sessionMailboxSeq > 0 && s.mailbox[sessionID][index].SessionMailboxSeq == sessionMailboxSeq {
			s.mailbox[sessionID][index].GlobalSeq = globalSeq
			return
		}
		if messageID != "" && strings.EqualFold(strings.TrimSpace(s.mailbox[sessionID][index].ID), messageID) {
			s.mailbox[sessionID][index].GlobalSeq = globalSeq
			return
		}
	}
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

// ListMailbox returns mailbox messages after a given mailbox sequence.
func (s *InMemoryRuntimeStore) ListMailbox(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]team.MailMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.mailbox[sessionID]
	if len(list) == 0 {
		return nil, nil
	}
	result := make([]team.MailMessage, 0, len(list))
	for _, message := range list {
		if message.Seq <= afterSeq {
			continue
		}
		result = append(result, cloneTeamMailMessage(message))
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// ListAgentControlMailbox returns mailbox messages with AgentControl envelope metadata.
func (s *InMemoryRuntimeStore) ListAgentControlMailbox(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]team.MailMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.controlMailbox[sessionID]
	if len(list) == 0 {
		return nil, nil
	}
	result := make([]team.MailMessage, 0, len(list))
	for _, message := range list {
		if message.ControlSeq <= afterSeq {
			continue
		}
		cloned := cloneTeamMailMessage(message)
		cloned.Seq = cloned.ControlSeq
		result = append(result, cloned)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// ListAgentControlMailboxRecords projects runtime control mailbox rows into
// the shared AgentControl mailbox registry read model.
func (s *InMemoryRuntimeStore) ListAgentControlMailboxRecords(ctx context.Context, filter agentcontrol.MailboxRecordFilter) ([]agentcontrol.MailboxRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeSession {
		return nil, nil
	}
	if filter.SessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.controlMailbox[filter.SessionID]
	if len(list) == 0 {
		return nil, nil
	}
	records := make([]agentcontrol.MailboxRecord, 0, len(list))
	for _, message := range list {
		if message.ControlSeq <= filter.AfterSeq {
			continue
		}
		workflow := agentcontrol.MetadataString(message.Metadata, agentcontrol.MetadataKeyWorkflow)
		if filter.Workflow != "" && workflow != filter.Workflow {
			continue
		}
		if filter.TeamID != "" && !strings.EqualFold(strings.TrimSpace(message.TeamID), filter.TeamID) {
			continue
		}
		records = append(records, mailboxRecordFromRuntimeMessage(filter.SessionID, message))
		if filter.Limit > 0 && len(records) >= filter.Limit {
			break
		}
	}
	return records, nil
}

// WatchEvents registers an in-process notification channel for session events.
func (s *InMemoryRuntimeStore) WatchEvents(ctx context.Context, sessionID string) (<-chan runtimeevents.Event, func()) {
	ch := make(chan runtimeevents.Event, 1)
	if s == nil {
		return ch, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	s.watchMu.Lock()
	if s.eventWatchers == nil {
		s.eventWatchers = make(map[int64]eventWatcher)
	}
	s.nextWatchID++
	watchID := s.nextWatchID
	s.eventWatchers[watchID] = eventWatcher{sessionID: sessionID, ch: ch}
	s.watchMu.Unlock()

	cancel := func() {
		s.watchMu.Lock()
		if s.eventWatchers != nil {
			delete(s.eventWatchers, watchID)
		}
		s.watchMu.Unlock()
	}
	if ctx != nil {
		done := ctx.Done()
		if done != nil {
			go func() {
				<-done
				cancel()
			}()
		}
	}
	return ch, cancel
}

// WatchMailbox registers an in-process notification channel for session mailbox rows.
func (s *InMemoryRuntimeStore) WatchMailbox(ctx context.Context, sessionID string) (<-chan team.MailMessage, func()) {
	ch := make(chan team.MailMessage, 1)
	if s == nil {
		return ch, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	s.watchMu.Lock()
	if s.mailWatchers == nil {
		s.mailWatchers = make(map[int64]mailboxWatcher)
	}
	s.nextWatchID++
	watchID := s.nextWatchID
	s.mailWatchers[watchID] = mailboxWatcher{sessionID: sessionID, ch: ch}
	s.watchMu.Unlock()

	cancel := func() {
		s.watchMu.Lock()
		if s.mailWatchers != nil {
			delete(s.mailWatchers, watchID)
		}
		s.watchMu.Unlock()
	}
	if ctx != nil {
		done := ctx.Done()
		if done != nil {
			go func() {
				<-done
				cancel()
			}()
		}
	}
	return ch, cancel
}

// WatchAgentControlMailbox registers notifications for AgentControl mailbox rows.
func (s *InMemoryRuntimeStore) WatchAgentControlMailbox(ctx context.Context, sessionID string) (<-chan team.MailMessage, func()) {
	ch := make(chan team.MailMessage, 1)
	if s == nil {
		return ch, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	s.watchMu.Lock()
	if s.controlWatchers == nil {
		s.controlWatchers = make(map[int64]mailboxWatcher)
	}
	s.nextWatchID++
	watchID := s.nextWatchID
	s.controlWatchers[watchID] = mailboxWatcher{sessionID: sessionID, ch: ch}
	s.watchMu.Unlock()

	cancel := func() {
		s.watchMu.Lock()
		if s.controlWatchers != nil {
			delete(s.controlWatchers, watchID)
		}
		s.watchMu.Unlock()
	}
	if ctx != nil {
		done := ctx.Done()
		if done != nil {
			go func() {
				<-done
				cancel()
			}()
		}
	}
	return ch, func() {
		cancel()
	}
}

// LastEventSeq returns the current durable event high-water mark for a session.
func (s *InMemoryRuntimeStore) LastEventSeq(ctx context.Context, sessionID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s == nil {
		return 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seq[strings.TrimSpace(sessionID)], nil
}

// LastMailboxSeq returns the current durable mailbox high-water mark for a session.
func (s *InMemoryRuntimeStore) LastMailboxSeq(ctx context.Context, sessionID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s == nil {
		return 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mailboxSeq[strings.TrimSpace(sessionID)], nil
}

// LastAgentControlMailboxSeq returns the high-water mark for AgentControl mailbox rows.
func (s *InMemoryRuntimeStore) LastAgentControlMailboxSeq(ctx context.Context, sessionID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s == nil {
		return 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.controlSeq[strings.TrimSpace(sessionID)], nil
}

// LastAgentControlMailboxRecordSeq returns the shared AgentControl mailbox
// registry high-water mark for runtime/session rows.
func (s *InMemoryRuntimeStore) LastAgentControlMailboxRecordSeq(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s == nil {
		return 0, nil
	}
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeSession {
		return 0, nil
	}
	if filter.SessionID == "" {
		return 0, fmt.Errorf("session id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.controlSeq[filter.SessionID], nil
}

func (s *InMemoryRuntimeStore) notifyEventWatchers(seq int64, event runtimeevents.Event) {
	if s == nil {
		return
	}
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return
	}
	event = cloneRuntimeEvent(event)
	if event.Payload == nil {
		event.Payload = map[string]interface{}{}
	}
	event.Payload["seq"] = seq
	s.watchMu.Lock()
	watchers := make([]eventWatcher, 0, len(s.eventWatchers))
	for _, watcher := range s.eventWatchers {
		if watcher.ch == nil {
			continue
		}
		if watcher.sessionID != "" && !strings.EqualFold(watcher.sessionID, sessionID) {
			continue
		}
		watchers = append(watchers, watcher)
	}
	s.watchMu.Unlock()
	for _, watcher := range watchers {
		select {
		case watcher.ch <- event:
		default:
		}
	}
}

func (s *InMemoryRuntimeStore) notifyMailboxWatchers(sessionID string, message team.MailMessage) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	message = cloneTeamMailMessage(message)
	s.watchMu.Lock()
	watchers := make([]mailboxWatcher, 0, len(s.mailWatchers))
	for _, watcher := range s.mailWatchers {
		if watcher.ch == nil {
			continue
		}
		if watcher.sessionID != "" && !strings.EqualFold(watcher.sessionID, sessionID) {
			continue
		}
		watchers = append(watchers, watcher)
	}
	s.watchMu.Unlock()
	for _, watcher := range watchers {
		select {
		case watcher.ch <- message:
		default:
		}
	}
}

func (s *InMemoryRuntimeStore) notifyAgentControlMailboxWatchers(sessionID string, message team.MailMessage) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	message = cloneTeamMailMessage(message)
	s.watchMu.Lock()
	watchers := make([]mailboxWatcher, 0, len(s.controlWatchers))
	for _, watcher := range s.controlWatchers {
		if watcher.ch == nil {
			continue
		}
		if watcher.sessionID != "" && !strings.EqualFold(watcher.sessionID, sessionID) {
			continue
		}
		watchers = append(watchers, watcher)
	}
	s.watchMu.Unlock()
	for _, watcher := range watchers {
		select {
		case watcher.ch <- message:
		default:
		}
	}
}

func cloneTeamMailMessage(message team.MailMessage) team.MailMessage {
	cloned := message
	if message.TaskID != nil {
		taskID := *message.TaskID
		cloned.TaskID = &taskID
	}
	if len(message.Metadata) > 0 {
		cloned.Metadata = make(map[string]interface{}, len(message.Metadata))
		for key, value := range message.Metadata {
			cloned.Metadata[key] = value
		}
	}
	if message.AckedAt != nil {
		ackedAt := *message.AckedAt
		cloned.AckedAt = &ackedAt
	}
	return cloned
}

func mailboxRecordFromRuntimeMessage(sessionID string, message team.MailMessage) agentcontrol.MailboxRecord {
	taskID := ""
	if message.TaskID != nil {
		taskID = strings.TrimSpace(*message.TaskID)
	}
	metadata := map[string]interface{}{}
	for key, value := range message.Metadata {
		metadata[key] = value
	}
	workflow := agentcontrol.MetadataString(metadata, agentcontrol.MetadataKeyWorkflow)
	seq := message.ControlSeq
	if seq == 0 {
		seq = message.Seq
	}
	return agentcontrol.MailboxRecord{
		Seq:               seq,
		GlobalSeq:         message.GlobalSeq,
		Workflow:          workflow,
		Scope:             agentcontrol.MailboxScopeSession,
		SessionID:         strings.TrimSpace(sessionID),
		SessionMailboxSeq: message.SessionMailboxSeq,
		TeamID:            strings.TrimSpace(message.TeamID),
		MessageID:         strings.TrimSpace(message.ID),
		FromAgent:         strings.TrimSpace(message.FromAgent),
		ToAgent:           strings.TrimSpace(message.ToAgent),
		TaskID:            taskID,
		Kind:              strings.TrimSpace(message.Kind),
		Body:              message.Body,
		Metadata:          metadata,
		CreatedAt:         message.CreatedAt,
	}.Normalize()
}

// SQLiteRuntimeStore persists runtime data in sqlite.
type SQLiteRuntimeStore struct {
	mu sync.Mutex
	db *sql.DB

	globalMailboxWriter agentcontrol.GlobalMailboxWriter

	watchMu         sync.Mutex
	nextWatchID     int64
	eventWatchers   map[int64]eventWatcher
	mailWatchers    map[int64]mailboxWatcher
	controlWatchers map[int64]mailboxWatcher
}

// SetGlobalMailboxWriter configures an optional write-through target for the
// durable cross-workflow AgentControl mailbox registry.
func (s *SQLiteRuntimeStore) SetGlobalMailboxWriter(writer agentcontrol.GlobalMailboxWriter) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.globalMailboxWriter = writer
	s.mu.Unlock()
}

// AgentControlMailboxProjectionStatus reports the runtime store's local to
// global AgentControl mailbox projection semantics.
func (s *SQLiteRuntimeStore) AgentControlMailboxProjectionStatus() agentcontrol.MailboxProjectionStatus {
	status := agentcontrol.MailboxProjectionStatus{Store: "runtime_sqlite"}
	if s == nil || s.db == nil {
		status.Mode = agentcontrol.MailboxProjectionModeLocalOnly
		status.Reason = "store_not_configured"
		return status.Normalize()
	}
	s.mu.Lock()
	writer := s.globalMailboxWriter
	s.mu.Unlock()
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
	if strings.Contains(dsn, "mode=memory") {
		db.SetMaxOpenConns(1)
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

func (s *SQLiteRuntimeStore) AcquireLease(ctx context.Context, req LeaseRequest) (*SessionLease, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	lease, err := buildLeaseFromRequest(req)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin lease transaction: %w", err)
	}
	defer tx.Rollback()

	existing, err := getSessionLeaseTx(ctx, tx, lease.SessionID)
	if err != nil {
		return nil, err
	}
	if existing != nil && !leaseExpired(existing, lease.HeartbeatAt) && existing.OwnerID != lease.OwnerID {
		return nil, &LeaseConflictError{Lease: existing}
	}
	if existing != nil && existing.OwnerID == lease.OwnerID && !existing.AcquiredAt.IsZero() {
		lease.AcquiredAt = existing.AcquiredAt
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_actor_leases (
			session_id, owner_id, owner_kind, pid, hostname, acquired_at, expires_at, heartbeat_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			owner_id = excluded.owner_id,
			owner_kind = excluded.owner_kind,
			pid = excluded.pid,
			hostname = excluded.hostname,
			acquired_at = excluded.acquired_at,
			expires_at = excluded.expires_at,
			heartbeat_at = excluded.heartbeat_at
	`, lease.SessionID, lease.OwnerID, lease.OwnerKind, lease.PID, lease.Hostname, lease.AcquiredAt.Format(time.RFC3339Nano), lease.ExpiresAt.Format(time.RFC3339Nano), lease.HeartbeatAt.Format(time.RFC3339Nano)); err != nil {
		return nil, fmt.Errorf("save session lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session lease: %w", err)
	}
	return cloneSessionLease(lease), nil
}

func (s *SQLiteRuntimeStore) RenewLease(ctx context.Context, sessionID, ownerID string, ttl time.Duration) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runtime store is not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	ownerID = strings.TrimSpace(ownerID)
	if sessionID == "" || ownerID == "" {
		return fmt.Errorf("session id and owner id are required")
	}
	if ttl <= 0 {
		ttl = defaultSessionLeaseTTL
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin lease transaction: %w", err)
	}
	defer tx.Rollback()
	existing, err := getSessionLeaseTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("session lease not found")
	}
	if existing.OwnerID != ownerID {
		return &LeaseConflictError{Lease: existing}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE session_actor_leases
		SET heartbeat_at = ?, expires_at = ?
		WHERE session_id = ? AND owner_id = ?
	`, now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano), sessionID, ownerID)
	if err != nil {
		return fmt.Errorf("renew session lease: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("session lease not found")
	}
	return tx.Commit()
}

func (s *SQLiteRuntimeStore) ReleaseLease(ctx context.Context, sessionID, ownerID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runtime store is not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	ownerID = strings.TrimSpace(ownerID)
	if sessionID == "" || ownerID == "" {
		return fmt.Errorf("session id and owner id are required")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM session_actor_leases WHERE session_id = ? AND owner_id = ?`, sessionID, ownerID)
	if err != nil {
		return fmt.Errorf("release session lease: %w", err)
	}
	return nil
}

func (s *SQLiteRuntimeStore) GetLease(ctx context.Context, sessionID string) (*SessionLease, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	lease, err := scanSessionLease(s.db.QueryRowContext(ctx, `
		SELECT session_id, owner_id, owner_kind, pid, hostname, acquired_at, expires_at, heartbeat_at
		FROM session_actor_leases
		WHERE session_id = ?
	`, sessionID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session lease: %w", err)
	}
	return lease, nil
}

// LoadState loads runtime state for a session.
func (s *SQLiteRuntimeStore) LoadState(ctx context.Context, sessionID string) (*RuntimeState, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT session_id, status, current_turn_id, current_checkpoint_id, current_run_meta_json, ambient_run_meta_json, stable_tool_surface_json, frozen_turn_tools_json,
		       pending_tool_json, pending_approval_json, pending_question_json, head_offset, active_job_ids_json, updated_at
		FROM session_runtime_state
		WHERE session_id = ?
	`, sessionID)

	var (
		state                RuntimeState
		statusRaw            string
		currentRunMetaRaw    sql.NullString
		ambientRunMetaRaw    sql.NullString
		stableToolSurfaceRaw sql.NullString
		frozenTurnToolsRaw   sql.NullString
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
		&stableToolSurfaceRaw,
		&frozenTurnToolsRaw,
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
	if stableToolSurfaceRaw.Valid && strings.TrimSpace(stableToolSurfaceRaw.String) != "" {
		var tools []types.ToolDefinition
		if err := json.Unmarshal([]byte(stableToolSurfaceRaw.String), &tools); err == nil {
			state.StableToolSurface = cloneRuntimeToolDefinitions(tools)
			state.StableToolSurfaceSet = true
		}
	}
	if frozenTurnToolsRaw.Valid && strings.TrimSpace(frozenTurnToolsRaw.String) != "" {
		var frozen []types.ToolDefinition
		if err := json.Unmarshal([]byte(frozenTurnToolsRaw.String), &frozen); err == nil {
			state.FrozenTurnTools = cloneRuntimeToolDefinitions(frozen)
			state.FrozenTurnToolsSet = true
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
	stableToolSurfaceJSON := ""
	if state.StableToolSurfaceSet {
		payload, err := json.Marshal(state.StableToolSurface)
		if err != nil {
			return fmt.Errorf("marshal stable tool surface: %w", err)
		}
		stableToolSurfaceJSON = string(payload)
	}
	frozenTurnToolsJSON := ""
	if state.FrozenTurnToolsSet {
		payload, err := json.Marshal(state.FrozenTurnTools)
		if err != nil {
			return fmt.Errorf("marshal frozen turn tools: %w", err)
		}
		frozenTurnToolsJSON = string(payload)
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
			session_id, status, current_turn_id, current_checkpoint_id, current_run_meta_json, ambient_run_meta_json, stable_tool_surface_json,
			frozen_turn_tools_json, pending_tool_json, pending_approval_json, pending_question_json, head_offset, active_job_ids_json, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			status = excluded.status,
			current_turn_id = excluded.current_turn_id,
			current_checkpoint_id = excluded.current_checkpoint_id,
			current_run_meta_json = excluded.current_run_meta_json,
			ambient_run_meta_json = excluded.ambient_run_meta_json,
			stable_tool_surface_json = excluded.stable_tool_surface_json,
			frozen_turn_tools_json = excluded.frozen_turn_tools_json,
			pending_tool_json = excluded.pending_tool_json,
			pending_approval_json = excluded.pending_approval_json,
			pending_question_json = excluded.pending_question_json,
			head_offset = excluded.head_offset,
			active_job_ids_json = excluded.active_job_ids_json,
			updated_at = excluded.updated_at
	`, state.SessionID, string(state.Status), nullIfEmpty(state.CurrentTurnID), nullIfEmpty(state.CurrentCheckpointID),
		nullIfEmpty(currentRunMetaJSON), nullIfEmpty(ambientRunMetaJSON), nullIfEmpty(stableToolSurfaceJSON), nullIfEmpty(frozenTurnToolsJSON), nullIfEmpty(pendingToolJSON), pendingApprovalJSON, pendingQuestionJSON, state.HeadOffset, activeJobsJSON, state.UpdatedAt.Format(time.RFC3339Nano))
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
	s.mu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("begin event tx: %w", err)
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1 FROM session_events WHERE session_id = ?
	`, event.SessionID).Scan(&seq); err != nil {
		_ = tx.Rollback()
		s.mu.Unlock()
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
		s.mu.Unlock()
		return 0, fmt.Errorf("insert session event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("commit session event: %w", err)
	}
	s.mu.Unlock()
	s.notifyEventWatchers(seq, event)
	return seq, nil
}

// AppendMailbox stores an agent mailbox message through the runtime store's
// mailbox substrate.
func (s *SQLiteRuntimeStore) AppendMailbox(ctx context.Context, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, error) {
	if s == nil || s.db == nil {
		return runtimeevents.Event{}, 0, fmt.Errorf("runtime store is not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return runtimeevents.Event{}, 0, fmt.Errorf("session id is required")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	globalPrimaryUsed := false
	var err error
	if IsAgentControlMailboxMessage(message) {
		message, globalPrimaryUsed, err = s.appendPrimaryGlobalMailboxRecord(ctx, sessionID, message)
		if err != nil {
			return runtimeevents.Event{}, 0, err
		}
	}
	s.mu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.mu.Unlock()
		return runtimeevents.Event{}, 0, fmt.Errorf("begin mailbox tx: %w", err)
	}
	result, err := s.appendMailboxTx(ctx, tx, sessionID, message)
	if err != nil {
		_ = tx.Rollback()
		s.mu.Unlock()
		return result.event, result.mailboxSeq, err
	}
	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return result.event, result.mailboxSeq, fmt.Errorf("commit mailbox tx: %w", err)
	}
	s.mu.Unlock()
	if globalPrimaryUsed {
		if refreshed, _, err := s.appendPrimaryGlobalMailboxRecord(ctx, sessionID, result.message); err != nil {
			return result.event, result.mailboxSeq, err
		} else if refreshed.GlobalSeq > 0 {
			result.message.GlobalSeq = refreshed.GlobalSeq
		}
	} else if globalSeq, _ := s.appendGlobalMailboxRecord(ctx, sessionID, result); globalSeq > 0 {
		result.message.GlobalSeq = globalSeq
	}
	s.notifyMailboxWatchers(sessionID, result.message)
	if result.controlSeq > 0 {
		controlMessage := cloneTeamMailMessage(result.message)
		controlMessage.Seq = result.controlSeq
		controlMessage.ControlSeq = result.controlSeq
		controlMessage.SessionMailboxSeq = result.mailboxSeq
		s.notifyAgentControlMailboxWatchers(sessionID, controlMessage)
	}
	s.notifyEventWatchers(result.eventSeq, result.event)
	return result.event, result.mailboxSeq, nil
}

func (s *SQLiteRuntimeStore) appendAgentControlMailboxSameTx(ctx context.Context, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, bool, error) {
	if s == nil || s.db == nil {
		return runtimeevents.Event{}, 0, false, nil
	}
	s.mu.Lock()
	writer := s.globalMailboxWriter
	s.mu.Unlock()
	txWriter, ok := writer.(agentcontrol.GlobalMailboxSQLiteTxWriter)
	if !ok || txWriter == nil {
		return runtimeevents.Event{}, 0, false, nil
	}
	if _, ok := txWriter.GlobalMailboxAttachDSN(); !ok {
		return runtimeevents.Event{}, 0, false, nil
	}
	if strings.TrimSpace(message.ID) == "" {
		message.ID = "mailbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return runtimeevents.Event{}, 0, false, nil
	}
	defer conn.Close()
	txWriter, schema, detach, attached, err := agentcontrol.AttachGlobalMailboxSQLiteTx(ctx, conn, writer)
	if err != nil || !attached {
		return runtimeevents.Event{}, 0, false, nil
	}
	defer detach()

	s.mu.Lock()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		s.mu.Unlock()
		return runtimeevents.Event{}, 0, true, fmt.Errorf("begin agent control mailbox tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	record := mailboxRecordFromRuntimeMessage(sessionID, message)
	appended, err := txWriter.AppendPrimaryGlobalMailboxRecordTx(ctx, tx, schema, record)
	if err != nil {
		s.mu.Unlock()
		return runtimeevents.Event{}, 0, true, fmt.Errorf("append primary global runtime mailbox record: %w", err)
	}
	if appended.GlobalSeq > 0 {
		message.GlobalSeq = appended.GlobalSeq
	}
	if strings.TrimSpace(appended.MessageID) != "" {
		message.ID = appended.MessageID
	}
	result, err := s.appendAgentControlMailboxPrimaryTx(ctx, tx, sessionID, message)
	if err != nil {
		s.mu.Unlock()
		return result.event, result.mailboxSeq, true, err
	}
	refreshed, err := txWriter.AppendPrimaryGlobalMailboxRecordTx(ctx, tx, schema, mailboxRecordFromRuntimeMessage(sessionID, result.message))
	if err != nil {
		s.mu.Unlock()
		return result.event, result.mailboxSeq, true, fmt.Errorf("refresh primary global runtime mailbox record: %w", err)
	}
	if refreshed.GlobalSeq > 0 {
		appended = refreshed
		result.message.GlobalSeq = refreshed.GlobalSeq
	}
	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return result.event, result.mailboxSeq, true, fmt.Errorf("commit agent control mailbox tx: %w", err)
	}
	committed = true
	s.mu.Unlock()

	if notifier, ok := txWriter.(agentcontrol.GlobalMailboxWakeNotifier); ok {
		notifier.NotifyGlobalMailboxWake(appended)
	}
	controlMessage := cloneTeamMailMessage(result.message)
	controlMessage.Seq = result.controlSeq
	controlMessage.ControlSeq = result.controlSeq
	controlMessage.SessionMailboxSeq = result.mailboxSeq
	s.notifyAgentControlMailboxWatchers(sessionID, controlMessage)
	s.notifyMailboxWatchers(sessionID, result.message)
	s.notifyEventWatchers(result.eventSeq, result.event)
	return result.event, result.mailboxSeq, true, nil
}

// AppendAgentControlMailbox stores an AgentControl envelope mailbox message
// through the runtime store's canonical control-plane write surface, then keeps
// the legacy session mailbox/event rows as compatibility mirrors.
func (s *SQLiteRuntimeStore) AppendAgentControlMailbox(ctx context.Context, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, error) {
	if err := validateAgentControlMailboxMessage(message); err != nil {
		return runtimeevents.Event{}, 0, err
	}
	if s == nil || s.db == nil {
		return runtimeevents.Event{}, 0, fmt.Errorf("runtime store is not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return runtimeevents.Event{}, 0, fmt.Errorf("session id is required")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	if event, mailboxSeq, ok, err := s.appendAgentControlMailboxSameTx(ctx, sessionID, message); ok {
		return event, mailboxSeq, err
	}
	var globalPrimaryUsed bool
	var err error
	message, globalPrimaryUsed, err = s.appendPrimaryGlobalMailboxRecord(ctx, sessionID, message)
	if err != nil {
		return runtimeevents.Event{}, 0, err
	}
	s.mu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.mu.Unlock()
		return runtimeevents.Event{}, 0, fmt.Errorf("begin agent control mailbox tx: %w", err)
	}
	result, err := s.appendAgentControlMailboxPrimaryTx(ctx, tx, sessionID, message)
	if err != nil {
		_ = tx.Rollback()
		s.mu.Unlock()
		return result.event, result.mailboxSeq, err
	}
	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return result.event, result.mailboxSeq, fmt.Errorf("commit agent control mailbox tx: %w", err)
	}
	s.mu.Unlock()
	if globalPrimaryUsed {
		if refreshed, _, err := s.appendPrimaryGlobalMailboxRecord(ctx, sessionID, result.message); err != nil {
			return result.event, result.mailboxSeq, err
		} else if refreshed.GlobalSeq > 0 {
			result.message.GlobalSeq = refreshed.GlobalSeq
		}
	} else if globalSeq, _ := s.appendGlobalMailboxRecord(ctx, sessionID, result); globalSeq > 0 {
		result.message.GlobalSeq = globalSeq
	}
	controlMessage := cloneTeamMailMessage(result.message)
	controlMessage.Seq = result.controlSeq
	controlMessage.ControlSeq = result.controlSeq
	controlMessage.SessionMailboxSeq = result.mailboxSeq
	s.notifyAgentControlMailboxWatchers(sessionID, controlMessage)
	s.notifyMailboxWatchers(sessionID, result.message)
	s.notifyEventWatchers(result.eventSeq, result.event)
	return result.event, result.mailboxSeq, nil
}

type mailboxAppendTxResult struct {
	event      runtimeevents.Event
	message    team.MailMessage
	mailboxSeq int64
	controlSeq int64
	eventSeq   int64
}

func (s *SQLiteRuntimeStore) appendMailboxTx(ctx context.Context, tx *sql.Tx, sessionID string, message team.MailMessage) (mailboxAppendTxResult, error) {
	var result mailboxAppendTxResult
	var mailboxSeq int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(session_mailbox_seq), 0) + 1
		FROM agent_control_mailbox_records
		WHERE scope = ? AND session_id = ?
	`, agentcontrol.MailboxScopeSession, sessionID).Scan(&mailboxSeq); err != nil {
		return result, fmt.Errorf("next mailbox seq: %w", err)
	}
	message.Seq = mailboxSeq
	message.SessionMailboxSeq = mailboxSeq
	if strings.TrimSpace(message.ID) == "" {
		message.ID = fmt.Sprintf("mailbox_%d", mailboxSeq)
	}
	metadataJSON, err := json.Marshal(message.Metadata)
	if err != nil {
		result.mailboxSeq = mailboxSeq
		return result, fmt.Errorf("marshal mailbox metadata: %w", err)
	}
	recordSeq, err := insertRuntimeMailboxRecordTx(ctx, tx, sessionID, mailboxSeq, message, metadataJSON, message.GlobalSeq)
	if err != nil {
		result.mailboxSeq = mailboxSeq
		return result, err
	}
	controlSeq := int64(0)
	if agentcontrol.HasEnvelopeMetadata(message.Metadata) {
		controlSeq = recordSeq
	}
	event := NewMailboxReceivedEvent(sessionID, message)
	payloadJSON, err := json.Marshal(event.Payload)
	if err != nil {
		result.event = event
		result.mailboxSeq = mailboxSeq
		result.controlSeq = controlSeq
		return result, fmt.Errorf("marshal mailbox event payload: %w", err)
	}
	var eventSeq int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1 FROM session_events WHERE session_id = ?
	`, sessionID).Scan(&eventSeq); err != nil {
		result.event = event
		result.mailboxSeq = mailboxSeq
		result.controlSeq = controlSeq
		return result, fmt.Errorf("next mailbox event seq: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_events (
			session_id, seq, type, trace_id, agent_name, tool_name, payload_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, sessionID, eventSeq, event.Type, nullIfEmpty(event.TraceID), nullIfEmpty(event.AgentName),
		nullIfEmpty(event.ToolName), string(payloadJSON), event.Timestamp.Format(time.RFC3339Nano))
	if err != nil {
		result.event = event
		result.mailboxSeq = mailboxSeq
		result.controlSeq = controlSeq
		result.eventSeq = eventSeq
		return result, fmt.Errorf("insert mailbox session event: %w", err)
	}
	if event.Payload == nil {
		event.Payload = map[string]interface{}{}
	}
	event.Payload["seq"] = eventSeq
	event.Payload["mailbox_seq"] = mailboxSeq
	result.event = event
	result.message = message
	result.mailboxSeq = mailboxSeq
	result.controlSeq = controlSeq
	result.eventSeq = eventSeq
	return result, nil
}

func (s *SQLiteRuntimeStore) appendAgentControlMailboxPrimaryTx(ctx context.Context, tx *sql.Tx, sessionID string, message team.MailMessage) (mailboxAppendTxResult, error) {
	var result mailboxAppendTxResult
	var mailboxSeq int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(session_mailbox_seq), 0) + 1
		FROM agent_control_mailbox_records
		WHERE scope = ? AND session_id = ?
	`, agentcontrol.MailboxScopeSession, sessionID).Scan(&mailboxSeq); err != nil {
		return result, fmt.Errorf("next session mailbox seq: %w", err)
	}
	message.Seq = mailboxSeq
	message.SessionMailboxSeq = mailboxSeq
	if strings.TrimSpace(message.ID) == "" {
		message.ID = fmt.Sprintf("mailbox_%d", mailboxSeq)
	}
	metadataJSON, err := json.Marshal(message.Metadata)
	if err != nil {
		result.mailboxSeq = mailboxSeq
		return result, fmt.Errorf("marshal agent control mailbox metadata: %w", err)
	}
	recordSeq, err := insertRuntimeMailboxRecordTx(ctx, tx, sessionID, mailboxSeq, message, metadataJSON, message.GlobalSeq)
	if err != nil {
		result.mailboxSeq = mailboxSeq
		return result, err
	}
	event := NewMailboxReceivedEvent(sessionID, message)
	payloadJSON, err := json.Marshal(event.Payload)
	if err != nil {
		result.event = event
		result.mailboxSeq = mailboxSeq
		result.controlSeq = recordSeq
		return result, fmt.Errorf("marshal mailbox event payload: %w", err)
	}
	var eventSeq int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1 FROM session_events WHERE session_id = ?
	`, sessionID).Scan(&eventSeq); err != nil {
		result.event = event
		result.mailboxSeq = mailboxSeq
		result.controlSeq = recordSeq
		return result, fmt.Errorf("next mailbox event seq: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_events (
			session_id, seq, type, trace_id, agent_name, tool_name, payload_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, sessionID, eventSeq, event.Type, nullIfEmpty(event.TraceID), nullIfEmpty(event.AgentName),
		nullIfEmpty(event.ToolName), string(payloadJSON), event.Timestamp.Format(time.RFC3339Nano)); err != nil {
		result.event = event
		result.mailboxSeq = mailboxSeq
		result.controlSeq = recordSeq
		result.eventSeq = eventSeq
		return result, fmt.Errorf("insert mailbox session event mirror: %w", err)
	}
	if event.Payload == nil {
		event.Payload = map[string]interface{}{}
	}
	event.Payload["seq"] = eventSeq
	event.Payload["mailbox_seq"] = mailboxSeq
	result.event = event
	result.message = message
	result.mailboxSeq = mailboxSeq
	result.controlSeq = recordSeq
	result.eventSeq = eventSeq
	return result, nil
}

func (s *SQLiteRuntimeStore) appendGlobalMailboxRecord(ctx context.Context, sessionID string, result mailboxAppendTxResult) (int64, error) {
	if s == nil {
		return 0, nil
	}
	s.mu.Lock()
	writer := s.globalMailboxWriter
	s.mu.Unlock()
	if writer == nil || result.controlSeq <= 0 {
		return 0, nil
	}
	message := cloneTeamMailMessage(result.message)
	message.ControlSeq = result.controlSeq
	message.SessionMailboxSeq = result.mailboxSeq
	record := mailboxRecordFromRuntimeMessage(sessionID, message)
	record.Seq = result.controlSeq
	record.SourceSeq = result.controlSeq
	globalSeq, err := writer.AppendGlobalMailboxRecord(ctx, agentcontrol.MailboxSourceRuntimeSessions, record)
	if err != nil {
		return 0, fmt.Errorf("append global runtime mailbox record: %w", err)
	}
	if globalSeq > 0 {
		if err := s.updateRuntimeMailboxGlobalSeq(ctx, sessionID, result.controlSeq, globalSeq); err != nil {
			return globalSeq, err
		}
	}
	return globalSeq, nil
}

func (s *SQLiteRuntimeStore) appendPrimaryGlobalMailboxRecord(ctx context.Context, sessionID string, message team.MailMessage) (team.MailMessage, bool, error) {
	if s == nil {
		return message, false, nil
	}
	s.mu.Lock()
	writer := s.globalMailboxWriter
	s.mu.Unlock()
	primary, ok := writer.(agentcontrol.GlobalMailboxPrimaryWriter)
	if !ok || primary == nil {
		return message, false, nil
	}
	if strings.TrimSpace(message.ID) == "" {
		message.ID = "mailbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	record := mailboxRecordFromRuntimeMessage(sessionID, message)
	appended, err := primary.AppendPrimaryGlobalMailboxRecord(ctx, record)
	if err != nil {
		return message, false, fmt.Errorf("append primary global runtime mailbox record: %w", err)
	}
	if appended.GlobalSeq > 0 {
		message.GlobalSeq = appended.GlobalSeq
	}
	if strings.TrimSpace(appended.MessageID) != "" {
		message.ID = appended.MessageID
	}
	return message, true, nil
}

func (s *SQLiteRuntimeStore) updateRuntimeMailboxGlobalSeq(ctx context.Context, sessionID string, controlSeq int64, globalSeq int64) error {
	if s == nil || s.db == nil || controlSeq <= 0 || globalSeq <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `
		UPDATE agent_control_mailbox_records
		SET global_seq = ?
		WHERE scope = ? AND session_id = ? AND id = ?
	`, globalSeq, agentcontrol.MailboxScopeSession, strings.TrimSpace(sessionID), controlSeq)
	if err != nil {
		return fmt.Errorf("update runtime mailbox global sequence: %w", err)
	}
	return nil
}

// RepairAgentControlMailboxProjection materializes local AgentControl mailbox
// rows that are missing a global registry backlink, then records the returned
// global sequence on each repaired local row.
func (s *SQLiteRuntimeStore) RepairAgentControlMailboxProjection(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("runtime store is not initialized")
	}
	s.mu.Lock()
	writer := s.globalMailboxWriter
	s.mu.Unlock()
	if writer == nil {
		return 0, nil
	}
	records, err := s.listUnprojectedRuntimeMailboxRecords(ctx, filter)
	if err != nil {
		return 0, err
	}
	var repaired int64
	for _, record := range records {
		record.SourceSeq = record.Seq
		globalSeq, err := writer.AppendGlobalMailboxRecord(ctx, agentcontrol.MailboxSourceRuntimeSessions, record)
		if err != nil {
			return repaired, fmt.Errorf("repair global runtime mailbox record: %w", err)
		}
		if globalSeq <= 0 {
			continue
		}
		if err := s.updateRuntimeMailboxGlobalSeq(ctx, record.SessionID, record.Seq, globalSeq); err != nil {
			return repaired, err
		}
		repaired++
	}
	return repaired, nil
}

func (s *SQLiteRuntimeStore) listUnprojectedRuntimeMailboxRecords(ctx context.Context, filter agentcontrol.MailboxRecordFilter) ([]agentcontrol.MailboxRecord, error) {
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeSession {
		return nil, nil
	}
	clauses := []string{"scope = ?", "global_seq = 0", "COALESCE(workflow, '') <> ''"}
	args := []interface{}{agentcontrol.MailboxScopeSession}
	if filter.SessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, filter.SessionID)
	}
	if filter.Workflow != "" {
		clauses = append(clauses, "workflow = ?")
		args = append(args, filter.Workflow)
	}
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
		return nil, fmt.Errorf("list unprojected runtime mailbox records: %w", err)
	}
	defer rows.Close()

	records := make([]agentcontrol.MailboxRecord, 0)
	for rows.Next() {
		var (
			record      agentcontrol.MailboxRecord
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
		if err := rows.Scan(&record.Seq, &record.GlobalSeq, &workflowRaw, &scopeRaw, &sessionID, &record.SessionMailboxSeq, &teamID, &record.TeamSeq, &record.MessageID,
			&fromAgent, &toAgent, &taskID, &kind, &record.Body, &metadataRaw, &createdRaw, &ackedRaw); err != nil {
			return nil, fmt.Errorf("scan unprojected runtime mailbox record: %w", err)
		}
		record.Workflow = workflowRaw.String
		record.Scope = scopeRaw.String
		record.SessionID = sessionID.String
		record.TeamID = teamID.String
		record.FromAgent = fromAgent.String
		record.ToAgent = toAgent.String
		record.TaskID = taskID.String
		record.Kind = kind.String
		if metadataRaw != "" {
			_ = json.Unmarshal([]byte(metadataRaw), &record.Metadata)
		}
		if record.Metadata == nil {
			record.Metadata = map[string]interface{}{}
		}
		if createdRaw != "" {
			record.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		}
		if ackedRaw.Valid {
			ackedAt, _ := time.Parse(time.RFC3339Nano, ackedRaw.String)
			record.AckedAt = &ackedAt
		}
		records = append(records, record.Normalize())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// RepairAgentControlMailboxLocalProjection backfills missing local runtime
// mailbox projection rows from the durable global registry.
func (s *SQLiteRuntimeStore) RepairAgentControlMailboxLocalProjection(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("runtime store is not initialized")
	}
	s.mu.Lock()
	writer := s.globalMailboxWriter
	s.mu.Unlock()
	reader, ok := writer.(agentcontrol.MailboxRegistryReader)
	if !ok || reader == nil {
		return 0, nil
	}
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeSession {
		return 0, nil
	}
	filter.Scope = agentcontrol.MailboxScopeSession
	records, err := reader.ListAgentControlMailboxRecords(ctx, filter)
	if err != nil {
		return 0, err
	}
	var repaired int64
	for _, record := range records {
		record = record.Normalize()
		if record.Scope != agentcontrol.MailboxScopeSession || record.SessionID == "" {
			continue
		}
		if record.GlobalSeq <= 0 {
			record.GlobalSeq = record.Seq
		}
		ok, err := s.repairRuntimeMailboxLocalProjectionRecord(ctx, record)
		if err != nil {
			return repaired, err
		}
		if ok {
			repaired++
		}
	}
	return repaired, nil
}

func (s *SQLiteRuntimeStore) repairRuntimeMailboxLocalProjectionRecord(ctx context.Context, record agentcontrol.MailboxRecord) (bool, error) {
	if record.GlobalSeq <= 0 {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin runtime mailbox local projection repair tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existing int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM agent_control_mailbox_records
		WHERE scope = ? AND global_seq = ?
	`, agentcontrol.MailboxScopeSession, record.GlobalSeq).Scan(&existing); err != nil {
		return false, fmt.Errorf("check runtime mailbox projection by global seq: %w", err)
	}
	if existing > 0 {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_control_mailbox_records
		SET global_seq = ?
		WHERE scope = ? AND session_id = ? AND message_id = ? AND global_seq = 0
	`, record.GlobalSeq, agentcontrol.MailboxScopeSession, record.SessionID, record.MessageID)
	if err != nil {
		return false, fmt.Errorf("repair runtime mailbox projection backlink: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit runtime mailbox projection backlink repair: %w", err)
		}
		return true, nil
	}

	mailboxSeq, err := nextRuntimeMailboxProjectionSeqTx(ctx, tx, record.SessionID, record.SessionMailboxSeq)
	if err != nil {
		return false, err
	}
	message := runtimeMessageFromMailboxRecord(record, mailboxSeq)
	metadataJSON, err := json.Marshal(message.Metadata)
	if err != nil {
		return false, fmt.Errorf("marshal runtime mailbox projection metadata: %w", err)
	}
	if _, err := insertRuntimeMailboxRecordTx(ctx, tx, record.SessionID, mailboxSeq, message, metadataJSON, record.GlobalSeq); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit runtime mailbox local projection repair: %w", err)
	}
	return true, nil
}

func nextRuntimeMailboxProjectionSeqTx(ctx context.Context, tx *sql.Tx, sessionID string, preferred int64) (int64, error) {
	if preferred > 0 {
		var used int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM agent_control_mailbox_records
			WHERE scope = ? AND session_id = ? AND session_mailbox_seq = ?
		`, agentcontrol.MailboxScopeSession, sessionID, preferred).Scan(&used); err != nil {
			return 0, fmt.Errorf("check runtime mailbox projection sequence: %w", err)
		}
		if used == 0 {
			return preferred, nil
		}
	}
	var next int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(session_mailbox_seq), 0) + 1
		FROM agent_control_mailbox_records
		WHERE scope = ? AND session_id = ?
	`, agentcontrol.MailboxScopeSession, sessionID).Scan(&next); err != nil {
		return 0, fmt.Errorf("next runtime mailbox projection sequence: %w", err)
	}
	return next, nil
}

func runtimeMessageFromMailboxRecord(record agentcontrol.MailboxRecord, mailboxSeq int64) team.MailMessage {
	metadata := map[string]interface{}{}
	for key, value := range record.Metadata {
		metadata[key] = value
	}
	if record.Workflow != "" && agentcontrol.MetadataString(metadata, agentcontrol.MetadataKeyWorkflow) == "" {
		metadata[agentcontrol.MetadataKeyWorkflow] = record.Workflow
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
	return team.MailMessage{
		ID:                record.MessageID,
		Seq:               mailboxSeq,
		GlobalSeq:         record.GlobalSeq,
		SessionMailboxSeq: mailboxSeq,
		TeamID:            record.TeamID,
		FromAgent:         record.FromAgent,
		ToAgent:           record.ToAgent,
		TaskID:            taskID,
		Kind:              record.Kind,
		Body:              record.Body,
		Metadata:          metadata,
		CreatedAt:         createdAt,
	}
}

func insertRuntimeMailboxRecordTx(ctx context.Context, tx *sql.Tx, sessionID string, mailboxSeq int64, message team.MailMessage, metadataJSON []byte, globalSeq int64) (int64, error) {
	taskID := interface{}(nil)
	if message.TaskID != nil && strings.TrimSpace(*message.TaskID) != "" {
		taskID = strings.TrimSpace(*message.TaskID)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_control_mailbox_records (
			scope, global_seq, workflow, session_id, session_mailbox_seq, team_id, team_seq, message_id, from_agent, to_agent, task_id,
			kind, body, metadata_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, agentcontrol.MailboxScopeSession,
		globalSeq,
		nullIfEmpty(agentcontrol.MetadataString(message.Metadata, agentcontrol.MetadataKeyWorkflow)),
		nullIfEmpty(sessionID), mailboxSeq, nullIfEmpty(message.TeamID), 0, strings.TrimSpace(message.ID),
		nullIfEmpty(message.FromAgent), nullIfEmpty(message.ToAgent), taskID, nullIfEmpty(message.Kind),
		strings.TrimSpace(message.Body), string(metadataJSON), message.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("insert runtime mailbox record: %w", err)
	}
	recordSeq, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("runtime mailbox record id: %w", err)
	}
	return recordSeq, nil
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

// ListMailbox returns mailbox messages after a given mailbox sequence.
func (s *SQLiteRuntimeStore) ListMailbox(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]team.MailMessage, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	query := `
		SELECT id, global_seq, session_mailbox_seq, message_id, team_id, from_agent, to_agent, task_id, kind, body, metadata_json, created_at
		FROM agent_control_mailbox_records
		WHERE scope = ? AND session_id = ? AND session_mailbox_seq > ?
		ORDER BY session_mailbox_seq ASC
	`
	args := []interface{}{agentcontrol.MailboxScopeSession, strings.TrimSpace(sessionID), afterSeq}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list session mailbox: %w", err)
	}
	defer rows.Close()

	messages := make([]team.MailMessage, 0)
	for rows.Next() {
		var (
			message           team.MailMessage
			recordSeq         int64
			globalSeq         int64
			sessionMailboxSeq int64
			teamID            sql.NullString
			fromAgent         sql.NullString
			toAgent           sql.NullString
			taskID            sql.NullString
			kind              sql.NullString
			metadata          string
			createdRaw        string
		)
		if err := rows.Scan(&recordSeq, &globalSeq, &sessionMailboxSeq, &message.ID, &teamID, &fromAgent, &toAgent, &taskID, &kind, &message.Body, &metadata, &createdRaw); err != nil {
			return nil, fmt.Errorf("scan session mailbox: %w", err)
		}
		message.Seq = sessionMailboxSeq
		message.SessionMailboxSeq = sessionMailboxSeq
		message.GlobalSeq = globalSeq
		message.TeamID = teamID.String
		message.FromAgent = fromAgent.String
		message.ToAgent = toAgent.String
		if taskID.Valid {
			task := taskID.String
			message.TaskID = &task
		}
		message.Kind = kind.String
		if metadata != "" {
			_ = json.Unmarshal([]byte(metadata), &message.Metadata)
		}
		if message.Metadata == nil {
			message.Metadata = map[string]interface{}{}
		}
		if createdRaw != "" {
			message.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		}
		if agentcontrol.HasEnvelopeMetadata(message.Metadata) {
			message.ControlSeq = recordSeq
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// ListAgentControlMailbox returns mailbox messages with AgentControl envelope metadata.
func (s *SQLiteRuntimeStore) ListAgentControlMailbox(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]team.MailMessage, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	query := `
		SELECT id, global_seq, session_mailbox_seq, message_id, team_id, from_agent, to_agent, task_id, kind, body, metadata_json, created_at
		FROM agent_control_mailbox_records
		WHERE scope = ? AND session_id = ? AND id > ? AND COALESCE(workflow, '') <> ''
		ORDER BY id ASC
	`
	args := []interface{}{agentcontrol.MailboxScopeSession, strings.TrimSpace(sessionID), afterSeq}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list agent control mailbox: %w", err)
	}
	defer rows.Close()

	messages := make([]team.MailMessage, 0)
	for rows.Next() {
		var (
			message           team.MailMessage
			globalSeq         int64
			sessionMailboxSeq int64
			teamID            sql.NullString
			fromAgent         sql.NullString
			toAgent           sql.NullString
			taskID            sql.NullString
			kind              sql.NullString
			metadata          string
			createdRaw        string
		)
		if err := rows.Scan(&message.ControlSeq, &globalSeq, &sessionMailboxSeq, &message.ID, &teamID, &fromAgent, &toAgent, &taskID, &kind, &message.Body, &metadata, &createdRaw); err != nil {
			return nil, fmt.Errorf("scan agent control mailbox: %w", err)
		}
		message.Seq = message.ControlSeq
		message.SessionMailboxSeq = sessionMailboxSeq
		message.GlobalSeq = globalSeq
		message.TeamID = teamID.String
		message.FromAgent = fromAgent.String
		message.ToAgent = toAgent.String
		if taskID.Valid {
			task := taskID.String
			message.TaskID = &task
		}
		message.Kind = kind.String
		if metadata != "" {
			_ = json.Unmarshal([]byte(metadata), &message.Metadata)
		}
		if message.Metadata == nil {
			message.Metadata = map[string]interface{}{}
		}
		if createdRaw != "" {
			message.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// ListAgentControlMailboxRecords projects runtime control mailbox rows into
// the shared AgentControl mailbox registry read model.
func (s *SQLiteRuntimeStore) ListAgentControlMailboxRecords(ctx context.Context, filter agentcontrol.MailboxRecordFilter) ([]agentcontrol.MailboxRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeSession {
		return nil, nil
	}
	if filter.SessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	clauses := []string{"scope = ?", "session_id = ?", "id > ?", "COALESCE(workflow, '') <> ''"}
	args := []interface{}{agentcontrol.MailboxScopeSession, filter.SessionID, filter.AfterSeq}
	if filter.Workflow != "" {
		clauses = append(clauses, "workflow = ?")
		args = append(args, filter.Workflow)
	}
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
		if err := rows.Scan(&record.Seq, &record.GlobalSeq, &workflowRaw, &scopeRaw, &sessionID, &record.SessionMailboxSeq, &teamID, &record.TeamSeq, &record.MessageID,
			&fromAgent, &toAgent, &taskID, &kind, &record.Body, &metadataRaw, &createdRaw, &ackedRaw); err != nil {
			return nil, fmt.Errorf("scan agent control mailbox record: %w", err)
		}
		record.Workflow = workflowRaw.String
		record.Scope = scopeRaw.String
		record.SessionID = sessionID.String
		record.TeamID = teamID.String
		record.FromAgent = fromAgent.String
		record.ToAgent = toAgent.String
		record.TaskID = taskID.String
		record.Kind = kind.String
		if metadataRaw != "" {
			_ = json.Unmarshal([]byte(metadataRaw), &record.Metadata)
		}
		if record.Metadata == nil {
			record.Metadata = map[string]interface{}{}
		}
		if record.Workflow == "" {
			record.Workflow = agentcontrol.MetadataString(record.Metadata, agentcontrol.MetadataKeyWorkflow)
		}
		if createdRaw != "" {
			record.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		}
		if ackedRaw.Valid {
			ackedAt, _ := time.Parse(time.RFC3339Nano, ackedRaw.String)
			record.AckedAt = &ackedAt
		}
		records = append(records, record.Normalize())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// WatchEvents registers an in-process notification channel for session events.
func (s *SQLiteRuntimeStore) WatchEvents(ctx context.Context, sessionID string) (<-chan runtimeevents.Event, func()) {
	ch := make(chan runtimeevents.Event, 1)
	if s == nil {
		return ch, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	s.watchMu.Lock()
	if s.eventWatchers == nil {
		s.eventWatchers = make(map[int64]eventWatcher)
	}
	s.nextWatchID++
	watchID := s.nextWatchID
	s.eventWatchers[watchID] = eventWatcher{sessionID: sessionID, ch: ch}
	s.watchMu.Unlock()

	cancel := func() {
		s.watchMu.Lock()
		if s.eventWatchers != nil {
			delete(s.eventWatchers, watchID)
		}
		s.watchMu.Unlock()
	}
	if ctx != nil {
		done := ctx.Done()
		if done != nil {
			go func() {
				<-done
				cancel()
			}()
		}
	}
	return ch, cancel
}

// WatchMailbox registers an in-process notification channel for session mailbox rows.
func (s *SQLiteRuntimeStore) WatchMailbox(ctx context.Context, sessionID string) (<-chan team.MailMessage, func()) {
	ch := make(chan team.MailMessage, 1)
	if s == nil {
		return ch, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	s.watchMu.Lock()
	if s.mailWatchers == nil {
		s.mailWatchers = make(map[int64]mailboxWatcher)
	}
	s.nextWatchID++
	watchID := s.nextWatchID
	s.mailWatchers[watchID] = mailboxWatcher{sessionID: sessionID, ch: ch}
	s.watchMu.Unlock()

	cancel := func() {
		s.watchMu.Lock()
		if s.mailWatchers != nil {
			delete(s.mailWatchers, watchID)
		}
		s.watchMu.Unlock()
	}
	if ctx != nil {
		done := ctx.Done()
		if done != nil {
			go func() {
				<-done
				cancel()
			}()
		}
	}
	return ch, cancel
}

// WatchAgentControlMailbox registers notifications for AgentControl mailbox rows.
func (s *SQLiteRuntimeStore) WatchAgentControlMailbox(ctx context.Context, sessionID string) (<-chan team.MailMessage, func()) {
	ch := make(chan team.MailMessage, 1)
	if s == nil {
		return ch, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	s.watchMu.Lock()
	if s.controlWatchers == nil {
		s.controlWatchers = make(map[int64]mailboxWatcher)
	}
	s.nextWatchID++
	watchID := s.nextWatchID
	s.controlWatchers[watchID] = mailboxWatcher{sessionID: sessionID, ch: ch}
	s.watchMu.Unlock()

	cancel := func() {
		s.watchMu.Lock()
		if s.controlWatchers != nil {
			delete(s.controlWatchers, watchID)
		}
		s.watchMu.Unlock()
	}
	if ctx != nil {
		done := ctx.Done()
		if done != nil {
			go func() {
				<-done
				cancel()
			}()
		}
	}
	return ch, func() {
		cancel()
	}
}

// LastEventSeq returns the current durable event high-water mark for a session.
func (s *SQLiteRuntimeStore) LastEventSeq(ctx context.Context, sessionID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("runtime store is not initialized")
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0)
		FROM session_events
		WHERE session_id = ?
	`, strings.TrimSpace(sessionID)).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("last session event sequence: %w", err)
	}
	return seq, nil
}

// LastMailboxSeq returns the current durable mailbox high-water mark for a session.
func (s *SQLiteRuntimeStore) LastMailboxSeq(ctx context.Context, sessionID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("runtime store is not initialized")
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(session_mailbox_seq), 0)
		FROM agent_control_mailbox_records
		WHERE scope = ? AND session_id = ?
	`, agentcontrol.MailboxScopeSession, strings.TrimSpace(sessionID)).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("last session mailbox sequence: %w", err)
	}
	return seq, nil
}

// LastAgentControlMailboxSeq returns the high-water mark for AgentControl mailbox rows.
func (s *SQLiteRuntimeStore) LastAgentControlMailboxSeq(ctx context.Context, sessionID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("runtime store is not initialized")
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(id), 0)
		FROM agent_control_mailbox_records
		WHERE scope = ? AND session_id = ? AND COALESCE(workflow, '') <> ''
	`, agentcontrol.MailboxScopeSession, strings.TrimSpace(sessionID)).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("last agent control mailbox sequence: %w", err)
	}
	return seq, nil
}

// LastAgentControlMailboxRecordSeq returns the shared AgentControl mailbox
// registry high-water mark for runtime/session rows.
func (s *SQLiteRuntimeStore) LastAgentControlMailboxRecordSeq(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("runtime store is not initialized")
	}
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeSession {
		return 0, nil
	}
	if filter.SessionID == "" {
		return 0, fmt.Errorf("session id is required")
	}
	clauses := []string{"scope = ?", "session_id = ?", "COALESCE(workflow, '') <> ''"}
	args := []interface{}{agentcontrol.MailboxScopeSession, filter.SessionID}
	if filter.Workflow != "" {
		clauses = append(clauses, "workflow = ?")
		args = append(args, filter.Workflow)
	}
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

func (s *SQLiteRuntimeStore) notifyEventWatchers(seq int64, event runtimeevents.Event) {
	if s == nil {
		return
	}
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return
	}
	event = cloneRuntimeEvent(event)
	if event.Payload == nil {
		event.Payload = map[string]interface{}{}
	}
	event.Payload["seq"] = seq
	s.watchMu.Lock()
	watchers := make([]eventWatcher, 0, len(s.eventWatchers))
	for _, watcher := range s.eventWatchers {
		if watcher.ch == nil {
			continue
		}
		if watcher.sessionID != "" && !strings.EqualFold(watcher.sessionID, sessionID) {
			continue
		}
		watchers = append(watchers, watcher)
	}
	s.watchMu.Unlock()
	for _, watcher := range watchers {
		select {
		case watcher.ch <- event:
		default:
		}
	}
}

func (s *SQLiteRuntimeStore) notifyMailboxWatchers(sessionID string, message team.MailMessage) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	message = cloneTeamMailMessage(message)
	s.watchMu.Lock()
	watchers := make([]mailboxWatcher, 0, len(s.mailWatchers))
	for _, watcher := range s.mailWatchers {
		if watcher.ch == nil {
			continue
		}
		if watcher.sessionID != "" && !strings.EqualFold(watcher.sessionID, sessionID) {
			continue
		}
		watchers = append(watchers, watcher)
	}
	s.watchMu.Unlock()
	for _, watcher := range watchers {
		select {
		case watcher.ch <- message:
		default:
		}
	}
}

func (s *SQLiteRuntimeStore) notifyAgentControlMailboxWatchers(sessionID string, message team.MailMessage) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	message = cloneTeamMailMessage(message)
	s.watchMu.Lock()
	watchers := make([]mailboxWatcher, 0, len(s.controlWatchers))
	for _, watcher := range s.controlWatchers {
		if watcher.ch == nil {
			continue
		}
		if watcher.sessionID != "" && !strings.EqualFold(watcher.sessionID, sessionID) {
			continue
		}
		watchers = append(watchers, watcher)
	}
	s.watchMu.Unlock()
	for _, watcher := range watchers {
		select {
		case watcher.ch <- message:
		default:
		}
	}
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
		{
			Version: 8,
			Name:    "session_runtime_state_frozen_turn_tools",
			UpSQL: `
				ALTER TABLE session_runtime_state ADD COLUMN frozen_turn_tools_json BLOB;
			`,
		},
		{
			Version: 9,
			Name:    "session_mailbox_messages",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS session_mailbox_messages (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					session_id TEXT NOT NULL,
					seq INTEGER NOT NULL,
					message_id TEXT NOT NULL,
					team_id TEXT,
					from_agent TEXT,
					to_agent TEXT,
					task_id TEXT,
					kind TEXT,
					body TEXT NOT NULL,
					metadata_json BLOB NOT NULL DEFAULT '{}',
					created_at TEXT NOT NULL,
					UNIQUE(session_id, seq)
				);
				CREATE INDEX IF NOT EXISTS idx_session_mailbox_session_seq
				ON session_mailbox_messages(session_id, seq);
				CREATE INDEX IF NOT EXISTS idx_session_mailbox_session_message
				ON session_mailbox_messages(session_id, message_id);
			`,
		},
		{
			Version: 10,
			Name:    "agent_control_mailbox_messages",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS agent_control_mailbox_messages (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					session_id TEXT NOT NULL,
					session_mailbox_seq INTEGER NOT NULL,
					message_id TEXT NOT NULL,
					team_id TEXT,
					from_agent TEXT,
					to_agent TEXT,
					task_id TEXT,
					kind TEXT,
					message_type TEXT,
					control_action TEXT,
					workflow TEXT,
					mailbox_delivery TEXT,
					mailbox_kind TEXT,
					body TEXT NOT NULL,
					metadata_json BLOB NOT NULL DEFAULT '{}',
					created_at TEXT NOT NULL,
					UNIQUE(session_id, session_mailbox_seq)
				);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_session_seq
				ON agent_control_mailbox_messages(session_id, session_mailbox_seq);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_message
				ON agent_control_mailbox_messages(message_id);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_workflow_action
				ON agent_control_mailbox_messages(workflow, control_action);
			`,
		},
		{
			Version: 11,
			Name:    "agent_control_mailbox_sequence",
			UpSQL: `
				ALTER TABLE agent_control_mailbox_messages ADD COLUMN seq INTEGER NOT NULL DEFAULT 0;
				UPDATE agent_control_mailbox_messages
				SET seq = (
					SELECT COUNT(*)
					FROM agent_control_mailbox_messages AS prior
					WHERE prior.session_id = agent_control_mailbox_messages.session_id
						AND prior.session_mailbox_seq <= agent_control_mailbox_messages.session_mailbox_seq
				)
				WHERE seq = 0;
				CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_control_mailbox_session_control_seq_unique
				ON agent_control_mailbox_messages(session_id, seq);
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_session_mailbox_seq
				ON agent_control_mailbox_messages(session_id, session_mailbox_seq);
			`,
		},
		{
			Version: 12,
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
					from_agent, to_agent, task_id, kind, body, metadata_json, created_at
				)
				SELECT
					'session',
					workflow,
					session_id,
					session_mailbox_seq,
					team_id,
					0,
					message_id,
					from_agent,
					to_agent,
					task_id,
					kind,
					body,
					metadata_json,
					created_at
				FROM agent_control_mailbox_messages
				ORDER BY session_id ASC, seq ASC, id ASC;
			`,
		},
		{
			Version: 13,
			Name:    "drop_agent_control_mailbox_scoped_mirror",
			UpSQL: `
				DROP TABLE IF EXISTS agent_control_mailbox_messages;
			`,
		},
		{
			Version: 14,
			Name:    "drop_session_mailbox_legacy_mirror",
			UpSQL: `
				INSERT INTO agent_control_mailbox_records (
					scope, workflow, session_id, session_mailbox_seq, team_id, team_seq, message_id,
					from_agent, to_agent, task_id, kind, body, metadata_json, created_at
				)
				SELECT
					'session',
					NULL,
					session_id,
					seq,
					team_id,
					0,
					message_id,
					from_agent,
					to_agent,
					task_id,
					kind,
					body,
					metadata_json,
					created_at
				FROM session_mailbox_messages legacy
				WHERE NOT EXISTS (
					SELECT 1
					FROM agent_control_mailbox_records records
					WHERE records.scope = 'session'
						AND records.session_id = legacy.session_id
						AND records.session_mailbox_seq = legacy.seq
				)
				ORDER BY session_id ASC, seq ASC, id ASC;
				DROP TABLE IF EXISTS session_mailbox_messages;
			`,
		},
		{
			Version: 15,
			Name:    "agent_control_mailbox_global_projection",
			UpSQL: `
				ALTER TABLE agent_control_mailbox_records ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
				CREATE INDEX IF NOT EXISTS idx_agent_control_mailbox_records_global_seq
				ON agent_control_mailbox_records(global_seq);
			`,
		},
		{
			Version: 16,
			Name:    "session_actor_leases",
			UpSQL: `
				CREATE TABLE IF NOT EXISTS session_actor_leases (
					session_id TEXT PRIMARY KEY,
					owner_id TEXT NOT NULL,
					owner_kind TEXT NOT NULL,
					pid INTEGER,
					hostname TEXT,
					acquired_at TEXT NOT NULL,
					expires_at TEXT NOT NULL,
					heartbeat_at TEXT NOT NULL
				);
				CREATE INDEX IF NOT EXISTS idx_session_actor_leases_expires_at
				ON session_actor_leases(expires_at);
			`,
		},
		{
			Version: 17,
			Name:    "session_runtime_state_stable_tool_surface",
			UpSQL: `
				ALTER TABLE session_runtime_state ADD COLUMN stable_tool_surface_json BLOB;
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

func getSessionLeaseTx(ctx context.Context, tx *sql.Tx, sessionID string) (*SessionLease, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction is nil")
	}
	lease, err := scanSessionLease(tx.QueryRowContext(ctx, `
		SELECT session_id, owner_id, owner_kind, pid, hostname, acquired_at, expires_at, heartbeat_at
		FROM session_actor_leases
		WHERE session_id = ?
	`, strings.TrimSpace(sessionID)))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session lease: %w", err)
	}
	return lease, nil
}

func buildLeaseFromRequest(req LeaseRequest) (*SessionLease, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	ownerID := strings.TrimSpace(req.OwnerID)
	if sessionID == "" || ownerID == "" {
		return nil, fmt.Errorf("session id and owner id are required")
	}
	ownerKind := strings.TrimSpace(req.OwnerKind)
	if ownerKind == "" {
		ownerKind = "unknown"
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = defaultSessionLeaseTTL
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	return &SessionLease{
		SessionID:   sessionID,
		OwnerID:     ownerID,
		OwnerKind:   ownerKind,
		PID:         req.PID,
		Hostname:    strings.TrimSpace(req.Hostname),
		AcquiredAt:  now,
		HeartbeatAt: now,
		ExpiresAt:   now.Add(ttl),
	}, nil
}

func leaseExpired(lease *SessionLease, now time.Time) bool {
	if lease == nil {
		return true
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return !lease.ExpiresAt.After(now.UTC())
}

func cloneSessionLease(lease *SessionLease) *SessionLease {
	if lease == nil {
		return nil
	}
	cloned := *lease
	return &cloned
}

func scanSessionLease(scanner interface {
	Scan(dest ...interface{}) error
}) (*SessionLease, error) {
	var lease SessionLease
	var acquiredAt, expiresAt, heartbeatAt string
	var pid sql.NullInt64
	var hostname sql.NullString
	if err := scanner.Scan(&lease.SessionID, &lease.OwnerID, &lease.OwnerKind, &pid, &hostname, &acquiredAt, &expiresAt, &heartbeatAt); err != nil {
		return nil, err
	}
	if pid.Valid {
		lease.PID = int(pid.Int64)
	}
	if hostname.Valid {
		lease.Hostname = hostname.String
	}
	lease.AcquiredAt = parseRFC3339Time(acquiredAt)
	lease.ExpiresAt = parseRFC3339Time(expiresAt)
	lease.HeartbeatAt = parseRFC3339Time(heartbeatAt)
	return &lease, nil
}

func parseRFC3339Time(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
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
