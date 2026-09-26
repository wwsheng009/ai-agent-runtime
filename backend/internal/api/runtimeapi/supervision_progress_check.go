package runtimeapi

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// P2-D 周期巡查（progress check）的 API 宿主实现。
//
// 缺口背景（2026-09-16）：CLI 宿主早已有 opt-in 的巡检（见
// chat_actor_progress_check.go），runtime-server 侧只有 handler 字段声明而没有
// 实现，于是"父会话空闲但子 agent 还在跑"时没有任何汇报点：后台 batch 既不落
// lifecycle 行（成功不唤醒契约），又不会触发 wake ⇒ 父 agent 只能等到下一次自然
// turn 才从 preflight digest 看到进度。本文件把 CLI 的契约原样搬到 API 路径：
//
//	存在 active batch → 父会话空闲 → 无待投递 wake → 预算未耗尽 → 调度并投递
//
// 与 CLI 的两点差异（都是宿主形态决定的，不是语义差异）：
//  1. API 宿主是 多会话 的，所以巡查单位是"batch store 里活跃批次所属的父会话"，
//     而不是单个根会话；单轮有界（见 apiSupervisionProgressCheckParentLimit）。
//  2. 巡查不创建 session actor：只为"宿主已经知道的会话"注入汇报 turn，避免给
//     历史/孤儿 batch 行凭空造出 actor（未命中时下一次自然 turn 的 preflight
//     仍然会注入 rollup，退化是可见的而不是静默丢失）。

// apiSupervisionProgressCheckReason 是周期巡查注入的 wake 原因。
//
// 刻意不含 approval/failure 关键词：WakeBudgetClassOf 会把它归入有界的
// "other" 类别，于是巡查与 critical lifecycle wake 共享同一份预算/去重，不需要
// 新建通知类别或第二套限流（P2-D 的"复用既有 wake admission"）。
const apiSupervisionProgressCheckReason = supervision.WakeReasonProgressCheck

const (
	// apiSupervisionProgressCheckParentLimit 是一轮巡查最多检查的父会话数。
	// runtime-server 可以同时承载大量会话，巡查是周期性 best-effort 提示，单轮
	// 必须有界，避免一个 tick 把持久化读打满。
	apiSupervisionProgressCheckParentLimit = 16
	// apiSupervisionProgressCheckBatchLimit 是每个 tick 的批次读取上限（先按最近
	// 活动排序再截断，所以被丢掉的总是最久没动静的批次）。
	apiSupervisionProgressCheckBatchLimit = 64
)

// apiSupervisionProgressCheckActiveStatuses 与 supervision.activeBatchStatuses
// 同口径：只有这些状态才算"还在产出进度"，终态批次没有新进度可汇报。
var apiSupervisionProgressCheckActiveStatuses = []subagentbatch.BatchStatus{
	subagentbatch.BatchQueued,
	subagentbatch.BatchRunning,
	subagentbatch.BatchPartiallyCompleted,
}

// apiProgressCheckParent 是一轮巡查的作用域单位：batch 行记录的父会话 + 根 scope
// （与 apiBatchLifecycleProjector 同口径，RootScopeID 为空时回落到父会话）。
type apiProgressCheckParent struct {
	RootScopeID     string
	ParentSessionID string
}

// wireSupervisionSources 把只读的 batch 控制面接到 wake scheduler 上：
//
//   - progress 投影：progress wake 不带生命周期通知，rollup 就是它唯一的
//     digest 内容；没有这条投影，digest 会被判成"无内容"而不投递（见
//     supervision.digestDeliverable），巡查就永远产生不了汇报 turn。
//   - 账本投影（G2）：resume 上下文要回答 pending_count（I1 收尾判据）与终态
//     rollup / 失败清单（§16.4）；没有它，DeliverResume 只能拿到 pending=-1、
//     status=unknown 的降级上下文。
//
// 只读、幂等，可在巡查前 / 控制面装配点重复调用；store 未装配时不做任何接线
// （peek 不触发懒加载，避免为一次接线凭空虚建 store）。
func (h *Handler) wireSupervisionSources() {
	scheduler := h.getSupervisionWakeScheduler()
	if scheduler == nil {
		return
	}
	if store := h.peekSubagentBatchStore(); store != nil {
		scheduler.SetObligationSource(supervision.NewBatchObligationSource(store))
	}
	scheduler.SetProgressSource(h.supervisionProgressSource())
}

