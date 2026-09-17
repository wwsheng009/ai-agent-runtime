// 由 P0-2 第 3 批第 29 项拆分：入口保留状态/派生/副作用与四段侧栏装配，渲染块下沉到 workspace-sidebar/。

import { useDeferredValue, useMemo, useState } from "react";
import { SessionSearchDialog } from "@/components/workspace/session-search-dialog";
import { WorkspaceDirectoryAddDialog } from "@/components/workspace/workspace-directory-add-dialog";
import { WorkspaceDirectoryDeleteDialog } from "@/components/workspace/workspace-directory-delete-dialog";
import { WorkspaceDirectoryManageDialog } from "@/components/workspace/workspace-directory-manage-dialog";
import { type Thread } from "@/data/mock";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";
import { useSessionStats } from "@/hooks/workspace/use-session-stats";
import { useSessionGroupView } from "@/hooks/workspace/use-session-group-view";
import { WorkspaceSidebarChatsSection } from "@/components/workspace/workspace-sidebar/chats-section";
import { WorkspaceSidebarCollapsedRail } from "@/components/workspace/workspace-sidebar/collapsed-rail";
import { WorkspaceSidebarDirectoriesSection } from "@/components/workspace/workspace-sidebar/directories-section";
import { WorkspaceSidebarHeader } from "@/components/workspace/workspace-sidebar/sidebar-header";
import { useDirectoryRegistry } from "@/components/workspace/workspace-sidebar/use-directory-registry";
import { WorkspaceSidebarRuntimeSection } from "@/components/workspace/workspace-sidebar/runtime-section";
import { WorkspaceSidebarRuntimeTeamsSurface } from "@/components/workspace/workspace-sidebar/runtime-teams-surface";
import { useSidebarEffects } from "@/components/workspace/workspace-sidebar/use-sidebar-effects";
import { SessionAttentionBar } from "@/components/workspace/workspace-sidebar/session-attention-bar";
import { collectAttentionSessionIds } from "@/components/workspace/workspace-sidebar/session-attention";
import { splitRuntimeSessionsByVisibility } from "@/components/workspace/workspace-sidebar/session-row-status";
import {
  type SidebarSectionId,
  type SidebarSectionState,
  type WorkspaceSidebarProps,
} from "@/components/workspace/workspace-sidebar/types";

