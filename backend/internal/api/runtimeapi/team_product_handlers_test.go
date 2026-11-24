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

func TestTeamProductDefinitionAPIRequiresTrustedIdentityAndScopesResources(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team-product.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled: true,
		APIKeyScopes: map[string]UsageScope{
			"alice-workspace-a": {UserID: "alice", WorkspaceID: "workspace-a"},
			"alice-workspace-b": {UserID: "alice", WorkspaceID: "workspace-b"},
			"bob-workspace-a":   {UserID: "bob", WorkspaceID: "workspace-a"},
		},
		UserHeaders:      []string{"X-User-ID"},
		WorkspaceHeaders: []string{"X-Workspace-ID"},
	})
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	doRequest := func(method, path, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		if key != "" {
			request.Header.Set("X-API-Key", key)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	unauthenticated := doRequest(http.MethodPost, "/api/runtime/team-definitions", "",
		`{"name":"forged","config":{"mode":"guided"},"owner_id":"alice","workspace_id":"workspace-a"}`)
	require.Equal(t, http.StatusUnauthorized, unauthenticated.Code)

	callerChosenRequest := httptest.NewRequest(http.MethodPost, "/api/runtime/team-definitions?user_id=alice&workspace_id=workspace-a", nil)
	callerChosenRequest.Header.Set("X-User-ID", "alice")
	callerChosenRequest.Header.Set("X-Workspace-ID", "workspace-a")
	callerChosenRequest.Header.Set("Content-Type", "application/json")
	callerChosen := httptest.NewRecorder()
	router.ServeHTTP(callerChosen, callerChosenRequest)
	require.Equal(t, http.StatusUnauthorized, callerChosen.Code)

	forgedOwner := doRequest(http.MethodPost, "/api/runtime/team-definitions", "alice-workspace-a",
		`{"name":"forged","config":{"mode":"guided"},"owner_id":"bob","workspace_id":"workspace-b"}`)
	require.Equal(t, http.StatusBadRequest, forgedOwner.Code, "caller cannot select owner/workspace in JSON")

	created := doRequest(http.MethodPost, "/api/runtime/team-definitions", "alice-workspace-a",
		`{"name":"Alice Team","description":"workspace-bound","config":{"mode":"guided"}}`)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var createPayload struct {
		Definition   team.TeamDefinition `json:"definition"`
		Capabilities map[string]string   `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &createPayload))
	require.Equal(t, "alice", createPayload.Definition.OwnerID)
	require.Equal(t, "workspace-a", createPayload.Definition.WorkspaceID)
	require.Equal(t, "unsupported", createPayload.Capabilities["run_pause"])
	require.Equal(t, "unsupported", createPayload.Capabilities["run_resume"])
	require.Equal(t, "unsupported", createPayload.Capabilities["run_cancel"])
	require.Equal(t, "unsupported", createPayload.Capabilities["run_route_preview"])
	require.Equal(t, "unsupported", createPayload.Capabilities["run_history"])
	require.Equal(t, "unsupported", createPayload.Capabilities["worker_session_liveness"])
	require.Equal(t, "unsupported", createPayload.Capabilities["budget_hard_stop"])
	require.Equal(t, "unsupported", createPayload.Capabilities["event_stream"])
	require.Equal(t, "unsupported", createPayload.Capabilities["artifact_download"])
	require.Equal(t, "unsupported", createPayload.Capabilities["artifact_retention"])
	require.Equal(t, "unsupported", createPayload.Capabilities["attempt_lineage"])
	definitionPath := "/api/runtime/team-definitions/" + createPayload.Definition.ID

	ownRead := doRequest(http.MethodGet, definitionPath, "alice-workspace-a", "")
	require.Equal(t, http.StatusOK, ownRead.Code)

	differentWorkspace := doRequest(http.MethodGet, definitionPath, "alice-workspace-b", "")
	require.Equal(t, http.StatusNotFound, differentWorkspace.Code)
	require.NotContains(t, differentWorkspace.Body.String(), "Alice Team")

	differentOwner := doRequest(http.MethodGet, definitionPath, "bob-workspace-a", "")
	require.Equal(t, http.StatusNotFound, differentOwner.Code)
	require.NotContains(t, differentOwner.Body.String(), "Alice Team")

	versionConflict := doRequest(http.MethodPatch, definitionPath, "alice-workspace-a",
		`{"expected_version":9,"name":"Renamed","description":"","config":{"mode":"advanced"}}`)
	require.Equal(t, http.StatusConflict, versionConflict.Code)
	require.Contains(t, versionConflict.Body.String(), "TEAM_DEFINITION_VERSION_CONFLICT")

	update := doRequest(http.MethodPatch, definitionPath, "alice-workspace-a",
		`{"expected_version":1,"name":"Renamed","description":"","config":{"mode":"advanced"}}`)
	require.Equal(t, http.StatusOK, update.Code, update.Body.String())
	var updatePayload struct {
		Definition team.TeamDefinition `json:"definition"`
	}
	require.NoError(t, json.Unmarshal(update.Body.Bytes(), &updatePayload))
	require.Equal(t, int64(2), updatePayload.Definition.Version)
}

func TestTeamProductRunDraftAPIIdempotencyAndLifecycleBoundary(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team-run-product.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled: true,
		APIKeyScopes: map[string]UsageScope{
			"alice":                 {UserID: "alice", WorkspaceID: "workspace-a"},
			"alice-other-workspace": {UserID: "alice", WorkspaceID: "workspace-b"},
		},
	})
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	doRunDraft := func(key, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/runtime/team-runs", bytes.NewBufferString(body))
		request.Header.Set("X-API-Key", "alice")
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	first := doRunDraft("draft-key-1", `{"goal":"Review the patch","config":{"mode":"guided"}}`)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	var firstPayload struct {
		Run          team.TeamRun      `json:"run"`
		Capabilities map[string]string `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstPayload))
	require.Equal(t, "alice", firstPayload.Run.OwnerID)
	require.Equal(t, "workspace-a", firstPayload.Run.WorkspaceID)
	require.Equal(t, team.TeamRunStatusDraft, firstPayload.Run.Status)
	require.Equal(t, "supported", firstPayload.Capabilities["run_draft_create"])
	require.Equal(t, "supported", firstPayload.Capabilities["run_start"])
	require.Equal(t, "supported", firstPayload.Capabilities["run_preflight"])
	require.Equal(t, "unsupported", firstPayload.Capabilities["run_route_preview"])
	require.Equal(t, "unsupported", firstPayload.Capabilities["run_pause"])
	require.Equal(t, "unsupported", firstPayload.Capabilities["run_resume"])
	require.Equal(t, "unsupported", firstPayload.Capabilities["run_cancel"])
	require.Equal(t, "unsupported", firstPayload.Capabilities["worker_session_liveness"])
	require.Equal(t, "unsupported", firstPayload.Capabilities["budget_hard_stop"])
	require.Equal(t, "unsupported", firstPayload.Capabilities["event_stream"])
	require.Equal(t, "unsupported", firstPayload.Capabilities["artifact_download"])
	require.Equal(t, "unsupported", firstPayload.Capabilities["artifact_retention"])
	require.Equal(t, "unsupported", firstPayload.Capabilities["attempt_lineage"])

	replayed := doRunDraft("draft-key-1", `{"goal":"Review the patch","config":{"mode":"guided"}}`)
	require.Equal(t, http.StatusCreated, replayed.Code, replayed.Body.String())
	var replayPayload struct {
		Run team.TeamRun `json:"run"`
	}
	require.NoError(t, json.Unmarshal(replayed.Body.Bytes(), &replayPayload))
	require.Equal(t, firstPayload.Run.ID, replayPayload.Run.ID, "same request replays one immutable Run draft")

	ownRunRead := httptest.NewRequest(http.MethodGet,
		"/api/runtime/team-runs/"+firstPayload.Run.ID, nil)
	ownRunRead.Header.Set("X-API-Key", "alice")
	ownRunRecorder := httptest.NewRecorder()
	router.ServeHTTP(ownRunRecorder, ownRunRead)
	require.Equal(t, http.StatusOK, ownRunRecorder.Code)

	preflightRequest := httptest.NewRequest(http.MethodGet,
		"/api/runtime/team-runs/"+firstPayload.Run.ID+"/preflight", nil)
	preflightRequest.Header.Set("X-API-Key", "alice")
	preflightRecorder := httptest.NewRecorder()
	router.ServeHTTP(preflightRecorder, preflightRequest)
	require.Equal(t, http.StatusOK, preflightRecorder.Code, preflightRecorder.Body.String())
	require.Contains(t, preflightRecorder.Body.String(), `"run_id":"`+firstPayload.Run.ID+`"`)
	require.Contains(t, preflightRecorder.Body.String(), `"ready":false`)
	require.Contains(t, preflightRecorder.Body.String(), `"route_preview_capability":"disabled"`)

	foreignWorkspacePreflight := httptest.NewRequest(http.MethodGet,
		"/api/runtime/team-runs/"+firstPayload.Run.ID+"/preflight", nil)
	foreignWorkspacePreflight.Header.Set("X-API-Key", "alice-other-workspace")
	foreignWorkspacePreflightRecorder := httptest.NewRecorder()
	router.ServeHTTP(foreignWorkspacePreflightRecorder, foreignWorkspacePreflight)
	require.Equal(t, http.StatusNotFound, foreignWorkspacePreflightRecorder.Code)
	require.NotContains(t, foreignWorkspacePreflightRecorder.Body.String(), firstPayload.Run.ID)

	foreignWorkspaceRead := httptest.NewRequest(http.MethodGet,
		"/api/runtime/team-runs/"+firstPayload.Run.ID, nil)
	foreignWorkspaceRead.Header.Set("X-API-Key", "alice-other-workspace")
	foreignWorkspaceRecorder := httptest.NewRecorder()
	router.ServeHTTP(foreignWorkspaceRecorder, foreignWorkspaceRead)
	require.Equal(t, http.StatusNotFound, foreignWorkspaceRecorder.Code)
	require.NotContains(t, foreignWorkspaceRecorder.Body.String(), firstPayload.Run.ID)

	conflict := doRunDraft("draft-key-1", `{"goal":"Different goal","config":{"mode":"guided"}}`)
	require.Equal(t, http.StatusConflict, conflict.Code)
	require.Contains(t, conflict.Body.String(), "TEAM_RUN_IDEMPOTENCY_CONFLICT")

	otherWorkspaceRequest := httptest.NewRequest(http.MethodGet,
		"/api/runtime/team-runs/"+firstPayload.Run.ID, nil)
	otherWorkspaceRequest.Header.Set("X-API-Key", "unmapped")
	otherWorkspaceRecorder := httptest.NewRecorder()
	router.ServeHTTP(otherWorkspaceRecorder, otherWorkspaceRequest)
	require.Equal(t, http.StatusUnauthorized, otherWorkspaceRecorder.Code)
}

