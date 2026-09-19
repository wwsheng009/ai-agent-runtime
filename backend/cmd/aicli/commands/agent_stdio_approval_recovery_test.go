package commands

import (
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestDecideACPRestoredApproval(t *testing.T) {
	now := time.Date(2026, 9, 19, 13, 47, 0, 0, time.UTC)
	live := &runtimechat.ApprovalRequest{ID: "approval_live", ToolName: "apply_patch", ExpiresAt: now.Add(time.Minute)}
	expired := &runtimechat.ApprovalRequest{ID: "approval_expired", ToolName: "apply_patch", ExpiresAt: now.Add(-time.Minute)}
	noExpiry := &runtimechat.ApprovalRequest{ID: "approval_no_expiry", ToolName: "apply_patch"}

	cases := []struct {
		name     string
		mode     runtimepolicy.Mode
		approval *runtimechat.ApprovalRequest
		want     acpRestoredApprovalDecision
	}{
		{"yolo approves expired approval", runtimepolicy.ModeBypassPermissions, expired, acpRestoredApprovalAllow},
		{"yolo approves live approval", runtimepolicy.ModeBypassPermissions, live, acpRestoredApprovalAllow},
		{"default asks while the window is open", runtimepolicy.ModeDefault, live, acpRestoredApprovalAsk},
		{"accept edits asks while the window is open", runtimepolicy.ModeAcceptEdits, live, acpRestoredApprovalAsk},
		{"plan asks while the window is open", runtimepolicy.ModePlan, live, acpRestoredApprovalAsk},
		{"default denies an expired approval", runtimepolicy.ModeDefault, expired, acpRestoredApprovalDeny},
		{"accept edits denies an expired approval", runtimepolicy.ModeAcceptEdits, expired, acpRestoredApprovalDeny},
		{"missing expiry asks", runtimepolicy.ModeDefault, noExpiry, acpRestoredApprovalAsk},
		{"no approval asks", runtimepolicy.ModeDefault, nil, acpRestoredApprovalAsk},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideACPRestoredApproval(tc.mode, tc.approval, now); got != tc.want {
				t.Fatalf("decideACPRestoredApproval(%q, %v) = %v, want %v", tc.mode, tc.approval, got, tc.want)
			}
		})
	}
}
