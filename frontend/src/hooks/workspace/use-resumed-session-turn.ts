import { type Dispatch, type SetStateAction, useEffect, useRef } from "react";

import type { Thread } from "@/data/mock";
import type { RuntimeSessionActiveTurn } from "@/lib/runtime-api";
import { normalizeSessionId } from "@/lib/session-id";
import {
  adoptResumedTurnInThread,
  releaseResumedTurnInThread,
} from "@/lib/thread-state/resumed-turn";

/**
 * 续传挂载期间的心跳：`/runtime` 快照只在会话切换 / 手动刷新时拉取，回合结束后
 * `active_turn` 不会自己变空——不轮询的话刷新页面上的气泡会一直转圈。挂载期间
 * 按固定间隔重新拉取（只在续传生效时开启，回合结束后自动停），快照收敛为
 * 无在途回合时由 release 撤销 streaming。
 */
export const RESUMED_TURN_HEARTBEAT_MS = 5_000;

export type UseResumedSessionTurnOptions = {
  /** 当前选中的会话 id（路由 / 线程）。空串 = 无会话（草稿会话）。 */
  sessionId?: string;
  /** `GET /runtime` 快照里的在途回合（见 use-session-runtime-state）。 */
  activeTurn: RuntimeSessionActiveTurn | null;
  /** 本地直连回合身份：非空说明本地 POST 拥有本回合，续传不得抢。 */
  localTurnId: string | null;
  /** 本地直连回合是否在跑（与 localTurnId 同源）。 */
  localResponding: boolean;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  /** 重新拉取 `/runtime` 快照（心跳），通常传 use-session-runtime-state 的 refresh。 */
  refreshRuntimeState?: () => void;
};

export type UseResumedSessionTurnResult = {
  /**
   * 生效中的续传回合 id；无则 null。
   *
   * 消费点（三处必须一致，否则表现为「刷新后流还在但什么都不渲染」）：
   * - `useSessionRuntimeStream` 的 `renderLiveDeltas` 闸门；
   * - `useSessionRuntimeStream` 的 `activeTurnId`（增量帧的回合归属判定）；
   * - `isThreadResponding`（会话是否在生成回复：切换确认、顶栏状态、历史同步节流）。
   */
  resumedTurnId: string | null;
  /** 续传回合是否仍在服务端执行（回合结束后仍保留 id 直到下一次快照收敛）。 */
  resumedTurnActive: boolean;
};

function updateSessionThread(
  threads: Thread[],
  sessionId: string,
  updater: (thread: Thread) => Thread,
): Thread[] {
  const target = normalizeSessionId(sessionId);
  if (!target) {
    return threads;
  }
  let changed = false;
  const next = threads.map((thread) => {
    if (normalizeSessionId(thread.sessionId || thread.id) !== target) {
      return thread;
    }
    const updated = updater(thread);
    if (updated !== thread) {
      changed = true;
    }
    return updated;
  });
  return changed ? next : threads;
}

/**
 * P4-刷新续传：把服务端仍在执行的回合重新挂载到刷新后的页面上。
 *
 * 症状（优化前）：刷新会 abort `/api/agent/chat` 的 POST，服务端回合随请求上下文
 * 取消而中止，且新页面没有任何回合身份——`renderLiveDeltas` 闸门关闭，`/runtime/stream`
 * 即使按游标重连，落库的增量帧也会被整帧过滤，表现为「刷新后整个 SSE live 断开」。
 *
 * 前置（服务端 + 快照）：`resume_on_disconnect` 让回合在客户端断开后继续执行
 * （见 backend handler.go / session_active_turn.go），`/runtime` 以 `active_turn`
 * 暴露「本会话此刻在跑哪个回合」。
 *
 * 本 hook 的职责只有两件：
 * 1. 挂载：快照报告在途回合、且本地没有在途回合时，把回合身份认领到线程末尾
 *    那条半截助手消息上（纯函数见 lib/thread-state/resumed-turn.ts），并向外
 *    暴露 `resumedTurnId` 供增量闸门 / 归属判定使用；
 * 2. 撤销：回合结束（快照的 active_turn 消失或被别的回合取代）时清掉 streaming
 *    标记，避免气泡永远转圈。
 *
 * 不接管的情况：本地直连回合在跑（localTurnId 非空）——那是本地 POST 的回合，
 * 服务端快照里的 active_turn 可能与它同源，重复挂载只会把回合身份搅乱。
 */
export function useResumedSessionTurn({
  sessionId,
  activeTurn,
  localTurnId,
  localResponding,
  setThreads,
  refreshRuntimeState,
}: UseResumedSessionTurnOptions): UseResumedSessionTurnResult {
  const normalizedSessionId = normalizeSessionId(sessionId ?? "");
  const serverTurnId = normalizedSessionId
    ? (activeTurn?.turnId ?? "").trim()
    : "";
  const occupiedByLocalTurn = Boolean(localTurnId?.trim()) || localResponding;
  const adoptable = Boolean(serverTurnId) && !occupiedByLocalTurn;

  // 已挂载的回合身份（会话 + 回合）。用 ref 而非 state：它只用于「是否已经写过
  // 线程」的去重判定，不参与渲染；渲染侧的续传身份由 adoptable 直接派生，避免
  // 服务端快照收敛与本地状态之间出现第二份真相。
  const mountedRef = useRef<{ sessionId: string; turnId: string } | null>(null);

  // 挂载 / 撤销是同一条状态机（挂载过谁 → 现在该挂谁），合并成一个 effect：
  // 分两条写时「服务端回合换了 id」这种转换会在旧回合撤销与新回合挂载之间留下
  // 一条永远转圈的消息（旧回合的 streaming 没人清）。
  useEffect(() => {
    const mounted = mountedRef.current;
    const alreadyMounted =
      mounted !== null &&
      mounted.sessionId === normalizedSessionId &&
      mounted.turnId === serverTurnId;

    if (!adoptable) {
      if (!mounted) {
        return;
      }
      mountedRef.current = null;
      setThreads((current) =>
        updateSessionThread(current, mounted.sessionId, (thread) =>
          releaseResumedTurnInThread(thread, mounted.turnId),
        ),
      );
      return;
    }
    if (alreadyMounted) {
      return;
    }
    mountedRef.current = {
      sessionId: normalizedSessionId,
      turnId: serverTurnId,
    };
    setThreads((current) => {
      // 先撤销上一个（可能属于别的会话）的挂载，再挂新的：两条转换在同一个
      // setState 里完成，避免中间态渲染出「旧回合仍在 streaming」。
      const released = mounted
        ? updateSessionThread(current, mounted.sessionId, (thread) =>
            releaseResumedTurnInThread(thread, mounted.turnId),
          )
        : current;
      return updateSessionThread(released, normalizedSessionId, (thread) =>
        adoptResumedTurnInThread(thread, serverTurnId),
      );
    });
  }, [adoptable, normalizedSessionId, serverTurnId, setThreads]);

  const resumedTurnId = adoptable ? serverTurnId : null;

  useEffect(() => {
    if (!resumedTurnId || !refreshRuntimeState) {
      return;
    }
    const timer = window.setInterval(
      refreshRuntimeState,
      RESUMED_TURN_HEARTBEAT_MS,
    );
    return () => window.clearInterval(timer);
  }, [resumedTurnId, refreshRuntimeState]);

  return {
    resumedTurnId,
    resumedTurnActive: Boolean(resumedTurnId),
  };
}
