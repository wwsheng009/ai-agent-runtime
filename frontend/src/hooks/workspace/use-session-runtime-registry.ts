// Batch 2（多会话并发运行时）：注册表的 React 绑定（方案 §4.2）。
//
// - 单例：注册表的生命周期与"页面"一致，而不是与某个组件挂载绑定——否则会话
//   切换/侧栏重渲染会重建连接。模块级单例 + 页面卸载时 `dispose()`，形态与
//   `lib/live-diagnostics/store.ts` 一致。
// - `useSessionRuntimeEntry`：`useSyncExternalStore` + 选择器；entry 内部已做
//   浅比较，快照引用只在字段变化时更新，满足 useSyncExternalStore 的稳定性要求。

import { useCallback, useEffect, useState, useSyncExternalStore } from "react";

import {
  createSessionRuntimeRegistry,
  type SessionRuntimeRegistry,
} from "@/lib/session-runtime/registry";
import { MULTI_SESSION_REGISTRY_ENABLED } from "@/lib/session-runtime/flags";
import type { SessionRuntimeEntrySnapshot } from "@/lib/session-runtime/types";

const EMPTY_ENTRIES: readonly SessionRuntimeEntrySnapshot[] = [];

let singleton: SessionRuntimeRegistry | null = null;
/**
 * 使用者计数 + 延迟释放：注册表的生命周期跟随"页面"，而不是某个组件的挂载。
 *
 * 若在组件的卸载清理里直接 `dispose()`，dev 下 React StrictMode 的
 * 「挂载 → 卸载 → 挂载」会把单例打死：第二次挂载复用同一个已 disposed 实例
 * （`useState` 惰性初始化只执行一次），而 `ensure()` 在 disposed 后静默 no-op，
 * 后台订阅从此不再建立（live 实测确认的缺陷）。因此只有计数归零、且跨过一个
 * 宏任务后仍为零（StrictMode 的重挂载在同一提交内重新 acquire）才真正释放。
 */
let activeMounts = 0;
let releaseTimer: ReturnType<typeof setTimeout> | null = null;
/**
 * 轨迹池 → 注册表的建连游标来源（§4.3 步 1）。由页面在挂载时注册、卸载时清除：
 * 后台会话首次建连的 `after` 因此等于「该会话轨迹窗口已回放到的 seq」，
 * 不会从 0 触发整份事件日志 dump。
 */
let replayCursorProvider: ((sessionId: string) => number | undefined) | null =
  null;

export function setSessionRuntimeReplayCursorProvider(
  provider: ((sessionId: string) => number | undefined) | null,
): void {
  replayCursorProvider = provider;
}

export function getSessionRuntimeRegistry(): SessionRuntimeRegistry {
  singleton ??= createSessionRuntimeRegistry({
    getReplayCursor: (sessionId) => replayCursorProvider?.(sessionId),
  });
  return singleton;
}

/** 登记一个使用者（hook 挂载）：返回当前单例。 */
export function acquireSessionRuntimeRegistry(): SessionRuntimeRegistry {
  activeMounts += 1;
  return getSessionRuntimeRegistry();
}

/** 注销一个使用者（hook 卸载）：最后一个使用者离开并跨过宏任务后才释放。 */
export function releaseSessionRuntimeRegistry(): void {
  if (activeMounts > 0) {
    activeMounts -= 1;
  }
  if (activeMounts > 0 || releaseTimer !== null) {
    return;
  }
  const target = singleton;
  releaseTimer = setTimeout(() => {
    releaseTimer = null;
    // 期间被重新 acquire，或单例已被替换：交给新的生命周期处理。
    if (activeMounts > 0 || singleton !== target) {
      return;
    }
    disposeSessionRuntimeRegistry();
  }, 0);
}

/** 页面卸载时释放：所有 entry 的连接/定时器随之停止（后台订阅不跨页面存活）。 */
export function disposeSessionRuntimeRegistry(): void {
  if (releaseTimer !== null) {
    clearTimeout(releaseTimer);
    releaseTimer = null;
  }
  singleton?.dispose();
  singleton = null;
}

export function useSessionRuntimeRegistry(): SessionRuntimeRegistry {
  // useState 惰性初始化：渲染期不做副作用（react-hooks/refs 规则）。
  const [registry] = useState(getSessionRuntimeRegistry);
  // 引用计数挂载：卸载清理只注销使用者，不直接杀单例（见 activeMounts 注释）。
  useEffect(() => {
    acquireSessionRuntimeRegistry();
    return () => {
      releaseSessionRuntimeRegistry();
    };
  }, []);
  return registry;
}

/** 订阅单个会话的注册表投影（无该会话订阅时返回 undefined）。 */
export function useSessionRuntimeEntry(
  sessionId: string | null | undefined,
): SessionRuntimeEntrySnapshot | undefined {
  const registry = useSessionRuntimeRegistry();
  const subscribe = useCallback(
    (onStoreChange: () => void) =>
      sessionId
        ? registry.subscribeEntry(sessionId, onStoreChange)
        : () => {},
    [registry, sessionId],
  );
  const getSnapshot = useCallback(
    () => (sessionId ? registry.snapshot(sessionId) : undefined),
    [registry, sessionId],
  );
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}

/**
 * 订阅注册表的全部条目（Batch 3 §4.7：侧栏投影「谁在运行 / 谁在等我」）。
 *
 * 关闭开关时不订阅、也不返回任何条目——多会话注册表必须显式打开才会改变
 * 侧栏行为（旧行为：只有选中会话有本地活动信号）。
 */
export function useSessionRuntimeEntries(): readonly SessionRuntimeEntrySnapshot[] {
  const registry = useSessionRuntimeRegistry();
  const subscribe = useCallback(
    (onStoreChange: () => void) =>
      MULTI_SESSION_REGISTRY_ENABLED
        ? registry.subscribeEntries(onStoreChange)
        : () => {},
    [registry],
  );
  const getSnapshot = useCallback(
    () =>
      MULTI_SESSION_REGISTRY_ENABLED
        ? registry.getEntriesSnapshot()
        : EMPTY_ENTRIES,
    [registry],
  );
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
