package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

// ============================================================================
// aicli 参与调度领导权（S5，opt-in）
//
// 默认不参战（schedule.driver.participants 默认 [daemon, runtime-server]）；
// 用户在配置里加入 "aicli" 后，本进程在**会话宿主挂载期间**以最低优先级参与：
//
//   - 只在没有更高优先级调度者（daemon > runtime-server）在岗时持租约
//     （schedulehost 的优先级让位 v2，DeferToHigherPriority）；
//   - 对方上线时主动释放租约（不等 TTL），避免交互进程与常驻调度者争抢。
//
// 投递组合：L1 当前会话注入 → L2 mesh 就近（其它节点）→ L3 HTTP delegate；
// 不装配内嵌执行核（aicli 的 embedded 复用属 E3.4，另行设计——拿不到执行核
// 时 Runtime 的 tryEmbedded 自然为 false，退回 mailbox 兜底）。
// ============================================================================

var chatScheduleDriver struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	leader *schedulehost.Leader
	// owned 是驱动自建的 service（远端 runtime-server 执行器模式下没有本地宿主）；
	// 停止时负责关闭其 store。复用本地宿主的 service 时保持 nil（宿主自己关）。
	owned *schedule.Service
}

// aicliLocalSessionDeliverer 把提醒注入本进程当前会话（L1）。目标会话不是本
// 进程会话时返回错误，让 LocalFirst 继续降级到 L2/L3。
type aicliLocalSessionDeliverer struct{}

func (aicliLocalSessionDeliverer) Deliver(_ context.Context, sessionID, text string) error {
	session := chatWebSession()
	if session == nil {
		return fmt.Errorf("no active chat session")
	}
	current := strings.TrimSpace(currentRuntimeSessionID(session))
	if current == "" {
		return fmt.Errorf("current session id unavailable")
	}
	target := strings.TrimSpace(sessionID)
	if target != "" && target != current {
		return fmt.Errorf("session %s is not served by this process (%s)", target, current)
	}
	if _, ok := injectChatWebPrompt(session, text); !ok {
		return fmt.Errorf("input queue unavailable")
	}
	return nil
}

