package team

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dsn := fmt.Sprintf("file:team_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	store, err := NewSQLiteStore(&StoreConfig{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestOrchestratorClaimReadyTasksHonorsMaxWriters(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{MaxWriters: 1})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-b", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)

	_, err = store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "task-1",
		Status:     TaskStatusReady,
		WritePaths: []string{"a.txt"},
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "task-2",
		Status:     TaskStatusReady,
		WritePaths: []string{"b.txt"},
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	assignments, err := orchestrator.ClaimReadyTasks(ctx, teamID, 0)
	require.NoError(t, err)
	require.Len(t, assignments, 1)

	running, err := store.ListTasks(ctx, TaskFilter{
		TeamID: teamID,
		Status: []TaskStatus{TaskStatusRunning},
	})
	require.NoError(t, err)
	require.Len(t, running, 1)
}

func TestOrchestratorClaimReadyTasksRespectsPinnedAssignee(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-b", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)

	assignee := "mate-b"
	_, err = store.CreateTask(ctx, Task{
		TeamID:   teamID,
		Title:    "pinned",
		Status:   TaskStatusReady,
		Assignee: &assignee,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "unassigned",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	assignments, err := orchestrator.ClaimReadyTasks(ctx, teamID, 1)
	require.NoError(t, err)
	require.Len(t, assignments, 1)
	require.Equal(t, assignee, assignments[0].Teammate.ID)
}

func TestOrchestratorClaimReadyTasksWithClaimsCreatesPathClaim(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{ID: "mate-a", TeamID: teamID, State: TeammateStateIdle})
	require.NoError(t, err)

	taskID, err := store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "claimed-with-paths",
		Status:     TaskStatusReady,
		WritePaths: []string{"src/file.txt"},
	})
	require.NoError(t, err)

	claims := NewPathClaimManager(store, "workspace")
	orchestrator := NewOrchestrator(store, claims, nil)
	assignments, err := orchestrator.ClaimReadyTasks(ctx, teamID, 0)
	require.NoError(t, err)
	require.Len(t, assignments, 1)
	require.Equal(t, taskID, assignments[0].Task.ID)
	require.Equal(t, "mate-a", assignments[0].Teammate.ID)

	pathClaims, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Len(t, pathClaims, 1)
	assert.Equal(t, "workspace/src/file.txt", pathClaims[0].Path)
	assert.Equal(t, PathClaimWrite, pathClaims[0].Mode)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, TaskStatusRunning, task.Status)
	require.NotNil(t, task.Assignee)
	assert.Equal(t, "mate-a", *task.Assignee)
}

func TestOrchestratorRunStopsWhenTeamNotActive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{
		Status: TeamStatusDone,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.TickInterval = 10 * time.Millisecond
	require.NoError(t, orchestrator.Run(ctx, teamID))
}

func TestOrchestratorExecuteAssignmentBlocksTaskAndReplans(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "blocked task",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.Mailbox = NewMailboxService(store)
	orchestrator.Runner = &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "```json\n{\"task_status\":\"blocked\",\"summary\":\"waiting on architecture review\",\"blocker\":\"need architecture review\"}\n```",
			},
		},
	}
	orchestrator.LeadPlanner = &LeadPlanner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  `{"tasks":[{"id":"task-followup","title":"follow up","goal":"collect missing info"}]}`,
			},
		},
		Store: store,
	}

	orchestrator.executeAssignment(ctx, teamID, Assignment{
		Task: Task{
			ID:     taskID,
			TeamID: teamID,
			Title:  "blocked task",
		},
		Teammate: Teammate{
			ID:        "mate-1",
			TeamID:    teamID,
			SessionID: "mate-session",
		},
	})

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, TaskStatusBlocked, task.Status)

	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	assert.Equal(t, TeammateStateBlocked, mate.State)

	tasks, err := store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
}

