package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestLocalActorRegistryListIncludesBatchLedgerRows 锁定 2026-10-14 缺口：
// subagent_spawn_batch 的子代理只写批任务账本、不落 SessionStore/注册表，
// subagent_list（ActorRegistry.List）必须与 /agents 列表、wait 快照
// （batchTaskSnapshot）同口径合并账本行，否则批量派发场景下列表恒为空，
// 与 subagent_spawn 的列表行为不一致。
func TestLocalActorRegistryListIncludesBatchLedgerRows(t *testing.T) {
	ctx := context.Background()
	root := runtimechat.NewSession("batch-list-user")
	root.ID = "batch-list-root"
	host := &localChatRuntimeHost{
		SessionStore: runtimechat.NewInMemoryStorage(),
		SessionUser:  "batch-list-user",
	}
	host.BaseSession = &ChatSession{RuntimeSession: root}
	host.SubagentBatches = newTestSubagentBatchStore(t)
	host.ActorRegistry = newLocalActorRegistry(host)

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         "batch-list-1",
		RootScopeID:     root.ID,
		ParentSessionID: root.ID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       3,
		RunningCount:    1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{
		{
			TaskID:         "demo-explorer",
			BatchID:        batch.BatchID,
			OrderIndex:     1,
			UpdatedAt:      now,
			Version:        1,
			ChildSessionID: "subagent_demo-explorer_abc",
			Status:         subagentbatch.TaskRunning,
			TaskType:       "explore",
			TaskSubject:    "explore repo",
		},
		{
			TaskID:         "demo-builder",
			BatchID:        batch.BatchID,
			OrderIndex:     2,
			UpdatedAt:      now,
			Version:        1,
			ChildSessionID: "subagent_demo-builder_abc",
			Status:         subagentbatch.TaskSucceeded,
			TaskType:       "implement",
			TaskSubject:    "build feature",
		},
		{
			// 派发前失败的任务没有 child session：默认（不含终态）不可见，
			// include_closed=true 时必须以 task id 兜底可见（失败观测）。
			TaskID:      "demo-implementer",
			BatchID:     batch.BatchID,
			OrderIndex:  3,
			UpdatedAt:   now,
			Version:     1,
			Status:      subagentbatch.TaskFailed,
			ErrorClass:  "policy",
			ErrorCode:   "single_writer",
			TaskType:    "implement",
			TaskSubject: "implement feature",
		},
	})
	require.NoError(t, err)
	require.True(t, created)

	// 默认口径：只列运行中的账本行，终态行不出现（与注册表来源的 closed 过滤同口径）。
	list, err := host.ActorRegistry.List(ctx, root.ID, toolbroker.ListAgentsArgs{})
	require.NoError(t, err)
	require.Equal(t, 1, list.Count)
	require.Equal(t, "subagent_demo-explorer_abc", list.Agents[0].SessionID)
	require.Equal(t, string(runtimechat.SessionRunning), list.Agents[0].Status)
	require.Equal(t, "subagent", list.Agents[0].AgentType)
	require.Equal(t, root.ID, list.Agents[0].ParentSessionID)
	require.Equal(t, "demo-explorer", list.Agents[0].CurrentTaskID)
	require.Equal(t, string(subagentbatch.TaskRunning), list.Agents[0].CurrentTaskStatus)

	// include_closed=true：终态行补齐；未绑定的失败任务以 task id 兜底可见。
	list, err = host.ActorRegistry.List(ctx, root.ID, toolbroker.ListAgentsArgs{IncludeClosed: true})
	require.NoError(t, err)
	require.Equal(t, 3, list.Count)
	byID := map[string]toolbroker.AgentStatusResult{}
	for _, agent := range list.Agents {
		byID[agent.SessionID] = agent
	}
	require.Equal(t, string(runtimechat.SessionIdle), byID["subagent_demo-builder_abc"].Status)
	require.Equal(t, "demo-implementer", byID["demo-implementer"].SessionID)
	require.Equal(t, string(runtimechat.SessionStopped), byID["demo-implementer"].Status)
	require.Contains(t, byID["demo-implementer"].Error, "single_writer")

	// path_prefix 收窄到子树时账本行不参与过滤命中（无 agent 路径，不合成）。
	list, err = host.ActorRegistry.List(ctx, root.ID, toolbroker.ListAgentsArgs{PathPrefix: "/root/subagent_demo-explorer_abc"})
	require.NoError(t, err)
	require.Equal(t, 0, list.Count)
}

