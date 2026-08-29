package commands

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// newWakeConsumerTestHost builds a minimal localChatRuntimeHost wired with a
// durable supervision store and a wake consumer whose Deliver records calls.
func newWakeConsumerTestHost(t *testing.T, name string) (*localChatRuntimeHost, *supervision.SQLiteSupervisionStore, *syncWaitDeliveries) {
	t.Helper()
	store, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{
		DSN: "file:" + name + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	scheduler := supervision.NewWakeScheduler(store, supervision.WakeSchedulerConfig{})
	deliveries := &syncWaitDeliveries{}

	var host *localChatRuntimeHost
	host = &localChatRuntimeHost{
		EventBus: runtimeevents.NewBusWithRetention(16),
		BaseSession: &ChatSession{
			RuntimeSession: &runtimechat.Session{ID: "root-session"},
		},
		RuntimeStore: runtimechat.NewInMemoryRuntimeStore(16),
		supervisionWake: &supervision.WakeConsumer{
			Wakes: scheduler,
			Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool {
				if host == nil || host.RuntimeStore == nil {
					return false
				}
				state, err := host.RuntimeStore.LoadState(ctx, parentSessionID)
				if err != nil || state == nil {
					return false
				}
				return !state.Summary().Busy()
			},
			Deliver: func(ctx context.Context, parentSessionID string, digest *supervision.Digest, wakeIDs []string) error {
				deliveries.record(parentSessionID, digest, wakeIDs)
				return nil
			},
		},
	}
	host.bindSupervisionWakeConsumer()
	return host, store, deliveries
}

// syncWaitDeliveries records wake deliveries with a channel for waiting.
type syncWaitDeliveries struct {
	mu     sync.Mutex
	calls  int
	parent string
	ch     chan struct{}
}

func (d *syncWaitDeliveries) record(parentSessionID string, digest *supervision.Digest, wakeIDs []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.parent = parentSessionID
	if d.ch == nil {
		d.ch = make(chan struct{}, 1)
	}
	select {
	case d.ch <- struct{}{}:
	default:
	}
}

func (d *syncWaitDeliveries) wait(t *testing.T, timeout time.Duration) {
	t.Helper()
	d.mu.Lock()
	ch := d.ch
	d.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for wake delivery")
	}
}

func (d *syncWaitDeliveries) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *syncWaitDeliveries) lastParent() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.parent
}

// scheduleCriticalWake writes one critical lifecycle notification plus its
// durable wake for the root parent.
func scheduleCriticalWake(t *testing.T, store *supervision.SQLiteSupervisionStore, scheduler *supervision.WakeScheduler) {
	t.Helper()
	_, err := supervision.ProjectLifecycle(context.Background(), store, scheduler, supervision.LifecycleProjection{
		RootScopeID:           "root-session",
		TargetParentSessionID: "root-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
	})
	require.NoError(t, err)
}

// TestLocalHostWakeConsumer_ParentTurnEndDrainsWake verifies the P2 closure
// on the aicli host: wakes accumulated while the parent was busy are drained
// and delivered as a parent turn as soon as the parent session ends a turn.
func TestLocalHostWakeConsumer_ParentTurnEndDrainsWake(t *testing.T) {
	host, store, deliveries := newWakeConsumerTestHost(t, "aicli-wake-sessionend")
	ctx := context.Background()

	// Parent currently running (busy): completion-time drain must not fire.
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: "root-session",
		Status:    runtimechat.SessionRunning,
		UpdatedAt: time.Now().UTC(),
	}))
	scheduleCriticalWake(t, store, host.supervisionWake.Wakes)
	err := host.wakeSupervisedParent(ctx, "root-session", "root-session")
	require.ErrorIs(t, err, supervision.ErrWakeParentBusy)
	require.Equal(t, 0, deliveries.count(), "busy parent must not receive a wake turn")

	// Parent finishes its turn and becomes idle.
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: "root-session",
		Status:    runtimechat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))
	host.EventBus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "root-session",
		Payload:   map[string]interface{}{"success": true},
	})

	deliveries.wait(t, 5*time.Second)
	require.Equal(t, 1, deliveries.count())
	require.Equal(t, "root-session", deliveries.lastParent())

	// The wake was resolved: nothing left to drain.
	claimed, digest, err := host.supervisionWake.Wakes.DrainRunnable(ctx, "root-session", "", "root-session", func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true })
	require.NoError(t, err)
	require.Empty(t, claimed)
	require.Nil(t, digest)
}