func TestOrchestratorBlockedReplanFollowupClaimsDifferentIdleTeammate(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session-1",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-2",
		TeamID:    teamID,
		SessionID: "mate-session-2",
		State:     TeammateStateIdle,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "blocked task",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.Mailbox = NewMailboxService(store)
	orchestrator.Runner = &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "```json\n{\"task_status\":\"blocked\",\"summary\":\"waiting on architecture review\",\"blocker\":\"need architecture review\"}\n```",
			},
		},
	}
	orchestrator.LeadPlanner = &LeadPlanner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  `{"tasks":[{"id":"task-followup","title":"follow up","goal":"collect missing info"}]}`,
			},
		},
		Store: store,
	}

	orchestrator.executeAssignment(ctx, teamID, Assignment{
		Task: Task{
			ID:     taskID,
			TeamID: teamID,
			Title:  "blocked task",
		},
		Teammate: Teammate{
			ID:        "mate-1",
			TeamID:    teamID,
			SessionID: "mate-session-1",
		},
	})

	assignments, err := orchestrator.ClaimReadyTasks(ctx, teamID, 0)
	require.NoError(t, err)
	require.Len(t, assignments, 1)
	assert.Equal(t, "follow up", assignments[0].Task.Title)
	assert.Equal(t, "mate-2", assignments[0].Teammate.ID)

	followupTasks, err := store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	require.NoError(t, err)
	var followupTask *Task
	for i := range followupTasks {
		if followupTasks[i].Title == "follow up" {
			followupTask = &followupTasks[i]
			break
		}
	}
	require.NotNil(t, followupTask)
	assert.Equal(t, TaskStatusRunning, followupTask.Status)
	require.NotNil(t, followupTask.Assignee)
	assert.Equal(t, "mate-2", *followupTask.Assignee)

	mate1, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate1)
	assert.Equal(t, TeammateStateBlocked, mate1.State)

	mate2, err := store.GetTeammate(ctx, "mate-2")
	require.NoError(t, err)
	require.NotNil(t, mate2)
	assert.Equal(t, TeammateStateBusy, mate2.State)
}

func TestOrchestratorExecuteAssignmentFailsProtocolErrorOutput(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "protocol task",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.Runner = &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "blocked: waiting on architecture review",
			},
		},
	}

	orchestrator.executeAssignment(ctx, teamID, Assignment{
		Task: Task{
			ID:     taskID,
			TeamID: teamID,
			Title:  "protocol task",
		},
		Teammate: Teammate{
			ID:        "mate-1",
			TeamID:    teamID,
			SessionID: "mate-session",
		},
	})

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, TaskStatusFailed, task.Status)
	assert.Contains(t, task.Summary, "protocol error")

	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	assert.Equal(t, TeammateStateIdle, mate.State)
}

func TestOrchestratorExecuteAssignmentMarksTeamDoneWhenLastTaskCompletes(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		Status:        TeamStatusActive,
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "final task",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.Runner = &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"all done\"}\n```",
			},
		},
	}
	orchestrator.LeadPlanner = &LeadPlanner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "lead summary",
			},
		},
		Store: store,
	}

	orchestrator.executeAssignment(ctx, teamID, Assignment{
		Task: Task{
			ID:     taskID,
			TeamID: teamID,
			Title:  "final task",
		},
		Teammate: Teammate{
			ID:        "mate-1",
			TeamID:    teamID,
			SessionID: "mate-session",
		},
	})

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, TaskStatusDone, task.Status)

	teamRecord, err := store.GetTeam(ctx, teamID)
	require.NoError(t, err)
	require.NotNil(t, teamRecord)
	assert.Equal(t, TeamStatusDone, teamRecord.Status)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, events, 4)
	assert.Equal(t, "task.started", events[0].Type)
	assert.Equal(t, "task.completed", events[1].Type)
	assert.Equal(t, "all done", events[1].Payload["summary"])
	assert.Equal(t, "team.completed", events[2].Type)
	assert.Equal(t, "done", events[2].Payload["status"])
	assert.Equal(t, "team.summary", events[3].Type)
	assert.Equal(t, "lead summary", events[3].Payload["summary"])
}

func TestOrchestratorExecuteAssignmentSendsLeadProgressMailbox(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		Status: TeamStatusActive,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "final task",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	orchestrator := NewOrchestrator(store, nil, nil)
	orchestrator.Mailbox = NewMailboxService(store)
	orchestrator.Runner = &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"all done\"}\n```",
			},
		},
	}

	orchestrator.executeAssignment(ctx, teamID, Assignment{
		Task: Task{
			ID:     taskID,
			TeamID: teamID,
			Title:  "final task",
		},
		Teammate: Teammate{
			ID:        "mate-1",
			TeamID:    teamID,
			SessionID: "mate-session",
		},
	})

	messages, err := store.ListMail(ctx, MailFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, messages, 2)
	byKind := map[string]MailMessage{}
	for _, message := range messages {
		byKind[message.Kind] = message
	}
	progress, ok := byKind["progress"]
	require.True(t, ok, "expected progress mailbox message")
	assert.Equal(t, "lead", progress.ToAgent)
	assert.Equal(t, "mate-1", progress.FromAgent)
	assert.Equal(t, taskID, *progress.TaskID)
	assert.Contains(t, progress.Body, "Started task")

	done, ok := byKind["done"]
	require.True(t, ok, "expected done mailbox message")
	assert.Equal(t, "lead", done.ToAgent)
	assert.Equal(t, "mate-1", done.FromAgent)
	assert.Equal(t, taskID, *done.TaskID)
	assert.Equal(t, "all done", done.Body)
}
