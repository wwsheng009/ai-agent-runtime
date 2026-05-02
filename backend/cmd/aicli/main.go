package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// 默认配置文件搜索路径（首个存在即采用），可被 -c/--config 显式覆盖。
// 顺序：
//  1. $HOME/.aicli/config.yaml      —— 用户级全局配置
//  2. <cwd>/.aicli/config.yaml      —— 项目级 .aicli 目录
//  3. <cwd>/aicli.yaml              —— 项目级单文件
//  4. <cwd>/configs/config.yaml     —— 旧版默认路径（向后兼容）
func defaultConfigSearchPaths() []string {
	paths := make([]string, 0, 4)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".aicli", "config.yaml"))
	}
	paths = append(paths,
		filepath.Join(".aicli", "config.yaml"),
		"aicli.yaml",
		filepath.Join("configs", "config.yaml"),
	)
	return paths
}

// resolveConfigPath 在搜索路径中返回首个存在的文件；都不存在时返回空串（由 InitGlobalConfig 容忍）。
func resolveConfigPath(paths []string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

var (
	version     = "dev"
	buildTime   = "unknown"
	cfg         *config.Config
	logFilePath string // AICLI 日志文件路径（命令行覆盖）
)

func main() {
	// 加载 .env 文件（支持多个位置）
	// 优先级：项目根目录 > configs 目录
	envPaths := []string{".env", "./configs/.env"}
	envLoaded := false

	for _, path := range envPaths {
		if err := godotenv.Load(path); err == nil {
			envLoaded = true
			break
		}
	}

	if !envLoaded {
		// .env 文件不存在时不是致命错误，继续运行
		fmt.Fprintf(os.Stderr, "Warning: .env file not found in %v\n", envPaths)
	}

	commands.SetChatStatusBuildInfo(version, buildTime)

	// 创建 root 命令
	rootCmd := &cobra.Command{
		Use:   "aicli [子命令]",
		Short: "AI API Gateway 测试工具",
		Long: `AI CLI 是一个用于测试 AI Gateway 的命令行工具。

功能包括：
  - 列出当前配置信息（providers, provider_groups）
  - 对不同端点进行测试
  - 测试模型的最大上下文窗口和最大生成长度

文档入口：
  - docs/aicli/README.md
  - docs/aicli/install.md
  - docs/skill_runtime/aicli_skills_usage.md`,
		Example: `  # 列出配置信息
  aicli config
  aicli config --provider nvidia
  aicli config --groups

  # 测试端点
  aicli test --model gpt-4 --message "Hello"
  aicli test --provider nvidia --message "测试"
  aicli test --stream

  # 测试上下文窗口
  aicli context --model glm-4.7
  aicli context --provider nvidia --model gpt-4
  aicli context --model gpt-4 --step 5000`,
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
		},
	}

	// 全局 flags
	// 默认空串：未显式 -c 时按 defaultConfigSearchPaths() 顺序查找
	var configPath string
	rootCmd.PersistentFlags().StringP("config", "c", "", "配置文件路径（未指定时按 $HOME/.aicli/config.yaml -> ./.aicli/config.yaml -> ./aicli.yaml -> ./configs/config.yaml 顺序查找）")
	rootCmd.PersistentFlags().StringVarP(&logFilePath, "logfile", "l", "", "日志文件路径（默认使用 aicli.log.file_path 或 log.file_path）")
	rootCmd.PersistentFlags().String("theme", "", "输出主题（classic|focus|contrast|mono，留空使用配置或默认）")
	rootCmd.PersistentFlags().Bool("envelope", false, "JSON 输出时使用统一 envelope 结构（ok/command/data 或 ok/command/error）")

	// 解析配置后初始化
	cobra.OnInitialize(func() {
		// 读取配置文件路径
		cfgFlag, _ := rootCmd.Flags().GetString("config")
		if cfgFlag != "" {
			// 用户显式 -c：使用指定路径，不再回退
			configPath = cfgFlag
		} else {
			// 未显式指定：按搜索顺序找首个存在的配置文件
			configPath = resolveConfigPath(defaultConfigSearchPaths())
		}
		// 加载配置（configPath 为空时 InitGlobalConfig 会返回空配置而不报错）
		loadedConfig, err := config.InitGlobalConfig(configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to load config: %v\n", err)
			os.Exit(1)
		}
		cfg = loadedConfig

		// AICLI 日志路径覆盖（优先级：命令行 > aicli.log.file_path > log.file_path）
		if cfg != nil {
			if cfg.AICLI != nil && cfg.AICLI.Log != nil && cfg.AICLI.Log.FilePath != "" {
				cfg.Log.FilePath = cfg.AICLI.Log.FilePath
			}
			if logFilePath != "" {
				cfg.Log.FilePath = logFilePath
			}
		}

		themeName := ""
		if flagTheme, err := rootCmd.Flags().GetString("theme"); err == nil {
			themeName = strings.TrimSpace(flagTheme)
		}
		if themeName == "" && cfg != nil && cfg.AICLI != nil && cfg.AICLI.Theme != nil {
			themeName = strings.TrimSpace(cfg.AICLI.Theme.Name)
		}
		if themeName != "" {
			if err := ui.SetThemePreset(themeName); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		// 初始化日志系统
		if err := logger.InitLogger(&cfg.Log); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to initialize logger: %v\\n", err)
		}
	})

	// config 子命令
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "列出配置信息",
		Long:  `列出当前配置的 providers 和 provider_groups 信息。`,
		Example: `  aicli config                        # 显示所有配置
  aicli config --provider nvidia       # 只显示指定 provider
  aicli config --groups                # 只显示 provider groups
  aicli config --models                # 列出所有可用模型
  aicli config --output json           # 结构化 JSON 输出`,
		Run: func(cmd *cobra.Command, args []string) {
			commands.HandleConfig(cmd, cfg)
		},
	}
	configCmd.Flags().StringP("provider", "p", "", "指定 provider 名称")
	configCmd.Flags().BoolP("groups", "g", false, "显示 provider groups")
	configCmd.Flags().BoolP("models", "m", false, "列出所有可用模型")
	configCmd.Flags().String("output", "", "输出格式（text|json）")
	configCmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	rootCmd.AddCommand(configCmd)

	// test 子命令
	testCmd := &cobra.Command{
		Use:   "test",
		Short: "测试端点",
		Long:  `向配置的 endpoint 发送测试请求。`,
		Example: `  aicli test --model gpt-4 --message "Hello"
  aicli test --provider nvidia --message "测试"
  aicli test --provider bigmodel --path "/v1/messages" --message "Hello"
  aicli test --stream                                   # 测试流式响应
  aicli test --model gpt-4 --output text               # 只输出结果文本
  aicli test --model gpt-4 --output json               # 输出结构化 JSON`,
		Run: func(cmd *cobra.Command, args []string) {
			commands.HandleTest(cmd, cfg)
		},
	}
	testCmd.Flags().StringP("provider", "p", "", "指定 provider 名称")
	testCmd.Flags().StringP("model", "m", "", "指定模型名称")
	testCmd.Flags().StringP("message", "M", "Hello, how are you?", "测试消息")
	testCmd.Flags().StringP("path", "", "", "API 路径（默认根据 provider 类型决定）")
	testCmd.Flags().IntP("max-tokens", "t", 100, "最大输出 tokens")
	testCmd.Flags().Float64P("temperature", "", 0.7, "温度参数")
	testCmd.Flags().BoolP("stream", "s", false, "使用流式输出")
	testCmd.Flags().String("output", "", "输出格式（text|json|raw|pretty，优先于 --format）")
	testCmd.Flags().BoolP("json", "j", false, "输出完整 JSON 响应")
	testCmd.Flags().StringP("format", "f", "pretty", "输出格式 (pretty|json|raw)")
	testCmd.Flags().IntP("timeout", "", 60, "请求超时时间（秒）")
	testCmd.Flags().StringP("save", "", "", "保存测试数据到指定目录（原始请求和响应）")
	rootCmd.AddCommand(testCmd)

	// context 子命令
	contextCmd := &cobra.Command{
		Use:   "context",
		Short: "测试上下文窗口和最大输出",
		Long:  `测试模型的最大上下文窗口和最大生成长度。`,
		Example: `  aicli context --model glm-4.7
  aicli context --provider nvidia --model gpt-4
  aicli context --model gpt-4 --step 5000
  aicli context --model gpt-4 --max-output-only
  aicli context --model gpt-4 --start 10000 --end 20000
  aicli context --model gpt-4 --output json`,
		Run: func(cmd *cobra.Command, args []string) {
			commands.HandleContext(cmd, cfg)
		},
	}
	contextCmd.Flags().StringP("provider", "p", "", "指定 provider 名称")
	contextCmd.Flags().StringP("model", "m", "", "指定模型名称")
	contextCmd.Flags().IntP("start", "s", 0, "测试起始 token 数")
	contextCmd.Flags().IntP("end", "e", 0, "测试结束 token 数（0=使用 provider 配置的 max_tokens_limit）")
	contextCmd.Flags().IntP("step", "", 1000, "每次测试的步进")
	contextCmd.Flags().BoolP("max-output-only", "o", false, "仅测试最大输出长度")
	contextCmd.Flags().String("output", "", "输出格式（pretty|text|json）")
	contextCmd.Flags().BoolP("json", "j", false, "兼容选项：等价于 --output json")
	contextCmd.Flags().IntP("timeout", "", 60, "单次请求超时时间（秒）")
	contextCmd.Flags().IntP("retries", "r", 3, "失败重试次数")
	rootCmd.AddCommand(contextCmd)

	// chat 子命令
	chatCmd := &cobra.Command{
		Use:   "chat",
		Short: "交互式聊天",
		Long: `与 AI 模型进行交互式对话。

进入 chat 后可使用斜杠命令：
  - /functions <prompt>
  - /call <function> [args-json]
  - /tool <function> [args-json]
  - /skill <skill> <prompt>

更完整说明见：
  - docs/aicli/install.md
  - docs/skill_runtime/aicli_skills_usage.md`,
		Example: `  aicli chat                              # 交互式聊天
  aicli chat --profile dev                  # 使用命名 profile
  aicli chat --profile ./profiles/dev --agent coder
  aicli chat --provider nvidia            # 指定 provider
  aicli chat --provider nvidia --stream   # 流式输出
  aicli chat --resume                     # 恢复最近会话
  aicli chat --session session_xxx        # 加载指定会话
  aicli chat --list-sessions              # 列出会话
  aicli chat --list-sessions --session-provider nvidia --session-query review
  aicli chat --no-interactive --message "Hello"  # 非交互模式
  aicli chat --no-interactive --output json -M "Hello"  # JSON 输出

  # chat 内斜杠命令
  /functions 帮我生成一张图片
  /call openai_image_generate {"prompt":"帮我生成一张海边日落照片"}
  /skill imagegen 帮我生成一张海边日落照片`,
		Run: func(cmd *cobra.Command, args []string) {
			commands.HandleChat(cmd, cfg)
		},
	}
	defaultChatLogDir := commands.ResolveDefaultChatLogDir()
	chatCmd.Flags().String("profile", "", "profile 名称或目录路径（按 profiles 配置或显式路径解析）")
	chatCmd.Flags().String("agent", "", "profile 内 agent 标识（留空时使用 profile.default_agent）")
	chatCmd.Flags().StringP("provider", "p", "", "指定 provider 名称")
	chatCmd.Flags().StringP("model", "m", "", "指定模型名称")
	chatCmd.Flags().BoolP("stream", "s", false, "使用流式输出")
	chatCmd.Flags().Bool("no-interactive", false, "非交互模式（单次请求）")
	chatCmd.Flags().String("output", "", "非交互模式输出格式（text|json）")
	chatCmd.Flags().BoolP("json", "j", false, "兼容选项：等价于 --output json")
	chatCmd.Flags().StringP("message", "M", "", "非交互模式下发送的消息")
	chatCmd.Flags().StringP("log-dir", "", defaultChatLogDir, fmt.Sprintf("保存会话日志到指定目录（默认: %s）", defaultChatLogDir))
	chatCmd.Flags().String("request-timeout", "", "单次请求超时（例如 60s、2m，留空使用配置）")
	chatCmd.Flags().String("reasoning-effort", "", "当前模型配置显式支持的 reasoning_effort 值（留空则不注入，由配置和交互流程决定）")
	chatCmd.Flags().String("session", "", "加载指定 chat 会话 ID")
	chatCmd.Flags().Bool("resume", false, "恢复最近一次 chat 会话")
	chatCmd.Flags().Bool("list-sessions", false, "列出当前用户的 chat 会话并退出")
	chatCmd.Flags().String("session-dir", "", "chat 会话持久化目录（默认: ~/.aicli/sessions）")
	chatCmd.Flags().String("title", "", "设置当前 chat 会话标题")
	chatCmd.Flags().String("session-state", "", "按会话状态筛选（active|idle|closed|archived）")
	chatCmd.Flags().String("session-provider", "", "按 provider 名称筛选会话")
	chatCmd.Flags().String("session-model", "", "按模型名称筛选会话")
	chatCmd.Flags().String("session-query", "", "按会话 ID/标题/摘要/provider/model 模糊筛选")
	chatCmd.Flags().Int("session-limit", 20, "会话列表和启动选择器的最大展示数量")
	chatCmd.Flags().Bool("disable-tools", false, "禁用 aicli chat 的 tools/skills 暴露，避免上游 function calling 兼容性问题")
	chatCmd.Flags().Bool("debug-http", false, "记录 chat 请求的 HTTP 调试信息（重试尝试、状态码、最后响应预览）")
	chatCmd.Flags().Bool("fail-fast", false, "调试模式：禁用自动重试，首次失败立即返回")
	chatCmd.Flags().StringSlice("skills-dir", nil, "附加外部 skills 目录（可重复指定），与系统级 skills 一起加载")
	chatCmd.Flags().Int("skills-top-k", 0, "aicli chat 暴露给模型的候选 skills 数量（0=使用配置默认值）")
	chatCmd.Flags().String("skills-mode", "auto", "aicli chat 的 skills 暴露模式（auto|prefer|only）")
	chatCmd.Flags().Bool("skills-debug", false, "打印当前请求的 skill route 候选、暴露结果与模式")
	chatCmd.Flags().String("permission-mode", "default", "本地 actor/team 运行的权限模式（default|accept_edits|plan|bypass_permissions）")
	chatCmd.Flags().String("approval-reuse", "session_readonly_shell", "本地 actor/team 审批复用策略（off|session_readonly_shell|team_readonly_shell）")
	chatCmd.Flags().Bool("yolo", false, "快捷模式：等价于 --permission-mode bypass_permissions")
	chatCmd.Flags().StringSliceP("image", "i", nil, "附加本地图片文件路径（可重复指定，支持 PNG/JPEG/GIF/WebP）")
	rootCmd.AddCommand(chatCmd)

	// pipe 子命令 - 管道输入处理
	pipeCmd := &cobra.Command{
		Use:   "pipe",
		Short: "管道模式处理",
		Long: `从标准输入读取数据，结合提示词发送给 AI 处理。

支持两种模式：
  - 缓冲模式（默认）：读取所有输入后一次性发送
  - 流式模式（--stream）：实时处理管道输入

使用场景：
  - 日志分析：tail -f app.log | aicli pipe -p "分析异常"
  - 文件处理：cat file.txt | aicli pipe -p "翻译成法语"
  - CI/CD：git diff | aicli pipe -p "生成 PR 描述"`,
		Example: `  # 日志监控
  tail -f app.log | aicli pipe -p "如果出现异常，请通知我"

  # 翻译
  echo "Hello World" | aicli pipe -p "翻译成中文"

  # 流式处理
  tail -f app.log | aicli pipe -p "分析日志" --stream

  # 指定模型
  cat data.json | aicli pipe -p "格式化这个 JSON" --model gpt-4

  # JSON 输出
  echo "Hello" | aicli pipe -p "翻译成中文" --output json

  # CI 场景
  git diff main...HEAD | aicli pipe -p "为新代码生成 PR 描述"`,
		Run: func(cmd *cobra.Command, args []string) {
			commands.HandlePipe(cmd, cfg)
		},
	}
	pipeCmd.Flags().StringP("prompt", "p", "", "提示词/指令")
	pipeCmd.Flags().StringP("provider", "P", "", "指定 provider 名称")
	pipeCmd.Flags().StringP("model", "m", "", "指定模型名称")
	pipeCmd.Flags().IntP("buffer", "b", 4096, "缓冲区大小（字节）")
	pipeCmd.Flags().IntP("max-tokens", "t", 2000, "最大输出 tokens")
	pipeCmd.Flags().BoolP("stream", "s", false, "流式处理模式（实时发送）")
	pipeCmd.Flags().String("output", "", "输出格式（text|json）")
	pipeCmd.Flags().BoolP("json", "j", false, "兼容选项：等价于 --output json")
	pipeCmd.Flags().IntP("timeout", "", 120, "请求超时时间（秒）")
	rootCmd.AddCommand(pipeCmd)

	// version 子命令
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "显示版本信息",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("AI CLI version: %s\n", version)
			fmt.Printf("Build time: %s\n", buildTime)
		},
	}
	rootCmd.AddCommand(versionCmd)

	// mcp 子命令
	mcpCmd := commands.MCPCommand()
	rootCmd.AddCommand(mcpCmd)

	// 执行
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
