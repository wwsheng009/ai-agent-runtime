package knowledge

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// 跨任务加权检索（docs/plan/code-tools-gap-review-and-fix-plan-20260930.md §6.3）
// 的行为契约：
//   - exact > prefix > contains 权重优先，其后沿用 last_used_at/created_at/id；
//   - 限定名 / 路径符号归一化后，全名与末段都参与匹配；
//   - 无命中返回空；Limit 在排序后生效；
//   - 任务 / 会话两条路径的精确过滤语义不变。

// lookupNodeSpec 描述一条待写入的探索节点：target + created_at 相对基准的偏移。
type lookupNodeSpec struct {
	target string
	offset time.Duration
}

// appendLookupNodes 写入一批节点并返回 target → node id 映射。
func appendLookupNodes(t *testing.T, store Store, explorationID string, base time.Time, specs ...lookupNodeSpec) map[string]string {
	t.Helper()
	ids := make(map[string]string, len(specs))
	for _, spec := range specs {
		id, err := store.AppendExplorationNode(context.Background(), ExplorationNode{
			ExplorationID:    explorationID,
			NodeType:         NodeTypeSymbol,
			Target:           spec.target,
			KnowledgeVersion: "wv1",
			CreatedAt:        base.Add(spec.offset),
		})
		if err != nil {
			t.Fatalf("append %q: %v", spec.target, err)
		}
		ids[spec.target] = id
	}
	return ids
}

func lookupNodeTargets(nodes []ExplorationNode) []string {
	targets := make([]string, 0, len(nodes))
	for _, node := range nodes {
		targets = append(targets, node.Target)
	}
	return targets
}

// explorationLookupKeys 是归一化规则的稳定契约：直接钉住键集合。
func TestExplorationLookupKeysNormalization(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"bare name", "PlanInput", []string{"PlanInput"}},
		{"qualified name", "knowledge.Plan", []string{"knowledge.Plan", "Plan"}},
		{"multi-segment qualified name", "pkg.Type.Method", []string{"pkg.Type.Method", "Method"}},
		{"path", "internal/knowledge/planner.go", []string{"internal/knowledge/planner.go", "planner.go"}},
		{"file name keeps only full key", "planner.go", []string{"planner.go"}},
		{"path symbol", "pkg/a.go#Foo", []string{"pkg/a.go#Foo", "pkg/a.go", "Foo"}},
		{"windows separators and leading dot slash", `.\pkg\a.go`, []string{"pkg/a.go", "a.go"}},
		{"natural language tail", "请检查 knowledge.Plan 的复用", []string{"请检查 knowledge.Plan 的复用", "Plan"}},
		{"natural language path tail", "internal/knowledge/planner.go 的实现", []string{"internal/knowledge/planner.go 的实现", "planner.go"}},
		{"blank", "   ", nil},
	}
	for _, tc := range cases {
		got := explorationLookupKeys(tc.raw)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: keys(%q) = %v, want %v", tc.name, tc.raw, got, tc.want)
		}
	}
}

// exact > prefix > contains 权重必须压过时间：created_at 故意与期望顺序相反。
func TestLookupExplorationNodesCrossTaskWeightedTiers(t *testing.T) {
	ctx, store, wsID, explorationID := newExplorationFixture(t)
	base := time.UnixMilli(1_700_000_000_000)
	appendLookupNodes(t, store, explorationID, base,
		lookupNodeSpec{target: "PlanInput", offset: -3 * time.Hour},                // exact
		lookupNodeSpec{target: "PlanInputBuilder", offset: -2 * time.Hour},         // prefix
		lookupNodeSpec{target: "SessionSubscriptionPlanInput", offset: -time.Hour}, // contains
		lookupNodeSpec{target: "pkg/a.go", offset: -30 * time.Minute},              // 不命中
	)

	got, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: "PlanInput"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	want := []string{"PlanInput", "PlanInputBuilder", "SessionSubscriptionPlanInput"}
	if !reflect.DeepEqual(lookupNodeTargets(got), want) {
		t.Fatalf("targets = %v, want %v（权重必须先于时间）", lookupNodeTargets(got), want)
	}
}

