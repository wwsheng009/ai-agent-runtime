package toolbroker

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

const (
	AgentMailboxMessageKind           = agentcontrol.MailboxKindAgentMessage
	AgentMailboxFollowupKind          = agentcontrol.MailboxKindFollowupTask
	AgentMailboxMessageType           = agentcontrol.MessageTypeAgentMessage
	AgentMailboxFollowupMessageType   = agentcontrol.MessageTypeFollowupTask
	AgentMailboxMessageAction         = agentcontrol.ActionAgentMessage
	AgentMailboxFollowupAction        = agentcontrol.ActionAgentFollowupTask
	AgentMailboxWorkflow              = agentcontrol.WorkflowSpawnAgent
	AgentMailboxDeliverySessionStore  = agentcontrol.DeliverySessionMailbox
	SubagentCompletionMailboxKind     = agentcontrol.MailboxKindSubagentCompleted
	SubagentCompletionMessageType     = agentcontrol.MessageTypeSubagentCompleted
	SubagentCompletionAction          = agentcontrol.ActionAgentCompleted
	SubagentCompletionMirrorSource    = "agent_control_mailbox"
	SubagentBatchTerminalMirrorSource = "subagent_batch_terminal_mailbox"
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
		if seq, ok := payload["source_event_seq"]; ok {
			metadata["event_seq"] = seq
		} else if seq, ok := payload["seq"]; ok {
			metadata["event_seq"] = seq
		}
		for _, key := range []string{
			"requested_provider",
			"effective_provider",
			"requested_model",
			"effective_model",
			"requested_reasoning_effort",
			"effective_reasoning_effort",
			"requested_permission_mode",
			"effective_permission_mode",
			"difficulty",
			"difficulty_source",
			"difficulty_rationale",
			"route_provider",
			"route_model",
			"route_reasoning_effort",
			"route_source",
			"route_warnings",
			"fallback_used",
			"fallback_reason",
			"usage_prompt_tokens",
			"usage_completion_tokens",
			"usage_total_tokens",
			"usage_cached_tokens",
			"usage_cache_read_tokens",
			"usage_cache_creation_tokens",
			"usage_cache_read_reported",
			"usage_cache_status",
			"usage_reasoning_tokens",
		} {
			if value, ok := payload[key]; ok {
				metadata[key] = value
			}
		}
	}
	deliveryKey := subagentCompletionDeliveryKey(parentSessionID, childSessionID, sourceEventType, payload)
	metadata["delivery_key"] = deliveryKey
	status := "completed"
	if value, ok := metadata["status"].(string); ok && strings.TrimSpace(value) != "" {
		status = strings.TrimSpace(value)
	}
	return team.MailMessage{
		ID:        deliveryKey,
		FromAgent: childSessionID,
		ToAgent:   "parent",
		Kind:      SubagentCompletionMailboxKind,
		Body:      fmt.Sprintf("Subagent %s completed with status %s.", childSessionID, status),
		Metadata:  metadata,
		CreatedAt: time.Now().UTC(),
	}
}

// BuildSubagentBatchTerminalMailboxMessage creates the durable AgentControl
// mailbox record for a background spawn_subagents batch. deliveryKey is
// deterministic and becomes the message id so store retries can deduplicate it.
func BuildSubagentBatchTerminalMailboxMessage(parentSessionID, batchID, sourceEventType, deliveryKey string, payload map[string]interface{}) team.MailMessage {
	parentSessionID = strings.TrimSpace(parentSessionID)
	batchID = strings.TrimSpace(batchID)
	deliveryKey = strings.TrimSpace(deliveryKey)
	metadata := agentcontrol.Envelope{
		MessageType:     SubagentCompletionMessageType,
		ControlAction:   SubagentCompletionAction,
		Workflow:        "spawn_subagents",
		MailboxDelivery: AgentMailboxDeliverySessionStore,
		MailboxKind:     SubagentCompletionMailboxKind,
	}.Metadata()
	metadata["parent_session_id"] = parentSessionID
	metadata["batch_id"] = batchID
	metadata["source_event_type"] = strings.TrimSpace(sourceEventType)
	metadata["delivery_key"] = deliveryKey
	for _, key := range []string{
		"parent_turn_id", "parent_tool_call_id", "trace_id", "root_scope_id",
		"execution_mode", "status", "task_count", "completed_count",
		"failed_count", "canceled_count", "timed_out_count", "elapsed_ms",
		"error", "error_class", "cancel_reason", "cancel_requested_at",
	} {
		if value, ok := payload[key]; ok {
			metadata[key] = value
		}
	}
	status := strings.TrimSpace(fmt.Sprint(metadata["status"]))
	if status == "" {
		status = "completed"
	}
	return team.MailMessage{
		ID:        deliveryKey,
		FromAgent: "subagent-batch:" + batchID,
		ToAgent:   "parent",
		Kind:      SubagentCompletionMailboxKind,
		Body:      fmt.Sprintf("Subagent batch %s completed with status %s.", batchID, status),
		Metadata:  metadata,
		CreatedAt: time.Now().UTC(),
	}
}

func subagentCompletionDeliveryKey(parentSessionID, childSessionID, sourceEventType string, payload map[string]interface{}) string {
	terminalIdentity := ""
	for _, key := range []string{"source_event_seq", "seq", "source_event_trace_id", "source_event_timestamp"} {
		if payload == nil {
			break
		}
		if value, ok := payload[key]; ok && strings.TrimSpace(fmt.Sprint(value)) != "" {
			terminalIdentity = key + ":" + strings.TrimSpace(fmt.Sprint(value))
			break
		}
	}
	if terminalIdentity == "" && payload != nil {
		terminalIdentity = "status:" + strings.TrimSpace(fmt.Sprint(payload["status"]))
	}
	raw := strings.Join([]string{
		strings.TrimSpace(parentSessionID),
		strings.TrimSpace(childSessionID),
		strings.TrimSpace(sourceEventType),
		terminalIdentity,
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("subagent_completion_%x", sum[:16])
}

// AnnotateSubagentCompletionDisplayMirror marks the legacy subagent.completed
// session event as a display-only mirror of the durable AgentControl mailbox
// message. The mailbox message remains the primary control-plane record.
func AnnotateSubagentCompletionDisplayMirror(payload map[string]interface{}, message team.MailMessage, deliveryErr error) map[string]interface{} {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	payload["display_mirror"] = true
	payload["mirror_source"] = SubagentCompletionMirrorSource
	payload["mailbox_delivery_status"] = "delivered"
	if deliveryErr != nil {
		payload["mailbox_delivery_status"] = "failed"
		payload["mailbox_delivery_error"] = deliveryErr.Error()
	}
	if id := strings.TrimSpace(message.ID); id != "" {
		payload["mailbox_message_id"] = id
	}
	if kind := strings.TrimSpace(message.Kind); kind != "" {
		payload["mailbox_kind"] = kind
	}
	if value := agentcontrol.MetadataString(message.Metadata, "message_type"); value != "" {
		payload["message_type"] = value
	}
	if value := agentcontrol.MetadataString(message.Metadata, "control_action"); value != "" {
		payload["control_action"] = value
	}
	if value := agentcontrol.MetadataString(message.Metadata, "workflow"); value != "" {
		payload["workflow"] = value
	}
	if value := agentcontrol.MetadataString(message.Metadata, "mailbox_delivery"); value != "" {
		payload["mailbox_delivery"] = value
	}
	if value := agentcontrol.MetadataString(message.Metadata, "mailbox_kind"); value != "" {
		payload["mailbox_kind"] = value
	}
	return payload
}
