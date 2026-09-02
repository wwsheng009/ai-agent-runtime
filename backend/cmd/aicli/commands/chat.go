package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const chatSessionMetaLabelWidth = 18

// ChatSession 聊天会话状态
type ChatSession struct {
	ProviderName             string
	Provider                 config.Provider
	Adapter                  adapter.ProtocolAdapter
	Model                    string
	ReasoningEffort          string
	RequestedProvider        string
	EffectiveProvider        string
	RequestedModel           string
	EffectiveModel           string
	RequestedReasoningEffort string
	EffectiveReasoningEffort string
	RequestedPermissionMode  string
	EffectivePermissionMode  string
	RouteWarnings            []string
	FallbackUsed             bool
	FallbackReason           string
	SuppressReasoningOutput  bool
	DisableTools             bool
	DebugMode                bool
	// permissionModeCLIChanged 表示权限模式由命令行显式指定
	// （--yolo / --permission-mode）。恢复或切换持久化会话时，
	// 不得用会话中存储的权限模式覆盖 CLI 显式指定值。
	permissionModeCLIChanged bool
	HTTPDebug                bool
	Stream                   bool
	// FastMode enables Codex service_tier=priority. Only meaningful when protocol is codex.
	FastMode bool
	BaseURL  string
	// Messages 是当前模型热上下文投影（压缩/截断后 ≤ HotHistoryMessages 条）。
	Messages []runtimetypes.Message
	// ResumeHistory 是恢复会话后用于展示的 canonical 完整转录
	// （append-only session_messages 全量回放）。它与 Messages 严格分离：
	// 模型上下文始终使用 Messages，ResumeHistory 只供用户可见的历史回放
	// （/resume、启动恢复等），避免把完整长对话塞进模型上下文。
	ResumeHistory                   []runtimetypes.Message
	HTTPClient                      *http.Client
	cancelCtx                       context.Context               // 可取消的上下文
	cancelFunc                      context.CancelFunc            // 取消函数
	composerWakeMu                  sync.Mutex                    // 保护 composer 读取唤醒取消
	composerWakeCancel              context.CancelFunc            // 当前 composer 读取的唤醒取消
	interrupted                     atomic.Bool                   // 是否被中断（原子操作，避免竞态）
	interruptCleanupMu              sync.Mutex                    // 保护当前中断清理完成信号
	interruptCleanupDone            chan struct{}                 // 阻止下一轮与上一轮异步清理交错
	FunctionCatalog                 *aicliFunctionCatalog         // 统一管理 builtin tools + skills + schema cache
	FunctionRegistry                *functions.FunctionRegistry   // Function 注册表
	FunctionBuilder                 functions.FunctionCallBuilder // 协议对应的 function/tool builder
	BuiltinSchemas                  []map[string]interface{}      // 预构建的非 skill function schemas
	stableSharedToolSessionID       string                        // shared executor 会话级稳定工具面所属 runtime session
	stableSharedToolSelection       *aicliFunctionSelection       // shared executor 会话级稳定工具面快照，避免跨请求动态 tools
	Logger                          *ChatLogger                   // 聊天日志记录器
	Formatter                       *formatter.MarkdownFormatter  // Markdown 格式化器
	Layout                          *ui.Layout                    // 屏幕布局
	InputBox                        *ui.InputBox                  // 输入框
	TokenCount                      int                           // 当前会话累计的真实 LLM API token 使用量，用于 /status 的 Token usage
	InputTokenCount                 int                           // 当前会话累计 prompt/input tokens，用于状态栏 in 计数
	OutputTokenCount                int                           // 当前会话累计 completion/output tokens，用于状态栏 out 计数
	ContextTokenCount               int                           // 当前活跃上下文的 token 快照，用于 ctx used 与 compact 观察值
	ContextWindowTokenCount         int                           // 当前模型上下文窗口大小
	TurnContextTokenCount           int                           // 当前 turn 内请求上下文 token 诊断累计，仅用于调试
	providerContextTokenCount       int                           // provider usage 返回的当前活跃上下文快照，等待 runtime history 同步后保留
	providerContextWindowTokenCount int                           // provider usage 对应的上下文窗口大小
	MsgCount                        int                           // 消息计数
	StatusMessageCount              int                           // 状态栏展示的当前上下文消息数
	TurnRequestCount                int                           // 当前 turn 内的请求计数
	turnPrimed                      bool                          // 当前用户 turn 已在 sendMessage 入口计数，等待首个 request scope 消费
	SessionManager                  *runtimechat.SessionManager   // 持久化会话管理器
	RuntimeSession                  *runtimechat.Session          // 当前持久化会话
	runtimeSessionUnpersisted       bool                          // 新会话仅在内存中，尚未写入 session store
	SessionUserID                   string                        // 当前会话所属用户
	SessionDir                      string                        // 会话存储目录
	Ephemeral                       bool                          // 会话仅驻留内存，不写入会话文件
	SessionFilter                   ChatSessionListFilter         // 会话列表筛选条件
	NoInteractive                   bool                          // 是否为非交互模式
	JSONOutput                      bool                          // 是否输出 JSON
	JSONEnvelope                    bool                          // JSON 输出是否使用 envelope
	KeyHandler                      *ui.KeyHandler                // 键盘事件处理器（ESC 键中断）
	MCPEnabled                      bool                          // 是否启用 MCP
	MCPStatus                       *MCPStatus                    // MCP 状态
	SkillsBinding                   *skillsRuntimeBinding         // Skills 运行时绑定
	SkillsMode                      string                        // Skills 暴露模式
	SkillsDebug                     bool                          // Skills 调试输出
	Config                          *config.Config                // 载入的 aicli 全局配置，用于偏好持久化与 provider/model 解析
	RetryConfig                     RetryConfig                   // 重试配置
	RequestTimeout                  time.Duration                 // 请求超时（0 表示不设置）
	OutputFormat                    string                        // 输出格式（interactive|text|json）
	InputReader                     *bufio.Reader                 // 共享 stdin reader，避免交互阶段重复缓冲吞掉后续输入
	InputQueue                      *chatInputQueue               // interactive line queue fed by stdin pump
	ProfileReference                string                        // 用户指定或配置解析出的 profile 引用
	ProfileName                     string                        // 当前 profile 名称
	ProfileAgent                    string                        // 当前 profile agent
	ProfileRoot                     string                        // 当前 profile 根目录
	// AgentSourcePath is the winning agentdef/profile agent config path
	// (or builtin:<name>) that produced the active role binding.
	AgentSourcePath string
	// AgentSource classifies discovery origin: builtin|user|project|profile.
	AgentSource       string
	SystemPromptText  string                             // 组合后的系统提示
	RuntimeConfigPath string                             // 解析后的 runtime 配置路径
	MCPConfigPath     string                             // 解析后的 MCP 配置路径
	ResolvedSkillDirs []string                           // 解析后的 skills 目录
	ProfileContext    map[string]interface{}             // profile 提供的只读运行时上下文
	ToolPolicy        *runtimepolicy.ToolExecutionPolicy // profile 解析后的工具策略
	// BaseToolPolicy is the pre-overlay policy (profile / session base) so
	// project-root reloads can re-apply permissions without double-intersecting.
	BaseToolPolicy *runtimepolicy.ToolExecutionPolicy
	PermissionMode runtimepolicy.Mode // actor/team run permission mode
	// CLIAllowTools / CLIDenyTools from --allow-tool / --deny-tool.
	CLIAllowTools []string
	CLIDenyTools  []string
	// PermissionsOverlay is the merged project file + CLI permission product surface.
	PermissionsOverlay runtimepolicy.PermissionsOverlay
	// FolderTrust is the workspace trust resolution for project-scope plugins/hooks/MCP (R2).
	FolderTrust         foldertrust.Resolution
	ApprovalReuseMode   chatApprovalReuseMode   // local actor/team approval reuse policy
	ActiveTeam          *chatTeamBinding        // ambient team binding across turns
	SelectedAgentTarget string                  // explicit /agents target used by /agents send/followup
	RuntimeEventBridge  *chatRuntimeEventBridge // actor runtime event bridge
	ExecEventBridge     headlessEventBridge     // optional headless exec/ACP event bridge
	ActorFirstReady     bool                    // actor-first executor established for this session
	ChatExecutor        aicliChatExecutor       // 当前会话的统一 turn executor
	LocalRuntimeHost    *localChatRuntimeHost   // actor-first local runtime host
	actorWarmupMu       sync.Mutex
	actorWarmup         *chatActorWarmup
	// runtimeCtxMu guards the runtime-context fields that the event bridge
	// reads while the actor/executor restores them from a runtime session:
	// DebugMode, PermissionMode, ApprovalReuseMode, SelectedAgentTarget,
	// RequestedPermissionMode, EffectivePermissionMode and ActiveTeam.
	runtimeCtxMu sync.RWMutex
	Interaction  *chatInteractionCoordinator // unified interactive stdout/prompt coordinator
	Surface      *ui.FixedBottomSurface      // optional fixed-bottom terminal surface
	// TerminalSession is the sole physical writer for the unified interactive
	// renderer. Surface remains only as a compatibility state facade while the
	// session is active; it must not emit terminal bytes in that mode.
	TerminalSession            *ui.TerminalSession
	TerminalSessionExecutor    *ui.TerminalSessionExecutor
	TitleNotifier              *chatTitleNotifier      // terminal window/tab title notification sink
	SoundNotifier              *chatSoundNotifier      // lightweight terminal bell notification sink
	runtimeHTTPCapture         *chatRuntimeHTTPCapture // recent runtime HTTP response diagnostics
	localShellArtifactMu       sync.Mutex
	localShellArtifactCounter  int
	lastLocalShellArtifactPath string
	turnRecoveryMu             sync.Mutex
	turnRecovery               *chatTurnRecovery
	// goalStatusMu guards live goal-status turn timing used by the status line.
	// Codex accrues active-goal elapsed only while an agent turn is running.
	goalStatusMu                  sync.Mutex
	goalStatusActiveTurnStartedAt time.Time
	accountBalanceMu              sync.RWMutex
	accountBalanceProviderName    string
	accountBalanceProvider        config.Provider
	accountBalanceInitialized     bool
	accountBalanceRefresher       *chatAccountBalanceRefresher
	priorityPromptMu              sync.Mutex // serializes modal prompts that own the priority input channel
	queuedInputDrain              bool       // suppress repeated queued-input notices while draining
	queuedInputEchoed             bool       // queued input was already echoed in the fixed prompt while busy
	lastInteractiveInputQueued    bool       // last chatInteractiveReadLine result came from InputQueue
	ImagePaths                    []string   // explicit local image attachments for current turn
}

