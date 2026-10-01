package knowledge

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// W4 Planner 的测试：决策矩阵（表驱动）、<0.90 强制验证读取、Degraded
// （store 错误/超时）、同输入可复算、reader/off 层入口与真实 store 集成。

const plannerTestVersion = "wv1_test_current"

func plannerTestNow() time.Time {
	return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
}

func plannerTestInput() PlanInput {
	return PlanInput{
		WorkspaceID: "ws-1",
		TaskID:      "task-1",
		Query:       "locate the runtime agent loop entry",
		Target:      "backend/internal/agent/loop.go",
		Scope:       ReuseScopeTask,
		Current:     VersionObservation{Version: plannerTestVersion, ObservedAt: plannerTestNow()},
		Now:         plannerTestNow(),
		VersionTTL:  DefaultReuseVersionTTL,
	}
}

func plannerTestNode(target string, confidence float64, version string) ExplorationNode {
	return ExplorationNode{
		ID:               "en_" + target,
		ExplorationID:    "es_test",
		NodeType:         NodeTypeFile,
		Target:           target,
		Confidence:       confidence,
		KnowledgeVersion: version,
	}
}

func TestEvaluatePlanDecisionMatrix(t *testing.T) {
	const target = "backend/internal/agent/loop.go"

	reuseItem := func(scope ReuseScope, confidence float64, reason string, verify, provisional bool) ReuseItem {
		return ReuseItem{
			NodeID:           "en_" + target,
			NodeType:         NodeTypeFile,
			Target:           target,
			Confidence:       confidence,
			KnowledgeVersion: plannerTestVersion,
			Scope:            scope,
			Verify:           verify,
			Provisional:      provisional,
			Reason:           reason,
		}
	}
	exploreItem := func(nodeTarget string, reason string) ExploreItem {
		return ExploreItem{Target: nodeTarget, NodeType: NodeTypeFile, Reason: reason}
	}

	tests := []struct {
		name   string
		mutate func(*PlanInput)
		nodes  []ExplorationNode
		want   Plan
	}{
		{
			name:   "query_too_short",
			mutate: func(in *PlanInput) { in.Query = "go" },
			want:   Plan{Reason: PlanReasonQueryTooShort},
		},
		{
			name:   "query_exactly_min_length",
			mutate: func(in *PlanInput) { in.Query = "12345678"; in.Target = "" },
			want:   Plan{Explore: []ExploreItem{{Target: "12345678", Reason: PlanReasonNoCandidates}}, Reason: PlanReasonNoCandidates},
		},
		{
			name: "no_candidates",
			want: Plan{Explore: []ExploreItem{{Target: target, Reason: PlanReasonNoCandidates}}, Reason: PlanReasonNoCandidates},
		},
		{
			name:  "task_reuse_at_floor_080_forces_verify",
			nodes: []ExplorationNode{plannerTestNode(target, 0.80, plannerTestVersion)},
			want: Plan{
				Reuse:  []ReuseItem{reuseItem(ReuseScopeTask, 0.80, ReuseReasonVerifyReadBelow, true, false)},
				Reason: PlanReasonOK,
			},
		},
		{
			name:  "task_reuse_high_confidence_no_verify",
			nodes: []ExplorationNode{plannerTestNode(target, 0.95, plannerTestVersion)},
			want: Plan{
				Reuse:  []ReuseItem{reuseItem(ReuseScopeTask, 0.95, ReuseReasonOK, false, false)},
				Reason: PlanReasonOK,
			},
		},
		{
			name:  "task_reuse_boundary_090_no_verify",
			nodes: []ExplorationNode{plannerTestNode(target, 0.90, plannerTestVersion)},
			want: Plan{
				Reuse:  []ReuseItem{reuseItem(ReuseScopeTask, 0.90, ReuseReasonOK, false, false)},
				Reason: PlanReasonOK,
			},
		},
		{
			name:   "write_reuse_090_forces_verify",
			mutate: func(in *PlanInput) { in.Write = true },
			nodes:  []ExplorationNode{plannerTestNode(target, 0.90, plannerTestVersion)},
			want: Plan{
				Reuse:  []ReuseItem{reuseItem(ReuseScopeTask, 0.90, ReuseReasonWriteVerify, true, false)},
				Reason: PlanReasonOK,
			},
		},
		{
			name:   "write_below_floor_provisional",
			mutate: func(in *PlanInput) { in.Write = true },
			nodes:  []ExplorationNode{plannerTestNode(target, 0.85, plannerTestVersion)},
			want: Plan{
				Reuse:  []ReuseItem{reuseItem(ReuseScopeTask, 0.85, ReuseReasonProvisional, true, true)},
				Reason: PlanReasonOK,
			},
		},
		{
			name:   "cross_task_090_forces_verify",
			mutate: func(in *PlanInput) { in.Scope = ReuseScopeCrossTask },
			nodes:  []ExplorationNode{plannerTestNode(target, 0.90, plannerTestVersion)},
			want: Plan{
				Reuse:  []ReuseItem{reuseItem(ReuseScopeCrossTask, 0.90, ReuseReasonCrossTaskVerify, true, false)},
				Reason: PlanReasonOK,
			},
		},
		{
			name:   "task_scope_without_task_id_falls_back_cross_task",
			mutate: func(in *PlanInput) { in.TaskID = "" },
			nodes:  []ExplorationNode{plannerTestNode(target, 0.85, plannerTestVersion)},
			want: Plan{
				Reuse:  []ReuseItem{reuseItem(ReuseScopeCrossTask, 0.85, ReuseReasonProvisional, true, true)},
				Reason: PlanReasonOK,
			},
		},
		{
			name:  "provisional_band_floor_boundary_050",
			nodes: []ExplorationNode{plannerTestNode(target, 0.50, plannerTestVersion)},
			want: Plan{
				Reuse:  []ReuseItem{reuseItem(ReuseScopeTask, 0.50, ReuseReasonProvisional, true, true)},
				Reason: PlanReasonOK,
			},
		},
		{
			name:  "below_explore_floor_0499",
			nodes: []ExplorationNode{plannerTestNode(target, 0.499, plannerTestVersion)},
			want: Plan{
				Explore: []ExploreItem{exploreItem(target, ReuseReasonBelowExploreFloor)},
				Reason:  ReuseReasonBelowExploreFloor,
			},
		},
		{
			name:  "low_confidence_040_explores",
			nodes: []ExplorationNode{plannerTestNode(target, 0.40, plannerTestVersion)},
			want: Plan{
				Explore: []ExploreItem{exploreItem(target, ReuseReasonBelowExploreFloor)},
				Reason:  ReuseReasonBelowExploreFloor,
			},
		},
		{
			name:  "version_mismatch_explores",
			nodes: []ExplorationNode{plannerTestNode(target, 0.95, "wv1_old")},
			want: Plan{
				Explore: []ExploreItem{exploreItem(target, ReuseReasonVersionMismatch)},
				Reason:  ReuseReasonVersionMismatch,
			},
		},
		{
			name:  "version_unknown_explores",
			nodes: []ExplorationNode{plannerTestNode(target, 0.95, "")},
			want: Plan{
				Explore: []ExploreItem{exploreItem(target, ReuseReasonVersionUnknown)},
				Reason:  ReuseReasonVersionUnknown,
			},
		},
		{
			name:   "observation_lag_forces_verify",
			mutate: func(in *PlanInput) { in.Current.ObservedAt = plannerTestNow().Add(-2 * DefaultReuseVersionTTL) },
			nodes:  []ExplorationNode{plannerTestNode(target, 0.95, plannerTestVersion)},
			want: Plan{
				Reuse:  []ReuseItem{reuseItem(ReuseScopeTask, 0.95, ReuseReasonVersionObservationLag, true, false)},
				Reason: PlanReasonOK,
			},
		},
		{
			name: "mixed_candidates_reuse_and_explore",
			nodes: []ExplorationNode{
				plannerTestNode("a.go", 0.95, "wv1_old"),
				plannerTestNode("b.go", 0.95, plannerTestVersion),
			},
			want: Plan{
				Reuse: []ReuseItem{{
					NodeID: "en_b.go", NodeType: NodeTypeFile, Target: "b.go",
					Confidence: 0.95, KnowledgeVersion: plannerTestVersion,
					Scope: ReuseScopeTask, Reason: ReuseReasonOK,
				}},
				Explore: []ExploreItem{exploreItem("a.go", ReuseReasonVersionMismatch)},
				Reason:  PlanReasonOK,
			},
		},
		{
			name: "same_target_dedupes_to_strictest_reason",
			nodes: []ExplorationNode{
				plannerTestNode("a.go", 0.95, "wv1_old"),
				plannerTestNode("a.go", 0.40, plannerTestVersion),
			},
			want: Plan{
				Explore: []ExploreItem{exploreItem("a.go", ReuseReasonVersionMismatch)},
				Reason:  ReuseReasonVersionMismatch,
			},
		},
		{
			name:   "short_query_skips_even_with_candidates",
			mutate: func(in *PlanInput) { in.Query = "ls" },
			nodes:  []ExplorationNode{plannerTestNode(target, 0.95, plannerTestVersion)},
			want:   Plan{Reason: PlanReasonQueryTooShort},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := plannerTestInput()
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			got := EvaluatePlan(in, tc.nodes, DefaultPlannerConfig())
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("EvaluatePlan() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEvaluatePlanDeterministic(t *testing.T) {
	in := plannerTestInput()
	nodes := []ExplorationNode{
		plannerTestNode("c.go", 0.95, plannerTestVersion),
		plannerTestNode("a.go", 0.95, "wv1_old"),
		plannerTestNode("b.go", 0.70, plannerTestVersion),
	}
	first := EvaluatePlan(in, nodes, DefaultPlannerConfig())
	second := EvaluatePlan(in, nodes, DefaultPlannerConfig())
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same input produced different plans:\n%+v\n%+v", first, second)
	}
	reversed := []ExplorationNode{nodes[2], nodes[1], nodes[0]}
	third := EvaluatePlan(in, reversed, DefaultPlannerConfig())
	if !reflect.DeepEqual(first, third) {
		t.Fatalf("candidate order changed the plan:\n%+v\n%+v", first, third)
	}
	if nodes[0].Target != "c.go" || nodes[2].Target != "b.go" {
		t.Fatalf("EvaluatePlan mutated the caller slice: %+v", nodes)
	}
}

// fakePlanReader 是 ExplorationNodeReader 的可编程替身（含调用计数与超时阻塞）。
type fakePlanReader struct {
	nodes     []ExplorationNode
	err       error
	block     bool
	calls     int
	lastQuery ExplorationNodeQuery
	queries   []ExplorationNodeQuery
}

func (f *fakePlanReader) LookupExplorationNodes(ctx context.Context, q ExplorationNodeQuery) ([]ExplorationNode, error) {
	f.calls++
	f.lastQuery = q
	f.queries = append(f.queries, q)
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.nodes, f.err
}

// 会话级工作集路径（2026-09-30 修复）：宿主无任务锚点时 TaskID 为空、
// SessionID 圈定工作集，不得把自然语言查询当精确 target（那会零命中）。
func TestPlannerSessionWorkSetLookup(t *testing.T) {
	const target = "backend/internal/agent/loop.go"
	reader := &fakePlanReader{nodes: []ExplorationNode{plannerTestNode(target, 1.0, plannerTestVersion)}}
	planner := NewPlanner(PlannerOptions{Reader: reader, Now: plannerTestNow, VersionTTL: DefaultReuseVersionTTL})

	// 1) 会话级：TaskID 为空、SessionID 非空 → 会话工作集，Target 不参与过滤。
	in := plannerTestInput()
	in.TaskID = ""
	in.Target = ""
	in.SessionID = "sess-chat"
	plan, err := planner.Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan(session): %v", err)
	}
	if reader.lastQuery.TaskID != "" || reader.lastQuery.SessionID != "sess-chat" || reader.lastQuery.Target != "" {
		t.Fatalf("lookup query = %+v, want session work set", reader.lastQuery)
	}
	if len(plan.Reuse) != 1 || plan.Reuse[0].Target != target {
		t.Fatalf("plan = %+v, want single reuse item for %s", plan, target)
	}

	// 2) 任务优先：真实任务锚点存在时不走会话路径。
	reader.lastQuery = ExplorationNodeQuery{}
	in.TaskID = "task-1"
	in.SessionID = "sess-chat"
	if _, err := planner.Plan(context.Background(), in); err != nil {
		t.Fatalf("Plan(task): %v", err)
	}
	if reader.lastQuery.TaskID != "task-1" || reader.lastQuery.SessionID != "" {
		t.Fatalf("lookup query = %+v, want task work set", reader.lastQuery)
	}

	// 3) 无锚点：跨任务把 target 作为加权检索键交给 store（planner 侧键不变）。
	reader.lastQuery = ExplorationNodeQuery{}
	in.TaskID = ""
	in.SessionID = ""
	in.Scope = ReuseScopeCrossTask
	in.Target = target
	if _, err := planner.Plan(context.Background(), in); err != nil {
		t.Fatalf("Plan(cross): %v", err)
	}
	if reader.lastQuery.Target != target || reader.lastQuery.TaskID != "" || reader.lastQuery.SessionID != "" {
		t.Fatalf("lookup query = %+v, want cross-task target lookup", reader.lastQuery)
	}
}

// 跨任务加权检索端到端（§6.3 登记项）：真实 store 下，限定名查询命中探索记忆，
// 不再因为"整串查询当精确 target"而 no_candidates。
func TestPlannerCrossTaskWeightedLookupFindsQualifiedName(t *testing.T) {
	ctx := context.Background()
	store := openExplorationTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	sess, err := store.UpsertExplorationSession(ctx, ExplorationSession{WorkspaceID: wsID, SessionID: "sess-chat"})
	if err != nil {
		t.Fatalf("UpsertExplorationSession: %v", err)
	}
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: sess, NodeType: NodeTypeSymbol, Target: "knowledge.Plan",
		Confidence: 0.95, KnowledgeVersion: plannerTestVersion,
	}); err != nil {
		t.Fatalf("AppendExplorationNode: %v", err)
	}

	planner := NewPlanner(PlannerOptions{Reader: store, Now: plannerTestNow, VersionTTL: DefaultReuseVersionTTL})
	in := plannerTestInput()
	in.WorkspaceID = wsID
	in.TaskID = ""
	in.SessionID = ""
	in.Scope = ReuseScopeCrossTask
	in.Target = ""
	in.Query = "knowledge.Plan"
	in.Current = VersionObservation{Version: plannerTestVersion, ObservedAt: plannerTestNow()}
	in.Now = plannerTestNow()

	plan, err := planner.Plan(ctx, in)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Degraded || plan.Reason != PlanReasonOK || len(plan.Reuse) != 1 {
		t.Fatalf("plan = %+v, want one reusable qualified-name node", plan)
	}
	if plan.Reuse[0].Target != "knowledge.Plan" || !plan.Reuse[0].Verify {
		t.Fatalf("reuse = %+v, want knowledge.Plan with cross-task verify", plan.Reuse[0])
	}

	// 检索键无命中时仍是 no_candidates（不降级、不报错）。
	in.Query = "zzz-not-recorded-anywhere"
	plan, err = planner.Plan(ctx, in)
	if err != nil {
		t.Fatalf("Plan(no match): %v", err)
	}
	if plan.Degraded || plan.Reason != PlanReasonNoCandidates {
		t.Fatalf("plan(no match) = %+v, want no_candidates", plan)
	}
}

