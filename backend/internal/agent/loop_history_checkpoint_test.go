package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestReActLoop_NotifiesHistoryCheckpointAfterDurableCommit 回归：ReAct 循环每次
// 提交 durable 历史都必须通知宿主，宿主据此在长 turn 运行期间增量落库（而不是等
// turn 结束）。回调收到的必须是 durable 视图（不含 prompt-only system 消息）。
func TestReActLoop_NotifiesHistoryCheckpointAfterDurableCommit(t *testing.T) {
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{Content: "checkpoint reply", Model: "test-model"},
		},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	llmRuntime.RegisterProvider("test-provider", provider)

	apiAgent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Provider:     "test-provider",
			Model:        "test-model",
			SystemPrompt: "You are a helpful assistant.",
		},
		state: AgentState{},
	}
	session := newTestHistorySession("session-history-checkpoint")

	var mu sync.Mutex
	var snapshots [][]types.Message
	loop := NewReActLoop(apiAgent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: false,
		OnHistoryCheckpoint: func(_ context.Context, messages []types.Message) {
			mu.Lock()
			snapshots = append(snapshots, append([]types.Message(nil), messages...))
			mu.Unlock()
		},
	})

	result, err := loop.RunWithSession(context.Background(), "hello", session)
	if err != nil {
		t.Fatalf("RunWithSession failed: %v", err)
	}
	if result == nil || result.Output != "checkpoint reply" {
		t.Fatalf("unexpected result: %#v", result)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(snapshots) == 0 {
		t.Fatal("durable 历史提交必须触发 OnHistoryCheckpoint，否则长 turn 无法中途落库")
	}
	last := snapshots[len(snapshots)-1]
	if len(last) == 0 {
		t.Fatal("OnHistoryCheckpoint 收到空历史")
	}
	if got := last[len(last)-1].Content; got != "checkpoint reply" {
		t.Fatalf("最后一次 checkpoint 必须包含已提交的 assistant 回复，got %q", got)
	}
	for _, message := range last {
		if message.Role == "system" {
			t.Fatalf("durable 历史不得包含 system 消息，got %#v", message)
		}
	}
}

// TestReActLoop_NilHistoryCheckpointIsSafe 回归：未配置回调时必须完全无副作用
// （默认路径不受影响）。
func TestReActLoop_NilHistoryCheckpointIsSafe(t *testing.T) {
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{Content: "no checkpoint", Model: "test-model"},
		},
	}
	llmRuntime := llm.NewLLMRuntime(nil)
	llmRuntime.RegisterProvider("test-provider", provider)

	apiAgent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Provider:     "test-provider",
			Model:        "test-model",
			SystemPrompt: "You are a helpful assistant.",
		},
		state: AgentState{},
	}
	loop := NewReActLoop(apiAgent, llmRuntime, &LoopReActConfig{
		MaxSteps:        3,
		EnableThought:   true,
		EnableToolCalls: false,
	})

	result, err := loop.RunWithSession(context.Background(), "hello", newTestHistorySession("session-history-checkpoint-nil"))
	if err != nil {
		t.Fatalf("RunWithSession failed: %v", err)
	}
	if result == nil || result.Output != "no checkpoint" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
