package knowledge

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestLookupExplorationNodesFilters(t *testing.T) {
	ctx := context.Background()
	store := openExplorationTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	sessA, err := store.UpsertExplorationSession(ctx, ExplorationSession{WorkspaceID: wsID, SessionID: "sess-1", TaskID: "task-a"})
	if err != nil {
		t.Fatalf("session task-a: %v", err)
	}
	sessB, err := store.UpsertExplorationSession(ctx, ExplorationSession{WorkspaceID: wsID, SessionID: "sess-1", TaskID: "task-b"})
	if err != nil {
		t.Fatalf("session task-b: %v", err)
	}

	nodes := []ExplorationNode{
		{ExplorationID: sessA, NodeType: NodeTypeFile, Target: "pkg/a.go", KnowledgeVersion: "wv1"},
		{ExplorationID: sessA, NodeType: NodeTypeSymbol, Target: "pkg/a.go#Foo", KnowledgeVersion: "wv1"},
		{ExplorationID: sessA, NodeType: NodeTypeQuery, Target: "hash-q1", KnowledgeVersion: "wv1"},
		{ExplorationID: sessB, NodeType: NodeTypeFile, Target: "pkg/b.go", KnowledgeVersion: "wv1"},
	}
	for _, node := range nodes {
		if _, err := store.AppendExplorationNode(ctx, node); err != nil {
			t.Fatalf("append %+v: %v", node, err)
		}
	}

	cases := []struct {
		name string
		q    ExplorationNodeQuery
		want int
	}{
		{"workspace scope", ExplorationNodeQuery{WorkspaceID: wsID}, 4},
		{"task scope", ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-a"}, 3},
		{"other task scope", ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-b"}, 1},
		{"cross-task by target", ExplorationNodeQuery{WorkspaceID: wsID, Target: "pkg/a.go"}, 1},
		{"workspace by type", ExplorationNodeQuery{WorkspaceID: wsID, Type: NodeTypeFile}, 2},
		{"task by type", ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-a", Type: NodeTypeFile}, 1},
		{"target+type mismatch", ExplorationNodeQuery{WorkspaceID: wsID, Target: "pkg/a.go", Type: NodeTypeSymbol}, 0},
		{"unknown task", ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-x"}, 0},
	}
	for _, tc := range cases {
		got, err := store.LookupExplorationNodes(ctx, tc.q)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if len(got) != tc.want {
			t.Errorf("%s: %d nodes, want %d", tc.name, len(got), tc.want)
		}
	}

	// workspace_id 必填；非法枚举显式失败而不是静默返回全部。
	if _, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: "  "}); err == nil {
		t.Error("lookup without workspace_id succeeded, want error")
	}
	if _, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Type: NodeType("hint")}); err == nil {
		t.Error("lookup with unknown node_type succeeded, want error")
	}
}

