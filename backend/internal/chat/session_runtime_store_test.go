package chat

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type failingGlobalMailboxWriter struct{}

func (failingGlobalMailboxWriter) AppendGlobalMailboxRecord(context.Context, string, agentcontrol.MailboxRecord) (int64, error) {
	return 0, fmt.Errorf("global mailbox writer unavailable")
}

type failingRenewLeaseStore struct {
	mu        sync.Mutex
	renewals  int
	releases  int
	renewErr  error
	sessionID string
	ownerID   string
}

func (s *failingRenewLeaseStore) AcquireLease(_ context.Context, req LeaseRequest) (*SessionLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = req.SessionID
	s.ownerID = req.OwnerID
	now := time.Now().UTC()
	return &SessionLease{
		SessionID:   req.SessionID,
		OwnerID:     req.OwnerID,
		OwnerKind:   req.OwnerKind,
		AcquiredAt:  now,
		ExpiresAt:   now.Add(req.TTL),
		HeartbeatAt: now,
	}, nil
}

func (s *failingRenewLeaseStore) RenewLease(context.Context, string, string, time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewals++
	return s.renewErr
}

func (s *failingRenewLeaseStore) ReleaseLease(context.Context, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases++
	return nil
}

func (s *failingRenewLeaseStore) GetLease(context.Context, string) (*SessionLease, error) {
	return nil, nil
}

func (s *failingRenewLeaseStore) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewals, s.releases
}

