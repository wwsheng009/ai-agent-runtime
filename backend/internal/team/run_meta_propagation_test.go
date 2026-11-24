package team

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
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
		return c.result, c.err
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
	assert.Equal(t, "complete_task", client.lastRunMeta.CompletionRequirement)
	assert.Equal(t, "complete_task", EffectiveCompletionRequirement(client.lastRunMeta))
}

func TestEffectiveCompletionRequirement(t *testing.T) {
	assert.Equal(t, "none", EffectiveCompletionRequirement(nil))
	assert.Equal(t, "none", EffectiveCompletionRequirement(&RunMeta{}))
	assert.Equal(t, "none", EffectiveCompletionRequirement(&RunMeta{
		Team: &TeamRunMeta{TeamID: "team-1", AgentID: "mate-1", CurrentTaskID: "task-1"},
	}))
	assert.Equal(t, "complete_task", EffectiveCompletionRequirement(&RunMeta{
		CompletionRequirement: "complete_task",
		Team:                  &TeamRunMeta{TeamID: "team-1", AgentID: "mate-1"},
	}))
	assert.Equal(t, "none", EffectiveCompletionRequirement(&RunMeta{
		CompletionRequirement: "none",
		Team:                  &TeamRunMeta{TeamID: "team-1", AgentID: "mate-1"},
	}))
}

func TestTeamRunMetaCloneCopiesRouteWarnings(t *testing.T) {
	original := &TeamRunMeta{
		TeamID:        "team-1",
		AgentID:       "mate-1",
		CurrentTaskID: "task-1",
		RouteWarnings: []string{"warning-one"},
	}

	clone := original.Clone()
	require.NotNil(t, clone)
	require.Equal(t, []string{"warning-one"}, clone.RouteWarnings)

	clone.RouteWarnings[0] = "changed"
	require.Equal(t, []string{"warning-one"}, original.RouteWarnings)
}

func TestRunMetaCloneCopiesBudgetAndSource(t *testing.T) {
	original := &RunMeta{
		Source:         "cli",
		BudgetTokens:   4200,
		PermissionMode: "plan",
		Team:           &TeamRunMeta{TeamID: "team-1"},
	}
	clone := original.Clone()
	require.NotNil(t, clone)
	require.Equal(t, "cli", clone.Source)
	require.Equal(t, 4200, clone.BudgetTokens)
	require.Equal(t, "plan", clone.PermissionMode)
	require.NotSame(t, original.Team, clone.Team)
}

func TestLeadPlannerInitialPlanIncludesTeamRunMeta(t *testing.T) {
	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  `{"tasks":[{"id":"task-1","title":"Plan","goal":"Do work","difficulty":"Hard","difficulty_rationale":" Requires cross-team coordination. "}]}`,
		},
	}
	planner := &LeadPlanner{Sessions: client}

	result, err := planner.InitialPlan(context.Background(), Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
	}, "Ship feature")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Tasks, 1)
	require.NotNil(t, client.lastRunMeta)
	require.NotNil(t, client.lastRunMeta.Team)

	assert.Equal(t, TaskDifficultyHard, result.Tasks[0].Difficulty)
	assert.Equal(t, "Requires cross-team coordination.", result.Tasks[0].DifficultyRationale)
	assert.Contains(t, client.prompt, `"difficulty":"normal"`)
	assert.Contains(t, client.prompt, "Set difficulty to one of easy, normal, hard, expert")
	assert.Equal(t, "team-1", client.lastRunMeta.Team.TeamID)
	assert.Equal(t, "", client.lastRunMeta.Team.AgentID)
	assert.Equal(t, "", client.lastRunMeta.Team.CurrentTaskID)
	assert.Equal(t, "bypass_permissions", client.lastRunMeta.PermissionMode)
	assert.Equal(t, "none", client.lastRunMeta.CompletionRequirement)
	assert.Equal(t, "none", EffectiveCompletionRequirement(client.lastRunMeta))
}

