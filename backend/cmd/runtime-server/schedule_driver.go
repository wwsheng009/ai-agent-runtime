package main

import (
	"context"
	"fmt"
	"os"

	"github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

// startRuntimeServerScheduleDriver 让 runtime-server 以"在岗替补"身份参与调度
// 领导权（S1 联盟式调度，见 docs/automation-tasks/mesh-scheduler-federation-design.md）：
// 与 cmd/scheduled 竞争同一把 mesh 租约；持租约者驱动 Runtime，投递走进程内
// 会话 hub（L1 就近投递，不依赖 runtimeServerURL 配置）。
//
// 门控：schedule.enabled=false 或 driver.participants 不含 runtime-server →
// 不参与；任何装配失败只 warn，不影响主服务（调度能力降级为 daemon-only）。
// mesh 降级策略默认 standby（fail-closed）——不能判定唯一性时宁可不驱动。
func startRuntimeServerScheduleDriver(ctx context.Context, app *runtimeServerApp, host *mesh.Host) {
	if app == nil || app.runtimeManager == nil || app.handler == nil {
		return
	}
	if !runtimeServerParticipatesInSchedule(app) {
		return
	}
	cfg := app.runtimeManager.Get()
	scheduleCfg := cfg.Schedule.WithDefaults()
	service := app.handler.ScheduleService()
	store := app.handler.ScheduleStore()
	local := app.handler.ScheduleDeliverer()
	if service == nil || store == nil || local == nil {
		logger.Warn("schedule driver skipped: schedule store/service not available")
		return
	}
	// S2（§12.1）：接收侧账本去重——同一 occurrence 已送达时不再重复入 hub。
	if ledger, ok := store.(schedule.OccurrenceLedger); ok {
		local = schedule.GuardedDeliverer{
			Inner:  local,
			Ledger: ledger,
			Logf:   func(format string, args ...any) { logger.Warn(fmt.Sprintf(format, args...)) },
		}
	}
	nodeID := ""
	if host != nil {
		nodeID = host.NodeID()
	}
	if nodeID == "" {
		nodeID = fmt.Sprintf("runtime-server-%d", os.Getpid())
	}
	// L2：目标会话宿主（mesh.Call schedule.deliver）。宿主没有稳定 mesh 身份时
	// 跳过该层（fail-closed），退回本地/HTTP 路径。
	var peerDeliverer schedule.Deliverer
	if peer, peerErr := schedulehost.NewPeerDeliverer(schedulehost.PeerDelivererOptions{
		CallerNodeID: nodeID,
		Logf:         func(format string, args ...any) { logger.Warn(fmt.Sprintf(format, args...)) },
	}); peerErr != nil {
		logger.Warn("schedule peer delivery unavailable", logger.Err(peerErr))
	} else {
		peerDeliverer = peer
	}
	deliverer, err := schedulehost.NewLocalFirstDeliverer(schedulehost.LocalFirstOptions{
		Local: local,
		Peer:  peerDeliverer,
		Logf:  func(format string, args ...any) { logger.Warn(fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		logger.Warn("schedule driver disabled", logger.Err(err))
		return
	}

	leader, err := schedulehost.NewLeader(schedulehost.LeaderOptions{
		Store:             store,
		Service:           service,
		Deliverer:         deliverer,
		Mode:              scheduleCfg.Execution.Mode,
		MaxAttempts:       scheduleCfg.MaxDeliveryAttempts,
		FallbackToMailbox: scheduleCfg.Execution.FallbackToMailbox,
		// S4b（§4.4）：一次性任务补发宽限窗口（0=总是补发）。
		MisfireGrace: scheduleCfg.MisfireGrace,
		Identity: schedulehost.DriverIdentity{
			NodeID:   nodeID,
			Kind:     schedulehost.RoleRuntimeServer,
			Priority: scheduleCfg.DriverPriority(config.ScheduleRoleRuntimeServer),
		},
		LeaseTTL:     scheduleCfg.Driver.LeaseTTL,
		StandbyRetry: scheduleCfg.Driver.StandbyRetry,
		// S3 版本握手（§4.2）：store 超出本二进制支持档时退出/不参与领导权。
		StoreCompatCheck: runtimeServerStoreCompatCheck(store),
		// 宿主默认 degraded=standby（fail-closed）；配置显式覆盖优先。
		Degraded: schedulehost.DegradedPolicy(scheduleCfg.DriverDegradedPolicy(config.ScheduleRoleRuntimeServer)),
		Logf:     func(format string, args ...any) { logger.Warn(fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		logger.Warn("schedule driver disabled", logger.Err(err))
		return
	}
	leader.Start(ctx)
	app.scheduleDriver = leader
	logger.Info("schedule driver joined",
		logger.String("node_id", nodeID),
		logger.Int("priority", scheduleCfg.DriverPriority(config.ScheduleRoleRuntimeServer)),
		logger.String("lease_ttl", scheduleCfg.Driver.LeaseTTL.String()))
}

// runtimeServerParticipatesInSchedule 是"本进程以调度者身份参战"的配置门
// （mesh 档案能力位与 driver 启动共用同一判定，避免档案说在岗却没参战）。
func runtimeServerParticipatesInSchedule(app *runtimeServerApp) bool {
	if app == nil || app.runtimeManager == nil {
		return false
	}
	cfg := app.runtimeManager.Get()
	if cfg == nil || !cfg.Schedule.Enabled {
		return false
	}
	return cfg.Schedule.WithDefaults().DriverAllows(config.ScheduleRoleRuntimeServer)
}

// runtimeServerStoreCompatCheck 只对可查询 schema 版本的 store 生效（其它实现
// 跳过——其打开路径已 fail-closed）。
func runtimeServerStoreCompatCheck(store schedule.Store) func() error {
	versioned, ok := store.(interface {
		SchemaVersion(ctx context.Context) (int, error)
	})
	if !ok {
		return nil
	}
	return schedulehost.StoreCompatCheck(versioned.SchemaVersion)
}
