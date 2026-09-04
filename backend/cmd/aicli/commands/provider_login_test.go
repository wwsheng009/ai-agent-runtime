package commands

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

func setTestUserProfileDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
}

func testAuthStorePath(dir string) string {
	return filepath.Join(dir, ".aicli", "auth.json")
}

func seedTestAPIKeyAuth(t *testing.T, dir, ref, apiKey string) {
	t.Helper()
	setTestUserProfileDir(t, dir)
	if err := config.SaveProviderAuth(ref, config.ProviderAuthRecord{
		KeyType:  config.AuthKeyTypeAPIKey,
		AuthMode: config.AuthKeyTypeAPIKey,
		APIKey:   apiKey,
	}); err != nil {
		t.Fatalf("SaveProviderAuth: %v", err)
	}
}

func TestRunProviderLogin_CreatesProviderAfterModelsValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1-mini"},{"id":"gpt-5"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "alpha",
		LoginProtocol: "openai",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
		SetDefault:    true,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if !result.Created || result.ProviderName != "alpha" || result.DefaultModel != "gpt-4.1-mini" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if cfg.Providers.DefaultProvider != "alpha" {
		t.Fatalf("expected default provider alpha, got %q", cfg.Providers.DefaultProvider)
	}
	provider := cfg.Providers.Items["alpha"]
	if provider.Protocol != "openai" || provider.APIKey != "" || provider.APIKeyRef != "alpha" || len(provider.SupportedModels) != 2 {
		t.Fatalf("unexpected cfg provider: %+v", provider)
	}
	authStorePath := testAuthStorePath(dir)
	loadedAuth, err := config.LoadProviderAuthFromPath(authStorePath, "alpha")
	if err != nil {
		t.Fatalf("LoadProviderAuthFromPath: %v", err)
	}
	if loadedAuth.KeyType != config.AuthKeyTypeAPIKey || loadedAuth.AuthMode != config.AuthKeyTypeAPIKey || loadedAuth.APIKey != "sk-test" {
		t.Fatalf("unexpected auth record: %+v", loadedAuth)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"default_provider: alpha",
		"protocol: openai",
		"base_url: " + server.URL,
		"api_key_ref: alpha",
		"supported_models:",
		"default_model: gpt-4.1-mini",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "api_key:") {
		t.Fatalf("config should not persist inline api_key:\n%s", text)
	}
}

func TestRunProviderLogin_AddsDefaultCodexReasoningEfforts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.4-mini"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:            cfg,
		ProviderName:      "codex",
		LoginProtocol:     "codex-apikey",
		BaseURL:           server.URL + "/v1",
		APIKey:            "sk-test",
		DisableModelCards: true,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.DefaultModel != "gpt-5.4-mini" {
		t.Fatalf("unexpected result: %+v", result)
	}

	provider := cfg.Providers.Items["codex"]
	capability, ok := provider.ModelCapabilities["gpt-5.4-mini"]
	if !ok {
		t.Fatalf("expected model capability, got %+v", provider.ModelCapabilities)
	}
	if !capability.ReasoningModel || strings.Join(capability.ReasoningEfforts, ",") != "low,medium,high,xhigh,none" {
		t.Fatalf("unexpected capability: %+v", capability)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"model_capabilities:",
		"gpt-5.4-mini:",
		"reasoning_model: true",
		"- xhigh",
		"- none",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
}

func TestRunProviderLogin_AppliesBuiltinModelCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.4"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "codex",
		LoginProtocol: "codex-apikey",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if len(result.ModelCardsApplied) == 0 {
		t.Fatalf("expected model card applied, got %+v", result)
	}
	capability := cfg.Providers.Items["codex"].ModelCapabilities["gpt-5.4"]
	if capability.MaxContextTokens != 1050000 || capability.AutoCompactTokenLimit != 945000 || !capability.NativeTools.ImageGeneration {
		t.Fatalf("unexpected model-card capability: %+v", capability)
	}
	if strings.Join(capability.InputModalities, ",") != "text,image" {
		t.Fatalf("unexpected modalities: %+v", capability.InputModalities)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"gpt-5.4:",
		"max_context_tokens: 1050000",
		"auto_compact_token_limit: 945000",
		"image_generation: true",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
}