type chatRuntimeHTTPCapture struct {
	mu                   sync.Mutex
	lastSource           string
	lastProvider         string
	lastProtocol         string
	lastModel            string
	lastResponseStatus   int
	lastResponsePreview  string
	lastError            string
	artifactDir          string
	lastRequestArtifact  string
	lastResponseArtifact string
	artifactCounter      int
	pendingArtifactSeq   int
}

// Interrupt 中断当前操作
func (s *ChatSession) Interrupt() {
	s.interrupt(false)
}

// newComposerReadContext 返回一个可被 Web 输入唤醒的 composer 读取上下文。
// 每次 composer 读取开始时调用；返回的 done 用于在读取结束时清理本地取消。
func (s *ChatSession) newComposerReadContext() (context.Context, func()) {
	if s == nil {
		return context.Background(), func() {}
	}
	s.composerWakeMu.Lock()
	if s.composerWakeCancel != nil {
		s.composerWakeCancel() // 兜底：上一轮未清理的取消
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.composerWakeCancel = cancel
	s.composerWakeMu.Unlock()
	return ctx, func() { cancel() }
}

// wakeComposerRead 唤醒当前阻塞在交互式 composer 读取中的主循环
// （Web 客户端注入输入后调用），使下一轮循环重新检查输入队列。
func (s *ChatSession) wakeComposerRead() {
	if s == nil {
		return
	}
	s.composerWakeMu.Lock()
	defer s.composerWakeMu.Unlock()
	if s.composerWakeCancel != nil {
		s.composerWakeCancel()
		s.composerWakeCancel = nil
	}
}

// InterruptPreservePendingInput 中断当前 Agent 运行，但保留用户在运行期间
// 已排队的后续消息。运行期 Esc 使用该语义；普通 Composer 取消仍使用 Interrupt。
func (s *ChatSession) InterruptPreservePendingInput() {
	s.interrupt(true)
}

func (s *ChatSession) interrupt(preservePendingInput bool) {
	if s == nil {
		return
	}
	s.interrupted.Store(true)
	var composerDraft string
	if preservePendingInput && s.Interaction != nil {
		composerDraft = s.Interaction.PromptInputSnapshot().Text
	}
	if preservePendingInput && s.Interaction != nil {
		s.Interaction.SetAgentStage(chatAgentStageStopping)
	}
	s.startInterruptCleanup()
	if s.cancelFunc != nil {
		s.cancelFunc()
	}
	// 中断语义是“取消当前输入/当前轮次”，因此需要同时清掉尚未提交的输入草稿
	// 和已渲染但尚未重绘的 prompt 状态，避免下一轮仍被旧状态挡住。
	if !preservePendingInput {
		_ = discardPendingInteractiveInput(s)
	}
	if s.Interaction != nil {
		s.Interaction.ResetPromptState()
		// Drop transient thinking/streaming/waiting flags immediately so that
		// once Stopping is cleared by cleanup/reset, surface state becomes Ready.
		// Do not clear agentStage here: Stopping remains visible while actor
		// stop / lease release is still in flight.
		s.Interaction.clearActiveRunStateOnInterrupt()
	}
	if preservePendingInput {
		restoreChatPendingInputAfterInterrupt(s, composerDraft)
	}
}

// ResetInterrupt 重置中断状态
func (s *ChatSession) ResetInterrupt() {
	s.waitForInterruptCleanup()
	s.interrupted.Store(false)
	if s.Interaction != nil && s.Interaction.AgentStage() == chatAgentStageStopping {
		s.Interaction.ClearAgentStage()
	}
}

func (s *ChatSession) setInterruptCleanup(done chan struct{}) {
	if s == nil || done == nil {
		return
	}
	s.interruptCleanupMu.Lock()
	previous := s.interruptCleanupDone
	if previous == nil {
		s.interruptCleanupDone = done
		s.interruptCleanupMu.Unlock()
		return
	}
	combined := make(chan struct{})
	s.interruptCleanupDone = combined
	s.interruptCleanupMu.Unlock()
	go func() {
		<-previous
		<-done
		close(combined)
	}()
}

// reserveInterruptCleanup atomically registers one in-flight cleanup. A second
// Esc while the first cleanup is still running reuses that cleanup instead of
// chaining another forever-blocked signal.
func (s *ChatSession) reserveInterruptCleanup() (chan struct{}, bool) {
	if s == nil {
		return nil, false
	}
	s.interruptCleanupMu.Lock()
	defer s.interruptCleanupMu.Unlock()
	if done := s.interruptCleanupDone; done != nil {
		select {
		case <-done:
			// Completed but not yet detached by a waiter; replace it.
		default:
			return nil, false
		}
	}
	done := make(chan struct{})
	s.interruptCleanupDone = done
	return done, true
}

// startInterruptCleanup launches async interrupt cleanup exactly once per
// outstanding cleanup, so repeated Esc cannot stack waiting goroutines.
func (s *ChatSession) startInterruptCleanup() bool {
	done, ok := s.reserveInterruptCleanup()
	if !ok {
		return false
	}
	go s.runInterruptCleanup(done)
	return true
}

func (s *ChatSession) runInterruptCleanup(done chan struct{}) {
	defer close(done)
	if s == nil {
		return
	}
	defer s.finishInterruptCleanupUI()
	if s.LocalRuntimeHost == nil {
		return
	}
	host := s.LocalRuntimeHost
	baseSessionID := currentRuntimeSessionID(s)
	userID := strings.TrimSpace(s.SessionUserID)
	activeTeamID := activeTeamID(s)
	ctx, cancel := context.WithTimeout(context.Background(), chatInterruptCleanupTimeout)
	defer cancel()
	host.interruptActiveRuns(ctx, baseSessionID, userID, activeTeamID)
}

func (s *ChatSession) waitForInterruptCleanup() {
	s.waitForInterruptCleanupWithin(chatInterruptCleanupWaitTimeout)
}

func (s *ChatSession) waitForInterruptCleanupWithin(timeout time.Duration) {
	if s == nil {
		return
	}
	if timeout <= 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.interruptCleanupMu.Lock()
		done := s.interruptCleanupDone
		s.interruptCleanupMu.Unlock()
		if done == nil {
			return
		}
		select {
		case <-done:
			s.interruptCleanupMu.Lock()
			if s.interruptCleanupDone == done {
				s.interruptCleanupDone = nil
				s.interruptCleanupMu.Unlock()
				return
			}
			s.interruptCleanupMu.Unlock()
		case <-timer.C:
			// Cleanup work already has its own context deadline. This outer hard
			// limit prevents an abnormal cleanup implementation (or a future
			// regression that never closes its signal) from blocking the chat loop
			// forever. Only detach the signal observed by this waiter so a newer
			// concurrently registered cleanup remains available to the next reset.
			s.interruptCleanupMu.Lock()
			if s.interruptCleanupDone == done {
				s.interruptCleanupDone = nil
			}
			s.interruptCleanupMu.Unlock()
			return
		}
	}
}

