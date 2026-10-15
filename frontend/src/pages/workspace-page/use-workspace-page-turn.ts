// P0-2：workspace-page.tsx 机械拆分「回合 / 交互 / 轨迹 / live」区块——仅搬迁不改逻辑。

import { useCallback } from "react";

import { usePendingInteractions } from "@/hooks/workspace/use-pending-interactions";
import { useRuntimePlanMode } from "@/hooks/workspace/use-runtime-plan-mode";
import { useSessionBranch } from "@/hooks/workspace/use-session-branch";
import { useSessionHistorySync } from "@/hooks/workspace/use-session-history-sync";
import { useSessionRuntimeState } from "@/hooks/workspace/use-session-runtime-state";
import { useStopResponding } from "@/hooks/workspace/use-stop-responding";
import { getErrorMessage } from "@/hooks/workspace/thread-runtime";
import { useTrajectoryRecovery } from "@/hooks/workspace/use-trajectory-recovery";
import { useWorkspaceAgentChatTurn } from "@/hooks/workspace/use-workspace-agent-chat-turn";
import { useWorkspaceLive } from "@/hooks/workspace/use-workspace-live";
import { useWorkspaceSessionActions } from "@/hooks/workspace/use-workspace-session-actions";
import { ingestJobOutputEvent } from "@/lib/job-output-stream/store";
import { normalizeSessionId } from "@/lib/session-id";
import { applySessionHistoryToThread } from "@/lib/workspace-thread-state";

import { useWorkspacePageSessionContext } from "./use-workspace-page-session-context";