func assertDegradedPlan(t *testing.T, plan Plan, err error, reason string) {
	t.Helper()
	if err != nil {
		t.Fatalf("Plan returned error %v, want Degraded instead", err)
	}
	if !plan.Degraded || plan.Reason != reason {
		t.Fatalf("plan = %+v, want Degraded with reason %q", plan, reason)
	}
	if plan.Reuse != nil || plan.Explore != nil {
		t.Fatalf("degraded plan must not carry items: %+v", plan)
	}
}

func TestPlannerNilReaderDegrades(t *testing.T) {
	plan, err := NewPlanner(PlannerOptions{}).Plan(context.Background(), plannerTestInput())
	assertDegradedPlan(t, plan, err, PlanReasonStoreUnavailable)
}

func TestPlannerStoreErrorDegrades(t *testing.T) {
	reader := &fakePlanReader{err: errors.New("store exploded")}
	plan, err := NewPlanner(PlannerOptions{Reader: reader}).Plan(context.Background(), plannerTestInput())
	assertDegradedPlan(t, plan, err, PlanReasonStoreUnavailable)
	if reader.calls != 1 {
		t.Fatalf("reader calls = %d, want 1", reader.calls)
	}
}

func TestPlannerStoreTimeoutDegrades(t *testing.T) {
	reader := &fakePlanReader{block: true}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	plan, err := NewPlanner(PlannerOptions{Reader: reader}).Plan(ctx, plannerTestInput())
	assertDegradedPlan(t, plan, err, PlanReasonStoreTimeout)
}

