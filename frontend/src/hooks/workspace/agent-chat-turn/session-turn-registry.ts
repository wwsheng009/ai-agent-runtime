/**
 * 按会话分键的回合注册表（工作区多会话并发 Batch 1）。
 *
 * 改造前：`isResponding` / `activeTurnId` / `phase` / `activeTurnStateRef` /
 * `activeRequestControllerRef` / `streamStalled` 都是 hook 内单值——A 在跑时
 * B 的 composer 被判为「生成中」（提交闸门全局单飞），停止按钮也会打到 A。
 *
 * 本模块把这份状态按 `threadKey`（sessionId 优先、线程 id 兜底）分键：
 * - 单会话语义逐字节等价：条目创建即 isResponding；`activeTurnId` 在 finalize
 *   起手清空、条目保留到通道收尾（与旧实现的可见窗口一致）；phase 同步跟随；
 * - 多会话：`isBusy(key)` 只约束同一会话（跨会话并行提交），停止 / 静默看门狗
 *   都只作用于选中键；Map 尺寸为 1 时与旧实现等价（回滚口径）。
 *
 * 订阅面走 `useSyncExternalStore`：快照按 key 缓存，字段不变时引用稳定
 * （否则 React 会因 getSnapshot 每次返回新对象而反复重渲染）。
 */

import type { ChatStreamPhase } from "@/types/runtime";

import type { ChatTurnRuntimeState } from "./turn-state";

/** 在途回合条目；字段更新一律换新对象，保证快照可按引用比较。 */
export type SessionTurnEntry = {
  /** threadKey = sessionId 优先、线程 id 兜底（与草稿 / 轨迹池同口径）。 */
  key: string;
  threadId: string;
  turnId: string;
  controller: AbortController;
  turnState: ChatTurnRuntimeState;
  /** 在途回合身份：finalize 起手清空，条目仍保留到通道收尾（旧行为窗口）。 */
  activeTurnId: string | null;
  phase: ChatStreamPhase | null;
};

/** 选中会话的只读视图（`useSyncExternalStore` 的快照单元）。 */
export type SessionTurnSnapshot = {
  entry: SessionTurnEntry | null;
  stalled: boolean;
};

export type BeginSessionTurnInit = {
  key: string;
  threadId: string;
  turnId: string;
  controller: AbortController;
  turnState: ChatTurnRuntimeState;
};

export type SessionTurnRegistry = {
  subscribe: (listener: () => void) => () => void;
  getSnapshot: (key: string) => SessionTurnSnapshot;
  beginTurn: (init: BeginSessionTurnInit) => SessionTurnEntry;
  /** 该键是否有在途回合（提交闸门：同会话单飞、跨会话并行）。 */
  isBusy: (key: string) => boolean;
  setPhase: (turnId: string, phase: ChatStreamPhase | null) => void;
  /** finalize 起手清空在途身份（条目仍在，isResponding 保持 true）。 */
  clearActiveTurnId: (turnId: string) => void;
  isTurnRunning: (turnId: string) => boolean;
  phaseOf: (turnId: string) => ChatStreamPhase | null;
  activeTurnIdOf: (turnId: string) => string | null;
  /** 通道收尾：删除条目（isResponding→false、phase→null，与旧 finally 同步）。 */
  finishTurn: (turnId: string) => void;
  /** 静默看门狗标记：按会话保留，跨回合存活至显式 clear（旧 stall 语义）。 */
  setStalled: (key: string, stalled: boolean) => void;
  /** 同一线程内的键升级（线程 id → 服务端 sessionId），identity 保持。 */
  adopt: (fromKey: string, toKey: string) => void;
  /**
   * 按线程身份解析键：同一 threadId 的键升级（草稿线程 id → 服务端 sessionId）
   * 先迁移条目、再返回新键——迁移必须发生在**读取快照之前**，否则新键先读到
   * 空条目：isResponding 一帧为 false、提交闸门误开。threadId 为空时原样返回。
   */
  resolveThreadKey: (threadId: string, key: string) => string;
  abortAll: () => void;
  /** 诊断 / 测试：在途回合键。 */
  activeKeys: () => string[];
};

