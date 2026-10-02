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
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
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

	cancelCtx, cancelFunc, cancelCause := newChatCancelContext()
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
		TurnBudgetTokens:         opts.BudgetTokens,
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
		cancelCause:              cancelCause,
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
		NoSkills:                 opts.NoSkills,
		Config:                   cfg,
		RetryConfig:              runtimeState.retryCfg,
		RequestTimeout:           runtimeState.requestTimeout,
		OutputFormat:             opts.OutputFormat,
		InputReader:              chatOptionInputReader(opts),
		PermissionMode:           opts.PermissionMode,
		permissionModeCLIChanged: opts.PermissionModeChanged,
		AllowedRoots:             dedupeChatPaths(opts.AllowedDirs),
		CLIAllowTools:            append([]string(nil), opts.CLIAllowTools...),
		CLIDenyTools:             append([]string(nil), opts.CLIDenyTools...),
		ApprovalReuseMode:        opts.ApprovalReuseMode,
		Surface:                  surface,
		runtimeHTTPCapture:       &chatRuntimeHTTPCapture{},
		ImagePaths:               opts.ImagePaths,
	}
	session.Interaction = newChatInteractionCoordinator(session)
	session.Interaction.renderOutputFile = opts.RenderOutputFile
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
		// 生效面投影走唯一权威函数（D21）；会话内热切换复用同一份实现。
		applyProfileStateToChatSession(session, profileState)
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
		stopChatAccountsRefresh(session)
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

// chatTurnCancelCause 标记 CLI 侧 turn ctx 被取消的原因。actor 侧会把它汇总成
// session_end 的 cancel_cause=parent_context(<reason>)，用于区分「用户中断」
// 与「新一轮输入顶替上一轮 ctx」；见 internal/chat/actor.go 的
// sessionRunCancelDetail。
type chatTurnCancelCause string

func (c chatTurnCancelCause) Error() string { return string(c) }

const (
	chatTurnCancelReasonUserInterrupt = "user_interrupt"
	chatTurnCancelReasonNewInputCycle = "new_input_cycle"
)

func newChatCancelContext() (context.Context, context.CancelFunc, context.CancelCauseFunc) {
	base := runtimeexecution.WithCancelSource(context.Background(), chatTurnCancelReasonUserInterrupt)
	ctx, cancelCause := context.WithCancelCause(base)
	cancel := func() { cancelCause(chatTurnCancelCause(chatTurnCancelReasonUserInterrupt)) }
	return ctx, cancel, cancelCause
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
		markChatStartup("resume_metadata")
		// The bootstrap resume path must use the same canonical transcript as
		// /load. restoreChatStateFromRuntimeSession restores the compact prompt
		// projection only; the full history is needed for replay in the owned
		// viewport and must be loaded before the first frame is painted.
		loadResumeCanonicalHistoryForStartup(session, persistenceState.loadedRuntimeSession.ID)
		markChatStartup("resume_history")
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
	discovery, err := discoverChatCapabilities(cfg, opts, session)
	if err != nil {
		return nil, nil, err
	}
	return attachChatCapabilities(cfg, opts, session, discovery)
}

// chatCapabilityDiscovery 承载「发现阶段」的产物。
//
// 发现阶段只读 session、不写 session，因此可以安全地放到后台 goroutine 跑；
// 挂载阶段（attachChatCapabilities）是唯一写 session 能力字段的地方，必须留在
// 主 goroutine 上——首帧之后主 goroutine 仍在读这些字段（状态行、descriptor、
// 工具可用性），让后台去写就是数据竞争。
type chatCapabilityDiscovery struct {
	// toolsEnabled 为 false 表示 --no-tools：发现阶段整段跳过。
	toolsEnabled bool
	// runtimeServerConfigured 为 true 表示远端 runtime-server 执行器已接管，
	// 挂载阶段不需要本地能力面。
	runtimeServerConfigured bool
	runtimeServerURL        string

	useLocalMCP      bool
	sessionMCP       *acpSessionMCP
	globalMCP        mcpmanager.Manager
	runtimeToolCfg   *runtimecfg.RuntimeConfig
	runtimeCfgPath   string
	toolManager      *runtimetools.Manager
	toolDescs        []runtimetools.ToolDescriptor
	localRuntimeHost *localChatRuntimeHost
}

