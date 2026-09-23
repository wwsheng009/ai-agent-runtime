package skills

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// apiBatchLifecycleProjector 是 API 宿主的 spawn_subagents 终态桥，与 CLI 的
// localSubagentBatchLifecycleProjector 同语义：agent 包不依赖 supervision，由
// 宿主把 host-neutral 的 BatchTerminalLifecycle 翻成 durable 生命周期通知，
// 再给 wake consumer 一个可运行转换点。
//
// 缺口背景（2026-09-16）：API 宿主此前只装 subagent scheduler，不装 lifecycle
// projector，也不 drain wake。后台 batch 终态（失败/超时/孤儿/干净完成）因此只
// 留在 agent 包内部：supervision store 没有行、digest/preflight 没有内容、父
// 会话也收不到唤醒 ⇒ runtime-server 上的父/leader agent 永远等不到"batch 结束"
// 的汇报点（CLI 早已具备）。本桥把该对等能力补到 API 路径。
//
// 投影之后按 agents.autoCloseCompleted 收敛已完成的子会话（P1-C 方案 C，见
// supervision_autoclose.go）：策略默认 off，此时该调用不产生任何写入，宿主行为
// 与仅投影时逐字节一致。
func (h *Handler) apiBatchLifecycleProjector() agent.BatchLifecycleProjector {
	return func(ctx context.Context, terminal agent.BatchTerminalLifecycle) error {
		store := h.getSupervisionStore()
		if store == nil {
			return fmt.Errorf("api supervision control plane is not configured")
		}
		if ctx == nil {
			ctx = context.Background()
		}
		rootScopeID := strings.TrimSpace(terminal.RootScopeID)
		if rootScopeID == "" {
			rootScopeID = strings.TrimSpace(terminal.ParentSessionID)
		}
		parentSessionID := strings.TrimSpace(terminal.ParentSessionID)
		batchID := strings.TrimSpace(terminal.BatchID)
		if rootScopeID == "" || parentSessionID == "" || batchID == "" {
			return fmt.Errorf("subagent batch lifecycle requires root, parent and batch ids")
		}

		severity := supervision.SeverityInfo
		supervisionState := supervision.SupervisionTerminated
		resolution := supervision.ResolutionClosed
		recommended := string(supervision.ActionInspect)
		reason := fmt.Sprintf("subagent batch %s finished with status %s (%d/%d completed)",
			batchID, terminal.Status, terminal.CompletedCount, terminal.TaskCount)
		switch terminal.Status {
		case subagentbatch.BatchFailed:
			severity = supervision.SeverityCritical
			supervisionState = supervision.SupervisionBlocked
			resolution = supervision.ResolutionUnresolved
		case subagentbatch.BatchTimedOut:
			severity = supervision.SeverityCritical
			supervisionState = supervision.SupervisionTimedOut
			resolution = supervision.ResolutionUnresolved
			recommended = string(supervision.ActionCancel)
		case subagentbatch.BatchOrphaned:
			severity = supervision.SeverityCritical
			supervisionState = supervision.SupervisionOrphaned
			resolution = supervision.ResolutionUnresolved
			recommended = string(supervision.ActionCancel)
		case subagentbatch.BatchCanceled:
			severity = supervision.SeverityWarning
		default:
			// P1-C：干净完成的 batch 推荐"收敛"（模型用 close_agent 关闭已结束
			// 的子会话）。该行本身 resolution=closed，evaluator 对已关闭行只允许
			// inspect（plan §4.1.1），所以这里给的是模型面提示，而不是让模型对
			// 终态行发 control 动作。
			if terminal.FailedCount == 0 && terminal.TimedOutCount == 0 {
				recommended = string(supervision.ActionClose)
			}
		}
		if strings.TrimSpace(terminal.Error) != "" {
			reason += ": " + strings.TrimSpace(terminal.Error)
		}

		if _, err := supervision.ProjectLifecycle(ctx, store, h.getSupervisionWakeScheduler(), supervision.LifecycleProjection{
			RootScopeID:           rootScopeID,
			TargetParentSessionID: parentSessionID,
			SubjectKind:           supervision.SubjectAgentRun,
			SubjectID:             batchID,
			SubjectVersion:        terminal.SubjectVersion,
			EventType:             terminal.EventType,
			Severity:              severity,
			SupervisionState:      supervisionState,
			Reason:                reason,
			RecommendedAction:     recommended,
			AllowedActions:        []string{string(supervision.ActionInspect), string(supervision.ActionCancel), string(supervision.ActionClose)},
			ResolutionState:       resolution,
		}); err != nil {
			return err
		}

		// P1-C 方案 C：batch 终态投影之后按策略收敛已完成的子会话（默认 off
		// 时该调用不产生任何投影/动作）。
		h.apiConvergeTerminalBatchChildren(ctx, terminal)

		// 与 CLI 一致：ProjectLifecycle 只为未决 critical 建 wake；这里再 drain
		// 一次，让 info/warning（以及已存在的 pending wake）在父会话空闲时立刻起
		// 一轮汇报，而不是等到下个自然 turn 的 turn-end self-check。
		if err := h.drainSupervisedParentWake(ctx, rootScopeID, parentSessionID); err != nil {
			// 父忙/预算用尽是 durable 控制面的正常结果：wake 保持 pending，下个
			// 自然 turn 的 preflight 仍会注入，不能算投影失败。
			if errors.Is(err, supervision.ErrWakeParentBusy) || errors.Is(err, supervision.ErrWakeRateLimited) {
				return nil
			}
			return err
		}
		return nil
	}
}

