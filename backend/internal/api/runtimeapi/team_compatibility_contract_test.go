package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

type teamCompatibilityResponse struct {
	Team              team.Team         `json:"team"`
	CompatibilityMode string            `json:"compatibility_mode"`
	TeamVersion       *int64            `json:"team_version"`
	VersionSupported  bool              `json:"version_supported"`
	TeamStatus        team.TeamStatus   `json:"team_status"`
	EventSeq          int64             `json:"event_seq"`
	CursorScope       string            `json:"cursor_scope"`
	SummaryState      string            `json:"summary_state"`
	Capabilities      map[string]string `json:"capabilities"`
	RequestID         string            `json:"request_id"`
	TraceID           string            `json:"trace_id"`
}

func TestGetTeamReturnsAdditiveLegacyCompatibilityEnvelope(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:team-compatibility-detail?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{Status: team.TeamStatusActive})
	require.NoError(t, err)
	_, err = store.AppendTeamEvent(ctx, team.TeamEvent{
		TeamID: teamID,
		Type:   "team.created",
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	request := httptest.NewRequest(http.MethodGet, "/api/runtime/teams/"+teamID, nil)
	request.RemoteAddr = "127.0.0.1:4321"
	request.Header.Set("X-Request-ID", "req-team-contract")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)

	var response teamCompatibilityResponse
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
	require.Equal(t, teamID, response.Team.ID)
	require.Equal(t, team.TeamStatusActive, response.TeamStatus)
	require.Equal(t, "legacy_team_view", response.CompatibilityMode)
	require.Nil(t, response.TeamVersion)
	require.False(t, response.VersionSupported)
	require.EqualValues(t, 1, response.EventSeq)
	require.Equal(t, "team_local", response.CursorScope)
	require.Equal(t, "not_applicable", response.SummaryState)
	require.Equal(t, "req-team-contract", response.RequestID)
	require.NotEmpty(t, response.TraceID)
	require.Equal(t, response.TraceID, recorder.Header().Get("X-Trace-ID"))
	require.Equal(t, "supported", response.Capabilities["team_event_polling"])
	require.Equal(t, "supported", response.Capabilities["structured_task_outcome"])
	require.Equal(t, "supported", response.Capabilities["opaque_result_reference"])
	require.Equal(t, "unsupported", response.Capabilities["controlled_team_start"])
	require.Equal(t, "unsupported", response.Capabilities["worker_session_liveness"])
	require.Equal(t, "unsupported", response.Capabilities["artifact_download"])
	require.Equal(t, "unknown", response.Capabilities["workspace_scoped_authorization"])

	listRequest := httptest.NewRequest(http.MethodGet, "/api/runtime/teams", nil)
	listRequest.RemoteAddr = "127.0.0.1:4321"
	listRequest.Header.Set("X-Request-ID", "req-team-list-contract")
	listRecorder := httptest.NewRecorder()
	router.ServeHTTP(listRecorder, listRequest)
	require.Equal(t, http.StatusOK, listRecorder.Code)
	var listResponse struct {
		Teams             []team.Team       `json:"teams"`
		CompatibilityMode string            `json:"compatibility_mode"`
		TeamVersion       *int64            `json:"team_version"`
		VersionSupported  bool              `json:"version_supported"`
		CursorScope       string            `json:"cursor_scope"`
		Capabilities      map[string]string `json:"capabilities"`
		RequestID         string            `json:"request_id"`
		TraceID           string            `json:"trace_id"`
	}
	require.NoError(t, json.NewDecoder(listRecorder.Body).Decode(&listResponse))
	require.Len(t, listResponse.Teams, 1)
	require.Equal(t, "legacy_team_view", listResponse.CompatibilityMode)
	require.Nil(t, listResponse.TeamVersion)
	require.False(t, listResponse.VersionSupported)
	require.Equal(t, "team_local", listResponse.CursorScope)
	require.Equal(t, "unsupported", listResponse.Capabilities["controlled_team_start"])
	require.Equal(t, "req-team-list-contract", listResponse.RequestID)
	require.NotEmpty(t, listResponse.TraceID)
	require.NotContains(t, listRecorder.Body.String(), `"team_views"`)
	require.NotContains(t, listRecorder.Body.String(), `"event_seq"`)
}

func TestTeamCompatibilityEnvelopeTracksTerminalSummaryReadiness(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:team-compatibility-summary?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{Status: team.TeamStatusDone})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)

	getContract := func() teamCompatibilityResponse {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.GetTeam(
			recorder,
			mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/team", nil), map[string]string{"id": teamID}),
		)
		require.Equal(t, http.StatusOK, recorder.Code)
		var response teamCompatibilityResponse
		require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
		return response
	}

	require.Equal(t, "pending", getContract().SummaryState)

	_, err = store.AppendTeamEvent(ctx, team.TeamEvent{
		TeamID: teamID,
		Type:   "team.summary.failed",
		Payload: map[string]interface{}{
			"error_type": "prompt_preflight",
		},
	})
	require.NoError(t, err)
	_, err = store.AppendTeamEvent(ctx, team.TeamEvent{
		TeamID: teamID,
		Type:   "team.summary",
		Payload: map[string]interface{}{
			"summary":         "fallback summary",
			"summary_source":  "fallback",
			"used_fallback":   true,
			"fallback_reason": "lead_session_error",
			"error_type":      "prompt_preflight",
		},
	})
	require.NoError(t, err)
	failed := getContract()
	require.Equal(t, "failed", failed.SummaryState)
	require.EqualValues(t, 2, failed.EventSeq)

	_, err = store.AppendTeamEvent(ctx, team.TeamEvent{
		TeamID: teamID,
		Type:   "team.summary",
		Payload: map[string]interface{}{
			"summary":        "recovered summary",
			"summary_source": "lead",
		},
	})
	require.NoError(t, err)
	recovered := getContract()
	require.Equal(t, "ready", recovered.SummaryState)
	require.EqualValues(t, 3, recovered.EventSeq)
}

type teamCompatibilityPagedEventsStore struct {
	team.Store
	events []team.TeamEventRecord
	calls  int
}

func (s *teamCompatibilityPagedEventsStore) ListTeamEvents(_ context.Context, filter team.TeamEventFilter) ([]team.TeamEventRecord, error) {
	s.calls++
	page := make([]team.TeamEventRecord, 0, filter.Limit)
	for _, event := range s.events {
		if event.Seq <= filter.AfterSeq {
			continue
		}
		page = append(page, event)
		if filter.Limit > 0 && len(page) == filter.Limit {
			break
		}
	}
	return page, nil
}

func TestPersistedTeamSummaryStatePaginatesBeyondLegacyEventLimit(t *testing.T) {
	store := &teamCompatibilityPagedEventsStore{
		events: make([]team.TeamEventRecord, 0, 1001),
	}
	for sequence := int64(1); sequence <= 1000; sequence++ {
		store.events = append(store.events, team.TeamEventRecord{
			Seq: sequence,
			TeamEvent: team.TeamEvent{
				Type: "team.summary.failed",
				Payload: map[string]interface{}{
					"error_type": "older_failure",
				},
			},
		})
	}
	store.events = append(store.events, team.TeamEventRecord{
		Seq: 1001,
		TeamEvent: team.TeamEvent{
			Type: "team.summary",
			Payload: map[string]interface{}{
				"summary": "latest successful summary",
			},
		},
	})

	state, err := persistedTeamSummaryState(context.Background(), store, "team-paged-summary")
	require.NoError(t, err)
	require.Equal(t, "ready", state)
	require.Greater(t, store.calls, 1)
}
