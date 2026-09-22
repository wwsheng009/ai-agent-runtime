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
	return newWakeConsumerTestHostWithConfig(t, name, supervision.WakeSchedulerConfig{})
}

// newWakeConsumerTestHostWithConfig is the same harness with explicit wake
// scheduler tuning. The turn-end self-check (plan P1-6 方案 4) needs a
// one-unit class budget plus its own allowance to be observable.
func newWakeConsumerTestHostWithConfig(t *testing.T, name string, schedulerConfig supervision.WakeSchedulerConfig) (*localChatRuntimeHost, *supervision.SQLiteSupervisionStore, *syncWaitDeliveries) {
	t.Helper()
	// 2026-09-22 手动核查调整（docs/plan/supervision-manual-audit-plan-20260922.md）：
	// turn 结束自动 drain 现在默认关闭，本夹具显式打开
	// （supervision.turn_end_check=true）以覆盖灰度回退语义；默认关闭的对照见
	// TestLocalHostTurnEndCheck_DefaultOffKeepsWakePending。
	return newWakeConsumerTestHostWithTurnEndCheck(t, name, schedulerConfig, true)
}

// newWakeConsumerTestHostWithTurnEndCheck additionally controls the
// supervision.turn_end_check fallback switch: true restores the historical
// turn-end auto drain (2026-09-16 §6.5 规则 2), false keeps the manual audit
// default where a pending wake stays durable until the next natural turn
// preflight or an explicit /supervision wake --deliver.
func newWakeConsumerTestHostWithTurnEndCheck(t *testing.T, name string, schedulerConfig supervision.WakeSchedulerConfig, turnEndCheck bool) (*localChatRuntimeHost, *supervision.SQLiteSupervisionStore, *syncWaitDeliveries) {
	t.Helper()
	store, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{
		DSN: "file:" + name + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	scheduler := supervision.NewWakeScheduler(store, schedulerConfig)
	deliveries := &syncWaitDeliveries{}
	turnEnd := turnEndCheck

	var host *localChatRuntimeHost
	host = &localChatRuntimeHost{
		EventBus: runtimeevents.NewBusWithRetention(16),
		BaseSession: &ChatSession{
			RuntimeSession: &runtimechat.Session{ID: "root-session"},
		},
		RuntimeStore:      runtimechat.NewInMemoryRuntimeStore(16),
		supervisionConfig: supervision.Config{TurnEndCheck: &turnEnd},
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
			Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *supervision.Digest, wakeIDs []string) error {
				deliveries.record(parentSessionID, digest, wakeIDs)
				return nil
			},
		},
	}
	host.bindSupervisionWakeConsumer()
	return host, store, deliveries
}

// TestLocalHostTurnEndCheck_DefaultOffKeepsWakePending pins the manual audit
// default: with supervision.turn_end_check unset/false the host must not
// subscribe the turn-end drain, so a pending wake stays durable and is only
// surfaced by the next natural turn preflight or by an explicit
// /supervision wake --deliver.
func TestLocalHostTurnEndCheck_DefaultOffKeepsWakePending(t *testing.T) {
	host, store, deliveries := newWakeConsumerTestHostWithTurnEndCheck(t, "aicli-turn-end-check-off", supervision.WakeSchedulerConfig{}, false)
	ctx := context.Background()
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: "root-session",
		Status:    runtimechat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))
	scheduleCriticalWake(t, store, host.supervisionWake.Wakes)

	host.EventBus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "root-session",
		Payload:   map[string]interface{}{"success": true},
	})

	require.Equal(t, 0, deliveries.count(), "turn_end_check=false 时 turn 结束不得自动 drain")
	pending, err := store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:   "root-session",
		UnclaimedOnly: true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "wake 必须保持 durable，等待下一次自然 turn 的 preflight 或显式投递")

	// 显式投递仍走同一 runnable 门：空闲父会话可以被手动 drain。
	require.NoError(t, host.supervisionWake.MaybeWakeParent(ctx, "root-session", "", "root-session"))
	require.Equal(t, 1, deliveries.count(), "显式投递必须仍然可用")
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

