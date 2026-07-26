package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
)

type chatPersistenceState struct {
	runtimeSessionManager *runtimechat.SessionManager
	sessionUserID         string
	resolvedSessionDir    string
	loadedRuntimeSession  *runtimechat.Session
	ephemeral             bool
}

type chatRuntimeState struct {
	providerName             string
	requestedProvider        string
	providerSource           chatPreferenceSource
	provider                 config.Provider
	adapter                  adapter.ProtocolAdapter
	modelName                string
	requestedModel           string
	modelSource              chatPreferenceSource
	reasoningEffort          string
	requestedReasoningEffort string
	reasoningSource          chatPreferenceSource
	reasoningWarning         string
	shouldStream             bool
	streamSource             chatPreferenceSource
	fastMode                 bool
	baseURL                  string
	retryCfg                 RetryConfig
	requestTimeout           time.Duration
}

func prepareChatPersistence(cfg *config.Config, opts *chatCommandOptions, profileState *chatProfileState) (*chatPersistenceState, error) {
	state := &chatPersistenceState{}
	if opts == nil {
		return state, nil
	}

	runtimeConfig, runtimeConfigPath := loadChatPersistenceRuntimeConfig(cfg, profileState)
	markChatStartup("persistence_config")
	if manager, userID, sessionDir, configured, err := prepareRuntimeServerChatPersistence(runtimeConfig, opts); err != nil {
		return nil, err
	} else if configured {
		state.runtimeSessionManager = manager
		state.sessionUserID = userID
		state.resolvedSessionDir = sessionDir
		if manager != nil {
			loadedRuntimeSession, loadErr := loadRequestedRuntimeSession(context.Background(), manager, userID, opts.SessionIDFlag, opts.ResumeFlag)
			if loadErr != nil {
				return nil, fmt.Errorf("加载会话失败: %w", loadErr)
			}
			state.loadedRuntimeSession = loadedRuntimeSession
		}
		return state, nil
	}

	manager, userID, sessionDir, err := newChatSessionManagerWithRuntimeConfig(opts.SessionDirFlag, runtimeConfig, runtimeConfigPath, opts.SessionUserFlag)
	markChatStartup("persistence_store")
	if err != nil {
		if opts.SessionFeaturesRequested {
			return nil, fmt.Errorf("初始化会话管理失败: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Warning: 初始化文件会话管理失败，已退回内存会话: %v\n", err)
		return newEphemeralChatPersistenceState(opts.SessionUserFlag), nil
	}

	state.runtimeSessionManager = manager
	state.sessionUserID = userID
	state.resolvedSessionDir = sessionDir

	if manager != nil {
		loadedRuntimeSession, err := loadRequestedRuntimeSession(context.Background(), manager, userID, opts.SessionIDFlag, opts.ResumeFlag)
		if err != nil {
			return nil, fmt.Errorf("加载会话失败: %w", err)
		}
		state.loadedRuntimeSession = loadedRuntimeSession
	}

	return state, nil
}

func newEphemeralChatPersistenceState(explicitUserID string) *chatPersistenceState {
	managerConfig := runtimechat.DefaultSessionManagerConfig()
	managerConfig.MaxHistory = 200
	managerConfig.CleanupInterval = 6 * time.Hour
	managerConfig.IdleTimeout = 72 * time.Hour
	userID := sessionruntime.ResolveSessionUserID(sessionruntime.IdentitySource{
		CLIUserID: strings.TrimSpace(explicitUserID),
		CLILocal:  true,
	})
	return &chatPersistenceState{
		runtimeSessionManager: runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), managerConfig),
		sessionUserID:         userID,
		ephemeral:             true,
	}
}

func loadChatPersistenceRuntimeConfig(cfg *config.Config, profileState *chatProfileState) (*runtimecfg.RuntimeConfig, string) {
	runtimePath := ""
	if profileState != nil && profileState.Active() {
		runtimePath = strings.TrimSpace(profileState.RuntimeConfigPath())
	}
	if runtimePath == "" && cfg != nil && cfg.SkillsRuntime != nil && strings.TrimSpace(cfg.SkillsRuntime.ConfigFile) != "" {
		runtimePath = resolveGlobalRuntimeConfigPath(cfg)
	}
	if runtimePath == "" {
		return nil, ""
	}
	runtimeConfig, runtimeConfigPath, err := loadCachedRuntimeConfig(runtimePath)
	if err != nil || runtimeConfig == nil {
		return nil, runtimePath
	}
	sessionruntime.ApplyDefaults(runtimeConfig, sessionruntime.ResolveOptions{
		Config:     runtimeConfig,
		ConfigFile: runtimeConfigPath,
		SessionDir: strings.TrimSpace(runtimeConfig.Sessions.Dir),
		Mode:       sessionruntime.ModeCLILocal,
	})
	return runtimeConfig, runtimeConfigPath
}

