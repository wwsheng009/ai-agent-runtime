package commands

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func newLocalSupervisionTestHost(t *testing.T) *localChatRuntimeHost {
	t.Helper()
	plane, err := runtimeserver.BuildSupervisionControlPlane(t.TempDir(), supervision.Config{}, runtimeserver.SupervisionRuntimeHooks{})
	require.NoError(t, err)
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	host := &localChatRuntimeHost{
		Supervision:       plane,
		supervisionConfig: supervision.DefaultConfig(),
		TeamStore:         teamStore,
	}
	t.Cleanup(func() {
		_ = plane.Close()
		_ = teamStore.Close()
	})
	return host
}

func TestLocalSubagentBatchLifecycleProjectorPersistsAndDeduplicates(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	projector := localSubagentBatchLifecycleProjector(host)
	event := agent.BatchTerminalLifecycle{
		BatchID:         "batch-failed-1",
		RootScopeID:     "parent-session",
		ParentSessionID: "parent-session",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchFailed,
		EventType:       "subagent.batch.failed",
		SubjectVersion:  3,
		TaskCount:       2,
		CompletedCount:  1,
		FailedCount:     1,
		ErrorClass:      "provider",
		Error:           "provider unavailable",
	}
	require.NoError(t, projector(context.Background(), event))
	require.NoError(t, projector(context.Background(), event))

	notifications, err := host.Supervision.Store.ListNotifications(context.Background(), supervision.NotificationFilter{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "batch-failed-1",
		IncludeResolved:       true,
		Limit:                 10,
	})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, supervision.SeverityCritical, notifications[0].Severity)
	// 2026-09-26 真机：批次已终态失败 ⇒ terminated（blocked 只留给"等外部裁决"
	// 的行；否则 digest/矩阵会把失败读成"待决策卡住"，与 reason 自相矛盾）。
	require.Equal(t, supervision.SupervisionTerminated, notifications[0].SupervisionState)
	require.Equal(t, supervision.ResolutionUnresolved, notifications[0].ResolutionState)
}

func TestLocalSubagentBatchRecoveryLifecycleProjectorDefersWakeDelivery(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.BaseSession = &ChatSession{}
	ctx := context.Background()
	deliveries := 0
	host.supervisionWake = &supervision.WakeConsumer{
		Wakes: host.Supervision.Wakes,
		Runnable: func(context.Context, string, string, string) bool {
			return true
		},
		Deliver: func(context.Context, string, string, *supervision.Digest, []string) error {
			deliveries++
			return nil
		},
	}

	projector := localSubagentBatchRecoveryLifecycleProjector(host)
	require.NoError(t, projector(ctx, agent.BatchTerminalLifecycle{
		BatchID:         "batch-recovery-failed",
		RootScopeID:     "parent-session",
		ParentSessionID: "parent-session",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchFailed,
		EventType:       "subagent.batch.failed",
		SubjectVersion:  1,
		TaskCount:       1,
		FailedCount:     1,
		Error:           "provider unavailable",
	}))
	require.Zero(t, deliveries, "startup recovery must not drain the wake")

	pending, err := host.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "startup recovery must leave a durable wake")

	require.NoError(t, host.wakeSupervisedParent(ctx, "parent-session", "parent-session"))
	require.Equal(t, 1, deliveries, "a later runnable transition must deliver the wake")

	pending, err = host.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestLocalSubagentBatchRecoveryLifecycleProjectorKeepsHeadlessWakeDelivery(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.BaseSession = &ChatSession{NoInteractive: true}
	deliveries := 0
	host.supervisionWake = &supervision.WakeConsumer{
		Wakes: host.Supervision.Wakes,
		Runnable: func(context.Context, string, string, string) bool {
			return true
		},
		Deliver: func(context.Context, string, string, *supervision.Digest, []string) error {
			deliveries++
			return nil
		},
	}

	require.NoError(t, localSubagentBatchRecoveryLifecycleProjector(host)(
		context.Background(),
		agent.BatchTerminalLifecycle{
			BatchID:         "batch-headless-recovery-failed",
			RootScopeID:     "parent-session",
			ParentSessionID: "parent-session",
			ExecutionMode:   subagentbatch.ExecutionModeBackground,
			Status:          subagentbatch.BatchFailed,
			EventType:       "subagent.batch.failed",
			SubjectVersion:  1,
			TaskCount:       1,
			FailedCount:     1,
			Error:           "provider unavailable",
		},
	))
	require.Equal(t, 1, deliveries, "headless startup recovery must keep immediate wake delivery")
}

