package team

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyBlockedTaskOutcomeBlocksAndReplans(t *testing.T) {
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
	assignee := "mate-1"
	taskID, err := store.CreateTask(ctx, Task{
		TeamID:   teamID,
		Title:    "blocked task",
		Status:   TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)
	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)

	result, err := ApplyBlockedTaskOutcome(ctx, TaskOutcomeApplyServices{
		Store:   store,
		Mailbox: NewMailboxService(store),
		Planner: &LeadPlanner{
			Sessions: &staticSessionClient{
				result: &SessionResult{
					Success: true,
					Output:  `{"tasks":[{"id":"task-followup","title":"follow up","goal":"collect missing info"}]}`,
				},
			},
			Store: store,
		},
	}, BlockedTaskOutcomeRequest{
		Team: Team{
			ID:            teamID,
			LeadSessionID: "lead-session",
		},
		Task:       *task,
		TeammateID: "mate-1",
		Outcome: TaskOutcomeContract{
			Summary: "waiting on architecture review",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, TaskStatusBlocked, result.Task.Status)
	assert.Equal(t, "waiting on architecture review", result.Summary)
	assert.True(t, result.AutoReplan)
	assert.True(t, result.Replanned())
	require.NotNil(t, result.PlanResult)
	require.Len(t, result.PlanResult.Tasks, 1)
	require.NotNil(t, result.Message)
	assert.Equal(t, "lead", result.Message.ToAgent)
	assert.Equal(t, "warning", result.Message.Kind)

	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	assert.Equal(t, TeammateStateBlocked, mate.State)
}

func TestApplyBlockedTaskOutcomeHandoffSkipsReplan(t *testing.T) {
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
	assignee := "mate-1"
	taskID, err := store.CreateTask(ctx, Task{
		TeamID:   teamID,
		Title:    "handoff task",
		Status:   TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)
	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)

	result, err := ApplyBlockedTaskOutcome(ctx, TaskOutcomeApplyServices{
		Store:   store,
		Mailbox: NewMailboxService(store),
		Planner: &LeadPlanner{
			Sessions: &staticSessionClient{
				result: &SessionResult{
					Success: true,
					Output:  `{"tasks":[{"id":"should-not-run","title":"unused","goal":"unused"}]}`,
				},
			},
			Store: store,
		},
	}, BlockedTaskOutcomeRequest{
		Team: Team{
			ID:            teamID,
			LeadSessionID: "lead-session",
		},
		Task:       *task,
		TeammateID: "mate-1",
		Outcome: TaskOutcomeContract{
			Status:    TaskOutcomeHandoff,
			Summary:   "pass to reviewer",
			Blocker:   "need review",
			HandoffTo: "mate-2",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, TaskStatusBlocked, result.Task.Status)
	assert.Equal(t, "mate-2", result.HandoffTo)
	assert.False(t, result.AutoReplan)
	assert.False(t, result.Replanned())
	assert.Nil(t, result.PlanResult)
	require.NotNil(t, result.Message)
	assert.Equal(t, "mate-2", result.Message.ToAgent)
	assert.Equal(t, "handoff", result.Message.Kind)
}

func TestApplyTerminalTaskOutcomeReleasesTaskAndSetsResultRef(t *testing.T) {
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
	assignee := "mate-1"
	taskID, err := store.CreateTask(ctx, Task{
		TeamID:   teamID,
		Title:    "terminal task",
		Status:   TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)
	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)

	resultRef := "artifact://build-1"
	result, err := ApplyTerminalTaskOutcome(ctx, TaskOutcomeApplyServices{
		Store: store,
	}, TerminalTaskOutcomeRequest{
		Task:          *task,
		TeammateID:    "mate-1",
		ResultRef:     &resultRef,
		DefaultStatus: TaskOutcomeDone,
		Outcome: TaskOutcomeContract{
			Summary: "artifact published",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, TaskStatusDone, result.Status)
	assert.Equal(t, "artifact published", result.Summary)
	require.NotNil(t, result.Task.ResultRef)
	assert.Equal(t, "artifact://build-1", *result.Task.ResultRef)
	assert.Nil(t, result.Task.Assignee)
	assert.Nil(t, result.Task.LeaseUntil)

	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	assert.Equal(t, TeammateStateIdle, mate.State)
}
