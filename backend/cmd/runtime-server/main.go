package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/spf13/pflag"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/filebrowse"
	"github.com/wwsheng009/ai-agent-runtime/internal/filetransport"
	"github.com/wwsheng009/ai-agent-runtime/internal/gitbrowse"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/webui"
	"go.uber.org/zap/zapcore"
)

const runtimeServerDefaultConfigName = aiclipaths.DefaultConfigFileName

type runtimeServerCommandOptions struct {
	ConfigPath string
	ListenAddr string
	PIDFile    string
	PID        int
	Wait       time.Duration
	Pprof      bool
	WebPort    int
	WebPortSet bool
}

type runtimeServerStartConflict struct {
	PID                 int
	ListenAddr          string
	PIDFile             string
	RequestedConfigPath string
	RunningConfigPath   string
	Reason              string
}

type runtimeServerStartupLogCapture struct {
	Path        string
	InitialSize int64
	Existed     bool
}

func main() {
	os.Exit(run())
}

func run() int {
	args := os.Args[1:]
	loadEnv(args)

	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "help", "-h", "--help":
			printRuntimeServerRootUsage()
			return 0
		case "version", "-V", "--version":
			printRuntimeServerVersion()
			return 0
		case "serve":
			return runServe(args[1:])
		case "start":
			return runStart(args[1:])
		case "stop":
			return runStop(args[1:])
		case "status":
			return runStatus(args[1:])
		default:
			if !strings.HasPrefix(strings.TrimSpace(args[0]), "-") {
				fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", args[0])
				printRuntimeServerRootUsage()
				return 1
			}
		}
	}

	return runServe(args)
}

func printRuntimeServerRootUsage() {
	fmt.Fprintf(os.Stdout, `runtime-server - ai-agent-runtime 的本地运行时服务

Description:
  提供 Web UI、HTTP API、MCP 端点与终端会话能力。
  不带子命令时等价于 serve，历史启动方式保持兼容。

Usage:
  runtime-server serve   [options]   前台启动服务（默认子命令）
  runtime-server start   [options]   后台启动服务并写入 PID 文件
  runtime-server stop    [options]   停止受管实例
  runtime-server status  [options]   查看服务状态、配置与监听地址
  runtime-server help | -h | --help  显示本帮助
  runtime-server version | -V | --version   显示版本与构建信息

Options (serve):
  -c, --config PATH        配置文件路径；未指定时按 Notes 中的搜索顺序查找 %[1]s
      --listen HOST:PORT   监听地址，优先级高于配置文件，例如 127.0.0.1:8101
      --pid-file PATH      PID 文件路径（默认 ./logs/runtime-server.pid）
      --pprof              启用 pprof 诊断端点（监听 127.0.0.1 随机空闲端口，可用 --web-port 或 AICLI_PPROF 指定地址）
      --web-port PORT      指定 pprof 诊断端点监听端口（1-65535；等价 AICLI_PPROF=127.0.0.1:<port> 且优先）
  -h, --help               显示子命令帮助

Options (start):
  -c, --config PATH        同 serve
      --listen HOST:PORT   同 serve
      --pid-file PATH      同 serve（默认 ./logs/runtime-server.pid）
      --wait DURATION      等待后台进程完成启动的超时时间（默认 30s）
      --pprof              同 serve
      --web-port PORT      同 serve（随子进程转发）
  -h, --help               显示子命令帮助

Options (stop):
      --pid-file PATH      PID 文件路径（默认 ./logs/runtime-server.pid）
      --pid PID            直接停止指定 PID，跳过 PID 文件
      --wait DURATION      等待进程退出的超时时间（默认 10s）
  -h, --help               显示子命令帮助

Options (status):
  -c, --config PATH        同 serve
      --listen HOST:PORT   同 serve
      --pid-file PATH      同 serve
  -h, --help               显示子命令帮助

Examples:
  # 前台启动（默认监听配置中的地址）
  runtime-server serve

  # 指定配置与监听地址
  runtime-server serve --config ./configs/mcp-server.yaml --listen 127.0.0.1:8101

  # 后台启动并等待就绪
  runtime-server start --wait 30s

  # 停止受管实例
  runtime-server stop

  # 查看状态
  runtime-server status

Notes:
  - 不带子命令时，等价于 serve，以前的启动方式保持兼容。
  - start 会在后台启动服务并写入 PID 文件。
  - stop 优先使用 PID 文件停止受管实例，也支持 --pid 直接停止指定进程。
  - 未指定 --config 时，按优先级从高到低取第一个存在的文件：%[2]s。
  - 默认 PID 文件为 ./logs/runtime-server.pid。
  - 每个子命令都支持 -h / --help，例如：runtime-server serve --help。
`, runtimeServerDefaultConfigName, config.ConfigSearchSummary())
}

func printRuntimeServerVersion() {
	info := buildinfo.Backend()
	fmt.Fprintf(os.Stdout, "runtime-server version %s\n", info.Version)
	if buildTime := strings.TrimSpace(info.BuildTime); buildTime != "" {
		fmt.Fprintf(os.Stdout, "build time: %s\n", buildTime)
	}
	if commit := strings.TrimSpace(info.GitCommit); commit != "" {
		fmt.Fprintf(os.Stdout, "git commit: %s\n", commit)
	}
}

func newRuntimeServerFlagSet(name string) *pflag.FlagSet {
	flags := pflag.NewFlagSet(name, pflag.ContinueOnError)
	flags.SetOutput(os.Stdout)
	return flags
}

func parseServeOptions(args []string) (runtimeServerCommandOptions, error) {
	opts := runtimeServerCommandOptions{
		PIDFile: runtimeserver.DefaultPIDFile,
	}
	flags := newRuntimeServerFlagSet("runtime-server serve")
	flags.StringVarP(&opts.ConfigPath, "config", "c", opts.ConfigPath, fmt.Sprintf(
		"配置文件路径；未指定时按默认搜索顺序查找 %s（找不到时回退 %s）",
		runtimeServerDefaultConfigName,
		aiclipaths.StandardConfigFileName,
	))
	flags.StringVar(&opts.ListenAddr, "listen", "", "监听地址，优先级高于配置文件，例如 127.0.0.1:8101")
	flags.StringVar(&opts.PIDFile, "pid-file", opts.PIDFile, "PID 文件路径")
	flags.BoolVar(&opts.Pprof, "pprof", false, "启用 pprof 诊断端点（监听 127.0.0.1 随机空闲端口；可用 --web-port 或 AICLI_PPROF 指定地址）")
	flags.IntVar(&opts.WebPort, "web-port", 0, "指定 pprof 诊断端点监听端口（1-65535；等价于 AICLI_PPROF=127.0.0.1:<port> 且优先级更高）")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	opts.WebPortSet = flags.Changed("web-port")
	return opts, nil
}

func parseStartOptions(args []string) (runtimeServerCommandOptions, error) {
	opts := runtimeServerCommandOptions{
		PIDFile: runtimeserver.DefaultPIDFile,
		Wait:    30 * time.Second,
	}
	flags := newRuntimeServerFlagSet("runtime-server start")
	flags.StringVarP(&opts.ConfigPath, "config", "c", opts.ConfigPath, fmt.Sprintf(
		"配置文件路径；未指定时按默认搜索顺序查找 %s（找不到时回退 %s）",
		runtimeServerDefaultConfigName,
		aiclipaths.StandardConfigFileName,
	))
	flags.StringVar(&opts.ListenAddr, "listen", "", "监听地址，优先级高于配置文件，例如 127.0.0.1:8101")
	flags.StringVar(&opts.PIDFile, "pid-file", opts.PIDFile, "PID 文件路径")
	flags.DurationVar(&opts.Wait, "wait", opts.Wait, "等待后台进程完成启动的超时时间")
	flags.BoolVar(&opts.Pprof, "pprof", false, "启用 pprof 诊断端点（监听 127.0.0.1 随机空闲端口；可用 --web-port 或 AICLI_PPROF 指定地址）")
	flags.IntVar(&opts.WebPort, "web-port", 0, "指定 pprof 诊断端点监听端口（1-65535；等价于 AICLI_PPROF=127.0.0.1:<port> 且优先级更高）")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	opts.WebPortSet = flags.Changed("web-port")
	return opts, nil
}

func parseStopOptions(args []string) (runtimeServerCommandOptions, error) {
	opts := runtimeServerCommandOptions{
		PIDFile: runtimeserver.DefaultPIDFile,
		Wait:    10 * time.Second,
	}
	flags := newRuntimeServerFlagSet("runtime-server stop")
	flags.StringVar(&opts.PIDFile, "pid-file", opts.PIDFile, "PID 文件路径")
	flags.IntVar(&opts.PID, "pid", 0, "直接停止指定 PID，跳过 PID 文件")
	flags.DurationVar(&opts.Wait, "wait", opts.Wait, "等待进程退出的超时时间")
	return opts, flags.Parse(args)
}

func parseStatusOptions(args []string) (runtimeServerCommandOptions, error) {
	opts := runtimeServerCommandOptions{
		PIDFile: runtimeserver.DefaultPIDFile,
	}
	flags := newRuntimeServerFlagSet("runtime-server status")
	flags.StringVarP(&opts.ConfigPath, "config", "c", opts.ConfigPath, fmt.Sprintf(
		"配置文件路径；未指定时按默认搜索顺序查找 %s（找不到时回退 %s）",
		runtimeServerDefaultConfigName,
		aiclipaths.StandardConfigFileName,
	))
	flags.StringVar(&opts.ListenAddr, "listen", "", "监听地址，优先级高于配置文件，例如 127.0.0.1:8101")
	flags.StringVar(&opts.PIDFile, "pid-file", opts.PIDFile, "PID 文件路径")
	return opts, flags.Parse(args)
}

