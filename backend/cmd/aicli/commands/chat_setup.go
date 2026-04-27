package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func buildChatSession(cfg *config.Config, opts *chatCommandOptions, profileState *chatProfileState, persistenceState *chatPersistenceState, runtimeState *chatRuntimeState) (*ChatSession, func(), error) {
	if opts == nil || runtimeState == nil {
		return nil, nil, fmt.Errorf("chat setup requires options and runtime state")
	}

	cancelCtx, cancelFunc := context.WithCancel(context.Background())
	registry := functions.NewFunctionRegistry()
	functionCatalog := newAICLIFunctionCatalog(runtimeState.provider.GetProtocol(), registry)

	logger := NewChatLogger(runtimeState.providerName, runtimeState.provider.GetProtocol(), runtimeState.modelName, runtimeState.shouldStream, runtimeState.baseURL)
	if opts.LogDir != "" {
		if err := logger.SetLogDir(opts.LogDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to set log directory: %v\n", err)
		}
	}
	if opts.Message != "" {
		logger.SetInitialMessage(opts.Message)
	}

	if opts.NoInteractive {
		mcpmanager.SetStatusOutput(io.Discard)
	} else {
		mcpmanager.SetStatusOutput(newChatSystemOutputWriter(os.Stdout))
	}

	var (
		layout     *ui.Layout
		inputBox   *ui.InputBox
		keyHandler *ui.KeyHandler
	)
	if !opts.NoInteractive {
		layout = ui.NewLayout(ui.LayoutAdvanced)
		layout.Enable()
		inputBox = ui.NewInputBox(layout)
		keyHandler = ui.NewKeyHandler()
		keyHandler.Start()
	}

	session := &ChatSession{
		ProviderName:       runtimeState.providerName,
		Provider:           runtimeState.provider,
		Adapter:            runtimeState.adapter,
		Model:              runtimeState.modelName,
		ReasoningEffort:    runtimetypes.NormalizeReasoningEffort(runtimeState.reasoningEffort),
		DisableTools:       opts.DisableTools,
		HTTPDebug:          opts.HTTPDebug,
		Stream:             runtimeState.shouldStream,
		BaseURL:            runtimeState.baseURL,
		Messages:           []map[string]interface{}{},
		HTTPClient:         httpclient.GetHTTPClientWithProvider(cfg, &runtimeState.provider),
		cancelCtx:          cancelCtx,
		cancelFunc:         cancelFunc,
		interrupted:        atomic.Bool{},
		FunctionCatalog:    functionCatalog,
		FunctionRegistry:   registry,
		FunctionBuilder:    functionCatalog.Builder(runtimeState.provider.GetProtocol()),
		Logger:             logger,
		Formatter:          formatter.NewMarkdownFormatter(true),
		Layout:             layout,
		InputBox:           inputBox,
		KeyHandler:         keyHandler,
		TokenCount:         0,
		MsgCount:           0,
		TurnRequestCount:   0,
		SessionManager:     persistenceState.runtimeSessionManager,
		RuntimeSession:     nil,
		SessionUserID:      persistenceState.sessionUserID,
		SessionDir:         persistenceState.resolvedSessionDir,
		SessionFilter:      opts.SessionFilter,
		NoInteractive:      opts.NoInteractive,
		JSONOutput:         opts.OutputFormat == "json",
		JSONEnvelope:       opts.JSONEnvelope,
		MCPStatus:          nil,
		MCPEnabled:         false,
		SkillsMode:         opts.CLISkillsMode,
		SkillsDebug:        opts.CLISkillsDebug,
		RetryConfig:        runtimeState.retryCfg,
		RequestTimeout:     runtimeState.requestTimeout,
		OutputFormat:       opts.OutputFormat,
		InputReader:        chatOptionInputReader(opts),
		PermissionMode:     opts.PermissionMode,
		ApprovalReuseMode:  opts.ApprovalReuseMode,
		ChatExecutor:       newAICLISharedChatExecutor(),
		runtimeHTTPCapture: &chatRuntimeHTTPCapture{},
		ImagePaths:         opts.ImagePaths,
	}
	session.Interaction = newChatInteractionCoordinator(session)
	if profileState != nil && profileState.Active() {
		session.ProfileName = profileState.Resolved.ProfileName
		session.ProfileAgent = profileState.Resolved.AgentID
		session.ProfileRoot = profileState.Resolved.ProfileRoot
		session.SystemPromptText = profileState.PromptText
		session.RuntimeConfigPath = profileState.RuntimeConfigPath()
		session.MCPConfigPath = profileState.MCPConfigPath()
		session.ResolvedSkillDirs = profileState.SkillDirs()
		session.ProfileContext = cloneSkillContextMap(profileState.ContextValues)
		session.ToolPolicy = profileState.ToolPolicy
		if session.FunctionCatalog != nil && session.ToolPolicy != nil {
			session.FunctionCatalog.SetToolPolicy(session.ToolPolicy)
		}
	}

	cleanup := func() {
		mcpmanager.SetStatusOutput(os.Stdout)
		if keyHandler != nil {
			keyHandler.Stop()
		}
	}

	return session, cleanup, nil
}

