package chat

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func idempotentControlMailboxMessage(id string) team.MailMessage {
	return team.MailMessage{
		ID:        id,
		FromAgent: "child",
		ToAgent:   "parent",
		Kind:      agentcontrol.MailboxKindSubagentCompleted,
		Body:      "terminal result",
		Metadata: agentcontrol.Envelope{
			MessageType:     agentcontrol.MessageTypeSubagentCompleted,
			ControlAction:   agentcontrol.ActionAgentCompleted,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindSubagentCompleted,
		}.Metadata(),
	}
}

func TestInMemoryRuntimeStoreAgentControlMailboxMessageIDIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(64)
	message := idempotentControlMailboxMessage("delivery-fixed")

	first, firstSeq, err := store.AppendAgentControlMailbox(ctx, "parent-a", message)
	require.NoError(t, err)
	second, secondSeq, err := store.AppendAgentControlMailbox(ctx, "parent-a", message)
	require.NoError(t, err)

	assert.Equal(t, int64(1), firstSeq)
	assert.Equal(t, firstSeq, secondSeq)
	assert.NotEqual(t, true, first.Payload["duplicate"])
	assert.Equal(t, true, second.Payload["duplicate"])

	messages, err := store.ListMailbox(ctx, "parent-a", 0, 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	control, err := store.ListAgentControlMailbox(ctx, "parent-a", 0, 10)
	require.NoError(t, err)
	require.Len(t, control, 1)
	events, err := store.ListEvents(ctx, "parent-a", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)

	_, otherSeq, err := store.AppendAgentControlMailbox(ctx, "parent-b", message)
	require.NoError(t, err)
	assert.Equal(t, int64(1), otherSeq, "the same delivery key is scoped by parent session")
	other, err := store.ListMailbox(ctx, "parent-b", 0, 10)
	require.NoError(t, err)
	require.Len(t, other, 1)
}

func TestInMemoryRuntimeStoreConcurrentMailboxRetriesDoNotDuplicate(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(64)
	message := idempotentControlMailboxMessage("delivery-concurrent")

	const callers = 24
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	seqs := make(chan int64, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, seq, err := store.AppendAgentControlMailbox(ctx, "parent", message)
			errs <- err
			seqs <- seq
		}()
	}
	wg.Wait()
	close(errs)
	close(seqs)
	for err := range errs {
		require.NoError(t, err)
	}
	for seq := range seqs {
		assert.Equal(t, int64(1), seq)
	}
	messages, err := store.ListMailbox(ctx, "parent", 0, callers+1)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	events, err := store.ListEvents(ctx, "parent", 0, callers+1)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestSQLiteRuntimeStoreAgentControlMailboxMessageIDIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: filepath.Join(t.TempDir(), "runtime.sqlite")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	message := idempotentControlMailboxMessage("delivery-fixed")

	first, firstSeq, err := store.AppendAgentControlMailbox(ctx, "parent-a", message)
	require.NoError(t, err)
	second, secondSeq, err := store.AppendAgentControlMailbox(ctx, "parent-a", message)
	require.NoError(t, err)
	assert.Equal(t, int64(1), firstSeq)
	assert.Equal(t, firstSeq, secondSeq)
	assert.NotEqual(t, true, first.Payload["duplicate"])
	assert.Equal(t, true, second.Payload["duplicate"])

	messages, err := store.ListMailbox(ctx, "parent-a", 0, 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	events, err := store.ListEvents(ctx, "parent-a", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)

	_, otherSeq, err := store.AppendAgentControlMailbox(ctx, "parent-b", message)
	require.NoError(t, err)
	assert.Equal(t, int64(1), otherSeq)
	other, err := store.ListMailbox(ctx, "parent-b", 0, 10)
	require.NoError(t, err)
	require.Len(t, other, 1)
}

