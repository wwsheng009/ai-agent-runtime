package knowledge

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// openExplorationTestStore 打开临时目录下的 owner store。
func openExplorationTestStore(t *testing.T) Store {
	t.Helper()
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "knowledge.db"), false)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// rawCount / rawFloat 通过 store 自身的连接做物理行断言（与既有测试的
// `store.(*sqliteStore)` 模式一致），不再开第二个连接去对账 WAL sidecar。
func rawCount(t *testing.T, store Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := rawStore(t, store).db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("raw count %q: %v", query, err)
	}
	return n
}

func rawFloat(t *testing.T, store Store, query string, args ...any) float64 {
	t.Helper()
	var v float64
	if err := rawStore(t, store).db.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("raw float %q: %v", query, err)
	}
	return v
}

func rawStore(t *testing.T, store Store) *sqliteStore {
	t.Helper()
	sqlite, ok := store.(*sqliteStore)
	if !ok || sqlite.db == nil {
		t.Fatalf("store 类型 = %T，无法做 raw 断言", store)
	}
	return sqlite
}

// newExplorationFixture 建立 workspace + 会话，返回
// (ctx, store, workspace id, exploration(session 行) id)。
func newExplorationFixture(t *testing.T) (context.Context, Store, string, string) {
	t.Helper()
	ctx := context.Background()
	store := openExplorationTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	explorationID, err := store.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: wsID,
		SessionID:   "sess-1",
		TaskID:      "task-1",
	})
	if err != nil {
		t.Fatalf("UpsertExplorationSession: %v", err)
	}
	return ctx, store, wsID, explorationID
}

func TestUpsertExplorationSessionIdempotent(t *testing.T) {
	ctx, store, wsID, explorationID := newExplorationFixture(t)

	second, err := store.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: wsID,
		SessionID:   "sess-1",
		TaskID:      "task-1",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second != explorationID {
		t.Fatalf("session id changed on re-upsert: %q vs %q", second, explorationID)
	}
	if n := rawCount(t, store, `SELECT COUNT(*) FROM exploration_sessions WHERE workspace_id = ?`, wsID); n != 1 {
		t.Fatalf("session rows = %d, want 1", n)
	}

	sess, ok, err := store.LatestExplorationSession(ctx, wsID, "sess-1")
	if err != nil || !ok {
		t.Fatalf("LatestExplorationSession = ok %v, err %v; want found", ok, err)
	}
	if sess.ID != explorationID || sess.TaskID != "task-1" {
		t.Fatalf("session = %+v, want id %q task task-1", sess, explorationID)
	}
	if sess.CreatedAt.IsZero() || sess.UpdatedAt.IsZero() {
		t.Fatalf("session timestamps not set: %+v", sess)
	}

	// 同一 session 的不同 task 是不同行，Latest 取最近更新的一行。
	if _, err := store.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: wsID,
		SessionID:   "sess-1",
		TaskID:      "task-2",
	}); err != nil {
		t.Fatalf("upsert task-2: %v", err)
	}
	if n := rawCount(t, store, `SELECT COUNT(*) FROM exploration_sessions WHERE workspace_id = ?`, wsID); n != 2 {
		t.Fatalf("session rows = %d, want 2 (one per task)", n)
	}
	latest, ok, err := store.LatestExplorationSession(ctx, wsID, "sess-1")
	if err != nil || !ok {
		t.Fatalf("LatestExplorationSession(task-2) = ok %v, err %v", ok, err)
	}
	if latest.TaskID != "task-2" {
		t.Fatalf("latest task = %q, want task-2", latest.TaskID)
	}

	if _, err := store.UpsertExplorationSession(ctx, ExplorationSession{SessionID: "sess-1"}); err == nil {
		t.Error("session without workspace_id accepted")
	}
	if _, err := store.UpsertExplorationSession(ctx, ExplorationSession{WorkspaceID: wsID}); err == nil {
		t.Error("session without session_id accepted")
	}
}

