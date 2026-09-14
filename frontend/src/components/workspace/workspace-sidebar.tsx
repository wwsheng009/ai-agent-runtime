// 由 P0-2 第 3 批第 29 项拆分：入口保留状态/派生/副作用与四段侧栏装配，渲染块下沉到 workspace-sidebar/。

import { useDeferredValue, useMemo, useState } from "react";
import { SessionSearchDialog } from "@/components/workspace/session-search-dialog";
import { mergeDirectoryGroups, type MergedDirectoryGroup } from "@/components/workspace/workspace-sidebar-shared";
import { WorkspaceDirectoryAddDialog } from "@/components/workspace/workspace-directory-add-dialog";
import { WorkspaceDirectoryDeleteDialog } from "@/components/workspace/workspace-directory-delete-dialog";
import { type Thread } from "@/data/mock";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";
import { useSessionStats } from "@/hooks/workspace/use-session-stats";
import { useSessionOrder } from "@/hooks/workspace/use-session-order";
import { WorkspaceSidebarChatsSection } from "@/components/workspace/workspace-sidebar/chats-section";
import { WorkspaceSidebarDirectoriesSection } from "@/components/workspace/workspace-sidebar/directories-section";
import { WorkspaceSidebarHeader } from "@/components/workspace/workspace-sidebar/sidebar-header";
import { WorkspaceSidebarRuntimeSection } from "@/components/workspace/workspace-sidebar/runtime-section";
import { WorkspaceSidebarRuntimeTeamsSurface } from "@/components/workspace/workspace-sidebar/runtime-teams-surface";
import { WorkspaceSidebarSessionsSection } from "@/components/workspace/workspace-sidebar/sessions-section";
import { useSidebarEffects } from "@/components/workspace/workspace-sidebar/use-sidebar-effects";
import { splitRuntimeSessionsByVisibility } from "@/components/workspace/workspace-sidebar/session-row-status";
import {
  type SidebarSectionId,
  type SidebarSectionState,
  type WorkspaceSidebarProps,
} from "@/components/workspace/workspace-sidebar/types";


