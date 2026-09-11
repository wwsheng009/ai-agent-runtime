package skills

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspaceregistry"
)

func newSessionBindingTestHandler(t *testing.T) (*Handler, *mux.Router, *workspaceregistry.Store) {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	registry := workspaceregistry.LoadFrom(filepath.Join(t.TempDir(), "workspace_directories.yaml"))
	handler.workspaceDirectories = registry
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	router.HandleFunc("/sessions", handler.ListSessions).Methods(http.MethodGet)
	router.HandleFunc("/sessions", handler.CreateSession).Methods(http.MethodPost)
	return handler, router, registry
}

func createSessionViaAPI(t *testing.T, router *mux.Router, body map[string]string) (int, map[string]interface{}) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return rec.Code, payload
}

func sessionContextString(t *testing.T, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	session, ok := payload["session"].(map[string]interface{})
	require.True(t, ok)
	metadata, ok := session["metadata"].(map[string]interface{})
	require.True(t, ok)
	context, ok := metadata["context"].(map[string]interface{})
	require.True(t, ok)
	return context
}

func TestCreateSessionWithoutBindingKeepsLegacyShape(t *testing.T) {
	_, router, _ := newSessionBindingTestHandler(t)

	code, payload := createSessionViaAPI(t, router, map[string]string{"title": "plain"})
	require.Equal(t, http.StatusCreated, code)
	context := sessionContextString(t, payload)
	_, bound := context[sessionmeta.WorkspacePath]
	require.False(t, bound, "legacy create must not bind a workspace path")
}

func TestCreateSessionBindsWorkspacePath(t *testing.T) {
	_, router, _ := newSessionBindingTestHandler(t)
	dir := t.TempDir()

	code, payload := createSessionViaAPI(t, router, map[string]string{
		"title":          "bound",
		"workspace_path": dir + string(filepath.Separator) + ".",
	})
	require.Equal(t, http.StatusCreated, code)
	context := sessionContextString(t, payload)
	require.Equal(t, filepath.Clean(dir), context[sessionmeta.WorkspacePath])

	// GET /sessions exposes the binding for directory grouping.
	req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var listPayload struct {
		Sessions []chat.Session `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listPayload))
	require.Len(t, listPayload.Sessions, 1)
	bound, ok := listPayload.Sessions[0].Metadata.Context[sessionmeta.WorkspacePath].(string)
	require.True(t, ok)
	require.Equal(t, filepath.Clean(dir), bound)
}

func TestCreateSessionBindsDirectoryIDAndTouchesRegistry(t *testing.T) {
	handler, router, registry := newSessionBindingTestHandler(t)
	dir := t.TempDir()
	record, _, err := registry.Add(dir, "demo")
	require.NoError(t, err)

	code, payload := createSessionViaAPI(t, router, map[string]string{
		"directory_id": record.ID,
	})
	require.Equal(t, http.StatusCreated, code)
	context := sessionContextString(t, payload)
	require.Equal(t, filepath.Clean(dir), context[sessionmeta.WorkspacePath])

	fresh := workspaceregistry.LoadFrom(registry.Path())
	touched, ok := fresh.Get(record.ID)
	require.True(t, ok)
	require.Greater(t, touched.LastUsedAt, int64(0), "directory_id binding must refresh last_used_at")
	require.NotNil(t, handler)
}

func TestCreateSessionBindingValidation(t *testing.T) {
	_, router, registry := newSessionBindingTestHandler(t)

	// Unknown directory_id → 404
	code, _ := createSessionViaAPI(t, router, map[string]string{"directory_id": "nope-id"})
	require.Equal(t, http.StatusNotFound, code)

	// Missing directory path → 400
	code, _ = createSessionViaAPI(t, router, map[string]string{
		"workspace_path": filepath.Join(t.TempDir(), "missing"),
	})
	require.Equal(t, http.StatusBadRequest, code)

	// directory_id wins over an invalid workspace_path (id is resolved first).
	dir := t.TempDir()
	record, _, err := registry.Add(dir, "")
	require.NoError(t, err)
	code, payload := createSessionViaAPI(t, router, map[string]string{
		"directory_id":   record.ID,
		"workspace_path": filepath.Join(t.TempDir(), "ignored-missing"),
	})
	require.Equal(t, http.StatusCreated, code)
	require.Equal(t, filepath.Clean(dir), sessionContextString(t, payload)[sessionmeta.WorkspacePath])
}

func TestAgentChatWorkspacePathFallback(t *testing.T) {
	bound := filepath.Clean(t.TempDir())

	// Explicit request path wins over the bound value.
	session := chat.NewSession("user-fallback")
	session.SetContext(sessionmeta.WorkspacePath, bound)
	got := agentChatEffectiveWorkspacePath(session, "E:\\other\\dir")
	require.Equal(t, "E:\\other\\dir", got)

	// No explicit path → fall back to the bound directory.
	got = agentChatEffectiveWorkspacePath(session, "   ")
	require.Equal(t, bound, got)

	// Bound path is never overwritten (no drift).
	require.False(t, sessionNeedsWorkspacePathMaterialization(session, "E:\\other\\dir"))

	// Legacy GetOrCreate flow: empty context + explicit request → materialize once.
	legacy := chat.NewSession("user-fallback")
	require.True(t, sessionNeedsWorkspacePathMaterialization(legacy, bound))
	require.False(t, sessionNeedsWorkspacePathMaterialization(legacy, ""))
	require.False(t, sessionNeedsWorkspacePathMaterialization(nil, bound))

	// Unbound session without explicit path keeps legacy empty behavior.
	got = agentChatEffectiveWorkspacePath(legacy, "")
	require.Equal(t, "", got)
	got = agentChatEffectiveWorkspacePath(nil, "")
	require.Equal(t, "", got)
}
