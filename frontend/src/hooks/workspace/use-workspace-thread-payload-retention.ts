// 会话载荷驻留治理的页面接线：按「最近选中」维护 LRU，并在每次会话切换后
// 对超出保留窗口的冷会话执行一次有界卸载（见 lib/thread-state/retention.ts）。
//
// 设计口径：
// - 只在选中会话变化时触发一次扫描（不跟随流式内容提交逐帧重跑）；
// - 最近访问顺序由本 hook 自持（内存态，不持久化），并把「当前选中」计入
//   保留集合，保证切回最近会话永远是即开即显；
// - protectedKeys（在途回合 / 服务端活动回合）由调用方每帧传入，经 ref 取最新值，
//   避免把快速变化的集合写进 effect 依赖导致重复扫描；
// - 卸载是无损的：重新选中冷会话时 `useSessionHistorySync` 会按 sessionId 重拉
//   权威历史（见 retention.ts 顶部注释）。

import {
  useEffect,
  useMemo,
  useRef,
  type Dispatch,
  type SetStateAction,
} from "react";

import { type Thread } from "@/data/mock";
import type { SessionRuntimeEntrySnapshot } from "@/lib/session-runtime/types";
import { normalizeSessionId } from "@/lib/session-id";
import {
  THREAD_PAYLOAD_LRU_SIZE,
  THREAD_PAYLOAD_RETENTION_ENABLED,
  trimIdleThreadPayloads,
} from "@/lib/thread-state/retention";

const EMPTY_KEYS: ReadonlySet<string> = new Set();

export type WorkspaceThreadPayloadRetentionOptions = {
  /** 当前选中线程 id（未归一，可空）。 */
  selectedThreadId?: string | null;
  /** 本地在途回合键（未归一）；与下面两项一起构成保护集合。 */
  activeSessionKeys?: readonly string[];
  /** 注册表条目：其中 `activeTurn` 非空的会话同样受保护。 */
  sessionRuntimeEntries?: readonly Pick<
    SessionRuntimeEntrySnapshot,
    "sessionId" | "activeTurn"
  >[];
  /** 直接声明的保护键（未归一；测试与特殊场景使用）。 */
  protectedKeys?: readonly string[];
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  /** 回滚开关（测试注入）。 */
  enabled?: boolean;
  /** 保留完整载荷的最近会话数（不含选中；测试注入）。 */
  lruSize?: number;
};

function normalizeKey(value: string | null | undefined): string {
  if (!value) {
    return "";
  }
  return normalizeSessionId(value) || value.trim();
}

export function useWorkspaceThreadPayloadRetention({
  selectedThreadId,
  activeSessionKeys,
  sessionRuntimeEntries,
  protectedKeys,
  setThreads,
  enabled = THREAD_PAYLOAD_RETENTION_ENABLED,
  lruSize = THREAD_PAYLOAD_LRU_SIZE,
}: WorkspaceThreadPayloadRetentionOptions): void {
  // 最近访问（新→旧）的归一化键；上限 = 保留窗口 + 余量，本身有界。
  const recentKeysRef = useRef<string[]>([]);
  // 保护集合 = 显式键 ∪ 本地在途回合 ∪ 注册表仍有活动回合的会话。归一化后的
  // 集合按引用稳定（依赖不变不重算），供 ref 写入与扫描 effect 使用。
  const mergedProtectedKeys = useMemo(() => {
    const keys = new Set<string>();
    for (const raw of protectedKeys ?? []) {
      const key = normalizeKey(raw);
      if (key) {
        keys.add(key);
      }
    }
    for (const raw of activeSessionKeys ?? []) {
      const key = normalizeKey(raw);
      if (key) {
        keys.add(key);
      }
    }
    for (const entry of sessionRuntimeEntries ?? []) {
      if (!entry.activeTurn) {
        continue;
      }
      const key = normalizeKey(entry.sessionId);
      if (key) {
        keys.add(key);
      }
    }
    return [...keys];
  }, [activeSessionKeys, protectedKeys, sessionRuntimeEntries]);
  // 每帧最新值经 ref 传递：集合来源（activeKeys / 注册表条目）会随流式帧变化，
  // 进 effect 依赖会把「一次切换一次扫描」放大成逐帧扫描。ref 写入必须放 effect
  // （react-hooks/refs）；本 effect 声明在扫描 effect 之前，保证同帧更新的保护
  // 集合先生效。
  const protectedKeysRef = useRef<ReadonlySet<string>>(EMPTY_KEYS);
  useEffect(() => {
    protectedKeysRef.current = mergedProtectedKeys.length
      ? new Set(mergedProtectedKeys)
      : EMPTY_KEYS;
  }, [mergedProtectedKeys]);

  useEffect(() => {
    if (!enabled) {
      return;
    }
    const selectedKey = normalizeKey(selectedThreadId);
    const recents = recentKeysRef.current;
    const head = recents[0];
    if (selectedKey && selectedKey !== head) {
      recentKeysRef.current = [
        selectedKey,
        ...recents.filter((key) => key !== selectedKey),
      ].slice(0, Math.max(0, lruSize) + 2);
    }

    setThreads((current) =>
      trimIdleThreadPayloads({
        threads: current,
        selectedThreadId,
        recentKeys: recentKeysRef.current,
        protectedKeys: protectedKeysRef.current,
        lruSize,
      }),
    );
  }, [enabled, lruSize, selectedThreadId, setThreads]);
}
