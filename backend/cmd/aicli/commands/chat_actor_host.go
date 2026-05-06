package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	toolbrokersessionctx "github.com/wwsheng009/ai-agent-runtime/internal/toolbroker/sessionctx"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type localChatRuntimeHost struct {
	Bootstrap     *runtimebootstrap.Manager
	RuntimeConfig *runtimecfg.RuntimeConfig
	SessionHub    *runtimechat.SessionHub
	RuntimeStore  runtimechat.RuntimeStateStore
	EventStore    runtimechat.EventStore
	ReceiptStore  runtimechat.ToolReceiptStore
	TeamStore     team.Store
	TeamClaims    *team.PathClaimManager
	Orchestrator  *team.Orchestrator
	ToolSurface   runtimeskill.MCPManager
	EventBus      *runtimeevents.Bus
	SessionStore  runtimechat.SessionStorage
	SessionUser   string
	BaseSession   *ChatSession
	TeamLifecycle teamLifecycleService
	ActorRegistry *localActorRegistry
	cleanupFns    []func()
}

func (h *localChatRuntimeHost) Close() {
	if h == nil {
		return
	}
	for i := len(h.cleanupFns) - 1; i >= 0; i-- {
		if h.cleanupFns[i] != nil {
			h.cleanupFns[i]()
		}
	}
}

func initializeLocalChatRuntimeHost(cfg *config.Config, session *ChatSession, toolManager *runtimetools.Manager) (*localChatRuntimeHost, error) {
	if session == nil {
		return nil, fmt.Errorf("chat session is nil")
	}
	if session.SessionManager == nil || session.RuntimeSession == nil {
		return nil, fmt.Errorf("chat session persistence is not initialized")
	}
	sessionStore := session.SessionManager.GetStorage()
	if sessionStore == nil {
		return nil, fmt.Errorf("chat session storage is not configured")
	}

	runtimeConfig, err := loadLocalChatRuntimeConfig(cfg, session)
	if err != nil {
		return nil, err
	}

	var runtimeMCP runtimeskill.MCPManager
	if toolManager != nil {
		runtimeMCP = runtimetools.NewAgentAdapter(toolManager)
	}

	bootstrapManager, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config:          runtimeConfig,
		SkillDirs:       resolveChatSkillDirs(cfg, session, nil),
		DiscoverOnly:    true,
		MCPManager:      runtimeMCP,
		ProviderConfigs: buildSkillsProviderConfigs(cfg),
	})
	if err != nil {
		return nil, err
	}
	if err := ensureLocalRuntimeProvider(bootstrapManager.LLMRuntime(), session); err != nil {
		_ = bootstrapManager.Stop()
		return nil, err
	}

	runtimeStore, eventStore := buildLocalChatRuntimeStores(session)
	receiptStore, _ := runtimeStore.(runtimechat.ToolReceiptStore)
	eventBus := runtimeevents.NewBusWithRetention(2048)
	host := &localChatRuntimeHost{
		Bootstrap:     bootstrapManager,
		RuntimeConfig: runtimeConfig,
		RuntimeStore:  runtimeStore,
		EventStore:    eventStore,
		ReceiptStore:  receiptStore,
		TeamStore:     bootstrapManager.TeamStore(),
		ToolSurface:   runtimeMCP,
		EventBus:      eventBus,
		SessionStore:  sessionStore,
		SessionUser:   session.SessionUserID,
		BaseSession:   session,
	}
	host.TeamLifecycle = newLocalTeamLifecycleService(host)

	workspaceRoot := resolveLocalWorkspacePath(runtimeConfig, session)
	claims := team.NewPathClaimManager(host.TeamStore, workspaceRoot)
	host.TeamClaims = claims
	host.Orchestrator = team.NewOrchestrator(host.TeamStore, claims, nil)
	host.ActorRegistry = newLocalActorRegistry(host)
	if host.Orchestrator != nil {
		mailbox := team.NewMailboxService(host.TeamStore)
		host.Orchestrator.Mailbox = mailbox
		host.Orchestrator.Dispatcher = host.ActorRegistry
		host.Orchestrator.Runner = &team.TeammateRunner{
			Sessions: host.ActorRegistry,
			Mailbox:  mailbox,
			Context:  team.NewContextBuilder(host.TeamStore),
		}
		host.Orchestrator.LeadPlanner = &team.LeadPlanner{
			Sessions:    host.ActorRegistry,
			Store:       host.TeamStore,
			Mailbox:     mailbox,
			AutoPersist: true,
		}
		host.Orchestrator.LeaseManager = team.NewLeaseManager(host.TeamStore, claims)
		host.Orchestrator.LeaseManager.Mailbox = mailbox
	}
	host.bindTeamLifecycleEvents()
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		return host.buildSessionActor(sessionID, session, sessionStore, runtimeConfig, workspaceRoot)
	})
	host.cleanupFns = []func(){
		func() {
			host.stopTeamLifecycleLoops()
		},
		func() {
			if host.SessionHub != nil {
				host.SessionHub.StopAll()
			}
		},
		func() {
			closeLocalRuntimeStores(runtimeStore, eventStore)
		},
		func() {
			_ = bootstrapManager.Stop()
		},
	}

	return host, nil
}

