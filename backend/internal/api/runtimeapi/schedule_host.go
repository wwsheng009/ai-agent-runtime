package runtimeapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// scheduleSessionDeliverer 以进程内方式把兜底提醒投递进目标会话 actor
// （D1：宿主恢复后 drain pending）。提交语义为"被 actor 接受"
// （SubmitPromptAsync），不等待 turn 完成——与 HTTP delegate 的 2xx 回执口径一致。
type scheduleSessionDeliverer struct {
	handler *Handler
}

func (d scheduleSessionDeliverer) Deliver(ctx context.Context, sessionID, text string) error {
	if d.handler == nil {
		return fmt.Errorf("schedule: handler not configured")
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
	actor, err := hub.GetOrCreate(strings.TrimSpace(sessionID))
	if err != nil {
		return fmt.Errorf("schedule: get session actor %s: %w", sessionID, err)
	}
	if err := actor.SubmitPromptAsync(ctx, text, nil); err != nil {
		return fmt.Errorf("schedule: submit prompt to %s: %w", sessionID, err)
	}
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
