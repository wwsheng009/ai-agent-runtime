package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/term"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func newChatMarkdownFormatter() *formatter.MarkdownFormatter {
	f := formatter.NewMarkdownFormatter(true)
	f.ThemeContextProvider = ui.CurrentThemeContext
	return f
}

func buildChatSession(cfg *config.Config, opts *chatCommandOptions, profileState *chatProfileState, persistenceState *chatPersistenceState, runtimeState *chatRuntimeState) (*ChatSession, func(), error) {
	if opts == nil || runtimeState == nil {
		return nil, nil, fmt.Errorf("chat setup requires options and runtime state")
	}

	cancelCtx, cancelFunc := newChatCancelContext()
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

	var (
		layout     *ui.Layout
		inputBox   *ui.InputBox
		keyHandler *ui.KeyHandler
		surface    *ui.FixedBottomSurface
	)
	// --compat-mode 强制走无 ANSI 降级路径：跳过 TUI/keyHandler 初始化，
	// 使用系统 Unicode cooked 输入（默认）或显式选择的自定义控制台编辑器。
	compatMode := opts != nil && opts.CompatMode
	setChatDebugFlag(opts.Debug)
	// A process normally builds one chat session, but tests and embedded callers may
	// build more than one. Never leak a previous compatibility input selection.
	resetChatConsoleInputMode()
	interactiveUI := shouldInitializeChatInteractiveUI(opts) && !compatMode
	if shouldInitializeChatKeyHandler(opts) && !compatMode {
		keyHandler = ui.NewKeyHandler()
		keyHandler.Start()
	}
	if interactiveUI {
		layout = ui.NewLayout(ui.LayoutAdvanced)
		layout.Enable()
		inputBox = ui.NewInputBox(layout)
		surface = ui.NewFixedBottomSurface(layout.Terminal())
		// The compatibility facade still needs its geometry and logical state,
		// but an interactive production session must never let it emit even the
		// initial legacy frame. TerminalSession becomes the only writer below.
		surface.SetPhysicalWritesEnabled(false)
		enabled := surface.Enable()
		if chatDebugFlagEnabled() {
			aicliDiagf("[aicli-diag] surface.Enable() = %v\n", enabled)
		}
		if !enabled {
			if chatDebugFlagEnabled() {
				aicliDiagln("[aicli-diag] surface.Enable() FAILED -> plain interactive mode + legacy console line editor")
			}
			// A TTY alone is insufficient for the owned renderer: the primary
			// transaction requires ANSI plus DECSTBM scroll-region support and a
			// confirmed geometry source. Native Windows 7 consoles cannot enable
			// virtual-terminal processing, so attaching TerminalSession here would
			// render control bytes literally and corrupt its retained-history
			// bookkeeping.
			//
			// Fall back to the existing sequential line-mode path used by
			// interactive sessions without a TTY. Discard every partially-created
			// screen component first: a disabled surface plus a stopped key handler
			// guarantees that stdout and stdin each retain a single owner.
			layout.Disable()
			if keyHandler != nil {
				keyHandler.Stop()
			}
			_, _ = fmt.Fprintln(newChatSystemOutputWriter(os.Stderr),
				"Warning: terminal does not support ANSI scroll-region rendering; using plain interactive mode")
			// 诊断探针：仅 --debug 时输出。
			if opts.Debug {
				aicliDiagln("[aicli-diag] no ANSI -> plain interactive mode + legacy console line editor")
			}
			layout = nil
			inputBox = nil
			keyHandler = nil
			surface = nil
			interactiveUI = false
			// 降级模式优先使用 ReadConsoleW 系统 Unicode 行输入，让 Win7
			// conhost 保留 IME 组合/候选词；不可用时回退自定义逐键编辑器。
			setChatConsoleInputMode(opts.InputMode)
		}
	}
	if compatMode {
		// 强制兼容模式：即使终端支持 ANSI 也走兼容控制台行输入。
		layout = nil
		inputBox = nil
		keyHandler = nil
		surface = nil
		interactiveUI = false
		setChatConsoleInputMode(opts.InputMode)
		if opts.Debug {
			aicliDiagf("[aicli-diag] compat mode: plain interactive mode + %s console input\n", opts.InputMode)
		}
	}
	if opts.NoInteractive || opts.OutputFormat == "json" {
		mcpmanager.SetStatusOutput(io.Discard)
	} else {
		mcpmanager.SetStatusOutput(newChatSystemOutputWriterWithSurface(os.Stdout, surface))
	}

	session := &ChatSession{
		ProviderName:             runtimeState.providerName,
		Provider:                 runtimeState.provider,
		Adapter:                  runtimeState.adapter,
		Model:                    runtimeState.modelName,
		ReasoningEffort:          runtimetypes.NormalizeReasoningEffort(runtimeState.reasoningEffort),
		RequestedProvider:        strings.TrimSpace(runtimeState.requestedProvider),
		EffectiveProvider:        strings.TrimSpace(runtimeState.providerName),
		RequestedModel:           strings.TrimSpace(runtimeState.requestedModel),
		EffectiveModel:           strings.TrimSpace(runtimeState.modelName),
		RequestedReasoningEffort: strings.TrimSpace(runtimeState.requestedReasoningEffort),
		EffectiveReasoningEffort: runtimetypes.NormalizeReasoningEffort(runtimeState.reasoningEffort),
		RequestedPermissionMode:  string(opts.PermissionMode),
		EffectivePermissionMode:  string(opts.PermissionMode),
		DisableTools:             opts.DisableTools,
		HTTPDebug:                opts.HTTPDebug,
		Stream:                   runtimeState.shouldStream,
		FastMode:                 runtimeState.fastMode,
		BaseURL:                  runtimeState.baseURL,
		Messages:                 nil,
		HTTPClient:               httpclient.GetHTTPClientWithProvider(cfg, &runtimeState.provider),
		cancelCtx:                cancelCtx,
		cancelFunc:               cancelFunc,
		interrupted:              atomic.Bool{},
		FunctionCatalog:          functionCatalog,
		FunctionRegistry:         registry,
		FunctionBuilder:          functionCatalog.Builder(runtimeState.provider.GetProtocol()),
		Logger:                   logger,
		Formatter:                newChatMarkdownFormatter(),
		Layout:                   layout,
		InputBox:                 inputBox,
		KeyHandler:               keyHandler,
		TokenCount:               0,
		MsgCount:                 0,
		TurnRequestCount:         0,
		SessionManager:           persistenceState.runtimeSessionManager,
		RuntimeSession:           nil,
		SessionUserID:            persistenceState.sessionUserID,
		SessionDir:               persistenceState.resolvedSessionDir,
		Ephemeral:                persistenceState.ephemeral,
		SessionFilter:            opts.SessionFilter,
		NoInteractive:            opts.NoInteractive,
		JSONOutput:               opts.OutputFormat == "json",
		JSONEnvelope:             opts.JSONEnvelope,
		MCPStatus:                nil,
		MCPEnabled:               false,
		SkillsMode:               opts.CLISkillsMode,
		SkillsDebug:              opts.CLISkillsDebug,
		Config:                   cfg,
		RetryConfig:              runtimeState.retryCfg,
		RequestTimeout:           runtimeState.requestTimeout,
		OutputFormat:             opts.OutputFormat,
		InputReader:              chatOptionInputReader(opts),
		PermissionMode:           opts.PermissionMode,
		permissionModeCLIChanged: opts.PermissionModeChanged,
		CLIAllowTools:            append([]string(nil), opts.CLIAllowTools...),
		CLIDenyTools:             append([]string(nil), opts.CLIDenyTools...),
		ApprovalReuseMode:        opts.ApprovalReuseMode,
		Surface:                  surface,
		runtimeHTTPCapture:       &chatRuntimeHTTPCapture{},
		ImagePaths:               opts.ImagePaths,
	}
	session.Interaction = newChatInteractionCoordinator(session)
	// The compatibility facade is already physically fenced above, so it is
	// safe to mount its geometry and semantic bottom-pane inputs before the
	// primary presenter attaches. Doing so makes the first TerminalSession frame
	// use the real terminal dimensions instead of the global 80x24 fallback.
	// SetSurface cannot revive the legacy writer: its physical-write fence is
	// established before Enable and is made permanent by presenter attachment.
	session.Interaction.SetSurface(surface)
	if interactiveUI {
		// This is an authority transition, not a feature flag. Never continue an
		// interactive session with a fenced legacy writer and no TerminalSession.
		// The already-mounted compatibility facade contributes geometry and
		// semantic bottom-pane state only; TerminalSession becomes the sole
		// physical terminal owner below.
		//
		// Phase 6 收口：interactive 生产默认经 render output gateway 交付
		// （PhysicalSink→RenderOutputGateway，receipt/journal/mirror 可观测）。
		// factory 失败时 fail-closed 终止会话——不再回退直写 unified renderer
		// （回退路径会让全部 terminal effects 绕过 gateway，违反
		// "所有 interactive effects 经 session-scoped gateway" 收敛目标）。
		// 直写 unified renderer（enableUnifiedRendererWithWriter）仅保留给测试。
		if session.Interaction.EnableUnifiedRendererGateway() == nil {
			aicliDiagln("[aicli-diag] EnableUnifiedRendererGateway: factory failed -> fail-closed (no direct-writer fallback)")
			mcpmanager.SetStatusOutput(os.Stdout)
			if surface != nil {
				surface.Disable()
			}
			if layout != nil {
				layout.Disable()
			}
			if keyHandler != nil {
				keyHandler.Stop()
			}
			return nil, nil, fmt.Errorf("initialize unified terminal renderer gateway")
		}
		// MCP bootstrap/status output is semantic transcript input in the unified
		// session. Do not leave the old system writer pointed at a physically
		// fenced surface, because that would silently discard notices or create a
		// raw stdout bypass when the fence changes.
		mcpmanager.SetStatusOutput(newChatSystemOutputWriterWithSemanticSink(session.Interaction))
	}
	initializeChatAccountBalanceRefresh(session)
	session.Interaction.RefreshStatus("")
	if profileState != nil && profileState.Active() {
		session.ProfileReference = profileState.Reference
		session.ProfileName = profileState.Resolved.ProfileName
		session.ProfileAgent = profileState.Resolved.AgentID
		session.ProfileRoot = profileState.Resolved.ProfileRoot
		session.AgentSourcePath = strings.TrimSpace(profileState.AgentSourcePath)
		session.AgentSource = strings.TrimSpace(profileState.AgentSource)
		session.SystemPromptText = profileState.PromptText
		session.RuntimeConfigPath = profileState.RuntimeConfigPath()
		session.MCPConfigPath = profileState.MCPConfigPath()
		session.ResolvedSkillDirs = profileState.SkillDirs()
		session.ProfileContext = cloneSkillContextMap(profileState.ContextValues)
		session.ToolPolicy = profileState.ToolPolicy
		if session.ToolPolicy != nil {
			session.BaseToolPolicy = session.ToolPolicy.Clone()
		}
		if session.FunctionCatalog != nil && session.ToolPolicy != nil {
			session.FunctionCatalog.SetToolPolicy(session.ToolPolicy)
		}
		for _, warning := range profileState.SandboxWarnings {
			emitChatSandboxWarning(warning)
		}
	}
	// Project + CLI permission product surface (R1). Workspace may refine later
	// when local runtime host resolves an absolute root; cwd is the bootstrap root.
	applyChatPermissionsOverlay(session, "")

	// Folder trust (R2): attach process-level resolution (resolved early in HandleChat/exec).
	applyChatFolderTrust(session, currentFolderTrust())

	cleanup := func() {
		mcpmanager.SetStatusOutput(os.Stdout)
		stopChatAccountBalanceRefresh(session)
		if session.TitleNotifier != nil {
			session.TitleNotifier.Close()
		}
		if session.Interaction != nil {
			session.Interaction.Shutdown()
		}
		if layout != nil {
			layout.Disable()
		}
		if keyHandler != nil {
			keyHandler.Stop()
		}
		if surface != nil {
			surface.Disable()
		}
	}

	return session, cleanup, nil
}

