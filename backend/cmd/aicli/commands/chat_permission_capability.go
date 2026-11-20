package commands

import "github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"

// chatReadOnlyApprovalBypassedLabel is intentionally separate from the
// permission-mode labels. Permission mode describes the approval behavior;
// this label also reports the effective read-only capability boundary.
const chatReadOnlyApprovalBypassedLabel = "Read-only · Approval bypassed"

// chatSessionEffectiveReadOnly reports the effective read-only boundary already
// resolved by the runtime host. It deliberately does not infer read-only from
// an agent type such as "explore": the type is descriptive, while the durable
// read_only context flag is the policy fact enforced by the actor/tool policy.
//
// Use the synchronized context helper rather than reading
// RuntimeSession.Metadata.Context directly. This function is presentation-only;
// tool execution remains enforced by the runtime agent policy.
func chatSessionEffectiveReadOnly(session *ChatSession) bool {
	if session == nil {
		return false
	}
	readOnly, ok := chatRuntimeContextBool(session, toolbroker.AgentSessionContextReadOnly)
	return ok && readOnly
}
