import { useMemo } from "react";

import { SessionSwitchConfirmDialog } from "@/components/workspace/session-switch-confirm-dialog";
import { SessionRuntimeNotices } from "@/components/workspace/session-runtime-notices";
import { WorkspaceShell } from "@/components/workspace/workspace-shell";
import { useRuntimeTeamsData } from "@/hooks/workspace/use-runtime-teams-data";
import { useRuntimeSessionsData } from "@/hooks/workspace/use-runtime-sessions-data";
import { useRuntimeWorkspaceDirectories } from "@/hooks/workspace/use-runtime-workspace-directories";
import { useSessionBacktrack } from "@/hooks/workspace/use-session-backtrack";
import { useSessionBranch } from "@/hooks/workspace/use-session-branch";
import { useSessionHistorySync } from "@/hooks/workspace/use-session-history-sync";
import { useSessionRefresh } from "@/hooks/workspace/use-session-refresh";
import { usePendingInteractions } from "@/hooks/workspace/use-pending-interactions";
import { useRuntimePlanMode } from "@/hooks/workspace/use-runtime-plan-mode";
import { useSessionRuntimeState } from "@/hooks/workspace/use-session-runtime-state";
import { useWorkspaceMultiSessionRuntime } from "@/hooks/workspace/use-workspace-multi-session-runtime";
import { useWorkspaceSessionSwitchGuard } from "@/hooks/workspace/use-workspace-session-switch-guard";
import { useTrajectoryRecovery } from "@/hooks/workspace/use-trajectory-recovery";
import { useWorkspaceTrajectoryStore } from "@/hooks/workspace/use-trajectory-store-pool";
import { useWorkspaceAgentChatTurn } from "@/hooks/workspace/use-workspace-agent-chat-turn";
import { useWorkspaceLive } from "@/hooks/workspace/use-workspace-live";
import { useWorkspaceSessionActions } from "@/hooks/workspace/use-workspace-session-actions";
import { useWorkspaceThreadSelection } from "@/hooks/workspace/use-workspace-thread-selection";
import { getErrorMessage } from "@/hooks/workspace/thread-runtime";
import { withTransportDegradation } from "@/lib/connection-status";
import {
  resetStoredRuntimeClientId,
  useRuntimeClientIdentity,
} from "@/lib/runtime-client";
import { normalizeSessionId } from "@/lib/session-id";
import {
  applySessionHistoryToThread,
  createRuntimeDeltaCoordinator,
} from "@/lib/workspace-thread-state";
import { useParams } from "react-router-dom";