func refreshLocalRuntimeAfterModelSelection(session *ChatSession) error {
	if session == nil {
		return nil
	}

	setChatActorWarmup(session, nil)
	var errs []string
	if session.LocalRuntimeHost != nil && session.LocalRuntimeHost.SessionHub != nil && session.RuntimeSession != nil {
		session.LocalRuntimeHost.SessionHub.Stop(session.RuntimeSession.ID)
	}
	if session.LocalRuntimeHost != nil && session.LocalRuntimeHost.Bootstrap != nil && session.Config != nil {
		if err := session.LocalRuntimeHost.Bootstrap.ReloadProviderConfigs(buildSkillsProviderConfigs(session.Config)); err != nil {
			errs = append(errs, fmt.Sprintf("reload providers: %v", err))
		}
	}
	if session.LocalRuntimeHost != nil && session.LocalRuntimeHost.Bootstrap != nil {
		if err := ensureLocalRuntimeProvider(session.LocalRuntimeHost.Bootstrap.LLMRuntime(), session); err != nil {
			errs = append(errs, fmt.Sprintf("ensure session provider: %v", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	startChatActorWarmup(session)
	return nil
}

func (h *localChatRuntimeHost) buildSessionActor(sessionID string, session *ChatSession, sessionStore runtimechat.SessionStorage, runtimeConfig *runtimecfg.RuntimeConfig, workspaceRoot string) (*runtimechat.SessionActor, error) {
	childAgentType := ""
	requestedModel := ""
	if sessionStore != nil {
		if runtimeSession, err := sessionStore.Load(context.Background(), sessionID); err == nil && runtimeSession != nil {
			if value, ok := runtimeSession.GetContext(toolbroker.AgentSessionContextAgentType); ok {
				if text, ok := value.(string); ok {
					childAgentType = strings.TrimSpace(text)
				}
			}
			if value, ok := runtimeSession.GetContext(toolbroker.AgentSessionContextRequestedModel); ok {
				if text, ok := value.(string); ok {
					requestedModel = strings.TrimSpace(text)
				}
			}
		}
	}
	apiAgent := buildLocalChatAgent(session, h, runtimeConfig, workspaceRoot, childAgentType, requestedModel)
	actor, err := runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   h.Bootstrap.LLMRuntime(),
		SessionStore: sessionStore,
		StateStore:   h.RuntimeStore,
		EventStore:   h.EventStore,
		EventBus:     h.EventBus,
		LoopConfig:   buildLocalChatLoopConfig(runtimeConfig, session),
	})
	if err != nil {
		return nil, err
	}
	return actor, nil
}

func buildLocalChatAgent(session *ChatSession, host *localChatRuntimeHost, runtimeConfig *runtimecfg.RuntimeConfig, workspaceRoot string, childAgentType string, requestedModel string) *agent.Agent {
	agentConfig := &agent.Config{
		Name:         firstNonEmptyChatValue(strings.TrimSpace(childAgentType), "aicli-chat"),
		Provider:     resolveLocalChatAgentProvider(session, host),
		Model:        resolveLocalChatAgentModel(session, host),
		SystemPrompt: composeLocalChatSystemPrompt(session, workspaceRoot),
		MaxSteps:     0,
	}
	if session != nil {
		if maxTokens := session.Provider.GetMaxTokensLimit(); maxTokens > 0 {
			agentConfig.DefaultMaxTokens = maxTokens
		}
	}
	if strings.TrimSpace(requestedModel) != "" {
		agentConfig.Model = strings.TrimSpace(requestedModel)
	}
	if runtimeConfig != nil {
		agentConfig.MaxSteps = agent.NormalizeMaxSteps(runtimeConfig.Agent.MaxMaxSteps)
	}
	workspaceMode := resolveLocalChatWorkspaceMode(runtimeConfig)
	workspaceContextEnabled := workspaceMode != "" && !strings.EqualFold(workspaceMode, contextmgr.WorkspaceModeDisabled)
	if session.Stream || (workspaceRoot != "" && workspaceContextEnabled) || len(session.ProfileContext) > 0 {
		agentConfig.Options = make(map[string]interface{})
		if session.Stream {
			agentConfig.Options["stream"] = true
		}
		if reasoningEffort := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort); reasoningEffort != "" {
			agentConfig.Options["reasoning_effort"] = reasoningEffort
		}
		if workspaceRoot != "" && workspaceContextEnabled {
			agentConfig.Options["workspace_path"] = workspaceRoot
			agentConfig.Options["context_workspace_mode"] = workspaceMode
			agentConfig.Options["context_min_workspace_query_length"] = 4
		}
		if len(session.ProfileContext) > 0 {
			agentConfig.Options["profile_context"] = cloneSkillContextMap(session.ProfileContext)
		}
	}
	applyLocalChatContextOptions(agentConfig, runtimeConfig)

	apiAgent := agent.NewAgentWithLLM(agentConfig, host.ToolSurface, host.Bootstrap.LLMRuntime())
	if registry := host.Bootstrap.Registry(); registry != nil {
		for _, summary := range registry.ListSummaries() {
			if summary == nil {
				continue
			}
			_ = apiAgent.RegisterSkill(summary.ToSkillStub())
		}
	}
	if embeddingRouter := host.Bootstrap.EmbeddingRouter(); embeddingRouter != nil {
		if cloned, err := embeddingRouter.CloneForRegistry(apiAgent.GetSkillRouter().Registry()); err == nil {
			apiAgent.GetSkillRouter().SetEmbeddingRouter(cloned)
		}
	}
	if host.EventBus != nil {
		apiAgent.SetEventBus(host.EventBus)
	}
	if host.TeamStore != nil {
		if ctxMgr := apiAgent.GetContextManager(); ctxMgr != nil {
			ctxMgr.TeamContext = team.NewContextBuilder(host.TeamStore)
		}
	}
	if host.TeamStore != nil {
		broker := apiAgent.GetToolBroker()
		if broker == nil {
			broker = &toolbroker.Broker{}
			apiAgent.SetToolBroker(broker)
		}
		if broker.SessionContextStore == nil {
			broker.SessionContextStore = toolbrokersessionctx.New(host.SessionStore)
		}
		broker.AgentSessions = host.ActorRegistry
		broker.TeamStore = host.TeamStore
		broker.TeamClaims = host.TeamClaims
		broker.TeamDispatcher = host.ActorRegistry
		broker.TeamLifecycleChanged = host.syncTeamLifecycleLoops
		if host.Orchestrator != nil {
			broker.TeamPlanner = host.Orchestrator.LeadPlanner
		}
	}
	if apiAgent.GetToolBroker() == nil && host.ActorRegistry != nil {
		apiAgent.SetToolBroker(&toolbroker.Broker{
			AgentSessions:       host.ActorRegistry,
			SessionContextStore: toolbrokersessionctx.New(host.SessionStore),
		})
	} else if broker := apiAgent.GetToolBroker(); broker != nil && broker.AgentSessions == nil && host.ActorRegistry != nil {
		broker.AgentSessions = host.ActorRegistry
	}
	if broker := apiAgent.GetToolBroker(); broker != nil && broker.SessionContextStore == nil {
		broker.SessionContextStore = toolbrokersessionctx.New(host.SessionStore)
	}
	if toolPolicy := buildLocalChatToolPolicy(session, host.ToolSurface, apiAgent.GetToolBroker()); toolPolicy != nil {
		apiAgent.SetToolExecutionPolicy(toolPolicy)
	}
	if runtimeConfig != nil && len(runtimeConfig.Hooks) > 0 {
		apiAgent.SetHookManager(runtimehooks.NewManager(runtimeConfig.Hooks))
	}

	return apiAgent
}

func resolveLocalChatWorkspaceMode(runtimeConfig *runtimecfg.RuntimeConfig) string {
	if runtimeConfig == nil {
		return ""
	}
	if mode := strings.TrimSpace(runtimeConfig.Context.WorkspaceMode); mode != "" {
		return strings.ToLower(mode)
	}
	if mode := strings.TrimSpace(runtimeConfig.Workspace.Mode); mode != "" {
		return strings.ToLower(mode)
	}
	if runtimeConfig.Workspace.Enabled {
		return contextmgr.WorkspaceModeSignals
	}
	return ""
}

func composeLocalChatSystemPrompt(session *ChatSession, workspaceRoot string) string {
	base := strings.TrimSpace(composeChatSystemPromptWithGuidance(session))

	lines := []string{}
	if base != "" {
		lines = append(lines, base)
	}
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot != "" {
		lines = append(lines,
			fmt.Sprintf("Current workspace root: %s", workspaceRoot),
			"Interpret \"当前目录\", \".\", and relative paths as relative to the current workspace root unless the user explicitly says otherwise.",
			"If the user asks to inspect or search the current workspace, do that directly instead of asking which current directory they mean.",
			"When planning file or directory work, only use paths that you directly confirmed from tool output in the current workspace. Do not invent sibling directories or extrapolate missing paths from naming patterns.",
			"Team-only tools such as read_task_spec, read_task_context, send_team_message, read_mailbox_digest, report_task_outcome, and block_current_task require an active team run. Only call them after spawn_team has created the team run or when the current chat is already bound to an active team task.",
			"When calling team tools, leave teammate session_id unset unless you truly need a fixed explicit session. Never use session_id=\"current\" for teammates.",
			"When calling spawn_team from the current chat, do not set lead_session_id unless the user explicitly asked for a different lead session. The current session will be used automatically.",
			"When you call spawn_team with auto_start=true, treat the delegated work as already in progress. Do not ask the user to choose the next step while the team is running; instead briefly state that the team is working in the background and that you will summarize when it finishes.",
			"Do not use wait_agent or read_agent_events for spawn_team teammate ids such as member-1. Those tools are only for spawn_agent child sessions; team progress arrives through team lifecycle events and the final team.summary.",
		)
	}
	return strings.Join(lines, "\n\n")
}

func buildLocalChatToolPolicy(session *ChatSession, toolSurface runtimeskill.MCPManager, broker *toolbroker.Broker) *runtimepolicy.ToolExecutionPolicy {
	if session == nil {
		return nil
	}
	policy := session.ToolPolicy.Clone()
	if policy == nil {
		switch {
		case session.DisableTools:
			policy = runtimepolicy.NewToolExecutionPolicy([]string{}, false)
		case toolSurface != nil || broker != nil:
			var allowedTools []string
			if toolSurface != nil {
				allowedTools = runtimeToolNames(toolSurface.ListTools())
			}
			allowedTools = append(allowedTools, brokerToolNames(broker.Definitions())...)
			if allowedTools == nil {
				allowedTools = []string{}
			}
			policy = runtimepolicy.NewToolExecutionPolicy(allowedTools, false)
		}
	}
	if session.DisableTools {
		if policy == nil {
			policy = runtimepolicy.NewToolExecutionPolicy([]string{}, false)
		}
		policy.AllowlistEnabled = true
		policy.AllowedTools = map[string]bool{}
	}
	return policy
}

func buildLocalChatLoopConfig(runtimeConfig *runtimecfg.RuntimeConfig, session *ChatSession) *agent.LoopReActConfig {
	config := &agent.LoopReActConfig{
		MaxSteps:             0,
		EnableThought:        true,
		EnableToolCalls:      true,
		EnableParallelTools:  false,
		MaxParallelToolCalls: 1,
		Temperature:          0.7,
	}
	if runtimeConfig != nil {
		config.MaxSteps = agent.NormalizeMaxSteps(runtimeConfig.Agent.MaxMaxSteps)
		config.EnableParallelTools = runtimeConfig.Agent.EnableParallelTools
		if runtimeConfig.Agent.MaxParallelToolCalls > 0 {
			config.MaxParallelToolCalls = runtimeConfig.Agent.MaxParallelToolCalls
		}
	}
	if session != nil {
		if reasoningEffort := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort); reasoningEffort != "" {
			config.ReasoningEffort = reasoningEffort
		}
	}
	return config
}

