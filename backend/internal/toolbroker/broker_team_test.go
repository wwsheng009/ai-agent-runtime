package toolbroker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/types"
	"github.com/ai-gateway/ai-agent-runtime/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type plannerSessionClient struct {
	response string
}

func (c *plannerSessionClient) SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *team.RunMeta) (*team.SessionResult, error) {
	return &team.SessionResult{
		Success: true,
		Output:  c.response,
	}, nil
}

func newTeamStore(t *testing.T) *team.SQLiteStore {
	t.Helper()
	return teamTestStore(t)
}

func teamTestStore(t *testing.T) *team.SQLiteStore {
	t.Helper()
	store, err := team.NewSQLiteStore(&team.StoreConfig{
		DSN: "file:toolbroker-team-test-" + time.Now().UTC().Format("150405.000000000") + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestBrokerExecuteSendTeamMessageUsesRunMetaDefaults(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "task-1",
		Status: team.TaskStatusRunning,
	})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        teamID,
			AgentID:       "mate-1",
			CurrentTaskID: taskID,
		},
	})

	raw, _, err := broker.Execute(runCtx, "session-1", ToolSendTeamMessage, map[string]interface{}{
		"to_agent": "lead",
		"kind":     "question",
		"body":     "blocked on tests",
	})
	require.NoError(t, err)

	result, ok := raw.(SendTeamMessageResult)
	require.True(t, ok)
	assert.Equal(t, teamID, result.TeamID)
	assert.Equal(t, "mate-1", result.FromAgent)
	assert.Equal(t, "lead", result.ToAgent)
	assert.Equal(t, taskID, result.TaskID)

	messages, err := store.ListMail(ctx, team.MailFilter{
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "mate-1", messages[0].FromAgent)
	assert.Equal(t, "lead", messages[0].ToAgent)
	require.NotNil(t, messages[0].TaskID)
	assert.Equal(t, taskID, *messages[0].TaskID)
}

func TestBrokerDefinitionsMarkCanonicalAndCompatibilityOutcomeTools(t *testing.T) {
	store := newTeamStore(t)
	broker := &Broker{TeamStore: store}

	defs := broker.Definitions()
	var reportDef, blockDef *types.ToolDefinition
	for i := range defs {
		switch defs[i].Name {
		case ToolReportTaskOutcome:
			reportDef = &defs[i]
		case ToolBlockCurrentTask:
			blockDef = &defs[i]
		}
	}

	require.NotNil(t, reportDef)
	require.NotNil(t, blockDef)
	require.NotNil(t, reportDef.Metadata)
	require.NotNil(t, blockDef.Metadata)
	assert.Equal(t, true, reportDef.Metadata["canonical"])
	assert.Contains(t, reportDef.Description, "structured done, failed, blocked, or handoff")
	assert.Equal(t, true, blockDef.Metadata["compatibility_alias"])
	assert.Equal(t, ToolReportTaskOutcome, blockDef.Metadata["canonical_tool"])
	assert.Contains(t, blockDef.Description, "Compatibility alias")

	reportParams, ok := reportDef.Parameters["properties"].(map[string]interface{})
	require.True(t, ok)
	reportTaskStatus, ok := reportParams["task_status"].(map[string]interface{})
	require.True(t, ok)
	reportEnum, ok := reportTaskStatus["enum"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"done", "failed", "blocked", "handoff"}, reportEnum)

	blockParams, ok := blockDef.Parameters["properties"].(map[string]interface{})
	require.True(t, ok)
	blockTaskStatus, ok := blockParams["task_status"].(map[string]interface{})
	require.True(t, ok)
	blockEnum, ok := blockTaskStatus["enum"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"blocked", "handoff"}, blockEnum)
}

