// 由 components/workspace/runtime-teams/shared.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 排序辅助（任务 / 成员 / 事件 / 信箱 / 路径租约 / 派发监控）

import {
  type RuntimePathClaimRecord,
  type RuntimeTeamEventRecord,
  type RuntimeTeamMailboxMessage,
  type RuntimeTeamTask,
  type RuntimeTeammateRecord,
} from "@/lib/runtime-api";
import { type DispatchMonitorEntry } from "@/components/workspace/runtime-teams/shared/types";

export function sortTasks(tasks: RuntimeTeamTask[]) {
  return [...tasks].sort((left, right) => {
    const leftPriority = left.priority ?? 0;
    const rightPriority = right.priority ?? 0;
    if (leftPriority !== rightPriority) {
      return rightPriority - leftPriority;
    }
    return (left.title || left.id).localeCompare(right.title || right.id);
  });
}

export function sortTeammates(teammates: RuntimeTeammateRecord[]) {
  return [...teammates].sort((left, right) =>
    (left.name || left.id).localeCompare(right.name || right.id),
  );
}

export function sortEvents(events: RuntimeTeamEventRecord[]) {
  return [...events].sort((left, right) => right.seq - left.seq);
}

export function sortMailbox(messages: RuntimeTeamMailboxMessage[]) {
  return [...messages].sort((left, right) => {
    const leftTime = left.created_at ? new Date(left.created_at).getTime() : 0;
    const rightTime = right.created_at ? new Date(right.created_at).getTime() : 0;
    return rightTime - leftTime;
  });
}

export function sortPathClaims(claims: RuntimePathClaimRecord[]) {
  return [...claims].sort((left, right) => {
    const leftLease = left.lease_until
      ? new Date(left.lease_until).getTime()
      : Number.POSITIVE_INFINITY;
    const rightLease = right.lease_until
      ? new Date(right.lease_until).getTime()
      : Number.POSITIVE_INFINITY;
    if (leftLease !== rightLease) {
      return rightLease - leftLease;
    }
    return left.path.localeCompare(right.path);
  });
}

export function sortDispatchMonitor(entries: DispatchMonitorEntry[]) {
  return [...entries].sort((left, right) => {
    const leftUpdated = left.updatedAt ? new Date(left.updatedAt).getTime() : 0;
    const rightUpdated = right.updatedAt ? new Date(right.updatedAt).getTime() : 0;
    return rightUpdated - leftUpdated;
  });
}
