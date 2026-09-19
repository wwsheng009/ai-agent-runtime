// 待交互计数：汇总与比较（从 `entry.ts` 抽出，让 entry 只保留订阅状态机编排）。
import type { PendingInteractionState } from "@/lib/pending-interaction";

import type { SessionRuntimePendingCounts } from "./types";

/** 待交互条目 → 计数（呈现层仍按选中会话过滤，这里只做汇总）。 */
export function countPending(state: PendingInteractionState): SessionRuntimePendingCounts {
  let approvals = 0;
  let questions = 0;
  let planPending = false;
  for (const item of state.items) {
    if (item.status !== "pending" && item.status !== "resolving") {
      continue;
    }
    if (item.kind === "approval") {
      approvals += 1;
    } else if (item.kind === "question") {
      questions += 1;
    } else if (item.kind === "plan_review") {
      planPending = true;
    }
  }
  return { approvals, questions, planPending };
}

export function samePendingCounts(
  a: SessionRuntimePendingCounts,
  b: SessionRuntimePendingCounts,
): boolean {
  return (
    a.approvals === b.approvals &&
    a.questions === b.questions &&
    a.planPending === b.planPending
  );
}
