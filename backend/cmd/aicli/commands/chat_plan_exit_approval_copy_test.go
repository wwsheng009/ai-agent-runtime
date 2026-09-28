package commands

import (
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// A model-authored exit_plan_mode approval reaches the CLI as a policy reason
// key; the TUI card must explain what allow/deny do instead of showing the key.
func TestHumanApprovalReasonMapsPlanAutoExit(t *testing.T) {
	approve := humanApprovalReason("plan_mode:model_auto_exit_approve")
	if !strings.Contains(approve, "允许=退出计划模式") || !strings.Contains(approve, "plan_mode:model_auto_exit_approve") {
		t.Fatalf("expected allow/deny copy plus the raw key, got %q", approve)
	}
	quit := humanApprovalReason("plan_mode:model_auto_exit_quit")
	if !strings.Contains(quit, "关闭计划模式") || !strings.Contains(quit, "plan_mode:model_auto_exit_quit") {
		t.Fatalf("expected quit copy plus the raw key, got %q", quit)
	}
	if got := humanApprovalReason("Plan_Mode:Model_Auto_Exit_Approve"); got != approve {
		t.Fatalf("reason matching must be case-insensitive, got %q", got)
	}
}

// The plan-exit approval payload (decision/plan_path/notes) must render as
// readable fields, not a raw JSON dump.
func TestApprovalPreviewRendersPlanExitPayload(t *testing.T) {
	approval := &runtimechat.ApprovalRequest{
		ToolName: "exit_plan_mode",
		ArgsJSON: []byte(`{"decision":"approve","plan_path":"docs/plan/x.md","notes":"ready"}`),
	}
	lines := approvalRequestPreviewLines(approval)
	want := []string{"decision=approve", "plan_path=docs/plan/x.md", "notes=ready"}
	if len(lines) != len(want) {
		t.Fatalf("expected %d preview lines, got %#v", len(want), lines)
	}
	for i, line := range want {
		if lines[i] != line {
			t.Fatalf("preview line %d = %q, want %q", i, lines[i], line)
		}
	}
	rendered := localizeApprovalPreviewLine(lines[1])
	if !strings.Contains(rendered, "计划文件") || !strings.Contains(rendered, "docs/plan/x.md") {
		t.Fatalf("expected a Chinese label for plan_path, got %q", rendered)
	}
	if got := localizeApprovalPreviewLine("decision=approve"); !strings.Contains(got, "裁决") {
		t.Fatalf("expected a Chinese label for decision, got %q", got)
	}
}

// Unrelated tools that happen to carry decision/notes must keep the generic
// preview path (no plan-mode special casing leaks).
func TestApprovalPreviewKeepsGenericForNonPlanPayload(t *testing.T) {
	approval := &runtimechat.ApprovalRequest{
		ToolName: "subagent_ack_lifecycle",
		ArgsJSON: []byte(`{"notification_id":"ntf_1","decision":"defer","note":"later"}`),
	}
	lines := approvalRequestPreviewLines(approval)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "args=") {
		t.Fatalf("expected the generic args fallback, got %#v", lines)
	}
}