// TestLocalActorRegistryListPathPrefixHonorsBoundaries 钉住本地兜底路径过滤的
// 边界语义与 API 宿主（agentcontrol.AgentPathMatchesPrefix）一致：
// /root/alpha 不得匹配兄弟路径 /root/alpha2（历史 strings.HasPrefix 会误命中）。
func TestLocalActorRegistryListPathPrefixHonorsBoundaries(t *testing.T) {
	ctx := context.Background()
	root := runtimechat.NewSession("path-user")
	root.ID = "path-boundary-root"
	host := &localChatRuntimeHost{
		SessionStore: runtimechat.NewInMemoryStorage(),
		SessionUser:  "path-user",
	}
	host.BaseSession = &ChatSession{RuntimeSession: root}
	host.ActorRegistry = newLocalActorRegistry(host)

	addChild := func(id, path string) {
		child := runtimechat.NewSession("path-user")
		child.ID = id
		agentcontrol.SetContextIfChanged(child, toolbroker.AgentSessionContextParentSessionID, root.ID)
		agentcontrol.SetContextIfChanged(child, toolbroker.AgentSessionContextRootSessionID, root.ID)
		agentcontrol.SetContextIfChanged(child, toolbroker.AgentSessionContextPath, path)
		require.NoError(t, host.SessionStore.Save(ctx, child))
	}
	addChild("path-alpha", "/root/alpha")
	addChild("path-alpha2", "/root/alpha2")

	list, err := host.ActorRegistry.List(ctx, root.ID, toolbroker.ListAgentsArgs{PathPrefix: "/root/alpha"})
	require.NoError(t, err)
	require.Equal(t, 1, list.Count)
	require.Equal(t, "path-alpha", list.Agents[0].SessionID)

	list, err = host.ActorRegistry.List(ctx, root.ID, toolbroker.ListAgentsArgs{PathPrefix: "/root"})
	require.NoError(t, err)
	require.Equal(t, 2, list.Count)
}

// TestLocalActorRegistryRejectsMessagingBatchTaskTargets 锁定 2026-10-14 缺口：
// 批任务 worker 不是可对话会话——subagent_send / subagent_followup / subagent_input 必须
// 给出可行动拒绝（而非 ensureSession 为批任务伪造会话行、或 followup 起一次重复
// 运行）。子会话 id 与 task id 两种寻址都要命中。
func TestLocalActorRegistryRejectsMessagingBatchTaskTargets(t *testing.T) {
	ctx := context.Background()
	root := runtimechat.NewSession("batch-msg-user")
	root.ID = "batch-msg-root"
	host := &localChatRuntimeHost{
		SessionStore: runtimechat.NewInMemoryStorage(),
		SessionUser:  "batch-msg-user",
		SessionHub: runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
			return nil, errors.New("unexpected actor creation for a batch worker")
		}),
	}
	host.BaseSession = &ChatSession{RuntimeSession: root}
	host.SubagentBatches = newTestSubagentBatchStore(t)
	host.ActorRegistry = newLocalActorRegistry(host)

	const childSessionID = "subagent_guard-child_abc"
	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         "batch-msg-guard",
		RootScopeID:     root.ID,
		ParentSessionID: root.ID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		RunningCount:    1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{
		{
			TaskID:         "guard-task",
			BatchID:        batch.BatchID,
			OrderIndex:     1,
			UpdatedAt:      now,
			Version:        1,
			ChildSessionID: childSessionID,
			Status:         subagentbatch.TaskRunning,
		},
	})
	require.NoError(t, err)
	require.True(t, created)

	for _, target := range []string{childSessionID, "guard-task"} {
		_, sendErr := host.ActorRegistry.SendMessage(ctx, root.ID, toolbroker.AgentMessageArgs{Target: target, Message: "steer"})
		require.ErrorIs(t, sendErr, toolbroker.ErrAgentBatchTaskNotMessageable, "target %q", target)
		require.Contains(t, sendErr.Error(), "next_action=read|wait")
	}
	_, followupErr := host.ActorRegistry.FollowupTask(ctx, root.ID, toolbroker.AgentMessageArgs{Target: childSessionID, Message: "go"})
	require.ErrorIs(t, followupErr, toolbroker.ErrAgentBatchTaskNotMessageable)
	_, inputErr := host.ActorRegistry.SendInput(ctx, toolbroker.SendAgentInputArgs{ID: childSessionID, Message: "steer"})
	require.ErrorIs(t, inputErr, toolbroker.ErrAgentBatchTaskNotMessageable)

	// 拒绝必须先于任何副作用：不得为批任务伪造会话行，也不得为它建 actor。
	if _, loadErr := host.SessionStore.Load(ctx, childSessionID); !errors.Is(loadErr, runtimechat.ErrSessionNotFound) {
		t.Fatalf("batch worker must not get a fabricated session row, got load err=%v", loadErr)
	}
	if _, ok := host.SessionHub.Get(childSessionID); ok {
		t.Fatalf("batch worker must not get an actor")
	}
}
