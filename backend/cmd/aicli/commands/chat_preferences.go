package commands

import (
	"fmt"
	"os"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type chatPreferenceSource string

const (
	chatPreferenceSourceFlag        chatPreferenceSource = "flag"
	chatPreferenceSourceSession     chatPreferenceSource = "session"
	chatPreferenceSourceConfig      chatPreferenceSource = "config"
	chatPreferenceSourceInteractive chatPreferenceSource = "interactive"
	chatPreferenceSourceWorkspace   chatPreferenceSource = "workspace"
	chatPreferenceSourceDefault     chatPreferenceSource = "default"
)

// resolveWorkspaceChatPreferences loads the workspace-scoped chat preferences
// (decision D5): $HOME/.aicli/workspace/<hash>/chat-prefs.yaml. It sits below
// the restored session context and above the global aicli.chat defaults so a
// workspace that already made an interactive choice stops re-prompting, while
// unconfigured workspaces fall back to the global defaults and then the
// interactive selectors. Load errors only warn — preference storage must never
// block startup.
func resolveWorkspaceChatPreferences() *config.AICLIChatConfig {
	prefs, err := config.LoadWorkspaceChatPreferences()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 读取 workspace 偏好失败: %v\n", err)
		return nil
	}
	return prefs
}

func workspacePreferenceString(prefs *config.AICLIChatConfig, value string) (string, bool) {
	if prefs == nil {
		return "", false
	}
	trimmed := strings.TrimSpace(value)
	return trimmed, trimmed != ""
}

func workspacePreferenceBool(prefs *config.AICLIChatConfig, value *bool) (bool, bool) {
	if prefs == nil || value == nil {
		return false, false
	}
	return *value, true
}

// modelBelongsToProvider checks whether a saved preference model is still
// offered by the provider: listed in its catalog, configured as its default,
// or covered by a model mapping. Providers without an explicit catalog accept
// any model (the upstream is authoritative at request time).
func modelBelongsToProvider(provider config.Provider, modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(provider.DefaultModel), modelName) {
		return true
	}
	for _, supported := range provider.SupportedModels {
		if strings.EqualFold(strings.TrimSpace(supported), modelName) {
			return true
		}
	}
	if provider.ModelMappings != nil {
		if _, ok := provider.ModelMappings[modelName]; ok {
			return true
		}
	}
	return len(provider.SupportedModels) == 0
}

func resolveChatProviderChoice(cfg *config.Config, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) (string, chatPreferenceSource) {
	if opts != nil && strings.TrimSpace(opts.ProviderFlag) != "" {
		return strings.TrimSpace(opts.ProviderFlag), chatPreferenceSourceFlag
	}
	if loadedRuntimeSession != nil {
		if storedProvider := runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextProviderName); storedProvider != "" {
			if cfg == nil {
				return storedProvider, chatPreferenceSourceSession
			}
			if canonicalProvider, ok := canonicalEnabledProviderName(cfg, storedProvider); ok {
				return canonicalProvider, chatPreferenceSourceSession
			}
			if storedProtocol := runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextProtocol); storedProtocol != "" {
				if providerName, ok := resolveEnabledProviderNameByProtocol(cfg, storedProtocol); ok {
					return providerName, chatPreferenceSourceSession
				}
			}
			// stored provider 已不在启用集合、也无法按 protocol 兜底时，
			// 不能把失效的名字直接返回——那会让 resolveProviderExecutionContext
			// 硬报 "provider not found" 并使整个 chat 启动失败。降级继续走
			// workspace -> config -> default 的偏好链（session 偏好只在其
			// 指向的 provider 仍可用时生效）。
		}
		if storedProtocol := runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextProtocol); storedProtocol != "" {
			if providerName, ok := resolveEnabledProviderNameByProtocol(cfg, storedProtocol); ok {
				return providerName, chatPreferenceSourceSession
			}
		}
	}

	// Workspace-scoped preference (D5): beats the global aicli.chat default but
	// is still validated against the enabled provider set.
	if prefs := resolveWorkspaceChatPreferences(); prefs != nil {
		if preferred, ok := workspacePreferenceString(prefs, prefs.DefaultProvider); ok {
			if cfg == nil || isEnabledProvider(cfg, preferred) {
				return preferred, chatPreferenceSourceWorkspace
			}
		}
	}

	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil {
		if preferred := strings.TrimSpace(cfg.AICLI.Chat.DefaultProvider); preferred != "" && isEnabledProvider(cfg, preferred) {
			return preferred, chatPreferenceSourceConfig
		}
	}

	if opts != nil && !opts.NoInteractive && !opts.Headless {
		if selected := selectProviderWithReader(cfg, chatOptionInputReader(opts)); selected != "" {
			return selected, chatPreferenceSourceInteractive
		}
	}

	if cfg != nil {
		if defaultProvider := strings.TrimSpace(cfg.Providers.DefaultProvider); defaultProvider != "" {
			return defaultProvider, chatPreferenceSourceDefault
		}
		// 无默认 provider 时兜底选第一个可用 provider，避免启动期阻塞在
		// 交互选择器上（headless/非交互场景是硬需求；交互场景下选择器
		// 分支已在前置分支返回，走到这里意味着用户会直接得到可用 provider）。
		if enabled := listEnabledProviderNames(cfg); len(enabled) > 0 {
			return enabled[0], chatPreferenceSourceDefault
		}
	}
	return "", chatPreferenceSourceDefault
}

