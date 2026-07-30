package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

var (
	version     = "dev"
	buildTime   = "unknown"
	cfg         *config.Config
	logFilePath string // AICLI 日志文件路径（命令行覆盖）
)

func main() {
	// 加载 .env 文件（按 config.yaml 同样的查找顺序）
	envPaths := config.DefaultDotEnvSearchPaths()
	envPath := config.ResolveDotEnvPath(envPaths)
	if envPath != "" {
		_ = godotenv.Load(envPath)
	} else {
		fmt.Fprintf(os.Stderr, "Warning: .env file not found in %v\n", envPaths)
	}

	commands.SetChatStatusBuildInfo(version, buildTime)

	// 创建 root 命令
	rootCmd := &cobra.Command{
		Use:     "aicli [子命令]",
		Short:   "AI CLI 工具，默认进入 chat",
		Long:    rootCommandLongHelp,
		Example: rootCommandExampleHelp,
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
		},
	}
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if !shouldBootstrapConfigForCommand(cmd, args) {
			return nil
		}

		cfgFlag, _ := rootCmd.Flags().GetString("config")
		configPath := strings.TrimSpace(cfgFlag)
		if configPath == "" {
			configPath = config.ResolveConfigPath(config.DefaultConfigSearchPaths())
		}
		if starterPath, created, starterErr := config.EnsureStarterConfigFile(configPath); starterErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to prepare starter config: %v\n", starterErr)
		} else {
			configPath = starterPath
			if created {
				fmt.Fprintf(os.Stderr, "Info: no config found, created starter config at %s\n", configPath)
			}
		}
		loadedConfig, err := config.InitGlobalConfig(configPath)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		cfg = loadedConfig

		// AICLI 日志配置覆盖（优先级：命令行 > aicli.log > log），并统一为文件日志，
		// 避免内部 JSON 日志污染交互提示、管道输出或结构化命令结果。
		if cfg != nil {
			commands.ConfigureAICLILoggerForCLI(cfg, logFilePath)
		}

		// Theme startup precedence: CLI flags > environment > config.
		flagTheme := ""
		if v, err := rootCmd.Flags().GetString("theme"); err == nil {
			flagTheme = strings.TrimSpace(v)
		}
		flagSyntax := ""
		if v, err := rootCmd.Flags().GetString("syntax-theme"); err == nil {
			flagSyntax = strings.TrimSpace(v)
		}
		inputs := startupThemeInputs{
			flagTheme:       flagTheme,
			flagSyntax:      flagSyntax,
			envTheme:        os.Getenv("AICLI_THEME"),
			envMode:         os.Getenv("AICLI_THEME_MODE"),
			envSyntax:       os.Getenv("AICLI_THEME_SYNTAX"),
			envSyntaxLegacy: os.Getenv("AICLI_SYNTAX_THEME"),
		}
		if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Theme != nil {
			inputs.configPalette = cfg.AICLI.Theme.Name
			inputs.configMode = cfg.AICLI.Theme.Mode
			inputs.configSyntax = cfg.AICLI.Theme.Syntax
		}
		selection := resolveStartupTheme(inputs)
		if selection.palette != "" {
			if err := ui.SetThemePreset(selection.palette); err != nil {
				return err
			}
		}
		if selection.mode != "" {
			if err := ui.SetThemeMode(selection.mode); err != nil {
				return err
			}
		}
		if err := ui.SetSyntaxTheme(selection.syntax); err != nil {
			return err
		}

		// 初始化日志系统
		if err := logger.InitLogger(&cfg.Log); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to initialize logger: %v\\n", err)
		}
		return nil
	}

	// 全局 flags
	rootCmd.PersistentFlags().StringP("config", "c", "", "配置文件路径（未指定时按 $HOME/.aicli/config.yaml -> ./.aicli/config.yaml -> ./aicli.yaml -> ./configs/config.yaml 顺序查找）")
	rootCmd.PersistentFlags().StringVarP(&logFilePath, "logfile", "l", "", "日志文件路径（默认使用 aicli.log.file_path 或 log.file_path）")
	rootCmd.PersistentFlags().String("theme", "", "输出主题配色或明暗（classic|focus|contrast|mono 或 auto|dark|light；优先级: --theme > AICLI_THEME/AICLI_THEME_MODE > 配置）")
	rootCmd.PersistentFlags().String("syntax-theme", "", "代码语法高亮主题（auto 或 Chroma 主题名；优先级: --syntax-theme > 环境变量 > 配置）")
	rootCmd.PersistentFlags().Bool("envelope", false, "JSON 输出时使用统一 envelope 结构（ok/command/data 或 ok/command/error）")

	// config 子命令
	configCmd := &cobra.Command{
		Use:     "config",
		Short:   "管理配置",
		Long:    configCommandLongHelp,
		Example: configCommandExampleHelp,
		Run: func(cmd *cobra.Command, args []string) {
			commands.HandleConfig(cmd, cfg)
		},
	}
	configCmd.Flags().StringP("provider", "p", "", "指定 provider 名称")
	configCmd.Flags().BoolP("groups", "g", false, "显示 provider groups")
	configCmd.Flags().BoolP("models", "m", false, "列出所有可用模型")
	configCmd.Flags().Bool("tui", false, "打开交互式配置管理界面")
	configCmd.Flags().Bool("no-tui", false, "禁用默认交互界面，使用传统摘要输出")
	configCmd.Flags().String("output", "", "输出格式（text|json）")
	configCmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	rootCmd.AddCommand(configCmd)

	// init 子命令
	rootCmd.AddCommand(commands.NewInitCommand())

	// uninstall 子命令
	rootCmd.AddCommand(commands.NewUninstallCommand())

	// test 子命令
	testCmd := &cobra.Command{
		Use:     "test",
		Short:   "测试端点",
		Long:    testCommandLongHelp,
		Example: testCommandExampleHelp,
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

	// login 子命令
	rootCmd.AddCommand(commands.NewLoginCommand(func() *config.Config {
		return cfg
	}))

	// provider 管理子命令
	rootCmd.AddCommand(commands.NewProviderCommand(func() *config.Config {
		return cfg
	}))

	// doctor 诊断子命令
	rootCmd.AddCommand(commands.NewDoctorCommand(func() *config.Config {
		return cfg
	}))

	// balance 账户余额子命令
	rootCmd.AddCommand(commands.NewBalanceCommand(func() *config.Config {
		return cfg
	}))

	// skill 管理子命令
	rootCmd.AddCommand(commands.NewSkillCommand())

	// plugin 本地打包/信任管理（无 marketplace）
	rootCmd.AddCommand(commands.NewPluginCommand())

	// image 子命令
	rootCmd.AddCommand(commands.NewImageCommand(func() *config.Config {
		return cfg
	}))

	// context 子命令
	contextCmd := &cobra.Command{
		Use:     "context",
		Short:   "测试上下文窗口和最大输出",
		Long:    contextCommandLongHelp,
		Example: contextCommandExampleHelp,
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

	// chat / resume 子命令（共享 flags 与 HandleChat 启动路径）
	chatCmd := commands.NewChatCommand(func() *config.Config {
		return cfg
	})
	rootCmd.AddCommand(chatCmd)
	rootCmd.AddCommand(commands.NewResumeCommand(func() *config.Config {
		return cfg
	}))

	rootCmd.AddCommand(commands.NewExecCommand(func() *config.Config {
		return cfg
	}))

	// agent 子命令 — ACP 等外部 Agent 协议宿主
	rootCmd.AddCommand(commands.NewAgentCommand(func() *config.Config {
		return cfg
	}))

	// pipe 子命令 - 管道输入处理
	pipeCmd := &cobra.Command{
		Use:     "pipe",
		Short:   "管道模式处理",
		Long:    pipeCommandLongHelp,
		Example: pipeCommandExampleHelp,
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

	rootCmd.SetArgs(prependDefaultChatCommand(os.Args[1:], rootCmd.PersistentFlags(), chatCmd.Flags()))

	// 执行
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func shouldBootstrapConfigForCommand(cmd *cobra.Command, args []string) bool {
	if cmd == nil {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(cmd.Name()))
	switch name {
	case "", "aicli", "help", "init", "uninstall", "version", "skill", "skills":
		return false
	}
	for current := cmd; current != nil; current = current.Parent() {
		switch strings.ToLower(strings.TrimSpace(current.Name())) {
		case "skill", "skills":
			return false
		}
	}
	if len(args) == 0 && cmd.Parent() == nil {
		return false
	}
	if value, err := cmd.Flags().GetBool("help"); err == nil && value {
		return false
	}
	return true
}

// isStartupThemeModeToken reports whether --theme value should be treated as a
// light/dark mode rather than a palette name.
func isStartupThemeModeToken(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "auto", "system", "dark", "night", "black", "light", "day", "white":
		return true
	default:
		return false
	}
}

type startupThemeInputs struct {
	flagTheme       string
	flagSyntax      string
	envTheme        string
	envMode         string
	envSyntax       string
	envSyntaxLegacy string
	configPalette   string
	configMode      string
	configSyntax    string
}

type startupThemeSelection struct {
	palette string
	mode    string
	syntax  string
}

func resolveStartupTheme(in startupThemeInputs) startupThemeSelection {
	selection := startupThemeSelection{
		palette: strings.TrimSpace(in.configPalette),
		mode:    strings.TrimSpace(in.configMode),
		syntax:  strings.TrimSpace(in.configSyntax),
	}
	applyThemeToken := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if isStartupThemeModeToken(raw) {
			selection.mode = ui.NormalizeThemeModeName(raw)
			return
		}
		selection.palette = raw
	}

	applyThemeToken(in.envTheme)
	if value := strings.TrimSpace(in.envMode); value != "" {
		selection.mode = value
	}
	// AICLI_THEME_SYNTAX is canonical; keep the documented older alias.
	if value := strings.TrimSpace(in.envSyntaxLegacy); value != "" {
		selection.syntax = value
	}
	if value := strings.TrimSpace(in.envSyntax); value != "" {
		selection.syntax = value
	}
	applyThemeToken(in.flagTheme)
	if value := strings.TrimSpace(in.flagSyntax); value != "" {
		selection.syntax = value
	}
	if selection.syntax == "" {
		selection.syntax = "auto"
	}
	return selection
}