func TestEveryRegisteredTeamProductResourceRouteRequiresTrustedIdentity(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team-product-route-auth.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled: true,
	})
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	type registeredRoute struct {
		method string
		path   string
	}
	routes := make([]registeredRoute, 0, 8)
	resourceIDPattern := regexp.MustCompile(`\{([^}:]+)(?::[^}]*)?\}`)
	err = router.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		template, templateErr := route.GetPathTemplate()
		if templateErr != nil ||
			(!strings.HasPrefix(template, "/api/runtime/team-definitions") &&
				!strings.HasPrefix(template, "/api/runtime/team-runs")) {
			return nil
		}
		methods, methodsErr := route.GetMethods()
		if methodsErr != nil {
			return methodsErr
		}
		for _, method := range methods {
			path := resourceIDPattern.ReplaceAllStringFunc(template, func(match string) string {
				return "foreign-" + strings.TrimSuffix(strings.Split(strings.Trim(match, "{}"), ":")[0], "?")
			})
			routes = append(routes, registeredRoute{method: method, path: path})
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, routes, "registered Definition/Run resource routes must be discovered")

	for _, route := range routes {
		t.Run(route.method+" "+strings.TrimPrefix(route.path, "/api/runtime"), func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
			request.RemoteAddr = "203.0.113.8:4321"
			request.Header.Set("Content-Type", "application/json")
			match := &mux.RouteMatch{}
			require.True(t, router.Match(request, match), "test route must be registered")

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
			require.Contains(t, recorder.Body.String(), "TEAM_PRODUCT_UNAUTHENTICATED")
			require.NotContains(t, recorder.Body.String(), "foreign-")
		})
	}
}

