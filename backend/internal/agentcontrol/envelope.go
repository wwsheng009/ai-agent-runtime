package agentcontrol

import "strings"

const (
	WorkflowSpawnAgent = "spawn_agent"
	WorkflowSpawnTeam  = "spawn_team"

	DeliverySessionMailbox = "session_mailbox"

	MailboxKindAgentMessage       = "agent_message"
	MailboxKindFollowupTask       = "followup_task"
	MailboxKindSubagentCompleted  = "subagent.completed"
	MailboxKindTeamTaskAssignment = "team.task_assignment"
	MailboxKindTeamTaskLifecycle  = "team.task_lifecycle"
	MailboxKindTeamLifecycle      = "team.lifecycle"

	MessageTypeAgentMessage       = "agent_control.agent_message"
	MessageTypeFollowupTask       = "agent_control.followup_task"
	MessageTypeSubagentCompleted  = "agent_control.subagent_completed"
	MessageTypeTeamTaskAssignment = "agent_control.task_assignment"
	MessageTypeTeamTaskLifecycle  = "agent_control.task_lifecycle"
	MessageTypeTeamLifecycle      = "agent_control.team_lifecycle"

	ActionAgentMessage      = "agent.message"
	ActionAgentFollowupTask = "agent.followup_task"
	ActionAgentCompleted    = "agent.completed"
	ActionTaskAssign        = "task.assign"
	ActionTaskLifecycle     = "task.lifecycle"
	ActionTeamLifecycle     = "team.lifecycle"

	ViaTriggerTask = "agent_control.trigger_task"
)

const (
	MetadataKeyMessageType     = "message_type"
	MetadataKeyControlAction   = "control_action"
	MetadataKeyWorkflow        = "workflow"
	MetadataKeyMailboxDelivery = "mailbox_delivery"
	MetadataKeyMailboxKind     = "mailbox_kind"
)

// Envelope is the small, shared control-plane contract persisted with mailbox
// messages. It intentionally stays storage-neutral so current session mailbox
// rows and a future AgentControl mailbox table can consume the same metadata.
type Envelope struct {
	MessageType     string
	ControlAction   string
	Workflow        string
	MailboxDelivery string
	MailboxKind     string
}

// Metadata returns the canonical metadata keys for an AgentControl envelope.
func (e Envelope) Metadata() map[string]interface{} {
	metadata := map[string]interface{}{}
	if e.MessageType != "" {
		metadata[MetadataKeyMessageType] = e.MessageType
	}
	if e.ControlAction != "" {
		metadata[MetadataKeyControlAction] = e.ControlAction
	}
	if e.Workflow != "" {
		metadata[MetadataKeyWorkflow] = e.Workflow
	}
	if e.MailboxDelivery != "" {
		metadata[MetadataKeyMailboxDelivery] = e.MailboxDelivery
	}
	if e.MailboxKind != "" {
		metadata[MetadataKeyMailboxKind] = e.MailboxKind
	}
	return metadata
}

// ApplyEnvelope adds the canonical envelope fields to metadata and returns the
// same map. Existing domain-specific fields are preserved unless they are one
// of the AgentControl envelope keys.
func ApplyEnvelope(metadata map[string]interface{}, envelope Envelope) map[string]interface{} {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	for key, value := range envelope.Metadata() {
		metadata[key] = value
	}
	return metadata
}

// MetadataString extracts a trimmed string metadata field.
func MetadataString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 || strings.TrimSpace(key) == "" {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// HasEnvelopeMetadata reports whether metadata carries any canonical
// AgentControl envelope key.
func HasEnvelopeMetadata(metadata map[string]interface{}) bool {
	return MetadataString(metadata, MetadataKeyMessageType) != "" ||
		MetadataString(metadata, MetadataKeyControlAction) != "" ||
		MetadataString(metadata, MetadataKeyWorkflow) != "" ||
		MetadataString(metadata, MetadataKeyMailboxDelivery) != "" ||
		MetadataString(metadata, MetadataKeyMailboxKind) != ""
}
