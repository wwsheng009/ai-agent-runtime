package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
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
	req.RemoteAddr = "127.0.0.1:4321"
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
	req.RemoteAddr = "127.0.0.1:4321"
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
	req.RemoteAddr = "127.0.0.1:4321"
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
	req.RemoteAddr = "127.0.0.1:4321"
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Events, 1)
	require.Equal(t, int64(2), resp.Events[0].Seq)
	require.Equal(t, "team.summary", resp.Events[0].Type)
}

func TestListTeamEventsHandlerKeepsPageCursorSeparateFromHighWater(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:team-events-cursor-handler?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	for _, eventType := range []string{"task.started", "task.completed", "team.completed"} {
		_, err := store.AppendTeamEvent(ctx, team.TeamEvent{
			TeamID: teamID,
			Type:   eventType,
		})
		require.NoError(t, err)
	}

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	decode := func(path string) struct {
		Events     []team.TeamEventRecord `json:"events"`
		AfterSeq   int64                  `json:"after_seq"`
		LatestSeq  int64                  `json:"latest_seq"`
		NextCursor int64                  `json:"next_cursor"`
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.RemoteAddr = "127.0.0.1:4321"
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code)
		var response struct {
			Events     []team.TeamEventRecord `json:"events"`
			AfterSeq   int64                  `json:"after_seq"`
			LatestSeq  int64                  `json:"latest_seq"`
			NextCursor int64                  `json:"next_cursor"`
		}
		require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
		return response
	}

	first := decode("/api/runtime/teams/" + teamID + "/events?limit=1")
	require.Len(t, first.Events, 1)
	require.EqualValues(t, 1, first.Events[0].Seq)
	require.Zero(t, first.AfterSeq)
	require.EqualValues(t, 1, first.NextCursor)
	require.EqualValues(t, 3, first.LatestSeq, "high-water may be beyond this page")

	second := decode("/api/runtime/teams/" + teamID + "/events?after_seq=1&limit=1")
	require.Len(t, second.Events, 1)
	require.EqualValues(t, 2, second.Events[0].Seq)
	require.EqualValues(t, 1, second.AfterSeq)
	require.EqualValues(t, 2, second.NextCursor)
	require.EqualValues(t, 3, second.LatestSeq)
}

func TestUpdateTeamRejectsUncontrolledStatusTransition(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:team-update-status-contract?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:     "team-update-status-contract",
		Status: team.TeamStatusActive,
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(
		http.MethodPatch,
		"/api/runtime/teams/"+teamID,
		strings.NewReader(`{"status":"paused"}`),
	)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	updated, err := store.GetTeam(ctx, teamID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, team.TeamStatusActive, updated.Status)
	assert.Contains(t, rec.Body.String(), "controlled lifecycle actions are not available")
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
	req.RemoteAddr = "127.0.0.1:4321"
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
