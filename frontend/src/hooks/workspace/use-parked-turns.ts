// §6.8 托管挂起（parked turn）控制器：事件流 → 纯函数归约 → 按当前会话 select。
//
// 归约状态**全局累积、呈现按会话过滤**（与 `usePendingInteractions` 同口径）：
// 切换会话时 select 自然回落到空，不会把 A 会话的挂起态泄漏给 B 会话；
// 切回原会话时仍能看到它自己的挂起态（无需重放事件）。
//
// 归约状态由 `useWorkspaceLive` 持有（前台会话的事件入口就在那里），
// 与待交互归约并列消费同一事件流——`agent.turn.finished` 也在同一入口到达，
// 但归约器显式忽略它（只表示本次 run 结束，不表示义务终态）。

import { useCallback, useEffect, useMemo, useState } from "react";

import { projectParkedTurnTaskCounts } from "@/components/workspace/session-agents-panel-shared";
import {
  applyParkedTurnEvent,
  emptyParkedTurnState,
  selectParkedTurn,
  type ParkedTurnSnapshot,
  type ParkedTurnState,
  type ParkedTurnView,
} from "@/lib/parked-turn";
import type { RuntimeAgentRecord, SessionRuntimeEvent } from "@/types/runtime";

export type UseParkedTurnsOptions = {
  /** 当前会话 id；空串表示未附着会话（select 恒为 null）。 */
  sessionId?: string;
};

export type UseParkedTurnsResult = {
  /** 投递一条运行时事件（非挂起/恢复事件按原状态返回）。 */
  applyRuntimeEvent: (event: SessionRuntimeEvent) => void;
  /** 当前会话的挂起快照；未挂起 / 无会话时为 null。 */
  parkedTurn: ParkedTurnSnapshot | null;
  /** 完整归约状态（测试与诊断用）。 */
  state: ParkedTurnState;
};

export function useParkedTurns({
  sessionId = "",
}: UseParkedTurnsOptions = {}): UseParkedTurnsResult {
  const [state, setState] = useState<ParkedTurnState>(emptyParkedTurnState);

  const applyRuntimeEvent = useCallback((event: SessionRuntimeEvent) => {
    setState((current) => applyParkedTurnEvent(current, event));
  }, []);

  const parkedTurn = useMemo(
    () => selectParkedTurn(state, sessionId),
    [sessionId, state],
  );

  return { applyRuntimeEvent, parkedTurn, state };
}

/**
 * 呈现视图：把挂起快照与 AgentControl 目录投影打包（见 `ParkedTurnView`）。
 *
 * 目录投影只在挂起边沿补刷一次：挂起期间新起的子代理不在会话切换时的旧目录里，
 * 「完成 / 异常」会被少算；一次挂起一次请求，不是每条运行时事件都刷。
 * 参数用位置参数（三个短参数）是为了让调用点的行数预算友好（P0-2 行数门禁）。
 */
export function useParkedTurnView(
  parkedTurn: ParkedTurnSnapshot | null | undefined,
  agents: readonly RuntimeAgentRecord[],
  refreshAgents: () => void,
): ParkedTurnView | null {
  const view = useMemo(
    () =>
      parkedTurn
        ? { turn: parkedTurn, taskCounts: projectParkedTurnTaskCounts(agents) }
        : null,
    [agents, parkedTurn],
  );

  const parkedTurnId = parkedTurn?.turnId ?? "";
  useEffect(() => {
    if (!parkedTurnId) {
      return;
    }
    refreshAgents();
  }, [parkedTurnId, refreshAgents]);

  return view;
}
