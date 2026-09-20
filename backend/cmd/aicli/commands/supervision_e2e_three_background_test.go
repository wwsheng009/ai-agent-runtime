package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// §8.5 验收场景的宿主级、可重复版本。
//
// 手工场景是「spawn 3 个 background 子会话（快 / 慢 / 超时）→ 父 turn 调一次
// supervision_descendants，观察矩阵；再观察 preflight progress 行（2/3 …）」。
// 本文件把它固定下来：数据源全部是生产实现（durable agent registry 是 CLI
// descendant 投影的真实来源、durable supervision store、durable batch store），
// 只有「模型发起 spawn」这一步被替换为直接写 registry —— spawn 本身有独立测试，
// §8.5 要验证的是父 agent 最终看得见什么。
//
// 与真实交互式运行唯一无法覆盖的是真实模型对工具的选择；provider 投影 → 状态
// 矩阵 → 通知合并 → 渲染 → preflight 注入全部是生产代码路径。

const supervisionE2EParent = "parent-session"

// newSupervisionE2EHost 装配 CLI 宿主的真实数据面。
func newSupervisionE2EHost(t *testing.T) (*localChatRuntimeHost, *agentcontrol.SQLiteGlobalAgentRegistryStore, supervision.ExecutionRunStore) {
	t.Helper()
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agents.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = agentStore.Close() })

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })

	plane, err := runtimeserver.BuildSupervisionControlPlane(t.TempDir(), supervision.Config{}, runtimeserver.SupervisionRuntimeHooks{
		AgentRegistry: agentStore,
		TeamStore:     teamStore,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = plane.Close() })

	runStore, ok := plane.Store.(supervision.ExecutionRunStore)
	require.True(t, ok, "the durable control plane must expose execution runs")

	host := &localChatRuntimeHost{
		Supervision:       plane,
		supervisionConfig: supervision.DefaultConfig(),
		TeamStore:         teamStore,
		SubagentBatches:   newTestSubagentBatchStore(t),
	}
	return host, agentStore, runStore
}

