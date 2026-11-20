package runtimeapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// scheduleSessionReminderRunTimeout 是会话提醒真正执行 turn 的上限。
// 不能复用调度器 Deliver 的 30s 上下文：Deliver 返回后 Runtime.drive 会立即
// cancel 该 ctx，actor 的 run 会以 cause=parent_context 被取消。
const scheduleSessionReminderRunTimeout = 10 * time.Minute

// scheduleReminderRunContext returns the run-scoped context for a session
// reminder. It deliberately detaches from the delivery context's cancellation:
// the scheduler cancels that context as soon as Deliver returns, while the
// actor's turn must keep running. The bounded timeout prevents a reminder from
// pinning a session forever.
func scheduleReminderRunContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), scheduleSessionReminderRunTimeout)
}

// scheduleSessionDeliverer 以进程内方式把兜底提醒投递进目标会话 actor
// （D1：宿主恢复后 drain pending）。提交语义为"被 actor 接受"
// （SubmitPromptAsync），不等待 turn 完成——与 HTTP delegate 的 2xx 回执口径一致。
type scheduleSessionDeliverer struct {
	handler *Handler
}

func (d scheduleSessionDeliverer) Deliver(ctx context.Context, sessionID, text string) error {
	return d.DeliverEnvelope(ctx, schedule.DeliveryEnvelope{SessionID: sessionID, Text: text})
}

// DeliverEnvelope 在 legacy Deliver 的基础上携带 occurrence 键，并要求 actor
// 返回“提示词已经持久化、run 已被接收”的回执。旧的 SubmitPromptAsync 只保证
// 命令进入 channel：actor 随后在 ensureReady/loadSession/persist/prepareRun
// 失败时，错误会落在无人读取的 reply 里，调度器却把 occurrence 记成 delivered，
// 最终表现为“已投递、无历史、无 run、无日志”。这里改用带回执的提交，把这类
// 失败重新变成可重试的投递失败。
func (d scheduleSessionDeliverer) DeliverEnvelope(ctx context.Context, env schedule.DeliveryEnvelope) error {
	if d.handler == nil {
		return fmt.Errorf("schedule: handler not configured")
	}
	sessionID := strings.TrimSpace(env.SessionID)
	text := strings.TrimSpace(env.Text)
	if sessionID == "" {
		return fmt.Errorf("schedule: session id is required")
	}
	if text == "" {
		return fmt.Errorf("schedule: delivery text is required")
	}
	// S2b（§3.3 L1 前置）：只服务本进程确实托管的会话——已有活 actor，或会话
	// 可从本进程会话库加载且**绑定持有者就是本进程**；否则返回错误让组合投递器
	// 降级到 L2。没有这道闸门会为外部会话凭空建幽灵 actor：提醒被"接受"却投进
	// 错误进程、宿主完全不可见（2026-10-15 ConPTY E2E 实机抓到：aicli 托管的
	// TUI 会话提醒被 runtime-server 吞掉，TUI 不可见）。
	if !d.handler.scheduleLocalSessionHosted(ctx, sessionID) {
		return fmt.Errorf("schedule: session %s is not hosted by this process", sessionID)
	}
	hub := d.handler.getSessionHub()
	if hub == nil {
		return fmt.Errorf("schedule: session hub not configured")
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		return fmt.Errorf("schedule: get session actor %s: %w", sessionID, err)
	}
	occurrences := strings.Join(env.Occurrences, ",")
	// 回执等待和真正执行都必须脱离调度器的短上下文：Runtime.drive 在
	// Deliver 返回后立即 cancel 传入的 30s ctx，actor 的 run 不能继承它。
	// WithoutCancel 保留请求作用域值，10m 超时防止提醒永久占用会话。
	runCtx, cancelRun := scheduleReminderRunContext(ctx)
	resultCh, err := actor.SubmitPromptAsyncWithReceipt(runCtx, text, nil)
	if err != nil {
		cancelRun()
		logger.Warn("schedule: session reminder rejected",
			logger.String("session", sessionID),
			logger.String("occurrences", occurrences),
			logger.Err(err))
		return fmt.Errorf("schedule: submit prompt to %s: %w", sessionID, err)
	}
	logger.Info("schedule: session reminder accepted",
		logger.String("session", sessionID),
		logger.String("occurrences", occurrences))
	go func() {
		defer cancelRun()
		select {
		case result, ok := <-resultCh:
			if !ok {
				return
			}
			if result.Err != nil {
				logger.Warn("schedule: session reminder run failed",
					logger.String("session", sessionID),
					logger.String("occurrences", occurrences),
					logger.Err(result.Err))
				return
			}
			logger.Info("schedule: session reminder completed",
				logger.String("session", sessionID),
				logger.String("occurrences", occurrences))
		case <-runCtx.Done():
			logger.Warn("schedule: session reminder run timed out",
				logger.String("session", sessionID),
				logger.String("occurrences", occurrences),
				logger.Err(runCtx.Err()))
		}
	}()
	return nil
}

