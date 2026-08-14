package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
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

// testLoginSelectPrompter implements both providerLoginPrompter and
// providerLoginSelectPrompter so the login flow routes through the shared
// full-screen searchable picker path (same contract as chatLoginPrompter,
// without needing a UI surface).
type testLoginSelectPrompter struct {
	*testLoginPrompter
	selectQueue []string
	selectErr   error
	cancelled   bool
}

func (p *testLoginSelectPrompter) PromptSelect(label, kind string, options []string, current string, allowCreate bool) (string, bool, error) {
	if p.selectErr != nil {
		return "", false, p.selectErr
	}
	if p.cancelled {
		return "", true, nil
	}
	if len(p.selectQueue) > 0 {
		value := p.selectQueue[0]
		p.selectQueue = p.selectQueue[1:]
		return value, false, nil
	}
	return current, false, nil
}

// TestChatLoginPrompterPromptSelectNilSessionFallsBack pins the fallback
// contract: without a picker-capable surface PromptSelect must return
// ui.ErrFullScreenUnavailable so the login flow falls back to the numbered
// text picker instead of crashing.
func TestChatLoginPrompterPromptSelectNilSessionFallsBack(t *testing.T) {
	var p chatLoginPrompter // nil session
	_, _, err := p.PromptSelect("Provider", "provider", []string{"alpha"}, "", true)
	if !errors.Is(err, ui.ErrFullScreenUnavailable) {
		t.Fatalf("expected ui.ErrFullScreenUnavailable, got %v", err)
	}
}

// TestResolveLoginProviderNameUsesSearchablePicker verifies the /login
// provider stage prefers the full-screen picker (like /model) when the
// prompter supports it.
func TestResolveLoginProviderNameUsesSearchablePicker(t *testing.T) {
	base := &testLoginPrompter{}
	selectPrompter := &testLoginSelectPrompter{testLoginPrompter: base, selectQueue: []string{"beta"}}
	cfg := &config.Config{Providers: config.ProvidersConfig{Items: map[string]config.Provider{"alpha": {}, "beta": {}}}}
	req := providerLoginRequest{Interactive: true, Prompter: selectPrompter, Config: cfg}
	name, err := resolveLoginProviderName(req, cfg)
	if err != nil {
		t.Fatalf("resolveLoginProviderName: %v", err)
	}
	if name != "beta" {
		t.Fatalf("expected beta from the searchable picker, got %q", name)
	}
	if len(base.lines) != 0 {
		t.Fatalf("searchable picker path must not print the numbered list, got %d lines: %#v", len(base.lines), base.lines)
	}
}

// TestResolveLoginProviderNameSearchablePickerCancelled verifies that an
// Esc/q cancellation in the full-screen picker surfaces as the neutral
// errChatLoginPickerCancelled (mapped to "已取消" by callers).
func TestResolveLoginProviderNameSearchablePickerCancelled(t *testing.T) {
	selectPrompter := &testLoginSelectPrompter{testLoginPrompter: &testLoginPrompter{}, cancelled: true}
	cfg := &config.Config{Providers: config.ProvidersConfig{Items: map[string]config.Provider{"alpha": {}}}}
	req := providerLoginRequest{Interactive: true, Prompter: selectPrompter, Config: cfg}
	_, err := resolveLoginProviderName(req, cfg)
	if !errors.Is(err, errChatLoginPickerCancelled) {
		t.Fatalf("expected errChatLoginPickerCancelled, got %v", err)
	}
}

// TestResolveLoginProviderNameFallsBackToTextPicker verifies that an
// unsupported surface (ui.ErrFullScreenUnavailable) degrades to the legacy
// numbered provider picker.
func TestResolveLoginProviderNameFallsBackToTextPicker(t *testing.T) {
	base := &testLoginPrompter{textQueue: map[string][]string{
		"Provider 名称/编号/搜索（/关键词, n下一页, p上一页）": {"gamma"},
	}}
	selectPrompter := &testLoginSelectPrompter{testLoginPrompter: base, selectErr: ui.ErrFullScreenUnavailable}
	cfg := &config.Config{Providers: config.ProvidersConfig{
		Items:           map[string]config.Provider{"alpha": {}, "beta": {}},
		DefaultProvider: "beta",
	}}
	req := providerLoginRequest{Interactive: true, Prompter: selectPrompter, Config: cfg}
	name, err := resolveLoginProviderName(req, cfg)
	if err != nil {
		t.Fatalf("resolveLoginProviderName: %v", err)
	}
	if name != "gamma" {
		t.Fatalf("expected fallback text picker selection gamma, got %q", name)
	}
	if len(base.lines) == 0 {
		t.Fatal("fallback path must print the numbered provider list")
	}
}

func TestPromptLoginProtocolUsesSearchablePicker(t *testing.T) {
	selectPrompter := &testLoginSelectPrompter{testLoginPrompter: &testLoginPrompter{}, selectQueue: []string{"anthropic"}}
	selected, err := promptLoginProtocol(selectPrompter)
	if err != nil {
		t.Fatalf("promptLoginProtocol: %v", err)
	}
	if selected != "anthropic" {
		t.Fatalf("expected anthropic from the searchable picker, got %q", selected)
	}
}

func TestPromptLoginProtocolSearchablePickerCancelled(t *testing.T) {
	selectPrompter := &testLoginSelectPrompter{testLoginPrompter: &testLoginPrompter{}, cancelled: true}
	_, err := promptLoginProtocol(selectPrompter)
	if !errors.Is(err, errChatLoginPickerCancelled) {
		t.Fatalf("expected errChatLoginPickerCancelled, got %v", err)
	}
}

func TestPromptLoginProtocolFallsBackToNumberedText(t *testing.T) {
	base := &testLoginPrompter{textQueue: map[string][]string{"协议编号或名称": {"3"}}}
	selectPrompter := &testLoginSelectPrompter{testLoginPrompter: base, selectErr: ui.ErrFullScreenUnavailable}
	selected, err := promptLoginProtocol(selectPrompter)
	if err != nil {
		t.Fatalf("promptLoginProtocol: %v", err)
	}
	options := loginProtocolOptions()
	if selected != options[2] {
		t.Fatalf("expected fallback selection %q, got %q", options[2], selected)
	}
}

func TestPromptExplicitLoginProtocolUsesSearchablePicker(t *testing.T) {
	selectPrompter := &testLoginSelectPrompter{testLoginPrompter: &testLoginPrompter{}, selectQueue: []string{"gemini"}}
	selected, err := promptExplicitLoginProtocol(selectPrompter)
	if err != nil {
		t.Fatalf("promptExplicitLoginProtocol: %v", err)
	}
	if selected != "gemini" {
		t.Fatalf("expected gemini from the searchable picker, got %q", selected)
	}
}

func TestPromptExplicitLoginProtocolSearchablePickerCancelled(t *testing.T) {
	selectPrompter := &testLoginSelectPrompter{testLoginPrompter: &testLoginPrompter{}, cancelled: true}
	_, err := promptExplicitLoginProtocol(selectPrompter)
	if !errors.Is(err, errChatLoginPickerCancelled) {
		t.Fatalf("expected errChatLoginPickerCancelled, got %v", err)
	}
}