func TestBrokerExecuteTeamToolRequiresRunMeta(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	_, _, err = broker.Execute(ctx, "session-1", ToolSendTeamMessage, map[string]interface{}{
		"team_id": teamID,
		"body":    "should fail without run meta",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "team tools require an active team run")
}

func TestBrokerExecuteReadMailboxDigestUsesUnreadMessagesForCurrentAgent(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	_, err = store.InsertMail(ctx, team.MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "*",
		Kind:      "info",
		Body:      "broadcast",
	})
	require.NoError(t, err)
	directID, err := store.InsertMail(ctx, team.MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-1",
		Kind:      "question",
		Body:      "direct ask",
	})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:  teamID,
			AgentID: "mate-1",
		},
	})

	raw, _, err := broker.Execute(runCtx, "session-1", ToolReadMailboxDigest, map[string]interface{}{"limit": 5})
	require.NoError(t, err)

	result, ok := raw.(ReadMailboxDigestResult)
	require.True(t, ok)
	assert.Equal(t, teamID, result.TeamID)
	assert.Equal(t, "mate-1", result.AgentID)
	assert.Equal(t, 2, result.MessageCount)
	assert.True(t, result.MarkedRead)
	assert.Contains(t, result.Digest, "broadcast")
	assert.Contains(t, result.Digest, "direct ask")
	assert.Contains(t, result.MessageIDs, directID)

	remaining, err := store.ListMail(ctx, team.MailFilter{
		TeamID:           teamID,
		ToAgent:          "mate-1",
		IncludeBroadcast: true,
		UnreadOnly:       true,
	})
	require.NoError(t, err)
	require.Len(t, remaining, 0)
}

func TestBrokerExecuteReadTaskSpecUsesCurrentTaskRunMeta(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID:       teamID,
		Title:        "fix-team-gap",
		Goal:         "wire toolbroker",
		Inputs:       []string{"analysis.md"},
		ReadPaths:    []string{"internal/runtime/toolbroker"},
		WritePaths:   []string{"internal/team"},
		Deliverables: []string{"tests"},
		Status:       team.TaskStatusRunning,
		Priority:     3,
		Summary:      "in progress",
	})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        teamID,
			AgentID:       "mate-1",
			CurrentTaskID: taskID,
		},
	})

	raw, _, err := broker.Execute(runCtx, "session-1", ToolReadTaskSpec, map[string]interface{}{})
	require.NoError(t, err)

	result, ok := raw.(ReadTaskSpecResult)
	require.True(t, ok)
	assert.Equal(t, taskID, result.TaskID)
	assert.Equal(t, teamID, result.TeamID)
	assert.Equal(t, "fix-team-gap", result.Title)
	assert.Equal(t, "wire toolbroker", result.Goal)
	assert.Equal(t, []string{"analysis.md"}, result.Inputs)
	assert.Equal(t, []string{"internal/runtime/toolbroker"}, result.ReadPaths)
	assert.Equal(t, []string{"internal/team"}, result.WritePaths)
	assert.Equal(t, []string{"tests"}, result.Deliverables)
	assert.Equal(t, string(team.TaskStatusRunning), result.Status)
}

func TestBrokerExecuteReadTaskSpecFallsBackToCurrentRunTeamForUnknownExplicitTeamID(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "fix-team-gap",
		Status: team.TaskStatusPending,
	})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        teamID,
			AgentID:       "mate-1",
			CurrentTaskID: taskID,
		},
	})

	raw, _, err := broker.Execute(runCtx, "session-1", ToolReadTaskSpec, map[string]interface{}{
		"team_id": "team-hallucinated",
		"task_id": taskID,
	})
	require.NoError(t, err)

	result, ok := raw.(ReadTaskSpecResult)
	require.True(t, ok)
	assert.Equal(t, taskID, result.TaskID)
	assert.Equal(t, teamID, result.TeamID)
}

func TestBrokerExecuteSendTeamMessageFallsBackToCurrentRunTeamForUnknownExplicitTeamID(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:  teamID,
			AgentID: "lead",
		},
	})

	raw, _, err := broker.Execute(runCtx, "session-1", ToolSendTeamMessage, map[string]interface{}{
		"team_id":  "team-hallucinated",
		"to_agent": "planner",
		"kind":     "info",
		"body":     "status?",
	})
	require.NoError(t, err)

	result, ok := raw.(SendTeamMessageResult)
	require.True(t, ok)
	assert.Equal(t, teamID, result.TeamID)
	assert.Equal(t, "lead", result.FromAgent)
	assert.Equal(t, "planner", result.ToAgent)
}

