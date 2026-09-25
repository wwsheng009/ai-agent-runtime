package commands

import (
	"strings"
	"testing"
)

// The plan auto-enter confirmation reaches the CLI as a policy reason key; the
// approval prompt must show what the model is asking for, not the raw key.
func TestHumanApprovalReasonMapsPlanAutoEnter(t *testing.T) {
	text := humanApprovalReason("plan_mode:model_auto_enter")
	if !strings.Contains(text, "计划模式") {
		t.Fatalf("expected the plan-mode confirmation copy, got %q", text)
	}
	if !strings.Contains(text, "plan_mode:model_auto_enter") {
		t.Fatalf("expected the raw key to stay visible for support, got %q", text)
	}
	if got := humanApprovalReason("Plan_Mode:Model_Auto_Enter"); got != text {
		t.Fatalf("reason matching must be case-insensitive, got %q", got)
	}
	// Unknown reasons stay verbatim so nothing is hidden from the user.
	if got := humanApprovalReason("custom_reason"); got != "custom_reason" {
		t.Fatalf("unknown reason must be echoed, got %q", got)
	}
}
