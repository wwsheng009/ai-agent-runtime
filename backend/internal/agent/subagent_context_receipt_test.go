package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestForkSnapshotHashStableAndSensitive 锁定 §6.4/§6.5：fork 快照指纹对同一
// 冻结快照稳定、对内容变化敏感；无快照返回空串（未 fork 与空快照分开表达）。
func TestForkSnapshotHashStableAndSensitive(t *testing.T) {
	snapshot := forkFixture()
	first := forkSnapshotHash(snapshot)
	if first == "" || len(first) != 16 {
		t.Fatalf("hash must be non-empty 16-hex, got %q", first)
	}
	// 深拷贝等价内容 → 同一指纹（快照一旦冻结，父会话后续改写不影响）。
	cloned := make([]types.Message, 0, len(snapshot))
	for _, message := range snapshot {
		cloned = append(cloned, *message.Clone())
	}
	if again := forkSnapshotHash(cloned); again != first {
		t.Fatalf("same content must hash identically: %q vs %q", first, again)
	}
	mutated := make([]types.Message, len(cloned))
	copy(mutated, cloned)
	mutated[len(mutated)-1].Content += " mutated"
	if changed := forkSnapshotHash(mutated); changed == first {
		t.Fatal("mutated content must change the fingerprint")
	}
	if empty := forkSnapshotHash(nil); empty != "" {
		t.Fatalf("empty snapshot must hash to empty, got %q", empty)
	}
}

// TestSubagentContextReceipt 锁定 §6.5：requested/effective 分离 + 注入事实。
func TestSubagentContextReceipt(t *testing.T) {
	freshTask := SubagentTask{ID: "t-fresh"}
	receipt := subagentContextReceipt(freshTask, 0)
	if receipt["requested_context"] != "fresh" || receipt["effective_context"] != "fresh" {
		t.Fatalf("fresh task receipt: %+v", receipt)
	}
	if receipt["parent_history_injected"] != false {
		t.Fatalf("fresh must not claim injected history: %+v", receipt)
	}

	forkTask := SubagentTask{ID: "t-fork", Context: SubagentContextFork}
	receipt = subagentContextReceipt(forkTask, 12)
	if receipt["requested_context"] != "fork" || receipt["effective_context"] != "fork" {
		t.Fatalf("fork task receipt: %+v", receipt)
	}
	if receipt["parent_history_injected"] != true || receipt["fork_snapshot_messages"] != 12 {
		t.Fatalf("fork snapshot facts missing: %+v", receipt)
	}

	// 请求 fork 但快照为空（首轮等价 fresh）：requested 保留、effective 降为 fresh。
	receipt = subagentContextReceipt(forkTask, 0)
	if receipt["requested_context"] != "fork" || receipt["effective_context"] != "fresh" {
		t.Fatalf("empty fork snapshot must degrade to fresh effective: %+v", receipt)
	}

	// §8.3 窗口：请求了 fork_messages 时 receipt 同时给出窗口与实际注入条数
	//（requested 窗口 ≠ 实际条数：配平/上限仍会计入）。
	windowedTask := SubagentTask{ID: "t-fork-window", Context: SubagentContextFork, ForkMessages: 20}
	receipt = subagentContextReceipt(windowedTask, 18)
	if receipt["fork_messages"] != 20 || receipt["fork_snapshot_messages"] != 18 {
		t.Fatalf("windowed fork receipt must separate requested window from injected count: %+v", receipt)
	}
	// 未请求窗口时不出现该键（"未请求" ≠ "窗口=0"）。
	receipt = subagentContextReceipt(forkTask, 12)
	if _, ok := receipt["fork_messages"]; ok {
		t.Fatalf("unrequested window must not fabricate fork_messages: %+v", receipt)
	}
}

// TestEmitSubagentRouteResolvedCarriesForkReceipt 锁定 §6.5 emit 接线：fork 任务
// 的路由审计载荷携带 effective=fork、注入事实与快照指纹（同一快照跨 attempt
// 指纹一致）。
func TestEmitSubagentRouteResolvedCarriesForkReceipt(t *testing.T) {
	rig := newAttemptEventRig(t)
	scheduler := NewSubagentScheduler(rig.agent, SubagentSchedulerConfig{})
	task := SubagentTask{ID: "t-fork", Role: "explore", Context: SubagentContextFork}
	spec := ChildAgentSpec{ForkHistory: forkFixture()}
	scheduler.emitSubagentRouteResolved(SubagentRunOptions{ParentSessionID: "p-1"}, task, spec, "child-1", "explore", 1, 2)

	events := rig.snapshot()
	var receipt map[string]interface{}
	for _, event := range events {
		if event.eventType == runtimeevents.EventSubagentRouteResolved {
			receipt = event.payload
		}
	}
	require.NotNil(t, receipt, "route.resolved 必须发出")
	require.Equal(t, "fork", receipt["requested_context"])
	require.Equal(t, "fork", receipt["effective_context"])
	require.Equal(t, true, receipt["parent_history_injected"])
	require.Equal(t, len(forkFixture()), receipt["fork_snapshot_messages"])
	require.Equal(t, forkSnapshotHash(forkFixture()), receipt["fork_snapshot_hash"])
}

