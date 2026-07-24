package commands

import (
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
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

func TestFormatChatCompactReport_SuccessIncludesCompactLineage(t *testing.T) {
	report := &chatCompactReport{
		Result: &compactruntime.Result{
			Mode:               compactruntime.ModeLocal,
			TokenBefore:        900,
			TokenAfter:         120,
			CompactedMessages:  4,
			ReplacementHistory: []types.Message{},
		},
		Generation: 2,
		Title:      "检查登录流程为什么失败 · compact #2",
		RootTitle:  "检查登录流程为什么失败",
	}

	output := formatChatCompactReport(report)
	if !strings.Contains(output, "generation=2") {
		t.Fatalf("expected generation in compact report, got %q", output)
	}
	if !strings.Contains(output, "title=检查登录流程为什么失败 · compact #2") {
		t.Fatalf("expected title in compact report, got %q", output)
	}
	if strings.Contains(output, "root_title=") {
		t.Fatalf("did not expect root_title when title is present, got %q", output)
	}
}

func TestAttachChatCompactLineageReadsRuntimeSession(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.Metadata.Title = "检查登录流程为什么失败 · compact #1"
	runtimeSession.Metadata.Context = map[string]interface{}{
		runtimechat.ContextCompactGeneration: 1,
		runtimechat.ContextCompactRootTitle:  "检查登录流程为什么失败",
	}
	report := &chatCompactReport{}
	attachChatCompactLineage(report, &ChatSession{RuntimeSession: runtimeSession})
	if report.Generation != 1 {
		t.Fatalf("expected generation 1, got %d", report.Generation)
	}
	if report.RootTitle != "检查登录流程为什么失败" {
		t.Fatalf("expected root title, got %q", report.RootTitle)
	}
	if report.Title != "检查登录流程为什么失败 · compact #1" {
		t.Fatalf("expected session title, got %q", report.Title)
	}
}
