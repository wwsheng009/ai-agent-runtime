package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestExecuteStructuredModelCommandStatusStaysReadOnly(t *testing.T) {
	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        config.Provider{Protocol: "openai", DefaultModel: "gpt-4.1"},
		Model:           "gpt-4.1",
		ReasoningEffort: "medium",
	}
	result, handled := executeStructuredModelCommand(session, "/model status")
	if !handled {
		t.Fatal("/model status was not handled by the structured executor")
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "当前 provider: alpha") || !strings.Contains(text, "当前模型: gpt-4.1") {
		t.Fatalf("/model status document missing state, got:\n%s", text)
	}
	if result.OpenModelPicker != nil {
		t.Fatalf("/model status must not open the picker: %#v", result.OpenModelPicker)
	}
}

func TestExecuteStructuredModelCommandBareWithoutSurfaceDegradesToStatus(t *testing.T) {
	// No interaction/surface on this session: canOpenChatModelPicker fails, so
	// bare /model must degrade to the read-only status document instead of a
	// legacy prompt or the migration fence.
	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        config.Provider{Protocol: "openai", DefaultModel: "gpt-4.1"},
		Model:           "gpt-4.1",
		ReasoningEffort: "medium",
		PermissionMode:  runtimepolicy.ModeDefault,
	}
	result, handled := executeStructuredModelCommand(session, "/model")
	if !handled {
		t.Fatal("bare /model was not handled by the structured executor")
	}
	if result.OpenModelPicker != nil {
		t.Fatalf("bare /model without a picker-capable surface must not request the typed picker, got %#v", result.OpenModelPicker)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "当前 provider: alpha") || !strings.Contains(text, "当前模型: gpt-4.1") {
		t.Fatalf("bare /model must degrade to the status document, got:\n%s", text)
	}
}

func TestModelPickerRequestCarriesReasoningStage(t *testing.T) {
	// The reasoning-only mutation variant pins provider/model and asks only for
	// the reasoning stage; this mirrors the immutable-request contract of the
	// resume/backtrack pickers.
	request := ModelPickerRequest{
		Provider:      "alpha",
		Model:         "gpt-4.1-mini",
		NeedReasoning: true,
	}
	if !request.NeedReasoning {
		t.Fatal("reasoning-only picker request lost the NeedReasoning flag")
	}
	if request.Provider != "alpha" || request.Model != "gpt-4.1-mini" {
		t.Fatalf("reasoning-only picker request must preserve pinned provider/model, got %#v", request)
	}
}

func TestExecuteStructuredModelCommandExplicitMutationAppliesDirectly(t *testing.T) {
	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        config.Provider{Protocol: "openai", DefaultModel: "gpt-4.1"},
		Model:           "gpt-4.1",
		ReasoningEffort: "medium",
		PermissionMode:  runtimepolicy.ModeDefault,
	}
	result, handled := executeStructuredModelCommand(session, "/model --model gpt-4.1-mini -r low")
	if !handled {
		t.Fatal("explicit mutation was not handled by the structured executor")
	}
	if result.OpenModelPicker != nil {
		t.Fatalf("explicit mutation must not open the picker, got %#v", result.OpenModelPicker)
	}
	if session.Model != "gpt-4.1-mini" {
		t.Fatalf("expected model switch to gpt-4.1-mini, got %q", session.Model)
	}
	if session.ReasoningEffort != "low" {
		t.Fatalf("expected reasoning effort low, got %q", session.ReasoningEffort)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "当前模型: gpt-4.1-mini") {
		t.Fatalf("mutation result document missing new model, got:\n%s", text)
	}
}

func TestExecuteStructuredModelCommandDirectFlagSkipsReasoningPicker(t *testing.T) {
	// web 客户端注入的 /model 命令带 --direct:即便存在可用的 picker surface
	// 也必须直接落盘,不得请求 reasoning 选择阶段。注入方无法驱动 TUI 键盘,
	// 弹 picker 会让 TUI 卡在全屏选择器、web 端轮询超时。
	request, err := parseModelCommandRequest("/model --model gpt-4.1-mini --direct")
	if err != nil {
		t.Fatalf("parse --direct: %v", err)
	}
	if !request.DirectApply {
		t.Fatal("--direct must set DirectApply")
	}
	if !request.ModelExplicit {
		t.Fatal("--direct must not swallow --model")
	}

	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        config.Provider{Protocol: "openai", DefaultModel: "gpt-4.1"},
		Model:           "gpt-4.1",
		ReasoningEffort: "medium",
		PermissionMode:  runtimepolicy.ModeDefault,
	}
	result, handled := executeStructuredModelCommand(session, "/model --model gpt-4.1-mini --direct")
	if !handled {
		t.Fatal("--direct mutation was not handled by the structured executor")
	}
	if result.OpenModelPicker != nil {
		t.Fatalf("--direct must never open the picker, got %#v", result.OpenModelPicker)
	}
	if session.Model != "gpt-4.1-mini" {
		t.Fatalf("expected model switch to gpt-4.1-mini, got %q", session.Model)
	}
	if session.ReasoningEffort != "medium" {
		t.Fatalf("--direct apply must keep the current reasoning, got %q", session.ReasoningEffort)
	}
}