func assertSQLiteTableMissing(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name = ?
	`, table).Scan(&count))
	assert.Equal(t, 0, count)
}

func TestInMemoryRuntimeStoreSessionLeaseLifecycle(t *testing.T) {
	store := NewInMemoryRuntimeStore(16)
	ctx := context.Background()
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)

	lease, err := store.AcquireLease(ctx, LeaseRequest{
		SessionID: "session-lease",
		OwnerID:   "owner-1",
		OwnerKind: "test",
		Now:       now,
		TTL:       time.Minute,
	})
	require.NoError(t, err)
	require.Equal(t, "owner-1", lease.OwnerID)

	_, err = store.AcquireLease(ctx, LeaseRequest{
		SessionID: "session-lease",
		OwnerID:   "owner-2",
		OwnerKind: "test",
		Now:       now.Add(time.Second),
		TTL:       time.Minute,
	})
	var conflict *LeaseConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "owner-1", conflict.Lease.OwnerID)

	lease, err = store.AcquireLease(ctx, LeaseRequest{
		SessionID: "session-lease",
		OwnerID:   "owner-2",
		OwnerKind: "test",
		Now:       now.Add(2 * time.Minute),
		TTL:       time.Minute,
	})
	require.NoError(t, err)
	require.Equal(t, "owner-2", lease.OwnerID)

	err = store.RenewLease(ctx, "session-lease", "owner-1", time.Minute)
	conflict = nil
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "owner-2", conflict.Lease.OwnerID)

	require.NoError(t, store.ReleaseLease(ctx, "session-lease", "owner-2"))
	lease, err = store.GetLease(ctx, "session-lease")
	require.NoError(t, err)
	require.Nil(t, lease)
}

func TestSQLiteRuntimeStoreSessionLeaseLifecycle(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "runtime.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		SessionID: "session-lease",
		OwnerID:   "owner-1",
		OwnerKind: "test",
		PID:       123,
		Hostname:  "host-a",
		Now:       now,
		TTL:       time.Minute,
	})
	require.NoError(t, err)
	require.Equal(t, "owner-1", lease.OwnerID)
	require.Equal(t, now, lease.AcquiredAt)

	reentrant, err := store.AcquireLease(ctx, LeaseRequest{
		SessionID: "session-lease",
		OwnerID:   "owner-1",
		OwnerKind: "test",
		Now:       now.Add(time.Second),
		TTL:       time.Minute,
	})
	require.NoError(t, err)
	require.Equal(t, lease.AcquiredAt, reentrant.AcquiredAt)

	_, err = store.AcquireLease(ctx, LeaseRequest{
		SessionID: "session-lease",
		OwnerID:   "owner-2",
		OwnerKind: "test",
		Now:       now.Add(2 * time.Second),
		TTL:       time.Minute,
	})
	var conflict *LeaseConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "owner-1", conflict.Lease.OwnerID)

	taken, err := store.AcquireLease(ctx, LeaseRequest{
		SessionID: "session-lease",
		OwnerID:   "owner-2",
		OwnerKind: "test",
		Now:       now.Add(2 * time.Minute),
		TTL:       time.Minute,
	})
	require.NoError(t, err)
	require.Equal(t, "owner-2", taken.OwnerID)

	err = store.RenewLease(ctx, "session-lease", "owner-1", time.Minute)
	conflict = nil
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "owner-2", conflict.Lease.OwnerID)

	loaded, err := store.GetLease(ctx, "session-lease")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, "owner-2", loaded.OwnerID)

	require.NoError(t, store.ReleaseLease(ctx, "session-lease", "owner-2"))
	loaded, err = store.GetLease(ctx, "session-lease")
	require.NoError(t, err)
	require.Nil(t, loaded)
}

func TestSessionLeaseHandleStopsRenewingAfterRenewError(t *testing.T) {
	store := &failingRenewLeaseStore{renewErr: fmt.Errorf("database closed")}
	handle, err := AcquireSessionLease(context.Background(), store, LeaseRequest{
		SessionID: "session-renew-error",
		OwnerID:   "owner-renew-error",
		OwnerKind: "test",
		TTL:       30 * time.Millisecond,
	})
	require.NoError(t, err)
	time.Sleep(80 * time.Millisecond)
	require.NoError(t, handle.Release(context.Background()))

	renewals, releases := store.counts()
	require.Equal(t, 1, renewals)
	require.Equal(t, 1, releases)
}

func TestSQLiteRuntimeStorePersistsCurrentRunMeta(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-current-run-meta-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID: "session-1",
		Status:    SessionWaitingInput,
		CurrentRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID:        "team-1",
				AgentID:       "mate-1",
				CurrentTaskID: "task-1",
			},
		},
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.CurrentRunMeta)
	require.NotNil(t, loaded.CurrentRunMeta.Team)
	assert.Equal(t, "team-1", loaded.CurrentRunMeta.Team.TeamID)
	assert.Equal(t, "mate-1", loaded.CurrentRunMeta.Team.AgentID)
	assert.Equal(t, "task-1", loaded.CurrentRunMeta.Team.CurrentTaskID)
}

func TestSQLiteRuntimeStorePersistsAmbientRunMeta(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-ambient-run-meta-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID: "session-ambient-1",
		Status:    SessionIdle,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID:        "team-ambient",
				AgentID:       "lead",
				CurrentTaskID: "",
			},
		},
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-ambient-1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.AmbientRunMeta)
	require.NotNil(t, loaded.AmbientRunMeta.Team)
	assert.Equal(t, "team-ambient", loaded.AmbientRunMeta.Team.TeamID)
	assert.Equal(t, "lead", loaded.AmbientRunMeta.Team.AgentID)
	assert.Equal(t, SessionIdle, loaded.Status)
}

func TestSQLiteRuntimeStoreRepairsMailboxGlobalProjection(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-mailbox-repair-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	sessionID := "session-mailbox-repair"
	_, _, err = store.AppendAgentControlMailbox(ctx, sessionID, team.MailMessage{
		ID:        "mailbox-repair-runtime",
		FromAgent: "lead",
		ToAgent:   "worker",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "repair runtime backlink",
		Metadata: agentcontrol.Envelope{
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
	})
	require.NoError(t, err)

	records, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, int64(0), records[0].GlobalSeq)

	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = globalStore.Close() })
	store.SetGlobalMailboxWriter(globalStore)

	repaired, err := store.RepairAgentControlMailboxProjection(ctx, agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), repaired)

	globalRecords, err := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Len(t, globalRecords, 1)
	require.Equal(t, "mailbox-repair-runtime", globalRecords[0].MessageID)

	records, err = store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, globalRecords[0].Seq, records[0].GlobalSeq)
}

func TestSQLiteRuntimeStoreWriteThroughFailureKeepsLocalProjectionRepairable(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-mailbox-write-through-failure-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	store.SetGlobalMailboxWriter(failingGlobalMailboxWriter{})

	sessionID := "session-write-through-failure"
	_, _, err = store.AppendAgentControlMailbox(ctx, sessionID, team.MailMessage{
		ID:        "mailbox-write-through-failure-runtime",
		FromAgent: "lead",
		ToAgent:   "worker",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "repairable runtime local row",
		Metadata: agentcontrol.Envelope{
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
	})
	require.NoError(t, err)
	records, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, int64(0), records[0].GlobalSeq)

	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = globalStore.Close() })
	store.SetGlobalMailboxWriter(globalStore)
	repaired, err := store.RepairAgentControlMailboxProjection(ctx, agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), repaired)
	globalRecords, err := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Len(t, globalRecords, 1)
	require.Equal(t, "mailbox-write-through-failure-runtime", globalRecords[0].MessageID)
}

func TestSQLiteRuntimeStoreRepairsMailboxLocalProjectionFromGlobal(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-mailbox-local-repair-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = globalStore.Close() })
	store.SetGlobalMailboxWriter(globalStore)

	sessionID := "session-local-repair"
	globalRecord, err := globalStore.AppendPrimaryGlobalMailboxRecord(ctx, agentcontrol.MailboxRecord{
		Workflow:          agentcontrol.WorkflowSpawnAgent,
		Scope:             agentcontrol.MailboxScopeSession,
		SessionID:         sessionID,
		SessionMailboxSeq: 4,
		MessageID:         "global-only-runtime",
		FromAgent:         "child",
		ToAgent:           "parent",
		Kind:              agentcontrol.MailboxKindAgentMessage,
		Body:              "global only",
		Metadata: agentcontrol.Envelope{
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
		CreatedAt: time.Unix(30, 0).UTC(),
	})
	require.NoError(t, err)

	localRecords, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Empty(t, localRecords)

	repaired, err := store.RepairAgentControlMailboxLocalProjection(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), repaired)

	localRecords, err = store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Len(t, localRecords, 1)
	require.Equal(t, globalRecord.Seq, localRecords[0].GlobalSeq)
	require.Equal(t, int64(4), localRecords[0].SessionMailboxSeq)

	repaired, err = store.RepairAgentControlMailboxLocalProjection(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), repaired)
}

func TestSQLiteRuntimeStoreAppendAgentControlMailboxCanCommitGlobalAndLocalInOneTx(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: filepath.Join(dir, "runtime.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(dir, "agent-control.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = globalStore.Close() })
	store.SetGlobalMailboxWriter(globalStore)

	_, _, err = store.AppendAgentControlMailbox(ctx, "session-atomic", team.MailMessage{
		ID:        "atomic-runtime-mailbox",
		FromAgent: "child",
		ToAgent:   "parent",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "atomic runtime mailbox",
		Metadata: agentcontrol.Envelope{
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
	})
	require.NoError(t, err)

	localRecords, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		SessionID: "session-atomic",
	})
	require.NoError(t, err)
	require.Len(t, localRecords, 1)
	require.Greater(t, localRecords[0].GlobalSeq, int64(0))
	globalRecords, err := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		SessionID: "session-atomic",
	})
	require.NoError(t, err)
	require.Len(t, globalRecords, 1)
	require.Equal(t, globalRecords[0].Seq, localRecords[0].GlobalSeq)
	require.Equal(t, "atomic-runtime-mailbox", globalRecords[0].MessageID)
}

func TestSQLiteRuntimeStoreAppendEventIsSerialized(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime-events.db")
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	const (
		sessionID = "session-event-serial"
		total     = 32
	)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, appendErr := store.AppendEvent(context.Background(), runtimeevents.Event{
				Type:      "tool.completed",
				SessionID: sessionID,
				ToolName:  "read_a",
				Payload: map[string]interface{}{
					"index": index,
				},
			})
			if appendErr != nil {
				errCh <- appendErr
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(errCh)

	for appendErr := range errCh {
		require.NoError(t, appendErr)
	}

	events, err := store.ListEvents(context.Background(), sessionID, 0, 0)
	require.NoError(t, err)
	require.Len(t, events, total)
	for idx, event := range events {
		require.NotNil(t, event.Payload)
		assert.Equal(t, int64(idx+1), event.Payload["seq"])
	}
}

func TestInMemoryRuntimeStoreWatchEventsAndLastSeq(t *testing.T) {
	store := NewInMemoryRuntimeStore(16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watch, unwatch := store.WatchEvents(ctx, "session-watch")
	defer unwatch()
	seq, err := store.AppendEvent(ctx, runtimeevents.Event{
		Type:      EventMailboxReceived,
		SessionID: "session-watch",
		Payload:   map[string]interface{}{"kind": "agent_message"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)

	select {
	case event := <-watch:
		assert.Equal(t, EventMailboxReceived, event.Type)
		assert.Equal(t, "session-watch", event.SessionID)
		assert.Equal(t, int64(1), event.Payload["seq"])
	case <-time.After(time.Second):
		t.Fatal("event watcher did not wake")
	}
	lastSeq, err := store.LastEventSeq(ctx, "session-watch")
	require.NoError(t, err)
	assert.Equal(t, int64(1), lastSeq)
}

func TestSQLiteRuntimeStoreMailboxProjectionStatus(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: filepath.Join(t.TempDir(), "runtime.sqlite")})
	require.NoError(t, err)
	defer store.Close()

	status := store.AgentControlMailboxProjectionStatus()
	require.Equal(t, agentcontrol.MailboxProjectionModeLocalOnly, status.Mode)
	require.Equal(t, "global_writer_not_configured", status.Reason)

	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global.sqlite"),
	})
	require.NoError(t, err)
	defer globalStore.Close()
	store.SetGlobalMailboxWriter(globalStore)
	status = store.AgentControlMailboxProjectionStatus()
	require.Equal(t, agentcontrol.MailboxProjectionModeTransactional, status.Mode)
	require.True(t, status.Transactional)
	require.Equal(t, "global_registry_attachable", status.Reason)
}

func TestInMemoryRuntimeStoreMailboxProjectionStatus(t *testing.T) {
	store := NewInMemoryRuntimeStore(16)
	status := store.AgentControlMailboxProjectionStatus()
	require.Equal(t, agentcontrol.MailboxProjectionModeLocalOnly, status.Mode)

	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global.sqlite"),
	})
	require.NoError(t, err)
	defer globalStore.Close()
	store.SetGlobalMailboxWriter(globalStore)
	status = store.AgentControlMailboxProjectionStatus()
	require.Equal(t, agentcontrol.MailboxProjectionModeWriteThrough, status.Mode)
	require.Equal(t, "in_memory_store_cannot_share_sqlite_transaction", status.Reason)
}

func TestInMemoryRuntimeStoreAppendMailbox(t *testing.T) {
	store := NewInMemoryRuntimeStore(16)
	_, err := store.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      "assistant_message",
		SessionID: "session-mailbox",
		Payload:   map[string]interface{}{"content": "before mailbox"},
	})
	require.NoError(t, err)
	event, seq, err := store.AppendMailbox(context.Background(), "session-mailbox", team.MailMessage{
		FromAgent: "parent",
		ToAgent:   "child",
		Kind:      "agent_message",
		Body:      "hello mailbox",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)
	assert.Equal(t, EventMailboxReceived, event.Type)
	assert.Equal(t, int64(2), event.Payload["seq"])
	assert.Equal(t, int64(1), event.Payload["mailbox_seq"])

	events, err := store.ListEvents(context.Background(), "session-mailbox", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, EventMailboxReceived, events[1].Type)
	assert.Equal(t, "hello mailbox", events[1].Payload["body"])
	assert.Equal(t, int64(1), events[1].Payload["mailbox_seq"])
}

func TestInMemoryRuntimeStoreAgentControlMailboxFiltersEnvelope(t *testing.T) {
	store := NewInMemoryRuntimeStore(16)
	_, _, err := store.AppendMailbox(context.Background(), "session-mailbox", team.MailMessage{
		FromAgent: "parent",
		ToAgent:   "child",
		Kind:      "info",
		Body:      "non control mailbox",
	})
	require.NoError(t, err)
	_, _, err = store.AppendAgentControlMailbox(context.Background(), "session-mailbox", team.MailMessage{
		FromAgent: "parent",
		ToAgent:   "child",
		Kind:      "info",
		Body:      "missing envelope",
	})
	require.Error(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch, unwatch := store.WatchAgentControlMailbox(ctx, "session-mailbox")
	defer unwatch()

	_, seq, err := store.AppendAgentControlMailbox(context.Background(), "session-mailbox", team.MailMessage{
		FromAgent: "parent",
		ToAgent:   "child",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "control mailbox",
		Metadata: agentcontrol.Envelope{
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), seq)

	messages, err := store.ListAgentControlMailbox(context.Background(), "session-mailbox", 0, 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, int64(1), messages[0].Seq)
	assert.Equal(t, int64(1), messages[0].ControlSeq)
	assert.Equal(t, int64(2), messages[0].SessionMailboxSeq)
	assert.Equal(t, "control mailbox", messages[0].Body)

	lastSeq, err := store.LastAgentControlMailboxSeq(context.Background(), "session-mailbox")
	require.NoError(t, err)
	assert.Equal(t, int64(1), lastSeq)

	select {
	case message := <-watch:
		assert.Equal(t, int64(1), message.Seq)
		assert.Equal(t, int64(1), message.ControlSeq)
		assert.Equal(t, int64(2), message.SessionMailboxSeq)
		assert.Equal(t, "control mailbox", message.Body)
	case <-time.After(time.Second):
		t.Fatal("agent control mailbox watcher did not wake")
	}
}

func TestInMemoryRuntimeStoreAppendAgentControlMailboxWritesGlobalRegistry(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(16)
	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = globalStore.Close() })
	store.SetGlobalMailboxWriter(globalStore)

	_, _, err = store.AppendAgentControlMailbox(ctx, "parent-session", team.MailMessage{
		ID:        "child-completed-memory",
		FromAgent: "child",
		ToAgent:   "parent",
		Kind:      agentcontrol.MailboxKindSubagentCompleted,
		Body:      "done",
		Metadata: agentcontrol.Envelope{
			MessageType:     agentcontrol.MessageTypeSubagentCompleted,
			ControlAction:   agentcontrol.ActionAgentCompleted,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindSubagentCompleted,
		}.Metadata(),
	})
	require.NoError(t, err)

	records, err := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: "parent-session",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, agentcontrol.MailboxSourceGlobal, records[0].Source)
	assert.Equal(t, "child-completed-memory", records[0].MessageID)

	localRecords, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: "parent-session",
	})
	require.NoError(t, err)
	require.Len(t, localRecords, 1)
	assert.Equal(t, records[0].Seq, localRecords[0].GlobalSeq)
}

func TestSQLiteRuntimeStoreWatchEventsAndLastSeq(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-watch-events-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch, unwatch := store.WatchEvents(ctx, "session-watch")
	defer unwatch()
	seq, err := store.AppendEvent(ctx, runtimeevents.Event{
		Type:      EventMailboxReceived,
		SessionID: "session-watch",
		Payload:   map[string]interface{}{"kind": "agent_message"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)

	select {
	case event := <-watch:
		assert.Equal(t, EventMailboxReceived, event.Type)
		assert.Equal(t, "session-watch", event.SessionID)
		assert.Equal(t, int64(1), event.Payload["seq"])
	case <-time.After(time.Second):
		t.Fatal("event watcher did not wake")
	}
	lastSeq, err := store.LastEventSeq(ctx, "session-watch")
	require.NoError(t, err)
	assert.Equal(t, int64(1), lastSeq)
}

func TestSQLiteRuntimeStoreAppendMailbox(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-append-mailbox-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      "assistant_message",
		SessionID: "session-mailbox",
		Payload:   map[string]interface{}{"content": "before mailbox"},
	})
	require.NoError(t, err)
	event, seq, err := store.AppendMailbox(context.Background(), "session-mailbox", team.MailMessage{
		FromAgent: "parent",
		ToAgent:   "child",
		Kind:      "agent_message",
		Body:      "hello sqlite mailbox",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)
	assert.Equal(t, EventMailboxReceived, event.Type)
	assert.Equal(t, int64(2), event.Payload["seq"])
	assert.Equal(t, int64(1), event.Payload["mailbox_seq"])

	events, err := store.ListEvents(context.Background(), "session-mailbox", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, EventMailboxReceived, events[1].Type)
	assert.Equal(t, "hello sqlite mailbox", events[1].Payload["body"])
	assert.EqualValues(t, 1, events[1].Payload["mailbox_seq"])

	assertSQLiteTableMissing(t, store.db, "session_mailbox_messages")

	var count int
	var storedSeq int64
	var storedBody string
	err = store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*), COALESCE(MAX(session_mailbox_seq), 0), COALESCE(MAX(body), '')
		FROM agent_control_mailbox_records
		WHERE scope = ? AND session_id = ?
	`, agentcontrol.MailboxScopeSession, "session-mailbox").Scan(&count, &storedSeq, &storedBody)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, int64(1), storedSeq)
	assert.Equal(t, "hello sqlite mailbox", storedBody)
}