func TestBrokerExecuteReadTaskContextCombinesSpecContextAndMailbox(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	dependencyID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "prep",
		Status: team.TaskStatusDone,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID:       teamID,
		Title:        "implement",
		Goal:         "wire richer task context",
		Inputs:       []string{"design.md"},
		ReadPaths:    []string{"internal/runtime/toolbroker"},
		WritePaths:   []string{"internal/team"},
		Deliverables: []string{"tests"},
		Status:       team.TaskStatusRunning,
	})
	require.NoError(t, err)
	followupID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "review",
		Status: team.TaskStatusPending,
	})
	require.NoError(t, err)
	require.NoError(t, store.AddTaskDependency(ctx, taskID, dependencyID))
	require.NoError(t, store.AddTaskDependency(ctx, followupID, taskID))
	_, err = store.InsertMail(ctx, team.MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-1",
		Kind:      "info",
		Body:      "confirm the task boundary before editing",
	})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        teamID,
			AgentID:       "mate-1",
			CurrentTaskID: taskID,
		},
	})

	raw, _, err := broker.Execute(runCtx, "session-1", ToolReadTaskContext, map[string]interface{}{
		"mailbox_limit": 5,
	})
	require.NoError(t, err)

	result, ok := raw.(ReadTaskContextResult)
	require.True(t, ok)
	assert.Equal(t, taskID, result.Spec.TaskID)
	assert.Equal(t, "implement", result.Spec.Title)
	assert.Contains(t, result.TeamContext, "Team context:")
	assert.Contains(t, result.TeamContext, "current[running]: implement")
	assert.Contains(t, result.MailboxDigest, "confirm the task boundary")
	assert.Equal(t, 1, result.MessageCount)
	assert.True(t, result.MarkedRead)
	assert.Equal(t, []string{dependencyID}, result.Dependencies)
	assert.Equal(t, []string{followupID}, result.Dependents)

	remaining, err := store.ListMail(ctx, team.MailFilter{
		TeamID:           teamID,
		ToAgent:          "mate-1",
		IncludeBroadcast: true,
		UnreadOnly:       true,
	})
	require.NoError(t, err)
	require.Len(t, remaining, 0)
}

func TestBrokerExecuteBlockCurrentTaskBlocksAndReplans(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     team.TeammateStateBusy,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "blocked task",
		Status: team.TaskStatusRunning,
	})
	require.NoError(t, err)

	planPayload := map[string]interface{}{
		"tasks": []map[string]interface{}{
			{
				"id":    "followup-1",
				"title": "investigate blocker",
				"goal":  "collect missing info",
			},
		},
		"dependencies": []map[string]interface{}{},
	}
	rawPayload, err := json.Marshal(planPayload)
	require.NoError(t, err)

	broker := &Broker{
		TeamStore: store,
		TeamPlanner: &team.LeadPlanner{
			Sessions:    &plannerSessionClient{response: string(rawPayload)},
			Store:       store,
			AutoPersist: true,
		},
	}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        teamID,
			AgentID:       "mate-1",
			CurrentTaskID: taskID,
		},
	})

	raw, _, err := broker.Execute(runCtx, "session-1", ToolBlockCurrentTask, map[string]interface{}{
		"task_status": "blocked",
		"summary":     "waiting on architecture decision",
		"blocker":     "waiting on architecture decision",
	})
	require.NoError(t, err)

	result, ok := raw.(BlockCurrentTaskResult)
	require.True(t, ok)
	assert.Equal(t, taskID, result.TaskID)
	assert.Equal(t, string(team.TaskStatusBlocked), result.Status)
	assert.Equal(t, string(team.TaskOutcomeBlocked), result.Outcome)
	assert.Equal(t, "mate-1", result.BlockedBy)
	assert.True(t, result.Replanned)
	assert.Len(t, result.PlannedTaskIDs, 1)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, team.TaskStatusBlocked, task.Status)

	teammate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, teammate)
	assert.Equal(t, team.TeammateStateBusy, teammate.State)

	messages, err := store.ListMail(ctx, team.MailFilter{
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "lead", messages[0].ToAgent)
}

