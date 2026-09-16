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
  | "onMoveRuntimeSession"
  | "onRefreshRuntimeTeams"
  | "onRemoveWorkspaceDirectory"
  | "onRenameRuntimeSession"
  | "onRenameWorkspaceDirectory"
  | "onRestoreRuntimeSession"
  | "onSelectThread"
  | "sessionActivity"
  | "runtimeSessionUsers"
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
  /** 桌面侧栏收起状态与切换口（由工作区壳层持有，供网格列宽共用）。 */
  collapsed: boolean;
  density: WorkspaceDensity;
  mobileOpen: boolean;
  onCollapsedChange: (collapsed: boolean) => void;
  openSettings: (section?: SettingsSectionId) => void;
  setMobileSidebarOpen: Dispatch<SetStateAction<boolean>>;
};

export function WorkspaceSidebarSection({
  onAddWorkspaceDirectory,
  onArchiveRuntimeSession,
  onCreateSessionInDirectory,
  onDeleteRuntimeSession,
  onForkRuntimeSession,
  onMoveRuntimeSession,
  onRefreshRuntimeTeams,
  onRemoveWorkspaceDirectory,
  onRenameRuntimeSession,
  onRenameWorkspaceDirectory,
  onRestoreRuntimeSession,
  onSelectThread,
  sessionActivity,
  runtimeSessionUsers,
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
  collapsed,
  density,
  mobileOpen,
  onCollapsedChange,
  openSettings,
  setMobileSidebarOpen,
}: WorkspaceSidebarSectionProps) {
  return (
    <WorkspaceSidebar
      collapsed={collapsed}
      density={density}
      mobileOpen={mobileOpen}
      onCollapsedChange={onCollapsedChange}
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
      runtimeSessionUsers={runtimeSessionUsers}
      selectedRuntimeSessionUserId={selectedRuntimeSessionUserId}
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
      onMoveRuntimeSession={onMoveRuntimeSession}
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