func TestRunProviderLogin_OpenAICompatibleCodexModelsUseCodexTemplate(t *testing.T) {
	codexModels := []string{
		"codex-auto-review",
		"gpt-5.2",
		"gpt-5.2-openai-compact",
		"gpt-5.3-codex",
		"gpt-5.3-codex-openai-compact",
		"gpt-5.4-nano",
		"gpt-5.4-openai-compact",
		"gpt-5.5",
		"gpt-5.5-openai-compact",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		parts := make([]string, 0, len(codexModels))
		for _, model := range codexModels {
			parts = append(parts, fmt.Sprintf(`{"id":%q}`, model))
		}
		_, _ = w.Write([]byte(`{"data":[` + strings.Join(parts, ",") + `]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "superapi",
		LoginProtocol: "openai",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.Protocol != "codex" || result.LoginProtocol != "codex-apikey" {
		t.Fatalf("expected codex provider selected from model metadata, got %+v", result)
	}
	if strings.Join(result.SupportedModels, ",") != strings.Join(codexModels, ",") {
		t.Fatalf("unexpected supported models: %+v", result.SupportedModels)
	}
	if len(result.ModelCardsApplied) != len(codexModels) {
		t.Fatalf("expected one codex model card per model, got %+v", result.ModelCardsApplied)
	}
	for _, applied := range result.ModelCardsApplied {
		if applied.CardID == "fallback.openai.chat" {
			t.Fatalf("codex-compatible model still used openai chat fallback: %+v", result.ModelCardsApplied)
		}
	}

	provider := cfg.Providers.Items["superapi"]
	if provider.Protocol != "codex" || provider.APIPath != "/v1/responses" || provider.ForwardURL != "/v1/responses" {
		t.Fatalf("expected codex.responses provider defaults, got %+v", provider)
	}
	expectedLimits := map[string]struct {
		maxContextTokens      int
		autoCompactTokenLimit int
	}{
		"codex-auto-review":            {400000, 360000},
		"gpt-5.2":                      {400000, 360000},
		"gpt-5.2-openai-compact":       {400000, 360000},
		"gpt-5.3-codex":                {400000, 360000},
		"gpt-5.3-codex-openai-compact": {400000, 360000},
		"gpt-5.4-nano":                 {400000, 360000},
		"gpt-5.4-openai-compact":       {1050000, 945000},
		"gpt-5.5":                      {1050000, 945000},
		"gpt-5.5-openai-compact":       {1050000, 945000},
		"gpt-5.6-sol":                  {1050000, 945000},
		"gpt-5.6-terra":                {1050000, 945000},
		"gpt-5.6-luna":                 {1050000, 945000},
	}
	for _, model := range codexModels {
		capability, ok := provider.ModelCapabilities[model]
		if !ok {
			t.Fatalf("missing capability for %s: %+v", model, provider.ModelCapabilities)
		}
		expected, ok := expectedLimits[model]
		if !ok {
			t.Fatalf("missing expected limits for %s", model)
		}
		if capability.MaxContextTokens != expected.maxContextTokens ||
			capability.AutoCompactTokenLimit != expected.autoCompactTokenLimit ||
			!capability.ReasoningModel {
			t.Fatalf("unexpected capability for %s: %+v", model, capability)
		}
	}
}

func TestRunProviderLogin_DisableModelCardsKeepsDiscoveryOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.4"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:            cfg,
		ProviderName:      "codex",
		LoginProtocol:     "codex-apikey",
		BaseURL:           server.URL,
		APIKey:            "sk-test",
		DisableModelCards: true,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if len(result.ModelCardsApplied) != 0 {
		t.Fatalf("expected no model cards, got %+v", result.ModelCardsApplied)
	}
	capability := cfg.Providers.Items["codex"].ModelCapabilities["gpt-5.4"]
	if capability.MaxContextTokens != 0 || capability.NativeTools.ImageGeneration {
		t.Fatalf("expected discovery-only capability, got %+v", capability)
	}
	if !capability.ReasoningModel {
		t.Fatalf("expected codex reasoning fallback, got %+v", capability)
	}
}

func TestRunProviderLogin_AppliesAnthropicModelCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-6"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "anthropic",
		LoginProtocol: "anthropic",
		BaseURL:       server.URL,
		APIKey:        "sk-ant-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if len(result.ModelCardsApplied) == 0 || result.ModelCardsApplied[0].CardID != "anthropic.claude-sonnet-4-6" {
		t.Fatalf("expected anthropic model card, got %+v", result.ModelCardsApplied)
	}
	capability := cfg.Providers.Items["anthropic"].ModelCapabilities["claude-sonnet-4-6"]
	if capability.MaxContextTokens != 1000000 || capability.MaxTokens != 128000 || !capability.ReasoningModel {
		t.Fatalf("unexpected anthropic capability: %+v", capability)
	}
	if strings.Join(capability.InputModalities, ",") != "text,image" {
		t.Fatalf("unexpected modalities: %+v", capability.InputModalities)
	}
	provider := cfg.Providers.Items["anthropic"]
	if provider.APIPath != "/v1/messages" || provider.ForwardURL != "/v1/messages" || provider.MaxTokensLimit != 131072 {
		t.Fatalf("unexpected provider template defaults: %+v", provider)
	}
	if strings.Join(provider.SupportTypes, ",") != "anthropic" {
		t.Fatalf("unexpected support types: %+v", provider.SupportTypes)
	}
}

func TestRunProviderLogin_ExplicitAnthropicKeepsAnthropicEndpointForDeepSeekModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}
	baseURL := server.URL + "/anthropic"

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "deepseek_anthropic",
		LoginProtocol: "anthropic",
		BaseURL:       baseURL,
		APIKey:        "sk-ant-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.Protocol != "anthropic" || result.LoginProtocol != "anthropic" {
		t.Fatalf("explicit anthropic login should not switch protocols, got %+v", result)
	}
	provider := cfg.Providers.Items["deepseek_anthropic"]
	if provider.Protocol != "anthropic" || provider.BaseURL != baseURL || provider.APIPath != "/v1/messages" || provider.ForwardURL != "/v1/messages" {
		t.Fatalf("expected anthropic provider endpoint fields, got %+v", provider)
	}
	if endpoint := buildProviderURL(provider, "/v1/messages", "deepseek-v4-flash"); endpoint != server.URL+"/anthropic/v1/messages" {
		t.Fatalf("unexpected anthropic endpoint: %s", endpoint)
	}
	if _, exists := cfg.Providers.Items["deepseek_anthropic_openai"]; exists {
		t.Fatalf("explicit anthropic login should not create an openai primary fallback: %+v", cfg.Providers.Items["deepseek_anthropic_openai"])
	}
}

func TestRunProviderLogin_GroupsModelsByProviderTemplate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1"},{"id":"claude-sonnet-4-6"},{"id":"gpt-image-2"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "mixed",
		LoginProtocol: "openai",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if strings.Join(result.SupportedModels, ",") != "gpt-4.1" || result.DefaultModel != "gpt-4.1" {
		t.Fatalf("unexpected supported models: %+v", result)
	}
	if len(result.ModelsSkippedByProtocol) != 0 {
		t.Fatalf("expected no skipped models after grouping, got %+v", result.ModelsSkippedByProtocol)
	}
	if len(result.ProviderConfigs) != 3 {
		t.Fatalf("expected three provider configs, got %+v", result.ProviderConfigs)
	}
	configs := map[string]providerLoginGeneratedProviderInfo{}
	for _, item := range result.ProviderConfigs {
		configs[item.ProviderName] = item
	}
	if strings.Join(configs["mixed"].SupportedModels, ",") != "gpt-4.1" ||
		strings.Join(configs["mixed_anthropic"].SupportedModels, ",") != "claude-sonnet-4-6" ||
		strings.Join(configs["mixed_openai_image"].SupportedModels, ",") != "gpt-image-2" {
		t.Fatalf("unexpected provider configs: %+v", result.ProviderConfigs)
	}
	appliedModels := map[string]bool{}
	for _, item := range result.ModelCardsApplied {
		appliedModels[item.Model] = true
	}
	if !appliedModels["gpt-4.1"] || !appliedModels["claude-sonnet-4-6"] || !appliedModels["gpt-image-2"] {
		t.Fatalf("expected model cards for all groups, got %+v", result.ModelCardsApplied)
	}
	provider := cfg.Providers.Items["mixed"]
	if provider.APIPath != "/v1/chat/completions" || provider.ForwardURL != "/v1/chat/completions" {
		t.Fatalf("unexpected openai provider template defaults: %+v", provider)
	}
	if strings.Join(provider.SupportedModels, ",") != "gpt-4.1" {
		t.Fatalf("unexpected persisted supported models: %+v", provider.SupportedModels)
	}
	if _, exists := provider.ModelCapabilities["claude-sonnet-4-6"]; exists {
		t.Fatalf("grouped model capability should not be persisted in primary provider: %+v", provider.ModelCapabilities)
	}
	anthropic := cfg.Providers.Items["mixed_anthropic"]
	if anthropic.Protocol != "anthropic" || anthropic.APIPath != "/v1/messages" || strings.Join(anthropic.SupportedModels, ",") != "claude-sonnet-4-6" {
		t.Fatalf("unexpected anthropic grouped provider: %+v", anthropic)
	}
	image := cfg.Providers.Items["mixed_openai_image"]
	if image.Protocol != "openai_image" || image.APIPath != "/v1/images/generations" || strings.Join(image.SupportedModels, ",") != "gpt-image-2" {
		t.Fatalf("unexpected image grouped provider: %+v", image)
	}
}

func TestRunProviderLogin_OpenAIImageProtocolUsesImageTemplate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-image" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-image-2"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "image_api",
		LoginProtocol: "openai_image",
		BaseURL:       server.URL,
		APIKey:        "sk-image",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.Protocol != "openai_image" || result.LoginProtocol != "openai_image" || result.DefaultModel != "gpt-image-2" {
		t.Fatalf("unexpected result: %+v", result)
	}
	provider := cfg.Providers.Items["image_api"]
	if provider.Protocol != "openai_image" || provider.APIPath != "/v1/images/generations" || provider.ForwardURL != "/v1/images/generations" {
		t.Fatalf("expected image provider template defaults, got %+v", provider)
	}
	if strings.Join(provider.SupportTypes, ",") != "openai_image" {
		t.Fatalf("unexpected support types: %+v", provider.SupportTypes)
	}
	capability := provider.ModelCapabilities["gpt-image-2"]
	if !capability.NativeTools.ImagesGenerationsAPI || strings.Join(capability.InputModalities, ",") != "text,image" {
		t.Fatalf("unexpected image capability: %+v", capability)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"protocol: openai_image",
		"api_path: /v1/images/generations",
		"forward_url: /v1/images/generations",
		"- openai_image",
		"images_generations_api: true",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
}

func TestRunProviderLogin_AutoGroupsModelsByMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1"},{"id":"claude-sonnet-4-6"},{"id":"gemini-2.5-pro"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "auto_mix",
		LoginProtocol: "auto",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.LoginProtocol != "openai" || result.Protocol != "openai" {
		t.Fatalf("auto should resolve to primary openai provider, got %+v", result)
	}
	if len(result.ProviderConfigs) != 3 {
		t.Fatalf("expected three provider configs, got %+v", result.ProviderConfigs)
	}
	providers := cfg.Providers.Items
	if strings.Join(providers["auto_mix"].SupportedModels, ",") != "gpt-4.1" {
		t.Fatalf("unexpected primary provider: %+v", providers["auto_mix"])
	}
	if providers["auto_mix_anthropic"].Protocol != "anthropic" || strings.Join(providers["auto_mix_anthropic"].SupportedModels, ",") != "claude-sonnet-4-6" {
		t.Fatalf("unexpected anthropic provider: %+v", providers["auto_mix_anthropic"])
	}
	if providers["auto_mix_gemini"].Protocol != "gemini" || strings.Join(providers["auto_mix_gemini"].SupportedModels, ",") != "gemini-2.5-pro" {
		t.Fatalf("unexpected gemini provider: %+v", providers["auto_mix_gemini"])
	}
}

func TestRunProviderLogin_AutoGroupsImageKeywordModelsToOpenAIImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1"},{"id":"custom-image-model"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "auto_image_mix",
		LoginProtocol: "auto",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.LoginProtocol != "openai" || result.Protocol != "openai" {
		t.Fatalf("auto should resolve to primary openai provider, got %+v", result)
	}
	if strings.Join(result.SupportedModels, ",") != "gpt-4.1" {
		t.Fatalf("unexpected primary supported models: %+v", result.SupportedModels)
	}
	if len(result.ProviderConfigs) != 2 {
		t.Fatalf("expected two provider configs, got %+v", result.ProviderConfigs)
	}

	providers := cfg.Providers.Items
	primary := providers["auto_image_mix"]
	if primary.Protocol != "openai" || primary.APIPath != "/v1/chat/completions" || strings.Join(primary.SupportedModels, ",") != "gpt-4.1" {
		t.Fatalf("unexpected primary provider: %+v", primary)
	}
	image, ok := providers["auto_image_mix_openai_image"]
	if !ok {
		t.Fatalf("expected openai_image grouped provider, got %+v", providers)
	}
	if image.Protocol != "openai_image" || image.APIPath != "/v1/images/generations" || image.ForwardURL != "/v1/images/generations" {
		t.Fatalf("unexpected image provider template defaults: %+v", image)
	}
	if strings.Join(image.SupportedModels, ",") != "custom-image-model" {
		t.Fatalf("unexpected image supported models: %+v", image.SupportedModels)
	}
	capability := image.ModelCapabilities["custom-image-model"]
	if !capability.NativeTools.ImagesGenerationsAPI {
		t.Fatalf("expected images generations capability, got %+v", capability)
	}
}

func TestRunProviderLogin_AutoImageKeywordDoesNotOverrideExplicitModelCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1"},{"id":"claude-image-4-6"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "auto_card_image",
		LoginProtocol: "auto",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.LoginProtocol != "openai" || strings.Join(result.SupportedModels, ",") != "gpt-4.1" {
		t.Fatalf("unexpected primary result: %+v", result)
	}

	providers := cfg.Providers.Items
	if _, exists := providers["auto_card_image_openai_image"]; exists {
		t.Fatalf("explicit model card should not be overridden by image keyword: %+v", providers["auto_card_image_openai_image"])
	}
	anthropic := providers["auto_card_image_anthropic"]
	if anthropic.Protocol != "anthropic" || anthropic.APIPath != "/v1/messages" || strings.Join(anthropic.SupportedModels, ",") != "claude-image-4-6" {
		t.Fatalf("unexpected anthropic grouped provider: %+v", anthropic)
	}
}

func TestRunProviderLogin_OpenAIProtocolDoesNotUseAutoImageKeywordHeuristic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1"},{"id":"custom-image-model"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "openai_image_name",
		LoginProtocol: "openai",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.LoginProtocol != "openai" || result.Protocol != "openai" {
		t.Fatalf("unexpected result protocol: %+v", result)
	}
	if strings.Join(result.SupportedModels, ",") != "gpt-4.1,custom-image-model" {
		t.Fatalf("unexpected supported models: %+v", result.SupportedModels)
	}
	provider := cfg.Providers.Items["openai_image_name"]
	if provider.Protocol != "openai" || provider.APIPath != "/v1/chat/completions" || strings.Join(provider.SupportedModels, ",") != "gpt-4.1,custom-image-model" {
		t.Fatalf("unexpected openai provider: %+v", provider)
	}
	if _, exists := cfg.Providers.Items["openai_image_name_openai_image"]; exists {
		t.Fatalf("explicit openai login should not create auto image group")
	}
}

func TestRunProviderLogin_AutoGroupsGrokAcrossCompatibleProtocols(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1"},{"id":"grok-4.5"},{"id":"claude-sonnet-4-6"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "auto_grok",
		LoginProtocol: "auto",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.LoginProtocol != "openai" || result.Protocol != "openai" {
		t.Fatalf("auto should resolve to primary openai provider, got %+v", result)
	}

	providers := cfg.Providers.Items
	primary := providers["auto_grok"]
	if primary.Protocol != "openai" {
		t.Fatalf("unexpected primary protocol: %+v", primary)
	}
	// grok-4.5 should appear in openai primary group together with gpt-4.1
	if !containsString(primary.SupportedModels, "gpt-4.1") || !containsString(primary.SupportedModels, "grok-4.5") {
		t.Fatalf("expected primary openai group to include gpt-4.1 and grok-4.5, got %+v", primary.SupportedModels)
	}

	anthropic, ok := providers["auto_grok_anthropic"]
	if !ok {
		t.Fatalf("expected anthropic grouped provider, got %+v", providers)
	}
	if anthropic.Protocol != "anthropic" || anthropic.APIPath != "/v1/messages" {
		t.Fatalf("unexpected anthropic provider: %+v", anthropic)
	}
	if !containsString(anthropic.SupportedModels, "grok-4.5") || !containsString(anthropic.SupportedModels, "claude-sonnet-4-6") {
		t.Fatalf("expected anthropic group to include grok-4.5 and claude-sonnet-4-6, got %+v", anthropic.SupportedModels)
	}

	codex, ok := providers["auto_grok_codex"]
	if !ok {
		t.Fatalf("expected codex grouped provider, got %+v", providers)
	}
	if codex.Protocol != "codex" || codex.APIPath != "/v1/responses" {
		t.Fatalf("unexpected codex provider: %+v", codex)
	}
	if strings.Join(codex.SupportedModels, ",") != "grok-4.5" {
		t.Fatalf("expected codex group to only include grok-4.5, got %+v", codex.SupportedModels)
	}

	// Ensure multi-group expansion created at least openai/anthropic/codex providers.
	if len(result.ProviderConfigs) < 3 {
		t.Fatalf("expected multi-protocol provider configs, got %+v", result.ProviderConfigs)
	}
}

func TestRunProviderLogin_ProtocolChangeReplacesKnownProviderTemplateDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-6"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://old.example.com
      api_path: /v1/chat/completions
      forward_url: /v1/chat/completions
      support_types:
        - openai
      max_tokens_limit: 10000
      default_model: gpt-4.1
      supported_models:
        - gpt-4.1
`)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{
		ConfigFilePath: path,
		Providers: config.ProvidersConfig{Items: map[string]config.Provider{
			"alpha": {
				Enabled:         true,
				Protocol:        "openai",
				BaseURL:         "https://old.example.com",
				APIPath:         "/v1/chat/completions",
				ForwardURL:      "/v1/chat/completions",
				SupportTypes:    []string{"openai"},
				MaxTokensLimit:  10000,
				DefaultModel:    "gpt-4.1",
				SupportedModels: []string{"gpt-4.1"},
			},
		}},
	}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "alpha",
		LoginProtocol: "anthropic",
		BaseURL:       server.URL,
		APIKey:        "sk-ant-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if !result.Updated || result.Protocol != "anthropic" || result.DefaultModel != "claude-sonnet-4-6" {
		t.Fatalf("unexpected result: %+v", result)
	}
	provider := cfg.Providers.Items["alpha"]
	if provider.Protocol != "anthropic" || provider.APIPath != "/v1/messages" || provider.ForwardURL != "/v1/messages" {
		t.Fatalf("expected anthropic endpoint defaults, got %+v", provider)
	}
	if strings.Join(provider.SupportTypes, ",") != "anthropic" || provider.MaxTokensLimit != 131072 {
		t.Fatalf("expected anthropic template defaults, got %+v", provider)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"protocol: anthropic",
		"api_path: /v1/messages",
		"forward_url: /v1/messages",
		"- anthropic",
		"max_tokens_limit: 131072",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
}

func TestApplyProviderLoginProviderTemplateDefaults_CodexChatGPTUsesResponsesPath(t *testing.T) {
	provider := config.Provider{
		Protocol: "codex",
		BaseURL:  "https://chatgpt.com/backend-api/codex",
	}
	applyProviderLoginProviderTemplateDefaults(&provider, modelcard.ProviderTemplate{
		ID:             "codex.responses",
		Protocol:       "codex",
		APIPath:        "/v1/responses",
		ForwardURL:     "/v1/responses",
		SupportTypes:   []string{"codex"},
		MaxTokensLimit: 10000,
	}, providerLoginTemplateDefaults{}, false)
	if provider.APIPath != "/responses" || provider.ForwardURL != "/responses" {
		t.Fatalf("expected ChatGPT Codex /responses path, got %+v", provider)
	}
	if provider.MaxTokensLimit != 10000 || strings.Join(provider.SupportTypes, ",") != "codex" {
		t.Fatalf("unexpected provider defaults: %+v", provider)
	}
}

func TestApplyProviderLoginProviderTemplateDefaults_ReplacesOnlyKnownPreviousDefaults(t *testing.T) {
	provider := config.Provider{
		Protocol:       "anthropic",
		BaseURL:        "https://api.example.com",
		APIPath:        "/custom/messages",
		ForwardURL:     "/v1/chat/completions",
		SupportTypes:   []string{"openai"},
		MaxTokensLimit: 10000,
	}
	previous := providerLoginTemplateDefaults{
		APIPathCandidates:        []string{"/v1/chat/completions"},
		ForwardURLCandidates:     []string{"/v1/chat/completions"},
		SupportTypes:             []string{"openai"},
		MaxTokensLimit:           10000,
		MaxTokensLimitCandidates: []int{10000, 128000},
	}
	applyProviderLoginProviderTemplateDefaults(&provider, modelcard.ProviderTemplate{
		ID:             "anthropic.messages",
		Protocol:       "anthropic",
		APIPath:        "/v1/messages",
		ForwardURL:     "/v1/messages",
		SupportTypes:   []string{"anthropic"},
		MaxTokensLimit: 131072,
	}, previous, true)

	if provider.APIPath != "/custom/messages" {
		t.Fatalf("custom api_path should be preserved, got %+v", provider)
	}
	if provider.ForwardURL != "/v1/messages" || strings.Join(provider.SupportTypes, ",") != "anthropic" || provider.MaxTokensLimit != 131072 {
		t.Fatalf("known previous defaults should be replaced, got %+v", provider)
	}
}

func TestRunProviderLogin_ModelCardsPreserveExistingCapabilityFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.4","inputModalities":["text","image"],"reasoningEfforts":["high"],"maxContextTokens":999999}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  items:
    codex:
      enabled: true
      protocol: codex
      base_url: https://old.example.com
      default_model: gpt-5.4
      supported_models:
        - gpt-5.4
      model_capabilities:
        gpt-5.4:
          input_modalities:
            - text
          reasoning_efforts:
            - low
          max_context_tokens: 123
`)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{
		ConfigFilePath: path,
		Providers: config.ProvidersConfig{Items: map[string]config.Provider{
			"codex": {
				Enabled:         true,
				Protocol:        "codex",
				BaseURL:         "https://old.example.com",
				DefaultModel:    "gpt-5.4",
				SupportedModels: []string{"gpt-5.4"},
				ModelCapabilities: map[string]config.ModelCapabilitySpec{
					"gpt-5.4": {
						InputModalities:  []string{"text"},
						ReasoningEfforts: []string{"low"},
						MaxContextTokens: 123,
					},
				},
			},
		}},
	}

	_, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "codex",
		LoginProtocol: "codex-apikey",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	capability := cfg.Providers.Items["codex"].ModelCapabilities["gpt-5.4"]
	if strings.Join(capability.InputModalities, ",") != "text" {
		t.Fatalf("input modalities overwritten: %+v", capability)
	}
	if strings.Join(capability.ReasoningEfforts, ",") != "low" {
		t.Fatalf("reasoning efforts overwritten: %+v", capability)
	}
	if capability.MaxContextTokens != 123 {
		t.Fatalf("max context overwritten: %+v", capability)
	}
	if capability.AutoCompactTokenLimit != 945000 || !capability.NativeTools.ImageGeneration {
		t.Fatalf("expected template to fill missing fields: %+v", capability)
	}
}

func TestRunProviderLogin_ModelCardWarningsNonStrict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	cardsPath := filepath.Join(dir, "broken-model-cards.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(cardsPath, []byte("version: ["), 0o644); err != nil {
		t.Fatalf("write cards: %v", err)
	}

	result, err := runProviderLogin(providerLoginRequest{
		Config:               &config.Config{ConfigFilePath: path},
		ProviderName:         "alpha",
		LoginProtocol:        "openai",
		BaseURL:              server.URL,
		APIKey:               "sk-test",
		ModelCardCatalogPath: cardsPath,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if len(result.ModelCardWarnings) != 1 || result.ModelCardWarnings[0].Code != "parse_failed" {
		t.Fatalf("expected parse warning, got %+v", result.ModelCardWarnings)
	}
}

func TestRunProviderLogin_ModelCardStrictFailsBeforeWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	cardsPath := filepath.Join(dir, "broken-model-cards.yaml")
	initial := "providers:\n  items: {}\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(cardsPath, []byte("version: ["), 0o644); err != nil {
		t.Fatalf("write cards: %v", err)
	}

	_, err := runProviderLogin(providerLoginRequest{
		Config:               &config.Config{ConfigFilePath: path},
		ProviderName:         "alpha",
		LoginProtocol:        "openai",
		BaseURL:              server.URL,
		APIKey:               "sk-test",
		ModelCardCatalogPath: cardsPath,
		ModelCardsStrict:     true,
	})
	if err == nil {
		t.Fatal("expected strict model card error")
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}
	if string(content) != initial {
		t.Fatalf("config changed on strict model card failure:\n%s", string(content))
	}
}

func TestBuildProviderLoginModelCapabilities_AddsSensenovaReasoningEfforts(t *testing.T) {
	capabilities := buildProviderLoginModelCapabilities("sensenova", "openai", config.Provider{
		Protocol: "openai",
		BaseURL:  "https://token.sensenova.cn/v1",
	}, []providerModelInfo{{ID: "sensenova-6.7-flash-lite"}})

	capability, ok := capabilities["sensenova-6.7-flash-lite"]
	if !ok {
		t.Fatalf("expected sensenova model capability, got %+v", capabilities)
	}
	if !capability.ReasoningModel || strings.Join(capability.ReasoningEfforts, ",") != "low,medium,high,none" {
		t.Fatalf("unexpected capability: %+v", capability)
	}
	if wildcard := capabilities["*"]; !wildcard.ReasoningModel || strings.Join(wildcard.ReasoningEfforts, ",") != "low,medium,high,none" {
		t.Fatalf("unexpected wildcard capability: %+v", wildcard)
	}
}

func TestRunProviderLogin_DoesNotWriteOnModelsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	initial := "providers:\n  items: {}\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	_, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "alpha",
		LoginProtocol: "openai",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(content) != initial {
		t.Fatalf("config changed on failure:\n%s", string(content))
	}
	if len(cfg.Providers.Items) != 0 {
		t.Fatalf("cfg changed on failure: %+v", cfg.Providers.Items)
	}
}

func TestRunProviderLogin_InteractiveManualModelsWhenEndpointReturnsHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html>not found</html>`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}
	prompter := &testLoginPrompter{text: map[string]string{
		"模型列表（逗号分隔）": "manual-model, manual-model-2",
	}}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "manual_html",
		LoginProtocol: "openai",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
		Interactive:   true,
		Prompter:      prompter,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.LoginProtocol != "openai" || result.Protocol != "openai" {
		t.Fatalf("unexpected protocol result: %+v", result)
	}
	if result.ModelsVerifiedAt != "" {
		t.Fatalf("manual models should not be marked verified, got %q", result.ModelsVerifiedAt)
	}
	if strings.Join(result.SupportedModels, ",") != "manual-model,manual-model-2" || result.DefaultModel != "manual-model" {
		t.Fatalf("unexpected manual models result: %+v", result)
	}
	provider := cfg.Providers.Items["manual_html"]
	if strings.Join(provider.SupportedModels, ",") != "manual-model,manual-model-2" || provider.DefaultModel != "manual-model" {
		t.Fatalf("unexpected persisted provider: %+v", provider)
	}
}