// drainSupervisedParentWake 是 API 宿主的 wake 投递入口：与控制器
// sessionAgentController.wakeSupervisedParent 共用同一个 consumer 实现，避免
// batch 终态与 approval 事件走两套 admission 语义。
func (h *Handler) drainSupervisedParentWake(ctx context.Context, rootScopeID, parentSessionID string) error {
	if h == nil {
		return nil
	}
	scheduler := h.getSupervisionWakeScheduler()
	if scheduler == nil {
		return nil
	}
	consumer := h.supervisionWakeConsumer(scheduler)
	if consumer == nil {
		return nil
	}
	return consumer.MaybeWakeParent(ctx, parentSessionID, "", rootScopeID)
}

// supervisionWakeConsumer 是 API 宿主唯一的 wake consumer 构造点（batch 终态桥
// 与 sessionAgentController 都委托到它）。runnable 门（actor 存在且不忙）与投递
// 路径（SubmitPromptAsync 入队，不阻塞整个父 turn）必须两处一致，否则 batch
// 终态可能绕过父会话 admission 直接起 turn。
//
// consumer 按 scheduler 指针缓存并复用（同 CLI 的 host.supervisionWake）：投递
// 实现只有一份，宿主/测试可以覆盖 handler.supervisionWake 来断言投递次数；
// scheduler 被替换时（Wakes 指针变了）自动重建，避免旧 consumer 继续用旧限流。
func (h *Handler) supervisionWakeConsumer(scheduler *supervision.WakeScheduler) *supervision.WakeConsumer {
	if h == nil || scheduler == nil {
		return nil
	}
	h.supervisionWakeMu.Lock()
	defer h.supervisionWakeMu.Unlock()
	if h.supervisionWake != nil && h.supervisionWake.Wakes == scheduler {
		return h.supervisionWake
	}
	h.supervisionWake = &supervision.WakeConsumer{
		Wakes: scheduler,
		// A6：与 CLI 宿主同口径——resume 先过同一套门控（MaxConcurrent /
		// MaxDepth）。瞬时容量（槽位占满）⇒ 排队 + 位次，不丢 wake；静态策略
		//（深度越界）不可恢复，禁止排队，改为放行 + digest 的 resume_gate 行
		// 显式告知本回合不得再派发。
		ResumeCapacity: supervision.CombineResumeProbes(
			supervision.NewSubagentCapacityProbe(h.subagentCapacityView),
			supervision.NewResumePolicyGate(h.resumePolicyView),
		),
		Runnable: func(_ context.Context, _, parentSessionID, _ string) bool {
			actor := h.apiSessionActor(parentSessionID)
			if actor == nil {
				return false
			}
			state, ok := actor.StateSummary()
			return ok && !state.Busy()
		},
		Deliver: func(ctx context.Context, parentSessionID, _ string, _ *supervision.Digest, _ []string) error {
			actor := h.apiSessionActor(parentSessionID)
			if actor == nil {
				return fmt.Errorf("parent actor not found")
			}
			return actor.SubmitPromptAsync(ctx, supervision.AutoWakePrompt, h.apiSessionRunMeta(ctx, parentSessionID))
		},
	}
	return h.supervisionWake
}

// resumePolicyView 返回 A6 的**静态**派发门控视图（深度）。口径与
// applyAPIAgentChildDepthPolicy 完全一致：depth >= ceiling 的会话在重建时会被
// 禁掉 spawn_*，因此这类会话 resume 出来的回合同样不能再派发。
//
// 命中时**不拒绝 resume**（纯汇报型回合不需要 spawn，拒绝会让它永久排队并不断
// 升级）：放行并由 digest 的 resume_gate 行显式告知本回合不得再派发。查询失败
// 一律 fail-open——门控不可读不得卡住 supervision。
func (h *Handler) resumePolicyView(ctx context.Context, req supervision.ResumeCapacityRequest) supervision.ResumePolicyView {
	if h == nil || h.sessionManager == nil {
		return supervision.ResumePolicyView{}
	}
	parentSessionID := strings.TrimSpace(req.TargetParentSessionID)
	if parentSessionID == "" {
		return supervision.ResumePolicyView{}
	}
	ceiling := apiAgentDepthCeiling(h.runtimeConfig)
	if ceiling <= 0 {
		return supervision.ResumePolicyView{}
	}
	session, err := h.sessionManager.Get(ctx, parentSessionID)
	if err != nil || session == nil {
		return supervision.ResumePolicyView{}
	}
	if depth := apiAgentSessionDepth(session); depth >= ceiling {
		return supervision.ResumePolicyView{
			Restricted: true,
			Reason:     supervision.ResumeGateDepth,
			Detail:     fmt.Sprintf("depth=%d max=%d", depth, ceiling),
		}
	}
	return supervision.ResumePolicyView{}
}

// apiSessionActor 解析/按需创建会话 actor（沿用 hub.Get 命中优先的口径：已存在
// 的 actor 必须原样复用，不能被 GetOrCreate 重建）。
func (h *Handler) apiSessionActor(sessionID string) *chat.SessionActor {
	if h == nil {
		return nil
	}
	hub := h.getSessionHub()
	if hub == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if actor, ok := hub.Get(sessionID); ok && actor != nil {
		return actor
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		return nil
	}
	return actor
}

// apiSessionRunMeta 与 sessionAgentController.apiAgentRunMeta 同源：wake turn 的
// RunMeta 必须由真会话重建（SpawnAgentRunMetaFromContext 会把普通 spawn 子会话的
// completion_requirement 归一为 none）。
func (h *Handler) apiSessionRunMeta(ctx context.Context, sessionID string) *team.RunMeta {
	if h == nil || h.sessionManager == nil {
		return nil
	}
	session, err := h.sessionManager.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil || session == nil {
		return nil
	}
	return toolbroker.SpawnAgentRunMetaFromContext(session)
}