func applyLocalChatContextOptions(agentConfig *agent.Config, runtimeConfig *runtimecfg.RuntimeConfig) {
	if agentConfig == nil || runtimeConfig == nil {
		return
	}
	ctxCfg := runtimeConfig.Context
	wsCfg := runtimeConfig.Workspace
	hasContextOptions := strings.TrimSpace(ctxCfg.Profile) != "" ||
		strings.TrimSpace(ctxCfg.CompactionMode) != "" ||
		strings.TrimSpace(ctxCfg.RecallMode) != "" ||
		strings.TrimSpace(ctxCfg.ObservationMode) != "" ||
		strings.TrimSpace(ctxCfg.WorkspaceMode) != "" ||
		ctxCfg.MinCompactionMessages > 0 ||
		ctxCfg.MinRecallQueryLength > 0 ||
		ctxCfg.LedgerLoadLimit > 0 ||
		ctxCfg.MaxPromptTokens > 0 ||
		ctxCfg.FallbackMaxPromptTokens > 0 ||
		ctxCfg.MaxMessages > 0 ||
		ctxCfg.KeepRecentMessages > 0 ||
		ctxCfg.MaxRecallResults > 0 ||
		ctxCfg.MaxObservationItems > 0
	hasWorkspaceOptions := wsCfg.MaxFileSize > 0 ||
		strings.TrimSpace(wsCfg.Mode) != "" ||
		wsCfg.MaxChunkSize > 0 ||
		wsCfg.ChunkOverlap > 0 ||
		len(wsCfg.Include) > 0 ||
		len(wsCfg.Exclude) > 0
	if !hasContextOptions && !hasWorkspaceOptions {
		return
	}
	if agentConfig.Options == nil {
		agentConfig.Options = make(map[string]interface{})
	}
	if strings.TrimSpace(ctxCfg.Profile) != "" {
		agentConfig.Options["context_profile"] = strings.TrimSpace(ctxCfg.Profile)
	}
	if strings.TrimSpace(ctxCfg.CompactionMode) != "" {
		agentConfig.Options["context_compaction_mode"] = strings.TrimSpace(ctxCfg.CompactionMode)
	}
	if strings.TrimSpace(ctxCfg.RecallMode) != "" {
		agentConfig.Options["context_recall_mode"] = strings.TrimSpace(ctxCfg.RecallMode)
	}
	if strings.TrimSpace(ctxCfg.ObservationMode) != "" {
		agentConfig.Options["context_observation_mode"] = strings.TrimSpace(ctxCfg.ObservationMode)
	}
	if strings.TrimSpace(ctxCfg.WorkspaceMode) != "" {
		agentConfig.Options["context_workspace_mode"] = strings.ToLower(strings.TrimSpace(ctxCfg.WorkspaceMode))
	} else if strings.TrimSpace(wsCfg.Mode) != "" {
		agentConfig.Options["context_workspace_mode"] = strings.ToLower(strings.TrimSpace(wsCfg.Mode))
	}
	if ctxCfg.MinCompactionMessages > 0 {
		agentConfig.Options["context_min_compaction_messages"] = ctxCfg.MinCompactionMessages
	}
	if ctxCfg.MinRecallQueryLength > 0 {
		agentConfig.Options["context_min_recall_query_length"] = ctxCfg.MinRecallQueryLength
	}
	if ctxCfg.LedgerLoadLimit > 0 {
		agentConfig.Options["context_ledger_load_limit"] = ctxCfg.LedgerLoadLimit
	}
	if ctxCfg.MaxPromptTokens > 0 {
		agentConfig.Options["context_max_prompt_tokens"] = ctxCfg.MaxPromptTokens
	}
	if ctxCfg.FallbackMaxPromptTokens > 0 {
		agentConfig.Options["context_fallback_max_prompt_tokens"] = ctxCfg.FallbackMaxPromptTokens
	}
	if ctxCfg.MaxMessages > 0 {
		agentConfig.Options["context_max_messages"] = ctxCfg.MaxMessages
	}
	if ctxCfg.KeepRecentMessages > 0 {
		agentConfig.Options["context_keep_recent_messages"] = ctxCfg.KeepRecentMessages
	}
	if ctxCfg.MaxRecallResults > 0 {
		agentConfig.Options["context_max_recall_results"] = ctxCfg.MaxRecallResults
	}
	if ctxCfg.MaxObservationItems > 0 {
		agentConfig.Options["context_max_observation_items"] = ctxCfg.MaxObservationItems
	}

	if wsCfg.MaxFileSize > 0 {
		agentConfig.Options["workspace_max_file_size"] = wsCfg.MaxFileSize
	}
	if wsCfg.MaxChunkSize > 0 {
		agentConfig.Options["workspace_max_chunk_size"] = wsCfg.MaxChunkSize
	}
	if wsCfg.ChunkOverlap > 0 {
		agentConfig.Options["workspace_chunk_overlap"] = wsCfg.ChunkOverlap
	}
	if len(wsCfg.Include) > 0 {
		agentConfig.Options["workspace_include"] = append([]string(nil), wsCfg.Include...)
	}
	if len(wsCfg.Exclude) > 0 {
		agentConfig.Options["workspace_exclude"] = append([]string(nil), wsCfg.Exclude...)
	}
}

