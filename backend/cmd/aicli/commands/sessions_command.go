package commands

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// aicli sessions 退出码约定（与 aicli export / import / stats 对齐）：
//
//	0 = 列出成功（含 0 条结果）
//	1 = 参数错误（未知输出格式、负 limit、多余位置参数）
//	2 = 确定性错误（会话存储不可读、工作目录解析失败）
const (
	sessionsExitOK      = 0
	sessionsExitUsage   = 1
	sessionsExitFailure = 2
)

// sessionsExitHook 是退出钩子：生产路径为 os.Exit，测试注入以断言退出码。
var sessionsExitHook = os.Exit

func sessionsExit(code int) {
	if code == sessionsExitOK {
		return
	}
	runExitCleanup()
	sessionsExitHook(code)
}

// sessionsDefaultLimit 与 chat 的 --session-limit 默认值保持一致（20 条）。
const sessionsDefaultLimit = 20

// sessionsUnlimitedLimit 用于 --limit 0（不限制）：沿用 listFilteredChatSessions
// 的分页扫描语义，只把“无上限”表达成一个不可能触顶的上界。
const sessionsUnlimitedLimit = 1_000_000

const sessionsCommandLongHelp = `列出 chat 会话（默认仅当前工作目录）。

这是 aicli resume --list-sessions 的独立等价入口：只读会话库并打印列表，
不启动 provider / actor / 工具面，也不会进入交互式对话。

范围：
  默认只列「当前工作目录」的会话（口径与 chat --resume 一致：runtime.yaml 的
  workspace.root 优先，否则取进程 cwd）；--all 列出所有工作目录的会话。

筛选：
  --query <text>            按会话 ID/标题/摘要/provider/model/工作目录模糊筛选
  --state <s>               按状态筛选（active|idle|closed|archived）
  --provider <p>            按 provider 名称筛选
  --model <m>               按模型名称筛选
  --limit <n>               最大条数（默认 20；0 = 不限制）

存储与身份（与 resume/export 同一套解析）：
  --session-dir <dir>       会话持久化目录（默认: ~/.aicli/sessions）
  --user <id>               会话用户 ID（优先于 AICLI_SESSION_USER 与 runtime
                            sessions.defaultUserId）

结构化输出：
  --json 或 --output json   输出机器可读列表；全局 --envelope 会额外包成
                            {ok,command,data}。失败时同样给出
                            {ok:false,command:"sessions",error}。

退出码：
  0 = 列出成功（含 0 条结果）
  1 = 参数错误
  2 = 确定性错误（会话存储不可读、工作目录解析失败）`

const sessionsCommandExampleHelp = `  aicli sessions                       # 当前工作目录的会话
  aicli sessions --all                 # 所有工作目录的会话
  aicli sessions --query review --limit 50
  aicli sessions --state active --json
  aicli sessions --all --session-dir ./sessions --user alice`

// NewSessionsCommand 创建 `aicli sessions`：把会话列表从 chat 引导路径里独立
// 出来（等价 aicli resume --list-sessions），默认只看当前工作目录，--all 看全部。
func NewSessionsCommand(getConfig func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sessions",
		Short:   "列出 chat 会话（默认当前工作目录，--all 列出全部）",
		Long:    sessionsCommandLongHelp,
		Example: sessionsCommandExampleHelp,
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			sessionsExit(runSessionsCommand(cmd, getConfig))
		},
	}
	cmd.Flags().Bool("all", false, "列出所有工作目录的会话（默认仅当前工作目录）")
	cmd.Flags().String("query", "", "按会话 ID/标题/摘要/provider/model/工作目录模糊筛选")
	cmd.Flags().String("state", "", "按会话状态筛选（active|idle|closed|archived）")
	cmd.Flags().String("provider", "", "按 provider 名称筛选")
	cmd.Flags().String("model", "", "按模型名称筛选")
	cmd.Flags().Int("limit", sessionsDefaultLimit, "最大条数（0 = 不限制）")
	cmd.Flags().String("session-dir", "", "会话持久化目录（默认: ~/.aicli/sessions）")
	cmd.Flags().String("user", "", "会话用户 ID（优先于 AICLI_SESSION_USER 与 runtime sessions.defaultUserId）")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 格式输出会话列表（等价于 --output json）")
	cmd.Flags().Bool("envelope", false, "JSON 输出使用 {ok,command,data} 包装")
	return cmd
}

// sessionsCommandRequest 是 `aicli sessions` 的解析结果（可测试，不依赖 cobra）。
type sessionsCommandRequest struct {
	// All 列出全部工作目录；false 时按当前工作目录过滤。
	All        bool
	State      string
	Provider   string
	Model      string
	Query      string
	Limit      int
	SessionDir string
	User       string
	Output     structuredOutputOptions
}

