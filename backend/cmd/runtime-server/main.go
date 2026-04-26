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
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	"go.uber.org/zap/zapcore"
)

const defaultRuntimeServerConfigPath = "./configs/config.yaml"

type runtimeServerCommandOptions struct {
	ConfigPath string
	ListenAddr string
	PIDFile    string
	PID        int
	Wait       time.Duration
}

func main() {
	os.Exit(run())
}

func run() int {
	loadEnv()

	args := os.Args[1:]
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "help", "-h", "--help":
			printRuntimeServerRootUsage()
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
	fmt.Fprintln(os.Stdout, "Usage:")
	fmt.Fprintln(os.Stdout, "  runtime-server serve  [--config PATH] [--listen HOST:PORT] [--pid-file PATH]")
	fmt.Fprintln(os.Stdout, "  runtime-server start  [--config PATH] [--listen HOST:PORT] [--pid-file PATH] [--wait 30s]")
	fmt.Fprintln(os.Stdout, "  runtime-server stop   [--pid-file PATH] [--pid PID] [--wait 10s]")
	fmt.Fprintln(os.Stdout, "  runtime-server status [--config PATH] [--listen HOST:PORT] [--pid-file PATH]")
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "Notes:")
	fmt.Fprintln(os.Stdout, "  - 不带子命令时，等价于 `serve`，以前的启动方式保持兼容。")
	fmt.Fprintln(os.Stdout, "  - `start` 会在后台启动服务并写入 PID 文件。")
	fmt.Fprintln(os.Stdout, "  - `stop` 优先使用 PID 文件停止受管实例，也支持 `--pid` 直接停止指定进程。")
	fmt.Fprintln(os.Stdout, "  - 默认 PID 文件为 ./logs/runtime-server.pid。")
}

func newRuntimeServerFlagSet(name string) *pflag.FlagSet {
	flags := pflag.NewFlagSet(name, pflag.ContinueOnError)
	flags.SetOutput(os.Stdout)
	return flags
}

func parseServeOptions(args []string) (runtimeServerCommandOptions, error) {
	opts := runtimeServerCommandOptions{
		ConfigPath: defaultRuntimeServerConfigPath,
		PIDFile:    runtimeserver.DefaultPIDFile,
	}
	flags := newRuntimeServerFlagSet("runtime-server serve")
	flags.StringVarP(&opts.ConfigPath, "config", "c", opts.ConfigPath, "配置文件路径")
	flags.StringVar(&opts.ListenAddr, "listen", "", "监听地址，优先级高于配置文件，例如 127.0.0.1:8101")
	flags.StringVar(&opts.PIDFile, "pid-file", opts.PIDFile, "PID 文件路径")
	return opts, flags.Parse(args)
}

func parseStartOptions(args []string) (runtimeServerCommandOptions, error) {
	opts := runtimeServerCommandOptions{
		ConfigPath: defaultRuntimeServerConfigPath,
		PIDFile:    runtimeserver.DefaultPIDFile,
		Wait:       30 * time.Second,
	}
	flags := newRuntimeServerFlagSet("runtime-server start")
	flags.StringVarP(&opts.ConfigPath, "config", "c", opts.ConfigPath, "配置文件路径")
	flags.StringVar(&opts.ListenAddr, "listen", "", "监听地址，优先级高于配置文件，例如 127.0.0.1:8101")
	flags.StringVar(&opts.PIDFile, "pid-file", opts.PIDFile, "PID 文件路径")
	flags.DurationVar(&opts.Wait, "wait", opts.Wait, "等待后台进程完成启动的超时时间")
	return opts, flags.Parse(args)
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
		ConfigPath: defaultRuntimeServerConfigPath,
		PIDFile:    runtimeserver.DefaultPIDFile,
	}
	flags := newRuntimeServerFlagSet("runtime-server status")
	flags.StringVarP(&opts.ConfigPath, "config", "c", opts.ConfigPath, "配置文件路径")
	flags.StringVar(&opts.ListenAddr, "listen", "", "监听地址，优先级高于配置文件，例如 127.0.0.1:8101")
	flags.StringVar(&opts.PIDFile, "pid-file", opts.PIDFile, "PID 文件路径")
	return opts, flags.Parse(args)
}