func TestAppendExplorationNodeIdempotentAndCounters(t *testing.T) {
	ctx, store, wsID, explorationID := newExplorationFixture(t)

	firstID, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID:    explorationID,
		NodeType:         NodeTypeFile,
		Target:           "pkg/a.go",
		FileID:           "f_1",
		Summary:          "first",
		Confidence:       0.9,
		KnowledgeVersion: "wv1_a",
	})
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	nodes, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-1"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	if nodes[0].UseCount != 1 || !nodes[0].LastUsedAt.IsZero() {
		t.Fatalf("first write = use_count %d last_used %v; want 1 / NULL",
			nodes[0].UseCount, nodes[0].LastUsedAt)
	}
	if nodes[0].KnowledgeVersion != "wv1_a" || nodes[0].Summary != "first" {
		t.Fatalf("first write payload = %+v", nodes[0])
	}

	secondID, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID:    explorationID,
		NodeType:         NodeTypeFile,
		Target:           "pkg/a.go",
		FileID:           "f_1",
		Summary:          "second",
		Confidence:       0.7,
		KnowledgeVersion: "wv1_b",
	})
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("node id changed on same target: %q vs %q", secondID, firstID)
	}
	nodes, err = store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-1"})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("after re-append: %d nodes, err %v; want 1", len(nodes), err)
	}
	got := nodes[0]
	if got.UseCount != 2 {
		t.Fatalf("use_count = %d, want 2", got.UseCount)
	}
	if got.LastUsedAt.IsZero() {
		t.Fatal("last_used_at not refreshed on re-append")
	}
	if got.Summary != "second" || got.KnowledgeVersion != "wv1_b" {
		t.Fatalf("re-append did not refresh payload: %+v", got)
	}
	if math.Abs(got.Confidence-0.7) > 1e-9 {
		t.Fatalf("confidence = %v, want 0.7", got.Confidence)
	}
	if n := rawCount(t, store, `SELECT COUNT(*) FROM exploration_nodes WHERE exploration_id = ?`, explorationID); n != 1 {
		t.Fatalf("node rows = %d, want 1 (same target must dedupe)", n)
	}
	if n := rawCount(t, store, `SELECT COUNT(*) FROM exploration_nodes WHERE knowledge_version IS NULL OR knowledge_version = ''`); n != 0 {
		t.Fatalf("rows with empty knowledge_version = %d, want 0", n)
	}

	// 调用方传入的 UseCount 被忽略：计数由 store 独占维护。
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID:    explorationID,
		NodeType:         NodeTypeQuery,
		Target:           "q-1",
		KnowledgeVersion: "wv1_a",
		UseCount:         99,
	}); err != nil {
		t.Fatalf("append with caller use_count: %v", err)
	}
	q, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: "q-1"})
	if err != nil || len(q) != 1 {
		t.Fatalf("lookup q-1: %d nodes, err %v", len(q), err)
	}
	if q[0].UseCount != 1 {
		t.Fatalf("caller use_count leaked into store: %d, want 1", q[0].UseCount)
	}
}

func TestAppendExplorationNodeValidationAndDefaults(t *testing.T) {
	ctx, store, wsID, explorationID := newExplorationFixture(t)

	// knowledge_version 必填：校验阶段拒绝，且不落任何行（DoD ③）。
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID,
		NodeType:      NodeTypeFile,
		Target:        "pkg/missing-version.go",
	}); err == nil {
		t.Fatal("node without knowledge_version accepted")
	}
	if n := rawCount(t, store, `SELECT COUNT(*) FROM exploration_nodes`); n != 0 {
		t.Fatalf("rejected node was persisted: %d rows", n)
	}
	// 非法枚举 / 缺 target / 缺 exploration_id 同样拒绝。
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID, NodeType: NodeType("hint"), Target: "x", KnowledgeVersion: "wv1",
	}); err == nil {
		t.Error("node with unknown node_type accepted")
	}
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID, NodeType: NodeTypeFile, KnowledgeVersion: "wv1",
	}); err == nil {
		t.Error("node without target accepted")
	}
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		NodeType: NodeTypeFile, Target: "x", KnowledgeVersion: "wv1",
	}); err == nil {
		t.Error("node without exploration_id accepted")
	}

	// 置信度零值落到 DDL 默认 0.5。
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID:    explorationID,
		NodeType:         NodeTypeQuery,
		Target:           "q-default",
		KnowledgeVersion: "wv1",
	}); err != nil {
		t.Fatalf("append default confidence: %v", err)
	}
	nodes, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: "q-default"})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("lookup q-default: %d nodes, err %v", len(nodes), err)
	}
	if math.Abs(nodes[0].Confidence-0.5) > 1e-9 {
		t.Fatalf("default confidence = %v, want 0.5", nodes[0].Confidence)
	}
}

