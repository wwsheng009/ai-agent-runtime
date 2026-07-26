package team

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// ReconcileFailedTaskDependencies applies dependency failure propagation and
// its durable events in one transaction.
func (s *SQLiteStore) ReconcileFailedTaskDependencies(ctx context.Context, teamID string) ([]DependencyFailure, error) {
	if s == nil {
		return nil, fmt.Errorf("team store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return nil, err
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}

	applied := make([]DependencyFailure, 0)
	err := s.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		tasks, err := loadDependencyFailureTasksTx(ctx, tx, teamID)
		if err != nil {
			return err
		}
		dependencies, err := loadTaskDependenciesTx(ctx, tx, teamID)
		if err != nil {
			return err
		}
		failures := resolveDependencyFailuresFromGraph(tasks, dependencies)
		now := time.Now().UTC()
		for _, failure := range failures {
			result, err := tx.ExecContext(ctx, `
				UPDATE agent_control_task_records
				SET status = ?, summary = ?, assignee = NULL, session_id = NULL,
					agent_path = NULL, lease_until = NULL, version = version + 1, updated_at = ?
				WHERE workflow = ? AND team_id = ? AND task_id = ?
				  AND status IN (?, ?, ?)
			`, string(TaskStatusFailed), failure.Summary, formatTime(now),
				agentcontrol.WorkflowSpawnTeam, teamID, failure.TaskID,
				string(TaskStatusPending), string(TaskStatusReady), string(TaskStatusBlocked))
			if err != nil {
				return fmt.Errorf("fail dependency-blocked task %s: %w", failure.TaskID, err)
			}
			affected, _ := result.RowsAffected()
			if affected == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM team_path_claims WHERE task_id = ?`, failure.TaskID); err != nil {
				return fmt.Errorf("release dependency-blocked task claims: %w", err)
			}
			if failure.Assignee != "" {
				if _, err := tx.ExecContext(ctx, `
					UPDATE teammates SET state = ?, updated_at = ? WHERE id = ?
				`, string(TeammateStateIdle), formatTime(now), failure.Assignee); err != nil {
					return fmt.Errorf("release dependency-blocked teammate: %w", err)
				}
			}
			if err := appendDependencyFailureEventTx(ctx, tx, dependencyFailureEvent(teamID, failure, now)); err != nil {
				return err
			}
			applied = append(applied, failure)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, failure := range applied {
		_ = s.appendTaskSignalForTask(ctx, failure.TaskID, TaskSignalTaskStatus, TaskStatusFailed)
	}
	return applied, nil
}

func loadDependencyFailureTasksTx(ctx context.Context, tx *sql.Tx, teamID string) ([]Task, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT task_id, status, COALESCE(summary, ''), COALESCE(assignee, '')
		FROM agent_control_task_records
		WHERE workflow = ? AND team_id = ?
		ORDER BY task_id ASC
	`, agentcontrol.WorkflowSpawnTeam, teamID)
	if err != nil {
		return nil, fmt.Errorf("load dependency failure tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]Task, 0)
	for rows.Next() {
		var task Task
		var status, assignee string
		if err := rows.Scan(&task.ID, &status, &task.Summary, &assignee); err != nil {
			return nil, fmt.Errorf("scan dependency failure task: %w", err)
		}
		task.Status = TaskStatus(status)
		if assignee = strings.TrimSpace(assignee); assignee != "" {
			task.Assignee = &assignee
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func loadTaskDependenciesTx(ctx context.Context, tx *sql.Tx, teamID string) (map[string][]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT task_id, depends_on_id
		FROM agent_control_task_dependencies
		WHERE workflow = ? AND team_id = ?
		ORDER BY task_id ASC, depends_on_id ASC
	`, agentcontrol.WorkflowSpawnTeam, teamID)
	if err != nil {
		return nil, fmt.Errorf("load task dependencies: %w", err)
	}
	defer rows.Close()

	dependencies := make(map[string][]string)
	for rows.Next() {
		var taskID, dependencyID string
		if err := rows.Scan(&taskID, &dependencyID); err != nil {
			return nil, fmt.Errorf("scan task dependency: %w", err)
		}
		taskID = strings.TrimSpace(taskID)
		dependencyID = strings.TrimSpace(dependencyID)
		if taskID != "" && dependencyID != "" {
			dependencies[taskID] = append(dependencies[taskID], dependencyID)
		}
	}
	return dependencies, rows.Err()
}

func appendDependencyFailureEventTx(ctx context.Context, tx *sql.Tx, event TeamEvent) error {
	payloadJSON, err := encodeMetadata(event.Payload)
	if err != nil {
		return err
	}
	createdAt := formatTime(event.Timestamp)
	var seq int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1 FROM team_events WHERE team_id = ?
	`, event.TeamID).Scan(&seq); err != nil {
		return fmt.Errorf("next dependency failure event seq: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_events (team_id, seq, type, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, event.TeamID, seq, event.Type, payloadJSON, createdAt); err != nil {
		return fmt.Errorf("insert dependency failure event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO agent_control_task_graph_events (
			workflow, team_id, team_seq, type, payload_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, agentcontrol.WorkflowSpawnTeam, event.TeamID, seq, event.Type, payloadJSON, createdAt); err != nil {
		return fmt.Errorf("insert dependency failure graph event: %w", err)
	}
	return nil
}