// syncSupervisionProgressCheck 让巡检循环跟随配置：ProgressCheckInterval > 0
// 且控制面已接线时启动，否则保持"无 ticker、无 goroutine"。
//
// SetSupervisionConfig 是唯一启动点（见 handler.go 字段注释）。先停后起，因此
// 重复调用幂等，interval 调大/调小/改回 0 都会立刻生效。
func (h *Handler) syncSupervisionProgressCheck() {
	if h == nil {
		return
	}
	h.StopSupervisionProgressCheck()
	tuning := h.supervisionTuning()
	if tuning.ProgressCheckInterval <= 0 {
		// opt-in 关闭（默认）：不注册 ticker、不新增 goroutine，宿主行为与引入该
		// 开关前完全一致。
		return
	}
	// 钳制告警（P0-2 改动 3）：原始配置低于下限时 WithDefaults 会抬到 30s，
	// 必须让运维看到"配了 1s 实际是 30s"，而不是静默改语义。0=关闭不受影响。
	h.supervisionStoreMu.RLock()
	rawInterval := h.supervisionConfig.ProgressCheckInterval
	h.supervisionStoreMu.RUnlock()
	if rawInterval > 0 && rawInterval < supervision.MinProgressCheckInterval {
		logger.Warnf("supervision: progress_check_interval %s is below the %s floor; clamped to %s",
			rawInterval, supervision.MinProgressCheckInterval, tuning.ProgressCheckInterval)
	}
	if h.getSupervisionStore() == nil {
		// 控制面还没接线：等下一次 SetSupervisionConfig（宿主总是最后调用它）再
		// 启动，而不是留一个每 tick 都空转的 goroutine。
		return
	}
	h.startSupervisionProgressCheck(tuning.ProgressCheckInterval)
}

// startSupervisionProgressCheck 启动巡检循环；已在运行时是 no-op。
func (h *Handler) startSupervisionProgressCheck(interval time.Duration) {
	if h == nil || interval <= 0 {
		return
	}
	h.supervisionProgressCheckMu.Lock()
	if h.supervisionProgressCheckStop != nil {
		h.supervisionProgressCheckMu.Unlock()
		return
	}
	// API 宿主没有 CLI 那样的 lifecycleCtx（进程即宿主），因此巡检挂在自己的
	// root context 上，由 StopSupervisionProgressCheck / 重配来收敛。
	ctx, cancel := context.WithCancel(context.Background())
	h.supervisionProgressCheckStop = cancel
	h.supervisionProgressCheckWG.Add(1)
	h.supervisionProgressCheckMu.Unlock()

	go func() {
		defer h.supervisionProgressCheckWG.Done()
		h.runSupervisionProgressCheck(ctx, interval)
	}()
}

// StopSupervisionProgressCheck 停止巡检循环并等待它退出；未启动时是 no-op。
//
// SetSupervisionConfig 在重配/关闭时调用它，宿主关闭流程与测试也可以显式调用：
// 返回时保证没有残留的巡检 goroutine（与 CLI 的 asyncWG 收敛同语义）。
func (h *Handler) StopSupervisionProgressCheck() {
	if h == nil {
		return
	}
	h.supervisionProgressCheckMu.Lock()
	cancel := h.supervisionProgressCheckStop
	h.supervisionProgressCheckStop = nil
	h.supervisionProgressCheckMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	h.supervisionProgressCheckWG.Wait()
}

// runSupervisionProgressCheck 是巡检循环本体：每个 tick 做一次有界检查。
func (h *Handler) runSupervisionProgressCheck(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 巡查是 best-effort：一次失败（store 临时不可用、单个父会话出错）
			// 只跳过本轮，绝不中断循环或影响父会话。
			_, _ = h.runSupervisionProgressCheckOnce(ctx)
		}
	}
}