// isInterruptCleanupInFlight reports whether async stop/lease cleanup is still
// outstanding. A closed-but-not-yet-detached signal counts as finished.
func (s *ChatSession) isInterruptCleanupInFlight() bool {
	if s == nil {
		return false
	}
	s.interruptCleanupMu.Lock()
	done := s.interruptCleanupDone
	s.interruptCleanupMu.Unlock()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
		return true
	}
}

// finishInterruptCleanupUI leaves the Stopping composer stage once actor stop
// and lease release have completed, without waiting for the next user input.
func (s *ChatSession) finishInterruptCleanupUI() {
	if s == nil || s.Interaction == nil {
		return
	}
	if s.Interaction.AgentStage() == chatAgentStageStopping {
		s.Interaction.ClearAgentStage()
	}
}

// IsInterrupted 检查是否被中断
// 优先检查原子标志（由信号处理器设置），再检查 cancelCtx 状态作为回退
func (s *ChatSession) IsInterrupted() bool {
	if s == nil {
		return false
	}
	if s.interrupted.Load() {
		return true
	}
	if s.cancelCtx == nil {
		return false
	}
	select {
	case <-s.cancelCtx.Done():
		s.interrupted.Store(true)
		return true
	default:
		return false
	}
}

// ShellCommandConfig Shell 命令执行配置
type ShellCommandConfig struct {
	Timeout          time.Duration // 命令超时时间
	MaxLines         int           // 最大输出行数
	MaxOutputSize    int           // 兼容字段：未显式设置 OutputBytesCap 时作为输出字节上限使用
	OutputBytesCap   int           // shell 输出 capture limit（字节）；0 表示回退到 MaxOutputSize / 默认值
	DisableOutputCap bool          // 关闭 shell 输出 capture limit，尽量保留完整原始输出
}

// 默认 Shell 命令配置
const (
	DefaultShellTimeout       = 30 * time.Second // 默认超时 30 秒
	DefaultShellMaxLines      = 1000             // 默认最多 1000 行输出
	DefaultShellMaxOutputSize = 256 * 1024       // 默认最多 256KB capture 输出
)

