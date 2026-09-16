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
