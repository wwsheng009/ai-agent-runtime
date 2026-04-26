package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestListTeamEventsHandlerFilters(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:team-events-handler?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)

	base := time.Now().UTC().Add(-2 * time.Hour)
	_, err = store.AppendTeamEvent(ctx, team.TeamEvent{
		TeamID:    teamID,
		Type:      "task.completed",
		Timestamp: base,
		Payload:   map[string]interface{}{"task_id": "task-1"},
	})
	require.NoError(t, err)
	_, err = store.AppendTeamEvent(ctx, team.TeamEvent{
		TeamID:    teamID,
		Type:      "team.summary",
		Timestamp: base.Add(30 * time.Minute),
		Payload:   map[string]interface{}{"summary": "done"},
	})
	require.NoError(t, err)
	_, err = store.AppendTeamEvent(ctx, team.TeamEvent{
		TeamID:    teamID,
		Type:      "task.failed",
		Timestamp: base.Add(60 * time.Minute),
		Payload:   map[string]interface{}{"task_id": "task-2"},
	})
	require.NoError(t, err)

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetTeamStore(store)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/teams/"+teamID+"/events?event_type=task.*", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		TeamID string                 `json:"team_id"`
		Events []team.TeamEventRecord `json:"events"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, teamID, resp.TeamID)
	require.Len(t, resp.Events, 2)
	require.Equal(t, "task.completed", resp.Events[0].Type)
	require.Equal(t, "task.failed", resp.Events[1].Type)

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/teams/"+teamID+"/events?since="+base.Add(30*time.Minute).Format(time.RFC3339Nano), nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Events, 2)
	require.Equal(t, int64(2), resp.Events[0].Seq)
	require.Equal(t, "team.summary", resp.Events[0].Type)
	require.Equal(t, int64(3), resp.Events[1].Seq)
	require.Equal(t, "task.failed", resp.Events[1].Type)

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/teams/"+teamID+"/events?until="+base.Add(45*time.Minute).Format(time.RFC3339Nano), nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Events, 2)
	require.Equal(t, int64(1), resp.Events[0].Seq)
	require.Equal(t, "task.completed", resp.Events[0].Type)
	require.Equal(t, int64(2), resp.Events[1].Seq)
	require.Equal(t, "team.summary", resp.Events[1].Type)

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/teams/"+teamID+"/events?since="+base.Add(15*time.Minute).Format(time.RFC3339Nano)+"&until="+base.Add(45*time.Minute).Format(time.RFC3339Nano), nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Events, 1)
	require.Equal(t, int64(2), resp.Events[0].Seq)
	require.Equal(t, "team.summary", resp.Events[0].Type)
}

func TestGetTeamFinalSummaryReturnsFallbackMetadataWhenLeadSessionExecutionFails(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:team-final-summary-handler?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{
		TeamID:  teamID,
		Title:   "done task",
		Status:  team.TaskStatusDone,
		Summary: "finished",
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/teams/"+teamID+"/summary/final", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, teamID, resp["team_id"])
	require.Equal(t, "fallback", resp["summary_source"])
	require.Equal(t, true, resp["used_fallback"])
	require.Equal(t, "lead_session_error", resp["fallback_reason"])
	require.Contains(t, resp["summary"], "Team "+teamID+" summary:")

	events := handler.getRuntimeEventBus().Query(runtimeevents.QueryFilter{
		TeamID:    teamID,
		EventType: "team.summary.generated",
		Limit:     10,
	})
	require.NotEmpty(t, events)
	require.Equal(t, "fallback", events[0].Payload["summary_source"])
	require.Equal(t, true, events[0].Payload["used_fallback"])
	require.Equal(t, "lead_session_error", events[0].Payload["fallback_reason"])
}
