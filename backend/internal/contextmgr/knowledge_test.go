package contextmgr

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// W5 contextmgr 知识层注入测试：off 零调用/基线等价、signals/broad 形状、
// Verify/Provisional 标注与验证读取安排、Degraded 回退、可逆性、二次
// stale/version 过滤、ReuseConfidenceFloor 与最小查询长度、预算截断。

// 编译期钉齐：W4 的 Layer 可直接注入 Manager.Knowledge（无需适配器）。
var _ knowledge.Planner = (*knowledge.Layer)(nil)

type fakeKnowledgePlanner struct {
	calls     int
	lastInput knowledge.PlanInput
	plan      knowledge.Plan
	err       error
}

func (f *fakeKnowledgePlanner) Plan(ctx context.Context, in knowledge.PlanInput) (knowledge.Plan, error) {
	f.calls++
	f.lastInput = in
	if f.err != nil {
		return knowledge.Plan{}, f.err
	}
	return f.plan, nil
}

type recordingEventPublisher struct {
	events []runtimeevents.Event
}

func (p *recordingEventPublisher) Publish(event runtimeevents.Event) {
	p.events = append(p.events, event)
}

func (p *recordingEventPublisher) types() []string {
	types := make([]string, 0, len(p.events))
	for _, event := range p.events {
		types = append(types, event.Type)
	}
	return types
}

func knowledgeTestBuildInput(goal string) BuildInput {
	return BuildInput{
		TraceID:     "trace-knowledge",
		WorkspaceID: "ws-1",
		SessionID:   "session-knowledge",
		TaskID:      "task-1",
		Goal:        goal,
		History: []types.Message{
			*types.NewSystemMessage("system prompt"),
			*types.NewUserMessage(goal),
		},
	}
}

func newKnowledgeTestManager(planner knowledge.Planner, mode string) *Manager {
	manager := NewManager(DefaultBudget(), nil)
	manager.Knowledge = planner
	manager.Strategy.KnowledgeMode = mode
	return manager
}

func knowledgeTestReuseItem(target, version, reason string, confidence float64, verify, provisional bool) knowledge.ReuseItem {
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

func knowledgeMessageFrom(result BuildResult) *types.Message {
	for index := range result.Messages {
		if result.Messages[index].Metadata.GetString("context_stage", "") == knowledgeStage {
			return &result.Messages[index]
		}
	}
	return nil
}

// 接线口径（2026-09-30 修复）：agent 循环的会话级回退（TaskID == SessionID）
// 不是任务锚点——必须折叠为 task_id 为空 + SessionID，走会话级工作集；
// 真实任务锚点保持任务作用域。
func TestKnowledgePlannerWiringTaskAndSessionScope(t *testing.T) {
	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{Reason: knowledge.PlanReasonNoCandidates}}
	manager := newKnowledgeTestManager(planner, KnowledgeModeSignals)

	input := knowledgeTestBuildInput("locate the runtime agent loop entry")
	input.TaskID = input.SessionID // loop.go 的会话级回退
	_ = manager.Build(context.Background(), input)
	if planner.lastInput.TaskID != "" || planner.lastInput.SessionID != input.SessionID {
		t.Fatalf("plan input = %+v, want session work set", planner.lastInput)
	}
	if planner.lastInput.Scope != knowledge.ReuseScopeTask {
		t.Fatalf("scope = %q, want task (session work set)", planner.lastInput.Scope)
	}

	planner.lastInput = knowledge.PlanInput{}
	input.TaskID = "task-1"
	_ = manager.Build(context.Background(), input)
	if planner.lastInput.TaskID != "task-1" || planner.lastInput.SessionID != input.SessionID {
		t.Fatalf("plan input = %+v, want task scope", planner.lastInput)
	}
	if planner.lastInput.Scope != knowledge.ReuseScopeTask {
		t.Fatalf("scope = %q, want task", planner.lastInput.Scope)
	}
}

