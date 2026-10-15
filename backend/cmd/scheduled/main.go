// Command scheduled 是"自动化任务"（Schedule）的调度守护进程。
//
// 装配（S1 联盟式调度后）：加载配置 → 打开共享 schedule.sqlite → mesh 入网
// （无 endpoint 档案 + scheduler 能力位）→ 以 mesh 租约竞争"调度领导权"
// （schedulehost.Leader）→ 持租约者驱动单定时器运行时；失租/被接管自动回到
// standby（待命而非退出），任一在岗节点在 TTL 内接管
// （见 docs/automation-tasks/mesh-scheduler-federation-design.md §3.2/§12.2.1）。
//
// 到点投递走 delegate → embedded → mailbox 三级兜底
// （见 docs/automation-tasks/scheduler-daemon-design.md §6）。
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
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
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
	logFile := fs.String("log-file", "", "运行期日志文件（默认 <home>/.aicli/logs/scheduled.log）")
	command, err := parseCommandFlags(fs, args)
	if err != nil {
		return err
	}
	// S0 收尾：运行期日志落盘（服务模式 stderr 不可见）；打开失败只警告。
	logPath := strings.TrimSpace(*logFile)
	if logPath == "" {
		logPath = defaultDaemonLogPath()
	}
	initDaemonLog(logPath)
	defer closeDaemonLog()

	// 子命令路由：run = 显式前台（等价于无参数）；doctor = 自检；
	// install/uninstall/start/stop/status = 服务管理（需管理员）。
	switch command {
	case "":
		// 无子命令：等价 run（历史行为）。
	case "run":
		// 显式前台：继续下面的前台装配。
	case "doctor":
		return runDoctorCommand(*configPath, *storePath, *dataDir, *runtimeServer)
	default:
		return runServiceCommand(command, *configPath)
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
			logf("scheduled: warning: shared session lease store unavailable (%v); embedded execution stays fail-closed", leaseErr)
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
			logf("scheduled: warning: embedded executor unavailable (%v); embedded stays disabled", cfgErr)
		} else if headless, headlessErr := buildHeadlessExecutor(runtimeConfig, *configPath); headlessErr != nil {
			logf("scheduled: warning: embedded executor unavailable (%v); embedded stays disabled", headlessErr)
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

	// S1（M4）：daemon 以**无 endpoint** 的 mesh 节点入网——节点档案负责在岗
	// 可见性（capabilities 含 scheduler），不注册 HTTP 端点（只被看见、不被
	// mesh.Call 调用）；mesh 不可用只降级 warning（与 aicli/runtime-server 同纪律）。
	meshHost := startDaemonMeshHost()
	defer stopDaemonMeshHost(meshHost)

	service := schedule.NewService(store, schedule.SystemClock)
	// S2：投递走"就近优先"组合（L1 本进程 hub → L2 目标节点 → L3 HTTP delegate）。
	// daemon 没有会话 hub（L1 为空）；L2 = mesh.Call schedule.deliver（目标会话
	// 宿主优先，S2b）；L3 = 既有 HTTP delegate。全部携带 occurrence 键。
	var peerDeliverer schedule.Deliverer
	if peer, peerErr := schedulehost.NewPeerDeliverer(schedulehost.PeerDelivererOptions{
		CallerNodeID: meshHost.NodeID(),
		Logf:         logf,
	}); peerErr != nil {
		logf("scheduled: warning: peer delivery unavailable (%v); falling back to HTTP delegate", peerErr)
	} else {
		peerDeliverer = peer
	}
	deliverer, err := schedulehost.NewLocalFirstDeliverer(schedulehost.LocalFirstOptions{
		Peer: peerDeliverer,
		HTTP: &schedule.HTTPDeliverer{BaseURL: cfg.Execution.RuntimeServerURL},
		Logf: logf,
	})
	if err != nil {
		return fmt.Errorf("schedule deliverer: %w", err)
	}

	// S1：调度领导权 = mesh 可抢占角色（联盟式调度）。daemon 仍是默认常驻成员
	// 与第一优先级；持租约者驱动 Runtime，失租/被接管回到 standby（不再退出），
	// 由任一在岗成员接管（见联盟设计 §12.2.1）。
	if !cfg.DriverAllows(config.ScheduleRoleDaemon) {
		return fmt.Errorf("schedule.driver.participants excludes %q; refusing to run as a schedule driver", config.ScheduleRoleDaemon)
	}
	leader, err := schedulehost.NewLeader(schedulehost.LeaderOptions{
		Store:             store,
		Service:           service,
		Deliverer:         deliverer,
		MaxAttempts:       cfg.MaxDeliveryAttempts,
		FallbackToMailbox: cfg.Execution.FallbackToMailbox,
		// S4b（§4.4）：一次性任务补发宽限窗口（0=总是补发）。
		MisfireGrace: cfg.MisfireGrace,
		// 执行模式：delegate 永不内嵌（即使未来注入 Executor）；embedded/auto 才内嵌。
		Mode: cfg.Execution.Mode,
		// E3.3：内嵌执行核（embedded/auto 时注入；delegate 为 nil）。
		Executor: embeddedExecutor,
		// E1：会话租约仲裁（与宿主同源，见 runtimehost-executor-extraction-plan.md §2.5a）。
		Arbiter: newDaemonSessionArbiter(sessionLeaseStore),
		// S3 版本握手（§4.2）：store 被更新的二进制升级后本进程不再理解，
		// 立即放弃/退出领导权并标记 incompatible，让合格节点接管。
		StoreCompatCheck: schedulehost.StoreCompatCheck(store.SchemaVersion),
		Identity: schedulehost.DriverIdentity{
			NodeID:   meshHost.NodeID(),
			Kind:     schedulehost.RoleDaemon,
			Priority: cfg.DriverPriority(config.ScheduleRoleDaemon),
		},
		LeaseTTL:     cfg.Driver.LeaseTTL,
		StandbyRetry: cfg.Driver.StandbyRetry,
		// daemon 默认 degraded=standalone（历史行为）；配置显式覆盖优先。
		Degraded: schedulehost.DegradedPolicy(cfg.DriverDegradedPolicy(config.ScheduleRoleDaemon)),
		Logf:     logf,
	})
	if err != nil {
		return fmt.Errorf("schedule driver: %w", err)
	}

	// D3：loopback 运维端点（127.0.0.1:8919）。
	status := &statusServer{
		store:         store,
		leader:        leader, // P6/S1：/status 携带 driver 状态与 metrics 快照
		mode:          cfg.Execution.Mode,
		runtimeServer: cfg.Execution.RuntimeServerURL,
		startedAt:     time.Now(),
	}
	stopStatus, err := status.Serve(8919)
	if err != nil {
		logf("scheduled: warning: status endpoint unavailable: %v", err)
	} else {
		defer stopStatus()
	}

	// runUntil 启动领导权监督循环，直到外部 stop（服务控制/进程信号）。
	runUntil := func(stop <-chan struct{}) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		leader.Start(ctx)
		<-stop
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		leader.Stop(stopCtx)
		stopCancel()
	}

	// Windows 服务模式：由 SCM 启动时改走服务循环（不装 signal handler）。
	if err := runAsWindowsService(runUntil); err == nil {
		return nil
	} else if err != errNotService {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	leader.Start(ctx)
	logf("scheduled: running (store=%s, mode=%s, runtime-server=%s, driver=%s)",
		path, cfg.Execution.Mode, cfg.Execution.RuntimeServerURL, leader.Status().State)
	<-ctx.Done()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	leader.Stop(stopCtx)
	stopCancel()
	return nil
}