func resolveRuntimeServerConfigPath(configPath string) string {
	configPath = strings.TrimSpace(configPath)
	if configPath != "" {
		return resolveRuntimeServerConfigCandidate(configPath)
	}

	for _, candidate := range defaultRuntimeServerConfigSearchPaths() {
		resolved := resolveRuntimeServerConfigCandidate(candidate)
		if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
			return resolved
		}
	}
	return ""
}

// runtimeServerConfigSearchNames returns the bootstrap config filenames tried
// by default discovery, most specific first. The active build profile's name
// leads; the standard profile's name (config.yaml) follows as a compatibility
// fallback so a win7compat binary can still discover standard-layout agent
// configs when no profile-specific file exists.
func runtimeServerConfigSearchNames() []string {
	names := []string{runtimeServerDefaultConfigName}
	if aiclipaths.StandardConfigFileName != runtimeServerDefaultConfigName {
		names = append(names, aiclipaths.StandardConfigFileName)
	}
	return names
}

// defaultRuntimeServerConfigSearchPaths returns the shared bootstrap config
// layer stack (highest precedence first). It must stay derived from
// agentconfig so the CLI and the server cannot drift apart: a server and a
// CLI resolving different files for the same directory tree is what makes
// "which config is actually in effect" unanswerable.
func defaultRuntimeServerConfigSearchPaths() []string {
	return config.DefaultConfigSearchPaths()
}

func resolveRuntimeServerConfigCandidate(configPath string) string {
	cleaned := filepath.Clean(strings.TrimSpace(configPath))
	if cleaned == "" {
		return ""
	}
	if filepath.IsAbs(cleaned) {
		return cleaned
	}
	if absolutePath, err := filepath.Abs(cleaned); err == nil {
		return absolutePath
	}
	return cleaned
}

func runServe(args []string) int {
	opts, err := parseServeOptions(args)
	if err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintf(os.Stderr, "failed to parse serve flags: %v\n", err)
		return 1
	}
	opts.ConfigPath = resolveRuntimeServerConfigPath(opts.ConfigPath)

	if presetsPath, created, presetsErr := config.EnsureUserPresetsFile(); presetsErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to prepare user presets: %v\n", presetsErr)
	} else if created {
		fmt.Fprintf(os.Stderr, "Info: no user presets found, created presets at %s\n", presetsPath)
	}

	// 内置 profile 首次初始化落盘（与 user presets 同一时机、同一层根）。
	// 服务端不额外写 project 层：内置内容进用户层一次即可，会话工作区的项目层
	// 只属于用户自己（FR-14 的只读发现语义不变）。
	if seeded, seedErr := profilesys.SeedUserProfiles(); seedErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to seed built-in profiles: %v\n", seedErr)
	} else if len(seeded) > 0 {
		fmt.Fprintf(os.Stderr,
			"Info: no user profiles found, seeded %d built-in profiles at %s (%s)\n",
			len(seeded), filepath.Dir(seeded[0].Root), strings.Join(profilesys.BuiltinProfileNames(), ", "))
	}

	cfg, configSnapshotInfo, err := runtimeserver.LoadRuntimeAgentConfig(opts.ConfigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		return 1
	}

	if err := logger.InitLogger(&cfg.Log); err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		return 1
	}
	defer func() {
		_ = logger.Sync()
	}()

	// pprof 诊断端点按需启动：AICLI_PPROF 环境变量或 --pprof 显式开启，
	// 默认绑定 127.0.0.1 随机空闲端口（仅本机可访问）。
	// 启动于 PID 冲突检查之后，避免冗余实例短暂占用 pprof 端口。
	var pprofHandle *pprofServerHandle
	defer func() {
		if pprofHandle != nil {
			_ = pprofHandle.Close()
		}
	}()

	pidFile := runtimeserver.ResolvePIDFilePath(opts.PIDFile)
	if info, err := runtimeserver.ReadInstanceInfo(pidFile); err == nil {
		if targetPID, alive := runtimeserver.ResolveInstancePID(info.PID, info.ListenAddr); alive {
			logger.Error("Runtime server already running",
				logger.Int("pid", targetPID),
				logger.String("pid_file", pidFile),
				logger.String("listen", info.ListenAddr),
			)
			return 1
		}
		_ = runtimeserver.RemoveInstanceInfoIfPID(pidFile, info.PID)
	}

	// pprof 服务器在通过 PID 冲突检查后启动：--web-port / AICLI_PPROF 环境变量或 --pprof 显式开启。
	pprofAddr, addrErr := resolveRuntimeServerPprofAddr(opts.Pprof, opts.WebPort, opts.WebPortSet)
	if addrErr != nil {
		fmt.Fprintf(os.Stderr, "%v\n", addrErr)
		logger.Error("Invalid pprof listen address", logger.Err(addrErr))
		return 1
	}
	if pprofAddr != "" {
		if !isLoopbackAddr(pprofAddr) {
			logger.Warn("pprof endpoint will listen on a non-loopback address; "+
				"pprof 可触发 GC / 执行分析代码，暴露到网络上有风险，建议使用 127.0.0.1",
				logger.String("addr", pprofAddr))
		}
		handle, err := startPprofServer(pprofAddr)
		if err != nil {
			logger.Error("Failed to start pprof server", logger.Err(err))
			return 1
		}
		pprofHandle = handle
		logger.Info("pprof endpoint enabled", logger.String("url", handle.URL()))
		fmt.Fprintf(os.Stderr, "Info: pprof endpoint enabled: %s\n", handle.URL())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := newRuntimeServerApp(ctx, cfg, opts.ConfigPath)
	if err != nil {
		logger.Error("Failed to initialize runtime server", logger.Err(err))
		return 1
	}
	defer app.close()

	addr := resolveListenAddr(cfg, opts.ListenAddr)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fields := []zapcore.Field{logger.Err(err), logger.String("listen", addr)}
		if ownerPID, lookupErr := runtimeserver.FindListeningPID(addr); lookupErr == nil && ownerPID > 0 {
			fields = append(fields, logger.Int("port_owner_pid", ownerPID))
		}
		logger.Error("Runtime server exited with error", fields...)
		return 1
	}
	defer listener.Close()

	cwd, _ := os.Getwd()
	if err := runtimeserver.WriteInstanceInfo(pidFile, runtimeserver.InstanceInfo{
		PID:        os.Getpid(),
		ListenAddr: addr,
		ConfigPath: strings.TrimSpace(opts.ConfigPath),
		Cwd:        cwd,
		StartedAt:  time.Now().UTC(),
	}); err != nil {
		logger.Error("Failed to write runtime server pid file", logger.Err(err), logger.String("pid_file", pidFile))
		return 1
	}
	defer func() {
		if err := runtimeserver.RemoveInstanceInfoIfPID(pidFile, os.Getpid()); err != nil {
			logger.Warn("Failed to remove runtime server pid file", logger.Err(err), logger.String("pid_file", pidFile))
		}
	}()
	app.configureServiceControl(pidFile, addr, opts.ConfigPath, cwd)

	server := &http.Server{
		Handler:           app.router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Warn("Runtime server shutdown returned error", logger.Err(err))
		}
	}()

	logger.Info("AI agent runtime HTTP server started",
		logger.String("listen", addr),
		logger.String("config_file", strings.TrimSpace(opts.ConfigPath)),
		logger.String("active_config_file", strings.TrimSpace(configSnapshotInfo.ActivePath)),
		logger.String("config_snapshot_file", strings.TrimSpace(configSnapshotInfo.SnapshotPath)),
		logger.String("runtime_config_file", strings.TrimSpace(app.runtimeManager.GetFilePath())),
		logger.String("skill_dir", strings.TrimSpace(app.skillsCfg.SkillDir)),
		logger.Int("extra_skill_dir_count", len(app.skillsCfg.ExtraSkillDirs)),
		logger.Int("pid", os.Getpid()),
		logger.String("pid_file", pidFile),
	)

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		logger.Error("Runtime server exited with error", logger.Err(err))
		return 1
	}
	return 0
}

