package memorytaskbridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// SessionReader is the narrow chat-session lookup needed to bind a durable
// team task terminal event to the canonical session incarnation.
type SessionReader interface {
	GetSession(context.Context, string) (*chat.Session, error)
}

// RuntimeResolver resolves the workspace-scoped memory runtime for a session.
// A nil runtime means memory is disabled for that workspace and the event is
// intentionally ignored rather than written to another workspace's store.
type RuntimeResolver func(context.Context, string) (*memoryruntime.Runtime, error)

// Dependencies are the host-owned collaborators for the task-event bridge.
// The bridge never imports the runtime API or CLI host types.
type Dependencies struct {
	TeamStore         team.Store
	Sessions          SessionReader
	RuntimeForSession RuntimeResolver
	OnError           func(error)
}

// BindTeamEvents subscribes to durable terminal task events. Blocked/handoff
// events are deliberately excluded: only completed, failed, and cancelled task
// states create an Episode.
func BindTeamEvents(bus *team.TeamEventBus, deps Dependencies) func() {
	if bus == nil || deps.TeamStore == nil || deps.Sessions == nil || deps.RuntimeForSession == nil {
		return func() {}
	}
	handler := func(event team.TeamEvent) {
		_, err := ConsumeTeamEvent(context.Background(), event, deps)
		if err != nil && deps.OnError != nil {
			deps.OnError(err)
		}
	}
	for _, eventType := range []string{
		"task.completed", "task.failed", "task.cancelled",
		"team.task.completed", "team.task.failed", "team.task.cancelled",
	} {
		bus.Subscribe(eventType, handler)
	}
	// TeamEventBus has no unsubscribe API. Hosts bind once per bus lifetime and
	// keep the returned no-op for a symmetric caller contract.
	return func() {}
}

// ConsumeTeamEvent maps one durable team task terminal event to the memory
// runtime Episode hook. It returns accepted=false for non-terminal events or
// when the workspace has no enabled memory runtime.
func ConsumeTeamEvent(ctx context.Context, event team.TeamEvent, deps Dependencies) (bool, error) {
	if deps.TeamStore == nil || deps.Sessions == nil || deps.RuntimeForSession == nil {
		return false, nil
	}
	outcome, ok := taskEventOutcome(event.Type)
	if !ok {
		return false, nil
	}
	payload := event.Payload
	if payload == nil {
		payload = map[string]interface{}{}
	}
	taskID := payloadString(payload, "task_id")
	teamID := firstNonEmpty(payloadString(payload, "team_id"), strings.TrimSpace(event.TeamID))
	if taskID == "" || teamID == "" {
		return false, fmt.Errorf("memorytaskbridge: terminal task event lacks task/team identity")
	}

	task, err := deps.TeamStore.GetTask(ctx, taskID)
	if err != nil {
		return false, fmt.Errorf("memorytaskbridge: load task %s: %w", taskID, err)
	}
	if task == nil {
		return false, fmt.Errorf("memorytaskbridge: task %s not found", taskID)
	}
	record, err := deps.TeamStore.GetTeam(ctx, teamID)
	if err != nil {
		return false, fmt.Errorf("memorytaskbridge: load team %s: %w", teamID, err)
	}

	sessionID, err := taskSessionID(ctx, deps.TeamStore, payload, task, record)
	if err != nil {
		return false, err
	}
	session, err := deps.Sessions.GetSession(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("memorytaskbridge: load session %s: %w", sessionID, err)
	}
	if session == nil || strings.TrimSpace(session.ID) != sessionID {
		return false, fmt.Errorf("memorytaskbridge: session %s identity unavailable", sessionID)
	}
	runtime, err := deps.RuntimeForSession(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("memorytaskbridge: resolve memory runtime: %w", err)
	}
	if runtime == nil || runtime.Store == nil {
		return false, nil
	}

	endedAt := event.Timestamp
	if endedAt.IsZero() {
		endedAt = task.UpdatedAt
	}
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}
	sourceEventID := fmt.Sprintf("team_task:%s:%s:%s", teamID, taskID, outcome)
	result := payloadString(payload, "summary")
	if result == "" {
		result = task.Summary
	}
	failureReason := payloadString(payload, "error")
	if failureReason == "" && outcome == "failed" {
		failureReason = task.Summary
	}
	scope := runtime.Store.ScopeContext()
	taskEvent := memoryruntime.TaskCompletionEvent{
		TenantID:          scope.TenantID,
		ProjectKey:        runtime.Store.ProjectKey(),
		SessionID:         sessionID,
		TaskID:            taskID,
		Outcome:           outcome,
		Problem:           firstNonEmpty(strings.TrimSpace(task.Goal), strings.TrimSpace(task.Title)),
		Result:            result,
		FailureReason:     failureReason,
		SourceEventID:     sourceEventID,
		SourceEventIDs:    []string{sourceEventID},
		Durable:           true,
		TeamID:            teamID,
		ResultRef:         payloadString(payload, "result_ref"),
		SessionGeneration: session.SessionGeneration,
		StartedAt:         task.CreatedAt,
		EndedAt:           endedAt,
		Timestamp:         endedAt,
	}
	handled, err := runtime.HandleTaskCompletionEvent(ctx, taskEvent)
	if err != nil {
		return false, err
	}
	return handled.Accepted, nil
}

func taskSessionID(
	ctx context.Context,
	store team.Store,
	payload map[string]interface{},
	task *team.Task,
	record *team.Team,
) (string, error) {
	assignee := payloadString(payload, "assignee")
	if assignee == "" && task != nil && task.Assignee != nil {
		assignee = strings.TrimSpace(*task.Assignee)
	}
	if assignee != "" {
		teammate, err := store.GetTeammate(ctx, assignee)
		if err != nil {
			return "", fmt.Errorf("memorytaskbridge: load teammate %s: %w", assignee, err)
		}
		if teammate != nil && strings.TrimSpace(teammate.SessionID) != "" {
			return strings.TrimSpace(teammate.SessionID), nil
		}
	}
	if record != nil && strings.TrimSpace(record.LeadSessionID) != "" {
		return strings.TrimSpace(record.LeadSessionID), nil
	}
	return "", fmt.Errorf("memorytaskbridge: terminal task event has no resolvable session")
}

func taskEventOutcome(eventType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "task.completed", "team.task.completed":
		return "success", true
	case "task.failed", "team.task.failed":
		return "failed", true
	case "task.cancelled", "team.task.cancelled":
		return "cancelled", true
	default:
		return "", false
	}
}

func payloadString(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	switch value := payload[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
