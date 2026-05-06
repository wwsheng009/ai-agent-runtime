package toolbroker

import (
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

const (
	AgentMailboxMessageKind          = "agent_message"
	AgentMailboxFollowupKind         = "followup_task"
	AgentMailboxMessageType          = "agent_control.agent_message"
	AgentMailboxFollowupMessageType  = "agent_control.followup_task"
	AgentMailboxMessageAction        = "agent.message"
	AgentMailboxFollowupAction       = "agent.followup_task"
	AgentMailboxWorkflow             = "spawn_agent"
	AgentMailboxDeliverySessionStore = "session_mailbox"
)

// BuildAgentMailboxMessage creates the mailbox envelope used by send_message
// and followup_task when a child agent cannot or should not be interrupted.
func BuildAgentMailboxMessage(fromSessionID, targetSessionID, message string, trigger bool) team.MailMessage {
	kind := AgentMailboxMessageKind
	messageType := AgentMailboxMessageType
	controlAction := AgentMailboxMessageAction
	if trigger {
		kind = AgentMailboxFollowupKind
		messageType = AgentMailboxFollowupMessageType
		controlAction = AgentMailboxFollowupAction
	}
	targetSessionID = strings.TrimSpace(targetSessionID)
	return team.MailMessage{
		FromAgent: strings.TrimSpace(fromSessionID),
		ToAgent:   targetSessionID,
		Kind:      kind,
		Body:      strings.TrimSpace(message),
		CreatedAt: time.Now().UTC(),
		Metadata: map[string]interface{}{
			"message_type":      messageType,
			"control_action":    controlAction,
			"workflow":          AgentMailboxWorkflow,
			"mailbox_delivery":  AgentMailboxDeliverySessionStore,
			"mailbox_kind":      kind,
			"from_session_id":   strings.TrimSpace(fromSessionID),
			"target_session_id": targetSessionID,
			"trigger_turn":      trigger,
		},
	}
}