func newChatCancelContext() (context.Context, context.CancelFunc) {
	base := runtimeexecution.WithCancelSource(context.Background(), "user_interrupt")
	return context.WithCancel(base)
}

func shouldInitializeChatKeyHandler(opts *chatCommandOptions) bool {
	if opts == nil || opts.NoInteractive || opts.OutputFormat == "json" {
		return false
	}
	return chatIsInteractiveTerminal()
}

func shouldInitializeChatInteractiveUI(opts *chatCommandOptions) bool {
	if opts == nil || opts.NoInteractive || opts.OutputFormat == "json" {
		return false
	}
	// An ANSI-capable interactive TTY has one production screen renderer:
	// TerminalSession. Capability probing happens when FixedBottomSurface is
	// enabled; buildChatSession downgrades an unsupported TTY to sequential
	// line mode before publishing the session, so it never creates a competing
	// screen authority.
	return chatIsInteractiveTerminal()
}

func shouldShowChatStartupBanner(opts *chatCommandOptions) bool {
	if opts == nil || opts.NoInteractive {
		return false
	}
	// TUI 模式会在 bootstrap 后接管屏幕，欢迎页没有必要先打印。
	return !shouldInitializeChatInteractiveUI(opts)
}

func shouldShowChatSessionStartupPreamble(opts *chatCommandOptions) bool {
	// 启动前置信息和欢迎页使用同一套 TUI 判定，避免两条路径出现分叉。
	return shouldShowChatStartupBanner(opts)
}