// TestLocalHostWakeConsumer_CompletionTimeWakeWhenIdle verifies the aicli
// completion projection point: an idle parent is woken immediately when a
// critical child completion arrives (no explicit wait needed).
func TestLocalHostWakeConsumer_CompletionTimeWakeWhenIdle(t *testing.T) {
	host, store, deliveries := newWakeConsumerTestHost(t, "aicli-wake-completion")
	ctx := context.Background()
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: "root-session",
		Status:    runtimechat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))
	scheduleCriticalWake(t, store, host.supervisionWake.Wakes)

	require.NoError(t, host.wakeSupervisedParent(ctx, "root-session", "root-session"))
	deliveries.wait(t, 5*time.Second)
	require.Equal(t, 1, deliveries.count())
}

// TestLocalHostWakeConsumer_ParentSessionEndIgnoresOtherSessions verifies
// the turn-end subscription only reacts to the root parent session.
func TestLocalHostWakeConsumer_ParentSessionEndIgnoresOtherSessions(t *testing.T) {
	host, store, deliveries := newWakeConsumerTestHost(t, "aicli-wake-filter")
	ctx := context.Background()
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: "root-session",
		Status:    runtimechat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))
	scheduleCriticalWake(t, store, host.supervisionWake.Wakes)

	// A child session end must not consume the parent's wake.
	host.EventBus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "child-1",
		Payload:   map[string]interface{}{"success": true},
	})
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, 0, deliveries.count())

	// The root parent turn end does.
	host.EventBus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "root-session",
		Payload:   map[string]interface{}{"success": true},
	})
	deliveries.wait(t, 5*time.Second)
	require.Equal(t, 1, deliveries.count())
}

// TestLocalHostWakeConsumer_WakeTurnAppliesRunEpochProtocol is the
// regression test for the blank-parent-UI bug: the supervision wake turn
// used to be submitted without BeginRun, so every runtime event it emitted
// was captured with run epoch 0 and rejected by the epoch fence
// (isRunEpochCurrent) — the parent UI stayed blank for the whole wake while
// the debug log filled with "render suppressed ... closed run epoch". The
// wake turn must join the run-epoch protocol like every other turn.
func TestLocalHostWakeConsumer_WakeTurnAppliesRunEpochProtocol(t *testing.T) {
	host, _, _ := newWakeConsumerTestHost(t, "aicli-wake-epoch")
	require.NotNil(t, host.BaseSession)

	bridge := ensureChatRuntimeEventBridge(host.BaseSession)
	require.NotNil(t, bridge)

	// Bug precondition: before any run has begun the fence rejects every
	// event ("closed run epoch") — this is exactly what blanked the UI.
	require.Zero(t, bridge.currentRunEpoch())
	require.False(t, bridge.isRunEpochCurrent(1))

	// Engaging the wake-turn run protocol advances the epoch.
	endRun := host.beginWakeTurnRun()
	require.Equal(t, uint64(1), bridge.currentRunEpoch())
	require.True(t, bridge.isRunEpochCurrent(1), "wake turn events must clear the epoch fence")

	// EndRun keeps the epoch at 1: late ambient events of the same run stay
	// renderable (documented isRunEpochCurrent contract).
	endRun()
	require.Equal(t, uint64(1), bridge.currentRunEpoch())
	require.True(t, bridge.isRunEpochCurrent(1))
}
