package memoryruntime

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
)

const (
	maxTaskCompletionSourceIDs = 32
	maxTaskCompletionTextRunes = 4000
)

// HandleTaskCompletionEvent is the narrow G0 hook for callers that have
// already read a durable task-terminal event. It validates source identity,
// project/session incarnation and bounded/redacted payload before delegating
// to the future G4 consumer. It deliberately does not create an Episode,
// candidate, or active memory itself.
func (r *Runtime) HandleTaskCompletionEvent(
	ctx context.Context, event TaskCompletionEvent,
) (TaskCompletionResult, error) {
	result := TaskCompletionResult{Trigger: TriggerTaskCompletion}
	if r == nil || !r.enabled() || r.Config.GenerationDisabled {
		result.Reason = "memory_or_generation_disabled"
		return result, nil
	}
	if r.Config.GenerationTrigger != string(TriggerTaskCompletion) {
		result.Reason = "task_completion_trigger_not_selected"
		return result, nil
	}
	if r.Store == nil || r.Source == nil {
		return result, fmt.Errorf("memoryruntime: task-completion runtime is not configured")
	}
	if !event.Durable {
		return result, fmt.Errorf("memoryruntime: task-completion event is not marked durable")
	}
	event, err := normalizeTaskCompletionEvent(event)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(event.TenantID) != "" {
		return result, fmt.Errorf("memoryruntime: task-completion tenant scope is not configured")
	}
	projectKey := strings.TrimSpace(event.ProjectKey)
	if projectKey == "" || projectKey != r.Store.ProjectKey() ||
		(r.Config.ProjectKey != "" && projectKey != r.Config.ProjectKey) {
		return result, fmt.Errorf("memoryruntime: task-completion project identity mismatch")
	}
	session, err := r.findSession(ctx, event.SessionID)
	if err != nil {
		return result, fmt.Errorf("memoryruntime: task-completion session identity unavailable")
	}
	if session.SessionGeneration <= 0 || session.SessionGeneration != event.SessionGeneration {
		result.Reason = "session_generation_mismatch"
		return result, nil
	}
	if ok, reason := r.eligible(ctx, session, TriggerTaskCompletion); !ok {
		result.Reason = "session_ineligible:" + reason
		return result, nil
	}
	if r.TaskCompletions == nil {
		return result, fmt.Errorf("memoryruntime: durable task-completion consumer is not configured")
	}
	if err := r.TaskCompletions.ConsumeTaskCompletion(ctx, event); err != nil {
		return result, err
	}
	result.Accepted = true
	result.Reason = "durable_event_accepted"
	return result, nil
}

func normalizeTaskCompletionEvent(event TaskCompletionEvent) (TaskCompletionEvent, error) {
	event.SourceEventID = strings.TrimSpace(event.SourceEventID)
	event.TaskID = strings.TrimSpace(event.TaskID)
	event.SessionID = strings.TrimSpace(event.SessionID)
	event.ProjectKey = strings.TrimSpace(event.ProjectKey)
	event.TeamID = strings.TrimSpace(event.TeamID)
	if event.SourceEventID == "" || event.TaskID == "" || event.SessionID == "" ||
		event.ProjectKey == "" || event.TeamID == "" || event.SessionGeneration <= 0 {
		return TaskCompletionEvent{}, fmt.Errorf("memoryruntime: task-completion event identity is incomplete")
	}
	if event.EndedAt.IsZero() {
		event.EndedAt = event.Timestamp
	}
	if event.EndedAt.IsZero() {
		return TaskCompletionEvent{}, fmt.Errorf("memoryruntime: task-completion event timestamp is required")
	}
	if event.EndedAt.After(time.Now().UTC().Add(5 * time.Minute)) {
		return TaskCompletionEvent{}, fmt.Errorf("memoryruntime: task-completion event timestamp is invalid")
	}
	event.EndedAt = event.EndedAt.UTC()
	if !event.StartedAt.IsZero() {
		event.StartedAt = event.StartedAt.UTC()
		if event.StartedAt.After(event.EndedAt) {
			return TaskCompletionEvent{}, fmt.Errorf("memoryruntime: task-completion event interval is invalid")
		}
	}
	outcome := strings.ToLower(strings.TrimSpace(event.Outcome))
	switch outcome {
	case "success", "completed", "done":
		event.Outcome = "success"
	case "failed", "failure":
		event.Outcome = "failed"
	case "partial", "partial_success":
		event.Outcome = "partial"
	case "blocked":
		event.Outcome = "blocked"
	case "cancelled", "canceled":
		event.Outcome = "cancelled"
	default:
		return TaskCompletionEvent{}, fmt.Errorf("memoryruntime: task-completion outcome is not terminal")
	}
	ids := make([]string, 0, len(event.SourceEventIDs)+1)
	seen := make(map[string]struct{}, len(event.SourceEventIDs)+1)
	for _, id := range append(event.SourceEventIDs, event.SourceEventID) {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 256 {
			return TaskCompletionEvent{}, fmt.Errorf("memoryruntime: task-completion source event identity is invalid")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) > maxTaskCompletionSourceIDs {
			return TaskCompletionEvent{}, fmt.Errorf("memoryruntime: task-completion source event list exceeds limit")
		}
	}
	if len(ids) == 0 {
		return TaskCompletionEvent{}, fmt.Errorf("memoryruntime: durable source event identity is required")
	}
	if event.SourceEventID == "" {
		event.SourceEventID = ids[0]
	}
	foundPrimary := false
	for _, id := range ids {
		foundPrimary = foundPrimary || id == event.SourceEventID
	}
	if !foundPrimary {
		return TaskCompletionEvent{}, fmt.Errorf("memoryruntime: primary source event is missing from source event list")
	}
	event.SourceEventIDs = ids
	event.Problem = sanitizeTaskCompletionText(event.Problem)
	event.Result = sanitizeTaskCompletionText(event.Result)
	event.FailureReason = sanitizeTaskCompletionText(event.FailureReason)
	event.ResultRef = sanitizeTaskCompletionText(event.ResultRef)
	for index, action := range event.Actions {
		event.Actions[index] = sanitizeTaskCompletionText(action)
	}
	if len(event.Actions) > maxTaskCompletionSourceIDs {
		event.Actions = event.Actions[:maxTaskCompletionSourceIDs]
	}
	return event, nil
}

func sanitizeTaskCompletionText(text string) string {
	text = strings.TrimSpace(text)
	text, _ = memoryredact.Redact(text)
	text, _ = truncateTurnDigestText(text, maxTaskCompletionTextRunes)
	if utf8.RuneCountInString(text) > maxTaskCompletionTextRunes {
		runes := []rune(text)
		text = string(runes[:maxTaskCompletionTextRunes])
	}
	return text
}