func TestLeadPlannerReplanOnFailureIncludesTaskRunMeta(t *testing.T) {
	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  `{"tasks":[{"id":"task-2","title":"Retry","goal":"Retry work","difficulty":"expert","difficulty_rationale":"Failure recovery touches shared state."}]}`,
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
	require.Len(t, result.Tasks, 1)
	require.NotNil(t, client.lastRunMeta)
	require.NotNil(t, client.lastRunMeta.Team)

	assert.Equal(t, TaskDifficultyExpert, result.Tasks[0].Difficulty)
	assert.Equal(t, "Failure recovery touches shared state.", result.Tasks[0].DifficultyRationale)
	assert.Equal(t, "team-1", client.lastRunMeta.Team.TeamID)
	assert.Equal(t, "", client.lastRunMeta.Team.AgentID)
	assert.Equal(t, "task-9", client.lastRunMeta.Team.CurrentTaskID)
	assert.Equal(t, "bypass_permissions", client.lastRunMeta.PermissionMode)
}

func TestLeadPlannerInitialPlanRejectsInvalidDifficulty(t *testing.T) {
	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  `{"tasks":[{"id":"task-1","title":"Plan","goal":"Do work","difficulty":"extreme"}]}`,
		},
	}
	planner := &LeadPlanner{Sessions: client}

	result, err := planner.InitialPlan(context.Background(), Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
	}, "Ship feature")
	require.Error(t, err)
	require.Nil(t, result)
	assert.Contains(t, err.Error(), "invalid task difficulty")
}

func TestLeadPlannerInitialPlanWrapsPromptPreflightSessionExecutionError(t *testing.T) {
	client := &capturingSessionClient{
		result: &SessionResult{
			Success:   false,
			Error:     "prompt preflight budget exceeded",
			TraceID:   "trace-plan-preflight",
			ErrorType: "prompt_preflight",
			ErrorMetadata: map[string]interface{}{
				"failure_reason_code":         "prompt_still_exceeds_budget_after_compaction",
				"replacement_history_applied": true,
			},
		},
		err: assert.AnError,
	}
	planner := &LeadPlanner{Sessions: client}

	result, err := planner.InitialPlan(context.Background(), Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
	}, "Ship feature")
	require.Error(t, err)
	require.Nil(t, result)

	sessionErr, ok := AsSessionExecutionError(err)
	require.True(t, ok)
	require.NotNil(t, sessionErr)
	assert.Equal(t, "trace-plan-preflight", sessionErr.TraceID)
	assert.Equal(t, "prompt_preflight", sessionErr.ErrorType)
	assert.Equal(t, "prompt_still_exceeds_budget_after_compaction", sessionErr.CloneMetadata()["failure_reason_code"])
	assert.Equal(t, true, sessionErr.CloneMetadata()["replacement_history_applied"])
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

func TestLeadPlannerAutoPersistDependencyFailureIsReturned(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{LeadSessionID: "lead-session"})
	require.NoError(t, err)
	failedTaskID, err := store.CreateTask(ctx, Task{
		ID:     "failed-replan-source",
		TeamID: teamID,
		Title:  "failed source",
		Status: TaskStatusFailed,
	})
	require.NoError(t, err)
	failedTask, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, failedTask)

	_, err = store.db.ExecContext(ctx, `
		CREATE TRIGGER reject_replan_dependency
		BEFORE INSERT ON agent_control_task_dependencies
		BEGIN
			SELECT RAISE(ABORT, 'dependency write rejected');
		END
	`)
	require.NoError(t, err)

	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output: `{"tasks":[
				{"id":"task-followup","title":"follow up"},
				{"id":"task-verify","title":"verify"}
			],"dependencies":[
				{"task":"task-verify","depends_on":"task-followup"}
			]}`,
		},
	}
	planner := &LeadPlanner{Sessions: client, Store: store, AutoPersist: true}

	result, err := planner.ReplanOnFailureWithVersion(ctx, Team{
		ID:            teamID,
		LeadSessionID: "lead-session",
	}, *failedTask, failedTask.Version)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "persist replan dependency")
	require.Contains(t, err.Error(), "dependency write rejected")

	tasks, err := store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, tasks, 1, "all plan tasks must roll back when dependency persistence fails")
	require.Equal(t, failedTaskID, tasks[0].ID)
	updatedFailed, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, updatedFailed)
	require.Equal(t, failedTask.Version, updatedFailed.Version, "failed task version fence must roll back with the plan")
	dependencies, err := store.ListTaskDependencyRecords(ctx, TaskDependencyFilter{TaskID: "task-verify"})
	require.NoError(t, err)
	require.Empty(t, dependencies)
	events, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Empty(t, events)
}