func clearChatStartupScreen(opts *chatCommandOptions) {
	if !shouldClearChatStartupScreen(opts) {
		return
	}
	ui.NewTerminal().ClearIfSupported()
}

func shouldClearChatStartupScreen(opts *chatCommandOptions) bool {
	if opts == nil || opts.NoInteractive || opts.OutputFormat == "json" || opts.ListSessionsFlag {
		return false
	}
	return chatIsInteractiveTerminal()
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
		// The bootstrap resume path must use the same canonical transcript as
		// /load. restoreChatStateFromRuntimeSession restores the compact prompt
		// projection only; the full history is needed for replay in the owned
		// viewport and must be loaded before the first frame is painted.
		loadResumeCanonicalHistory(session, persistenceState.loadedRuntimeSession.ID)
		return nil
	}

	if persistenceState.runtimeSessionManager == nil {
		return nil
	}

	if err := createNewRuntimeConversation(session, opts.SessionTitleFlag); err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	ensureChatSystemPromptMessage(session)
	// New sessions stay in-memory until the first user turn so bootstrap does
	// not open the large session history store for an empty shell.
	return nil
}

func initializeChatCapabilities(cfg *config.Config, opts *chatCommandOptions, session *ChatSession) (*skillsRuntimeBinding, func(), error) {
	if session == nil || opts == nil {
		return nil, nil, nil
	}
	if !session.DisableTools {
		registerGoalFunctions(session)
	}
	if configured, err := configureRuntimeServerChatExecutor(context.Background(), opts, session); err != nil {
		return nil, nil, err
	} else if configured {
		return nil, nil, nil
	}

	var (
		skillsBinding *skillsRuntimeBinding
		toolManager   *runtimetools.Manager
	)

	if session.DisableTools {
		logpkg.Info("AICLI chat tools exposure disabled by flag")
	} else {
		if err := prepareChatMCPManager(cfg, session); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: 初始化 MCP 失败: %v\n", err)
			logpkg.Warnf("AICLI MCP init failed: %v", err)
		}
		markChatStartup("capabilities_mcp")

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
		markChatStartup("capabilities_tools")
	}

	// Build the local runtime host first so skills can reuse its DiscoverOnly
	// bootstrap manager instead of scanning skill directories a second time.
	localRuntimeHost, hostErr := initializeLocalChatRuntimeHost(cfg, session, toolManager)
	if hostErr != nil {
		return nil, nil, fmt.Errorf("初始化 actor runtime host 失败: %w", hostErr)
	}
	if localRuntimeHost == nil {
		return nil, nil, fmt.Errorf("初始化 actor runtime host 失败: runtime host is nil")
	}
	session.LocalRuntimeHost = localRuntimeHost
	session.ActorFirstReady = true
	restoreLocalRuntimeHostTeamState(session)
	session.ChatExecutor = newAICLIActorChatExecutor()
	startChatActorWarmup(session)
	markChatStartup("capabilities_runtime_host")

	if !session.DisableTools {
		var sharedBootstrap *runtimebootstrap.Manager
		if localRuntimeHost != nil {
			sharedBootstrap = localRuntimeHost.Bootstrap
		}
		var err error
		skillsBinding, err = initSkillFunctionsWithManager(cfg, session, toolManager, sharedBootstrap, opts.CLISkillDirs, opts.CLISkillsTopK, opts.CLISkillsMode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: 初始化 Skills 失败: %v\n", err)
		} else if skillsBinding != nil {
			session.SkillsBinding = skillsBinding
			if session.FunctionCatalog != nil {
				session.FunctionCatalog.SetSkillsBinding(skillsBinding)
			}
		}
		markChatStartup("capabilities_skills")
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

	if err := materializeChatSessionSandbox(session, profileState); err != nil {
		buildChatFinalCleanup(session, cleanupSession)()
		return nil, nil, err
	}
	if err := restoreChatPersistenceState(session, persistenceState, opts); err != nil {
		buildChatFinalCleanup(session, cleanupSession)()
		return nil, nil, err
	}
	initializeChatTitleNotifier(session)
	initializeChatSoundNotifier(session)
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}

	_, cleanupCapabilities, err := initializeChatCapabilities(cfg, opts, session)
	if err != nil {
		buildChatFinalCleanup(session, cleanupSession)()
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

// materializeChatSessionSandbox re-applies named sandbox profiles once the
// concrete workspace root is known so path bounds are not left incomplete.
func materializeChatSessionSandbox(session *ChatSession, profileState *chatProfileState) error {
	if session == nil || session.ToolPolicy == nil {
		return nil
	}
	sandboxMap := map[string]interface{}{}
	if profileState != nil && profileState.Active() && profileState.Resolved != nil {
		sandboxMap = profileState.Resolved.ToolPolicy.Sandbox
	}
	if len(sandboxMap) == 0 && session.ToolPolicy.Sandbox != nil {
		cfg := session.ToolPolicy.Sandbox.Config()
		if mode := strings.TrimSpace(cfg.Profile); mode != "" {
			sandboxMap = map[string]interface{}{"mode": mode}
		}
	}
	if len(sandboxMap) == 0 {
		return nil
	}
	workspaceRoot := resolveLocalWorkspacePath(loadRuntimeToolConfig(session.Config, session), session)
	warnings, err := runtimeprofileinput.MaterializeSandboxForWorkspace(session.ToolPolicy, sandboxMap, workspaceRoot)
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		emitChatSandboxWarning(warning)
	}
	if session.FunctionCatalog != nil {
		session.FunctionCatalog.SetToolPolicy(session.ToolPolicy)
	}
	return nil
}