// TestSubagentParentPromptCacheKeyDerivation 锁定 §6.4：父 key 由 spawn 时刻父
// epoch 派生；父会话或 epoch 不可观时为空串（不猜测）。
func TestSubagentParentPromptCacheKeyDerivation(t *testing.T) {
	if got := subagentParentPromptCacheKey("", func() int { return 3 }); got != "" {
		t.Fatalf("empty parent session must stay empty, got %q", got)
	}
	if got := subagentParentPromptCacheKey("parent-1", nil); got != "" {
		t.Fatalf("unknown epoch provider must stay empty, got %q", got)
	}
	if got := subagentParentPromptCacheKey("parent-1", func() int { return 0 }); got != "parent-1" {
		t.Fatalf("epoch 0 must degrade to the bare session id, got %q", got)
	}
	if got := subagentParentPromptCacheKey("parent-1", func() int { return 3 }); got != "parent-1#prompt-cache-epoch-3" {
		t.Fatalf("unexpected parent key: %q", got)
	}
}

// TestReActLoopStampsParentPromptCacheKeyOnChildRequests 锁定 §6.4 接线：
// loopRunOptions.ParentPromptCacheKey 必须进入子请求 metadata（随 llm.request.*
// 载荷落库）。有值时携带，缺省不产生该键。
func TestReActLoopStampsParentPromptCacheKeyOnChildRequests(t *testing.T) {
	provider := &SequenceLLMProvider{
		name:      "parent-key-provider",
		responses: []*llm.LLMResponse{{Content: "done", Model: "test-model", Usage: &types.TokenUsage{}}},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime.RegisterProvider("parent-key-provider", provider))
	agent := NewAgentWithLLM(&Config{
		Name:         "parent-key-child",
		Provider:     "parent-key-provider",
		Model:        "test-model",
		SystemPrompt: "You are a worker.",
	}, nil, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        2,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	_, err := loop.run(context.Background(), "do the work", loopRunOptions{
		TraceID:              "trace-parent-key",
		IncludePrompt:        true,
		SubagentID:           "sub-1",
		ParentPromptCacheKey: "parent-session#prompt-cache-epoch-3",
	})
	require.NoError(t, err)
	require.NotEmpty(t, provider.requests)
	require.Equal(t, "parent-session#prompt-cache-epoch-3", provider.requests[0].Metadata["parent_prompt_cache_key"])

	// 缺省（空串）不得产生该 metadata 键：读取侧把"未携带"与"空值"分开。
	provider2 := &SequenceLLMProvider{
		name:      "parent-key-provider",
		responses: []*llm.LLMResponse{{Content: "done", Model: "test-model", Usage: &types.TokenUsage{}}},
	}
	llmRuntime2 := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime2.RegisterProvider("parent-key-provider", provider2))
	agent2 := NewAgentWithLLM(&Config{
		Name:         "parent-key-child-2",
		Provider:     "parent-key-provider",
		Model:        "test-model",
		SystemPrompt: "You are a worker.",
	}, nil, llmRuntime2)
	loop2 := NewReActLoop(agent2, llmRuntime2, &LoopReActConfig{MaxSteps: 2, EnableThought: true, EnableToolCalls: true})
	_, err = loop2.run(context.Background(), "do the work", loopRunOptions{
		TraceID:       "trace-parent-key-2",
		IncludePrompt: true,
		SubagentID:    "sub-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, provider2.requests)
	_, hasKey := provider2.requests[0].Metadata["parent_prompt_cache_key"]
	require.False(t, hasKey, "empty parent key must not be stamped")
}

// TestExportedReceiptSurface 锁定宿主复用面（§6.4/§6.5）：导出助手与内部实现
// 同源——commands 的 direct spawn 依赖它们构造与 batch 路径同字段名的 receipt。
func TestExportedReceiptSurface(t *testing.T) {
	snapshot := forkFixture()
	if ForkSnapshotHash(snapshot) != forkSnapshotHash(snapshot) {
		t.Fatal("exported hash must match the internal implementation")
	}
	receipt := SubagentContextReceipt("", 3)
	if receipt["requested_context"] != SubagentContextModeFresh || receipt["effective_context"] != SubagentContextModeFork {
		t.Fatalf("exported receipt mismatch: %+v", receipt)
	}
	if receipt["parent_history_injected"] != true || receipt["fork_snapshot_messages"] != 3 {
		t.Fatalf("exported receipt injection facts mismatch: %+v", receipt)
	}
	if got := SubagentParentPromptCacheKey("s-1", func() int { return 2 }); got != "s-1#prompt-cache-epoch-2" {
		t.Fatalf("exported parent key mismatch: %q", got)
	}
}