// 限定名：全名与末段都参与匹配；权重仍优先于时间。
func TestLookupExplorationNodesCrossTaskQualifiedName(t *testing.T) {
	ctx, store, wsID, explorationID := newExplorationFixture(t)
	base := time.UnixMilli(1_700_000_000_000)

	// 全名精确（最旧）→ 全名前缀 → 尾段包含（最新）：时间顺序与期望相反。
	appendLookupNodes(t, store, explorationID, base,
		lookupNodeSpec{target: "knowledge.Plan", offset: -4 * time.Hour},
		lookupNodeSpec{target: "knowledge.PlanInput", offset: -3 * time.Hour},
		lookupNodeSpec{target: "backend/internal/knowledge/planner.go#Plan", offset: -2 * time.Hour},
		lookupNodeSpec{target: "unrelated.go", offset: -time.Hour},
	)
	got, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: "knowledge.Plan"})
	if err != nil {
		t.Fatalf("lookup qualified: %v", err)
	}
	want := []string{"knowledge.Plan", "knowledge.PlanInput", "backend/internal/knowledge/planner.go#Plan"}
	if !reflect.DeepEqual(lookupNodeTargets(got), want) {
		t.Fatalf("qualified targets = %v, want %v", lookupNodeTargets(got), want)
	}

	// 自然语言查询把限定名嵌在句中：末段截断后仍命中精确名节点。
	got, err = store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: "请检查 knowledge.Plan 的复用"})
	if err != nil {
		t.Fatalf("lookup natural language: %v", err)
	}
	found := false
	for _, node := range got {
		if node.Target == "knowledge.Plan" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("natural language lookup = %v, want knowledge.Plan among hits", lookupNodeTargets(got))
	}

	// 末段参与：查询 pkg.Type.Method 的键集 = {pkg.Type.Method, Method}。
	// 全名精确最优先；尾段 Method 的前缀命中（MethodImpl）先于包含命中
	// （Type.Method 与路径符号 a.go#Method 都只是"包含尾段"）。
	appendLookupNodes(t, store, explorationID, base,
		lookupNodeSpec{target: "pkg.Type.Method", offset: -4 * time.Hour}, // 全名精确
		lookupNodeSpec{target: "Type.Method", offset: -3 * time.Hour},     // 尾段包含
		lookupNodeSpec{target: "MethodImpl", offset: -2 * time.Hour},      // 尾段前缀
		lookupNodeSpec{target: "a.go#Method", offset: -time.Hour},         // 尾段包含（更新）
	)
	got, err = store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: "pkg.Type.Method"})
	if err != nil {
		t.Fatalf("lookup tail: %v", err)
	}
	want = []string{"pkg.Type.Method", "MethodImpl", "a.go#Method", "Type.Method"}
	if !reflect.DeepEqual(lookupNodeTargets(got), want) {
		t.Fatalf("tail targets = %v, want %v", lookupNodeTargets(got), want)
	}
}

// 路径符号与 Windows 分隔符归一化：反斜杠 / 大小写不影响命中。
func TestLookupExplorationNodesCrossTaskPathSymbolNormalization(t *testing.T) {
	ctx, store, wsID, explorationID := newExplorationFixture(t)
	base := time.UnixMilli(1_700_000_000_000)
	appendLookupNodes(t, store, explorationID, base,
		lookupNodeSpec{target: "pkg/a.go", offset: -2 * time.Hour},    // 文件节点
		lookupNodeSpec{target: "pkg/a.go#Foo", offset: -time.Hour},    // 符号节点（更新）
		lookupNodeSpec{target: "pkg/b.go", offset: -30 * time.Minute}, // 不命中
	)

	got, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: `PKG\A.GO`})
	if err != nil {
		t.Fatalf("lookup windows path: %v", err)
	}
	want := []string{"pkg/a.go", "pkg/a.go#Foo"}
	if !reflect.DeepEqual(lookupNodeTargets(got), want) {
		t.Fatalf("windows path targets = %v, want %v", lookupNodeTargets(got), want)
	}

	// 路径符号：全名精确与路径部分精确同权重，按 created_at 倒序（符号节点更新）。
	got, err = store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: `pkg\a.go#Foo`})
	if err != nil {
		t.Fatalf("lookup path symbol: %v", err)
	}
	want = []string{"pkg/a.go#Foo", "pkg/a.go"}
	if !reflect.DeepEqual(lookupNodeTargets(got), want) {
		t.Fatalf("path symbol targets = %v, want %v", lookupNodeTargets(got), want)
	}
}

// 无命中与 workspace 隔离：加权路径不得放大成全量返回，也不得跨工作区。
func TestLookupExplorationNodesCrossTaskWeightedNoMatch(t *testing.T) {
	ctx, store, wsID, explorationID := newExplorationFixture(t)
	base := time.UnixMilli(1_700_000_000_000)
	appendLookupNodes(t, store, explorationID, base,
		lookupNodeSpec{target: "PlanInput", offset: -time.Hour},
	)

	got, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: "zzz-not-recorded"})
	if err != nil {
		t.Fatalf("lookup no match: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("no-match lookup = %v, want empty", lookupNodeTargets(got))
	}

	// LIKE 通配符按字面量处理：'%' / '_' 不得放大为通配。
	for _, wildcard := range []string{"Plan%", "Plan_"} {
		got, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: wildcard})
		if err != nil {
			t.Fatalf("lookup wildcard %q: %v", wildcard, err)
		}
		if len(got) != 0 {
			t.Fatalf("wildcard %q = %v, want empty", wildcard, lookupNodeTargets(got))
		}
	}

	otherWS, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace(other): %v", err)
	}
	got, err = store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: otherWS, Target: "PlanInput"})
	if err != nil {
		t.Fatalf("lookup other workspace: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("other-workspace lookup = %v, want empty", lookupNodeTargets(got))
	}
}

