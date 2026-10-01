package contextpack

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// Phase 6 切片 4：contextpack knowledge.Provider（只读视图）。

type fakeKnowledgePlanner struct {
	calls     int
	lastInput knowledge.PlanInput
	plan      knowledge.Plan
	err       error
}

func (f *fakeKnowledgePlanner) Plan(_ context.Context, in knowledge.PlanInput) (knowledge.Plan, error) {
	f.calls++
	f.lastInput = in
	if f.err != nil {
		return knowledge.Plan{}, f.err
	}
	return f.plan, nil
}

func knowledgeReuseItem(target, version, reason string, confidence float64, verify, provisional bool) knowledge.ReuseItem {
	return knowledge.ReuseItem{
		NodeID:           "en_" + target,
		NodeType:         knowledge.NodeTypeFile,
		Target:           target,
		Summary:          "remembered exploration summary for " + target,
		Confidence:       confidence,
		KnowledgeVersion: version,
		Scope:            knowledge.ReuseScopeTask,
		Verify:           verify,
		Provisional:      provisional,
		Reason:           reason,
	}
}

func knowledgePackInput(prompt string) *Input {
	return &Input{
		Prompt:  prompt,
		TaskID:  "task-1",
		Session: &SessionSnapshot{ID: "session-1"},
	}
}

// 零成本跳过：未装配 / 空查询 / 短查询一律 (nil, nil)，且不调用 Planner。
func TestKnowledgeProviderSkipsWhenInactive(t *testing.T) {
	ctx := context.Background()

	payload, err := NewKnowledgeProvider(nil).Build(ctx, knowledgePackInput("locate the runtime agent loop entry"))
	require.NoError(t, err)
	require.Nil(t, payload)

	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{Reason: knowledge.PlanReasonNoCandidates}}
	provider := NewKnowledgeProvider(planner)
	payload, err = provider.Build(ctx, nil)
	require.NoError(t, err)
	require.Nil(t, payload)
	payload, err = provider.Build(ctx, knowledgePackInput(""))
	require.NoError(t, err)
	require.Nil(t, payload)
	payload, err = provider.Build(ctx, &Input{Prompt: "short", Session: &SessionSnapshot{ID: "s"}})
	require.NoError(t, err)
	require.Nil(t, payload)
	if planner.calls != 0 {
		t.Fatalf("空/短查询不得调用 Planner，calls=%d", planner.calls)
	}

	provider.MinQueryLength = 4
	payload, err = provider.Build(ctx, knowledgePackInput("ok query"))
	require.NoError(t, err)
	require.Nil(t, payload, "无候选时 pack 不得出现 knowledge 键")
	if planner.calls != 1 || planner.lastInput.Write || planner.lastInput.Query != "ok query" {
		t.Fatalf("planner 输入口径不符：calls=%d input=%#v", planner.calls, planner.lastInput)
	}
}

// 只读视图：结构化字段 + 有界 digest data block；dropped 只计数不注入。
func TestKnowledgeProviderBuildsCompiledReadOnlyView(t *testing.T) {
	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{
		Reuse: []knowledge.ReuseItem{
			knowledgeReuseItem("backend/hot.go", "wv1", knowledge.ReuseReasonOK, 0.95, false, false),
			knowledgeReuseItem("backend/warm.go", "wv1", knowledge.ReuseReasonCrossTaskVerify, 0.93, true, false),
			knowledgeReuseItem("backend/stale.go", "wv-old", knowledge.ReuseReasonVersionMismatch, 0.95, false, false),
		},
		Explore: []knowledge.ExploreItem{{Target: "backend/new.go", Reason: knowledge.ReuseReasonVersionUnknown}},
		Reason:  knowledge.PlanReasonOK,
	}}
	provider := NewKnowledgeProvider(planner)

	payload, err := provider.Build(context.Background(), knowledgePackInput("locate the runtime agent loop entry"))
	require.NoError(t, err)
	require.NotNil(t, payload)
	require.Equal(t, "compiled", payload["mode"])
	require.Equal(t, 2, payload["count"])
	require.Equal(t, 0, payload["stale_item_injected"], "stale 条目不得进入视图")

	items, ok := payload["items"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, items, 2)
	require.Equal(t, "hot", items[0]["tier"])
	require.Equal(t, "warm", items[1]["tier"])
	require.Equal(t, "CODE_INTELLIGENCE", items[0]["trust"])
	require.Equal(t, "wv1", items[0]["version"])

	dropped, ok := payload["dropped"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, dropped, 1)
	require.Equal(t, knowledge.CompileReasonStale, dropped[0]["reason"])

	tiers, ok := payload["tiers"].(map[string]int)
	require.True(t, ok)
	require.Equal(t, 1, tiers["hot"])
	require.Equal(t, 1, tiers["warm"])

	digest, ok := payload["digest_block"].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(digest, `<data type="exploration"`), "digest 必须是 data block：%q", digest)
	require.True(t, strings.HasSuffix(digest, "</data>"), "data block 必须闭合：%q", digest)
	require.Contains(t, digest, "hot=1 warm=1")
	require.NotContains(t, digest, "remembered exploration summary", "digest 不得含条目正文")
}

