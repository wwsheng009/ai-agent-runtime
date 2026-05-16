package commands

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func writeConfigTUITestConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `
server:
  name: test
  host: 127.0.0.1
  port: 8080
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://alpha.example.com
      api_path: /v1/chat/completions
      default_model: gpt-alpha
      api_key_ref: alpha-ref
      supported_models:
        - gpt-alpha
    beta:
      enabled: false
      protocol: anthropic
      base_url: https://beta.example.com
      default_model: claude-beta
provider_groups:
  - name: default
    strategy: round_robin
    providers:
      - name: alpha
        weight: 1
aicli:
  chat:
    default_provider: alpha
    default_model: gpt-alpha
    reasoning_effort: medium
    stream: true
`
	if err := os.WriteFile(path, []byte(strings.TrimSpace(raw)+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	return cfg, path
}

func TestConfigTUI_ProviderToggleAndSetDefault(t *testing.T) {
	cfg, path := writeConfigTUITestConfig(t)
	input := strings.NewReader(strings.Join([]string{
		"2", // providers
		"2", // beta detail
		"e", // enable beta
		"d", // set beta default
		"b",
		"b",
		"q",
		"",
	}, "\n"))
	var output bytes.Buffer

	if err := runConfigTUI(input, &output, cfg); err != nil {
		t.Fatalf("runConfigTUI: %v\noutput:\n%s", err, output.String())
	}
	loaded, err := config.InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if !loaded.Providers.Items["beta"].Enabled {
		t.Fatal("expected beta to be enabled")
	}
	if loaded.Providers.DefaultProvider != "beta" {
		t.Fatalf("expected beta default provider, got %q", loaded.Providers.DefaultProvider)
	}
}

func TestConfigTUI_DefaultRouting(t *testing.T) {
	tests := []struct {
		name string
		opts configTUIOptions
		want bool
	}{
		{
			name: "plain text config defaults to tui",
			opts: configTUIOptions{OutputFormat: "text"},
			want: true,
		},
		{
			name: "explicit tui still enters tui",
			opts: configTUIOptions{ExplicitTUI: true, OutputFormat: "text"},
			want: true,
		},
		{
			name: "no tui keeps summary",
			opts: configTUIOptions{NoTUI: true, OutputFormat: "text"},
		},
		{
			name: "provider flag keeps detail output",
			opts: configTUIOptions{ProviderFlag: "alpha", OutputFormat: "text"},
		},
		{
			name: "groups flag keeps groups output",
			opts: configTUIOptions{ShowGroups: true, OutputFormat: "text"},
		},
		{
			name: "models flag keeps models output",
			opts: configTUIOptions{ShowModels: true, OutputFormat: "text"},
		},
		{
			name: "json keeps non-interactive output",
			opts: configTUIOptions{OutputFormat: "json"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRunConfigTUI(tt.opts); got != tt.want {
				t.Fatalf("shouldRunConfigTUI() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigTUI_EditProviderCommonFields(t *testing.T) {
	cfg, path := writeConfigTUITestConfig(t)
	input := strings.NewReader(strings.Join([]string{
		"2",                         // providers
		"1",                         // alpha detail
		"m",                         // modify
		"3",                         // protocol codex-apikey
		"https://codex.example.com", // base url
		"/responses",                // api path
		"/models",                   // models path
		"gpt-5.4",                   // default model
		"codex-ref",                 // api key ref
		"256000",                    // max tokens
		"false",                     // enabled
		"b",
		"b",
		"q",
		"",
	}, "\n"))
	var output bytes.Buffer

	if err := runConfigTUI(input, &output, cfg); err != nil {
		t.Fatalf("runConfigTUI: %v\noutput:\n%s", err, output.String())
	}
	loaded, err := config.InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	provider := loaded.Providers.Items["alpha"]
	if provider.GetProtocol() != "codex" || provider.BaseURL != "https://codex.example.com" || provider.APIPath != "/responses" {
		t.Fatalf("unexpected provider endpoint fields: %+v", provider)
	}
	if provider.DefaultModel != "gpt-5.4" || provider.APIKeyRef != "codex-ref" || provider.GetMaxTokensLimit() != 256000 || provider.Enabled {
		t.Fatalf("unexpected provider edited fields: %+v", provider)
	}
	if provider.AuthMode != "api_key" {
		t.Fatalf("expected protocol selection to sync auth_mode=api_key, got %q", provider.AuthMode)
	}
}

func TestConfigTUI_LoginProviderUsesProviderLoginTemplates(t *testing.T) {
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
	cfg, err := config.InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	input := strings.NewReader(strings.Join([]string{
		"2",        // providers
		"a",        // login/new provider
		"true",     // set default
		"gamma",    // provider name
		"2",        // protocol openai
		server.URL, // base url
		"sk-test",  // api key
		"b",        // back providers
		"q",
		"",
	}, "\n"))
	var output bytes.Buffer

	if err := runConfigTUI(input, &output, cfg); err != nil {
		t.Fatalf("runConfigTUI: %v\noutput:\n%s", err, output.String())
	}
	loaded, err := config.InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	provider := loaded.Providers.Items["gamma"]
	if provider.GetProtocol() != "openai" || provider.BaseURL != server.URL || provider.APIKeyRef != "gamma" {
		t.Fatalf("unexpected provider created by login: %+v", provider)
	}
	if provider.DefaultModel != "gpt-4.1-mini" || len(provider.SupportedModels) != 2 {
		t.Fatalf("expected models from login validation, got %+v", provider)
	}
	if loaded.Providers.DefaultProvider != "gamma" {
		t.Fatalf("expected TUI login to set default provider, got %q", loaded.Providers.DefaultProvider)
	}
	if _, err := config.LoadProviderAuthSecretFromPath(testAuthStorePath(dir), "gamma", config.AuthKeyTypeAPIKey); err != nil {
		t.Fatalf("expected auth store secret: %v", err)
	}
}

func TestConfigTUI_AdvancedEditProviderUpdatesOneField(t *testing.T) {
	cfg, path := writeConfigTUITestConfig(t)
	input := strings.NewReader(strings.Join([]string{
		"2", // providers
		"1", // alpha detail
		"a", // advanced edit
		"2", // base_url
		"https://new.example.com",
		"b",
		"b",
		"b",
		"q",
		"",
	}, "\n"))
	var output bytes.Buffer

	if err := runConfigTUI(input, &output, cfg); err != nil {
		t.Fatalf("runConfigTUI: %v\noutput:\n%s", err, output.String())
	}
	loaded, err := config.InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	provider := loaded.Providers.Items["alpha"]
	if provider.BaseURL != "https://new.example.com" {
		t.Fatalf("expected base_url update, got %+v", provider)
	}
	if provider.APIPath != "/v1/chat/completions" || provider.DefaultModel != "gpt-alpha" {
		t.Fatalf("unexpected unrelated field change: %+v", provider)
	}
}

func TestConfigTUI_AdvancedEditProviderProtocolSyncsAuthMode(t *testing.T) {
	cfg, path := writeConfigTUITestConfig(t)
	input := strings.NewReader(strings.Join([]string{
		"2", // providers
		"1", // alpha detail
		"a", // advanced edit
		"1", // protocol
		"6", // codex-oauth
		"b",
		"b",
		"b",
		"q",
		"",
	}, "\n"))
	var output bytes.Buffer

	if err := runConfigTUI(input, &output, cfg); err != nil {
		t.Fatalf("runConfigTUI: %v\noutput:\n%s", err, output.String())
	}
	loaded, err := config.InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	provider := loaded.Providers.Items["alpha"]
	if provider.GetProtocol() != "codex" || provider.AuthMode != "oauth" {
		t.Fatalf("expected codex-oauth selection to sync protocol/auth_mode, got %+v", provider)
	}
	if provider.APIKeyRef != "" {
		t.Fatalf("expected codex-oauth selection to clear api_key_ref, got %+v", provider)
	}
}

func TestConfigTUI_EditProviderSetDefaultFalseDoesNotRewrite(t *testing.T) {
	cfg, path := writeConfigTUITestConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config before: %v", err)
	}
	input := strings.NewReader(strings.Join([]string{
		"2",     // providers
		"2",     // beta detail
		"m",     // modify
		"",      // protocol
		"",      // base url
		"",      // api path
		"",      // models path
		"",      // default model
		"",      // api key ref
		"",      // max tokens
		"",      // enabled
		"false", // set default should be ignored
		"b",
		"b",
		"q",
		"",
	}, "\n"))
	var output bytes.Buffer

	if err := runConfigTUI(input, &output, cfg); err != nil {
		t.Fatalf("runConfigTUI: %v\noutput:\n%s", err, output.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("expected no rewrite when set-default=false is the only input\nbefore:\n%s\n\nafter:\n%s", string(before), string(after))
	}
}

func TestConfigTUI_ChatPreferences(t *testing.T) {
	cfg, path := writeConfigTUITestConfig(t)
	input := strings.NewReader(strings.Join([]string{
		"3",      // chat prefs
		"p", "2", // provider beta
		"m", "claude-beta",
		"r", "high",
		"s", "false",
		"b",
		"q",
		"",
	}, "\n"))
	var output bytes.Buffer

	if err := runConfigTUI(input, &output, cfg); err != nil {
		t.Fatalf("runConfigTUI: %v\noutput:\n%s", err, output.String())
	}
	loaded, err := config.InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Chat == nil {
		t.Fatal("expected aicli.chat config")
	}
	chat := loaded.AICLI.Chat
	if chat.DefaultProvider != "beta" || chat.DefaultModel != "claude-beta" || chat.ReasoningEffort != "high" {
		t.Fatalf("unexpected chat prefs: %+v", chat)
	}
	if chat.Stream == nil || *chat.Stream {
		t.Fatalf("expected stream=false, got %v", chat.Stream)
	}
}
