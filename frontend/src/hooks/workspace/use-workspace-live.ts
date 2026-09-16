import { type Dispatch, type SetStateAction } from "react";

import { type Thread } from "@/data/mock";
import { type ConnectionStatus } from "@/lib/connection-status";
import {
  type RuntimeSessionActiveTurn,
  type SessionRuntimeEvent,
} from "@/lib/runtime-api";
import { trajectoryEventAction } from "@/lib/trajectory/recovery";
import { type RuntimeDeltaCoordinator } from "@/lib/workspace-thread-state";
import {
  applyRuntimeDeltaToThread,
  applyRuntimeEventToThread,
  getErrorMessage,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
} from "./thread-runtime";
import { useResumedSessionTurn } from "./use-resumed-session-turn";
import { useSessionRuntimeStream } from "./use-session-runtime-stream";
import { type TrajectoryStore } from "./use-trajectory-snapshot";
import { isThreadResponding } from "./use-workspace-thread-selection";

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
  const { resumedTurnId, resumedTurnActive } = useResumedSessionTurn({
    sessionId,
    activeTurn: sessionActiveTurn,
    localTurnId,
    localResponding,
    setThreads,
    refreshRuntimeState,
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
      // （幂等：reducer 按 seq 去重）。被过滤的事件（tool_started/
      // tool_finished 等与 chat.sse 共享同一 EventStore 全局 seq）已持久化
      // 但不会渲染——advanceCursor 跳过其空洞，避免后续事件永久卡 pending。
      const action = trajectoryEventAction(event);
      if (action.kind === "push") {
        trajectoryStore.push(action.push.kind, action.push.payload);
      } else if (action.kind === "skip") {
        trajectoryStore.advanceCursor(action.seq);
      }
    },
    onRuntimeEvent,
    // 方案B：请求进行中才渲染 runtime/stream 的打字机增量（delta/reasoning/
    // image_progress）；回放/reload 只进事件快照，不误渲染历史增量。
    // P4-刷新续传：刷新后本地没有直连回合，但服务端可能仍在跑——续传身份补上后
    // 闸门重新打开，落库的增量帧才会继续写进（被认领的）同一条消息。
    activeTurnId: liveTurnId,
    deltaCoordinator,
    renderLiveDeltas:
      (localResponding || resumedTurnActive) && Boolean(liveTurnId),
    selectedThread,
    setThreads,
  });

  return {
    connectionStatus,
    currentSessionResponding,
    liveTurnId,
    retryConnection,
  };
}
