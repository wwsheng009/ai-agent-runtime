package memoryruntime

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
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
	if r == nil || !r.enabled() {
		result.Reason = "memory_disabled"
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
	storeScope := r.Store.ScopeContext()
	if strings.TrimSpace(event.TenantID) != "" && strings.TrimSpace(event.TenantID) != storeScope.TenantID {
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
	if session.SessionGeneration <= 0 {
		result.Reason = "session_generation_unavailable"
		return result, nil
	}
	if event.SessionGeneration <= 0 {
		event.SessionGeneration = session.SessionGeneration
	}
	if session.SessionGeneration != event.SessionGeneration {
		result.Reason = "session_generation_mismatch"
		return result, nil
	}
	if ok, reason := r.taskCompletionEligible(ctx, session); !ok {
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
		event.ProjectKey == "" {
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

// taskCompletionEligible is deliberately narrower than the extraction
// eligibility gate. Task Episodes are deterministic operational memories and
// must remain recordable for subagent/team sessions and recently active work;
// they do not depend on idle thresholds or the LLM generation trigger.
func (r *Runtime) taskCompletionEligible(ctx context.Context, session SessionRef) (bool, string) {
	if r == nil || !r.enabled() {
		return false, "mode_off"
	}
	if session.Ephemeral {
		return false, "ephemeral"
	}
	if r.Modes != nil {
		mode, err := r.Modes.GetSessionMode(ctx, session.ID)
		if err != nil {
			return false, "session_mode_error"
		}
		if mode == memorystore.SessionModeDisabled || mode == memorystore.SessionModePolluted {
			return false, "session_mode_" + string(mode)
		}
	}
	if r.Config.Workspace != "" {
		if strings.TrimSpace(session.WorkspacePath) == "" {
			if r.Config.RequireWorkspaceMatch {
				return false, "workspace_missing"
			}
		} else if !sameWorkspace(r.Config.Workspace, session.WorkspacePath) {
			return false, "workspace_mismatch"
		}
	}
	return true, ""
}

// StoreTaskCompletionConsumer is the durable G4 Episode boundary. It creates
// one candidate Episode from a validated task-terminal event; consolidation or
// explicit review may later promote it.
type StoreTaskCompletionConsumer struct {
	Store *memorystore.Store
}

func NewStoreTaskCompletionConsumer(store *memorystore.Store) *StoreTaskCompletionConsumer {
	return &StoreTaskCompletionConsumer{Store: store}
}

func (c *StoreTaskCompletionConsumer) ConsumeTaskCompletion(ctx context.Context, event TaskCompletionEvent) error {
	if c == nil || c.Store == nil {
		return fmt.Errorf("memoryruntime: task-completion store consumer is not configured")
	}
	_, err := c.Store.CreateEpisode(ctx, memorystore.EpisodeInput{
		TenantID: event.TenantID, ProjectKey: event.ProjectKey,
		TaskID: event.TaskID, SessionID: event.SessionID,
		Problem: event.Problem, Actions: append([]string(nil), event.Actions...),
		Result: event.Result, FailureReason: event.FailureReason,
		Outcome:   memorystore.EpisodeOutcome(event.Outcome),
		StartedAt: event.StartedAt, EndedAt: event.EndedAt,
		SourceEventIDs: append([]string(nil), event.SourceEventIDs...),
	})
	return err
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