// 降级与错误：Degraded 计划零注入；Planner 错误经 Builder 变成 _warnings。
func TestKnowledgeProviderDegradedAndPlannerError(t *testing.T) {
	ctx := context.Background()

	degraded := &fakeKnowledgePlanner{plan: knowledge.Plan{Degraded: true, Reason: knowledge.PlanReasonStoreTimeout}}
	payload, err := NewKnowledgeProvider(degraded).Build(ctx, knowledgePackInput("locate the runtime agent loop entry"))
	require.NoError(t, err)
	require.Nil(t, payload, "降级计划必须零注入")

	failing := &fakeKnowledgePlanner{err: errors.New("planner exploded")}
	builder := NewBuilder()
	builder.AddProvider(NewKnowledgeProvider(failing))
	pack, warnings := builder.Build(ctx, knowledgePackInput("locate the runtime agent loop entry"))
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "knowledge:")
	require.NotContains(t, pack, "knowledge", "失败时 pack 不得出现 knowledge 键")
}

// 预算与置信度下限由编译器执行（与 contextmgr 同口径）。
func TestKnowledgeProviderBudgetAndFloor(t *testing.T) {
	reuse := make([]knowledge.ReuseItem, 0, 30)
	for index := 0; index < 30; index++ {
		item := knowledgeReuseItem("backend/file-"+strings.Repeat("x", 40)+".go", "wv1", knowledge.ReuseReasonOK, 0.95, false, false)
		item.Summary = strings.Repeat("long exploration summary ", 10)
		reuse = append(reuse, item)
	}
	reuse = append(reuse, knowledgeReuseItem("backend/low.go", "wv1", knowledge.ReuseReasonProvisional, 0.40, true, true))

	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{Reuse: reuse, Reason: knowledge.PlanReasonOK}}
	provider := NewKnowledgeProvider(planner)
	provider.Floor = 0.50

	payload, err := provider.Build(context.Background(), knowledgePackInput("locate the runtime agent loop entry"))
	require.NoError(t, err)
	require.NotNil(t, payload)
	count, ok := payload["count"].(int)
	require.True(t, ok)
	require.Greater(t, count, 0)
	require.Less(t, count, len(reuse), "预算必须截断")

	dropped, ok := payload["dropped"].([]map[string]interface{})
	require.True(t, ok)
	reasons := map[string]bool{}
	for _, item := range dropped {
		reasons[item["reason"].(string)] = true
	}
	require.True(t, reasons[knowledge.CompileReasonBelowFloor], "低置信条目必须按 floor 丢弃：%#v", dropped)
	require.True(t, reasons[knowledge.CompileReasonBudget], "超预算条目必须计数：%#v", dropped)
}

// Reduce 保留完整 digest data block（不截断），且不携带条目明细。
func TestReduceKeepsKnowledgeDigestBlockIntact(t *testing.T) {
	provider := NewKnowledgeProvider(&fakeKnowledgePlanner{plan: knowledge.Plan{
		Reuse:  []knowledge.ReuseItem{knowledgeReuseItem("backend/hot.go", "wv1", knowledge.ReuseReasonOK, 0.95, false, false)},
		Reason: knowledge.PlanReasonOK,
	}})
	payload, err := provider.Build(context.Background(), knowledgePackInput("locate the runtime agent loop entry"))
	require.NoError(t, err)
	require.NotNil(t, payload)

	reduced := Reduce(map[string]interface{}{"knowledge": payload})
	require.NotNil(t, reduced)
	knowledgeSummary, ok := reduced["knowledge"].(map[string]interface{})
	require.True(t, ok)
	digest, ok := knowledgeSummary["digest"].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(digest, `<data `) && strings.HasSuffix(digest, "</data>"))
	require.NotContains(t, knowledgeSummary, "items", "reduced 视图不得携带条目明细")
	require.Equal(t, 1, knowledgeSummary["count"])
}