// parseCommandFlags 解析"子命令 + flags"的两种书写顺序，返回子命令名
// （空串 = 无子命令，等价 run）：
//
//	scheduled run  --store X   （命令在前）
//	scheduled --store X run    （flags 在前）
//
// 为什么必须显式剥离子命令：Go flag 在首个非 flag 参数处停止解析，若不先
// 剥离 `run`，`run --store X` 的 --store 会被**静默丢弃**、daemon 落回默认
// store（2026-10-14 冒烟事故根因）。多余的位置参数一律报错，不再静默吞掉。
func parseCommandFlags(fs *flag.FlagSet, args []string) (string, error) {
	command := ""
	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(strings.TrimSpace(args[0]), "-") {
		command = strings.TrimSpace(args[0])
		parseArgs = args[1:]
	}
	if err := fs.Parse(parseArgs); err != nil {
		return "", err
	}
	if command == "" && fs.NArg() > 0 {
		command = fs.Arg(0)
	}
	remainder := fs.Args()
	if command != "" && len(remainder) > 0 && remainder[0] == command {
		remainder = remainder[1:]
	}
	if len(remainder) > 0 {
		return "", fmt.Errorf("unexpected arguments: %s", strings.Join(remainder, " "))
	}
	return command, nil
}

// runServiceCommand 处理 install/uninstall/start/stop/status；
// 无参数时是普通前台运行。服务模式（SCM 启动）由 runAsWindowsService 检测。
func runServiceCommand(command, configPath string) error {
	executable, _ := os.Executable()
	switch command {
	case "install":
		return installWindowsService(executable, configPath)
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
