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
}

type chatRuntimeState struct {
	providerName     string
	providerSource   chatPreferenceSource
	provider         config.Provider
	adapter          adapter.ProtocolAdapter
	modelName        string
	modelSource      chatPreferenceSource
	reasoningEffort  string
	reasoningSource  chatPreferenceSource
	reasoningWarning string
	shouldStream     bool
	streamSource     chatPreferenceSource
	baseURL          string
	retryCfg         RetryConfig
	requestTimeout   time.Duration
}

func prepareChatPersistence(cfg *config.Config, opts *chatCommandOptions, profileState *chatProfileState) (*chatPersistenceState, error) {
	state := &chatPersistenceState{}
	if opts == nil {
		return state, nil
	}

	runtimeConfig, runtimeConfigPath := loadChatPersistenceRuntimeConfig(cfg, profileState)
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
	if err != nil {
		if opts.SessionFeaturesRequested {
			return nil, fmt.Errorf("初始化会话管理失败: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Warning: 初始化会话管理失败，已退回临时会话: %v\n", err)
		return state, nil
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
	manager := runtimecfg.NewRuntimeManager(runtimePath)
	if err := manager.Load(); err != nil {
		return nil, runtimePath
	}
	runtimeConfig := manager.Get()
	runtimeConfigPath := manager.GetFilePath()
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

	providerName, providerSource := resolveChatProviderChoice(cfg, opts, loadedRuntimeSession)
	providerContext, details, err := resolveProviderExecutionContext(cfg, providerName, "")
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
				loadedRuntimeSession.ID, storedProtocol, providerName, provider.GetProtocol())
		}
	}

	modelName, modelSource := resolveChatModelChoice(cfg, provider, opts, loadedRuntimeSession)
	finalContext, details, err := resolveProviderExecutionContext(cfg, providerName, modelName)
	if err != nil {
		return nil, details, err
	}
	provider = finalContext.Provider
	modelName = finalContext.Model
	adapter := finalContext.Adapter

	shouldStream, streamSource := resolveChatStreamChoice(cfg, opts, loadedRuntimeSession)
	reasoningEffort, reasoningSource, warningMessage, err := resolveChatReasoningChoice(cfg, provider, modelName, opts, loadedRuntimeSession)
	if err != nil {
		return nil, nil, err
	}
	if warningMessage != "" {
		fmt.Fprintln(os.Stderr, warningMessage)
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
		providerName:     providerName,
		providerSource:   providerSource,
		provider:         provider,
		adapter:          adapter,
		modelName:        modelName,
		modelSource:      modelSource,
		reasoningEffort:  reasoningEffort,
		reasoningSource:  reasoningSource,
		reasoningWarning: warningMessage,
		shouldStream:     shouldStream,
		streamSource:     streamSource,
		baseURL:          buildProviderURL(provider, adapter.GetAPIPath(), modelName),
		retryCfg:         retryCfg,
		requestTimeout:   requestTimeout,
	}, nil, nil
}
