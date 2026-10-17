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
	// goalMaxContinuations 来自 runtime.yaml 的 goal.maxConsecutiveContinuations：
	// 0 = 不限制（对齐 Codex 的 continue_if_idle）；正整数为自动续跑安全阀。
	// 见 docs/plan/aicli-goal-continuation-codex-alignment-plan-20261014.md。
	goalMaxContinuations int
	// goalDefaultTokenBudget 来自 runtime.yaml 的 goal.defaultTokenBudget：
	// 新建 goal 的默认 token 预算（0=无预算）。
	goalDefaultTokenBudget int
	// goalDefaultMaxRounds 来自 runtime.yaml 的 goal.maxRounds：新建 goal 的
	// 默认总轮数上限（0=不限制；G2，DSH goal 借鉴）。
	goalDefaultMaxRounds int
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
	if runtimeConfig != nil && runtimeConfig.Goal.MaxConsecutiveContinuations > 0 {
		state.goalMaxContinuations = runtimeConfig.Goal.MaxConsecutiveContinuations
	}
	if runtimeConfig != nil && runtimeConfig.Goal.DefaultTokenBudget > 0 {
		state.goalDefaultTokenBudget = runtimeConfig.Goal.DefaultTokenBudget
	}
	if runtimeConfig != nil && runtimeConfig.Goal.MaxRounds > 0 {
		state.goalDefaultMaxRounds = runtimeConfig.Goal.MaxRounds
	}
	markChatStartup("persistence_config")
	if manager, userID, sessionDir, configured, err := prepareRuntimeServerChatPersistence(runtimeConfig, opts); err != nil {
		return nil, err
	} else if configured {
		state.runtimeSessionManager = manager
		state.sessionUserID = userID
		state.resolvedSessionDir = sessionDir
		if manager != nil {
			loadedRuntimeSession, loadErr := loadRequestedRuntimeSessionWithFilter(context.Background(), manager, userID, opts.SessionIDFlag, opts.ResumeFlag, opts.SessionFilter)
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
		loadedRuntimeSession, err := loadRequestedRuntimeSessionWithFilter(context.Background(), manager, userID, opts.SessionIDFlag, opts.ResumeFlag, opts.SessionFilter)
		if err != nil {
			return nil, fmt.Errorf("加载会话失败: %w", err)
		}
		state.loadedRuntimeSession = loadedRuntimeSession
	}
	// 「读会话」与「建管理器」是两笔性质不同的成本：前者是存储整份加载（含消息
	// 历史），后者是打开存储/建连接。窗口化恢复要求首帧前不读全量历史，因此这条
	// 打点用于持续盯住会话加载是否退化成全量加载。
	markChatStartup("persistence_session")

	return state, nil
}

func newEphemeralChatPersistenceState(explicitUserID string) *chatPersistenceState {
	managerConfig := runtimechat.DefaultSessionManagerConfig()
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
	// 无条件走解析器：resolveGlobalRuntimeConfigPath 内部处理「无 config_file →
	// .aicli 层自动发现」，此处以 ConfigFile != "" 作为前置门会把删除了
	// config_file 指针的配置变成「整个运行时层不加载」（与 chat.go
	// resolveRuntimeToolConfigPath 修过的假开关同一模式）。
	if runtimePath == "" {
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
	// 首次解析只为了确定 provider 与其协议（决定 SessionFilter 与会话恢复
	// 校验），此时模型选择还没走完：模型可能来自 --model / 恢复会话 /
	// workspace 偏好（default_model），甚至可能在交互式选择器里产生。
	// 因此这里允许 provider 暂无 default_model，否则项目层 .aicli/config.yaml
	// 只含 gateway 段、遮蔽用户级 providers 配置时（preset 提供的
	// opencode.ai 条目没有 default_model），workspace 偏好里明明存了
	// default_model 也会在模型解析之前硬报错退出。
	providerContext, details, err := resolveProviderContextAllowMissingModel(cfg, requestedProvider)
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

	requestedModel, modelSource := resolveChatModelChoice(cfg, provider, providerSource, opts, loadedRuntimeSession)
	finalContext, details, err := resolveProviderContextAllowMissingModel(cfg, providerContext.ProviderName)
	if err != nil {
		return nil, details, err
	}
	if requestedModel != "" {
		withModel, withModelDetails, modelErr := resolveProviderExecutionContext(cfg, providerContext.ProviderName, requestedModel)
		if modelErr != nil {
			return nil, withModelDetails, modelErr
		}
		finalContext = withModel
		details = withModelDetails
	} else if opts != nil && opts.Headless {
		// headless（管道/远端调用）没有交互式模型选择器兜底，空模型会在
		// 首次请求时才失败且信息不可读；这里显式报错并带上 provider 上下文。
		return nil, details, fmt.Errorf("no model specified and no default model configured for provider '%s' (set --model or providers.items.%s.default_model)", providerContext.ProviderName, providerContext.ProviderName)
	} else {
		fmt.Fprintf(os.Stderr, "Warning: provider '%s' 未配置 default_model，也未通过 --model/workspace 偏好指定模型；可用 /model 命令在会话内选择\n", providerContext.ProviderName)
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