func loadLocalChatRuntimeConfig(cfg *config.Config, session *ChatSession) (*runtimecfg.RuntimeConfig, error) {
	configPath := resolveChatRuntimeConfigPath(cfg, session)
	if strings.TrimSpace(configPath) == "" {
		config := runtimecfg.DefaultRuntimeConfig()
		applyLocalChatRuntimePersistenceDefaults(config, session)
		return config, nil
	}
	manager := runtimecfg.NewRuntimeManager(configPath)
	if err := manager.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 加载 actor runtime 配置失败，已退回默认配置: %v\n", err)
		config := runtimecfg.DefaultRuntimeConfig()
		applyLocalChatRuntimePersistenceDefaults(config, session)
		return config, nil
	}
	config := manager.Get()
	if session != nil && session.Model != "" {
		config.Agent.DefaultModel = session.Model
	}
	applyLocalChatRuntimePersistenceDefaults(config, session)
	return config, nil
}

func applyLocalChatRuntimePersistenceDefaults(config *runtimecfg.RuntimeConfig, session *ChatSession) {
	if config == nil || session == nil {
		return
	}
	if strings.TrimSpace(config.Team.StorePath) == "" && strings.TrimSpace(config.Team.StoreDSN) == "" {
		if teamStorePath := resolveLocalChatTeamStorePath(session); teamStorePath != "" {
			config.Team.StorePath = teamStorePath
		}
	}
}

