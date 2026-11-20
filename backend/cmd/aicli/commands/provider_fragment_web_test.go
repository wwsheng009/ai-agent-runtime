package commands

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

func TestWebConfigWritesProvidersToFragmentAndChatToMain(t *testing.T) {
	previousGlobal := agentconfig.GetGlobalConfig()
	t.Cleanup(func() { agentconfig.SetGlobalConfig(previousGlobal) })
	previousHome := agentconfig.UserHomeDirForTest()
	home := t.TempDir()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })
	t.Setenv(agentconfig.SystemPresetDirEnv, filepath.Join(t.TempDir(), "absent-system-presets"))
	t.Setenv(agentconfig.MergeConfigEnvVar, "on")
	t.Setenv(agentconfig.ProviderConfigEnvVar, "on")

	mainPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeCommandsTestFile(t, mainPath, "aicli:\n  chat:\n    stream: true\n")
	providerPath := filepath.Join(home, ".aicli", agentconfig.ProviderConfigFileName)
	writeCommandsTestFile(t, providerPath, `
providers:
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://alpha.example/v1
      default_model: gpt-4o
`)
	cfg, err := agentconfig.InitGlobalConfigLayered(mainPath, "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	session := newWebTestSession()
	session.RuntimeConfigPath = mainPath
	session.Config = cfg
	withWebTestSession(t, session)

	rec := postConfigJSON(t, HandleChatWebAPIConfigProviders, map[string]interface{}{
		"name":          "alpha",
		"default_model": "gpt-5",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("provider update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	providerRaw := readCommandsTestFile(t, providerPath)
	if !strings.Contains(providerRaw, "gpt-5") {
		t.Fatalf("provider update must land in providers.yaml:\n%s", providerRaw)
	}
	if strings.Contains(readCommandsTestFile(t, mainPath), "gpt-5") {
		t.Fatalf("provider update must not land in main config")
	}

	rec = postConfigJSON(t, HandleChatWebAPIConfigChat, map[string]interface{}{
		"default_model": "chat-model",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("chat update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(readCommandsTestFile(t, mainPath), "chat-model") {
		t.Fatalf("chat default must land in main config")
	}
	if strings.Contains(readCommandsTestFile(t, providerPath), "aicli:") {
		t.Fatalf("providers.yaml must never receive aicli section:\n%s", readCommandsTestFile(t, providerPath))
	}

	// Snapshot must expose the provider file/layer metadata used by the UI.
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIConfigPath, nil)
	rr := httptest.NewRecorder()
	HandleChatWebAPIConfig(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "providers_config_path") ||
		!strings.Contains(rr.Body.String(), "user-providers") {
		t.Fatalf("snapshot must expose provider layer metadata: %s", rr.Body.String())
	}

	rec = postConfigJSON(t, HandleChatWebAPIConfigProvidersEnabled, map[string]interface{}{
		"names":   []string{"alpha"},
		"enabled": false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(readCommandsTestFile(t, providerPath), "enabled: false") {
		t.Fatalf("enable toggle must write the provider fragment")
	}

	rec = postConfigJSON(t, HandleChatWebAPIConfigProvidersDelete, map[string]interface{}{
		"names": []string{"alpha"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(readCommandsTestFile(t, providerPath), "alpha") {
		t.Fatalf("delete must remove the provider from providers.yaml")
	}
}

func writeCommandsTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readCommandsTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
