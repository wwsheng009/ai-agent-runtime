package commands

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestPrepareChatRuntimeState_UsesPersistedChatDefaults(t *testing.T) {
	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]agentconfig.Provider{
				"alpha": {
					Enabled:      true,
					Protocol:     "openai",
					DefaultModel: "alpha-model",
				},
				"beta": {
					Enabled:      true,
					Protocol:     "openai",
					DefaultModel: "beta-model",
				},
			},
		},
		AICLI: &agentconfig.AICLIConfig{
			Chat: &agentconfig.AICLIChatConfig{
				DefaultProvider: "beta",
				DefaultModel:    "beta-model",
				ReasoningEffort: "high",
			},
		},
	}

	state, details, err := prepareChatRuntimeState(cfg, &chatCommandOptions{NoInteractive: true}, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v", err)
	}
	if details != nil {
		t.Fatalf("expected nil details, got %+v", details)
	}
	if state.providerName != "beta" {
		t.Fatalf("expected provider beta, got %q", state.providerName)
	}
	if state.modelName != "beta-model" {
		t.Fatalf("expected model beta-model, got %q", state.modelName)
	}
	if state.reasoningEffort != "high" {
		t.Fatalf("expected reasoning high, got %q", state.reasoningEffort)
	}
}

func TestPrepareChatRuntimeState_DoesNotPersistOnValidationError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://alpha.example.com
      default_model: alpha-model
      supported_models:
        - alpha-model
aicli:
  chat:
    default_provider: alpha