func emitChatSandboxWarning(warning string) {
	warning = strings.TrimSpace(warning)
	if warning == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	logpkg.Warnf("AICLI sandbox profile: %s", warning)
}

func buildChatFinalCleanup(session *ChatSession, cleanupSession func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			finalizeChatSession(session)
			if session != nil && session.TitleNotifier != nil {
				session.TitleNotifier.Close()
			}
			if session != nil && session.Interaction != nil {
				session.Interaction.Shutdown()
			}
			if cleanupSession != nil {
				cleanupSession()
			}
			if session == nil || session.NoInteractive || session.JSONOutput || session.Layout == nil {
				return
			}
			if term := session.Layout.Terminal(); term != nil {
				term.CleanupOnExit(true)
			}
			printChatExitResumeHint(session)
		})
	}
}

func printChatExitResumeHint(session *ChatSession) {
	if session == nil || session.Ephemeral || session.runtimeSessionUnpersisted {
		return
	}
	sessionID := strings.TrimSpace(currentRuntimeSessionID(session))
	if sessionID == "" {
		return
	}
	fmt.Printf("\n下次可使用以下命令恢复当前会话：\n  aicli resume %s\n", sessionID)
}

func restoreLocalRuntimeHostTeamState(session *ChatSession) {
	if session == nil || session.LocalRuntimeHost == nil {
		return
	}
	if restoreAmbientTeamBindingFromRuntimeStore(session) {
		warnIfChatSessionSyncFails(session, "restore ambient team binding", syncRuntimeSessionFromChat(session))
	}
	validateAmbientTeamBinding(session, session.LocalRuntimeHost.TeamStore)
	// 启动早期做一次 stale-active team 探测：进程异常退出/状态未持久化会
	// 在 TeamStore 留下 active 但实际已结束的团队。若不处理，resume 后主循环
	// 会因 Pending==true 永久等待其 terminal 事件，prompt 与输入区永不渲染，
	// UI 却显示执行状态。必须赶在 syncTeamLifecycleLoops 之前：已被判定终止的
	// 团队不会被重新拉起 loop；真在执行的团队保持 active，正常恢复运行。
	reconcileStaleAmbientTeams(session)
	if activeTeam := chatSessionActiveTeam(session); activeTeam != nil && strings.TrimSpace(activeTeam.TeamID) != "" {
		session.LocalRuntimeHost.replayStoredTerminalTeamLifecycleEvents(activeTeam.TeamID)
	}
	warnIfChatSessionSyncFails(session, "sync ambient team lifecycle state", syncAmbientTeamLifecycleState(session))
	session.LocalRuntimeHost.syncTeamLifecycleLoops()
}

