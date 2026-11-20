// 侧栏「最近会话」时间桶（P1-7）：今天 / 昨天。
//
// 目录分组假设用户按项目回忆；真实场景里更常见的线索是「什么时候做的」。
// 该纯函数把跨目录会话按本地日历日聚成两个短列表，作为目录树之上的快捷带，
// 不改变目录树本身的分组与顺序账目。

import type { RuntimeSessionRecord } from "@/types/runtime";

export type RecentSessionBucketId = "today" | "yesterday";

export type RecentSessionBucket = {
  id: RecentSessionBucketId;
  sessions: RuntimeSessionRecord[];
};

const DAY_MS = 24 * 60 * 60 * 1000;

function sessionTimestamp(session: RuntimeSessionRecord): number {
  const raw = session.updatedAt || session.createdAt || "";
  const parsed = Date.parse(raw);
  return Number.isNaN(parsed) ? 0 : parsed;
}

function startOfLocalDay(now: Date): number {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
}

export function buildRecentSessionBuckets(
  sessions: readonly RuntimeSessionRecord[],
  options: { now?: Date; limitPerBucket?: number } = {},
): RecentSessionBucket[] {
  const now = options.now ?? new Date();
  const limit = Math.max(1, Math.trunc(options.limitPerBucket ?? 3));
  const todayStart = startOfLocalDay(now);
  const yesterdayStart = todayStart - DAY_MS;

  const sorted = [...sessions]
    .map((session) => ({ session, timestamp: sessionTimestamp(session) }))
    .filter((entry) => entry.timestamp > 0)
    .sort((left, right) => right.timestamp - left.timestamp);

  const seen = new Set<string>();
  const buckets: RecentSessionBucket[] = [];

  for (const id of ["today", "yesterday"] as const) {
    const lower = id === "today" ? todayStart : yesterdayStart;
    const upper = id === "today" ? todayStart + DAY_MS : todayStart;
    const items: RuntimeSessionRecord[] = [];

    for (const entry of sorted) {
      if (items.length >= limit) {
        break;
      }
      if (entry.timestamp < lower || entry.timestamp >= upper) {
        continue;
      }
      if (seen.has(entry.session.id)) {
        continue;
      }
      seen.add(entry.session.id);
      items.push(entry.session);
    }

    if (items.length > 0) {
      buckets.push({ id, sessions: items });
    }
  }

  return buckets;
}