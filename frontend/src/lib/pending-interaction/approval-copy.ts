/**
 * 审批原因（`approval_requested.reason`）的中文说明。
 *
 * 运行时策略层给出的 reason 是稳定的机器键（如 permission_mode_requires_approval），
 * CLI 侧的 humanApprovalReason 维护同一份口径；这里补齐 Web 待办卡片的展示，
 * 未知键原样回显，避免隐藏真实原因。
 */
const APPROVAL_REASON_COPY: Record<string, string> = {
	permission_mode_requires_approval: "当前权限模式要求在执行前获得确认",
	"plan_mode:model_auto_enter": "模型请求进入计划模式：进入后会限制为只读探索、仅可写计划文件，需要你确认",
	approval_required: "当前操作需要审批",
	"manual approval": "当前工具策略要求人工审批",
};

export function approvalReasonText(reason: string): string {
	const trimmed = reason.trim();
	if (!trimmed) {
		return "";
	}
	return APPROVAL_REASON_COPY[trimmed.toLowerCase()] ?? trimmed;
}