func TestBrokerExecuteBlockCurrentTaskAcceptsStructuredHandoffOutcome(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session-1",
		State:     team.TeammateStateBusy,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-2",
		TeamID:    teamID,
		SessionID: "mate-session-2",
		State:     team.TeammateStateIdle,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "handoff task",
		Status: team.TaskStatusRunning,
	})
	require.NoError(t, err)

	broker := &Broker{
		TeamStore: store,
		TeamPlanner: &team.LeadPlanner{
			Sessions:    &plannerSessionClient{response: `{"tasks":[{"id":"should-not-run"}]}`},
			Store:       store,
			AutoPersist: true,
		},
	}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        teamID,
			AgentID:       "mate-1",
			CurrentTaskID: taskID,
		},
	})

	raw, _, err := broker.Execute(runCtx, "session-1", ToolBlockCurrentTask, map[string]interface{}{
		"task_status": "handoff",
		"summary":     "pass to reviewer",
		"blocker":     "need review",
		"handoff_to":  "mate-2",
	})
	require.NoError(t, err)

	result, ok := raw.(BlockCurrentTaskResult)
	require.True(t, ok)
	assert.Equal(t, string(team.TaskStatusBlocked), result.Status)
	assert.Equal(t, string(team.TaskOutcomeHandoff), result.Outcome)
	assert.Equal(t, "mate-2", result.HandoffTo)
	assert.False(t, result.Replanned)
	assert.Empty(t, result.PlannedTaskIDs)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, team.TaskStatusBlocked, task.Status)
	assert.Equal(t, "pass to reviewer", task.Summary)

	messages, err := store.ListMail(ctx, team.MailFilter{
		TeamID: teamID,
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "mate-2", messages[0].ToAgent)
	assert.Equal(t, "handoff", messages[0].Kind)
}

func TestBrokerExecuteBlockCurrentTaskRejectsInvalidStructuredOutcome(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "invalid handoff",
		Status: team.TaskStatusRunning,
	})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        teamID,
			AgentID:       "mate-1",
			CurrentTaskID: taskID,
		},
	})

	_, _, err = broker.Execute(runCtx, "session-1", ToolBlockCurrentTask, map[string]interface{}{
		"task_status": "handoff",
		"summary":     "pass to reviewer",
		"blocker":     "need review",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handoff_to is required")
}

func TestBrokerExecuteReportTaskOutcomeMarksTaskDone(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     team.TeammateStateBusy,
	})
	require.NoError(t, err)
	assignee := "mate-1"
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID:   teamID,
		Title:    "done task",
		Status:   team.TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        teamID,
			AgentID:       "mate-1",
			CurrentTaskID: taskID,
		},
	})

	raw, _, err := broker.Execute(runCtx, "session-1", ToolReportTaskOutcome, map[string]interface{}{
		"task_status": "done",
		"summary":     "task finished",
		"result_ref":  "artifact://done-task",
	})
	require.NoError(t, err)

	result, ok := raw.(ReportTaskOutcomeResult)
	require.True(t, ok)
	assert.Equal(t, string(team.TaskStatusDone), result.Status)
	assert.Equal(t, string(team.TaskOutcomeDone), result.Outcome)
	assert.Equal(t, "artifact://done-task", result.ResultRef)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, team.TaskStatusDone, task.Status)
	require.NotNil(t, task.ResultRef)
	assert.Equal(t, "artifact://done-task", *task.ResultRef)

	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	assert.Equal(t, team.TeammateStateBusy, mate.State)
}

func TestBrokerExecuteReportTaskOutcomeMarksTeamDoneWhenLastTaskCompletes(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{
		Status: team.TeamStatusActive,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session",
		State:     team.TeammateStateBusy,
	})
	require.NoError(t, err)
	assignee := "mate-1"
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID:   teamID,
		Title:    "final task",
		Status:   team.TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        teamID,
			AgentID:       "mate-1",
			CurrentTaskID: taskID,
		},
	})

	_, _, err = broker.Execute(runCtx, "session-1", ToolReportTaskOutcome, map[string]interface{}{
		"task_status": "done",
		"summary":     "final task finished",
	})
	require.NoError(t, err)

	teamRecord, err := store.GetTeam(ctx, teamID)
	require.NoError(t, err)
	require.NotNil(t, teamRecord)
	assert.Equal(t, team.TeamStatusDone, teamRecord.Status)
}

