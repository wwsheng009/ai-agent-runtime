package team

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// TaskOutcomeApplyServices groups shared collaborators used to apply task outcomes.
type TaskOutcomeApplyServices struct {
	Store   Store
	Claims  *PathClaimManager
	Mailbox *MailboxService
	Planner *LeadPlanner
	Events  *TeamEventBus
}

// BlockedTaskOutcomeRequest describes a blocked or handoff task outcome application.
type BlockedTaskOutcomeRequest struct {
	Team            Team
	Task            Task
	TeammateID      string
	Outcome         TaskOutcomeContract
	NotifyRecipient *bool
	AutoReplan      *bool
	SkipStateUpdate bool
}

// BlockedTaskOutcomeResult captures the side effects produced by a blocked or handoff outcome.
type BlockedTaskOutcomeResult struct {
	Task                Task
	Outcome             TaskOutcomeContract
	Summary             string
	HandoffTo           string
	Message             *MailMessage
	AutoReplan          bool
	PlanResult          *PlanResult
	ReplanError         string
	ReplanTraceID       string
	ReplanErrorType     string
	ReplanErrorMetadata map[string]interface{}
}

// Replanned reports whether follow-up work was created.
func (r *BlockedTaskOutcomeResult) Replanned() bool {
	if r == nil || r.PlanResult == nil {
		return false
	}
	return len(r.PlanResult.Tasks) > 0
}

// TerminalTaskOutcomeRequest describes a done or failed task outcome application.
type TerminalTaskOutcomeRequest struct {
	Task            Task
	TeammateID      string
	Outcome         TaskOutcomeContract
	ResultRef       *string
	DefaultStatus   TaskOutcomeStatus
	SkipStateUpdate bool
	TraceID         string
	Error           string
	ErrorType       string
	ErrorMetadata   map[string]interface{}
}

// TerminalTaskOutcomeResult captures side effects produced by a done or failed outcome.
type TerminalTaskOutcomeResult struct {
	Task      Task
	Outcome   TaskOutcomeContract
	Status    TaskStatus
	Summary   string
	ResultRef *string
}

// ApplyTerminalTaskOutcome applies done or failed task outcomes using shared side effects.
func ApplyTerminalTaskOutcome(ctx context.Context, services TaskOutcomeApplyServices, req TerminalTaskOutcomeRequest) (*TerminalTaskOutcomeResult, error) {
	if services.Store == nil {
		return nil, fmt.Errorf("team store is not configured")
	}
	taskID := strings.TrimSpace(req.Task.ID)
	if taskID == "" {
		return nil, fmt.Errorf("task id is required")
	}

	defaultStatus := req.DefaultStatus
	if defaultStatus == "" {
		defaultStatus = TaskOutcomeDone
	}
	normalized, structured, err := NormalizeTaskOutcomeContract(defaultStatus, req.Outcome)
	if err != nil {
		return nil, err
	}
	if structured {
		if err := ValidateAllowedTaskOutcomeStatus(normalized, defaultStatus); err != nil {
			return nil, err
		}
	}

	status := TaskStatusDone
	if normalized.Status == TaskOutcomeFailed {
		status = TaskStatusFailed
	}
	summary := strings.TrimSpace(firstNonEmptyString(normalized.Summary, normalized.Blocker))
	resultRef := normalizeOptionalTaskResultRef(req.ResultRef)
	teammateID := strings.TrimSpace(req.TeammateID)
	if teammateID == "" && req.Task.Assignee != nil {
		teammateID = strings.TrimSpace(*req.Task.Assignee)
	}

	taskWriter := NewAgentControlTaskRegistry(services.Store)
	if _, err := taskWriter.UpdateAgentControlTaskTerminal(ctx, agentcontrol.TaskTerminalUpdateRequest{
		ID:              taskID,
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		TeamID:          req.Task.TeamID,
		Status:          string(status),
		Summary:         summary,
		ResultRef:       resultRef,
		TeammateID:      teammateID,
		SkipStateUpdate: req.SkipStateUpdate,
	}); err != nil {
		return nil, err
	}
	if services.Claims != nil {
		_ = services.Claims.Release(ctx, taskID)
	}

	result := &TerminalTaskOutcomeResult{
		Outcome:   normalized,
		Status:    status,
		Summary:   summary,
		ResultRef: resultRef,
	}
	current, err := services.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if current != nil {
		result.Task = *current
	} else {
		updatedTask := req.Task
		updatedTask.Status = status
		updatedTask.Summary = summary
		updatedTask.ResultRef = resultRef
		updatedTask.Assignee = nil
		updatedTask.LeaseUntil = nil
		result.Task = updatedTask
	}
	emitTerminalTaskOutcomeEvent(ctx, services, result, teammateID, req)
	return result, nil
}

