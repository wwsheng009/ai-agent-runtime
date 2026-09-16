package toolbroker

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestApprovalResolutionNotApplied pins which resolutions mean "recorded but
// never applied": every host keys its allowed/resumed reporting off this, and
// the terminal-run classes must not be mistaken for applied decisions (P0-3).
func TestApprovalResolutionNotApplied(t *testing.T) {
	cases := map[string]bool{
		ApprovalResolutionAllowed:             false,
		ApprovalResolutionDenied:              false,
		ApprovalResolutionExpired:             false,
		ApprovalResolutionRunTerminated:       true,
		ApprovalResolutionRunTerminalNoResume: true,
		"":                                    false,
		" allowed ":                           false,
		" run_terminated ":                    true,
	}
	for resolution, expected := range cases {
		assert.Equal(t, expected, ApprovalResolutionNotApplied(resolution), "resolution %q", resolution)
	}
}

// TestAgentApprovalCacheSafeSummaryReportsUnappliedDecisions keeps the model
// facing summary honest: a decision against an already-terminated run must not
// read as "approved" (P0-3).
func TestAgentApprovalCacheSafeSummaryReportsUnappliedDecisions(t *testing.T) {
	cases := []struct {
		name       string
		result     *AgentApprovalResult
		contains   string
		notContain string
	}{
		{
			name:     "allowed decision",
			result:   &AgentApprovalResult{SessionID: "child-1", RequestID: "req-1", Allowed: true, Resolved: true, Resumed: true, Resolution: ApprovalResolutionAllowed},
			contains: "approval approved",
		},
		{
			name:     "denied decision",
			result:   &AgentApprovalResult{SessionID: "child-1", RequestID: "req-1", Resolved: true, Resolution: ApprovalResolutionDenied},
			contains: "approval denied",
		},
		{
			name:     "expired decision",
			result:   &AgentApprovalResult{SessionID: "child-1", RequestID: "req-1", Resolved: true, Resolution: ApprovalResolutionExpired},
			contains: "approval denied",
		},
		{
			name:       "retired with the run",
			result:     &AgentApprovalResult{SessionID: "child-1", RequestID: "req-1", Allowed: false, Resolved: true, Resolution: ApprovalResolutionRunTerminated},
			contains:   "approval not applied",
			notContain: "approval approved",
		},
		{
			name:       "late decision against a terminated run",
			result:     &AgentApprovalResult{SessionID: "child-1", RequestID: "req-1", Allowed: true, Resolved: true, Resolution: ApprovalResolutionRunTerminalNoResume},
			contains:   "approval not applied",
			notContain: "approval approved",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := agentApprovalCacheSafeSummary(tc.result)
			assert.Contains(t, summary, tc.contains)
			if tc.notContain != "" {
				assert.NotContains(t, summary, tc.notContain)
			}
			if ApprovalResolutionNotApplied(tc.result.Resolution) {
				assert.True(t, strings.Contains(summary, "no work was resumed"))
			}
		})
	}

	assert.Equal(t, "", agentApprovalCacheSafeSummary(nil))
}
