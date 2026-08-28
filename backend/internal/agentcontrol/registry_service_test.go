package agentcontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegistryServiceUsesSingleSQLiteDBForMailboxAndAgents(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "agent-control.sqlite")
	service, err := NewRegistryService(ctx, RegistryServiceConfig{StorePath: storePath})
	if err != nil {
		t.Fatalf("NewRegistryService: %v", err)
	}
	defer service.Close()
	if service.MailboxStore == nil {
		t.Fatal("expected mailbox store")
	}
	if service.AgentStore == nil {
		t.Fatal("expected agent store")
	}

	mailboxRecord, err := service.MailboxStore.AppendPrimaryGlobalMailboxRecord(ctx, MailboxRecord{
		Workflow:  WorkflowSpawnAgent,
		Scope:     MailboxScopeSession,
		SessionID: "root-session",
		MessageID: "message-1",
		FromAgent: "child",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "hello",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("AppendPrimaryGlobalMailboxRecord: %v", err)
	}
	if mailboxRecord.GlobalSeq <= 0 {
		t.Fatalf("expected global mailbox sequence, got %#v", mailboxRecord)
	}

	agentRecord, err := service.AgentStore.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-1",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	})
	if err != nil {
		t.Fatalf("UpsertAgentControlAgent: %v", err)
	}
	if agentRecord.Seq <= 0 {
		t.Fatalf("expected agent registry sequence, got %#v", agentRecord)
	}

	reopened, err := NewRegistryService(ctx, RegistryServiceConfig{StorePath: storePath})
	if err != nil {
		t.Fatalf("reopen NewRegistryService: %v", err)
	}
	defer reopened.Close()
	mailboxRows, err := reopened.MailboxStore.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{Scope: MailboxScopeSession})
	if err != nil {
		t.Fatalf("ListAgentControlMailboxRecords: %v", err)
	}
	if len(mailboxRows) != 1 || mailboxRows[0].MessageID != "message-1" {
		t.Fatalf("unexpected mailbox rows: %#v", mailboxRows)
	}
	agentRows, err := reopened.AgentStore.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: "root-session", IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListAgentControlAgents: %v", err)
	}
	if len(agentRows) != 1 || agentRows[0].AgentID != "agent-1" {
		t.Fatalf("unexpected agent rows: %#v", agentRows)
	}
}

