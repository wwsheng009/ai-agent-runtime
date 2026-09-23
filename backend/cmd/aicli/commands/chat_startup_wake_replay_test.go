package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// newStartupReplayTestHost 在 wake consumer 夹具之上补齐启动重放需要的两件事：
// 控制面（projector 读 host.Supervision.Store/Wakes）与一个 idle 的父会话运行态
// （wake consumer 的 runnable 门要求父会话不在 Busy）。
func newStartupReplayTestHost(t *testing.T, name string, noInteractive bool) (*localChatRuntimeHost, *supervision.SQLiteSupervisionStore, *syncWaitDeliveries) {
	t.Helper()
	host, store, deliveries := newWakeConsumerTestHostWithTurnEndCheck(t, name, supervision.WakeSchedulerConfig{}, false)
	host.BaseSession.NoInteractive = noInteractive
	host.Supervision = &runtimeserver.SupervisionControlPlane{Store: store, Wakes: host.supervisionWake.Wakes}
	require.NoError(t, host.RuntimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "root-session",
		Status:    runtimechat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))
	return host, store, deliveries
}

// newStartupReplayBatchStore 装配一个空的可持久化 batch store。
func newStartupReplayBatchStore(t *testing.T, host *localChatRuntimeHost) subagentbatch.BatchStore {
	t.Helper()
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "subagent_batches_startup_replay.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	host.SubagentBatches = store
	return store
}

// seedFailedStartupReplayBatch 写入一个属于 root-session 的 failed background
// batch：失败终态会投影 critical/unresolved 行并 schedule 一个 durable wake，
// 这正是现场 resume 时被重放并当场 drain 掉的那一类行。
func seedFailedStartupReplayBatch(t *testing.T, store subagentbatch.BatchStore) {
	t.Helper()
	ctx := context.Background()
	now := subagentbatch.Now()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         "batch-startup-replay",
		RootScopeID:     "root-session",
		ParentSessionID: "root-session",
		ParentTurnID:    "turn-previous",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchFailed,
		TaskCount:       1,
		CompletedCount:  0,
		FailedCount:     1,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := store.CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{{
		TaskID:         "task-1",
		BatchID:        batch.BatchID,
		ChildSessionID: "child-task-1",
		Role:           "writer",
		Difficulty:     "easy",
		Status:         subagentbatch.TaskFailed,
		OrderIndex:     1,
		Spec:           []byte(`{"id":"task-1"}`),
		UpdatedAt:      now,
		Version:        1,
	}})
	require.NoError(t, err)
	require.True(t, created)
}

func countStartupReplayPendingWakes(t *testing.T, store *supervision.SQLiteSupervisionStore) int {
	t.Helper()
	pending, err := store.ListWakePending(context.Background(), supervision.WakeFilter{
		RootScopeID:   "root-session",
		UnclaimedOnly: true,
	})
	require.NoError(t, err)
	return len(pending)
}

// TestStartupTerminalReplayDefersWakeOnInteractiveResume 钉住 2026-09-23 的 resume
// 回归：启动期终态重放（ReplayTerminalDeliveries → projectTerminalLifecycle →
// ProjectLifecycle → wakeSupervisedParent）在交互式会话上不得把重新投影出的 wake
// 当场 drain 成一个隐藏的 auto-wake 父 turn。
//
// 现场证据：`aicli resume` 启动期先落一行 supervision_wake_delivered
// （15:46:48.819），6 秒后 session_start 带着 supervision.AutoWakePrompt
// （prompt_length=1791）直接开了一个隐藏 turn。用户看到的是没有 `>` 输入区的
// "Analyzing"（actor Busy 抑制 composer）和在 5 万 token 恢复上下文上白烧的一个
// LLM turn（"恢复非常慢"）。修复前，实时装配路径（chat_actor_host.go 的
// registerSubagentCoordinator 分支）用的是 drainWake 恒为 true 的实时投影器。
func TestStartupTerminalReplayDefersWakeOnInteractiveResume(t *testing.T) {
	t.Run("interactive resume keeps the wake durable", func(t *testing.T) {
		host, store, deliveries := newStartupReplayTestHost(t, "aicli-startup-replay-interactive", false)
		require.True(t, chatHostSessionInteractive(host.BaseSession), "夹具必须是交互式会话，否则本用例会退化成空断言")
		batchStore := newStartupReplayBatchStore(t, host)
		seedFailedStartupReplayBatch(t, batchStore)

		localSubagentBatchStartupReplay(context.Background(), host, batchStore, "root-session", 512)

		require.Equal(t, 0, deliveries.count(), "交互式启动重放不得 drain wake 成隐藏父 turn（composer 会被 Busy 抑制）")
		require.Equal(t, 1, countStartupReplayPendingWakes(t, store), "wake 必须保持 durable，等待下一次自然 turn 的 preflight 或显式投递")

		// 反证：主机是 runnable 的、wake 是可投递的 —— 上面 0 次投递来自
		// interactive 闸门，而不是"父会话忙/状态缺失"这类偶然原因。
		require.NoError(t, host.supervisionWake.MaybeWakeParent(context.Background(), "root-session", "", "root-session"))
		require.Equal(t, 1, deliveries.count(), "显式投递必须仍然可用")
	})

	t.Run("headless startup replay still delivers", func(t *testing.T) {
		host, store, deliveries := newStartupReplayTestHost(t, "aicli-startup-replay-headless", true)
		require.False(t, chatHostSessionInteractive(host.BaseSession), "夹具必须是 headless 会话")
		batchStore := newStartupReplayBatchStore(t, host)
		seedFailedStartupReplayBatch(t, batchStore)

		localSubagentBatchStartupReplay(context.Background(), host, batchStore, "root-session", 512)

		require.Equal(t, 1, deliveries.count(), "headless 启动重放必须保持立即投递（行为不变）")
		require.Equal(t, 0, countStartupReplayPendingWakes(t, store), "headless 投递后不再有未领取的 wake")
	})
}
