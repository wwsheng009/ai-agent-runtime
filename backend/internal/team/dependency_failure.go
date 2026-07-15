package team

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const TaskDependencyFailedEvent = "task.dependency_failed"

// DependencyFailure describes one task transitioned to failed because an
// upstream dependency can no longer complete.
type DependencyFailure struct {
	TaskID            string     `json:"task_id"`
	DependencyID      string     `json:"dependency_id"`
	DependencyStatus  TaskStatus `json:"dependency_status"`
	DependencySummary string     `json:"dependency_summary,omitempty"`
	Summary           string     `json:"summary"`
	Assignee          string     `json:"assignee,omitempty"`
}

type dependencyFailureStore interface {
	ReconcileFailedTaskDependencies(ctx context.Context, teamID string) ([]DependencyFailure, error)
}

// ReconcileFailedTaskDependencies recursively fails tasks whose dependencies
// failed or were cancelled. SQLite uses an atomic implementation; the generic
// path keeps alternate Store implementations behaviorally compatible.
func ReconcileFailedTaskDependencies(ctx context.Context, store Store, teamID string) ([]DependencyFailure, error) {
	if store == nil {
		return nil, fmt.Errorf("team store is not configured")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	if reconciler, ok := store.(dependencyFailureStore); ok {
		return reconciler.ReconcileFailedTaskDependencies(ctx, teamID)
	}

	tasks, err := store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	if err != nil {
		return nil, err
	}
	failures, err := resolveDependencyFailures(ctx, store, tasks)
	if err != nil {
		return nil, err
	}
	for _, failure := range failures {
		if err := store.UpdateTaskStatus(ctx, failure.TaskID, TaskStatusFailed, failure.Summary); err != nil {
			return nil, err
		}
		if err := store.ReleaseTask(ctx, failure.TaskID, TaskStatusFailed); err != nil {
			return nil, err
		}
		if err := store.ReleasePathClaimsByTask(ctx, failure.TaskID); err != nil {
			return nil, err
		}
		if failure.Assignee != "" {
			_ = store.UpdateTeammateState(ctx, failure.Assignee, TeammateStateIdle)
		}
		if _, err := store.AppendTeamEvent(ctx, dependencyFailureEvent(teamID, failure, time.Now().UTC())); err != nil {
			return nil, err
		}
	}
	return failures, nil
}

func resolveDependencyFailures(ctx context.Context, store Store, tasks []Task) ([]DependencyFailure, error) {
	dependencies := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		dependencyIDs, err := store.ListTaskDependencies(ctx, task.ID)
		if err != nil {
			return nil, err
		}
		dependencies[task.ID] = dependencyIDs
	}
	return resolveDependencyFailuresFromGraph(tasks, dependencies), nil
}

func resolveDependencyFailuresFromGraph(tasks []Task, dependencies map[string][]string) []DependencyFailure {
	tasksByID := make(map[string]Task, len(tasks))
	for _, task := range tasks {
		tasksByID[strings.TrimSpace(task.ID)] = task
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	failures := make([]DependencyFailure, 0)
	for changed := true; changed; {
		changed = false
		for _, candidate := range tasks {
			task := tasksByID[candidate.ID]
			if !dependencyFailureEligible(task.Status) {
				continue
			}
			dependencyIDs := append([]string(nil), dependencies[task.ID]...)
			sort.Strings(dependencyIDs)
			for _, dependencyID := range dependencyIDs {
				dependency, ok := tasksByID[strings.TrimSpace(dependencyID)]
				if !ok || !failedDependencyStatus(dependency.Status) {
					continue
				}
				failure := newDependencyFailure(task, dependency)
				failures = append(failures, failure)
				task.Status = TaskStatusFailed
				task.Summary = failure.Summary
				tasksByID[task.ID] = task
				changed = true
				break
			}
		}
	}
	return failures
}

func dependencyFailureEligible(status TaskStatus) bool {
	return status == TaskStatusPending || status == TaskStatusReady || status == TaskStatusBlocked
}

func failedDependencyStatus(status TaskStatus) bool {
	return status == TaskStatusFailed || status == TaskStatusCancelled
}

func newDependencyFailure(task, dependency Task) DependencyFailure {
	dependencySummary := strings.TrimSpace(dependency.Summary)
	summary := fmt.Sprintf("dependency %s %s", dependency.ID, dependency.Status)
	if dependencySummary != "" {
		summary += ": " + dependencySummary
	}
	assignee := ""
	if task.Assignee != nil {
		assignee = strings.TrimSpace(*task.Assignee)
	}
	return DependencyFailure{
		TaskID:            strings.TrimSpace(task.ID),
		DependencyID:      strings.TrimSpace(dependency.ID),
		DependencyStatus:  dependency.Status,
		DependencySummary: dependencySummary,
		Summary:           summary,
		Assignee:          assignee,
	}
}

func dependencyFailureEvent(teamID string, failure DependencyFailure, timestamp time.Time) TeamEvent {
	return TeamEvent{
		Type:      TaskDependencyFailedEvent,
		TeamID:    strings.TrimSpace(teamID),
		Timestamp: timestamp,
		Payload: map[string]interface{}{
			"task_id":            failure.TaskID,
			"dependency_id":      failure.DependencyID,
			"dependency_status":  string(failure.DependencyStatus),
			"dependency_summary": failure.DependencySummary,
			"summary":            failure.Summary,
			"propagated":         true,
		},
	}
}

func publishDependencyFailureEvents(events *TeamEventBus, teamID string, failures []DependencyFailure) {
	if events == nil {
		return
	}
	now := time.Now().UTC()
	for _, failure := range failures {
		events.Publish(dependencyFailureEvent(teamID, failure, now))
	}
}
