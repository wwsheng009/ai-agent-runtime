package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// P1-C 方案 C：batch 终态后的子会话自动收敛（API 宿主）。
//
// 与 CLI 宿主（cmd/aicli/commands/chat_actor_host.go 的
// localConvergeTerminalBatchChildren）同语义：batch 终态投影完成后，按
// agents.autoCloseCompleted 决定是否把「任务成功」的终态子会话真正关闭。此前
// API 宿主只投影终态行并推荐 close（apiBatchLifecycleProjector 的 default 分支），
// 收敛动作留给模型自己发 close_agent，于是 runtime-server 上的 spawn 线程配额要
// 等到模型想起这一步才释放。
const (
	// apiBatchConvergenceEventType 与 CLI 的 localBatchConvergenceEventType 同值：
	// 收敛行必须与 ProjectAgentCompletion 的 agent_completed 区分，否则「已完成」
	// 的存档行会把「待收敛」的动作需求顶掉。
	apiBatchConvergenceEventType = "agent_close_recommended"
)

// apiAutoCloseCompletedPolicy 解析 agents.autoCloseCompleted。未装配 runtime
// config 或字段未设置时回落 "off"（与实施前逐字节一致）；未知取值一律按 off
// 处理——配置校验已拒绝它们，这里再兜一层，避免把拼写错误解释成「自动关闭所有
// 子会话」。
func (h *Handler) apiAutoCloseCompletedPolicy() string {
	if h == nil || h.runtimeConfig == nil {
		return runtimecfg.AutoClosePolicyOff
	}
	switch policy := strings.ToLower(strings.TrimSpace(
		runtimecfg.NormalizeAgentsConfig(h.runtimeConfig.Agents).AutoCloseCompleted)); policy {
	case runtimecfg.AutoClosePolicyCompleted, runtimecfg.AutoClosePolicyBatchTerminal:
		return policy
	default:
		return runtimecfg.AutoClosePolicyOff
	}
}

// apiConvergeTerminalBatchChildren 是 API 宿主的 P1-C 收敛钩子：batch 终态后，
// 对「任务成功」的终态子会话先投影一条 unresolved 的收敛行（recommended_action=
// close），再通过 LocalControlService.Control 真正关闭子会话——关闭动作因此有
// durable action audit，回执由 ActionService 的 resolution 投影产出。
//
// 契约（与 CLI 逐条对齐）：
//   - opt-in：agents.autoCloseCompleted 默认 off，本函数在 off 时不产生任何投影/
//     动作；
//   - 只收敛成功的子会话：failed/timed_out/canceled 的终态子会话是父 agent 需要
//     判断的现场，批次级 critical/警告行已经覆盖它们；
//   - 幂等：重放（startup recovery + 实时投影）时收敛行已 decided/resolved，
//     ActionRequired() 为 false，不重复关闭、不重复写 audit；
//   - best-effort：控制面未装配执行器时连收敛行都不投影（悬空建议比没有建议更
//     糟）；单点失败不影响 batch 终态投影结果，也不阻塞其它子会话。
func (h *Handler) apiConvergeTerminalBatchChildren(ctx context.Context, terminal agent.BatchTerminalLifecycle) {
	policy := h.apiAutoCloseCompletedPolicy()
	if policy == runtimecfg.AutoClosePolicyOff {
		return
	}
	if h == nil || ctx == nil {
		return
	}
	store := h.getSupervisionStore()
	batches := h.getSubagentBatchStore()
	if store == nil || batches == nil {
		return
	}
	// 「completed」只收敛干净完成的批次；失败/超时/孤儿的现场交给批次级行。
	if policy == runtimecfg.AutoClosePolicyCompleted && terminal.Status != subagentbatch.BatchCompleted {
		return
	}
	actions := h.getSupervisionActionService()
	if actions == nil || !actions.ExecutorReady() {
		return
	}
	rootScopeID := strings.TrimSpace(terminal.RootScopeID)
	parentSessionID := strings.TrimSpace(terminal.ParentSessionID)
	batchID := strings.TrimSpace(terminal.BatchID)
	if rootScopeID == "" || batchID == "" {
		return
	}
	requestedBy := parentSessionID
	if requestedBy == "" {
		requestedBy = rootScopeID
	}
	tasks, err := batches.ListTasks(ctx, batchID)
	if err != nil {
		return
	}
	control := supervision.NewLocalControlService(store, actions)
	auditReason := fmt.Sprintf(
		"auto-close child session: subagent batch %s finished with status %s (agents.autoCloseCompleted=%s)",
		batchID, terminal.Status, policy)
	for _, task := range tasks {
		childSessionID := strings.TrimSpace(task.ChildSessionID)
		if childSessionID == "" || task.Status != subagentbatch.TaskSucceeded {
			continue
		}
		notification, err := supervision.ProjectLifecycle(ctx, store, nil, supervision.LifecycleProjection{
			RootScopeID:           rootScopeID,
			TargetParentSessionID: parentSessionID,
			SubjectKind:           supervision.SubjectAgentSession,
			SubjectID:             childSessionID,
			// 用 batch 终态版本做 subject version：同一终态的重复投影命中同一行。
			SubjectVersion: terminal.SubjectVersion,
			EventType:      apiBatchConvergenceEventType,
			// Severity 必须是 warning/critical：Notification.ActionRequired()
			//（以及控制面）拒绝对 info 行执行动作，否则建议会永久悬空。关闭动作
			// 落地的回执会把该行 resolution 置为 closed，从而停止打扰。
			Severity:          supervision.SeverityWarning,
			SupervisionState:  supervision.SupervisionTerminated,
			Reason:            fmt.Sprintf("child session %s finished with batch %s; close it to release the spawn thread quota", childSessionID, batchID),
			RecommendedAction: string(supervision.ActionClose),
			ResolutionState:   supervision.ResolutionUnresolved,
		})
		if err != nil {
			continue
		}
		if !notification.ActionRequired() || strings.TrimSpace(notification.NotificationID) == "" {
			// 已收敛（或已由父 agent 处理）：不重复关闭，也不重复写 audit。
			continue
		}
		if _, err := control.Control(ctx, supervision.ControlRequest{
			NotificationID: notification.NotificationID,
			Scopes:         []string{rootScopeID, parentSessionID},
			RequestedByID:  requestedBy,
			Action:         supervision.ActionClose,
			Reason:         auditReason,
		}); err != nil {
			// 拒绝/执行失败都已落 durable action 记录；收敛行保持可见，父 agent
			// 下一次 preflight 仍能按 next_action 处理。
			continue
		}
	}
}
