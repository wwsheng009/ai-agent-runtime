package knowledge

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// newRecorderFixture 打开 owner store 并登记 workspace，返回 (ctx, store, wsID)。
func newRecorderFixture(t *testing.T) (context.Context, Store, string) {
	t.Helper()
	ctx := context.Background()
	store := openExplorationTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	require.NoError(t, err)
	return ctx, store, wsID
}

func grepObservedCall(pattern, path, output string) ObservedCall {
	return ObservedCall{
		SessionID: "sess-rec",
		Tool:      "grep",
		Args:      map[string]any{"pattern": pattern, "path": path},
		Output:    output,
		Source:    ObservationSourceMainSession,
	}
}

func findRecorderNode(t *testing.T, nodes []ExplorationNode, nodeType NodeType, target string) ExplorationNode {
	t.Helper()
	for _, node := range nodes {
		if node.NodeType == nodeType && node.Target == target {
			return node
		}
	}
	t.Fatalf("node %s/%q not found in %#v", nodeType, target, nodes)
	return ExplorationNode{}
}

// 映射语义：grep → query 节点（哈希，不落明文）+ 去重后的 file 节点 + derived_from 边；
// 节点必须带 confidence 与 knowledge_version（G2）。
func TestExplorationRecorderMapsGrepToQueryAndFileNodes(t *testing.T) {
	ctx, store, wsID := newRecorderFixture(t)
	recorder := NewExplorationRecorder(ExplorationRecorderConfig{
		Store: store, Workspace: t.TempDir(), WorkspaceID: wsID, Mode: ModeShadow,
	})
	require.NotNil(t, recorder)
	t.Cleanup(recorder.Close)

	output := "backend/a.go:3:func Foo\nbackend/b.go:9:call Foo\nbackend/a.go:7:Foo again\n"
	recorder.Record(ctx, grepObservedCall("Foo", "backend", output))
	require.NoError(t, recorder.Flush(ctx))

	nodes, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, nodes, 3, "query 节点 + 2 个去重后的 file 节点")

	queryNode := findRecorderNode(t, nodes, NodeTypeQuery, queryHash("grep", "Foo", "backend"))
	require.Equal(t, observedQueryNodeConfidence, queryNode.Confidence)
	require.True(t, strings.HasPrefix(queryNode.KnowledgeVersion, "wv1_"), "节点必须携带工作区版本")
	require.NotContains(t, queryNode.Target, "Foo", "隐私：query 只落哈希，不落 pattern 明文")

	for _, file := range []string{"backend/a.go", "backend/b.go"} {
		node := findRecorderNode(t, nodes, NodeTypeFile, file)
		require.Equal(t, observedFileNodeConfidence, node.Confidence)
		require.True(t, strings.HasPrefix(node.KnowledgeVersion, "wv1_"))
	}

	require.Equal(t, 2, rawCount(t, store, `SELECT COUNT(*) FROM exploration_edges`))
	require.Equal(t, 1, rawCount(t, store, `SELECT COUNT(*) FROM exploration_sessions WHERE session_id = ?`, "sess-rec"))
	require.Equal(t, 1, rawCount(t, store, `SELECT COUNT(*) FROM exploration_nodes WHERE node_type = ?`, string(NodeTypeQuery)))
}

// view → file 节点 + query 节点（区间作用域参与哈希）。
func TestExplorationRecorderMapsViewToFileNode(t *testing.T) {
	ctx, store, wsID := newRecorderFixture(t)
	recorder := NewExplorationRecorder(ExplorationRecorderConfig{
		Store: store, Workspace: t.TempDir(), WorkspaceID: wsID, Mode: ModeShadow,
	})
	t.Cleanup(recorder.Close)

	recorder.Record(ctx, ObservedCall{
		SessionID: "sess-view",
		Tool:      "view",
		Args:      map[string]any{"file_path": "backend/a.go", "offset": 10, "limit": 20},
		Output:    "line one\nline two\n",
		Source:    ObservationSourceMainSession,
	})
	require.NoError(t, recorder.Flush(ctx))

	nodes, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	findRecorderNode(t, nodes, NodeTypeQuery, queryHash("view", "backend/a.go", "backend/a.go:10+20"))
	findRecorderNode(t, nodes, NodeTypeFile, "backend/a.go")
	require.Equal(t, 1, rawCount(t, store, `SELECT COUNT(*) FROM exploration_edges`))
}

// 幂等/去重：同 target 重复观察命中同一行（use_count 累加），不堆叠重复节点。
func TestExplorationRecorderDeduplicatesSameTarget(t *testing.T) {
	ctx, store, wsID := newRecorderFixture(t)
	recorder := NewExplorationRecorder(ExplorationRecorderConfig{
		Store: store, Workspace: t.TempDir(), WorkspaceID: wsID, Mode: ModeShadow,
	})
	t.Cleanup(recorder.Close)

	call := grepObservedCall("Foo", "backend", "backend/a.go:3:func Foo\n")
	recorder.Record(ctx, call)
	recorder.Record(ctx, call)
	require.NoError(t, recorder.Flush(ctx))

	nodes, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, nodes, 2, "重复观察不得堆叠重复节点")
	queryNode := findRecorderNode(t, nodes, NodeTypeQuery, queryHash("grep", "Foo", "backend"))
	require.Equal(t, 2, queryNode.UseCount, "同 target 合并：use_count 累加")
}

