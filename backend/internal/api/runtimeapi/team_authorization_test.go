package runtimeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestLegacyTeamRoutesEnforceTrustedWorkspaceIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "legacy-team-auth.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	teamAID, err := store.CreateTeam(ctx, team.Team{ID: "team-workspace-a", WorkspaceID: "workspace-a"})
	require.NoError(t, err)
	teamBID, err := store.CreateTeam(ctx, team.Team{ID: "team-workspace-b", WorkspaceID: "workspace-b"})
	require.NoError(t, err)
	taskBID, err := store.CreateTask(ctx, team.Task{
		ID:     "task-workspace-b",
		TeamID: teamBID,
		Title:  "private task",
		Goal:   "must not be readable across workspaces",
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled: true,
		APIKeyScopes: map[string]UsageScope{
			"alice-workspace-a": {UserID: "alice", WorkspaceID: "workspace-a"},
			"alice-workspace-b": {UserID: "alice", WorkspaceID: "workspace-b"},
		},
	})
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	doRequest := func(method, path, apiKey, body string, remoteAddr string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.RemoteAddr = remoteAddr
		if apiKey != "" {
			request.Header.Set("X-API-Key", apiKey)
		}
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	remote := "203.0.113.8:4321"

	t.Run("remote request without product identity is rejected", func(t *testing.T) {
		response := doRequest(http.MethodGet, "/api/runtime/teams", "", "", remote)
		require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "TEAM_PRODUCT_UNAUTHENTICATED")
	})

	t.Run("loopback transport and runtime access are not product identity", func(t *testing.T) {
		response := doRequest(http.MethodGet, "/api/runtime/teams", "", "", "127.0.0.1:4321")
		require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	})

	t.Run("workspace query cannot select a foreign workspace", func(t *testing.T) {
		response := doRequest(http.MethodGet,
			"/api/runtime/teams?workspace_id=workspace-b&team_ids="+teamBID,
			"alice-workspace-a", "", remote)
		require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
		require.NotContains(t, response.Body.String(), teamBID)

		response = doRequest(http.MethodGet,
			"/api/runtime/teams?team_ids="+teamBID,
			"alice-workspace-a", "", remote)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var payload struct {
			Teams []team.Team `json:"teams"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
		require.Empty(t, payload.Teams)
	})

	t.Run("summary ids are intersected with the authenticated workspace", func(t *testing.T) {
		response := doRequest(http.MethodGet,
			"/api/runtime/teams/summary?team_ids="+teamBID,
			"alice-workspace-a", "", remote)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.NotContains(t, response.Body.String(), teamBID)
		require.NotContains(t, response.Body.String(), taskBID)
		require.Contains(t, response.Body.String(), `"count":0`)
	})

	t.Run("foreign Team and nested resources return indistinguishable not found", func(t *testing.T) {
		for _, path := range []string{
			"/api/runtime/teams/" + teamBID,
			"/api/runtime/teams/" + teamBID + "/tasks",
			"/api/runtime/teams/" + teamBID + "/mailbox",
			"/api/runtime/teams/" + teamBID + "/path-claims",
			"/api/runtime/teams/" + teamBID + "/tasks/" + taskBID + "/retry",
			"/api/runtime/teams/" + teamBID + "/tasks/" + taskBID + "/outcome",
		} {
			response := doRequest(http.MethodGet, path, "alice-workspace-a", "", remote)
			if path == "/api/runtime/teams/"+teamBID+"/tasks/"+taskBID+"/retry" ||
				path == "/api/runtime/teams/"+teamBID+"/tasks/"+taskBID+"/outcome" {
				response = doRequest(http.MethodPost, path, "alice-workspace-a", `{}`, remote)
			}
			require.Equal(t, http.StatusNotFound, response.Code, "%s: %s", path, response.Body.String())
			require.NotContains(t, response.Body.String(), teamBID)
			require.NotContains(t, response.Body.String(), taskBID)
		}
	})

	t.Run("create binds workspace to trusted identity", func(t *testing.T) {
		forged := doRequest(http.MethodPost, "/api/runtime/teams", "alice-workspace-a",
			`{"id":"forged-team","workspace_id":"workspace-b"}`, remote)
		require.Equal(t, http.StatusForbidden, forged.Code, forged.Body.String())
		record, getErr := store.GetTeam(ctx, "forged-team")
		require.NoError(t, getErr)
		require.Nil(t, record)

		created := doRequest(http.MethodPost, "/api/runtime/teams", "alice-workspace-a",
			`{"id":"bound-team","strategy":"parallel"}`, remote)
		require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
		var payload struct {
			Team team.Team `json:"team"`
		}
		require.NoError(t, json.Unmarshal(created.Body.Bytes(), &payload))
		require.Equal(t, "workspace-a", payload.Team.WorkspaceID)

		updated := doRequest(http.MethodPatch, "/api/runtime/teams/"+teamAID,
			"alice-workspace-a", `{"workspace_id":"workspace-b"}`, remote)
		require.Equal(t, http.StatusForbidden, updated.Code, updated.Body.String())
	})
}

func TestLegacyTeamRoutesScopeDisabledOnlyAllowsDirectLoopback(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "legacy-team-local.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.CreateTeam(ctx, team.Team{ID: "local-team"})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	remote := httptest.NewRequest(http.MethodGet, "/api/runtime/teams", nil)
	remote.RemoteAddr = "203.0.113.9:4321"
	remoteRecorder := httptest.NewRecorder()
	router.ServeHTTP(remoteRecorder, remote)
	require.Equal(t, http.StatusUnauthorized, remoteRecorder.Code)

	loopback := httptest.NewRequest(http.MethodGet, "/api/runtime/teams", nil)
	loopback.RemoteAddr = "127.0.0.1:4321"
	loopbackRecorder := httptest.NewRecorder()
	router.ServeHTTP(loopbackRecorder, loopback)
	require.Equal(t, http.StatusOK, loopbackRecorder.Code, loopbackRecorder.Body.String())
}

func TestEveryRegisteredLegacyTeamResourceRouteChecksWorkspaceBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "legacy-team-all-routes-auth.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	const (
		foreignTeamID = "team-foreign-workspace"
		foreignTaskID = "task-foreign-workspace"
	)
	_, err = store.CreateTeam(ctx, team.Team{ID: foreignTeamID, WorkspaceID: "workspace-b"})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{
		ID: foreignTaskID, TeamID: foreignTeamID, Title: "private foreign task",
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled: true,
		APIKeyScopes: map[string]UsageScope{
			"alice-workspace-a": {UserID: "alice", WorkspaceID: "workspace-a"},
		},
	})
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	type registeredRoute struct {
		method string
		path   string
	}
	routes := make([]registeredRoute, 0, 48)
	teamIDPattern := regexp.MustCompile(`\{([^}:]+)(?::[^}]*)?\}`)
	err = router.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		template, templateErr := route.GetPathTemplate()
		if templateErr != nil ||
			!strings.HasPrefix(template, "/api/runtime/teams/{id}") {
			return nil
		}
		methods, methodsErr := route.GetMethods()
		if methodsErr != nil {
			return methodsErr
		}
		for _, method := range methods {
			path := teamIDPattern.ReplaceAllStringFunc(template, func(match string) string {
				name := strings.TrimSuffix(strings.Split(strings.Trim(match, "{}"), ":")[0], "?")
				switch {
				case name == "id":
					return foreignTeamID
				case strings.Contains(name, "task"):
					return foreignTaskID
				default:
					return "foreign-" + name
				}
			})
			routes = append(routes, registeredRoute{method: method, path: path})
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, routes, "registered legacy Team resource routes must be discovered")

	for _, route := range routes {
		t.Run(route.method+" "+strings.TrimPrefix(route.path, "/api/runtime"), func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, bytes.NewBufferString(`{}`))
			request.RemoteAddr = "203.0.113.8:4321"
			request.Header.Set("X-API-Key", "alice-workspace-a")
			request.Header.Set("Content-Type", "application/json")

			match := &mux.RouteMatch{}
			require.True(t, router.Match(request, match), "test route must be registered")

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
			require.Contains(t, recorder.Body.String(), "TEAM_NOT_FOUND")
			require.NotContains(t, recorder.Body.String(), foreignTeamID)
			require.NotContains(t, recorder.Body.String(), foreignTaskID)
		})
	}
}
