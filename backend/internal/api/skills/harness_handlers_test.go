package skills

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func newHarnessTestRouter(t *testing.T) (*Handler, *mux.Router) {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return handler, router
}

func harnessWorkspaceQuery(workspace string) string {
	return "?workspace_path=" + url.QueryEscape(workspace)
}

func TestHarnessPermissionsAndGrants(t *testing.T) {
	workspace := t.TempDir()
	aicliDir := filepath.Join(workspace, ".aicli")
	require.NoError(t, os.MkdirAll(aicliDir, 0o755))
	permPath := filepath.Join(aicliDir, "permissions.yaml")
	require.NoError(t, os.WriteFile(permPath, []byte(`
version: 1
deny_tools: [shell]
allow_tools: [view]
rules:
  - name: ask-writes
    tools: [write, edit]
    decision: ask
    reason: review_writes
`), 0o644))

	_, router := newHarnessTestRouter(t)

	// permissions
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/harness/permissions"+harnessWorkspaceQuery(workspace), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var perms harnessPermissionsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&perms))
	require.True(t, perms.Exists)
	require.Contains(t, perms.DenyTools, "shell")
	require.Contains(t, perms.AllowTools, "view")
	require.NotEmpty(t, perms.Rules)
	require.Equal(t, workspace, perms.WorkspacePath)

	// remember grant
	body := `{"action":"remember","tool":"write","pattern":"README.md","scope":"project"}`
	postReq := httptest.NewRequest(http.MethodPost, "/api/runtime/harness/grants"+harnessWorkspaceQuery(workspace), strings.NewReader(body))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	router.ServeHTTP(postRec, postReq)
	require.Equal(t, http.StatusOK, postRec.Code, postRec.Body.String())

	var grants harnessGrantsResponse
	require.NoError(t, json.NewDecoder(postRec.Body).Decode(&grants))
	require.Equal(t, "remember", grants.Action)
	require.Equal(t, 1, grants.Count)
	require.Equal(t, "write", grants.Grants[0].Tool)
	require.Equal(t, "README.md", grants.Grants[0].Pattern)

	// list grants
	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/harness/grants"+harnessWorkspaceQuery(workspace), nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code)
	var listed harnessGrantsResponse
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listed))
	require.Equal(t, 1, listed.Count)

	// reject dangerous
	dangerReq := httptest.NewRequest(http.MethodPost, "/api/runtime/harness/grants"+harnessWorkspaceQuery(workspace), strings.NewReader(`{"action":"remember","tool":"shell"}`))
	dangerReq.Header.Set("Content-Type", "application/json")
	dangerRec := httptest.NewRecorder()
	router.ServeHTTP(dangerRec, dangerReq)
	require.Equal(t, http.StatusBadRequest, dangerRec.Code)

	// revoke
	revokeReq := httptest.NewRequest(http.MethodPost, "/api/runtime/harness/grants"+harnessWorkspaceQuery(workspace), strings.NewReader(`{"action":"revoke","tool":"write","pattern":"README.md"}`))
	revokeReq.Header.Set("Content-Type", "application/json")
	revokeRec := httptest.NewRecorder()
	router.ServeHTTP(revokeRec, revokeReq)
	require.Equal(t, http.StatusOK, revokeRec.Code, revokeRec.Body.String())
	var revoked harnessGrantsResponse
	require.NoError(t, json.NewDecoder(revokeRec.Body).Decode(&revoked))
	require.Equal(t, "revoke", revoked.Action)
	require.Equal(t, 1, revoked.Removed)
	require.Equal(t, 0, revoked.Count)
}

