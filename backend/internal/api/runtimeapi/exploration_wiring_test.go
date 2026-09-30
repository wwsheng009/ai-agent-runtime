package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 三入口之一（runtime-server API）：off / 未接线时 hook 必须保持 nil（零写入）；
// 注入采集器后 grep 观察经真实 store 落探索记忆（端到端断言）。
func TestApplyAPISessionToolObservation_RecorderOffKeepsHookNil(t *testing.T) {
	config := &agent.LoopReActConfig{}
	applyAPISessionToolObservation(config, &Handler{}, "sess-1", knowledge.ObservationSourceMainSession, "")
	require.Nil(t, config.OnToolObserved)

	applyAPISessionToolObservation(config, nil, "sess-1", knowledge.ObservationSourceMainSession, "")
	require.Nil(t, config.OnToolObserved)

	applyAPISessionToolObservation(nil, &Handler{}, "sess-1", knowledge.ObservationSourceMainSession, "") // 不得 panic
}

func TestApplyAPISessionToolObservation_RecorderForwardsToStore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeShadow
	act, err := knowledge.Activate(ctx, cfg, root, knowledge.ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	require.NotNil(t, act)
	t.Cleanup(func() { _ = act.Close() })

	recorder := act.Recorder()
	require.NotNil(t, recorder)

	config := &agent.LoopReActConfig{}
	applyAPISessionToolObservation(config, &Handler{knowledgeRecorder: recorder}, "sess-host",
		knowledge.ObservationSourceMainSession, "task-wiring")
	require.NotNil(t, config.OnToolObserved)

	config.OnToolObserved(ctx, "", runtimetypes.ToolCall{
		Name: "grep",
		Args: map[string]interface{}{"pattern": "Foo", "path": "backend"},
	}, "backend/a.go:3:func Foo\n", "")
	require.NoError(t, recorder.Flush(ctx))

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
	require.True(t, foundFile, "grep baseline 文件必须落成 file 节点")

	session, ok, err := store.LatestExplorationSession(ctx, wsID, "sess-host")
	require.NoError(t, err)
	require.True(t, ok, "空 sessionID 回落到宿主会话")
	require.Equal(t, "sess-host", session.SessionID)
	require.Equal(t, "task-wiring", session.TaskID, "W6：任务作用域必须透传到探索会话行")
}