func presentChatSession(session *ChatSession) {
	if session == nil || !shouldPrintChatSessionPreamble(session) {
		return
	}

	beginDirectInteractiveOutput(session)
	printChatSessionPreamble(session)
	if !session.DisableTools && session.SkillsBinding != nil && session.SkillsBinding.Count() > 0 {
		printChatSessionInfoRow(os.Stderr, "Skills:", fmt.Sprintf("已启用 (%d 个 AI 可调用 skills)", session.SkillsBinding.Count()), chatSessionMetaLabelWidth)
		printChatSessionInfoRow(os.Stderr, "Skills Mode:", resolvedChatSkillsMode(session, session.SkillsBinding), chatSessionMetaLabelWidth)
		printChatSessionInfoRow(os.Stderr, "Skills Top-K:", fmt.Sprintf("%d", resolvedChatSkillsTopK(session.SkillsBinding)), chatSessionMetaLabelWidth)
	}
}

func printChatSessionPreamble(session *ChatSession) {
	if session == nil {
		return
	}

	info := buildChatSessionInfo(session)
	theme := ui.GetTheme(ui.ThemeAuto)

	printChatSelectionBlankLine()
	fmt.Fprintln(os.Stderr, ui.NewSeparator().SetType(ui.SeparatorThick).Build())
	printChatSessionInfoLine(os.Stderr, theme.SystemIcon+" ", "Provider:", "( "+info.ProviderName+" )", style.RoleSuccess)
	if info.Protocol != "" {
		printChatSessionInfoLine(os.Stderr, strings.Repeat(" ", ui.DisplayWidth(theme.SystemIcon+" ")), "Protocol:", info.Protocol, style.RoleTextMuted)
	}
	if info.EndpointURL != "" {
		printChatSessionInfoLine(os.Stderr, strings.Repeat(" ", ui.DisplayWidth(theme.SystemIcon+" ")), "Endpoint:", info.EndpointURL, style.RoleTextMuted)
	}
	if info.Host != "" {
		printChatSessionInfoLine(os.Stderr, strings.Repeat(" ", ui.DisplayWidth(theme.SystemIcon+" ")), "Host:", info.Host, style.RoleTextMuted)
	}
	if info.KeyCount > 0 {
		printChatSessionInfoLine(os.Stderr, strings.Repeat(" ", ui.DisplayWidth(theme.SystemIcon+" ")), "Auth Keys:", fmt.Sprintf("%d", info.KeyCount), style.RoleTextMuted)
	}
	if info.Timeout != "" {
		printChatSessionInfoLine(os.Stderr, strings.Repeat(" ", ui.DisplayWidth(theme.SystemIcon+" ")), "Timeout:", info.Timeout, style.RoleTextMuted)
	}
	printChatSessionInfoLine(os.Stderr, theme.SystemIcon+" ", "Model:", info.ModelName, style.RoleSuccess)
	if info.IsStream {
		printChatSessionInfoLine(os.Stderr, theme.SystemIcon+" ", "Stream:", "on", style.RoleSuccess)
	} else {
		printChatSessionInfoLine(os.Stderr, theme.SystemIcon+" ", "Stream:", "off", style.RoleTextMuted)
	}
	// Fast is Codex-only (service_tier=priority); never imply Stream.
	if info.SupportsFast {
		if info.IsFast {
			printChatSessionInfoLine(os.Stderr, theme.SystemIcon+" ", "Fast:", "on", style.RoleSuccess)
		} else {
			printChatSessionInfoLine(os.Stderr, theme.SystemIcon+" ", "Fast:", "off", style.RoleTextMuted)
		}
	}
	if info.ReasoningEnabled {
		printChatSessionInfoLine(os.Stderr, theme.SystemIcon+" ", "Reasoning:", "enabled", style.RoleWarning)
	}

	if session.MCPEnabled && session.MCPStatus != nil {
		printChatSessionInfoRow(os.Stderr, "MCP:", fmt.Sprintf("已启用 (%d 个工具, %d 个 MCP 服务器)",
			session.MCPStatus.ToolCount, session.MCPStatus.MCPCount), chatSessionMetaLabelWidth)
	}
	if session.ProfileName != "" {
		profileValue := session.ProfileName
		if session.ProfileAgent != "" {
			profileValue += fmt.Sprintf(" (agent=%s)", session.ProfileAgent)
		}
		printChatSessionInfoRow(os.Stderr, "Profile:", profileValue, chatSessionMetaLabelWidth)
	}
	if line := formatChatAgentSourceLine(session); line != "" {
		printChatSessionInfoRow(os.Stderr, "Agent Source:", line, chatSessionMetaLabelWidth)
	}
	if reasoningEffort := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort); reasoningEffort != "" {
		printChatSessionInfoRow(os.Stderr, "Reasoning Effort:", reasoningEffort, chatSessionMetaLabelWidth)
	}
	if session.LocalRuntimeHost != nil {
		ctx := snapshotChatRuntimeContext(session)
		printChatSessionInfoRow(os.Stderr, "Permission Mode:", string(ctx.PermissionMode), chatSessionMetaLabelWidth)
		printChatSessionInfoRow(os.Stderr, "Approval Reuse:", formatChatApprovalReuseMode(ctx.ApprovalReuseMode), chatSessionMetaLabelWidth)
	}
	if summary := runtimepolicy.FormatPermissionsOverlaySummary(session.PermissionsOverlay); summary != "" && summary != "<none>" {
		printChatSessionInfoRow(os.Stderr, "Permission Rules:", summary, chatSessionMetaLabelWidth)
	}
	if queuedCount, draining := queuedInteractiveInputState(session); queuedCount > 0 || draining {
		value := fmt.Sprintf("%d pending", queuedCount)
		if draining {
			value += " (draining)"
		}
		printChatSessionInfoRow(os.Stderr, "Queued Input:", value, chatSessionMetaLabelWidth)
	}
	if session.DisableTools {
		printChatSessionInfoRow(os.Stderr, "Tools:", "disabled", chatSessionMetaLabelWidth)
	} else if session.ToolPolicy != nil {
		if names := session.ToolPolicy.AllowedToolNames(); len(names) > 0 {
			printChatSessionInfoRow(os.Stderr, "Tools Allowlist:", strings.Join(names, ", "), chatSessionMetaLabelWidth)
		}
	}
	if session.HTTPDebug {
		printChatSessionInfoRow(os.Stderr, "HTTP Debug:", "on", chatSessionMetaLabelWidth)
	}
	if session.RetryConfig.DisableRetries {
		printChatSessionInfoRow(os.Stderr, "Retry Mode:", "fail-fast", chatSessionMetaLabelWidth)
	}

	printChatSelectionBlankLine()
	fmt.Fprintln(os.Stderr, ui.NewSeparator().SetType(ui.SeparatorThick).Build())
	printChatSelectionBlankLine()

	if session.RuntimeSession != nil {
		printChatCurrentRuntimeSessionStderr(session)
	}
}