func TestKnowledgeOffZeroCallsAndBaseline(t *testing.T) {
	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{
		Reuse:  []knowledge.ReuseItem{knowledgeTestReuseItem("a.go", "wv1", knowledge.ReuseReasonOK, 0.95, false, false)},
		Reason: knowledge.PlanReasonOK,
	}}
	input := knowledgeTestBuildInput("locate the runtime agent loop entry")

	for _, mode := range []string{"", KnowledgeModeOff, "disabled", "unknown-mode"} {
		manager := newKnowledgeTestManager(planner, mode)
		result := manager.Build(context.Background(), input)
		if planner.calls != 0 {
			t.Fatalf("mode=%q: expected zero planner calls, got %d", mode, planner.calls)
		}
		if message := knowledgeMessageFrom(result); message != nil {
			t.Fatalf("mode=%q: expected zero injection, got message %q", mode, message.Content)
		}
		baseline := NewManager(DefaultBudget(), nil).Build(context.Background(), input)
		if !reflect.DeepEqual(result.Messages, baseline.Messages) {
			t.Fatalf("mode=%q: messages differ from baseline", mode)
		}
		if !reflect.DeepEqual(result.Metadata, baseline.Metadata) {
			t.Fatalf("mode=%q: metadata differs from baseline: %#v", mode, result.Metadata)
		}
		if _, exists := result.Metadata["knowledge_mode"]; exists {
			t.Fatalf("mode=%q: off must not add knowledge metadata keys", mode)
		}
	}
}

func TestKnowledgeSignalsInjectionShape(t *testing.T) {
	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{
		Reuse: []knowledge.ReuseItem{
			knowledgeTestReuseItem("backend/a.go", "wv1", knowledge.ReuseReasonOK, 0.95, false, false),
			knowledgeTestReuseItem("backend/b.go", "wv1", knowledge.ReuseReasonProvisional, 0.72, true, true),
		},
		Explore: []knowledge.ExploreItem{{Target: "backend/c.go", Reason: knowledge.ReuseReasonVersionUnknown}},
		Reason:  knowledge.PlanReasonOK,
	}}
	manager := newKnowledgeTestManager(planner, KnowledgeModeSignals)
	manager.Strategy.MinKnowledgeQueryLength = 6
	input := knowledgeTestBuildInput("locate the runtime agent loop entry")
	input.KnowledgeWrite = true

	result := manager.Build(context.Background(), input)
	if planner.calls != 1 {
		t.Fatalf("expected exactly one planner call, got %d", planner.calls)
	}
	if planner.lastInput.WorkspaceID != "ws-1" || planner.lastInput.TaskID != "task-1" {
		t.Fatalf("planner input lost scope: %#v", planner.lastInput)
	}
	if planner.lastInput.Query != input.Goal || planner.lastInput.Scope != knowledge.ReuseScopeTask {
		t.Fatalf("planner input scope/query mismatch: %#v", planner.lastInput)
	}
	if planner.lastInput.MinQueryLength != 6 || !planner.lastInput.Write {
		t.Fatalf("planner input pass-through mismatch: %#v", planner.lastInput)
	}

	message := knowledgeMessageFrom(result)
	if message == nil {
		t.Fatal("expected signals injection message")
	}
	if message.Role != "assistant" {
		t.Fatalf("unexpected role %q", message.Role)
	}
	if !strings.Contains(message.Content, "signals") || !strings.Contains(message.Content, "verify_required=1") {
		t.Fatalf("signals content missing summary signal: %q", message.Content)
	}
	if strings.Contains(message.Content, "confidence=") {
		t.Fatalf("signals must not inject item detail: %q", message.Content)
	}
	if result.Metadata["knowledge_mode"] != KnowledgeModeSignals ||
		result.Metadata["knowledge_injected"] != true ||
		result.Metadata["knowledge_count"] != 2 ||
		result.Metadata["knowledge_reuse_count"] != 2 ||
		result.Metadata["knowledge_explore_count"] != 1 {
		t.Fatalf("unexpected knowledge metadata: %#v", result.Metadata)
	}
	if result.Metadata["knowledge_provisional_count"] != 1 {
		t.Fatalf("expected one provisional item, metadata=%#v", result.Metadata)
	}
	verifyTargets, ok := result.Metadata["knowledge_verify_targets"].([]map[string]interface{})
	if !ok || len(verifyTargets) != 1 || verifyTargets[0]["target"] != "backend/b.go" {
		t.Fatalf("expected verify target backend/b.go, got %#v", result.Metadata["knowledge_verify_targets"])
	}
	if verifyTargets[0]["reason"] != knowledge.ReuseReasonProvisional {
		t.Fatalf("verify target must keep gate reason, got %#v", verifyTargets[0])
	}

	items, ok := message.Metadata["knowledge_items"].([]map[string]interface{})
	if !ok || len(items) != 2 {
		t.Fatalf("expected two item metadata entries, got %#v", message.Metadata["knowledge_items"])
	}
	if items[0]["item_type"] != "exploration" || items[0]["source"] != "memory" ||
		items[0]["knowledge_version"] != "wv1" || items[0]["reason"] != knowledge.ReuseReasonOK {
		t.Fatalf("unexpected item metadata: %#v", items[0])
	}
	if items[1]["verify"] != true || items[1]["provisional"] != true ||
		items[1]["reason"] != knowledge.ReuseReasonProvisional {
		t.Fatalf("verify/provisional markers missing: %#v", items[1])
	}
}

