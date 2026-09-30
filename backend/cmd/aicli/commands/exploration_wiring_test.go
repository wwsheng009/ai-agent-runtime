package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 三入口之一（aicli：TUI 与 ACP 共用 applyLocalChatToolObservation）：
// 知识层 shadow + owner 时采集器把 view/grep 观察落探索记忆；
// 账本未启用（shadow 观察器为 nil）时采集器仍应独立接线；off 零接线。
func TestApplyLocalChatToolObservation_WritesExplorationMemory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeShadow
	act, err := knowledge.Activate(ctx, cfg, root, knowledge.ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	require.NotNil(t, act)
	t.Cleanup(func() { _ = act.Close() })

	// ledgerSvc == nil → shadow 观察器为 nil；采集器不依赖账本，必须独立接线。
	session := &ChatSession{Knowledge: act}
	host := &localChatRuntimeHost{}
	config := &agent.LoopReActConfig{}
	applyLocalChatToolObservation(config, session, host, knowledge.ObservationSourceMainSession, "")
	require.NotNil(t, config.OnToolObserved, "知识层 shadow 时采集器必须接线")

	config.OnToolObserved(ctx, "sess-cli", runtimetypes.ToolCall{
		Name: "view",
		Args: map[string]interface{}{"file_path": "backend/a.go", "offset": 1, "limit": 10},
	}, "package main\n", "")
	require.NoError(t, act.Recorder().Flush(ctx))

	store := act.Layer().Store()
	wsID, ok, err := store.FindWorkspace(ctx, root)
	require.NoError(t, err)
	require.True(t, ok)
	nodes, err := store.LookupExplorationNodes(ctx, knowledge.ExplorationNodeQuery{WorkspaceID: wsID, Limit: 50})
	require.NoError(t, err)
	require.Len(t, nodes, 2, "query 节点 + file 节点")
	var foundFile bool
	for _, node := range nodes {
		if node.NodeType == knowledge.NodeTypeFile && node.Target == "backend/a.go" {
			foundFile = true
		}
	}
	require.True(t, foundFile)

	// W6：只读子代理来源零写入——hook 仍接线（shadow 归因面不受影响），
	// 但 Recorder 侧门禁丢弃观察，不得新增节点。
	readOnlyConfig := &agent.LoopReActConfig{}
	applyLocalChatToolObservation(readOnlyConfig, session, host,
		knowledge.ObservationSourceFor(true, true), "")
	require.NotNil(t, readOnlyConfig.OnToolObserved, "只读来源仍需接线（shadow 归因面独立）")
	readOnlyConfig.OnToolObserved(ctx, "sess-cli", runtimetypes.ToolCall{
		Name: "view",
		Args: map[string]interface{}{"file_path": "backend/b.go", "offset": 1, "limit": 10},
	}, "package other\n", "")
	require.NoError(t, act.Recorder().Flush(ctx))
	afterReadOnly, err := store.LookupExplorationNodes(ctx, knowledge.ExplorationNodeQuery{WorkspaceID: wsID, Limit: 50})
	require.NoError(t, err)
	require.Len(t, afterReadOnly, 2, "只读子代理不得新增探索节点")

	// mode=off（Knowledge == nil）→ 零接线。
	offConfig := &agent.LoopReActConfig{}
	applyLocalChatToolObservation(offConfig, &ChatSession{}, host, knowledge.ObservationSourceMainSession, "")
	require.Nil(t, offConfig.OnToolObserved, "知识层 off 时不得接线")
}