func TestTeamProductRunStartAPIProjectsAndReplaysWithTrustedScope(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team-run-start-api.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(store)
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled: true,
		APIKeyScopes: map[string]UsageScope{
			"alice":                 {UserID: "alice", WorkspaceID: "workspace-a"},
			"alice-other-workspace": {UserID: "alice", WorkspaceID: "workspace-b"},
			"bob":                   {UserID: "bob", WorkspaceID: "workspace-a"},
		},
	})
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	draftRequest := httptest.NewRequest(http.MethodPost, "/api/runtime/team-runs", bytes.NewBufferString(`{
		"goal":"run through the API",
		"config":{
			"members":[{"id":"worker","session_id":"worker-session","state":"idle"}],
			"tasks":[{"id":"task-1","title":"Work","goal":"do the work","assignee":"worker"}]
		}
	}`))
	draftRequest.Header.Set("X-API-Key", "alice")
	draftRequest.Header.Set("Idempotency-Key", "api-draft-1")
	draftRequest.Header.Set("Content-Type", "application/json")
	draftRecorder := httptest.NewRecorder()
	router.ServeHTTP(draftRecorder, draftRequest)
	require.Equal(t, http.StatusCreated, draftRecorder.Code, draftRecorder.Body.String())
	var draftPayload struct {
		Run team.TeamRun `json:"run"`
	}
	require.NoError(t, json.Unmarshal(draftRecorder.Body.Bytes(), &draftPayload))

	preflightRequest := httptest.NewRequest(http.MethodGet,
		"/api/runtime/team-runs/"+draftPayload.Run.ID+"/preflight", nil)
	preflightRequest.Header.Set("X-API-Key", "alice")
	preflightRecorder := httptest.NewRecorder()
	router.ServeHTTP(preflightRecorder, preflightRequest)
	require.Equal(t, http.StatusOK, preflightRecorder.Code, preflightRecorder.Body.String())
	var preflightPayload struct {
		Run          team.TeamRun                `json:"run"`
		Preflight    team.TeamRunPreflightReport `json:"preflight"`
		Readiness    bool                        `json:"readiness"`
		Capabilities map[string]string           `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(preflightRecorder.Body.Bytes(), &preflightPayload))
	require.True(t, preflightPayload.Readiness)
	require.True(t, preflightPayload.Preflight.Ready)
	require.Equal(t, "unsupported", preflightPayload.Capabilities["worker_session_liveness"])
	require.Equal(t, "unsupported", preflightPayload.Capabilities["budget_hard_stop"])
	require.Equal(t, "unsupported", preflightPayload.Capabilities["event_stream"])
	require.Empty(t, preflightPayload.Preflight.LegacyTeamID)
	require.Empty(t, preflightPayload.Run.LegacyTeamID)
	require.Equal(t, team.PreflightRouteCapabilityDisabled, preflightPayload.Preflight.RoutePreviewCapability)

	doStart := func(key, apiKey, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/runtime/team-runs/"+draftPayload.Run.ID+"/start", bytes.NewBufferString(body))
		request.Header.Set("X-API-Key", apiKey)
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	startedRecorder := doStart("api-start-1", "alice", `{"expected_version":1}`)
	require.Equal(t, http.StatusOK, startedRecorder.Code, startedRecorder.Body.String())
	var startedPayload struct {
		Run          team.TeamRun      `json:"run"`
		Capabilities map[string]string `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(startedRecorder.Body.Bytes(), &startedPayload))
	require.Equal(t, team.TeamRunStatusActive, startedPayload.Run.Status)
	require.Equal(t, team.TeamRunPhaseRunning, startedPayload.Run.Phase)
	require.NotEmpty(t, startedPayload.Run.LegacyTeamID)
	require.Equal(t, "supported", startedPayload.Capabilities["run_start"])

	replayRecorder := doStart("api-start-1", "alice", `{"expected_version":1}`)
	require.Equal(t, http.StatusOK, replayRecorder.Code, replayRecorder.Body.String())
	require.Contains(t, replayRecorder.Body.String(), startedPayload.Run.LegacyTeamID)

	foreignRecorder := doStart("foreign-start", "bob", `{"expected_version":1}`)
	require.Equal(t, http.StatusNotFound, foreignRecorder.Code)

	conflictRecorder := doStart("different-start", "alice", `{"expected_version":2}`)
	require.Equal(t, http.StatusConflict, conflictRecorder.Code, conflictRecorder.Body.String())
	require.Contains(t, conflictRecorder.Body.String(), "TEAM_RUN_INVALID_TRANSITION")
}

