package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// 编译期断言：handlerExecutor 满足 runtimehost.Executor。
var _ runtimehost.Executor = (*handlerExecutor)(nil)

type slowTestLLMProvider struct {
	*testLLMProvider
	delay time.Duration
}

func (p *slowTestLLMProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	time.Sleep(p.delay)
	return p.testLLMProvider.Call(ctx, req)
}

func (p *slowTestLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	time.Sleep(p.delay)
	return p.testLLMProvider.Stream(ctx, req)
}

// TestHandlerExecutorContract 验证 E3.0 适配器契约：Executor() 非空、Hub() 暴露
// 同一个 hub、Close 可用——行为零变化（不改变任何既有执行路径）。
func TestHandlerExecutorContract(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	exec := handler.Executor()
	if exec == nil {
		t.Fatal("Executor() must return a non-nil adapter")
	}
	if exec.Hub() == nil {
		t.Fatal("Hub() must expose a hub")
	}
	if exec.Hub() != handler.getSessionHub() {
		t.Fatal("Hub() must expose the handler's own hub (same instance)")
	}
	if err := exec.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestHandlerExecutorGuardsUnconfigured 验证未配置 handler 时全部 fail-closed。
func TestHandlerExecutorGuardsUnconfigured(t *testing.T) {
	var nilHandler *Handler
	if nilHandler.Executor() != nil {
		t.Fatal("nil handler must yield nil executor")
	}

	exec := &handlerExecutor{}
	if _, err := exec.SubmitTurn(context.Background(), runtimehost.TurnRequest{SessionID: "s", Prompt: "p"}); err == nil {
		t.Fatal("SubmitTurn with nil handler must error")
	}
	if _, err := exec.SubmitTurnWait(context.Background(), runtimehost.TurnRequest{SessionID: "s", Prompt: "p"}); err == nil {
		t.Fatal("SubmitTurnWait with nil handler must error")
	}
	if err := exec.SubmitAsync(context.Background(), runtimehost.TurnRequest{SessionID: "s", Prompt: "p"}); err == nil {
		t.Fatal("SubmitAsync with nil handler must error")
	}
	if exec.Hub() != nil {
		t.Fatal("Hub() with nil handler must be nil")
	}
	if err := exec.Close(context.Background()); err != nil {
		t.Fatalf("Close with nil handler must be a no-op: %v", err)
	}
}

// TestHandlerExecutorRejectsBlankSession 验证空 session id 被拒（不误建 actor）。
func TestHandlerExecutorRejectsBlankSession(t *testing.T) {
	exec := NewHandler(nil, nil, nil).Executor()
	if _, err := exec.SubmitTurn(context.Background(), runtimehost.TurnRequest{Prompt: "p"}); err == nil {
		t.Fatal("blank session id must error")
	}
	waiter, ok := exec.(runtimehost.TurnWaiter)
	if !ok {
		t.Fatal("handler executor must implement TurnWaiter")
	}
	if _, err := waiter.SubmitTurnWait(context.Background(), runtimehost.TurnRequest{Prompt: "p"}); err == nil {
		t.Fatal("blank session id must error for waiting submit")
	}
	if err := exec.SubmitAsync(context.Background(), runtimehost.TurnRequest{Prompt: "p"}); err == nil {
		t.Fatal("blank session id must error")
	}
}

func TestHandlerExecutorSubmitTurnWaitWaitsBeyondSubmitBudget(t *testing.T) {
	mcpManager := &testMCPManager{}
	handler := NewHandler(skill.NewRegistry(mcpManager), nil, mcpManager)

	provider := &slowTestLLMProvider{
		testLLMProvider: &testLLMProvider{name: "test-model", content: "slow response"},
		delay:           150 * time.Millisecond,
	}
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	if err := llmRuntime.RegisterProvider("test-model", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	handler.SetLLMRuntime(llmRuntime)

	manager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(manager.Stop)
	handler.SetSessionManager(manager)
	session, err := manager.Create(context.Background(), "slow-user")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	waiter, ok := handler.Executor().(runtimehost.TurnWaiter)
	if !ok {
		t.Fatal("handler executor must implement TurnWaiter")
	}
	result, err := waiter.SubmitTurnWait(context.Background(), runtimehost.TurnRequest{
		SessionID: session.ID,
		Prompt:    "hello",
	})
	if err != nil {
		t.Fatalf("SubmitTurnWait: %v", err)
	}
	if !result.Completed {
		t.Fatal("waiting submit must return a terminal completion")
	}
	if !result.Success || result.Output != "slow response" {
		t.Fatalf("unexpected terminal result: %+v", result)
	}
}
