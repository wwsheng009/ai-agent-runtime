package toolbroker

import (
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

const (
	AgentMailboxMessageKind          = agentcontrol.MailboxKindAgentMessage
	AgentMailboxFollowupKind         = agentcontrol.MailboxKindFollowupTask
	AgentMailboxMessageType          = agentcontrol.MessageTypeAgentMessage
	AgentMailboxFollowupMessageType  = agentcontrol.MessageTypeFollowupTask
	AgentMailboxMessageAction        = agentcontrol.ActionAgentMessage
	AgentMailboxFollowupAction       = agentcontrol.ActionAgentFollowupTask
	AgentMailboxWorkflow             = agentcontrol.WorkflowSpawnAgent
	AgentMailboxDeliverySessionStore = agentcontrol.DeliverySessionMailbox
	SubagentCompletionMailboxKind    = agentcontrol.MailboxKindSubagentCompleted
	SubagentCompletionMessageType    = agentcontrol.MessageTypeSubagentCompleted
	SubagentCompletionAction         = agentcontrol.ActionAgentCompleted
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
	metadata := agentcontrol.Envelope{
		MessageType:     messageType,
		ControlAction:   controlAction,
		Workflow:        AgentMailboxWorkflow,
		MailboxDelivery: AgentMailboxDeliverySessionStore,
		MailboxKind:     kind,
	}.Metadata()
	metadata["from_session_id"] = strings.TrimSpace(fromSessionID)
	metadata["target_session_id"] = targetSessionID
	metadata["trigger_turn"] = trigger
	return team.MailMessage{
		FromAgent: strings.TrimSpace(fromSessionID),
		ToAgent:   targetSessionID,
		Kind:      kind,
		Body:      strings.TrimSpace(message),
		CreatedAt: time.Now().UTC(),
		Metadata:  metadata,
	}
}

// BuildSubagentCompletionMailboxMessage creates the durable AgentControl
// mailbox envelope used to notify a parent session that a child agent reached a
// terminal state.
func BuildSubagentCompletionMailboxMessage(parentSessionID, childSessionID, childPath, childType, sourceEventType string, payload map[string]interface{}) team.MailMessage {
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID = strings.TrimSpace(childSessionID)
	metadata := agentcontrol.Envelope{
		MessageType:     SubagentCompletionMessageType,
		ControlAction:   SubagentCompletionAction,
		Workflow:        AgentMailboxWorkflow,
		MailboxDelivery: AgentMailboxDeliverySessionStore,
		MailboxKind:     SubagentCompletionMailboxKind,
	}.Metadata()
	for key, value := range map[string]interface{}{
		"session_id":        childSessionID,
		"parent_session_id": parentSessionID,
		"path":              strings.TrimSpace(childPath),
		"source_event_type": strings.TrimSpace(sourceEventType),
	} {
		metadata[key] = value
	}
	if childType = strings.TrimSpace(childType); childType != "" {
		metadata["agent_type"] = childType
	}
	if payload != nil {
		if status, ok := payload["status"]; ok {
			metadata["status"] = status
		}
		if success, ok := payload["success"]; ok {
			metadata["success"] = success
		}
		if errText, ok := payload["error"]; ok {
			metadata["error"] = errText
		}
		if seq, ok := payload["seq"]; ok {
			metadata["event_seq"] = seq
		}
	}
	status := "completed"
	if value, ok := metadata["status"].(string); ok && strings.TrimSpace(value) != "" {
		status = strings.TrimSpace(value)
	}
	return team.MailMessage{
		FromAgent: childSessionID,
		ToAgent:   "parent",
		Kind:      SubagentCompletionMailboxKind,
		Body:      fmt.Sprintf("Subagent %s completed with status %s.", childSessionID, status),
		Metadata:  metadata,
		CreatedAt: time.Now().UTC(),
	}
}