export function createSessionTurnRegistry(): SessionTurnRegistry {
  const entries = new Map<string, SessionTurnEntry>();
  const byTurnId = new Map<string, SessionTurnEntry>();
  const stalledKeys = new Set<string>();
  const cache = new Map<string, SessionTurnSnapshot>();
  const listeners = new Set<() => void>();
  // 线程身份 → 最近一次解析出的键：只用于识别「同一线程的键升级」（adopt 用）。
  const threadKeys = new Map<string, string>();

  const emit = () => {
    for (const listener of listeners) {
      listener();
    }
  };
  const invalidate = (key: string) => {
    cache.delete(key);
  };
  const store = (entry: SessionTurnEntry) => {
    entries.set(entry.key, entry);
    byTurnId.set(entry.turnId, entry);
    invalidate(entry.key);
    emit();
  };

  const snapshotOf = (key: string): SessionTurnSnapshot => {
    const cached = cache.get(key);
    if (cached) {
      return cached;
    }
    const next: SessionTurnSnapshot = {
      entry: entries.get(key) ?? null,
      stalled: stalledKeys.has(key),
    };
    cache.set(key, next);
    return next;
  };

  const adoptKeys = (fromKey: string, toKey: string) => {
    if (fromKey === toKey) {
      return;
    }
    const entry = entries.get(fromKey);
    const stalled = stalledKeys.has(fromKey);
    if (!entry && !stalled) {
      return;
    }
    if (entry) {
      entries.delete(fromKey);
      const moved = { ...entry, key: toKey };
      entries.set(toKey, moved);
      byTurnId.set(moved.turnId, moved);
    }
    if (stalled) {
      stalledKeys.delete(fromKey);
      stalledKeys.add(toKey);
    }
    invalidate(fromKey);
    invalidate(toKey);
    emit();
  };

  return {
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot: snapshotOf,
    beginTurn(init) {
      const entry: SessionTurnEntry = {
        key: init.key,
        threadId: init.threadId,
        turnId: init.turnId,
        controller: init.controller,
        turnState: init.turnState,
        activeTurnId: init.turnId,
        phase: null,
      };
      store(entry);
      return entry;
    },
    isBusy(key) {
      return entries.has(key);
    },
    setPhase(turnId, phase) {
      const entry = byTurnId.get(turnId);
      if (!entry || entry.phase === phase) {
        return;
      }
      store({ ...entry, phase });
    },
    clearActiveTurnId(turnId) {
      const entry = byTurnId.get(turnId);
      if (!entry || entry.activeTurnId === null) {
        return;
      }
      store({ ...entry, activeTurnId: null });
    },
    isTurnRunning(turnId) {
      return byTurnId.has(turnId);
    },
    phaseOf(turnId) {
      return byTurnId.get(turnId)?.phase ?? null;
    },
    activeTurnIdOf(turnId) {
      return byTurnId.get(turnId)?.activeTurnId ?? null;
    },
    finishTurn(turnId) {
      const entry = byTurnId.get(turnId);
      if (!entry) {
        return;
      }
      byTurnId.delete(turnId);
      if (entries.get(entry.key)?.turnId === turnId) {
        entries.delete(entry.key);
      }
      invalidate(entry.key);
      emit();
    },
    setStalled(key, stalled) {
      if (stalledKeys.has(key) === stalled) {
        return;
      }
      if (stalled) {
        stalledKeys.add(key);
      } else {
        stalledKeys.delete(key);
      }
      invalidate(key);
      emit();
    },
    adopt: adoptKeys,
    resolveThreadKey(threadId, key) {
      const normalizedThreadId = (threadId ?? "").trim();
      if (!normalizedThreadId) {
        return key;
      }
      const previousKey = threadKeys.get(normalizedThreadId);
      if (previousKey && previousKey !== key) {
        // 目标键已被占用（真切换/重放）时 adopt 内部会拒绝，条目留在旧键。
        adoptKeys(previousKey, key);
      }
      threadKeys.set(normalizedThreadId, key);
      return key;
    },
    abortAll() {
      for (const entry of entries.values()) {
        entry.controller.abort();
      }
    },
    activeKeys() {
      return [...entries.keys()];
    },
  };
}

/**
 * `phaseRef` 兼容层：既有写入器（streaming-writers）按 ref 形状读写相位，
 * 这里把它桥接到注册表的按回合条目上。
 */
export function createTurnPhaseRef(
  registry: SessionTurnRegistry,
  turnId: string,
): { current: ChatStreamPhase | null } {
  return {
    get current(): ChatStreamPhase | null {
      return registry.phaseOf(turnId);
    },
    set current(next: ChatStreamPhase | null) {
      registry.setPhase(turnId, next);
    },
  };
}

/**
 * `activeTurnIdRef` 兼容层：读 = 该回合当前在途身份；写 null = 清空在途身份
 * （finalize 起手与通道收尾都会走到，幂等）。
 */
export function createTurnActiveIdRef(
  registry: SessionTurnRegistry,
  turnId: string,
): { current: string | null } {
  return {
    get current(): string | null {
      return registry.activeTurnIdOf(turnId);
    },
    set current(next: string | null) {
      if (next === null) {
        registry.clearActiveTurnId(turnId);
      }
    },
  };
}

/**
 * 回合相位/在途身份的写入口（Batch 1 机械拆分）：把 `phaseRef` /
 * `activeTurnIdRef` 两个兼容 ref 与对应 setter 一次性绑定到某个回合，供
 * stream-handlers / streaming-writers / finalize-turn 使用，等价于拆出前
 * submitPrompt 里的四个局部闭包。
 */
export type SessionTurnBindings = {
  phaseRef: { current: ChatStreamPhase | null };
  activeTurnIdRef: { current: string | null };
  setActiveTurnId: (turnId: string | null) => void;
  setPhase: (phase: ChatStreamPhase | null) => void;
};

export function bindSessionTurn(
  registry: SessionTurnRegistry,
  turnId: string,
): SessionTurnBindings {
  return {
    activeTurnIdRef: createTurnActiveIdRef(registry, turnId),
    phaseRef: createTurnPhaseRef(registry, turnId),
    setActiveTurnId: (nextTurnId) => {
      if (nextTurnId === null) {
        registry.clearActiveTurnId(turnId);
      }
    },
    setPhase: (nextPhase) => {
      registry.setPhase(turnId, nextPhase);
    },
  };
}