func TestResolveLocalChatSupervisionDataDir(t *testing.T) {
	session := &ChatSession{SessionDir: t.TempDir()}
	require.Equal(t, filepath.Join(session.SessionDir, "runtime", "supervision"), resolveLocalChatSupervisionDataDir(session, nil))

	ephemeral := &ChatSession{Ephemeral: true}
	require.NotEmpty(t, resolveLocalChatSupervisionDataDir(ephemeral, nil))
}

// TestInjectLocalSupervisionPreflight verifies the CLI path automatically
// injects a durable lifecycle digest and only records delivered+seen (not ack).
func TestInjectLocalSupervisionPreflight(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	ctx := context.Background()

	notification, err := host.Supervision.Store.UpsertNotification(ctx, supervision.Notification{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-agent",
		SubjectVersion:        1,
		EventSeq:              5,
		EventType:             "timeout",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionTimedOut,
		Reason:                "deadline exceeded",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)

	prompt, err := injectLocalSupervisionPreflight(ctx, host, "parent-session", "continue work", nil)
	require.NoError(t, err)
	require.Contains(t, prompt, "[Child lifecycle preflight]")
	require.Contains(t, prompt, "child-agent")
	require.Contains(t, prompt, "continue work")

	updated, err := host.Supervision.Store.GetNotification(ctx, notification.NotificationID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, supervision.DeliverySeen, updated.DeliveryState)
	require.Equal(t, supervision.DecisionUnacknowledged, updated.DecisionState)
}

// TestInjectLocalSupervisionPreflight_IncludesWakeBudgetLine covers the P1-6
// follow-up on the CLI path: local turns render the same auto-wake ledger as
// the API preflight, so a deferral (rate-limited wake) stays observable inside
// the turn. A host with a store but no wake scheduler keeps the previous text
// instead of advertising an invented 0/limit budget.
func TestInjectLocalSupervisionPreflight_IncludesWakeBudgetLine(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	ctx := context.Background()
	// The default local ledger is the in-process one, so usage is consumed by
	// actually delivering a wake (not by writing a durable claim row).
	_, err := supervision.ProjectLifecycle(ctx, host.Supervision.Store, host.Supervision.Wakes, supervision.LifecycleProjection{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-agent",
		EventType:             supervision.WakeReasonExecutionFailed,
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionTimedOut,
	})
	require.NoError(t, err)
	claimed, _, err := host.Supervision.Wakes.DrainRunnable(ctx, "parent-session", "", "parent-session", nil)
	require.NoError(t, err)
	require.NotEmpty(t, claimed, "the failure wake must be delivered before the ledger shows usage")

	prompt, err := injectLocalSupervisionPreflight(ctx, host, "parent-session", "continue work", nil)
	require.NoError(t, err)
	require.Contains(t, prompt, "wake_budget:")
	require.Contains(t, prompt, "failure=1/5")
	require.Contains(t, prompt, "approval=0/unlimited")
	require.Contains(t, prompt, "continue work")

	noLedger := &localChatRuntimeHost{Supervision: &runtimeserver.SupervisionControlPlane{Store: host.Supervision.Store}}
	noLedgerPrompt, err := injectLocalSupervisionPreflight(ctx, noLedger, "parent-session", "continue work", nil)
	require.NoError(t, err)
	require.NotContains(t, noLedgerPrompt, "wake_budget:")
	require.Contains(t, noLedgerPrompt, "[Child lifecycle preflight]")
}

// TestInjectLocalSupervisionPreflight_IncludesBatchProgress covers the P0-B
// acceptance on the CLI path: an active background batch whose children are
// mostly done reaches the parent turn as "2/3 completed, worker-2 running" even
// though no lifecycle notification exists. Before P0-B a healthy batch was
// invisible, so "巡查" could only ever surface failures.
func TestInjectLocalSupervisionPreflight_IncludesBatchProgress(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	ctx := context.Background()
	batchStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "subagent_batches.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = batchStore.Close() })
	host.SubagentBatches = batchStore

	_, err = batchStore.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         "batch-progress-1",
		RootScopeID:     "parent-session",
		ParentSessionID: "parent-session",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       3,
		CompletedCount:  2,
		RunningCount:    1,
	}, []subagentbatch.SubagentTaskRecord{
		{TaskID: "worker-1", ChildSessionID: "session-1", Status: subagentbatch.TaskSucceeded},
		{TaskID: "worker-2", ChildSessionID: "session-2", Status: subagentbatch.TaskRunning},
		{TaskID: "worker-3", ChildSessionID: "session-3", Status: subagentbatch.TaskSucceeded},
	})
	require.NoError(t, err)

	prompt, err := injectLocalSupervisionPreflight(ctx, host, "parent-session", "continue work", nil)
	require.NoError(t, err)
	require.Contains(t, prompt, "[Child lifecycle preflight]")
	require.Contains(t, prompt, "progress:")
	require.Contains(t, prompt, "batch-progress-1: 2/3 completed, 1 running")
	require.Contains(t, prompt, "worker-2: running; session=session-2")
	require.NotContains(t, prompt, "worker-1", "terminal children stay out of the rollup")
	require.Contains(t, prompt, "continue work")

	// A host without a batch store keeps the pre-P0-B text: the rollup is opt-in,
	// and with nothing to report the prompt is returned untouched.
	unwired := newLocalSupervisionTestHost(t)
	unwiredPrompt, err := injectLocalSupervisionPreflight(ctx, unwired, "parent-session", "continue work", nil)
	require.NoError(t, err)
	require.Equal(t, "continue work", unwiredPrompt)

	// Batches owned by another parent never leak into this turn's scope.
	otherPrompt, err := injectLocalSupervisionPreflight(ctx, host, "other-session", "continue work", nil)
	require.NoError(t, err)
	require.NotContains(t, otherPrompt, "batch-progress-1")
}

