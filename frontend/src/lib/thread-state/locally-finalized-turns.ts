/**
 * 「本地已收到终态帧」的回合仲裁（2026-09-18）。
 *
 * 背景：`/runtime` 快照的 `active_turn` 由服务端在收尾时释放，而 `chat.sse.done`
 * 是 release 之前就写进 SSE 缓冲的——客户端可能先收到 done、后看到快照收敛。
 * 续传挂载（`useResumedSessionTurn` → `adoptResumedTurnInThread`）对「同
 * runtimeTurnId 但 streaming 丢了」的消息会补回 streaming，于是这个时间差会把
 * 刚定稿的气泡重新标回转圈，直到下一次心跳（5s）才收敛。
 *
 * 这里记录「本地已收终态帧」的回合 id：在快照收敛（active_turn 变空或换成别的
 * 回合）之前，该回合不再被重新认领；5s 心跳保留为纯粹的快照收敛兜底。
 *
 * 纯函数、无 React 依赖；按会话隔离，会话切换即视为空集。
 */

export type LocallyFinalizedTurns = {
  sessionId: string;
  turnIds: ReadonlySet<string>;
} | null;

/** 无抑制项时返回的共享空集：保证调用方拿到稳定引用（React effect 依赖安全）。 */
export const NO_LOCALLY_FINALIZED_TURNS: ReadonlySet<string> = new Set<string>();

export function finalizedTurnIdsFor(
  state: LocallyFinalizedTurns,
  sessionId: string,
): ReadonlySet<string> {
  if (!state || state.sessionId !== sessionId.trim()) {
    return NO_LOCALLY_FINALIZED_TURNS;
  }
  return state.turnIds;
}

/** 记录一个本地已终态的回合（幂等；无变化时保持原引用）。 */
export function withLocallyFinalizedTurn(
  state: LocallyFinalizedTurns,
  sessionId: string,
  turnId: string | null | undefined,
): LocallyFinalizedTurns {
  const sid = sessionId.trim();
  const tid = (turnId ?? "").trim();
  if (!sid || !tid) {
    return state;
  }
  const current = finalizedTurnIdsFor(state, sid);
  if (current.has(tid)) {
    return state;
  }
  const turnIds = new Set(current);
  turnIds.add(tid);
  return { sessionId: sid, turnIds };
}

/**
 * 快照更新时的收敛清理：只保留「仍被快照报告的回合」这一条抑制项——
 * `active_turn` 变空（回合真正结束）或换成新回合后，旧回合自动解除抑制。
 * 无变化时保持原引用，避免调用方 effect 自激。
 */
export function withSnapshotTurn(
  state: LocallyFinalizedTurns,
  sessionId: string,
  snapshotTurnId: string | null | undefined,
): LocallyFinalizedTurns {
  const sid = sessionId.trim();
  if (!sid) {
    return state && state.turnIds.size === 0 ? state : null;
  }
  const keep = (snapshotTurnId ?? "").trim();
  const current = finalizedTurnIdsFor(state, sid);
  let changed = state === null || state.sessionId !== sid;
  const turnIds = new Set<string>();
  current.forEach((id) => {
    if (id === keep) {
      turnIds.add(id);
    } else {
      changed = true;
    }
  });
  if (!changed) {
    return state;
  }
  return turnIds.size > 0 ? { sessionId: sid, turnIds } : null;
}