func TestBrokerExecuteReportTaskOutcomeHandlesHandoff(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{LeadSessionID: "lead-session"})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "mate-session-1",
		State:     team.TeammateStateBusy,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-2",
		TeamID:    teamID,
		SessionID: "mate-session-2",
		State:     team.TeammateStateIdle,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "handoff task",
		Status: team.TaskStatusRunning,
	})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:        teamID,
			AgentID:       "mate-1",
			CurrentTaskID: taskID,
		},
	})

	raw, _, err := broker.Execute(runCtx, "session-1", ToolReportTaskOutcome, map[string]interface{}{
		"task_status": "handoff",
		"summary":     "pass to reviewer",
		"blocker":     "need review",
		"handoff_to":  "mate-2",
	})
	require.NoError(t, err)

	result, ok := raw.(ReportTaskOutcomeResult)
	require.True(t, ok)
	assert.Equal(t, string(team.TaskStatusBlocked), result.Status)
	assert.Equal(t, string(team.TaskOutcomeHandoff), result.Outcome)
	assert.Equal(t, "need review", result.Blocker)
	assert.Equal(t, "mate-2", result.HandoffTo)
	assert.False(t, result.Replanned)

	messages, err := store.ListMail(ctx, team.MailFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "mate-2", messages[0].ToAgent)
	assert.Equal(t, "handoff", messages[0].Kind)
}

func TestBrokerExecuteSpawnTeamCreatesTeamTeammatesAndTasks(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	broker := &Broker{TeamStore: store}
	loopCalls := 0
	broker.TeamLifecycleChanged = func() { loopCalls++ }

	raw, _, err := broker.Execute(ctx, "session-1", ToolSpawnTeam, map[string]interface{}{
		"team_id":       "team-auto",
		"workspace_id":  "ws-1",
		"max_teammates": 2,
		"max_writers":   1,
		"teammates": []interface{}{
			map[string]interface{}{
				"id":   "mate-a",
				"name": "planner",
			},
			map[string]interface{}{
				"id":    "mate-b",
				"name":  "executor",
				"state": "idle",
			},
		},
		"tasks": []interface{}{
			map[string]interface{}{
				"id":    "task-1",
				"title": "draft plan",
				"goal":  "create task plan",
			},
			map[string]interface{}{
				"title": "execute steps",
				"goal":  "carry out the plan",
			},
		},
	})
	require.NoError(t, err)

	result, ok := raw.(SpawnTeamResult)
	require.True(t, ok)
	assert.Equal(t, "team-auto", result.TeamID)
	assert.True(t, result.CreatedTeam)
	assert.Equal(t, 2, result.TeammateCount)
	assert.Equal(t, 2, result.TaskCount)
	assert.Equal(t, 1, loopCalls)

	teamRecord, err := store.GetTeam(ctx, "team-auto")
	require.NoError(t, err)
	require.NotNil(t, teamRecord)
	assert.Equal(t, team.TeamStatusActive, teamRecord.Status)

	teammates, err := store.ListTeammates(ctx, "team-auto")
	require.NoError(t, err)
	assert.Len(t, teammates, 2)

	tasks, err := store.ListTasks(ctx, team.TaskFilter{
		TeamID: "team-auto",
	})
	require.NoError(t, err)
	assert.Len(t, tasks, 2)
}

