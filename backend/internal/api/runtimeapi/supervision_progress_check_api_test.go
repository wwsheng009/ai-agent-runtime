package runtimeapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// apiProgressCheckParentSession 是巡查夹具的父会话 id（scope 是会话本身，与
// preflight digest / wake consumer 同口径）。
const apiProgressCheckParentSession = "api-progress-parent"

// apiProgressCheckBatch 落一条指定状态的真实 batch 行：巡查的判定完全建立在
// durable 投影之上，用假 store 会绕过真正的 ListBatches/ListTasks。
func apiProgressCheckBatch(t *testing.T, store subagentbatch.BatchStore, batchID, parentSessionID string, status subagentbatch.BatchStatus) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          status,
		TaskCount:       1,
		RunningCount:    1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	task := subagentbatch.SubagentTaskRecord{
		TaskID:         "task-" + batchID,
		ChildSessionID: "child-" + batchID,
		Status:         subagentbatch.TaskRunning,
		OrderIndex:     1,
		UpdatedAt:      now,
		Version:        1,
	}
	if status.Terminal() {
		finished := now
		batch.FinishedAt = &finished
		batch.RunningCount = 0
		batch.CompletedCount = 1
		task.Status = subagentbatch.TaskSucceeded
	}
	created, err := store.CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{task})
	require.NoError(t, err)
	require.True(t, created, "batch fixture must be created")
}

// newAPIProgressCheckFixture 组装 P2-D 的 API 宿主：durable supervision store +
// 真实 batch store + 已存在的空闲父会话 actor + 计数 wake consumer。
//
// 只有「父会话空闲 + 有 active batch + 无待投递 wake + 预算未耗尽」四条同时成立
// 才允许注入汇报 turn，这正是本文件要钉住的契约（与 CLI 的
// chat_actor_progress_check_test.go 逐条对应）。
func newAPIProgressCheckFixture(t *testing.T, storeName string, batchStatus subagentbatch.BatchStatus) (*Handler, *int) {
	t.Helper()
	ctx := context.Background()
	handler, _ := newAPISupervisionBudgetTestHandler(t, storeName)
	handler.SetSubagentBatchStore(newAPISupervisionBatchStore(t))
	apiProgressCheckBatch(t, handler.getSubagentBatchStore(), "batch-"+storeName, apiProgressCheckParentSession, batchStatus)

	apiAgent := handler.newAPIAgent(&agent.Config{Name: "api-progress-agent", Model: "test-model", MaxSteps: 3})
	require.NotNil(t, apiAgent)
	t.Cleanup(func() { _ = apiAgent.Close() })

	runtimeStore := chat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID: apiProgressCheckParentSession,
		Status:    chat.SessionIdle,
	}))
	handler.sessionRuntimeStore = runtimeStore

	actor, err := chat.NewSessionActor(apiProgressCheckParentSession, chat.SessionActorConfig{
		Agent:      apiAgent,
		StateStore: runtimeStore,
		EventStore: runtimeStore,
	})
	require.NoError(t, err)
	handler.sessionHub = chat.NewSessionHub(func(string) (*chat.SessionActor, error) { return actor, nil })
	t.Cleanup(handler.sessionHub.StopAll)
	_, err = handler.sessionHub.GetOrCreate(apiProgressCheckParentSession)
	require.NoError(t, err)

	// 投递必须被计数而不是真起 turn：巡查的契约是"允许注入汇报 turn"，被测的是
	// admission，不是 agent 执行本身（与 CLI 夹具同做法）。
	deliveries := 0
	handler.supervisionWakeMu.Lock()
	handler.supervisionWake = &supervision.WakeConsumer{
		Wakes:    handler.getSupervisionWakeScheduler(),
		Runnable: func(context.Context, string, string, string) bool { return true },
		Deliver: func(context.Context, string, string, *supervision.Digest, []string) error {
			deliveries++
			return nil
		},
	}
	handler.supervisionWakeMu.Unlock()
	return handler, &deliveries
}

