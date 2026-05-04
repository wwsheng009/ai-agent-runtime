package commands

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
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
			t.Fatalf("unexpected path: %s", r.URL.Path)
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

func TestRunProviderLogin_PartialEditPreservesExistingAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		"Provider 名称（新建或现有）": "1",
		"Base URL":           server.URL,
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

func TestRunProviderLogin_CodexAPIKeyWritesCodexProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	req, err := parseChatLoginCommandRequest(`/login --provider alpha --protocol openai --base-url http://localhost:4000 --models-path /v1/models --set-default --switch`)
	if err != nil {
		t.Fatalf("parseChatLoginCommandRequest: %v", err)
	}
	if req.Provider != "alpha" || req.Protocol != "openai" || req.BaseURL != "http://localhost:4000" || req.ModelsPath != "/v1/models" || !req.SetDefault || !req.Switch {
		t.Fatalf("unexpected req: %+v", req)
	}
}

type testLoginPrompter struct {
	text    map[string]string
	secrets map[string]string
}

func (p *testLoginPrompter) PromptText(label, current string, required bool) (string, error) {
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

func (p *testLoginPrompter) PrintLine(line string) {}