// HandleChat 处理 chat 命令
func HandleChat(cmd *cobra.Command, cfg *config.Config) {
	startupTiming := newChatStartupTiming()
	activeChatStartupTiming = startupTiming
	defer func() {
		activeChatStartupTiming = nil
	}()
	startupTiming.mark("begin")
	// 启动挂起 watchdog：90s 未到 ready 自动 dump goroutine 栈。
	armChatStartupHangWatchdog()

	opts, err := parseChatCommandOptions(cmd, cfg)
	if err != nil {
		exitCommandError("chat", "json", err, nil)
	}
	startupTiming.mark("parse_options")

	// Resolve folder trust before profile/plugin discovery so project-scope
	// plugins/hooks/MCP are gated consistently for this process.
	ensureProcessFolderTrust(opts.TrustGrant, !opts.NoInteractive)

	if restoreLogger := suppressChatConsoleLogger(cfg, opts); restoreLogger != nil {
		defer restoreLogger()
	}

	// Warm PATH/health capability cache off the critical path so the first
	// system-prompt freeze is usually a memory/disk hit instead of a cold probe.
	runtimeprompt.WarmEnvironmentCapabilitiesAsync()

	profileState, err := resolveChatProfileState(cfg, opts)
	if err != nil {
		exitCommandError("chat", opts.OutputFormat, err, nil)
	}
	applyProfileDefaultsToChatOptions(opts, profileState)
	startupTiming.mark("profile")

	persistenceState, err := prepareChatPersistence(cfg, opts, profileState)
	if err != nil {
		exitCommandError("chat", opts.OutputFormat, err, nil)
	}
	if persistenceState.runtimeSessionManager != nil {
		defer persistenceState.runtimeSessionManager.Stop()
	}
	startupTiming.mark("persistence")

	if opts.ListSessionsFlag {
		if err := printChatSessionSummaries(persistenceState.runtimeSessionManager, persistenceState.sessionUserID, "", opts.SessionFilter); err != nil {
			exitCommandError("chat", opts.OutputFormat, err, nil)
		}
		return
	}

	clearChatStartupScreen(opts)

	if shouldShowChatStartupBanner(opts) {
		printWelcome()
	}

	// 启动时不再弹出历史会话选择菜单：默认直接进入新会话，用户可在聊天中通过
	// /resume 恢复最近可恢复会话、/sessions [query] 浏览历史、/load <id> 加载指定会话、
	// /new 创建新会话。`--session <id>`、`--resume`、`--list-sessions` 等显式
	// 命令行参数仍然按原有语义生效。

	runtimeState, details, err := prepareChatRuntimeState(cfg, opts, persistenceState.loadedRuntimeSession)
	if err != nil {
		exitCommandError("chat", opts.OutputFormat, err, details)
	}
	startupTiming.mark("runtime_state")

	session, cleanupSession, err := bootstrapChatSession(cfg, opts, profileState, persistenceState, runtimeState)
	if err != nil {
		exitCommandError("chat", opts.OutputFormat, err, nil)
	}
	startupTiming.mark("bootstrap")
	persistChatStartupPreferences(cfg, opts, persistenceState.loadedRuntimeSession, runtimeState)
	finalCleanup := buildChatFinalCleanup(session, cleanupSession)
	registerExitCleanup(finalCleanup)
	defer runExitCleanup()

	// 注册渲染/显示状态 HTTP provider：/debug/chat/status 端点回调此函数
	// 获取当前会话快照，实现 --debug/--pprof 模式下在线连续采样渲染状态。
	// 会话结束时注销（返回 nil），避免端点拿到已销毁的会话。
	RegisterChatDebugDisplayProvider(func() *ChatSession { return session })
	registerExitCleanup(func() {
		RegisterChatDebugDisplayProvider(func() *ChatSession { return nil })
	})

	// Welcome/meta preamble stays TUI-gated, but restored history must still
	// replay so `aicli resume <id>` / `aicli chat --session` match in-chat
	// `/resume` visibility even when interactive TUI is enabled.
	presentChatStartupSession(session, opts, persistenceState.loadedRuntimeSession)
	startupTiming.mark("ready")
	startupTiming.flush(opts)

	// 开始聊天循环
	runChatLoop(session, opts.NoInteractive, opts.Message)
}

func loadRuntimeToolConfig(cfg *config.Config, session *ChatSession) *runtimecfg.RuntimeConfig {
	configPath := ""
	if session != nil && strings.TrimSpace(session.RuntimeConfigPath) != "" {
		configPath = strings.TrimSpace(session.RuntimeConfigPath)
	} else if cfg != nil && cfg.SkillsRuntime != nil && strings.TrimSpace(cfg.SkillsRuntime.ConfigFile) != "" {
		configPath = resolveGlobalRuntimeConfigPath(cfg)
	}
	if configPath == "" {
		resolved := runtimecfg.DefaultRuntimeConfig()
		resolved.Workspace.Root = resolveLocalWorkspacePath(resolved, session)
		return resolved
	}

	resolved, _, err := loadCachedRuntimeConfig(configPath)
	if err != nil || resolved == nil {
		reason := formatRuntimeConfigLoadFallback(configPath, err)
		fmt.Fprintf(os.Stderr, "Warning: 加载 runtime tools 配置失败，已退回默认 sandbox 配置: %s\n", reason)
		logpkg.Warnf("AICLI runtime tools config load failed: %s", reason)
		return runtimecfg.DefaultRuntimeConfig()
	}
	if session != nil && session.ToolPolicy != nil && session.ToolPolicy.Sandbox != nil {
		runtimeexecutor.OverlaySandboxConfig(&resolved.Sandbox, session.ToolPolicy.Sandbox.Config())
	}
	resolved.Workspace.Root = resolveLocalWorkspacePath(resolved, session)
	return resolved
}

// printWelcome 打印欢迎信息
func printWelcome() {
	ui.PrintWelcome()
}

// selectProvider 选择 Provider
func selectProvider(cfg *config.Config) string {
	return selectProviderWithReader(cfg, bufio.NewReader(os.Stdin))
}

func selectProviderWithReader(cfg *config.Config, reader *bufio.Reader) string {
	printChatSelectionSection("选择 Provider")

	// 列出可用的 providers
	var providers []string
	for name, p := range cfg.Providers.Items {
		if p.Enabled {
			providers = append(providers, name)
		}
	}
	sort.Strings(providers)

	if len(providers) == 0 {
		ui.PrintErrorTo(os.Stderr, "没有可用的 providers")
		return ""
	}

	maxNameLen := 0
	for _, p := range providers {
		if width := ui.DisplayWidth(p); width > maxNameLen {
			maxNameLen = width
		}
	}

	for i, p := range providers {
		summary := ""
		if provider, ok := cfg.Providers.Items[p]; ok {
			summary = describeProviderSelection(provider)
		}
		padding := strings.Repeat(" ", maxNameLen-ui.DisplayWidth(p))
		primary := fmt.Sprintf("  [%d] %s%s", i+1, p, padding)
		var muted []string
		if summary != "" {
			muted = append(muted, "  "+summary)
		}
		if p == cfg.Providers.DefaultProvider {
			muted = append(muted, " (默认)")
		}
		printChatSelectionMutedSuffix(primary, muted...)
	}
	printChatSelectionBlankLine()

	for {
		printChatSelectionPrompt("请输入选项 (或直接回车使用默认): ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "" {
			return cfg.Providers.DefaultProvider
		}

		if num, err := strconv.Atoi(input); err == nil {
			if num >= 1 && num <= len(providers) {
				return providers[num-1]
			}
			printChatSelectionWarning("无效的选择，请重新输入")
			continue
		}

		for _, p := range providers {
			if strings.EqualFold(p, input) {
				return p
			}
		}

		printChatSelectionWarning("无效的选择，请重新输入")
	}
}

func describeProviderSelection(provider config.Provider) string {
	parts := make([]string, 0, 4)

	if protocol := strings.TrimSpace(provider.GetProtocol()); protocol != "" {
		parts = append(parts, "protocol="+protocol)
	}

	if host := providerSelectionHost(provider); host != "" {
		parts = append(parts, "host="+host)
	} else if rawURL := providerSelectionURL(provider); rawURL != "" {
		parts = append(parts, "url="+rawURL)
	}

	if model := strings.TrimSpace(provider.DefaultModel); model != "" {
		parts = append(parts, "model="+model)
	}

	return strings.Join(parts, " | ")
}

func providerSelectionHost(provider config.Provider) string {
	if host := extractChatSessionHost(providerSelectionURL(provider)); host != "" {
		return host
	}
	return extractChatSessionHost(strings.TrimSpace(provider.BaseURL))
}

func providerSelectionURL(provider config.Provider) string {
	if forwardURL := strings.TrimSpace(provider.ForwardURL); forwardURL != "" {
		if strings.HasPrefix(forwardURL, "/") {
			baseURL := strings.TrimSuffix(strings.TrimSpace(provider.BaseURL), "/")
			if baseURL != "" {
				return baseURL + forwardURL
			}
		}
		return forwardURL
	}
	return strings.TrimSpace(provider.BaseURL)
}

// selectModel 选择 Model
func selectModel(provider config.Provider) string {
	return selectModelWithReader(provider, bufio.NewReader(os.Stdin))
}