func TestInjectLocalSupervisionPreflight_DoesNotRepeatAcknowledgedNotification(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	ctx := context.Background()

	notification, err := host.Supervision.Store.UpsertNotification(ctx, supervision.Notification{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "acknowledged-child",
		SubjectVersion:        1,
		EventType:             "subagent.batch.failed",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
		Reason:                "provider timeout",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)
	ok, err := host.Supervision.Store.AcknowledgeNotification(ctx, notification.NotificationID, time.Now().UTC(), notification.Version)
	require.NoError(t, err)
	require.True(t, ok)

	prompt, err := injectLocalSupervisionPreflight(ctx, host, "parent-session", "continue work", nil)
	require.NoError(t, err)
	require.Equal(t, "continue work", prompt)
}

// TestInjectLocalSupervisionPreflight_DoesNotLeakTeamInboxToWorker ensures a
// team task worker does not consume a Team lead's lifecycle notifications.
func TestInjectLocalSupervisionPreflight_DoesNotLeakTeamInboxToWorker(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	ctx := context.Background()
	_, err := host.TeamStore.CreateTeam(ctx, team.Team{ID: "team-1", LeadSessionID: "lead-session", Status: team.TeamStatusActive})
	require.NoError(t, err)

	notification, err := host.Supervision.Store.UpsertNotification(ctx, supervision.Notification{
		RootScopeID:           "team-1",
		TargetParentSessionID: "lead-session",
		TargetParentTeamID:    "team-1",
		SubjectKind:           supervision.SubjectTeam,
		SubjectID:             "child-team",
		SubjectVersion:        1,
		EventSeq:              7,
		EventType:             "orphaned",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionOrphaned,
		Reason:                "orchestrator missing",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)

	workerPrompt, err := injectLocalSupervisionPreflight(ctx, host, "worker-session", "perform task", &team.RunMeta{Team: &team.TeamRunMeta{TeamID: "team-1"}})
	require.NoError(t, err)
	require.Equal(t, "perform task", workerPrompt)

	updated, err := host.Supervision.Store.GetNotification(ctx, notification.NotificationID)
	require.NoError(t, err)
	require.Equal(t, supervision.DeliveryPending, updated.DeliveryState)

	leadPrompt, err := injectLocalSupervisionPreflight(ctx, host, "lead-session", "review team", &team.RunMeta{Team: &team.TeamRunMeta{TeamID: "team-1"}})
	require.NoError(t, err)
	require.Contains(t, leadPrompt, "child-team")
}

// TestLocalActorRegistry_SubmitPromptInjectsPreflight covers the CLI actor
// registry integration point mandated by the P2 plan. The action aborts at the
// intentionally unavailable SessionHub after preflight, which is enough to
// prove the hook ran and marked the notification seen.
func TestLocalActorRegistry_SubmitPromptInjectsPreflight(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	registry := newLocalActorRegistry(host)
	ctx := context.Background()

	notification, err := host.Supervision.Store.UpsertNotification(ctx, supervision.Notification{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-agent",
		SubjectVersion:        1,
		EventSeq:              9,
		EventType:             "invalid",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionInvalid,
		Reason:                "worker state invalid",
		DecisionState:         supervision.DecisionUnacknowledged,
		ResolutionState:       supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)

	_, err = registry.SubmitPrompt(ctx, "parent-session", "continue", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "session hub not configured")

	updated, err := host.Supervision.Store.GetNotification(ctx, notification.NotificationID)
	require.NoError(t, err)
	require.Equal(t, supervision.DeliverySeen, updated.DeliveryState)
}

// TestRequeueSupervisionWakeKeepsRetryPath 覆盖 plan §6-F：CLI 的投递是异步的，
// 唤醒行在 Deliver 返回时就被 resolve；因此提交失败若只被记录，父会话唯一的
// auto-wake 就被静默消费。requeue 必须留下一条新的 durable wake 并发布可观测事件。
func TestRequeueSupervisionWakeKeepsRetryPath(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.EventBus = runtimeevents.NewBusWithRetention(8)
	var mu sync.Mutex
	var events []runtimeevents.Event
	host.EventBus.Subscribe(EventSupervisionWakeDeliveryFailed, func(event runtimeevents.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	})

	host.requeueSupervisionWake("parent-session", "parent-session", []string{"wake-1"}, errors.New("actor registry is not ready"))

	ctx := context.Background()
	pending, err := host.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "a failed delivery must leave a durable wake behind")
	require.Equal(t, "critical_lifecycle", pending[0].WakeReason)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 1)
	require.Equal(t, "parent-session", events[0].Payload["parent_session_id"])
	require.Equal(t, "parent-session", events[0].Payload["root_scope_id"])
	require.Equal(t, "wake-1", events[0].Payload["wake_ids"])
	require.Equal(t, "actor registry is not ready", events[0].Payload["delivery_error"])
}

// TestRequeueSupervisionWakeIgnoresIncompleteScope 固定空 scope 的保护：没有
// root scope 时不能伪造 wake（ScheduleWake 会拒绝，等于最坏情况的静默失败）。
func TestRequeueSupervisionWakeIgnoresIncompleteScope(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.EventBus = runtimeevents.NewBusWithRetention(8)
	delivered := 0
	host.EventBus.Subscribe(EventSupervisionWakeDeliveryFailed, func(runtimeevents.Event) { delivered++ })

	host.requeueSupervisionWake("parent-session", "", nil, errors.New("boom"))

	pending, err := host.Supervision.Store.ListWakePending(context.Background(), supervision.WakeFilter{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending)
	require.Zero(t, delivered)
}
