package team

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// TerminalTeamServices groups collaborators used to reconcile team terminal state.
type TerminalTeamServices struct {
	Store               Store
	Planner             *LeadPlanner
	Mailbox             *MailboxService
	Events              *TeamEventBus
	IgnoreBusyTeammates bool
}

// TerminalTeamResult captures the outcome of a terminal-state reconciliation.
type TerminalTeamResult struct {
	Terminal              bool
	Transition            bool
	Status                TeamStatus
	Summary               string
	SummarySource         string
	SummaryUsedFallback   bool
	SummaryFallbackReason string
	SummaryTraceID        string
	SummaryErrorType      string
	SummaryErrorMetadata  map[string]interface{}
}

var terminalSummaryLocks sync.Map

// ReconcileTerminalTeamState updates a team to a terminal state once no active tasks remain.
func ReconcileTerminalTeamState(ctx context.Context, services TerminalTeamServices, teamID string) (*TerminalTeamResult, error) {
	if services.Store == nil {
		return nil, fmt.Errorf("team store is not configured")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	dependencyFailures, err := ReconcileFailedTaskDependencies(ctx, services.Store, teamID)
	if err != nil {
		return nil, err
	}
	publishDependencyFailureEvents(services.Events, teamID, dependencyFailures)
	if sqliteStore, ok := services.Store.(*SQLiteStore); ok {
		return reconcileTerminalTeamStateSQLite(ctx, sqliteStore, services, teamID)
	}

	current, err := services.Store.GetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if IsTerminalTeamStatus(currentTeamStatus(current)) {
		return finalizeTerminalTeamState(ctx, services, teamID, &TerminalTeamResult{
			Terminal:   true,
			Transition: false,
			Status:     current.Status,
		}, nil)
	}
	if currentTeamStatus(current) == TeamStatusPaused {
		return &TerminalTeamResult{
			Terminal:   false,
			Transition: false,
			Status:     TeamStatusPaused,
		}, nil
	}

	active, err := services.Store.ListTasks(ctx, TaskFilter{
		TeamID: teamID,
		Status: []TaskStatus{TaskStatusPending, TaskStatusReady, TaskStatusRunning, TaskStatusBlocked},
	})
	if err != nil {
		return nil, err
	}
	if len(active) > 0 {
		return &TerminalTeamResult{Terminal: false}, nil
	}
	if !services.IgnoreBusyTeammates {
		if busy, err := hasBusyTeammates(ctx, services.Store, teamID); err != nil {
			return nil, err
		} else if busy {
			return &TerminalTeamResult{Terminal: false}, nil
		}
	}

	allTasks, err := services.Store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	if err != nil {
		return nil, err
	}
	if len(allTasks) == 0 {
		return &TerminalTeamResult{Terminal: false}, nil
	}

	status := terminalStatusForTasks(allTasks)

	if current != nil && current.Status == status {
		return &TerminalTeamResult{
			Terminal:   true,
			Transition: false,
			Status:     status,
		}, nil
	}

	if err := services.Store.UpdateTeamStatus(ctx, teamID, status); err != nil {
		return nil, err
	}

	result := &TerminalTeamResult{
		Terminal:   true,
		Transition: true,
		Status:     status,
	}
	return finalizeTerminalTeamState(ctx, services, teamID, result, allTasks)
}

func reconcileTerminalTeamStateSQLite(ctx context.Context, store *SQLiteStore, services TerminalTeamServices, teamID string) (*TerminalTeamResult, error) {
	var (
		result *TerminalTeamResult
		err    error
	)
	for attempt := 0; attempt < 8; attempt++ {
		result = &TerminalTeamResult{}
		err = store.WithImmediateTx(ctx, func(tx *sql.Tx) error {
			currentStatus, err := loadTeamStatusTx(ctx, tx, teamID)
			if err != nil {
				return err
			}
			if IsTerminalTeamStatus(currentStatus) {
				result.Terminal = true
				result.Transition = false
				result.Status = currentStatus
				return nil
			}
			if currentStatus == TeamStatusPaused {
				result.Terminal = false
				result.Transition = false
				result.Status = currentStatus
				return nil
			}

			activeCount, err := countTasksByStatusTx(ctx, tx, teamID, TaskStatusPending, TaskStatusReady, TaskStatusRunning, TaskStatusBlocked)
			if err != nil {
				return err
			}
			if activeCount > 0 {
				result.Terminal = false
				return nil
			}
			if !services.IgnoreBusyTeammates {
				busyCount, err := countTeammatesByStateTx(ctx, tx, teamID, TeammateStateBusy)
				if err != nil {
					return err
				}
				if busyCount > 0 {
					result.Terminal = false
					return nil
				}
			}

			taskCount, err := countTeamTasksTx(ctx, tx, teamID)
			if err != nil {
				return err
			}
			if taskCount == 0 {
				result.Terminal = false
				return nil
			}

			failedCount, err := countTasksByStatusTx(ctx, tx, teamID, TaskStatusFailed)
			if err != nil {
				return err
			}
			doneCount, err := countTasksByStatusTx(ctx, tx, teamID, TaskStatusDone)
			if err != nil {
				return err
			}
			canceledCount, err := countTasksByStatusTx(ctx, tx, teamID, TaskStatusCancelled)
			if err != nil {
				return err
			}

			status := terminalStatusFromCounts(doneCount, failedCount, canceledCount, taskCount)

			if currentStatus == status {
				result.Terminal = true
				result.Transition = false
				result.Status = status
				return nil
			}

			if _, err := tx.ExecContext(ctx, `
				UPDATE teams SET status = ?, updated_at = ? WHERE id = ?
			`, string(status), formatTime(time.Now().UTC()), teamID); err != nil {
				return fmt.Errorf("update team status: %w", err)
			}
			result.Terminal = true
			result.Transition = true
			result.Status = status
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
	if !result.Terminal {
		return result, nil
	}
	tasks, err := services.Store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	if err != nil {
		return nil, err
	}
	return finalizeTerminalTeamState(ctx, services, teamID, result, tasks)
}

func hasBusyTeammates(ctx context.Context, store Store, teamID string) (bool, error) {
	teammates, err := store.ListTeammates(ctx, teamID)
	if err != nil {
		return false, err
	}
	for _, teammate := range teammates {
		if teammate.State == TeammateStateBusy {
			return true, nil
		}
	}
	return false, nil
}

func countTasksByStatusTx(ctx context.Context, tx *sql.Tx, teamID string, statuses ...TaskStatus) (int, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	placeholders := make([]string, 0, len(statuses))
	args := make([]interface{}, 0, len(statuses)+2)
	args = append(args, agentcontrol.WorkflowSpawnTeam, teamID)
	for _, status := range statuses {
		placeholders = append(placeholders, "?")
		args = append(args, string(status))
	}
	row := tx.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM agent_control_task_records
		WHERE workflow = ? AND team_id = ? AND status IN (`+strings.Join(placeholders, ",")+`)
	`, args...)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("count team tasks: %w", err)
	}
	return count, nil
}

func countTeamTasksTx(ctx context.Context, tx *sql.Tx, teamID string) (int, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM agent_control_task_records
		WHERE workflow = ? AND team_id = ?
	`, agentcontrol.WorkflowSpawnTeam, teamID)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("count team tasks: %w", err)
	}
	return count, nil
}

func countTeammatesByStateTx(ctx context.Context, tx *sql.Tx, teamID string, states ...TeammateState) (int, error) {
	if len(states) == 0 {
		return 0, nil
	}
	placeholders := make([]string, 0, len(states))
	args := make([]interface{}, 0, len(states)+1)
	args = append(args, teamID)
	for _, state := range states {
		placeholders = append(placeholders, "?")
		args = append(args, string(state))
	}
	row := tx.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM teammates
		WHERE team_id = ? AND state IN (`+strings.Join(placeholders, ",")+`)
	`, args...)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("count teammates: %w", err)
	}
	return count, nil
}

func loadTeamStatusTx(ctx context.Context, tx *sql.Tx, teamID string) (TeamStatus, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT status
		FROM teams
		WHERE id = ?
	`, teamID)
	var status string
	if err := row.Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("load team status: %w", err)
	}
	return TeamStatus(status), nil
}