func TestHarnessMemoryListSearchAppend(t *testing.T) {
	workspace := t.TempDir()
	_, router := newHarnessTestRouter(t)

	appendBody := `{"text":"prefer apply_patch for multi-hunk edits","tags":["workflow"],"source":"settings"}`
	appendReq := httptest.NewRequest(http.MethodPost, "/api/runtime/harness/memory"+harnessWorkspaceQuery(workspace), strings.NewReader(appendBody))
	appendReq.Header.Set("Content-Type", "application/json")
	appendRec := httptest.NewRecorder()
	router.ServeHTTP(appendRec, appendReq)
	require.Equal(t, http.StatusOK, appendRec.Code, appendRec.Body.String())

	var appended harnessMemoryResponse
	require.NoError(t, json.NewDecoder(appendRec.Body).Decode(&appended))
	require.Equal(t, "append", appended.Action)
	require.NotNil(t, appended.Note)
	require.Contains(t, appended.Note.Text, "apply_patch")

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/harness/memory"+harnessWorkspaceQuery(workspace), nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	var listed harnessMemoryResponse
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listed))
	require.Equal(t, 1, listed.Count)
	require.Len(t, listed.Notes, 1)

	searchReq := httptest.NewRequest(http.MethodGet, "/api/runtime/harness/memory"+harnessWorkspaceQuery(workspace)+"&q=apply_patch", nil)
	searchRec := httptest.NewRecorder()
	router.ServeHTTP(searchRec, searchReq)
	require.Equal(t, http.StatusOK, searchRec.Code, searchRec.Body.String())
	var searched harnessMemoryResponse
	require.NoError(t, json.NewDecoder(searchRec.Body).Decode(&searched))
	require.Equal(t, 1, searched.Count)
	require.Len(t, searched.Hits, 1)
	require.Contains(t, searched.Hits[0].Text, "apply_patch")
}

func TestHarnessPluginsTrustEnable(t *testing.T) {
	workspace := t.TempDir()
	pluginRoot := filepath.Join(workspace, ".aicli", "plugins", "demo-plugin")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginRoot, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "plugin.yaml"), []byte(`
name: demo-plugin
version: 0.1.0
description: harness test plugin
skills_dir: skills
`), 0o644))

	home := t.TempDir()
	t.Setenv("AICLI_HOME", home)

	_, router := newHarnessTestRouter(t)

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/harness/plugins"+harnessWorkspaceQuery(workspace), nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())

	var listed harnessPluginsResponse
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listed))
	require.GreaterOrEqual(t, listed.Count, 1)
	found := false
	for _, plugin := range listed.Plugins {
		if plugin.ID == "demo-plugin" {
			found = true
			require.False(t, plugin.Active)
			require.Equal(t, "untrusted", plugin.Trust)
		}
	}
	require.True(t, found, "expected demo-plugin in catalog: %#v", listed.Plugins)

	trustReq := httptest.NewRequest(http.MethodPost, "/api/runtime/harness/plugins/demo-plugin"+harnessWorkspaceQuery(workspace), strings.NewReader(`{"action":"trust"}`))
	trustReq.Header.Set("Content-Type", "application/json")
	trustRec := httptest.NewRecorder()
	router.ServeHTTP(trustRec, trustReq)
	require.Equal(t, http.StatusOK, trustRec.Code, trustRec.Body.String())

	var trusted harnessPluginsResponse
	require.NoError(t, json.NewDecoder(trustRec.Body).Decode(&trusted))
	require.Equal(t, "trust", trusted.Action)
	require.NotNil(t, trusted.Plugin)
	require.Equal(t, "trusted", trusted.Plugin.Trust)
	require.True(t, trusted.Plugin.Active)

	disableReq := httptest.NewRequest(http.MethodPost, "/api/runtime/harness/plugins/demo-plugin"+harnessWorkspaceQuery(workspace), strings.NewReader(`{"action":"disable"}`))
	disableReq.Header.Set("Content-Type", "application/json")
	disableRec := httptest.NewRecorder()
	router.ServeHTTP(disableRec, disableReq)
	require.Equal(t, http.StatusOK, disableRec.Code, disableRec.Body.String())
	var disabled harnessPluginsResponse
	require.NoError(t, json.NewDecoder(disableRec.Body).Decode(&disabled))
	require.NotNil(t, disabled.Plugin)
	require.False(t, disabled.Plugin.Enabled)
	require.False(t, disabled.Plugin.Active)
}