func TestRunProviderLogin_AutoInteractivePromptsExplicitProtocolAndManualModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}
	prompter := &testLoginPrompter{text: map[string]string{
		"协议编号或名称":    "1",
		"模型列表（逗号分隔）": "manual-auto-model",
	}}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "manual_auto",
		LoginProtocol: "auto",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
		Interactive:   true,
		Prompter:      prompter,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.LoginProtocol != "openai" || result.Protocol != "openai" || result.DefaultModel != "manual-auto-model" {
		t.Fatalf("auto fallback should require explicit protocol and manual model, got %+v", result)
	}
}

func TestRunProviderLogin_AutoNonInteractiveReportsUnsupportedWithoutModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer server.Close()

	_, err := runProviderLogin(providerLoginRequest{
		Config:        &config.Config{},
		ProviderName:  "bad_auto",
		LoginProtocol: "auto",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
	})
	if err == nil {
		t.Fatal("expected auto unsupported error")
	}
	if !strings.Contains(err.Error(), "auto protocol is not supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunProviderLogin_PartialEditPreservesExistingAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer old-key" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"new-model"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	seedTestAPIKeyAuth(t, dir, "alpha", "old-key")
	path := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://old.example.com
      api_key_ref: alpha
      extra_field: keep
      supported_models:
        - old-model
      default_model: old-model
`)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{
		ConfigFilePath: path,
		Providers: config.ProvidersConfig{Items: map[string]config.Provider{
			"alpha": {
				Enabled:         true,
				Protocol:        "openai",
				BaseURL:         "https://old.example.com",
				APIKey:          "",
				APIKeyRef:       "alpha",
				SupportedModels: []string{"old-model"},
				DefaultModel:    "old-model",
			},
		}},
	}

	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "alpha",
		LoginProtocol: "openai",
		BaseURL:       server.URL,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if !result.Updated || result.DefaultModel != "new-model" {
		t.Fatalf("unexpected result: %+v", result)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"api_key_ref: alpha",
		"extra_field: keep",
		"base_url: " + server.URL,
		"- new-model",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
}

func TestRunProviderLogin_InteractiveEditPromptsBaseURLAndKeepsBlankAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer old-key" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"new-model"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	seedTestAPIKeyAuth(t, dir, "alpha", "old-key")
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(`
providers:
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://old.example.com
      api_key_ref: alpha
      default_model: old-model
      supported_models:
        - old-model
`)), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{
		ConfigFilePath: path,
		Providers: config.ProvidersConfig{Items: map[string]config.Provider{
			"alpha": {
				Enabled:         true,
				Protocol:        "openai",
				BaseURL:         "https://old.example.com",
				APIKeyRef:       "alpha",
				DefaultModel:    "old-model",
				SupportedModels: []string{"old-model"},
			},
		}},
	}
	prompter := &testLoginPrompter{text: map[string]string{
		"Base URL": server.URL,
	}}
	result, err := runProviderLogin(providerLoginRequest{
		Config:       cfg,
		ProviderName: "alpha",
		Interactive:  true,
		Prompter:     prompter,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.BaseURL != server.URL || result.DefaultModel != "new-model" {
		t.Fatalf("unexpected result: %+v", result)
	}
	authStorePath := testAuthStorePath(dir)
	loadedAuth, err := config.LoadProviderAuthFromPath(authStorePath, "alpha")
	if err != nil {
		t.Fatalf("LoadProviderAuthFromPath: %v", err)
	}
	if loadedAuth.KeyType != config.AuthKeyTypeAPIKey || loadedAuth.APIKey != "old-key" {
		t.Fatalf("expected API key to be resolved from auth store, got %+v", loadedAuth)
	}
}

func TestRunProviderLogin_InteractiveProviderSelectionByNumber(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"new-model"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{
		ConfigFilePath: path,
		Providers: config.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:      true,
					Protocol:     "openai",
					BaseURL:      server.URL,
					APIKey:       "old-key",
					DefaultModel: "old-model",
				},
			},
		},
	}
	prompter := &testLoginPrompter{text: map[string]string{
		"Provider 名称/编号/搜索（/关键词, n下一页, p上一页）": "1",
		"Base URL": server.URL,
	}}
	result, err := runProviderLogin(providerLoginRequest{
		Config:      cfg,
		Interactive: true,
		Prompter:    prompter,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.ProviderName != "alpha" || !result.Updated {
		t.Fatalf("unexpected provider selection result: %+v", result)
	}
}

func TestLoginProviderSelectionOptions_SortsAndOmitsGhostDefault(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "gemini_local",
			Items: map[string]config.Provider{
				"zeta":  {Enabled: true},
				"alpha": {Enabled: true},
			},
		},
	}
	options := loginProviderSelectionOptions(cfg)
	if len(options) != 2 || options[0] != "alpha" || options[1] != "zeta" {
		t.Fatalf("expected sorted real providers only, got %#v", options)
	}
	if got := resolveLoginProviderDefault(cfg, options); got != "" {
		t.Fatalf("expected empty default for missing provider, got %q", got)
	}
}

func TestResolveLoginProviderSelection_NumberAndInvalidNumber(t *testing.T) {
	options := []string{"alpha", "beta", "gamma"}
	selected, ok, reason := resolveLoginProviderSelection("2", "alpha", options)
	if !ok || selected != "beta" || reason != "" {
		t.Fatalf("expected beta from number 2, got selected=%q ok=%v reason=%q", selected, ok, reason)
	}
	selected, ok, reason = resolveLoginProviderSelection("99", "alpha", options)
	if ok || selected != "" || !strings.Contains(reason, "无效编号") {
		t.Fatalf("expected invalid number rejection, got selected=%q ok=%v reason=%q", selected, ok, reason)
	}
	selected, ok, reason = resolveLoginProviderSelection("", "", options)
	if ok || selected != "" || reason == "" {
		t.Fatalf("expected empty input without default to fail, got selected=%q ok=%v reason=%q", selected, ok, reason)
	}
	selected, ok, reason = resolveLoginProviderSelection("new_provider", "alpha", options)
	if !ok || selected != "new_provider" || reason != "" {
		t.Fatalf("expected free-form new provider name, got selected=%q ok=%v reason=%q", selected, ok, reason)
	}
}

func TestFilterLoginProviders_FuzzyRanking(t *testing.T) {
	options := []string{
		"openai_proxy",
		"openai",
		"azure_openai",
		"claude",
		"deepseek",
	}
	matched := filterLoginProviders(options, "open")
	if len(matched) != 3 {
		t.Fatalf("expected 3 open* matches, got %#v", matched)
	}
	if matched[0] != "openai" {
		t.Fatalf("expected openai ranked first, got %#v", matched)
	}
	// 无通配符时不支持拆分匹配：oai 不再命中 openai。
	matched = filterLoginProviders(options, "oai")
	if len(matched) != 0 {
		t.Fatalf("expected no split-subsequence match for oai, got %#v", matched)
	}
	// 显式通配符恢复模糊匹配：o*i -> openai。
	matched = filterLoginProviders(options, "o*i")
	if len(matched) == 0 || matched[0] != "openai" {
		t.Fatalf("expected wildcard match openai first, got %#v", matched)
	}
	matched = filterLoginProviders(options, "nope")
	if len(matched) != 0 {
		t.Fatalf("expected no matches, got %#v", matched)
	}
}

func TestFilterLoginProviders_NoWildcardNoSplit(t *testing.T) {
	options := []string{
		"muse-spark-1.2",
		"muse-spark-1.3-contributor-free",
		"stepfun-ai/step-3.5-flash",
		"stepaudio-2.5-asr-stream",
	}
	// 连续多个字符必须完整连续匹配：muse 只命中真实含 muse 的名称。
	matched := filterLoginProviders(options, "muse")
	if len(matched) != 2 {
		t.Fatalf("expected 2 contiguous muse matches, got %#v", matched)
	}
	for _, name := range matched {
		if name != "muse-spark-1.2" && name != "muse-spark-1.3-contributor-free" {
			t.Fatalf("unexpected match %q", name)
		}
	}
	// *spark* 通配命中所有含 spark 的名称。
	matched = filterLoginProviders(options, "*spark*")
	if len(matched) != 2 {
		t.Fatalf("expected 2 wildcard spark matches, got %#v", matched)
	}
	// 纯前缀匹配保持有效。
	matched = filterLoginProviders(options, "muse-spark-1.3")
	if len(matched) != 1 || matched[0] != "muse-spark-1.3-contributor-free" {
		t.Fatalf("expected prefix match only, got %#v", matched)
	}
}

func TestApplyLoginProviderPickerInput_FilterPageAndSelect(t *testing.T) {
	options := make([]string, 0, 20)
	for i := 1; i <= 20; i++ {
		options = append(options, fmt.Sprintf("provider_%02d", i))
	}
	options = append(options, "openai", "openai_proxy", "azure_openai")
	sort.Strings(options)

	state := loginProviderPickerState{
		Options:  options,
		Current:  "openai",
		PageSize: 5,
	}

	// fuzzy multi-match becomes a filter
	state, result := applyLoginProviderPickerInput(state, "openai")
	// exact name openai exists, so exact match wins
	if !result.Done || result.Selected != "openai" {
		t.Fatalf("expected exact openai select, got %+v", result)
	}

	state = loginProviderPickerState{Options: options, Current: "openai", PageSize: 5}
	state, result = applyLoginProviderPickerInput(state, "/open")
	if result.Done || !result.Redraw || state.Filter != "open" {
		t.Fatalf("expected filter applied, got state=%+v result=%+v", state, result)
	}
	pageItems, page, pageCount, total := state.pageWindow()
	if page != 0 || total < 2 || len(pageItems) == 0 {
		t.Fatalf("unexpected page window: items=%#v page=%d pageCount=%d total=%d", pageItems, page, pageCount, total)
	}

	// page-local number selects filtered item
	state, result = applyLoginProviderPickerInput(state, "1")
	if !result.Done || result.Selected == "" {
		t.Fatalf("expected page-local selection, got %+v", result)
	}
	if !strings.Contains(strings.ToLower(result.Selected), "open") {
		t.Fatalf("expected selected item to match filter, got %q", result.Selected)
	}

	// pagination
	state = loginProviderPickerState{Options: options, PageSize: 5}
	state, result = applyLoginProviderPickerInput(state, "n")
	if !result.Redraw || state.Page != 1 {
		t.Fatalf("expected next page, got state=%+v result=%+v", state, result)
	}
	state, result = applyLoginProviderPickerInput(state, "p")
	if !result.Redraw || state.Page != 0 {
		t.Fatalf("expected prev page, got state=%+v result=%+v", state, result)
	}

	// bare multi-match filters without exact option name
	state = loginProviderPickerState{Options: options, PageSize: 10}
	state, result = applyLoginProviderPickerInput(state, "provider")
	if result.Done || !result.Redraw || state.Filter != "provider" {
		t.Fatalf("expected multi-match filter, got state=%+v result=%+v", state, result)
	}

	// forced create bypasses reserved command words
	state = loginProviderPickerState{Options: options, PageSize: 10}
	state, result = applyLoginProviderPickerInput(state, "+next")
	if !result.Done || result.Selected != "next" {
		t.Fatalf("expected forced create next, got %+v", result)
	}
}

func TestRunProviderLogin_InteractiveFilterAndPageSelect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"new-model"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	items := map[string]config.Provider{}
	for i := 1; i <= 18; i++ {
		name := fmt.Sprintf("provider_%02d", i)
		items[name] = config.Provider{
			Enabled:      true,
			Protocol:     "openai",
			BaseURL:      server.URL,
			APIKey:       "k-" + name,
			DefaultModel: "m-" + name,
		}
	}
	items["openai_proxy"] = config.Provider{
		Enabled:      true,
		Protocol:     "openai",
		BaseURL:      server.URL,
		APIKey:       "proxy-key",
		DefaultModel: "proxy-model",
	}
	items["azure_openai"] = config.Provider{
		Enabled:      true,
		Protocol:     "openai",
		BaseURL:      server.URL,
		APIKey:       "azure-key",
		DefaultModel: "azure-model",
	}
	cfg := &config.Config{
		ConfigFilePath: path,
		Providers: config.ProvidersConfig{
			DefaultProvider: "provider_01",
			Items:           items,
		},
	}
	prompter := &testLoginPrompter{
		text: map[string]string{
			"Base URL": server.URL,
		},
		textQueue: map[string][]string{
			"Provider 名称/编号/搜索（/关键词, n下一页, p上一页）": {"/open", "1"},
		},
	}
	result, err := runProviderLogin(providerLoginRequest{
		Config:      cfg,
		Interactive: true,
		Prompter:    prompter,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.ProviderName != "azure_openai" && result.ProviderName != "openai_proxy" {
		t.Fatalf("expected filtered open* provider, got %+v", result)
	}
	joined := strings.Join(prompter.lines, "\n")
	if !strings.Contains(joined, "过滤") && !strings.Contains(joined, "匹配") {
		t.Fatalf("expected filter/page output, got lines:\n%s", joined)
	}
}

func TestRunProviderLogin_InteractiveRejectsGhostDefaultAndInvalidNumber(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"new-model"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{
		ConfigFilePath: path,
		Providers: config.ProvidersConfig{
			DefaultProvider: "gemini_local",
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:      true,
					Protocol:     "openai",
					BaseURL:      server.URL,
					APIKey:       "old-key",
					DefaultModel: "old-model",
				},
				"beta": {
					Enabled:      true,
					Protocol:     "openai",
					BaseURL:      server.URL,
					APIKey:       "beta-key",
					DefaultModel: "beta-model",
				},
			},
		},
	}
	prompter := &testLoginPrompter{
		text: map[string]string{
			"Provider 名称/编号/搜索（/关键词, n下一页, p上一页）": "99",
			"Base URL": server.URL,
		},
		textQueue: map[string][]string{
			"Provider 名称/编号/搜索（/关键词, n下一页, p上一页）": {"99", "2"},
		},
	}
	result, err := runProviderLogin(providerLoginRequest{
		Config:      cfg,
		Interactive: true,
		Prompter:    prompter,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.ProviderName != "beta" {
		t.Fatalf("expected beta after rejecting invalid number, got %+v", result)
	}
	joined := strings.Join(prompter.lines, "\n")
	if !strings.Contains(joined, "default_provider=gemini_local") {
		t.Fatalf("expected ghost default warning, got lines:\n%s", joined)
	}
	if !strings.Contains(joined, "无效编号 99") {
		t.Fatalf("expected invalid number message, got lines:\n%s", joined)
	}
}

func TestRunProviderLogin_CodexAPIKeyWritesCodexProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			// Allow best-effort site-detect probes without failing the models-focused test.
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-codex" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5-codex"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}
	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "codex_key",
		LoginProtocol: "codex-apikey",
		BaseURL:       server.URL,
		APIKey:        "sk-codex",
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.Protocol != "codex" || result.LoginProtocol != "codex-apikey" || result.AuthMode != providerAuthModeAPIKey {
		t.Fatalf("unexpected result: %+v", result)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{"protocol: codex", "auth_mode: api_key", "api_key_ref: codex_key", "- gpt-5-codex"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
	loadedAuth, err := config.LoadProviderAuthFromPath(testAuthStorePath(dir), "codex_key")
	if err != nil {
		t.Fatalf("LoadProviderAuthFromPath: %v", err)
	}
	if loadedAuth.KeyType != config.AuthKeyTypeAPIKey || loadedAuth.APIKey != "sk-codex" {
		t.Fatalf("unexpected auth record: %+v", loadedAuth)
	}
}

func TestRunProviderLogin_CodexOAuthStoresAuthRefWithoutTokenInConfig(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/deviceauth/usercode", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected usercode method: %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"device_auth_id":"device-1","user_code":"CODE-1","interval":"0"}`))
	})
	mux.HandleFunc("/api/accounts/deviceauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected token poll method: %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"authorization_code":"auth-code","code_challenge":"challenge","code_verifier":"verifier"}`))
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected oauth token method: %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"id_token":"id-token","access_token":"access-token","refresh_token":"refresh-token"}`))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"}]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	authStorePath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(configPath, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: configPath}
	result, err := runProviderLogin(providerLoginRequest{
		Config:        cfg,
		ProviderName:  "codex_oauth",
		LoginProtocol: "codex-oauth",
		BaseURL:       server.URL,
		AuthRef:       "codex_ref",
		AuthStorePath: authStorePath,
		OAuthIssuer:   server.URL,
		OAuthTimeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.AuthMode != providerAuthModeOAuth || result.AuthRef != "codex_ref" || result.DefaultModel != "gpt-5" {
		t.Fatalf("unexpected result: %+v", result)
	}
	loadedAuth, err := config.LoadProviderAuthFromPath(authStorePath, "codex_ref")
	if err != nil {
		t.Fatalf("LoadProviderAuthFromPath: %v", err)
	}
	if loadedAuth.AccessToken != "access-token" || loadedAuth.RefreshToken != "refresh-token" {
		t.Fatalf("unexpected auth record: %+v", loadedAuth)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, forbidden := range []string{"access-token", "refresh-token", "id-token", "api_key:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("config leaked %q:\n%s", forbidden, text)
		}
	}
	for _, expected := range []string{"protocol: codex", "auth_mode: oauth", "auth_ref: codex_ref", "- gpt-5"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
}

func TestRunProviderLogin_CodexOAuthDoesNotPersistAuthOnModelsFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/deviceauth/usercode", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_auth_id":"device-1","user_code":"CODE-1","interval":"0"}`))
	})
	mux.HandleFunc("/api/accounts/deviceauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"authorization_code":"auth-code","code_challenge":"challenge","code_verifier":"verifier"}`))
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id_token":"id-token","access_token":"access-token","refresh_token":"refresh-token"}`))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	authStorePath := filepath.Join(dir, "auth.json")
	initial := "providers:\n  items: {}\n"
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := runProviderLogin(providerLoginRequest{
		Config:        &config.Config{ConfigFilePath: configPath},
		ProviderName:  "codex_oauth",
		LoginProtocol: "codex-oauth",
		BaseURL:       server.URL,
		AuthRef:       "codex_ref",
		AuthStorePath: authStorePath,
		OAuthIssuer:   server.URL,
		OAuthTimeout:  time.Second,
	})
	if err == nil {
		t.Fatal("expected models validation error")
	}
	if _, statErr := os.Stat(authStorePath); !os.IsNotExist(statErr) {
		t.Fatalf("expected auth store not to be written, stat err=%v", statErr)
	}
	content, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}
	if string(content) != initial {
		t.Fatalf("config changed on failure:\n%s", string(content))
	}
}

