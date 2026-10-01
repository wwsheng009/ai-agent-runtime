package knowledge

import (
	"context"
	"errors"
	"testing"
)

// scopedPlanReader：会话/任务查询返回 sessionNodes，跨任务查询返回 crossNodes；
// failFrom > 0 时从该次调用起返回错误（用于回退失败路径）。
type scopedPlanReader struct {
	sessionNodes []ExplorationNode
	crossNodes   []ExplorationNode
	failFrom     int
	calls        []ExplorationNodeQuery
}

func (f *scopedPlanReader) LookupExplorationNodes(_ context.Context, q ExplorationNodeQuery) ([]ExplorationNode, error) {
	f.calls = append(f.calls, q)
	if f.failFrom > 0 && len(f.calls) > f.failFrom {
		return nil, errors.New("boom")
	}
	if q.TaskID != "" || q.SessionID != "" {
		return f.sessionNodes, nil
	}
	return f.crossNodes, nil
}

// 会话工作集为空时回退到跨任务（per-workspace）检索：冷会话对既有探索记忆
// 不再零召回；跨任务命中必须带强制验证与更保守的 reason（04 §4.4 / C8）。
func TestPlannerFallsBackToCrossTaskWhenSessionWorkspaceEmpty(t *testing.T) {
	target := "backend/internal/knowledge/planner.go"
	reader := &scopedPlanReader{crossNodes: []ExplorationNode{plannerTestNode(target, 0.95, plannerTestVersion)}}
	planner := NewPlanner(PlannerOptions{Reader: reader, Now: plannerTestNow, VersionTTL: DefaultReuseVersionTTL})

	in := plannerTestInput()
	in.TaskID = ""
	in.SessionID = "sess-fresh" // 冷会话：本会话无探索记忆
	in.Target = ""              // 跨任务检索键退回 Query
	in.Query = target

	plan, err := planner.Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(reader.calls) != 2 {
		t.Fatalf("lookup calls = %d, want session lookup + cross-task fallback", len(reader.calls))
	}
	if reader.calls[0].SessionID != "sess-fresh" || reader.calls[0].Target != "" {
		t.Fatalf("first lookup = %+v, want session work set", reader.calls[0])
	}
	if reader.calls[1].SessionID != "" || reader.calls[1].TaskID != "" || reader.calls[1].Target != target {
		t.Fatalf("fallback lookup = %+v, want cross-task target lookup", reader.calls[1])
	}
	if len(plan.Reuse) != 1 {
		t.Fatalf("plan = %+v, want 1 cross-task reuse item", plan)
	}
	item := plan.Reuse[0]
	if item.Target != target || item.Scope != ReuseScopeCrossTask || !item.Verify || item.Reason != ReuseReasonCrossTaskVerify {
		t.Fatalf("reuse item = %+v, want cross_task_verify + verify", item)
	}
}

// 会话工作集有可复用项时不触发回退（既有语义不变，且不多打一次 store）。
func TestPlannerSessionHitSkipsCrossTaskFallback(t *testing.T) {
	target := "backend/internal/knowledge/compiler.go"
	reader := &scopedPlanReader{
		sessionNodes: []ExplorationNode{plannerTestNode(target, 0.95, plannerTestVersion)},
		crossNodes:   []ExplorationNode{plannerTestNode("other.go", 0.99, plannerTestVersion)},
	}
	planner := NewPlanner(PlannerOptions{Reader: reader, Now: plannerTestNow, VersionTTL: DefaultReuseVersionTTL})

	in := plannerTestInput()
	in.TaskID = ""
	in.SessionID = "sess-warm"
	plan, err := planner.Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(reader.calls) != 1 {
		t.Fatalf("lookup calls = %d, want session hit without fallback", len(reader.calls))
	}
	if len(plan.Reuse) != 1 || plan.Reuse[0].Target != target || plan.Reuse[0].Scope != ReuseScopeTask {
		t.Fatalf("plan = %+v, want session-scope reuse for %s", plan, target)
	}
}

// 低于硬下限（0.40 < explore_below=0.50）：回退不产出复用，保留原判定。
func TestPlannerFallbackRespectsCrossTaskFloor(t *testing.T) {
	reader := &scopedPlanReader{crossNodes: []ExplorationNode{plannerTestNode("pkg/x.go", 0.40, plannerTestVersion)}}
	planner := NewPlanner(PlannerOptions{Reader: reader, Now: plannerTestNow, VersionTTL: DefaultReuseVersionTTL})

	in := plannerTestInput()
	in.TaskID = ""
	in.SessionID = "sess-fresh"
	in.Target = ""
	in.Query = "pkg/x.go"

	plan, err := planner.Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(reader.calls) != 2 {
		t.Fatalf("lookup calls = %d, want fallback attempted", len(reader.calls))
	}
	if len(plan.Reuse) != 0 || plan.Reason != PlanReasonNoCandidates {
		t.Fatalf("plan = %+v, want original no_candidates plan preserved", plan)
	}
}

// 回退查询失败：保留原判定，不冒泡错误（Degrade-Not-Fail）。
func TestPlannerFallbackDegradesQuietly(t *testing.T) {
	reader := &scopedPlanReader{failFrom: 1} // 首次（会话）成功且为空，回退调用失败
	planner := NewPlanner(PlannerOptions{Reader: reader, Now: plannerTestNow, VersionTTL: DefaultReuseVersionTTL})

	in := plannerTestInput()
	in.TaskID = ""
	in.SessionID = "sess-fresh"
	plan, err := planner.Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan must not bubble fallback errors, got %v", err)
	}
	if plan.Degraded || plan.Reason != PlanReasonNoCandidates {
		t.Fatalf("plan = %+v, want quiet original plan", plan)
	}
}

// 真实 store 端到端：旧会话写入的探索记忆，被**新会话**按 per-workspace
// 复用路径召回（此前冷会话恒零召回）。
func TestPlannerFreshSessionReusesWorkspaceMemoryEndToEnd(t *testing.T) {
	ctx := context.Background()
	store := openExplorationTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	oldSession, err := store.UpsertExplorationSession(ctx, ExplorationSession{WorkspaceID: wsID, SessionID: "sess-old"})
	if err != nil {
		t.Fatalf("UpsertExplorationSession: %v", err)
	}
	if _, err := store.AppendExplorationNode(ctx, ExplorationNode{
		ExplorationID: oldSession, NodeType: NodeTypeSymbol, Target: "knowledge.Plan",
		Confidence: 0.95, KnowledgeVersion: plannerTestVersion,
	}); err != nil {
		t.Fatalf("AppendExplorationNode: %v", err)
	}

	planner := NewPlanner(PlannerOptions{Reader: store, Now: plannerTestNow, VersionTTL: DefaultReuseVersionTTL})
	in := plannerTestInput()
	in.WorkspaceID = wsID
	in.TaskID = ""
	in.SessionID = "sess-new" // 全新会话
	in.Target = ""
	in.Query = "knowledge.Plan"
	in.Current = VersionObservation{Version: plannerTestVersion, ObservedAt: plannerTestNow()}
	in.Now = plannerTestNow()

	plan, err := planner.Plan(ctx, in)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Reuse) != 1 || plan.Reuse[0].Target != "knowledge.Plan" {
		t.Fatalf("plan = %+v, want workspace-scope reuse for knowledge.Plan", plan)
	}
	if !plan.Reuse[0].Verify || plan.Reuse[0].Reason != ReuseReasonCrossTaskVerify {
		t.Fatalf("reuse item = %+v, want cross_task_verify + verify", plan.Reuse[0])
	}
}
