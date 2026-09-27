package llm

import (
	"context"
	"errors"
	"testing"
)

// malformedSyntaxError 构造一个「语法类 invalid_tool_arguments」错误（无截断证据），
// 供共享配额分类断言使用。
func malformedSyntaxError(t *testing.T) error {
	t.Helper()
	err := validateAssistantMessageSemantics(map[string]interface{}{
		"finish_reason": "tool_calls",
		"tool_calls": []map[string]interface{}{{
			"id": "call_1",
			"function": map[string]interface{}{
				"name":      "write",
				"arguments": `{"content":"truncated`,
			},
		}},
	})
	if err == nil {
		t.Fatal("expected malformed tool-arguments error")
	}
	if got := classifyRetryableLLMError(err).Reason; got != "invalid_tool_arguments" {
		t.Fatalf("fixture reason=%q want invalid_tool_arguments", got)
	}
	return err
}

// truncatedMalformedError 实现截断证据接口：真截断走预算扩容路径，不消耗共享配额。
type truncatedMalformedError struct{}

func (truncatedMalformedError) Error() string {
	return "openai_stream_protocol_error: code=invalid_tool_arguments: tool call 0 was cut off by the completion budget"
}

func (truncatedMalformedError) ToolCallArgumentsTruncated() bool { return true }

func TestDegenerateRecoveryBudgetAllowsAndExhausts(t *testing.T) {
	budget := NewDegenerateRecoveryBudget(2)
	if budget.Max() != 2 || budget.Remaining() != 2 || budget.Used() != 0 {
		t.Fatalf("fresh budget max=%d remaining=%d used=%d", budget.Max(), budget.Remaining(), budget.Used())
	}
	if !budget.Allow() || !budget.Allow() {
		t.Fatal("budget must allow the configured number of consumptions")
	}
	if budget.Allow() {
		t.Fatal("budget must report exhaustion after max consumptions")
	}
	if budget.Used() != 2 || budget.Remaining() != 0 {
		t.Fatalf("exhausted budget used=%d remaining=%d", budget.Used(), budget.Remaining())
	}
	if NewDegenerateRecoveryBudget(0).Max() != DefaultDegenerateRecoveryBudget {
		t.Fatalf("max<=0 must fall back to the default budget")
	}
	// nil 安全：无配额（nil）不阻塞调用方。
	var nilBudget *DegenerateRecoveryBudget
	if !nilBudget.Allow() || nilBudget.Used() != 0 || nilBudget.Max() != 0 || nilBudget.Remaining() != 0 {
		t.Fatal("nil budget must be inert and allow replay (legacy behavior)")
	}
}

func TestDegenerateRecoveryBudgetContextRoundTrip(t *testing.T) {
	budget := NewDegenerateRecoveryBudget(3)
	ctx := WithDegenerateRecoveryBudget(context.Background(), budget)
	if got := DegenerateRecoveryBudgetFromContext(ctx); got != budget {
		t.Fatalf("ctx round-trip returned %v", got)
	}
	if DegenerateRecoveryBudgetFromContext(context.Background()) != nil {
		t.Fatal("unbound context must return nil budget")
	}
	if DegenerateRecoveryBudgetFromContext(nil) != nil {
		t.Fatal("nil context must return nil budget")
	}
	if WithDegenerateRecoveryBudget(nil, budget) != nil {
		t.Fatal("nil context must pass through unchanged")
	}
	if got := WithDegenerateRecoveryBudget(context.Background(), nil); got == nil {
		t.Fatal("nil budget must not clear the context")
	}
}

func TestConsumeDegenerateRecoveryBudgetOnlyChargesDegenerateClasses(t *testing.T) {
	syntaxErr := malformedSyntaxError(t)

	ctx := WithDegenerateRecoveryBudget(context.Background(), NewDegenerateRecoveryBudget(1))
	if !consumeDegenerateRecoveryBudget(ctx, syntaxErr) {
		t.Fatal("first syntax-class replay must be allowed")
	}
	if consumeDegenerateRecoveryBudget(ctx, syntaxErr) {
		t.Fatal("shared budget must stop the second syntax-class replay")
	}

	// 真截断不消耗共享配额（走预算扩容路径）。
	truncatedCtx := WithDegenerateRecoveryBudget(context.Background(), NewDegenerateRecoveryBudget(1))
	if !consumeDegenerateRecoveryBudget(truncatedCtx, truncatedMalformedError{}) {
		t.Fatal("truncated class must not be blocked by the shared budget")
	}
	if used := DegenerateRecoveryBudgetFromContext(truncatedCtx).Used(); used != 0 {
		t.Fatalf("truncated class charged the shared budget: used=%d", used)
	}

	// transport/服务端类必须保持真实重试语义，不受共享配额约束。
	transportCtx := WithDegenerateRecoveryBudget(context.Background(), NewDegenerateRecoveryBudget(1))
	if !consumeDegenerateRecoveryBudget(transportCtx, errors.New("connection reset by peer")) {
		t.Fatal("transport class must not be blocked by the shared budget")
	}
	if used := DegenerateRecoveryBudgetFromContext(transportCtx).Used(); used != 0 {
		t.Fatalf("transport class charged the shared budget: used=%d", used)
	}

	// 未挂载配额时保持旧行为（不阻塞）。
	if !consumeDegenerateRecoveryBudget(context.Background(), syntaxErr) {
		t.Fatal("unbound context must keep the legacy retry behavior")
	}
}