func TestLeadPlannerVersionedReplanPersistsAtomicallyAndFencesReplay(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{LeadSessionID: "lead-session"})
	require.NoError(t, err)
	failedTaskID, err := store.CreateTask(ctx, Task{
		ID:     "versioned-replan-source",
		TeamID: teamID,
		Title:  "failed source",
		Status: TaskStatusFailed,
	})
	require.NoError(t, err)
	failedTask, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, failedTask)

	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output: `{"tasks":[
				{"id":"versioned-followup","title":"follow up","goal":"continue work"},
				{"id":"versioned-verify","title":"verify","goal":"check work"}
			],"dependencies":[
				{"task":"versioned-verify","depends_on":"versioned-followup"}
			]}`,
		},
	}
	planner := &LeadPlanner{Sessions: client, Store: store, AutoPersist: true}
	result, err := planner.ReplanOnFailureWithVersion(ctx, Team{
		ID:            teamID,
		LeadSessionID: "lead-session",
	}, *failedTask, failedTask.Version)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Tasks, 2)
	require.Len(t, result.Dependencies, 1)
	require.EqualValues(t, 1, result.Tasks[0].Version)
	require.False(t, result.Tasks[0].CreatedAt.IsZero())

	persistedFailed, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, persistedFailed)
	require.Equal(t, failedTask.Version+1, persistedFailed.Version)
	dependencies, err := store.ListTaskDependencyRecords(ctx, TaskDependencyFilter{
		TaskID: "versioned-verify",
	})
	require.NoError(t, err)
	require.Len(t, dependencies, 1)
	require.Equal(t, "versioned-followup", dependencies[0].DependsOnID)
	events, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID:    teamID,
		EventType: TaskDependencyCreatedEvent,
	})
	require.NoError(t, err)
	require.Len(t, events, 1)

	_, err = planner.ReplanOnFailureWithVersion(ctx, Team{
		ID:            teamID,
		LeadSessionID: "lead-session",
	}, *failedTask, failedTask.Version)
	require.Error(t, err)
	require.True(t, errors.Is(err, agentcontrol.ErrTaskVersionConflict))
	tasks, err := store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, tasks, 3, "a stale replay must not create duplicate plan tasks")
}