func selectModelWithReader(provider config.Provider, reader *bufio.Reader) string {
	printChatSelectionSection("选择 Model")

	if len(provider.SupportedModels) == 0 {
		return ""
	}

	sort.Strings(provider.SupportedModels)
	for i, m := range provider.SupportedModels {
		if m == provider.DefaultModel {
			printChatSelectionMutedSuffix(fmt.Sprintf("  [%d] %s ", i+1, m), "(默认)")
		} else {
			printChatSelectionLine("  [%d] %s", i+1, m)
		}
	}
	printChatSelectionBlankLine()

	for {
		printChatSelectionPrompt("请输入选项 (或直接回车使用默认): ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "" {
			return provider.DefaultModel
		}

		if num, err := strconv.Atoi(input); err == nil {
			if num >= 1 && num <= len(provider.SupportedModels) {
				return provider.SupportedModels[num-1]
			}
			printChatSelectionWarning("无效的选择，请重新输入")
			continue
		}

		for _, model := range provider.SupportedModels {
			if strings.EqualFold(model, input) {
				return model
			}
		}

		return input
	}
}

// selectStreamMode 选择流式模式
func selectStreamMode() bool {
	return selectStreamModeWithReader(bufio.NewReader(os.Stdin))
}

func selectStreamModeWithReader(reader *bufio.Reader) bool {
	printChatSelectionSection("选择输出模式")

	printChatSelectionLine("  [1] 普通 (等待完整响应)")
	printChatSelectionParts(
		chatPart("  [2] ", style.RoleTextPrimary),
		chatBoldPart("流式", style.RoleSuccess),
		chatPart(" (实时输出) ", style.RoleTextPrimary),
		chatPart("(默认)", style.RoleTextMuted),
	)
	printChatSelectionBlankLine()

	for {
		printChatSelectionPrompt("请输入选项 (默认: 流式): ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "" {
			return true
		}

		switch strings.ToLower(input) {
		case "1", "normal", "n", "普通":
			return false
		case "2", "stream", "s", "流式":
			return true
		}

		printChatSelectionWarning("无效的选择，请重新输入")
	}
}

// printSessionInfo 打印会话信息
func printSessionInfo(session *ChatSession) {
	ui.PrintSessionInfo(buildChatSessionInfo(session))
	if descriptor, ok := chatRuntimeExecutorDescriptor(session.ChatExecutor); ok {
		printChatSessionMetaRow("Runtime Core:", fmt.Sprintf("%s contract=v%d", descriptor.Core.Name, descriptor.Core.ContractVersion))
		printChatSessionMetaRow("Runtime Transport:", descriptor.Transport)
	}

	// 显示 MCP 状态
	if session.MCPEnabled && session.MCPStatus != nil {
		printChatSessionMetaRow("MCP:", fmt.Sprintf("已启用 (%d 个工具, %d 个 MCP 服务器)",
			session.MCPStatus.ToolCount, session.MCPStatus.MCPCount))
	}
	if session.ProfileName != "" {
		profileValue := session.ProfileName
		if session.ProfileAgent != "" {
			profileValue += fmt.Sprintf(" (agent=%s)", session.ProfileAgent)
		}
		printChatSessionMetaRow("Profile:", profileValue)
	}
	if line := formatChatAgentSourceLine(session); line != "" {
		printChatSessionMetaRow("Agent Source:", line)
	}
	if reasoningEffort := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort); reasoningEffort != "" {
		printChatSessionMetaRow("Reasoning Effort:", reasoningEffort)
	}
	if !chatReasoningOutputEnabled(session) {
		printChatSessionMetaRow("Reasoning Output:", "off")
	}
	if session.LocalRuntimeHost != nil {
		ctx := snapshotChatRuntimeContext(session)
		printChatSessionMetaRow("Permission Mode:", string(ctx.PermissionMode))
		printChatSessionMetaRow("Approval Reuse:", formatChatApprovalReuseMode(ctx.ApprovalReuseMode))
	}
	if queuedCount, draining := queuedInteractiveInputState(session); queuedCount > 0 || draining {
		value := fmt.Sprintf("%d pending", queuedCount)
		if draining {
			value += " (draining)"
		}
		printChatSessionMetaRow("Queued Input:", value)
	}
	if session.DisableTools {
		printChatSessionMetaRow("Tools:", "disabled")
	} else if session.ToolPolicy != nil {
		if names := session.ToolPolicy.AllowedToolNames(); len(names) > 0 {
			printChatSessionMetaRow("Tools Allowlist:", strings.Join(names, ", "))
		}
	}
	if session.HTTPDebug {
		printChatSessionMetaRow("HTTP Debug:", "on")
	}
	if session.RetryConfig.DisableRetries {
		printChatSessionMetaRow("Retry Mode:", "fail-fast")
	}
	printChatSessionCompactLineage(session)
	// Codex Fast preference is already shown via ui.SessionInfo when supported.
}

func printChatSessionCompactLineage(session *ChatSession) {
	if session == nil || session.RuntimeSession == nil {
		return
	}
	runtimeSession := session.RuntimeSession
	generation := runtimeSessionCompactGeneration(runtimeSession)
	if generation <= 0 {
		return
	}
	// Labels stay within chatSessionMetaLabelWidth (18) so /session rows align.
	printChatSessionMetaRow("Compact Gen:", fmt.Sprintf("#%d", generation))
	if rootTitle := strings.TrimSpace(runtimeSessionContextString(runtimeSession, runtimechat.ContextCompactRootTitle)); rootTitle != "" {
		printChatSessionMetaRow("Compact Root:", rootTitle)
	}
	if rootID := strings.TrimSpace(runtimeSessionContextString(runtimeSession, runtimechat.ContextCompactRootSessionID)); rootID != "" {
		printChatSessionMetaRow("Compact Root ID:", rootID)
	}
}

func printChatSessionMetaRow(label, value string) {
	if strings.TrimSpace(label) == "" {
		return
	}
	printChatSessionInfoRow(os.Stdout, label, value, chatSessionMetaLabelWidth)
}

// formatChatAgentSourceLine renders "source · path" for the active agentdef/profile agent.
// Empty when neither source nor path is known (no agent binding).
func formatChatAgentSourceLine(session *ChatSession) string {
	if session == nil {
		return ""
	}
	source := strings.TrimSpace(session.AgentSource)
	path := strings.TrimSpace(session.AgentSourcePath)
	if path != "" && !strings.HasPrefix(path, "builtin:") {
		path = resolveAbsoluteChatPath(path)
	}
	switch {
	case source != "" && path != "":
		return source + " · " + path
	case path != "":
		return path
	case source != "":
		return source
	default:
		return ""
	}
}

func resolvedChatSkillsMode(session *ChatSession, binding *skillsRuntimeBinding) string {
	if binding != nil && binding.exposureMode != "" {
		return binding.exposureMode
	}
	if session != nil && strings.TrimSpace(session.SkillsMode) != "" {
		return strings.TrimSpace(session.SkillsMode)
	}
	return "auto"
}

func resolvedChatSkillsTopK(binding *skillsRuntimeBinding) int {
	if binding == nil || binding.exposureTopK <= 0 {
		return 0
	}
	return binding.exposureTopK
}

