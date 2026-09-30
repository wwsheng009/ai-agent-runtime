package knowledge

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// W6 多 Agent 写入门禁测试（06 §4 Phase 2 W6）：
//   - 只读子代理 / 只读会话 / 未标注来源零写入（fail closed）；
//   - 可写子代理按 per-task 作用域写入（session_id + task_id + workspace_id）；
//   - 写入侧跨任务硬规则（≥0.90 不得下调）与 W3 Gate 的阈值边界一致；
//   - workspace 隔离（不同工作区互不可见）。

// 来源 → 可写性判定矩阵（04 §4.5 交付 6 / §6 R10）。
func TestObservationSourceWriteAllowedMatrix(t *testing.T) {
	cases := []struct {
		name     string
		source   ObservationSource
		writable bool
	}{
		{"unspecified_fail_closed", ObservationSourceUnspecified, false},
		{"main_session", ObservationSourceMainSession, true},
		{"subagent_writable", ObservationSourceSubagent, true},
		{"subagent_read_only", ObservationSourceSubagentReadOnly, false},
		{"read_only_session", ObservationSourceReadOnlySession, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.writable, tc.source.WriteAllowed())
		})
	}

	require.Equal(t, ObservationSourceSubagentReadOnly, ObservationSourceFor(true, true))
	require.Equal(t, ObservationSourceReadOnlySession, ObservationSourceFor(false, true))
	require.Equal(t, ObservationSourceSubagent, ObservationSourceFor(true, false))
	require.Equal(t, ObservationSourceMainSession, ObservationSourceFor(false, false))
}

// 只读子代理 / 只读会话 / 未标注来源：观察零写入（三表都不落行）。
func TestExplorationRecorderReadOnlySourcesZeroWrite(t *testing.T) {
	ctx, store, wsID := newRecorderFixture(t)
	recorder := NewExplorationRecorder(ExplorationRecorderConfig{
		Store: store, Workspace: t.TempDir(), WorkspaceID: wsID, Mode: ModeShadow,
	})
	require.NotNil(t, recorder)
	t.Cleanup(recorder.Close)

	for _, source := range []ObservationSource{
		ObservationSourceSubagentReadOnly,
		ObservationSourceReadOnlySession,
		ObservationSourceUnspecified,
	} {
		call := grepObservedCall("Foo", "backend", "backend/a.go:3:func Foo\n")
		call.Source = source
		recorder.Record(ctx, call)
	}
	require.NoError(t, recorder.Flush(ctx))

	require.Zero(t, rawCount(t, store, `SELECT COUNT(*) FROM exploration_sessions`), "只读来源不得登记探索会话")
	require.Zero(t, rawCount(t, store, `SELECT COUNT(*) FROM exploration_nodes`), "只读来源不得写探索节点")
	require.Zero(t, rawCount(t, store, `SELECT COUNT(*) FROM exploration_edges`), "只读来源不得写探索边")
}

// 可写子代理：按 per-task 作用域写入（session_id + task_id + workspace_id），
// 主会话与可写子代理走同一写路径，只读一侧才是硬拦。
func TestExplorationRecorderWritableSubagentWritesTaskScope(t *testing.T) {
	ctx, store, wsID := newRecorderFixture(t)
	recorder := NewExplorationRecorder(ExplorationRecorderConfig{
		Store: store, Workspace: t.TempDir(), WorkspaceID: wsID, Mode: ModeShadow,
	})
	t.Cleanup(recorder.Close)

	call := grepObservedCall("Foo", "backend", "backend/a.go:3:func Foo\n")
	call.SessionID = "sess-sub"
	call.TaskID = "task-7"
	call.Source = ObservationSourceSubagent
	recorder.Record(ctx, call)
	require.NoError(t, recorder.Flush(ctx))

	require.Equal(t, 1, rawCount(t, store,
		`SELECT COUNT(*) FROM exploration_sessions WHERE session_id = ? AND task_id = ?`, "sess-sub", "task-7"),
		"节点作用域必须写 session_id + task_id + workspace_id")

	scoped, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{
		WorkspaceID: wsID, TaskID: "task-7", Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, scoped, 2, "query 节点 + file 节点")

	// 跨任务查询（TaskID 为空）按 workspace 全量可见：复用侧再由 W3 Gate
	// 按 ≥0.90 + 强制验证读取判定（作用域隔离不等于任务隔离写入）。
	all, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, all, 2)

	// 其它任务切片看不到本任务节点。
	other, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{
		WorkspaceID: wsID, TaskID: "task-other", Limit: 100,
	})
	require.NoError(t, err)
	require.Empty(t, other)
}