func TestRegistryServiceAllowsMailboxAndAgentOverrides(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	service, err := NewRegistryService(ctx, RegistryServiceConfig{
		StorePath:        filepath.Join(dir, "agent-control.sqlite"),
		MailboxStorePath: filepath.Join(dir, "mailbox.sqlite"),
		AgentStorePath:   filepath.Join(dir, "agents.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewRegistryService: %v", err)
	}
	defer service.Close()
	if service.MailboxStore == nil || service.AgentStore == nil {
		t.Fatalf("expected both override stores, got mailbox=%T agent=%T", service.MailboxStore, service.AgentStore)
	}
}

func TestRegistryService_PathBackedIsLazyUntilFirstUse(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agent_control.sqlite")
	service, err := NewRegistryService(ctx, RegistryServiceConfig{StorePath: path})
	if err != nil {
		t.Fatalf("NewRegistryService: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	mailbox, ok := service.MailboxStore.(*SQLiteGlobalMailboxRegistryStore)
	if !ok {
		t.Fatalf("expected mailbox sqlite store, got %T", service.MailboxStore)
	}
	agentStore, ok := service.AgentStore.(*SQLiteGlobalAgentRegistryStore)
	if !ok {
		t.Fatalf("expected agent sqlite store, got %T", service.AgentStore)
	}
	if mailbox.Opened() || agentStore.Opened() {
		t.Fatal("expected registry service stores to stay closed until first use")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("bootstrap must not create agent_control.sqlite early: %v", err)
	}

	rows, err := mailbox.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{})
	if err != nil {
		t.Fatalf("ListAgentControlMailboxRecords: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty mailbox rows, got %#v", rows)
	}
	if mailbox.Opened() || agentStore.Opened() {
		t.Fatal("empty reads must not force the first open")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty reads must not create agent_control.sqlite: %v", err)
	}

	if _, err := service.AgentStore.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-1",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	}); err != nil {
		t.Fatalf("UpsertAgentControlAgent: %v", err)
	}
	if !mailbox.Opened() || !agentStore.Opened() {
		t.Fatal("expected shared registry open after first write")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected agent_control.sqlite after first write: %v", err)
	}
}

func TestRegistryServiceHealthModeAndIdempotentClose(t *testing.T) {
	ctx := context.Background()
	service, err := NewRegistryService(ctx, RegistryServiceConfig{StorePath: filepath.Join(t.TempDir(), "agent-control.sqlite")})
	if err != nil {
		t.Fatalf("NewRegistryService: %v", err)
	}
	health, err := service.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Mode != RegistryServiceModeSingleSQLite || !health.SharedDB || !health.MailboxConfigured || !health.AgentConfigured {
		t.Fatalf("unexpected health: %#v", health)
	}
	if service.Mode() != RegistryServiceModeSingleSQLite {
		t.Fatalf("unexpected mode: %s", service.Mode())
	}

	mailboxWake, unwatchMailbox := service.MailboxStore.WatchAgentControlMailboxWake(ctx, MailboxWakeFilter{})
	defer unwatchMailbox()
	agentWakeSource, ok := service.AgentStore.(AgentWakeSource)
	if !ok {
		t.Fatalf("expected agent wake source")
	}
	agentWake, unwatchAgent := agentWakeSource.WatchAgentControlAgentWake(ctx, AgentWakeFilter{})
	defer unwatchAgent()

	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("second Close should be idempotent: %v", err)
	}
	closedHealth, err := service.Health(ctx)
	if !errors.Is(err, ErrRegistryServiceClosed) {
		t.Fatalf("expected closed health error, got health=%#v err=%v", closedHealth, err)
	}
	if !closedHealth.Closed {
		t.Fatalf("expected closed health flag, got %#v", closedHealth)
	}
	if _, err := service.MailboxStore.AppendPrimaryGlobalMailboxRecord(ctx, MailboxRecord{Scope: MailboxScopeSession, SessionID: "root", MessageID: "m"}); !errors.Is(err, ErrRegistryServiceClosed) {
		t.Fatalf("expected closed mailbox store error, got %v", err)
	}
	if _, err := service.AgentStore.ListAgentControlAgents(ctx, AgentFilter{}); !errors.Is(err, ErrRegistryServiceClosed) {
		t.Fatalf("expected closed agent store error, got %v", err)
	}
	assertChannelClosed(t, mailboxWake, "mailbox wake")
	assertChannelClosed(t, agentWake, "agent wake")
}

func TestRegistryServiceDisabledHealth(t *testing.T) {
	service, err := NewRegistryService(context.Background(), RegistryServiceConfig{})
	if err != nil {
		t.Fatalf("NewRegistryService: %v", err)
	}
	defer service.Close()
	health, err := service.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Mode != RegistryServiceModeDisabled || health.SharedDB || health.MailboxConfigured || health.AgentConfigured {
		t.Fatalf("unexpected disabled health: %#v", health)
	}
}