func printChatSessionInfoLine(writer io.Writer, prefix, label, value string, valueRole style.Role) {
	if writer == nil {
		return
	}
	label = strings.Join(strings.Fields(ui.SanitizeTerminalText(label)), " ")
	pad := chatSessionMetaLabelWidth - ui.DisplayWidth(label)
	if pad < 0 {
		pad = 0
	}
	writeChatParts(writer, true,
		chatPart(prefix, style.RoleSystem),
		chatBoldPart(label+strings.Repeat(" ", pad), style.RoleMetaLabel),
		chatPart(" "+value, valueRole),
	)
}

func printChatCurrentRuntimeSessionStderr(session *ChatSession) {
	if session == nil || session.RuntimeSession == nil {
		return
	}

	preview := session.RuntimeSession.BuildPreview()
	if preview == nil {
		return
	}

	printChatSessionInfoRow(os.Stderr, "Session:", fmt.Sprintf("%s [%s]", preview.ID, preview.State), chatSessionMetaLabelWidth)
	if sessionPath := currentRuntimeSessionPath(session); sessionPath != "" {
		printChatSessionInfoRow(os.Stderr, "Session File:", sessionPath, chatSessionMetaLabelWidth)
	}
	if store := currentRuntimeSessionStoreSummary(session); store != "" {
		printChatSessionInfoRow(os.Stderr, "Session Store:", store, chatSessionMetaLabelWidth)
	}
	if logPath := currentChatLogFile(session); logPath != "" {
		printChatSessionInfoRow(os.Stderr, "Chat Log File:", logPath, chatSessionMetaLabelWidth)
	}
	if debugPath := currentDebugLogFile(session); debugPath != "" {
		printChatSessionInfoRow(os.Stderr, "Debug Log File:", debugPath, chatSessionMetaLabelWidth)
	}
	if artifactDir := currentRuntimeHTTPArtifactDir(session); artifactDir != "" {
		printChatSessionInfoRow(os.Stderr, "HTTP Artifact Dir:", artifactDir, chatSessionMetaLabelWidth)
	}
	if artifactDir := currentLocalShellArtifactDir(session); artifactDir != "" {
		printChatSessionInfoRow(os.Stderr, "Shell Artifact Dir:", artifactDir, chatSessionMetaLabelWidth)
	}
	if session.runtimeHTTPCapture != nil {
		snapshot := session.runtimeHTTPCapture.Snapshot()
		if snapshot.RequestArtifactPath != "" {
			printChatSessionInfoRow(os.Stderr, "Last HTTP Req:", resolveAbsoluteChatPath(snapshot.RequestArtifactPath), chatSessionMetaLabelWidth)
		}
		if snapshot.ResponseArtifactPath != "" {
			printChatSessionInfoRow(os.Stderr, "Last HTTP Resp:", resolveAbsoluteChatPath(snapshot.ResponseArtifactPath), chatSessionMetaLabelWidth)
		}
	}
	if path := currentLastLocalShellArtifactPath(session); path != "" {
		printChatSessionInfoRow(os.Stderr, "Last Shell Out:", path, chatSessionMetaLabelWidth)
	}
	if preview.Title != "" {
		printChatSessionInfoRow(os.Stderr, "Title:", preview.Title, chatSessionMetaLabelWidth)
	}
	if preview.MessageCount > 0 {
		printChatSessionInfoRow(os.Stderr, "History:", fmt.Sprintf("%d messages", preview.MessageCount), chatSessionMetaLabelWidth)
	}
}

