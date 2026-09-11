package skills

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspaceregistry"
)

func newWorkspaceDirectoryTestHandler(t *testing.T) (*Handler, *mux.Router) {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.workspaceDirectories = workspaceregistry.LoadFrom(
		filepath.Join(t.TempDir(), "workspace_directories.yaml"))
	router := mux.NewRouter()
	handler.RegisterWorkspaceDirectoryRoutes(router)
	return handler, router
}

func serveWorkspaceDirectoryRequest(t *testing.T, router *mux.Router, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeWorkspaceDirectoryResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload
}

func TestWorkspaceDirectoryCreateListDelete(t *testing.T) {
	_, router := newWorkspaceDirectoryTestHandler(t)
	dir := t.TempDir()

	// Create → 201 + existing=false
	rec := serveWorkspaceDirectoryRequest(t, router, http.MethodPost, "/workspace-directories",
		map[string]string{"path": dir, "name": "demo"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	payload := decodeWorkspaceDirectoryResponse(t, rec)
	require.Equal(t, false, payload["existing"])
	directory, ok := payload["directory"].(map[string]interface{})
	require.True(t, ok)
	id, ok := directory["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, id)
	require.Equal(t, filepath.Clean(dir), directory["path"])

	// Duplicate add (case variants fold on Windows via registry keys) → 200 + existing
	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodPost, "/workspace-directories",
		map[string]string{"path": dir})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload = decodeWorkspaceDirectoryResponse(t, rec)
	require.Equal(t, true, payload["existing"])

	// List → one entry with exists=true
	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodGet, "/workspace-directories", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload = decodeWorkspaceDirectoryResponse(t, rec)
	require.Equal(t, float64(1), payload["count"])
	list, ok := payload["directories"].([]interface{})
	require.True(t, ok)
	require.Len(t, list, 1)
	entry := list[0].(map[string]interface{})
	require.Equal(t, id, entry["id"])
	require.Equal(t, true, entry["exists"])

	// Rename alias
	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodPatch, "/workspace-directories/"+id,
		map[string]string{"name": "renamed"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload = decodeWorkspaceDirectoryResponse(t, rec)
	renamed := payload["directory"].(map[string]interface{})
	require.Equal(t, "renamed", renamed["name"])
	require.Equal(t, filepath.Clean(dir), renamed["path"], "path must stay immutable")

	// Delete → 200, then 404 on repeat
	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodDelete, "/workspace-directories/"+id, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload = decodeWorkspaceDirectoryResponse(t, rec)
	require.Equal(t, true, payload["deleted"])
	require.Equal(t, id, payload["id"])

	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodDelete, "/workspace-directories/"+id, nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestWorkspaceDirectoryValidationErrors(t *testing.T) {
	_, router := newWorkspaceDirectoryTestHandler(t)

	// Missing directory → 400
	rec := serveWorkspaceDirectoryRequest(t, router, http.MethodPost, "/workspace-directories",
		map[string]string{"path": filepath.Join(t.TempDir(), "missing")})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// Relative path → 400
	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodPost, "/workspace-directories",
		map[string]string{"path": "relative/dir"})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// File instead of directory → 400
	file := filepath.Join(t.TempDir(), "plain.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodPost, "/workspace-directories",
		map[string]string{"path": file})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// PATCH unknown id → 404
	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodPatch, "/workspace-directories/nope",
		map[string]string{"name": "x"})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	// PATCH without name → 400
	dir := t.TempDir()
	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodPost, "/workspace-directories",
		map[string]string{"path": dir})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	id := decodeWorkspaceDirectoryResponse(t, rec)["directory"].(map[string]interface{})["id"].(string)
	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodPatch, "/workspace-directories/"+id,
		map[string]string{})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestWorkspaceDirectorySessionCount(t *testing.T) {
	handler, router := newWorkspaceDirectoryTestHandler(t)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)

	dir := t.TempDir()
	rec := serveWorkspaceDirectoryRequest(t, router, http.MethodPost, "/workspace-directories",
		map[string]string{"path": dir})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	id := decodeWorkspaceDirectoryResponse(t, rec)["directory"].(map[string]interface{})["id"].(string)

	// Two sessions bound to the directory (one via a case variant on Windows
	// path keys) and one unbound session.
	ctx := t.Context()
	for i := 0; i < 2; i++ {
		session, err := sessionManager.Create(ctx, "workspace-dirs-user")
		require.NoError(t, err)
		session.SetContext(sessionmeta.WorkspacePath, dir)
		require.NoError(t, sessionManager.Update(ctx, session))
	}
	unbound, err := sessionManager.Create(ctx, "workspace-dirs-user")
	require.NoError(t, err)
	require.NotNil(t, unbound)

	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodGet, "/workspace-directories", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	entries := decodeWorkspaceDirectoryResponse(t, rec)["directories"].([]interface{})
	require.Len(t, entries, 1)
	require.Equal(t, float64(2), entries[0].(map[string]interface{})["session_count"])

	// Delete reports the affected session count (informational only).
	rec = serveWorkspaceDirectoryRequest(t, router, http.MethodDelete, "/workspace-directories/"+id, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload := decodeWorkspaceDirectoryResponse(t, rec)
	require.Equal(t, true, payload["deleted"])
	require.Equal(t, float64(2), payload["sessions_affected"])
}