func requireNoPendingAPIWake(t *testing.T, handler *Handler, parentSessionID, message string) {
	t.Helper()
	pending, err := handler.getSupervisionStore().ListWakePending(context.Background(), supervision.WakeFilter{
		RootScopeID:           parentSessionID,
		TargetParentSessionID: parentSessionID,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending, message)
}

func TestAPISupervisionProgressCheckInjectsReportTurn(t *testing.T) {
	handler, deliveries := newAPIProgressCheckFixture(t, "api-progress-turn", subagentbatch.BatchRunning)

	reported, err := handler.runSupervisionProgressCheckOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, reported, "an active batch with an idle parent must produce exactly one report turn")
	require.Equal(t, 1, *deliveries)
	requireNoPendingAPIWake(t, handler, apiProgressCheckParentSession, "the injected turn claims the progress wake")
}

func TestAPISupervisionProgressCheckSkipsWithoutActiveBatch(t *testing.T) {
	handler, deliveries := newAPIProgressCheckFixture(t, "api-progress-terminal", subagentbatch.BatchCompleted)

	reported, err := handler.runSupervisionProgressCheckOnce(context.Background())
	require.NoError(t, err)
	require.Zero(t, reported, "a terminal batch has no new progress to report")
	require.Zero(t, *deliveries)
	requireNoPendingAPIWake(t, handler, apiProgressCheckParentSession, "no active batch must not even schedule a wake row")
}

func TestAPISupervisionProgressCheckSkipsBusyParent(t *testing.T) {
	handler, deliveries := newAPIProgressCheckFixture(t, "api-progress-busy", subagentbatch.BatchRunning)
	ctx := context.Background()
	require.NoError(t, handler.sessionRuntimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID: apiProgressCheckParentSession,
		Status:    chat.SessionRunning,
	}))

	reported, err := handler.runSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, reported, "a busy parent owns the turn; the sweep must never interleave")
	require.Zero(t, *deliveries)
	requireNoPendingAPIWake(t, handler, apiProgressCheckParentSession, "a skipped sweep leaves no stale wake behind")
}

func TestAPISupervisionProgressCheckDefersToPendingWake(t *testing.T) {
	handler, deliveries := newAPIProgressCheckFixture(t, "api-progress-pending", subagentbatch.BatchRunning)
	ctx := context.Background()
	_, err := handler.getSupervisionWakeScheduler().ScheduleWake(ctx, supervision.WakeRequest{
		RootScopeID:           apiProgressCheckParentSession,
		TargetParentSessionID: apiProgressCheckParentSession,
		WakeReason:            "critical_lifecycle",
	})
	require.NoError(t, err)

	reported, err := handler.runSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, reported, "an already-pending lifecycle wake owns the next turn")
	require.Zero(t, *deliveries)

	pending, err := handler.getSupervisionStore().ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           apiProgressCheckParentSession,
		TargetParentSessionID: apiProgressCheckParentSession,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "the sweep must not stack a second wake on top of a pending one")
	require.Equal(t, "critical_lifecycle", pending[0].WakeReason)
}

// TestAPISupervisionProgressCheckSkipsUnknownParent 钉住多会话宿主的 scope：batch 行
// 指向的父会话若没有 actor（历史/孤儿行），巡查不得为它凭空造 actor，也不得给别的
// 会话注入 turn —— 这是 API 宿主相对 CLI（单根会话）必须额外守住的一条。
func TestAPISupervisionProgressCheckSkipsUnknownParent(t *testing.T) {
	handler, deliveries := newAPIProgressCheckFixture(t, "api-progress-scope", subagentbatch.BatchRunning)
	apiProgressCheckBatch(t, handler.getSubagentBatchStore(), "batch-orphan", "api-progress-unknown-parent", subagentbatch.BatchRunning)

	reported, err := handler.runSupervisionProgressCheckOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, reported, "only the known idle parent may be reported")
	require.Equal(t, 1, *deliveries)
	requireNoPendingAPIWake(t, handler, apiProgressCheckParentSession, "the known parent's wake is claimed by the report turn")
	requireNoPendingAPIWake(t, handler, "api-progress-unknown-parent", "an unknown parent must not be scheduled at all")
	_, actorExists := handler.peekSessionHub().Get("api-progress-unknown-parent")
	require.False(t, actorExists, "the sweep must not create actors for stale batch rows")
}

