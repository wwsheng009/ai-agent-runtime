package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// ChatCommandLongHelp is the long help text for `aicli chat`.
const ChatCommandLongHelp = `与 AI 模型进行交互式对话。

进入 chat 后可使用斜杠命令：
  - /help
  - /model [status|model|--provider ...]
  - /login ...
  - /stream on|off
  - /status /sessions /resume
  - /functions <prompt>
  - /call <function> [args-json]
  - /tool <function> [args-json]
  - /skill <skill> <prompt>
  - /skills [query]

更完整说明见：
  - docs/aicli/quickstart.md
  - docs/aicli/install.md
  - docs/aicli/faq.md
  - docs/aicli/agents.md
  - docs/skill_runtime/aicli_skills_usage.md`

// ChatCommandExampleHelp is the example help text for `aicli chat`.
const ChatCommandExampleHelp = `  aicli chat                              # 交互式聊天
  aicli chat --prompt "检查当前项目"        # 启动后自动提交，并继续留在交互界面
  aicli chat --profile dev                  # 使用命名 profile
  aicli chat --profile ./profiles/dev --agent coder
  aicli chat --agent explore              # portable agentdef（无需 profile）
  aicli chat --provider nvidia            # 指定 provider
  aicli chat --provider nvidia --stream   # 流式输出
  aicli chat --provider codex --fast     # Codex Fast（service_tier=priority）
  aicli chat --resume                     # 恢复当前工作目录的最近会话
  aicli chat --resume --cwd=false         # 跨工作目录恢复最近会话
  aicli chat --session session_xxx        # 加载指定会话
  aicli chat --list-sessions              # 列出当前工作目录的会话
  aicli chat --list-sessions --session-provider nvidia --session-query review
  aicli resume                            # 顶层恢复当前工作目录的最近会话
  aicli resume --cwd=false                # 顶层跨工作目录恢复最近会话
  aicli resume session_xxx                # 顶层加载指定会话
  aicli chat --no-interactive --prompt "Hello"  # 非交互模式
  aicli chat --no-interactive --output json -M "Hello"  # JSON 输出

  # chat 内斜杠命令
  /resume                                 # 默认仅显示当前工作目录的会话
  /functions 帮我生成一张图片
  /call openai_image_generate 帮我生成一张海边日落照片
  /call openai_image_generate {"prompt":"帮我生成一张海边日落照片"}
  /skill imagegen 帮我生成一张海边日落照片
  /skills`

// NewChatCommand builds the interactive chat subcommand.
func NewChatCommand(getCfg func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "chat",
		Short:   "交互式聊天",
		Long:    ChatCommandLongHelp,
		Example: ChatCommandExampleHelp,
		Run: func(cmd *cobra.Command, args []string) {
			HandleChat(cmd, getCfg())
		},
	}
	registerChatFlags(cmd)
	return cmd
}