func restoreChatPersistenceState(session *ChatSession, persistenceState *chatPersistenceState, opts *chatCommandOptions) error {
	if session == nil || opts == nil || persistenceState == nil {
		return nil
	}

	if persistenceState.loadedRuntimeSession != nil {
		if err := restoreChatStateFromRuntimeSession(session, persistenceState.loadedRuntimeSession); err != nil {
			return fmt.Errorf("恢复会话失败: %w", err)
		}
		ensureChatSystemPromptMessage(session)
		if opts.SessionTitleFlag != "" && session.RuntimeSession != nil {
			session.RuntimeSession.UpdateTitle(opts.SessionTitleFlag)
		}
		warnIfChatSessionSyncFails(session, "restore session", syncRuntimeSessionFromChat(session))
		return nil
	}

	if persistenceState.runtimeSessionManager == nil {
		return nil
	}

	if err := createNewRuntimeConversation(session, opts.SessionTitleFlag); err != nil {
		if opts.SessionFeaturesRequested {
			return fmt.Errorf("创建会话失败: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Warning: 创建持久化会话失败，当前会话不会保存: %v\n", err)
		session.SessionManager = nil
		session.RuntimeSession = nil
		session.SessionUserID = ""
		session.SessionDir = ""
	}
	ensureChatSystemPromptMessage(session)
	warnIfChatSessionSyncFails(session, "init session prompt", syncRuntimeSessionFromChat(session))
	return nil
}

func initializeChatCapabilities(cfg *config.Config, opts *chatCommandOptions, session *ChatSession) (*skillsRuntimeBinding, func(), error) {
	if session == nil || opts == nil {
		return nil, nil, nil
	}

	var (
		skillsBinding *skillsRuntimeBinding
		toolManager   *runtimetools.Manager
	)

	if session.DisableTools {
		logpkg.Info("AICLI chat tools exposure disabled by flag")
	} else {
		if err := initMCPManager(resolveChatMCPConfigPath(cfg, session)); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: 初始化 MCP 失败: %v\n", err)
			logpkg.Warnf("AICLI MCP init failed: %v", err)
		}

		toolManager = runtimetools.NewDefaultManagerWithRuntimeConfig(MCPManagerInstance, loadRuntimeToolConfig(cfg, session))
		toolDescs := toolManager.ListTools()
		for _, desc := range toolDescs {
			session.FunctionCatalog.RegisterBuiltinToolFunction(functions.NewRuntimeToolFunction(toolManager, desc), desc)
		}
		if MCPManagerInstance != nil {
			session.MCPStatus = Status()
			session.MCPEnabled = session.MCPStatus.Enabled
		}
		if len(toolDescs) == 0 {
			logpkg.Warn("AICLI tool registry is empty (no toolkit or MCP tools loaded)")
		} else {
			toolNames := make([]string, 0, len(toolDescs))
			for _, tool := range toolDescs {
				toolNames = append(toolNames, tool.Name)
			}
			sort.Strings(toolNames)
			logpkg.Infof("AICLI tools loaded: %d (%s)", len(toolNames), strings.Join(toolNames, ", "))
		}

		var err error
		skillsBinding, err = initSkillFunctions(cfg, session, opts.CLISkillDirs, opts.CLISkillsTopK, opts.CLISkillsMode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: 初始化 Skills 失败: %v\n", err)
		} else if skillsBinding != nil {
			session.SkillsBinding = skillsBinding
			if session.FunctionCatalog != nil {
				session.FunctionCatalog.SetSkillsBinding(skillsBinding)
			}
		}
	}

	localRuntimeHost, hostErr := initializeLocalChatRuntimeHost(cfg, session, toolManager)
	if hostErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: 初始化 actor runtime host 失败，继续使用 legacy chat executor: %v\n", hostErr)
		logpkg.Warnf("AICLI actor runtime host init failed: %v", hostErr)
	} else if localRuntimeHost != nil {
		session.LocalRuntimeHost = localRuntimeHost
		session.ActorFirstReady = true
		restoreLocalRuntimeHostTeamState(session)
		session.ChatExecutor = newAICLIActorChatExecutor()
	}

	refreshBuiltinFunctionSchemas(session)
	if session.FunctionCatalog != nil {
		stats := session.FunctionCatalog.Stats()
		logpkg.Infof("AICLI function catalog ready: total=%d builtin_tools=%d skill_functions=%d",
			stats.TotalFunctions, stats.BuiltinTools, stats.SkillFunctions)
	}

	cleanup := func() {
		if session.LocalRuntimeHost != nil {
			session.LocalRuntimeHost.Close()
		}
		if skillsBinding != nil {
			if stopErr := skillsBinding.Close(); stopErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: 停止 Skills Runtime 失败: %v\n", stopErr)
			}
		}
	}

	return skillsBinding, cleanup, nil
}

