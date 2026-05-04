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
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const chatSessionMetaLabelWidth = 18

// ChatSession 聊天会话状态
type ChatSession struct {
	ProviderName                    string
	Provider                        config.Provider
	Adapter                         adapter.ProtocolAdapter
	Model                           string
	ReasoningEffort                 string
	DisableTools                    bool
	HTTPDebug                       bool
	Stream                          bool
	BaseURL                         string
	Messages                        []runtimetypes.Message
	HTTPClient                      *http.Client
	cancelCtx                       context.Context                    // 可取消的上下文
	cancelFunc                      context.CancelFunc                 // 取消函数
	interrupted                     atomic.Bool                        // 是否被中断（原子操作，避免竞态）
	FunctionCatalog                 *aicliFunctionCatalog              // 统一管理 builtin tools + skills + schema cache
	FunctionRegistry                *functions.FunctionRegistry        // Function 注册表
	FunctionBuilder                 functions.FunctionCallBuilder      // 协议对应的 function/tool builder
	BuiltinSchemas                  []map[string]interface{}           // 预构建的非 skill function schemas
	Logger                          *ChatLogger                        // 聊天日志记录器
	Formatter                       *formatter.MarkdownFormatter       // Markdown 格式化器
	Layout                          *ui.Layout                         // 屏幕布局
	InputBox                        *ui.InputBox                       // 输入框
	TokenCount                      int                                // 当前会话累计的真实 LLM API token 使用量，用于 /status 的 Token usage
	ContextTokenCount               int                                // 当前活跃上下文的 token 快照，用于 ctx used 与 compact 观察值
	ContextWindowTokenCount         int                                // 当前模型上下文窗口大小
	TurnContextTokenCount           int                                // 当前 turn 内请求上下文 token 诊断累计，仅用于调试
	providerContextTokenCount       int                                // provider usage 返回的当前活跃上下文快照，等待 runtime history 同步后保留
	providerContextWindowTokenCount int                                // provider usage 对应的上下文窗口大小
	MsgCount                        int                                // 消息计数
	TurnRequestCount                int                                // 当前 turn 内的请求计数
	SessionManager                  *runtimechat.SessionManager        // 持久化会话管理器
	RuntimeSession                  *runtimechat.Session               // 当前持久化会话
	SessionUserID                   string                             // 当前会话所属用户
	SessionDir                      string                             // 会话存储目录
	SessionFilter                   ChatSessionListFilter              // 会话列表筛选条件
	NoInteractive                   bool                               // 是否为非交互模式
	JSONOutput                      bool                               // 是否输出 JSON
	JSONEnvelope                    bool                               // JSON 输出是否使用 envelope
	KeyHandler                      *ui.KeyHandler                     // 键盘事件处理器（ESC 键中断）
	MCPEnabled                      bool                               // 是否启用 MCP
	MCPStatus                       *MCPStatus                         // MCP 状态
	SkillsBinding                   *skillsRuntimeBinding              // Skills 运行时绑定
	SkillsMode                      string                             // Skills 暴露模式
	SkillsDebug                     bool                               // Skills 调试输出
	Config                          *config.Config                     // 载入的 aicli 全局配置，用于偏好持久化与 provider/model 解析
	RetryConfig                     RetryConfig                        // 重试配置
	RequestTimeout                  time.Duration                      // 请求超时（0 表示不设置）
	OutputFormat                    string                             // 输出格式（interactive|text|json）
	InputReader                     *bufio.Reader                      // 共享 stdin reader，避免交互阶段重复缓冲吞掉后续输入
	InputQueue                      *chatInputQueue                    // interactive line queue fed by stdin pump
	ProfileName                     string                             // 当前 profile 名称
	ProfileAgent                    string                             // 当前 profile agent
	ProfileRoot                     string                             // 当前 profile 根目录
	SystemPromptText                string                             // 组合后的系统提示
	RuntimeConfigPath               string                             // 解析后的 runtime 配置路径
	MCPConfigPath                   string                             // 解析后的 MCP 配置路径
	ResolvedSkillDirs               []string                           // 解析后的 skills 目录
	ProfileContext                  map[string]interface{}             // profile 提供的只读运行时上下文
	ToolPolicy                      *runtimepolicy.ToolExecutionPolicy // profile 解析后的工具策略
	PermissionMode                  runtimepolicy.Mode                 // actor/team run permission mode
	ApprovalReuseMode               chatApprovalReuseMode              // local actor/team approval reuse policy
	ActiveTeam                      *chatTeamBinding                   // ambient team binding across turns
	RuntimeEventBridge              *chatRuntimeEventBridge            // actor runtime event bridge
	ActorFirstReady                 bool                               // actor-first executor established for this session
	ChatExecutor                    aicliChatExecutor                  // shared chatcore-backed chat executor
	LocalRuntimeHost                *localChatRuntimeHost              // actor-first local runtime host
	actorWarmupMu                   sync.Mutex
	actorWarmup                     *chatActorWarmup
	Interaction                     *chatInteractionCoordinator // unified interactive stdout/prompt coordinator
	Surface                         *ui.FixedBottomSurface      // optional fixed-bottom terminal surface
	runtimeHTTPCapture              *chatRuntimeHTTPCapture     // recent runtime HTTP response diagnostics
	localShellArtifactMu            sync.Mutex
	localShellArtifactCounter       int
	lastLocalShellArtifactPath      string
	queuedInputDrain                bool     // suppress repeated queued-input notices while draining
	ImagePaths                      []string // explicit local image attachments for current turn
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
	if s == nil {
		return
	}
	if s.cancelFunc != nil {
		s.cancelFunc()
	}
	// 中断语义是“取消当前输入/当前轮次”，因此需要同时清掉尚未提交的输入草稿
	// 和已渲染但尚未重绘的 prompt 状态，避免下一轮仍被旧状态挡住。
	_ = discardPendingInteractiveInput(s)
	if s.Interaction != nil {
		s.Interaction.ResetPromptState()
	}
	s.interrupted.Store(true)
}