func TestSQLiteRuntimeStoreAppendMailboxMirrorsAgentControlEnvelope(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-agent-control-mailbox-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, _, err = store.AppendMailbox(context.Background(), "teammate-session", team.MailMessage{
		FromAgent: "parent",
		ToAgent:   "member-1",
		Kind:      "info",
		Body:      "non control mailbox",
	})
	require.NoError(t, err)
	_, _, err = store.AppendAgentControlMailbox(context.Background(), "teammate-session", team.MailMessage{
		FromAgent: "parent",
		ToAgent:   "member-1",
		Kind:      "info",
		Body:      "missing envelope",
	})
	require.Error(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch, unwatch := store.WatchAgentControlMailbox(ctx, "teammate-session")
	defer unwatch()

	metadata := agentcontrol.ApplyEnvelope(map[string]interface{}{
		"team_id": "team-1",
	}, agentcontrol.Envelope{
		MessageType:     agentcontrol.MessageTypeTeamTaskAssignment,
		ControlAction:   agentcontrol.ActionTaskAssign,
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		MailboxDelivery: agentcontrol.DeliverySessionMailbox,
		MailboxKind:     agentcontrol.MailboxKindTeamTaskAssignment,
	})
	taskID := "task-1"
	_, seq, err := store.AppendAgentControlMailbox(context.Background(), "teammate-session", team.MailMessage{
		TeamID:    "team-1",
		FromAgent: "team-orchestrator",
		ToAgent:   "member-1",
		TaskID:    &taskID,
		Kind:      agentcontrol.MailboxKindTeamTaskAssignment,
		Body:      "Team task task-1 assigned.",
		Metadata:  metadata,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), seq)

	allMessages, err := store.ListMailbox(context.Background(), "teammate-session", 0, 10)
	require.NoError(t, err)
	require.Len(t, allMessages, 2)

	controlMessages, err := store.ListAgentControlMailbox(context.Background(), "teammate-session", 0, 10)
	require.NoError(t, err)
	require.Len(t, controlMessages, 1)
	assert.Equal(t, int64(2), controlMessages[0].Seq)
	assert.Equal(t, int64(2), controlMessages[0].ControlSeq)
	assert.Equal(t, int64(2), controlMessages[0].SessionMailboxSeq)
	assert.Equal(t, agentcontrol.MessageTypeTeamTaskAssignment, agentcontrol.MetadataString(controlMessages[0].Metadata, agentcontrol.MetadataKeyMessageType))

	lastSeq, err := store.LastAgentControlMailboxSeq(context.Background(), "teammate-session")
	require.NoError(t, err)
	assert.Equal(t, int64(2), lastSeq)

	select {
	case message := <-watch:
		assert.Equal(t, int64(2), message.Seq)
		assert.Equal(t, int64(2), message.ControlSeq)
		assert.Equal(t, int64(2), message.SessionMailboxSeq)
		assert.Equal(t, agentcontrol.MailboxKindTeamTaskAssignment, message.Kind)
	case <-time.After(time.Second):
		t.Fatal("agent control mailbox watcher did not wake")
	}

	assertSQLiteTableMissing(t, store.db, "agent_control_mailbox_messages")

	var count int
	var controlSeq, mailboxSeq int64
	err = store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*), COALESCE(MAX(id), 0), COALESCE(MAX(session_mailbox_seq), 0)
		FROM agent_control_mailbox_records
		WHERE scope = ? AND session_id = ? AND COALESCE(workflow, '') <> ''
	`, agentcontrol.MailboxScopeSession, "teammate-session").Scan(&count, &controlSeq, &mailboxSeq)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, int64(2), controlSeq)
	assert.Equal(t, int64(2), mailboxSeq)
}

func TestSQLiteRuntimeStoreAppendAgentControlMailboxWritesGlobalRegistry(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-global-mailbox-write-through-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = globalStore.Close() })
	store.SetGlobalMailboxWriter(globalStore)

	metadata := agentcontrol.ApplyEnvelope(map[string]interface{}{}, agentcontrol.Envelope{
		MessageType:     agentcontrol.MessageTypeSubagentCompleted,
		ControlAction:   agentcontrol.ActionAgentCompleted,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		MailboxDelivery: agentcontrol.DeliverySessionMailbox,
		MailboxKind:     agentcontrol.MailboxKindSubagentCompleted,
	})
	_, _, err = store.AppendAgentControlMailbox(ctx, "parent-session", team.MailMessage{
		ID:        "child-completed",
		FromAgent: "child",
		ToAgent:   "parent",
		Kind:      agentcontrol.MailboxKindSubagentCompleted,
		Body:      "done",
		Metadata:  metadata,
		CreatedAt: time.Unix(10, 0).UTC(),
	})
	require.NoError(t, err)

	records, err := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: "parent-session",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, agentcontrol.MailboxSourceGlobal, records[0].Source)
	assert.Positive(t, records[0].SourceSeq)
	assert.Equal(t, "child-completed", records[0].MessageID)
	assert.Equal(t, agentcontrol.MailboxScopeSession, records[0].Scope)
	assert.Equal(t, int64(1), records[0].SessionMailboxSeq)

	localMessages, err := store.ListAgentControlMailbox(ctx, "parent-session", 0, 10)
	require.NoError(t, err)
	require.Len(t, localMessages, 1)
	assert.Equal(t, records[0].Seq, localMessages[0].GlobalSeq)

	localRecords, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: "parent-session",
	})
	require.NoError(t, err)
	require.Len(t, localRecords, 1)
	assert.Equal(t, records[0].Seq, localRecords[0].GlobalSeq)

	count, err := globalStore.MaterializeMailboxRecords(ctx, []agentcontrol.NamedMailboxRegistrySource{
		{Name: agentcontrol.MailboxSourceRuntimeSessions, Source: NewAgentControlMailboxRegistry(store)},
	}, agentcontrol.MailboxRecordFilter{Workflow: agentcontrol.WorkflowSpawnAgent, SessionID: "parent-session"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	records, err = globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: "parent-session",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
}

func TestSQLiteRuntimeStoreAppendMailboxControlEnvelopeUsesGlobalPrimary(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-append-mailbox-global-primary-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = globalStore.Close() })
	store.SetGlobalMailboxWriter(globalStore)

	_, _, err = store.AppendMailbox(ctx, "session-append", team.MailMessage{
		ID:        "ordinary-mail",
		FromAgent: "child",
		ToAgent:   "parent",
		Kind:      "info",
		Body:      "ordinary",
		CreatedAt: time.Unix(20, 0).UTC(),
	})
	require.NoError(t, err)

	metadata := agentcontrol.ApplyEnvelope(map[string]interface{}{}, agentcontrol.Envelope{
		MessageType:     agentcontrol.MessageTypeSubagentCompleted,
		ControlAction:   agentcontrol.ActionAgentCompleted,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		MailboxDelivery: agentcontrol.DeliverySessionMailbox,
		MailboxKind:     agentcontrol.MailboxKindSubagentCompleted,
	})
	_, _, err = store.AppendMailbox(ctx, "session-append", team.MailMessage{
		ID:        "control-via-append",
		FromAgent: "child",
		ToAgent:   "parent",
		Kind:      agentcontrol.MailboxKindSubagentCompleted,
		Body:      "control",
		Metadata:  metadata,
		CreatedAt: time.Unix(21, 0).UTC(),
	})
	require.NoError(t, err)

	records, err := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: "session-append",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, agentcontrol.MailboxSourceGlobal, records[0].Source)
	assert.Equal(t, "control-via-append", records[0].MessageID)
	assert.Equal(t, int64(2), records[0].SessionMailboxSeq)

	localRecords, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: "session-append",
	})
	require.NoError(t, err)
	require.Len(t, localRecords, 1)
	assert.Equal(t, records[0].Seq, localRecords[0].GlobalSeq)
}

func TestSQLiteRuntimeStoreAppendAgentControlMailboxPrefersControlRows(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-agent-control-mailbox-primary-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	metadata := agentcontrol.ApplyEnvelope(map[string]interface{}{
		"team_id":    "team-1",
		"event_type": "task.done",
	}, agentcontrol.Envelope{
		MessageType:     agentcontrol.MessageTypeTeamTaskLifecycle,
		ControlAction:   agentcontrol.ActionTaskLifecycle,
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		MailboxDelivery: agentcontrol.DeliverySessionMailbox,
		MailboxKind:     agentcontrol.MailboxKindTeamTaskLifecycle,
	})
	taskID := "task-primary"
	event, seq, err := store.AppendAgentControlMailbox(context.Background(), "primary-session", team.MailMessage{
		TeamID:    "team-1",
		FromAgent: "team-orchestrator",
		ToAgent:   "member-1",
		TaskID:    &taskID,
		Kind:      agentcontrol.MailboxKindTeamTaskLifecycle,
		Body:      "control primary body",
		Metadata:  metadata,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)
	assert.Equal(t, EventMailboxReceived, event.Type)
	assert.EqualValues(t, 1, event.Payload["mailbox_seq"])

	assertSQLiteTableMissing(t, store.db, "session_mailbox_messages")

	legacyMessages, err := store.ListMailbox(context.Background(), "primary-session", 0, 10)
	require.NoError(t, err)
	require.Len(t, legacyMessages, 1)
	assert.Equal(t, "control primary body", legacyMessages[0].Body)

	controlMessages, err := store.ListAgentControlMailbox(context.Background(), "primary-session", 0, 10)
	require.NoError(t, err)
	require.Len(t, controlMessages, 1)
	assert.Equal(t, "control primary body", controlMessages[0].Body)
	assert.Equal(t, int64(1), controlMessages[0].Seq)
	assert.Equal(t, int64(1), controlMessages[0].ControlSeq)
	assert.Equal(t, int64(1), controlMessages[0].SessionMailboxSeq)
	assert.Equal(t, agentcontrol.MessageTypeTeamTaskLifecycle, agentcontrol.MetadataString(controlMessages[0].Metadata, agentcontrol.MetadataKeyMessageType))

	lastControlSeq, err := store.LastAgentControlMailboxSeq(context.Background(), "primary-session")
	require.NoError(t, err)
	assert.Equal(t, int64(1), lastControlSeq)

	var unifiedRows int
	require.NoError(t, store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*)
		FROM agent_control_mailbox_records
		WHERE scope = ? AND session_id = ?
	`, agentcontrol.MailboxScopeSession, "primary-session").Scan(&unifiedRows))
	assert.Equal(t, 1, unifiedRows)

	assertSQLiteTableMissing(t, store.db, "agent_control_mailbox_messages")

	controlMessages, err = store.ListAgentControlMailbox(context.Background(), "primary-session", 0, 10)
	require.NoError(t, err)
	require.Len(t, controlMessages, 1)
	assert.Equal(t, "control primary body", controlMessages[0].Body)

	registryRecords, err := store.ListAgentControlMailboxRecords(context.Background(), agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: "primary-session",
		Workflow:  agentcontrol.WorkflowSpawnTeam,
	})
	require.NoError(t, err)
	require.Len(t, registryRecords, 1)
	assert.Equal(t, int64(1), registryRecords[0].Seq)
	assert.Equal(t, agentcontrol.MailboxScopeSession, registryRecords[0].Scope)
	assert.Equal(t, "primary-session", registryRecords[0].SessionID)
	assert.Equal(t, int64(1), registryRecords[0].SessionMailboxSeq)
	assert.Equal(t, "team-1", registryRecords[0].TeamID)
	assert.Equal(t, "control primary body", registryRecords[0].Body)
	assert.Equal(t, agentcontrol.MessageTypeTeamTaskLifecycle, agentcontrol.MetadataString(registryRecords[0].Metadata, agentcontrol.MetadataKeyMessageType))

	registrySeq, err := store.LastAgentControlMailboxRecordSeq(context.Background(), agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: "primary-session",
		Workflow:  agentcontrol.WorkflowSpawnTeam,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), registrySeq)
}

