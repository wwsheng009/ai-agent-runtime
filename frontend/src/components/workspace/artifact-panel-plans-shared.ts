// 「计划归档」面的纯函数（与 artifact-panel-shared.ts 同定位）：
// 组件文件只导出组件（react-refresh/only-export-components），状态/决策的文案键与摘要
// 逻辑放这里，便于单测直接钉住边界。

import type { RuntimeStoredPlan } from "@/types/runtime";

/** 备注摘要上限：列表里只给判读片段，完整备注不展开（阅读面不抢正文位置）。 */
export const ROUND_NOTES_SUMMARY_MAX = 140;

/** 动态文案键前缀（`t()` 需要完整键路径，不能只给叶子名）。 */
const PLAN_I18N_PREFIX = "panels.artifacts.plans.";

export const STORED_PLAN_STATUS_I18N_KEYS: Record<string, string> = {
  pending: "pending",
  approved: "approved",
  not_implemented: "notImplemented",
};

const PLAN_DECISION_I18N_KEYS: Record<string, string> = {
  approve: "approve",
  request_changes: "requestChanges",
  quit: "quit",
  enter: "enter",
};

/** 状态徽标配色：待评审=橙（待办）、已批准=青（通过）、未实施=灰（终态）。 */
export function storedPlanStatusClass(status: string) {
  switch (status) {
    case "pending":
      return "border-accent-orange/30 bg-accent-orange/10 text-accent-orange";
    case "approved":
      return "border-accent-teal/30 bg-accent-teal/10 text-accent-teal";
    case "not_implemented":
      return "border-white/12 bg-white/5 text-muted-foreground";
    default:
      return undefined;
  }
}

/** 未知状态不猜语义：原文透传（后端新增状态时不至于显示成空白）。 */
export function storedPlanStatusLabel(status: string) {
  const normalized = status.trim();
  if (!normalized) {
    return "—";
  }
  return STORED_PLAN_STATUS_I18N_KEYS[normalized]
    ? `${PLAN_I18N_PREFIX}status.${STORED_PLAN_STATUS_I18N_KEYS[normalized]}`
    : normalized;
}

export function storedPlanDecisionLabel(decision?: string) {
  const normalized = decision?.trim();
  if (!normalized) {
    return "—";
  }
  return PLAN_DECISION_I18N_KEYS[normalized]
    ? `${PLAN_I18N_PREFIX}decisions.${PLAN_DECISION_I18N_KEYS[normalized]}`
    : normalized;
}

export function summarizeRoundNotes(notes?: string) {
  const normalized = notes?.replace(/\s+/g, " ").trim() ?? "";
  if (normalized.length <= ROUND_NOTES_SUMMARY_MAX) {
    return normalized;
  }
  return `${normalized.slice(0, ROUND_NOTES_SUMMARY_MAX - 1)}…`;
}

export function formatStoredPlanProject(plan: RuntimeStoredPlan) {
  return plan.project_slug?.trim() || plan.project?.trim() || plan.project_path?.trim() || "";
}

export function formatStoredPlanTitle(plan: RuntimeStoredPlan) {
  return plan.title?.trim() || plan.plan_path?.trim() || plan.id;
}