func TestExecuteStructuredModelCommandModelPinnedWithoutReasoningDegradesWithoutSurface(t *testing.T) {
	// ModelExplicit without reasoning normally requests the reasoning picker,
	// but on this session canOpenChatModelPicker fails (no surface), so the
	// mutation degrades to a direct apply that keeps the current reasoning.
	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        config.Provider{Protocol: "openai", DefaultModel: "gpt-4.1"},
		Model:           "gpt-4.1",
		ReasoningEffort: "medium",
		PermissionMode:  runtimepolicy.ModeDefault,
	}
	result, handled := executeStructuredModelCommand(session, "/model --model gpt-4.1-mini")
	if !handled {
		t.Fatal("/model --model was not handled by the structured executor")
	}
	if result.OpenModelPicker != nil {
		t.Fatalf("without a picker-capable surface the mutation must not open the picker, got %#v", result.OpenModelPicker)
	}
	if session.Model != "gpt-4.1-mini" {
		t.Fatalf("expected model switch to gpt-4.1-mini, got %q", session.Model)
	}
	if session.ReasoningEffort != "medium" {
		t.Fatalf("degraded apply must keep the current reasoning, got %q", session.ReasoningEffort)
	}
}

func TestExecuteStructuredModelCommandClearReasoningAppliesDirectly(t *testing.T) {
	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        config.Provider{Protocol: "openai", DefaultModel: "gpt-4.1"},
		Model:           "gpt-4.1",
		ReasoningEffort: "medium",
		PermissionMode:  runtimepolicy.ModeDefault,
	}
	result, handled := executeStructuredModelCommand(session, "/model --clear-reasoning")
	if !handled {
		t.Fatal("/model --clear-reasoning was not handled by the structured executor")
	}
	if result.OpenModelPicker != nil {
		t.Fatalf("clear-reasoning must not open the picker, got %#v", result.OpenModelPicker)
	}
	if session.ReasoningEffort != "" {
		t.Fatalf("expected cleared reasoning effort, got %q", session.ReasoningEffort)
	}
	if session.Model != "gpt-4.1" {
		t.Fatalf("clear-reasoning must not change the model, got %q", session.Model)
	}
}

func TestExecuteStructuredModelCommandInvalidArgsReportError(t *testing.T) {
	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        config.Provider{Protocol: "openai", DefaultModel: "gpt-4.1"},
		Model:           "gpt-4.1",
		ReasoningEffort: "medium",
	}
	result, handled := executeStructuredModelCommand(session, "/model --bogus-flag")
	if !handled {
		t.Fatal("invalid /model args must be handled by the structured executor")
	}
	if result.OpenModelPicker != nil {
		t.Fatalf("invalid args must not open the picker, got %#v", result.OpenModelPicker)
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "未知的 /model 参数") {
		t.Fatalf("invalid args must report the parse error, got:\n%s", ui.RenderDocumentPlain(result.Document()))
	}
}

func TestModelPickerModelOptionsDeduplicateAndSort(t *testing.T) {
	provider := config.Provider{
		DefaultModel:    "gpt-4.1",
		SupportedModels: []string{"gpt-4.1-mini", "gpt-4.1", "gpt-5"},
	}
	options := modelPickerModelOptions(provider, "gpt-4.1")
	seen := map[string]bool{}
	for _, option := range options {
		if seen[option] {
			t.Fatalf("duplicate model option %q in %#v", option, options)
		}
		seen[option] = true
	}
	if len(options) != 3 {
		t.Fatalf("expected 3 deduplicated models, got %#v", options)
	}
	if options[0] != "gpt-4.1" {
		t.Fatalf("expected current model first, got %#v", options)
	}
}

func TestCanOpenChatModelPickerRequiresUnifiedSurface(t *testing.T) {
	if canOpenChatModelPicker(nil) {
		t.Fatal("nil session must not open the model picker")
	}
	session := &ChatSession{ProviderName: "alpha"}
	if canOpenChatModelPicker(session) {
		t.Fatal("bare session without interaction/surface must not open the model picker")
	}
}

func TestExecuteStructuredProviderCommandBareRequestsProviderPicker(t *testing.T) {
	// The provider stage selector decision: only a bare /provider (no pinned
	// provider) leads with the provider stage; /model and a pinned provider
	// never do.
	if !needProviderPickerStage(modelCommandVariantProvider, false) {
		t.Fatal("bare /provider must lead with the provider stage")
	}
	if needProviderPickerStage(modelCommandVariantProvider, true) {
		t.Fatal("/provider with a pinned provider must not re-ask for it")
	}
	if needProviderPickerStage(modelCommandVariantModel, false) {
		t.Fatal("/model must never lead with the provider stage")
	}
}