// discoverChatCapabilities 跑耗时的发现/建连：MCP 装配、工具管理器构造与工具
// 枚举、local runtime host（含技能目录扫描与多个 SQLite 打开）。
//
// 实测这几段占首帧前 ~98% 的时间（见 chat_capabilities_async.go 顶部的实测表），
// 且产物全部是局部值，因此放到首帧之后的后台 goroutine 执行。
func discoverChatCapabilities(cfg *config.Config, opts *chatCommandOptions, session *ChatSession) (*chatCapabilityDiscovery, error) {
	discovery := &chatCapabilityDiscovery{}
	if session == nil || opts == nil {
		return discovery, nil
	}
	if configured, err := runtimeServerChatExecutorConfigured(context.Background(), opts, session); err != nil {
		return nil, err
	} else if configured {
		discovery.runtimeServerConfigured = true
		discovery.runtimeServerURL = strings.TrimSpace(opts.RuntimeServerURL)
		return discovery, nil
	}

	if session.DisableTools {
		logpkg.Info("AICLI chat tools exposure disabled by flag")
	} else {
		// ACP 会话的 MCP 装配模式：off/client 不初始化本地配置链（会话级
		// 客户端下发由下面的 buildACPSessionMCP 负责），merge/local 保持旧行为。
		mcpMode := opts.ACPMCPMode.normalized()
		useLocalMCP := mcpMode.allowsLocal()
		if useLocalMCP {
			if err := prepareChatMCPManager(cfg, session); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: 初始化 MCP 失败: %v\n", err)
				logpkg.Warnf("AICLI MCP init failed: %v", err)
			}
		} else {
			logpkg.Infof("AICLI local MCP config chain disabled (acp-mcp=%s)", mcpMode)
		}
		markChatStartup("capabilities_mcp")
		discovery.toolsEnabled = true
		discovery.useLocalMCP = useLocalMCP

		if useLocalMCP {
			discovery.globalMCP = MCPManagerInstance
		}
		globalMCP := discovery.globalMCP
		// 会话级 MCP（ACP 客户端下发）优先于进程级配置链：同名工具在合并管理器
		// 里去重时保留会话级实现。
		sessionMCP := buildACPSessionMCP(context.Background(), acpMCPSessionLabel(session), session, mcpMode, opts.ACPMCPClientPlan, opts.ACPMCPCapabilities)
		discovery.sessionMCP = sessionMCP
		mcpForTools := globalMCP
		if sessionMCP != nil {
			mcpForTools = mcpmanager.NewMergedManager(sessionMCP.manager, globalMCP)
		}

		runtimeToolConfig := loadRuntimeToolConfig(cfg, session)
		discovery.runtimeToolCfg = runtimeToolConfig
		discovery.runtimeCfgPath = resolveRuntimeToolConfigPath(cfg, session)
		discovery.toolManager = runtimetools.NewDefaultManagerWithRuntimeConfig(mcpForTools, runtimeToolConfig)
		markChatStartup("tools_manager_ctor")
		discovery.toolDescs = discovery.toolManager.ListTools()
		markChatStartup("tools_list")
		// 工具面枚举完成，接下来是耗时最长的技能目录扫描
		//（host bootstrap，实测 ~10s）：切到「加载 Skills」阶段，
		// 让动态栏显示进度在走，而不是十几秒一直停在「加载工具」。
		// 仅在异步装载路径生效——同步路径没有启动进度可推进
		//（见 advanceChatStartupProgress）。
		advanceChatStartupProgress(session, chatStartupProgressPhaseSkills)
	}

	host, err := initializeLocalChatRuntimeHost(cfg, session, discovery.toolManager)
	if err != nil {
		return nil, err
	}
	discovery.localRuntimeHost = host
	markChatStartup("host_discovery")
	return discovery, nil
}

