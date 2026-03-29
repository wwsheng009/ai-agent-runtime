package team

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturingSessionClient struct {
	result      *SessionResult
	err         error
	sessionID   string
	prompt      string
	lastRunMeta *RunMeta
}

func (c *capturingSessionClient) SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *RunMeta) (*SessionResult, error) {
	c.sessionID = sessionID
	c.prompt = prompt
	c.lastRunMeta = runMeta.Clone()
	if c.err != nil {
		return nil, c.err
	}
	if c.result != nil {
		return c.result, nil
	}
	return &SessionResult{Success: true, Output: ""}, nil
}

func TestTeammateRunnerStartTaskIncludesRunMeta(t *testing.T) {
	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  "summary: completed",
		},
	}
	runner := &TeammateRunner{Sessions: client}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		Name:      "Mate",
		SessionID: "session-1",
	}, Task{
		ID:     "task-1",
		TeamID: "team-1",
		Title:  "Implement change",
		Goal:   "Implement change",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, client.lastRunMeta)
	require.NotNil(t, client.lastRunMeta.Team)

	assert.Equal(t, "team-1", client.lastRunMeta.Team.TeamID)
	assert.Equal(t, "mate-1", client.lastRunMeta.Team.AgentID)
	assert.Equal(t, "task-1", client.lastRunMeta.Team.CurrentTaskID)
	assert.Equal(t, "bypass_permissions", client.lastRunMeta.PermissionMode)
}

func TestLeadPlannerInitialPlanIncludesTeamRunMeta(t *testing.T) {
	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  `{"tasks":[{"id":"task-1","title":"Plan","goal":"Do work"}]}`,
		},
	}
	planner := &LeadPlanner{Sessions: client}

	result, err := planner.InitialPlan(context.Background(), Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
	}, "Ship feature")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, client.lastRunMeta)
	require.NotNil(t, client.lastRunMeta.Team)

	assert.Equal(t, "team-1", client.lastRunMeta.Team.TeamID)
	assert.Equal(t, "", client.lastRunMeta.Team.AgentID)
	assert.Equal(t, "", client.lastRunMeta.Team.CurrentTaskID)
	assert.Equal(t, "bypass_permissions", client.lastRunMeta.PermissionMode)
}

func TestLeadPlannerReplanOnFailureIncludesTaskRunMeta(t *testing.T) {
	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  `{"tasks":[{"id":"task-2","title":"Retry","goal":"Retry work"}]}`,
		},
	}
	planner := &LeadPlanner{Sessions: client}

	result, err := planner.ReplanOnFailure(context.Background(), Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
	}, Task{
		ID:     "task-9",
		TeamID: "team-1",
		Title:  "Broken step",
		Goal:   "Fix broken step",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, client.lastRunMeta)
	require.NotNil(t, client.lastRunMeta.Team)

	assert.Equal(t, "team-1", client.lastRunMeta.Team.TeamID)
	assert.Equal(t, "", client.lastRunMeta.Team.AgentID)
	assert.Equal(t, "task-9", client.lastRunMeta.Team.CurrentTaskID)
	assert.Equal(t, "bypass_permissions", client.lastRunMeta.PermissionMode)
}

func TestLeadPlannerReplanOnFailureIncludesTeamContextInPrompt(t *testing.T) {
	store, err := NewSQLiteStore(&StoreConfig{
		DSN: "file:lead-planner-context-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, Team{
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "Broken step",
		Goal:   "Fix broken step",
		Status: TaskStatusFailed,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "Ready follow-up",
		Goal:   "Handle next work",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)

	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  `{"tasks":[{"id":"task-2","title":"Retry","goal":"Retry work"}]}`,
		},
	}
	planner := &LeadPlanner{
		Sessions: client,
		Store:    store,
	}

	result, err := planner.ReplanOnFailure(ctx, Team{
		ID:            teamID,
		LeadSessionID: "lead-session",
	}, Task{
		ID:     taskID,
		TeamID: teamID,
		Title:  "Broken step",
		Goal:   "Fix broken step",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, client.prompt, "Team context:")
	assert.Contains(t, client.prompt, "Ready follow-up")
}

func TestLeadPlannerFinalSummaryIncludesTeamContextInPrompt(t *testing.T) {
	store, err := NewSQLiteStore(&StoreConfig{
		DSN: "file:lead-summary-context-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, Team{
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "Completed task",
		Goal:   "ship the feature",
		Status: TaskStatusDone,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "Blocked task",
		Goal:   "resolve review feedback",
		Status: TaskStatusBlocked,
	})
	require.NoError(t, err)

	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  "summary from lead",
		},
	}
	planner := &LeadPlanner{
		Sessions: client,
		Store:    store,
	}

	summary, err := planner.FinalSummary(ctx, teamID)
	require.NoError(t, err)
	assert.Equal(t, "summary from lead", summary)
	assert.Contains(t, client.prompt, "Team context:")
	assert.Contains(t, client.prompt, "Blocked task")
}
