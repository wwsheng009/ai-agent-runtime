package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// P2-9 方案 3「普通 spawn 回收」：自动 close 策略。
//
// 手动入口 `/agents cleanup`（chat_agent_cleanup.go）与 spawn 闸门共用同一套
// 判定；本文件补上第三处触发点——周期对账。对账循环每次跑完审计后调用本钩子，
// 于是终态容器（会话已关闭/归档、或容器已消失）不必等到下一次 spawn 被拒才释放
// 配额，"自动 close" 也不再是一句空话。
//
// 判定与事件口径完全复用 P2-8：`SweepAgentQuotaReclaim` /`ReclaimAgentQuota` /
// `ReclaimSourceReconcile`，所以「周期回收」与「闸门回收」「手工回收」在父事件流、
// `/debug` 与前端轨迹上口径一致，只靠 source 说明是谁触发的。
//
// observe 模式（默认）只评估不关闭：报告 `reclaim_candidates`/`reclaim_reasons`，
// 让运维先看到下一次 enforce 会释放什么，再决定是否切换（plan §P2-9 兼容与风险）。

// reclaimLocalAgentRegistryQuota is the host half of the periodic reclaim sweep:
// it resolves the writer, the shared idle policy and the event sink, then hands
// the audited roster to the agentcontrol driver so the decision path stays
// identical to the spawn gate and the manual cleanup entry.
func (r *localActorRegistry) reclaimLocalAgentRegistryQuota(ctx context.Context, records []agentcontrol.AgentRecord, enforce bool, now time.Time) (agentcontrol.ReclaimOutcome, error) {
	outcome := agentcontrol.ReclaimOutcome{}
	if r == nil {
		return outcome, fmt.Errorf("local agent registry is not initialized")
	}
	var reclaimStore agentcontrol.AgentReclaimStore
	if enforce {
		store := r.localAgentRegistryStore()
		if store == nil {
			return outcome, fmt.Errorf("local agent registry store is not initialized")
		}
		typed, ok := store.(agentcontrol.AgentReclaimStore)
		if !ok || typed == nil {
			return outcome, fmt.Errorf("local agent registry store does not support reclaim")
		}
		reclaimStore = typed
	}
	policy := agentcontrol.ReclaimPolicy{
		// 与 spawn 闸门同源：只有 agents.reclaimIdleMs > 0 时才驱逐空闲对象；
		// 终态/容器消失的判定不依赖该开关（P2-8 方案 3 的保守默认）。
		IdleTimeout: time.Duration(r.localAgentsConfig().ReclaimIdleMs) * time.Millisecond,
		Now:         now,
	}
	return agentcontrol.SweepAgentQuotaReclaim(
		ctx,
		reclaimStore,
		records,
		r.observeLocalQuotaChildren,
		policy,
		enforce,
		func(ctx context.Context, rootSessionID string, outcome agentcontrol.ReclaimOutcome) {
			// 周期回收没有"正在 spawn 的父会话"可归属：root 会话拥有整棵子树，
			// 事件就落在 root 的流上——那也是运维/前端正在观察的父流。
			r.recordLocalAgentReclaim(ctx, rootSessionID, rootSessionID, agentcontrol.ReclaimSourceReconcile, outcome)
		},
	)
}
