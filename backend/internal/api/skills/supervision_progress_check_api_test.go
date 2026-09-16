package skills

import (
	"context"
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
		Agent:        apiAgent,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
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