// attachChatCapabilities 把发现阶段的产物挂到 session 上。
//
// 必须与所有其它 session 读写同处一个 goroutine（主 goroutine）：它在首帧之后
// 才被调用（经 ensureChatExecutor 门控），而主 goroutine 在门控之前就已经在读
// 这些字段做状态行/descriptor 判断。
func attachChatCapabilities(cfg *config.Config, opts *chatCommandOptions, session *ChatSession, discovery *chatCapabilityDiscovery) (*skillsRuntimeBinding, func(), error) {
	if session == nil || opts == nil || discovery == nil {
		return nil, nil, nil
	}
	if !session.DisableTools {
		registerGoalFunctions(session)
	}
	if discovery.runtimeServerConfigured {
		// 判定已在发现阶段完成（runtimeServerChatExecutorConfigured），这里只把
		// executor 挂到 session 上——这一步必须留在主 goroutine。
		session.ChatExecutor = newAICLIRuntimeServerChatExecutor(discovery.runtimeServerURL)
		session.ActorFirstReady = true
		session.LocalRuntimeHost = nil
		// runtime-server 模式下本地能力面（skills/runtime host）整段不挂载；
		// 技能在 runtime-server 进程里。这会让 /skills 报 total=0 且 binding=nil
		// 且所有本地组件 未配置——看上去和"加载失败"一模一样，但其实是"没走这条路"。
		// 显式记下，免得诊断再次陷入"各闸全绿却什么都没"的死循环。
		session.CapabilitiesInitError = fmt.Sprintf("runtime-server 模式已接管（URL=%s），本地能力面（skills/runtime host）未挂载", discovery.runtimeServerURL)
		return nil, nil, nil
	}

	var (
		skillsBinding *skillsRuntimeBinding
		toolManager   *runtimetools.Manager
	)
	localRuntimeHost := discovery.localRuntimeHost
	toolDescs := discovery.toolDescs

	if session.DisableTools {
		logpkg.Info("AICLI chat tools exposure disabled by flag")
	} else {
		toolManager = discovery.toolManager
		sessionMCP := discovery.sessionMCP
		useLocalMCP := discovery.useLocalMCP
		globalMCP := discovery.globalMCP
		// /lsp 命令族需要会话级工具管理器读取 LSP 池（状态/诊断/手动重启）。
		session.ChatToolManager = toolManager
		for _, desc := range toolDescs {
			session.FunctionCatalog.RegisterBuiltinToolFunction(functions.NewRuntimeToolFunction(toolManager, desc), desc)
		}
		markChatStartup("tools_register")
		// 工具面刷新器：会话私有与本地配置链两条来源都是异步建连，迟到的工具
		// 只能在 turn 边界增量登记（§4.7 R1 / D5）。非 ACP 入口不装，保持既有行为。
		refresher := sessionMCP
		if refresher == nil && opts.ACPHost && useLocalMCP && globalMCP != nil {
			refresher = newACPSessionMCPSurface(acpMCPSessionLabel(session), session.FunctionCatalog, globalMCP)
		}
		if refresher != nil {
			if useLocalMCP {
				refresher.localManager = globalMCP
			}
			refresher.toolManager = toolManager
			// 建连期间已经就绪的工具在这里补登记；迟到的工具在首个 prompt 边界
			// 由 acpSessionMCP.prepareForPrompt 增量登记。
			refresher.registered = registeredMCPToolNames(toolDescs)
			// 目录变化后必须让本会话的稳定工具面失效，否则新工具进不了模型
			// 工具面（既有失效通道只覆盖 chatWebSession）。
			refresher.invalidateSurface = func() int { return invalidateACPSessionToolSurface(session) }
		}
		session.ACPMCPSession = refresher
		// 启动期 LSP 自动装配（异步）：轻量扫描项目类型 → 写工作区
		// .aicli/runtime.yaml → 挂载 LSP 池；新工具在下一个 turn 边界登记。
		// 不阻塞启动关键路径（见 chat_lsp_bootstrap.go）。
		startChatLSPBootstrap(session, toolManager, discovery.runtimeToolCfg, discovery.runtimeCfgPath)
		markChatStartup("tools_lsp_bootstrap")
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

	// local runtime host 已在发现阶段建好（含 DiscoverOnly bootstrap manager，
	// skills 直接复用，不再二次扫描技能目录）。这里只做挂载。
	if localRuntimeHost == nil {
		return nil, nil, fmt.Errorf("初始化 actor runtime host 失败: runtime host is nil")
	}
	session.LocalRuntimeHost = localRuntimeHost
	session.ActorFirstReady = true
	// LSP 观测接线：池事件 → 会话 EventBus（lsp.*，live-only）。runtime host
	// 在工具管理器构造之后才建立，因此走可后置注入的 SetLSPObserver；构造期
	// 已挂载的池也会从这一刻起把后续事件转发出去（方案 §3.2）。
	if toolManager != nil {
		toolManager.SetLSPObserver(chatLSPRuntimeObserver(session))
	}
	restoreLocalRuntimeHostTeamState(session)
	markChatStartup("host_attach")
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
		// LSP 池归宿主所有（adr/0002）：会话退出时显式释放语言服务器进程，
		// 不能依赖进程退出兜底（docs/lsp 03 W2）。
		if toolManager != nil {
			if closeErr := toolManager.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: 停止语言服务器池失败: %v\n", closeErr)
			}
		}
	}

	return skillsBinding, cleanup, nil
}