`)
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}

	opts := &chatCommandOptions{
		StreamFlag:    true,
		StreamChanged: true,
		OutputFormat:  "json",
		InputReader:   bufio.NewReader(strings.NewReader("\n")),
	}

	_, _, err = prepareChatRuntimeState(cfg, opts, nil)
	if err == nil {
		t.Fatal("expected validation error for --output json with --stream")
	}
	if !strings.Contains(err.Error(), "--output json 暂不支持与 --stream 同时使用") {
		t.Fatalf("unexpected error: %v", err)
	}

	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(content) != raw {
		t.Fatalf("expected config file to remain unchanged, got:\n%s", string(content))
	}
}

func TestPrepareChatRuntimeState_UsesPersistedStreamPreference(t *testing.T) {
	streamFalse := false
	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]agentconfig.Provider{
				"alpha": {
					Enabled:      true,
					Protocol:     "openai",
					DefaultModel: "alpha-model",
				},
			},
		},
		AICLI: &agentconfig.AICLIConfig{
			Chat: &agentconfig.AICLIChatConfig{
				DefaultProvider: "alpha",
				DefaultModel:    "alpha-model",
				Stream:          &streamFalse,
			},
		},
	}

	state, _, err := prepareChatRuntimeState(cfg, &chatCommandOptions{NoInteractive: true}, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v", err)
	}
	if state.shouldStream {
		t.Fatalf("expected stream=false from persisted preference, got true")
	}
	if state.streamSource != chatPreferenceSourceConfig {
		t.Fatalf("expected stream source=config, got %q", state.streamSource)
	}
}

func TestPersistChatStartupPreferences_SavesInteractiveStreamSelection(t *testing.T) {
	cfg, cfgPath := testModelCommandConfig(t)
	// Drop persisted defaults so the resolver triggers interactive prompts only for stream;
	// provider/model come from config and reasoning is unsupported here, so only stream is interactive.
	cfg.Providers.Items["alpha"] = agentconfig.Provider{
		Enabled:      true,
		Protocol:     "openai",
		DefaultModel: "alpha-model",
		BaseURL:      "https://alpha.example.com",
	}
	cfg.AICLI.Chat.ReasoningEffort = ""

	// Input "1" selects 普通 (stream=false).
	opts := &chatCommandOptions{
		InputReader: bufio.NewReader(strings.NewReader("1\n")),
	}

	state, _, err := prepareChatRuntimeState(cfg, opts, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v", err)
	}
	if state.streamSource != chatPreferenceSourceInteractive {
		t.Fatalf("expected stream source=interactive, got %q", state.streamSource)
	}
	if state.shouldStream {
		t.Fatalf("expected interactive selection of stream=false, got true")
	}

	persistChatStartupPreferences(cfg, opts, nil, state)

	loaded, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Chat == nil || loaded.AICLI.Chat.Stream == nil {
		t.Fatalf("expected persisted aicli.chat.stream, got %+v", loaded.AICLI)
	}
	if *loaded.AICLI.Chat.Stream != false {
		t.Fatalf("expected persisted stream=false, got %v", *loaded.AICLI.Chat.Stream)
	}
}

func TestPersistChatStartupPreferences_DoesNotRewriteConfiguredCustomReasoning(t *testing.T) {
	cfg, cfgPath := testModelCommandConfig(t)
	cfg.AICLI.Chat.ReasoningEffort = "ultra"
	cfg.Providers.Items["alpha"] = agentconfig.Provider{
		Enabled:      true,
		Protocol:     "openai",
		DefaultModel: "alpha-model",
		BaseURL:      "https://alpha.example.com",
		ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"alpha-model": {
				ReasoningEfforts: []string{"low", "medium"},
			},
		},
	}

	opts := &chatCommandOptions{
		StreamFlag:    true,
		StreamChanged: true,
		InputReader:   bufio.NewReader(strings.NewReader("0\n")),
	}

	state, details, err := prepareChatRuntimeState(cfg, opts, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v", err)
	}
	if details != nil {
		t.Fatalf("expected nil details, got %+v", details)
	}
	if state.reasoningEffort != "ultra" {
		t.Fatalf("expected custom reasoning effort to be preserved, got %q", state.reasoningEffort)
	}
	persistChatStartupPreferences(cfg, opts, nil, state)

	loaded, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Chat == nil {
		t.Fatalf("expected persisted aicli.chat section, got %+v", loaded.AICLI)
	}
	if loaded.AICLI.Chat.ReasoningEffort != "low" {
		t.Fatalf("expected unmodified persisted reasoning_effort, got %q", loaded.AICLI.Chat.ReasoningEffort)
	}
}

func TestPrepareChatRuntimeStatePreservesConfiguredCustomReasoning(t *testing.T) {
	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]agentconfig.Provider{
				"alpha": {
					Enabled:      true,
					Protocol:     "openai",
					DefaultModel: "alpha-model",
					ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
						"alpha-model": {ReasoningEfforts: []string{"low", "medium"}},
					},
				},
			},
		},
		AICLI: &agentconfig.AICLIConfig{Chat: &agentconfig.AICLIChatConfig{ReasoningEffort: "ultra"}},
	}

	state, _, err := prepareChatRuntimeState(cfg, &chatCommandOptions{NoInteractive: true}, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v", err)
	}
	if state.requestedReasoningEffort != "ultra" || state.reasoningEffort != "ultra" {
		t.Fatalf("expected requested and effective reasoning ultra, got requested=%q effective=%q", state.requestedReasoningEffort, state.reasoningEffort)
	}
}

func TestBootstrapChatSession_DoesNotPersistStartupPreferencesOnFailure(t *testing.T) {
	cfg, cfgPath := testModelCommandConfig(t)
	cfg.Providers.DefaultProvider = ""
	cfg.AICLI.Chat = nil
	cfg.Providers.Items["alpha"] = agentconfig.Provider{
		Enabled:      true,
		Protocol:     "openai",
		DefaultModel: "alpha-model",
		BaseURL:      "https://alpha.example.com",
		SupportedModels: []string{
			"alpha-model",
		},
		ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"alpha-model": {
				ReasoningEfforts: []string{"low"},
			},
		},
	}

	opts := &chatCommandOptions{
		InputReader: bufio.NewReader(strings.NewReader("1\n1\n1\n")),
	}

	runtimeState, _, err := prepareChatRuntimeState(cfg, opts, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v", err)
	}

	invalidSession := runtimechat.NewSession("tester")
	invalidSession.History = []runtimetypes.Message{
		{
			Role:     "",
			Metadata: runtimetypes.NewMetadata(),
		},
	}
	persistenceState := &chatPersistenceState{
		loadedRuntimeSession: invalidSession,
	}

	_, cleanup, err := bootstrapChatSession(cfg, opts, nil, persistenceState, runtimeState)
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("expected bootstrapChatSession to fail for invalid restored history")
	}

	loaded, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Chat == nil {
		t.Fatalf("expected config file to remain unchanged on bootstrap failure, got %+v", loaded.AICLI)
	}
	if loaded.AICLI.Chat.DefaultProvider != "alpha" {
		t.Fatalf("expected default_provider alpha to remain unchanged, got %q", loaded.AICLI.Chat.DefaultProvider)
	}
	if loaded.AICLI.Chat.DefaultModel != "alpha-model" {
		t.Fatalf("expected default_model alpha-model to remain unchanged, got %q", loaded.AICLI.Chat.DefaultModel)
	}
	if loaded.AICLI.Chat.ReasoningEffort != "low" {
		t.Fatalf("expected reasoning_effort low to remain unchanged, got %q", loaded.AICLI.Chat.ReasoningEffort)
	}
}

func TestHandleCommand_ModelSwitchPersistsProviderModelAndReasoning(t *testing.T) {
	cfg, cfgPath := testModelCommandConfig(t)

	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        cfg.Providers.Items["alpha"],
		Adapter:         adapter.GetAdapterOrDefault("openai"),
		Model:           "alpha-model",
		ReasoningEffort: "low",
		BaseURL:         buildProviderURL(cfg.Providers.Items["alpha"], adapter.GetAdapterOrDefault("openai").GetAPIPath(), "alpha-model"),
		Config:          cfg,
	}

	if quit := handleCommand(session, "/model --provider beta --model beta-model --reasoning-effort medium", true); quit {
		t.Fatal("expected /model command not to exit")
	}

	if session.ProviderName != "beta" {
		t.Fatalf("expected provider beta, got %q", session.ProviderName)
	}
	if session.Model != "beta-model" {
		t.Fatalf("expected model beta-model, got %q", session.Model)
	}
	if session.ReasoningEffort != "medium" {
		t.Fatalf("expected reasoning medium, got %q", session.ReasoningEffort)
	}
	if session.Adapter == nil || session.FunctionBuilder == nil || session.HTTPClient == nil {
		t.Fatal("expected runtime transport state to be refreshed")
	}

	loaded, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Chat == nil {
		t.Fatalf("expected persisted aicli.chat section, got %+v", loaded.AICLI)
	}
	if loaded.AICLI.Chat.DefaultProvider != "beta" {
		t.Fatalf("expected persisted default_provider beta, got %q", loaded.AICLI.Chat.DefaultProvider)
	}
	if loaded.AICLI.Chat.DefaultModel != "beta-model" {
		t.Fatalf("expected persisted default_model beta-model, got %q", loaded.AICLI.Chat.DefaultModel)
	}
	if loaded.AICLI.Chat.ReasoningEffort != "medium" {
		t.Fatalf("expected persisted reasoning_effort medium, got %q", loaded.AICLI.Chat.ReasoningEffort)
	}
}

func TestHandleCommand_ModelDirectFlagSuppressesInteractiveReasoningPrompt(t *testing.T) {
	// web 注入场景:noInteractive=false(真实交互会话)但命令带 --direct,
	// 必须跳过 reasoning 交互提示直接落盘,保持当前 effort——注入方无法驱动
	// TUI 键盘,弹交互选择会让 TUI 卡住、web 端轮询超时。
	cfg, _ := testModelCommandConfig(t)

	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        cfg.Providers.Items["alpha"],
		Adapter:         adapter.GetAdapterOrDefault("openai"),
		Model:           "alpha-model",
		ReasoningEffort: "low",
		BaseURL:         buildProviderURL(cfg.Providers.Items["alpha"], adapter.GetAdapterOrDefault("openai").GetAPIPath(), "alpha-model"),
		Config:          cfg,
	}

	if quit := handleCommand(session, "/model --provider beta --model beta-model --direct", false); quit {
		t.Fatal("expected /model command not to exit")
	}

	if session.ProviderName != "beta" {
		t.Fatalf("expected provider beta, got %q", session.ProviderName)
	}
	if session.Model != "beta-model" {
		t.Fatalf("expected model beta-model, got %q", session.Model)
	}
	if session.ReasoningEffort != "low" {
		t.Fatalf("--direct apply must keep the current reasoning, got %q", session.ReasoningEffort)
	}
}

func TestHandleCommand_ModelSwitchReloadsNewProviderFromLocalConfig(t *testing.T) {
	cfg, cfgPath := testModelCommandConfig(t)

	// Simulate another command/user adding a provider after chat startup. The
	// session still points at the startup snapshot in cfg.
	enabled := true
	protocol := "openai"
	baseURL := "https://gamma.example.com"
	defaultModel := "gamma-model"
	if _, err := agentconfig.UpdateProviderConfig(cfgPath, agentconfig.ProviderConfigUpdate{
		Name:         "gamma",
		Enabled:      &enabled,
		Protocol:     &protocol,
		BaseURL:      &baseURL,
		DefaultModel: &defaultModel,
	}); err != nil {
		t.Fatalf("add provider to local config: %v", err)
	}
	if _, ok := cfg.Providers.Items["gamma"]; ok {
		t.Fatal("test setup unexpectedly mutated the startup config snapshot")
	}

	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        cfg.Providers.Items["alpha"],
		Adapter:         adapter.GetAdapterOrDefault("openai"),
		Model:           "alpha-model",
		ReasoningEffort: "low",
		Config:          cfg,
	}

	if quit := handleCommand(session, "/model --provider gamma --model gamma-model --reasoning-effort medium", true); quit {
		t.Fatal("expected /model command not to exit")
	}
	if session.ProviderName != "gamma" {
		t.Fatalf("expected provider gamma after reloading config, got %q", session.ProviderName)
	}
	if session.Model != "gamma-model" {
		t.Fatalf("expected model gamma-model after reloading config, got %q", session.Model)
	}
	if session.Config == cfg {
		t.Fatal("expected /model to replace the stale config snapshot")
	}
	if _, ok := session.Config.Providers.Items["gamma"]; !ok {
		t.Fatalf("reloaded config does not contain newly added provider: %+v", session.Config.Providers.Items)
	}
}

func TestExecuteStructuredModelMutationReloadsNewProviderFromLocalConfig(t *testing.T) {
	cfg, cfgPath := testModelCommandConfig(t)

	enabled := true
	protocol := "openai"
	baseURL := "https://gamma.example.com"
	defaultModel := "gamma-model"
	if _, err := agentconfig.UpdateProviderConfig(cfgPath, agentconfig.ProviderConfigUpdate{
		Name:         "gamma",
		Enabled:      &enabled,
		Protocol:     &protocol,
		BaseURL:      &baseURL,
		DefaultModel: &defaultModel,
	}); err != nil {
		t.Fatalf("add provider to local config: %v", err)
	}

	session := &ChatSession{
		ProviderName:    "alpha",
		Provider:        cfg.Providers.Items["alpha"],
		Model:           "alpha-model",
		ReasoningEffort: "low",
		Config:          cfg,
	}
	result := executeStructuredModelMutation(session, modelCommandRequest{
		Provider:          "gamma",
		Model:             "gamma-model",
		ReasoningEffort:   "medium",
		ProviderExplicit:  true,
		ModelExplicit:     true,
		ReasoningExplicit: true,
	})
	if session.ProviderName != "gamma" || session.Model != "gamma-model" {
		t.Fatalf("structured /model mutation result=%+v; expected switch to gamma/gamma-model, got provider=%q model=%q",
			result, session.ProviderName, session.Model)
	}
}

func TestReloadChatConfigForModelCommandKeepsInMemoryConfigWhenPathIsMissing(t *testing.T) {
	cfg := &agentconfig.Config{
		ConfigFilePath: filepath.Join(t.TempDir(), "missing.yaml"),
		Providers: agentconfig.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]agentconfig.Provider{
				"alpha": {
					Enabled:      true,
					Protocol:     "openai",
					DefaultModel: "alpha-model",
				},
			},
		},
	}
	session := &ChatSession{Config: cfg}

	if err := reloadChatConfigForModelCommand(session); err != nil {
		t.Fatalf("reloadChatConfigForModelCommand: %v", err)
	}
	if session.Config != cfg {
		t.Fatal("missing config path must not replace an in-memory session config")
	}
	if _, ok := session.Config.Providers.Items["alpha"]; !ok {
		t.Fatal("missing config path unexpectedly discarded in-memory providers")
	}
}

func TestReloadChatConfigForModelCommandRejectsDirectory(t *testing.T) {
	cfg := &agentconfig.Config{
		ConfigFilePath: t.TempDir(),
		Providers: agentconfig.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]agentconfig.Provider{
				"alpha": {Enabled: true, Protocol: "openai", DefaultModel: "alpha-model"},
			},
		},
	}
	session := &ChatSession{Config: cfg}

	err := reloadChatConfigForModelCommand(session)
	if err == nil {
		t.Fatal("expected directory config path to be rejected")
	}
	if !strings.Contains(err.Error(), "路径是目录") {
		t.Fatalf("unexpected directory error: %v", err)
	}
	if session.Config != cfg {
		t.Fatal("directory error must preserve the previous session config")
	}
}

func TestReloadChatConfigForModelCommandKeepsPreviousConfigOnParseError(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("providers:\n  items: [\n"), 0o644); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}
	cfg := &agentconfig.Config{
		ConfigFilePath: cfgPath,
		Providers: agentconfig.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]agentconfig.Provider{
				"alpha": {Enabled: true, Protocol: "openai", DefaultModel: "alpha-model"},
			},
		},
	}
	session := &ChatSession{Config: cfg}

	err := reloadChatConfigForModelCommand(session)
	if err == nil {
		t.Fatal("expected invalid config to return an error")
	}
	if !strings.Contains(err.Error(), "重新读取本地配置文件") {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if session.Config != cfg {
		t.Fatal("parse error must preserve the previous session config")
	}
	if _, ok := session.Config.Providers.Items["alpha"]; !ok {
		t.Fatal("parse error unexpectedly discarded the previous provider")
	}
}

func TestHandleCommand_ModelClearReasoningPersistsPreference(t *testing.T) {
	cfg, cfgPath := testModelCommandConfig(t)

	session := &ChatSession{
		ProviderName:    "beta",
		Provider:        cfg.Providers.Items["beta"],
		Adapter:         adapter.GetAdapterOrDefault("openai"),
		Model:           "beta-model",
		ReasoningEffort: "high",
		BaseURL:         buildProviderURL(cfg.Providers.Items["beta"], adapter.GetAdapterOrDefault("openai").GetAPIPath(), "beta-model"),
		Config:          cfg,
	}

	if quit := handleCommand(session, "/model clear-reasoning", true); quit {
		t.Fatal("expected /model command not to exit")
	}
	if session.ReasoningEffort != "" {
		t.Fatalf("expected reasoning to be cleared, got %q", session.ReasoningEffort)
	}

	loaded, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Chat == nil {
		t.Fatalf("expected persisted aicli.chat section, got %+v", loaded.AICLI)
	}
	if loaded.AICLI.Chat.ReasoningEffort != "" {
		t.Fatalf("expected cleared reasoning_effort, got %q", loaded.AICLI.Chat.ReasoningEffort)
	}
}

func testModelCommandConfig(t *testing.T) (*agentconfig.Config, string) {
	t.Helper()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://alpha.example.com
      default_model: alpha-model
    beta:
      enabled: true
      protocol: openai
      base_url: https://beta.example.com
      default_model: beta-model
aicli:
  chat:
    default_provider: alpha
    default_model: alpha-model
    reasoning_effort: low
`)
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	return cfg, cfgPath
}