func runStart(args []string) int {
	opts, err := parseStartOptions(args)
	if err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintf(os.Stderr, "failed to parse start flags: %v\n", err)
		return 1
	}
	opts.ConfigPath = resolveRuntimeServerConfigPath(opts.ConfigPath)

	pidFile := runtimeserver.ResolvePIDFilePath(opts.PIDFile)
	conflict, err := detectRuntimeServerStartConflict(opts.ConfigPath, opts.ListenAddr, pidFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "检查 runtime-server 运行状态失败: %v\n", err)
		return 1
	}
	if conflict != nil {
		fmt.Fprintln(os.Stdout, formatRuntimeServerStartConflict(conflict))
		return 1
	}

	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve executable path: %v\n", err)
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve working directory: %v\n", err)
		return 1
	}
	logCapture, err := prepareRuntimeServerStartupLogCapture(opts.ConfigPath, cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve runtime-server log file: %v\n", err)
		return 1
	}

	commandArgs := []string{"serve", "--config", opts.ConfigPath, "--pid-file", pidFile}
	if strings.TrimSpace(opts.ListenAddr) != "" {
		commandArgs = append(commandArgs, "--listen", strings.TrimSpace(opts.ListenAddr))
	}
	if opts.Pprof {
		commandArgs = append(commandArgs, "--pprof")
	}
	if opts.WebPortSet {
		// 显式端口随子进程转发：否则 background serve 会退回随机端口，
		// 调用方拿到的 --web-port 就形同虚设。
		commandArgs = append(commandArgs, "--web-port", fmt.Sprintf("%d", opts.WebPort))
	}
	launchCommand, launchArgs, err := runtimeserver.PrepareStartCommand(executable, cwd, commandArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to prepare runtime-server start command: %v\n", err)
		return 1
	}

	env := ensureEnvDefault(os.Environ(), "LOG_OUTPUT", "file")
	env = ensureEnvDefault(env, "LOG_ENABLE_CONSOLE", "false")

	// 日志文件路径未配置时，把子进程 stdout/stderr 捕获到 PID 文件同目录的
	// fallback 文件，保证启动失败的真实原因可诊断（否则错误被丢弃只剩
	// "未配置日志文件路径"）。已配置日志文件路径时保持原行为。
	captureStdout, captureStderr := "", ""
	if strings.TrimSpace(logCapture.Path) == "" {
		fallbackPath := filepath.Join(filepath.Dir(pidFile), "runtime-server-startup.fallback.log")
		if err := os.MkdirAll(filepath.Dir(fallbackPath), 0o755); err == nil {
			captureStdout, captureStderr = fallbackPath, fallbackPath
			logCapture.Path = fallbackPath
			logCapture.Existed = false
			logCapture.InitialSize = 0
		}
	}

	cmd, err := runtimeserver.StartDetachedProcessWithOutput(launchCommand, launchArgs, env, captureStdout, captureStderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start runtime-server in background: %v\n", err)
		return 1
	}

	exitCh := make(chan error, 1)
	go func() {
		exitCh <- cmd.Wait()
	}()

	deadline := time.NewTimer(opts.Wait)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		if info, err := runtimeserver.ReadInstanceInfo(pidFile); err == nil && info.PID > 0 {
			if targetPID, alive := runtimeserver.ResolveInstancePID(info.PID, info.ListenAddr); alive {
				if runtimeServerReady(info.ListenAddr, 500*time.Millisecond) {
					fmt.Fprintf(os.Stdout, "runtime-server 已启动: pid=%d listen=%s pid_file=%s\n", targetPID, strings.TrimSpace(info.ListenAddr), pidFile)
					return 0
				}
			}
		}

		select {
		case err := <-exitCh:
			if err == nil {
				fmt.Fprintln(os.Stderr, buildRuntimeServerStartFailureMessage(
					"runtime-server 进程在写入 PID 文件前已退出。",
					nil,
					logCapture,
				))
			} else {
				fmt.Fprintln(os.Stderr, buildRuntimeServerStartFailureMessage(
					"runtime-server 启动失败。",
					err,
					logCapture,
				))
			}
			return 1
		case <-deadline.C:
			fmt.Fprintln(os.Stderr, buildRuntimeServerStartFailureMessage(
				fmt.Sprintf("runtime-server 启动超时，未在 %s 内写入 PID 文件: %s", opts.Wait, pidFile),
				nil,
				logCapture,
			))
			return 1
		case <-ticker.C:
		}
	}
}

func detectRuntimeServerStartConflict(configPath, listenOverride, pidFile string) (*runtimeServerStartConflict, error) {
	pidFile = runtimeserver.ResolvePIDFilePath(pidFile)
	requestedConfigPath := strings.TrimSpace(configPath)
	if info, err := runtimeserver.ReadInstanceInfo(pidFile); err == nil {
		if targetPID, alive := runtimeserver.ResolveInstancePID(info.PID, info.ListenAddr); alive {
			return &runtimeServerStartConflict{
				PID:                 targetPID,
				ListenAddr:          strings.TrimSpace(info.ListenAddr),
				PIDFile:             pidFile,
				RequestedConfigPath: requestedConfigPath,
				RunningConfigPath:   strings.TrimSpace(info.ConfigPath),
				Reason:              "managed_instance",
			}, nil
		}
		_ = runtimeserver.RemoveInstanceInfoIfPID(pidFile, info.PID)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	listenAddr, err := resolveListenAddrForCommand(configPath, listenOverride)
	if err != nil {
		return nil, err
	}
	if pid, err := runtimeserver.FindListeningPID(listenAddr); err == nil && pid > 0 {
		return &runtimeServerStartConflict{
			PID:                 pid,
			ListenAddr:          strings.TrimSpace(listenAddr),
			PIDFile:             pidFile,
			RequestedConfigPath: requestedConfigPath,
			Reason:              "listen_addr_in_use",
		}, nil
	} else if err != nil {
		return nil, err
	}

	return nil, nil
}

func formatRuntimeServerStartConflict(conflict *runtimeServerStartConflict) string {
	if conflict == nil {
		return "runtime-server 已在运行"
	}

	listenAddr := strings.TrimSpace(conflict.ListenAddr)
	pidFile := strings.TrimSpace(conflict.PIDFile)
	requestedConfig := strings.TrimSpace(conflict.RequestedConfigPath)
	runningConfig := strings.TrimSpace(conflict.RunningConfigPath)

	switch conflict.Reason {
	case "listen_addr_in_use":
		return fmt.Sprintf(
			"runtime-server 启动已拒绝: 目标监听地址已被现有实例占用; pid=%d listen=%s pid_file=%s requested_config=%s",
			conflict.PID,
			listenAddr,
			pidFile,
			fallbackDisplayValue(requestedConfig, "<auto>"),
		)
	default:
		message := fmt.Sprintf(
			"runtime-server 已在运行: pid=%d listen=%s pid_file=%s running_config=%s requested_config=%s",
			conflict.PID,
			listenAddr,
			pidFile,
			fallbackDisplayValue(runningConfig, "<unknown>"),
			fallbackDisplayValue(requestedConfig, "<auto>"),
		)
		if sameRuntimeServerConfigPath(runningConfig, requestedConfig) {
			return message + " 当前实例尚未停止，本次 start 不会重新加载配置。"
		}
		return message + " 当前运行实例与本次请求配置不同；请先 stop 或 restart，避免继续使用旧实例。"
	}
}

func sameRuntimeServerConfigPath(left, right string) bool {
	left = resolveRuntimeServerConfigCandidate(left)
	right = resolveRuntimeServerConfigCandidate(right)
	return left != "" && right != "" && strings.EqualFold(left, right)
}

func fallbackDisplayValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func prepareRuntimeServerStartupLogCapture(configPath, cwd string) (runtimeServerStartupLogCapture, error) {
	logPath, err := resolveRuntimeServerLogPath(configPath, cwd)
	if err != nil {
		return runtimeServerStartupLogCapture{}, err
	}
	capture := runtimeServerStartupLogCapture{
		Path: strings.TrimSpace(logPath),
	}
	if capture.Path == "" {
		return capture, nil
	}
	if info, err := os.Stat(capture.Path); err == nil && !info.IsDir() {
		capture.Existed = true
		capture.InitialSize = info.Size()
	}
	return capture, nil
}

func resolveRuntimeServerLogPath(configPath, cwd string) (string, error) {
	cfg, _, err := runtimeserver.LoadRuntimeAgentConfig(configPath)
	if err != nil {
		return "", err
	}
	logPath := strings.TrimSpace(cfg.Log.FilePath)
	if logPath == "" {
		return "", nil
	}
	logPath = aiclipaths.ExpandUserPath(logPath)
	if filepath.IsAbs(logPath) {
		return filepath.Clean(logPath), nil
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		if resolved, err := os.Getwd(); err == nil {
			cwd = resolved
		}
	}
	if cwd == "" {
		return filepath.Clean(logPath), nil
	}
	return filepath.Clean(filepath.Join(cwd, logPath)), nil
}

func buildRuntimeServerStartFailureMessage(prefix string, startErr error, capture runtimeServerStartupLogCapture) string {
	prefix = strings.TrimSpace(prefix)
	lines := make([]string, 0, 6)
	if prefix != "" {
		if startErr != nil {
			lines = append(lines, fmt.Sprintf("%s %v", prefix, startErr))
		} else {
			lines = append(lines, prefix)
		}
	} else if startErr != nil {
		lines = append(lines, startErr.Error())
	}

	logTail, logPath, tailMode, err := readRuntimeServerStartupLogTail(capture, 40)
	if err != nil {
		lines = append(lines, fmt.Sprintf("读取启动日志失败: %v", err))
		return strings.Join(lines, "\n")
	}
	if strings.TrimSpace(logPath) == "" {
		lines = append(lines, "未配置日志文件路径，无法输出启动失败日志。")
		return strings.Join(lines, "\n")
	}

	lines = append(lines, fmt.Sprintf("日志文件: %s", logPath))
	if strings.TrimSpace(logTail) == "" {
		lines = append(lines, "未读取到本次启动失败对应的日志输出。")
		return strings.Join(lines, "\n")
	}

	switch tailMode {
	case "full_tail":
		lines = append(lines, "日志尾部:")
	default:
		lines = append(lines, "本次启动新增日志:")
	}
	lines = append(lines, logTail)
	return strings.Join(lines, "\n")
}

func readRuntimeServerStartupLogTail(capture runtimeServerStartupLogCapture, maxLines int) (string, string, string, error) {
	logPath := strings.TrimSpace(capture.Path)
	if logPath == "" {
		return "", "", "", nil
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", logPath, "", nil
		}
		return "", logPath, "", err
	}

	content := raw
	mode := "new_tail"
	if capture.Existed && capture.InitialSize >= 0 && int64(len(raw)) > capture.InitialSize {
		content = raw[capture.InitialSize:]
	} else {
		mode = "full_tail"
	}
	return tailTextLines(string(content), maxLines), logPath, mode, nil
}