func TestRenderLoginCommandResultShowsAPIKeyRef(t *testing.T) {
	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	renderLoginCommandResult(&providerLoginResult{
		ProviderName:    "alpha",
		Protocol:        "openai",
		LoginProtocol:   "openai",
		AuthMode:        providerAuthModeAPIKey,
		APIKeyRef:       "alpha",
		BaseURL:         "https://api.example.com",
		ModelsEndpoint:  "/v1/models",
		DefaultModel:    "gpt-5",
		SupportedModels: []string{"gpt-5"},
		AuthStorePath:   "/home/test/.aicli/auth.json",
		APIKeyMasked:    "sk-t...test",
	}, structuredOutputOptions{Format: "text"})

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	os.Stdout = stdout
	data, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	text := string(bytes.TrimSpace(data))
	for _, expected := range []string{
		"API key ref:     alpha",
		"Auth store:      /home/test/.aicli/auth.json",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in output:\n%s", expected, text)
		}
	}
}

func TestDisplayProviderShowsAuthReferences(t *testing.T) {
	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	displayProvider(&config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:      true,
					Protocol:     "openai",
					BaseURL:      "https://api.example.com",
					APIPath:      "/v1",
					APIKeyRef:    "alpha",
					AuthMode:     "api_key",
					AuthRef:      "oauth_ref",
					DefaultModel: "gpt-5",
				},
			},
		},
	}, "alpha", false)

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	os.Stdout = stdout
	data, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	text := string(bytes.TrimSpace(data))
	for _, expected := range []string{
		"API key ref:     alpha",
		"Auth ref:        oauth_ref",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in output:\n%s", expected, text)
		}
	}
}

