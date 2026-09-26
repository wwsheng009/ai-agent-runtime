import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";

import { requestSessionTurnInterrupt } from "@/api/runtime/session-turn-control";
import { type Thread } from "@/data/mock";
import { type ConnectionStatus } from "@/lib/connection-status";
import {
  reportBlockedDelta,
  reportRenderGate,
  reportSnapshotRefresh,
  reportUnownedTurn,
} from "@/lib/live-diagnostics/store";
import type { ParkedTurnSnapshot } from "@/lib/parked-turn";
import { normalizeSessionId } from "@/lib/session-id";
import {
  type RuntimeSessionActiveTurn,
  type SessionRuntimeEvent,
} from "@/lib/runtime-api";
import { matchesActiveTurn } from "@/lib/thread-state/deltas";
import { isChatSseTerminalFrame } from "@/lib/thread-state/events-live";
import {
  finalizedTurnIdsFor,
  withLocallyFinalizedTurn,
  withSnapshotTurn,
  type LocallyFinalizedTurns,
} from "@/lib/thread-state/locally-finalized-turns";
import { trajectoryEventAction } from "@/lib/trajectory/recovery";
import {
  getRuntimeDeltaKind,
  getRuntimeEventTurnId,
  type RuntimeDeltaCoordinator,
} from "@/lib/workspace-thread-state";
import {
  applyRuntimeDeltaToThread,
  applyRuntimeEventToThread,
  getErrorMessage,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
} from "./thread-runtime";
import { useParkedTurns } from "./use-parked-turns";
import { useResumedSessionTurn } from "./use-resumed-session-turn";
import { useSessionRuntimeStream } from "./use-session-runtime-stream";
import { type TrajectoryStore } from "./use-trajectory-snapshot";
import { isThreadResponding } from "./use-workspace-thread-selection";

/**
 * 「未认领回合」快照刷新节流：发现服务端在途回合后拉一次 `/runtime`（续传认领
 * 的唯一入口），同一回合在该窗口内不重复触发；窗口落空（拉取失败 / 回合刚结束）
 * 时后续增量会再补一次，不会卡死。
 */
const UNOWNED_TURN_REFRESH_MS = 3_000;

export type UseWorkspaceLiveOptions = {
  /** 本地直连回合身份（`/api/agent/chat` 的 POST 拥有者）；空 = 本地没有在途回合。 */
  localTurnId: string | null;
  /** 本地直连回合是否在跑（与 localTurnId 同源）。 */
  localResponding: boolean;
  /** `GET /runtime` 快照里的在途回合（刷新后重建回合身份的唯一来源）。 */
  sessionActiveTurn: RuntimeSessionActiveTurn | null;
  /** 当前选中的会话 id（路由 / 线程）。空串 = 草稿会话。 */
  sessionId?: string;
  selectedThread: Thread | undefined;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  /** 重新拉取 `/runtime` 快照（续传挂载期间的心跳）。 */
  refreshRuntimeState?: () => void;
  /** 与直连 chat 共享的增量认领协调器（两条流不重复渲染同一段增量）。 */
  deltaCoordinator: RuntimeDeltaCoordinator;
  /** 轨迹首屏窗口是否已就绪（建连闸门，见 use-session-runtime-stream）。 */
  trajectoryReady: boolean;
  /** 轨迹快照仓库：生命周期事件桥接 + 首屏建连游标。 */
  trajectoryStore: TrajectoryStore;
  /** 待交互生命周期归约（每条运行时事件到达时通知）。 */
  onRuntimeEvent: (event: SessionRuntimeEvent) => void;
};

export type UseWorkspaceLiveResult = {
  connectionStatus: ConnectionStatus;
  retryConnection: () => void;
  /**
   * 本地直连回合 / 服务端续传回合统一后的「当前在途回合」身份；无则 null。
   *
   * 它是增量闸门（`renderLiveDeltas`）、增量帧归属判定（`activeTurnId`）与
   * 流式消息的对齐基准——刷新后续传就是靠它把渲染接回同一条消息。
   */
  liveTurnId: string | null;
  /** 当前会话是否正在生成回复（本地回合或续传回合）。 */
  currentSessionResponding: boolean;
  /** §6.8 当前会话的托管挂起快照；未挂起 / 无会话时为 null。 */
  parkedTurn: ParkedTurnSnapshot | null;
  /**
   * P4-刷新续传：停止「服务端仍在跑、本地没有请求可 abort」的续传回合。
   *
   * 本地回合不走这里——那条路径由 chat-turn hook 在 abort 的同时带本地回合身份
   * 投递 interrupt（见 use-workspace-agent-chat-turn.stopResponding）。这里只补
   * 刷新后的入口：新页面没有本地回合，停止按钮不显式发命令就只是本地幻觉
   * （`resume_on_disconnect` 之后 abort ≠ 服务端回合结束）。
   *
   * best-effort（网络失败 / 409 回合已换代都不阻塞 UI），随后主动刷新一次快照让
   * `active_turn` 收敛，不必等下一次心跳（5s）按钮才变回发送态。
   */
  stopResumedTurn: () => Promise<void>;
};