func TestRegistryServiceSharedSQLiteConcurrentInstances(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "agent-control.sqlite")
	first, err := NewRegistryService(ctx, RegistryServiceConfig{StorePath: storePath})
	if err != nil {
		t.Fatalf("first NewRegistryService: %v", err)
	}
	defer first.Close()
	second, err := NewRegistryService(ctx, RegistryServiceConfig{StorePath: storePath})
	if err != nil {
		t.Fatalf("second NewRegistryService: %v", err)
	}
	defer second.Close()

	root := AgentRecord{
		AgentID:       "root",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	}
	stores := []AgentSpawnReservationStore{
		mustSpawnReservationStore(t, first.AgentStore),
		mustSpawnReservationStore(t, second.AgentStore),
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	limitErrors := 0
	otherErrors := make([]error, 0)
	for i := 0; i < 12; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := AgentRecord{
				AgentID:         "child-" + string(rune('a'+i)),
				RootSessionID:   "root-session",
				ParentAgentID:   "root",
				ParentSessionID: "root-session",
				SessionID:       "child-session-" + string(rune('a'+i)),
				AgentPath:       "/root/child-" + string(rune('a'+i)),
				Depth:           1,
				AgentType:       AgentTypeChild,
				Status:          AgentStatusActive,
			}
			_, err := stores[i%len(stores)].ReserveAgentControlAgentSpawn(ctx, root, child, 3)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
				return
			}
			if strings.Contains(err.Error(), "agent spawn thread limit reached") {
				limitErrors++
				return
			}
			otherErrors = append(otherErrors, err)
		}()
	}
	wg.Wait()
	if first.db == nil || first.db != second.db {
		t.Fatalf("path-backed services must share one physical DB pool: first=%p second=%p", first.db, second.db)
	}
	if len(otherErrors) > 0 {
		msgs := make([]string, 0, len(otherErrors))
		for _, err := range otherErrors {
			msgs = append(msgs, err.Error())
		}
		t.Fatalf("unexpected reservation errors: %v", msgs)
	}
	if successes != 3 || limitErrors != 9 {
		t.Fatalf("expected 3 successes and 9 limit errors, got successes=%d limitErrors=%d", successes, limitErrors)
	}
	agents, err := first.AgentStore.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: "root-session"})
	if err != nil {
		t.Fatalf("ListAgentControlAgents: %v", err)
	}
	if len(agents) != 4 {
		t.Fatalf("expected root plus 3 children, got %#v", agents)
	}

	var mailboxWG sync.WaitGroup
	for i := 0; i < 20; i++ {
		i := i
		mailboxWG.Add(1)
		go func() {
			defer mailboxWG.Done()
			store := first.MailboxStore
			if i%2 == 1 {
				store = second.MailboxStore
			}
			record, err := store.AppendPrimaryGlobalMailboxRecord(ctx, MailboxRecord{
				Workflow:  WorkflowSpawnAgent,
				Scope:     MailboxScopeSession,
				SessionID: "root-session",
				MessageID: "message-" + string(rune('a'+i)),
				FromAgent: "child",
				ToAgent:   "parent",
				Kind:      "agent_message",
				Body:      "body",
				CreatedAt: time.Now().UTC(),
			})
			if err != nil {
				t.Errorf("AppendPrimaryGlobalMailboxRecord: %v", err)
				return
			}
			if record.GlobalSeq <= 0 {
				t.Errorf("expected global seq, got %#v", record)
			}
		}()
	}
	mailboxWG.Wait()
	mailboxRows, err := second.MailboxStore.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{Scope: MailboxScopeSession})
	if err != nil {
		t.Fatalf("ListAgentControlMailboxRecords: %v", err)
	}
	if len(mailboxRows) != 20 {
		t.Fatalf("expected 20 mailbox rows, got %d %#v", len(mailboxRows), mailboxRows)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first service: %v", err)
	}
	afterClose, err := second.AgentStore.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: "root-session"})
	if err != nil {
		t.Fatalf("second service should remain usable after first closes: %v", err)
	}
	if len(afterClose) != 4 {
		t.Fatalf("unexpected agent rows after first close: %#v", afterClose)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second service: %v", err)
	}

	reopened, err := NewRegistryService(ctx, RegistryServiceConfig{StorePath: storePath})
	if err != nil {
		t.Fatalf("reopen registry service: %v", err)
	}
	defer reopened.Close()
	reopenedAgents, err := reopened.AgentStore.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: "root-session"})
	if err != nil {
		t.Fatalf("reopened service should remain usable: %v", err)
	}
	if len(reopenedAgents) != 4 {
		t.Fatalf("unexpected agent rows after reopen: %#v", reopenedAgents)
	}
}

func assertChannelClosed[T any](t *testing.T, ch <-chan T, name string) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("expected %s channel to close", name)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s channel to close", name)
	}
}

func mustSpawnReservationStore(t *testing.T, store AgentRegistryStore) AgentSpawnReservationStore {
	t.Helper()
	reservation, ok := store.(AgentSpawnReservationStore)
	if !ok {
		t.Fatalf("expected spawn reservation store, got %T", store)
	}
	return reservation
}