func tailTextLines(content string, maxLines int) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if maxLines <= 0 || len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-maxLines:], "\n")
}

func runStop(args []string) int {
	opts, err := parseStopOptions(args)
	if err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintf(os.Stderr, "failed to parse stop flags: %v\n", err)
		return 1
	}

	pidFile := runtimeserver.ResolvePIDFilePath(opts.PIDFile)
	targetPID := opts.PID
	listenAddr := ""
	recordedPID := 0
	if targetPID <= 0 {
		info, err := runtimeserver.ReadInstanceInfo(pidFile)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "未找到 PID 文件: %s\n", pidFile)
			} else {
				fmt.Fprintf(os.Stderr, "读取 PID 文件失败: %v\n", err)
			}
			return 1
		}
		recordedPID = info.PID
		listenAddr = strings.TrimSpace(info.ListenAddr)
		var alive bool
		targetPID, alive, err = runtimeserver.ResolveStopTarget(info.PID, info.ListenAddr)
		if err != nil {
			// 无法确认目标进程身份：不得清理 PID 文件，也不得强杀。
			fmt.Fprintf(os.Stderr, "停止 runtime-server 失败: %v\n", err)
			return 1
		}
		if !alive {
			// 端口无监听者：服务已退出。记录 PID 即使还"存在"也只是被系统
			// 复用的无关进程，不能杀；直接清理陈旧 PID 文件。
			_ = runtimeserver.RemoveInstanceInfoIfPID(pidFile, info.PID)
			fmt.Fprintf(os.Stdout, "runtime-server 未在运行（PID 文件陈旧，端口 %s 无监听），已清理: pid=%d\n", displayListenAddr(listenAddr), info.PID)
			return 0
		}
	}

	if !runtimeserver.ProcessRunning(targetPID) {
		_ = runtimeserver.RemoveInstanceInfoIfPID(pidFile, targetPID)
		fmt.Fprintf(os.Stdout, "runtime-server 已停止: pid=%d\n", targetPID)
		return 0
	}

	if err := runtimeserver.TerminateProcess(targetPID, listenAddr, opts.Wait); err != nil {
		fmt.Fprintf(os.Stderr, "停止 runtime-server 失败: %v\n", err)
		return 1
	}
	// PID 文件记录可能因重启滞后于真实服务 PID（端口交叉验证找到的才是
	// 活跃者），清理时以文件记录值为准，避免残留陈旧文件。
	if recordedPID > 0 {
		_ = runtimeserver.RemoveInstanceInfoIfPID(pidFile, recordedPID)
	} else {
		_ = runtimeserver.RemoveInstanceInfoIfPID(pidFile, targetPID)
	}
	fmt.Fprintf(os.Stdout, "runtime-server 已停止: pid=%d\n", targetPID)
	return 0
}

func displayListenAddr(listenAddr string) string {
	if strings.TrimSpace(listenAddr) == "" {
		return "未知"
	}
	return listenAddr
}

func runStatus(args []string) int {
	opts, err := parseStatusOptions(args)
	if err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintf(os.Stderr, "failed to parse status flags: %v\n", err)
		return 1
	}
	opts.ConfigPath = resolveRuntimeServerConfigPath(opts.ConfigPath)

	pidFile := runtimeserver.ResolvePIDFilePath(opts.PIDFile)
	if info, err := runtimeserver.ReadInstanceInfo(pidFile); err == nil {
		if targetPID, alive := runtimeserver.ResolveInstancePID(info.PID, info.ListenAddr); alive {
			fmt.Fprintf(os.Stdout, "runtime-server 运行中: pid=%d listen=%s pid_file=%s\n", targetPID, strings.TrimSpace(info.ListenAddr), pidFile)
			return 0
		}
		fmt.Fprintf(os.Stdout, "runtime-server 未运行，发现陈旧 PID 文件: pid=%d pid_file=%s\n", info.PID, pidFile)
		return 1
	} else if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "读取 PID 文件失败: %v\n", err)
		return 1
	}

	addr, err := resolveListenAddrForCommand(opts.ConfigPath, opts.ListenAddr)
	if err == nil && strings.TrimSpace(addr) != "" {
		if pid, lookupErr := runtimeserver.FindListeningPID(addr); lookupErr == nil && pid > 0 {
			fmt.Fprintf(os.Stdout, "runtime-server 端口已被占用，但没有受管 PID 文件: listen=%s pid=%d pid_file=%s\n", addr, pid, pidFile)
			return 1
		}
	}

	fmt.Fprintf(os.Stdout, "runtime-server 未运行: pid_file=%s\n", pidFile)
	return 1
}

func resolveListenAddrForCommand(configPath, override string) (string, error) {
	cfg, _, err := runtimeserver.LoadRuntimeAgentConfig(configPath)
	if err != nil {
		return "", err
	}
	return resolveListenAddr(cfg, override), nil
}

func ensureEnvDefault(env []string, key, value string) []string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return env
		}
	}
	return append(env, prefix+value)
}

func runtimeServerReady(listenAddr string, timeout time.Duration) bool {
	healthURL, ok := runtimeServerHealthURL(listenAddr)
	if !ok {
		return false
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(healthURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func runtimeServerHealthURL(listenAddr string) (string, bool) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil || strings.TrimSpace(port) == "" {
		return "", false
	}

	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}

	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + strings.Trim(host, "[]") + "]"
	}

	return fmt.Sprintf("http://%s/healthz", net.JoinHostPort(host, port)), true
}

type runtimeServerApp struct {
	router          *mux.Router
	handler         *skillsapi.Handler
	cfg             *config.Config
	skillsCfg       *config.SkillsRuntimeConfig
	runtimeManager  *runtimecfg.RuntimeManager
	bootstrap       *runtimebootstrap.Manager
	mcpManager      mcpmanager.Manager
	ledgerStore     io.Closer
	supervision     *runtimeserver.SupervisionControlPlane
	subagentBatches io.Closer
}

// loadRuntimeServerManager 通过分层栈加载 runtime.yaml（P2）：
//
//   - 层栈只有用户级 $HOME/.aicli/runtime.yaml 与项目级 ./.aicli/runtime.yaml
//     （高层只覆盖它显式写的键）；开发仓库布局（configs/runtime.yaml /
//     backend/configs/runtime.yaml）**不是**隐式来源，只有调用方显式传入
//     （如 --config backend/configs/runtime.yaml）时才会被读取；
//   - 读取路径（RuntimeManager.GetFilePath）取「生效来源」= 最高存在层，未改动既有语义
//     （sessions 目录仍相对它解析）；全新安装（任何层都不存在）时回落内置默认
//     （RuntimeManager.Load 对空路径直接返回默认值）；
//   - 写回不在这里：agent.maxSteps 由分层版 persister 按层分摊，仓库布局文件永不被改写。
func loadRuntimeServerManager(runtimeManager *runtimecfg.RuntimeManager) error {
	merged, err := config.LoadMergedRuntimeConfigDocument()
	if err != nil {
		return fmt.Errorf("failed to load layered runtime config: %w", err)
	}
	if merged == nil {
		if err := runtimeManager.Load(); err != nil {
			return fmt.Errorf("failed to load runtime config: %w", err)
		}
		return nil
	}
	sourcePath := strings.TrimSpace(merged.SourcePath)
	if sourcePath == "" {
		if target, _ := config.RuntimeConfigWriteTarget(); target != "" {
			sourcePath = target
		}
	}
	if err := runtimeManager.LoadDocument(merged.MergedYAML, sourcePath); err != nil {
		return fmt.Errorf("failed to load runtime config: %w", err)
	}
	return nil
}