// ResetInterrupt 重置中断状态
func (s *ChatSession) ResetInterrupt() {
	s.interrupted.Store(false)
}

// IsInterrupted 检查是否被中断
// 优先检查原子标志（由信号处理器设置），再检查 cancelCtx 状态作为回退
func (s *ChatSession) IsInterrupted() bool {
	if s.interrupted.Load() {
		return true
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
	opts, err := parseChatCommandOptions(cmd, cfg)
	if err != nil {
		exitCommandError("chat", "json", err, nil)
	}

	if restoreLogger := suppressChatConsoleLogger(cfg, opts); restoreLogger != nil {
		defer restoreLogger()
	}

	profileState, err := resolveChatProfileState(cfg, opts)
	if err != nil {
		exitCommandError("chat", opts.OutputFormat, err, nil)
	}
	applyProfileDefaultsToChatOptions(opts, profileState)

	persistenceState, err := prepareChatPersistence(opts)
	if err != nil {
		exitCommandError("chat", opts.OutputFormat, err, nil)
	}
	if persistenceState.runtimeSessionManager != nil {
		defer persistenceState.runtimeSessionManager.Stop()
	}

	if opts.ListSessionsFlag {
		if err := printChatSessionSummaries(persistenceState.runtimeSessionManager, persistenceState.sessionUserID, "", opts.SessionFilter); err != nil {
			exitCommandError("chat", opts.OutputFormat, err, nil)
		}
		return
	}

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

	session, cleanupSession, err := bootstrapChatSession(cfg, opts, profileState, persistenceState, runtimeState)
	if err != nil {
		exitCommandError("chat", opts.OutputFormat, err, nil)
	}
	persistChatStartupPreferences(cfg, opts, persistenceState.loadedRuntimeSession, runtimeState)
	finalCleanup := buildChatFinalCleanup(session, cleanupSession)
	registerExitCleanup(finalCleanup)
	defer runExitCleanup()

	if shouldShowChatSessionStartupPreamble(opts) {
		presentChatSession(session)
		if persistenceState.loadedRuntimeSession != nil && shouldPrintChatSessionPreamble(session) && hasVisibleChatHistory(session) {
			beginDirectInteractiveOutput(session)
			fmt.Println()
			printVisibleChatHistory(session, "已加载历史会话")
		}
	}

	// 开始聊天循环
	runChatLoop(session, opts.NoInteractive, opts.Message)
	finalizeChatSession(session)
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

	runtimeManager := runtimecfg.NewRuntimeManager(configPath)
	if err := runtimeManager.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 加载 runtime tools 配置失败，已退回默认 sandbox 配置: %v\n", err)
		logpkg.Warnf("AICLI runtime tools config load failed: %v", err)
		return runtimecfg.DefaultRuntimeConfig()
	}
	resolved := runtimeManager.Get()
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
	theme := ui.GetTheme(ui.ThemeAuto)

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
		if len(p) > maxNameLen {
			maxNameLen = len(p)
		}
	}

	for i, p := range providers {
		summary := ""
		if provider, ok := cfg.Providers.Items[p]; ok {
			summary = describeProviderSelection(provider)
		}
		if p == cfg.Providers.DefaultProvider {
			if summary != "" {
				printChatSelectionLine("  [%d] %-*s  %s %s", i+1, maxNameLen, p, theme.Dimmed(summary), theme.Dimmed("(默认)"))
			} else {
				printChatSelectionLine("  [%d] %-*s  %s", i+1, maxNameLen, p, theme.Dimmed("(默认)"))
			}
		} else {
			if summary != "" {
				printChatSelectionLine("  [%d] %-*s  %s", i+1, maxNameLen, p, theme.Dimmed(summary))
			} else {
				printChatSelectionLine("  [%d] %s", i+1, p)
			}
		}
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
			printChatSelectionLine("  [%d] %s %s", i+1, m, ui.GetTheme(ui.ThemeAuto).Dimmed("(默认)"))
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
	printChatSelectionLine("  [2] %s (实时输出) %s", ui.GetTheme(ui.ThemeAuto).SuccessColor.Sprint("流式"), ui.GetTheme(ui.ThemeAuto).Dimmed("(默认)"))
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
	if reasoningEffort := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort); reasoningEffort != "" {
		printChatSessionMetaRow("Reasoning Effort:", reasoningEffort)
	}
	if session.LocalRuntimeHost != nil {
		printChatSessionMetaRow("Permission Mode:", string(session.PermissionMode))
		printChatSessionMetaRow("Approval Reuse:", formatChatApprovalReuseMode(session.ApprovalReuseMode))
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
}