func TestParseChatLoginCommandRequest(t *testing.T) {
	req, err := parseChatLoginCommandRequest(`/login --provider alpha --protocol openai --base-url http://localhost:4000 --models-path /v1/models --model-cards ./cards.yaml --no-model-cards --model-cards-strict --set-default --switch`)
	if err != nil {
		t.Fatalf("parseChatLoginCommandRequest: %v", err)
	}
	if req.Provider != "alpha" || req.Protocol != "openai" || req.BaseURL != "http://localhost:4000" || req.ModelsPath != "/v1/models" || req.ModelCardsPath != "./cards.yaml" || !req.NoModelCards || !req.ModelCardsStrict || !req.SetDefault || !req.Switch {
		t.Fatalf("unexpected req: %+v", req)
	}
}

type testLoginPrompter struct {
	text      map[string]string
	textQueue map[string][]string
	secrets   map[string]string
	lines     []string
}

func (p *testLoginPrompter) PromptText(label, current string, required bool) (string, error) {
	if p != nil && p.textQueue != nil {
		if queue := p.textQueue[label]; len(queue) > 0 {
			value := queue[0]
			p.textQueue[label] = queue[1:]
			return value, nil
		}
	}
	if p != nil && p.text != nil {
		if value, ok := p.text[label]; ok {
			return value, nil
		}
	}
	return current, nil
}