func TestSQLiteStorePersistTaskPlanOwnsNewTaskExecutionState(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{LeadSessionID: "lead-session"})
	require.NoError(t, err)
	failedTaskID, err := store.CreateTask(ctx, Task{
		ID:     "execution-state-replan-source",
		TeamID: teamID,
		Status: TaskStatusFailed,
	})
	require.NoError(t, err)
	failedTask, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, failedTask)

	leaseUntil := time.Now().UTC().Add(time.Hour)
	err = store.PersistTaskPlanWithVersion(ctx, failedTaskID, failedTask.Version, []Task{{
		ID:           "execution-state-replan-child",
		TeamID:       teamID,
		Status:       TaskStatusPending,
		Version:      99,
		Attempt:      7,
		RetryCount:   4,
		FencingToken: "untrusted-plan-token",
		LeaseUntil:   &leaseUntil,
	}}, nil)
	require.NoError(t, err)

	persisted, err := store.GetTask(ctx, "execution-state-replan-child")
	require.NoError(t, err)
	require.NotNil(t, persisted)
	assert.EqualValues(t, 1, persisted.Version)
	assert.Zero(t, persisted.Attempt)
	assert.Zero(t, persisted.RetryCount)
	assert.Empty(t, persisted.FencingToken)
	assert.Nil(t, persisted.LeaseUntil)

	err = store.PersistTaskPlanWithVersion(ctx, failedTaskID, failedTask.Version, []Task{{
		ID:     "execution-state-running-child",
		TeamID: teamID,
		Status: TaskStatusRunning,
	}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must start pending")
	notPersisted, err := store.GetTask(ctx, "execution-state-running-child")
	require.NoError(t, err)
	assert.Nil(t, notPersisted)
}

func TestSQLiteStorePersistTaskPlanRejectsForeignPlanTeam(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamA, err := store.CreateTeam(ctx, Team{LeadSessionID: "lead-a"})
	require.NoError(t, err)
	teamB, err := store.CreateTeam(ctx, Team{LeadSessionID: "lead-b"})
	require.NoError(t, err)
	failedTaskID, err := store.CreateTask(ctx, Task{
		ID:     "foreign-plan-source",
		TeamID: teamA,
		Status: TaskStatusFailed,
	})
	require.NoError(t, err)
	failedTask, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, failedTask)

	err = store.PersistTaskPlanWithVersion(ctx, failedTaskID, failedTask.Version, []Task{{
		ID:     "foreign-plan-task",
		TeamID: teamB,
	}}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed task does not belong to team")
	task, err := store.GetTask(ctx, "foreign-plan-task")
	require.NoError(t, err)
	require.Nil(t, task)
}

func TestSQLiteStorePersistTaskPlanRejectsCyclesWithoutFencingSource(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{LeadSessionID: "lead-session"})
	require.NoError(t, err)
	failedTaskID, err := store.CreateTask(ctx, Task{
		ID:     "cyclic-replan-source",
		TeamID: teamID,
		Status: TaskStatusFailed,
	})
	require.NoError(t, err)
	failedTask, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, failedTask)

	err = store.PersistTaskPlanWithVersion(ctx, failedTaskID, failedTask.Version, []Task{
		{ID: "cycle-a", TeamID: teamID},
		{ID: "cycle-b", TeamID: teamID},
	}, []TaskDependency{
		{TaskID: "cycle-a", DependsOnID: "cycle-b"},
		{TaskID: "cycle-b", DependsOnID: "cycle-a"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "dependency cycle")

	tasks, err := store.ListTasks(ctx, TaskFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, tasks, 1, "an invalid plan must not be partially materialized")
	unchanged, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, unchanged)
	require.Equal(t, failedTask.Version, unchanged.Version, "validation failure must not consume the source version")
}

func TestSQLiteStorePersistTaskPlanRejectsUnknownAndForeignAssignees(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		assignee      string
		createForeign bool
		wantError     string
	}{
		{name: "unknown", assignee: "missing-member", wantError: "unknown assignee"},
		{name: "foreign team", assignee: "foreign-member", createForeign: true, wantError: "does not belong to team"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore(t)
			teamA, err := store.CreateTeam(ctx, Team{LeadSessionID: "lead-a"})
			require.NoError(t, err)
			teamB, err := store.CreateTeam(ctx, Team{LeadSessionID: "lead-b"})
			require.NoError(t, err)
			if testCase.createForeign {
				_, err = store.UpsertTeammate(ctx, Teammate{
					ID:     testCase.assignee,
					TeamID: teamB,
					State:  TeammateStateIdle,
				})
				require.NoError(t, err)
			}
			failedTaskID, err := store.CreateTask(ctx, Task{
				ID:     "assignee-replan-source",
				TeamID: teamA,
				Status: TaskStatusFailed,
			})
			require.NoError(t, err)
			failedTask, err := store.GetTask(ctx, failedTaskID)
			require.NoError(t, err)
			require.NotNil(t, failedTask)

			assignee := testCase.assignee
			err = store.PersistTaskPlanWithVersion(ctx, failedTaskID, failedTask.Version, []Task{{
				ID:       "invalid-assignee-task",
				TeamID:   teamA,
				Assignee: &assignee,
			}}, nil)
			require.Error(t, err)
			require.Contains(t, err.Error(), testCase.wantError)

			planTask, err := store.GetTask(ctx, "invalid-assignee-task")
			require.NoError(t, err)
			require.Nil(t, planTask, "invalid assignee must not persist a task")
			unchanged, err := store.GetTask(ctx, failedTaskID)
			require.NoError(t, err)
			require.NotNil(t, unchanged)
			require.Equal(t, failedTask.Version, unchanged.Version)
		})
	}
}

