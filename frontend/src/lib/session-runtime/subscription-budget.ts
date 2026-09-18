// Batch 4（多会话并发运行时 §4.6）：订阅预算与 poll 名额调度。
//
// 从 `registry.ts` 抽出（P0-2 单文件 ≤500 非空行）：这里只回答「还有没有名额、
// 谁该被腾挪」，不持有模式切换真相（`applyDesiredMode` 仍在 registry）。
// `records` 与 `notify` 由注册表注入，模块本身无 React / 无 IO。

import type { SessionRuntimeEntryRecord as EntryRecord } from "./registry-types";

export type SubscriptionBudgetOptions = {
  records: Map<string, EntryRecord>;
  /** 后台 live 名额（前台恒有 1 个名额，不在此判定）。 */
  backgroundLiveBudget: number;
  /** poll 订阅上限。 */
  maxPollSessions: number;
  notify: (sessionId: string) => void;
};

export type SubscriptionBudget = {
  liveCount(role: "foreground" | "background"): number;
  backgroundLiveSlotsExhausted(): boolean;
  /** 显式升级：按最近活动 LRU 顶掉一条后台 live（腾出名额给新会话）。 */
  evictOldestBackgroundLive(exceptSessionId: string): void;
  /** 进入 poll：名额已满则保持 idle 等待补位（`deferredPoll`）。 */
  enterPollOrDefer(record: EntryRecord): void;
  /** 腾出 poll 名额后按等待顺序补位。 */
  promoteDeferredPoll(): void;
};

export function createSubscriptionBudget(
  options: SubscriptionBudgetOptions,
): SubscriptionBudget {
  const { records, backgroundLiveBudget, maxPollSessions, notify } = options;

  function liveCount(role: "foreground" | "background"): number {
    let count = 0;
    for (const record of records.values()) {
      if (record.role === role && record.entry.snapshot().mode === "live") {
        count += 1;
      }
    }
    return count;
  }

  function backgroundLiveSlotsExhausted(): boolean {
    return liveCount("background") >= backgroundLiveBudget;
  }

  /** 当前 poll 订阅数（可排除某个会话：名额判定要把自己摘出去）。 */
  function pollCount(excludeSessionId = ""): number {
    let count = 0;
    for (const [sessionId, record] of records) {
      if (sessionId === excludeSessionId) {
        continue;
      }
      if (record.entry.snapshot().mode === "poll") {
        count += 1;
      }
    }
    return count;
  }

  function pollSlotsExhausted(excludeSessionId = ""): boolean {
    return pollCount(excludeSessionId) >= maxPollSessions;
  }

  /** 腾出 poll 名额后按等待顺序补位（与 live 的 promoteDeferred 同口径）。 */
  function promoteDeferredPoll() {
    for (const [sessionId, record] of records) {
      if (!record.deferredPoll || pollSlotsExhausted(sessionId)) {
        continue;
      }
      record.deferredPoll = false;
      record.desired = "poll";
      record.deferredLive = false;
      record.entry.setMode("poll");
      notify(sessionId);
    }
  }

  function enterPollOrDefer(record: EntryRecord) {
    if (pollSlotsExhausted(record.entry.sessionId)) {
      record.deferredPoll = true;
      record.entry.setMode("idle");
      return;
    }
    record.deferredPoll = false;
    record.entry.setMode("poll");
    promoteDeferredPoll();
  }

  function evictOldestBackgroundLive(exceptSessionId: string) {
    let oldest: { sessionId: string; record: EntryRecord } | null = null;
    for (const [sessionId, record] of records) {
      if (
        sessionId === exceptSessionId ||
        record.role !== "background" ||
        record.entry.snapshot().mode !== "live"
      ) {
        continue;
      }
      if (!oldest || record.lastTouchedAt < oldest.record.lastTouchedAt) {
        oldest = { sessionId, record };
      }
    }
    if (!oldest) {
      return;
    }
    oldest.record.desired = "poll";
    oldest.record.deferredLive = false;
    enterPollOrDefer(oldest.record);
    notify(oldest.sessionId);
  }

  return {
    liveCount,
    backgroundLiveSlotsExhausted,
    evictOldestBackgroundLive,
    enterPollOrDefer,
    promoteDeferredPoll,
  };
}
