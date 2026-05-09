package team

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// FindTeammateBySession locates the team teammate that owns a runtime session.
// It is used by AgentControl registry projections without requiring a separate
// teammate registry table.
func FindTeammateBySession(ctx context.Context, store Store, sessionID string) (*Team, *Teammate, error) {
	if store == nil {
		return nil, nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil, nil
	}
	teams, err := store.ListTeams(ctx, TeamFilter{})
	if err != nil {
		return nil, nil, err
	}
	for _, record := range teams {
		teamID := strings.TrimSpace(record.ID)
		if teamID == "" {
			continue
		}
		teammates, err := store.ListTeammates(ctx, teamID)
		if err != nil {
			return nil, nil, err
		}
		for _, mate := range teammates {
			if !strings.EqualFold(strings.TrimSpace(mate.SessionID), sessionID) {
				continue
			}
			teamCopy := record
			mateCopy := mate
			return &teamCopy, &mateCopy, nil
		}
	}
	return nil, nil, nil
}

// ActiveTaskForAssignee returns the most relevant active task for a teammate.
func ActiveTaskForAssignee(ctx context.Context, store Store, teamID, assignee string) (*Task, error) {
	if store == nil {
		return nil, nil
	}
	teamID = strings.TrimSpace(teamID)
	assignee = strings.TrimSpace(assignee)
	if teamID == "" || assignee == "" {
		return nil, nil
	}
	tasks, err := store.ListTasks(ctx, TaskFilter{
		TeamID:   teamID,
		Assignee: &assignee,
		Status: []TaskStatus{
			TaskStatusRunning,
			TaskStatusReady,
			TaskStatusBlocked,
			TaskStatusPending,
		},
	})
	if err != nil {
		return nil, err
	}
	var selected *Task
	for _, task := range tasks {
		taskCopy := task
		if selected == nil || activeTaskStatusRank(taskCopy.Status) < activeTaskStatusRank(selected.Status) {
			selected = &taskCopy
		}
	}
	return selected, nil
}

// AgentControlTaskRecords projects team tasks into the shared AgentControl
// task read model for legacy/non-SQLite stores. SQLite stores should read the
// persisted AgentControl task graph directly.
func AgentControlTaskRecords(ctx context.Context, store Store, teamID string) ([]agentcontrol.TaskRecord, error) {
	if store == nil {
		return nil, nil
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, nil
	}
	tasks, err := store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	teammates, err := store.ListTeammates(ctx, teamID)
	if err != nil {
		return nil, err
	}
	teammatesByID := make(map[string]Teammate, len(teammates))
	for _, mate := range teammates {
		if id := strings.TrimSpace(mate.ID); id != "" {
			teammatesByID[id] = mate
		}
	}
	records := make([]agentcontrol.TaskRecord, 0, len(tasks))
	for _, task := range tasks {
		var mate *Teammate
		if assignee := taskAssigneeID(task); assignee != "" {
			if record, ok := teammatesByID[assignee]; ok {
				mateCopy := record
				mate = &mateCopy
			}
		}
		records = append(records, AgentControlTaskRecord(task, mate))
	}
	return records, nil
}

// ActiveAgentControlTaskRecordForAssignee returns the current active task as a
// shared AgentControl read-model record.
func ActiveAgentControlTaskRecordForAssignee(ctx context.Context, store Store, teamID, assignee string) (*agentcontrol.TaskRecord, error) {
	return NewAgentControlTaskRegistry(store).ActiveTaskForAssignee(ctx, teamID, assignee)
}

// AgentControlTaskRecord projects one native team task into the shared
// AgentControl task read model.
func AgentControlTaskRecord(task Task, teammate *Teammate) agentcontrol.TaskRecord {
	record := agentcontrol.TaskRecord{
		ID:        task.ID,
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		TeamID:    task.TeamID,
		Assignee:  taskAssigneeID(task),
		Title:     task.Title,
		Summary:   task.Summary,
		Status:    string(task.Status),
		Priority:  task.Priority,
		CreatedAt: task.CreatedAt,
		UpdatedAt: task.UpdatedAt,
	}
	if task.ParentTaskID != nil {
		record.ParentTaskID = strings.TrimSpace(*task.ParentTaskID)
	}
	if teammate != nil {
		record.SessionID = strings.TrimSpace(teammate.SessionID)
		record.Path = agentcontrol.TeamTeammatePath(task.TeamID, teammate.ID, teammate.Name, teammate.SessionID)
	} else if record.Assignee != "" {
		record.Path = agentcontrol.TeamTeammatePath(task.TeamID, record.Assignee, "", "")
	}
	return record.Normalize()
}

func taskAssigneeID(task Task) string {
	if task.Assignee == nil {
		return ""
	}
	return strings.TrimSpace(*task.Assignee)
}

func activeTaskStatusRank(status TaskStatus) int {
	switch status {
	case TaskStatusRunning:
		return 0
	case TaskStatusReady:
		return 1
	case TaskStatusBlocked:
		return 2
	case TaskStatusPending:
		return 3
	default:
		return 100
	}
}