func TestLeadPlannerReplanRejectsDependencyOutsideGeneratedPlan(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{LeadSessionID: "lead-session"})
	require.NoError(t, err)
	failedTaskID, err := store.CreateTask(ctx, Task{
		ID:     "external-dependency-source",
		TeamID: teamID,
		Status: TaskStatusFailed,
	})
	require.NoError(t, err)
	failedTask, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, failedTask)

	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output: `{"tasks":[{"id":"planned-task","title":"follow up"}],
				"dependencies":[{"task":"planned-task","depends_on":"external-dependency"}]}`,
		},
	}
	planner := &LeadPlanner{Sessions: client, Store: store, AutoPersist: true}
	result, err := planner.ReplanOnFailureWithVersion(ctx, Team{
		ID:            teamID,
		LeadSessionID: "lead-session",
	}, *failedTask, failedTask.Version)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "prerequisite is not in the generated plan")
	planTask, err := store.GetTask(ctx, "planned-task")
	require.NoError(t, err)
	require.Nil(t, planTask)
	unchanged, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, unchanged)
	require.Equal(t, failedTask.Version, unchanged.Version)
}

func TestLeadPlannerReplanRejectsForeignFailedTaskBeforeCallingLead(t *testing.T) {
	client := &capturingSessionClient{
		result: &SessionResult{Success: true, Output: `{"tasks":[{"id":"task-followup","title":"follow up"}]}`},
	}
	planner := &LeadPlanner{Sessions: client}

	result, err := planner.ReplanOnFailure(context.Background(), Team{
		ID:            "team-a",
		LeadSessionID: "lead-session",
	}, Task{ID: "foreign-task", TeamID: "team-b", Title: "foreign"})
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "does not belong to team")
	assert.Empty(t, client.prompt, "the lead must not be prompted for a task from another Team")
}

func TestLeadPlannerReplanAutoPersistDuplicateTaskIDDoesNotOverwriteExistingTask(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{LeadSessionID: "lead-session"})
	require.NoError(t, err)
	failedTaskID, err := store.CreateTask(ctx, Task{
		ID:     "duplicate-replan-source",
		TeamID: teamID,
		Title:  "failed source",
		Status: TaskStatusFailed,
	})
	require.NoError(t, err)
	failedTask, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, failedTask)
	_, err = store.CreateTask(ctx, Task{
		ID:     "existing-followup",
		TeamID: teamID,
		Title:  "original task",
		Status: TaskStatusPending,
	})
	require.NoError(t, err)

	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  `{"tasks":[{"id":"existing-followup","title":"replacement task"}]}`,
		},
	}
	planner := &LeadPlanner{Sessions: client, Store: store, AutoPersist: true}

	result, err := planner.ReplanOnFailureWithVersion(ctx, Team{
		ID:            teamID,
		LeadSessionID: "lead-session",
	}, *failedTask, failedTask.Version)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "UNIQUE constraint failed")
	require.Contains(t, err.Error(), "persist replan task")

	existing, err := store.GetTask(ctx, "existing-followup")
	require.NoError(t, err)
	require.NotNil(t, existing)
	require.Equal(t, "original task", existing.Title)
	require.Equal(t, TaskStatusPending, existing.Status)
}