// 会话级工作集（W1 DTO 的"宿主无任务语义"回退口径）：TaskID 为空、SessionID
// 圈定该会话下 task_id 为空的节点；任务行与其它会话都不得被带出。
func TestLookupExplorationNodesSessionWorkSet(t *testing.T) {
	ctx := context.Background()
	store := openExplorationTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	sessChat, err := store.UpsertExplorationSession(ctx, ExplorationSession{WorkspaceID: wsID, SessionID: "sess-chat"})
	if err != nil {
		t.Fatalf("session sess-chat: %v", err)
	}
	// 同一会话下跑过任务：任务行必须走 TaskID 路径，不得混入会话级工作集。
	sessTask, err := store.UpsertExplorationSession(ctx, ExplorationSession{WorkspaceID: wsID, SessionID: "sess-chat", TaskID: "task-1"})
	if err != nil {
		t.Fatalf("session sess-chat/task-1: %v", err)
	}
	sessOther, err := store.UpsertExplorationSession(ctx, ExplorationSession{WorkspaceID: wsID, SessionID: "sess-other"})
	if err != nil {
		t.Fatalf("session sess-other: %v", err)
	}

	nodes := []ExplorationNode{
		{ExplorationID: sessChat, NodeType: NodeTypeFile, Target: "pkg/a.go", KnowledgeVersion: "wv1"},
		{ExplorationID: sessChat, NodeType: NodeTypeQuery, Target: "hash-q1", KnowledgeVersion: "wv1"},
		{ExplorationID: sessTask, NodeType: NodeTypeFile, Target: "pkg/b.go", KnowledgeVersion: "wv1"},
		{ExplorationID: sessOther, NodeType: NodeTypeFile, Target: "pkg/c.go", KnowledgeVersion: "wv1"},
	}
	for _, node := range nodes {
		if _, err := store.AppendExplorationNode(ctx, node); err != nil {
			t.Fatalf("append %+v: %v", node, err)
		}
	}

	cases := []struct {
		name string
		q    ExplorationNodeQuery
		want int
	}{
		{"session work set", ExplorationNodeQuery{WorkspaceID: wsID, SessionID: "sess-chat"}, 2},
		{"session work set excludes task rows", ExplorationNodeQuery{WorkspaceID: wsID, SessionID: "sess-chat", Target: "pkg/b.go"}, 0},
		{"session work set by type", ExplorationNodeQuery{WorkspaceID: wsID, SessionID: "sess-chat", Type: NodeTypeFile}, 1},
		{"task rows stay on task path", ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-1"}, 1},
		{"unknown session", ExplorationNodeQuery{WorkspaceID: wsID, SessionID: "sess-x"}, 0},
		{"other session isolated", ExplorationNodeQuery{WorkspaceID: wsID, SessionID: "sess-other"}, 1},
	}
	for _, tc := range cases {
		got, err := store.LookupExplorationNodes(ctx, tc.q)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if len(got) != tc.want {
			t.Errorf("%s: %d nodes, want %d", tc.name, len(got), tc.want)
		}
	}
}

func TestLookupExplorationNodesLimitDefault(t *testing.T) {
	ctx, store, wsID, explorationID := newExplorationFixture(t)

	total := defaultQueryLimit + 5
	for i := 0; i < total; i++ {
		if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
			ExplorationID:    explorationID,
			NodeType:         NodeTypeQuery,
			Target:           fmt.Sprintf("q-%04d", i),
			KnowledgeVersion: "wv1",
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	defaulted, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-1"})
	if err != nil {
		t.Fatalf("lookup default limit: %v", err)
	}
	if len(defaulted) != defaultQueryLimit {
		t.Fatalf("default limit returned %d nodes, want %d", len(defaulted), defaultQueryLimit)
	}

	explicit, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-1", Limit: 3})
	if err != nil {
		t.Fatalf("lookup explicit limit: %v", err)
	}
	if len(explicit) != 3 {
		t.Fatalf("explicit limit returned %d nodes, want 3", len(explicit))
	}
}

func TestLookupExplorationNodesOrderStable(t *testing.T) {
	ctx, store, wsID, explorationID := newExplorationFixture(t)
	base := time.UnixMilli(1_700_000_000_000)

	oldID, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID, NodeType: NodeTypeFile, Target: "old.go",
		KnowledgeVersion: "wv1", CreatedAt: base.Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("append old: %v", err)
	}
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID, NodeType: NodeTypeFile, Target: "mid.go",
		KnowledgeVersion: "wv1", CreatedAt: base.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("append mid: %v", err)
	}
	freshID, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: explorationID, NodeType: NodeTypeFile, Target: "fresh.go",
		KnowledgeVersion: "wv1", CreatedAt: base.Add(-30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("append fresh: %v", err)
	}
	// old 被触碰：最近使用优先排在未使用节点之前。
	if err := store.TouchExplorationNode(ctx, oldID, base); err != nil {
		t.Fatalf("touch old: %v", err)
	}

	q := ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-1"}
	first, err := store.LookupExplorationNodes(ctx, q)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("nodes = %d, want 3", len(first))
	}
	wantFirst, wantSecond := oldID, freshID
	gotOrder := []string{first[0].ID, first[1].ID, first[2].ID}
	if gotOrder[0] != wantFirst || gotOrder[1] != wantSecond {
		t.Fatalf("order = %v, want used node first then newer unused (old=%s fresh=%s)", gotOrder, oldID, freshID)
	}
	if first[2].Target != "mid.go" {
		t.Fatalf("third node = %q, want mid.go (oldest unused)", first[2].Target)
	}

	second, err := store.LookupExplorationNodes(ctx, q)
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("lookup order is not stable:\nfirst  = %+v\nsecond = %+v", first, second)
	}

	limited, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-1", Limit: 2})
	if err != nil {
		t.Fatalf("limited lookup: %v", err)
	}
	if len(limited) != 2 || limited[0].ID != first[0].ID || limited[1].ID != first[1].ID {
		t.Fatalf("limited order = %+v, want prefix of full order", limited)
	}
}

func TestLatestExplorationSessionOrdering(t *testing.T) {
	ctx := context.Background()
	store := openExplorationTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if _, ok, err := store.LatestExplorationSession(ctx, wsID, "sess-x"); err != nil || ok {
		t.Fatalf("unknown session = ok %v, err %v; want not found", ok, err)
	}

	base := time.UnixMilli(1_700_000_000_000)
	firstID, err := store.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: wsID, SessionID: "sess-1", TaskID: "task-1",
		CreatedAt: base.Add(-2 * time.Hour), UpdatedAt: base.Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("upsert task-1: %v", err)
	}
	secondID, err := store.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: wsID, SessionID: "sess-1", TaskID: "task-2",
		CreatedAt: base.Add(-time.Hour), UpdatedAt: base.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("upsert task-2: %v", err)
	}
	latest, ok, err := store.LatestExplorationSession(ctx, wsID, "sess-1")
	if err != nil || !ok {
		t.Fatalf("latest = ok %v, err %v", ok, err)
	}
	if latest.ID != secondID || latest.TaskID != "task-2" {
		t.Fatalf("latest = %+v, want task-2 (%s)", latest, secondID)
	}

	// 重新登记 task-1 并推进 updated_at：latest 必须切换。
	if _, err := store.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: wsID, SessionID: "sess-1", TaskID: "task-1", UpdatedAt: base,
	}); err != nil {
		t.Fatalf("re-upsert task-1: %v", err)
	}
	latest, ok, err = store.LatestExplorationSession(ctx, wsID, "sess-1")
	if err != nil || !ok {
		t.Fatalf("latest after touch = ok %v, err %v", ok, err)
	}
	if latest.ID != firstID || latest.TaskID != "task-1" {
		t.Fatalf("latest after touch = %+v, want task-1 (%s)", latest, firstID)
	}

	if _, _, err := store.LatestExplorationSession(ctx, "", "sess-1"); err == nil {
		t.Error("latest without workspace_id succeeded, want error")
	}
	if _, _, err := store.LatestExplorationSession(ctx, wsID, ""); err == nil {
		t.Error("latest without session_id succeeded, want error")
	}
}