// 同权重排序稳定：last_used_at 优先、created_at 倒序、id 兜底；Limit 取全序前缀。
func TestLookupExplorationNodesCrossTaskWeightedOrderStableAndLimit(t *testing.T) {
	ctx, store, wsID, explorationID := newExplorationFixture(t)
	base := time.UnixMilli(1_700_000_000_000)
	ids := appendLookupNodes(t, store, explorationID, base,
		lookupNodeSpec{target: "Plan", offset: -5 * time.Hour},      // exact（最旧，但权重 0）
		lookupNodeSpec{target: "PlanAlpha", offset: -4 * time.Hour}, // prefix
		lookupNodeSpec{target: "PlanBeta", offset: -3 * time.Hour},  // prefix
		lookupNodeSpec{target: "PlanGamma", offset: -2 * time.Hour}, // prefix
	)
	// 触碰最旧的前缀节点：同权重内最近使用优先。
	if err := store.TouchExplorationNode(ctx, ids["PlanAlpha"], base); err != nil {
		t.Fatalf("touch PlanAlpha: %v", err)
	}

	q := ExplorationNodeQuery{WorkspaceID: wsID, Target: "Plan"}
	first, err := store.LookupExplorationNodes(ctx, q)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	want := []string{"Plan", "PlanAlpha", "PlanGamma", "PlanBeta"}
	if !reflect.DeepEqual(lookupNodeTargets(first), want) {
		t.Fatalf("targets = %v, want %v（权重 → last_used_at → created_at）", lookupNodeTargets(first), want)
	}

	second, err := store.LookupExplorationNodes(ctx, q)
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("lookup order is not stable:\nfirst  = %+v\nsecond = %+v", first, second)
	}

	limited, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: "Plan", Limit: 2})
	if err != nil {
		t.Fatalf("limited lookup: %v", err)
	}
	if !reflect.DeepEqual(lookupNodeTargets(limited), want[:2]) {
		t.Fatalf("limited targets = %v, want %v", lookupNodeTargets(limited), want[:2])
	}
}

// 任务 / 会话路径语义不变：Target 仍是精确过滤（不做 prefix/contains 扩召回）。
func TestLookupExplorationNodesTaskSessionTargetStaysExact(t *testing.T) {
	ctx := context.Background()
	store := openExplorationTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	sessTask, err := store.UpsertExplorationSession(ctx, ExplorationSession{WorkspaceID: wsID, SessionID: "sess-task", TaskID: "task-1"})
	if err != nil {
		t.Fatalf("session task: %v", err)
	}
	sessChat, err := store.UpsertExplorationSession(ctx, ExplorationSession{WorkspaceID: wsID, SessionID: "sess-chat"})
	if err != nil {
		t.Fatalf("session chat: %v", err)
	}
	for _, explorationID := range []string{sessTask, sessChat} {
		if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
			ExplorationID: explorationID, NodeType: NodeTypeSymbol, Target: "PlanInput", KnowledgeVersion: "wv1",
		}); err != nil {
			t.Fatalf("append exact: %v", err)
		}
		if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
			ExplorationID: explorationID, NodeType: NodeTypeSymbol, Target: "PlanInputBuilder", KnowledgeVersion: "wv1",
		}); err != nil {
			t.Fatalf("append prefix: %v", err)
		}
	}

	taskNodes, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, TaskID: "task-1", Target: "PlanInput"})
	if err != nil {
		t.Fatalf("task lookup: %v", err)
	}
	if len(taskNodes) != 1 || taskNodes[0].Target != "PlanInput" {
		t.Fatalf("task lookup = %v, want only exact PlanInput", lookupNodeTargets(taskNodes))
	}

	sessionNodes, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, SessionID: "sess-chat", Target: "PlanInput"})
	if err != nil {
		t.Fatalf("session lookup: %v", err)
	}
	if len(sessionNodes) != 1 || sessionNodes[0].Target != "PlanInput" {
		t.Fatalf("session lookup = %v, want only exact PlanInput", lookupNodeTargets(sessionNodes))
	}

	// 对照：同一个键在跨任务路径是加权检索，exact + prefix 都返回。
	crossNodes, err := store.LookupExplorationNodes(ctx, ExplorationNodeQuery{WorkspaceID: wsID, Target: "PlanInput"})
	if err != nil {
		t.Fatalf("cross lookup: %v", err)
	}
	if len(crossNodes) != 4 {
		t.Fatalf("cross lookup = %v, want 4 weighted hits", lookupNodeTargets(crossNodes))
	}
}