// providerSource 传入 provider 的解析来源：当 provider 是用户本轮交互式新选的
// （chatPreferenceSourceInteractive）时，持久化的 aicli.chat.default_model 多半
// 属于旧的 provider，不能用它短路 model 选择器——否则会出现“弹了 provider
// 选择器却不弹 model 选择器，还把旧 provider 的模型套在新 provider 上”。
func resolveChatModelChoice(cfg *config.Config, provider config.Provider, providerSource chatPreferenceSource, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) (string, chatPreferenceSource) {
	if opts != nil && strings.TrimSpace(opts.ModelFlag) != "" {
		return strings.TrimSpace(opts.ModelFlag), chatPreferenceSourceFlag
	}
	if loadedRuntimeSession != nil {
		if storedModel := runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextModel); storedModel != "" {
			return storedModel, chatPreferenceSourceSession
		}
	}

	// Workspace-scoped preference (D5): only short-circuits when the provider
	// was NOT freshly chosen interactively and the saved model still belongs to
	// the resolved provider — same guard the global aicli.chat default gets.
	if providerSource != chatPreferenceSourceInteractive {
		if prefs := resolveWorkspaceChatPreferences(); prefs != nil {
			if preferred, ok := workspacePreferenceString(prefs, prefs.DefaultModel); ok {
				if modelBelongsToProvider(provider, preferred) {
					return preferred, chatPreferenceSourceWorkspace
				}
			}
		}
	}

	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil && providerSource != chatPreferenceSourceInteractive {
		if preferred := strings.TrimSpace(cfg.AICLI.Chat.DefaultModel); preferred != "" {
			return preferred, chatPreferenceSourceConfig
		}
	}

	if opts != nil && !opts.NoInteractive && !opts.Headless {
		if selected := selectModelWithReader(provider, chatOptionInputReader(opts)); selected != "" {
			return selected, chatPreferenceSourceInteractive
		}
	}

	if defaultModel := strings.TrimSpace(provider.DefaultModel); defaultModel != "" {
		return defaultModel, chatPreferenceSourceDefault
	}
	return "", chatPreferenceSourceDefault
}

func resolveChatReasoningChoice(cfg *config.Config, provider config.Provider, modelName string, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) (string, string, chatPreferenceSource, string, error) {
	interactive := opts != nil && !opts.NoInteractive
	// headless 模式下 reasoning 选择器也不能阻塞：视为非交互。
	if opts != nil && opts.Headless {
		interactive = false
	}
	raw := ""
	source := chatPreferenceSourceDefault
	workspaceReasoning := ""
	if prefs := resolveWorkspaceChatPreferences(); prefs != nil {
		workspaceReasoning = strings.TrimSpace(prefs.ReasoningEffort)
	}

	switch {
	case opts != nil && opts.ReasoningEffortChanged:
		raw = runtimetypes.NormalizeReasoningEffort(opts.ReasoningEffortFlag)
		source = chatPreferenceSourceFlag
	case loadedRuntimeSession != nil:
		raw = runtimetypes.NormalizeReasoningEffort(runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextReasoningEffort))
		if raw != "" {
			source = chatPreferenceSourceSession
		}
	case workspaceReasoning != "":
		// Workspace-scoped preference (D5).
		raw = runtimetypes.NormalizeReasoningEffort(workspaceReasoning)
		source = chatPreferenceSourceWorkspace
	case cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil:
		raw = runtimetypes.NormalizeReasoningEffort(cfg.AICLI.Chat.ReasoningEffort)
		if raw != "" {
			source = chatPreferenceSourceConfig
		}
	}

	catalog := reasoningEffortCatalogForModel(provider, modelName)
	if raw == "" {
		if interactive && catalog.supported {
			selected := selectReasoningEffortWithReader("", catalog.options, chatOptionInputReader(opts))
			if selected != "" {
				return selected, selected, chatPreferenceSourceInteractive, "", nil
			}
		}
		return "", "", source, "", nil
	}

	reasoningEffort, warningMessage, err := resolveChatReasoningEffort(provider, modelName, raw, source == chatPreferenceSourceFlag)
	if err != nil {
		return "", raw, source, "", err
	}
	if warningMessage == "" {
		return reasoningEffort, raw, source, "", nil
	}

	if source == chatPreferenceSourceConfig && interactive && catalog.supported {
		selected := selectReasoningEffortWithReader(reasoningEffort, catalog.options, chatOptionInputReader(opts))
		return selected, selected, chatPreferenceSourceInteractive, "", nil
	}

	if source == chatPreferenceSourceConfig || source == chatPreferenceSourceDefault {
		return "", raw, source, warningMessage, nil
	}
	return reasoningEffort, raw, source, warningMessage, nil
}

