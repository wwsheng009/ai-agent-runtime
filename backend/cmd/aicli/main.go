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
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/consolehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

var (
	version     = "dev"
	buildTime   = "unknown"
	cfg         *config.Config
	logFilePath string // AICLI 日志文件路径（命令行覆盖）
	pprofHandle *pprofServerHandle
	meshHost    *mesh.Host // 本进程的网格接入点（--mesh=false 时为 nil）
)

func main() {
	// --console-host 必须先于 .env、配置、日志和终端初始化处理。MobaXterm /
	// mintty 中的原生 Windows 进程拿到的是 pipe；父进程通过
	// CREATE_NEW_CONSOLE 重启自身后，子进程才能从 CONIN$/CONOUT$ 获得真实
	// Windows Console 句柄。参数在重启前会被移除，因此不会递归启动。
	consoleBootstrap, err := consolehost.BootstrapSelf(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s: %v\n", consolehost.FlagName, err)
		os.Exit(1)
	}
	os.Args = append([]string{os.Args[0]}, consoleBootstrap.Args...)
	if consoleBootstrap.Launched {
		os.Exit(consoleBootstrap.ExitCode)
	}

	// 加载 .env 文件：显式 --config/-c 所在目录优先，其后按当前
	// build profile 的默认配置目录顺序查找。
	envPaths := config.StartupDotEnvSearchPaths(os.Args[1:], config.DefaultConfigSearchPaths())
	envPath := config.ResolveDotEnvPath(envPaths)
	if envPath != "" {
		_ = godotenv.Load(envPath)
	} else {
		fmt.Fprintf(os.Stderr, "Warning: .env file not found in %v\n", envPaths)
	}

	commands.SetChatStatusBuildInfo(version, buildTime)
	// Win7 及更早 conhost 无 VT 处理，先切输出代码页为 UTF-8，否则所有
	// 中文输出（chat、帮助、错误信息）都会按 GBK 解码成乱码。
	if restore := ui.EnsureConsoleUTF8Output(); restore != nil {
		defer restore()
	}
	// 向 /debug display 暴露 pprof 端点信息（未启用时显示提示）。
	commands.RegisterChatDebugPprofProvider(func() string {
		if pprofHandle == nil {
			return ""
		}
		return pprofHandle.URL()
	})

	// 创建 root 命令
	rootCmd := &cobra.Command{
		Use:     "aicli [子命令]",
		Short:   "AI CLI 工具，默认进入 chat",
		Long:    rootCommandLongHelp,
		Example: rootCommandExampleHelp,
		Version: version,
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
		},
	}
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// 写令牌可在启动时显式指定（--web-token > AICLI_WEB_TOKEN > 随机生成）。
		// 校验失败直接中止启动：静默回退随机令牌会让"我明明传了 token"变成难排查的 403。
		webTokenFlag, _ := rootCmd.Flags().GetString("web-token")
		webTokenSource, tokenErr := commands.ApplyChatWebAuthToken(webTokenFlag)
		if tokenErr != nil {
			return fmt.Errorf("failed to configure web write token: %w", tokenErr)
		}

		// mesh（多进程网格）接入：先于 loopback 服务器启动，进程一启动就写
		// 节点档案，endpoint 在监听成功后补写（见下方 SetEndpoint）。
		// --mesh=false 完全短路；网格故障只降级为 warning，绝不影响 chat。
		meshEnabled, meshFlagErr := rootCmd.Flags().GetBool("mesh")
		if meshFlagErr != nil {
			meshEnabled = true
		}
		if !meshEnabled {
			meshHost = nil
			mesh.SetCurrent(nil)
			fmt.Fprintln(os.Stderr, "Info: mesh disabled (--mesh=false); this process stays invisible on the mesh")
		} else if meshHost == nil && shouldJoinMesh(cmd) {
			meshHost = startMeshHost(cmd)
		}

		// loopback 服务器（Web 客户端 + /debug 端点）按需启动：
		// --web-port > AICLI_PPROF > --pprof/--debug 的随机空闲端口，
		// 实际地址打印到 stderr。当 --debug 开启时也自动启动（内置
		// /debug/chat/status 端点提供会话渲染/显示状态 JSON 快照）。
		// --web-host / AICLI_WEB_HOST 控制监听地址（默认 127.0.0.1；
		// 设为 0.0.0.0 可在局域网访问，同时所有请求需携带写令牌）。
		pprofFlag, _ := rootCmd.Flags().GetBool("pprof")
		pprofEnv := strings.TrimSpace(os.Getenv("AICLI_PPROF"))
		webPortFlag, _ := rootCmd.Flags().GetInt("web-port")
		webPortSet := rootCmd.Flags().Changed("web-port")
		webHostFlag, _ := rootCmd.Flags().GetString("web-host")
		webHost := strings.TrimSpace(webHostFlag)
		if webHost == "" {
			webHost = strings.TrimSpace(os.Getenv("AICLI_WEB_HOST"))
		}
		if webHost == "" {
			webHost = "127.0.0.1"
		}
		// --web-dev：开发模式，跳过回环模式下的写令牌校验。
		// 回环地址（127.0.0.1/localhost）下自动开启；
		// 非回环地址（0.0.0.0）下，回环 IP 始终免令牌，--web-dev 无效（warn 提示）。
		webDevFlag, _ := rootCmd.Flags().GetBool("web-dev")
		if rootCmd.Flags().Changed("web-dev") {
			commands.SetChatWebDevMode(webDevFlag)
			if !commands.ChatWebHostIsLoopback(webHost) && webDevFlag {
				fmt.Fprintln(os.Stderr, "WARN: --web-dev 在非回环模式（--web-host 0.0.0.0）下无效：回环 IP 已始终跳过令牌校验，本地网络 IP 与远程 IP 仍需令牌。")
			}
		} else if commands.ChatWebHostIsLoopback(webHost) {
			commands.SetChatWebDevMode(true)
		}
		debugFlag := false
		if cmd != nil {
			if f := cmd.Flags().Lookup("debug"); f != nil {
				debugFlag, _ = cmd.Flags().GetBool("debug")
			}
		}
		pprofAddr, addrErr := resolveLoopbackServerAddr(pprofFlag, debugFlag, webPortFlag, webPortSet, webHost, pprofEnv)
		if addrErr != nil {
			return addrErr
		}
		if pprofAddr != "" && pprofHandle == nil {
			// 会话粘性端口：resume 同一会话且未显式指定端口（--web-port /
			// AICLI_PPROF）时，复用该会话上次实际监听的端口，避免每次 resume
			// 都换一个随机端口导致 /debug/chat/*、/web/ 等调试 URL 失效。
			webPortExplicit := webPortSet || strings.TrimSpace(pprofEnv) != ""
			targetSessionID := resolveChatWebPortTargetSessionID(cmd, args)
			if !webPortExplicit {
				if stickyAddr, reused := stickyLoopbackServerAddr(webHost, pprofAddr, targetSessionID); reused {
					if stickyHandle, err := startPprofServer(stickyAddr); err == nil {
						pprofHandle = stickyHandle
						fmt.Fprintf(os.Stderr, "Info: reusing stored web port for session %s: %s (override with --web-port)\n", targetSessionID, stickyHandle.URL())
					} else {
						fmt.Fprintf(os.Stderr, "Warning: stored web port for session %s is unavailable (%v); falling back to a random port\n", targetSessionID, err)
					}
				}
			}
			if pprofHandle == nil {
				startedHandle, err := startPprofServer(pprofAddr)
				if err != nil {
					return fmt.Errorf("failed to start pprof server: %w", err)
				}
				pprofHandle = startedHandle
			}
			// 记录实际监听端口：该会话下次 resume（不带 --web-port）即可复用。
			// 会话加载路径（新建/恢复）也会为当前活动会话补写同一档案。
			listenHost, listenPort := loopbackServerHostPort(pprofHandle)
			if listenPort > 0 {
				commands.SetChatWebPortRuntimeInfo(listenPort, listenHost)
				// mesh：节点档案补上 endpoint/auth/capabilities，本节点从此可被
				// 其他节点调用（architecture §3.1 / §4.1）。
				if meshHost != nil {
					meshHost.SetEndpoint(
						meshEndpointForListenAddr(listenHost, listenPort),
						meshAuthForListenAddr(listenHost),
					)
				}
				if targetSessionID != "" {
					if err := commands.SaveChatWebPortRecord(targetSessionID, listenPort, listenHost); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to persist web port for session %s: %v\n", targetSessionID, err)
					}
				}
			}
			handle := pprofHandle
			tq := handle.TokenQueryParam()
			nonLoopback := tq != ""
			if nonLoopback {
				listenAddr := handle.Addr()
				lanAddrs := commands.ChatWebLocalAddresses()
				if len(lanAddrs) > 0 {
					// 展示实际局域网地址（0.0.0.0 在浏览器中不可直接访问）。
					// URL 直接指向 /debug/endpoints?format=text，粘贴即看调试信息。
					var addrURLs []string
					// tq 形如 "?token=xxx"，转换为 "&token=xxx" 拼到已有查询参数上。
					tqParam := strings.Replace(tq, "?", "&", 1)
					for _, ip := range lanAddrs {
						addrURLs = append(addrURLs, ip+":"+handle.WebPort()+"/debug/endpoints?format=text"+tqParam)
					}
					fmt.Fprintf(os.Stderr, "Info: pprof endpoint enabled (non-loopback listen: %s, all requests require token)\n", listenAddr)
					fmt.Fprintf(os.Stderr, "Info: LAN access URLs (paste in browser):\n")
					for _, u := range addrURLs {
						fmt.Fprintf(os.Stderr, "  http://%s\n", u)
					}
					fmt.Fprintf(os.Stderr, "  (fallback: http://0.0.0.0:%s/debug/endpoints?format=text%s)\n", handle.WebPort(), tqParam)
				} else {
					fmt.Fprintf(os.Stderr, "Info: pprof endpoint enabled (non-loopback listen: %s, all requests require token): %s\n", listenAddr, handle.URL()+tq)
					fmt.Fprintf(os.Stderr, "Info: use the actual LAN IP with the same port\n")
				}
			} else {
				fmt.Fprintf(os.Stderr, "Info: pprof endpoint enabled: %s\n", handle.URL())
			}
			fmt.Fprintf(os.Stderr, "Info: chat render status endpoint: %s%s (JSON; ?format=text for plain text)\n", handle.DisplayURL(), tq)
			fmt.Fprintf(os.Stderr, "Info: chat screen content endpoint: %s%s (JSON; ?format=text for plain text)\n", handle.ScreenURL(), tq)
			fmt.Fprintf(os.Stderr, "Info: chat debug endpoints list: %s%s (JSON; ?format=text for plain text)\n", handle.EndpointsURL(), tq)
			fmt.Fprintf(os.Stderr, "Info: chat web client / remote invoke endpoint: %s (POST %s)\n", handle.WebURL(), handle.InvokeURL())
			if nonLoopback {
				fmt.Fprintf(os.Stderr, "Info: web client URL (include ?token= for LAN access): %s\n", handle.WebURL())
			}
			tokenHint := "POST /web/api/* 必需"
			if nonLoopback {
				tokenHint = "ALL requests 必需"
			}
			if commands.IsChatWebDevMode() {
				if nonLoopback {
					tokenHint = "开发模式 (回环 IP 跳过校验)"
				} else {
					tokenHint = "开发模式 (回环地址跳过校验)"
				}
			}
			if webTokenSource != "" {
				tokenHint += "; 来自 " + webTokenSource + "（固定令牌，重启不轮换，注意保管）"
			}
			fmt.Fprintf(os.Stderr, "Info: web write token (%s): %s (%s)\n", commands.ChatWebAuthTokenHeader, commands.EnsureChatWebAuthToken(), tokenHint)
			if nonLoopback {
				fmt.Fprintf(os.Stderr, "Info: runtime observe plane: %s (local in-process; capabilities/snapshot/sessions/events; token required)\n", handle.Addr()+strings.TrimRight(commands.ChatDebugObservePrefix(), "/"))
			} else {
				fmt.Fprintf(os.Stderr, "Info: runtime observe plane: %s (local in-process; capabilities/snapshot/sessions/events)\n", handle.Addr()+strings.TrimRight(commands.ChatDebugObservePrefix(), "/"))
			}
		}

		if !shouldBootstrapConfigForCommand(cmd, args) {
			return nil
		}

		cfgFlag, _ := rootCmd.Flags().GetString("config")
		explicitConfigPath := strings.TrimSpace(cfgFlag)
		configPath := explicitConfigPath
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
		if presetsPath, created, presetsErr := config.EnsureUserPresetsFile(); presetsErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to prepare user presets: %v\n", presetsErr)
		} else if created {
			fmt.Fprintf(os.Stderr, "Info: no user presets found, created presets at %s\n", presetsPath)
		}
		loadedConfig, err := config.InitGlobalConfigLayered(configPath, explicitConfigPath)
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
	rootCmd.PersistentFlags().StringP("config", "c", "", fmt.Sprintf(
		"配置文件路径（未指定时按 $HOME/.aicli/%[1]s -> ./.aicli/%[1]s -> ./%[2]s -> ./configs/%[1]s 顺序查找）",
		aiclipaths.DefaultConfigFileName,
		aiclipaths.DefaultCLIConfigFileName,
	))
	rootCmd.PersistentFlags().StringVarP(&logFilePath, "logfile", "l", "", "日志文件路径（默认使用 aicli.log.file_path 或 log.file_path）")
	rootCmd.PersistentFlags().String("theme", "", "输出主题配色或明暗（classic|focus|contrast|mono 或 auto|dark|light；优先级: --theme > AICLI_THEME/AICLI_THEME_MODE > 配置）")
	rootCmd.PersistentFlags().String("syntax-theme", "", "代码语法高亮主题（auto 或 Chroma 主题名；优先级: --syntax-theme > 环境变量 > 配置）")
	rootCmd.PersistentFlags().Bool("envelope", false, "JSON 输出时使用统一 envelope 结构（ok/command/data 或 ok/command/error）")
	rootCmd.PersistentFlags().Bool("pprof", false, "启用 pprof 诊断端点（默认监听 127.0.0.1 随机空闲端口；可用 AICLI_PPROF 指定地址或 --web-host 0.0.0.0 在 IPv4 局域网访问、--web-host :: 在 IPv6；0.0.0.0 时回环 IP 免令牌，本地网络 IP 与远程 IP 需令牌）")
	rootCmd.PersistentFlags().Int("web-port", 0, "指定 loopback 服务器（Web 客户端 / /debug 端点）监听端口（1-65535；等价于 AICLI_PPROF=127.0.0.1:<port> 且优先级更高；默认随机空闲端口）")
	rootCmd.PersistentFlags().String("web-host", "", "指定 loopback 服务器监听地址（默认 127.0.0.1；设为 0.0.0.0 在 IPv4 局域网访问，设为 :: 在 IPv6；0.0.0.0 时回环 IP 始终免令牌，本地网络 IP 与远程 IP 需令牌；也可用 AICLI_WEB_HOST 环境变量指定）")
	rootCmd.PersistentFlags().String("web-token", "", "预设 Web 写令牌（默认每进程随机；也可用 AICLI_WEB_TOKEN；至少 16 位，字符集 A-Za-z0-9-._~）")
	rootCmd.PersistentFlags().Bool("web-dev", false, "开发模式：在回环模式下跳过写令牌校验（POST/PUT/DELETE 无需 token）；默认在 127.0.0.1/localhost 自动开启。非回环模式下（0.0.0.0）回环 IP 始终免令牌，此旗仅影响回环模式 POST 校验")
	rootCmd.PersistentFlags().Bool("mesh", true, "启用 mesh 多进程网格接入（默认开启；--mesh=false 完全不写网格节点档案、不注册 mesh 端点、不订阅网格事件）")
	rootCmd.PersistentFlags().Bool("console-host", false, "Windows：当前 stdin/stdout 为 PTY/pipe 时，在新的原生 Console 窗口中重启 aicli")

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

	// stats 只读分析子命令（会话用量/失败模式/采集健康自检）
	rootCmd.AddCommand(commands.NewStatsCommand())

	// usage-analytics 分析库维护子命令（rebuild-stats 对账/修复漂移）
	rootCmd.AddCommand(commands.NewUsageAnalyticsCommand())

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

	// export 子命令 — chat 内 /export 的顶层等价入口（完整 JSON / Markdown 导出）
	rootCmd.AddCommand(commands.NewExportCommand(func() *config.Config {
		return cfg
	}))

	// import 子命令 — export --full 的反向入口（把完整 JSON 会话导入回会话库）
	rootCmd.AddCommand(commands.NewImportCommand(func() *config.Config {
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

	// acp 子命令 — `aicli agent stdio` 的顶层等价入口，供通用 ACP 客户端
	// 以规范命令名直接拉起；`aicli --acp` 亦转发至此（见 prependACPFlag）。
	rootCmd.AddCommand(commands.NewACPCommand(func() *config.Config {
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

	// replay 子命令 — 离线回放录屏到虚拟终端（B2 场景）
	rootCmd.AddCommand(commands.NewReplayCommand())

	// mcp 子命令
	mcpCmd := commands.MCPCommand()
	rootCmd.AddCommand(mcpCmd)

	rootCmd.SetArgs(prependACPFlag(prependDefaultChatCommand(os.Args[1:], rootCmd.PersistentFlags(), chatCmd.Flags())))

	// 执行
	if err := rootCmd.Execute(); err != nil {
		mesh.CloseCurrent()
		os.Exit(1)
	}
	if pprofHandle != nil {
		_ = pprofHandle.Close()
	}
	// 正常退出路径：档案置 stopped 后删除、释放租约（S4 起）、写 node.stopped。
	mesh.CloseCurrent()
}

// prependACPFlag rewrites the root `--acp` / `-a` convenience flag into the
// canonical `acp` subcommand. `aicli --acp --yolo -P x -m y` becomes
// `aicli acp --yolo -P x -m y`, so generic ACP launchers can pass the protocol
// switch the way many tools expose it (a top-level mode flag) while aicli
// keeps a single implementation path (aicli agent stdio == aicli acp).
// The rewrite happens after prependDefaultChatCommand so the injected default
// `chat` subcommand is replaced, not nested under it.
func prependACPFlag(args []string) []string {
	for i, arg := range args {
		var match bool
		switch {
		case arg == "--acp" || arg == "-a":
			match = true
		case strings.HasPrefix(arg, "--acp="):
			if v := strings.TrimPrefix(arg, "--acp="); v == "true" {
				match = true
			} else {
				return args
			}
		default:
			continue
		}
		if !match {
			continue
		}
		// Remove the injected "chat" subcommand if present — prependDefaultChatCommand
		// adds it when no subcommand is specified, and prependACPFlag replaces it
		// with "acp" rather than nesting "chat" under "acp" (which would fail
		// because the acp command has Args: cobra.NoArgs).
		before := args[:i]
		if len(before) > 0 && before[0] == "chat" {
			before = before[1:]
		}
		rewritten := append([]string{"acp"}, before...)
		rewritten = append(rewritten, args[i+1:]...)
		return rewritten
	}
	return args
}

func shouldBootstrapConfigForCommand(cmd *cobra.Command, args []string) bool {
	if cmd == nil {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(cmd.Name()))
	switch name {
	case "", "aicli", "help", "init", "uninstall", "version", "skill", "skills", "stats":
		return false
	}
	for current := cmd; current != nil; current = current.Parent() {
		switch strings.ToLower(strings.TrimSpace(current.Name())) {
		case "skill", "skills", "stats":
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
