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

func TestGetTeamPreflightReportsLegacyCompatibilityAndRuntimeFindings(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:team-preflight-handler?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(ctx, team.Team{})
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

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/runtime/teams/"+teamID+"/preflight",
		nil,
	)
	request.RemoteAddr = "127.0.0.1:4321"
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)

	var response struct {
		TeamID            string `json:"team_id"`
		CompatibilityMode string `json:"compatibility_mode"`
		VersionSupported  bool   `json:"version_supported"`
		TeamStatus        string `json:"team_status"`
		EventSeq          int64  `json:"event_seq"`
		SummaryState      string `json:"summary_state"`
		Readiness         bool   `json:"readiness"`
		Preflight         struct {
			TeamExists   bool                    `json:"team_exists"`
			Ready        bool                    `json:"ready"`
			BlockerCount int                     `json:"blocker_count"`
			Blockers     []team.PreflightFinding `json:"blockers"`
		} `json:"preflight"`
		Capabilities map[string]string `json:"capabilities"`
	}
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
	require.Equal(t, teamID, response.TeamID)
	require.Equal(t, "legacy_team_view", response.CompatibilityMode)
	require.False(t, response.VersionSupported, "legacy Team records do not expose a Team version")
	require.Equal(t, string(team.TeamStatusActive), response.TeamStatus)
	require.EqualValues(t, 1, response.EventSeq)
	require.Equal(t, "not_applicable", response.SummaryState)
	require.False(t, response.Readiness)
	require.True(t, response.Preflight.TeamExists)
	require.False(t, response.Preflight.Ready)
	require.Positive(t, response.Preflight.BlockerCount)
	require.NotEmpty(t, response.Preflight.Blockers)
	require.Equal(t, "unsupported", response.Capabilities["team_cancel"])
	require.Equal(t, "unsupported", response.Capabilities["independent_run_history"])
	require.Equal(t, "supported", response.Capabilities["team_event_polling"])
	require.Equal(t, "unknown", response.Capabilities["workspace_scoped_authorization"])
}

func TestGetTeamReadinessReturnsFullPreflightExplanation(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{DSN: "file:team-readiness-handler?mode=memory&cache=shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamID, err := store.CreateTeam(context.Background(), team.Team{})
	require.NoError(t, err)
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)

	recorder := httptest.NewRecorder()
	handler.GetTeamReadiness(recorder, mux.SetURLVars(
		httptest.NewRequest(http.MethodGet, "/readiness", nil),
		map[string]string{"id": teamID},
	))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"preflight"`)
	require.Contains(t, recorder.Body.String(), `"readiness":false`)
}