func shouldPersistChatStartupPreferences(cfg *config.Config, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session, providerSource, modelSource, reasoningSource, streamSource chatPreferenceSource) bool {
	if cfg == nil || opts == nil || loadedRuntimeSession != nil || opts.NoInteractive {
		return false
	}
	if opts.ProviderChanged || opts.ModelChanged || opts.ReasoningEffortChanged {
		return false
	}
	return providerSource == chatPreferenceSourceInteractive ||
		modelSource == chatPreferenceSourceInteractive ||
		reasoningSource == chatPreferenceSourceInteractive ||
		streamSource == chatPreferenceSourceInteractive
}

// persistChatPreferences persists the provider/model/reasoning defaults from
// explicit session commands (/model, runtime pickers) into the workspace-scoped
// preference file (decision D5) instead of the global aicli.chat section, so a
// per-session switch stops the startup selectors from re-firing only in this
// working directory. cfg is kept for signature stability; the workspace path is
// resolved from $HOME plus the process working directory.
func persistChatPreferences(cfg *config.Config, providerName, modelName, reasoningEffort string) error {
	_ = cfg
	return config.SaveWorkspaceChatPreferences(config.AICLIChatPreferenceUpdate{
		DefaultProvider: stringValuePtr(strings.TrimSpace(providerName)),
		DefaultModel:    stringValuePtr(strings.TrimSpace(modelName)),
		ReasoningEffort: stringValuePtr(runtimetypes.NormalizeReasoningEffort(reasoningEffort)),
	})
}

func persistChatPreferencesIfNeeded(cfg *config.Config, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session, providerSource, modelSource, reasoningSource, streamSource chatPreferenceSource, reasoningWarning string, providerName, modelName, reasoningEffort string, shouldStream bool) {
	if !shouldPersistChatStartupPreferences(cfg, opts, loadedRuntimeSession, providerSource, modelSource, reasoningSource, streamSource) {
		return
	}
	update := config.AICLIChatPreferenceUpdate{}
	if providerSource == chatPreferenceSourceInteractive {
		update.DefaultProvider = stringValuePtr(providerName)
	}
	if modelSource == chatPreferenceSourceInteractive {
		update.DefaultModel = stringValuePtr(modelName)
	}
	if reasoningSource == chatPreferenceSourceInteractive || (reasoningSource == chatPreferenceSourceConfig && strings.TrimSpace(reasoningWarning) != "") {
		update.ReasoningEffort = stringValuePtr(reasoningEffort)
	}
	if streamSource == chatPreferenceSourceInteractive {
		update.Stream = boolValuePtr(shouldStream)
	}
	if update.DefaultProvider == nil && update.DefaultModel == nil && update.ReasoningEffort == nil && update.Stream == nil {
		return
	}

	// Workspace-scoped persistence (D5): interactive startup choices land in
	// $HOME/.aicli/workspace/<hash>/chat-prefs.yaml so they only apply to the
	// current working directory and stop the selectors from re-firing here,
	// while other workspaces keep their own defaults. The global aicli.chat
	// section is no longer written from the chat startup path.
	if err := config.SaveWorkspaceChatPreferences(update); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 workspace aicli.chat 偏好失败: %v\n", err)
	}
}

func persistChatStartupPreferences(cfg *config.Config, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session, runtimeState *chatRuntimeState) {
	if runtimeState == nil {
		return
	}
	persistChatPreferencesIfNeeded(
		cfg,
		opts,
		loadedRuntimeSession,
		runtimeState.providerSource,
		runtimeState.modelSource,
		runtimeState.reasoningSource,
		runtimeState.streamSource,
		runtimeState.reasoningWarning,
		runtimeState.providerName,
		runtimeState.modelName,
		runtimeState.reasoningEffort,
		runtimeState.shouldStream,
	)
}

func isEnabledProvider(cfg *config.Config, providerName string) bool {
	if cfg == nil {
		return false
	}
	provider, ok := cfg.Providers.Items[strings.TrimSpace(providerName)]
	return ok && provider.Enabled
}

func stringValuePtr(value string) *string {
	v := strings.TrimSpace(value)
	return &v
}

func boolValuePtr(value bool) **bool {
	inner := value
	innerPtr := &inner
	return &innerPtr
}