func TestKnowledgeBroadInjectionShapeAndEvents(t *testing.T) {
	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{
		Reuse: []knowledge.ReuseItem{
			knowledgeTestReuseItem("backend/a.go", "wv1", knowledge.ReuseReasonOK, 0.95, false, false),
			knowledgeTestReuseItem("backend/b.go", "wv1", knowledge.ReuseReasonCrossTaskVerify, 0.93, true, false),
		},
		Reason: knowledge.PlanReasonOK,
	}}
	manager := newKnowledgeTestManager(planner, KnowledgeModeBroad)
	publisher := &recordingEventPublisher{}
	manager.Events = publisher

	result := manager.Build(context.Background(), knowledgeTestBuildInput("locate the runtime agent loop entry"))
	message := knowledgeMessageFrom(result)
	if message == nil {
		t.Fatal("expected broad injection message")
	}
	for _, fragment := range []string{"- [exploration]", "target=backend/a.go", "confidence=0.95", "version=wv1", "reason=ok", "item_type=exploration source=memory"} {
		if !strings.Contains(message.Content, fragment) {
			t.Fatalf("broad content missing %q: %q", fragment, message.Content)
		}
	}
	types := publisher.types()
	if !containsString(types, "context.knowledge.injected") || !containsString(types, "context.knowledge.verify_requested") {
		t.Fatalf("expected injected + verify_requested events, got %v", types)
	}
}

func TestKnowledgeDegradedFallsBackToBaseline(t *testing.T) {
	input := knowledgeTestBuildInput("locate the runtime agent loop entry")
	baseline := NewManager(DefaultBudget(), nil).Build(context.Background(), input)

	degraded := &fakeKnowledgePlanner{plan: knowledge.Plan{Degraded: true, Reason: knowledge.PlanReasonStoreTimeout}}
	manager := newKnowledgeTestManager(degraded, KnowledgeModeBroad)
	result := manager.Build(context.Background(), input)
	if degraded.calls != 1 {
		t.Fatalf("expected planner to be called once, got %d", degraded.calls)
	}
	if !reflect.DeepEqual(result.Messages, baseline.Messages) {
		t.Fatal("degraded plan must fall back to baseline messages")
	}
	if result.Metadata["knowledge_degraded"] != true || result.Metadata["knowledge_injected"] != false {
		t.Fatalf("unexpected degraded metadata: %#v", result.Metadata)
	}
	if result.Metadata["knowledge_reason"] != knowledge.PlanReasonStoreTimeout {
		t.Fatalf("expected degraded reason preserved, got %#v", result.Metadata["knowledge_reason"])
	}

	failing := &fakeKnowledgePlanner{err: errors.New("planner exploded")}
	manager = newKnowledgeTestManager(failing, KnowledgeModeBroad)
	result = manager.Build(context.Background(), input)
	if !reflect.DeepEqual(result.Messages, baseline.Messages) {
		t.Fatal("planner error must fall back to baseline messages")
	}
	if result.Metadata["knowledge_degraded"] != true ||
		result.Metadata["knowledge_reason"] != knowledgeReasonPlannerError ||
		result.Metadata["knowledge_error"] != "planner exploded" {
		t.Fatalf("unexpected planner-error metadata: %#v", result.Metadata)
	}
}