// sessionsCommandItem 是列表里的单条会话（文本行与 --json 同源）。
type sessionsCommandItem struct {
	SessionID    string    `json:"session_id"`
	Title        string    `json:"title,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	State        string    `json:"state,omitempty"`
	Protocol     string    `json:"protocol,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Model        string    `json:"model,omitempty"`
	Workspace    string    `json:"workspace,omitempty"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// sessionsCommandResult 是 `aicli sessions` 的完整结果：既用于文本头部，也用于 --json。
type sessionsCommandResult struct {
	// Scope 为 workspace（默认，当前工作目录）或 all（--all）。
	Scope      string                `json:"scope"`
	Workspace  string                `json:"workspace,omitempty"`
	UserID     string                `json:"user_id"`
	SessionDir string                `json:"session_dir,omitempty"`
	Count      int                   `json:"count"`
	Sessions   []sessionsCommandItem `json:"sessions"`
}

// sessionsCurrentWorkspace 解析「当前工作目录」口径（与 chat --resume 同一套：
// runtime.yaml 的 workspace.root 优先，其次进程 cwd）。变量化是为了让测试不依赖
// 进程 cwd 与用户级 runtime.yaml 的 workspace.root。
var sessionsCurrentWorkspace = func(cfg *config.Config) (string, error) {
	if workspace := strings.TrimSpace(resolveLocalWorkspacePath(loadRuntimeToolConfig(cfg, nil), nil)); workspace != "" {
		return workspace, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("获取当前工作目录失败: %w", err)
	}
	return cwd, nil
}

func parseSessionsCommandRequest(cmd *cobra.Command) (sessionsCommandRequest, error) {
	if cmd == nil {
		return sessionsCommandRequest{}, fmt.Errorf("sessions command is nil")
	}
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		return sessionsCommandRequest{}, err
	}

	req := sessionsCommandRequest{Output: outputOptions}
	req.All, _ = cmd.Flags().GetBool("all")
	req.State, _ = cmd.Flags().GetString("state")
	req.Provider, _ = cmd.Flags().GetString("provider")
	req.Model, _ = cmd.Flags().GetString("model")
	req.Query, _ = cmd.Flags().GetString("query")
	req.Limit, _ = cmd.Flags().GetInt("limit")
	req.SessionDir, _ = cmd.Flags().GetString("session-dir")
	req.User, _ = cmd.Flags().GetString("user")

	if req.Limit < 0 {
		return sessionsCommandRequest{}, fmt.Errorf("--limit 不能为负数（0 = 不限制）")
	}
	return req, nil
}

func runSessionsCommand(cmd *cobra.Command, getConfig func() *config.Config) int {
	req, err := parseSessionsCommandRequest(cmd)
	if err != nil {
		printSessionsCommandError(err, structuredOutputOptions{Format: "text"})
		return sessionsExitUsage
	}

	var cfg *config.Config
	if getConfig != nil {
		cfg = getConfig()
	}

	result, sessions, err := collectSessionsCommandResult(cfg, req)
	if err != nil {
		printSessionsCommandError(err, req.Output)
		return sessionsExitFailure
	}

	if isJSONOutputFormat(req.Output.Format) {
		printCommandJSONOutput("sessions", req.Output.Envelope, result)
		return sessionsExitOK
	}
	renderSessionsCommandText(result, sessions)
	return sessionsExitOK
}

// collectSessionsCommandResult 打开会话库、应用范围与筛选并投影结果；只读不打印，
// 便于测试直接断言「默认当前工作目录 / --all 全部」而不解析 stdout。
func collectSessionsCommandResult(cfg *config.Config, req sessionsCommandRequest) (sessionsCommandResult, []*runtimechat.Session, error) {
	workspace := ""
	if !req.All {
		resolved, err := sessionsCurrentWorkspace(cfg)
		if err != nil {
			return sessionsCommandResult{}, nil, err
		}
		workspace = resolved
	}

	// 与 resume/export 共用同一套会话存储解析：这里不需要 provider/adapter/
	// actor/工具面，只打开会话库做只读列表。
	runtimeConfig, runtimeConfigPath := loadChatPersistenceRuntimeConfig(cfg, nil)
	manager, userID, sessionDir, err := newChatSessionManagerWithRuntimeConfig(req.SessionDir, runtimeConfig, runtimeConfigPath, req.User)
	if err != nil {
		return sessionsCommandResult{}, nil, fmt.Errorf("初始化会话存储失败: %w", err)
	}
	defer manager.Stop()

	limit := req.Limit
	if limit <= 0 {
		limit = sessionsUnlimitedLimit
	}
	filter := ChatSessionListFilter{
		State:     runtimechat.SessionState(strings.ToLower(strings.TrimSpace(req.State))),
		Provider:  strings.TrimSpace(req.Provider),
		Model:     strings.TrimSpace(req.Model),
		Query:     strings.TrimSpace(req.Query),
		Workspace: workspace,
		Limit:     limit,
	}
	sessions, err := listFilteredChatSessions(manager, userID, filter)
	if err != nil {
		return sessionsCommandResult{}, nil, fmt.Errorf("列出会话失败: %w", err)
	}

	result := sessionsCommandResult{
		Scope:      "all",
		UserID:     userID,
		SessionDir: sessionDir,
	}
	if !req.All {
		result.Scope = "workspace"
		result.Workspace = workspace
	}
	result.Sessions = buildSessionsCommandItems(sessions)
	result.Count = len(result.Sessions)
	return result, sessions, nil
}

// buildSessionsCommandItems 把会话投影成 --json 结构；文本行仍走
// renderRuntimeSessionSummaryLines，两条输出同源（同一批会话、同一批消息数）。
//
// 只发布 message_count 而不发布「轮次」：列表行是元数据加载（不读历史），
// SQLite/file 后端下按 user 消息统计的轮次恒为 0，发布它只会误导脚本。
func buildSessionsCommandItems(sessions []*runtimechat.Session) []sessionsCommandItem {
	items := make([]sessionsCommandItem, 0, len(sessions))
	for _, session := range sessions {
		if session == nil {
			continue
		}
		item := sessionsCommandItem{
			SessionID:    strings.TrimSpace(session.ID),
			State:        string(session.State),
			Protocol:     strings.TrimSpace(runtimeSessionContextString(session, chatRuntimeContextProtocol)),
			Provider:     strings.TrimSpace(runtimeSessionContextString(session, chatRuntimeContextProviderName)),
			Model:        strings.TrimSpace(runtimeSessionContextString(session, chatRuntimeContextModel)),
			Workspace:    runtimeSessionWorkspacePath(session),
			MessageCount: session.MessageCount(),
			CreatedAt:    session.CreatedAt,
			UpdatedAt:    session.UpdatedAt,
		}
		if preview := session.BuildPreview(); preview != nil {
			item.Title = strings.TrimSpace(preview.Title)
			item.Summary = strings.TrimSpace(preview.Summary)
		}
		items = append(items, item)
	}
	return items
}

func renderSessionsCommandText(result sessionsCommandResult, sessions []*runtimechat.Session) {
	if len(sessions) == 0 {
		if result.Scope == "workspace" {
			fmt.Println("当前工作目录暂无可用会话（--all 可查看全部工作目录）")
			return
		}
		fmt.Println("暂无可用会话")
		return
	}

	if result.Scope == "workspace" {
		fmt.Printf("可用会话（当前工作目录: %s）: %d 条\n", result.Workspace, len(sessions))
	} else {
		fmt.Printf("可用会话（全部工作目录）: %d 条\n", len(sessions))
	}
	now := time.Now()
	for _, session := range sessions {
		if session == nil {
			continue
		}
		id := strings.TrimSpace(session.ID)
		if id == "" {
			continue
		}
		// 控制台每条会话只占一行，只保留 4 个关键字段：时间 / id / 目录 / 简要。
		// 时间在最前便于按新旧扫读，id 紧随其后便于直接复制，目录紧跟 id；
		// 其余诊断字段（状态、协议、provider/model、消息数、摘要全文）走 --json。
		row := strings.Join([]string{
			formatSessionUpdatedAt(session.UpdatedAt, now),
			id,
			blankToDash(runtimeSessionWorkspacePath(session)),
			sessionsCommandBrief(session),
		}, " · ")
		// 整行不按终端宽度截断：4 个字段都必须留下（截断会把目录静默吞掉）；
		// 「简要」已在 sessionsCommandBrief 内限量，超长行交给终端自行折行。
		fmt.Println("  " + row)
	}
}

// sessionsCommandBriefMaxWidth 限制「简要」的显示宽度：保证同一行里还能看到
// 时间与目录（宽度按显示格数计，中英文混排不会错位）。
const sessionsCommandBriefMaxWidth = 60

// sessionsCommandBrief 取一行式会话简要：优先持久化标题，其次摘要，最后占位。
func sessionsCommandBrief(session *runtimechat.Session) string {
	if session == nil {
		return "(untitled)"
	}
	if preview := session.BuildPreview(); preview != nil {
		if title := strings.TrimSpace(preview.Title); title != "" {
			return fitDisplayText(title, sessionsCommandBriefMaxWidth)
		}
		if summary := strings.TrimSpace(preview.Summary); summary != "" {
			return fitDisplayText(summary, sessionsCommandBriefMaxWidth)
		}
	}
	return "(untitled)"
}

func printSessionsCommandError(err error, outputOptions structuredOutputOptions) {
	if err == nil {
		return
	}
	if isJSONOutputFormat(outputOptions.Format) {
		emitCommandError("sessions", outputOptions.Format, err, nil)
		return
	}
	fmt.Fprintf(os.Stderr, "错误: %v\n", err)
}
