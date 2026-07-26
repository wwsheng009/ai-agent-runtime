package agent

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
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