func TestKnowledgeReversibleOnOff(t *testing.T) {
	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{
		Reuse:  []knowledge.ReuseItem{knowledgeTestReuseItem("backend/a.go", "wv1", knowledge.ReuseReasonOK, 0.95, false, false)},
		Reason: knowledge.PlanReasonOK,
	}}
	manager := newKnowledgeTestManager(planner, KnowledgeModeOff)
	input := knowledgeTestBuildInput("locate the runtime agent loop entry")

	offResult := manager.Build(context.Background(), input)
	if planner.calls != 0 || knowledgeMessageFrom(offResult) != nil {
		t.Fatalf("off must be inert: calls=%d", planner.calls)
	}

	manager.Strategy.KnowledgeMode = KnowledgeModeSignals
	onResult := manager.Build(context.Background(), input)
	if planner.calls != 1 || knowledgeMessageFrom(onResult) == nil {
		t.Fatalf("on must inject: calls=%d", planner.calls)
	}

	manager.Strategy.KnowledgeMode = KnowledgeModeOff
	backOff := manager.Build(context.Background(), input)
	if planner.calls != 1 {
		t.Fatalf("turning off must stop planner calls, got %d", planner.calls)
	}
	baseline := NewManager(DefaultBudget(), nil).Build(context.Background(), input)
	if !reflect.DeepEqual(backOff.Messages, baseline.Messages) ||
		!reflect.DeepEqual(backOff.Metadata, baseline.Metadata) {
		t.Fatal("turning KnowledgeMode off must restore byte-identical baseline behavior")
	}
}

func TestKnowledgeStaleAndFloorFiltering(t *testing.T) {
	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{
		Reuse: []knowledge.ReuseItem{
			knowledgeTestReuseItem("no-version.go", "", knowledge.ReuseReasonOK, 0.95, false, false),
			knowledgeTestReuseItem("mismatch.go", "wv-old", knowledge.ReuseReasonVersionMismatch, 0.95, false, false),
			knowledgeTestReuseItem("low-confidence.go", "wv1", knowledge.ReuseReasonProvisional, 0.40, true, true),
			knowledgeTestReuseItem("valid.go", "wv1", knowledge.ReuseReasonOK, 0.85, false, false),
		},
		Reason: knowledge.PlanReasonOK,
	}}
	manager := newKnowledgeTestManager(planner, KnowledgeModeBroad)
	manager.Strategy.ReuseConfidenceFloor = 0.50

	result := manager.Build(context.Background(), knowledgeTestBuildInput("locate the runtime agent loop entry"))
	if result.Metadata["knowledge_stale_filtered"] != 2 {
		t.Fatalf("expected two stale-filtered items, metadata=%#v", result.Metadata)
	}
	if result.Metadata["knowledge_floor_filtered"] != 1 {
		t.Fatalf("expected one floor-filtered item, metadata=%#v", result.Metadata)
	}
	if result.Metadata["knowledge_count"] != 1 || result.Metadata["knowledge_injected"] != true {
		t.Fatalf("expected exactly one injected item, metadata=%#v", result.Metadata)
	}
	if result.Metadata["knowledge_stale_item_injected"] != 0 {
		t.Fatalf("stale_item_injected must stay 0, got %#v", result.Metadata["knowledge_stale_item_injected"])
	}
	message := knowledgeMessageFrom(result)
	if message == nil || strings.Contains(message.Content, "mismatch.go") || strings.Contains(message.Content, "low-confidence.go") {
		t.Fatalf("stale/floor items must not reach the prompt: %#v", message)
	}
}