func TestSQLiteRuntimeStoreMigratesCurrentRunMetaColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations (version, name, applied_at) VALUES
			(1, 'session_runtime_state', '2026-03-15T00:00:00Z'),
			(2, 'session_events', '2026-03-15T00:00:00Z');

		CREATE TABLE session_runtime_state (
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

		CREATE TABLE session_events (
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
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID: "session-2",
		Status:    SessionRunning,
		CurrentRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID:        "team-2",
				AgentID:       "mate-2",
				CurrentTaskID: "task-2",
			},
		},
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-2")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.CurrentRunMeta)
	require.NotNil(t, loaded.CurrentRunMeta.Team)
	assert.Equal(t, "team-2", loaded.CurrentRunMeta.Team.TeamID)
	assert.Equal(t, "mate-2", loaded.CurrentRunMeta.Team.AgentID)
	assert.Equal(t, "task-2", loaded.CurrentRunMeta.Team.CurrentTaskID)

	_, err = os.Stat(dbPath)
	require.NoError(t, err)
}

func TestSQLiteRuntimeStoreMigratesAmbientRunMetaColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations (version, name, applied_at) VALUES
			(1, 'session_runtime_state', '2026-03-15T00:00:00Z'),
			(2, 'session_events', '2026-03-15T00:00:00Z'),
			(3, 'session_runtime_state_current_run_meta', '2026-03-15T00:00:00Z'),
			(4, 'session_runtime_state_pending_tool', '2026-03-15T00:00:00Z'),
			(5, 'session_tool_receipts', '2026-03-15T00:00:00Z'),
			(6, 'session_tool_receipts_created_at_unix_nano', '2026-03-15T00:00:00Z');

		CREATE TABLE session_runtime_state (
			session_id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			current_turn_id TEXT,
			current_checkpoint_id TEXT,
			pending_approval_json BLOB,
			pending_question_json BLOB,
			head_offset INTEGER NOT NULL DEFAULT 0,
			active_job_ids_json BLOB NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL,
			current_run_meta_json BLOB,
			pending_tool_json BLOB
		);

		CREATE TABLE session_events (
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

		CREATE TABLE session_tool_receipts (
			session_id TEXT NOT NULL,
			tool_call_id TEXT NOT NULL,
			tool_name TEXT,
			message_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			created_at_unix_nano INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (session_id, tool_call_id)
		);
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID: "session-ambient-2",
		Status:    SessionIdle,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID:  "team-ambient-2",
				AgentID: "lead",
			},
		},
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-ambient-2")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.AmbientRunMeta)
	require.NotNil(t, loaded.AmbientRunMeta.Team)
	assert.Equal(t, "team-ambient-2", loaded.AmbientRunMeta.Team.TeamID)
}