func bootstrapChatSession(cfg *config.Config, opts *chatCommandOptions, profileState *chatProfileState, persistenceState *chatPersistenceState, runtimeState *chatRuntimeState) (*ChatSession, func(), error) {
	session, cleanupSession, err := buildChatSession(cfg, opts, profileState, persistenceState, runtimeState)
	if err != nil {
		return nil, nil, err
	}

	if err := restoreChatPersistenceState(session, persistenceState, opts); err != nil {
		if cleanupSession != nil {
			cleanupSession()
		}
		return nil, nil, err
	}

	_, cleanupCapabilities, err := initializeChatCapabilities(cfg, opts, session)
	if err != nil {
		if cleanupSession != nil {
			cleanupSession()
		}
		return nil, nil, err
	}

	cleanup := func() {
		if cleanupCapabilities != nil {
			cleanupCapabilities()
		}
		if cleanupSession != nil {
			cleanupSession()
		}
	}

	return session, cleanup, nil
}

func restoreLocalRuntimeHostTeamState(session *ChatSession) {
	if session == nil || session.LocalRuntimeHost == nil {
		return
	}
	if restoreAmbientTeamBindingFromRuntimeStore(session) {
		warnIfChatSessionSyncFails(session, "restore ambient team binding", syncRuntimeSessionFromChat(session))
	}
	validateAmbientTeamBinding(session, session.LocalRuntimeHost.TeamStore)
	if session.ActiveTeam != nil && strings.TrimSpace(session.ActiveTeam.TeamID) != "" {
		session.LocalRuntimeHost.replayStoredTerminalTeamLifecycleEvents(session.ActiveTeam.TeamID)
	}
	warnIfChatSessionSyncFails(session, "sync ambient team lifecycle state", syncAmbientTeamLifecycleState(session))
}

func presentChatSession(session *ChatSession) {
	if session == nil || !shouldPrintChatSessionPreamble(session) {
		return
	}

	printSessionInfo(session)
	printCurrentRuntimeSession(session)
	if !session.DisableTools && session.SkillsBinding != nil && session.SkillsBinding.Count() > 0 {
		printChatSessionMetaRow("Skills:", fmt.Sprintf("已启用 (%d 个 AI 可调用 skills)", session.SkillsBinding.Count()))
		printChatSessionMetaRow("Skills Mode:", resolvedChatSkillsMode(session, session.SkillsBinding))
		printChatSessionMetaRow("Skills Top-K:", fmt.Sprintf("%d", resolvedChatSkillsTopK(session.SkillsBinding)))
	}
}

func finalizeChatSession(session *ChatSession) {
	if session == nil {
		return
	}

	awaitNoInteractiveLocalTeamDrain(session)
	warnIfChatSessionSyncFails(session, "shutdown", syncRuntimeSessionFromChat(session))
	if session.Logger != nil && session.Logger.logDir != "" {
		if err := session.Logger.SaveSession(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to save chat logs: %v\n", err)
		} else if shouldPrintChatSessionPreamble(session) {
			fmt.Printf("会话日志已保存到: %s\n", session.Logger.logDir)
		}
	}
}
