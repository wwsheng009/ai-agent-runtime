package commands

import (
	"context"
	"errors"
	"strings"
	"time"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// localSupervisionProgressCheckReason 是周期巡查注入的 wake 原因。
//
// 刻意不含 approval/failure 关键词：WakeBudgetClassOf 会把它归入有界的
// "other" 类别，于是巡查与 critical lifecycle wake 共享同一份预算/去抖/去重，
// 不需要新建通知类别或第二套限流（P2-D 的"复用既有 wake admission"）。
const localSupervisionProgressCheckReason = supervision.WakeReasonProgressCheck

// wireLocalSupervisionSources 把只读的 batch 控制面接到 wake scheduler 上：
//
//   - progress 投影：progress wake 不带生命周期通知，rollup 就是它唯一的 digest
//     内容；没有这条投影，wake 会被判成"无内容"而不投递（见 supervision.
//     digestDeliverable），巡查就永远不会产生汇报 turn。
//   - 账本投影（G2）：resume 上下文要回答 pending_count（I1 收尾判据）与终态
//     rollup / 失败清单（§16.4）；没有它，DeliverResume 只能拿到 pending=-1、
//     status=unknown 的降级上下文。
//
// 只读、幂等，可在宿主启动与每次巡查前重复调用。
func (h *localChatRuntimeHost) wireLocalSupervisionSources() {
	if h == nil || h.Supervision == nil || h.Supervision.Wakes == nil || h.SubagentBatches == nil {
		return
	}
	h.Supervision.Wakes.SetProgressSource(h.newLocalBatchProgressSource())
	if source := supervision.NewBatchObligationSource(h.SubagentBatches); source != nil {
		h.Supervision.Wakes.SetObligationSource(source)
	}
}

// newLocalBatchProgressSource 构造 CLI 的只读 batch 进度投影，并把 per-host
// 的 live-only 进度镜像接到可选的 Messages 富化钩子上：supervision_descendants
// /digest 的 last_message 因此在 CLI 与 API 宿主同口径（P0-1c）。镜像实例由
// subagentProgressMirror() 统一持有，绝不在这里新建；store 未装配时返回 nil，
// 未接线宿主的行为逐字节不变。
func (h *localChatRuntimeHost) newLocalBatchProgressSource() supervision.ProgressSource {
	if h == nil || h.SubagentBatches == nil {
		return nil
	}
	source := supervision.NewBatchProgressSource(h.SubagentBatches)
	batchSource, ok := source.(*supervision.BatchProgressSource)
	if !ok || batchSource == nil {
		return source
	}
	if mirror := h.subagentProgressMirror(); mirror != nil {
		batchSource.Messages = supervision.MirrorProgressMessages{Mirror: mirror}
	}
	return source
}

// startLocalSupervisionProgressCheck 启动 opt-in 的 P2-D 周期巡查。
//
// 关闭（默认）时该函数立刻返回：不注册 ticker、不新增 goroutine，宿主行为与
// 引入该开关前完全一致。开启后循环挂在 host.lifecycleCtx 上，Close() 会连同
// asyncWG 一起等待它退出，因此不存在进程退出后残留的巡检 goroutine。
func (h *localChatRuntimeHost) startLocalSupervisionProgressCheck() {
	if h == nil || h.Supervision == nil || h.Supervision.Wakes == nil || h.SubagentBatches == nil {
		return
	}
	interval := h.supervisionConfig.WithDefaults().ProgressCheckInterval
	// 钳制告警（P0-2 改动 3）：宿主字段可能持有未钳制的原始配置（构造路径与
	// 直接注入的宿主都走这里），低于下限时必须留下 warning 而不是静默改语义。
	warnProgressCheckIntervalClamped(h.supervisionConfig.ProgressCheckInterval, interval)
	if interval <= 0 {
		return
	}
	if h.lifecycleCtx == nil || h.lifecycleCtx.Err() != nil {
		// Host 正在关闭或从未完成初始化：保持巡检器可同步调用，但不启动
		// Close() 不会再等待的后台循环。
		return
	}
	h.progressCheckOnce.Do(func() {
		ctx, cancel := context.WithCancel(h.lifecycleCtx)
		h.progressCheckStop = cancel
		h.asyncWG.Add(1)
		go func() {
			defer h.asyncWG.Done()
			h.runLocalSupervisionProgressCheck(ctx, interval)
		}()
	})
}

// warnProgressCheckIntervalClamped 在显式配置低于下限（< 30s）时记录 warning，
// 并返回是否发生了钳制。0/负数表示"关闭"，不触发告警（关闭语义不得改变）；
// 生效间隔由 supervision.Config.WithDefaults 统一钳到 MinProgressCheckInterval。
func warnProgressCheckIntervalClamped(raw, effective time.Duration) bool {
	if raw <= 0 || raw >= supervision.MinProgressCheckInterval {
		return false
	}
	logpkg.Warnf("supervision: progress_check_interval %s is below the %s floor; clamped to %s",
		raw, supervision.MinProgressCheckInterval, effective)
	return true
}

// stopLocalSupervisionProgressCheck 让测试/调试路径可以在不关闭整个 host 的
// 前提下停掉巡检循环（Close() 走 lifecycleCtx，不需要调用它）。
func (h *localChatRuntimeHost) stopLocalSupervisionProgressCheck() {
	if h == nil || h.progressCheckStop == nil {
		return
	}
	h.progressCheckStop()
}

// runLocalSupervisionProgressCheck 是巡检循环本体：每个 tick 做一次有界检查。
// 没有 active batch、父会话忙、或已有待投递 wake 时，本次 tick 不产生任何
// 写入与 turn——这就是"无 active batch 即停（不产生 turn）"的落点。
func (h *localChatRuntimeHost) runLocalSupervisionProgressCheck(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 巡查是 best-effort：一次读取失败（例如 store 临时不可用）
			// 只跳过本轮，绝不因此中断整个循环或影响父会话。
			_, _ = h.runLocalSupervisionProgressCheckOnce(ctx)
		}
	}
}

