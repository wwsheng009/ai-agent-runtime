package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