func ensureLocalRuntimeProvider(runtime *runtimellm.LLMRuntime, session *ChatSession) error {
	if runtime == nil || session == nil {
		return nil
	}
	providerName := strings.TrimSpace(session.ProviderName)
	if providerName == "" {
		return nil
	}
	if _, err := runtime.GetProvider(providerName); err != nil {
		provider, buildErr := runtimellm.NewProvider(&runtimellm.ProviderConfig{
			Type:              session.Provider.GetType(),
			APIKey:            session.Provider.GetAPIKey(),
			BaseURL:           session.Provider.BaseURL,
			APIPath:           session.Provider.APIPath,
			Timeout:           session.Provider.Timeout,
			MaxRetries:        3,
			DefaultModel:      session.Provider.DefaultModel,
			SupportedModels:   append([]string(nil), session.Provider.SupportedModels...),
			ModelMappings:     cloneStringMap(session.Provider.ModelMappings),
			ModelCapabilities: cloneProviderModelCapabilities(session.Provider.ModelCapabilities),
			Headers:           nil,
			HeaderMappings:    cloneStringMap(session.Provider.HeaderMappings),
			Proxy:             session.Provider.Proxy.Clone(),
			RequestsPerMinute: session.Provider.RequestsPerMinute,
		})
		if buildErr != nil {
			return buildErr
		}
		if registerErr := runtime.RegisterProvider(providerName, provider); registerErr != nil {
			return registerErr
		}
	}
	aliases := []string{session.Model, session.Provider.DefaultModel}
	aliases = append(aliases, session.Provider.SupportedModels...)
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		_ = runtime.RegisterProviderAlias(alias, providerName)
	}
	return nil
}