func TestTouchExplorationNode(t *testing.T) {
	ctx, store, wsID, explorationID := newExplorationFixture(t)

	nodeID, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID:    explorationID,
		NodeType:         NodeTypeFile,
		Target:           "pkg/touch.go",
		KnowledgeVersion: "wv1_a",
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	at1 := time.UnixMilli(1_700_000_000_000)
	if err := store.TouchExplorationNode(ctx, nodeID, at1); err != nil {
		t.Fatalf("touch 1: %v", err)
	}
	nodes, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: "pkg/touch.go"})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("lookup after touch: %d nodes, err %v", len(nodes), err)
	}
	if nodes[0].UseCount != 2 || nodes[0].LastUsedAt.UnixMilli() != at1.UnixMilli() {
		t.Fatalf("after touch 1 = use_count %d last_used %v; want 2 / %v",
			nodes[0].UseCount, nodes[0].LastUsedAt, at1)
	}

	at2 := at1.Add(2 * time.Second)
	if err := store.TouchExplorationNode(ctx, nodeID, at2); err != nil {
		t.Fatalf("touch 2: %v", err)
	}
	nodes, err = store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: "pkg/touch.go"})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("lookup after touch 2: %d nodes, err %v", len(nodes), err)
	}
	if nodes[0].UseCount != 3 || nodes[0].LastUsedAt.UnixMilli() != at2.UnixMilli() {
		t.Fatalf("after touch 2 = use_count %d last_used %v; want 3 / %v",
			nodes[0].UseCount, nodes[0].LastUsedAt, at2)
	}

	if err := store.TouchExplorationNode(ctx, "en_missing", time.Now()); err == nil {
		t.Error("touch unknown node succeeded, want error")
	}
	if err := store.TouchExplorationNode(ctx, "  ", time.Now()); err == nil {
		t.Error("touch empty node id succeeded, want error")
	}
}