// TestAPISupervisionProgressCheckLifecycleGatedByConfig 钉住 opt-in 开关的两端：
// 默认（interval=0）不注册 ticker；显式开启后 SetSupervisionConfig 拉起循环，
// StopSupervisionProgressCheck 能等到它真正退出（无残留巡检 goroutine）。
func TestAPISupervisionProgressCheckLifecycleGatedByConfig(t *testing.T) {
	handler, _ := newAPIProgressCheckFixture(t, "api-progress-lifecycle", subagentbatch.BatchRunning)

	t.Run("default config registers no ticker", func(t *testing.T) {
		require.False(t, handler.supervisionTuning().ProgressCheckEnabled(), "the sweep is opt-in, never implicit")
		require.Nil(t, handler.supervisionProgressCheckStop, "a disabled sweep must not start a background loop")
	})

	t.Run("opt-in interval starts and stops with the host", func(t *testing.T) {
		handler.SetSupervisionConfig(supervision.Config{ProgressCheckInterval: time.Minute})
		require.NotNil(t, handler.supervisionProgressCheckStop, "an opt-in interval must register the sweep")

		handler.SetSupervisionConfig(supervision.Config{})
		require.Nil(t, handler.supervisionProgressCheckStop, "reconfiguring back to 0 must converge the loop")

		handler.StopSupervisionProgressCheck() // 幂等：未运行时是 no-op
	})
}

// apiProgressCheckCriticalNotification 落一条未决 critical 通知（不产生 wake），
// 用于钉住 P0-2 改动 2 的 API 侧让位门：critical 未决时巡查不得抢跑汇报 turn；
// resolve 之后必须自动恢复。
func apiProgressCheckCriticalNotification(t *testing.T, handler *Handler) supervision.Notification {
	t.Helper()
	notification, err := handler.getSupervisionStore().UpsertNotification(context.Background(), supervision.Notification{
		RootScopeID:      apiProgressCheckParentSession,
		SubjectKind:      supervision.SubjectAgentRun,
		SubjectID:        "api-child-critical-1",
		SubjectVersion:   1,
		EventType:        "agent_failed",
		Severity:         supervision.SeverityCritical,
		SupervisionState: supervision.SupervisionBlocked,
		ResolutionState:  supervision.ResolutionUnresolved,
	})
	require.NoError(t, err)
	require.True(t, notification.Unresolved())
	return notification
}

// TestAPISupervisionProgressCheckDefersToUnresolvedCritical 钉住 API 侧让位门：
// 未决 critical 存在时本轮不调度、不起 turn、不留 stale wake；critical 被
// resolve 后巡查恢复注入。
func TestAPISupervisionProgressCheckDefersToUnresolvedCritical(t *testing.T) {
	handler, deliveries := newAPIProgressCheckFixture(t, "api-progress-critical-gate", subagentbatch.BatchRunning)
	ctx := context.Background()
	notification := apiProgressCheckCriticalNotification(t, handler)

	reported, err := handler.runSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, reported, "an unresolved critical notification owns the next turn")
	require.Zero(t, *deliveries, "the gated sweep must not start a progress turn")
	requireNoPendingAPIWake(t, handler, apiProgressCheckParentSession, "the gated sweep must not leave a stale progress wake behind")

	ok, err := handler.getSupervisionStore().ResolveNotification(ctx, notification.NotificationID,
		supervision.ResolutionRecovered, time.Now().UTC(), notification.Version)
	require.NoError(t, err)
	require.True(t, ok, "the fixture critical notification must be resolvable")

	reported, err = handler.runSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, reported, "the sweep resumes once the critical notification is resolved")
	require.Equal(t, 1, *deliveries)
}

// TestAPISupervisionProgressCheckSpendsOnlyProgressBudget 钉住 P0-2/ADR-2 的
// API 侧 durable 分账：汇报 turn 只写 progress 类 claim 行，failure/other 的
// 账本与判定完全不受影响。
func TestAPISupervisionProgressCheckSpendsOnlyProgressBudget(t *testing.T) {
	handler, deliveries := newAPIProgressCheckFixture(t, "api-progress-budget-split", subagentbatch.BatchRunning)
	ctx := context.Background()

	reported, err := handler.runSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, reported)
	require.Equal(t, 1, *deliveries)

	store := handler.getSupervisionStore()
	now := time.Now().UTC()
	progressClaims, err := store.CountWakeClaims(ctx, apiProgressCheckParentSession, supervision.WakeBudgetClassProgress, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, progressClaims, "the report turn books one durable progress claim")
	for _, class := range []supervision.WakeBudgetClass{supervision.WakeBudgetClassFailure, supervision.WakeBudgetClassOther} {
		count, err := store.CountWakeClaims(ctx, apiProgressCheckParentSession, class, now.Add(-time.Hour))
		require.NoError(t, err)
		require.Zerof(t, count, "a progress turn must not book a %s claim", class)
	}
	scheduler := handler.getSupervisionWakeScheduler()
	require.True(t, scheduler.AllowAutoWake(ctx, apiProgressCheckParentSession, supervision.WakeBudgetClassFailure, now))
	require.True(t, scheduler.AllowAutoWake(ctx, apiProgressCheckParentSession, supervision.WakeBudgetClassOther, now))
}