// runSupervisionProgressCheckOnce 执行一轮巡查，返回本轮真实提交的汇报 turn 数。
//
// 测试直接调用它，从而避免依赖 ticker 的定时。逐父会话的契约见
// runSupervisionProgressCheckForParent；这里只负责"有界枚举 + 接线 + 隔离失败"。
func (h *Handler) runSupervisionProgressCheckOnce(ctx context.Context) (int, error) {
	if h == nil {
		return 0, nil
	}
	scheduler := h.getSupervisionWakeScheduler()
	if scheduler == nil || h.getSupervisionStore() == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	parents, err := h.supervisionProgressCheckParents(ctx)
	if err != nil {
		return 0, err
	}
	if len(parents) == 0 {
		// 没有 active batch：本轮不读 wake、不落库、不产生 turn。
		return 0, nil
	}
	// progress wake 没有生命周期行可依赖，所以必须在调度前保证 scheduler 能看到
	// rollup；接线幂等，重复调用只是重新指向同一个只读投影。
	h.wireSupervisionSources()

	var (
		delivered int
		firstErr  error
	)
	now := time.Now().UTC()
	for _, parent := range parents {
		ok, err := h.runSupervisionProgressCheckForParent(ctx, scheduler, parent, now)
		if err != nil {
			// 单个父会话出错不影响其它父会话：一个持久化读失败不应该让整轮（乃至
			// 整个循环，见 runSupervisionProgressCheck）停下来。
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ok {
			delivered++
		}
	}
	return delivered, firstErr
}

// runSupervisionProgressCheckForParent 是单个父会话的巡查判定，契约与 CLI 的
// runLocalSupervisionProgressCheckOnce 逐条一致：
//
//	父会话空闲（否则汇报 turn 会插进正在跑的 turn）→ 没有待投递 wake（否则与
//	lifecycle wake 抢同一个 turn）→ 预算未耗尽（AllowAutoWake 只读判定，复用
//	wake 限流）→ 调度并投递。
func (h *Handler) runSupervisionProgressCheckForParent(
	ctx context.Context,
	scheduler *supervision.WakeScheduler,
	parent apiProgressCheckParent,
	now time.Time,
) (bool, error) {
	parentSessionID := strings.TrimSpace(parent.ParentSessionID)
	if h == nil || scheduler == nil || parentSessionID == "" {
		return false, nil
	}
	rootScopeID := strings.TrimSpace(parent.RootScopeID)
	if rootScopeID == "" {
		rootScopeID = parentSessionID
	}
	if !h.supervisionProgressParentRunnable(ctx, parentSessionID) {
		return false, nil
	}
	pending, err := h.getSupervisionStore().ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		UnclaimedOnly:         true,
	})
	if err != nil {
		return false, err
	}
	if len(pending) > 0 {
		// 已有一条待投递 wake：下一次 runnable transition 会投递它，本轮不再叠加
		// 第二个 turn（digest 里本来就含 progress 区块）。
		return false, nil
	}
	if h.supervisionHasUnresolvedCritical(ctx, rootScopeID) {
		// P0-2 改动 2：scope 内仍有未决的 critical 通知时让位。critical 的投递
		// 本身会带来同一个 digest（其中已含 progress 区块），progress 汇报不该
		// 抢在它前面消耗一次父 turn；critical 消除后巡查自动恢复。
		return false, nil
	}
	class := supervision.WakeBudgetClassOf(apiSupervisionProgressCheckReason)
	if !scheduler.AllowAutoWake(ctx, rootScopeID, class, now) {
		// 预算耗尽：progress 巡查不是决策，直接跳过而不是留下一条后续会被投递的
		// 陈旧 wake（critical lifecycle wake 仍按既有语义保持 durable）。
		return false, nil
	}
	if _, err := scheduler.ScheduleWake(ctx, supervision.WakeRequest{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		WakeReason:            apiSupervisionProgressCheckReason,
	}); err != nil {
		return false, err
	}
	if err := h.drainSupervisedParentWake(ctx, rootScopeID, parentSessionID); err != nil {
		if errors.Is(err, supervision.ErrWakeParentBusy) || errors.Is(err, supervision.ErrWakeRateLimited) {
			// 竞态（父会话在两次检查之间变忙）或预算判定后的边界情况：保持 durable
			// 语义，等下一次 transition 重试。
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// supervisionHasUnresolvedCritical reports whether the root scope still holds
// an unresolved critical notification. It reuses the same store read the
// preflight digest uses (ListNotifications) and adds no new access pattern; a
// read failure degrades to "no gate" so one best-effort tick keeps its
// historical behavior instead of blocking the sweep.
func (h *Handler) supervisionHasUnresolvedCritical(ctx context.Context, rootScopeID string) bool {
	store := h.getSupervisionStore()
	if store == nil {
		return false
	}
	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{
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

// supervisionProgressCheckParents 枚举本轮的父会话：只读 batch store，按最近活动
// 排序后去重、截断。这是"存在 active batch"判定的 API 版本——CLI 只有一个根会话，
// 所以把该判定写成按会话查一次；多会话宿主必须由 batch store 给出候选集。
func (h *Handler) supervisionProgressCheckParents(ctx context.Context) ([]apiProgressCheckParent, error) {
	batchStore := h.getSubagentBatchStore()
	if batchStore == nil {
		return nil, nil
	}
	batches, err := batchStore.ListBatches(ctx, subagentbatch.BatchFilter{
		Status: apiSupervisionProgressCheckActiveStatuses,
		Limit:  apiSupervisionProgressCheckBatchLimit,
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(batches, func(i, j int) bool {
		return apiBatchProgressTime(batches[i]).After(apiBatchProgressTime(batches[j]))
	})
	seen := make(map[string]struct{}, len(batches))
	parents := make([]apiProgressCheckParent, 0, len(batches))
	for _, batch := range batches {
		parentSessionID := strings.TrimSpace(batch.ParentSessionID)
		if parentSessionID == "" {
			continue
		}
		if _, ok := seen[parentSessionID]; ok {
			continue
		}
		seen[parentSessionID] = struct{}{}
		rootScopeID := strings.TrimSpace(batch.RootScopeID)
		if rootScopeID == "" {
			rootScopeID = parentSessionID
		}
		parents = append(parents, apiProgressCheckParent{
			RootScopeID:     rootScopeID,
			ParentSessionID: parentSessionID,
		})
		if len(parents) >= apiSupervisionProgressCheckParentLimit {
			break
		}
	}
	return parents, nil
}

// supervisionProgressParentRunnable 与 wake consumer 的 Runnable 门同口径
// （actor 存在且接受同一 turn 的 resume episode，见 supervisionWakeConsumer）：
// 挂起 turn（awaiting_obligations）允许注入进度汇报——汇报就是同一 turn 的新
// episode（Q10：不允许并发新 turn，但不阻塞 resume/steer）；执行中 / 等待审批 /
// 等待回答 / 回滚仍拒绝。两点差别都是巡查语境决定的：
//
//  1. 这里**不创建** actor：巡查是周期性提示，不该为历史/孤儿 batch 行凭空造
//     actor；未命中时父会话的下一轮自然 turn 仍会从 preflight digest 看到进度。
//  2. 除 actor 快照外还看 durable 运行状态：actor 快照可能比 store 旧（状态刚被
//     别处写入，actor 还没刷新），durable 一旦标忙同样拒绝；durable 读不到时只有
//     actor 自己给出非忙快照才允许注入（与 CLI 的 localSupervisionParentIdle 同口径：
//     读不到状态就不注入，宁可等下一次 transition，也不冒插进正在跑的 turn 的风险）。
func (h *Handler) supervisionProgressParentRunnable(ctx context.Context, sessionID string) bool {
	if h == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	hub := h.peekSessionHub()
	if hub == nil {
		return false
	}
	actor, ok := hub.Get(sessionID)
	if !ok || actor == nil {
		return false
	}
	snapshot, hasSnapshot := actor.StateSummary()
	if hasSnapshot && !snapshot.AcceptsResume() {
		return false
	}
	store := h.getSessionRuntimeStore()
	if store == nil {
		return hasSnapshot
	}
	state, err := store.LoadState(ctx, sessionID)
	if err != nil || state == nil {
		return hasSnapshot
	}
	return state.Summary().AcceptsResume()
}

// apiBatchProgressTime 取下 batch 行自带的最新时间戳（心跳优先，与
// supervision.batchProgressTime 同口径）：巡查先照顾最近有活动的父会话。
func apiBatchProgressTime(batch subagentbatch.SubagentBatch) time.Time {
	if !batch.HeartbeatAt.IsZero() {
		return batch.HeartbeatAt
	}
	return batch.UpdatedAt
}
