// Package agentstatus holds the host-neutral projection from a session runtime
// state onto the AgentControl status wire type.
package agentstatus

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// ApplyRuntimeState copies the decision-relevant fields of a runtime state
// summary onto an AgentStatusResult. Both runtime-server and aicli use it for
// live actor state and for the durable fallback after a restart, so the same
// parked approval/question cannot project differently per host.
func ApplyRuntimeState(result *toolbroker.AgentStatusResult, state chat.RuntimeStateSummary) {
	if result == nil {
		return
	}
	result.Status = string(state.Status)
	result.PendingApproval = state.PendingApproval
	if state.PendingApproval {
		result.PendingApprovalID = strings.TrimSpace(state.PendingApprovalID)
		result.PendingApprovalReason = strings.TrimSpace(state.PendingApprovalReason)
		result.PendingApprovalRiskLevel = strings.TrimSpace(state.PendingApprovalRiskLevel)
	}
	result.PendingQuestion = state.PendingQuestion
	result.CurrentTurnID = strings.TrimSpace(state.CurrentTurnID)
	if state.PendingTool {
		result.PendingToolName = strings.TrimSpace(state.PendingToolName)
		result.PendingToolCallID = strings.TrimSpace(state.PendingToolCallID)
	}
}