func TestExecuteStructuredProviderCommandBareWithoutSurfaceDegradesToStatus(t *testing.T) {
	// No picker-capable surface: bare /provider degrades to the read-only
	// status document just like bare /model.
	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        config.Provider{Protocol: "openai", DefaultModel: "gpt-4.1"},
		Model:           "gpt-4.1",
		ReasoningEffort: "medium",
	}
	result, handled := executeStructuredProviderCommand(session, "/provider")
	if !handled {
		t.Fatal("bare /provider without a surface was not handled")
	}
	if result.OpenModelPicker != nil {
		t.Fatalf("bare /provider without a surface must not open the picker, got %#v", result.OpenModelPicker)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "当前 provider: alpha") || !strings.Contains(text, "当前模型: gpt-4.1") {
		t.Fatalf("bare /provider must degrade to the status document, got:\n%s", text)
	}
}

func TestExecuteStructuredProviderCommandPinnedProviderSkipsPickerStage(t *testing.T) {
	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        config.Provider{Protocol: "openai", DefaultModel: "gpt-4.1"},
		Model:           "gpt-4.1",
		ReasoningEffort: "medium",
		Config: &config.Config{
			Providers: config.ProvidersConfig{
				DefaultProvider: "alpha",
				Items: map[string]config.Provider{
					"alpha": {
						Enabled:      true,
						Protocol:     "openai",
						DefaultModel: "gpt-4.1",
					},
					"beta": {
						Enabled:      true,
						Protocol:     "openai",
						DefaultModel: "gpt-4.1-mini",
					},
				},
			},
		},
	}
	result, handled := executeStructuredProviderCommand(session, "/provider --provider beta --model gpt-4.1-mini")
	if !handled {
		t.Fatal("/provider --provider --model was not handled")
	}
	if result.OpenModelPicker != nil {
		t.Fatal("explicit provider+model must apply directly, not open the picker")
	}
	if session.ProviderName != "beta" {
		t.Fatalf("expected provider switch to beta, got %q", session.ProviderName)
	}
	if session.Model != "gpt-4.1-mini" {
		t.Fatalf("expected model switch to gpt-4.1-mini, got %q", session.Model)
	}
}

func TestExecuteStructuredModelCommandBareDoesNotRequestProviderPicker(t *testing.T) {
	// The /model command is now model-only: bare /model degrades to the
	// read-only status document without ever requesting the provider stage.
	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        config.Provider{Protocol: "openai", DefaultModel: "gpt-4.1"},
		Model:           "gpt-4.1",
		ReasoningEffort: "medium",
	}
	result, handled := executeStructuredModelCommand(session, "/model")
	if !handled {
		t.Fatal("bare /model was not handled by the structured executor")
	}
	if result.OpenModelPicker != nil {
		t.Fatalf("bare /model without a surface must not open the picker, got %#v", result.OpenModelPicker)
	}
}

func TestResolveModelPickerProviderTiesModelToSelectedProvider(t *testing.T) {
	// Regression: after /provider selects beta (session.ProviderName=beta),
	// bare /model must resolve its interactive model stage against beta, not
	// the config default provider, while a bare /provider must stay empty so
	// the leading provider stage runs.
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]config.Provider{
				"alpha": {Enabled: true, Protocol: "openai", DefaultModel: "alpha-model"},
				"beta":  {Enabled: true, Protocol: "openai", DefaultModel: "beta-model"},
			},
		},
	}
	session := &ChatSession{
		ProviderName:    "beta",
		Provider:        cfg.Providers.Items["beta"],
		Model:           "beta-model",
		ReasoningEffort: "medium",
		Config:          cfg,
	}

	if got := resolveModelPickerProvider(session, modelCommandVariantModel, modelCommandRequest{}); got != "beta" {
		t.Fatalf("bare /model must resolve the current provider beta, got %q", got)
	}
	if got := resolveModelPickerProvider(session, modelCommandVariantModel, modelCommandRequest{Provider: "gamma", ProviderExplicit: true}); got != "gamma" {
		t.Fatalf("explicit /model --provider gamma must win over the current provider, got %q", got)
	}
	if got := resolveModelPickerProvider(session, modelCommandVariantProvider, modelCommandRequest{}); got != "" {
		t.Fatalf("bare /provider must stay empty so the leading provider stage runs, got %q", got)
	}
	if got := resolveModelPickerProvider(session, modelCommandVariantProvider, modelCommandRequest{Provider: "gamma", ProviderExplicit: true}); got != "gamma" {
		t.Fatalf("/provider --provider gamma must pin gamma, got %q", got)
	}
}
