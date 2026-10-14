package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	chat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// TestSessionAgentControllerListMergesBatchLedgerRows 锁定 2026-10-14 缺口：
// HTTP 宿主的 subagent_list 必须与本地宿主同口径，把 spawn_subagents 的批任务
// 账本行并入列表（运行中恒可见；终态遵循 include_closed；未绑定任务以 task id
// 兜底可见），否则批量派发场景下列表恒为空、与 spawn_agent 的列表行为不一致。
func TestSessionAgentControllerListMergesBatchLedgerRows(t *testing.T) {
	ctx := context.Background()
	const parentSessionID = "api-list-batch-parent"
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))
	handler.SetSubagentBatchStore(newAPISupervisionBatchStore(t))
	controller := &sessionAgentController{handler: handler}

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       3,
		RunningCount:    1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := handler.getSubagentBatchStore().CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{
		{
			TaskID:         "verify-wechat-media-protocol",
			BatchID:        batch.BatchID,
			OrderIndex:     1,
			UpdatedAt:      now,
			Version:        1,
			ChildSessionID: "subagent_verify-wechat-media-protocol_10eb",
			Status:         subagentbatch.TaskRunning,
			TaskType:       "verify",
			TaskSubject:    "核对媒体协议",
		},
		{
			TaskID:         "done-task",
			BatchID:        batch.BatchID,
			OrderIndex:     2,
			UpdatedAt:      now,
			Version:        1,
			ChildSessionID: "subagent_done-task_cafe",
			Status:         subagentbatch.TaskSucceeded,
			TaskType:       "explore",
			TaskSubject:    "done",
		},
		{
			// 派发前失败的任务没有 child session：默认（不含终态）不可见，
			// include_closed=true 时必须以 task id 兜底可见（失败观测）。
			TaskID:     "pre-dispatch-failed",
			BatchID:    batch.BatchID,
			OrderIndex: 3,
			UpdatedAt:  now,
			Version:    1,
			Status:     subagentbatch.TaskFailed,
			ErrorClass: "policy",
			ErrorCode:  "single_writer",
			TaskType:   "implement",
		},
	})
	require.NoError(t, err)
	require.True(t, created)

	// 默认口径：只列运行中的账本行，终态行不出现（与注册表来源的 closed 过滤同口径）。
	list, err := controller.List(ctx, parentSessionID, toolbroker.ListAgentsArgs{})
	require.NoError(t, err)
	require.Equal(t, 1, list.Count)
	require.Equal(t, "subagent_verify-wechat-media-protocol_10eb", list.Agents[0].SessionID)
	require.Equal(t, string(chat.SessionRunning), list.Agents[0].Status)
	require.Equal(t, "subagent", list.Agents[0].AgentType)
	require.Equal(t, parentSessionID, list.Agents[0].ParentSessionID)
	require.Equal(t, "verify-wechat-media-protocol", list.Agents[0].CurrentTaskID)
	require.Equal(t, string(subagentbatch.TaskRunning), list.Agents[0].CurrentTaskStatus)

	// include_closed=true：终态行补齐；未绑定的失败任务以 task id 兜底可见。
	list, err = controller.List(ctx, parentSessionID, toolbroker.ListAgentsArgs{IncludeClosed: true})
	require.NoError(t, err)
	require.Equal(t, 3, list.Count)
	byID := map[string]toolbroker.AgentStatusResult{}
	for _, agent := range list.Agents {
		byID[agent.SessionID] = agent
	}
	require.Equal(t, string(chat.SessionIdle), byID["subagent_done-task_cafe"].Status)
	require.Equal(t, "pre-dispatch-failed", byID["pre-dispatch-failed"].SessionID)
	require.Equal(t, string(chat.SessionStopped), byID["pre-dispatch-failed"].Status)
	require.Contains(t, byID["pre-dispatch-failed"].Error, "single_writer")

	// path_prefix 收窄到子树时账本行不参与过滤命中（无 agent 路径，不合成）。
	list, err = controller.List(ctx, parentSessionID, toolbroker.ListAgentsArgs{PathPrefix: "/root/subagent_verify-wechat-media-protocol_10eb"})
	require.NoError(t, err)
	require.Equal(t, 0, list.Count)
}

// TestSessionAgentControllerRejectsMessagingBatchTaskTargets 锁定 2026-10-14
// 缺口：批任务 worker 不是可对话会话——send_message / followup_task / send_input
// 必须给出可行动拒绝（而非为 followup/input 起一次重复运行、或为它建会话行）。
func TestSessionAgentControllerRejectsMessagingBatchTaskTargets(t *testing.T) {
	ctx := context.Background()
	const parentSessionID = "api-msg-batch-parent"
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)
	handler.SetSubagentBatchStore(newAPISupervisionBatchStore(t))
	controller := &sessionAgentController{handler: handler}

	const childSessionID = "subagent_guard-child_abc"
	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		RunningCount:    1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := handler.getSubagentBatchStore().CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{
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

	msgCtx := toolctx.WithSessionID(ctx, parentSessionID)
	_, sendErr := controller.SendMessage(msgCtx, parentSessionID, toolbroker.AgentMessageArgs{Target: childSessionID, Message: "steer"})
	require.ErrorIs(t, sendErr, toolbroker.ErrAgentBatchTaskNotMessageable)
	require.Contains(t, sendErr.Error(), "next_action=read|wait")
	_, followupErr := controller.FollowupTask(msgCtx, parentSessionID, toolbroker.AgentMessageArgs{Target: childSessionID, Message: "go"})
	require.ErrorIs(t, followupErr, toolbroker.ErrAgentBatchTaskNotMessageable)
	_, inputErr := controller.SendInput(msgCtx, toolbroker.SendAgentInputArgs{ID: childSessionID, Message: "steer"})
	require.ErrorIs(t, inputErr, toolbroker.ErrAgentBatchTaskNotMessageable)

	// 拒绝先行：不得为批任务子会话建出会话行。
	_, loadErr := sessionManager.Get(ctx, childSessionID)
	require.ErrorIs(t, loadErr, chat.ErrSessionNotFound)
}
