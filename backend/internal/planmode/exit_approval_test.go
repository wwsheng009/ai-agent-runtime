package planmode

import "testing"

func TestExitApprovalReasonMapsModelDecisions(t *testing.T) {
	cases := map[ExitDecision]string{
		ExitApprove:        ExitApprovalReasonApprove,
		ExitQuit:           ExitApprovalReasonQuit,
		ExitRequestChanges: "",
		ExitNone:           "",
	}
	for decision, want := range cases {
		if got := ExitApprovalReason(decision); got != want {
			t.Fatalf("ExitApprovalReason(%q) = %q, want %q", decision, got, want)
		}
	}
}