func TestBrokerExecuteSpawnTeamRehomesTerminalTeamAndTaskIDConflicts(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	broker := &Broker{TeamStore: store}

	_, err := store.CreateTeam(ctx, team.Team{
		ID:     "team_docs_explore",
		Status: team.TeamStatusDone,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{
		ID:     "task_docs_agents",
		TeamID: "team_docs_explore",
		Title:  "old task",
		Status: team.TaskStatusDone,
	})
	require.NoError(t, err)

	raw, _, err := broker.Execute(ctx, "session-1", ToolSpawnTeam, map[string]interface{}{
		"team_id":        "team_docs_explore",
		"allow_existing": true,
		"auto_start":     false,
		"teammates": []interface{}{
			map[string]interface{}{
				"id":   "agent_docs_agents",
				"name": "Agents Explorer",
			},
		},
		"tasks": []interface{}{
			map[string]interface{}{
				"id":         "task_docs_agents",
				"title":      "explore docs/agents",
				"goal":       "summarize docs/agents",
				"assignee":   "agent_docs_agents",
				"read_paths": []interface{}{"docs/agents"},
			},
		},
	})
	require.NoError(t, err)

	result, ok := raw.(SpawnTeamResult)
	require.True(t, ok)
	assert.Equal(t, "team_docs_explore_v2", result.TeamID)
	assert.True(t, result.CreatedTeam)
	require.Len(t, result.TaskIDs, 1)
	assert.Equal(t, "task_docs_agents_v2", result.TaskIDs[0])

	teamRecord, err := store.GetTeam(ctx, "team_docs_explore_v2")
	require.NoError(t, err)
	require.NotNil(t, teamRecord)
	assert.Equal(t, team.TeamStatusActive, teamRecord.Status)

	task, err := store.GetTask(ctx, "task_docs_agents_v2")
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, "team_docs_explore_v2", task.TeamID)
	assert.Equal(t, "explore docs/agents", task.Title)
}

