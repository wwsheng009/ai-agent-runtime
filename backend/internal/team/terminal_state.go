package team

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// TerminalTeamServices groups collaborators used to reconcile team terminal state.
type TerminalTeamServices struct {
	Store   Store
	Planner *LeadPlanner
	Mailbox *MailboxService
	Events  *TeamEventBus
}

// TerminalTeamResult captures the outcome of a terminal-state reconciliation.
type TerminalTeamResult struct {
	Terminal   bool
	Transition bool
	Status     TeamStatus
	Summary    string
}

// ReconcileTerminalTeamState updates a team to a terminal state once no active tasks remain.
func ReconcileTerminalTeamState(ctx context.Context, services TerminalTeamServices, teamID string) (*TerminalTeamResult, error) {
	if services.Store == nil {
		return nil, fmt.Errorf("team store is not configured")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	if sqliteStore, ok := services.Store.(*SQLiteStore); ok {
		return reconcileTerminalTeamStateSQLite(ctx, sqliteStore, services, teamID)
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

	failed, err := services.Store.ListTasks(ctx, TaskFilter{
		TeamID: teamID,
		Status: []TaskStatus{TaskStatusFailed},
	})
	if err != nil {
		return nil, err
	}

	status := TeamStatusDone
	if len(failed) > 0 {
		status = TeamStatusFailed
	}

	current, err := services.Store.GetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
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
	if !result.Transition {
		return result, nil
	}
	emitTerminalTeamEvent(services.Store, services.Events, TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": string(status),
		},
	})

	if status != TeamStatusDone || services.Planner == nil {
		return result, nil
	}

	summary, err := services.Planner.FinalSummary(ctx, teamID)
	if err != nil {
		return result, nil
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return result, nil
	}

	result.Summary = summary
	emitTerminalTeamEvent(services.Store, services.Events, TeamEvent{
		Type:   "team.summary",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"summary": summary,
		},
	})
	if services.Mailbox != nil {
		_, _ = services.Mailbox.Send(ctx, MailMessage{
			TeamID:    teamID,
			FromAgent: "lead",
			ToAgent:   "*",
			Kind:      "done",
			Body:      summary,
		})
	}

	return result, nil
}

func reconcileTerminalTeamStateSQLite(ctx context.Context, store *SQLiteStore, services TerminalTeamServices, teamID string) (*TerminalTeamResult, error) {
	var (
		result *TerminalTeamResult
		err    error
	)
	for attempt := 0; attempt < 8; attempt++ {
		result = &TerminalTeamResult{}
		err = store.WithImmediateTx(ctx, func(tx *sql.Tx) error {
			activeCount, err := countTasksByStatusTx(ctx, tx, teamID, TaskStatusPending, TaskStatusReady, TaskStatusRunning, TaskStatusBlocked)
			if err != nil {
				return err
			}
			if activeCount > 0 {
				result.Terminal = false
				return nil
			}

			failedCount, err := countTasksByStatusTx(ctx, tx, teamID, TaskStatusFailed)
			if err != nil {
				return err
			}

			status := TeamStatusDone
			if failedCount > 0 {
				status = TeamStatusFailed
			}

			currentStatus, err := loadTeamStatusTx(ctx, tx, teamID)
			if err != nil {
				return err
			}
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
	if !result.Transition {
		return result, nil
	}

	emitTerminalTeamEvent(services.Store, services.Events, TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": string(result.Status),
		},
	})

	if result.Status != TeamStatusDone || services.Planner == nil {
		return result, nil
	}

	summary, err := services.Planner.FinalSummary(ctx, teamID)
	if err != nil {
		return result, nil
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return result, nil
	}

	result.Summary = summary
	emitTerminalTeamEvent(services.Store, services.Events, TeamEvent{
		Type:   "team.summary",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"summary": summary,
		},
	})
	if services.Mailbox != nil {
		_, _ = services.Mailbox.Send(ctx, MailMessage{
			TeamID:    teamID,
			FromAgent: "lead",
			ToAgent:   "*",
			Kind:      "done",
			Body:      summary,
		})
	}
	return result, nil
}

func countTasksByStatusTx(ctx context.Context, tx *sql.Tx, teamID string, statuses ...TaskStatus) (int, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	placeholders := make([]string, 0, len(statuses))
	args := make([]interface{}, 0, len(statuses)+1)
	args = append(args, teamID)
	for _, status := range statuses {
		placeholders = append(placeholders, "?")
		args = append(args, string(status))
	}
	row := tx.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM team_tasks
		WHERE team_id = ? AND status IN (`+strings.Join(placeholders, ",")+`)
	`, args...)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("count team tasks: %w", err)
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

func emitTerminalTeamEvent(store Store, events *TeamEventBus, event TeamEvent) {
	if events != nil {
		events.Publish(event)
	}
	if store != nil {
		_, _ = store.AppendTeamEvent(context.Background(), event)
	}
}

// IsSQLiteLockError reports whether err represents a transient SQLite lock failure.
func IsSQLiteLockError(err error) bool {
	message := strings.ToLower(strings.TrimSpace(errorString(err)))
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked")
}
