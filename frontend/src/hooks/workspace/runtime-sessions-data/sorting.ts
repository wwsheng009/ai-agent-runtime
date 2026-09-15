// 由 hooks/workspace/use-runtime-sessions-data.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type RuntimeSessionRecord } from "@/lib/runtime-api";

import { type RuntimeSessionsSummary } from "./types";

// 热路径说明：`summarizeRuntimeSessions` 在 workspace 页每次 render 都会调用（流式回复
// 期间每帧一次），而输入 `runtimeSessions` 绝大多数渲染里是**同一个数组引用**。原来的
// 实现每次 `[...sessions].sort(...)` 并在比较器里重复 `Date.parse`：300+ 会话约 2000 次
// 比较 = 每次渲染 4000 次 `Date.parse` + 一次数组克隆。这里做两件事：
//   1. 单槽记忆（按输入数组引用）——引用不变时直接复用上次结果；
//   2. 比较键预计算——每个会话只解析一次时间戳。
// 语义与原来完全一致（含 `Date.parse` 失败时退回 id 比较的分支）。

type SortEntry = {
  session: RuntimeSessionRecord;
  /** `Date.parse` 结果（可能为 NaN，表示不可比较）。 */
  time: number;
  /** 时间戳是否可用于排序，等价于原来的 `Number.isFinite(...)`。 */
  hasTime: boolean;
};

function toSortEntry(session: RuntimeSessionRecord): SortEntry {
  const time = Date.parse(session.updatedAt || session.createdAt || "");
  return { session, time, hasTime: Number.isFinite(time) };
}

let sortCacheInput: RuntimeSessionRecord[] | null = null;
let sortCacheOutput: RuntimeSessionRecord[] = [];

/**
 * 按最近更新时间倒序（时间不可用时按 id）。
 *
 * 同一输入数组引用会返回**同一个**结果数组；调用方只读，不要原地修改返回值。
 */
export function sortRuntimeSessions(sessions: RuntimeSessionRecord[]) {
  if (sessions === sortCacheInput) {
    return sortCacheOutput;
  }

  const sorted = sessions
    .map(toSortEntry)
    .sort((left, right) => {
      if (left.hasTime && right.hasTime && left.time !== right.time) {
        return right.time - left.time;
      }

      return left.session.id.localeCompare(right.session.id);
    })
    .map((entry) => entry.session);

  sortCacheInput = sessions;
  sortCacheOutput = sorted;
  return sorted;
}

let summaryCacheInput: RuntimeSessionRecord[] | null = null;
let summaryCacheOutput: RuntimeSessionsSummary | null = null;

/**
 * 汇总可恢复 / 已归档会话数。
 *
 * 同一输入数组引用返回同一个汇总对象（供下游 memo 比较复用）。
 */
export function summarizeRuntimeSessions(
  sessions: RuntimeSessionRecord[],
): RuntimeSessionsSummary {
  if (sessions === summaryCacheInput && summaryCacheOutput) {
    return summaryCacheOutput;
  }

  const sorted = sortRuntimeSessions(sessions);
  let activeCount = 0;
  let archivedCount = 0;
  for (const session of sorted) {
    const state = (session.state || "").trim().toLowerCase();
    if (
      state === "" ||
      state === "active" ||
      state === "running" ||
      state === "idle"
    ) {
      activeCount += 1;
    } else if (state === "archived" || state === "closed") {
      archivedCount += 1;
    }
  }

  const summary: RuntimeSessionsSummary = {
    activeCount,
    archivedCount,
    latestSessionId: sorted[0]?.id,
    latestUpdatedAt: sorted[0]?.updatedAt || sorted[0]?.createdAt,
    recoverableCount: activeCount,
    totalCount: sorted.length,
  };

  summaryCacheInput = sessions;
  summaryCacheOutput = summary;
  return summary;
}