func (p *testLoginPrompter) PromptSecret(label, currentMasked string, required bool) (string, error) {
	if p != nil && p.secrets != nil {
		if value, ok := p.secrets[label]; ok {
			return value, nil
		}
	}
	return "", nil
}

func (p *testLoginPrompter) PrintLine(line string) {
	if p == nil {
		return
	}
	p.lines = append(p.lines, line)
}

// TestPrintLoginProviderPicker_EmitsSingleDenseBlock guards the chat /login
// provider list rendering: every PrintLine call becomes an independent
// transcript cell with a gap row in the unified surface, so the picker must
// submit the whole block in one call. Per-row calls used to render a blank
// line between every provider.
func TestPrintLoginProviderPicker_EmitsSingleDenseBlock(t *testing.T) {
	prompter := &testLoginPrompter{}
	state := loginProviderPickerState{
		Options:  []string{"alpha", "beta", "gamma"},
		Current:  "beta",
		PageSize: loginProviderPageSize,
	}
	printLoginProviderPicker(prompter, state, "", true)

	if len(prompter.lines) != 1 {
		t.Fatalf("expected the picker block in a single PrintLine call, got %d calls: %#v", len(prompter.lines), prompter.lines)
	}
	block := prompter.lines[0]
	rows := strings.Split(block, "\n")
	if len(rows) != 5 { // header + 3 providers + hint
		t.Fatalf("expected 5 rows in picker block, got %d:\n%s", len(rows), block)
	}
	for i, row := range rows {
		if strings.TrimSpace(row) == "" {
			t.Fatalf("unexpected blank row at index %d in picker block (rows must stay dense):\n%s", i, block)
		}
	}
	if !strings.Contains(block, "现有 providers（共 3）:") {
		t.Fatalf("missing picker header in block:\n%s", block)
	}
	for i, name := range []string{"alpha", "beta", "gamma"} {
		marker := ""
		if name == "beta" {
			marker = " (默认)"
		}
		expected := fmt.Sprintf("  [%d] %s%s", i+1, name, marker)
		if !strings.Contains(block, expected) {
			t.Fatalf("missing provider row %q in block:\n%s", expected, block)
		}
	}
	if !strings.Contains(block, "提示: 输入编号选择当前页") {
		t.Fatalf("missing first-run hint in block:\n%s", block)
	}
}