func bootstrapChatSession(cfg *config.Config, opts *chatCommandOptions, profileState *chatProfileState, persistenceState *chatPersistenceState, runtimeState *chatRuntimeState) (*ChatSession, func(), error) {
	session, cleanupSession, err := bootstrapChatSessionShell(cfg, opts, profileState, persistenceState, runtimeState)
	if err != nil {
		return nil, nil, err
	}

	_, cleanupCapabilities, err := initializeChatCapabilities(cfg, opts, session)
	if err != nil {
		buildChatFinalCleanup(session, cleanupSession)()
		return nil, nil, err
	}

	return session, func() {
		if cleanupCapabilities != nil {
			cleanupCapabilities()
		}
		if cleanupSession != nil {
			cleanupSession()
		}
	}, nil
}

// bootstrapChatSessionShell 只构建到「会话可以被首帧渲染」为止，不初始化能力面
// （工具面 / skills / runtime host / supervision 平面）。
//
// 这段能力面在首帧前实测占 ~98% 的启动时间（见 chat_capabilities_async.go 顶部
// 的实测表），而产物只在真正发起 turn 时才被需要，因此 TUI 启动走本函数 +
// prepareChatCapabilitiesAsync：先渲染，再在后台装载，第一个 turn 由
// ensureChatExecutor 门控等待。
//
// 非 TUI / 测试 / 工具路径继续用 bootstrapChatSession（同步装载，行为不变）。
func bootstrapChatSessionShell(cfg *config.Config, opts *chatCommandOptions, profileState *chatProfileState, persistenceState *chatPersistenceState, runtimeState *chatRuntimeState) (*ChatSession, func(), error) {
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
	return session, cleanupSession, nil
}