export function WorkspacePage() {
  const runtimeClient = useRuntimeClientIdentity();
  const { sessionId: routeSessionId } = useParams<{ sessionId?: string }>();
  const runtimeDeltaCoordinator = useMemo(
    () => createRuntimeDeltaCoordinator(),
    [],
  );

  function handleResetRuntimeClientIdentity() {
    if (typeof window === "undefined") {
      return;
    }

    resetStoredRuntimeClientId(window.localStorage);
    window.location.assign("/workspace/chats/new");
  }

  const {
    refreshRuntimeTeams: handleRefreshRuntimeTeams,
    runtimeTeamSummaries,
    runtimeTeams,
    runtimeTeamsError,
    runtimeTeamsLoading,
    runtimeTeamsRefreshing,
  } = useRuntimeTeamsData();
  const {
    refreshRuntimeSessions: handleRefreshRuntimeSessions,
    runtimeSessions,
    runtimeSessionsError,
    runtimeSessionsLoading,
    runtimeSessionsRefreshing,
    runtimeSessionsSummary,
    runtimeSessionUsers,
    selectedRuntimeSessionUserId,
  } = useRuntimeSessionsData({
    pinnedSessionId: routeSessionId,
    userId: runtimeClient.userId,
  });
  const {
    directories: workspaceDirectories,
    loading: workspaceDirectoriesLoading,
    refreshing: workspaceDirectoriesRefreshing,
    error: workspaceDirectoriesError,
    refresh: refreshWorkspaceDirectories,
    addDirectory: addWorkspaceDirectory,
    renameDirectory: renameWorkspaceDirectory,
    removeDirectory: removeWorkspaceDirectory,
  } = useRuntimeWorkspaceDirectories();
  const {
    onSelectArtifact: handleSelectArtifact,
    onSelectThread: handleSelectThread,
    selectedArtifact,
    selectedArtifactId,
    selectedThread,
    setSelectedArtifactId,
    setThreads,
    threads,
  } = useWorkspaceThreadSelection({
    initialThreads: [],
    runtimeSessions,
  });
  // Batch 1：轨迹 store 池——切换会话不再 reset（切回不重放），快照随会话保留。
  const { pool: trajectoryStorePool, store: trajectoryStore } = useWorkspaceTrajectoryStore(selectedThread);
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

  const { earlierLoader, recoverSessionHistory } = useSessionHistorySync({
    applySessionHistoryToThread,
    isResponding,
    // 回滚（检查点还原 / 会话回溯）会重写服务端历史，事件到达后必须重新同步消息列表。
    lastRuntimeEventType: selectedThread?.lastRuntimeEventType,
    runtimeEventCount: selectedThread?.runtimeEventCount,
    selectedThread,
    setThreads,
  });
  // P1-7：计划评审与 artifact 面板现有决策入口同源（同一 hook / 同一重载规则），
  // 仅把呈现位收敛到 composer 上沿，避免出现第二套 pending 判定。
  const {
    notesDraft: planNotesDraft,
    onNotesDraftChange: onPlanNotesChange,
    plan: runtimePlanMode,
    planActionPending,
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
  // P1-7：用户主动停止 → 未决审批 / 提问立即收敛，不等待（可能缺席的）中断事件，
  // 避免停止后卡片仍挂在 composer 上沿。
  function handleStopResponding() {
    convergePendingInteractions("session_interrupted");
    if (!activeTurnId) {
      // P4-刷新续传：刷新后的页面没有本地请求可 abort（停止按钮由会话级
      // currentSessionResponding 驱动），必须显式把 interrupt 投给服务端；
      // 本地回合在跑时不走这里——chat-turn hook 已带本地回合身份投递，避免重复。
      void stopResumedTurn();
    }
    stopResponding();
  }
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
  const {
    connectionStatus,
    currentSessionResponding,
    retryConnection,
    stopResumedTurn,
  } = useWorkspaceLive({
    deltaCoordinator: runtimeDeltaCoordinator,
    localResponding: isResponding,
    localTurnId: activeTurnId,
    onRuntimeEvent: applyPendingInteractionEvent,
    refreshRuntimeState: refreshSessionRuntimeState,
    selectedThread,
    sessionActiveTurn,
    sessionId: selectedThread?.sessionId,
    setThreads,
    trajectoryReady: trajectoryReplay.ready,
    trajectoryStore,
  });
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
    sessionRuntimeNotices,
    threadBySessionId,
  } = useWorkspaceMultiSessionRuntime({
    activeSessionKeys,
    applyPendingInteractionEvent,
    openThread: handleSelectThreadWithTrajectoryReset,
    pendingInteractionKind: pendingInteraction?.kind ?? null,
    responding: currentSessionResponding,
    selectedSessionId: selectedThread?.sessionId ?? null,
    stopSelectedSession: handleStopResponding,
    threads,
    trajectoryStorePool,
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
  // 轨迹视图的「加载更早」入口（尾部优先窗口）：对象引用保持稳定，避免下游
  // 每次 render 收到新 prop（`window` 状态不变时不必重建）。
  const trajectoryEarlier = useMemo(
    () => ({
      hasEarlier: trajectoryReplay.window.hasMore,
      loading: trajectoryReplay.loadingEarlier,
      onLoad: trajectoryReplay.loadEarlier,
    }),
    [
      trajectoryReplay.loadEarlier,
      trajectoryReplay.loadingEarlier,
      trajectoryReplay.window.hasMore,
    ],
  );
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

  if (!selectedThread) {
    return null;
  }

  const shell = (
    <WorkspaceShell
      threads={threads}
      runtimeTeams={runtimeTeams}
      runtimeTeamsError={runtimeTeamsError}
      runtimeTeamsLoading={runtimeTeamsLoading}
      runtimeTeamsRefreshing={runtimeTeamsRefreshing}
      runtimeTeamSummaries={runtimeTeamSummaries}
      runtimeSessionsError={runtimeSessionsError}
      runtimeSessions={runtimeSessions}
      runtimeSessionsLoading={runtimeSessionsLoading}
      runtimeSessionsRefreshing={runtimeSessionsRefreshing}
      runtimeSessionsSummary={runtimeSessionsSummary}
      runtimeSessionUsers={runtimeSessionUsers}
      workspaceDirectories={workspaceDirectories}
      workspaceDirectoriesError={workspaceDirectoriesError}
      workspaceDirectoriesLoading={workspaceDirectoriesLoading}
      workspaceDirectoriesRefreshing={workspaceDirectoriesRefreshing}
      onRefreshWorkspaceDirectories={refreshWorkspaceDirectories}
      onAddWorkspaceDirectory={addWorkspaceDirectory}
      onRenameWorkspaceDirectory={renameWorkspaceDirectory}
      onRemoveWorkspaceDirectory={removeWorkspaceDirectory}
      onRefreshSession={handleRefreshSession}
      onRetryConnection={handleRetryConnection}
      onCreateSessionInDirectory={handleCreateSessionInDirectory}
      onRenameRuntimeSession={handleRenameRuntimeSession}
      onMoveRuntimeSession={handleMoveRuntimeSession}
      onArchiveRuntimeSession={handleArchiveRuntimeSession}
      onRestoreRuntimeSession={handleRestoreRuntimeSession}
      onForkRuntimeSession={handleForkRuntimeSession}
      onBranchFromMessage={handleBranchFromMessage}
      branchPendingMessageId={branchPendingMessageId}
      branchError={branchError}
      onDeleteRuntimeSession={handleDeleteRuntimeSession}
      onStopRuntimeSession={handleStopRuntimeSession}
      sessionActivity={sessionActivity}
      runtimeClient={runtimeClient}
      selectedRuntimeSessionUserId={selectedRuntimeSessionUserId}
      selectedThread={selectedThread}
      sessionRefreshing={sessionRefreshing}
      selectedArtifact={selectedArtifact}
      selectedArtifactId={selectedArtifactId}
      composerAttachments={composerAttachments}
      connectionStatus={connectionStatusForUi}
      draft={draft}
      isResponding={currentSessionResponding}
      streamStalled={streamStalled}
      modelOptions={modelOptions}
      phase={phase}
      reasoningEffortDefault={reasoningEffortDefault}
      reasoningEffortError={reasoningEffortError}
      reasoningEffortOptions={reasoningEffortOptions}
      trajectoryStore={trajectoryStore}
      trajectoryEarlier={trajectoryEarlier}
      earlierLoader={earlierLoader}
      onDraftChange={setDraft}
      onModelChange={setSelectedModel}
      onProviderChange={setSelectedProvider}
      onReasoningEffortChange={setReasoningEffort}
      onSelectArtifact={handleSelectArtifact}
      onSelectThread={handleSelectThreadWithTrajectoryReset}
      onRefreshRuntimeTeams={handleRefreshRuntimeTeams}
      onRefreshRuntimeSessions={handleRefreshRuntimeSessions}
      onResetRuntimeClientIdentity={handleResetRuntimeClientIdentity}
      onStopResponding={handleStopResponding}
      onSubmit={submitPrompt}
      onBacktrackToMessage={backtrackToMessage}
      backtrackDialog={backtrackDialog}
      backtrackError={backtrackError}
      backtrackNotice={backtrackNotice}
      backtrackPendingMessageId={backtrackPendingMessageId}
      backtrackNavigationActive={backtrackNavigationActive}
      backtrackSelectedMessageId={backtrackSelectedMessageId}
      canBacktrack={canBacktrack}
      onCloseBacktrackDialog={closeBacktrackDialog}
      onConfirmBacktrack={confirmBacktrack}
      onBacktrackEditPromptChange={setBacktrackEditPrompt}
      onBacktrackModeChange={setBacktrackMode}
      onBacktrackPrefillChange={setBacktrackPrefill}
      onSelectBacktrackNavigationMessage={selectBacktrackNavigationMessage}
      providerOptions={providerOptions}
      runtimeModels={runtimeModels}
      runtimeModelsError={runtimeModelsError}
      runtimeModelsLoading={runtimeModelsLoading}
      runtimeSkills={runtimeSkills}
      runtimeSkillsError={runtimeSkillsError}
      runtimeSkillsLoading={runtimeSkillsLoading}
      selectedModel={selectedModel}
      selectedProvider={selectedProvider}
      selectedReasoningEffort={selectedReasoningEffort}
      pendingInteraction={pendingInteraction}
      onResolvePendingApproval={resolvePendingApproval}
      onAnswerPendingQuestion={answerPendingQuestion}
      planActionPending={planActionPending}
      planNotesDraft={planNotesDraft}
      onPlanNotesChange={onPlanNotesChange}
      onPlanDecision={submitPlanDecision}
    />
  );

  return (
    <>
      {shell}
      <SessionSwitchConfirmDialog
        open={pendingSessionSwitch !== null}
        sessionTitle={pendingSessionSwitch?.title ?? ""}
        onCancel={cancelSessionSwitch}
        onConfirm={handleConfirmSessionSwitch}
      />
      <SessionRuntimeNotices
        notices={sessionRuntimeNotices.notices}
        onDismiss={sessionRuntimeNotices.dismiss}
        onOpenSession={handleOpenRuntimeNoticeSession}
        resolveSessionTitle={(sessionId) =>
          threadBySessionId.get(normalizeSessionId(sessionId))?.title
        }
      />
    </>
  );
}