export function useWorkspacePageTurn() {
  const session = useWorkspacePageSessionContext();
  const {
    handleRefreshRuntimeSessions,
    routeSessionId,
    runtimeClient,
    runtimeDeltaCoordinator,
    selectedRuntimeSessionUserId,
    selectedThread,
    setSelectedArtifactId,
    setThreads,
    setThreadsLive,
    trajectoryStore,
    trajectoryStorePool,
  } = session;
  const {
    activeSessionKeys,
    activeTurnId,
    composerAttachments,
    draft,
    isResponding,
    streamStalled,
    modelOptions,
    phase,
    providerOptions,
    reasoningEffortDefault,
    reasoningEffortError,
    reasoningEffortOptions,
    runtimeModels,
    runtimeModelsError,
    runtimeModelsLoading,
    runtimeSkills,
    runtimeSkillsError,
    runtimeSkillsLoading,
    selectedModel,
    selectedProvider,
    selectedReasoningEffort,
    setDraft,
    setReasoningEffort,
    setSelectedModel,
    setSelectedProvider,
    stopResponding,
    clearStreamStall,
    imageSubmitFeedback,
    submitPrompt,
  } = useWorkspaceAgentChatTurn({
    onSessionTouched: handleRefreshRuntimeSessions,
    selectedThread,
    setSelectedArtifactId,
    setThreads,
    deltaCoordinator: runtimeDeltaCoordinator,
    trajectoryStore,
    trajectoryStorePool,
    userId: selectedRuntimeSessionUserId || runtimeClient.userId,
    workspacePath: runtimeClient.workspacePath,
  });

  // P1-9：会话行动作（重命名 / 归档 / 归档恢复 / Fork / 删除 / 目录内新建）由
  // hooks/workspace/use-workspace-session-actions.ts 收口（P0-2 A2 复检拆分）。
  const activeSessionId = normalizeSessionId(
    selectedThread?.sessionId || selectedThread?.id || routeSessionId || "",
  );
  const {
    archiveSession: handleArchiveRuntimeSession,
    createSessionInDirectory: handleCreateSessionInDirectory,
    deleteSession: handleDeleteRuntimeSession,
    moveSession: handleMoveRuntimeSession,
    renameSession: handleRenameRuntimeSession,
    restoreSession: handleRestoreRuntimeSession,
  } = useWorkspaceSessionActions({
    activeSessionId,
    clientUserId: runtimeClient.userId,
    onResetTrajectory: () =>
      trajectoryStorePool.reset(activeSessionId, { hard: true }),
    refreshSessions: handleRefreshRuntimeSessions,
    selectedUserId: selectedRuntimeSessionUserId,
    setThreads,
  });

  // 批次 1（会话分支方案 §5.3）：分支编排 hook —— 侧栏整会话分支与消息级锚点分支同源
  // （同一个 POST /sessions/{id}/branch），pending / 失败提示也共用一套状态。
  const {
    branchError,
    branchFromMessage: requestSessionBranch,
    branchPendingMessageId,
    forkSession: handleForkRuntimeSession,
  } = useSessionBranch({
    clientUserId: runtimeClient.userId,
    onResetTrajectory: () =>
      trajectoryStorePool.reset(activeSessionId, { hard: true }),
    refreshSessions: handleRefreshRuntimeSessions,
    selectedUserId: selectedRuntimeSessionUserId,
  });

  function handleBranchFromMessage(messageId: string) {
    void requestSessionBranch(
      activeSessionId,
      selectedThread?.title ?? "",
      messageId,
    );
  }

  // P1-7：计划评审与 artifact 面板现有决策入口同源（同一 hook / 同一重载规则），
  // 仅把呈现位收敛到 composer 上沿，避免出现第二套 pending 判定。
  const {
    notesDraft: planNotesDraft,
    onNotesDraftChange: onPlanNotesChange,
    plan: runtimePlanMode,
    planActionPending,
    planStatusLabel,
    submitDecision: submitPlanDecision,
  } = useRuntimePlanMode({
    lastRuntimeEventType: selectedThread?.lastRuntimeEventType,
    runtimeEventCount: selectedThread?.runtimeEventCount,
    sessionId: selectedThread?.sessionId,
  });
  // P2-1A：运行时状态快照（会话切换拉取一次）——重载 / 重连后重建未决审批与提问。
  // P4-刷新续传：快照同时带 active_turn（本会话此刻在跑的回合），刷新后的新页面
  // 据此重新挂载回合身份（见 use-workspace-live）。
  const {
    state: sessionRuntimeState,
    activeTurn: sessionActiveTurn,
    refresh: refreshSessionRuntimeState,
  } = useSessionRuntimeState(selectedThread?.sessionId);
  // P1-7：审批 / 提问 / 计划评审统一生命周期（事件流归约 → 决定投递 → 结果回填）。
  const {
    answerQuestion: answerPendingQuestion,
    applyRuntimeEvent: applyPendingInteractionEvent,
    converge: convergePendingInteractions,
    pending: pendingInteraction,
    resolveApproval: resolvePendingApproval,
  } = usePendingInteractions({
    sessionId: selectedThread?.sessionId,
    plan: runtimePlanMode,
    runtimeState: sessionRuntimeState,
    getErrorMessage,
  });
  // P2：runtime 事件流里的 `job_output` 增量同时喂给逐任务输出流 store，供 jobs
  // 面板对展开的 live 任务做事件驱动的增量跟随（不再纯靠轮询）。
  const handleRuntimeEvent = useCallback(
    (event: Parameters<typeof applyPendingInteractionEvent>[0]) => {
      applyPendingInteractionEvent(event);
      ingestJobOutputEvent(event);
    },
    [applyPendingInteractionEvent],
  );
  // 尾部优先回放：先回放「最近一页」事件，再放行实时流建连。顺序很重要——
  // 若先建连（after=0），后端会把整份事件日志按 SSE dump 重放一遍，窗口化
  // 就失效了；见 use-trajectory-recovery。
  const trajectoryReplay = useTrajectoryRecovery({
    store: trajectoryStore,
    sessionId: selectedThread?.sessionId,
    onError: (failedSessionId, message) => {
      // 轨迹恢复失败 → 发起恢复的会话对应的 thread 标记为连接降级
      // （Topbar/composer 显示"运行时降级 / 需要恢复关注"提示），
      // 不再静默吞错。按发起时的 sessionId 匹配，避免竞态错标。
      const normalizedFailedSession =
        normalizeSessionId(failedSessionId) || failedSessionId;
      if (!normalizedFailedSession) return;
      setThreads((current) =>
        current.map((t) =>
          normalizeSessionId(t.sessionId || t.id) === normalizedFailedSession
            ? {
                ...t,
                transport: "error",
                lastError: `Trajectory recovery failed: ${message}`,
              }
            : t,
        ),
      );
    },
  });
  // P4-刷新续传：live 通道（续传回合认领 → 在途回合身份统一 → /runtime/stream 按
  // 游标重连并继续渲染增量）整链路收口在 use-workspace-live。
  const { connectionStatus, currentSessionResponding, parkedTurn, retryConnection, stopResumedTurn } =
    useWorkspaceLive({
      deltaCoordinator: runtimeDeltaCoordinator,
      localResponding: isResponding,
      localTurnId: activeTurnId,
      onRuntimeEvent: handleRuntimeEvent,
      refreshRuntimeState: refreshSessionRuntimeState,
      selectedThread,
      sessionActiveTurn,
      sessionId: selectedThread?.sessionId,
      setThreads,
      setThreadsLive,
      trajectoryReady: trajectoryReplay.ready,
      trajectoryStore,
    });
  // 权威历史同步排在 live 通道之后：降级态收敛（断流自愈）必须等**服务端回合**
  // 结束才允许对齐——chat POST 被掐断后本地立即空闲，而服务端回合仍在跑
  // （resume_on_disconnect），此时拉到的历史不含最终消息，过早对齐会留下截断又
  // 清掉降级标记。`currentSessionResponding` 是本会话「本地 ∪ 续传」在途回合，
  // 与 `isResponding` 取并集后，两种回合都收尾才真正空闲。
  const { earlierLoader, recoverSessionHistory } = useSessionHistorySync({
    applySessionHistoryToThread,
    isResponding: isResponding || currentSessionResponding,
    // 回滚（检查点还原 / 会话回溯）会重写服务端历史，事件到达后必须重新同步消息列表。
    lastRuntimeEventType: selectedThread?.lastRuntimeEventType,
    runtimeEventCount: selectedThread?.runtimeEventCount,
    selectedThread,
    setThreads,
  });
  // P1-7 + ESC 阶段 A：停止 = 收敛未决交互 +（刷新后）服务端 interrupt；Esc 与按钮等价。
  const handleStopResponding = useStopResponding({
    activeTurnId,
    convergePendingInteractions,
    currentSessionResponding,
    stopResponding,
    stopResumedTurn,
  });

  return {
    ...session,
    activeSessionKeys,
    composerAttachments,
    clearStreamStall,
    connectionStatus,
    currentSessionResponding,
    draft,
    earlierLoader,
    handleArchiveRuntimeSession,
    handleBranchFromMessage,
    handleCreateSessionInDirectory,
    handleDeleteRuntimeSession,
    handleForkRuntimeSession,
    handleMoveRuntimeSession,
    handleRenameRuntimeSession,
    handleRestoreRuntimeSession,
    handleRuntimeEvent,
    handleStopResponding,
    imageSubmitFeedback,
    isResponding,
    modelOptions,
    onPlanNotesChange,
    parkedTurn,
    pendingInteraction,
    phase,
    planActionPending,
    planNotesDraft,
    planStatusLabel,
    providerOptions,
    reasoningEffortDefault,
    reasoningEffortError,
    reasoningEffortOptions,
    recoverSessionHistory,
    refreshSessionRuntimeState,
    resolvePendingApproval,
    answerPendingQuestion,
    retryConnection,
    runtimeModels,
    runtimeModelsError,
    runtimeModelsLoading,
    runtimePlanMode,
    runtimeSkills,
    runtimeSkillsError,
    runtimeSkillsLoading,
    selectedModel,
    selectedProvider,
    selectedReasoningEffort,
    setDraft,
    setReasoningEffort,
    setSelectedModel,
    setSelectedProvider,
    streamStalled,
    submitPlanDecision,
    submitPrompt,
    trajectoryReplay,
    branchError,
    branchPendingMessageId,
  };
}
