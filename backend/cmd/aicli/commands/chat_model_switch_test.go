package commands

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
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

func TestHandleCommand_ModelPromptCancelIsSilent(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.setExternalInputCaptureActive(true)
	queue.signalReadError(ui.ErrInteractiveInputInterrupted)

	session := &ChatSession{
		ProviderName: "alpha",
		Provider: config.Provider{
			Enabled:      true,
			Protocol:     "openai",
			DefaultModel: "gpt-4.1",
		},
		Model:      "gpt-4.1",
		InputQueue: queue,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/model", false); quit {
			t.Fatal("expected /model command not to exit")
		}
	})

	if strings.Contains(output, "错误:") || strings.Contains(output, "interactive input") {
		t.Fatalf("expected cancelled model prompt to avoid error output, got:\n%s", output)
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
		var usedPopup bool
		selected, usedPopup, err = selectRuntimeReasoningEffort(session, "", []string{"high", "max"})
		if err != nil {
			t.Fatalf("selectRuntimeReasoningEffort: %v", err)
		}
		if usedPopup {
			t.Fatal("expected legacy reasoning selection path without popup")
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

func TestRenderSelectionPopupLines_ShowsCurrentValueAndHint(t *testing.T) {
	lines := renderSelectionPopupLines(
		"选择模型",
		"模型",
		"gpt-4.1",
		[]string{"gpt-4.1", "gpt-4.1-mini"},
		"gpt-4.1",
		"",
		"  提示: ↑↓ 选择，回车确认高亮项；也可输入编号或模型名",
		"  [input] 检测到 1 条待处理输入；已在模型选择期间临时挂起，结束后将按原顺序恢复。",
		"",
		0,
	)

	rendered := strings.Join(lines, "\n")
	for _, expected := range []string{
		"选择模型",
		"当前模型: gpt-4.1",
		">[1] gpt-4.1",
		"(当前)",
		" [2] gpt-4.1-mini",
		"提示: ↑↓ 选择，回车确认高亮项；也可输入编号或模型名",
		"模型选择期间临时挂起",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected popup lines to contain %q, got:\n%s", expected, rendered)
		}
	}
}

func TestRenderSelectionPopupLinesAlignsCJKOptionsByDisplayWidth(t *testing.T) {
	lines := renderSelectionPopupLines(
		"选择模型",
		"模型",
		"模型甲",
		[]string{"模型甲", "gpt"},
		"模型甲",
		"gpt",
		"",
		"",
		"",
		0,
	)

	markerColumns := make([]int, 0, 2)
	for _, line := range lines {
		marker := "(当前)"
		if !strings.Contains(line, marker) {
			marker = "(默认)"
		}
		if index := strings.Index(line, marker); index >= 0 {
			markerColumns = append(markerColumns, ui.DisplayWidth(line[:index]))
		}
	}
	if len(markerColumns) != 2 || markerColumns[0] != markerColumns[1] {
		t.Fatalf("expected CJK and ASCII markers to align, got columns %#v from %#v", markerColumns, lines)
	}
}

func TestPrepareRuntimeSelectionInputSuspendsAndRestoresQueuedMessages(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.routeInputText("follow up\n")
	session := &ChatSession{InputQueue: queue}
	session.Interaction = newChatInteractionCoordinator(session)

	notice, restore := prepareRuntimeSelectionInput(session, "模型选择")
	if !strings.Contains(notice, "临时挂起") || strings.Contains(notice, "丢弃") {
		t.Fatalf("expected suspension notice, got %q", notice)
	}
	if got := queue.pendingCount(); got != 0 {
		t.Fatalf("expected queue to be suspended, got %d pending inputs", got)
	}
	if got := session.Interaction.InputMode(); got != chatInputModeSelection {
		t.Fatalf("expected selection input mode, got %q", got)
	}

	restore()
	if got := session.Interaction.InputMode(); got != chatInputModeChat {
		t.Fatalf("expected chat input mode after restore, got %q", got)
	}
	line, ok := queue.readAvailableLine()
	if !ok || normalizeQueuedInputLine(line) != "follow up" {
		t.Fatalf("expected queued input to be restored, got %q ok=%v", line, ok)
	}
}

func TestResolveRuntimeSelectionInput_SupportsNumericCustomAndBlank(t *testing.T) {
	options := []string{"gpt-4.1", "gpt-4.1-mini"}

	if got, ok := resolveRuntimeSelectionInput("", "gpt-4.1", "", options, true, false); !ok || got != "gpt-4.1" {
		t.Fatalf("expected blank input to keep current model, got %q ok=%v", got, ok)
	}
	if got, ok := resolveRuntimeSelectionInput("2", "gpt-4.1", "", options, true, false); !ok || got != "gpt-4.1-mini" {
		t.Fatalf("expected numeric selection to pick second option, got %q ok=%v", got, ok)
	}
	if got, ok := resolveRuntimeSelectionInput("custom-model", "gpt-4.1", "", options, true, false); !ok || got != "custom-model" {
		t.Fatalf("expected custom input to pass through, got %q ok=%v", got, ok)
	}
	if _, ok := resolveRuntimeSelectionInput("9", "gpt-4.1", "", options, true, false); ok {
		t.Fatal("expected out-of-range numeric choice to be rejected")
	}
}

func TestResolveRuntimeReasoningEffortInput_SupportsBlankDefaultAndClear(t *testing.T) {
	options := []string{"high", "max"}

	if got, ok := resolveRuntimeReasoningEffortInput("", "high", true, "", options); !ok || got != "high" {
		t.Fatalf("expected blank input to keep current effort, got %q ok=%v", got, ok)
	}
	if got, ok := resolveRuntimeReasoningEffortInput("", "", false, "high", options); !ok || got != "high" {
		t.Fatalf("expected blank input to pick default effort, got %q ok=%v", got, ok)
	}
	if got, ok := resolveRuntimeReasoningEffortInput("0", "high", true, "", options); !ok || got != "" {
		t.Fatalf("expected clear token to empty effort, got %q ok=%v", got, ok)
	}
	if got, ok := resolveRuntimeReasoningEffortInput("2", "high", true, "", options); !ok || got != "max" {
		t.Fatalf("expected numeric selection to pick max, got %q ok=%v", got, ok)
	}
	if got, ok := resolveRuntimeReasoningEffortInput("medium", "high", true, "", options); !ok || got != "medium" {
		t.Fatalf("expected custom effort to be accepted, got %q ok=%v", got, ok)
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
	if session.RequestedModel != "alias-model" || session.EffectiveModel != "canonical-model" {
		t.Fatalf("expected requested/effective model mapping to remain visible, got requested=%q effective=%q", session.RequestedModel, session.EffectiveModel)
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
	if got := sessionmeta.String(stored.Metadata.Context, sessionmeta.RequestedModel); got != "alias-model" {
		t.Fatalf("expected stored requested model alias-model, got %q", got)
	}
	if got := sessionmeta.String(stored.Metadata.Context, sessionmeta.EffectiveModel); got != "canonical-model" {
		t.Fatalf("expected stored effective model canonical-model, got %q", got)
	}
	if got := runtimeSessionContextString(stored, toolbroker.AgentSessionContextRequestedModel); got != "canonical-model" {
		t.Fatalf("expected stored requested model canonical-model, got %q", got)
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
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.lines <- chatQueuedInput{Text: "queued-follow-up\n", Source: "stdin"}
	// Pre-load priority inputs for provider selector (empty = accept current),
	// model selector (empty = accept current) and reasoning effort selector
	// ("2" = select second option).
	queue.priorityLines <- chatQueuedInput{Text: "", Source: "stdin"}
	queue.priorityLines <- chatQueuedInput{Text: "", Source: "stdin"}
	queue.priorityLines <- chatQueuedInput{Text: "2", Source: "stdin"}

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
	if !strings.Contains(output, "provider 选择期间临时挂起") {
		t.Fatalf("expected queued input suspension notice, got:\n%s", output)
	}
	if !strings.Contains(output, "当前模型: gpt-4.1") {
		t.Fatalf("expected current model summary, got:\n%s", output)
	}
	if !strings.Contains(output, "当前 reasoning_effort: medium") {
		t.Fatalf("expected reasoning effort summary, got:\n%s", output)
	}
	line, ok := queue.readAvailableLine()
	if !ok || normalizeQueuedInputLine(line) != "queued-follow-up" {
		t.Fatalf("expected queued follow-up to be restored, got %q ok=%v", line, ok)
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
func TestPrintRuntimeModelState_WritesThroughFixedBottomSurfaceAfterPromptClear(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)

	session := &ChatSession{
		ProviderName: "OpenAI-go-away",
		Provider:     config.Provider{Enabled: true, Protocol: "openai", BaseURL: "http://localhost:8080"},
		Model:        "gpt-5.4-mini",
		BaseURL:      "http://localhost:8080/v1",
	}

	output := captureStdout(t, func() {
		coord := newChatInteractionCoordinator(session)
		session.Interaction = coord
		session.Surface = surface
		coord.SetSurface(surface)
		// Rebind after stdout swap so ClearPrompt recognizes the interactive
		// surface writer (writer == os.Stdout) and releases reserved rows.
		coord.SetWriter(os.Stdout)
		coord.promptAdvanceFn = func() bool { return false }
		if !surface.ShowPrompt("> ") {
			t.Fatal("expected surface prompt")
		}
		coord.promptVisible = true
		coord.promptRenderedOnSurface = true
		printRuntimeModelState(session)
	})

	for _, expected := range []string{
		"当前 provider: OpenAI-go-away",
		"当前 protocol: openai",
		"当前模型: gpt-5.4-mini",
		"当前 reasoning_effort: (无)",
		"当前 baseURL: http://localhost:8080/v1",
		// WriteOutput normalizes LFs to CRLF for the surface path.
		"\r\n",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected surface model-state output to contain %q, got:\n%s", expected, output)
		}
	}
	// CSI Ps T is the deferred shrink flush WriteOutput must emit after ClearPrompt.
	if !strings.Contains(output, "\x1b[3T") {
		t.Fatalf("expected WriteOutput to flush pending bottom-reserve shrink before model text, got:\n%q", output)
	}
}