func resolveAICLIRetryConfig(cfg *config.Config) RetryConfig {
	retryCfg := defaultRetryConfig()
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.Retry == nil {
		return retryCfg
	}
	if cfg.AICLI.Retry.MaxTotalTime > 0 {
		retryCfg.MaxRetryTime = cfg.AICLI.Retry.MaxTotalTime
	}
	if cfg.AICLI.Retry.FastRetryCount > 0 {
		retryCfg.FastRetryCount = cfg.AICLI.Retry.FastRetryCount
	}
	if cfg.AICLI.Retry.FastRetryInterval > 0 {
		retryCfg.FastRetryInterval = cfg.AICLI.Retry.FastRetryInterval
	}
	if cfg.AICLI.Retry.SlowRetryInterval > 0 {
		retryCfg.SlowRetryInterval = cfg.AICLI.Retry.SlowRetryInterval
	}
	return retryCfg
}

func resolveAICLIRequestTimeout(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.Timeout == nil {
		return 0
	}
	if cfg.AICLI.Timeout.RequestTimeout > 0 {
		return cfg.AICLI.Timeout.RequestTimeout
	}
	return 0
}

func resolveChatOutputFormat(noInteractive bool, outputFlag string, jsonAlias bool) (string, error) {
	output := strings.ToLower(strings.TrimSpace(outputFlag))
	if output == "" && jsonAlias {
		output = "json"
	}

	if !noInteractive {
		if output == "" || output == "text" {
			return "interactive", nil
		}
		return "", fmt.Errorf("--output 仅支持 --no-interactive 模式，当前值: %s", output)
	}

	if output == "" {
		return "text", nil
	}
	switch output {
	case "text", "json":
		return output, nil
	default:
		return "", fmt.Errorf("无效的 output: %s（可选值: text|json）", outputFlag)
	}
}

func shouldDisplayFinalResponse(session *ChatSession, response string) bool {
	if session == nil {
		return false
	}
	if session.Stream && !shouldDisplayActorStreamFallback(session) {
		return false
	}
	return strings.TrimSpace(response) != ""
}

func shouldDisplayActorStreamFallback(session *ChatSession) bool {
	if session == nil || !session.Stream {
		return false
	}
	descriptor, ok := chatRuntimeExecutorDescriptor(session.ChatExecutor)
	return ok && descriptor.unifiedActorRuntime()
}

func wasInteractiveActorResponseAlreadyRendered(session *ChatSession, response string) bool {
	if session == nil || session.NoInteractive || session.JSONOutput || session.RuntimeEventBridge == nil {
		return false
	}
	return chatExecutorUsesRuntimeEvents(session.ChatExecutor) && session.RuntimeEventBridge.HasRenderedAssistantFinalResponse(response)
}

func finalizeInteractiveActorStreamIfNeeded(session *ChatSession, response string) bool {
	if session == nil || session.NoInteractive || session.JSONOutput || !session.Stream || session.RuntimeEventBridge == nil {
		return false
	}
	if !chatExecutorUsesRuntimeEvents(session.ChatExecutor) {
		return false
	}
	if !session.RuntimeEventBridge.HasRenderedAssistantDelta() {
		return false
	}
	completed := false
	if session.Interaction != nil {
		completed = session.Interaction.CompleteAssistantResponse(response)
	} else {
		completed = true
	}
	if completed {
		session.RuntimeEventBridge.MarkAssistantFinalResponseRendered(response)
	}
	return completed
}

func chatExecutorUsesRuntimeEvents(executor aicliChatExecutor) bool {
	descriptor, ok := chatRuntimeExecutorDescriptor(executor)
	return ok && descriptor.unifiedActorRuntime()
}

func chatRuntimeExecutorDescriptor(executor aicliChatExecutor) (aicliRuntimeExecutorDescriptor, bool) {
	if executor == nil {
		return aicliRuntimeExecutorDescriptor{}, false
	}
	return executor.RuntimeDescriptor(), true
}

func shouldPrintChatSessionPreamble(session *ChatSession) bool {
	return session != nil && !session.NoInteractive && !session.JSONOutput
}

// presentChatStartupSession renders startup session context after bootstrap.
//
// Welcome banner and full session meta remain TUI-gated via
// shouldShowChatSessionStartupPreamble. Restored conversation history is
// independent of that gate so CLI resume (`aicli resume`, `aicli chat
// --session`, `--resume`) still shows the transcript the user is continuing.
func presentChatStartupSession(session *ChatSession, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) {
	if session == nil || opts == nil {
		return
	}

	showPreamble := shouldShowChatSessionStartupPreamble(opts)
	if showPreamble {
		presentChatSession(session)
	}
	if !shouldPrintChatSessionPreamble(session) {
		return
	}
	hasHistory := hasVisibleChatHistory(session)

	// Legacy/plain interactive path already printed full meta via presentChatSession;
	// only append the transcript when visible history exists.
	if showPreamble {
		if !hasHistory {
			return
		}
		// ClearPrompt may leave pendingScrollDown layout debt. Do not raw-print
		// a blank here (bypasses surface); printVisibleChatHistory settles debt
		// then owns all content-plane spacing via RenderSupplement / gaps.
		beginDirectInteractiveOutput(session)
		printVisibleChatHistory(session, "已加载历史会话")
		return
	}

	// A restored runtime handle is metadata, not the source-of-truth gate for
	// transcript visibility. Canonical Messages/ResumeHistory can already be
	// present when a runtime host is unavailable or its startup handle was not
	// retained. In that case, the main primary viewport must still receive the
	// finalized history; otherwise it degenerates into a reasoning-only screen.
	if loadedRuntimeSession != nil {
		// TUI path: skip welcome/meta preamble, but still surface resume status +
		// history so CLI resume matches in-chat `/resume` visibility.
		beginDirectInteractiveOutput(session)
		printResumeSuccess(session)
		return
	}
	if hasHistory {
		beginDirectInteractiveOutput(session)
		printVisibleChatHistory(session, "已加载历史会话")
	}
}

type chatResponsePayload struct {
	Response                   string   `json:"response"`
	Provider                   string   `json:"provider,omitempty"`
	Protocol                   string   `json:"protocol,omitempty"`
	Model                      string   `json:"model,omitempty"`
	Stream                     bool     `json:"stream"`
	RuntimeCore                string   `json:"runtime_core,omitempty"`
	RuntimeContractVersion     int      `json:"runtime_contract_version,omitempty"`
	RuntimeTransport           string   `json:"runtime_transport,omitempty"`
	SessionID                  string   `json:"session_id,omitempty"`
	SessionPath                string   `json:"session_path,omitempty"`
	SessionStore               string   `json:"session_store,omitempty"`
	SessionState               string   `json:"session_state,omitempty"`
	QueuedInputCount           int      `json:"queued_input_count,omitempty"`
	QueuedInputDraining        bool     `json:"queued_input_draining,omitempty"`
	ReasoningEffort            string   `json:"reasoning_effort,omitempty"`
	RequestedProvider          string   `json:"requested_provider,omitempty"`
	EffectiveProvider          string   `json:"effective_provider,omitempty"`
	RequestedModel             string   `json:"requested_model,omitempty"`
	EffectiveModel             string   `json:"effective_model,omitempty"`
	RequestedReasoningEffort   string   `json:"requested_reasoning_effort,omitempty"`
	EffectiveReasoningEffort   string   `json:"effective_reasoning_effort,omitempty"`
	RequestedPermissionMode    string   `json:"requested_permission_mode,omitempty"`
	EffectivePermissionMode    string   `json:"effective_permission_mode,omitempty"`
	RouteWarnings              []string `json:"route_warnings,omitempty"`
	FallbackUsed               bool     `json:"fallback_used,omitempty"`
	FallbackReason             string   `json:"fallback_reason,omitempty"`
	TotalTokens                int      `json:"total_tokens,omitempty"`
	ResponseTimeMs             int64    `json:"average_response_time_ms,omitempty"`
	LogPath                    string   `json:"log_path,omitempty"`
	DebugLogPath               string   `json:"debug_log_path,omitempty"`
	HTTPArtifactDir            string   `json:"http_artifact_dir,omitempty"`
	LastHTTPRequestPath        string   `json:"last_http_request_path,omitempty"`
	LastHTTPResponsePath       string   `json:"last_http_response_path,omitempty"`
	LocalShellArtifactDir      string   `json:"local_shell_artifact_dir,omitempty"`
	LastLocalShellArtifactPath string   `json:"last_local_shell_artifact_path,omitempty"`
}