func emitTerminalTaskOutcomeEvent(ctx context.Context, services TaskOutcomeApplyServices, result *TerminalTaskOutcomeResult, teammateID string, req TerminalTaskOutcomeRequest) {
	if result == nil || services.Store == nil {
		return
	}
	teamID := strings.TrimSpace(result.Task.TeamID)
	if teamID == "" {
		teamID = strings.TrimSpace(req.Task.TeamID)
	}
	taskID := strings.TrimSpace(result.Task.ID)
	if taskID == "" {
		taskID = strings.TrimSpace(req.Task.ID)
	}
	if teamID == "" || taskID == "" {
		return
	}
	eventType := "task.completed"
	if result.Status == TaskStatusFailed {
		eventType = "task.failed"
	}
	payload := map[string]interface{}{
		"task_id": taskID,
	}
	if assignee := strings.TrimSpace(teammateID); assignee != "" {
		payload["assignee"] = assignee
	}
	if summary := strings.TrimSpace(result.Summary); summary != "" {
		payload["summary"] = summary
	}
	if result.ResultRef != nil && strings.TrimSpace(*result.ResultRef) != "" {
		payload["result_ref"] = strings.TrimSpace(*result.ResultRef)
	}
	if traceID := strings.TrimSpace(req.TraceID); traceID != "" {
		payload["trace_id"] = traceID
	}
	if errorText := strings.TrimSpace(req.Error); errorText != "" {
		payload["error"] = truncateLine(errorText, 240)
	}
	if errorType := strings.TrimSpace(req.ErrorType); errorType != "" {
		payload["error_type"] = errorType
	}
	for key, value := range req.ErrorMetadata {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := payload[key]; exists {
			continue
		}
		payload[key] = value
	}
	event := TeamEvent{
		Type:      eventType,
		TeamID:    teamID,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	}
	emitDedupedTerminalTaskEvent(ctx, services.Store, services.Events, event)
}

func emitDedupedTerminalTaskEvent(ctx context.Context, store Store, events *TeamEventBus, event TeamEvent) {
	if store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	event.Type = strings.TrimSpace(event.Type)
	event.TeamID = strings.TrimSpace(event.TeamID)
	if event.Type == "" || event.TeamID == "" {
		return
	}
	taskID := teamEventPayloadString(event.Payload["task_id"])
	if taskID == "" {
		return
	}
	if existing, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID:    event.TeamID,
		EventType: event.Type,
	}); err == nil {
		for _, record := range existing {
			if teamEventPayloadString(record.Payload["task_id"]) == taskID {
				return
			}
		}
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if _, err := store.AppendTeamEvent(ctx, event); err != nil {
		return
	}
	if events != nil {
		events.Publish(event)
	}
}

func teamEventPayloadString(value interface{}) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

