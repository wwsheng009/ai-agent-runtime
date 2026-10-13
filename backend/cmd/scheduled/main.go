// Command scheduled 是"自动化任务"（Schedule）的调度守护进程。
//
// P0（前台运行）：加载配置 → 打开共享 schedule.sqlite → 获取 mesh 单实例租约 →
// 启动单定时器运行时 → 到点经 runtime-server 的会话命令端点投递提醒。
//
// 后续阶段（见 docs/automation-tasks/scheduler-daemon-design.md）将补：
// 服务化安装（Windows Service / systemd）、持久 mailbox 兜底、内嵌执行、运维端点。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "scheduled:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("scheduled", flag.ContinueOnError)
	configPath := fs.String("config", "", "配置文件路径（可选；读取 schedule.* 段）")
	storePath := fs.String("store", "", "schedule.sqlite 路径（覆盖配置）")
	runtimeServer := fs.String("runtime-server", "", "delegate 目标 base URL（覆盖配置）")
	dataDir := fs.String("data-dir", "", "数据目录（默认 <auth-dir>/data/schedule）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 子命令路由：run = 显式前台（等价于无参数）；doctor = 自检；
	// install/uninstall/start/stop/status = 服务管理（需管理员）。
	if fs.NArg() > 0 {
		switch command := fs.Arg(0); command {
		case "run":
			// 显式前台：继续下面的前台装配。
		case "doctor":
			return runDoctorCommand(*configPath, *storePath, *dataDir, *runtimeServer)
		default:
			return runServiceCommand(command)
		}
	}

	cfg, err := loadScheduleConfig(*configPath, *runtimeServer)
	if err != nil {
		return err
	}

	path, err := resolveStorePath(*storePath, *dataDir, cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create schedule data dir: %w", err)
	}

	store, err := schedule.OpenStore(path)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	// E1：会话执行仲裁必须用**与宿主同一把** chat.SessionLeaseStore（共享
	// session_runtime.sqlite），而不是 internal/mesh 文件租约（见 §2.5a）。
	// 仅在允许内嵌时打开；未配置/打不开 → fail-closed（store=nil，不内嵌）。
	var sessionLeaseStore chat.SessionLeaseStore
	if schedule.ModeAllowsEmbedded(cfg.Execution.Mode) {
		leaseStore, closeLeaseStore, leaseErr := openSharedSessionLeaseStore(*configPath)
		if leaseErr != nil {
			fmt.Fprintf(os.Stderr, "scheduled: warning: shared session lease store unavailable (%v); embedded execution stays fail-closed\n", leaseErr)
		} else {
			sessionLeaseStore = leaseStore
			defer closeLeaseStore()
		}
	}

	// E3.3：内嵌执行核（仅 embedded/auto 构建；delegate 保持零装配、零行为变化）。
	// 任一步失败 → fail-closed（不内嵌，退回 delegate/mailbox）。
	var embeddedExecutor schedule.SessionExecutor
	if schedule.ModeAllowsEmbedded(cfg.Execution.Mode) {
		runtimeConfig, cfgErr := loadRuntimeConfig(*configPath)
		if cfgErr != nil {
			fmt.Fprintf(os.Stderr, "scheduled: warning: embedded executor unavailable (%v); embedded stays disabled\n", cfgErr)
		} else if headless, headlessErr := buildHeadlessExecutor(runtimeConfig, *configPath); headlessErr != nil {
			fmt.Fprintf(os.Stderr, "scheduled: warning: embedded executor unavailable (%v); embedded stays disabled\n", headlessErr)
		} else {
			embeddedExecutor = scheduleExecutorAdapter{
				exec:           headless.executor,
				profile:        cfg.Execution.Profile,
				createSession:  headless.createSession,
				archiveSession: headless.archiveSession,
			}
			defer headless.close()
		}
	}

	service := schedule.NewService(store, schedule.SystemClock)
	deliverer := &schedule.HTTPDeliverer{BaseURL: cfg.Execution.RuntimeServerURL}
	runtime := schedule.NewRuntime(schedule.RuntimeOptions{
		Store:             store,
		Service:           service,
		Deliverer:         deliverer,
		MaxAttempts:       cfg.MaxDeliveryAttempts,
		FallbackToMailbox: cfg.Execution.FallbackToMailbox,
		// 执行模式：delegate 永不内嵌（即使未来注入 Executor）；embedded/auto 才内嵌。
		Mode: cfg.Execution.Mode,
		// E3.3：内嵌执行核（embedded/auto 时注入；delegate 为 nil）。
		Executor: embeddedExecutor,
		// E1：会话租约仲裁（与宿主同源，见 runtimehost-executor-extraction-plan.md §2.5a）。
		Arbiter: newDaemonSessionArbiter(sessionLeaseStore),
		Logf:    func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	// 单实例租约：续租 + 失租停止。另一实例在跑时本进程待命退出（不双投递）。
	lease, ok := newSchedulerLease()
	if !ok {
		return nil
	}
	defer lease.release()

	// D3：loopback 运维端点（127.0.0.1:8919）。
	status := &statusServer{
		store:         store,
		runtime:       runtime, // P6：/status 携带 metrics 快照
		mode:          cfg.Execution.Mode,
		runtimeServer: cfg.Execution.RuntimeServerURL,
		startedAt:     time.Now(),
	}
	stopStatus, err := status.Serve(8919)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scheduled: warning: status endpoint unavailable: %v\n", err)
	} else {
		defer stopStatus()
	}

	// runUntil 启动运行时，直到外部 stop 或租约被接管。
	runUntil := func(stop <-chan struct{}) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runtime.Start(ctx)
		select {
		case <-stop:
		case <-lease.Lost():
			fmt.Fprintln(os.Stderr, "scheduled: scheduler lease lost to another instance; stopping to avoid double delivery")
		}
		runtime.Stop()
	}

	// Windows 服务模式：由 SCM 启动时改走服务循环（不装 signal handler）。
	if err := runAsWindowsService(runUntil); err == nil {
		return nil
	} else if err != errNotService {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runtime.Start(ctx)
	fmt.Fprintf(os.Stderr, "scheduled: running (store=%s, mode=%s, runtime-server=%s)\n",
		path, cfg.Execution.Mode, cfg.Execution.RuntimeServerURL)
	select {
	case <-ctx.Done():
	case <-lease.Lost():
		fmt.Fprintln(os.Stderr, "scheduled: scheduler lease lost to another instance; stopping to avoid double delivery")
	}
	runtime.Stop()
	return nil
}

// runServiceCommand 处理 install/uninstall/start/stop/status；
// 无参数时是普通前台运行。服务模式（SCM 启动）由 runAsWindowsService 检测。
func runServiceCommand(command string) error {
	executable, _ := os.Executable()
	switch command {
	case "install":
		return installWindowsService(executable)
	case "uninstall":
		return uninstallWindowsService()
	case "start", "stop", "status":
		status, err := controlWindowsService(command)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, status)
		return nil
	default:
		return fmt.Errorf("unknown command %q (supported: run, doctor, install, uninstall, start, stop, status)", command)
	}
}

