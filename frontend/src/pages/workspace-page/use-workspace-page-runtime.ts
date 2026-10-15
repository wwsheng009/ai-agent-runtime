// P0-2：workspace-page.tsx 机械拆分「多会话运行时 / 连接 / 刷新 / 回溯」区块——仅搬迁不改逻辑。

import { useSessionBacktrack } from "@/hooks/workspace/use-session-backtrack";
import { useSessionRefresh } from "@/hooks/workspace/use-session-refresh";
import { useTrajectoryEarlierEntry } from "@/hooks/workspace/use-trajectory-earlier-entry";
import { useWorkspaceMultiSessionRuntime } from "@/hooks/workspace/use-workspace-multi-session-runtime";
import { useWorkspaceSessionSwitchGuard } from "@/hooks/workspace/use-workspace-session-switch-guard";
import { useWorkspaceThreadPayloadRetention } from "@/hooks/workspace/use-workspace-thread-payload-retention";
import { withTransportDegradation } from "@/lib/connection-status";
import { applySessionHistoryToThread } from "@/lib/workspace-thread-state";

import { useWorkspacePageTurn } from "./use-workspace-page-turn";

export function useWorkspacePageRuntime() {
  const turn = useWorkspacePageTurn();
  const {
    activeSessionKeys,
    clearStreamStall,
    connectionStatus,
    currentSessionResponding,
    draft,
    handleRefreshRuntimeSessions,
    handleRuntimeEvent,
    handleSelectThread,
    handleStopResponding,
    isResponding,
    pendingInteraction,
    recoverSessionHistory,
    refreshSessionRuntimeState,
    retryConnection,
    selectedThread,
    setDraft,
    setThreads,
    threads,
    trajectoryReplay,
    trajectoryStorePool,
  } = turn;
  // P0-2 + §4.5：切线守卫（确认框 ↔ 后台继续跑）收口在
  // `hooks/workspace/use-workspace-session-switch-guard.ts`。必须排在
  // useWorkspaceLive 之后：守卫的 currentSessionResponding 来自 live 通道。
  const {
    cancelSessionSwitch,
    confirmSessionSwitch: handleConfirmSessionSwitch,
    pendingSessionSwitch,
    selectThread: handleSelectThreadWithTrajectoryReset,
  } = useWorkspaceSessionSwitchGuard({
    currentSessionResponding,
    onSelectThread: handleSelectThread,
    selectedThread,
    threads,
  });
  // P0-2：多会话运行时接线（后台订阅候选 / 建连游标 / 事件归约 / 活动投影 /
  // 通知 / 按会话就地停止）已收口到
  // `hooks/workspace/use-workspace-multi-session-runtime.ts`（§4.2 / §4.3 / §4.7）。
  const {
    handleOpenRuntimeNoticeSession,
    handleStopRuntimeSession,
    sessionActivity,
    sessionRuntimeEntries,
    sessionRuntimeNotices,
    threadBySessionId,
  } = useWorkspaceMultiSessionRuntime({
    activeSessionKeys,
    applyPendingInteractionEvent: handleRuntimeEvent,
    openThread: handleSelectThreadWithTrajectoryReset,
    pendingInteractionKind: pendingInteraction?.kind ?? null,
    responding: currentSessionResponding,
    selectedSessionId: selectedThread?.sessionId ?? null,
    stopSelectedSession: handleStopResponding,
    threads,
    trajectoryStorePool,
  });
  // 内存治理：会话载荷 LRU 卸载（保护集合 = 本地在途回合 ∪ 注册表活动回合，
  // 由 hook 汇总；见 lib/thread-state/retention.ts 与 hook 头注释）。
  useWorkspaceThreadPayloadRetention({
    selectedThreadId: selectedThread?.id ?? null,
    activeSessionKeys,
    sessionRuntimeEntries,
    setThreads,
  });
  // P1-8：连接状态统一收口。会话运行时流状态是主判据；直连 `/api/agent/chat`
  // 流失败会把线程标记为 transport=error（见 use-workspace-agent-chat-turn），
  // 此时会话流可能仍在线，但顶栏/流尾必须显示「断线 + 可手动重试」。手动重试
  // 复用会话流入口（重试前按本地 last seq 拉齐），不重发 chat 请求，因此
  // 不会产生重复回合请求。
  const connectionStatusForUi = withTransportDegradation(
    connectionStatus,
    selectedThread?.transport,
  );
  // P1-8：降级来源不止会话运行时流（直连 chat、轨迹恢复、历史同步失败都会置
  // transport=error）。只重启会话流无法清除这些降级——空闲会话上点「重试」
  // 徽标不变，用户会认为重试失效。降级时追加一次权威历史探活：成功即恢复，
  // 失败保留降级并刷新原因（回合进行中不做，避免覆盖在途的流式消息）。
  function handleRetryConnection() {
    // 读侧静默看门狗命中后的手动重试：先清掉「本页流已死」提示，再走既有
    // 会话流重连 + 权威历史探活（两者都成功才算真正恢复）。
    clearStreamStall();
    retryConnection();
    if (!isResponding && selectedThread?.transport === "error") {
      void recoverSessionHistory();
    }
  }
  // 顶栏「刷新当前会话」：历史 / 运行时状态 / 列表投影一次重拉（口径见 hook 注释）。
  const { refreshSession: handleRefreshSession, sessionRefreshing } =
    useSessionRefresh({
      recoverHistory: recoverSessionHistory,
      refreshRuntimeSessions: handleRefreshRuntimeSessions,
      refreshRuntimeState: refreshSessionRuntimeState,
      responding: currentSessionResponding,
    });
  // 轨迹视图的「加载更早」入口（尾部优先窗口）：稳定引用的 memo 收口在 hooks/workspace。
  const trajectoryEarlier = useTrajectoryEarlierEntry(trajectoryReplay);
  const {
    backtrackDialog,
    backtrackError,
    backtrackNotice,
    backtrackPendingMessageId,
    backtrackNavigationActive,
    backtrackSelectedMessageId,
    backtrackToMessage,
    canBacktrack,
    closeBacktrackDialog,
    confirmBacktrack,
    selectBacktrackNavigationMessage,
    setBacktrackEditPrompt,
    setBacktrackMode,
    setBacktrackPrefill,
  } = useSessionBacktrack({
    applySessionHistoryToThread,
    isResponding,
    selectedThread,
    setDraft,
    setThreads,
    draft,
  });

  return {
    ...turn,
    pendingSessionSwitch,
    cancelSessionSwitch,
    handleConfirmSessionSwitch,
    handleSelectThreadWithTrajectoryReset,
    handleStopRuntimeSession,
    sessionActivity,
    sessionRuntimeNotices,
    handleOpenRuntimeNoticeSession,
    threadBySessionId,
    connectionStatusForUi,
    handleRefreshSession,
    handleRetryConnection,
    sessionRefreshing,
    trajectoryEarlier,
    backtrackDialog,
    backtrackError,
    backtrackNotice,
    backtrackPendingMessageId,
    backtrackNavigationActive,
    backtrackSelectedMessageId,
    backtrackToMessage,
    canBacktrack,
    closeBacktrackDialog,
    confirmBacktrack,
    setBacktrackEditPrompt,
    setBacktrackMode,
    setBacktrackPrefill,
    selectBacktrackNavigationMessage,
  };
}