// scheduleLocalSessionHosted 报告本进程是否托管该会话（S2b L1 前置）：
// ① 已有活 actor；或 ② 会话能从本进程会话库加载，且 mesh binding 的
// last_node_id 就是本进程节点——会话库是**共享**的（所有宿主同库），
// "可加载"只说明会话存在过，不代表本进程持有它。
func (h *Handler) scheduleLocalSessionHosted(ctx context.Context, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	if hub := h.getSessionHub(); hub != nil {
		if _, ok := hub.Get(sessionID); ok {
			return true
		}
	}
	// S2b 复验（2026-10-15 ConPTY E2E）：共享会话库让"可加载"恒真，共享库
	// 分支曾把 aicli 托管的活跃会话判成"本进程托管"→ L1 幽灵 actor 把提醒
	// 吞进 runtime-server（TUI 不可见）而 commit 照常。活跃会话的宿主在
	// binding 里有权威记录（会话激活时 TouchBinding 写 last_node_id），
	// 不一致 = 别人托管 → 降级 L2 就近投递。binding 缺失时保守沿用旧判定
	// （老会话/未入网部署），不把 drain 主链堵死。
	if host := mesh.Current(); host != nil {
		if nodeID := strings.TrimSpace(host.NodeID()); nodeID != "" {
			if binding, ok := mesh.LoadBinding(mesh.ResolvePaths(), sessionID); ok {
				if owner := strings.TrimSpace(binding.LastNodeID); owner != "" && owner != nodeID {
					return false
				}
			}
		}
	}
	manager := h.sessionManager
	if manager == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := manager.Get(ctx, sessionID); err == nil {
		return true
	}
	return false
}

// attachScheduleDrainRuntime 给共享管理服务注册宿主侧兜底消费运行时：
// 会话激活时 drainSchedulePending 经 Service.Runtime() 消费 daemon 在宿主
// 离线期间落库的 pending 提醒。只读 store + 进程内 deliverer，不启动定时器。
//
// 没有这一步时 Service.Runtime() 恒为 nil，drainSchedulePending 直接返回，
// daemon 落库的兜底提醒永远不会被宿主补投。
func (h *Handler) attachScheduleDrainRuntime(service *schedule.Service, store schedule.Store) {
	if h == nil || service == nil || store == nil {
		return
	}
	service.SetRuntime(schedule.NewRuntime(schedule.RuntimeOptions{
		Store:     store,
		Service:   service,
		Deliverer: scheduleSessionDeliverer{handler: h},
		Logf:      func(format string, args ...any) { logger.Warn(fmt.Sprintf(format, args...)) },
	}))
}

// ScheduleService 暴露惰性初始化的 schedule 服务（未启用/打不开 → nil）。
// S1（联盟式调度）：runtime-server 用它把"调度领导权"接到本进程（见
// cmd/runtime-server/schedule_driver.go）。
func (h *Handler) ScheduleService() *schedule.Service {
	if h == nil {
		return nil
	}
	return h.getScheduleService()
}

// ScheduleStore 返回服务所用的共享 schedule store（未启用 → nil）。
func (h *Handler) ScheduleStore() schedule.Store {
	if h == nil {
		return nil
	}
	if h.getScheduleService() == nil {
		return nil
	}
	h.scheduleMu.Lock()
	defer h.scheduleMu.Unlock()
	return h.scheduleStore
}

// ScheduleDeliverer 返回"进程内投递一条提醒进本进程会话 hub"的投递器
// （L1 就近投递；未启用 schedule 时由调用方跳过——Leader 侧另有 Store 校验）。
func (h *Handler) ScheduleDeliverer() schedule.Deliverer {
	if h == nil {
		return nil
	}
	return scheduleSessionDeliverer{handler: h}
}

// scheduleOccurrenceLedger 返回交付账本（S2，§12.1）；未启用 schedule 或
// store 不可用时返回 nil（调用方按"无账本"路径继续，不做去重）。
func (h *Handler) scheduleOccurrenceLedger() *schedule.SQLiteStore {
	if h == nil {
		return nil
	}
	if h.getScheduleService() == nil {
		return nil
	}
	h.scheduleMu.Lock()
	defer h.scheduleMu.Unlock()
	store, _ := h.scheduleStore.(*schedule.SQLiteStore)
	return store
}
