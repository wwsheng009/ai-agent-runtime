package team

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// TaskOutcomeApplyServices groups shared collaborators used to apply task outcomes.
type TaskOutcomeApplyServices struct {
	Store   Store
	Claims  *PathClaimManager
	Mailbox *MailboxService
	Planner *LeadPlanner
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
	Task        Task
	Outcome     TaskOutcomeContract
	Summary     string
	HandoffTo   string
	Message     *MailMessage
	AutoReplan  bool
	PlanResult  *PlanResult
	ReplanError string
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

	if sqliteStore, ok := services.Store.(*SQLiteStore); ok {
		return applyTerminalTaskOutcomeSQLite(ctx, sqliteStore, services, req, normalized, status, summary, resultRef, teammateID)
	}

	updatedTask := req.Task
	updatedTask.Status = status
	updatedTask.Summary = summary
	updatedTask.ResultRef = resultRef
	if err := services.Store.UpdateTask(ctx, updatedTask); err != nil {
		return nil, err
	}
	if err := services.Store.ReleaseTask(ctx, taskID, status); err != nil {
		return nil, err
	}
	if services.Claims != nil {
		_ = services.Claims.Release(ctx, taskID)
	}
	if teammateID != "" && !req.SkipStateUpdate {
		_ = services.Store.UpdateTeammateState(ctx, teammateID, TeammateStateIdle)
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
		updatedTask.Assignee = nil
		updatedTask.LeaseUntil = nil
		result.Task = updatedTask
	}
	return result, nil
}

func applyTerminalTaskOutcomeSQLite(
	ctx context.Context,
	store *SQLiteStore,
	services TaskOutcomeApplyServices,
	req TerminalTaskOutcomeRequest,
	normalized TaskOutcomeContract,
	status TaskStatus,
	summary string,
	resultRef *string,
	teammateID string,
) (*TerminalTaskOutcomeResult, error) {
	taskID := strings.TrimSpace(req.Task.ID)
	updatedTask := req.Task
	updatedTask.Status = status
	updatedTask.Summary = summary
	updatedTask.ResultRef = resultRef

	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = store.WithImmediateTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `
				UPDATE team_tasks
				SET status = ?, summary = ?, result_ref = ?, assignee = NULL, lease_until = NULL, updated_at = ?
				WHERE id = ?
			`, string(status), summary, nullableString(resultRef), formatTime(time.Now().UTC()), taskID); err != nil {
				return fmt.Errorf("update task: %w", err)
			}
			if teammateID != "" && !req.SkipStateUpdate {
				if _, err := tx.ExecContext(ctx, `
					UPDATE teammates SET state = ?, updated_at = ? WHERE id = ?
				`, string(TeammateStateIdle), formatTime(time.Now().UTC()), teammateID); err != nil {
					return fmt.Errorf("update teammate state: %w", err)
				}
			}
			return nil
		})
		if err == nil || !IsSQLiteLockError(err) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 10 * time.Millisecond):
		}
	}
	if err != nil {
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
		updatedTask.Assignee = nil
		updatedTask.LeaseUntil = nil
		result.Task = updatedTask
	}
	return result, nil
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

	if err := services.Store.BlockTask(ctx, taskID, summary); err != nil {
		return nil, err
	}
	if services.Claims != nil {
		_ = services.Claims.Release(ctx, taskID)
	}
	if teammateID != "" && !req.SkipStateUpdate {
		_ = services.Store.UpdateTeammateState(ctx, teammateID, TeammateStateBlocked)
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
		kind := "warning"
		if recipient != "" && !strings.EqualFold(recipient, "lead") {
			kind = "handoff"
		}
		message := MailMessage{
			TeamID:    teamRecord.ID,
			FromAgent: firstNonEmptyString(teammateID, "teammate"),
			ToAgent:   recipient,
			TaskID:    &taskID,
			Kind:      kind,
			Body:      summary,
		}
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
