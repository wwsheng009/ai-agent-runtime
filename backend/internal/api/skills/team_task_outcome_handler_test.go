package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestCompleteTaskHandlerAcceptsStructuredOutcomeContract(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-outcome-complete-structured?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		State:     team.TeammateStateBusy,
		SessionID: "mate-session-1",
	})
	require.NoError(t, err)
	assignee := "mate-1"
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID:   teamID,
		Title:    "publish artifact",
		Status:   team.TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	body := `{"task_status":"done","summary":"artifact published","result_ref":"artifact://build-1","teammate_id":"mate-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/outcome", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Task team.Task `json:"task"`
	}
	require.NoError(t, decodeJSONResponse(rec, &resp))
	require.Equal(t, team.TaskStatusDone, resp.Task.Status)
	require.Equal(t, "artifact published", resp.Task.Summary)
	require.NotNil(t, resp.Task.ResultRef)
	require.Equal(t, "artifact://build-1", *resp.Task.ResultRef)

	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	require.Equal(t, team.TeammateStateIdle, mate.State)

	teamRecord, err := store.GetTeam(ctx, teamID)
	require.NoError(t, err)
	require.NotNil(t, teamRecord)
	require.Equal(t, team.TeamStatusDone, teamRecord.Status)
}

func TestReportTaskOutcomeHandlerAcceptsMinimalDonePayload(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-outcome-complete-legacy?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "legacy complete",
		Status: team.TaskStatusRunning,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/outcome", strings.NewReader(`{"task_status":"done","summary":"legacy complete"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, team.TaskStatusDone, task.Status)
	require.Equal(t, "legacy complete", task.Summary)
}

func TestReportTaskOutcomeHandlerUsesCanonicalHeaders(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-outcome-complete-warning?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "legacy complete warning",
		Status: team.TaskStatusRunning,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/outcome", strings.NewReader(`{"task_status":"done","summary":"legacy complete"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "canonical", rec.Header().Get("X-AI-Gateway-Entrypoint-Mode"))
	require.Equal(t, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/outcome", rec.Header().Get("X-AI-Gateway-Canonical-Entrypoint"))
	require.Empty(t, rec.Header().Get("Warning"))
}

func TestBlockTaskHandlerAcceptsStructuredHandoffOutcome(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-outcome-block-handoff?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{LeadSessionID: "lead-session"})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		State:     team.TeammateStateBusy,
		SessionID: "mate-session-1",
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-2",
		TeamID:    teamID,
		State:     team.TeammateStateIdle,
		SessionID: "mate-session-2",
	})
	require.NoError(t, err)
	assignee := "mate-1"
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID:   teamID,
		Title:    "handoff review",
		Status:   team.TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	body := `{"task_status":"handoff","summary":"pass to reviewer","blocker":"need review","handoff_to":"mate-2","teammate_id":"mate-1","auto_replan":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/outcome", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Task      team.Task `json:"task"`
		HandoffTo string    `json:"handoff_to"`
	}
	require.NoError(t, decodeJSONResponse(rec, &resp))
	require.Equal(t, team.TaskStatusBlocked, resp.Task.Status)
	require.Equal(t, "pass to reviewer", resp.Task.Summary)
	require.Equal(t, "mate-2", resp.HandoffTo)

	messages, err := store.ListMail(ctx, team.MailFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "mate-2", messages[0].ToAgent)
	require.Equal(t, "handoff", messages[0].Kind)
	require.Equal(t, "pass to reviewer", messages[0].Body)

	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	require.Equal(t, team.TeammateStateBlocked, mate.State)
}

func TestReportTaskOutcomeRouteSupportsCanonicalStatuses(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-outcome-legacy-warning-routes?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{LeadSessionID: "lead-session"})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		State:     team.TeammateStateBusy,
		SessionID: "mate-session-1",
	})
	require.NoError(t, err)
	assignee := "mate-1"

	cases := []struct {
		name string
		body string
	}{
		{name: "complete", body: `{"task_status":"done","summary":"done summary"}`},
		{name: "fail", body: `{"task_status":"failed","summary":"failed summary","blocker":"execution failed"}`},
		{name: "block", body: `{"task_status":"blocked","summary":"blocked summary","blocker":"waiting on dependency","auto_replan":false}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			taskID, err := store.CreateTask(ctx, team.Task{
				TeamID:   teamID,
				Title:    tc.name + " task",
				Status:   team.TaskStatusRunning,
				Assignee: &assignee,
			})
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/outcome", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			canonical := "/api/runtime/teams/" + teamID + "/tasks/" + taskID + "/outcome"
			require.Equal(t, "canonical", rec.Header().Get("X-AI-Gateway-Entrypoint-Mode"))
			require.Equal(t, canonical, rec.Header().Get("X-AI-Gateway-Canonical-Entrypoint"))
			require.Empty(t, rec.Header().Get("Warning"))
		})
	}
}