func buildLocalChatRuntimeStores(session *ChatSession) (runtimechat.RuntimeStateStore, runtimechat.EventStore) {
	storePath := resolveLocalChatRuntimeStorePath(session)
	if storePath != "" {
		store, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{Path: storePath})
		if err == nil {
			return store, store
		}
		fmt.Fprintf(os.Stderr, "Warning: 初始化 actor runtime store 失败，已退回内存模式: %v\n", err)
	}
	memoryStore := runtimechat.NewInMemoryRuntimeStore(2048)
	return memoryStore, memoryStore
}

func closeLocalRuntimeStores(store runtimechat.RuntimeStateStore, eventStore runtimechat.EventStore) {
	seen := map[interface{}]struct{}{}
	closeStore := func(value interface{}) {
		if value == nil {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		if closer, ok := value.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	closeStore(store)
	closeStore(eventStore)
}

func resolveLocalChatRuntimeStorePath(session *ChatSession) string {
	if session == nil || strings.TrimSpace(session.SessionDir) == "" {
		return ""
	}
	return filepath.Join(session.SessionDir, "runtime", "chat_runtime.sqlite")
}

func resolveLocalChatTeamStorePath(session *ChatSession) string {
	if session == nil || strings.TrimSpace(session.SessionDir) == "" {
		return ""
	}
	return filepath.Join(session.SessionDir, "runtime", "team_store.sqlite")
}

func resolveLocalWorkspacePath(runtimeConfig *runtimecfg.RuntimeConfig, session *ChatSession) string {
	if runtimeConfig != nil && strings.TrimSpace(runtimeConfig.Workspace.Root) != "" {
		root := strings.TrimSpace(runtimeConfig.Workspace.Root)
		if filepath.IsAbs(root) {
			return root
		}
		if session != nil && strings.TrimSpace(session.ProfileRoot) != "" {
			return filepath.Clean(filepath.Join(strings.TrimSpace(session.ProfileRoot), root))
		}
		if cwd, err := os.Getwd(); err == nil {
			return filepath.Clean(filepath.Join(cwd, root))
		}
		return root
	}
	if session != nil && strings.TrimSpace(session.ProfileRoot) != "" {
		return strings.TrimSpace(session.ProfileRoot)
	}
	if cwd, err := os.Getwd(); err == nil {
		if gitRoot := findGitRoot(cwd); gitRoot != "" {
			return gitRoot
		}
		return cwd
	}
	return ""
}

// findGitRoot walks upward from start looking for a .git directory or file
// (worktrees use a file). Returns the first ancestor containing .git, or "".
func findGitRoot(start string) string {
	dir := filepath.Clean(start)
	for {
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func resolveLocalChatAgentProvider(session *ChatSession, host *localChatRuntimeHost) string {
	if session != nil && strings.TrimSpace(session.ProviderName) != "" {
		return strings.TrimSpace(session.ProviderName)
	}
	if host != nil && host.Bootstrap != nil && host.Bootstrap.Config() != nil && strings.TrimSpace(host.Bootstrap.Config().Agent.DefaultProvider) != "" {
		return strings.TrimSpace(host.Bootstrap.Config().Agent.DefaultProvider)
	}
	if host != nil && host.Bootstrap != nil && host.Bootstrap.LLMRuntime() != nil {
		return strings.TrimSpace(host.Bootstrap.LLMRuntime().DefaultProvider())
	}
	return ""
}

func resolveLocalChatAgentModel(session *ChatSession, host *localChatRuntimeHost) string {
	if session != nil && strings.TrimSpace(session.Model) != "" {
		return strings.TrimSpace(session.Model)
	}
	if host != nil && host.Bootstrap != nil && host.Bootstrap.Config() != nil && strings.TrimSpace(host.Bootstrap.Config().Agent.DefaultModel) != "" {
		return strings.TrimSpace(host.Bootstrap.Config().Agent.DefaultModel)
	}
	if host != nil && host.Bootstrap != nil && host.Bootstrap.LLMRuntime() != nil {
		return strings.TrimSpace(host.Bootstrap.LLMRuntime().DefaultModel())
	}
	return ""
}

func runtimeToolNames(tools []runtimeskill.ToolInfo) []string {
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func brokerToolNames(definitions []runtimetypes.ToolDefinition) []string {
	if len(definitions) == 0 {
		return nil
	}
	names := make([]string, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, def := range definitions {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func (h *localChatRuntimeHost) syncTeamLifecycleLoops() {
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		lifecycle.SyncLoops()
	}
}

func (h *localChatRuntimeHost) bindTeamLifecycleEvents() {
	if h == nil || h.Orchestrator == nil || h.EventBus == nil {
		return
	}
	events := h.Orchestrator.Events
	if events == nil {
		events = team.NewTeamEventBus()
		h.Orchestrator.Events = events
	}
	events.Subscribe("", h.publishTeamLifecycleEvent)
}

func (h *localChatRuntimeHost) publishTeamLifecycleEvent(event team.TeamEvent) {
	h.dispatchTeamLifecycleEvent(event, true)
}

func (h *localChatRuntimeHost) dispatchTeamLifecycleEvent(event team.TeamEvent, persist bool) {
	if h == nil || strings.TrimSpace(event.Type) == "" {
		return
	}
	payload := make(map[string]interface{}, len(event.Payload)+1)
	for key, value := range event.Payload {
		payload[key] = value
	}
	if strings.TrimSpace(event.TeamID) != "" {
		payload["team_id"] = strings.TrimSpace(event.TeamID)
	}
	baseSessionID := h.baseRuntimeSessionID()
	sessionID := h.teamLifecycleEventSessionID(context.Background(), strings.TrimSpace(event.TeamID), baseSessionID)
	runtimeEvent := runtimeevents.Event{
		Type:      strings.TrimSpace(event.Type),
		AgentName: "team-orchestrator",
		SessionID: sessionID,
		Payload:   payload,
		Timestamp: event.Timestamp,
	}
	if persist && h.EventStore != nil {
		if seq, err := h.EventStore.AppendEvent(context.Background(), runtimeEvent); err == nil {
			if runtimeEvent.Payload == nil {
				runtimeEvent.Payload = map[string]interface{}{}
			}
			runtimeEvent.Payload["seq"] = seq
		}
	}
	if h.isLifecycleEventForBaseSession(sessionID) {
		if lifecycle := h.teamLifecycleService(); lifecycle != nil {
			lifecycle.Apply(runtimeEvent)
		}
		if h.EventBus != nil {
			h.EventBus.Publish(runtimeEvent)
		}
	}
	if h.BaseSession != nil && h.isLifecycleEventForBaseSession(sessionID) {
		warnIfChatSessionSyncFails(h.BaseSession, "team lifecycle sync", syncAmbientTeamLifecycleState(h.BaseSession))
	}
}

func (h *localChatRuntimeHost) baseRuntimeSessionID() string {
	if h == nil || h.BaseSession == nil || h.BaseSession.RuntimeSession == nil {
		return ""
	}
	return strings.TrimSpace(h.BaseSession.RuntimeSession.ID)
}

func (h *localChatRuntimeHost) teamLifecycleEventSessionID(ctx context.Context, teamID string, fallback string) string {
	teamID = strings.TrimSpace(teamID)
	if h != nil && h.TeamStore != nil && teamID != "" {
		if record, err := h.TeamStore.GetTeam(ctx, teamID); err == nil && record != nil {
			if leadSessionID := strings.TrimSpace(record.LeadSessionID); leadSessionID != "" {
				return leadSessionID
			}
		}
	}
	return strings.TrimSpace(fallback)
}

func (h *localChatRuntimeHost) isLifecycleEventForBaseSession(sessionID string) bool {
	baseSessionID := h.baseRuntimeSessionID()
	sessionID = strings.TrimSpace(sessionID)
	return baseSessionID == "" || sessionID == "" || strings.EqualFold(sessionID, baseSessionID)
}

func (h *localChatRuntimeHost) teamLifecycleService() teamLifecycleService {
	if h == nil {
		return nil
	}
	if h.TeamLifecycle != nil {
		return h.TeamLifecycle
	}
	h.TeamLifecycle = newLocalTeamLifecycleService(h)
	return h.TeamLifecycle
}

func (h *localChatRuntimeHost) mirrorTeamSummaryToBaseSession(teamID, summary string) {
	if h == nil || h.BaseSession == nil || h.BaseSession.SessionManager == nil || h.BaseSession.RuntimeSession == nil {
		return
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return
	}

	sessionID := strings.TrimSpace(h.BaseSession.RuntimeSession.ID)
	if sessionID == "" {
		return
	}
	ctx := context.Background()
	runtimeSession, err := h.BaseSession.SessionManager.Get(ctx, sessionID)
	if err != nil || runtimeSession == nil {
		return
	}
	for _, message := range runtimeSession.History {
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		if strings.TrimSpace(message.Content) == summary {
			return
		}
	}

	message := runtimetypes.NewAssistantMessage(summary)
	if strings.TrimSpace(teamID) != "" {
		if message.Metadata == nil {
			message.Metadata = runtimetypes.NewMetadata()
		}
		message.Metadata["team_id"] = strings.TrimSpace(teamID)
	}
	if err := h.BaseSession.SessionManager.AddMessage(ctx, sessionID, *message); err != nil {
		return
	}
	updated, err := h.BaseSession.SessionManager.Get(ctx, sessionID)
	if err != nil || updated == nil {
		return
	}
	_ = restoreChatStateFromRuntimeSession(h.BaseSession, updated)
	inferAmbientTeamBinding(h.BaseSession, updated)
}

func (h *localChatRuntimeHost) replayStoredTerminalTeamLifecycleEvents(teamID string) {
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		lifecycle.PublishStoredTerminalEvents(teamID)
	}
}

func (h *localChatRuntimeHost) stopTeamLifecycleLoops() {
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		lifecycle.StopLoops()
	}
}