func printChatSessionInfoRow(writer *os.File, label, value string, width int) {
	if writer == nil || strings.TrimSpace(label) == "" {
		return
	}
	label = strings.Join(strings.Fields(ui.SanitizeTerminalText(label)), " ")
	pad := width - ui.DisplayWidth(label)
	if pad < 0 {
		pad = 0
	}
	// Condense over-long values (paths, titles, summaries) to the remaining
	// interactive-terminal width so restore/resume/load preambles render as
	// compact rows like the chat command instead of spilling full-width text.
	value = condenseChatSessionInfoValue(writer, value, pad+ui.DisplayWidth(label))
	writeChatParts(writer, true,
		chatBoldPart(label+strings.Repeat(" ", pad), style.RoleMetaLabel),
		chatPart(" "+value, style.RoleTextSecondary),
	)
}

// condenseChatSessionInfoValue truncates a long session-meta value to the
// width left after the padded label on an interactive terminal, appending
// "..." via fitDisplayText. Non-terminal writers (pipes, test capture,
// redirects) keep the full value so redirected output stays lossless.
func condenseChatSessionInfoValue(writer *os.File, value string, labelWidth int) string {
	if writer == nil || value == "" || labelWidth <= 0 {
		return value
	}
	termWidth, ok := chatTerminalWidthProbe(writer)
	if !ok || termWidth <= labelWidth+1 {
		return value
	}
	return condenseChatSessionInfoValueForWidth(value, termWidth, labelWidth)
}