// ApplyBlockedTaskOutcome applies blocked or handoff teammate outcomes using shared side effects.
func ApplyBlockedTaskOutcome(ctx context.Context, services TaskOutcomeApplyServices, req BlockedTaskOutcomeRequest) (*BlockedTaskOutcomeResult, error) {
	if services.Store == nil {
		return nil, fmt.Errorf("team store is not configured")
	}
	taskID := strings.TrimSpace(req.Task.ID)
	if taskID == "" {
		return nil, fmt.Errorf("task id is required")
	}

	outcome := req.Outcome
	if outcome.Status == "" && strings.TrimSpace(outcome.HandoffTo) != "" {
		outcome.Status = TaskOutcomeHandoff
		if strings.TrimSpace(outcome.Blocker) == "" {
			outcome.Blocker = outcome.Summary
		}
	}
	normalized, structured, err := NormalizeTaskOutcomeContract(TaskOutcomeBlocked, outcome)
	if err != nil {
		return nil, err
	}
	if structured {
		if err := ValidateAllowedTaskOutcomeStatus(normalized, TaskOutcomeBlocked, TaskOutcomeHandoff); err != nil {
			return nil, err
		}
	}

	summary := strings.TrimSpace(firstNonEmptyString(normalized.Summary, normalized.Blocker))
	if summary == "" {
		summary = "task blocked"
	}

	teamRecord := req.Team
	if strings.TrimSpace(teamRecord.ID) == "" {
		teamRecord.ID = strings.TrimSpace(req.Task.TeamID)
	}
	teammateID := strings.TrimSpace(req.TeammateID)
	if teammateID == "" && req.Task.Assignee != nil {
		teammateID = strings.TrimSpace(*req.Task.Assignee)
	}
	handoffTo := strings.TrimSpace(normalized.HandoffTo)

	taskWriter := NewAgentControlTaskRegistry(services.Store)
	if _, err := taskWriter.BlockAgentControlTask(ctx, agentcontrol.TaskBlockRequest{
		ID:              taskID,
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		Summary:         summary,
		TeammateID:      teammateID,
		SkipStateUpdate: req.SkipStateUpdate,
	}); err != nil {
		return nil, err
	}
	if services.Claims != nil {
		_ = services.Claims.Release(ctx, taskID)
	}

	result := &BlockedTaskOutcomeResult{
		Outcome:   normalized,
		Summary:   summary,
		HandoffTo: handoffTo,
	}

	notifyRecipient := true
	if req.NotifyRecipient != nil {
		notifyRecipient = *req.NotifyRecipient
	}
	if notifyRecipient && services.Mailbox != nil {
		recipient := firstNonEmptyString(handoffTo, "lead")
		message := BuildBlockedTaskOutcomeMailboxMessage(teamRecord.ID, taskID, teammateID, recipient, summary, normalized)
		messageID, err := services.Mailbox.Send(ctx, message)
		if err != nil {
			return nil, err
		}
		message.ID = messageID
		result.Message = &message
	}

	autoReplan := true
	if req.AutoReplan != nil {
		autoReplan = *req.AutoReplan
	}
	if handoffTo != "" && !strings.EqualFold(handoffTo, "lead") {
		autoReplan = false
	}
	result.AutoReplan = autoReplan

	if autoReplan && services.Planner != nil {
		blockedTask := req.Task
		blockedTask.Status = TaskStatusBlocked
		blockedTask.Summary = summary

		planner := *services.Planner
		planner.AutoPersist = true
		planResult, err := planner.ReplanOnFailure(ctx, teamRecord, blockedTask)
		if err != nil {
			result.ReplanError = err.Error()
			if sessionErr, ok := AsSessionExecutionError(err); ok && sessionErr != nil {
				result.ReplanTraceID = strings.TrimSpace(sessionErr.TraceID)
				result.ReplanErrorType = strings.TrimSpace(sessionErr.ErrorType)
				result.ReplanErrorMetadata = sessionErr.CloneMetadata()
			}
		} else {
			result.PlanResult = planResult
		}
	}

	updated, err := services.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if updated != nil {
		result.Task = *updated
	} else {
		fallback := req.Task
		fallback.Status = TaskStatusBlocked
		fallback.Summary = summary
		result.Task = fallback
	}
	return result, nil
}

// BuildBlockedTaskOutcomeMailboxMessage creates a mailbox notification for
// blocked/handoff task outcomes while carrying the AgentControl task lifecycle
// envelope needed by session mailbox mirrors and future control substrates.
func BuildBlockedTaskOutcomeMailboxMessage(teamID, taskID, teammateID, recipient, summary string, outcome TaskOutcomeContract) MailMessage {
	teamID = strings.TrimSpace(teamID)
	taskID = strings.TrimSpace(taskID)
	teammateID = strings.TrimSpace(teammateID)
	recipient = firstNonEmptyString(strings.TrimSpace(recipient), "lead")
	summary = strings.TrimSpace(summary)
	handoffTo := strings.TrimSpace(outcome.HandoffTo)
	kind := "warning"
	eventType := "task.blocked"
	if handoffTo != "" && !strings.EqualFold(recipient, "lead") {
		kind = "handoff"
		eventType = "task.handoff"
	}
	metadata := agentcontrol.ApplyEnvelope(map[string]interface{}{
		"event_type":  eventType,
		"team_id":     teamID,
		"task_id":     taskID,
		"assignee":    teammateID,
		"blocked_by":  teammateID,
		"task_status": string(TaskStatusBlocked),
		"outcome":     string(outcome.Status),
	}, agentcontrol.Envelope{
		MessageType:     TaskLifecycleControlMessageType,
		ControlAction:   TaskLifecycleControlAction,
		Workflow:        TaskAssignmentWorkflow,
		MailboxDelivery: taskDispatchMailboxDeliveryAgentSubstr,
		MailboxKind:     TaskLifecycleMailboxKind,
	})
	if handoffTo != "" {
		metadata["handoff_to"] = handoffTo
	}
	if blocker := strings.TrimSpace(outcome.Blocker); blocker != "" {
		metadata["blocker"] = blocker
	}
	if outcomeSummary := strings.TrimSpace(outcome.Summary); outcomeSummary != "" {
		metadata["summary"] = outcomeSummary
	}
	var taskIDPtr *string
	if taskID != "" {
		taskIDValue := taskID
		taskIDPtr = &taskIDValue
	}
	return MailMessage{
		TeamID:    teamID,
		FromAgent: firstNonEmptyString(teammateID, "teammate"),
		ToAgent:   recipient,
		TaskID:    taskIDPtr,
		Kind:      kind,
		Body:      summary,
		Metadata:  metadata,
		CreatedAt: time.Now().UTC(),
	}
}

func normalizeOptionalTaskResultRef(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
