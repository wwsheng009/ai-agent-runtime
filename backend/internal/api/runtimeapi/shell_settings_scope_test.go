package runtimeapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestShellSettingsWorkspaceScopeRoutes(t *testing.T) {
	workspace := t.TempDir()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)

	var readScope, writeScope ShellSettingsScope
	handler.SetShellSettingsScopeProvider(func(scope ShellSettingsScope) (runtimeexecutor.ShellConfig, string, error) {
		readScope = scope
		return runtimeexecutor.ShellConfig{Prefer: []string{runtimeexecutor.ShellIDCmd}}, workspace + "/.aicli/runtime.yaml", nil
	})
	handler.SetShellSettingsScopePersister(func(next runtimeexecutor.ShellConfig, scope ShellSettingsScope) (string, error) {
		writeScope = scope
		return workspace + "/.aicli/runtime.yaml", nil
	})
	handler.SetShellSettingsProber(func(cfg runtimeexecutor.ShellConfig) (*runtimeexecutor.ShellResolution, error) {
		return &runtimeexecutor.ShellResolution{
			Shell: runtimeexecutor.Shell{
				ID:      runtimeexecutor.ShellIDCmd,
				Path:    "cmd.exe",
				Type:    runtimeexecutor.ShellTypeCmd,
				Dialect: runtimeexecutor.ShellDialectCmd,
			},
		}, nil
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	query := url.Values{"workspace_path": []string{workspace}, "scope": []string{"workspace"}}
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/api/runtime/config/shell?"+query.Encode(), nil))
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())
	require.Equal(t, "workspace", readScope.Scope)
	require.NotEmpty(t, readScope.WorkspacePath)

	body := []byte(`{"prefer":["cmd"],"workspace_path":` + strconv.Quote(workspace) + `,"scope":"workspace"}`)
	putRec := httptest.NewRecorder()
	router.ServeHTTP(putRec, httptest.NewRequest(http.MethodPut, "/api/runtime/config/shell", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, putRec.Code, putRec.Body.String())
	require.Equal(t, "workspace", writeScope.Scope)
	require.NotEmpty(t, writeScope.WorkspacePath)
}
