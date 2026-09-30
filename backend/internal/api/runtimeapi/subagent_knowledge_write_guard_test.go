package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// W6 编排路径断言（06 §4 Phase 2 W6）：宿主把子代理模式 / 任务作用域传进
// Recorder 后，只读子代理的观察零写入、可写子代理按 task 作用域写入。

type knowledgeGuardFixture struct {
	ctx      context.Context
	root     string
	act      *knowledge.Activation
	recorder *knowledge.ExplorationRecorder
}

func newKnowledgeGuardFixture(t *testing.T) knowledgeGuardFixture {
	t.Helper()
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
	return knowledgeGuardFixture{ctx: ctx, root: root, act: act, recorder: recorder}
}

func (f knowledgeGuardFixture) explorationCounts(t *testing.T) (sessions, nodes int) {
	t.Helper()
	store := f.act.Layer().Store()
	wsID, ok, err := store.FindWorkspace(f.ctx, f.root)
	require.NoError(t, err)
	if !ok {
		return 0, 0
	}
	_, found, err := store.LatestExplorationSession(f.ctx, wsID, "sess-child")
	require.NoError(t, err)
	if found {
		sessions = 1
	}
	items, err := store.LookupExplorationNodes(f.ctx, knowledge.ExplorationNodeQuery{WorkspaceID: wsID, Limit: 50})
	require.NoError(t, err)
	nodes = len(items)
	return sessions, nodes
}

// 只读子代理：hook 已接线但观察零写入（门禁在 Recorder 侧，宿主无需分支）。
func TestApplyAPISessionToolObservation_ReadOnlySubagentZeroWrite(t *testing.T) {
	fixture := newKnowledgeGuardFixture(t)
	config := &agent.LoopReActConfig{}
	applyAPISessionToolObservation(config, &Handler{knowledgeRecorder: fixture.recorder}, "sess-child",
		knowledge.ObservationSourceFor(true, true), "task-readonly")
	require.NotNil(t, config.OnToolObserved)

	config.OnToolObserved(fixture.ctx, "", runtimetypes.ToolCall{
		Name: "grep",
		Args: map[string]interface{}{"pattern": "Foo", "path": "backend"},
	}, "backend/a.go:3:func Foo\n", "")
	require.NoError(t, fixture.recorder.Flush(fixture.ctx))

	sessions, nodes := fixture.explorationCounts(t)
	require.Zero(t, sessions, "只读子代理不得登记探索会话")
	require.Zero(t, nodes, "只读子代理不得写探索节点")
}

// 未标注来源（判定失败）：默认不写（fail closed）。
func TestApplyAPISessionToolObservation_UnspecifiedSourceZeroWrite(t *testing.T) {
	fixture := newKnowledgeGuardFixture(t)
	config := &agent.LoopReActConfig{}
	applyAPISessionToolObservation(config, &Handler{knowledgeRecorder: fixture.recorder}, "sess-child",
		knowledge.ObservationSourceUnspecified, "task-unknown")
	require.NotNil(t, config.OnToolObserved)

	config.OnToolObserved(fixture.ctx, "", runtimetypes.ToolCall{
		Name: "view",
		Args: map[string]interface{}{"file_path": "backend/a.go"},
	}, "line one\n", "")
	require.NoError(t, fixture.recorder.Flush(fixture.ctx))

	sessions, nodes := fixture.explorationCounts(t)
	require.Zero(t, sessions)
	require.Zero(t, nodes)
}

// 可写子代理：观察落库，且任务作用域随观察写入（session_id + task_id）。
func TestApplyAPISessionToolObservation_WritableSubagentWritesTaskScope(t *testing.T) {
	fixture := newKnowledgeGuardFixture(t)
	config := &agent.LoopReActConfig{}
	applyAPISessionToolObservation(config, &Handler{knowledgeRecorder: fixture.recorder}, "sess-child",
		knowledge.ObservationSourceFor(true, false), "task-writable")
	require.NotNil(t, config.OnToolObserved)

	config.OnToolObserved(fixture.ctx, "", runtimetypes.ToolCall{
		Name: "grep",
		Args: map[string]interface{}{"pattern": "Foo", "path": "backend"},
	}, "backend/a.go:3:func Foo\n", "")
	require.NoError(t, fixture.recorder.Flush(fixture.ctx))

	sessions, nodes := fixture.explorationCounts(t)
	require.Equal(t, 1, sessions)
	require.Equal(t, 2, nodes, "query 节点 + file 节点")

	store := fixture.act.Layer().Store()
	wsID, ok, err := store.FindWorkspace(fixture.ctx, fixture.root)
	require.NoError(t, err)
	require.True(t, ok)
	session, found, err := store.LatestExplorationSession(fixture.ctx, wsID, "sess-child")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "task-writable", session.TaskID, "可写子代理必须携带任务作用域")
}