func newRuntimeServerApp(ctx context.Context, cfg *config.Config, configPath string) (*runtimeServerApp, error) {
	skillsCfg := normalizeSkillsRuntimeConfig(cfg)
	runtimeManager := runtimecfg.NewRuntimeManager(skillsCfg.ConfigFile)
	if err := loadRuntimeServerManager(runtimeManager); err != nil {
		return nil, err
	}
	runtimeConfig := runtimeManager.Get()
	runtimeConfig.Sessions.Dir = resolveRuntimeServerSessionDir(runtimeManager.GetFilePath(), runtimeConfig.Sessions.Dir)
	sessionruntime.ApplyDefaults(runtimeConfig, sessionruntime.ResolveOptions{
		Config:     runtimeConfig,
		ConfigFile: runtimeManager.GetFilePath(),
		Mode:       sessionruntime.ModeServer,
	})

	mcpAdapter, manager, err := buildSkillsMCPManager(ctx, cfg, runtimeConfig)
	if err != nil {
		return nil, err
	}

	bootstrapManager, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config:              runtimeConfig,
		SkillDir:            skillsCfg.SkillDir,
		SkillDirs:           resolvedExtraSkillDirs(skillsCfg),
		DiscoverOnly:        true,
		MCPManager:          mcpAdapter,
		GatewayProviderName: strings.TrimSpace(skillsCfg.GatewayProviderName),
		ProviderConfigs:     buildSkillsProviderConfigs(cfg),
	})
	if err != nil {
		if manager != nil {
			_ = manager.Stop()
		}
		return nil, fmt.Errorf("failed to initialize runtime bootstrap: %w", err)
	}
	if err := bootstrapManager.Validate(); err != nil {
		if manager != nil {
			_ = manager.Stop()
		}
		_ = bootstrapManager.Stop()
		return nil, fmt.Errorf("invalid runtime bootstrap: %w", err)
	}

	handler := skillsapi.NewHandler(bootstrapManager.Registry(), bootstrapManager.Loader(), mcpAdapter)
	if manager != nil {
		resolution := resolveRuntimeMCPConfigResolution(cfg)
		if resolution.Path != "" {
			handler.SetMCPAdminService(mcpadmin.NewService(resolution.Path,
				mcpadmin.WithManager(manager),
				mcpadmin.WithConfigDiagnostics(mcpadmin.ConfigDiagnosticsFromResolution(resolution)),
			))
		}
	}
	bootstrapManager.ApplyToSkillsHandler(handler)
	handler.SetAICLIConfig(cfg)
	handler.SetFileTransferService(filetransport.NewLocalService())
	siteAccountService := runtimeserver.NewLocalSiteAccountService(configPath, config.DefaultAuthStorePath())
	siteAccountService.SetProviderReloader(func(nextCfg *config.Config) error {
		return bootstrapManager.ReloadProviderConfigs(runtimeserver.BuildRuntimeProviderConfigs(nextCfg))
	})
	handler.SetSiteAccountService(siteAccountService)
	handler.SetRuntimeConfig(runtimeConfig, runtimeManager.GetFilePath())
	handler.SetRuntimeLogFilePath(strings.TrimSpace(cfg.Log.FilePath))
	handler.SetRuntimeConfigResolver(func(scope skillsapi.UsageScope) *runtimecfg.RuntimeConfig {
		selectedConfig, selectedPath := runtimeManager.SelectConfigForScopeWithPath(scope.ScopeKey)
		if selectedConfig == nil {
			return nil
		}
		rawConfig, err := json.Marshal(selectedConfig)
		if err != nil {
			return nil
		}
		var configCopy runtimecfg.RuntimeConfig
		if err := json.Unmarshal(rawConfig, &configCopy); err != nil {
			return nil
		}
		configCopy.Sessions.Dir = resolveRuntimeServerSessionDir(selectedPath, configCopy.Sessions.Dir)
		sessionruntime.ApplyDefaults(&configCopy, sessionruntime.ResolveOptions{
			Config:     &configCopy,
			ConfigFile: selectedPath,
			Mode:       sessionruntime.ModeServer,
		})
		return &configCopy
	})
	// 工作区设置里的「最大步骤数」保存：内存快照（RuntimeManager）与配置文件一起改，
	// 避免只改前端本地设置、重启后缺省值又丢回文件里的旧值。
	handler.SetAgentMaxStepsPersister(
		runtimeserver.NewLayeredRuntimeAgentMaxStepsPersister(runtimeManager),
	)
	// 同一个来源的读取端：设置页回显服务端缺省值 + 来源配置文件路径。
	handler.SetAgentMaxStepsProvider(
		runtimeserver.NewLayeredRuntimeAgentMaxStepsReader(runtimeManager),
	)
	// runtime.yaml 的层栈快照：设置页据此显示候选文件、只读层与写入目标。
	handler.SetRuntimeConfigLayersProvider(
		runtimeserver.NewRuntimeConfigLayersProvider(),
	)
	handler.SetProfileSupport(skillsapi.ProfileSupportConfig{
		Registry:          profilesys.NewRegistryFromProfilesConfig(cfg.Profiles),
		DefaultProfile:    defaultProfile(cfg),
		GlobalRuntimePath: strings.TrimSpace(runtimeManager.GetFilePath()),
		GlobalMCPPath:     configuredMCPConfigPath(cfg),
		GlobalSkillDirs:   allConfiguredSkillDirs(skillsCfg),
		// FR-11：`--profile auto` 的路由表（`profiles.auto`）；与 registry 同一快照
		// 生命周期（配置热重载时一起重建），未配置时为内置启发式。
		AutoRoute: profilesys.NewAutoRouteConfig(cfg.Profiles),
	})
	configDocumentService := runtimeserver.NewLocalConfigDocumentService(configPath)
	configHotReloader := runtimeserver.NewRuntimeConfigHotReloader(handler, cfg, bootstrapManager)
	if configDocumentService != nil {
		configDocumentService.SetHotReloader(configHotReloader)
	}
	handler.SetConfigDocumentService(configDocumentService)
	// 外部改动感知（§9.1 登记的「快照刷新边界」）：config document API 之外的写入
	// （另一个 aicli 进程、外部工具、手工编辑配置文件）不会经过 SetHotReloader，
	// 这里轮询来源文件签名——显式 --config 路径 + 分层搜索栈 + 预设层，即
	// LoadRuntimeAgentConfig 的全部输入——并用**同一个**热重载器应用变更（同一套
	// 热/冷路径判据与 warning），免得必须重启 runtime-server 才生效；ctx 取消时轮询退出。
	if external := runtimeserver.NewConfigExternalReloader(
		configHotReloader,
		func() (*config.Config, error) {
			next, _, err := runtimeserver.LoadRuntimeAgentConfig(configPath)
			return next, err
		},
		func() string { return runtimeserver.ConfigSourceSignatureFor(configPath) },
		0,
	); external != nil {
		go external.Run(ctx)
	}
	if persister := runtimeserver.NewSkillsRuntimePolicyPersister(configPath, cfg); persister != nil {
		handler.SetAuthPolicyPersister(persister.PersistAuthPolicy)
		handler.SetUsagePolicyPersister(persister.PersistUsagePolicy)
		handler.SetMutationPolicyPersister(persister.PersistMutationPolicy)
	}
	applySkillsRuntimePolicies(handler, skillsCfg)
	// usage ledger 是可选的观测 / 治理能力，不是服务可用性的前置依赖：
	// 配置启用了账本但初始化失败（database.dsn 为空、驱动不是 sqlite、建表失败或
	// 目录不可写等）时，这里降级为「启动告警 + 账本接口 503」，不再让整个
	// runtime-server 启动失败。原因会透传到 503 响应，避免"账本不可用"变成哑失败。
	// 其余子系统（例如下面的 supervision control plane）仍保持 fail-fast。
	ledgerStore, ledgerUnavailableReason := runtimeserver.ResolveUsageLedgerStore(cfg)
	if ledgerUnavailableReason != "" {
		logger.Warn("Skills usage ledger unavailable; /api/runtime/usage/ledger will return 503",
			logger.String("reason", ledgerUnavailableReason))
		handler.SetUsageLedgerUnavailableReason(ledgerUnavailableReason)
	}
	if ledgerStore != nil {
		handler.SetUsageLedgerStore(ledgerStore)
	}

	// P2 Parent/Lead Supervision Control Plane (doc 6.2-6.9): durable store +
	// action service + wake scheduler + descendant provider. Real mutation
	// executors (agentcontrol / team orchestrator) are injected by the aicli
	// host; until then cancel/close/retry fail durably with a clear result
	// instead of pretending success.
	//
	// P0-4：durable batch store 先建，再作为结果读出口（include_results /
	// read_agent_result）的数据源接进控制面；建库失败只降级告警（与 usage
	// ledger 同口径：没有跨重启 batch 控制面不等于服务不可用）。
	subagentBatchCloser, batchStoreErr := handler.EnableDurableSubagentBatches(
		filepath.Join(filepath.Dir(config.DefaultAuthStorePath()), "data", "subagent-batches"),
	)
	if batchStoreErr != nil {
		logger.Warn("Subagent batch store unavailable; restart recovery is disabled",
			logger.String("reason", batchStoreErr.Error()))
	}
	// EnableDurableSubagentBatches returns the store it installed (as an
	// io.Closer); recover the read interface for the P0-4 result source.
	var subagentBatches subagentbatch.BatchStore
	if store, ok := subagentBatchCloser.(subagentbatch.BatchStore); ok {
		subagentBatches = store
	}
	supervisionDataDir := filepath.Join(filepath.Dir(config.DefaultAuthStorePath()), "data", "supervision")
	supervisionPlane, err := runtimeserver.BuildSupervisionControlPlane(
		supervisionDataDir,
		cfg.Supervision.WithDefaults(),
		runtimeserver.SupervisionRuntimeHooks{
			TeamStore:               bootstrapManager.TeamStore(),
			SubagentBatchStore:      subagentBatches,
			CompletionMailboxReader: handler.SupervisionAgentControlMailboxReader(),
		},
	)
	if err != nil {
		if ledgerStore != nil {
			_ = ledgerStore.Close()
		}
		if manager != nil {
			_ = manager.Stop()
		}
		_ = bootstrapManager.Stop()
		return nil, fmt.Errorf("failed to initialize supervision control plane: %w", err)
	}
	handler.SetSupervisionStore(supervisionPlane.Store)
	handler.SetSupervisionActionService(supervisionPlane.Actions)
	handler.SetSupervisionWakeScheduler(supervisionPlane.Wakes)
	handler.SetSupervisionDescendantProvider(supervisionPlane.Provider)
	// Same tuning knobs the CLI host reads (digest/snapshot budgets), so the
	// two hosts cannot drift on injection size defaults (plan §9).
	handler.SetSupervisionConfig(cfg.Supervision.WithDefaults())
	// P2 恢复对等（2026-09-16）：CLI 宿主在启动时做「立即 + 宽限期后」两趟
	// batch 恢复，runtime-server 此前完全没有对应入口，而且 API 侧连 durable
	// batch store 都没接线（默认值是 per-process 内存库），重启后遗留的
	// queued/running batch 永远停在非终态、终态投递也永不重试。store 落到与
	// supervision 同级的数据目录并启动恢复（建库调用已上移到控制面装配之前，
	// 见 P0-4 结果读出口接线）。
	// Real mutation executor: agent close goes through the API session
	// controller; team cancel goes through the durable Team store. The
	// runtime-server host does not own an AgentControl identity graph, so
	// subtree resolution falls back to the close adapter's own target
	// resolution and graph persistence is delegated to the controller.
	supervisionPlane.SetActionExecutor((runtimeserver.SupervisionRuntimeExecutor{
		Store:     supervisionPlane.Store,
		TeamStore: bootstrapManager.TeamStore(),
		CloseAgent: func(ctx context.Context, sessionID string) error {
			return handler.CloseAgentSessionByID(ctx, sessionID)
		},
	}))

	router := mux.NewRouter()
	router.UseEncodedPath()
	router.HandleFunc("/healthz", runtimeInfoHandler).Methods(http.MethodGet)
	runtimeRouter := handler.RegisterRoutes(router)
	// 右侧栏「文件」面板（P0–P2）：/fs/roots|list|stat|preview|download + /fs/upload/*。
	// 追加式注册：不改变既有 /fs/read-file 等端点的语义；服务未注入时模块内统一 503 降级。
	if runtimeRouter != nil {
		if roots := handler.FSBrowserRoots(); roots != nil {
			skillsapi.RegisterFSBrowserRoutes(runtimeRouter, filebrowse.NewService(filebrowse.Deps{
				Roots:  roots,
				Limits: filebrowse.DefaultLimits(),
			}))
			// 右侧栏「Git」面板（P3 只读 + P4-1 stage/unstage）：/git/status|diff|commits|stage。
			// 与 /fs/* 共用同一个作用域解析器（gitbrowse.RootResolver 与 fsscope.RootResolver 同形）。
			// git 不可用时由服务层返回 git_unavailable（503），不影响启动。
			skillsapi.RegisterGitBrowseRoutes(runtimeRouter, gitbrowse.NewService(gitbrowse.Deps{Roots: roots}))
		}
	}
	if webui.Available() {
		router.PathPrefix("/").Handler(webui.Handler())
	} else {
		router.HandleFunc("/", runtimeInfoHandler).Methods(http.MethodGet)
	}

	return &runtimeServerApp{
		router:          router,
		handler:         handler,
		cfg:             cfg,
		skillsCfg:       skillsCfg,
		runtimeManager:  runtimeManager,
		bootstrap:       bootstrapManager,
		mcpManager:      manager,
		ledgerStore:     ledgerStore,
		supervision:     supervisionPlane,
		subagentBatches: subagentBatches,
	}, nil
}