// startChatScheduleDriver 在会话宿主挂载点尝试启动 aicli 调度者（幂等）。
// localService 为本地宿主的调度服务（远端 runtime-server 执行器模式下为 nil，
// 此时驱动自带 store/service）。未启用 / 未 opt-in / 缺 mesh 身份 / 已在运行时
// 静默跳过——绝不阻断会话。
func startChatScheduleDriver(runtimeCfg *config.RuntimeConfig, localService *schedule.Service) {
	if runtimeCfg == nil {
		return
	}
	if !runtimeCfg.Schedule.Enabled {
		// 只在显式 opt-in 后才吵：schedule 未启用是常态（默认配置）。
		return
	}
	sc := runtimeCfg.Schedule.WithDefaults()
	if !sc.DriverAllows(config.ScheduleRoleAICLI) {
		// opt-in：participants 未包含 aicli（默认如此）→ 不参战。
		fmt.Fprintln(os.Stderr, "schedule-driver: not opted in (schedule.driver.participants excludes \"aicli\"); aicli stays out of scheduling")
		return
	}
	host := mesh.Current()
	if host == nil {
		fmt.Fprintln(os.Stderr, "schedule-driver: mesh unavailable (--mesh=false?); aicli driver stays off")
		return
	}
	nodeID := strings.TrimSpace(host.NodeID())
	if nodeID == "" {
		fmt.Fprintln(os.Stderr, "schedule-driver: mesh node id unavailable; aicli driver stays off")
		return
	}
	service := localService
	var owned *schedule.Service
	if service == nil {
		// 远端 runtime-server 执行器模式下本地能力面整段不挂载：驱动自带一份
		// store/service（与宿主同库），退出时关闭。
		service = newLocalScheduleService(runtimeCfg, nil)
		owned = service
	}
	if service == nil {
		fmt.Fprintln(os.Stderr, "schedule-driver: schedule service unavailable; aicli driver stays off")
		return
	}
	store := service.Store()
	if store == nil {
		if owned != nil {
			_ = owned.Store().Close()
		}
		return
	}

	chatScheduleDriver.mu.Lock()
	defer chatScheduleDriver.mu.Unlock()
	if chatScheduleDriver.leader != nil {
		if owned != nil {
			_ = owned.Store().Close()
		}
		return // 已在运行（宿主重挂载时幂等）
	}

	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "schedule-driver: "+format+"\n", args...)
	}
	// L1：当前会话注入 + 交付账本守卫（driver 侧 occurrence 去重）。
	local := schedule.Deliverer(aicliLocalSessionDeliverer{})
	if ledger, ok := store.(schedule.OccurrenceLedger); ok {
		local = schedule.GuardedDeliverer{Inner: local, Ledger: ledger, Logf: logf}
	} else {
		logf("ledger unavailable (%T); occurrence dedup disabled on L1", store)
	}
	// L2：mesh 就近（其它节点）——失败不致命（LocalFirst 继续降级）。
	var peer schedule.Deliverer
	if p, err := schedulehost.NewPeerDeliverer(schedulehost.PeerDelivererOptions{
		CallerNodeID: nodeID,
		Logf:         logf,
	}); err != nil {
		logf("peer delivery unavailable (%v); falling back to HTTP delegate", err)
	} else {
		peer = p
	}
	deliverer, err := schedulehost.NewLocalFirstDeliverer(schedulehost.LocalFirstOptions{
		Local: local,
		Peer:  peer,
		HTTP:  &schedule.HTTPDeliverer{BaseURL: sc.Execution.RuntimeServerURL},
		Logf:  logf,
	})
	if err != nil {
		logf("driver disabled: %v", err)
		return
	}

	var compatCheck func() error
	if sqlStore, ok := store.(*schedule.SQLiteStore); ok {
		compatCheck = schedulehost.StoreCompatCheck(sqlStore.SchemaVersion)
	}
	leader, err := schedulehost.NewLeader(schedulehost.LeaderOptions{
		Store:             store,
		Service:           service,
		Deliverer:         deliverer,
		Mode:              sc.Execution.Mode,
		MaxAttempts:       sc.MaxDeliveryAttempts,
		FallbackToMailbox: sc.Execution.FallbackToMailbox,
		MisfireGrace:      sc.MisfireGrace,
		Identity: schedulehost.DriverIdentity{
			NodeID:   nodeID,
			Kind:     schedulehost.RoleAICLI,
			Priority: sc.DriverPriority(config.ScheduleRoleAICLI),
		},
		LeaseTTL:     sc.Driver.LeaseTTL,
		StandbyRetry: sc.Driver.StandbyRetry,
		// 交互宿主 fail-closed：mesh 不可用时待命，不自跑。
		Degraded:              schedulehost.DegradedStandby,
		StoreCompatCheck:      compatCheck,
		DeferToHigherPriority: true,
		Logf:                  logf,
	})
	if err != nil {
		logf("driver disabled: %v", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	leader.Start(ctx)
	// 参战即宣告能力位（节点档案先写、参战决策后做）：在岗可见性与优先级让位
	// 判定都依赖档案里的 scheduler 位；失败只影响可见性，不阻断驱动。
	host.AddCapabilities(
		schedulehost.CapabilityScheduler,
		schedulehost.SchedulerCapability(schedule.SupportedSchemaVersion()),
	)
	chatScheduleDriver.leader = leader
	chatScheduleDriver.cancel = cancel
	chatScheduleDriver.owned = owned
	logf("joined as %s (priority=%d, lease_ttl=%s)", nodeID, sc.DriverPriority(config.ScheduleRoleAICLI), sc.Driver.LeaseTTL)
}

// stopChatScheduleDriver 停止 aicli 调度者（幂等；会话宿主卸载时调用）。
func stopChatScheduleDriver() {
	chatScheduleDriver.mu.Lock()
	leader, cancel := chatScheduleDriver.leader, chatScheduleDriver.cancel
	owned := chatScheduleDriver.owned
	chatScheduleDriver.leader, chatScheduleDriver.cancel, chatScheduleDriver.owned = nil, nil, nil
	chatScheduleDriver.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if leader != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		leader.Stop(stopCtx)
	}
	if owned != nil {
		if store := owned.Store(); store != nil {
			_ = store.Close()
		}
	}
}