func TestAppendExplorationEdgeIdempotent(t *testing.T) {
	ctx, store, _, explorationID := newExplorationFixture(t)

	fromID, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID, NodeType: NodeTypeFile, Target: "pkg/a.go", KnowledgeVersion: "wv1",
	})
	if err != nil {
		t.Fatalf("append from: %v", err)
	}
	toID, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID, NodeType: NodeTypeSymbol, Target: "pkg/a.go#Foo", KnowledgeVersion: "wv1",
	})
	if err != nil {
		t.Fatalf("append to: %v", err)
	}

	firstID, err := store.AppendExplorationEdge(ctx, ExplorationEdge{
		ExplorationID: explorationID,
		FromNodeID:    fromID,
		ToNodeID:      toID,
		EdgeType:      EdgeTypeContains,
	})
	if err != nil {
		t.Fatalf("append edge: %v", err)
	}
	secondID, err := store.AppendExplorationEdge(ctx, ExplorationEdge{
		ExplorationID: explorationID,
		FromNodeID:    fromID,
		ToNodeID:      toID,
		EdgeType:      EdgeTypeContains,
		Weight:        2.5,
	})
	if err != nil {
		t.Fatalf("re-append edge: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("edge id changed on identical relation: %q vs %q", secondID, firstID)
	}
	if n := rawCount(t, store, `SELECT COUNT(*) FROM exploration_edges WHERE exploration_id = ?`, explorationID); n != 1 {
		t.Fatalf("edge rows = %d, want 1", n)
	}
	if w := rawFloat(t, store, `SELECT weight FROM exploration_edges WHERE id = ?`, firstID); math.Abs(w-2.5) > 1e-9 {
		t.Fatalf("edge weight = %v, want latest 2.5", w)
	}

	// 权重零值落到 DDL 默认 1.0。
	defaultID, err := store.AppendExplorationEdge(ctx, ExplorationEdge{
		ExplorationID: explorationID,
		FromNodeID:    toID,
		ToNodeID:      fromID,
		EdgeType:      EdgeTypeReferences,
	})
	if err != nil {
		t.Fatalf("append default-weight edge: %v", err)
	}
	if w := rawFloat(t, store, `SELECT weight FROM exploration_edges WHERE id = ?`, defaultID); math.Abs(w-1.0) > 1e-9 {
		t.Fatalf("default edge weight = %v, want 1.0", w)
	}

	if _, err := store.AppendExplorationEdge(ctx, ExplorationEdge{
		ExplorationID: explorationID, FromNodeID: fromID, ToNodeID: toID,
	}); err == nil {
		t.Error("edge without edge_type accepted")
	}
	if _, err := store.AppendExplorationEdge(ctx, ExplorationEdge{
		FromNodeID: fromID, ToNodeID: toID, EdgeType: EdgeTypeCalls,
	}); err == nil {
		t.Error("edge without exploration_id accepted")
	}
}

func TestReadOnlyExplorationStoreRejectsWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "knowledge.db")

	owner, err := OpenStore(ctx, path, false)
	if err != nil {
		t.Fatalf("OpenStore(owner): %v", err)
	}
	wsID, err := owner.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	explorationID, err := owner.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: wsID, SessionID: "sess-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatalf("UpsertExplorationSession: %v", err)
	}
	nodeID, err := owner.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID, NodeType: NodeTypeFile, Target: "pkg/a.go", KnowledgeVersion: "wv1",
	})
	if err != nil {
		t.Fatalf("AppendExplorationNode: %v", err)
	}
	if _, err := owner.AppendExplorationEdge(ctx, ExplorationEdge{
		ExplorationID: explorationID, FromNodeID: nodeID, ToNodeID: nodeID, EdgeType: EdgeTypeContains,
	}); err != nil {
		t.Fatalf("AppendExplorationEdge: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close owner: %v", err)
	}

	reader, err := OpenStore(ctx, path, true)
	if err != nil {
		t.Fatalf("OpenStore(reader): %v", err)
	}
	defer reader.Close()

	// 读路径在 reader 上必须可用。
	nodes, err := reader.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-1"})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("reader lookup = %d nodes, err %v; want 1", len(nodes), err)
	}
	if _, ok, err := reader.LatestExplorationSession(ctx, wsID, "sess-1"); err != nil || !ok {
		t.Fatalf("reader latest session = ok %v, err %v; want found", ok, err)
	}

	// 写路径必须硬失败 ErrReadOnlyStore（DoD ④）。
	if _, err := reader.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: wsID, SessionID: "sess-2", TaskID: "task-2",
	}); !errors.Is(err, ErrReadOnlyStore) {
		t.Fatalf("UpsertExplorationSession on reader = %v, want ErrReadOnlyStore", err)
	}
	if _, err := reader.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID, NodeType: NodeTypeFile, Target: "pkg/b.go", KnowledgeVersion: "wv1",
	}); !errors.Is(err, ErrReadOnlyStore) {
		t.Fatalf("AppendExplorationNode on reader = %v, want ErrReadOnlyStore", err)
	}
	if err := reader.TouchExplorationNode(ctx, nodeID, time.Now()); !errors.Is(err, ErrReadOnlyStore) {
		t.Fatalf("TouchExplorationNode on reader = %v, want ErrReadOnlyStore", err)
	}
	if _, err := reader.AppendExplorationEdge(ctx, ExplorationEdge{
		ExplorationID: explorationID, FromNodeID: nodeID, ToNodeID: nodeID, EdgeType: EdgeTypeCalls,
	}); !errors.Is(err, ErrReadOnlyStore) {
		t.Fatalf("AppendExplorationEdge on reader = %v, want ErrReadOnlyStore", err)
	}
}

