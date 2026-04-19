import { WorkspaceShell } from "@/components/workspace/workspace-shell";
import { useRuntimeTeamsData } from "@/hooks/workspace/use-runtime-teams-data";
import { useRuntimeSessionsData } from "@/hooks/workspace/use-runtime-sessions-data";
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
    userId: runtimeClient.userId,
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
      runtimeSessionsLoading={runtimeSessionsLoading}
      runtimeSessionsRefreshing={runtimeSessionsRefreshing}
      runtimeSessionsSummary={runtimeSessionsSummary}
      runtimeClient={runtimeClient}
      selectedThread={selectedThread}
      selectedArtifact={selectedArtifact}
      selectedArtifactId={selectedArtifactId}
      draft={draft}
      isResponding={isResponding}
      modelOptions={modelOptions}
      onDraftChange={setDraft}
      onModelChange={setSelectedModel}
      onProviderChange={setSelectedProvider}
      onSelectArtifact={handleSelectArtifact}
      onSelectThread={handleSelectThread}
      onRefreshRuntimeTeams={handleRefreshRuntimeTeams}
      onResetRuntimeClientIdentity={handleResetRuntimeClientIdentity}
      onStopResponding={stopResponding}
      onSubmit={submitPrompt}
      providerOptions={providerOptions}
      runtimeModelsError={runtimeModelsError}
      runtimeModelsLoading={runtimeModelsLoading}
      selectedModel={selectedModel}
      selectedProvider={selectedProvider}
    />
  );
}