func resolvePathFromConfigFile(configFile, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	configFile = strings.TrimSpace(configFile)
	if configFile == "" {
		return filepath.Clean(target)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(configFile), target))
}

func resolveRuntimeServerSessionDir(configFile, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return aiclipaths.DefaultSessionsDir()
	}
	return resolvePathFromConfigFile(configFile, target)
}

func (a *runtimeServerApp) configureServiceControl(pidFile, listenAddr, configPath, cwd string) {
	if a == nil || a.handler == nil {
		return
	}
	executable, _ := os.Executable()
	a.handler.SetServiceControlService(
		runtimeserver.NewLocalRuntimeServiceControl(
			executable,
			cwd,
			pidFile,
			configPath,
			listenAddr,
		),
	)
}

func (a *runtimeServerApp) close() {
	if a == nil {
		return
	}
	if a.bootstrap != nil {
		if err := a.bootstrap.Stop(); err != nil {
			logger.Warn("Failed to stop runtime bootstrap", logger.Err(err))
		}
	}
	if a.mcpManager != nil {
		if err := a.mcpManager.Stop(); err != nil {
			logger.Warn("Failed to stop MCP manager", logger.Err(err))
		}
	}
	if a.ledgerStore != nil {
		if err := a.ledgerStore.Close(); err != nil {
			logger.Warn("Failed to close usage ledger store", logger.Err(err))
		}
	}
	// 先停恢复循环再关库：宽限期后的第二趟扫描不能跑在已关闭的 store 上。
	if a.handler != nil {
		// P1.5：先 flush 事件持久化缓冲，再停恢复循环/关库，保证尾部事件落盘。
		a.handler.CloseRuntimeEventPersistence()
		a.handler.StopSubagentBatchRecovery()
	}
	if a.subagentBatches != nil {
		if err := a.subagentBatches.Close(); err != nil {
			logger.Warn("Failed to close subagent batch store", logger.Err(err))
		}
	}
	if a.supervision != nil {
		if err := a.supervision.Close(); err != nil {
			logger.Warn("Failed to close supervision control plane", logger.Err(err))
		}
	}
}

func loadEnv(args []string) {
	paths := config.StartupDotEnvSearchPaths(args, defaultRuntimeServerConfigSearchPaths())
	path := config.ResolveDotEnvPath(paths)
	if path == "" {
		return
	}
	_ = godotenv.Load(path)
}

func defaultRuntimeServerDotEnvSearchPaths() []string {
	return config.DotEnvSearchPathsForConfigPaths(defaultRuntimeServerConfigSearchPaths())
}

func normalizeSkillsRuntimeConfig(cfg *config.Config) *config.SkillsRuntimeConfig {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if cfg.SkillsRuntime == nil {
		cfg.SkillsRuntime = &config.SkillsRuntimeConfig{}
	}
	cfg.SkillsRuntime.ConfigFile = aiclipaths.ResolveRuntimeConfigBootstrapPath(
		cfg.SkillsRuntime.ConfigFile,
	)
	if strings.TrimSpace(cfg.SkillsRuntime.SkillDir) == "" {
		cfg.SkillsRuntime.SkillDir = "./.agents/skills"
	}
	if strings.TrimSpace(cfg.SkillsRuntime.GatewayProviderName) == "" {
		cfg.SkillsRuntime.GatewayProviderName = "gateway"
	}
	if cfg.SkillsRuntime.ReindexCooldown <= 0 {
		cfg.SkillsRuntime.ReindexCooldown = 30 * time.Second
	}
	if len(cfg.SkillsRuntime.TenantHeaders) == 0 {
		cfg.SkillsRuntime.TenantHeaders = []string{"X-Skills-Tenant", "X-Skills-Auth-Tenant", "X-Tenant-ID", "X-Authenticated-Tenant"}
	}
	if len(cfg.SkillsRuntime.ProjectHeaders) == 0 {
		cfg.SkillsRuntime.ProjectHeaders = []string{"X-Skills-Project", "X-Skills-Auth-Project", "X-Project-ID", "X-Authenticated-Project"}
	}
	if len(cfg.SkillsRuntime.UserHeaders) == 0 {
		cfg.SkillsRuntime.UserHeaders = []string{"X-Skills-User", "X-Skills-Auth-User", "X-User-ID", "X-Authenticated-User"}
	}
	if len(cfg.SkillsRuntime.RoleHeaders) == 0 {
		cfg.SkillsRuntime.RoleHeaders = []string{"X-Skills-Role", "X-Skills-Auth-Role", "X-Role", "X-Authenticated-Role"}
	}
	if len(cfg.SkillsRuntime.TenantClaims) == 0 {
		cfg.SkillsRuntime.TenantClaims = []string{"tenant_id", "tenant", "tid"}
	}
	if len(cfg.SkillsRuntime.ProjectClaims) == 0 {
		cfg.SkillsRuntime.ProjectClaims = []string{"project_id", "project", "pid"}
	}
	if len(cfg.SkillsRuntime.UserClaims) == 0 {
		cfg.SkillsRuntime.UserClaims = []string{"user_id", "user", "uid", "sub"}
	}
	if len(cfg.SkillsRuntime.RoleClaims) == 0 {
		cfg.SkillsRuntime.RoleClaims = []string{"role", "roles"}
	}
	// SK-7/SK-10（P0/P2）：文档模式与目录预算灰度开关默认值。
	// DocumentMode 默认 off（不改变既有 Codex 技能执行行为）；
	// CatalogBudgetChars 默认 8000（镜像 Codex min(8000 chars, 2% 上下文)）；
	// DisciplineBlock 默认开启。
	if strings.TrimSpace(cfg.SkillsRuntime.DocumentMode) == "" {
		cfg.SkillsRuntime.DocumentMode = "off"
	}
	if cfg.SkillsRuntime.CatalogBudgetChars <= 0 {
		cfg.SkillsRuntime.CatalogBudgetChars = 8000
	}
	if cfg.SkillsRuntime.DisciplineBlock == nil {
		cfg.SkillsRuntime.DisciplineBlock = ptrBool(true)
	}
	cfg.SkillsRuntime.ConfigFile = runtimeserver.ResolveUpwardPath(cfg.SkillsRuntime.ConfigFile)
	cfg.SkillsRuntime.SkillDir = runtimeserver.ResolveUpwardPath(cfg.SkillsRuntime.SkillDir)
	cfg.SkillsRuntime.SkillDirs = runtimeserver.ResolveUpwardPaths(cfg.SkillsRuntime.SkillDirs)
	cfg.SkillsRuntime.ExtraSkillDirs = runtimeserver.ResolveUpwardPaths(cfg.SkillsRuntime.ExtraSkillDirs)
	return cfg.SkillsRuntime
}