func TestKnowledgeMinQueryLengthShortCircuits(t *testing.T) {
	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{Reason: knowledge.PlanReasonNoCandidates}}
	manager := newKnowledgeTestManager(planner, KnowledgeModeSignals)
	manager.Strategy.MinKnowledgeQueryLength = 12

	result := manager.Build(context.Background(), knowledgeTestBuildInput("too short"))
	if planner.calls != 0 {
		t.Fatalf("short query must not reach the planner, calls=%d", planner.calls)
	}
	if result.Metadata["knowledge_reason"] != knowledge.PlanReasonQueryTooShort {
		t.Fatalf("expected query_too_short reason, got %#v", result.Metadata["knowledge_reason"])
	}

	longGoal := "locate the runtime agent loop entry"
	result = manager.Build(context.Background(), knowledgeTestBuildInput(longGoal))
	if planner.calls != 1 || planner.lastInput.MinQueryLength != 12 {
		t.Fatalf("expected pass-through min length, calls=%d input=%#v", planner.calls, planner.lastInput)
	}

	manager.Strategy.MinKnowledgeQueryLength = 0
	if manager.effectiveMinKnowledgeQueryLength() != knowledge.DefaultMinKnowledgeQueryLength {
		t.Fatalf("expected planner default min length, got %d", manager.effectiveMinKnowledgeQueryLength())
	}
}

func TestKnowledgeBudgetTruncation(t *testing.T) {
	reuse := make([]knowledge.ReuseItem, 0, 30)
	for index := 0; index < 30; index++ {
		item := knowledgeTestReuseItem("backend/file-"+strings.Repeat("x", 40)+".go", "wv1", knowledge.ReuseReasonOK, 0.95, false, false)
		item.Summary = strings.Repeat("long exploration summary ", 10)
		reuse = append(reuse, item)
	}
	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{Reuse: reuse, Reason: knowledge.PlanReasonOK}}
	manager := newKnowledgeTestManager(planner, KnowledgeModeBroad)

	result := manager.Build(context.Background(), knowledgeTestBuildInput("locate the runtime agent loop entry"))
	count, ok := result.Metadata["knowledge_count"].(int)
	if !ok || count <= 0 || count >= len(reuse) {
		t.Fatalf("expected budget truncation within (0,%d), got %#v", len(reuse), result.Metadata["knowledge_count"])
	}
	message := knowledgeMessageFrom(result)
	if message == nil {
		t.Fatal("expected broad injection message")
	}
	if approxKnowledgeTokens(message.Content) > DefaultKnowledgeTokens {
		t.Fatalf("broad content exceeds token budget: %d", approxKnowledgeTokens(message.Content))
	}
	items, ok := message.Metadata["knowledge_items"].([]map[string]interface{})
	if !ok || len(items) != count {
		t.Fatalf("item metadata must match injected count: count=%d items=%#v", count, message.Metadata["knowledge_items"])
	}
}

func TestKnowledgeSuppressedForActiveTurnReplay(t *testing.T) {
	planner := &fakeKnowledgePlanner{plan: knowledge.Plan{
		Reuse:  []knowledge.ReuseItem{knowledgeTestReuseItem("backend/a.go", "wv1", knowledge.ReuseReasonOK, 0.95, false, false)},
		Reason: knowledge.PlanReasonOK,
	}}
	manager := newKnowledgeTestManager(planner, KnowledgeModeBroad)
	input := knowledgeTestBuildInput("locate the runtime agent loop entry")
	input.History = append(input.History, *types.NewAssistantMessage("already working"))

	result := manager.Build(context.Background(), input)
	if planner.calls != 0 {
		t.Fatalf("active-turn replay must suppress knowledge injection, calls=%d", planner.calls)
	}
	if result.Metadata["knowledge_suppressed_for_active_turn"] != true ||
		result.Metadata["knowledge_reason"] != knowledgeReasonSuppressed {
		t.Fatalf("unexpected suppression metadata: %#v", result.Metadata)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// W7 激活切片：knowledge.mode（off|shadow|on）→ 注入档位映射。
// shadow 必须映射为 off（ModeShadow 契约：只构建索引与统计、绝不注入 prompt）；
// on 取保守档 signals；off / 未知值 fail closed。
func TestKnowledgeModeForLayerMode(t *testing.T) {
	cases := []struct {
		in   knowledge.Mode
		want string
	}{
		{knowledge.ModeOff, KnowledgeModeOff},
		{knowledge.ModeShadow, KnowledgeModeOff},
		{knowledge.ModeOn, KnowledgeModeSignals},
		{knowledge.Mode(""), KnowledgeModeOff},
		{knowledge.Mode("turbo"), KnowledgeModeOff},
	}
	for _, tc := range cases {
		if got := KnowledgeModeForLayerMode(tc.in); got != tc.want {
			t.Fatalf("KnowledgeModeForLayerMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