// TestLocalHostWakeConsumer_WakeTurnOwnsComposerStatus is the regression test
// for the "Worked for 28m 41s but still running" report. The foreground
// sendMessage releases the actor turn gate when its executor returns and only
// then reaches its deferred CompleteWaiting, so a supervision auto-wake can
// begin an internal run in between. The internal run owns the status line: the
// late foreground completion must not freeze a "Worked for …" summary over
// the wake turn, and the wake turn must keep the composer in a running state.
func TestLocalHostWakeConsumer_WakeTurnOwnsComposerStatus(t *testing.T) {
	host, _, _ := newWakeConsumerTestHost(t, "aicli-wake-composer-status")
	session := host.BaseSession
	require.NotNil(t, session)
	interaction := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	// Foreground turn in flight: sendMessage already called StartWaiting and
	// the executor has returned, releasing the actor turn gate.
	interaction.StartWaiting()
	interaction.mu.Lock()
	interaction.dynamicStatusStarted = time.Now().Add(-5 * time.Second)
	interaction.mu.Unlock()

	// The wake wins the released gate and starts its internal run.
	endRun := host.beginWakeTurnRun()

	// sendMessage now reaches its deferred CompleteWaiting.
	interaction.CompleteWaiting()
	interaction.mu.Lock()
	completed := interaction.dynamicStatusCompleted
	waiting := interaction.waitingActive
	interaction.mu.Unlock()
	require.False(t, completed, "wake turn must not inherit the foreground Worked for summary")
	require.False(t, waiting, "CompleteWaiting must still release the foreground waiting flag")
	require.NotEqual(t, "Ready", interaction.currentSurfaceStateForTest(),
		"wake turn must own a running composer state, not the foreground Ready state")

	endRun()
}

// projectCriticalWake writes one critical lifecycle notification plus its
// durable wake under an explicit subject id.
func projectCriticalWake(t *testing.T, store *supervision.SQLiteSupervisionStore, scheduler *supervision.WakeScheduler, subjectID string) {
	t.Helper()
	_, err := supervision.ProjectLifecycle(context.Background(), store, scheduler, supervision.LifecycleProjection{
		RootScopeID:           "root-session",
		TargetParentSessionID: "root-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             subjectID,
		EventType:             "exception",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
	})
	require.NoError(t, err)
}

// TestLocalHostTurnEndSelfCheck_DeliversDeferredWake verifies plan P1-6 方案 4
// on the aicli host: when the class budget defers a wake, the parent turn end
// still starts one bounded digest-only turn, and the allowance stops a second
// one inside the same window.
func TestLocalHostTurnEndSelfCheck_DeliversDeferredWake(t *testing.T) {
	host, store, deliveries := newWakeConsumerTestHostWithConfig(t, "aicli-self-check", supervision.WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
		SelfCheckPerWindow:   1,
	})
	ctx := context.Background()
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: "root-session",
		Status:    runtimechat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))

	// The first critical event spends the whole bounded class budget.
	projectCriticalWake(t, store, host.supervisionWake.Wakes, "child-1")
	require.NoError(t, host.wakeSupervisedParent(ctx, "root-session", "root-session"))
	deliveries.wait(t, 5*time.Second)
	require.Equal(t, 1, deliveries.count())

	// The second event is deferred: the natural drain keeps it durable.
	projectCriticalWake(t, store, host.supervisionWake.Wakes, "child-2")
	require.ErrorIs(t, host.wakeSupervisedParent(ctx, "root-session", "root-session"), supervision.ErrWakeRateLimited)
	require.Equal(t, 1, deliveries.count())

	// The parent turn ends: the self-check starts one digest-only turn.
	host.EventBus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "root-session",
		Payload:   map[string]interface{}{"success": true},
	})
	deliveries.wait(t, 5*time.Second)
	require.Equal(t, 2, deliveries.count(), "the deferred wake must get one self-check turn")

	// A second turn end in the same window must not start another one.
	host.EventBus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "root-session",
		Payload:   map[string]interface{}{"success": true},
	})
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, 2, deliveries.count(), "the self-check allowance is per window")
}