func TestSQLiteRuntimeStorePersistsPendingTool(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-pending-tool-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID: "session-pending-tool",
		Status:    SessionWaitingInput,
		PendingTool: &PendingToolInvocation{
			ToolCallID: "toolcall_pending_1",
			ToolName:   "ask_user_question",
			ArgsJSON:   []byte(`{"prompt":"Need confirmation","required":true}`),
			CreatedAt:  time.Now().UTC(),
		},
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-pending-tool")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.PendingTool)
	assert.Equal(t, "toolcall_pending_1", loaded.PendingTool.ToolCallID)
	assert.Equal(t, "ask_user_question", loaded.PendingTool.ToolName)
	assert.JSONEq(t, `{"prompt":"Need confirmation","required":true}`, string(loaded.PendingTool.ArgsJSON))
}

func TestSQLiteRuntimeStorePersistsFrozenTurnTools(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-frozen-turn-tools-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID:     "session-frozen-turn-tools",
		Status:        SessionRunning,
		CurrentTurnID: "turn-1",
		FrozenTurnTools: []types.ToolDefinition{
			{Name: "spawn_team", Description: "Create a team"},
			{Name: "ask_user_question", Description: "Ask the user"},
		},
		FrozenTurnToolsSet: true,
		UpdatedAt:          time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-frozen-turn-tools")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "turn-1", loaded.CurrentTurnID)
	assert.True(t, loaded.FrozenTurnToolsSet)
	require.Len(t, loaded.FrozenTurnTools, 2)
	assert.Equal(t, "spawn_team", loaded.FrozenTurnTools[0].Name)
	assert.Equal(t, "ask_user_question", loaded.FrozenTurnTools[1].Name)
}

