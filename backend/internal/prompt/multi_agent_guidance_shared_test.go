package prompt

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentguidance"
)

// TestRenderMultiAgentCollaborationGuidanceUsesSharedRules pins the prompt side
// of the shared discipline: the wait rules must come from internal/agentguidance
// (the same constants the collaboration tool descriptions use) instead of a
// private copy, so the prompt and the tool schema cannot drift apart again.
func TestRenderMultiAgentCollaborationGuidanceUsesSharedRules(t *testing.T) {
	got := RenderMultiAgentCollaborationGuidance()
	for _, rule := range []string{agentguidance.WaitBudgetRule, agentguidance.WaitEscalationRule} {
		if !strings.Contains(got, "- "+rule) {
			t.Fatalf("guidance must render the shared rule %q as its own bullet, got:\n%s", rule, got)
		}
	}
}
