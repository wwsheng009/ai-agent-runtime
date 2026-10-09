package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestChatAgentItemsIncludeBatchLedgerAgents 锁定 2026-10-09 现场缺口：
// spawn_subagents 派发的子代理只写批任务账本（不落 agent_control_agents），
// /agents 列表与 picker 必须合并账本来源，否则批量派发场景下列表恒为空
// （现场：3 个批任务代理在跑，列表显示"暂无子 agent"）。
func TestChatAgentItemsIncludeBatchLedgerAgents(t *testing.T) {
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
	session := &ChatSession{
		RuntimeSession:   root,
		SessionUserID:    "batch-list-user",
		LocalRuntimeHost: host,
	}

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         "batch-live-1",
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
			// 派发前失败的任务没有 child session：仍必须出现在列表里（失败观测）。
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

	graph, err := chatAgentGraphItems(session)
	require.NoError(t, err)
	require.Len(t, graph, 3, "列表必须包含全部批任务行（含终态）: %#v", graph)
	graphIDs := make(map[string]toolbroker.AgentStatusResult, len(graph))
	for _, item := range graph {
		graphIDs[firstNonEmptyChatValue(item.SessionID, item.ID)] = item
	}
	require.Contains(t, graphIDs, "subagent_demo-explorer_abc")
	require.Contains(t, graphIDs, "subagent_demo-builder_abc")
	require.Contains(t, graphIDs, "demo-implementer", "未绑定子会话的任务按 task id 可见")
	require.Equal(t, string(runtimechat.SessionRunning), graphIDs["subagent_demo-explorer_abc"].Status)
	require.Equal(t, string(runtimechat.SessionIdle), graphIDs["subagent_demo-builder_abc"].Status)
	require.Equal(t, string(runtimechat.SessionStopped), graphIDs["demo-implementer"].Status)
	require.Equal(t, "demo-explorer", graphIDs["subagent_demo-explorer_abc"].CurrentTaskID)
	require.Equal(t, root.ID, graphIDs["subagent_demo-explorer_abc"].ParentSessionID)

	picker, err := chatAgentPickerItems(session)
	require.NoError(t, err)
	require.Len(t, picker, 1, "picker 只含非终态批任务（与 closed 过滤同口径）: %#v", picker)
	require.Equal(t, "subagent_demo-explorer_abc", firstNonEmptyChatValue(picker[0].SessionID, picker[0].ID))

	// /agents 副屏列表必须出现批任务行，而不是"暂无子 agent"占位行。
	spec := chatScreenAgentsListSpec(session)
	require.Len(t, spec.Rows, 3)
	titles := make([]string, 0, len(spec.Rows))
	for _, row := range spec.Rows {
		titles = append(titles, row.Title)
		require.False(t, strings.Contains(row.Title, "暂无子 agent"), "不应再显示空列表占位行: %#v", spec.Rows)
	}
	require.Contains(t, titles, "subagent_demo-explorer_abc")
	require.Contains(t, titles, "demo-implementer")
}

// TestResolveChatAgentTargetFallsBackToBatchLedger 锁定：终态批任务不在 picker
// （includeClosed=false），但从列表（includeClosed=true）选中的历史行仍是合法
// target，必须能解析出子会话 id（否则点击行 → "unknown agent target"）。
func TestResolveChatAgentTargetFallsBackToBatchLedger(t *testing.T) {
	ctx := context.Background()
	root := runtimechat.NewSession("batch-target-user")
	root.ID = "batch-target-root"
	host := &localChatRuntimeHost{
		SessionStore: runtimechat.NewInMemoryStorage(),
		SessionUser:  "batch-target-user",
	}
	host.BaseSession = &ChatSession{RuntimeSession: root}
	host.SubagentBatches = newTestSubagentBatchStore(t)
	host.ActorRegistry = newLocalActorRegistry(host)
	session := &ChatSession{
		RuntimeSession:   root,
		SessionUserID:    "batch-target-user",
		LocalRuntimeHost: host,
	}

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         "batch-target-1",
		RootScopeID:     root.ID,
		ParentSessionID: root.ID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchCompleted,
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
			Status:         subagentbatch.TaskSucceeded,
		},
	})
	require.NoError(t, err)
	require.True(t, created)

	// 终态任务不在 picker 来源里。
	picker, err := chatAgentPickerItems(session)
	require.NoError(t, err)
	require.Empty(t, picker)

	// task id → 子会话 id。
	resolved, err := resolveChatAgentTarget(session, "demo-explorer")
	require.NoError(t, err)
	require.Equal(t, "subagent_demo-explorer_abc", firstNonEmptyChatValue(resolved.SessionID, resolved.ID))

	// 子会话 id 原样解析。
	resolved, err = resolveChatAgentTarget(session, "subagent_demo-explorer_abc")
	require.NoError(t, err)
	require.Equal(t, "subagent_demo-explorer_abc", firstNonEmptyChatValue(resolved.SessionID, resolved.ID))

	// 未知 target 仍是错误（不引入静默成功）。
	_, err = resolveChatAgentTarget(session, "no-such-agent")
	require.ErrorContains(t, err, "unknown agent target")
}

// TestChatAgentMergeItemsDedupesByChildSession 锁定合并去重：同一子会话同时
// 出现在注册表与批任务账本时只保留一行（注册表来源优先）。
func TestChatAgentMergeItemsDedupesByChildSession(t *testing.T) {
	primary := []toolbroker.AgentStatusResult{
		{ID: "child-1", SessionID: "child-1", Path: "/root/child-1", Status: "active"},
	}
	extra := []toolbroker.AgentStatusResult{
		{ID: "child-1", SessionID: "child-1", Status: "running"},
		{ID: "task-2", SessionID: "child-2", Status: "running"},
	}
	merged := chatAgentMergeItems(primary, extra)
	require.Len(t, merged, 2)
	require.Equal(t, "/root/child-1", merged[0].Path, "注册表来源优先保留")
	require.Equal(t, "child-2", firstNonEmptyChatValue(merged[1].SessionID, merged[1].ID))

	// extra 为空时保持原样（含 nil），不改变既有空列表语义。
	require.Nil(t, chatAgentMergeItems(nil, nil))
}