export function WorkspaceSidebar({
  collapsed = false,
  density,
  onCollapsedChange,
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
  runtimeSessionUsers,
  selectedRuntimeSessionUserId,
  onRefreshRuntimeTeams,
  workspaceDirectories,
  workspaceDirectoriesError,
  workspaceDirectoriesLoading,
  workspaceDirectoriesRefreshing,
  onAddWorkspaceDirectory,
  onRenameWorkspaceDirectory,
  onRemoveWorkspaceDirectory,
  onCreateSessionInDirectory,
  onRenameRuntimeSession,
  onMoveRuntimeSession,
  onArchiveRuntimeSession,
  onRestoreRuntimeSession,
  onForkRuntimeSession,
  onDeleteRuntimeSession,
  onStopRuntimeSession,
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
    runtime: false,
  });
  const [openSessionDirectories, setOpenSessionDirectories] = useState<
    Record<string, boolean>
  >({});
  /** 合并为单段后同一会话只有一处编辑器，不再需要按分区下发（方案 §3.5-E）。 */
  const [renamingSessionId, setRenamingSessionId] = useState<string | null>(null);
  /** P1-9：归档会话默认隐藏，可一键展开/收起（本次会话内有效）。 */
  const [showArchivedSessions, setShowArchivedSessions] = useState(false);
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
  const sessionVisibility = useMemo(
    () =>
      splitRuntimeSessionsByVisibility(runtimeSessions, {
        showArchived: showArchivedSessions,
      }),
    [runtimeSessions, showArchivedSessions],
  );
  // P2-6：会话段视图（分组模式 + 排序账目 + 跨组移动的乐观归属/写回）整体下沉到 hook。
  const {
    canMoveSessionToGroup,
    commitOrder: commitSessionOrder,
    groupingMode: sessionGroupingMode,
    mergedDirectoryGroups,
    moveSessionToGroup,
    orderMode: sessionOrderMode,
    sessionGroups: orderedSessionDirectoryGroups,
    sessionMoveError,
    setGroupingMode: setSessionGroupingMode,
    setOrderMode: setSessionOrderMode,
  } = useSessionGroupView({
    directories: workspaceDirectories,
    onMoveRuntimeSession,
    runtimeSessions,
    t,
    visibleSessions: sessionVisibility.visible,
  });
  // Phase 2（合并方案 §3.5-D/E）：目录注册表的弹层开关 / 行内重命名 / 目录内新建会话下沉到 hook。
  const {
    cancelDirectoryRename,
    commitDirectoryRename,
    creatingSessionKey,
    directoryAddOpen,
    directoryDeleteTarget,
    directoryManageOpen,
    directorySessionCounts,
    handleCreateSessionFromManager,
    handleCreateSessionInDirectory,
    handleRequestRemoveDirectory,
    handleRegisterAndCreateSession,
    registerDirectoryFromManager,
    registerAndCreateSession,
    registeringDirectoryPath,
    renameDirectoryById,
    renamingDirectoryId,
    setDirectoryAddOpen,
    setDirectoryDeleteTarget,
    setDirectoryManageOpen,
    setSidebarActionError,
    sidebarActionError,
    startDirectoryRename,
    unregisteredDirectories,
  } = useDirectoryRegistry({
    mergedDirectoryGroups,
    onCreateSessionInDirectory,
    onRegisterWorkspaceDirectory: onAddWorkspaceDirectory,
    onRenameWorkspaceDirectory,
  });
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

  // Batch 3（§4.7.2）：待办聚合入口——「哪个会话在等我」。选中会话不参与：
  // 它的审批 / 提问已经在前台 composer 上直接呈现，聚合入口只做跨会话提醒。
  const attentionSessionIds = useMemo(
    () =>
      collectAttentionSessionIds(sessionActivity).filter((sessionId) => {
        const thread = sessionThreadById.get(sessionId);
        return thread ? thread.id !== selectedThreadId : true;
      }),
    [sessionActivity, sessionThreadById, selectedThreadId],
  );
  const attentionTargetThreadId = useMemo(() => {
    for (const sessionId of attentionSessionIds) {
      const thread = sessionThreadById.get(sessionId);
      if (thread) {
        return thread.id;
      }
    }
    return null;
  }, [attentionSessionIds, sessionThreadById]);

  // P2-1A：会话统计（`GET /sessions/stats`）与侧栏用户筛选同口径（同一 userId）。
  const sessionStats = useSessionStats(selectedRuntimeSessionUserId);

  // 合并方案 §3.4：原两段显示条件的**并集**——任一段原本会出现的场景，合并段都仍然出现。
  const showWorkspaceSection =
    sessionThreads.length > 0 ||
    runtimeSessions.length > 0 ||
    runtimeSessionsLoading ||
    runtimeSessionsRefreshing ||
    Boolean(runtimeSessionsError) ||
    workspaceDirectories.length > 0 ||
    mergedDirectoryGroups.length > 0 ||
    workspaceDirectoriesLoading ||
    workspaceDirectoriesRefreshing ||
    Boolean(workspaceDirectoriesError) ||
    Boolean(deferredQuery);
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

  /** 图标列里点分区：展开侧栏，并把该段切到展开态（不动用户原本打开的其他段）。 */
  function selectSectionFromRail(section: SidebarSectionId) {
    setOpenSections((current) =>
      current[section] ? current : { ...current, [section]: true },
    );
    onCollapsedChange?.(false);
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

  /** 合并为单段后不再区分发起分区，同一会话在整段内只有一处行内编辑器（方案 §3.5-E）。 */
  function startSessionRename(sessionId: string) {
    setSidebarActionError(null);
    setRenamingSessionId(sessionId);
  }

  function cancelSessionRename() {
    setRenamingSessionId(null);
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
        <div
          className={cn(
            "flex min-h-0 flex-1 flex-col",
            collapsed && "xl:hidden",
          )}
        >
        <WorkspaceSidebarHeader
            onCollapsedChange={onCollapsedChange}
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

      {attentionSessionIds.length > 0 ? (
        <div
          className={cn(
            "border-b border-border",
            isCompact ? "px-2.5 py-2" : "px-3 py-2",
          )}
        >
          <SessionAttentionBar
            onOpenSession={
              attentionTargetThreadId
                ? () => onSelectThread(attentionTargetThreadId)
                : undefined
            }
            sessionIds={attentionSessionIds}
          />
        </div>
      ) : null}

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
          {/* Phase 2（合并方案 §3.2）：原「工作目录」与「会话」两段在此合并为单段。 */}
          <WorkspaceSidebarDirectoriesSection
              canMoveSessionToGroup={canMoveSessionToGroup}
              cancelDirectoryRename={cancelDirectoryRename}
              cancelSessionRename={cancelSessionRename}
              commitDirectoryRename={commitDirectoryRename}
              creatingSessionKey={creatingSessionKey}
              deferredQuery={deferredQuery}
              handleCreateSessionInDirectory={handleCreateSessionInDirectory}
              handleRenameSession={handleRenameSession}
              hiddenArchivedCount={sessionVisibility.hiddenArchivedCount}
              mergedDirectoryGroups={mergedDirectoryGroups}
              onArchiveSession={onArchiveRuntimeSession}
              onDeleteSession={onDeleteRuntimeSession}
              onForkSession={onForkRuntimeSession}
              onMoveSessionToGroup={(sessionId, groupKey) => {
                void moveSessionToGroup(sessionId, groupKey);
              }}
              onRefreshSessionStats={sessionStats.refresh}
              onReorderSessions={commitSessionOrder}
              onRegisterAndCreateSession={handleRegisterAndCreateSession}
              onRequestManageDirectories={() => setDirectoryManageOpen(true)}
              onRestoreSession={onRestoreRuntimeSession}
              onSelectSessionGroupingMode={setSessionGroupingMode}
              onSelectSessionOrderMode={setSessionOrderMode}
              onSelectThread={onSelectThread}
              onStopSession={onStopRuntimeSession}
              onToggleArchivedSessions={() =>
                setShowArchivedSessions((current) => !current)
              }
              openSections={openSections}
              openSessionDirectories={openSessionDirectories}
              renamingDirectoryId={renamingDirectoryId}
              renamingSessionId={renamingSessionId}
              registeringDirectoryPath={registeringDirectoryPath}
              selectedThreadId={selectedThreadId}
              sessionActivity={sessionActivity}
              sessionDirectoryGroups={orderedSessionDirectoryGroups}
              sessionGroupingMode={sessionGroupingMode}
              sessionMoveError={sessionMoveError}
              sessionOrderMode={sessionOrderMode}
              sessionStats={sessionStats.stats}
              sessionStatsError={sessionStats.error}
              sessionStatsStatus={sessionStats.status}
              sessionStatsUnavailable={sessionStats.unavailable}
              sessionThreadById={sessionThreadById}
              sessionThreads={sessionThreads}
              setDirectoryAddOpen={setDirectoryAddOpen}
              setDirectoryDeleteTarget={setDirectoryDeleteTarget}
              setSidebarActionError={setSidebarActionError}
              showArchivedSessions={showArchivedSessions}
              showWorkspaceSection={showWorkspaceSection}
              sidebarActionError={sidebarActionError}
              startDirectoryRename={startDirectoryRename}
              startSessionRename={startSessionRename}
              t={t}
              toggleSection={toggleSection}
              toggleSessionDirectory={toggleSessionDirectory}
              workspaceDirectories={workspaceDirectories}
              workspaceDirectoriesError={workspaceDirectoriesError}
              workspaceDirectoriesLoading={workspaceDirectoriesLoading}
              workspaceDirectoriesRefreshing={workspaceDirectoriesRefreshing}
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
        </div>
      {collapsed ? (
        <WorkspaceSidebarCollapsedRail
          onCollapsedChange={onCollapsedChange}
          onOpenSessionSearch={() => setSessionSearchOpen(true)}
          onOpenSettings={onOpenSettings}
          onSelectSection={selectSectionFromRail}
          onSelectThread={onSelectThread}
          selectedThreadId={selectedThreadId}
          t={t}
        />
      ) : null}
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
      {/* Phase 2（合并方案 §3.5-D）：平铺模式没有组头，目录注册表管理由本弹层承载。 */}
      <WorkspaceDirectoryManageDialog
        open={directoryManageOpen}
        onClose={() => setDirectoryManageOpen(false)}
        directories={workspaceDirectories}
        sessionCounts={directorySessionCounts}
        unregisteredDirectories={unregisteredDirectories}
        onRegisterDirectory={registerDirectoryFromManager}
        onRegisterAndCreateSession={registerAndCreateSession}
        onRequestAdd={() => {
          setDirectoryManageOpen(false);
          setDirectoryAddOpen(true);
        }}
        onCreateSession={handleCreateSessionFromManager}
        onRenameDirectory={renameDirectoryById}
        onRequestRemove={handleRequestRemoveDirectory}
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