func resolveRuntimeServerConfigPath(configPath string) string {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		configPath = defaultRuntimeServerConfigPath
	}

	cleaned := filepath.Clean(configPath)
	candidates := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	appendCandidate := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		candidates = append(candidates, path)
	}

	appendCandidate(cleaned)
	if !filepath.IsAbs(cleaned) {
		relative := strings.TrimPrefix(cleaned, "."+string(filepath.Separator))
		normalizedRelative := filepath.ToSlash(relative)
		switch {
		case normalizedRelative == "" || normalizedRelative == ".":
		case strings.HasPrefix(normalizedRelative, "backend/"):
			appendCandidate(filepath.FromSlash(strings.TrimPrefix(normalizedRelative, "backend/")))
		default:
			appendCandidate(filepath.Join("backend", relative))
		}
	}

	for _, candidate := range candidates {
		resolved := runtimeserver.ResolveUpwardPath(candidate)
		if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
			if absolutePath, absErr := filepath.Abs(resolved); absErr == nil {
				return absolutePath
			}
			return resolved
		}
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

	pidFile := runtimeserver.ResolvePIDFilePath(opts.PIDFile)
	if info, err := runtimeserver.ReadInstanceInfo(pidFile); err == nil {
		if runtimeserver.ProcessRunning(info.PID) {
			logger.Error("Runtime server already running",
				logger.Int("pid", info.PID),
				logger.String("pid_file", pidFile),
				logger.String("listen", info.ListenAddr),
			)
			return 1
		}
		_ = runtimeserver.RemoveInstanceInfoIfPID(pidFile, info.PID)
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
	if info, err := runtimeserver.ReadInstanceInfo(pidFile); err == nil {
		if runtimeserver.ProcessRunning(info.PID) {
			fmt.Fprintf(os.Stdout, "runtime-server 已在运行: pid=%d listen=%s pid_file=%s\n", info.PID, strings.TrimSpace(info.ListenAddr), pidFile)
			return 1
		}
		_ = runtimeserver.RemoveInstanceInfoIfPID(pidFile, info.PID)
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

	commandArgs := []string{"serve", "--config", opts.ConfigPath, "--pid-file", pidFile}
	if strings.TrimSpace(opts.ListenAddr) != "" {
		commandArgs = append(commandArgs, "--listen", strings.TrimSpace(opts.ListenAddr))
	}
	launchCommand, launchArgs, err := runtimeserver.PrepareStartCommand(executable, cwd, commandArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to prepare runtime-server start command: %v\n", err)
		return 1
	}

	env := ensureEnvDefault(os.Environ(), "LOG_OUTPUT", "file")
	env = ensureEnvDefault(env, "LOG_ENABLE_CONSOLE", "false")

	cmd, err := runtimeserver.StartDetachedProcess(launchCommand, launchArgs, env)
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
		if info, err := runtimeserver.ReadInstanceInfo(pidFile); err == nil && info.PID > 0 && runtimeserver.ProcessRunning(info.PID) {
			if runtimeServerReady(info.ListenAddr, 500*time.Millisecond) {
				fmt.Fprintf(os.Stdout, "runtime-server 已启动: pid=%d listen=%s pid_file=%s\n", info.PID, strings.TrimSpace(info.ListenAddr), pidFile)
				return 0
			}
		}

		select {
		case err := <-exitCh:
			if err == nil {
				fmt.Fprintf(os.Stderr, "runtime-server 进程在写入 PID 文件前已退出，请检查日志文件。\n")
			} else {
				fmt.Fprintf(os.Stderr, "runtime-server 启动失败，请检查日志文件: %v\n", err)
			}
			return 1
		case <-deadline.C:
			fmt.Fprintf(os.Stderr, "runtime-server 启动超时，未在 %s 内写入 PID 文件: %s\n", opts.Wait, pidFile)
			return 1
		case <-ticker.C:
		}
	}
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
		targetPID = info.PID
	}

	if !runtimeserver.ProcessRunning(targetPID) {
		_ = runtimeserver.RemoveInstanceInfoIfPID(pidFile, targetPID)
		fmt.Fprintf(os.Stdout, "runtime-server 已停止: pid=%d\n", targetPID)
		return 0
	}

	if err := runtimeserver.TerminateProcess(targetPID, opts.Wait); err != nil {
		fmt.Fprintf(os.Stderr, "停止 runtime-server 失败: %v\n", err)
		return 1
	}
	_ = runtimeserver.RemoveInstanceInfoIfPID(pidFile, targetPID)
	fmt.Fprintf(os.Stdout, "runtime-server 已停止: pid=%d\n", targetPID)
	return 0
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
		if runtimeserver.ProcessRunning(info.PID) {
			fmt.Fprintf(os.Stdout, "runtime-server 运行中: pid=%d listen=%s pid_file=%s\n", info.PID, strings.TrimSpace(info.ListenAddr), pidFile)
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
	router         *mux.Router
	handler        *skillsapi.Handler
	cfg            *config.Config
	skillsCfg      *config.SkillsRuntimeConfig
	runtimeManager *runtimecfg.RuntimeManager
	bootstrap      *runtimebootstrap.Manager
	mcpManager     mcpmanager.Manager
	ledgerStore    io.Closer
}

