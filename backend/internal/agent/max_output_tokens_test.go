package agent

import (
	"fmt"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

func TestShouldEscalateMaxOutputTokens(t *testing.T) {
	t.Setenv(llm.EnvDisableMaxTokensCap, "")
	t.Setenv(llm.EnvMaxOutputTokens, "")
	t.Setenv(llm.EnvAICLIMaxOutputTokens, "")

	req := &llm.LLMRequest{MaxTokens: llm.CappedDefaultMaxTokens}
	resp := &llm.LLMResponse{FinishReason: "max_tokens", Content: "partial"}
	if !shouldEscalateMaxOutputTokens(req, resp) {
		t.Fatal("expected capped request with max_tokens finish to escalate")
	}

	req.MaxTokens = llm.EscalatedMaxTokens
	if shouldEscalateMaxOutputTokens(req, resp) {
		t.Fatal("did not expect escalate when already above capped default")
	}

	req.MaxTokens = llm.CappedDefaultMaxTokens
	req.Metadata = map[string]interface{}{"max_output_tokens_escalated": true}
	if shouldEscalateMaxOutputTokens(req, resp) {
		t.Fatal("did not expect second escalate after flag set")
	}

	req.Metadata = nil
	resp.FinishReason = "stop"
	if shouldEscalateMaxOutputTokens(req, resp) {
		t.Fatal("did not expect escalate on clean stop")
	}
}

func TestResponseFinishReasonFallsBackToMetadata(t *testing.T) {
	resp := &llm.LLMResponse{Metadata: map[string]interface{}{"finish_reason": "length"}}
	if got := responseFinishReason(resp); got != "length" {
		t.Fatalf("expected length from metadata, got %q", got)
	}
}

// TestShouldEscalateTruncatedToolCallBudget 验证参数被输出预算截断的错误
// （MalformedToolCallError + Truncated）能触发一次性 8k→64k 升级，而语法退化
// 的非法 JSON 字面量不能——后者靠重采样恢复，扩大预算只会放大同类样本。
func TestShouldEscalateTruncatedToolCallBudget(t *testing.T) {
	t.Setenv(llm.EnvDisableMaxTokensCap, "")
	t.Setenv(llm.EnvMaxOutputTokens, "")
	t.Setenv(llm.EnvAICLIMaxOutputTokens, "")

	truncated := &llmadapter.MalformedToolCallError{
		Kind:         "openai_stream_protocol_error",
		Code:         "invalid_tool_arguments",
		FinishReason: "length",
		Truncated:    true,
		ToolCalls: []llmadapter.MalformedToolCall{{
			Index: 0, ID: "call_1", Name: "multiedit", Arguments: `{"path":"a.go"`,
		}},
	}
	req := &llm.LLMRequest{MaxTokens: llm.CappedDefaultMaxTokens}
	if !shouldEscalateTruncatedToolCallBudget(req, truncated) {
		t.Fatal("expected capped request with a truncated tool call to escalate")
	}
	// runtime 层会把错误包一层，errors.As 必须仍然可达。
	if !shouldEscalateTruncatedToolCallBudget(req, fmt.Errorf("failed to handle response: %w", truncated)) {
		t.Fatal("expected wrapped truncated tool call to escalate")
	}

	req.Metadata = map[string]interface{}{"max_output_tokens_escalated": true}
	if shouldEscalateTruncatedToolCallBudget(req, truncated) {
		t.Fatal("did not expect a second escalate after the flag is set")
	}
	req.Metadata = nil
	req.MaxTokens = llm.EscalatedMaxTokens
	if shouldEscalateTruncatedToolCallBudget(req, truncated) {
		t.Fatal("did not expect escalate when the budget is already widened")
	}

	// 语法退化：不是截断，不能扩大预算。
	req.MaxTokens = llm.CappedDefaultMaxTokens
	degenerate := &llmadapter.MalformedToolCallError{
		Kind:         "openai_stream_protocol_error",
		Code:         "invalid_tool_arguments",
		FinishReason: "tool_calls",
	}
	if shouldEscalateTruncatedToolCallBudget(req, degenerate) {
		t.Fatal("did not expect budget escalation for a degenerate JSON literal")
	}
	if shouldEscalateTruncatedToolCallBudget(req, nil) {
		t.Fatal("did not expect escalation without an error")
	}
	if shouldEscalateTruncatedToolCallBudget(nil, truncated) {
		t.Fatal("did not expect escalation without a request")
	}
}

// TestShouldEscalateAggregatedTruncatedToolCallBudget 验证聚合校验层丢弃的
// truncated_tool_call（响应带工具调用且 finish_reason=length）也能触发同一套
// 一次性 8k→64k 升级。这条通道此前只有 MalformedToolCallError 可达：聚合层
// 丢整条响应的形态没有升级出口，同预算重采样必然再次截断，只能等内层采样
// 耗尽后整轮失败。
func TestShouldEscalateAggregatedTruncatedToolCallBudget(t *testing.T) {
	t.Setenv(llm.EnvDisableMaxTokensCap, "")
	t.Setenv(llm.EnvMaxOutputTokens, "")
	t.Setenv(llm.EnvAICLIMaxOutputTokens, "")

	truncated := fmt.Errorf("truncated_tool_call: incomplete tool call markup in aggregated assistant response")
	req := &llm.LLMRequest{MaxTokens: llm.CappedDefaultMaxTokens}
	if !shouldEscalateTruncatedToolCallBudget(req, truncated) {
		t.Fatal("expected capped request with an aggregated truncated tool call to escalate")
	}
	// runtime 的终态错误带着包装返回，判定必须仍然可达。
	if !shouldEscalateTruncatedToolCallBudget(req, fmt.Errorf("LLM call failed after retries: %w", truncated)) {
		t.Fatal("expected wrapped aggregated truncated tool call to escalate")
	}

	req.Metadata = map[string]interface{}{"max_output_tokens_escalated": true}
	if shouldEscalateTruncatedToolCallBudget(req, truncated) {
		t.Fatal("did not expect a second escalate after the flag is set")
	}
	req.Metadata = nil
	req.MaxTokens = llm.EscalatedMaxTokens
	if shouldEscalateTruncatedToolCallBudget(req, truncated) {
		t.Fatal("did not expect escalate when the budget is already widened")
	}

	// 其它失败类别不受影响：空回复、传输失败、语法退化都不扩大预算。
	req.MaxTokens = llm.CappedDefaultMaxTokens
	for _, other := range []error{
		fmt.Errorf("empty_reply: stream ended without substantive output"),
		fmt.Errorf("transport: connection reset by peer"),
		&llmadapter.MalformedToolCallError{
			Kind:         "openai_stream_protocol_error",
			Code:         "invalid_tool_arguments",
			FinishReason: "tool_calls",
		},
	} {
		if shouldEscalateTruncatedToolCallBudget(req, other) {
			t.Fatalf("did not expect budget escalation for %v", other)
		}
	}
}