func TestLeadPlannerReplanPlanIDMappingIsDeterministic(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	teamID, err := store.CreateTeam(ctx, Team{LeadSessionID: "lead-session"})
	require.NoError(t, err)
	failedTaskID, err := store.CreateTask(ctx, Task{
		ID:     "stable-mapping-source",
		TeamID: teamID,
		Title:  "failed source",
		Status: TaskStatusFailed,
	})
	require.NoError(t, err)
	failedTask, err := store.GetTask(ctx, failedTaskID)
	require.NoError(t, err)
	require.NotNil(t, failedTask)

	client := &capturingSessionClient{
		result: &SessionResult{
			Success: true,
			Output: `{"tasks":[
				{"id":"z-task","title":"later"},
				{"id":"a-task","title":"earlier"},
				{"title":"generated id"}
			]}`,
		},
	}
	planner := &LeadPlanner{Sessions: client, Store: store, AutoPersist: true}

	result, err := planner.ReplanOnFailureWithVersion(ctx, Team{
		ID:            teamID,
		LeadSessionID: "lead-session",
	}, *failedTask, failedTask.Version)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Tasks, 3)
	for _, task := range result.Tasks {
		require.EqualValues(t, 1, task.Version)
		require.False(t, task.CreatedAt.IsZero())
	}

	var persistedPlanIDs []string
	for _, task := range result.Tasks {
		persistedPlanIDs = append(persistedPlanIDs, task.ID)
	}
	assert.Equal(t, []string{"z-task", "a-task", "task_3"}, persistedPlanIDs)

	for index, planID := range persistedPlanIDs {
		task, err := store.GetTask(ctx, planID)
		require.NoError(t, err)
		require.NotNil(t, task, fmt.Sprintf("expected task %q at index %d", planID, index))
		require.Equal(t, planID, task.ID)
	}
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

func TestLeadPlannerFinalSummaryDetailedCapturesPromptPreflightFallbackMetadata(t *testing.T) {
	store, err := NewSQLiteStore(&StoreConfig{
		DSN: "file:lead-summary-preflight-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, Team{
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		TeamID:  teamID,
		Title:   "Completed task",
		Goal:    "ship the feature",
		Status:  TaskStatusDone,
		Summary: "feature shipped",
	})
	require.NoError(t, err)

	client := &capturingSessionClient{
		result: &SessionResult{
			Success:   false,
			Error:     "prompt preflight budget exceeded",
			TraceID:   "trace-summary-preflight",
			ErrorType: "prompt_preflight",
			ErrorMetadata: map[string]interface{}{
				"failure_reason_code":         "prompt_still_exceeds_budget_after_compaction",
				"replacement_history_applied": true,
			},
		},
		err: assert.AnError,
	}
	planner := &LeadPlanner{
		Sessions: client,
		Store:    store,
	}

	result, err := planner.FinalSummaryDetailed(ctx, teamID)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, FinalSummarySourceFallback, result.SummarySource)
	assert.True(t, result.UsedFallback)
	assert.Equal(t, FinalSummaryFallbackLeadSessionError, result.FallbackReason)
	assert.Equal(t, "trace-summary-preflight", result.TraceID)
	assert.Equal(t, "prompt_preflight", result.ErrorType)
	assert.Equal(t, "prompt_still_exceeds_budget_after_compaction", result.CloneErrorMetadata()["failure_reason_code"])
	assert.Equal(t, true, result.CloneErrorMetadata()["replacement_history_applied"])
	require.Error(t, result.SessionError)
	assert.Contains(t, result.Summary, "Team "+teamID+" summary:")
	assert.Contains(t, result.Summary, "feature shipped")

	summary, err := planner.FinalSummary(ctx, teamID)
	require.NoError(t, err)
	assert.Equal(t, result.Summary, summary)
}