func newRuntimeServerApp(ctx context.Context, cfg *config.Config, configPath string) (*runtimeServerApp, error) {
	skillsCfg := normalizeSkillsRuntimeConfig(cfg)
	runtimeManager := runtimecfg.NewRuntimeManager(skillsCfg.ConfigFile)
	if err := runtimeManager.Load(); err != nil {
		return nil, fmt.Errorf("failed to load runtime config: %w", err)
	}
	runtimeConfig := runtimeManager.Get()
	runtimeConfig.Sessions.Dir = resolvePathFromConfigFile(
		runtimeManager.GetFilePath(),
		runtimeConfig.Sessions.Dir,
	)

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
	bootstrapManager.ApplyToSkillsHandler(handler)
	handler.SetRuntimeConfig(runtimeManager.Get(), runtimeManager.GetFilePath())
	handler.SetRuntimeLogFilePath(strings.TrimSpace(cfg.Log.FilePath))
	handler.SetRuntimeConfigResolver(func(scope skillsapi.UsageScope) *runtimecfg.RuntimeConfig {
		return runtimeManager.SelectConfigForScope(scope.ScopeKey)
	})
	handler.SetProfileSupport(skillsapi.ProfileSupportConfig{
		Registry:          profilesys.NewRegistryFromProfilesConfig(cfg.Profiles),
		DefaultProfile:    defaultProfile(cfg),
		GlobalRuntimePath: strings.TrimSpace(skillsCfg.ConfigFile),
		GlobalMCPPath:     configuredMCPConfigPath(cfg),
		GlobalSkillDirs:   allConfiguredSkillDirs(skillsCfg),
		MCPAutoConnect:    configuredMCPAutoConnect(cfg),
	})
	configDocumentService := runtimeserver.NewLocalConfigDocumentService(configPath)
	if configDocumentService != nil {
		configDocumentService.SetHotReloader(
			runtimeserver.NewRuntimeConfigHotReloader(handler, cfg, bootstrapManager),
		)
	}
	handler.SetConfigDocumentService(configDocumentService)
	if persister := runtimeserver.NewSkillsRuntimePolicyPersister(configPath, cfg); persister != nil {
		handler.SetAuthPolicyPersister(persister.PersistAuthPolicy)
		handler.SetUsagePolicyPersister(persister.PersistUsagePolicy)
		handler.SetMutationPolicyPersister(persister.PersistMutationPolicy)
	}
	applySkillsRuntimePolicies(handler, skillsCfg)
	ledgerStore, err := runtimeserver.BuildUsageLedgerStore(cfg)
	if err != nil {
		if manager != nil {
			_ = manager.Stop()
		}
		_ = bootstrapManager.Stop()
		return nil, err
	}
	if ledgerStore != nil {
		handler.SetUsageLedgerStore(ledgerStore)
	}

	router := mux.NewRouter()
	router.HandleFunc("/", runtimeInfoHandler).Methods(http.MethodGet)
	router.HandleFunc("/healthz", runtimeInfoHandler).Methods(http.MethodGet)
	handler.RegisterRoutes(router)

	return &runtimeServerApp{
		router:         router,
		handler:        handler,
		cfg:            cfg,
		skillsCfg:      skillsCfg,
		runtimeManager: runtimeManager,
		bootstrap:      bootstrapManager,
		mcpManager:     manager,
		ledgerStore:    ledgerStore,
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
}

func loadEnv() {
	for _, path := range []string{".env", "./configs/.env"} {
		if err := godotenv.Load(path); err == nil {
			return
		}
	}
}

func normalizeSkillsRuntimeConfig(cfg *config.Config) *config.SkillsRuntimeConfig {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if cfg.SkillsRuntime == nil {
		cfg.SkillsRuntime = &config.SkillsRuntimeConfig{}
	}
	if strings.TrimSpace(cfg.SkillsRuntime.ConfigFile) == "" {
		cfg.SkillsRuntime.ConfigFile = "backend/configs/runtime.yaml"
	}
	if strings.TrimSpace(cfg.SkillsRuntime.SkillDir) == "" {
		cfg.SkillsRuntime.SkillDir = "./docs/skill_runtime/skills"
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
	cfg.SkillsRuntime.ConfigFile = runtimeserver.ResolveUpwardPath(cfg.SkillsRuntime.ConfigFile)
	cfg.SkillsRuntime.SkillDir = runtimeserver.ResolveUpwardPath(cfg.SkillsRuntime.SkillDir)
	cfg.SkillsRuntime.SkillDirs = runtimeserver.ResolveUpwardPaths(cfg.SkillsRuntime.SkillDirs)
	cfg.SkillsRuntime.ExtraSkillDirs = runtimeserver.ResolveUpwardPaths(cfg.SkillsRuntime.ExtraSkillDirs)
	return cfg.SkillsRuntime
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

	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.MCP != nil && strings.TrimSpace(cfg.AICLI.MCP.ConfigFile) != "" && cfg.AICLI.MCP.AutoConnect {
		cfg.AICLI.MCP.ConfigFile = runtimeserver.ResolveUpwardPath(cfg.AICLI.MCP.ConfigFile)

		manager = mcpmanager.NewManager()
		if err := manager.LoadConfig(cfg.AICLI.MCP.ConfigFile); err != nil {
			return nil, nil, fmt.Errorf("failed to load MCP config: %w", err)
		}
		if err := manager.Start(ctx); err != nil {
			return nil, nil, fmt.Errorf("failed to start MCP manager: %w", err)
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
		maxRetries := cfg.Providers.MaxRetries
		if maxRetries <= 0 && cfg.Retry != nil && cfg.Retry.DefaultMaxRetries > 0 {
			maxRetries = cfg.Retry.DefaultMaxRetries
		}
		if maxRetries <= 0 {
			maxRetries = 3
		}

		providerConfigs[name] = &runtimellm.ProviderConfig{
			Type:               providerType,
			APIKey:             provider.GetAPIKey(),
			BaseURL:            provider.BaseURL,
			APIPath:            provider.APIPath,
			Timeout:            timeout,
			MaxRetries:         maxRetries,
			RetryTuning:        retryTuning,
			RetryRules:         retryRules,
			DefaultModel:       provider.DefaultModel,
			SupportedModels:    append([]string(nil), provider.SupportedModels...),
			ModelMappings:      cloneStringMap(provider.ModelMappings),
			ModelCapabilities:  cloneProviderModelCapabilities(provider.ModelCapabilities),
			Headers:            cloneStringMap(provider.Headers),
			HeaderMappings:     cloneStringMap(provider.HeaderMappings),
			HeaderMappingRules: cloneHeaderMappingRules(provider.HeaderMappingRules),
			Proxy:              config.EffectiveProxyConfig(&cfg.Providers.Proxy, provider.Proxy),
		}
	}

	return providerConfigs
}

func buildLLMRetryTuning(cfg *config.Config) runtimellm.RetryTuning {
	if cfg == nil {
		return runtimellm.RetryTuning{}
	}
	tuning := runtimellm.RetryTuning{
		BaseDelay:  cfg.Providers.Backoff.InitialInterval,
		MaxDelay:   cfg.Providers.Backoff.MaxInterval,
		Multiplier: cfg.Providers.Backoff.Multiplier,
	}
	if cfg.Retry != nil {
		if tuning.BaseDelay <= 0 && cfg.Retry.DefaultRetryDelayMS > 0 {
			tuning.BaseDelay = time.Duration(cfg.Retry.DefaultRetryDelayMS) * time.Millisecond
		}
		if tuning.Multiplier < 1 && cfg.Retry.DefaultBackoffMultiplier >= 1 {
			tuning.Multiplier = cfg.Retry.DefaultBackoffMultiplier
		}
	}
	return tuning
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
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.MCP == nil {
		return ""
	}
	return strings.TrimSpace(cfg.AICLI.MCP.ConfigFile)
}

func configuredMCPAutoConnect(cfg *config.Config) bool {
	return cfg != nil && cfg.AICLI != nil && cfg.AICLI.MCP != nil && cfg.AICLI.MCP.AutoConnect
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
	return dirs
}

func allConfiguredSkillDirs(cfg *config.SkillsRuntimeConfig) []string {
	if cfg == nil {
		return nil
	}
	result := make([]string, 0, 1+len(cfg.SkillDirs)+len(cfg.ExtraSkillDirs))
	if trimmed := strings.TrimSpace(cfg.SkillDir); trimmed != "" {
		result = append(result, trimmed)
	}
	result = append(result, resolvedExtraSkillDirs(cfg)...)
	return result
}

func runtimeInfoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":      true,
		"service": "ai-agent-runtime",
		"routes":  []string{"/api/agent/chat", "/api/runtime"},
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