// mode=off（构造器直接返回 nil）→ 全部方法 no-op，零写入（硬约束）。
func TestExplorationRecorderOffModeZeroWrite(t *testing.T) {
	ctx, store, wsID := newRecorderFixture(t)
	recorder := NewExplorationRecorder(ExplorationRecorderConfig{
		Store: store, Workspace: t.TempDir(), WorkspaceID: wsID, Mode: ModeOff,
	})
	require.Nil(t, recorder, "off 模式不得构造采集器")

	recorder.Record(ctx, grepObservedCall("Foo", "backend", "backend/a.go:3:Foo\n"))
	require.NoError(t, recorder.Flush(ctx))
	recorder.Close()

	require.Zero(t, rawCount(t, store, `SELECT COUNT(*) FROM exploration_sessions`))
	require.Zero(t, rawCount(t, store, `SELECT COUNT(*) FROM exploration_nodes`))
	require.Zero(t, rawCount(t, store, `SELECT COUNT(*) FROM exploration_edges`))
}

// Activation 门控：off → nil；shadow + owner → 非 nil 且同一实例复用。
func TestActivationRecorderGating(t *testing.T) {
	ctx := context.Background()

	off, err := Activate(ctx, DefaultConfig(), t.TempDir(), ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	require.Nil(t, off, "off 不产生 Activation")
	require.Nil(t, off.Recorder(), "off 的 Recorder 必须为 nil")

	root := t.TempDir()
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Mode = ModeShadow
	act, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = act.Close() })

	recorder := act.Recorder()
	require.NotNil(t, recorder, "shadow + owner 必须提供采集器")
	require.Same(t, recorder, act.Recorder(), "同一 Activation 复用同一采集器实例")

	// reader 角色不写库：即使 mode=shadow，Recorder 也必须为 nil（零写入）。
	readerStore := openExplorationTestStore(t)
	reader := &Activation{layer: &Layer{
		cfg:   Config{Mode: ModeShadow, Workspace: t.TempDir()},
		role:  RoleReader,
		store: readerStore,
	}}
	require.Nil(t, reader.Recorder(), "reader 角色不得提供采集器")
}

// 未启用/未接线（nil 指针）→ 所有方法 no-op、不 panic。
func TestExplorationRecorderNilSafe(t *testing.T) {
	var recorder *ExplorationRecorder
	recorder.Record(context.Background(), grepObservedCall("Foo", "backend", "backend/a.go:3:Foo\n"))
	require.NoError(t, recorder.Flush(context.Background()))
	recorder.Close()
}

// 写失败不冒泡：Flush 返回 nil，错误只经 OnError 上报。
func TestExplorationRecorderErrorsDoNotBubble(t *testing.T) {
	ctx, store, wsID := newRecorderFixture(t)
	var mu sync.Mutex
	var errs []error
	recorder := NewExplorationRecorder(ExplorationRecorderConfig{
		Store: store, Workspace: t.TempDir(), WorkspaceID: wsID, Mode: ModeShadow,
		OnError: func(err error) {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		},
	})
	t.Cleanup(recorder.Close)
	require.NoError(t, store.Close(), "先关库让写路径失败")

	recorder.Record(ctx, grepObservedCall("Foo", "backend", "backend/a.go:3:Foo\n"))
	require.NoError(t, recorder.Flush(ctx), "写失败不得从 Flush 冒泡")

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, errs, "写失败必须经 OnError 显式上报")
}

// 单次观察的文件目标数上限。
func TestExplorationRecorderCapsTargetsPerCall(t *testing.T) {
	ctx, store, wsID := newRecorderFixture(t)
	recorder := NewExplorationRecorder(ExplorationRecorderConfig{
		Store: store, Workspace: t.TempDir(), WorkspaceID: wsID, Mode: ModeShadow,
		MaxTargetsPerCall: 2,
	})
	t.Cleanup(recorder.Close)

	recorder.Record(ctx, grepObservedCall("A", "backend",
		"backend/a.go:1:A\nbackend/b.go:1:B\nbackend/c.go:1:C\n"))
	require.NoError(t, recorder.Flush(ctx))

	nodes, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, nodes, 3, "query + 上限内的 2 个文件")
	require.Equal(t, 2, rawCount(t, store, `SELECT COUNT(*) FROM exploration_edges`))
	require.Zero(t, rawCount(t, store, `SELECT COUNT(*) FROM exploration_nodes WHERE target = ?`, "backend/c.go"))
}

// 非目标工具 / 失败调用 / 无会话 → 零写入。
func TestExplorationRecorderIgnoresNonTargetAndFailedCalls(t *testing.T) {
	ctx, store, wsID := newRecorderFixture(t)
	recorder := NewExplorationRecorder(ExplorationRecorderConfig{
		Store: store, Workspace: t.TempDir(), WorkspaceID: wsID, Mode: ModeShadow,
	})
	t.Cleanup(recorder.Close)

	recorder.Record(ctx, ObservedCall{
		SessionID: "s", Tool: "glob", Output: "x", Source: ObservationSourceMainSession,
	})
	recorder.Record(ctx, ObservedCall{
		SessionID: "s", Tool: "grep",
		Args:   map[string]any{"pattern": "Foo"},
		Output: "backend/a.go:1:Foo\n",
		Err:    "exit status 1",
		Source: ObservationSourceMainSession,
	})
	recorder.Record(ctx, ObservedCall{
		Tool: "grep", Args: map[string]any{"pattern": "Foo"}, Source: ObservationSourceMainSession,
	})
	require.NoError(t, recorder.Flush(ctx))

	require.Zero(t, rawCount(t, store, `SELECT COUNT(*) FROM exploration_sessions`))
	require.Zero(t, rawCount(t, store, `SELECT COUNT(*) FROM exploration_nodes`))
}
