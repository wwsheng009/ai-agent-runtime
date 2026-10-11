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
	"github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
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

	// 服务管理子命令（需管理员）：install/uninstall/start/stop/status。
	if fs.NArg() > 0 {
		return runServiceCommand(fs.Arg(0))
	}

	cfg := config.DefaultScheduleConfig()
	if strings.TrimSpace(*configPath) != "" {
		manager := config.NewRuntimeManager(*configPath)
		if err := manager.Load(); err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		cfg = manager.Get().Schedule.WithDefaults()
	} else {
		cfg = cfg.WithDefaults()
	}
	if strings.TrimSpace(*runtimeServer) != "" {
		cfg.Execution.RuntimeServerURL = strings.TrimSpace(*runtimeServer)
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

	service := schedule.NewService(store, schedule.SystemClock)
	deliverer := &schedule.HTTPDeliverer{BaseURL: cfg.Execution.RuntimeServerURL}
	runtime := schedule.NewRuntime(schedule.RuntimeOptions{
		Store:             store,
		Service:           service,
		Deliverer:         deliverer,
		MaxAttempts:       cfg.MaxDeliveryAttempts,
		FallbackToMailbox: cfg.Execution.FallbackToMailbox,
		// E1：session 租约仲裁（delegate 失败且会话空闲时允许 embedded 路径；
		// Executor 尚未注入——E0 完整执行栈见 implementation-status §5）。
		Executor: nil,
		Arbiter:  newDaemonSessionArbiter(mesh.ResolvePaths()),
		Logf:     func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})

	release := acquireSchedulerLease()
	defer release()

	// D3：loopback 运维端点（127.0.0.1:8919）。
	status := &statusServer{
		store:         store,
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

	// Windows 服务模式：由 SCM 启动时改走服务循环（不装 signal handler）。
	if err := runAsWindowsService(func(stop <-chan struct{}) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runtime.Start(ctx)
		<-stop
		runtime.Stop()
	}); err == nil {
		return nil
	} else if err != errNotService {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runtime.Start(ctx)
	fmt.Fprintf(os.Stderr, "scheduled: running (store=%s, mode=%s, runtime-server=%s)\n",
		path, cfg.Execution.Mode, cfg.Execution.RuntimeServerURL)
	<-ctx.Done()
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
		return fmt.Errorf("unknown command %q (supported: install, uninstall, start, stop, status)", command)
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

// acquireSchedulerLease 获取 mesh 单实例租约；不可用时降级为无互斥运行（返回空释放函数）。
func acquireSchedulerLease() func() {
	paths := mesh.ResolvePaths()
	if strings.TrimSpace(paths.Root) == "" {
		fmt.Fprintln(os.Stderr, "scheduled: warning: mesh root unavailable; running without single-instance mutual exclusion")
		return func() {}
	}
	owner := mesh.LeaseOwner{NodeID: fmt.Sprintf("scheduled-%d", os.Getpid()), PID: os.Getpid()}
	outcome := mesh.Acquire(paths, "schedule-scheduler", "global", owner, mesh.AcquireOptions{TTL: 30 * time.Second})
	if outcome.Err != nil || !outcome.Acquired {
		fmt.Fprintf(os.Stderr, "scheduled: warning: scheduler lease not acquired (%s); another instance may be active\n", outcome.Reason)
		return func() {}
	}
	return func() { mesh.Release(paths, "schedule-scheduler", "global", owner) }
}