// ptrBool 返回 bool 指针（用于 *bool 配置默认值）。
func ptrBool(v bool) *bool {
	return &v
}

func resolveListenAddr(cfg *config.Config, override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}

	host := "127.0.0.1"
	port := 8101
	if cfg != nil {
		if trimmed := strings.TrimSpace(cfg.Server.Host); trimmed != "" {
			host = trimmed
		}
		if cfg.Server.Port > 0 {
			port = cfg.Server.Port
		}
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}

func buildSkillsMCPManager(ctx context.Context, cfg *config.Config, runtimeConfig *runtimecfg.RuntimeConfig) (runtimeskill.MCPManager, mcpmanager.Manager, error) {
	var manager mcpmanager.Manager

	resolution := resolveRuntimeMCPConfigResolution(cfg)
	mcpConfigPath := resolution.Path
	if mcpConfigPath != "" {
		if err := mcpadmin.EnsureFile(mcpConfigPath); err != nil {
			return nil, nil, fmt.Errorf("failed to prepare MCP config: %w", err)
		}
		manager = mcpmanager.NewManager()
		// 分层加载（§4.5 Step 1）：用户级提供基础项，项目级同名整体覆盖。
		// 显式 aicli.mcp.config_file 仍然精确加载单个文件。
		if err := loadRuntimeMCPManagerConfig(manager, cfg); err != nil {
			return nil, nil, fmt.Errorf("failed to load MCP config: %w", err)
		}
		statuses := manager.ListMCPs()
		enabledCount := 0
		for _, status := range statuses {
			if status != nil && status.Enabled {
				enabledCount++
			}
		}
		// 分层加载的告警（如低优先级文件损坏被跳过）必须可观测，不能静默丢配置。
		if reporter, ok := manager.(mcpmanager.ConfigOriginReporter); ok && reporter != nil {
			for _, warning := range reporter.MCPConfigWarnings() {
				logger.Warn("MCP config layer skipped", logger.String("detail", warning))
			}
		}
		logger.Info("MCP config loaded",
			logger.String("path", mcpConfigPath),
			logger.String("source", resolution.Source),
			logger.Int("servers", len(statuses)),
			logger.Int("enabled", enabledCount),
		)
		// 默认启动即连：MCP 客户端后台并行建连，HTTP 服务不等待全部 MCP 就绪，
		// 工具随各服务器连接完成动态进入工具面（ListTools 实时读取注册表）。
		// 单服务器是否参与连接由 mcpServers.<name>.enabled 决定。
		if async, ok := manager.(mcpmanager.AsyncManager); ok {
			if err := async.StartAsync(ctx); err != nil {
				return nil, nil, fmt.Errorf("failed to start MCP manager: %w", err)
			}
			logger.Info("MCP manager starting in background",
				logger.Int("servers", len(statuses)),
				logger.Int("enabled", enabledCount),
			)
		} else if err := manager.Start(ctx); err != nil {
			return nil, nil, fmt.Errorf("failed to start MCP manager: %w", err)
		}
		if cfg != nil && cfg.AICLI != nil && cfg.AICLI.MCP != nil {
			cfg.AICLI.MCP.ConfigFile = mcpConfigPath
		}
	}

	toolManager := runtimetools.NewDefaultManagerWithRuntimeConfig(manager, runtimeConfig)
	return runtimetools.NewAgentAdapter(toolManager), manager, nil
}

func buildSkillsProviderConfigs(cfg *config.Config) map[string]*runtimellm.ProviderConfig {
	providerConfigs := make(map[string]*runtimellm.ProviderConfig)
	if cfg == nil {
		return providerConfigs
	}

	retryTuning := buildLLMRetryTuning(cfg)
	retryRules := buildLLMRetryRules(cfg)

	for name, provider := range cfg.Providers.Items {
		if !provider.Enabled {
			continue
		}

		providerType := provider.GetType()
		if providerType == "" {
			continue
		}

		timeout := provider.Timeout
		if timeout <= 0 {
			timeout = cfg.Providers.Timeout
		}
		maxRetries := runtimellm.ProviderMaxRetriesFromAgentConfig(cfg)

		providerConfigs[name] = &runtimellm.ProviderConfig{
			Type:                  providerType,
			APIKey:                provider.GetAPIKey(),
			BaseURL:               provider.BaseURL,
			APIPath:               provider.APIPath,
			CompatibilityProfile:  provider.Compatibility.Profile,
			Timeout:               timeout,
			MaxRetries:            maxRetries,
			MaxTransportRetries:   runtimellm.ProviderMaxTransportRetriesFromAgentConfig(cfg),
			RetryTuning:           retryTuning,
			RetryRules:            retryRules,
			DefaultModel:          provider.DefaultModel,
			SupportedModels:       append([]string(nil), provider.SupportedModels...),
			ModelMappings:         cloneStringMap(provider.ModelMappings),
			ModelCapabilities:     cloneProviderModelCapabilities(provider.ModelCapabilities),
			EnableImageGeneration: provider.EnableImageGeneration,
			StreamReadTimeout:     runtimellm.ProviderStreamReadTimeoutFromAgentConfig(cfg),
			ResponseHeaderTimeout: runtimellm.ProviderResponseHeaderTimeoutFromAgentConfig(cfg),
			Headers:               config.EffectiveProviderHeaders(cfg.Providers.Headers, provider.Headers),
			HeaderMappings:        cloneStringMap(provider.HeaderMappings),
			HeaderMappingRules:    cloneHeaderMappingRules(provider.HeaderMappingRules),
			ResponseMarkerRules:   cloneResponseMarkerRules(provider.ResponseMarkerRules),
			Proxy:                 config.EffectiveProxyConfig(&cfg.Providers.Proxy, provider.Proxy),
			RequestsPerMinute:     provider.RequestsPerMinute,
		}
	}

	return providerConfigs
}

func buildLLMRetryTuning(cfg *config.Config) runtimellm.RetryTuning {
	return runtimellm.RetryTuningFromAgentConfig(cfg)
}

func buildLLMRetryRules(cfg *config.Config) []runtimellm.RetryRule {
	if cfg == nil || cfg.Retry == nil || !cfg.Retry.Enabled || len(cfg.Retry.Rules) == 0 {
		return nil
	}
	result := make([]runtimellm.RetryRule, 0, len(cfg.Retry.Rules))
	for _, rule := range cfg.Retry.Rules {
		result = append(result, runtimellm.RetryRule{
			Name:              rule.Name,
			Description:       rule.Description,
			Enabled:           rule.Enabled,
			Action:            runtimellm.RetryRuleAction(rule.Action),
			MaxRetries:        rule.MaxRetries,
			RetryDelay:        time.Duration(rule.RetryDelayMS) * time.Millisecond,
			BackoffMultiplier: rule.BackoffMultiplier,
			Keyword: runtimellm.RetryKeywordMatcher{
				CaseSensitive: rule.Keyword.CaseSensitive,
				Values:        append([]string(nil), rule.Keyword.Values...),
				Patterns:      append([]string(nil), rule.Keyword.Patterns...),
			},
			ErrorCode: runtimellm.RetryErrorCodeMatcher{
				Codes:   append([]string(nil), rule.ErrorCode.Codes...),
				Pattern: rule.ErrorCode.Pattern,
			},
			StatusCode: runtimellm.RetryStatusCodeMatcher{
				Codes: append([]int(nil), rule.StatusCode.Codes...),
				Range: rule.StatusCode.Range,
			},
		})
	}
	return result
}

func applySkillsRuntimePolicies(handler *skillsapi.Handler, cfg *config.SkillsRuntimeConfig) {
	if handler == nil || cfg == nil {
		return
	}
	handler.SetAdminToken(cfg.AdminToken)
	handler.SetSearchReindexCooldown(cfg.ReindexCooldown)
	handler.SetMutationPolicy(skillsapi.MutationPolicy{
		ReadOnly:         cfg.ReadOnly,
		DisableImport:    cfg.DisableImport,
		DisablePersist:   cfg.DisablePersist,
		DisableReloadOps: cfg.DisableReloadOps,
		DisableHotReload: cfg.DisableHotReloadOps,
	})
	handler.SetUsagePolicy(skillsapi.UsagePolicy{
		TrackingEnabled:    cfg.UsageTrackingEnabled,
		QuotaEnabled:       cfg.QuotaEnabled,
		DefaultMaxRequests: cfg.DefaultMaxRequests,
		DefaultMaxTokens:   cfg.DefaultMaxTokens,
		TenantQuotas:       buildSkillsUsageQuotaLimits(cfg.QuotaPolicies.Tenants),
		ProjectQuotas:      buildSkillsUsageQuotaLimits(cfg.QuotaPolicies.Projects),
		UserQuotas:         buildSkillsUsageQuotaLimits(cfg.QuotaPolicies.Users),
	})
	handler.SetScopeResolverConfig(buildSkillsScopeResolverConfig(cfg))
}