func TestExplorationCascadeDelete(t *testing.T) {
	ctx, store, _, explorationID := newExplorationFixture(t)

	fromID, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID, NodeType: NodeTypeFile, Target: "pkg/a.go", KnowledgeVersion: "wv1",
	})
	if err != nil {
		t.Fatalf("append from: %v", err)
	}
	toID, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID, NodeType: NodeTypeSymbol, Target: "pkg/a.go#Foo", KnowledgeVersion: "wv1",
	})
	if err != nil {
		t.Fatalf("append to: %v", err)
	}
	if _, err := store.AppendExplorationEdge(ctx, ExplorationEdge{
		ExplorationID: explorationID, FromNodeID: fromID, ToNodeID: toID, EdgeType: EdgeTypeContains,
	}); err != nil {
		t.Fatalf("append edge: %v", err)
	}
	if n := rawCount(t, store, `SELECT COUNT(*) FROM exploration_nodes`); n != 2 {
		t.Fatalf("node rows = %d, want 2", n)
	}
	if n := rawCount(t, store, `SELECT COUNT(*) FROM exploration_edges`); n != 1 {
		t.Fatalf("edge rows = %d, want 1", n)
	}

	// 删除会话：nodes / edges 必须随外键 CASCADE 一起消失。
	db := rawStore(t, store).db
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	res, err := db.ExecContext(ctx, `DELETE FROM exploration_sessions WHERE id = ?`, explorationID)
	if err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if affected, err := res.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("delete session affected = %d (err %v), want 1", affected, err)
	}
	if n := rawCount(t, store, `SELECT COUNT(*) FROM exploration_nodes`); n != 0 {
		t.Fatalf("node rows after cascade = %d, want 0", n)
	}
	if n := rawCount(t, store, `SELECT COUNT(*) FROM exploration_edges`); n != 0 {
		t.Fatalf("edge rows after cascade = %d, want 0", n)
	}
}

func TestExplorationWorkspaceIsolation(t *testing.T) {
	ctx, store, ws1, exploration1 := newExplorationFixture(t)
	ws2, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace(ws2): %v", err)
	}
	exploration2, err := store.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: ws2, SessionID: "sess-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatalf("UpsertExplorationSession(ws2): %v", err)
	}

	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: exploration1, NodeType: NodeTypeFile, Target: "shared.go", KnowledgeVersion: "wv1",
	}); err != nil {
		t.Fatalf("append ws1 node: %v", err)
	}
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: exploration2, NodeType: NodeTypeFile, Target: "shared.go", KnowledgeVersion: "wv1",
	}); err != nil {
		t.Fatalf("append ws2 node: %v", err)
	}

	got1, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: ws1, Target: "shared.go"})
	if err != nil || len(got1) != 1 || got1[0].ExplorationID != exploration1 {
		t.Fatalf("ws1 lookup = %+v (err %v), want only exploration %q", got1, err, exploration1)
	}
	got2, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: ws2, Target: "shared.go"})
	if err != nil || len(got2) != 1 || got2[0].ExplorationID != exploration2 {
		t.Fatalf("ws2 lookup = %+v (err %v), want only exploration %q", got2, err, exploration2)
	}

	// task 过滤不串任务。
	if got, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: ws1, TaskID: "task-x"}); err != nil || len(got) != 0 {
		t.Fatalf("task-x lookup = %d nodes (err %v), want 0", len(got), err)
	}
}