func TestTeamProductRunStartCapabilityDowngradesWithoutStarter(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetTeamStore(teamProductNonStarterStore{})
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled: true,
		APIKeyScopes: map[string]UsageScope{
			"alice": {UserID: "alice", WorkspaceID: "workspace-a"},
		},
	})
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	listRequest := httptest.NewRequest(http.MethodGet, "/api/runtime/team-runs", nil)
	listRequest.Header.Set("X-API-Key", "alice")
	listRecorder := httptest.NewRecorder()
	router.ServeHTTP(listRecorder, listRequest)
	require.Equal(t, http.StatusOK, listRecorder.Code, listRecorder.Body.String())
	var listPayload struct {
		Capabilities map[string]string `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(listRecorder.Body.Bytes(), &listPayload))
	require.Equal(t, "supported", listPayload.Capabilities["run_draft_create"])
	require.Equal(t, "unsupported", listPayload.Capabilities["run_start"])
	require.Equal(t, "unsupported", listPayload.Capabilities["run_preflight"])
	require.Equal(t, "unsupported", listPayload.Capabilities["run_route_preview"])
	require.Equal(t, "unsupported", listPayload.Capabilities["run_history"])
	require.Equal(t, "unsupported", listPayload.Capabilities["worker_session_liveness"])
	require.Equal(t, "unsupported", listPayload.Capabilities["budget_hard_stop"])
	require.Equal(t, "unsupported", listPayload.Capabilities["event_stream"])
	require.Equal(t, "unsupported", listPayload.Capabilities["artifact_retention"])
	require.Equal(t, "unsupported", listPayload.Capabilities["attempt_lineage"])

	startRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/runtime/team-runs/trun_unavailable/start",
		strings.NewReader(`{"expected_version":1}`),
	)
	startRequest.Header.Set("X-API-Key", "alice")
	startRequest.Header.Set("Idempotency-Key", "start-1")
	startRecorder := httptest.NewRecorder()
	router.ServeHTTP(startRecorder, startRequest)
	require.Equal(t, http.StatusServiceUnavailable, startRecorder.Code, startRecorder.Body.String())
	require.Contains(t, startRecorder.Body.String(), "TEAM_RUN_START_UNAVAILABLE")

	preflightRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/runtime/team-runs/trun_unavailable/preflight",
		nil,
	)
	preflightRequest.Header.Set("X-API-Key", "alice")
	preflightRecorder := httptest.NewRecorder()
	router.ServeHTTP(preflightRecorder, preflightRequest)
	require.Equal(t, http.StatusServiceUnavailable, preflightRecorder.Code, preflightRecorder.Body.String())
	require.Contains(t, preflightRecorder.Body.String(), "TEAM_RUN_PREFLIGHT_UNAVAILABLE")
}

type teamProductNonStarterStore struct {
	team.Store
	team.TeamDefinitionRunStore
}

func (teamProductNonStarterStore) ListTeamDefinitions(
	context.Context,
	string,
	string,
	int,
) ([]team.TeamDefinition, error) {
	return []team.TeamDefinition{}, nil
}

func (teamProductNonStarterStore) ListTeamRuns(
	context.Context,
	string,
	string,
	int,
) ([]team.TeamRun, error) {
	return []team.TeamRun{}, nil
}
