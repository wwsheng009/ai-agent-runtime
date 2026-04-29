package commands

import (
	"bufio"
	"context"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestHandleCommand_ModelDoesNotFallThroughToPermissionMode(t *testing.T) {
	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        config.Provider{Protocol: "openai", DefaultModel: "gpt-4.1"},
		Model:           "gpt-4.1",
		ReasoningEffort: "medium",
		PermissionMode:  runtimepolicy.ModeDefault,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/model", true); quit {
			t.Fatal("expected /model command not to exit")
		}
	})

	if session.PermissionMode != runtimepolicy.ModeDefault {
		t.Fatalf("expected permission mode to stay unchanged, got %s", session.PermissionMode)
	}
	if !strings.Contains(output, "当前模型: gpt-4.1") {
		t.Fatalf("expected current model output, got:\n%s", output)
	}
	if strings.Contains(output, "permission-mode") {
		t.Fatalf("expected /model to avoid permission-mode handler, got:\n%s", output)
	}
}

func TestSelectRuntimeReasoningEffort_DefaultsToFirstOnInitialSelection(t *testing.T) {
	session := &ChatSession{
		InputReader: bufio.NewReader(strings.NewReader("\n")),
	}

	oldShouldDiscard := shouldDiscardPendingInput
	shouldDiscardPendingInput = func() bool { return false }
	defer func() {
		shouldDiscardPendingInput = oldShouldDiscard
	}()

	var selected string
	output := captureStdout(t, func() {
		var err error
		selected, err = selectRuntimeReasoningEffort(session, "", []string{"high", "max"})
		if err != nil {
			t.Fatalf("selectRuntimeReasoningEffort: %v", err)
		}
	})

	if selected != "high" {
		t.Fatalf("expected blank input to default to first option high, got %q", selected)
	}
	if !strings.Contains(output, "(默认)") || !strings.Contains(output, "请输入选项 (回车默认: high / 输入 0 清空): ") {
		t.Fatalf("expected default-first prompt output, got:\n%s", output)
	}
}

func TestRuntimeModelSelectionOptions_UsesStableOrdering(t *testing.T) {
	provider := config.Provider{
		DefaultModel: "deepseek-ai/DeepSeek-V4-Pro",
		SupportedModels: []string{
			"deepseek-ai/DeepSeek-V4-Flash",
			"deepseek-ai/DeepSeek-V4-Pro",
		},
	}

	session := &ChatSession{
		Provider: provider,
		Model:    "deepseek-ai/DeepSeek-V4-Pro",
	}
	options := runtimeModelSelectionOptions(session)
	if len(options) != 2 {
		t.Fatalf("expected 2 options, got %v", options)
	}
	if options[0] != "deepseek-ai/DeepSeek-V4-Flash" || options[1] != "deepseek-ai/DeepSeek-V4-Pro" {
		t.Fatalf("expected stable sorted order, got %v", options)
	}

	session.Model = "deepseek-ai/DeepSeek-V4-Flash"
	options = runtimeModelSelectionOptions(session)
	if len(options) != 2 {
		t.Fatalf("expected 2 options after model switch, got %v", options)
	}
	if options[0] != "deepseek-ai/DeepSeek-V4-Flash" || options[1] != "deepseek-ai/DeepSeek-V4-Pro" {
		t.Fatalf("expected stable sorted order after model switch, got %v", options)
	}
}