// runLocalSupervisionProgressCheckOnce 执行一轮巡查，返回是否真的提交了一次
// 汇报 turn。测试直接调用它，从而避免依赖 ticker 的定时。
//
// 依次要求：存在 active batch（否则没有"新进度"可汇报）→ 父会话空闲 →
// 没有待投递 wake（避免与 lifecycle wake 抢同一个 turn）→ 预算未耗尽
// （AllowAutoWake 只读判定，复用 wake 限流）→ 调度并投递。
func (h *localChatRuntimeHost) runLocalSupervisionProgressCheckOnce(ctx context.Context) (bool, error) {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil ||
		h.Supervision.Wakes == nil || h.SubagentBatches == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	parentSessionID := h.localSupervisionProgressCheckSessionID()
	if parentSessionID == "" {
		return false, nil
	}
	h.wireLocalSupervisionSources()
	active, err := h.localSupervisionHasActiveProgress(ctx, parentSessionID)
	if err != nil {
		return false, err
	}
	if !active {
		return false, nil
	}
	if !h.localSupervisionParentIdle(ctx, parentSessionID) {
		return false, nil
	}
	pending, err := h.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           parentSessionID,
		TargetParentSessionID: parentSessionID,
		UnclaimedOnly:         true,
	})
	if err != nil {
		return false, err
	}
	if len(pending) > 0 {
		// 已有一条待投递 wake：下一次 runnable transition 会投递它，本轮
		// 不再叠加第二个 turn（digest 里本来就含 progress 区块）。
		return false, nil
	}
	if h.localSupervisionHasUnresolvedCritical(ctx, parentSessionID) {
		// P0-2 改动 2：scope 内仍有未决的 critical 通知时让位。critical 的投递
		// 本身会带来同一个 digest（其中已含 progress 区块），progress 汇报不该
		// 抢在它前面消耗一次父 turn；critical 消除后巡查自动恢复。
		return false, nil
	}
	class := supervision.WakeBudgetClassOf(localSupervisionProgressCheckReason)
	if !h.Supervision.Wakes.AllowAutoWake(ctx, parentSessionID, class, time.Now().UTC()) {
		// 预算耗尽：progress 巡查不是决策，直接跳过而不是留下一条后续会被
		// 投递的陈旧 wake（critical lifecycle wake 仍按既有语义保持 durable）。
		return false, nil
	}
	if _, err := h.Supervision.Wakes.ScheduleWake(ctx, supervision.WakeRequest{
		RootScopeID:           parentSessionID,
		TargetParentSessionID: parentSessionID,
		WakeReason:            localSupervisionProgressCheckReason,
	}); err != nil {
		return false, err
	}
	if err := h.wakeSupervisedParent(ctx, parentSessionID, parentSessionID); err != nil {
		if errors.Is(err, supervision.ErrWakeParentBusy) || errors.Is(err, supervision.ErrWakeRateLimited) {
			// 竞态（父会话在两次检查之间变忙）或预算判定后的边界情况：
			// 保持 durable 语义，等下一次 transition 重试。
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// localSupervisionHasUnresolvedCritical reports whether the root scope still
// holds an unresolved critical notification. It reuses the same store read the
// preflight digest uses (ListNotifications) and does not add a new access
// pattern; a read failure is surfaced to the caller so one best-effort tick is
// skipped instead of guessing.
func (h *localChatRuntimeHost) localSupervisionHasUnresolvedCritical(ctx context.Context, rootScopeID string) bool {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil {
		return false
	}
	notifications, err := h.Supervision.Store.ListNotifications(ctx, supervision.NotificationFilter{
		RootScopeID:     strings.TrimSpace(rootScopeID),
		IncludeResolved: false,
	})
	if err != nil {
		return false
	}
	for _, notification := range notifications {
		if notification.Severity == supervision.SeverityCritical && notification.Unresolved() {
			return true
		}
	}
	return false
}

// localSupervisionProgressCheckSessionID 是巡检的作用域：本 host 的根会话
// （与 bindSupervisionWakeConsumer、preflight digest 使用同一个 session id）。
func (h *localChatRuntimeHost) localSupervisionProgressCheckSessionID() string {
	if h == nil || h.BaseSession == nil || h.BaseSession.RuntimeSession == nil {
		return ""
	}
	return strings.TrimSpace(h.BaseSession.RuntimeSession.ID)
}

// localSupervisionHasActiveProgress 复用 P0-B 的只读投影判断是否存在
// running/queued/partially-completed batch。只读：不落表、不产生事件。
func (h *localChatRuntimeHost) localSupervisionHasActiveProgress(ctx context.Context, parentSessionID string) (bool, error) {
	source := supervision.NewBatchProgressSource(h.SubagentBatches)
	if source == nil {
		return false, nil
	}
	groups, err := source.ListProgress(ctx, supervision.ProgressRequest{
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
	})
	if err != nil {
		return false, err
	}
	for _, group := range groups {
		if !group.Terminal {
			return true, nil
		}
	}
	return false, nil
}

// localSupervisionParentIdle 与 wake consumer 的 Runnable 判定同口径（C4-3 /
// AC-P3-3c）：父会话没有正在执行的 running/approval/input turn 时才允许注入汇报
// turn。挂起 turn（`awaiting_obligations`）拒绝的是**新** turn，resume episode
// 照常放行——巡检汇报正是同一 turn 的续跑。
func (h *localChatRuntimeHost) localSupervisionParentIdle(ctx context.Context, parentSessionID string) bool {
	if h == nil || h.RuntimeStore == nil {
		return false
	}
	state, err := h.RuntimeStore.LoadState(ctx, parentSessionID)
	if err != nil || state == nil {
		return false
	}
	return state.Summary().AcceptsResume()
}
