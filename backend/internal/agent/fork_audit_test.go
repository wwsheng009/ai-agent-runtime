package agent

// fork_audit_test.go — fork 首请求前缀审计测试（P2-1' G3 度量）。
//
// 覆盖：
//   - 前缀保持判定矩阵：整段保持 / 完全缺失 / 部分改写（kept 缩水）；
//   - forkAuditSpecForTask 的门控（仅 fork 首尝试且快照非空）；
//   - loop 端到端：fork 运行的首个请求携带 fork_* 审计 metadata
//     （随 llm.request.started 载荷进入 cache analytics）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func forkAuditConversation() []types.Message {
	return []types.Message{
		{Role: "user", Content: "analyze the auth module"},
		{Role: "assistant", Content: "the auth module has three handlers"},
		{Role: "user", Content: "now check the api layer"},
		{Role: "assistant", Content: "the api layer is thin"},
	}
}

func TestEvaluateForkPrefixPreservationIntact(t *testing.T) {
	snapshot := forkAuditConversation()
	managed := append([]types.Message{{Role: "system", Content: "child system prompt"}}, snapshot...)
	managed = append(managed, types.Message{Role: "user", Content: "new task"})

	stats := evaluateForkPrefixPreservation(snapshot, managed, nil)
	if !stats.preserved {
		t.Fatalf("intact snapshot must be preserved, got %+v", stats)
	}
	if stats.snapshotMessages != 4 || stats.keptMessages != 4 {
		t.Fatalf("full snapshot must be kept, got %+v", stats)
	}
	if stats.snapshotTokens <= 0 {
		t.Fatalf("snapshot tokens must be estimated, got %d", stats.snapshotTokens)
	}
}

func TestEvaluateForkPrefixPreservationMissing(t *testing.T) {
	snapshot := forkAuditConversation()
	// managed 完全不含快照（例如注入丢失）：kept=0、preserved=false。
	managed := []types.Message{
		{Role: "system", Content: "child system prompt"},
		{Role: "user", Content: "completely different task"},
	}
	stats := evaluateForkPrefixPreservation(snapshot, managed, nil)
	if stats.preserved || stats.keptMessages != 0 {
		t.Fatalf("absent snapshot must report zero kept, got %+v", stats)
	}
}

func TestEvaluateForkPrefixPreservationPartialRewrite(t *testing.T) {
	snapshot := forkAuditConversation()
	// 前两条保持原样，第三条被上下文装配改写 → kept 缩水为 2。
	managed := []types.Message{
		{Role: "system", Content: "child system prompt"},
		snapshot[0],
		snapshot[1],
		{Role: "assistant", Content: "REWRITTEN summary of later turns"},
	}
	stats := evaluateForkPrefixPreservation(snapshot, managed, nil)
	if stats.preserved {
		t.Fatalf("rewritten snapshot must not report preserved, got %+v", stats)
	}
	if stats.keptMessages != 2 {
		t.Fatalf("expected kept=2 after partial rewrite, got %d", stats.keptMessages)
	}
}

func TestForkAuditSpecForTaskGating(t *testing.T) {
	history := forkAuditConversation()
	forkTask := SubagentTask{ID: "f", Goal: "g", Context: SubagentContextFork}
	freshTask := SubagentTask{ID: "s", Goal: "g"}

	if spec := forkAuditSpecForTask(forkTask, history, 1); spec == nil || len(spec.Snapshot) != len(history) {
		t.Fatalf("fork first attempt must enable audit, got %+v", spec)
	}
	if spec := forkAuditSpecForTask(forkTask, history, 2); spec != nil {
		t.Fatal("retry attempts must not re-audit")
	}
	if spec := forkAuditSpecForTask(freshTask, history, 1); spec != nil {
		t.Fatal("fresh tasks must not enable fork audit")
	}
	if spec := forkAuditSpecForTask(forkTask, nil, 1); spec != nil {
		t.Fatal("empty snapshot must not enable fork audit")
	}
}

func TestReActLoop_ForkAuditStampsFirstRequestMetadata(t *testing.T) {
	provider := &SequenceLLMProvider{
		name:      "fork-audit-provider",
		responses: []*llm.LLMResponse{{Content: "done", Model: "test-model", Usage: &types.TokenUsage{}}},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	require.NoError(t, llmRuntime.RegisterProvider("fork-audit-provider", provider))

	agent := NewAgentWithLLM(&Config{
		Name:         "fork-audit-child",
		Provider:     "fork-audit-provider",
		Model:        "test-model",
		SystemPrompt: "You are a worker.",
	}, nil, llmRuntime)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        2,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	history := forkAuditConversation()
	_, err := loop.run(context.Background(), "do the work", loopRunOptions{
		TraceID:       "trace-fork-audit",
		IncludePrompt: true,
		History:       history,
		ForkAudit:     &ForkAuditSpec{Snapshot: history},
	})
	require.NoError(t, err)
	require.NotEmpty(t, provider.requests)

	first := provider.requests[0]
	require.Equal(t, true, first.Metadata[forkPrefixPreservedKey],
		"intact fork snapshot must stamp preserved=true")
	require.Equal(t, len(history), first.Metadata[forkSnapshotMessagesKey])
	require.Equal(t, len(history), first.Metadata[forkPrefixKeptMessagesKey])
	if tokens, ok := first.Metadata[forkSnapshotTokensKey].(int); !ok || tokens <= 0 {
		t.Fatalf("snapshot tokens must be stamped as a positive int, got %#v", first.Metadata[forkSnapshotTokensKey])
	}

	// 审计只盖首个请求：后续请求不应再携带（run 作用域一次性）。
	if len(provider.requests) > 1 {
		require.NotContains(t, provider.requests[1].Metadata, forkPrefixPreservedKey)
	}
}
