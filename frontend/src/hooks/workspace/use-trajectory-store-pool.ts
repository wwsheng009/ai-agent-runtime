/**
 * 轨迹 store 池接线（工作区多会话并发 Batch 1）。
 *
 * 页面级单例池 + 「选中会话」的 store 解析：
 * - 池跨会话存活，切换会话不再 `reset({ hard: true })`（旧实现切走即清空，
 *   切回必须整段重放）；选中会话的 store 由 key 解析得出，游标/快照随会话保留；
 * - key = `sessionId` 优先、线程 id 兜底（与草稿 / 附件分键口径一致）；
 * - 草稿线程在首个回合落库后 key 会从「线程 id」升级为「sessionId」：
 *   同一线程的这次升级走 `pool.adopt` 迁移 store，避免同一次生成中途换到
 *   空快照（见 `lib/trajectory/store-pool.ts` 的 adopt 注释）。
 */
import { useEffect, useMemo, useState } from "react";

import {
  createTrajectoryStore,
  type TrajectoryStore,
} from "@/hooks/workspace/use-trajectory-snapshot";
import { normalizeSessionId } from "@/lib/session-id";
import {
  ANONYMOUS_TRAJECTORY_STORE_KEY,
  createTrajectoryStorePool,
  type TrajectoryStorePool,
} from "@/lib/trajectory/store-pool";

/** 页面级轨迹 store 池（卸载时统一 dispose）。 */
export function useTrajectoryStorePool(options?: {
  maxStores?: number;
}): TrajectoryStorePool {
  const maxStores = options?.maxStores;
  const pool = useMemo(
    () => createTrajectoryStorePool({ maxStores }),
    [maxStores],
  );
  useEffect(() => {
    return () => {
      pool.disposeAll();
    };
  }, [pool]);
  return pool;
}

/** 会话键：sessionId 优先，草稿线程用线程 id 兜底（空身份落匿名桶）。 */
export function resolveTrajectoryStoreKey(
  sessionId: string | null | undefined,
  threadId: string | null | undefined,
): string {
  const normalizedSessionId = normalizeSessionId(sessionId ?? "");
  if (normalizedSessionId) {
    return normalizedSessionId;
  }
  const normalizedThreadId = (threadId ?? "").trim();
  return normalizedThreadId || ANONYMOUS_TRAJECTORY_STORE_KEY;
}

/**
 * 取「选中会话」的 store：key 不变时同一实例（切回不重放），key 变更时
 * 按同一线程的身份升级做迁移，否则取目标会话自己的 store。
 */
export function useSelectedTrajectoryStore(
  pool: TrajectoryStorePool,
  selection: { sessionId?: string; threadId?: string },
): TrajectoryStore {
  const key = resolveTrajectoryStoreKey(selection.sessionId, selection.threadId);
  const threadId = (selection.threadId ?? "").trim();
  // 同一线程（id 未变）的键升级：草稿线程 id → 服务端 sessionId。身份记忆放在
  // 池里（`acquireForThread`：先迁移、后取），渲染期不读写 ref / state 的
  // 「上一轮值」，也就绕开了 react-hooks/refs 与 set-state-in-* 的限制。
  return pool.acquireForThread(threadId, key);
}

/**
 * 页面接线口径：池 + 选中会话 store 一次取齐（`thread` 只需要 id / sessionId）。
 */
export function useWorkspaceTrajectoryStore(
  thread: { id?: string; sessionId?: string } | undefined,
): { pool: TrajectoryStorePool; store: TrajectoryStore } {
  const pool = useTrajectoryStorePool();
  const store = useSelectedTrajectoryStore(pool, {
    sessionId: thread?.sessionId,
    threadId: thread?.id,
  });
  return { pool, store };
}

/**
 * 未传池化 store 时的自持 store（缺省旧行为：单实例 + 卸载 dispose）。
 * 传入 `provided` 时直接透传，生命周期归调用方（页面级池）所有。
 */
export function useOwnedTrajectoryStore(
  provided?: TrajectoryStore,
): TrajectoryStore {
  // 惰性一次（`useState` 初始化器）。传入 `provided` 时自持实例只是占位，
  // 卸载时一并 dispose——生命周期归页面级池的 store 不受影响。
  const [owned] = useState(createTrajectoryStore);
  useEffect(() => {
    return () => {
      owned.dispose();
    };
  }, [owned]);
  return provided ?? owned;
}
