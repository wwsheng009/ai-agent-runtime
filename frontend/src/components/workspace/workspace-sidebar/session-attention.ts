// Batch 3（多会话并发运行时 §4.7.2 / §4.8）：侧栏「谁在等我 / 谁能停」的纯判定。
//
// 与 `session-row-status.ts` 的分工：那里决定「行状态图标」；这里只回答两个
// 交互问题——哪些会话需要用户处理（顶栏/侧栏聚合入口）、哪些会话可以就地停止。
// 两者共用同一套活动信号（`SidebarSessionActivity`），不引入新的状态来源。

import { type SidebarSessionActivity } from "./session-row-status";

/** 「在等我」判据：等待类信号（审批 / 计划 / 回答），运行中不算。 */
export function isSessionWaitingOnUser(
  activity?: SidebarSessionActivity | undefined,
): boolean {
  if (!activity) {
    return false;
  }
  return (
    (activity.pendingApprovals ?? 0) > 0 ||
    activity.planPending === true ||
    activity.waitingAnswer === true
  );
}

/**
 * 待办聚合（§4.7.2）：按投影顺序收集「在等我」的会话 id。
 * 顺序保持投影顺序（侧栏行的顺序），点击入口因此可预期地落到最靠前的一条。
 */
export function collectAttentionSessionIds(
  activity: Record<string, SidebarSessionActivity | undefined> | undefined,
): string[] {
  if (!activity) {
    return [];
  }
  const result: string[] = [];
  for (const [sessionId, signal] of Object.entries(activity)) {
    const normalized = sessionId.trim();
    if (!normalized || !isSessionWaitingOnUser(signal)) {
      continue;
    }
    result.push(normalized);
  }
  return result;
}

/**
 * 就地停止入口（§4.8）：只要有在途回合（运行中 / 子代理）或等待用户裁决的挂起项，
 * 就允许投递 `interrupt`。等待类同样可停——用户可能想直接放弃这个回合，
 * 而不是先处理审批再停。
 */
export function shouldOfferSessionStop(
  activity?: SidebarSessionActivity | undefined,
): boolean {
  if (!activity) {
    return false;
  }
  return (
    activity.running === true ||
    (activity.runningAgents ?? 0) > 0 ||
    isSessionWaitingOnUser(activity)
  );
}
