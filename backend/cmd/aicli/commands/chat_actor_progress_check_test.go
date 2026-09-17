package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// localProgressCheckTestSession 是巡查夹具用的父会话 id：与 wake consumer、
// preflight digest 使用同一个 session id（scope 是会话本身）。
const localProgressCheckTestSession = "parent-session"

// newTestSubagentBatchStore 返回一个真实的内存 batch store：巡查的判定完全
// 建立 durable 投影之上，用假 store 会让测试绕过真正的 ListBatches/ListTasks。
func newTestSubagentBatchStore(t *testing.T) subagentbatch.BatchStore {
	t.Helper()
	store, err := subagentbatch.NewSQLiteBatchStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// newLocalProgressCheckHost 组装 P2-D 的最小宿主：durable supervision store +
// 真实 batch store + 空闲父会话状态 + 计数 wake consumer。
//
// 只有「父会话空闲 + 有 active batch + 无待投递 wake + 预算未耗尽」四条同时
// 成立时才允许注入汇报 turn，这正是本文件要钉住的契约。
func newLocalProgressCheckHost(t *testing.T, batchStatus subagentbatch.BatchStatus) (*localChatRuntimeHost, *int) {
	t.Helper()
	ctx := context.Background()
	host := newLocalSupervisionTestHost(t)
	host.SubagentBatches = newTestSubagentBatchStore(t)

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     localProgressCheckTestSession,
		ParentSessionID: localProgressCheckTestSession,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          batchStatus,
		TaskCount:       1,
		RunningCount:    1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	task := subagentbatch.SubagentTaskRecord{
		TaskID:         "task-1",
		ChildSessionID: "child-1",
		Status:         subagentbatch.TaskRunning,
		OrderIndex:     1,
		UpdatedAt:      now,
		Version:        1,
	}
	if batchStatus.Terminal() {
		finished := now
		batch.FinishedAt = &finished
		batch.RunningCount = 0
		batch.CompletedCount = 1
		task.Status = subagentbatch.TaskSucceeded
	}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{task})
	require.NoError(t, err)
	require.True(t, created, "batch fixture must be created")

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: localProgressCheckTestSession,
		Status:    runtimechat.SessionIdle,
	}))
	host.RuntimeStore = runtimeStore
	host.BaseSession = &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: localProgressCheckTestSession},
	}
	host.supervisionConfig = supervision.Config{ProgressCheckInterval: time.Minute}

	deliveries := 0
	host.supervisionWake = &supervision.WakeConsumer{
		Wakes:    host.Supervision.Wakes,
		Runnable: func(context.Context, string, string, string) bool { return true },
		Deliver: func(context.Context, string, string, *supervision.Digest, []string) error {
			deliveries++
			return nil
		},
	}
	return host, &deliveries
}

func requireNoPendingWake(t *testing.T, host *localChatRuntimeHost, message string) {
	t.Helper()
	pending, err := host.Supervision.Store.ListWakePending(context.Background(), supervision.WakeFilter{
		RootScopeID:           localProgressCheckTestSession,
		TargetParentSessionID: localProgressCheckTestSession,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending, message)
}

func TestLocalSupervisionProgressCheckInjectsReportTurn(t *testing.T) {
	host, deliveries := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)

	reported, err := host.runLocalSupervisionProgressCheckOnce(context.Background())
	require.NoError(t, err)
	require.True(t, reported, "an active batch with an idle parent must produce exactly one report turn")
	require.Equal(t, 1, *deliveries)
	requireNoPendingWake(t, host, "the injected turn claims the progress wake")
}

func TestLocalSupervisionProgressCheckSkipsWithoutActiveBatch(t *testing.T) {
	host, deliveries := newLocalProgressCheckHost(t, subagentbatch.BatchCompleted)

	reported, err := host.runLocalSupervisionProgressCheckOnce(context.Background())
	require.NoError(t, err)
	require.False(t, reported, "a terminal batch has no new progress to report")
	require.Zero(t, *deliveries)
	requireNoPendingWake(t, host, "no active batch must not even schedule a wake row")
}