// 写入侧跨任务硬规则：任务作用域未知时，低于跨任务下限（0.90）的节点不得落库；
// 阈值取 W3 默认值，不得下调。
func TestCrossTaskWriteFloorBoundary(t *testing.T) {
	require.Equal(t, 0.90, DefaultCrossTaskConfidence, "跨任务下限默认 0.90（W6 不得下调）")
	require.Equal(t, DefaultCrossTaskConfidence, crossTaskWriteFloor())

	queryNode := ExplorationNode{ID: "n-query", NodeType: NodeTypeQuery, Target: "q", Confidence: 0.90}
	fileNode := ExplorationNode{ID: "n-file", NodeType: NodeTypeFile, Target: "a.go", Confidence: 1.0}
	lowNode := ExplorationNode{ID: "n-low", NodeType: NodeTypeFile, Target: "b.go", Confidence: 0.8999}
	edges := []ExplorationEdge{
		{ID: "e-ok", FromNodeID: "n-query", ToNodeID: "n-file"},
		{ID: "e-low", FromNodeID: "n-query", ToNodeID: "n-low"},
	}

	// task_id 为空：== 0.90 视为通过（≥ 语义）；0.8999 丢弃，边同步丢弃。
	nodes, keptEdges := applyCrossTaskWriteFloor("", []ExplorationNode{queryNode, fileNode, lowNode}, edges)
	require.Len(t, nodes, 2)
	require.Len(t, keptEdges, 1)
	require.Equal(t, "e-ok", keptEdges[0].ID, "指向被丢弃节点的边不得悬挂")

	// 有任务作用域：低置信节点允许写入（同任务复用带 ≥0.80 由 W3 Gate 判定）。
	nodes, keptEdges = applyCrossTaskWriteFloor("task-1", []ExplorationNode{queryNode, lowNode}, edges)
	require.Len(t, nodes, 2)
	require.Len(t, keptEdges, 2)
}

// 阈值边界与 W3 Gate 对齐：跨任务 0.90 达到直接复用阈值（强制验证读取）；
// 0.8999 落入待验证带（reuse_verify + provisional），仍不可直接复用。
func TestCrossTaskReuseGateBoundary(t *testing.T) {
	cfg := DefaultPlannerConfig()
	base := ReuseGateInput{
		Scope:         ReuseScopeCrossTask,
		StoredVersion: "wv1_x",
		Current:       VersionObservation{Version: "wv1_x"},
		Thresholds:    cfg,
	}

	at := base
	at.Confidence = 0.90
	gate := EvaluateReuseGate(at)
	require.True(t, gate.Usable)
	require.Equal(t, ReuseDecisionReuse, gate.Decision)
	require.True(t, gate.Verify, "跨任务复用必须强制一次验证读取")
	require.False(t, gate.Provisional)

	below := base
	below.Confidence = 0.8999
	gate = EvaluateReuseGate(below)
	require.True(t, gate.Usable)
	require.Equal(t, ReuseDecisionReuseVerify, gate.Decision)
	require.True(t, gate.Provisional)
	require.True(t, gate.Verify)
}

// workspace 隔离：不同工作区的探索记忆互不可见（读取路径 join sessions）。
func TestExplorationRecorderWorkspaceIsolation(t *testing.T) {
	ctx, store, wsID := newRecorderFixture(t)
	otherWS, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	require.NoError(t, err)
	require.NotEqual(t, wsID, otherWS)

	recorder := NewExplorationRecorder(ExplorationRecorderConfig{
		Store: store, Workspace: t.TempDir(), WorkspaceID: wsID, Mode: ModeShadow,
	})
	t.Cleanup(recorder.Close)

	call := grepObservedCall("Foo", "backend", "backend/a.go:3:func Foo\n")
	call.TaskID = "task-1"
	recorder.Record(ctx, call)
	require.NoError(t, recorder.Flush(ctx))

	mine, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, mine, 2)

	foreign, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: otherWS, Limit: 100})
	require.NoError(t, err)
	require.Empty(t, foreign, "workspace 隔离：不得跨工作区命中探索节点")
}
