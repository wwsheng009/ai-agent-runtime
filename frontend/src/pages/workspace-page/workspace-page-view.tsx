// P0-2：workspace-page.tsx 机械拆分后的页面组合层——仅保留 hook 编排与布局渲染，逻辑不变。

import { SessionSwitchConfirmDialog } from "@/components/workspace/session-switch-confirm-dialog";
import { SessionRuntimeNotices } from "@/components/workspace/session-runtime-notices";
import { WorkspaceShell } from "@/components/workspace/workspace-shell";
import { normalizeSessionId } from "@/lib/session-id";
import { WorkspaceThreadStoreContext } from "@/lib/thread-state/thread-store";

import { useWorkspacePageRuntime } from "./use-workspace-page-runtime";

export function WorkspacePage() {
  const {
    runtimeClient,
    handleResetRuntimeClientIdentity,
    handleRefreshRuntimeTeams,
    runtimeTeamSummaries,
    runtimeTeams,
    runtimeTeamsError,
    runtimeTeamsLoading,
    runtimeTeamsRefreshing,
    handleRefreshRuntimeSessions,
    runtimeSessions,
    runtimeSessionsError,
    runtimeSessionsLoading,
    runtimeSessionsRefreshing,
    runtimeSessionsSummary,
    runtimeSessionUsers,
    selectedRuntimeSessionUserId,
    workspaceDirectories,
    workspaceDirectoriesError,
    workspaceDirectoriesLoading,
    workspaceDirectoriesRefreshing,
    refreshWorkspaceDirectories,
    addWorkspaceDirectory,
    renameWorkspaceDirectory,
    removeWorkspaceDirectory,
    handleSelectArtifact,
    selectedArtifact,
    selectedArtifactId,
    selectedThread,
    threadStore,
    threads,
    trajectoryStore,
    composerAttachments,
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
    handleStopResponding,
    imageSubmitFeedback,
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
    resolvePendingApproval,
    answerPendingQuestion,
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
    branchError,
    branchPendingMessageId,
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
  } = useWorkspacePageRuntime();

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
      imageSubmitFeedback={imageSubmitFeedback}
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
      parkedTurn={parkedTurn}
      onResolvePendingApproval={resolvePendingApproval}
      onAnswerPendingQuestion={answerPendingQuestion}
      plan={runtimePlanMode}
      planActionPending={planActionPending}
      planNotesDraft={planNotesDraft}
      planStatusLabel={planStatusLabel}
      onPlanNotesChange={onPlanNotesChange}
      onPlanDecision={submitPlanDecision}
    />
  );

  return (
    <>
      <WorkspaceThreadStoreContext.Provider value={threadStore}>
        {shell}
      </WorkspaceThreadStoreContext.Provider>
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
        resolveSessionTitle={(sessionId) => threadBySessionId.get(normalizeSessionId(sessionId))?.title}
      />
    </>
  );
}