func TestBlockTaskHandlerRejectsInvalidStructuredOutcome(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-outcome-block-invalid?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "invalid handoff",
		Status: team.TaskStatusRunning,
	})
	require.NoError(t, err)

	body := `{"task_status":"handoff","summary":"pass to reviewer","blocker":"need review","auto_replan":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/outcome", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "handoff_to is required")
}

func TestReportTaskOutcomeHandlerSupportsStructuredFailedOutcome(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-outcome-report-failed?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		State:     team.TeammateStateBusy,
		SessionID: "mate-session-1",
	})
	require.NoError(t, err)
	assignee := "mate-1"
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID:   teamID,
		Title:    "report failure",
		Status:   team.TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	body := `{"task_status":"failed","summary":"tests failed","blocker":"nil token case","result_ref":"log://task-1","teammate_id":"mate-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/outcome", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Task team.Task `json:"task"`
	}
	require.NoError(t, decodeJSONResponse(rec, &resp))
	require.Equal(t, team.TaskStatusFailed, resp.Task.Status)
	require.Equal(t, "tests failed", resp.Task.Summary)
	require.NotNil(t, resp.Task.ResultRef)
	require.Equal(t, "log://task-1", *resp.Task.ResultRef)
}

func TestReportTaskOutcomeHandlerAddsCanonicalRouteHeaders(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-outcome-report-canonical-header?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "canonical outcome",
		Status: team.TaskStatusRunning,
	})
	require.NoError(t, err)

	body := `{"task_status":"done","summary":"finished"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/outcome", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	canonical := "/api/runtime/teams/" + teamID + "/tasks/" + taskID + "/outcome"
	require.Equal(t, canonical, rec.Header().Get("X-AI-Gateway-Entrypoint"))
	require.Equal(t, canonical, rec.Header().Get("X-AI-Gateway-Canonical-Entrypoint"))
	require.Equal(t, "canonical", rec.Header().Get("X-AI-Gateway-Entrypoint-Mode"))
	require.Empty(t, rec.Header().Get("Warning"))
}

func TestReportTaskOutcomeHandlerRejectsMissingStructuredStatus(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-outcome-report-missing-status?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID: teamID,
		Title:  "missing status",
		Status: team.TaskStatusRunning,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/outcome", strings.NewReader(`{"summary":"legacy body not allowed here"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "task_status is required")
}

func TestCreateTaskUsesAgentControlTaskCreateWriter(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-create-agentcontrol-writer?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks", strings.NewReader(`{"id":"task-create-api","title":"create through api","goal":"verify create","status":"ready","priority":5,"assignee":"mate-1","read_paths":["docs"],"write_paths":["docs/plan"],"deliverables":["summary"],"summary":"created through agentcontrol"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var resp struct {
		Task team.Task `json:"task"`
	}
	require.NoError(t, decodeJSONResponse(rec, &resp))
	require.Equal(t, "task-create-api", resp.Task.ID)
	require.Equal(t, team.TaskStatusReady, resp.Task.Status)
	require.Equal(t, "create through api", resp.Task.Title)
	require.Equal(t, "created through agentcontrol", resp.Task.Summary)
	require.Equal(t, 5, resp.Task.Priority)
	require.NotNil(t, resp.Task.Assignee)
	require.Equal(t, "mate-1", *resp.Task.Assignee)
	require.Equal(t, []string{"docs"}, resp.Task.ReadPaths)
	require.Equal(t, []string{"docs/plan"}, resp.Task.WritePaths)

	created, err := store.GetTask(ctx, "task-create-api")
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, team.TaskStatusReady, created.Status)
	require.Equal(t, []string{"summary"}, created.Deliverables)
}

func TestReleaseTaskLeaseUsesAgentControlTaskReleaseWriter(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-release-agentcontrol-writer?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	assignee := "mate-1"
	leaseUntil := time.Now().UTC().Add(time.Minute)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID:     teamID,
		Title:      "release through api",
		Status:     team.TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/release", strings.NewReader(`{"status":"ready","summary":"released through agentcontrol","teammate_id":"mate-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Task team.Task `json:"task"`
	}
	require.NoError(t, decodeJSONResponse(rec, &resp))
	require.Equal(t, team.TaskStatusReady, resp.Task.Status)
	require.Equal(t, "released through agentcontrol", resp.Task.Summary)
	require.Nil(t, resp.Task.Assignee)
	require.Nil(t, resp.Task.LeaseUntil)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, team.TaskStatusReady, updated.Status)
	require.Equal(t, "released through agentcontrol", updated.Summary)
	require.Nil(t, updated.Assignee)
	require.Nil(t, updated.LeaseUntil)
}