func printChatSessionMetaRow(label, value string) {
	if strings.TrimSpace(label) == "" {
		return
	}
	theme := ui.GetTheme(ui.ThemeAuto)
	fmt.Printf("%-*s %s\n", chatSessionMetaLabelWidth, theme.ColorizeLabel(label), theme.ColorizeSecondary(value))
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
	_, ok := session.ChatExecutor.(*aicliActorChatExecutor)
	return ok
}

func wasInteractiveActorResponseAlreadyRendered(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput || session.RuntimeEventBridge == nil {
		return false
	}
	_, ok := session.ChatExecutor.(*aicliActorChatExecutor)
	return ok && session.RuntimeEventBridge.HasRenderedAssistantFinal()
}

func finalizeInteractiveActorStreamIfNeeded(session *ChatSession, response string) bool {
	if session == nil || session.NoInteractive || session.JSONOutput || !session.Stream || session.RuntimeEventBridge == nil {
		return false
	}
	if _, ok := session.ChatExecutor.(*aicliActorChatExecutor); !ok {
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
		session.RuntimeEventBridge.MarkAssistantFinalRendered()
	}
	return completed
}

func shouldPrintChatSessionPreamble(session *ChatSession) bool {
	return session != nil && !session.NoInteractive && !session.JSONOutput
}

type chatResponsePayload struct {
	Response                   string `json:"response"`
	Provider                   string `json:"provider,omitempty"`
	Protocol                   string `json:"protocol,omitempty"`
	Model                      string `json:"model,omitempty"`
	Stream                     bool   `json:"stream"`
	SessionID                  string `json:"session_id,omitempty"`
	SessionPath                string `json:"session_path,omitempty"`
	SessionStore               string `json:"session_store,omitempty"`
	SessionState               string `json:"session_state,omitempty"`
	QueuedInputCount           int    `json:"queued_input_count,omitempty"`
	QueuedInputDraining        bool   `json:"queued_input_draining,omitempty"`
	ReasoningEffort            string `json:"reasoning_effort,omitempty"`
	TotalTokens                int    `json:"total_tokens,omitempty"`
	ResponseTimeMs             int64  `json:"average_response_time_ms,omitempty"`
	LogPath                    string `json:"log_path,omitempty"`
	DebugLogPath               string `json:"debug_log_path,omitempty"`
	HTTPArtifactDir            string `json:"http_artifact_dir,omitempty"`
	LastHTTPRequestPath        string `json:"last_http_request_path,omitempty"`
	LastHTTPResponsePath       string `json:"last_http_response_path,omitempty"`
	LocalShellArtifactDir      string `json:"local_shell_artifact_dir,omitempty"`
	LastLocalShellArtifactPath string `json:"last_local_shell_artifact_path,omitempty"`
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
	payload.ReasoningEffort = runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
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
		session.Interaction.RenderAssistant(response)
		return
	}
	ui.DisplayAssistantMessage(session.Formatter.Format(response))
}

