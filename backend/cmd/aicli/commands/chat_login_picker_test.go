package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestParseChatLoginCommandRequestProviderFlag(t *testing.T) {
	req, err := parseChatLoginCommandRequest("/login --provider alpha --base-url http://localhost:8080/v1")
	if err != nil {
		t.Fatalf("parse /login: %v", err)
	}
	if req.Provider != "alpha" {
		t.Fatalf("expected provider alpha, got %q", req.Provider)
	}
	if req.BaseURL != "http://localhost:8080/v1" {
		t.Fatalf("expected base URL, got %q", req.BaseURL)
	}
}

func TestParseChatLoginCommandRequestDryRunFlag(t *testing.T) {
	req, err := parseChatLoginCommandRequest("/login --provider alpha --dry-run")
	if err != nil {
		t.Fatalf("parse /login --dry-run: %v", err)
	}
	if !req.DryRun {
		t.Fatal("expected dry-run flag")
	}
}

func TestExecuteStructuredLoginCommandNilSession(t *testing.T) {
	result, handled := executeStructuredLoginCommand(nil, "/login")
	if !handled {
		t.Fatal("/login must be handled by the structured executor")
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "当前没有活动会话") {
		t.Fatalf("nil session must report an error, got:\n%s", text)
	}
}

func TestBuildChatLoginResultDocumentCoversKeyFields(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	result := &providerLoginResult{
		ProviderName:    "alpha",
		Protocol:        "openai",
		LoginProtocol:   "openai",
		AuthMode:        "api_key",
		APIKeyMasked:    "sk-***abc",
		BaseURL:         "http://localhost:8080/v1",
		ModelsEndpoint:  "http://localhost:8080/v1/models",
		DefaultModel:    "gpt-test",
		SupportedModels: []string{"gpt-test", "gpt-test-2"},
		ConfigPath:      "/tmp/aicli.yaml",
	}
	doc := buildChatLoginResultDocument(result)
	text := strings.TrimSpace(ui.RenderDocumentPlain(doc))
	for _, want := range []string{"Provider 登录成功", "Provider:", "alpha", "Protocol:", "openai", "Auth mode:", "api_key", "API key:", "Base URL:", "Default model:", "gpt-test", "Config:", "/tmp/aicli.yaml"} {
		if !strings.Contains(text, want) {
			t.Fatalf("login result document missing %q, got:\n%s", want, text)
		}
	}
}

func TestRefreshUnifiedLoginSessionDryRunNoWarnings(t *testing.T) {
	result := &providerLoginResult{ProviderName: "alpha", DryRun: true}
	warnings, notice := refreshUnifiedLoginSession(&ChatSession{}, result, false)
	if len(warnings) != 0 || notice != "" {
		t.Fatalf("dry-run login must not refresh the session, got warnings %#v notice %q", warnings, notice)
	}
}

func TestRefreshUnifiedLoginSessionNoProviderMatchNoWarnings(t *testing.T) {
	result := &providerLoginResult{ProviderName: "alpha"}
	session := &ChatSession{ProviderName: "beta"}
	warnings, notice := refreshUnifiedLoginSession(session, result, false)
	if len(warnings) != 0 || notice != "" {
		t.Fatalf("unrelated provider must not refresh the session, got warnings %#v notice %q", warnings, notice)
	}
}

func TestRefreshUnifiedLoginSessionMatchingProviderBranch(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// Matching provider routes into the refresh branch: the outcome is either a
	// warning (refresh failed) or a success notice, and exactly one of them is
	// set. This pins the success-notice semantics (notice is NOT a warning).
	result := &providerLoginResult{ProviderName: "alpha", DefaultModel: "gpt-test"}
	session := &ChatSession{ProviderName: "alpha", Model: "gpt-test"}
	warnings, notice := refreshUnifiedLoginSession(session, result, false)
	if len(warnings) > 0 && notice != "" {
		t.Fatalf("refresh must set either warnings or notice, not both: warnings %#v notice %q", warnings, notice)
	}
	if len(warnings) == 0 && notice == "" {
		t.Fatal("matching provider must produce a refresh outcome (warning or notice)")
	}
}