func seedSupervisionE2EChild(t *testing.T, store agentcontrol.AgentRegistryStore, id, status string, now time.Time) {
	t.Helper()
	_, err := store.UpsertAgentControlAgent(context.Background(), agentcontrol.AgentRecord{
		AgentID:         id,
		RootSessionID:   supervisionE2EParent,
		ParentSessionID: supervisionE2EParent,
		SessionID:       id,
		AgentPath:       "/root/" + id,
		Depth:           1,
		AgentType:       agentcontrol.AgentTypeChild,
		Status:          status,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	require.NoError(t, err)
}

// TestSupervisionE2E_ThreeBackgroundChildrenOneCallAndProgressLine 是 §8.5 的
// 主场景：一次 supervision_descendants 覆盖全部 3 个子会话（快=已收敛、慢=运行中、
// 超时=timed_out + action_required），同一 turn 的 preflight 带着 2/3 的 progress 行。
func TestSupervisionE2E_ThreeBackgroundChildrenOneCallAndProgressLine(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	host, agentStore, runStore := newSupervisionE2EHost(t)
	// 真实 CLI 宿主在 actor registry 就绪后会补装 action executor
	// （chat_actor_host.go:620）。没有它，capability 过滤会（正确地）不宣告
	// cancel/close —— P2-12 方案 1；这里装上记录型执行器以观察完整矩阵。
	host.Supervision.SetActionExecutor(&recordingSupervisionExecutor{})

	// 3 个 background 子会话的 durable 身份：快（已关闭）、慢（仍在跑）、超时。
	seedSupervisionE2EChild(t, agentStore, "worker-fast", agentcontrol.AgentStatusClosed, now)
	seedSupervisionE2EChild(t, agentStore, "worker-slow", agentcontrol.AgentStatusActive, now)
	seedSupervisionE2EChild(t, agentStore, "worker-timeout", agentcontrol.AgentStatusActive, now)

	// 超时子会话的 run 被 watchdog 终结（与生产 projection 相同的落点），
	// 并因此产生一条 critical lifecycle 通知。
	_, err := runStore.CreateExecutionRun(ctx, supervision.ExecutionRun{
		RunID:     "run-worker-timeout",
		Kind:      supervision.RunKindAgentRun,
		Workflow:  supervision.RunWorkflowSpawnAgent,
		SessionID: "worker-timeout",
		AgentID:   "worker-timeout",
		Status:    supervision.RunStatusRunning,
		OwnerID:   supervisionE2EParent,
		StartedAt: now,
	})
	require.NoError(t, err)
	finalized, err := runStore.MarkExecutionRunTerminal(ctx, "run-worker-timeout", supervision.RunStatusTimedOut, "progress_stalled", "", now)
	require.NoError(t, err)
	require.True(t, finalized)

	_, err = supervision.ProjectLifecycle(ctx, host.Supervision.Store, host.Supervision.Wakes, supervision.LifecycleProjection{
		RootScopeID:           supervisionE2EParent,
		TargetParentSessionID: supervisionE2EParent,
		SubjectKind:           supervision.SubjectAgentSession,
		SubjectID:             "worker-timeout",
		SubjectVersion:        1,
		EventType:             "execution_run.timed_out",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionTimedOut,
		Reason:                "execution deadline exceeded: no progress for 120s",
	})
	require.NoError(t, err)

	// batch 控制面：3 个任务，1 完成 2 运行 → progress 行 "1/3 completed, 2 running"。
	// 计数以 task 行为单一事实源（batch 计数列只在建批与终态收敛时刷新），
	// 因此这里的存储列必须与 task 行一致，否则快照自相矛盾。
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     supervisionE2EParent,
		ParentSessionID: supervisionE2EParent,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       3,
		RunningCount:    2,
		CompletedCount:  1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	tasks := []subagentbatch.SubagentTaskRecord{
		{TaskID: "task-fast", ChildSessionID: "worker-fast", Status: subagentbatch.TaskSucceeded, OrderIndex: 1, UpdatedAt: now, Version: 1},
		{TaskID: "task-slow", ChildSessionID: "worker-slow", Status: subagentbatch.TaskRunning, OrderIndex: 2, UpdatedAt: now, Version: 1},
		{TaskID: "task-timeout", ChildSessionID: "worker-timeout", Status: subagentbatch.TaskRunning, OrderIndex: 3, UpdatedAt: now, Version: 1},
	}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, tasks)
	require.NoError(t, err)
	require.True(t, created, "the e2e batch fixture must be created")

	// 1) 父 turn 的一次巡查调用：整批可见。
	session := newChatDebugSupervisionSession(host, supervisionE2EParent)
	controller := newLocalSupervisionToolController(host, session)
	require.NotNil(t, controller)

	snapshot, err := controller.SupervisionDescendants(ctx, supervisionE2EParent, toolbroker.SupervisionDescendantsArgs{})
	require.NoError(t, err)
	require.False(t, snapshot.Truncated)

	byID := make(map[string]supervision.SnapshotItem, len(snapshot.Descendants))
	for _, item := range snapshot.Descendants {
		byID[item.ID] = item
	}
	require.Len(t, snapshot.Descendants, 3, "one call must list all three background children")
	require.Contains(t, byID, "worker-fast")
	require.Contains(t, byID, "worker-slow")
	require.Contains(t, byID, "worker-timeout")

	require.Equal(t, supervision.SupervisionTerminated, byID["worker-fast"].SupervisionState)
	require.Equal(t, supervision.SupervisionRunning, byID["worker-slow"].SupervisionState)
	require.Equal(t, supervision.SupervisionTimedOut, byID["worker-timeout"].SupervisionState)
	require.Equal(t, "cancel", byID["worker-timeout"].RecommendedAction)
	require.True(t, byID["worker-timeout"].ActionRequired)
	require.Contains(t, byID["worker-timeout"].AllowedActions, string(supervision.ActionCancel))
	require.Contains(t, byID["worker-timeout"].AllowedActions, string(supervision.ActionClose))
	require.NotEmpty(t, byID["worker-timeout"].NotificationID)

	require.Equal(t, 1, snapshot.Summary.Running)
	require.Equal(t, 1, snapshot.Summary.TimedOut)
	require.Equal(t, 1, snapshot.Summary.ActionRequired)

	// 2) 同一父 turn 的 preflight：progress 行与 lifecycle 行一起注入。
	prompt, err := injectLocalSupervisionPreflight(ctx, host, supervisionE2EParent, "USER PROMPT", nil)
	require.NoError(t, err)
	require.Contains(t, prompt, "progress:")
	require.Contains(t, prompt, "1/3 completed, 2 running")
	require.Contains(t, prompt, "worker-timeout")
	require.Contains(t, prompt, "USER PROMPT")
}

// TestSupervisionE2E_ProgressOnlyTurnIsStillInjected 钉住 P0-B 放宽的空 digest
// 门：没有任何 lifecycle 行、只有 progress 区块的 turn 过去会被
// len(digest.Items)==0 直接吞掉 —— 正常进度正是"看起来什么都没发生"的那一半。
func TestSupervisionE2E_ProgressOnlyTurnIsStillInjected(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	// 独立宿主：控制面里没有任何通知行。
	host, _, _ := newSupervisionE2EHost(t)

	finished := now
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     supervisionE2EParent,
		ParentSessionID: supervisionE2EParent,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchCompleted,
		TaskCount:       3,
		CompletedCount:  3,
		FinishedAt:      &finished,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	tasks := []subagentbatch.SubagentTaskRecord{
		{TaskID: "task-1", ChildSessionID: "child-1", Status: subagentbatch.TaskSucceeded, OrderIndex: 1, UpdatedAt: now, Version: 1},
		{TaskID: "task-2", ChildSessionID: "child-2", Status: subagentbatch.TaskSucceeded, OrderIndex: 2, UpdatedAt: now, Version: 1},
		{TaskID: "task-3", ChildSessionID: "child-3", Status: subagentbatch.TaskSucceeded, OrderIndex: 3, UpdatedAt: now, Version: 1},
	}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, tasks)
	require.NoError(t, err)
	require.True(t, created)

	prompt, err := injectLocalSupervisionPreflight(ctx, host, supervisionE2EParent, "USER PROMPT", nil)
	require.NoError(t, err)
	require.Contains(t, prompt, "progress:")
	require.Contains(t, prompt, "3/3 completed")
	require.Contains(t, prompt, "USER PROMPT")
}