func TestLocalSupervisionProgressCheckSkipsBusyParent(t *testing.T) {
	host, deliveries := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
	ctx := context.Background()
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: localProgressCheckTestSession,
		Status:    runtimechat.SessionRunning,
	}))

	reported, err := host.runLocalSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.False(t, reported, "a busy parent owns the turn; the sweep must never interleave")
	require.Zero(t, *deliveries)
	requireNoPendingWake(t, host, "a skipped sweep leaves no stale wake behind")
}

func TestLocalSupervisionProgressCheckDefersToPendingWake(t *testing.T) {
	host, deliveries := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
	ctx := context.Background()
	_, err := host.Supervision.Wakes.ScheduleWake(ctx, supervision.WakeRequest{
		RootScopeID:           localProgressCheckTestSession,
		TargetParentSessionID: localProgressCheckTestSession,
		WakeReason:            "critical_lifecycle",
	})
	require.NoError(t, err)

	reported, err := host.runLocalSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.False(t, reported, "an already-pending lifecycle wake owns the next turn")
	require.Zero(t, *deliveries)

	pending, err := host.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           localProgressCheckTestSession,
		TargetParentSessionID: localProgressCheckTestSession,
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "the sweep must not stack a second wake on top of a pending one")
	require.Equal(t, "critical_lifecycle", pending[0].WakeReason)
}

// TestLocalSupervisionProgressCheckLifecycleGatedByConfig 钉住 opt-in 开关的
// 两端：默认（interval=0）不注册 ticker；显式开启后循环挂在 lifecycleCtx 上，
// stop 之后 Close() 等待的 WaitGroup 能正常收敛（无残留巡检 goroutine）。
func TestLocalSupervisionProgressCheckLifecycleGatedByConfig(t *testing.T) {
	t.Run("default config registers no ticker", func(t *testing.T) {
		host := newLocalSupervisionTestHost(t)
		host.SubagentBatches = newTestSubagentBatchStore(t)
		require.False(t, host.supervisionConfig.ProgressCheckEnabled(), "the sweep is opt-in, never implicit")

		host.startLocalSupervisionProgressCheck()
		require.Nil(t, host.progressCheckStop, "a disabled sweep must not start a background loop")
	})

	t.Run("enabled config stops with the host lifecycle", func(t *testing.T) {
		host, _ := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		host.lifecycleCtx = ctx

		host.startLocalSupervisionProgressCheck()
		require.NotNil(t, host.progressCheckStop, "an opt-in interval must register the sweep")
		require.True(t, host.supervisionConfig.ProgressCheckEnabled())

		host.stopLocalSupervisionProgressCheck()
		done := make(chan struct{})
		go func() {
			host.asyncWG.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the progress sweep goroutine did not exit after stop")
		}
	})
}