func buildSkillsScopeResolverConfig(cfg *config.SkillsRuntimeConfig) skillsapi.ScopeResolverConfig {
	if cfg == nil {
		return skillsapi.ScopeResolverConfig{}
	}
	return skillsapi.ScopeResolverConfig{
		Enabled:          cfg.ScopeResolverEnabled,
		TenantHeaders:    append([]string(nil), cfg.TenantHeaders...),
		ProjectHeaders:   append([]string(nil), cfg.ProjectHeaders...),
		UserHeaders:      append([]string(nil), cfg.UserHeaders...),
		RoleHeaders:      append([]string(nil), cfg.RoleHeaders...),
		JWTClaimsEnabled: cfg.JWTClaimsEnabled,
		JWTSecret:        strings.TrimSpace(cfg.JWTSecret),
		TenantClaims:     append([]string(nil), cfg.TenantClaims...),
		ProjectClaims:    append([]string(nil), cfg.ProjectClaims...),
		UserClaims:       append([]string(nil), cfg.UserClaims...),
		RoleClaims:       append([]string(nil), cfg.RoleClaims...),
		AdminRoles:       append([]string(nil), cfg.AdminRoles...),
		APIKeyScopes:     buildSkillsScopeBindings(cfg.APIKeyScopes),
	}
}

func configuredMCPConfigPath(cfg *config.Config) string {
	if cfg == nil || cfg.AICLI == nil {
		return ""
	}
	// Same priority as the runtime config and the CLI: ./.aicli/mcp.yaml >
	// ~/.aicli/mcp.yaml > explicit override > upward search > configs/mcp.yaml.
	// An unset aicli.mcp.config_file is discovery mode: the workspace layer wins
	// without the key being written first (MCP_CONFIG_FILE 环境变量优先，见
	// agentconfig.EffectiveAICLIMCPConfigFile)。
	return aiclipaths.ResolveMCPConfigPath(config.EffectiveAICLIMCPConfigFile(cfg))
}

// loadRuntimeMCPManagerConfig 让 runtime-server 的 MCP manager 按发现链分层加载：
// 低优先级文件提供基础项，高优先级文件同名整体覆盖（§4.5 Step 1）。
// 显式 aicli.mcp.config_file / MCP_CONFIG_FILE 仍走精确加载。
func loadRuntimeMCPManagerConfig(manager mcpmanager.Manager, cfg *config.Config) error {
	explicit := ""
	if cfg != nil {
		explicit = config.EffectiveAICLIMCPConfigFile(cfg)
	}
	if loader, ok := manager.(mcpmanager.LayeredConfigLoader); ok && loader != nil {
		return loader.LoadConfigEffective(explicit)
	}
	// 兼容实现（无分层能力）：退回单文件加载，保持既有行为。
	return manager.LoadConfig(configuredMCPConfigPath(cfg))
}

// resolveRuntimeMCPConfigPath 解析 runtime-server 实际使用的 MCP 配置路径：
// 优先已存在的解析结果；都不存在时落到用户级 ~/.aicli/mcp.yaml（由 admin 包自动创建），
// 避免 runtime-server 在任意工作目录下生成 configs/mcp.yaml。
func resolveRuntimeMCPConfigPath(cfg *config.Config) string {
	return resolveRuntimeMCPConfigResolution(cfg).Path
}

// resolveRuntimeMCPConfigResolution 在 resolveRuntimeMCPConfigPath 的基础上保留
// 命中来源（explicit/project/user/upward/executable/default）与候选清单，供启动日志
// 与管理接口观测使用。
func resolveRuntimeMCPConfigResolution(cfg *config.Config) aiclipaths.MCPConfigResolution {
	if cfg == nil || cfg.AICLI == nil {
		return aiclipaths.MCPConfigResolution{}
	}
	return applyMCPUserFallback(aiclipaths.ResolveMCPConfigPathDetailed(config.EffectiveAICLIMCPConfigFile(cfg)))
}

// applyMCPUserFallback 在解析结果不存在时把模板约定默认值（相对 configs/mcp.yaml）
// 改落用户级 ~/.aicli/mcp.yaml（由 admin 包自动创建），避免 runtime-server 在任意
// 工作目录下生成 configs/mcp.yaml；其它非默认路径（含显式覆盖）按用户指定位置创建。
//
// 该规则不依赖文件系统层级，可脱离向上搜索单独验证。
func applyMCPUserFallback(resolution aiclipaths.MCPConfigResolution) aiclipaths.MCPConfigResolution {
	if resolution.Path == "" {
		return resolution
	}
	if _, err := os.Stat(resolution.Path); err == nil {
		return resolution
	}
	if !filepath.IsAbs(resolution.Path) && filepath.ToSlash(resolution.Path) == aiclipaths.DefaultMCPConfigRelativePath {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			resolution.Path = filepath.Join(home, ".aicli", "mcp.yaml")
			resolution.Source = "user-fallback"
		}
	}
	return resolution
}

func defaultProfile(cfg *config.Config) string {
	if cfg == nil || cfg.Profiles == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Profiles.DefaultProfile)
}

func resolvedExtraSkillDirs(cfg *config.SkillsRuntimeConfig) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]struct{})
	dirs := make([]string, 0, len(cfg.SkillDirs)+len(cfg.ExtraSkillDirs))
	addDir := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || value == strings.TrimSpace(cfg.SkillDir) {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		dirs = append(dirs, value)
	}
	for _, dir := range cfg.SkillDirs {
		addDir(dir)
	}
	for _, dir := range cfg.ExtraSkillDirs {
		addDir(dir)
	}
	if configFile := strings.TrimSpace(cfg.ConfigFile); configFile != "" {
		if resolvedConfigFile := runtimeserver.ResolveUpwardPath(configFile); strings.TrimSpace(resolvedConfigFile) != "" {
			for _, dir := range runtimeskill.DiscoverCodexCompatibleSkillDirs(filepath.Dir(resolvedConfigFile), resolvedConfigFile) {
				addDir(dir)
			}
		}
	}
	return dirs
}

func allConfiguredSkillDirs(cfg *config.SkillsRuntimeConfig) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, 1+len(cfg.SkillDirs)+len(cfg.ExtraSkillDirs)+6)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if trimmed := strings.TrimSpace(cfg.SkillDir); trimmed != "" {
		add(trimmed)
	}
	for _, dir := range resolvedExtraSkillDirs(cfg) {
		add(dir)
	}
	return result
}

func runtimeInfoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":             true,
		"status":         "ok",
		"service":        "ai-agent-runtime",
		"execution_core": runtimechat.SessionActorRuntimeCore(),
		"backend":        buildinfo.Backend(),
		"frontend":       webui.Provenance(),
		"routes":         []string{"/api/agent/chat", "/api/runtime"},
	})
}

func buildSkillsUsageQuotaLimits(configured map[string]config.SkillsRuntimeQuotaLimit) map[string]skillsapi.UsageQuotaLimit {
	if len(configured) == 0 {
		return nil
	}
	limits := make(map[string]skillsapi.UsageQuotaLimit, len(configured))
	for key, value := range configured {
		limits[key] = skillsapi.UsageQuotaLimit{
			MaxRequests: value.MaxRequests,
			MaxTokens:   value.MaxTokens,
		}
	}
	return limits
}

func buildSkillsScopeBindings(configured map[string]config.SkillsRuntimeScopeBinding) map[string]skillsapi.UsageScope {
	if len(configured) == 0 {
		return nil
	}
	bindings := make(map[string]skillsapi.UsageScope, len(configured))
	for key, value := range configured {
		bindings[key] = skillsapi.UsageScope{
			TenantID:  value.TenantID,
			ProjectID: value.ProjectID,
			UserID:    value.UserID,
		}
	}
	return bindings
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneResponseMarkerRules(input []config.ResponseMarkerRule) []config.ResponseMarkerRule {
	if len(input) == 0 {
		return nil
	}
	output := make([]config.ResponseMarkerRule, len(input))
	for i, rule := range input {
		output[i] = config.ResponseMarkerRule{
			Models:  append([]string(nil), rule.Models...),
			Markers: append([]string(nil), rule.Markers...),
		}
	}
	return output
}

func cloneProviderModelCapabilities(input map[string]config.ModelCapabilitySpec) map[string]config.ModelCapabilitySpec {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]config.ModelCapabilitySpec, len(input))
	for key, value := range input {
		cloned := value
		if len(value.InputModalities) > 0 {
			cloned.InputModalities = append([]string(nil), value.InputModalities...)
		}
		output[key] = cloned
	}
	return output
}

func cloneHeaderMappingRules(input []config.HeaderMappingRule) []runtimellm.HeaderMappingRule {
	if len(input) == 0 {
		return nil
	}
	output := make([]runtimellm.HeaderMappingRule, len(input))
	for i, rule := range input {
		output[i] = runtimellm.HeaderMappingRule{
			Name:         rule.Name,
			Enabled:      rule.Enabled,
			Header:       rule.Header,
			TargetHeader: rule.TargetHeader,
			MatchType:    rule.MatchType,
			Match:        rule.Match,
			Value:        rule.Value,
		}
	}
	return output
}
