// 由 hooks/workspace/use-runtime-sessions-data.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type RuntimeSessionRecord } from "@/lib/runtime-api";

import { type RuntimeSessionsSummary } from "./types";

export function sortRuntimeSessions(sessions: RuntimeSessionRecord[]) {
  return [...sessions].sort((left, right) => {
    const leftTime = Date.parse(left.updatedAt || left.createdAt || "");
    const rightTime = Date.parse(right.updatedAt || right.createdAt || "");

    if (Number.isFinite(leftTime) && Number.isFinite(rightTime) && leftTime !== rightTime) {
      return rightTime - leftTime;
    }

    return left.id.localeCompare(right.id);
  });
}

export function summarizeRuntimeSessions(
  sessions: RuntimeSessionRecord[],
): RuntimeSessionsSummary {
  const sorted = sortRuntimeSessions(sessions);
  const activeCount = sorted.filter((session) => {
    const state = (session.state || "").trim().toLowerCase();
    return state === "" || state === "active" || state === "running" || state === "idle";
  }).length;
  const archivedCount = sorted.filter((session) => {
    const state = (session.state || "").trim().toLowerCase();
    return state === "archived" || state === "closed";
  }).length;

  return {
    activeCount,
    archivedCount,
    latestSessionId: sorted[0]?.id,
    latestUpdatedAt: sorted[0]?.updatedAt || sorted[0]?.createdAt,
    recoverableCount: activeCount,
    totalCount: sorted.length,
  };
}
