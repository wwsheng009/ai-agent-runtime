package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type chatPersistenceState struct {
	runtimeSessionManager *runtimechat.SessionManager
	sessionUserID         string
	resolvedSessionDir    string
	loadedRuntimeSession  *runtimechat.Session
}

type chatRuntimeState struct {
	providerName    string
	provider        config.Provider
	adapter         adapter.ProtocolAdapter
	modelName       string
	reasoningEffort string
	shouldStream    bool
	baseURL         string
	retryCfg        RetryConfig
	requestTimeout  time.Duration
}

func prepareChatPersistence(opts *chatCommandOptions) (*chatPersistenceState, error) {
	state := &chatPersistenceState{}
	if opts == nil {
		return state, nil
	}

	manager, userID, sessionDir, err := newChatSessionManager(opts.SessionDirFlag)
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

	providerName := resolveChatProviderName(cfg, opts, loadedRuntimeSession)
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

	reasoningSource := runtimetypes.NormalizeReasoningEffort(opts.ReasoningEffortFlag)
	if !opts.ReasoningEffortChanged && loadedRuntimeSession != nil {
		if storedEffort := runtimetypes.NormalizeReasoningEffort(runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextReasoningEffort)); storedEffort != "" {
			reasoningSource = storedEffort
		}
	}
	reasoningExplicit := opts.ReasoningEffortChanged

	modelName := resolveChatModelName(provider, opts, loadedRuntimeSession)
	finalContext, details, err := resolveProviderExecutionContext(cfg, providerName, modelName)
	if err != nil {
		return nil, details, err
	}
	provider = finalContext.Provider
	modelName = finalContext.Model
	adapter := finalContext.Adapter

	shouldStream := resolveChatStreamMode(opts, loadedRuntimeSession)
	reasoningEffort, warningMessage, err := resolveChatReasoningEffort(provider, modelName, reasoningSource, reasoningExplicit)
	if err != nil {
		return nil, nil, err
	}
	if warningMessage != "" {
		fmt.Fprintln(os.Stderr, warningMessage)
	}
	if !reasoningExplicit && !opts.NoInteractive {
		catalog := reasoningEffortCatalogForModel(provider, modelName)
		if catalog.supported {
			reasoningEffort = selectReasoningEffortWithReader(reasoningEffort, catalog.options, chatOptionInputReader(opts))
		}
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
		providerName:    providerName,
		provider:        provider,
		adapter:         adapter,
		modelName:       modelName,
		reasoningEffort: reasoningEffort,
		shouldStream:    shouldStream,
		baseURL:         buildProviderURL(provider, adapter.GetAPIPath(), modelName),
		retryCfg:        retryCfg,
		requestTimeout:  requestTimeout,
	}, nil, nil
}
