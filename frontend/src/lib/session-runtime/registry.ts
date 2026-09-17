// Batch 2（多会话并发运行时）：会话运行时注册表（headless，无 React）。
//
// 职责（方案 §4.2 / §4.6）：
// - entries 管理：`ensure` / `release` / `retry`，同一会话至多一条订阅；
// - 预算与调度：`live` 连接总数 = 前台 1 + 后台 2（可配置）；超预算的后台会话
//   降级 `poll` 并记入 `deferred`，腾出名额时自动补位；显式升级（promote）按最近
//   活动 LRU 顶掉一条后台 live；
// - 订阅者通知：按会话分发 entry 快照（错误隔离 + 浅比较在 entry 内完成）；
// - 事件出口：`onEvent` 把「谁的事件」交给页面层（前台写 threads，后台只投影）。
//
// 不变量：注册表自身不碰 `threads`、不渲染消息（D1-B）。

import { createSessionRuntimeEntry } from "./entry";
import {
  DEFAULT_BACKGROUND_LIVE_BUDGET,
  DEFAULT_FOREGROUND_LIVE_BUDGET,
} from "./flags";
import type {
  SessionRuntimeEntrySnapshot,
  SessionRuntimeEventOutlet,
} from "./types";
import type {
  SessionRuntimeEnsureOptions,
  SessionRuntimeEnsureReason,
  SessionRuntimeEntryRecord as EntryRecord,
  SessionRuntimeRegistry,
  SessionRuntimeRegistryOptions,
} from "./registry-types";

// 类型面外置到 `registry-types.ts`（P0-2 文件行数约束），这里保持原导出路径不变，
// 避免调用方（hooks / policy / 测试）被迫改 import。
export type {
  SessionRuntimeEnsureOptions,
  SessionRuntimeEnsureReason,
  SessionRuntimeEntryRecord,
  SessionRuntimeRegistry,
  SessionRuntimeRegistryOptions,
  SessionRuntimeReleaseReason,
} from "./registry-types";

const LIVE_GRACE_MS = 10_000;

