package chat

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestPoolReentryGuardDetectsNestedAcquisition(t *testing.T) {
	var guard poolReentryGuard
	require.NoError(t, guard.check("outer"))

	guard.enter("appendAgentControlMailboxSameTx")
	err := guard.check("recoverSQLiteMailboxDuplicate")
	require.Error(t, err)
	require.Contains(t, err.Error(), "re-entry")

	guard.exit()
	require.NoError(t, guard.check("after-release"))
	require.Equal(t, int64(1), guard.detections.Load())
}

func TestSQLiteRuntimeStoreRefusesNestedPoolAcquisition(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: filepath.Join(t.TempDir(), "runtime.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	require.NoError(t, store.SaveState(ctx, &RuntimeState{SessionID: "s-nested"}))

	// Simulate the old deadlock shape: the same goroutine holds a dedicated
	// connection and then asks the shared pool for another one.
	store.enterDedicatedConn("test-hold")
	defer store.exitDedicatedConn()

	_, err = store.AppendEvent(ctx, runtimeevents.Event{Type: "tool.completed", SessionID: "s-nested"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "re-entry")
	require.GreaterOrEqual(t, store.ReentryDetections(), int64(1))

	// LoadState must be refused the same way.
	_, err = store.LoadState(ctx, "s-nested")
	require.Error(t, err)
	require.Contains(t, err.Error(), "re-entry")
}

func TestSQLiteRuntimeStoreOperationContextBoundsDeadline(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{DSN: "file:runtime-store-op-ctx?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx, cancel := store.operationContext(context.Background())
	defer cancel()
	_, ok := ctx.Deadline()
	require.True(t, ok, "background context must gain a deadline")

	callerCtx, callerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer callerCancel()
	preserved, preservedCancel := store.operationContext(callerCtx)
	defer preservedCancel()
	deadline, ok := preserved.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(5*time.Second), deadline, 500*time.Millisecond)
}

func TestSQLiteRuntimeStoreOperationTimeoutWhenPoolSaturated(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: filepath.Join(t.TempDir(), "runtime.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	require.NoError(t, store.SaveState(ctx, &RuntimeState{SessionID: "s-saturated"}))

	// Shorten the bound only after open/migration completed, so the test does
	// not depend on open latency.
	store.opTimeout = 300 * time.Millisecond

	conn, err := store.db.Conn(ctx)
	require.NoError(t, err)

	start := time.Now()
	_, err = store.AppendEvent(ctx, runtimeevents.Event{Type: "tool.completed", SessionID: "s-saturated"})
	elapsed := time.Since(start)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, elapsed, 5*time.Second, "AppendEvent must not wait forever for the pool")

	require.NoError(t, conn.Close())

	// Releasing the connection restores normal operation.
	_, err = store.AppendEvent(ctx, runtimeevents.Event{Type: "tool.completed", SessionID: "s-saturated"})
	require.NoError(t, err)
}

func TestSQLiteRuntimeStoreRecoverMailboxDuplicateUsesHeldConnection(t *testing.T) {
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

	ctx := context.Background()
	message := team.MailMessage{
		ID:        "held-conn-duplicate",
		FromAgent: "child",
		ToAgent:   "parent",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "held connection duplicate",
		Metadata: agentcontrol.Envelope{
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
	}
	const sessionID = "session-held-conn"
	_, _, err = store.AppendAgentControlMailbox(ctx, sessionID, message)
	require.NoError(t, err)

	// Hold the only pooled connection, then recover the duplicate through it.
	conn, err := store.db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	store.enterDedicatedConn("test-held-conn")
	defer store.exitDedicatedConn()

	start := time.Now()
	event, seq, ok := store.recoverSQLiteMailboxDuplicate(ctx, conn, sessionID, message)
	require.True(t, ok)
	require.Greater(t, seq, int64(0))
	require.NotEmpty(t, event.Type)
	require.Less(t, time.Since(start), time.Second, "recovery on the held connection must not wait on the pool")

	// Asking the shared pool while holding the connection is refused instead of
	// deadlocking (defense in depth for future call sites).
	start = time.Now()
	_, _, ok = store.recoverSQLiteMailboxDuplicate(ctx, nil, sessionID, message)
	require.False(t, ok)
	require.Less(t, time.Since(start), time.Second)
}

func TestSQLiteRuntimeStoreMailboxEntryTimesOutWhenPoolSaturated(t *testing.T) {
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

	ctx := context.Background()
	message := team.MailMessage{
		ID:        "mailbox-saturated",
		FromAgent: "child",
		ToAgent:   "parent",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "saturate the pool",
		Metadata: agentcontrol.Envelope{
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
	}
	const sessionID = "session-saturated"
	// Open + migrate + happy path under the default timeout.
	_, _, err = store.AppendAgentControlMailbox(ctx, sessionID, message)
	require.NoError(t, err)

	store.opTimeout = 300 * time.Millisecond
	conn, err := store.db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()

	second := message
	second.ID = "mailbox-saturated-2"
	start := time.Now()
	_, _, err = store.AppendAgentControlMailbox(ctx, sessionID, second)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second, "mailbox append must fail bounded instead of hanging")
}

func TestSQLiteRuntimeStoreNotifyDropStats(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{DSN: "file:runtime-store-notify-drops?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	store.watchMu.Lock()
	if store.eventWatchers == nil {
		store.eventWatchers = map[int64]eventWatcher{}
	}
	store.nextWatchID++
	store.eventWatchers[store.nextWatchID] = eventWatcher{ch: make(chan runtimeevents.Event, 1)}
	store.watchMu.Unlock()

	event := runtimeevents.Event{Type: "tool.completed", SessionID: "s-drop"}
	store.notifyEventWatchers(1, event)
	store.notifyEventWatchers(2, event)

	stats := store.NotifyDropStats()
	require.Equal(t, int64(1), stats.Events)
	require.Equal(t, int64(0), stats.Mailbox)
	require.Equal(t, int64(0), stats.AgentControl)
}
