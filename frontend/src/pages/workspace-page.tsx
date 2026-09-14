import { useMemo, useState } from "react";

import { SessionSwitchConfirmDialog } from "@/components/workspace/session-switch-confirm-dialog";
import { WorkspaceShell } from "@/components/workspace/workspace-shell";
import { useRuntimeTeamsData } from "@/hooks/workspace/use-runtime-teams-data";
import { useRuntimeSessionsData } from "@/hooks/workspace/use-runtime-sessions-data";
import { useRuntimeWorkspaceDirectories } from "@/hooks/workspace/use-runtime-workspace-directories";
import { useSessionBacktrack } from "@/hooks/workspace/use-session-backtrack";
import { useSessionHistorySync } from "@/hooks/workspace/use-session-history-sync";
import { usePendingInteractions } from "@/hooks/workspace/use-pending-interactions";
import { useRuntimePlanMode } from "@/hooks/workspace/use-runtime-plan-mode";
import { useSessionRuntimeState } from "@/hooks/workspace/use-session-runtime-state";
import { useSessionRuntimeStream } from "@/hooks/workspace/use-session-runtime-stream";
import { useTrajectoryRecovery } from "@/hooks/workspace/use-trajectory-recovery";
import { useWorkspaceAgentChatTurn } from "@/hooks/workspace/use-workspace-agent-chat-turn";
import { useWorkspaceSessionActions } from "@/hooks/workspace/use-workspace-session-actions";
import {
  isThreadResponding,
  shouldConfirmThreadSwitch,
  useWorkspaceThreadSelection,
} from "@/hooks/workspace/use-workspace-thread-selection";
import {
  applyRuntimeDeltaToThread,
  applyRuntimeEventToThread,
  getErrorMessage,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
} from "@/hooks/workspace/thread-runtime";
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
import { trajectoryEventAction } from "@/lib/trajectory/recovery";
import { buildSidebarSessionActivity } from "@/components/workspace/workspace-sidebar/session-row-status";
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
    runtimeSessionDefaultUserId,
    runtimeSessionUsers,
    runtimeSessionUsersError,
    runtimeSessionUsersLoading,
    selectedRuntimeSessionUserId,
    selectRuntimeSessionUserId,
  } = useRuntimeSessionsData({
    pinnedSessionId: routeSessionId,
    userId: runtimeClient.userId,
  });
  const {
    directories: workspaceDirectories,
    loading: workspaceDirectoriesLoading,
    refreshing: workspaceDirectoriesRefreshing,
    error: workspaceDirectoriesError,
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
  const {
    activeTurnId,
    composerAttachments,
    draft,
    isResponding,
    modelOptions,
    phase,
    providerOptions,
    reasoningEffortDefault,
    reasoningEffortError,
    reasoningEffortOptions,
    runtimeModels,
    runtimeModelsError,
    runtimeModelsLoading,
    selectedModel,
    selectedProvider,
    selectedReasoningEffort,
    setDraft,
    setReasoningEffort,
    setSelectedModel,
    setSelectedProvider,
    stopResponding,
    submitPrompt,
    trajectoryStore,
  } = useWorkspaceAgentChatTurn({
    onSessionTouched: handleRefreshRuntimeSessions,
    selectedThread,
    setSelectedArtifactId,
    setThreads,
    deltaCoordinator: runtimeDeltaCoordinator,
    userId: selectedRuntimeSessionUserId || runtimeClient.userId,
    workspacePath: runtimeClient.workspacePath,
  });

  const [pendingSessionSwitch, setPendingSessionSwitch] = useState<{
    threadId: string;
    title: string;
  } | null>(null);

  // 只有「当前会话正在生成回复」时才需要二次确认：后台会话的 turn 会让全局
  // isResponding 保持 true，因此按会话归属（流式消息挂在哪个线程上）判断。
  const currentSessionResponding = isThreadResponding(selectedThread, activeTurnId);

  // 用户主动切换线程：轨迹快照 reset（同步于选择动作；新 turn 的 reset
  // 在提交 hook 内处理，避免导航渲染迟到的 effect reset 打断流事件收集）。
  function performSelectThread(threadId: string) {
    // 线程/会话切换：硬重置游标——新会话的事件日志独立自增，
    // 由 useTrajectoryRecovery 按新会话从 seq=1 重新回放。
    trajectoryStore.reset({ hard: true });
    handleSelectThread(threadId);
  }

  // 左侧会话列表点击其它会话：当前会话仍在生成回复时先弹确认对话框，确认后
  // 才真正切换（轨迹重置同样延后到确认之后，避免取消时已经丢掉当前会话的
  // 轨迹游标）；当前会话空闲时直接切换，不打断用户。
  function handleSelectThreadWithTrajectoryReset(threadId: string) {
    if (
      !shouldConfirmThreadSwitch(
        selectedThread?.id,
        threadId,
        currentSessionResponding,
      )
    ) {
      performSelectThread(threadId);
      return;
    }

    const targetThread = threads.find((thread) => thread.id === threadId);
    setPendingSessionSwitch({
      threadId,
      title: targetThread?.title ?? threadId,
    });
  }

  function handleConfirmSessionSwitch() {
    const pending = pendingSessionSwitch;
    setPendingSessionSwitch(null);
    if (pending) {
      performSelectThread(pending.threadId);
    }
  }

  // P1-9：会话行动作（重命名 / 归档 / 归档恢复 / Fork / 删除 / 目录内新建）由
  // hooks/workspace/use-workspace-session-actions.ts 收口（P0-2 A2 复检拆分）。
  const activeSessionId = normalizeSessionId(
    selectedThread?.sessionId || selectedThread?.id || routeSessionId || "",
  );
  const {
    archiveSession: handleArchiveRuntimeSession,
    createSessionInDirectory: handleCreateSessionInDirectory,
    deleteSession: handleDeleteRuntimeSession,
    forkSession: handleForkRuntimeSession,
    moveSession: handleMoveRuntimeSession,
    renameSession: handleRenameRuntimeSession,
    restoreSession: handleRestoreRuntimeSession,
  } = useWorkspaceSessionActions({
    activeSessionId,
    clientUserId: runtimeClient.userId,
    onResetTrajectory: () => trajectoryStore.reset({ hard: true }),
    refreshSessions: handleRefreshRuntimeSessions,
    runtimeSessions,
    selectedUserId: selectedRuntimeSessionUserId,
    setThreads,
  });

  useSessionHistorySync({
    applySessionHistoryToThread,
    isResponding,
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
  const { state: sessionRuntimeState } = useSessionRuntimeState(
    selectedThread?.sessionId,
  );
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
    stopResponding();
  }
  // P1-9：侧栏行状态只消费本地已知信号（当前会话的运行/待交互），
  // 后台会话缺信号时回落为快照状态，不伪造「在等我」。
  const sessionActivity = useMemo(
    () =>
      buildSidebarSessionActivity({
        sessionId: selectedThread?.sessionId,
        pendingInteractionKind: pendingInteraction?.kind ?? null,
        responding: currentSessionResponding,
      }),
    [
      currentSessionResponding,
      pendingInteraction?.kind,
      selectedThread?.sessionId,
    ],
  );
  const { connectionStatus, retryConnection } = useSessionRuntimeStream({
    applyRuntimeEventToThread,
    applyRuntimeDeltaToThread,
    getErrorMessage,
    getRuntimeEventSeq,
    mergeRuntimeEvent,
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
    onRuntimeEvent: applyPendingInteractionEvent,
    // 方案B：请求进行中才渲染 runtime/stream 的打字机增量（delta/reasoning/
    // image_progress）；回放/reload 只进事件快照，不误渲染历史增量。
    activeTurnId,
    deltaCoordinator: runtimeDeltaCoordinator,
    renderLiveDeltas: isResponding && Boolean(activeTurnId),
    selectedThread,
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
  useTrajectoryRecovery({
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
      runtimeSessionDefaultUserId={runtimeSessionDefaultUserId}
      runtimeSessionUsers={runtimeSessionUsers}
      runtimeSessionUsersError={runtimeSessionUsersError}
      runtimeSessionUsersLoading={runtimeSessionUsersLoading}
      workspaceDirectories={workspaceDirectories}
      workspaceDirectoriesError={workspaceDirectoriesError}
      workspaceDirectoriesLoading={workspaceDirectoriesLoading}
      workspaceDirectoriesRefreshing={workspaceDirectoriesRefreshing}
      onAddWorkspaceDirectory={addWorkspaceDirectory}
      onRenameWorkspaceDirectory={renameWorkspaceDirectory}
      onRemoveWorkspaceDirectory={removeWorkspaceDirectory}
      onRetryConnection={retryConnection}
      onCreateSessionInDirectory={handleCreateSessionInDirectory}
      onRenameRuntimeSession={handleRenameRuntimeSession}
      onMoveRuntimeSession={handleMoveRuntimeSession}
      onArchiveRuntimeSession={handleArchiveRuntimeSession}
      onRestoreRuntimeSession={handleRestoreRuntimeSession}
      onForkRuntimeSession={handleForkRuntimeSession}
      onDeleteRuntimeSession={handleDeleteRuntimeSession}
      sessionActivity={sessionActivity}
      runtimeClient={runtimeClient}
      selectedRuntimeSessionUserId={selectedRuntimeSessionUserId}
      selectedThread={selectedThread}
      selectedArtifact={selectedArtifact}
      selectedArtifactId={selectedArtifactId}
      composerAttachments={composerAttachments}
      connectionStatus={connectionStatusForUi}
      draft={draft}
      isResponding={isResponding}
      modelOptions={modelOptions}
      phase={phase}
      reasoningEffortDefault={reasoningEffortDefault}
      reasoningEffortError={reasoningEffortError}
      reasoningEffortOptions={reasoningEffortOptions}
      trajectoryStore={trajectoryStore}
      onDraftChange={setDraft}
      onModelChange={setSelectedModel}
      onProviderChange={setSelectedProvider}
      onReasoningEffortChange={setReasoningEffort}
      onSelectArtifact={handleSelectArtifact}
      onSelectThread={handleSelectThreadWithTrajectoryReset}
      onRefreshRuntimeTeams={handleRefreshRuntimeTeams}
      onSelectRuntimeSessionUser={selectRuntimeSessionUserId}
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
        onCancel={() => setPendingSessionSwitch(null)}
        onConfirm={handleConfirmSessionSwitch}
      />
    </>
  );
}
