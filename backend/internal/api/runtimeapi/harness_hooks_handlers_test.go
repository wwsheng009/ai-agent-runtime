package runtimeapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
)

func TestGetHarnessHooksListsProjectHooksWhenTrusted(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".aicli"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".aicli", "hooks.json"), []byte(`{
  "hooks": {
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "check.sh", "timeout": 5}]}
    ]
  }
}`), 0o600))

	t.Setenv(foldertrust.EnvFolderTrust, "1")
	_, err := foldertrust.GrantTrust(workspace)
	require.NoError(t, err)

	handler := &Handler{runtimeConfig: &runtimecfg.RuntimeConfig{Workspace: runtimecfg.WorkspaceConfig{Root: workspace}}}
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/harness/hooks", nil)
	rec := httptest.NewRecorder()
	handler.GetHarnessHooks(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var response harnessHooksResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, 1, response.Count)
	require.Equal(t, "user_prompt_submit", response.Hooks[0].Event)
	require.Equal(t, "sync", response.Hooks[0].ExecutionMode)
	require.True(t, response.Hooks[0].Enabled)
}

func TestUpdateHarnessHookWritesStateForDiscoveredFile(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".aicli"), 0o755))
	path := filepath.Join(workspace, ".aicli", "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo ok"}]}]}
}`), 0o600))

	t.Setenv(foldertrust.EnvFolderTrust, "1")
	_, err := foldertrust.GrantTrust(workspace)
	require.NoError(t, err)

	handler := &Handler{runtimeConfig: &runtimecfg.RuntimeConfig{Workspace: runtimecfg.WorkspaceConfig{Root: workspace}}}
	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime/harness/hooks?workspace_path="+workspace, nil)
	getRec := httptest.NewRecorder()
	handler.GetHarnessHooks(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())
	var listed harnessHooksResponse
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &listed))
	require.Len(t, listed.Hooks, 1)
	hookID := listed.Hooks[0].ID

	body, _ := json.Marshal(map[string]interface{}{
		"id":          hookID,
		"source_path": path,
		"enabled":     false,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/harness/hooks?workspace_path="+workspace, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.UpdateHarnessHook(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var response harnessHooksResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, 0, response.Count)

	states, err := runtimehooks.LoadHookStatesFile(path)
	require.NoError(t, err)
	require.NotNil(t, states[hookID].Enabled)
	require.False(t, *states[hookID].Enabled)
}

func TestUpdateHarnessHookRejectsUndiscoveredFile(t *testing.T) {
	workspace := t.TempDir()
	handler := &Handler{runtimeConfig: &runtimecfg.RuntimeConfig{Workspace: runtimecfg.WorkspaceConfig{Root: workspace}}}
	body, _ := json.Marshal(map[string]interface{}{
		"id":          "handler-1",
		"source_path": filepath.Join(workspace, "evil.json"),
		"enabled":     false,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/harness/hooks", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.UpdateHarnessHook(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}