func TestSQLiteRuntimeStorePersistsStableToolSurface(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-stable-tool-surface-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	state := &RuntimeState{
		SessionID: "session-stable-tool-surface",
		Status:    SessionIdle,
		StableToolSurface: []types.ToolDefinition{
			{Name: "get_goal", Description: "Read goal"},
			{Name: "update_goal", Description: "Complete goal"},
		},
		StableToolSurfaceSet: true,
		UpdatedAt:            time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	loaded, err := store.LoadState(ctx, "session-stable-tool-surface")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.True(t, loaded.StableToolSurfaceSet)
	require.Len(t, loaded.StableToolSurface, 2)
	assert.Equal(t, "get_goal", loaded.StableToolSurface[0].Name)
	assert.Equal(t, "update_goal", loaded.StableToolSurface[1].Name)
}

func TestSQLiteRuntimeStorePersistsToolReceipt(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-tool-receipt-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	receipt := ToolExecutionReceipt{
		SessionID:   "session-receipt",
		ToolCallID:  "tool_receipt_1",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"stored receipt","tool_call_id":"tool_receipt_1","metadata":{}}`),
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, store.SaveToolReceipt(ctx, receipt))

	loaded, err := store.GetToolReceipt(ctx, "session-receipt", "tool_receipt_1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "team_echo", loaded.ToolName)
	assert.JSONEq(t, `{"role":"tool","content":"stored receipt","tool_call_id":"tool_receipt_1","metadata":{}}`, string(loaded.MessageJSON))

	require.NoError(t, store.DeleteToolReceipt(ctx, "session-receipt", "tool_receipt_1"))
	loaded, err = store.GetToolReceipt(ctx, "session-receipt", "tool_receipt_1")
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestSQLiteRuntimeStoreListsToolReceiptsByRecency(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-tool-receipt-list-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	first := ToolExecutionReceipt{
		SessionID:   "session-receipt-list",
		ToolCallID:  "tool_receipt_old",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"old","tool_call_id":"tool_receipt_old","metadata":{}}`),
		CreatedAt:   time.Now().UTC().Add(-1 * time.Minute),
	}
	second := ToolExecutionReceipt{
		SessionID:   "session-receipt-list",
		ToolCallID:  "tool_receipt_new",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"new","tool_call_id":"tool_receipt_new","metadata":{}}`),
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, store.SaveToolReceipt(ctx, first))
	require.NoError(t, store.SaveToolReceipt(ctx, second))

	receipts, err := store.ListToolReceipts(ctx, "session-receipt-list", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	assert.Equal(t, "tool_receipt_new", receipts[0].ToolCallID)
	assert.Equal(t, "tool_receipt_old", receipts[1].ToolCallID)
}

func TestSQLiteRuntimeStoreListsToolReceiptsByRecencyWithMixedTimestampPrecision(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-tool-receipt-mixed-precision-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	base := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	require.NoError(t, store.SaveToolReceipt(ctx, ToolExecutionReceipt{
		SessionID:   "session-receipt-mixed-precision",
		ToolCallID:  "tool_receipt_whole_second",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"whole-second","tool_call_id":"tool_receipt_whole_second","metadata":{}}`),
		CreatedAt:   base,
	}))
	require.NoError(t, store.SaveToolReceipt(ctx, ToolExecutionReceipt{
		SessionID:   "session-receipt-mixed-precision",
		ToolCallID:  "tool_receipt_fractional",
		ToolName:    "team_echo",
		MessageJSON: []byte(`{"role":"tool","content":"fractional","tool_call_id":"tool_receipt_fractional","metadata":{}}`),
		CreatedAt:   base.Add(100 * time.Millisecond),
	}))

	receipts, err := store.ListToolReceipts(ctx, "session-receipt-mixed-precision", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	assert.Equal(t, "tool_receipt_fractional", receipts[0].ToolCallID)
	assert.Equal(t, "tool_receipt_whole_second", receipts[1].ToolCallID)
}