export function createSessionRuntimeRegistry(
  options: SessionRuntimeRegistryOptions = {},
): SessionRuntimeRegistry {
  const {
    createEntry = createSessionRuntimeEntry,
    backgroundLiveBudget = DEFAULT_BACKGROUND_LIVE_BUDGET,
    foregroundLiveBudget = DEFAULT_FOREGROUND_LIVE_BUDGET,
    backgroundLiveGraceMs = LIVE_GRACE_MS,
    now = () => Date.now(),
    getReplayCursor,
  } = options;

  const records = new Map<string, EntryRecord>();
  const subscribers = new Map<
    string,
    Set<(snapshot: SessionRuntimeEntrySnapshot | undefined) => void>
  >();
  const eventHandlers = new Set<SessionRuntimeEventOutlet>();
  const demoteTimers = new Map<string, ReturnType<typeof setTimeout>>();
  const entryListeners = new Set<() => void>();
  let entriesCache: SessionRuntimeEntrySnapshot[] | null = null;
  let pageHidden = false;
  let disposed = false;

  /**
   * 让全局投影缓存失效并通知订阅者。所有会改变条目集合或快照的路径都要经过
   * 这里（新增 / 释放 / 模式切换 / 可见性变化），否则侧栏投影会停在旧快照上。
   */
  function invalidateEntries() {
    entriesCache = null;
    for (const listener of [...entryListeners]) {
      try {
        listener();
      } catch {
        // 订阅者错误隔离：与 notify 同口径。
      }
    }
  }

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

  function notify(sessionId: string) {
    // 全局投影先失效：即使没有按会话订阅者，侧栏也要收到这次变化。
    invalidateEntries();
    const scoped = subscribers.get(sessionId);
    if (!scoped || scoped.size === 0) {
      return;
    }
    const snapshot = records.get(sessionId)?.entry.snapshot();
    if (!snapshot) {
      return;
    }
    for (const callback of [...scoped]) {
      try {
        callback(snapshot);
      } catch {
        // 订阅者错误隔离：单个订阅者抛错不影响其它订阅者与注册表调度。
      }
    }
  }

  function clearDemoteTimer(sessionId: string) {
    const timer = demoteTimers.get(sessionId);
    if (timer !== undefined) {
      clearTimeout(timer);
      demoteTimers.delete(sessionId);
    }
  }

  /**
   * 后台 live 的终态宽限：没有在途回合时起表，宽限内出现新回合/新事件则重新起表。
   */
  function scheduleGraceDemotion(sessionId: string) {
    clearDemoteTimer(sessionId);
    const record = records.get(sessionId);
    if (!record || record.role === "foreground" || pageHidden) {
      return;
    }
    const snapshot = record.entry.snapshot();
    if (snapshot.mode !== "live" || snapshot.activeTurn) {
      return;
    }
    demoteTimers.set(
      sessionId,
      setTimeout(() => {
        demoteTimers.delete(sessionId);
        const current = records.get(sessionId);
        if (!current || current.role === "foreground") {
          return;
        }
        const latest = current.entry.snapshot();
        if (latest.mode === "live" && !latest.activeTurn) {
          current.desired = "poll";
          current.entry.setMode("poll");
          notify(sessionId);
          promoteDeferred();
        }
      }, backgroundLiveGraceMs),
    );
  }

  /** 腾出名额后，把等待中的显式升级请求补位（先到先得）。 */
  function promoteDeferred() {
    if (pageHidden) {
      return;
    }
    for (const [sessionId, record] of records) {
      if (record.deferredLive && !backgroundLiveSlotsExhausted()) {
        record.deferredLive = false;
        record.desired = "live";
        record.entry.setMode("live");
        notify(sessionId);
      }
    }
  }

  /** 按预算决定实际模式：前台用一个名额，后台共享另一个名额池。 */
  function applyDesiredMode(record: EntryRecord) {
    if (disposed) {
      return;
    }
    if (record.desired === "idle") {
      record.entry.setMode("idle");
      return;
    }
    if (record.desired === "poll") {
      record.entry.setMode("poll");
      return;
    }
    // desired === "live"
    // 已在 live 的会话不参与预算判定：否则"再次 ensure"会把自己计入占用、
    // 反手把自己降级成 poll（预算校验必须排除自身）。
    const alreadyLive = record.entry.snapshot().mode === "live";
    if (record.role === "foreground") {
      if (!alreadyLive && liveCount("foreground") >= foregroundLiveBudget) {
        record.desired = "poll";
        record.entry.setMode("poll");
        return;
      }
      record.entry.setMode("live");
      return;
    }
    if (pageHidden) {
      // §4.6：页面隐藏时后台不保留 live，恢复可见时按 desired 回升。
      if (alreadyLive) {
        record.suspendedMode = "live";
      }
      record.entry.setMode("poll");
      return;
    }
    if (alreadyLive) {
      return;
    }
    if (backgroundLiveSlotsExhausted()) {
      record.deferredLive = true;
      record.entry.setMode("poll");
      return;
    }
    record.deferredLive = false;
    record.entry.setMode("live");
    record.lastTouchedAt = now();
  }

  function handleEntrySnapshot(sessionId: string, snapshot: SessionRuntimeEntrySnapshot) {
    if (disposed) {
      return;
    }
    const record = records.get(sessionId);
    if (!record) {
      return;
    }
    scheduleGraceDemotion(sessionId);
    if (snapshot.activeTurn) {
      // 活跃回合会话：即便当前是 poll（超预算/隐藏降级），也保持升级意愿。
      if (record.desired !== "live" && !pageHidden && !backgroundLiveSlotsExhausted()) {
        record.desired = "live";
        record.entry.setMode("live");
      }
    }
    notify(sessionId);
  }

  function ensureEntry(sessionId: string): EntryRecord {
    const existing = records.get(sessionId);
    if (existing) {
      return existing;
    }
    const entry = createEntry({
      sessionId,
      ...(getReplayCursor
        ? { getReplayCursor: () => getReplayCursor(sessionId) ?? 0 }
        : {}),
      onEvent: (event) => {
        for (const handler of [...eventHandlers]) {
          try {
            handler(sessionId, event);
          } catch {
            // 事件出口同样做错误隔离：一个消费方抛错不影响其它订阅方。
          }
        }
      },
    });
    const record: EntryRecord = {
      entry,
      role: "background",
      desired: "idle",
      deferredLive: false,
      suspendedMode: null,
      lastTouchedAt: now(),
    };
    records.set(sessionId, record);
    entry.subscribe((snapshot) => handleEntrySnapshot(sessionId, snapshot));
    return record;
  }

  function demotePreviousForeground(selectedId: string) {
    for (const [sessionId, record] of records) {
      if (record.role === "foreground" && sessionId !== selectedId) {
        record.role = "background";
        // 前一前台会话：有在途回合则尝试保留 live（受后台预算约束），否则 poll。
        const snapshot = record.entry.snapshot();
        record.desired = snapshot.activeTurn ? "live" : "poll";
        record.deferredLive = Boolean(snapshot.activeTurn);
        applyDesiredMode(record);
      }
    }
  }

  function ensure(
    sessionId: string,
    reason: SessionRuntimeEnsureReason,
    ensureOptions: SessionRuntimeEnsureOptions = {},
  ) {
    if (disposed || !sessionId) {
      return;
    }
    try {
      ensureRecord(sessionId, reason, ensureOptions);
    } finally {
      // 新增条目 / 仅刷新活动时间等路径不会都走到 notify，这里统一收口。
      invalidateEntries();
    }
  }

  function ensureRecord(
    sessionId: string,
    reason: SessionRuntimeEnsureReason,
    ensureOptions: SessionRuntimeEnsureOptions,
  ) {
    const record = ensureEntry(sessionId);
    record.lastTouchedAt = now();
    if (reason === "selected") {
      demotePreviousForeground(sessionId);
      record.role = "foreground";
      record.desired = "live";
      record.deferredLive = false;
      applyDesiredMode(record);
      return;
    }
    if (record.role === "foreground") {
      // 前台会话收到后台理由（active-turn/recent）：保持前台身份，只刷新活动时间。
      return;
    }
    if (reason === "explicit") {
      if (backgroundLiveSlotsExhausted()) {
        evictOldestBackgroundLive(sessionId);
      }
      record.desired = "live";
      record.deferredLive = backgroundLiveSlotsExhausted();
      applyDesiredMode(record);
      return;
    }
    if (reason === "active-turn" || ensureOptions.promote) {
      record.desired = "live";
      record.deferredLive = backgroundLiveSlotsExhausted() || pageHidden;
      applyDesiredMode(record);
      return;
    }
    // reason === "recent"：低频轮询；已 live 的会话不降级（由宽限/预算收口）。
    if (record.entry.snapshot().mode !== "live") {
      record.desired = "poll";
      record.deferredLive = false;
      applyDesiredMode(record);
    }
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
    oldest.record.entry.setMode("poll");
    notify(oldest.sessionId);
  }

  function release(sessionId: string) {
    if (disposed) {
      return;
    }
    const record = records.get(sessionId);
    if (!record) {
      return;
    }
    clearDemoteTimer(sessionId);
    record.entry.dispose();
    records.delete(sessionId);
    notifySubscribersGone(sessionId);
    invalidateEntries();
    promoteDeferred();
  }

  function notifySubscribersGone(sessionId: string) {
    const scoped = subscribers.get(sessionId);
    if (!scoped) {
      return;
    }
    // 释放后快照消失：通知订阅者失效（React 绑定的 getSnapshot 同样返回
    // undefined），避免订阅者停留在过期快照上。
    for (const callback of [...scoped]) {
      try {
        callback(undefined);
      } catch {
        // 同上：错误隔离。
      }
    }
  }

  function noteVisibility(hidden: boolean) {
    if (disposed || pageHidden === hidden) {
      return;
    }
    pageHidden = hidden;
    for (const [sessionId, record] of records) {
      if (record.role !== "background") {
        continue;
      }
      const snapshot = record.entry.snapshot();
      if (hidden) {
        if (snapshot.mode === "live") {
          record.suspendedMode = "live";
          record.entry.setMode("poll");
          notify(sessionId);
        }
        continue;
      }
      // 恢复可见：按期望模式回升（受后台预算约束）。
      if (record.suspendedMode === "live") {
        record.suspendedMode = null;
        record.desired = "live";
        record.deferredLive = backgroundLiveSlotsExhausted();
        applyDesiredMode(record);
        notify(sessionId);
      }
    }
    invalidateEntries();
  }

  return {
    ensure,
    release,
    retry(sessionId: string) {
      records.get(sessionId)?.entry.retry();
    },
    subscribeEntry(sessionId, callback) {
      let scoped = subscribers.get(sessionId);
      if (!scoped) {
        scoped = new Set();
        subscribers.set(sessionId, scoped);
      }
      scoped.add(callback);
      return () => {
        const current = subscribers.get(sessionId);
        if (!current) {
          return;
        }
        current.delete(callback);
        if (current.size === 0) {
          subscribers.delete(sessionId);
        }
      };
    },
    snapshot(sessionId) {
      return records.get(sessionId)?.entry.snapshot();
    },
    entriesSnapshot() {
      const result = new Map<string, SessionRuntimeEntrySnapshot>();
      for (const [sessionId, record] of records) {
        result.set(sessionId, record.entry.snapshot());
      }
      return result;
    },
    subscribeEntries(listener) {
      entryListeners.add(listener);
      return () => {
        entryListeners.delete(listener);
      };
    },
    getEntriesSnapshot() {
      if (!entriesCache) {
        entriesCache = [...records.values()].map((record) =>
          record.entry.snapshot(),
        );
      }
      return entriesCache;
    },
    noteVisibility,
    onEvent(handler) {
      eventHandlers.add(handler);
      return () => {
        eventHandlers.delete(handler);
      };
    },
    dispose() {
      if (disposed) {
        return;
      }
      disposed = true;
      for (const timer of demoteTimers.values()) {
        clearTimeout(timer);
      }
      demoteTimers.clear();
      for (const record of records.values()) {
        record.entry.dispose();
      }
      records.clear();
      subscribers.clear();
      eventHandlers.clear();
      invalidateEntries();
      entryListeners.clear();
    },
  };
}
