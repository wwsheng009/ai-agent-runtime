package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestFormatChatCompactReport_MissingModelCapabilityIncludesConfigHint(t *testing.T) {
	report := &chatCompactReport{
		RequestedMode: compactruntime.ModeLocal,
		Status: compactruntime.Status{
			Mode:             compactruntime.ModeLocal,
			Reason:           "missing_model_capability",
			ResolvedProvider: "codex_cli_gpt",
			ResolvedModel:    "gpt-5.5",
			TokenBefore:      3743238,
		},
	}

	output := formatChatCompactReport(report)
	if !strings.Contains(output, "reason=missing_model_capability") {
		t.Fatalf("expected missing_model_capability reason, got %q", output)
	}
	if !strings.Contains(output, "`providers.items.codex_cli_gpt.model_capabilities.gpt-5.5`") {
		t.Fatalf("expected concrete model capability path hint, got %q", output)
	}
	if !strings.Contains(output, "`max_context_tokens` / `auto_compact_token_limit`") {
		t.Fatalf("expected compact config fields hint, got %q", output)
	}
	if !strings.Contains(output, "token_source="+compactTokenSourceObservedUsage) {
		t.Fatalf("expected token source hint, got %q", output)
	}
}

func TestFormatChatCompactReport_SuccessIncludesObservedUsageTokenSource(t *testing.T) {
	report := &chatCompactReport{
		Result: &compactruntime.Result{
			Mode:               compactruntime.ModeLocal,
			TokenBefore:        900,
			TokenAfter:         120,
			CompactedMessages:  4,
			ReplacementHistory: []types.Message{},
		},
	}

	output := formatChatCompactReport(report)
	if !strings.Contains(output, "token_source="+compactTokenSourceObservedUsage) {
		t.Fatalf("expected token source hint, got %q", output)
	}
}
