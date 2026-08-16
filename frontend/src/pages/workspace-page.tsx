import { WorkspaceShell } from "@/components/workspace/workspace-shell";
import { useRuntimeTeamsData } from "@/hooks/workspace/use-runtime-teams-data";
import { useRuntimeSessionsData } from "@/hooks/workspace/use-runtime-sessions-data";
import { useSessionBacktrack } from "@/hooks/workspace/use-session-backtrack";
import { useSessionHistorySync } from "@/hooks/workspace/use-session-history-sync";
import { useSessionRuntimeStream } from "@/hooks/workspace/use-session-runtime-stream";
import { useWorkspaceAgentChatTurn } from "@/hooks/workspace/use-workspace-agent-chat-turn";
import { useWorkspaceThreadSelection } from "@/hooks/workspace/use-workspace-thread-selection";
import {
  applyRuntimeEventToThread,
  getErrorMessage,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
} from "@/hooks/workspace/thread-runtime";
import {
  resetStoredRuntimeClientId,
  useRuntimeClientIdentity,
} from "@/lib/runtime-client";
import {
  applySessionHistoryToThread,
} from "@/lib/workspace-thread-state";
import { useParams } from "react-router-dom";

export function WorkspacePage() {
  const runtimeClient = useRuntimeClientIdentity();
  const { sessionId: routeSessionId } = useParams<{ sessionId?: string }>();

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
    draft,
    isResponding,
    modelOptions,
    phase,
    providerOptions,
    runtimeModelsError,
    runtimeModelsLoading,
    selectedModel,
    selectedProvider,
    setDraft,
    setSelectedModel,
    setSelectedProvider,
    stopResponding,
    submitPrompt,
  } = useWorkspaceAgentChatTurn({
    onSessionTouched: handleRefreshRuntimeSessions,
    selectedThread,
    setSelectedArtifactId,
    setThreads,
    userId: selectedRuntimeSessionUserId || runtimeClient.userId,
    workspacePath: runtimeClient.workspacePath,
  });

  useSessionHistorySync({
    applySessionHistoryToThread,
    isResponding,
    selectedThread,
    setThreads,
  });
  useSessionRuntimeStream({
    applyRuntimeEventToThread,
    getErrorMessage,
    getRuntimeEventSeq,
    mergeRuntimeEvent,
    selectedThread,
    setThreads,
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

  return (
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
      runtimeClient={runtimeClient}
      selectedRuntimeSessionUserId={selectedRuntimeSessionUserId}
      selectedThread={selectedThread}
      selectedArtifact={selectedArtifact}
      selectedArtifactId={selectedArtifactId}
      draft={draft}
      isResponding={isResponding}
      modelOptions={modelOptions}
      phase={phase}
      onDraftChange={setDraft}
      onModelChange={setSelectedModel}
      onProviderChange={setSelectedProvider}
      onSelectArtifact={handleSelectArtifact}
      onSelectThread={handleSelectThread}
      onRefreshRuntimeTeams={handleRefreshRuntimeTeams}
      onSelectRuntimeSessionUser={selectRuntimeSessionUserId}
      onResetRuntimeClientIdentity={handleResetRuntimeClientIdentity}
      onStopResponding={stopResponding}
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
      runtimeModelsError={runtimeModelsError}
      runtimeModelsLoading={runtimeModelsLoading}
      selectedModel={selectedModel}
      selectedProvider={selectedProvider}
    />
  );
}