// runChatLoop 运行聊天循环
func runChatLoop(session *ChatSession, noInteractive bool, initialMessage string) {
	if !noInteractive {
		if shouldUseInteractiveLineEditor(session) {
			// 交互 TTY 场景使用逐键 line editor，不再走按行队列。
			session.InputQueue = nil
		} else {
			ensureChatInputQueue(session)
		}
	}

	// 设置信号处理（平台特定：Unix 支持 Ctrl+C Ctrl+Break ESC; Windows 仅 Ctrl+C）
	sigChan := make(chan os.Signal, 1)
	sigCountChan := make(chan int, 1)
	var shouldExit atomic.Bool
	setupSignalHandler(session, sigChan, sigCountChan, &shouldExit)

	// 监听二次 Ctrl+C，触发优雅退出
	go func() {
		for range sigCountChan {
		}
		shouldExit.Store(true)
	}()

	// 聊天循环
	for {
		// 检查二次终止信号
		if shouldExit.Load() {
			beginDirectInteractiveOutput(session)
			fmt.Println()
			break
		}

		// 重置中断状态（新的输入开始）
		session.ResetInterrupt()
		// 创建新的可取消上下文用于本次操作
		if session.cancelFunc != nil {
			session.cancelFunc()
		}
		session.cancelCtx, session.cancelFunc = context.WithCancel(context.Background())
		if shouldExit.Load() {
			beginDirectInteractiveOutput(session)
			fmt.Println()
			break
		}

		var input string
		var err error

		// 非交互模式下使用初始消息
		if noInteractive && initialMessage != "" {
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
					} else {
						ui.PrintError("操作错误: %v", err)
					}
					continue
				}
				if shouldExit.Load() {
					beginDirectInteractiveOutput(session)
					fmt.Println()
					break
				}
				if notice != "" {
					if session.Interaction != nil {
						session.Interaction.RenderAsyncLine(notice)
					} else {
						fmt.Println(notice)
					}
				}
				if shouldExit.Load() {
					beginDirectInteractiveOutput(session)
					fmt.Println()
					break
				}
				if showPrompt {
					if session.Interaction != nil {
						session.Interaction.PrintPrompt()
					} else {
						fmt.Print(ui.FormatUserPromptWithAttachments(len(session.ImagePaths)))
					}
				}
				if shouldExit.Load() {
					beginDirectInteractiveOutput(session)
					fmt.Println()
					break
				}
			}

			input, err = chatInteractiveReadLine(session, session.cancelCtx)
			if session.Interaction != nil {
				session.Interaction.ClearPrompt()
			}
			if err != nil {
				if errors.Is(err, ui.ErrInteractiveInputExitRequested) {
					beginDirectInteractiveOutput(session)
					fmt.Println("正在退出...")
					break
				}
				// Ctrl+D (EOF)：交互行编辑器场景静默忽略；队列/普通 reader 场景在输入结束后退出循环，避免空转。
				if errors.Is(err, io.EOF) {
					if !shouldUseInteractiveLineEditor(session) {
						beginDirectInteractiveOutput(session)
						fmt.Println()
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
				beginDirectInteractiveOutput(session)
				fmt.Println("\n" + ui.FormatErrorMessage("读取输入失败"))
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
				} else {
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
				} else {
					ui.PrintError("操作错误: %v", err)
				}
				continue
			}
			handledByStreamFinalize := finalizeInteractiveActorStreamIfNeeded(session, response)
			if shouldDisplayFinalResponse(session, response) && !handledByStreamFinalize && !wasInteractiveActorResponseAlreadyRendered(session) {
				renderChatResponse(session, response)
			}
			// 消息发送成功后清空已使用的图片附件
			session.ImagePaths = nil
			// 实时保存会话日志
			if session.Logger.logDir != "" {
				if err := session.Logger.FlushSession(); err != nil {
					writeChatLogSaveError(session, err)
				}
			} else {
				writeChatLogBufferedMarker(session)
			}
			continue
		}

		// 处理命令
		if strings.HasPrefix(input, "/") {
			if handleCommand(session, input, noInteractive) {
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

		// 发送消息
		response, err := sendMessage(session, input)
		if err != nil {
			// 检查是否是用户中断
			if session.IsInterrupted() {
				// 用户中断，直接继续到下一次循环（不打印错误）
				continue
			}
			// 其他错误，打印错误信息
			if session != nil && session.NoInteractive {
				exitCommandError("chat", session.OutputFormat, fmt.Errorf("操作错误: %w", err), nil)
			}
			if session != nil && session.Interaction != nil {
				session.Interaction.RenderError(err)
			} else {
				ui.PrintError("操作错误: %v", err)
			}
			continue
		}

		// 非流式模式：显示完整响应
		// 流式模式：内容已在 sendMessage 中实时打印
		handledByStreamFinalize := finalizeInteractiveActorStreamIfNeeded(session, response)
		if shouldDisplayFinalResponse(session, response) && !handledByStreamFinalize && !wasInteractiveActorResponseAlreadyRendered(session) {
			renderChatResponse(session, response)
		} else if session.Stream && !noInteractive && !handledByStreamFinalize {
			// 流式模式下添加换行
			beginDirectInteractiveOutput(session)
			fmt.Println()
		}

		// 消息发送成功后清空已使用的图片附件
		session.ImagePaths = nil

		// 实时保存会话日志
		if session.Logger.logDir != "" {
			if err := session.Logger.FlushSession(); err != nil {
				writeChatLogSaveError(session, err)
			}
		} else {
			writeChatLogBufferedMarker(session)
		}

		// 非交互模式下，发送一条消息后退出
		if noInteractive {
			break
		}
	}
}