// chatTerminalWidthProbe reports terminal width for a writer. It is a package
// variable so tests can simulate an interactive terminal without a real tty;
// production behavior is exactly chatTerminalWriterWidth.
var chatTerminalWidthProbe = func(writer *os.File) (int, bool) {
	return chatTerminalWriterWidth(writer)
}

// condenseChatSessionInfoValueForWidth is the pure width-math half of
// condenseChatSessionInfoValue, kept separate for deterministic tests.
func condenseChatSessionInfoValueForWidth(value string, termWidth, labelWidth int) string {
	if value == "" || labelWidth <= 0 || termWidth <= labelWidth+1 {
		return value
	}
	remaining := termWidth - labelWidth - 1 // leading space before the value
	if remaining < 8 {                      // never destroy a short meaningful tail
		return value
	}
	return fitDisplayText(value, remaining)
}

// chatTerminalWriterWidth reports the interactive terminal width for writer.
// It returns ok=false when the writer is not a terminal or its size is
// unavailable, in which case values are left untruncated. Setting the
// AICLI_TERM_WIDTH environment variable overrides terminal detection so
// headless runs, pty-less test harnesses, and redirected output exercise the
// same condensing that interactive terminals get.
func chatTerminalWriterWidth(writer *os.File) (int, bool) {
	if writer == nil {
		return 0, false
	}
	if v := os.Getenv("AICLI_TERM_WIDTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n, true
		}
	}
	fd := int(writer.Fd())
	if !term.IsTerminal(fd) {
		return 0, false
	}
	width, _, err := term.GetSize(fd)
	if err != nil || width <= 0 {
		return 0, false
	}
	return width, true
}

func finalizeChatSession(session *ChatSession) {
	finalizeChatSessionWithError(session, nil)
}

func finalizeChatSessionWithError(session *ChatSession, terminalErr error) {
	if session == nil {
		return
	}

	awaitNoInteractiveLocalTeamDrain(session)
	// Skip durable flush for brand-new shells that never left memory. Writing
	// an empty system-prompt-only session only pollutes history and forces a
	// late session-history SQLite open during shutdown.
	if !session.runtimeSessionUnpersisted || runtimeSessionHasConversation(session.RuntimeSession) || chatMessagesHaveConversation(session.Messages) {
		warnIfChatSessionSyncFails(session, "shutdown", syncRuntimeSessionFromChat(session))
	}
	if session.Logger != nil && session.Logger.logDir != "" {
		var err error
		if terminalErr != nil {
			err = session.Logger.FailSession(terminalErr)
		} else {
			err = session.Logger.SaveSession()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to save chat logs: %v\n", err)
		} else if shouldPrintChatSessionPreamble(session) {
			printfDirectInteractiveOutput(session, "会话日志已保存到: %s\n", resolveAbsoluteChatPath(session.Logger.logDir))
		}
	}
}