func TestPlannerInvalidInputDegrades(t *testing.T) {
	reader := &fakePlanReader{}
	in := plannerTestInput()
	in.WorkspaceID = "  "
	plan, err := NewPlanner(PlannerOptions{Reader: reader}).Plan(context.Background(), in)
	assertDegradedPlan(t, plan, err, PlanReasonInvalidInput)
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0 for invalid input", reader.calls)
	}
}

func TestPlannerQueryTooShortSkipsStore(t *testing.T) {
	reader := &fakePlanReader{nodes: []ExplorationNode{plannerTestNode("a.go", 0.95, plannerTestVersion)}}
	in := plannerTestInput()
	in.Query = "go"
	plan, err := NewPlanner(PlannerOptions{Reader: reader}).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Reason != PlanReasonQueryTooShort || plan.Degraded {
		t.Fatalf("plan = %+v, want query_too_short without degrade", plan)
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0 for short query", reader.calls)
	}
}

func TestPlannerLookupShape(t *testing.T) {
	ctx := context.Background()
	reader := &fakePlanReader{}
	planner := NewPlanner(PlannerOptions{Reader: reader})

	in := plannerTestInput()
	if _, err := planner.Plan(ctx, in); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// 新语义（per-workspace 复用）：任务工作集无候选时追加一次跨任务回退查询；
	// 首次查询形状保持不变。
	if reader.calls != 2 {
		t.Fatalf("reader calls = %d, want task lookup + cross-task fallback", reader.calls)
	}
	if first := reader.queries[0]; first.WorkspaceID != "ws-1" || first.TaskID != "task-1" || first.SessionID != "" || first.Target != "" {
		t.Fatalf("task-scope lookup = %+v, want workspace/task work set without target filter", first)
	}
	if fallback := reader.queries[1]; fallback.TaskID != "" || fallback.SessionID != "" || fallback.Target != in.Target {
		t.Fatalf("cross-task fallback lookup = %+v, want anchor-free target lookup", fallback)
	}

	cross := plannerTestInput()
	cross.Scope = ReuseScopeCrossTask
	cross.Target = ""
	if _, err := planner.Plan(ctx, cross); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if reader.lastQuery.TaskID != "" || reader.lastQuery.Target != cross.Query {
		t.Fatalf("cross-task lookup = %+v, want no task filter and query as target", reader.lastQuery)
	}
}