func TestSQLiteRuntimeStoreConcurrentInstancesDeduplicateMessageID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.sqlite")
	firstStore, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = firstStore.Close() })
	// Apply migrations before opening a second store so the test targets
	// delivery contention rather than concurrent schema bootstrap.
	_, err = firstStore.AppendEvent(ctx, runtimeevents.Event{
		Type:      "schema.initialized",
		SessionID: "schema-init",
	})
	require.NoError(t, err)
	secondStore, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = secondStore.Close() })
	_, err = secondStore.ListEvents(ctx, "parent", 0, 1)
	require.NoError(t, err)

	message := idempotentControlMailboxMessage("delivery-cross-store")
	stores := []*SQLiteRuntimeStore{firstStore, secondStore}
	const callers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	seqs := make(chan int64, callers)
	for i := 0; i < callers; i++ {
		store := stores[i%len(stores)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, seq, appendErr := store.AppendAgentControlMailbox(ctx, "parent", message)
			errs <- appendErr
			seqs <- seq
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(seqs)
	for appendErr := range errs {
		require.NoError(t, appendErr)
	}
	for seq := range seqs {
		assert.Equal(t, int64(1), seq)
	}
	rows, err := firstStore.ListMailbox(ctx, "parent", 0, callers+1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	events, err := firstStore.ListEvents(ctx, "parent", 0, callers+1)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestMailboxEmptyMessageIDsRemainDistinct(t *testing.T) {
	ctx := context.Background()
	memory := NewInMemoryRuntimeStore(16)
	for i := 0; i < 2; i++ {
		_, _, err := memory.AppendAgentControlMailbox(ctx, "parent", idempotentControlMailboxMessage(""))
		require.NoError(t, err)
	}
	memoryRows, err := memory.ListMailbox(ctx, "parent", 0, 10)
	require.NoError(t, err)
	require.Len(t, memoryRows, 2)
	assert.NotEmpty(t, memoryRows[0].ID)
	assert.NotEmpty(t, memoryRows[1].ID)
	assert.NotEqual(t, memoryRows[0].ID, memoryRows[1].ID)

	sqliteStore, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: filepath.Join(t.TempDir(), "runtime.sqlite")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqliteStore.Close() })
	for i := 0; i < 2; i++ {
		_, _, err := sqliteStore.AppendAgentControlMailbox(ctx, "parent", idempotentControlMailboxMessage(""))
		require.NoError(t, err)
	}
	sqliteRows, err := sqliteStore.ListMailbox(ctx, "parent", 0, 10)
	require.NoError(t, err)
	require.Len(t, sqliteRows, 2)
	assert.NotEmpty(t, sqliteRows[0].ID)
	assert.NotEmpty(t, sqliteRows[1].ID)
	assert.NotEqual(t, sqliteRows[0].ID, sqliteRows[1].ID)
}

func TestDuplicateMailboxAppendDoesNotWakeWatchers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := NewInMemoryRuntimeStore(16)
	mailWatch, stopMail := store.WatchMailbox(ctx, "parent")
	defer stopMail()
	controlWatch, stopControl := store.WatchAgentControlMailbox(ctx, "parent")
	defer stopControl()
	eventWatch, stopEvent := store.WatchEvents(ctx, "parent")
	defer stopEvent()
	message := idempotentControlMailboxMessage("delivery-watch")

	_, _, err := store.AppendAgentControlMailbox(ctx, "parent", message)
	require.NoError(t, err)
	select {
	case <-mailWatch:
	case <-time.After(time.Second):
		t.Fatal("initial mailbox watcher notification missing")
	}
	select {
	case <-controlWatch:
	case <-time.After(time.Second):
		t.Fatal("initial control watcher notification missing")
	}
	select {
	case <-eventWatch:
	case <-time.After(time.Second):
		t.Fatal("initial event watcher notification missing")
	}

	_, _, err = store.AppendAgentControlMailbox(ctx, "parent", message)
	require.NoError(t, err)
	for name, watch := range map[string]<-chan team.MailMessage{"mailbox": mailWatch, "control": controlWatch} {
		select {
		case got := <-watch:
			t.Fatalf("duplicate append unexpectedly woke %s watcher: %#v", name, got)
		case <-time.After(50 * time.Millisecond):
		}
	}
	select {
	case got := <-eventWatch:
		t.Fatalf("duplicate append unexpectedly woke event watcher: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDeliverMailboxEventFirstResultReportsDurableDuplicate(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(16)
	bus := runtimeevents.NewBusWithRetention(16)
	message := idempotentControlMailboxMessage("delivery-result")

	first, err := DeliverMailboxEventFirstResult(ctx, store, bus, nil, "parent", message)
	require.NoError(t, err)
	assert.False(t, first.Duplicate)
	assert.Equal(t, int64(1), first.Sequence)
	second, err := DeliverMailboxEventFirstResult(ctx, store, bus, nil, "parent", message)
	require.NoError(t, err)
	assert.True(t, second.Duplicate)
	assert.Equal(t, first.Sequence, second.Sequence)
	assert.Len(t, bus.Recent(10), 1)
}