func TestRenewTaskLeaseUsesAgentControlTaskLeaseRenewWriter(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-renew-agentcontrol-writer?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	assignee := "mate-1"
	initialLease := time.Now().UTC().Add(time.Minute)
	taskID, err := store.CreateTask(ctx, team.Task{
		TeamID:     teamID,
		Title:      "renew through api",
		Status:     team.TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &initialLease,
	})
	require.NoError(t, err)
	renewedLease := time.Now().UTC().Add(5 * time.Minute)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/teams/"+teamID+"/tasks/"+taskID+"/lease", strings.NewReader(`{"lease_until":"`+renewedLease.Format(time.RFC3339Nano)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Task team.Task `json:"task"`
	}
	require.NoError(t, decodeJSONResponse(rec, &resp))
	require.Equal(t, team.TaskStatusRunning, resp.Task.Status)
	require.NotNil(t, resp.Task.LeaseUntil)
	require.WithinDuration(t, renewedLease, *resp.Task.LeaseUntil, time.Second)
	require.NotNil(t, resp.Task.Assignee)
	require.Equal(t, assignee, *resp.Task.Assignee)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.NotNil(t, updated.LeaseUntil)
	require.WithinDuration(t, renewedLease, *updated.LeaseUntil, time.Second)
}

func TestListAgentControlTasksHandlerProjectsTeamTasks(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-agentcontrol-tasks?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{ID: "team-1"})
	require.NoError(t, err)
	assignee := "mate-1"
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:        assignee,
		TeamID:    teamID,
		Name:      "Mate One",
		SessionID: "mate-session-1",
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{
		ID:       "task-agentcontrol",
		TeamID:   teamID,
		Title:    "Projected task",
		Status:   team.TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/agent-control/tasks?workflow=spawn_team&team_id="+teamID+"&status=running&limit=5", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Tasks []struct {
			ID        string `json:"id"`
			Workflow  string `json:"workflow"`
			TeamID    string `json:"team_id"`
			Assignee  string `json:"assignee"`
			SessionID string `json:"session_id"`
			Path      string `json:"path"`
			Title     string `json:"title"`
			Status    string `json:"status"`
		} `json:"tasks"`
		Count   int                    `json:"count"`
		Filters map[string]interface{} `json:"filters"`
	}
	require.NoError(t, decodeJSONResponse(rec, &payload))
	require.Equal(t, 1, payload.Count)
	require.Len(t, payload.Tasks, 1)
	task := payload.Tasks[0]
	require.Equal(t, "task-agentcontrol", task.ID)
	require.Equal(t, "spawn_team", task.Workflow)
	require.Equal(t, teamID, task.TeamID)
	require.Equal(t, assignee, task.Assignee)
	require.Equal(t, "mate-session-1", task.SessionID)
	require.Equal(t, "/root/teams/team-1/mate-1", task.Path)
	require.Equal(t, "Projected task", task.Title)
	require.Equal(t, "running", task.Status)
	require.Equal(t, "spawn_team", payload.Filters["workflow"])
	require.Equal(t, teamID, payload.Filters["team_id"])
}

func TestAgentControlTaskWriteHandlersUseTaskRegistrySeams(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-agentcontrol-task-write-handlers?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{ID: "team-1"})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:     "mate-1",
		TeamID: teamID,
		State:  team.TeammateStateIdle,
	})
	require.NoError(t, err)

	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime/agent-control/tasks", strings.NewReader(`{"id":"task-control","workflow":"spawn_team","team_id":"team-1","title":"Control task","status":"ready","assignee":"mate-1","write_paths":["docs/plan.md"]}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)
	var createPayload struct {
		Task struct {
			ID       string `json:"id"`
			Workflow string `json:"workflow"`
			TeamID   string `json:"team_id"`
			Status   string `json:"status"`
			Assignee string `json:"assignee"`
		} `json:"task"`
	}
	require.NoError(t, decodeJSONResponse(createRec, &createPayload))
	require.Equal(t, "task-control", createPayload.Task.ID)
	require.Equal(t, "spawn_team", createPayload.Task.Workflow)
	require.Equal(t, teamID, createPayload.Task.TeamID)
	require.Equal(t, "ready", createPayload.Task.Status)
	require.Equal(t, "mate-1", createPayload.Task.Assignee)

	task, err := store.GetTask(ctx, "task-control")
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, int64(1), task.Version)

	claimBody := `{"workflow":"spawn_team","team_id":"team-1","assignee":"mate-1","expected_version":1,"duration_sec":600,"use_path_claims":true,"write_paths":["docs/plan.md"],"workspace_root":"workspace"}`
	claimReq := httptest.NewRequest(http.MethodPost, "/api/runtime/agent-control/tasks/task-control/claim", strings.NewReader(claimBody))
	claimReq.Header.Set("Content-Type", "application/json")
	claimRec := httptest.NewRecorder()
	router.ServeHTTP(claimRec, claimReq)
	require.Equal(t, http.StatusOK, claimRec.Code)
	var claimPayload struct {
		Task struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
		Claimed bool `json:"claimed"`
	}
	require.NoError(t, decodeJSONResponse(claimRec, &claimPayload))
	require.True(t, claimPayload.Claimed)
	require.Equal(t, "running", claimPayload.Task.Status)

	claims, err := store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, "workspace/docs/plan.md", claims[0].Path)

	terminalReq := httptest.NewRequest(http.MethodPost, "/api/runtime/agent-control/tasks/task-control/terminal", strings.NewReader(`{"workflow":"spawn_team","team_id":"team-1","status":"done","summary":"finished","result_ref":"artifact://done","teammate_id":"mate-1"}`))
	terminalReq.Header.Set("Content-Type", "application/json")
	terminalRec := httptest.NewRecorder()
	router.ServeHTTP(terminalRec, terminalReq)
	require.Equal(t, http.StatusOK, terminalRec.Code)
	var terminalPayload struct {
		Task struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Summary string `json:"summary"`
		} `json:"task"`
	}
	require.NoError(t, decodeJSONResponse(terminalRec, &terminalPayload))
	require.Equal(t, "done", terminalPayload.Task.Status)
	require.Equal(t, "finished", terminalPayload.Task.Summary)

	updated, err := store.GetTask(ctx, "task-control")
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, team.TaskStatusDone, updated.Status)
	require.Nil(t, updated.Assignee)
	require.Nil(t, updated.LeaseUntil)
	require.NotNil(t, updated.ResultRef)
	require.Equal(t, "artifact://done", *updated.ResultRef)
	claims, err = store.ListPathClaims(ctx, teamID)
	require.NoError(t, err)
	require.Empty(t, claims)

	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	require.Equal(t, team.TeammateStateIdle, mate.State)
}

