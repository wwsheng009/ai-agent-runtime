/**
 * 线程 store 的 **live / 结构双通道**（P1-1 第一刀）。
 *
 * 背景（方案 §6 / §9.1）：流式期间的高频「内容提交」（正文 / 推理副本）此前直接写
 * 页面级 thread state，`WorkspacePage → WorkspaceShell → 侧栏 / 面板 / 消息列` 整棵
 * 树重渲染一次。文本平滑已由 `lib/live-stream-text.ts` 按消息订阅承担，因此内容提交
 * 本身**不需要**惊动页面级订阅者。
 *
 * 两条通道：
 * - **结构通道**（`getStructuralSnapshot` / `subscribeStructural`）：页面级骨架
 *   （会话列表、topbar、面板、以及 `selectedThread` 的派生）。只在结构提交时推进 /
 *   通知——内容提交不触发整树重渲染。
 * - **live 通道**（`getLiveThread` / `subscribeThread`）：按会话寻址的最新视图，
 *   内容提交与结构提交都会通知该会话的订阅者（消息列据此拿到段落骨架）。
 *
 * 写入方仍是同一个 `setThreads` 语义的 updater；只有流式内容副本需要显式标记
 * `{ live: true }`（`setThreadsLive`）。
 *
 * 不变式：
 * 1. `update` 的 updater 一律作用在**最新数组**（含 live 提交）上——结构提交不会
 *    丢内容；updater 返回同一引用时视为无变化（不通知、不换快照）。
 * 2. live 提交**不推进**结构快照：`getStructuralSnapshot()` 在两次结构提交之间
 *    必须保持同一引用，否则 `useSyncExternalStore` 会误判「变了」。
 */
import {
  createContext,
  useCallback,
  useContext,
  useSyncExternalStore,
} from "react";

import { type Thread } from "@/data/mock";

export type ThreadStoreUpdater = (current: Thread[]) => Thread[];

export type ThreadStoreWriteOptions = {
  /**
   * 内容提交：只更新 live 视图并通知**按会话订阅者**；
   * 不推进结构快照、不通知页面级结构订阅者。
   */
  live?: boolean;
};

export type WorkspaceThreadStore = {
  /** 结构快照：最近一次结构提交后的数组（live 提交不推进）。 */
  getStructuralSnapshot: () => Thread[];
  /** live 快照：包含最近的内容提交（按会话读取的权威来源）。 */
  getLiveSnapshot: () => Thread[];
  /** 按会话读取 live 视图（消息列 / 侧栏行）。 */
  getLiveThread: (threadId: string) => Thread | undefined;
  /** 页面级订阅：只在结构提交时通知。 */
  subscribeStructural: (listener: () => void) => () => void;
  /** 按会话订阅：该会话对象换引用（含内容提交 / 移除）时通知。 */
  subscribeThread: (threadId: string, listener: () => void) => () => void;
  /** 写入入口（与 `setThreads` 同语义）。 */
  update: (updater: ThreadStoreUpdater, options?: ThreadStoreWriteOptions) => void;
};

/** 变化会话 id 集合：按对象引用 diff（新增 / 移除 / 换引用）。 */
function collectChangedThreadIds(
  previous: Thread[],
  next: Thread[],
): Set<string> {
  const changed = new Set<string>();
  const previousById = new Map(previous.map((thread) => [thread.id, thread]));
  const nextById = new Map(next.map((thread) => [thread.id, thread]));

  for (const [id, thread] of nextById) {
    if (previousById.get(id) !== thread) {
      changed.add(id);
    }
  }
  for (const id of previousById.keys()) {
    if (!nextById.has(id)) {
      changed.add(id);
    }
  }

  return changed;
}

export function createWorkspaceThreadStore(
  initial: Thread[] = [],
): WorkspaceThreadStore {
  let latest: Thread[] = initial;
  let structural: Thread[] = initial;
  const structuralListeners = new Set<() => void>();
  const threadListeners = new Map<string, Set<() => void>>();

  const emitThreadListeners = (threadIds: Iterable<string>) => {
    for (const threadId of threadIds) {
      const listeners = threadListeners.get(threadId);
      if (!listeners) {
        continue;
      }
      // 复制再遍历：监听器可能在回调里退订（unmount / 会话切换）。
      for (const listener of Array.from(listeners)) {
        listener();
      }
    }
  };

  const emitStructuralListeners = () => {
    for (const listener of Array.from(structuralListeners)) {
      listener();
    }
  };

  return {
    getStructuralSnapshot: () => structural,
    getLiveSnapshot: () => latest,
    getLiveThread: (threadId) =>
      latest.find((thread) => thread.id === threadId),

    subscribeStructural: (listener) => {
      structuralListeners.add(listener);
      return () => {
        structuralListeners.delete(listener);
      };
    },

    subscribeThread: (threadId, listener) => {
      let listeners = threadListeners.get(threadId);
      if (!listeners) {
        listeners = new Set();
        threadListeners.set(threadId, listeners);
      }
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
        if (listeners.size === 0) {
          threadListeners.delete(threadId);
        }
      };
    },

    update: (updater, options) => {
      const next = updater(latest);
      if (next === latest) {
        return;
      }

      const changedThreadIds = collectChangedThreadIds(latest, next);
      latest = next;
      if (!options?.live) {
        structural = next;
      }

      emitThreadListeners(changedThreadIds);
      if (!options?.live) {
        emitStructuralListeners();
      }
    },
  };
}

/** 页面实例级 store 的注入点（`WorkspacePage` 持有，消息列按会话订阅）。 */
export const WorkspaceThreadStoreContext =
  createContext<WorkspaceThreadStore | null>(null);

export function useWorkspaceThreadStore(): WorkspaceThreadStore | null {
  return useContext(WorkspaceThreadStoreContext);
}

// 无 store / 无会话时的稳定快照：保证 `useSyncExternalStore` 不会因每次新引用死循环。
const NO_THREAD: Thread | undefined = undefined;
const getNoThreadSnapshot = () => NO_THREAD;
const subscribeNothing = () => () => {};

/**
 * 订阅某会话的 live 视图。返回 undefined 表示该会话不在 store 里（调用方应回落到
 * 结构快照，例如页面级 `selectedThread`）；返回值只在真正变化时换引用。
 */
export function useLiveThread(
  threadId: string | null | undefined,
): Thread | undefined {
  const store = useWorkspaceThreadStore();
  const key = threadId ?? "";
  const subscribe = useCallback(
    (listener: () => void) =>
      store && key ? store.subscribeThread(key, listener) : subscribeNothing(),
    [key, store],
  );
  const getSnapshot = useCallback(
    () => (store && key ? store.getLiveThread(key) : NO_THREAD),
    [key, store],
  );

  return useSyncExternalStore(subscribe, getSnapshot, getNoThreadSnapshot);
}
