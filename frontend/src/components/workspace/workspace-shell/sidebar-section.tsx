// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Dispatch, type SetStateAction } from "react";

import { type SettingsSectionId } from "@/components/workspace/settings";
import { WorkspaceSidebar } from "@/components/workspace/workspace-sidebar";
import { type WorkspaceDensity } from "@/core/settings";

import { type WorkspaceShellProps } from "@/components/workspace/workspace-shell/types";

type WorkspaceSidebarSectionProps = Pick<
  WorkspaceShellProps,
  | "onAddWorkspaceDirectory"
  | "onArchiveRuntimeSession"
  | "onCreateSessionInDirectory"
  | "onDeleteRuntimeSession"
  | "onForkRuntimeSession"
  | "onRefreshRuntimeTeams"
  | "onRemoveWorkspaceDirectory"
  | "onRenameRuntimeSession"
  | "onRenameWorkspaceDirectory"
  | "onRestoreRuntimeSession"
  | "onSelectRuntimeSessionUser"
  | "onSelectThread"
  | "sessionActivity"
  | "runtimeSessionDefaultUserId"
  | "runtimeSessionUsers"
  | "runtimeSessionUsersError"
  | "runtimeSessionUsersLoading"
  | "runtimeSessions"
  | "runtimeSessionsError"
  | "runtimeSessionsLoading"
  | "runtimeSessionsRefreshing"
  | "runtimeSessionsSummary"
  | "runtimeTeamSummaries"
  | "runtimeTeams"
  | "runtimeTeamsError"
  | "runtimeTeamsLoading"
  | "runtimeTeamsRefreshing"
  | "selectedRuntimeSessionUserId"
  | "selectedThread"
  | "threads"
  | "workspaceDirectories"
  | "workspaceDirectoriesError"
  | "workspaceDirectoriesLoading"
  | "workspaceDirectoriesRefreshing"
> & {
  density: WorkspaceDensity;
  mobileOpen: boolean;
  openSettings: (section?: SettingsSectionId) => void;
  setMobileSidebarOpen: Dispatch<SetStateAction<boolean>>;
};

export function WorkspaceSidebarSection({
  onAddWorkspaceDirectory,
  onArchiveRuntimeSession,
  onCreateSessionInDirectory,
  onDeleteRuntimeSession,
  onForkRuntimeSession,
  onRefreshRuntimeTeams,
  onRemoveWorkspaceDirectory,
  onRenameRuntimeSession,
  onRenameWorkspaceDirectory,
  onRestoreRuntimeSession,
  onSelectRuntimeSessionUser,
  onSelectThread,
  sessionActivity,
  runtimeSessionDefaultUserId,
  runtimeSessionUsers,
  runtimeSessionUsersError,
  runtimeSessionUsersLoading,
  runtimeSessions,
  runtimeSessionsError,
  runtimeSessionsLoading,
  runtimeSessionsRefreshing,
  runtimeSessionsSummary,
  runtimeTeamSummaries,
  runtimeTeams,
  runtimeTeamsError,
  runtimeTeamsLoading,
  runtimeTeamsRefreshing,
  selectedRuntimeSessionUserId,
  selectedThread,
  threads,
  workspaceDirectories,
  workspaceDirectoriesError,
  workspaceDirectoriesLoading,
  workspaceDirectoriesRefreshing,
  density,
  mobileOpen,
  openSettings,
  setMobileSidebarOpen,
}: WorkspaceSidebarSectionProps) {
  return (
    <WorkspaceSidebar
      density={density}
      mobileOpen={mobileOpen}
      onCloseMobile={() => setMobileSidebarOpen(false)}
      onOpenSettings={() => {
        setMobileSidebarOpen(false);
        openSettings("workspace");
      }}
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
      selectedRuntimeSessionUserId={selectedRuntimeSessionUserId}
      onSelectRuntimeSessionUser={onSelectRuntimeSessionUser}
      onRefreshRuntimeTeams={onRefreshRuntimeTeams}
      workspaceDirectories={workspaceDirectories}
      workspaceDirectoriesError={workspaceDirectoriesError}
      workspaceDirectoriesLoading={workspaceDirectoriesLoading}
      workspaceDirectoriesRefreshing={workspaceDirectoriesRefreshing}
      onAddWorkspaceDirectory={onAddWorkspaceDirectory}
      onArchiveRuntimeSession={onArchiveRuntimeSession}
      onDeleteRuntimeSession={onDeleteRuntimeSession}
      onForkRuntimeSession={onForkRuntimeSession}
      onRenameWorkspaceDirectory={onRenameWorkspaceDirectory}
      onRestoreRuntimeSession={onRestoreRuntimeSession}
      onRemoveWorkspaceDirectory={onRemoveWorkspaceDirectory}
      onCreateSessionInDirectory={onCreateSessionInDirectory}
      onRenameRuntimeSession={onRenameRuntimeSession}
      threads={threads}
      selectedThreadId={selectedThread.id}
      onSelectThread={(threadId) => {
        setMobileSidebarOpen(false);
        onSelectThread(threadId);
      }}
      sessionActivity={sessionActivity}
    />
  );
}