func TestAgentControlTaskBlockHandlerUsesTaskRegistrySeam(t *testing.T) {
	ctx, store, router := newTeamTaskOutcomeTestRouter(t, "file:team-agentcontrol-task-block-handler?mode=memory&cache=shared")

	teamID, err := store.CreateTeam(ctx, team.Team{ID: "team-1"})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID:     "mate-1",
		TeamID: teamID,
		State:  team.TeammateStateBusy,
	})
	require.NoError(t, err)
	assignee := "mate-1"
	taskID, err := store.CreateTask(ctx, team.Task{
		ID:       "task-block-control",
		TeamID:   teamID,
		Title:    "Block through control",
		Status:   team.TaskStatusRunning,
		Assignee: &assignee,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/agent-control/tasks/"+taskID+"/block", strings.NewReader(`{"workflow":"spawn_team","summary":"waiting on dependency","teammate_id":"mate-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Task struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Summary string `json:"summary"`
		} `json:"task"`
	}
	require.NoError(t, decodeJSONResponse(rec, &payload))
	require.Equal(t, taskID, payload.Task.ID)
	require.Equal(t, "blocked", payload.Task.Status)
	require.Equal(t, "waiting on dependency", payload.Task.Summary)

	updated, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, team.TaskStatusBlocked, updated.Status)
	require.Equal(t, "waiting on dependency", updated.Summary)
	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	require.Equal(t, team.TeammateStateBlocked, mate.State)
}

func newTeamTaskOutcomeTestRouter(t *testing.T, dsn string) (context.Context, team.Store, *mux.Router) {
	t.Helper()

	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	handler.sessionHub = chat.NewSessionHub(nil)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return context.Background(), store, router
}

func decodeJSONResponse(rec *httptest.ResponseRecorder, target interface{}) error {
	return json.NewDecoder(rec.Body).Decode(target)
}