func TestPlannerInjectedClockDrivesLag(t *testing.T) {
	fixedNow := plannerTestNow()
	reader := &fakePlanReader{nodes: []ExplorationNode{plannerTestNode("a.go", 0.95, plannerTestVersion)}}
	planner := NewPlanner(PlannerOptions{
		Reader: reader,
		Now:    func() time.Time { return fixedNow },
	})
	in := plannerTestInput()
	in.Target = "a.go"
	in.Now = time.Time{} // 由注入时钟补齐
	in.Current.ObservedAt = fixedNow.Add(-2 * DefaultReuseVersionTTL)

	plan, err := planner.Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Reuse) != 1 || !plan.Reuse[0].Verify || plan.Reuse[0].Reason != ReuseReasonVersionObservationLag {
		t.Fatalf("plan = %+v, want lagging observation to force verify", plan)
	}
}

func TestPlannerReuseFromSQLiteStore(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	sessID, err := store.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: wsID, SessionID: "sess-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatalf("UpsertExplorationSession: %v", err)
	}
	version, err := WorkspaceVersion(ctx, store, wsID)
	if err != nil {
		t.Fatalf("WorkspaceVersion: %v", err)
	}
	const target = "backend/internal/agent/loop.go"
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: sessID, NodeType: NodeTypeFile, Target: target,
		Confidence: 0.96, KnowledgeVersion: version,
	}); err != nil {
		t.Fatalf("AppendExplorationNode: %v", err)
	}

	plan, err := NewPlanner(PlannerOptions{Reader: store}).Plan(ctx, PlanInput{
		WorkspaceID: wsID, TaskID: "task-1",
		Query: target, Target: target, Scope: ReuseScopeTask,
		Current: VersionObservation{Version: version},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Degraded || len(plan.Reuse) != 1 || len(plan.Explore) != 0 {
		t.Fatalf("plan = %+v, want one reuse item from sqlite store", plan)
	}
	item := plan.Reuse[0]
	if item.Target != target || item.Confidence != 0.96 || item.KnowledgeVersion != version {
		t.Fatalf("reuse item = %+v, want target/confidence/version carried through", item)
	}
	if item.Verify || item.Provisional || item.Reason != ReuseReasonOK {
		t.Fatalf("reuse item = %+v, want direct reuse without verify", item)
	}
}

func assertDisabledPlan(t *testing.T, plan Plan, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Plan returned error %v, want empty disabled plan", err)
	}
	if plan.Degraded || plan.Reason != PlanReasonDisabled || plan.Reuse != nil || plan.Explore != nil {
		t.Fatalf("plan = %+v, want empty disabled plan", plan)
	}
}