func resolveStorePath(flagStore, flagDataDir string, cfg config.ScheduleConfig) (string, error) {
	if path := strings.TrimSpace(flagStore); path != "" {
		return path, nil
	}
	if path := strings.TrimSpace(cfg.StorePath); path != "" {
		return path, nil
	}
	dir := strings.TrimSpace(flagDataDir)
	if dir == "" {
		base := filepath.Dir(agentconfig.DefaultAuthStorePath())
		if base == "." || base == "" {
			return "", fmt.Errorf("cannot resolve default data directory; pass --store or --data-dir")
		}
		dir = filepath.Join(base, "data", "schedule")
	}
	return filepath.Join(dir, "schedule.sqlite"), nil
}

// loadScheduleConfig 读取 schedule.* 配置（缺省回落内置默认），并应用
// --runtime-server 覆盖。
func loadScheduleConfig(configPath, runtimeServer string) (config.ScheduleConfig, error) {
	cfg := config.DefaultScheduleConfig()
	if strings.TrimSpace(configPath) != "" {
		manager := config.NewRuntimeManager(configPath)
		if err := manager.Load(); err != nil {
			return config.ScheduleConfig{}, fmt.Errorf("load config: %w", err)
		}
		cfg = manager.Get().Schedule.WithDefaults()
	} else {
		cfg = cfg.WithDefaults()
	}
	if strings.TrimSpace(runtimeServer) != "" {
		cfg.Execution.RuntimeServerURL = strings.TrimSpace(runtimeServer)
	}
	return cfg, nil
}

// loadRuntimeConfig 加载完整 runtime 配置（E3.3 内嵌执行核装配用）。
func loadRuntimeConfig(configPath string) (*config.RuntimeConfig, error) {
	if strings.TrimSpace(configPath) == "" {
		return nil, fmt.Errorf("no --config: runtime config is required for embedded execution")
	}
	manager := config.NewRuntimeManager(configPath)
	if err := manager.Load(); err != nil {
		return nil, fmt.Errorf("load runtime config: %w", err)
	}
	return manager.Get(), nil
}