/**
 * P4-刷新续传：会话运行时 live 通道的唯一接线点（回合身份 + `/runtime/stream`）。
 *
 * 刷新会 abort 在途的 `/api/agent/chat`，但服务端回合不再随之取消
 * （`resume_on_disconnect`），并通过 `/runtime` 的 `active_turn` 告诉新页面
 * 「本会话仍在跑哪个回合」。这条身份必须挂回来，否则 runtime/stream 虽然按游标
 * 重连，增量帧也会因闸门关闭被整帧过滤——表现为「刷新后整个 SSE live 断开」。
 *
 * 三件事收口在一处，避免调用方漏接其中一个（任一处断开都会重现上面的症状）：
 * 1. 续传挂载 / 撤销：`useResumedSessionTurn` 从快照认领回合身份；
 * 2. 身份统一：本地直连回合优先，其次续传回合（两者合并成 liveTurnId）；
 * 3. 建连 / 续传：`useSessionRuntimeStream` 按游标重连并渲染增量。
 */
export function useWorkspaceLive({
  deltaCoordinator,
  localResponding,
  localTurnId,
  onRuntimeEvent,
  refreshRuntimeState,
  selectedThread,
  sessionActiveTurn,
  sessionId,
  setThreads,
  trajectoryReady,
  trajectoryStore,
}: UseWorkspaceLiveOptions): UseWorkspaceLiveResult {
  const normalizedSessionId = normalizeSessionId(sessionId ?? "");
  // 本地已收终态帧的回合：终态帧先于 `/runtime` 快照 release 可见时，阻断
  // 「同回合重新认领」（否则会把刚定稿的消息补回 streaming）。抑制集在快照
  // 收敛（active_turn 变空 / 换回合）后自动清理，见下面的 prune effect。
  const [locallyFinalizedTurns, setLocallyFinalizedTurns] =
    useState<LocallyFinalizedTurns>(null);
  const locallyFinalizedTurnIds = finalizedTurnIdsFor(
    locallyFinalizedTurns,
    normalizedSessionId,
  );
  // 快照回合变化时收敛本会话的抑制集：key（会话 + 回合）变化才触发一次性修剪，
  // 不写回 props，不产生级联（同 message-list / use-reasoning-effort 的既有先例）。
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLocallyFinalizedTurns((current) =>
      withSnapshotTurn(current, normalizedSessionId, sessionActiveTurn?.turnId),
    );
  }, [normalizedSessionId, sessionActiveTurn?.turnId]);

  const { resumedTurnId, resumedTurnActive } = useResumedSessionTurn({
    sessionId,
    activeTurn: sessionActiveTurn,
    locallyFinalizedTurnIds,
    localTurnId,
    localResponding,
    setThreads,
    refreshRuntimeState,
  });
  // §6.8 托管挂起：本会话的 `turn.suspended` / `turn.resumed` 边沿投影。状态挂在
  // 事件入口的同一层（本 hook 持有前台会话的 onRuntimeEvent 投递），随会话切换
  // 按 session_id select；`agent.turn.finished` 不参与清除（见 lib/parked-turn）。
  const { applyRuntimeEvent: applyParkedTurnEvent, parkedTurn } = useParkedTurns({
    sessionId,
  });
  // 本地直连回合 / 服务端续传回合统一成一个「当前在途回合」身份：增量闸门、
  // 流通道归属判定与流式消息都按它对齐。
  const liveTurnId = localTurnId ?? resumedTurnId;
  // 只有「当前会话正在生成回复」时才需要二次确认与刷新抑制：后台会话的 turn 会让
  // 全局 isResponding 保持 true，因此按会话归属（流式消息挂在哪个线程上）判断；
  // 续传回合同理——它挂在刷新后重新认领的那条消息上。
  const currentSessionResponding =
    isThreadResponding(selectedThread, localTurnId) ||
    isThreadResponding(selectedThread, resumedTurnId);
  // 增量闸门：请求在跑（本地或续传）且认领到了回合身份，runtime/stream 的打字机
  // 增量才允许落到消息上。同一个值既传给流 hook，也上报给「网络详情」观测块——
  // 「事件到了但闸门关着」正是页面不动的两类根因之一。
  const renderLiveDeltas =
    (localResponding || resumedTurnActive) && Boolean(liveTurnId);
  useEffect(() => {
    reportRenderGate(sessionId, {
      liveTurnId: liveTurnId ?? null,
      resumedTurnId: resumedTurnId ?? null,
      localResponding,
      rendering: renderLiveDeltas,
    });
  }, [liveTurnId, localResponding, renderLiveDeltas, resumedTurnId, sessionId]);
  // 未认领回合发现 → 快照刷新。没有这一跳，闸门会停在关闭状态：本地 chat 流
  // 静默中断（读侧看门狗）后本地回合身份被清空，而 `/runtime` 快照只在会话
  // 切换 / 手动刷新 / 续传心跳里拉取，续传心跳又只在认领之后启动——鸡生蛋。
  // 落库增量因此被 renderLiveDeltas 整帧过滤，只有整页刷新（重拉快照 + 历史）
  // 才能重新看到内容。
  const unownedTurnNotifyRef = useRef<{ turnId: string; at: number } | null>(
    null,
  );
  const handleUnownedTurn = useCallback(
    (turnId: string) => {
      const now = Date.now();
      const last = unownedTurnNotifyRef.current;
      if (
        last &&
        last.turnId === turnId &&
        now - last.at < UNOWNED_TURN_REFRESH_MS
      ) {
        return;
      }
      unownedTurnNotifyRef.current = { turnId, at: now };
      // 观测（「网络详情」）：未认领回合是「事件在到达、闸门却是关的」的根因之一，
      // 与随后的快照刷新成对记账，面板上才看得出自愈动作有没有真的发生。
      reportUnownedTurn(sessionId, turnId);
      if (refreshRuntimeState) {
        refreshRuntimeState();
        reportSnapshotRefresh(sessionId);
      }
    },
    [refreshRuntimeState, sessionId],
  );
  const stopResumedTurn = useCallback(async () => {
    // resumedTurnId 只在「本地无在途回合」时认领（见 useResumedSessionTurn 的
    // adoptable 判定），因此本地回合在跑时这里天然是空操作，不会重复投递。
    if (!resumedTurnId) {
      return;
    }
    await requestSessionTurnInterrupt(sessionId, resumedTurnId);
    refreshRuntimeState?.();
  }, [refreshRuntimeState, resumedTurnId, sessionId]);
  const { connectionStatus, retryConnection } = useSessionRuntimeStream({
    applyRuntimeEventToThread,
    applyRuntimeDeltaToThread,
    getErrorMessage,
    getRuntimeEventSeq,
    mergeRuntimeEvent,
    // 建连闸门 + 建连游标：窗口就绪后才连，且只订阅窗口之后的新事件。
    enabled: trajectoryReady,
    getReplayCursor: () => trajectoryStore.getSnapshot().lastEventSeq,
    onTrajectoryEvent: (event) => {
      // Q4：runtime 生命周期事件实时投递到轨迹，与恢复路径共用同一转换
      // （幂等：reducer 按 seq 去重）。工具生命周期（tool_started/
      // tool_finished/tool_receipt_recorded）已转为可渲染的工具行；不建行的
      // tail-only / provenance 事件（如 subagent.started、recall.performed）
      // 与 chat.sse 共享同一 EventStore 全局 seq，已持久化但不会渲染——
      // advanceCursor 跳过其空洞，避免后续事件永久卡 pending。
      const action = trajectoryEventAction(event);
      if (action.kind === "push") {
        trajectoryStore.push(action.push.kind, action.push.payload);
      } else if (action.kind === "skip") {
        trajectoryStore.advanceCursor(action.seq);
      }
    },
    // 未认领回合发现（见 handleUnownedTurn 注释）：运行时流只负责传输，
    // 「这条事件属不属于本页的在途回合」的判定收口在这里——本页没有任何回合
    // 身份、事件却带着一个 turn 身份，说明服务端有个未认领的回合正在产增量。
    // 不主动去认领，renderLiveDeltas 闸门就会一直关着（刷新前整条流都不渲染）。
    onRuntimeEvent: (event) => {
      const turnId = getRuntimeEventTurnId(event);
      // 终态帧 = 本回合在本地已经结束：先记账再交给上层归约。服务端的 release
      // 可能还没在快照里可见，靠这份记账让同一回合不被续传重新认领。
      if (turnId && isChatSseTerminalFrame(event.type)) {
        setLocallyFinalizedTurns((current) =>
          withLocallyFinalizedTurn(current, normalizedSessionId, turnId),
        );
      }
      const deltaKind = getRuntimeDeltaKind(event.type);
      if (!liveTurnId) {
        if (turnId && deltaKind) {
          handleUnownedTurn(turnId);
        }
      }
      // 归属判定与流 hook 内一致（`未知` ≠ `其他 turn`）：不满足「闸门开 + 帧属于
      // 在途回合」的增量帧不会渲染，逐帧记账——这是「网络详情」判定
      // 「事件到了但没渲染」的唯一证据来源。
      if (
        deltaKind &&
        !(renderLiveDeltas && matchesActiveTurn(liveTurnId ?? "", turnId))
      ) {
        reportBlockedDelta(sessionId);
      }
      // §6.8：挂起 / 恢复事件在同一入口做边沿归约（只读投影，不影响线程状态）。
      applyParkedTurnEvent(event);
      onRuntimeEvent(event);
    },
    // 方案B：请求进行中才渲染 runtime/stream 的打字机增量（delta/reasoning/
    // image_progress）；回放/reload 只进事件快照，不误渲染历史增量。
    // P4-刷新续传：刷新后本地没有直连回合，但服务端可能仍在跑——续传身份补上后
    // 闸门重新打开，落库的增量帧才会继续写进（被认领的）同一条消息。
    activeTurnId: liveTurnId,
    deltaCoordinator,
    renderLiveDeltas,
    selectedThread,
    setThreads,
  });

  return {
    connectionStatus,
    currentSessionResponding,
    liveTurnId,
    parkedTurn,
    retryConnection,
    stopResumedTurn,
  };
}
