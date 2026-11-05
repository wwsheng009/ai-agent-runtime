package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestProbeShellSettingsUsesFullDraftTransport(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	current := runtimeexecutor.ShellConfig{
		Prefer:         []string{runtimeexecutor.ShellIDPowerShell},
		ScriptDelivery: runtimeexecutor.ScriptDeliveryArgv,
	}
	handler.SetShellSettingsProvider(func() (runtimeexecutor.ShellConfig, string, error) {
		return current, "runtime.yaml", nil
	})

	var probed runtimeexecutor.ShellConfig
	handler.SetShellSettingsProber(func(cfg runtimeexecutor.ShellConfig) (*runtimeexecutor.ShellResolution, error) {
		probed = cfg
		return &runtimeexecutor.ShellResolution{}, nil
	})

	body := `{
		"auto_detect": false,
		"prefer": ["git-bash"],
		"path": " C:/Tools/bash.exe ",
		"probe_timeout": "5s",
		"strict_probe": false,
		"script_delivery": "stdin",
		"env": {"LANG": "C.UTF-8"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/config/shell/probe", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ProbeShellSettings(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, probed.ShellAutoDetect())
	require.Equal(t, []string{"git-bash"}, probed.Prefer)
	require.Equal(t, "C:/Tools/bash.exe", probed.Path)
	require.Equal(t, "5s", probed.EffectiveProbeTimeout().String())
	require.False(t, probed.ShellStrictProbe())
	require.Equal(t, runtimeexecutor.ScriptDeliveryStdin, probed.EffectiveScriptDelivery())
	require.Equal(t, "C.UTF-8", probed.Env["LANG"])
}

func TestProbeShellSettingsRejectsMalformedDraft(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetShellSettingsProvider(func() (runtimeexecutor.ShellConfig, string, error) {
		return runtimeexecutor.ShellConfig{}, "runtime.yaml", nil
	})
	called := false
	handler.SetShellSettingsProber(func(runtimeexecutor.ShellConfig) (*runtimeexecutor.ShellResolution, error) {
		called = true
		return &runtimeexecutor.ShellResolution{}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/config/shell/probe", strings.NewReader(`{"prefer":`))
	rec := httptest.NewRecorder()
	handler.ProbeShellSettings(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.False(t, called)
}

func TestProbeShellSettingsRejectsInvalidDraftValues(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetShellSettingsProvider(func() (runtimeexecutor.ShellConfig, string, error) {
		return runtimeexecutor.ShellConfig{}, "runtime.yaml", nil
	})
	called := false
	handler.SetShellSettingsProber(func(runtimeexecutor.ShellConfig) (*runtimeexecutor.ShellResolution, error) {
		called = true
		return &runtimeexecutor.ShellResolution{}, nil
	})

	for _, body := range []string{
		`{"probe_timeout": "not-a-duration"}`,
		`{"prefer": ["not-a-shell"]}`,
		`{"script_delivery": "pipe"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/config/shell/probe", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler.ProbeShellSettings(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, body+": "+rec.Body.String())
	}
	require.False(t, called)
}

func TestShellSettingsRoutesRegistered(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetShellSettingsProvider(func() (runtimeexecutor.ShellConfig, string, error) {
		return runtimeexecutor.ShellConfig{Prefer: []string{runtimeexecutor.ShellIDCmd}}, "runtime.yaml", nil
	})
	handler.SetShellSettingsProber(func(runtimeexecutor.ShellConfig) (*runtimeexecutor.ShellResolution, error) {
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

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/config/shell", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "runtime.yaml", payload["config_path"])
}