func TestHandleCommand_ModelSwitchAppliesMappingAndClearsUnsupportedReasoning(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := config.Provider{
		Enabled:      true,
		Protocol:     "openai",
		BaseURL:      "https://api.example.com",
		ForwardURL:   "/v1/{model}/responses",
		DefaultModel: "legacy-model",
		ModelMappings: map[string]string{
			"alias-model": "canonical-model",
		},
		ModelCapabilities: map[string]config.ModelCapabilitySpec{
			"canonical-model": {
				ReasoningEfforts: []string{"low", "medium"},
			},
		},
	}

	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        provider,
		Adapter:         adapter.GetAdapterOrDefault("openai"),
		Model:           "legacy-model",
		ReasoningEffort: "high",
		BaseURL:         buildProviderURL(provider, adapter.GetAdapterOrDefault("openai").GetAPIPath(), "legacy-model"),
		SessionManager:  manager,
		RuntimeSession:  runtimeSession,
		SessionUserID:   userID,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/model alias-model", true); quit {
			t.Fatal("expected /model command not to exit")
		}
	})

	expectedBaseURL := buildProviderURL(provider, adapter.GetAdapterOrDefault("openai").GetAPIPath(), "canonical-model")
	if session.Model != "canonical-model" {
		t.Fatalf("expected mapped model canonical-model, got %q", session.Model)
	}
	if session.ReasoningEffort != "" {
		t.Fatalf("expected unsupported reasoning effort to be cleared, got %q", session.ReasoningEffort)
	}
	if session.BaseURL != expectedBaseURL {
		t.Fatalf("expected base URL %q, got %q", expectedBaseURL, session.BaseURL)
	}
	if !strings.Contains(output, "模型已映射 alias-model -> canonical-model") {
		t.Fatalf("expected mapping notice, got:\n%s", output)
	}

	stored, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	if got := runtimeSessionContextString(stored, chatRuntimeContextModel); got != "canonical-model" {
		t.Fatalf("expected stored model canonical-model, got %q", got)
	}
	if got := runtimeSessionContextString(stored, chatRuntimeContextReasoningEffort); got != "" {
		t.Fatalf("expected stored reasoning effort to be cleared, got %q", got)
	}
}

func TestHandleCommand_ModelPromptKeepsCurrentModelAndUsesPriorityReasoningSelection(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := config.Provider{
		Enabled:      true,
		Protocol:     "codex",
		BaseURL:      "https://api.example.com",
		ForwardURL:   "/v1/{model}/responses",
		DefaultModel: "gpt-4.1",
		SupportedModels: []string{
			"gpt-4.1",
			"gpt-4.1-mini",
		},
		ModelCapabilities: map[string]config.ModelCapabilitySpec{
			"gpt-4.1": {
				ReasoningEfforts: []string{"low", "medium"},
			},
		},
	}
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("\n2\n")))
	queue.lines <- chatQueuedInput{Text: "stale-input\n", Source: "stdin"}

	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        provider,
		Adapter:         adapter.GetAdapterOrDefault("openai"),
		Model:           "gpt-4.1",
		ReasoningEffort: "low",
		BaseURL:         buildProviderURL(provider, adapter.GetAdapterOrDefault("openai").GetAPIPath(), "gpt-4.1"),
		SessionManager:  manager,
		RuntimeSession:  runtimeSession,
		SessionUserID:   userID,
		InputQueue:      queue,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/model", false); quit {
			t.Fatal("expected /model command not to exit")
		}
	})

	if session.Model != "gpt-4.1" {
		t.Fatalf("expected current model to be preserved, got %q", session.Model)
	}
	if session.ReasoningEffort != "medium" {
		t.Fatalf("expected reasoning effort to switch to medium, got %q", session.ReasoningEffort)
	}
	if !strings.Contains(output, "模型选择前丢弃这些输入") {
		t.Fatalf("expected stale input discard notice, got:\n%s", output)
	}
	if !strings.Contains(output, "当前模型: gpt-4.1") {
		t.Fatalf("expected current model summary, got:\n%s", output)
	}
	if !strings.Contains(output, "当前 reasoning_effort: medium") {
		t.Fatalf("expected reasoning effort summary, got:\n%s", output)
	}

	stored, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	if got := runtimeSessionContextString(stored, chatRuntimeContextModel); got != "gpt-4.1" {
		t.Fatalf("expected stored model gpt-4.1, got %q", got)
	}
	if got := runtimeSessionContextString(stored, chatRuntimeContextReasoningEffort); got != "medium" {
		t.Fatalf("expected stored reasoning effort medium, got %q", got)
	}
}