// TestPrintLoginProviderPicker_EmptyFilterMatchesStaysDense covers the
// "无匹配项" branch: it must also render as a single dense block.
func TestPrintLoginProviderPicker_EmptyFilterMatchesStaysDense(t *testing.T) {
	prompter := &testLoginPrompter{}
	state := loginProviderPickerState{
		Options:  []string{"alpha", "beta"},
		Filter:   "nope",
		PageSize: loginProviderPageSize,
	}
	printLoginProviderPicker(prompter, state, "alpha", false)

	if len(prompter.lines) != 1 {
		t.Fatalf("expected the filtered picker in a single PrintLine call, got %d calls: %#v", len(prompter.lines), prompter.lines)
	}
	block := prompter.lines[0]
	for _, row := range strings.Split(block, "\n") {
		if strings.TrimSpace(row) == "" {
			t.Fatalf("unexpected blank row in filtered picker block:\n%s", block)
		}
	}
	if !strings.Contains(block, "无匹配项") {
		t.Fatalf("missing no-match notice in block:\n%s", block)
	}
}

// TestPrintLoginProtocolOptions_EmitsSingleDenseBlock guards the chat /login
// protocol list rendering: like the provider picker, the whole protocol list
// must be submitted as one PrintLine block so rows stay dense in the unified
// surface instead of getting a gap row between every entry.
func TestPrintLoginProtocolOptions_EmitsSingleDenseBlock(t *testing.T) {
	prompter := &testLoginPrompter{}
	options := loginProtocolOptions()
	printLoginProtocolOptions(prompter, "请选择登录协议:", options)

	if len(prompter.lines) != 1 {
		t.Fatalf("expected the protocol list in a single PrintLine call, got %d calls: %#v", len(prompter.lines), prompter.lines)
	}
	block := prompter.lines[0]
	rows := strings.Split(block, "\n")
	if len(rows) != 1+len(options) {
		t.Fatalf("expected %d rows in protocol block, got %d:\n%s", 1+len(options), len(rows), block)
	}
	for i, row := range rows {
		if strings.TrimSpace(row) == "" {
			t.Fatalf("unexpected blank row at index %d in protocol block (rows must stay dense):\n%s", i, block)
		}
	}
	if !strings.HasPrefix(block, "请选择登录协议:") {
		t.Fatalf("missing protocol header in block:\n%s", block)
	}
	for i, option := range options {
		expected := fmt.Sprintf("  [%d] %s", i+1, option)
		if !strings.Contains(block, expected) {
			t.Fatalf("missing protocol row %q in block:\n%s", expected, block)
		}
	}
}

