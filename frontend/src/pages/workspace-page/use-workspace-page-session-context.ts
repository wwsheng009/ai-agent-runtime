// P0-2：workspace-page.tsx（原 517 非空行）机械拆分「会话数据与选择」区块——仅搬迁不改逻辑。

import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import { useRuntimeSessionsData } from "@/hooks/workspace/use-runtime-sessions-data";
import { useRuntimeTeamsData } from "@/hooks/workspace/use-runtime-teams-data";
import { useRuntimeWorkspaceDirectories } from "@/hooks/workspace/use-runtime-workspace-directories";
import {
  NEW_THREAD_ID,
  useWorkspaceThreadSelection,
} from "@/hooks/workspace/use-workspace-thread-selection";
import { useWorkspaceTrajectoryStore } from "@/hooks/workspace/use-trajectory-store-pool";
import {
  resetStoredRuntimeClientId,
  useRuntimeClientIdentity,
} from "@/lib/runtime-client";
import { createRuntimeDeltaCoordinator } from "@/lib/workspace-thread-state";

export function useWorkspacePageSessionContext() {
  const navigate = useNavigate();
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
    navigate("/workspace/chats/new");
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
    setThreadsLive,
    threadStore,
    threads,
  } = useWorkspaceThreadSelection({
    initialThreads: [],
    runtimeSessions,
  });
  const [selectedWorkspaceDirectoryId, setSelectedWorkspaceDirectoryId] =
    useState<string | null>(null);
  const selectedWorkspaceDirectory = useMemo(
    () =>
      workspaceDirectories.find(
        (directory) => directory.id === selectedWorkspaceDirectoryId,
      ) ?? null,
    [selectedWorkspaceDirectoryId, workspaceDirectories],
  );
  useEffect(() => {
    if (selectedThread?.id !== NEW_THREAD_ID) {
      setSelectedWorkspaceDirectoryId(null);
      return;
    }
    if (
      !workspaceDirectoriesLoading &&
      selectedWorkspaceDirectoryId &&
      !selectedWorkspaceDirectory
    ) {
      setSelectedWorkspaceDirectoryId(null);
    }
  }, [
    selectedThread?.id,
    selectedWorkspaceDirectoryId,
    selectedWorkspaceDirectory,
    workspaceDirectoriesLoading,
  ]);
  const handleSelectWorkspaceDirectory = useCallback(
    (directoryId: string | null) => {
      if (selectedThread?.id !== NEW_THREAD_ID) {
        return;
      }
      const exists =
        directoryId === null ||
        workspaceDirectories.some((directory) => directory.id === directoryId);
      if (exists) {
        setSelectedWorkspaceDirectoryId(directoryId);
      }
    },
    [selectedThread?.id, workspaceDirectories],
  );
  // Batch 1：轨迹 store 池——切换会话不再 reset（切回不重放），快照随会话保留。
  const { pool: trajectoryStorePool, store: trajectoryStore } =
    useWorkspaceTrajectoryStore(selectedThread);

  return {
    runtimeClient,
    routeSessionId,
    runtimeDeltaCoordinator,
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
    selectedWorkspaceDirectory,
    selectedWorkspaceDirectoryId,
    handleSelectWorkspaceDirectory,
    refreshWorkspaceDirectories,
    addWorkspaceDirectory,
    renameWorkspaceDirectory,
    removeWorkspaceDirectory,
    handleSelectArtifact,
    handleSelectThread,
    selectedArtifact,
    selectedArtifactId,
    selectedThread,
    setSelectedArtifactId,
    setThreads,
    setThreadsLive,
    threadStore,
    threads,
    trajectoryStorePool,
    trajectoryStore,
  };
}
