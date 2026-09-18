// Batch 2（多会话并发运行时）：注册表的类型面（从 `registry.ts` 抽出，见 §4.2）。
//
// 抽出的动机只有一个：`registry.ts` 的实现 + 调度逻辑已经接近项目 P0-2 的
// 「单文件 ≤500 非空行」约束，把**纯类型**外置后实现文件留出余量。这里不放任何
// 运行时逻辑，避免出现第二处调度真相。

import type { SessionRuntimeEntry, SessionRuntimeEntryConfig } from "./entry";
import type {
  SessionRuntimeEntrySnapshot,
  SessionRuntimeEventOutlet,
  SubscriptionMode,
} from "./types";

export type SessionRuntimeEnsureReason =
  | "selected"
  | "active-turn"
  | "recent"
  | "explicit";

export type SessionRuntimeReleaseReason = "deselected" | "idle" | "evicted";

export type SessionRuntimeEnsureOptions = {
  /**
   * 显式「跟随实时」：超预算时按最近活动 LRU 顶掉一条后台 live，而不是降级 poll。
   */
  promote?: boolean;
};

export type SessionRuntimeRegistryOptions = {
  /** entry 注入点（单测用）：缺省 `createSessionRuntimeEntry`。 */
  createEntry?: (config: SessionRuntimeEntryConfig) => SessionRuntimeEntry;
  /** 后台 live 名额（缺省 2；前台恒有 1 个名额）。 */
  backgroundLiveBudget?: number;
  /** 前台 live 名额（缺省 1；置 0 表示列表页等无前台场景）。 */
  foregroundLiveBudget?: number;
  /**
   * poll 订阅上限（缺省 8）：超出后新会话保持 idle（`deferredPoll`），
   * 名额空出时按等待顺序补位。只约束后台轮询总量，不影响 live 预算。
   */
  maxPollSessions?: number;
  /**
   * 后台 live 的回合终态宽限：终态后该时长内没有新事件才降级 poll（§4.2 表格）。
   */
  backgroundLiveGraceMs?: number;
  now?: () => number;
  /**
   * IN1 建连游标来源（§4.3 步 1）：后台会话首次建连时 `after` 取
   * `max(entry.lastSeq, 轨迹窗口 lastEventSeq)`，避免从 0 开始整份 dump。
   */
  getReplayCursor?: (sessionId: string) => number | undefined;
};

export type SessionRuntimeRegistry = {
  ensure(
    sessionId: string,
    reason: SessionRuntimeEnsureReason,
    options?: SessionRuntimeEnsureOptions,
  ): void;
  release(sessionId: string, reason: SessionRuntimeReleaseReason): void;
  retry(sessionId: string): void;
  subscribeEntry(
    sessionId: string,
    callback: (snapshot: SessionRuntimeEntrySnapshot | undefined) => void,
  ): () => void;
  snapshot(sessionId: string): SessionRuntimeEntrySnapshot | undefined;
  entriesSnapshot(): ReadonlyMap<string, SessionRuntimeEntrySnapshot>;
  /**
   * 全局条目订阅（Batch 3 §4.7）：任何条目的新增 / 释放 / 快照变化都会触发，
   * `getEntriesSnapshot` 在同一变更点内保持引用稳定（useSyncExternalStore 口径）。
   */
  subscribeEntries(listener: () => void): () => void;
  getEntriesSnapshot(): readonly SessionRuntimeEntrySnapshot[];
  /** 生命周期：页面可见性（§4.6）。hidden 时后台 live 降 poll，恢复时按策略回升。 */
  noteVisibility(hidden: boolean): void;
  onEvent(handler: SessionRuntimeEventOutlet): () => void;
  dispose(): void;
};

/** 注册表内部记录：entry + 调度所需的角色 / 期望模式 / 触达时间。 */
export type SessionRuntimeEntryRecord = {
  entry: SessionRuntimeEntry;
  role: "foreground" | "background";
  /** 期望模式（预算不足时实际模式可能被降级为 poll）。 */
  desired: SubscriptionMode;
  /** 显式升级请求：等待 live 名额。 */
  deferredLive: boolean;
  /** poll 名额已满：等待轮询名额（防止轮询总量随窗口内会话数线性增长）。 */
  deferredPoll: boolean;
  /** 可见性降采样前的实际模式（恢复时回升）。 */
  suspendedMode: SubscriptionMode | null;
  lastTouchedAt: number;
};