func TestSQLiteRuntimeStoreMigratesToolReceiptOrderingColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)

	_, err = db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations (version, name, applied_at) VALUES
			(1, 'session_runtime_state', '2026-03-15T00:00:00Z'),
			(2, 'session_events', '2026-03-15T00:00:00Z'),
			(3, 'session_runtime_state_current_run_meta', '2026-03-15T00:00:00Z'),
			(4, 'session_runtime_state_pending_tool', '2026-03-15T00:00:00Z'),
			(5, 'session_tool_receipts', '2026-03-15T00:00:00Z');

		CREATE TABLE session_runtime_state (
			session_id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			current_turn_id TEXT,
			current_checkpoint_id TEXT,
			pending_approval_json BLOB,
			pending_question_json BLOB,
			head_offset INTEGER NOT NULL DEFAULT 0,
			active_job_ids_json BLOB NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL,
			current_run_meta_json BLOB,
			pending_tool_json BLOB
		);

		CREATE TABLE session_events (
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

		CREATE TABLE session_tool_receipts (
			session_id TEXT NOT NULL,
			tool_call_id TEXT NOT NULL,
			tool_name TEXT,
			message_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (session_id, tool_call_id)
		);

		INSERT INTO session_tool_receipts (session_id, tool_call_id, tool_name, message_json, created_at) VALUES
			('session-receipt-migration', 'tool_receipt_whole_second', 'team_echo', '{"role":"tool","content":"whole-second","tool_call_id":"tool_receipt_whole_second","metadata":{}}', '2026-03-15T10:00:00Z'),
			('session-receipt-migration', 'tool_receipt_fractional', 'team_echo', '{"role":"tool","content":"fractional","tool_call_id":"tool_receipt_fractional","metadata":{}}', '2026-03-15T10:00:00.1Z');
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	receipts, err := store.ListToolReceipts(context.Background(), "session-receipt-migration", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	assert.Equal(t, "tool_receipt_fractional", receipts[0].ToolCallID)
	assert.Equal(t, "tool_receipt_whole_second", receipts[1].ToolCallID)
}

func TestSQLiteRuntimeStoreMigratesFrozenTurnToolsColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations (version, name, applied_at) VALUES
			(1, 'session_runtime_state', '2026-03-15T00:00:00Z'),
			(2, 'session_events', '2026-03-15T00:00:00Z'),
			(3, 'session_runtime_state_current_run_meta', '2026-03-15T00:00:00Z'),
			(4, 'session_runtime_state_pending_tool', '2026-03-15T00:00:00Z'),
			(5, 'session_tool_receipts', '2026-03-15T00:00:00Z'),
			(6, 'session_tool_receipts_created_at_unix_nano', '2026-03-15T00:00:00Z'),
			(7, 'session_runtime_state_ambient_run_meta', '2026-03-15T00:00:00Z');

		CREATE TABLE session_runtime_state (
			session_id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			current_turn_id TEXT,
			current_checkpoint_id TEXT,
			pending_approval_json BLOB,
			pending_question_json BLOB,
			head_offset INTEGER NOT NULL DEFAULT 0,
			active_job_ids_json BLOB NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL,
			current_run_meta_json BLOB,
			pending_tool_json BLOB,
			ambient_run_meta_json BLOB
		);

		CREATE TABLE session_events (
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

		CREATE TABLE session_tool_receipts (
			session_id TEXT NOT NULL,
			tool_call_id TEXT NOT NULL,
			tool_name TEXT,
			message_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			created_at_unix_nano INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (session_id, tool_call_id)
		);
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	state := &RuntimeState{
		SessionID:     "session-frozen-turn-tools-migration",
		Status:        SessionRunning,
		CurrentTurnID: "turn-migration",
		FrozenTurnTools: []types.ToolDefinition{
			{Name: "spawn_team", Description: "Create a team"},
		},
		FrozenTurnToolsSet: true,
		UpdatedAt:          time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(context.Background(), state))

	loaded, err := store.LoadState(context.Background(), "session-frozen-turn-tools-migration")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.True(t, loaded.FrozenTurnToolsSet)
	require.Len(t, loaded.FrozenTurnTools, 1)
	assert.Equal(t, "spawn_team", loaded.FrozenTurnTools[0].Name)
}

func TestSQLiteRuntimeStoreMigratesSessionMailboxTable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations (version, name, applied_at) VALUES
			(1, 'session_runtime_state', '2026-03-15T00:00:00Z'),
			(2, 'session_events', '2026-03-15T00:00:00Z'),
			(3, 'session_runtime_state_current_run_meta', '2026-03-15T00:00:00Z'),
			(4, 'session_runtime_state_pending_tool', '2026-03-15T00:00:00Z'),
			(5, 'session_tool_receipts', '2026-03-15T00:00:00Z'),
			(6, 'session_tool_receipts_created_at_unix_nano', '2026-03-15T00:00:00Z'),
			(7, 'session_runtime_state_ambient_run_meta', '2026-03-15T00:00:00Z'),
			(8, 'session_runtime_state_frozen_turn_tools', '2026-03-15T00:00:00Z');

		CREATE TABLE session_runtime_state (
			session_id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			current_turn_id TEXT,
			current_checkpoint_id TEXT,
			pending_approval_json BLOB,
			pending_question_json BLOB,
			head_offset INTEGER NOT NULL DEFAULT 0,
			active_job_ids_json BLOB NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL,
			current_run_meta_json BLOB,
			pending_tool_json BLOB,
			ambient_run_meta_json BLOB,
			frozen_turn_tools_json BLOB
		);

		CREATE TABLE session_events (
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

		CREATE TABLE session_tool_receipts (
			session_id TEXT NOT NULL,
			tool_call_id TEXT NOT NULL,
			tool_name TEXT,
			message_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			created_at_unix_nano INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (session_id, tool_call_id)
		);
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	event, mailboxSeq, err := store.AppendMailbox(context.Background(), "session-mailbox-migration", team.MailMessage{
		FromAgent: "parent",
		ToAgent:   "child",
		Kind:      "agent_message",
		Body:      "after migration",
		Metadata: agentcontrol.Envelope{
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), mailboxSeq)
	assert.Equal(t, int64(1), event.Payload["mailbox_seq"])

	var count int
	assertSQLiteTableMissing(t, store.db, "session_mailbox_messages")

	assertSQLiteTableMissing(t, store.db, "agent_control_mailbox_messages")

	err = store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*)
		FROM agent_control_mailbox_records
		WHERE scope = ? AND session_id = ?
	`, agentcontrol.MailboxScopeSession, "session-mailbox-migration").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestSQLiteRuntimeStoreMigratesAgentControlMailboxSequence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations (version, name, applied_at) VALUES
			(1, 'session_runtime_state', '2026-03-15T00:00:00Z'),
			(2, 'session_events', '2026-03-15T00:00:00Z'),
			(3, 'session_runtime_state_current_run_meta', '2026-03-15T00:00:00Z'),
			(4, 'session_runtime_state_pending_tool', '2026-03-15T00:00:00Z'),
			(5, 'session_tool_receipts', '2026-03-15T00:00:00Z'),
			(6, 'session_tool_receipts_created_at_unix_nano', '2026-03-15T00:00:00Z'),
			(7, 'session_runtime_state_ambient_run_meta', '2026-03-15T00:00:00Z'),
			(8, 'session_runtime_state_frozen_turn_tools', '2026-03-15T00:00:00Z'),
			(9, 'session_mailbox_messages', '2026-03-15T00:00:00Z'),
			(10, 'agent_control_mailbox_messages', '2026-03-15T00:00:00Z');

		CREATE TABLE session_runtime_state (
			session_id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			current_turn_id TEXT,
			current_checkpoint_id TEXT,
			pending_approval_json BLOB,
			pending_question_json BLOB,
			head_offset INTEGER NOT NULL DEFAULT 0,
			active_job_ids_json BLOB NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL,
			current_run_meta_json BLOB,
			pending_tool_json BLOB,
			ambient_run_meta_json BLOB,
			frozen_turn_tools_json BLOB
		);

		CREATE TABLE session_events (
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

		CREATE TABLE session_tool_receipts (
			session_id TEXT NOT NULL,
			tool_call_id TEXT NOT NULL,
			tool_name TEXT,
			message_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			created_at_unix_nano INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (session_id, tool_call_id)
		);

		CREATE TABLE session_mailbox_messages (
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

		CREATE TABLE agent_control_mailbox_messages (
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

		INSERT INTO session_mailbox_messages (
			session_id, seq, message_id, from_agent, to_agent, kind, body, metadata_json, created_at
		) VALUES
			('session-control-migration', 2, 'control-old-2', 'child-2', 'parent', 'agent_message', 'old control 2', '{"message_type":"agent_control.agent_message"}', '2026-03-15T00:00:00Z'),
			('session-control-migration', 4, 'control-old-4', 'child-4', 'parent', 'agent_message', 'old control 4', '{"message_type":"agent_control.agent_message"}', '2026-03-15T00:00:01Z');

		INSERT INTO agent_control_mailbox_messages (
			session_id, session_mailbox_seq, message_id, from_agent, to_agent, kind,
			message_type, control_action, workflow, mailbox_delivery, mailbox_kind,
			body, metadata_json, created_at
		) VALUES
			('session-control-migration', 2, 'control-old-2', 'child-2', 'parent', 'agent_message',
			 'agent_control.agent_message', 'agent.message', 'spawn_agent', 'session_mailbox', 'agent_message',
			 'old control 2', '{"message_type":"agent_control.agent_message"}', '2026-03-15T00:00:00Z'),
			('session-control-migration', 4, 'control-old-4', 'child-4', 'parent', 'agent_message',
			 'agent_control.agent_message', 'agent.message', 'spawn_agent', 'session_mailbox', 'agent_message',
			 'old control 4', '{"message_type":"agent_control.agent_message"}', '2026-03-15T00:00:01Z');
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	assertSQLiteTableMissing(t, store.db, "session_mailbox_messages")
	assertSQLiteTableMissing(t, store.db, "agent_control_mailbox_messages")

	messages, err := store.ListAgentControlMailbox(context.Background(), "session-control-migration", 0, 10)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, int64(1), messages[0].Seq)
	assert.Equal(t, int64(2), messages[0].SessionMailboxSeq)
	assert.Equal(t, int64(2), messages[1].Seq)
	assert.Equal(t, int64(4), messages[1].SessionMailboxSeq)

	_, seq, err := store.AppendAgentControlMailbox(context.Background(), "session-control-migration", team.MailMessage{
		FromAgent: "child-5",
		ToAgent:   "parent",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "new control 5",
		Metadata: agentcontrol.Envelope{
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(5), seq)

	messages, err = store.ListAgentControlMailbox(context.Background(), "session-control-migration", 2, 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, int64(3), messages[0].Seq)
	assert.Equal(t, int64(5), messages[0].SessionMailboxSeq)
}