// prepareChatCapabilitiesAsync 准备「首帧之后后台装载能力面」的启动器。
//
// 返回两个函数：
//   - start：真正拉起后台装载。必须在首帧落地之后调用，否则又退回「先装载后渲染」；
//   - cleanup：接进会话收尾，释放能力面与会话其余资源。
//
// 与 bootstrapChatSession 拆开是为了让「首帧」真正发生在能力面之前：
// 实测这三段发现型调用占首帧前 ~98% 的时间（见 chat_capabilities_async.go 顶部
// 的实测表），而它们的产物只在真正发起 turn 时才被需要。
func prepareChatCapabilitiesAsync(cfg *config.Config, opts *chatCommandOptions, session *ChatSession, cleanupSession func()) (start func(), cleanup func()) {
	if session == nil {
		return func() {}, cleanupSession
	}

	// 能力面清理要挂在会话收尾上，但只有装载成功之后才有 cleanup 可调。装载
	// goroutine 写、退出路径读，两条路径都可能先到：互斥量取出后立即置 nil，
	// 因此重复调用是幂等的（真正释放只发生一次），无需额外 Once 配对。
	var (
		capabilitiesCleanupMu sync.Mutex
		capabilitiesCleanup   func()
	)
	cleanupCapabilities := func() {
		capabilitiesCleanupMu.Lock()
		fn := capabilitiesCleanup
		capabilitiesCleanup = nil
		capabilitiesCleanupMu.Unlock()
		if fn == nil {
			return
		}
		fn()
	}
	// 会话可能在能力面装载完成之前就退出（例如用户在首帧后立刻 /exit）。此时
	// 装载 goroutine 仍在跑，cleanup 要等它落地才能调用；cleanupAll 保证
	// 「退出」与「装载完成」无论谁先到都只释放一次。
	var cleanupAllOnce sync.Once
	cleanupAll := func() {
		cleanupAllOnce.Do(func() {
			load := currentChatCapabilityLoad(session)
			if load != nil {
				// 有界等待：发现阶段（后台）+ 挂载阶段（此处，主 goroutine）都跑完
				// 才能释放 store，否则会与仍在写的挂载阶段抢同一批句柄。若真挂起，
				// 到点后照样释放会话其余资源，不把退出卡死在后台扫描上。
				ctx, cancel := context.WithTimeout(context.Background(), chatCapabilitiesWaitLimit)
				_ = awaitChatCapabilities(ctx, session)
				cancel()
			}
			cleanupCapabilities()
			if cleanupSession != nil {
				cleanupSession()
			}
		})
	}

	// 门控句柄必须在首帧之前安装：否则「首帧后立刻提交」这条极短窗口里，
	// ensureChatExecutor 看不到 handle，会绕过 await 直接报 "not initialized"。
	// 安装与「是否已开始装载」是两件事，因此这里先 install、start 时再 complete。
	installChatCapabilitiesGate(session, func() (*chatCapabilityDiscovery, error) {
		return discoverChatCapabilities(cfg, opts, session)
	}, func(discovery *chatCapabilityDiscovery) (func(), error) {
		// 挂载阶段写 session，只能在主 goroutine 上跑（await 之后）。
		_, cleanup, err := attachChatCapabilities(cfg, opts, session, discovery)
		if err != nil {
			return nil, err
		}
		capabilitiesCleanupMu.Lock()
		capabilitiesCleanup = cleanup
		capabilitiesCleanupMu.Unlock()
		return cleanup, nil
	})
	start = func() {
		go runChatCapabilitiesLoad(session)
	}
	return start, cleanupAll
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
			// 知识层接入（Phase 1 交付 6）：会话结束时释放本会话持有的引用；
			// 引用归零才真正关 store。mode=off 时 session.Knowledge 为 nil，
			// 这里是纯 no-op。
			if session != nil && session.Knowledge != nil {
				releaseChatKnowledge(session.Knowledge.Workspace(), session.Knowledge)
				session.Knowledge = nil
			}
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
	if teamID, suspended := suspendRestoredAmbientTeamForInteractiveResume(session); suspended {
		// 交互式 resume 的契约是"恢复会话后等待用户输入"：把上一进程遗留的
		// 团队执行停放到 paused，而不是在启动阶段重新拉起 loop 继续执行。
		// 否则 interactiveTeamPending 恒为 true，主循环阻塞在
		// waitForTeamTerminal，composer 永不渲染（UI 却显示执行状态）。
		// 复用 preamble 的信息行渲染，避免在 chat_setup.go 引入新的直接写入者
		// （chat direct-writer inventory 是不允许扩张的回归围栏）。
		printChatSessionInfoRow(os.Stderr, "Resume:", resumeTeamSuspendedNotice(teamID), chatSessionMetaLabelWidth)
	} else if activeTeam := chatSessionActiveTeam(session); activeTeam != nil && strings.TrimSpace(activeTeam.TeamID) != "" {
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
		// profile 生效面（Batch 3/FR-5）：计数 + prompt 估算，随启动摘要一起输出；
		// 该函数由 shouldPrintChatSessionPreamble 统一门控（quiet/JSON/headless 抑制）。
		for _, row := range chatProfileSurfaceRows(session) {
			printChatSessionInfoRow(os.Stderr, row.label, row.value, chatSessionMetaLabelWidth)
		}
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
	// 会话关闭：本会话的待投递 wake 义务随之作废（证据仍在 notification/job store，
	// 由 resume 后首个自然 turn 的 preflight digest 呈现），避免恢复后再补投一轮
	// 过时 digest。
	resolveLocalChatSessionPendingWakes(session)
	// 同一时刻停掉本会话的巡检计时器：会话没了，迟到的 check 无处投递。
	disarmLocalChatSessionMonitors(session)
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