func TestBrokerExecuteSpawnTeamRehomesActiveTeamWhenLeadSessionDiffers(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	broker := &Broker{TeamStore: store}

	_, err := store.CreateTeam(ctx, team.Team{
		ID:            "team_docs_explore",
		LeadSessionID: "old-lead-session",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)

	raw, _, err := broker.Execute(ctx, "session-1", ToolSpawnTeam, map[string]interface{}{
		"team_id":         "team_docs_explore",
		"lead_session_id": "session-1",
		"allow_existing":  true,
		"auto_start":      false,
		"tasks": []interface{}{
			map[string]interface{}{
				"id":    "task-1",
				"title": "explore docs",
				"goal":  "summarize docs",
			},
		},
	})
	require.NoError(t, err)

	result, ok := raw.(SpawnTeamResult)
	require.True(t, ok)
	assert.Equal(t, "team_docs_explore_v2", result.TeamID)
	assert.True(t, result.CreatedTeam)

	teamRecord, err := store.GetTeam(ctx, "team_docs_explore_v2")
	require.NoError(t, err)
	require.NotNil(t, teamRecord)
	assert.Equal(t, "session-1", teamRecord.LeadSessionID)
	assert.Equal(t, team.TeamStatusActive, teamRecord.Status)

	original, err := store.GetTeam(ctx, "team_docs_explore")
	require.NoError(t, err)
	require.NotNil(t, original)
	assert.Equal(t, "old-lead-session", original.LeadSessionID)
}

func TestBrokerExecuteSpawnTeamResolvesCurrentLeadAndTeammateSessionPlaceholders(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	broker := &Broker{TeamStore: store}
	broker.TeamDispatcher = teammateSessionAllocatorStub{}

	raw, _, err := broker.Execute(ctx, "session-1", ToolSpawnTeam, map[string]interface{}{
		"team_id":         "team-current",
		"lead_session_id": "current",
		"workspace_id":    "current",
		"auto_start":      false,
		"teammates": []interface{}{
			map[string]interface{}{
				"id":         "mate-a",
				"name":       "planner",
				"session_id": "current",
			},
		},
		"tasks": []interface{}{
			map[string]interface{}{
				"id":       "task-1",
				"title":    "draft plan",
				"goal":     "create task plan",
				"assignee": "mate-a",
			},
		},
	})
	require.NoError(t, err)

	result, ok := raw.(SpawnTeamResult)
	require.True(t, ok)
	assert.Equal(t, "team-current", result.TeamID)

	teamRecord, err := store.GetTeam(ctx, "team-current")
	require.NoError(t, err)
	require.NotNil(t, teamRecord)
	assert.Equal(t, "session-1", teamRecord.LeadSessionID)
	assert.Empty(t, teamRecord.WorkspaceID)

	teammates, err := store.ListTeammates(ctx, "team-current")
	require.NoError(t, err)
	require.Len(t, teammates, 1)
	assert.Equal(t, "team-current__mate_a", teammates[0].SessionID)
}

func TestBrokerExecuteSpawnTeamForcesCurrentLeadSession(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	broker := &Broker{TeamStore: store}

	raw, _, err := broker.Execute(ctx, "session-1", ToolSpawnTeam, map[string]interface{}{
		"team_id":         "team-foreign-lead",
		"lead_session_id": "lead-docs-explore",
		"auto_start":      false,
		"tasks": []interface{}{
			map[string]interface{}{
				"id":    "task-1",
				"title": "draft plan",
				"goal":  "create task plan",
			},
		},
	})
	require.NoError(t, err)

	result, ok := raw.(SpawnTeamResult)
	require.True(t, ok)
	assert.Equal(t, "team-foreign-lead", result.TeamID)

	teamRecord, err := store.GetTeam(ctx, "team-foreign-lead")
	require.NoError(t, err)
	require.NotNil(t, teamRecord)
	assert.Equal(t, "session-1", teamRecord.LeadSessionID)
}

func TestBrokerExecuteSpawnTeamRejectsMissingReadPathsWhenWorkspaceRootKnown(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	root := t.TempDir()
	broker := &Broker{
		TeamStore:  store,
		TeamClaims: team.NewPathClaimManager(store, root),
	}

	_, _, err := broker.Execute(ctx, "session-1", ToolSpawnTeam, map[string]interface{}{
		"team_id":    "team-missing-read-path",
		"auto_start": false,
		"tasks": []interface{}{
			map[string]interface{}{
				"id":         "task-1",
				"title":      "inspect missing docs",
				"goal":       "inspect missing docs",
				"read_paths": []interface{}{"docs/agents"},
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `read_path "docs/agents" not found`)

	teamRecord, loadErr := store.GetTeam(ctx, "team-missing-read-path")
	require.NoError(t, loadErr)
	assert.Nil(t, teamRecord)
}

func TestBrokerExecuteSpawnTeamNormalizesExistingReadPathsAgainstWorkspaceRoot(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs", "aicli"), 0o755))
	broker := &Broker{
		TeamStore:  store,
		TeamClaims: team.NewPathClaimManager(store, root),
	}

	raw, _, err := broker.Execute(ctx, "session-1", ToolSpawnTeam, map[string]interface{}{
		"team_id":    "team-existing-read-path",
		"auto_start": false,
		"tasks": []interface{}{
			map[string]interface{}{
				"id":         "task-1",
				"title":      "inspect docs/aicli",
				"goal":       "inspect docs/aicli",
				"read_paths": []interface{}{".\\docs\\aicli"},
			},
		},
	})
	require.NoError(t, err)

	result, ok := raw.(SpawnTeamResult)
	require.True(t, ok)
	assert.Equal(t, "team-existing-read-path", result.TeamID)

	taskRecord, loadErr := store.GetTask(ctx, "task-1")
	require.NoError(t, loadErr)
	require.NotNil(t, taskRecord)
	assert.Equal(t, []string{"docs/aicli"}, taskRecord.ReadPaths)
}

type teammateSessionAllocatorStub struct{}

func (teammateSessionAllocatorStub) DispatchTeamMailboxMessage(ctx context.Context, message team.MailMessage) error {
	return nil
}

func (teammateSessionAllocatorStub) EnsureTeammateSessionIDs(teamID string, specs []SpawnTeammateSpec) []SpawnTeammateSpec {
	if len(specs) == 0 {
		return nil
	}
	resolved := make([]SpawnTeammateSpec, len(specs))
	copy(resolved, specs)
	for i := range resolved {
		if strings.TrimSpace(resolved[i].SessionID) == "" {
			resolved[i].SessionID = teamID + "__" + strings.ReplaceAll(strings.ToLower(strings.TrimSpace(firstNonEmptyString(resolved[i].ID, resolved[i].Name))), "-", "_")
		}
	}
	return resolved
}
