package agent

import (
	"context"
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// W7 激活切片：会话装配把 `context_knowledge_*` options 装配进 contextmgr——
// on 时 Planner 被调用、off（含未知档位 fail-closed）时零调用，默认（无
// options）不新增任何 knowledge_* key，行为与改动前一致。

type assemblyFakePlanner struct {
	calls int
}

func (f *assemblyFakePlanner) Plan(ctx context.Context, in knowledge.PlanInput) (knowledge.Plan, error) {
	f.calls++
	return knowledge.Plan{
		Reuse: []knowledge.ReuseItem{{
			NodeID:           "en_loop.go",
			NodeType:         knowledge.NodeTypeFile,
			Target:           "backend/internal/agent/loop.go",
			Summary:          "remembered loop entry",
			Confidence:       0.95,
			KnowledgeVersion: "wv1",
			Scope:            knowledge.ReuseScopeTask,
			Reason:           knowledge.ReuseReasonOK,
		}},
		Reason: knowledge.PlanReasonOK,
	}, nil
}

func assemblyBuildInput() contextmgr.BuildInput {
	goal := "locate the runtime agent loop entry"
	return contextmgr.BuildInput{
		TraceID:     "trace-assembly",
		WorkspaceID: "ws-1",
		SessionID:   "session-assembly",
		TaskID:      "task-1",
		Goal:        goal,
		History: []types.Message{
			*types.NewSystemMessage("system prompt"),
			*types.NewUserMessage(goal),
		},
	}
}

func TestContextManagerKnowledgeAssemblyOffByDefault(t *testing.T) {
	manager := newDefaultContextManager(&Config{}, nil)
	if manager == nil {
		t.Fatal("expected manager")
	}
	if manager.Knowledge != nil {
		t.Fatal("default assembly must not attach a knowledge planner")
	}
	if manager.Strategy.KnowledgeMode != contextmgr.KnowledgeModeOff {
		t.Fatalf("default knowledge mode = %q, want off", manager.Strategy.KnowledgeMode)
	}
	result := manager.Build(context.Background(), assemblyBuildInput())
	if _, exists := result.Metadata["knowledge_mode"]; exists {
		t.Fatalf("off assembly must not add knowledge metadata keys, got %#v", result.Metadata["knowledge_mode"])
	}
}

func TestContextManagerKnowledgeAssemblyOnCallsPlanner(t *testing.T) {
	planner := &assemblyFakePlanner{}
	cfg := &Config{Options: map[string]interface{}{
		"context_knowledge_mode":             contextmgr.KnowledgeModeSignals,
		"context_knowledge_layer":            planner,
		"context_min_knowledge_query_length": 6,
		"context_reuse_confidence_floor":     0.50,
	}}
	manager := newDefaultContextManager(cfg, nil)
	if manager == nil {
		t.Fatal("expected manager")
	}
	if manager.Knowledge == nil {
		t.Fatal("on assembly must attach the knowledge planner")
	}
	if manager.Strategy.KnowledgeMode != contextmgr.KnowledgeModeSignals {
		t.Fatalf("knowledge mode = %q, want signals", manager.Strategy.KnowledgeMode)
	}
	if manager.Strategy.MinKnowledgeQueryLength != 6 || manager.Strategy.ReuseConfidenceFloor != 0.50 {
		t.Fatalf("knowledge knobs not passed through: %+v", manager.Strategy)
	}

	result := manager.Build(context.Background(), assemblyBuildInput())
	if planner.calls != 1 {
		t.Fatalf("expected exactly 1 planner call on assembly, got %d", planner.calls)
	}
	if result.Metadata["knowledge_mode"] != contextmgr.KnowledgeModeSignals {
		t.Fatalf("expected knowledge_mode=signals metadata, got %#v", result.Metadata["knowledge_mode"])
	}
}

func TestContextManagerKnowledgeAssemblyOffWithPlannerZeroCalls(t *testing.T) {
	planner := &assemblyFakePlanner{}
	cfg := &Config{Options: map[string]interface{}{
		"context_knowledge_mode":  contextmgr.KnowledgeModeOff,
		"context_knowledge_layer": planner,
	}}
	manager := newDefaultContextManager(cfg, nil)
	if manager == nil || manager.Knowledge == nil {
		t.Fatal("supplied planner must be attached even when mode is off")
	}
	result := manager.Build(context.Background(), assemblyBuildInput())
	if planner.calls != 0 {
		t.Fatalf("off must not call the planner, got %d calls", planner.calls)
	}
	if _, exists := result.Metadata["knowledge_mode"]; exists {
		t.Fatalf("off must not add knowledge metadata keys, got %#v", result.Metadata["knowledge_mode"])
	}
}

func TestContextManagerKnowledgeAssemblyUnknownModeFailsClosed(t *testing.T) {
	planner := &assemblyFakePlanner{}
	cfg := &Config{Options: map[string]interface{}{
		"context_knowledge_mode":  "turbo",
		"context_knowledge_layer": planner,
	}}
	manager := newDefaultContextManager(cfg, nil)
	if manager == nil {
		t.Fatal("expected manager")
	}
	if manager.Strategy.KnowledgeMode != contextmgr.KnowledgeModeOff {
		t.Fatalf("unknown mode must fail closed to off, got %q", manager.Strategy.KnowledgeMode)
	}
	manager.Build(context.Background(), assemblyBuildInput())
	if planner.calls != 0 {
		t.Fatalf("unknown mode must not call the planner, got %d calls", planner.calls)
	}
}

// TestContextManagerKnowledgeAssemblyReversibleEndToEnd 是 G4 的端到端口径
// （W7 测量段）：装配链（W7a attachKnowledgePlanner + W5 注入）off 档与
// 「未挂知识层」的基线逐字节一致，且 off → on → off 可逆回基线。
// 与 contextmgr 单测（TestKnowledgeOffZeroCallsAndBaseline /
// TestKnowledgeReversibleOnOff）互补：这里走的是 agent 装配入口。
func TestContextManagerKnowledgeAssemblyReversibleEndToEnd(t *testing.T) {
	input := assemblyBuildInput()
	baseline := newDefaultContextManager(&Config{}, nil).Build(context.Background(), input)

	planner := &assemblyFakePlanner{}
	cfg := &Config{Options: map[string]interface{}{
		"context_knowledge_mode":  contextmgr.KnowledgeModeOff,
		"context_knowledge_layer": planner,
	}}
	manager := newDefaultContextManager(cfg, nil)
	if manager == nil || manager.Knowledge == nil {
		t.Fatal("supplied planner must be attached even when mode is off")
	}

	offResult := manager.Build(context.Background(), input)
	if planner.calls != 0 {
		t.Fatalf("off must not call the planner, got %d calls", planner.calls)
	}
	assertKnowledgeBaselineEqual(t, offResult, baseline)

	manager.Strategy.KnowledgeMode = contextmgr.KnowledgeModeSignals
	onResult := manager.Build(context.Background(), input)
	if planner.calls != 1 {
		t.Fatalf("on must call the planner exactly once, got %d", planner.calls)
	}
	if onResult.Metadata["knowledge_mode"] != contextmgr.KnowledgeModeSignals {
		t.Fatalf("on must publish knowledge_mode metadata, got %#v", onResult.Metadata["knowledge_mode"])
	}
	if !hasKnowledgeStageMessage(onResult.Messages) {
		t.Fatal("on must inject a knowledge-stage message")
	}

	manager.Strategy.KnowledgeMode = contextmgr.KnowledgeModeOff
	backOff := manager.Build(context.Background(), input)
	if planner.calls != 1 {
		t.Fatalf("turning off must stop planner calls, got %d", planner.calls)
	}
	assertKnowledgeBaselineEqual(t, backOff, baseline)
}

// assertKnowledgeBaselineEqual 断言构建结果与无知识层基线逐字节一致。
func assertKnowledgeBaselineEqual(t *testing.T, got, baseline contextmgr.BuildResult) {
	t.Helper()
	if !reflect.DeepEqual(got.Messages, baseline.Messages) {
		t.Fatal("off must restore byte-identical baseline messages")
	}
	if !reflect.DeepEqual(got.Metadata, baseline.Metadata) {
		t.Fatalf("off must restore byte-identical baseline metadata: %#v", got.Metadata)
	}
	if _, exists := got.Metadata["knowledge_mode"]; exists {
		t.Fatal("off must not add knowledge metadata keys")
	}
}

// hasKnowledgeStageMessage 报告构建结果里是否存在知识层注入消息。
func hasKnowledgeStageMessage(messages []types.Message) bool {
	for index := range messages {
		if messages[index].Metadata["context_stage"] == "knowledge" {
			return true
		}
	}
	return false
}