func currentTeamStatus(record *Team) TeamStatus {
	if record == nil {
		return ""
	}
	return record.Status
}

func finalizeTerminalTeamState(ctx context.Context, services TerminalTeamServices, teamID string, result *TerminalTeamResult, tasks []Task) (*TerminalTeamResult, error) {
	if result == nil || !result.Terminal {
		return result, nil
	}
	if result.Transition {
		emitTerminalTeamEvent(services.Store, services.Events, TeamEvent{
			Type:   "team.completed",
			TeamID: teamID,
			Payload: map[string]interface{}{
				"status": string(result.Status),
			},
		})
	}
	unlock := lockTerminalTeamSummary(teamID)
	defer unlock()
	if found, err := applyPersistedTerminalSummary(ctx, services.Store, teamID, result); err != nil {
		return nil, err
	} else if found {
		return result, nil
	}
	if tasks == nil {
		var err error
		tasks, err = services.Store.ListTasks(ctx, TaskFilter{TeamID: teamID})
		if err != nil {
			return nil, err
		}
	}

	var summaryResult *FinalSummaryResult
	if services.Planner != nil {
		generated, err := services.Planner.FinalSummaryDetailed(ctx, teamID)
		if err != nil {
			emitTerminalTeamSummaryFailure(services.Store, services.Events, teamID, nil, err)
			summaryResult = terminalFallbackSummary(teamID, tasks, FinalSummaryFallbackLeadSessionError)
		} else {
			summaryResult = generated
		}
	} else {
		summaryResult = terminalFallbackSummary(teamID, tasks, FinalSummaryFallbackPlannerNotConfigured)
	}
	if summaryResult == nil || strings.TrimSpace(summaryResult.Summary) == "" {
		summaryResult = terminalFallbackSummary(teamID, tasks, FinalSummaryFallbackLeadOutputEmpty)
	}
	applyTerminalTeamSummaryResult(result, summaryResult)
	if summaryResult.HasSessionError() {
		emitTerminalTeamSummaryFailure(services.Store, services.Events, teamID, summaryResult, summaryResult.SessionError)
	}

	payload := BuildFinalSummaryEventPayload(summaryResult)
	appendTerminalOutcomePayload(payload, result.Status, tasks)
	emitTerminalTeamEvent(services.Store, services.Events, TeamEvent{
		Type:    "team.summary",
		TeamID:  teamID,
		Payload: payload,
	})
	if services.Mailbox != nil {
		_, _ = services.Mailbox.Send(ctx, MailMessage{
			TeamID:    teamID,
			FromAgent: "lead",
			ToAgent:   "*",
			Kind:      "done",
			Body:      result.Summary,
			Metadata:  payload,
		})
	}
	return result, nil
}