func TestLayerPlanNilAndOff(t *testing.T) {
	ctx := context.Background()
	in := PlanInput{Query: "locate the runtime agent loop entry"}

	var nilLayer *Layer
	plan, err := nilLayer.Plan(ctx, in)
	assertDisabledPlan(t, plan, err)

	off := &Layer{cfg: DefaultConfig()}
	plan, err = off.Plan(ctx, in)
	assertDisabledPlan(t, plan, err)

	cfg := DefaultConfig()
	cfg.Mode = ModeShadow
	shadowWithoutStore := &Layer{cfg: cfg}
	plan, err = shadowWithoutStore.Plan(ctx, in)
	assertDisabledPlan(t, plan, err)
}

func TestLayerPlanReaderIndexUnavailableThenReuse(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	root := t.TempDir()
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Mode = ModeShadow
	layer := &Layer{cfg: cfg, role: RoleReader, store: store}

	// reader 且 workspace 尚未登记（owner 还没索引过）→ 索引不可用降级。
	plan, err := layer.Plan(ctx, PlanInput{Query: "locate the runtime agent loop entry"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.Degraded || plan.Reason != PlanReasonIndexUnavailable {
		t.Fatalf("plan = %+v, want index_unavailable degrade", plan)
	}

	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	sessID, err := store.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: wsID, SessionID: "sess-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatalf("UpsertExplorationSession: %v", err)
	}
	version, err := WorkspaceVersion(ctx, store, wsID)
	if err != nil {
		t.Fatalf("WorkspaceVersion: %v", err)
	}
	const target = "pkg/main.go"
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: sessID, NodeType: NodeTypeFile, Target: target,
		Confidence: 0.95, KnowledgeVersion: version,
	}); err != nil {
		t.Fatalf("AppendExplorationNode: %v", err)
	}

	// Current 留空：Layer 按 TTL 采样 WorkspaceVersion 后仍可复用。
	plan, err = layer.Plan(ctx, PlanInput{
		Query: target, Target: target, TaskID: "task-1", Scope: ReuseScopeTask,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Degraded || len(plan.Reuse) != 1 {
		t.Fatalf("plan = %+v, want one reuse item via sampled version", plan)
	}
	if plan.Reuse[0].KnowledgeVersion != version || plan.Reuse[0].Verify || plan.Reuse[0].Reason != ReuseReasonOK {
		t.Fatalf("reuse item = %+v, want direct reuse on matching sampled version", plan.Reuse[0])
	}
}

func TestVersionCacheTTL(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	t0 := plannerTestNow()
	var cache versionCache

	first, err := cache.observe(ctx, store, wsID, time.Minute, t0, 0)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	second, err := cache.observe(ctx, store, wsID, time.Minute, t0.Add(10*time.Second), 0)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if second.Version != first.Version || !second.ObservedAt.Equal(t0) {
		t.Fatalf("cached observation = %+v, want version %q observed at %v", second, first.Version, t0)
	}

	third, err := cache.observe(ctx, store, wsID, time.Minute, t0.Add(2*time.Minute), 0)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if third.Version != first.Version || !third.ObservedAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("expired observation = %+v, want refreshed ObservedAt with same version", third)
	}
}
