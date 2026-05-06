package team

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// TaskDispatchRequestedEvent records that a team task is being submitted to
	// a teammate session through the agent-control trigger surface.
	TaskDispatchRequestedEvent = "task.dispatch.requested"
	// TaskDispatchCompletedEvent records the submit result for a teammate task.
	TaskDispatchCompletedEvent             = "task.dispatch.completed"
	TaskAssignmentMailboxKind              = "team.task_assignment"
	TaskAssignmentControlMessageType       = "agent_control.task_assignment"
	TaskAssignmentControlAction            = "task.assign"
	TaskAssignmentWorkflow                 = "spawn_team"
	TeamLifecycleMailboxKind               = "team.lifecycle"
	TeamLifecycleControlMessageType        = "agent_control.team_lifecycle"
	taskDispatchViaAgentControl            = "agent_control.trigger_task"
	taskDispatchMailboxDeliveryAgentSubstr = "session_mailbox"
)

// AppendTaskDispatchRequested persists an audit event for a teammate task
// dispatch request. Missing stores or team IDs are treated as no-op so task
// execution is not coupled to observability persistence.
func AppendTaskDispatchRequested(ctx context.Context, store Store, request TaskTriggerRequest) (int64, error) {
	return appendTaskDispatchEvent(ctx, store, TaskDispatchRequestedEvent, request, nil, nil)
}

// AppendTaskDispatchCompleted persists an audit event for a teammate task
// dispatch result. Missing stores or team IDs are treated as no-op so task
// execution is not coupled to observability persistence.
func AppendTaskDispatchCompleted(ctx context.Context, store Store, request TaskTriggerRequest, result *SessionResult, dispatchErr error) (int64, error) {
	return appendTaskDispatchEvent(ctx, store, TaskDispatchCompletedEvent, request, result, dispatchErr)
}

// BuildTaskAssignmentMailboxMessage creates a durable mailbox envelope for the
// teammate session receiving a team task assignment.
func BuildTaskAssignmentMailboxMessage(request TaskTriggerRequest) MailMessage {
	teamID := taskDispatchTeamID(request)
	agentID := taskDispatchAgentID(request)
	taskID := taskDispatchTaskID(request)
	toAgent := firstNonEmptyString(agentID, strings.TrimSpace(request.SessionID))
	body := "Team task assigned."
	if taskID != "" {
		body = "Team task " + taskID + " assigned."
	}
	metadata := taskDispatchPayload(request)
	metadata["message_type"] = TaskAssignmentControlMessageType
	metadata["control_action"] = TaskAssignmentControlAction
	metadata["workflow"] = TaskAssignmentWorkflow
	metadata["mailbox_delivery"] = taskDispatchMailboxDeliveryAgentSubstr
	metadata["mailbox_kind"] = TaskAssignmentMailboxKind
	metadata["target_session_id"] = strings.TrimSpace(request.SessionID)
	if prompt := truncateLine(request.Prompt, 240); prompt != "" {
		metadata["prompt_preview"] = prompt
	}
	var taskIDPtr *string
	if taskID != "" {
		taskIDValue := taskID
		taskIDPtr = &taskIDValue
	}
	return MailMessage{
		TeamID:    teamID,
		FromAgent: "team-orchestrator",
		ToAgent:   toAgent,
		TaskID:    taskIDPtr,
		Kind:      TaskAssignmentMailboxKind,
		Body:      body,
		Metadata:  metadata,
		CreatedAt: time.Now().UTC(),
	}
}

// BuildTeamLifecycleMailboxMessage creates a durable mailbox envelope for a
// terminal team lifecycle event delivered to a parent/lead session.
func BuildTeamLifecycleMailboxMessage(event TeamEvent) MailMessage {
	eventType := strings.TrimSpace(event.Type)
	teamID := strings.TrimSpace(event.TeamID)
	metadata := map[string]interface{}{
		"message_type":     TeamLifecycleControlMessageType,
		"workflow":         TaskAssignmentWorkflow,
		"mailbox_delivery": taskDispatchMailboxDeliveryAgentSubstr,
		"mailbox_kind":     TeamLifecycleMailboxKind,
		"event_type":       eventType,
		"team_id":          teamID,
	}
	for key, value := range event.Payload {
		if _, exists := metadata[key]; !exists {
			metadata[key] = value
		}
	}
	body := "Team lifecycle event."
	switch eventType {
	case "team.completed":
		body = "Team completed."
		if status, _ := metadata["status"].(string); strings.TrimSpace(status) != "" {
			body = "Team completed with status " + strings.TrimSpace(status) + "."
		}
	case "team.summary":
		body = "Team summary available."
		if summary, _ := metadata["summary"].(string); strings.TrimSpace(summary) != "" {
			body = truncateLine(strings.TrimSpace(summary), 240)
		}
	}
	createdAt := event.Timestamp
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return MailMessage{
		TeamID:    teamID,
		FromAgent: "team-orchestrator",
		ToAgent:   "parent",
		Kind:      TeamLifecycleMailboxKind,
		Body:      body,
		Metadata:  metadata,
		CreatedAt: createdAt,
	}
}