func lockTerminalTeamSummary(teamID string) func() {
	key := strings.TrimSpace(teamID)
	value, _ := terminalSummaryLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func terminalFallbackSummary(teamID string, tasks []Task, reason string) *FinalSummaryResult {
	return &FinalSummaryResult{
		Summary:        buildSummaryFallback(teamID, tasks),
		SummarySource:  FinalSummarySourceFallback,
		UsedFallback:   true,
		FallbackReason: strings.TrimSpace(reason),
	}
}

func applyPersistedTerminalSummary(ctx context.Context, store Store, teamID string, result *TerminalTeamResult) (bool, error) {
	events, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teamID, EventType: "team.summary", Limit: 1000})
	if err != nil {
		return false, err
	}
	for index := len(events) - 1; index >= 0; index-- {
		payload := events[index].Payload
		summary, _ := payload["summary"].(string)
		if strings.TrimSpace(summary) == "" {
			continue
		}
		result.Summary = strings.TrimSpace(summary)
		result.SummarySource, _ = payload["summary_source"].(string)
		result.SummaryUsedFallback, _ = payload["used_fallback"].(bool)
		result.SummaryFallbackReason, _ = payload["fallback_reason"].(string)
		result.SummaryTraceID, _ = payload["trace_id"].(string)
		result.SummaryErrorType, _ = payload["error_type"].(string)
		return true, nil
	}
	return false, nil
}

func emitTerminalTeamEvent(store Store, events *TeamEventBus, event TeamEvent) {
	if events != nil {
		events.Publish(event)
	}
	if store != nil {
		_, _ = store.AppendTeamEvent(context.Background(), event)
	}
}

func applyTerminalTeamSummaryResult(target *TerminalTeamResult, summaryResult *FinalSummaryResult) {
	if target == nil || summaryResult == nil {
		return
	}
	target.Summary = strings.TrimSpace(summaryResult.Summary)
	target.SummarySource = strings.TrimSpace(summaryResult.SummarySource)
	target.SummaryUsedFallback = summaryResult.UsedFallback
	target.SummaryFallbackReason = strings.TrimSpace(summaryResult.FallbackReason)
	target.SummaryTraceID = strings.TrimSpace(summaryResult.TraceID)
	target.SummaryErrorType = strings.TrimSpace(summaryResult.ErrorType)
	target.SummaryErrorMetadata = summaryResult.CloneErrorMetadata()
}

func emitTerminalTeamSummaryFailure(store Store, events *TeamEventBus, teamID string, summaryResult *FinalSummaryResult, err error) {
	if err == nil {
		return
	}
	payload := BuildFinalSummaryFailurePayload(summaryResult, err)
	emitTerminalTeamEvent(store, events, TeamEvent{
		Type:    "team.summary.failed",
		TeamID:  teamID,
		Payload: payload,
	})
}

// IsSQLiteLockError reports whether err represents a transient SQLite lock failure.
func IsSQLiteLockError(err error) bool {
	message := strings.ToLower(strings.TrimSpace(errorString(err)))
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked")
}
