package supervision

import "strings"

// P2-12 方案 1：动作集合按宿主能力过滤。
//
// EvaluateAllowedActions 描述的是「标的允许什么」，与宿主无关——durable 行必须
// 保持宿主中立，否则同一条通知在不同宿主读出来会成为两套事实。但 preflight /
// digest / snapshot / CLI list 是**宣告**：宣告的必须是「这个宿主真的能执行的
// 动作」。宿主没有 wired executor 时照抄 cancel/close，模型会去规划一个必然失败
// 的步骤（历史症状：control_descendant 返回 "no executor configured"，父回合
// 白白多跑一轮）。
//
// 因此口径分两层，且由同一函数计算：
//   - 执行层（enforcement 与 durable 行）：EvaluateAllowedActions(n)，宿主中立；
//   - 宣告层（digest / snapshot / CLI list）：EvaluateAllowedActionsForHost(n, caps)，
//     在宿主中立集合上减去该宿主没有入口的动作，并在 NextAction 里说明原因。
//
// 能力只影响宣告，永远不能放宽执行：LocalControlService.requireAllowed 与
// ActionService.RequestAction 仍然用宿主中立集合复核，declaring 一个能力不会
// 给宿主增加任何权限。

const (
	// HintAcknowledgeRequiresLocalCommand is emitted when the notification would
	// allow acknowledge/defer but this host exposes no decision channel
	// (CLI `/debug supervision ack|defer|resolve`, model `ack_lifecycle`).
	HintAcknowledgeRequiresLocalCommand = "ack_requires_local_command"
	// HintControlRequiresActionExecutor is emitted when the notification would
	// allow cancel/close/cancel_subtree but this host has no wired durable
	// action executor, so the mutation could only be recorded, never executed.
	HintControlRequiresActionExecutor = "control_requires_wired_action_executor"
)

// HostCapabilities declares which notification action channels the calling host
// has actually wired.
type HostCapabilities struct {
	// DecisionActions is true when acknowledge/defer/resolve have an entry
	// point in this host.
	DecisionActions bool
	// ControlActions is true when cancel/close/cancel_subtree map to a wired
	// runtime executor (ActionService.ExecutorReady).
	ControlActions bool
}

// FullHostCapabilities is the "everything wired" declaration. Hosts that have
// not been audited yet pass a nil *HostCapabilities instead, which keeps the
// pre-P2-12 behavior (nothing filtered) rather than guessing.
func FullHostCapabilities() HostCapabilities {
	return HostCapabilities{DecisionActions: true, ControlActions: true}
}

// Supports reports whether this host can execute the action kind. inspect is
// always reachable: it is a read, served by the digest/snapshot itself.
func (c HostCapabilities) Supports(action ActionKind) bool {
	switch action {
	case ActionAcknowledge, ActionDefer:
		return c.DecisionActions
	case ActionCancel, ActionClose, ActionCancelSubtree:
		return c.ControlActions
	default:
		return true
	}
}

// EvaluateAllowedActionsForHost returns the action kinds this host may announce
// for the notification, plus a next_action hint describing the remediation
// paths that were filtered out (empty when nothing was filtered). A nil caps
// means "capabilities not declared": the host-neutral set is returned unchanged,
// so missing wiring can never hide a real remediation path.
func (e Evaluator) EvaluateAllowedActionsForHost(n Notification, caps *HostCapabilities) ([]string, string) {
	allowed := e.EvaluateAllowedActions(n)
	if caps == nil {
		return allowed, ""
	}
	filtered := make([]string, 0, len(allowed))
	decisionDropped := false
	controlDropped := false
	for _, value := range allowed {
		action := ActionKind(strings.TrimSpace(value))
		if caps.Supports(action) {
			filtered = append(filtered, value)
			continue
		}
		switch action {
		case ActionAcknowledge, ActionDefer:
			decisionDropped = true
		case ActionCancel, ActionClose, ActionCancelSubtree:
			controlDropped = true
		}
	}
	return filtered, ActionCapabilityHint(decisionDropped, controlDropped)
}

// ActionCapabilityHint renders the stable next_action hint for filtered
// channels. It is exported so hosts can reuse the same wording when they
// explain a rejected command.
func ActionCapabilityHint(decisionDropped, controlDropped bool) string {
	hints := make([]string, 0, 2)
	if decisionDropped {
		hints = append(hints, HintAcknowledgeRequiresLocalCommand)
	}
	if controlDropped {
		hints = append(hints, HintControlRequiresActionExecutor)
	}
	return strings.Join(hints, "; ")
}