func buildChatResponsePayload(session *ChatSession, response string) chatResponsePayload {
	payload := chatResponsePayload{
		Response: response,
	}
	if session == nil {
		return payload
	}
	payload.Provider = session.ProviderName
	payload.Protocol = session.Provider.GetProtocol()
	payload.Model = session.Model
	payload.Stream = session.Stream
	if descriptor, ok := chatRuntimeExecutorDescriptor(session.ChatExecutor); ok {
		payload.RuntimeCore = descriptor.Core.Name
		payload.RuntimeContractVersion = descriptor.Core.ContractVersion
		payload.RuntimeTransport = descriptor.Transport
	}
	payload.ReasoningEffort = runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	payload.RequestedProvider = strings.TrimSpace(firstNonEmptyChatValue(session.RequestedProvider, session.ProviderName))
	payload.EffectiveProvider = strings.TrimSpace(firstNonEmptyChatValue(session.EffectiveProvider, session.ProviderName))
	payload.RequestedModel = strings.TrimSpace(firstNonEmptyChatValue(session.RequestedModel, session.Model))
	payload.EffectiveModel = strings.TrimSpace(firstNonEmptyChatValue(session.EffectiveModel, session.Model))
	payload.RequestedReasoningEffort = runtimetypes.NormalizeReasoningEffort(firstNonEmptyChatValue(session.RequestedReasoningEffort, session.ReasoningEffort))
	payload.EffectiveReasoningEffort = runtimetypes.NormalizeReasoningEffort(firstNonEmptyChatValue(session.EffectiveReasoningEffort, session.ReasoningEffort))
	ctx := snapshotChatRuntimeContext(session)
	payload.RequestedPermissionMode = strings.TrimSpace(firstNonEmptyChatValue(ctx.RequestedPermissionMode, string(ctx.PermissionMode)))
	payload.EffectivePermissionMode = strings.TrimSpace(firstNonEmptyChatValue(ctx.EffectivePermissionMode, string(ctx.PermissionMode)))
	payload.RouteWarnings = append([]string(nil), session.RouteWarnings...)
	payload.FallbackUsed = session.FallbackUsed
	payload.FallbackReason = strings.TrimSpace(session.FallbackReason)
	if session.RuntimeSession != nil {
		payload.SessionID = session.RuntimeSession.ID
		payload.SessionPath = currentRuntimeSessionPath(session)
		payload.SessionStore = currentRuntimeSessionStoreSummary(session)
		payload.SessionState = string(session.RuntimeSession.State)
	}
	payload.QueuedInputCount, payload.QueuedInputDraining = queuedInteractiveInputState(session)
	if session.Logger != nil {
		if summary := session.Logger.CurrentSummary(); summary != nil {
			payload.TotalTokens = summary.TotalTokens
			payload.ResponseTimeMs = summary.AverageResponseTimeMs
		}
		payload.LogPath = currentChatLogFile(session)
		payload.DebugLogPath = currentDebugLogFile(session)
	}
	payload.HTTPArtifactDir = currentRuntimeHTTPArtifactDir(session)
	payload.LocalShellArtifactDir = currentLocalShellArtifactDir(session)
	if session.runtimeHTTPCapture != nil {
		snapshot := session.runtimeHTTPCapture.Snapshot()
		payload.LastHTTPRequestPath = resolveAbsoluteChatPath(snapshot.RequestArtifactPath)
		payload.LastHTTPResponsePath = resolveAbsoluteChatPath(snapshot.ResponseArtifactPath)
	}
	payload.LastLocalShellArtifactPath = currentLastLocalShellArtifactPath(session)
	return payload
}

func renderChatResponse(session *ChatSession, response string) {
	if session == nil {
		return
	}
	if session.JSONOutput {
		payload := buildChatResponsePayload(session, response)
		printCommandJSONOutput("chat", session.JSONEnvelope, payload)
		return
	}
	if session.NoInteractive {
		fmt.Println(response)
		return
	}
	if session.Interaction != nil {
		// This fallback has no runtime assistant event to encode. Keep it out
		// of RenderAssistant, which is also the projection callback for events
		// that have already entered Scene through chatRuntimeEventBridge.
		session.Interaction.RenderLocalAssistant(response)
		return
	}
	newAICLITranscriptRenderer(session).RenderAssistant(response)
}