func maybeSelectStartupSession(opts *chatCommandOptions, state *chatPersistenceState) error {
	if opts == nil || state == nil {
		return nil
	}
	if state.runtimeSessionManager == nil || state.loadedRuntimeSession != nil || opts.NoInteractive || strings.TrimSpace(opts.SessionIDFlag) != "" || opts.ResumeFlag {
		return nil
	}

	selectedSession, createNew, err := promptStartupSessionSelectionWithReader(state.runtimeSessionManager, state.sessionUserID, opts.SessionFilter, chatOptionInputReader(opts))
	if err != nil {
		return fmt.Errorf("选择会话失败: %w", err)
	}
	if !createNew {
		state.loadedRuntimeSession = selectedSession
	}
	return nil
}

func prepareChatRuntimeState(cfg *config.Config, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) (*chatRuntimeState, map[string]interface{}, error) {
	if opts == nil {
		return nil, nil, fmt.Errorf("chat options is nil")
	}

	requestedProvider, providerSource := resolveChatProviderChoice(cfg, opts, loadedRuntimeSession)
	providerContext, details, err := resolveProviderExecutionContext(cfg, requestedProvider, "")
	if err != nil {
		return nil, details, err
	}

	provider := providerContext.Provider
	if opts.SessionFilter.Protocol == "" {
		opts.SessionFilter.Protocol = provider.GetProtocol()
	}
	if loadedRuntimeSession != nil {
		storedProtocol := runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextProtocol)
		if storedProtocol != "" && !strings.EqualFold(storedProtocol, provider.GetProtocol()) {
			return nil, nil, fmt.Errorf("会话 %s 使用协议 %s，当前 provider %s 使用协议 %s，无法恢复",
				loadedRuntimeSession.ID, storedProtocol, requestedProvider, provider.GetProtocol())
		}
	}

	requestedModel, modelSource := resolveChatModelChoice(cfg, provider, opts, loadedRuntimeSession)
	finalContext, details, err := resolveProviderExecutionContext(cfg, providerContext.ProviderName, requestedModel)
	if err != nil {
		return nil, details, err
	}
	provider = finalContext.Provider
	modelName := finalContext.Model
	adapter := finalContext.Adapter

	shouldStream, streamSource := resolveChatStreamChoice(cfg, opts, loadedRuntimeSession)
	fastMode := resolveChatFastModeChoice(cfg, opts, loadedRuntimeSession)
	reasoningEffort, requestedReasoningEffort, reasoningSource, warningMessage, err := resolveChatReasoningChoice(cfg, provider, modelName, opts, loadedRuntimeSession)
	if err != nil {
		return nil, nil, err
	}
	if warningMessage != "" {
		fmt.Fprintln(os.Stderr, warningMessage)
	}
	// Fast only affects Codex requests; warn when the CLI flag is set on other protocols.
	if opts != nil && opts.FastChanged && !strings.EqualFold(strings.TrimSpace(provider.GetProtocol()), "codex") {
		protocol := strings.TrimSpace(provider.GetProtocol())
		if protocol == "" {
			protocol = "(unknown)"
		}
		fmt.Fprintf(os.Stderr, "Warning: --fast 仅对 codex 协议生效（当前: %s），请求不会注入 service_tier\n", protocol)
	}
	if opts.OutputFormat == "json" && shouldStream {
		return nil, nil, fmt.Errorf("--output json 暂不支持与 --stream 同时使用")
	}

	retryCfg := resolveAICLIRetryConfig(cfg)
	if opts.FailFast {
		retryCfg.DisableRetries = true
	}

	requestTimeout := resolveAICLIRequestTimeout(cfg)
	if strings.TrimSpace(opts.RequestTimeoutFlag) != "" {
		parsedTimeout, err := time.ParseDuration(strings.TrimSpace(opts.RequestTimeoutFlag))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: 无效的 request-timeout: %s\n", opts.RequestTimeoutFlag)
		} else {
			requestTimeout = parsedTimeout
		}
	}

	return &chatRuntimeState{
		providerName:             providerContext.ProviderName,
		providerSource:           providerSource,
		provider:                 provider,
		adapter:                  adapter,
		modelName:                modelName,
		modelSource:              modelSource,
		reasoningEffort:          reasoningEffort,
		requestedProvider:        strings.TrimSpace(requestedProvider),
		requestedModel:           strings.TrimSpace(finalContext.RequestedModel),
		requestedReasoningEffort: strings.TrimSpace(requestedReasoningEffort),
		reasoningSource:          reasoningSource,
		reasoningWarning:         warningMessage,
		shouldStream:             shouldStream,
		streamSource:             streamSource,
		fastMode:                 fastMode,
		baseURL:                  buildProviderURL(provider, adapter.GetAPIPath(), modelName),
		retryCfg:                 retryCfg,
		requestTimeout:           requestTimeout,
	}, nil, nil
}