// TestAPISupervisionProgressCheckSurvivesExhaustedCriticalBudget 是分账的反方向：
// failure/other 的 durable 额度被整份花光时，progress 巡查仍按自己的额度注入，
// 并且不追加 critical 类 claim。
func TestAPISupervisionProgressCheckSurvivesExhaustedCriticalBudget(t *testing.T) {
	handler, deliveries := newAPIProgressCheckFixture(t, "api-progress-exhausted-budget", subagentbatch.BatchRunning)
	ctx := context.Background()
	store, ok := handler.getSupervisionStore().(*supervision.SQLiteSupervisionStore)
	require.True(t, ok, "the API budget fixture wires a durable SQLite ledger")
	require.NotNil(t, store)

	// 用光共享有界额度：failure/other 各 5 次（默认窗口上限）。
	for i := 0; i < 5; i++ {
		recordBudgetClaim(t, store, fmt.Sprintf("api-other-claim-%d", i), apiProgressCheckParentSession,
			supervision.WakeBudgetClassOther, "critical_lifecycle")
		recordBudgetClaim(t, store, fmt.Sprintf("api-failure-claim-%d", i), apiProgressCheckParentSession,
			supervision.WakeBudgetClassFailure, supervision.WakeReasonExecutionFailed)
	}
	scheduler := handler.getSupervisionWakeScheduler()
	now := time.Now().UTC()
	require.False(t, scheduler.AllowAutoWake(ctx, apiProgressCheckParentSession, supervision.WakeBudgetClassOther, now),
		"the fixture must exhaust the other class")
	require.False(t, scheduler.AllowAutoWake(ctx, apiProgressCheckParentSession, supervision.WakeBudgetClassFailure, now),
		"the fixture must exhaust the failure class")

	reported, err := handler.runSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, reported, "an exhausted critical budget must not block the progress check")
	require.Equal(t, 1, *deliveries)

	progressClaims, err := store.CountWakeClaims(ctx, apiProgressCheckParentSession, supervision.WakeBudgetClassProgress, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, progressClaims)
	otherClaims, err := store.CountWakeClaims(ctx, apiProgressCheckParentSession, supervision.WakeBudgetClassOther, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 5, otherClaims, "the progress turn must not add an other-class claim")
	failureClaims, err := store.CountWakeClaims(ctx, apiProgressCheckParentSession, supervision.WakeBudgetClassFailure, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 5, failureClaims, "the progress turn must not consume the failure budget")
}

// TestAPISupervisionProgressCheckClampsSubFloorInterval 钉住 P0-2 改动 3 的 API
// 侧：配置 1s 仍开启巡查，但生效间隔被钳到 30s（并在 SetSupervisionConfig 的
// 启动路径记 warning）；改回 0 必须回到"无 ticker、无 goroutine"。
func TestAPISupervisionProgressCheckClampsSubFloorInterval(t *testing.T) {
	handler, _ := newAPIProgressCheckFixture(t, "api-progress-clamp", subagentbatch.BatchRunning)

	handler.SetSupervisionConfig(supervision.Config{ProgressCheckInterval: time.Second})
	require.NotNil(t, handler.supervisionProgressCheckStop,
		"a sub-floor opt-in still starts the sweep at the clamped interval")
	require.Equal(t, supervision.MinProgressCheckInterval, handler.supervisionTuning().ProgressCheckInterval)
	handler.StopSupervisionProgressCheck()

	handler.SetSupervisionConfig(supervision.Config{})
	require.False(t, handler.supervisionTuning().ProgressCheckEnabled())
	require.Nil(t, handler.supervisionProgressCheckStop, "0 must keep the sweep off")
}
