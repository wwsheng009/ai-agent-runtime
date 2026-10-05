package runtimeapi

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// Step 4：等待期兜底反馈的独立默认环（2026-10-05）。
//
// 背景：Step 2/4 把 wait_feedback 的调度挂进了 progress_check_interval 的巡检，
// 于是 API 宿主的兜底反馈跟着这个 opt-in 开关一起默认关闭——而反馈本身是默认
// 开启的兜底语义（CLI 把同一 sweep 挂在默认开启的 wake fallback 环上，见
// cmd/aicli/commands/chat_actor_wake_fallback.go）。本文件把反馈计时与巡查解耦：
//
//   - progress 巡查（batch 进度汇报）保持 opt-in（progress_check_interval > 0）；
//   - wait_feedback 扫描走独立默认环，节奏复用 wake_fallback_interval 的宿主
//     解释：0=默认 60s，负值=显式关闭，正数=不低于 15s 下限（与 CLI 的
//     localWakeFallbackInterval 逐条同构）。
//
// 候选父会话沿用巡查的枚举口径：batch store 里活跃批次所属的父会话，单轮有界
// （apiSupervisionProgressCheckParentLimit / BatchLimit）。真正决定“是否排反馈”
// 的仍然是 supervision.ScheduleWaitFeedbackWake 的挂起记录 / 进展节拍 / pending
// / critical / 预算判定，本环只提供默认开启的时钟；每 tick 只做一次有界的
// durable 读，空闲时零副作用。
//
// 边界：候选集来自活跃批次，纯 team 挂起的父会话（没有 batch 行）不在本环的
// 枚举范围内——这类会话仍依赖下一次自然 turn 的 preflight digest；扩大枚举需要
// 跨会话的挂起记录读取口，属独立议题，不在本次解耦内。

// apiWaitFeedbackSweepInterval resolves the effective wait-feedback sweep
// cadence with the same knob and semantics as the CLI wake fallback: unset (0)
// means the default-on 60s sweep, negative disables it (historical "natural
// turn only" behavior), and a positive value is clamped to the 15s floor.
func apiWaitFeedbackSweepInterval(cfg supervision.Config) time.Duration {
	switch {
	case cfg.WakeFallbackInterval < 0:
		return 0
	case cfg.WakeFallbackInterval == 0:
		return supervision.DefaultWakeFallbackInterval
	case cfg.WakeFallbackInterval < supervision.MinWakeFallbackInterval:
		return supervision.MinWakeFallbackInterval
	default:
		return cfg.WakeFallbackInterval
	}
}

// syncSupervisionWaitFeedbackSweep lets the independent feedback loop follow the
// config: default-on, disabled by a negative wake_fallback_interval, and
// restarted in place on every reconfigure (same idempotent stop-then-start
// contract as syncSupervisionProgressCheck).
func (h *Handler) syncSupervisionWaitFeedbackSweep() {
	if h == nil {
		return
	}
	h.StopSupervisionWaitFeedbackSweep()
	interval := apiWaitFeedbackSweepInterval(h.supervisionTuning())
	if interval <= 0 {
		return
	}
	// 控制面还没接线时等下一次 SetSupervisionConfig（宿主总是最后调用它），
	// 不留一个每 tick 空转的 goroutine；scheduler 缺失时 sweep 本身也无事可做。
	if h.getSupervisionStore() == nil || h.getSupervisionWakeScheduler() == nil {
		return
	}
	h.startSupervisionWaitFeedbackSweep(interval)
}

// startSupervisionWaitFeedbackSweep starts the loop; already running is a no-op.
func (h *Handler) startSupervisionWaitFeedbackSweep(interval time.Duration) {
	if h == nil || interval <= 0 {
		return
	}
	h.supervisionWaitFeedbackMu.Lock()
	if h.supervisionWaitFeedbackStop != nil {
		h.supervisionWaitFeedbackMu.Unlock()
		return
	}
	// API 宿主没有 CLI 那样的 lifecycleCtx（进程即宿主），因此反馈环挂在自己的
	// root context 上，由 StopSupervisionWaitFeedbackSweep / 重配来收敛。
	ctx, cancel := context.WithCancel(context.Background())
	h.supervisionWaitFeedbackStop = cancel
	h.supervisionWaitFeedbackWG.Add(1)
	h.supervisionWaitFeedbackMu.Unlock()

	go func() {
		defer h.supervisionWaitFeedbackWG.Done()
		h.runSupervisionWaitFeedbackSweepLoop(ctx, interval)
	}()
}

// StopSupervisionWaitFeedbackSweep stops the loop and waits for it to exit;
// not running is a no-op (same convergence contract as the progress check).
func (h *Handler) StopSupervisionWaitFeedbackSweep() {
	if h == nil {
		return
	}
	h.supervisionWaitFeedbackMu.Lock()
	cancel := h.supervisionWaitFeedbackStop
	h.supervisionWaitFeedbackStop = nil
	h.supervisionWaitFeedbackMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	h.supervisionWaitFeedbackWG.Wait()
}

// runSupervisionWaitFeedbackSweepLoop is the loop body: one bounded sweep per
// tick, best-effort (a failed tick never stops the loop).
func (h *Handler) runSupervisionWaitFeedbackSweepLoop(ctx context.Context, interval time.Duration) {
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
			if _, err := h.runSupervisionWaitFeedbackSweepOnce(ctx); err != nil {
				logger.Warnf("supervision: wait feedback sweep failed: %v", err)
			}
		}
	}
}

// runSupervisionWaitFeedbackSweepOnce runs one bounded feedback sweep over the
// same candidate parents as the progress check and returns how many feedback
// wakes were actually scheduled. Tests call it directly instead of waiting for
// the ticker.
func (h *Handler) runSupervisionWaitFeedbackSweepOnce(ctx context.Context) (int, error) {
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
	// 反馈 wake 的 digest 内容同样只能来自投影（rollup / 账本）；调度前必须保证
	// scheduler 能看到接线后的投影。接线幂等（与 runSupervisionProgressCheckOnce
	// 同一理由——progress wake 没有 lifecycle 行可依赖），scheduler 晚于 batch
	// store 装配时这里也能补上。
	h.wireSupervisionSources()
	parents, err := h.supervisionProgressCheckParents(ctx)
	if err != nil {
		return 0, err
	}
	var (
		scheduled int
		firstErr  error
	)
	for _, parent := range parents {
		parentSessionID := strings.TrimSpace(parent.ParentSessionID)
		if parentSessionID == "" {
			continue
		}
		rootScopeID := strings.TrimSpace(parent.RootScopeID)
		if rootScopeID == "" {
			rootScopeID = parentSessionID
		}
		// 活跃 run / 等待审批 / 待回答的父会话保持静默：反馈是空闲等待期的兜底，
		// 不能插进正在跑的 turn（与巡查共用同一 runnable 门）。
		if !h.supervisionProgressParentRunnable(ctx, parentSessionID) {
			continue
		}
		ok, err := h.maybeScheduleWaitFeedbackWake(ctx, scheduler, parentSessionID, rootScopeID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ok {
			scheduled++
		}
	}
	return scheduled, firstErr
}