// localProgressCheckCriticalNotification 落一条未决 critical 通知（不产生
// wake），用于钉住 P0-2 改动 2 的让位门：scope 内仍有 critical 未决时，巡查
// 不得抢跑汇报 turn；该通知被 resolve 后巡查必须自动恢复。
func localProgressCheckCriticalNotification(t *testing.T, host *localChatRuntimeHost) supervision.Notification {
	t.Helper()
	notification, err := host.Supervision.Store.UpsertNotification(context.Background(), supervision.Notification{
		RootScopeID:      localProgressCheckTestSession,
		SubjectKind:      supervision.SubjectAgentRun,
		SubjectID:        "child-critical-1",
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

// TestLocalSupervisionProgressCheckDefersToUnresolvedCritical 钉住 P0-2 改动 2：
// 存在未决 critical 时本轮不调度、不起 turn，且不留 stale wake；critical 被
// resolve 后同一巡查逻辑立即恢复注入。
func TestLocalSupervisionProgressCheckDefersToUnresolvedCritical(t *testing.T) {
	host, deliveries := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
	ctx := context.Background()
	notification := localProgressCheckCriticalNotification(t, host)

	reported, err := host.runLocalSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.False(t, reported, "an unresolved critical notification owns the next turn")
	require.Zero(t, *deliveries, "the gated sweep must not start a progress turn")
	requireNoPendingWake(t, host, "the gated sweep must not leave a stale progress wake behind")

	ok, err := host.Supervision.Store.ResolveNotification(ctx, notification.NotificationID,
		supervision.ResolutionRecovered, time.Now().UTC(), notification.Version)
	require.NoError(t, err)
	require.True(t, ok, "the fixture critical notification must be resolvable")

	reported, err = host.runLocalSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.True(t, reported, "the sweep resumes once the critical notification is resolved")
	require.Equal(t, 1, *deliveries)
}

// TestLocalSupervisionProgressCheckSpendsOnlyProgressBudget 钉住 P0-2/ADR-2 的
// CLI 侧分账：汇报 turn 记一条 progress claim，绝不占用 failure/other 的有界
// 额度；连续巡查只受自己的 6 次/窗口约束，用尽后跳过且不留 stale wake。
func TestLocalSupervisionProgressCheckSpendsOnlyProgressBudget(t *testing.T) {
	host, deliveries := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
	ctx := context.Background()

	reported, err := host.runLocalSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.True(t, reported)
	require.Equal(t, 1, *deliveries)

	progress := host.Supervision.Wakes.BudgetState(ctx, localProgressCheckTestSession, supervision.WakeBudgetClassProgress)
	require.Equal(t, 1, progress.Used, "the report turn books one progress claim")
	require.Equal(t, 6, progress.Limit)
	require.Zero(t, host.Supervision.Wakes.BudgetState(ctx, localProgressCheckTestSession, supervision.WakeBudgetClassOther).Used)
	require.Zero(t, host.Supervision.Wakes.BudgetState(ctx, localProgressCheckTestSession, supervision.WakeBudgetClassFailure).Used)
	now := time.Now().UTC()
	require.True(t, host.Supervision.Wakes.AllowAutoWake(ctx, localProgressCheckTestSession, supervision.WakeBudgetClassOther, now),
		"a progress turn must not consume the other budget")
	require.True(t, host.Supervision.Wakes.AllowAutoWake(ctx, localProgressCheckTestSession, supervision.WakeBudgetClassFailure, now),
		"a progress turn must not consume the failure budget")

	// 继续巡查直到 progress 的 6 次窗口额度用尽：第 7 次跳过（不投递、不落
	// stale wake），而 failure/other 的额度始终未受影响。
	for i := 0; i < 5; i++ {
		reported, err = host.runLocalSupervisionProgressCheckOnce(ctx)
		require.NoError(t, err)
		require.Truef(t, reported, "sweep %d must still deliver within the progress allowance", i+2)
	}
	require.Equal(t, 6, *deliveries)

	reported, err = host.runLocalSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.False(t, reported, "the progress allowance (6 per window) must rate-limit the sweep")
	requireNoPendingWake(t, host, "a rate-limited sweep skips instead of leaving a stale wake")
	require.True(t, host.Supervision.Wakes.AllowAutoWake(ctx, localProgressCheckTestSession, supervision.WakeBudgetClassFailure, now))
	require.True(t, host.Supervision.Wakes.AllowAutoWake(ctx, localProgressCheckTestSession, supervision.WakeBudgetClassOther, now))
}

// TestLocalSupervisionProgressCheckSurvivesExhaustedCriticalBudget 是分账的
// 反方向：共享有界额度（这里收紧到 1/窗口）已整份花光、花光它的 critical
// 通知也已 resolve 之后，progress 巡查仍有自己的额度并正常注入。
func TestLocalSupervisionProgressCheckSurvivesExhaustedCriticalBudget(t *testing.T) {
	host, deliveries := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
	ctx := context.Background()

	tight := supervision.NewWakeScheduler(host.Supervision.Store, supervision.WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
	})
	host.Supervision.Wakes = tight
	host.supervisionWake = &supervision.WakeConsumer{
		Wakes:    tight,
		Runnable: func(context.Context, string, string, string) bool { return true },
		Deliver: func(context.Context, string, string, *supervision.Digest, []string) error {
			*deliveries = *deliveries + 1
			return nil
		},
	}

	// 一条 other 类 critical lifecycle wake 花光唯一的共享额度（memory 账本）。
	projected, err := supervision.ProjectLifecycle(ctx, host.Supervision.Store, tight, supervision.LifecycleProjection{
		RootScopeID:           localProgressCheckTestSession,
		TargetParentSessionID: localProgressCheckTestSession,
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "critical-budget-1",
		SubjectVersion:        1,
		EventType:             "critical_lifecycle",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
	})
	require.NoError(t, err)
	require.NoError(t, host.wakeSupervisedParent(ctx, localProgressCheckTestSession, localProgressCheckTestSession))
	require.False(t, tight.AllowAutoWake(ctx, localProgressCheckTestSession, supervision.WakeBudgetClassOther, time.Now().UTC()),
		"the fixture must exhaust the shared bounded budget")

	// critical 消除（让位门放行）后，progress 类不应因共享额度耗尽被挡。
	// 投递会 MarkNotificationDelivered 抬升 version，resolve 前必须重读拿最新版本。
	require.NotEmpty(t, projected.NotificationID)
	notifications, err := host.Supervision.Store.ListNotifications(ctx, supervision.NotificationFilter{
		RootScopeID:     localProgressCheckTestSession,
		IncludeResolved: false,
	})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	ok, err := host.Supervision.Store.ResolveNotification(ctx, notifications[0].NotificationID,
		supervision.ResolutionRecovered, time.Now().UTC(), notifications[0].Version)
	require.NoError(t, err)
	require.True(t, ok, "the delivered lifecycle notification must still be resolvable via its current version")

	reported, err := host.runLocalSupervisionProgressCheckOnce(ctx)
	require.NoError(t, err)
	require.True(t, reported, "an exhausted critical budget must not block the progress check")
	require.Equal(t, 2, *deliveries)

	other := tight.BudgetState(ctx, localProgressCheckTestSession, supervision.WakeBudgetClassOther)
	require.Equal(t, 1, other.Used)
	require.Equal(t, 1, other.Limit, "the progress turn must not add an other-class claim")
	progress := tight.BudgetState(ctx, localProgressCheckTestSession, supervision.WakeBudgetClassProgress)
	require.Equal(t, 1, progress.Used)
	require.Equal(t, 6, progress.Limit)
}

// TestLocalSupervisionProgressCheckClampsSubFloorInterval 钉住 P0-2 改动 3 的
// 钳制告警判定与生效间隔：1s→30s（warning=true），0/负数保持"关闭"且不告警，
// 正常值不变且不告警；钳制点仍然只有 supervision.Config.WithDefaults 一处。
func TestLocalSupervisionProgressCheckClampsSubFloorInterval(t *testing.T) {
	cases := []struct {
		name      string
		raw       time.Duration
		effective time.Duration
		wantWarn  bool
	}{
		{"sub-floor is clamped and warned", time.Second, supervision.MinProgressCheckInterval, true},
		{"exactly the floor is untouched", supervision.MinProgressCheckInterval, supervision.MinProgressCheckInterval, false},
		{"a normal value is untouched", 90 * time.Second, 90 * time.Second, false},
		{"zero keeps the opt-in off without a warning", 0, 0, false},
		{"negative keeps the opt-in off without a warning", -time.Second, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.wantWarn, warnProgressCheckIntervalClamped(tc.raw, tc.effective))
			require.Equal(t, tc.effective,
				supervision.Config{ProgressCheckInterval: tc.raw}.WithDefaults().ProgressCheckInterval,
				"WithDefaults 仍是唯一的钳制点")
		})
	}

	// 配了 1s 的宿主仍然开启巡查（只是生效间隔被抬到 30s），0 配置不注册 ticker。
	host, _ := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
	host.supervisionConfig = supervision.Config{ProgressCheckInterval: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	host.lifecycleCtx = ctx
	host.startLocalSupervisionProgressCheck()
	require.NotNil(t, host.progressCheckStop, "a sub-floor opt-in still starts the sweep at the clamped interval")
	host.stopLocalSupervisionProgressCheck()

	off, _ := newLocalProgressCheckHost(t, subagentbatch.BatchRunning)
	off.supervisionConfig = supervision.Config{}
	require.False(t, off.supervisionConfig.ProgressCheckEnabled())
	off.startLocalSupervisionProgressCheck()
	require.Nil(t, off.progressCheckStop, "0 must keep the sweep off")
}
