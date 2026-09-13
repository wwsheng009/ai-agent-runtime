import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";

import type { PendingPlanReviewInteraction } from "./types";

/** 计划评审条目的稳定身份前缀（与会话一一对应）。 */
export const PLAN_REVIEW_INTERACTION_PREFIX = "plan-review:";

export function pendingPlanReviewInteractionId(sessionId: string): string {
  return `${PLAN_REVIEW_INTERACTION_PREFIX}${sessionId}`;
}

/**
 * 计划模式激活时 → 计划评审条目（P1-7 统一呈现位）。
 *
 * 计划评审不是事件驱动，而是「计划模式是否 active」的投影：active 即有待决策，
 * 退出（inactive / exited）即不再呈现。`canSubmitPlanModeDecision` 的谓词与此一致。
 */
export function pendingPlanReviewFromPlan(
  plan: RuntimeSessionPlanMode | null,
  sessionId?: string,
): PendingPlanReviewInteraction | null {
  const id = sessionId?.trim();
  if (!plan?.active || !id) {
    return null;
  }
  const planPath = plan.plan_path?.trim();
  const notes = plan.notes?.trim();
  return {
    kind: "plan_review",
    id: pendingPlanReviewInteractionId(id),
    status: "pending",
    sessionId: id,
    ...(planPath ? { planPath } : {}),
    ...(notes ? { notes } : {}),
  };
}
