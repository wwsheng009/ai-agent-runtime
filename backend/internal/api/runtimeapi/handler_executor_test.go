package runtimeapi

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
)

// 编译期断言：handlerExecutor 满足 runtimehost.Executor。
var _ runtimehost.Executor = (*handlerExecutor)(nil)

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
	if err := exec.SubmitAsync(context.Background(), runtimehost.TurnRequest{Prompt: "p"}); err == nil {
		t.Fatal("blank session id must error")
	}
}