// registerChatFlags attaches the shared chat/resume flag set to cmd.
// Keep chat and top-level resume flag surfaces in sync so default-arg
// rewriting (`aicli --resume` → `aicli chat --resume`) and explicit
// `aicli resume` share the same option contract.
func registerChatFlags(cmd *cobra.Command) {
	if cmd == nil {
		return
	}
	defaultChatLogDir := ResolveDefaultChatLogDir()
	cmd.Flags().String("profile", "", "profile 名称或目录路径（按 profiles 配置或显式路径解析）")
	cmd.Flags().String("agent", "", "agent 标识：有 profile 时为 profile 内 agent；无 profile 时加载 portable agentdef（builtin/项目 .agents/agents）")
	cmd.Flags().StringP("provider", "p", "", "指定 provider 名称")
	cmd.Flags().StringP("model", "m", "", "指定模型名称")
	cmd.Flags().BoolP("stream", "s", false, "使用流式输出")
	cmd.Flags().Bool("fast", false, "启用 Codex Fast 模式（service_tier=priority；仅 protocol=codex 生效）")
	cmd.Flags().Bool("no-interactive", false, "非交互模式（单次请求）")
	cmd.Flags().Bool("compat-mode", false, "强制兼容模式：不走 TUI，使用原生控制台行输入（无 ANSI 降级路径；Win7 等终端使用）")
	cmd.Flags().String("input-mode", chatConsoleInputAuto, "兼容控制台输入模式（auto|system|custom；auto 优先 ReadConsoleW，system 保留 Win7 中文输入法）")
	cmd.Flags().Bool("debug", false, "输出诊断调试信息到 stderr（兼容控制台输入/重绘/按键等）")
	cmd.Flags().String("output", "", "非交互模式输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "兼容选项：等价于 --output json")
	cmd.Flags().String("render-output-file", "", "将交互会话的终端输出镜像写入指定文件（wire ANSI 字节；console 渲染不变）")
	cmd.Flags().String("prompt", "", "启动后自动提交的消息（交互模式提交后继续留在界面）")
	cmd.Flags().StringP("message", "M", "", "兼容选项：等价于 --prompt")
	cmd.Flags().StringP("log-dir", "", defaultChatLogDir, fmt.Sprintf("保存会话日志到指定目录（默认: %s）", defaultChatLogDir))
	cmd.Flags().String("request-timeout", "", "单次请求超时（例如 60s、2m，留空使用配置）")
	cmd.Flags().String("reasoning-effort", "", "当前模型配置显式支持的 reasoning_effort 值（留空则不注入，由配置和交互流程决定）")
	cmd.Flags().String("runtime-mode", "", "执行宿主模式（local|server|auto；留空使用 aicli.runtime.mode 或 local）")
	cmd.Flags().String("runtime-server", "", "runtime-server 地址或模式别名（server|auto|local|http://127.0.0.1:8101）")
	cmd.Flags().String("session", "", "加载指定 chat 会话 ID")
	cmd.Flags().Bool("resume", false, "恢复最近一次 chat 会话")
	cmd.Flags().Bool("list-sessions", false, "列出当前用户的 chat 会话并退出")
	cmd.Flags().String("session-dir", "", "chat 会话持久化目录（默认: ~/.aicli/sessions）")
	cmd.Flags().String("user", "", "chat 会话用户 ID（优先于 AICLI_SESSION_USER 和 runtime sessions.defaultUserId）")
	cmd.Flags().String("title", "", "设置当前 chat 会话标题")
	cmd.Flags().String("session-state", "", "按会话状态筛选（active|idle|closed|archived）")
	cmd.Flags().String("session-provider", "", "按 provider 名称筛选会话")
	cmd.Flags().String("session-model", "", "按模型名称筛选会话")
	cmd.Flags().Bool("cwd", true, "仅显示并恢复当前工作目录的历史会话（默认启用；使用 --cwd=false 查看全部目录）")
	cmd.Flags().String("session-query", "", "按会话 ID/标题/摘要/provider/model/工作目录模糊筛选")
	cmd.Flags().Int("session-limit", 20, "会话列表和启动选择器的最大展示数量")
	cmd.Flags().Bool("disable-tools", false, "禁用 aicli chat 的 tools/skills 暴露，避免上游 function calling 兼容性问题")
	cmd.Flags().Bool("debug-http", false, "记录 chat 请求的 HTTP 调试信息（重试尝试、状态码、最后响应预览）")
	cmd.Flags().Bool("fail-fast", false, "调试模式：禁用自动重试，首次失败立即返回")
	cmd.Flags().StringSlice("skills-dir", nil, "附加外部 skills 目录（可重复指定），与系统级 skills 一起加载")
	cmd.Flags().Int("skills-top-k", 0, "aicli chat 暴露给模型的候选 skills 数量（0=使用配置默认值）")
	cmd.Flags().String("skills-mode", "auto", "aicli chat 的 skills 暴露模式（auto|prefer|only）")
	cmd.Flags().Bool("skills-debug", false, "打印当前请求的 skill route 候选、暴露结果与模式")
	cmd.Flags().String("permission-mode", "default", "本地 actor/team 运行的权限模式（default|accept_edits|plan|bypass_permissions）")
	cmd.Flags().StringSlice("allow-tool", nil, "允许指定工具（可重复；写入权限规则 allow，并参与工具 allowlist）")
	cmd.Flags().StringSlice("deny-tool", nil, "拒绝指定工具（可重复；硬拒绝，优先于项目 allow 规则）")
	cmd.Flags().Bool("trust", false, "信任当前工作区并允许项目级 plugins/hooks/MCP（写入 durable store；需 AICLI_FOLDER_TRUST=1）")
	cmd.Flags().String("approval-reuse", "session_readonly_shell", "本地 actor/team 审批复用策略（off|session_readonly_shell|team_readonly_shell）")
	cmd.Flags().Bool("yolo", false, "快捷模式：等价于 --permission-mode bypass_permissions")
	cmd.Flags().StringSliceP("image", "i", nil, "附加本地图片文件路径（可重复指定，支持 PNG/JPEG/GIF/WebP）")
}
