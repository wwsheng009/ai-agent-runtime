package runtimeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestAgentDefinitionsEndpointUsesSharedRendering(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/api/runtime/agent-definitions?action=list", want: "Agent definitions ("},
		{path: "/api/runtime/agent-definitions?action=show&name=explore", want: "Agent definition: explore"},
		{path: "/api/runtime/agent-definitions?action=lint", want: "Agent definitions lint:"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, tc.path)
		var payload map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		text, _ := payload["text"].(string)
		require.Contains(t, text, tc.want)
	}
}

func TestExecutionSupervisorSnapshotStaysReadOnlyWithoutControlPlane(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/supervision/watchdog", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Available bool                   `json:"available"`
		Built     bool                   `json:"built"`
		Stats     map[string]interface{} `json:"stats"`
		Text      string                 `json:"text"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.False(t, payload.Available)
	require.False(t, payload.Built)
	require.Contains(t, payload.Text, "执行看门狗（P0-4）：未接线")
	require.Nil(t, handler.peekExecutionSupervisor(), "debug snapshot must not build the watchdog")
}

func TestSessionLeaseStoreUsesIndependentSharedPath(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	cfg := runtimecfg.DefaultRuntimeConfig()
	sessionDir := t.TempDir()
	cfg.Sessions.Dir = sessionDir
	handler.SetRuntimeConfig(cfg, "")
	t.Cleanup(func() { _ = handler.CloseSessionLeaseStore() })

	leaseStore := handler.getSessionLeaseStore()
	require.NotNil(t, leaseStore)
	pathProvider, ok := leaseStore.(interface{ Path() string })
	require.True(t, ok, "shared lease store must be file-backed")
	expected := filepath.Join(sessionDir, "runtime", "session_leases.sqlite")
	require.Equal(t, filepath.Clean(expected), filepath.Clean(pathProvider.Path()))

	runtimeStore := handler.getSessionRuntimeStore()
	require.False(t, any(runtimeStore) == any(leaseStore), "lease storage must not alias the runtime store")
}

func TestAgentControlCleanupDryRunUsesRegistryReclaimPolicy(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent-registry.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	handler.SetAgentControlAgentStore(store)
	t.Cleanup(func() {
		if hub := handler.getSessionHub(); hub != nil {
			hub.StopAll()
		}
	})

	_, err = store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:       "root-agent",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
	})
	require.NoError(t, err)
	childSession := chat.NewSession("user-cleanup")
	childSession.ID = "idle-child-session"
	childSession.SetContext(toolbroker.AgentSessionContextParentSessionID, "root-session")
	childSession.SetContext(toolbroker.AgentSessionContextRootSessionID, "root-session")
	childSession.SetContext(toolbroker.AgentSessionContextPath, "/root/idle-child")
	childSession.SetContext(toolbroker.AgentSessionContextDepth, 1)
	childSession.SetContext(toolbroker.AgentSessionContextAgentType, "worker")
	childSession.UpdatedAt = time.Now().UTC().Add(-time.Hour)
	childSession.PreserveUpdatedAt = true
	require.NoError(t, sessionManager.GetStorage().Save(ctx, childSession))

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	body, _ := json.Marshal(map[string]interface{}{
		"root_session_id": "root-session",
		"dry_run":         true,
		"idle_ms":         1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/agent-control/cleanup", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "ok", payload["reclaim"])
	require.Equal(t, true, payload["dry_run"])
	text, _ := payload["text"].(string)
	require.Contains(t, text, "dry_run=true")
	require.Contains(t, text, "root_session=root-session")
	require.Contains(t, text, "would_reclaim")
	require.True(t, strings.Contains(text, "/root/idle-child"), text)
}