func appendTaskDispatchEvent(ctx context.Context, store Store, eventType string, request TaskTriggerRequest, result *SessionResult, dispatchErr error) (int64, error) {
	if store == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	teamID := taskDispatchTeamID(request)
	if teamID == "" {
		return 0, nil
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return 0, nil
	}
	payload := taskDispatchPayload(request)
	if eventType == TaskDispatchCompletedEvent {
		appendTaskDispatchResultPayload(payload, result, dispatchErr)
	}
	return store.AppendTeamEvent(ctx, TeamEvent{
		Type:      eventType,
		TeamID:    teamID,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	})
}

func taskDispatchPayload(request TaskTriggerRequest) map[string]interface{} {
	teamID := taskDispatchTeamID(request)
	agentID := taskDispatchAgentID(request)
	taskID := taskDispatchTaskID(request)
	payload := map[string]interface{}{
		"team_id":      teamID,
		"agent_id":     agentID,
		"assignee":     agentID,
		"task_id":      taskID,
		"session_id":   strings.TrimSpace(request.SessionID),
		"prompt_chars": utf8.RuneCountInString(request.Prompt),
		"via":          taskDispatchViaAgentControl,
	}
	if request.RunMeta != nil {
		if permissionMode := strings.TrimSpace(request.RunMeta.PermissionMode); permissionMode != "" {
			payload["permission_mode"] = permissionMode
		}
	}
	return payload
}

func appendTaskDispatchResultPayload(payload map[string]interface{}, result *SessionResult, dispatchErr error) {
	success := false
	if result != nil {
		success = result.Success
		if traceID := strings.TrimSpace(result.TraceID); traceID != "" {
			payload["trace_id"] = traceID
		}
		if result.Steps > 0 {
			payload["steps"] = result.Steps
		}
		if result.Output != "" {
			payload["output_chars"] = utf8.RuneCountInString(result.Output)
		}
		if sessionError := strings.TrimSpace(result.Error); sessionError != "" {
			payload["session_error"] = truncateLine(sessionError, 240)
		}
		if errorType := strings.TrimSpace(result.ErrorType); errorType != "" {
			payload["error_type"] = errorType
		}
		for key, value := range result.ErrorMetadata {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if _, exists := payload[key]; exists {
				continue
			}
			payload[key] = value
		}
	}
	if dispatchErr != nil {
		success = false
		payload["error"] = truncateLine(dispatchErr.Error(), 240)
		if sessionErr, ok := AsSessionExecutionError(dispatchErr); ok && sessionErr != nil {
			if traceID := strings.TrimSpace(sessionErr.TraceID); traceID != "" {
				payload["trace_id"] = traceID
			}
			if errorType := strings.TrimSpace(sessionErr.ErrorType); errorType != "" {
				payload["error_type"] = errorType
			}
			for key, value := range sessionErr.CloneMetadata() {
				key = strings.TrimSpace(key)
				if key == "" {
					continue
				}
				if _, exists := payload[key]; exists {
					continue
				}
				payload[key] = value
			}
		}
	}
	payload["success"] = success
}

func taskDispatchTeamID(request TaskTriggerRequest) string {
	if teamID := strings.TrimSpace(request.TeamID); teamID != "" {
		return teamID
	}
	if request.RunMeta != nil && request.RunMeta.Team != nil {
		return strings.TrimSpace(request.RunMeta.Team.TeamID)
	}
	return ""
}

func taskDispatchAgentID(request TaskTriggerRequest) string {
	if agentID := strings.TrimSpace(request.AgentID); agentID != "" {
		return agentID
	}
	if request.RunMeta != nil && request.RunMeta.Team != nil {
		return strings.TrimSpace(request.RunMeta.Team.AgentID)
	}
	return ""
}

func taskDispatchTaskID(request TaskTriggerRequest) string {
	if taskID := strings.TrimSpace(request.TaskID); taskID != "" {
		return taskID
	}
	if request.RunMeta != nil && request.RunMeta.Team != nil {
		return strings.TrimSpace(request.RunMeta.Team.CurrentTaskID)
	}
	return ""
}
