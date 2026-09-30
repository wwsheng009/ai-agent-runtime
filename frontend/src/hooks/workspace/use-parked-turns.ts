// §6.8 托管挂起（parked turn）控制器：事件流 → 纯函数归约 → 按当前会话 select。
//
// 归约状态**全局累积、呈现按会话过滤**（与 `usePendingInteractions` 同口径）：
// 切换会话时 select 自然回落到空，不会把 A 会话的挂起态泄漏给 B 会话；
// 切回原会话时仍能看到它自己的挂起态（无需重放事件）。
//
// 归约状态由 `useWorkspaceLive` 持有（前台会话的事件入口就在那里），
// 与待交互归约并列消费同一事件流——`agent.turn.finished` 也在同一入口到达，
// 但归约器显式忽略它（只表示本次 run 结束，不表示义务终态）。
//
// gap 3b 迟到唤醒：父 turn 正常结束、从未 suspended 时，若干分钟后的
// `turn.resumed` 仍会 select 出一条恢复通知；它是瞬时表达——TTL 到点自动清掉，
// 同会话新一轮 `turn.suspended` 也会在归约层清除（不与挂起态混淆）。

import { useCallback, useEffect, useMemo, useState } from "react";

import { projectParkedTurnTaskCounts } from "@/components/workspace/session-agents-panel-shared";
import {
  applyParkedTurnEvent,
  clearResumedTurnNotice,
  emptyParkedTurnState,
  selectParkedTurn,
  selectResumedTurnNotice,
  type ParkedTurnSnapshot,
  type ParkedTurnState,
  type ParkedTurnSurface,
  type ParkedTurnView,
  type ResumedTurnNotice,
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
  /** §6.8 迟到唤醒：最近一次被监督恢复的瞬时通知；到期 / 新一轮挂起清除。 */
  resumedNotice: ResumedTurnNotice | null;
  /** 完整归约状态（测试与诊断用）。 */
  state: ParkedTurnState;
};

/** 迟到唤醒通知的存活窗口：到点自动消失，不长期粘在 composer 上沿。 */
export const RESUMED_TURN_NOTICE_TTL_MS = 10_000;

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

  const resumedNotice = useMemo(
    () => selectResumedTurnNotice(state, sessionId),
    [sessionId, state],
  );

  // 瞬时通知的自动消失：只有当前这条仍存活时才由归约器清除（引用比对，
  // 迟到的定时器不会误删新一轮通知）。
  useEffect(() => {
    if (!resumedNotice) {
      return;
    }
    const timer = setTimeout(() => {
      setState((current) => clearResumedTurnNotice(current, resumedNotice));
    }, RESUMED_TURN_NOTICE_TTL_MS);
    return () => clearTimeout(timer);
  }, [resumedNotice]);

  return { applyRuntimeEvent, parkedTurn, resumedNotice, state };
}

/**
 * 呈现视图：把挂起 / 迟到唤醒呈现面与 AgentControl 目录投影打包（见 `ParkedTurnView`）。
 *
 * 目录投影只在挂起边沿补刷一次：挂起期间新起的子代理不在会话切换时的旧目录里，
 * 「完成 / 异常」会被少算；一次挂起一次请求，不是每条运行时事件都刷。
 * 仅通知存在时 `taskCounts` 为 null（通知不涉及目录，不伪造计数）。
 */
export function useParkedTurnView(
  surface: ParkedTurnSurface | null | undefined,
  agents: readonly RuntimeAgentRecord[],
  refreshAgents: () => void,
): ParkedTurnView | null {
  const turn = surface?.turn ?? null;
  const resumedNotice = surface?.resumedNotice ?? null;
  const view = useMemo(
    () =>
      turn || resumedNotice
        ? {
            turn,
            taskCounts: turn ? projectParkedTurnTaskCounts(agents) : null,
            resumedNotice,
          }
        : null,
    [agents, resumedNotice, turn],
  );

  const parkedTurnId = turn?.turnId ?? "";
  useEffect(() => {
    if (!parkedTurnId) {
      return;
    }
    refreshAgents();
  }, [parkedTurnId, refreshAgents]);

  return view;
}