func TestPromptLoginProtocol_EmitsSingleDenseBlock(t *testing.T) {
	prompter := &testLoginPrompter{
		textQueue: map[string][]string{
			"协议编号或名称": {"3"},
		},
	}
	selected, err := promptLoginProtocol(prompter)
	if err != nil {
		t.Fatalf("promptLoginProtocol: %v", err)
	}
	options := loginProtocolOptions()
	if selected != options[2] {
		t.Fatalf("expected selection %q, got %q", options[2], selected)
	}
	if len(prompter.lines) != 1 {
		t.Fatalf("expected the protocol list in a single PrintLine call, got %d calls: %#v", len(prompter.lines), prompter.lines)
	}
	rows := strings.Split(prompter.lines[0], "\n")
	if len(rows) != 1+len(options) {
		t.Fatalf("expected %d rows in protocol block, got %d:\n%s", 1+len(options), len(rows), prompter.lines[0])
	}
	for i, row := range rows {
		if strings.TrimSpace(row) == "" {
			t.Fatalf("unexpected blank row at index %d in protocol block:\n%s", i, prompter.lines[0])
		}
	}
}

func TestPromptExplicitLoginProtocol_EmitsSingleDenseBlock(t *testing.T) {
	prompter := &testLoginPrompter{
		textQueue: map[string][]string{
			"协议编号或名称": {"1"},
		},
	}
	selected, err := promptExplicitLoginProtocol(prompter)
	if err != nil {
		t.Fatalf("promptExplicitLoginProtocol: %v", err)
	}
	if selected != "openai" {
		t.Fatalf("expected selection \"openai\", got %q", selected)
	}
	if len(prompter.lines) != 1 {
		t.Fatalf("expected the explicit protocol list in a single PrintLine call, got %d calls: %#v", len(prompter.lines), prompter.lines)
	}
	block := prompter.lines[0]
	if !strings.HasPrefix(block, "请选择显式登录协议:") {
		t.Fatalf("missing explicit protocol header in block:\n%s", block)
	}
	rows := strings.Split(block, "\n")
	if len(rows) != 7 { // header + 6 non-auto protocols
		t.Fatalf("expected 7 rows in explicit protocol block, got %d:\n%s", len(rows), block)
	}
	for i, row := range rows {
		if strings.TrimSpace(row) == "" {
			t.Fatalf("unexpected blank row at index %d in explicit protocol block:\n%s", i, block)
		}
	}
}
