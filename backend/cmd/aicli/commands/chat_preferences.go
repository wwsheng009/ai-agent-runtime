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
	chatPreferenceSourceDefault     chatPreferenceSource = "default"
)

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
			return storedProvider, chatPreferenceSourceSession
		}
		if storedProtocol := runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextProtocol); storedProtocol != "" {
			if providerName, ok := resolveEnabledProviderNameByProtocol(cfg, storedProtocol); ok {
				return providerName, chatPreferenceSourceSession
			}
		}
	}

	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil {
		if preferred := strings.TrimSpace(cfg.AICLI.Chat.DefaultProvider); preferred != "" && isEnabledProvider(cfg, preferred) {
			return preferred, chatPreferenceSourceConfig
		}
	}

	if opts != nil && !opts.NoInteractive {
		if selected := selectProviderWithReader(cfg, chatOptionInputReader(opts)); selected != "" {
			return selected, chatPreferenceSourceInteractive
		}
	}

	if cfg != nil {
		if defaultProvider := strings.TrimSpace(cfg.Providers.DefaultProvider); defaultProvider != "" {
			return defaultProvider, chatPreferenceSourceDefault
		}
	}
	return "", chatPreferenceSourceDefault
}

func resolveChatModelChoice(cfg *config.Config, provider config.Provider, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) (string, chatPreferenceSource) {
	if opts != nil && strings.TrimSpace(opts.ModelFlag) != "" {
		return strings.TrimSpace(opts.ModelFlag), chatPreferenceSourceFlag
	}
	if loadedRuntimeSession != nil {
		if storedModel := runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextModel); storedModel != "" {
			return storedModel, chatPreferenceSourceSession
		}
	}

	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil {
		if preferred := strings.TrimSpace(cfg.AICLI.Chat.DefaultModel); preferred != "" {
			return preferred, chatPreferenceSourceConfig
		}
	}

	if opts != nil && !opts.NoInteractive {
		if selected := selectModelWithReader(provider, chatOptionInputReader(opts)); selected != "" {
			return selected, chatPreferenceSourceInteractive
		}
	}

	if defaultModel := strings.TrimSpace(provider.DefaultModel); defaultModel != "" {
		return defaultModel, chatPreferenceSourceDefault
	}
	return "", chatPreferenceSourceDefault
}

func resolveChatReasoningChoice(cfg *config.Config, provider config.Provider, modelName string, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) (string, chatPreferenceSource, string, error) {
	interactive := opts != nil && !opts.NoInteractive
	raw := ""
	source := chatPreferenceSourceDefault

	switch {
	case opts != nil && opts.ReasoningEffortChanged:
		raw = runtimetypes.NormalizeReasoningEffort(opts.ReasoningEffortFlag)
		source = chatPreferenceSourceFlag
	case loadedRuntimeSession != nil:
		raw = runtimetypes.NormalizeReasoningEffort(runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextReasoningEffort))
		if raw != "" {
			source = chatPreferenceSourceSession
		}
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
				return selected, chatPreferenceSourceInteractive, "", nil
			}
		}
		return "", source, "", nil
	}

	reasoningEffort, warningMessage, err := resolveChatReasoningEffort(provider, modelName, raw, source == chatPreferenceSourceFlag)
	if err != nil {
		return "", source, "", err
	}
	if warningMessage == "" {
		return reasoningEffort, source, "", nil
	}

	if source == chatPreferenceSourceConfig && interactive && catalog.supported {
		selected := selectReasoningEffortWithReader(reasoningEffort, catalog.options, chatOptionInputReader(opts))
		return selected, chatPreferenceSourceInteractive, "", nil
	}

	if source == chatPreferenceSourceConfig || source == chatPreferenceSourceDefault {
		return "", source, warningMessage, nil
	}
	return reasoningEffort, source, warningMessage, nil
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

func persistChatPreferences(cfg *config.Config, providerName, modelName, reasoningEffort string) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	configPath, err := ensureWritableAICLIConfigPath(cfg, cfg.ConfigFilePath)
	if err != nil {
		return err
	}

	_, err = config.UpdateAICLIChatPreferences(configPath, config.AICLIChatPreferenceUpdate{
		DefaultProvider: stringValuePtr(strings.TrimSpace(providerName)),
		DefaultModel:    stringValuePtr(strings.TrimSpace(modelName)),
		ReasoningEffort: stringValuePtr(runtimetypes.NormalizeReasoningEffort(reasoningEffort)),
	})
	return err
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
	configPath, err := ensureWritableAICLIConfigPath(cfg, cfg.ConfigFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 aicli.chat 偏好失败: %v\n", err)
		return
	}
	if _, err := config.UpdateAICLIChatPreferences(configPath, update); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 aicli.chat 偏好失败: %v\n", err)
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