// runChatLoop 运行聊天循环
func runChatLoop(session *ChatSession, noInteractive bool, initialMessage string) {
	if !noInteractive {
		if shouldUseInteractiveLineEditor(session) {
			// 交互 TTY 场景使用逐键 line editor，不再走按行队列。
			session.InputQueue = nil
			if chatDebugFlagEnabled() {
				aicliDiagln("[aicli-diag] input path: interactive line editor (TUI composer)")
			}
		} else if chatPipeLineEditorPreferred() {
			// 管道/PTY 场景（MobaXterm、cygwin/mintty、winpty、SSH 等）：
			// legacy 控制台编辑器不可用，且 stdin/stdout 是管道或字符设备。
			// 不启动 queue/pump，由 chatInteractiveReadLine 回退分支独占 stdin
			// 打开逐键编辑器，避免 pump 后台读取与其竞争同一句柄。
			session.InputQueue = nil
			if chatDebugFlagEnabled() {
				aicliDiagln("[aicli-diag] input path: pipe/PTY interactive line editor (direct reader)")
			}
		} else {
			ensureChatInputQueue(session)
			if chatDebugFlagEnabled() {
				aicliDiagln("[aicli-diag] input path: line-mode queue (pump/legacy editor)")
			}
		}
	}

	// 设置信号处理（平台特定：Unix 支持 Ctrl+C Ctrl+Break ESC; Windows 仅 Ctrl+C）
	sigChan := make(chan os.Signal, 1)
	var shouldExit atomic.Bool
	stopSignalHandler := setupSignalHandler(session, sigChan, &shouldExit)
	defer stopSignalHandler()

	// 聊天循环
	for {
		// 检查二次终止信号
		if shouldExit.Load() {
			printDirectInteractiveOutput(session, "\n")
			break
		}

		// 重置中断状态（新的输入开始）
		session.ResetInterrupt()
		// 创建新的可取消上下文用于本次操作
		if session.cancelFunc != nil {
			session.cancelFunc()
		}
		session.cancelCtx, session.cancelFunc = newChatCancelContext()
		if shouldExit.Load() {
			printDirectInteractiveOutput(session, "\n")
			break
		}

		var input string
		var err error

		// CLI 启动消息在 TUI 初始化和历史恢复完成后优先提交一次。
		// 交互模式提交完成后继续下一轮输入；非交互模式在本轮结束后退出。
		if initialMessage != "" {
			input = initialMessage
			// 使用后清空，避免循环发送
			initialMessage = ""
		} else {
			if !noInteractive {
				showPrompt, notice, err := prepareInteractiveRead(session)
				if err != nil {
					if session.IsInterrupted() {
						continue
					}
					if session != nil && session.Interaction != nil {
						session.Interaction.RenderError(err)
					} else if !unifiedInteractiveOutputMustFailClosed(session) {
						ui.PrintError("操作错误: %v", err)
					}
					continue
				}
				if shouldExit.Load() {
					printDirectInteractiveOutput(session, "\n")
					break
				}
				if notice != "" {
					if session.Interaction != nil {
						session.Interaction.RenderLocalSupplement(notice)
					} else if !unifiedInteractiveOutputMustFailClosed(session) {
						fmt.Println(notice)
					}
				}
				if shouldExit.Load() {
					printDirectInteractiveOutput(session, "\n")
					break
				}
				if showPrompt {
					if session.Interaction != nil {
						session.Interaction.PrintPrompt()
					} else if !unifiedInteractiveOutputMustFailClosed(session) {
						fmt.Print(ui.FormatUserPromptWithAttachments(len(session.ImagePaths)))
					}
				}
				if shouldExit.Load() {
					printDirectInteractiveOutput(session, "\n")
					break
				}
			}

			input, err = chatInteractiveReadLine(session, session.cancelCtx)
			finishChatInteractiveReadPromptState(session, err)
			if err != nil {
				if errors.Is(err, ui.ErrInteractiveInputTranscriptRequested) {
					openChatTranscriptPager(session)
					continue
				}
				if errors.Is(err, ui.ErrInteractiveInputExitRequested) {
					printDirectInteractiveOutput(session, "正在退出...\n")
					break
				}
				// Bare Esc on empty composer: open user-turn backtrack picker (Codex-style).
				if errors.Is(err, ui.ErrInteractiveInputBacktrackRequested) {
					handleInteractiveBacktrackSelect(session)
					continue
				}
				// Ctrl+D (EOF)：交互行编辑器场景静默忽略；队列/普通 reader 场景在输入结束后退出循环，避免空转。
				if errors.Is(err, io.EOF) {
					if !shouldUseInteractiveLineEditor(session) {
						// Web 输入唤醒（/web/api/input 排队后取消读取上下文）会把
						// context.Canceled 归一化为 io.EOF；若输入队列中已有 Web
						// 输入待消费，不能当作 stdin 关闭退出，应继续下一轮优先
						// 消费队列（chatInteractiveReadLine 顶部会先读队列）。
						if chatInputQueueHasQueuedLines(session) {
							continue
						}
						printDirectInteractiveOutput(session, "\n")
						break
					}
					continue
				}
				// 读取失败通常是因为用户按了 Ctrl+C
				// 这种情况下应该跳过本次循环，重新开始
				if session.IsInterrupted() {
					continue
				}
				// 其他错误才真正退出
				if session != nil && session.NoInteractive {
					exitCommandError("chat", session.OutputFormat, fmt.Errorf("读取输入失败"), nil)
				}
				printDirectInteractiveOutput(session, "\n"+ui.FormatErrorMessage("读取输入失败")+"\n")
				break
			}
			input = strings.TrimSpace(normalizeQueuedInputLine(input))
		}

		// 处理 Shell 命令（! 前缀）
		if strings.HasPrefix(input, "!") {
			// 在执行前检查中断状态
			if session.IsInterrupted() {
				continue
			}
			if unifiedDirectInteractiveOutput(session) {
				// 统一渲染 TTY 拥有 /shell：命令以捕获模式执行并提交为一个
				// Scene 命令 cell，输出经 post-commit 发送效应作为独立 turn
				// 分享给 AI（与 /shell 斜杠命令同一入口）。
				dispatchChatCommand(session, "/shell "+strings.TrimPrefix(input, "!"), noInteractive)
				continue
			}

			result, err := executeShellCommandDetailed(session, input)
			if err != nil {
				// 检查是否是用户中断
				if session.IsInterrupted() {
					continue
				}
				if session != nil && session.NoInteractive {
					exitCommandError("chat", session.OutputFormat, fmt.Errorf("操作错误: %w", err), nil)
				}
				if session != nil && session.Interaction != nil {
					session.Interaction.RenderError(err)
				} else if !unifiedInteractiveOutputMustFailClosed(session) {
					ui.PrintError("操作错误: %v", err)
				}
				continue
			}

			// 将命令输出作为消息发送给 AI
			aiInput := buildShellCommandAIInput(result)
			response, err := sendMessage(session, aiInput)
			if err != nil {
				if session != nil && session.NoInteractive {
					exitCommandError("chat", session.OutputFormat, fmt.Errorf("操作错误: %w", err), nil)
				}
				if session != nil && session.Interaction != nil {
					session.Interaction.RenderError(err)
				} else if !unifiedInteractiveOutputMustFailClosed(session) {
					ui.PrintError("操作错误: %v", err)
				}
				continue
			}
			finishSuccessfulChatSend(session, response, noInteractive)
			continue
		}

		// 处理命令
		if strings.HasPrefix(input, "/") {
			if !chatInputCommandAllowed(session, input) {
				if session != nil && session.Interaction != nil {
					session.Interaction.RenderLocalSupplement("[input] 当前状态不是 Ready，暂不接受 slash 命令；可等待 Ready 后重试，或连续按两次 Ctrl+C 中断/退出。")
				}
				continue
			}
			if dispatchChatCommand(session, input, noInteractive) {
				break
			}
			if noInteractive {
				break
			}
			continue
		}

		if input == "" && !noInteractive {
			continue
		}

		// 在发送消息前检查中断状态（用户可能在等待输入的过程中按了 Ctrl+C）
		if session.IsInterrupted() {
			continue
		}

		renderSubmittedUserInputEcho(session, input)

		// 发送消息
		response, err := sendMessage(session, input)
		if err != nil {
			interrupted := session.IsInterrupted()
			rememberChatTurnRecovery(session, input, interrupted)
			// 检查是否是用户中断
			if interrupted {
				// 用户中断，直接继续到下一次循环（不打印错误）
				renderChatTurnRecoveryHintForError(session, err)
				continue
			}
			// 其他错误，打印错误信息
			if session != nil && session.NoInteractive {
				exitCommandError("chat", session.OutputFormat, fmt.Errorf("操作错误: %w", err), nil)
			}
			if session != nil && session.Interaction != nil {
				session.Interaction.RenderError(err)
			} else if !unifiedInteractiveOutputMustFailClosed(session) {
				ui.PrintError("操作错误: %v", err)
			}
			renderChatTurnRecoveryHintForError(session, err)
			continue
		}

		finishSuccessfulChatSend(session, response, noInteractive)

		// 非交互模式下，发送一条消息后退出
		if noInteractive {
			break
		}
	}
}