export function WorkspaceSidebar({
  density,
  mobileOpen = false,
  onCloseMobile,
  onOpenSettings,
  runtimeTeams,
  runtimeTeamsError,
  runtimeTeamsLoading,
  runtimeTeamsRefreshing,
  runtimeTeamSummaries,
  runtimeSessionsError,
  runtimeSessions,
  runtimeSessionsLoading,
  runtimeSessionsRefreshing,
  runtimeSessionsSummary,
  runtimeSessionDefaultUserId,
  runtimeSessionUsers,
  runtimeSessionUsersError,
  runtimeSessionUsersLoading,
  selectedRuntimeSessionUserId,
  onRefreshRuntimeTeams,
  onSelectRuntimeSessionUser,
  workspaceDirectories,
  workspaceDirectoriesError,
  workspaceDirectoriesLoading,
  workspaceDirectoriesRefreshing,
  onAddWorkspaceDirectory,
  onRenameWorkspaceDirectory,
  onRemoveWorkspaceDirectory,
  onCreateSessionInDirectory,
  onRenameRuntimeSession,
  onArchiveRuntimeSession,
  onRestoreRuntimeSession,
  onForkRuntimeSession,
  onDeleteRuntimeSession,
  sessionActivity,
  threads,
  selectedThreadId,
  onSelectThread,
}: WorkspaceSidebarProps) {
  const { t } = useTranslation("workspace");
  const isCompact = density === "compact";
  const [query, setQuery] = useState("");
  const [runtimeTeamsDialogOpen, setRuntimeTeamsDialogOpen] = useState(false);
  /** P2-1A：服务端会话元数据检索弹层（与本地标题过滤并存）。 */
  const [sessionSearchOpen, setSessionSearchOpen] = useState(false);
  const [openSections, setOpenSections] = useState<SidebarSectionState>({
    directories: true,
    chats: true,
    sessions: true,
    runtime: false,
  });
  const [openSessionDirectories, setOpenSessionDirectories] = useState<
    Record<string, boolean>
  >({});
  const [directoryAddOpen, setDirectoryAddOpen] = useState(false);
  const [directoryDeleteTarget, setDirectoryDeleteTarget] = useState<{
    id: string;
    label: string;
    fullPath: string;
    sessionCount: number;
  } | null>(null);
  const [renamingDirectoryId, setRenamingDirectoryId] = useState<string | null>(
    null,
  );
  const [submittingDirectoryRename, setSubmittingDirectoryRename] =
    useState(false);
  const [renamingSessionId, setRenamingSessionId] = useState<string | null>(null);
  /** P1-9：归档会话默认隐藏，可一键展开/收起（本次会话内有效）。 */
  const [showArchivedSessions, setShowArchivedSessions] = useState(false);
  const [creatingSessionKey, setCreatingSessionKey] = useState<string | null>(
    null,
  );
  const [sidebarActionError, setSidebarActionError] = useState<string | null>(
    null,
  );
  const deferredQuery = useDeferredValue(query.trim().toLowerCase());
  const filteredThreads = deferredQuery
    ? threads.filter((thread) => {
        const haystack = [
          thread.title,
          thread.summary,
          thread.sessionId,
          thread.runtimeSource,
          thread.tags.join(" "),
        ]
          .filter(Boolean)
          .join(" ")
          .toLowerCase();
        return haystack.includes(deferredQuery);
      })
    : threads;
  const chatThreads = useMemo(
    () => filteredThreads.filter((thread) => !thread.sessionId),
    [filteredThreads],
  );
  const sessionThreads = useMemo(
    () =>
      [...filteredThreads]
        .filter((thread) => Boolean(thread.sessionId))
        .sort(
          (left, right) =>
            Date.parse(right.updatedAt) - Date.parse(left.updatedAt),
        ),
    [filteredThreads],
  );
  const sessionUserMenuItems = useMemo(() => {
    const seen = new Set<string>();
    const items = runtimeSessionUsers
      .map((user) => {
        const userId = user.user_id.trim();
        if (!userId || seen.has(userId)) {
          return null;
        }
        seen.add(userId);
        const displayName = user.display_name?.trim() || userId;
        const sessionCount = user.session_count ?? 0;
        const isDefaultUser = runtimeSessionDefaultUserId?.trim() === userId;
        return {
          userId,
          displayName,
          isDefaultUser,
          sessionCount,
        };
      })
      .filter(Boolean) as Array<{
        displayName: string;
        isDefaultUser: boolean;
        sessionCount: number;
        userId: string;
      }>;

    const selectedUserId = selectedRuntimeSessionUserId.trim();
    if (
      selectedUserId &&
      !seen.has(selectedUserId) &&
      runtimeSessionsSummary.totalCount > 0
    ) {
      items.unshift({
        userId: selectedUserId,
        displayName: selectedUserId,
        isDefaultUser: runtimeSessionDefaultUserId?.trim() === selectedUserId,
        sessionCount: runtimeSessionsSummary.totalCount,
      });
    }
    return items;
  }, [
    runtimeSessionDefaultUserId,
    runtimeSessionUsers,
    runtimeSessionsSummary.totalCount,
    selectedRuntimeSessionUserId,
  ]);
  const sessionVisibility = useMemo(
    () =>
      splitRuntimeSessionsByVisibility(runtimeSessions, {
        showArchived: showArchivedSessions,
      }),
    [runtimeSessions, showArchivedSessions],
  );
  const mergedDirectoryGroups = useMemo(
    () => mergeDirectoryGroups(workspaceDirectories, sessionVisibility.visible),
    [sessionVisibility.visible, workspaceDirectories],
  );
  // Per-user session browser: skip registered directories without sessions
  // so the friendly empty state survives directory-only registrations.
  const sessionDirectoryGroups = useMemo(
    () => mergedDirectoryGroups.filter((group) => group.sessions.length > 0),
    [mergedDirectoryGroups],
  );
  // P2-6：排序模式（设置域）+ 手动顺序账目（浏览器本地）；只重排分组内会话，不改分组口径。
  const { commitOrder: commitSessionOrder, mode: sessionOrderMode, orderFor, setMode: setSessionOrderMode } = useSessionOrder();
  const orderedSessionDirectoryGroups = useMemo(
    () => sessionDirectoryGroups.map((group) => ({ ...group, sessions: orderFor(group.key, group.sessions) })),
    [orderFor, sessionDirectoryGroups],
  );
  const sessionThreadById = useMemo(() => {
    const byId = new Map<string, Thread>();
    for (const thread of sessionThreads) {
      if (thread.sessionId) {
        byId.set(thread.sessionId, thread);
      }
      byId.set(thread.id, thread);
    }
    return byId;
  }, [sessionThreads]);

  // P2-1A：会话统计（`GET /sessions/stats`）与侧栏用户筛选同口径（同一 userId）。
  const sessionStats = useSessionStats(selectedRuntimeSessionUserId);

  const showSessionsSection =
    sessionThreads.length > 0 ||
    runtimeSessions.length > 0 ||
    runtimeSessionsLoading ||
    runtimeSessionsRefreshing ||
    Boolean(runtimeSessionsError) ||
    runtimeSessionUsersLoading ||
    Boolean(runtimeSessionUsersError) ||
    sessionUserMenuItems.length > 0 ||
    Boolean(deferredQuery);
  const showDirectoriesSection =
    workspaceDirectories.length > 0 ||
    mergedDirectoryGroups.length > 0 ||
    workspaceDirectoriesLoading ||
    workspaceDirectoriesRefreshing ||
    Boolean(workspaceDirectoriesError);
  const showChatsSection = chatThreads.length > 0 || Boolean(deferredQuery);
  const showSearch = threads.length > 0 || runtimeSessions.length > 0;

  useSidebarEffects({
    deferredQuery,
    mergedDirectoryGroups,
    mobileOpen,
    onCloseMobile,
    selectedThreadId,
    sessionThreadById,
    setOpenSections,
    setOpenSessionDirectories,
  });

  function toggleSection(section: SidebarSectionId) {
    setOpenSections((current) => ({
      ...current,
      [section]: !current[section],
    }));
  }

  function toggleSessionDirectory(directoryKey: string) {
    setOpenSessionDirectories((current) => ({
      ...current,
      [directoryKey]: !current[directoryKey],
    }));
  }

  function startDirectoryRename(group: MergedDirectoryGroup) {
    if (!group.directoryId) {
      return;
    }
    setSidebarActionError(null);
    setRenamingDirectoryId(group.directoryId);
  }

  function cancelDirectoryRename() {
    setRenamingDirectoryId(null);
  }

  async function commitDirectoryRename(nextName: string) {
    const directoryId = renamingDirectoryId;
    const trimmedName = nextName.trim();
    if (!directoryId || submittingDirectoryRename) {
      return;
    }
    if (!trimmedName) {
      cancelDirectoryRename();
      return;
    }
    setSubmittingDirectoryRename(true);
    try {
      await onRenameWorkspaceDirectory(directoryId, trimmedName);
      cancelDirectoryRename();
    } catch (renameError) {
      setSidebarActionError(
        renameError instanceof Error ? renameError.message : String(renameError),
      );
    } finally {
      setSubmittingDirectoryRename(false);
    }
  }

  async function handleCreateSessionInDirectory(group: MergedDirectoryGroup) {
    if (!group.fullPath || creatingSessionKey) {
      return;
    }
    setSidebarActionError(null);
    setCreatingSessionKey(group.key);
    try {
      await onCreateSessionInDirectory({
        path: group.fullPath,
        directoryId: group.directoryId,
        label: group.label,
      });
    } catch (createError) {
      setSidebarActionError(
        createError instanceof Error ? createError.message : String(createError),
      );
    } finally {
      setCreatingSessionKey(null);
    }
  }

  async function handleRenameSession(sessionId: string, title: string) {
    setRenamingSessionId(null);
    try {
      await onRenameRuntimeSession(sessionId, title);
    } catch (renameError) {
      setSidebarActionError(
        renameError instanceof Error ? renameError.message : String(renameError),
      );
    }
  }

  function startSessionRename(sessionId: string) {
    setSidebarActionError(null);
    setRenamingSessionId(sessionId);
  }

  return (
    <>
      {mobileOpen ? (
        <button
          type="button"
          className="fixed inset-0 z-40 bg-black/55 backdrop-blur-[2px] xl:hidden"
          onClick={onCloseMobile}
          aria-label={t("sidebar.closeNavigation")}
        />
      ) : null}
      <aside
        role={mobileOpen ? "dialog" : "navigation"}
        aria-modal={mobileOpen ? "true" : undefined}
        aria-label={t("sidebar.navigation")}
        className={cn(
          "fixed inset-y-0 left-0 z-50 flex h-full w-[min(20rem,calc(100vw-3rem))] min-h-0 flex-col overflow-hidden border-r border-border [background:var(--workspace-sidebar-bg)] shadow-[0_18px_48px_rgba(0,0,0,0.35)] transition-[transform,visibility] duration-200 xl:visible xl:static xl:z-auto xl:w-auto xl:translate-x-0 xl:shadow-none",
          mobileOpen
            ? "visible translate-x-0"
            : "invisible -translate-x-full",
        )}
      >
        <WorkspaceSidebarHeader
            isCompact={isCompact}
            onCloseMobile={onCloseMobile}
            onOpenSessionSearch={() => setSessionSearchOpen(true)}
            onOpenSettings={onOpenSettings}
            onRefreshRuntimeTeams={onRefreshRuntimeTeams}
            onSelectThread={onSelectThread}
            query={query}
            selectedThreadId={selectedThreadId}
            setQuery={setQuery}
            showSearch={showSearch}
            t={t}
          />

      <div
        className={cn(
          "min-h-0 flex-1 overflow-y-auto",
          isCompact ? "p-2.5" : "p-3",
        )}
      >
        <div className={cn(isCompact ? "space-y-3.5" : "space-y-4")}>
          <WorkspaceSidebarChatsSection
              chatThreads={chatThreads}
              deferredQuery={deferredQuery}
              onSelectThread={onSelectThread}
              openSections={openSections}
              selectedThreadId={selectedThreadId}
              showChatsSection={showChatsSection}
              t={t}
              toggleSection={toggleSection}
            />
          <WorkspaceSidebarDirectoriesSection
              cancelDirectoryRename={cancelDirectoryRename}
              commitDirectoryRename={commitDirectoryRename}
              creatingSessionKey={creatingSessionKey}
              handleCreateSessionInDirectory={handleCreateSessionInDirectory}
              handleRenameSession={handleRenameSession}
              mergedDirectoryGroups={mergedDirectoryGroups}
              onSelectThread={onSelectThread}
              openSections={openSections}
              openSessionDirectories={openSessionDirectories}
              renamingDirectoryId={renamingDirectoryId}
              renamingSessionId={renamingSessionId}
              selectedThreadId={selectedThreadId}
              sessionThreadById={sessionThreadById}
              setDirectoryAddOpen={setDirectoryAddOpen}
              setDirectoryDeleteTarget={setDirectoryDeleteTarget}
              setRenamingSessionId={setRenamingSessionId}
              setSidebarActionError={setSidebarActionError}
              showDirectoriesSection={showDirectoriesSection}
              sidebarActionError={sidebarActionError}
              startDirectoryRename={startDirectoryRename}
              startSessionRename={startSessionRename}
              toggleSection={toggleSection}
              toggleSessionDirectory={toggleSessionDirectory}
              workspaceDirectories={workspaceDirectories}
              workspaceDirectoriesError={workspaceDirectoriesError}
              workspaceDirectoriesLoading={workspaceDirectoriesLoading}
              workspaceDirectoriesRefreshing={workspaceDirectoriesRefreshing}
              t={t}
            />
          <WorkspaceSidebarSessionsSection
              deferredQuery={deferredQuery}
              handleRenameSession={handleRenameSession}
              hiddenArchivedCount={sessionVisibility.hiddenArchivedCount}
              onArchiveSession={onArchiveRuntimeSession}
              onDeleteSession={onDeleteRuntimeSession}
              onForkSession={onForkRuntimeSession}
              onRefreshSessionStats={sessionStats.refresh}
              onReorderSessions={commitSessionOrder}
              onRestoreSession={onRestoreRuntimeSession}
              onSelectSessionOrderMode={setSessionOrderMode}
              onToggleArchivedSessions={() =>
                setShowArchivedSessions((current) => !current)
              }
              onSelectRuntimeSessionUser={onSelectRuntimeSessionUser}
              onSelectThread={onSelectThread}
              openSections={openSections}
              openSessionDirectories={openSessionDirectories}
              renamingSessionId={renamingSessionId}
              runtimeSessionUsersError={runtimeSessionUsersError}
              runtimeSessionUsersLoading={runtimeSessionUsersLoading}
              selectedRuntimeSessionUserId={selectedRuntimeSessionUserId}
              selectedThreadId={selectedThreadId}
              sessionDirectoryGroups={orderedSessionDirectoryGroups}
              sessionOrderMode={sessionOrderMode}
              sessionStats={sessionStats.stats}
              sessionStatsError={sessionStats.error}
              sessionStatsStatus={sessionStats.status}
              sessionStatsUnavailable={sessionStats.unavailable}
              sessionThreadById={sessionThreadById}
              sessionThreads={sessionThreads}
              sessionUserMenuItems={sessionUserMenuItems}
              sessionActivity={sessionActivity}
              setRenamingSessionId={setRenamingSessionId}
              showArchivedSessions={showArchivedSessions}
              showSessionsSection={showSessionsSection}
              startSessionRename={startSessionRename}
              t={t}
              toggleSection={toggleSection}
              toggleSessionDirectory={toggleSessionDirectory}
            />
          <WorkspaceSidebarRuntimeSection
              onCloseMobile={onCloseMobile}
              openSections={openSections}
              runtimeSessionsError={runtimeSessionsError}
              runtimeSessionsLoading={runtimeSessionsLoading}
              runtimeSessionsRefreshing={runtimeSessionsRefreshing}
              runtimeSessionsSummary={runtimeSessionsSummary}
              runtimeTeams={runtimeTeams}
              setRuntimeTeamsDialogOpen={setRuntimeTeamsDialogOpen}
              t={t}
              threads={threads}
              toggleSection={toggleSection}
            />
        </div>
      </div>
      {runtimeTeamsDialogOpen ? (
      <WorkspaceSidebarRuntimeTeamsSurface
          error={runtimeTeamsError}
          isLoading={runtimeTeamsLoading}
          isRefreshing={runtimeTeamsRefreshing}
          onClose={() => setRuntimeTeamsDialogOpen(false)}
          onRefresh={onRefreshRuntimeTeams}
          open={runtimeTeamsDialogOpen}
          summaries={runtimeTeamSummaries}
          teams={runtimeTeams}
        />
      ) : null}
      <WorkspaceDirectoryAddDialog
        open={directoryAddOpen}
        onClose={() => setDirectoryAddOpen(false)}
        onAdd={onAddWorkspaceDirectory}
      />
      <WorkspaceDirectoryDeleteDialog
        open={Boolean(directoryDeleteTarget)}
        directory={directoryDeleteTarget}
        sessionCount={directoryDeleteTarget?.sessionCount ?? 0}
        onClose={() => setDirectoryDeleteTarget(null)}
        onConfirm={onRemoveWorkspaceDirectory}
      />
      <SessionSearchDialog
        defaultUserId={selectedRuntimeSessionUserId}
        // 打开状态变化即重挂载：每次打开都是全新的筛选与结果（不展示过期检索）。
        key={sessionSearchOpen ? "open" : "closed"}
        onClose={() => setSessionSearchOpen(false)}
        onSelectSession={(sessionId) => {
          setSessionSearchOpen(false);
          onSelectThread(sessionId);
        }}
        open={sessionSearchOpen}
        users={runtimeSessionUsers}
      />
      </aside>
    </>
  );
}
