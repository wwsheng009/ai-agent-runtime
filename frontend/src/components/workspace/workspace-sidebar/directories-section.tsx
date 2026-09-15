// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2）。
// Phase 2（合并方案 §3.6）：本件是**合并后的段主体**——原「工作目录」段与「会话」段合并为
// 一棵「目录会话树」：段壳沿用 directories（标题「工作目录」+ 会话数徽标），段内的工具条、
// 会话行、目录组头全部来自 Phase 1 抽出的公共件。
// 能力落位见方案 §3.3：目录注册表管理挂组头 hover（平铺模式走段内「管理目录」弹层，§3.5-D），
// 会话浏览能力（用户 / 统计 / 排序 / 分组 / 归档 / 组内折叠 / 行菜单 / 拖拽 / 谱系）整体迁入本段。

import {
  FolderIcon,
  FolderPlusIcon,
  LoaderCircleIcon,
} from "lucide-react";
import { type Dispatch, type SetStateAction } from "react";
import { type TFunction } from "i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import type { SessionStatsStatus } from "@/hooks/workspace/use-session-stats";
import { type SessionGroupingMode } from "@/lib/workspace/session-grouping";
import { type SessionOrderMode } from "@/lib/workspace/session-order";
import { type RuntimeWorkspaceDirectory } from "@/lib/runtime-api";
import { type RuntimeSessionStats } from "@/types/runtime";

import { appendEmptyRegisteredGroups } from "../workspace-sidebar-shared";
import { WorkspaceSidebarDirectoryGroupActions } from "./directory-group-actions";
import { WorkspaceSidebarDirectoryGroupHeader } from "./directory-group-header";
import { SidebarSection } from "./section-shell";
import { InlineRenameInput } from "./session-item";
import { WorkspaceSidebarSessionBrowserToolbar } from "./session-browser-toolbar";
import { WorkspaceSidebarSessionGroupToggle } from "./session-group-toggle";
import { WorkspaceSidebarSessionRow } from "./session-row";
import { type SidebarSessionActivity } from "./session-row-status";
import { type WorkspaceSidebarSessionUserMenuItem } from "./session-user-menu";
import { useSidebarSessionDrag } from "./use-session-drag-reorder";
import { useSessionGroupVisibility } from "./use-session-group-visibility";
import {
  type SidebarDirectoryGroup,
  type SidebarSectionId,
  type SidebarSectionState,
  type SidebarThread,
} from "./types";

/** 移除目录的确认目标（由接线层的删除确认弹窗消费）。 */
export type SidebarDirectoryDeleteTarget = {
  id: string;
  label: string;
  fullPath: string;
  sessionCount: number;
};

type WorkspaceSidebarDirectoriesSectionProps = {
  // ── 目录注册表管理（原工作目录段） ──────────────────────────────
  cancelDirectoryRename: () => void;
  commitDirectoryRename: (nextName: string) => Promise<void>;
  creatingSessionKey: string | null;
  handleCreateSessionInDirectory: (
    group: SidebarDirectoryGroup,
  ) => Promise<void>;
  mergedDirectoryGroups: SidebarDirectoryGroup[];
  renamingDirectoryId: string | null;
  setDirectoryAddOpen: Dispatch<SetStateAction<boolean>>;
  setDirectoryDeleteTarget: Dispatch<
    SetStateAction<SidebarDirectoryDeleteTarget | null>
  >;
  startDirectoryRename: (group: SidebarDirectoryGroup) => void;
  /** 平铺模式下的目录管理入口（段内工具条按钮 → 管理弹层）。 */
  onRequestManageDirectories: () => void;
  workspaceDirectories: RuntimeWorkspaceDirectory[];
  workspaceDirectoriesError: string | null;
  workspaceDirectoriesLoading: boolean;
  workspaceDirectoriesRefreshing?: boolean;
  // ── 会话浏览（原会话段） ───────────────────────────────────────
  cancelSessionRename: () => void;
  canMoveSessionToGroup: (groupKey: string) => boolean;
  deferredQuery: string;
  handleRenameSession: (sessionId: string, title: string) => Promise<void>;
  hiddenArchivedCount: number;
  onArchiveSession?: (sessionId: string) => void;
  onDeleteSession?: (sessionId: string) => void;
  onForkSession?: (sessionId: string, sourceTitle: string) => void;
  onMoveSessionToGroup: (sessionId: string, groupKey: string) => void;
  onRefreshSessionStats: () => void;
  onReorderSessions: (accountKey: string, order: readonly string[]) => void;
  onRestoreSession?: (sessionId: string) => void;
  onSelectSessionGroupingMode: (mode: SessionGroupingMode) => void;
  onSelectSessionOrderMode: (mode: SessionOrderMode) => void;
  onToggleArchivedSessions: () => void;
  onSelectRuntimeSessionUser: (userId: string) => void;
  sessionActivity?: Record<string, SidebarSessionActivity>;
  sessionDirectoryGroups: SidebarDirectoryGroup[];
  sessionGroupingMode: SessionGroupingMode;
  /** 跨组移动失败的就地提示（失败即回滚，提示挂在合并段而不是独立目录段）。 */
  sessionMoveError: string | null;
  sessionOrderMode: SessionOrderMode;
  sessionStats: RuntimeSessionStats | null;
  sessionStatsError: unknown;
  sessionStatsStatus: SessionStatsStatus;
  sessionStatsUnavailable: boolean;
  sessionThreadById: Map<string, SidebarThread>;
  sessionThreads: SidebarThread[];
  sessionUserMenuItems: WorkspaceSidebarSessionUserMenuItem[];
  showArchivedSessions: boolean;
  // ── 接线层公共 ────────────────────────────────────────────────
  onSelectThread: (threadId: string) => void;
  openSections: SidebarSectionState;
  openSessionDirectories: Record<string, boolean>;
  renamingSessionId: string | null;
  runtimeSessionUsersError: string | null;
  runtimeSessionUsersLoading: boolean;
  selectedRuntimeSessionUserId: string;
  selectedThreadId: string;
  showWorkspaceSection: boolean;
  sidebarActionError: string | null;
  setSidebarActionError: Dispatch<SetStateAction<string | null>>;
  startSessionRename: (sessionId: string) => void;
  t: TFunction<"workspace">;
  toggleSection: (section: SidebarSectionId) => void;
  toggleSessionDirectory: (directoryKey: string) => void;
};

export function WorkspaceSidebarDirectoriesSection({
  cancelDirectoryRename,
  cancelSessionRename,
  canMoveSessionToGroup,
  commitDirectoryRename,
  creatingSessionKey,
  deferredQuery,
  handleCreateSessionInDirectory,
  handleRenameSession,
  hiddenArchivedCount,
  mergedDirectoryGroups,
  onArchiveSession,
  onDeleteSession,
  onForkSession,
  onMoveSessionToGroup,
  onRefreshSessionStats,
  onReorderSessions,
  onRequestManageDirectories,
  onRestoreSession,
  onSelectRuntimeSessionUser,
  onSelectSessionGroupingMode,
  onSelectSessionOrderMode,
  onSelectThread,
  onToggleArchivedSessions,
  openSections,
  openSessionDirectories,
  renamingDirectoryId,
  renamingSessionId,
  runtimeSessionUsersError,
  runtimeSessionUsersLoading,
  selectedRuntimeSessionUserId,
  selectedThreadId,
  sessionActivity,
  sessionDirectoryGroups,
  sessionGroupingMode,
  sessionMoveError,
  sessionOrderMode,
  sessionStats,
  sessionStatsError,
  sessionStatsStatus,
  sessionStatsUnavailable,
  sessionThreadById,
  sessionThreads,
  sessionUserMenuItems,
  setDirectoryAddOpen,
  setDirectoryDeleteTarget,
  setSidebarActionError,
  showArchivedSessions,
  showWorkspaceSection,
  sidebarActionError,
  startDirectoryRename,
  startSessionRename,
  t,
  toggleSection,
  toggleSessionDirectory,
  workspaceDirectories,
  workspaceDirectoriesError,
  workspaceDirectoriesLoading,
  workspaceDirectoriesRefreshing,
}: WorkspaceSidebarDirectoriesSectionProps) {
  // 平铺视图：没有组头（跨组拖拽也不启用），只输出扁平会话行。
  // 一条会话行都没有时退回目录树，否则「已登记但 0 会话」的目录与管理入口会无处可见（§3.5-D/F）。
  const renderPlainSessionList =
    sessionGroupingMode === "flat" && sessionDirectoryGroups.length > 0;
  const groups = renderPlainSessionList
    ? sessionDirectoryGroups
    : appendEmptyRegisteredGroups(
        sessionDirectoryGroups,
        mergedDirectoryGroups,
      );
  const hasRegisteredDirectories =
    workspaceDirectories.length > 0 ||
    mergedDirectoryGroups.some((group) => group.registered);

  function findSessionTitle(sessionId: string): string {
    const session = sessionDirectoryGroups
      .flatMap((group) => group.sessions)
      .find((item) => item.id === sessionId);
    return session
      ? sessionThreadById.get(session.id)?.title ||
          session.metadata?.title?.trim() ||
          session.id
      : sessionId;
  }

  function describeSessionDirectory(groupKey: string): string {
    const group = sessionDirectoryGroups.find((item) => item.key === groupKey);
    if (!group) {
      return groupKey;
    }
    return group.fullPath ? group.label : t("sidebar.sessionDirectoryUnscoped");
  }

  // P2-6：组内拖拽重排 + （按目录视图下的）跨组移动。
  // 跨组移动只在组边界可见时可表达：平铺视图不接收落点。
  const {
    announcement: orderAnnouncement,
    dragPropsFor,
    groupDropPropsFor,
  } = useSidebarSessionDrag({
    enabled: sessionOrderMode === "manual",
    groups: sessionDirectoryGroups,
    onReorder: onReorderSessions,
    crossGroupEnabled: sessionGroupingMode === "directory",
    canMoveToGroup: canMoveSessionToGroup,
    onMoveSession: (sessionId, target) =>
      onMoveSessionToGroup(sessionId, target.groupKey),
    describeMoved: (accountKey, sessionId) => {
      const moved = sessionDirectoryGroups
        .find((group) => group.key === accountKey)
        ?.sessions.find((session) => session.id === sessionId);
      return t("sidebar.sessionOrder.moved", {
        title: findSessionTitle(moved?.id ?? sessionId),
      });
    },
    describeMovedToGroup: (sessionId, groupKey) =>
      t("sidebar.sessionMove.moved", {
        title: findSessionTitle(sessionId),
        directory: describeSessionDirectory(groupKey),
      }),
  });

  // P2-6 子片 3：组内展开 / 折叠（Show N more）——只影响呈现，不改排序账目。
  const { toggle: toggleSessionGroup, visibilityFor } =
    useSessionGroupVisibility();

  const emptyState = (
    <div className="rounded-card border border-dashed border-border px-3 py-3 text-sm leading-6 text-muted-foreground">
      <p>
        {deferredQuery
          ? t("sidebar.emptySessions.search")
          : hiddenArchivedCount > 0
            ? t("sidebar.emptySessions.allArchived")
            : hasRegisteredDirectories
              ? t("sidebar.emptySessions.default")
              : t("sidebar.directories.empty")}
      </p>
      {deferredQuery || hiddenArchivedCount > 0 ? null : (
        <Button
          variant="secondary"
          size="sm"
          className="mt-2"
          onClick={() => {
            setSidebarActionError(null);
            setDirectoryAddOpen(true);
          }}
        >
          <FolderPlusIcon size={13} />
          {t("sidebar.directories.add")}
        </Button>
      )}
    </div>
  );

  return showWorkspaceSection ? (
    <SidebarSection
      id="directories"
      icon={FolderIcon}
      iconClassName="text-accent-primary"
      title={t("sidebar.sections.directories")}
      count={<Badge>{sessionThreads.length}</Badge>}
      isOpen={openSections.directories}
      onToggle={toggleSection}
      action={
        <Button
          variant="ghost"
          size="icon"
          onClick={() => {
            setSidebarActionError(null);
            setDirectoryAddOpen(true);
          }}
          aria-label={t("sidebar.directories.add")}
          title={t("sidebar.directories.add")}
        >
          <FolderPlusIcon size={14} />
        </Button>
      }
    >
      <div className="space-y-2">
        {workspaceDirectoriesLoading || workspaceDirectoriesRefreshing ? (
          <div className="inline-flex items-center gap-1.5 rounded-control border border-border bg-surface-soft px-2 py-1 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            <LoaderCircleIcon size={12} className="animate-spin" />
            {t("sidebar.runtimeStats.syncing")}
          </div>
        ) : null}
        {workspaceDirectoriesError ? (
          <div className="rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-2.5 py-2 text-xs leading-5 text-muted-foreground">
            {workspaceDirectoriesError}
          </div>
        ) : null}
        {sidebarActionError ? (
          <div className="rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-2.5 py-2 text-xs leading-5 text-muted-foreground">
            {sidebarActionError}
          </div>
        ) : null}
        <WorkspaceSidebarSessionBrowserToolbar
          emptyState={emptyState}
          hasGroups={groups.length > 0}
          hiddenArchivedCount={hiddenArchivedCount}
          onManageDirectories={onRequestManageDirectories}
          onRefreshSessionStats={onRefreshSessionStats}
          onSelectRuntimeSessionUser={onSelectRuntimeSessionUser}
          onSelectSessionGroupingMode={onSelectSessionGroupingMode}
          onSelectSessionOrderMode={onSelectSessionOrderMode}
          onToggleArchivedSessions={onToggleArchivedSessions}
          runtimeSessionUsersError={runtimeSessionUsersError}
          runtimeSessionUsersLoading={runtimeSessionUsersLoading}
          selectedRuntimeSessionUserId={selectedRuntimeSessionUserId}
          sessionGroupingMode={sessionGroupingMode}
          sessionMoveError={sessionMoveError}
          sessionOrderMode={sessionOrderMode}
          sessionStats={sessionStats}
          sessionStatsError={sessionStatsError}
          sessionStatsStatus={sessionStatsStatus}
          sessionStatsUnavailable={sessionStatsUnavailable}
          sessionUserMenuItems={sessionUserMenuItems}
          showArchivedSessions={showArchivedSessions}
          t={t}
        >
          {groups.length > 0
            ? groups.map((group) => {
                const isDirectoryOpen =
                  openSessionDirectories[group.key] ?? false;
                const isCreating = creatingSessionKey === group.key;
                const isRenamingDirectory =
                  Boolean(group.directoryId) &&
                  renamingDirectoryId === group.directoryId;
                const displayLabel = group.fullPath
                  ? group.label
                  : t("sidebar.sessionDirectoryUnscoped");
                const pinnedSessionId =
                  group.sessions.find(
                    (session) =>
                      (sessionThreadById.get(session.id)?.id ?? session.id) ===
                      selectedThreadId,
                  )?.id ?? null;
                const groupVisibility = visibilityFor(
                  group.key,
                  group.sessions,
                  pinnedSessionId,
                );
                // `dropActive` 只用于样式/测试钩子，必须与事件处理器分开：
                // 整体展开会把非 DOM 属性透传到 <button>（React 会告警）。
                const { dropActive, ...groupDragHandlers } =
                  groupDropPropsFor(group.key);

                return (
                  <div
                    className="space-y-1"
                    data-group-key={group.key}
                    data-testid="sidebar-session-group"
                    key={group.key}
                  >
                    {renderPlainSessionList ? null : (
                      <WorkspaceSidebarDirectoryGroupHeader
                        actions={
                          group.registered ? (
                            <WorkspaceSidebarDirectoryGroupActions
                              creating={isCreating}
                              onCreate={() =>
                                void handleCreateSessionInDirectory(group)
                              }
                              onRename={() => startDirectoryRename(group)}
                              onRemove={() =>
                                setDirectoryDeleteTarget({
                                  id: group.directoryId ?? "",
                                  label: displayLabel,
                                  fullPath: group.fullPath,
                                  sessionCount: group.sessions.length,
                                })
                              }
                              t={t}
                            />
                          ) : undefined
                        }
                        drop={{ active: dropActive, ...groupDragHandlers }}
                        isOpen={isDirectoryOpen}
                        label={displayLabel}
                        missingLabel={
                          group.registered && group.exists === false
                            ? t("sidebar.directories.existsWarning")
                            : undefined
                        }
                        onToggle={() => toggleSessionDirectory(group.key)}
                        registered={group.registered}
                        sessionCount={group.sessions.length}
                        testId="sidebar-session-group-drop"
                        title={group.fullPath || displayLabel}
                      />
                    )}
                    {isRenamingDirectory ? (
                      <div className="px-1">
                        <InlineRenameInput
                          ariaLabel={t("sidebar.directories.rename")}
                          initial={group.label}
                          onCancel={cancelDirectoryRename}
                          onSubmit={(value) =>
                            void commitDirectoryRename(value)
                          }
                          placeholder={t("sidebar.session.renamePlaceholder")}
                        />
                      </div>
                    ) : null}
                    {renderPlainSessionList || isDirectoryOpen ? (
                      // 2026-09-15 样式优化：取消会话行的嵌套缩进（原 ml-4），
                      // 目录与会话共用同一左边界，让出的横向空间归还会话标题。
                      <div className="space-y-1">
                        {groupVisibility.visible.map((session) => (
                          <WorkspaceSidebarSessionRow
                            activity={sessionActivity?.[session.id]}
                            dragProps={dragPropsFor(group.key, session.id)}
                            key={`session-row-${session.id}`}
                            onArchive={onArchiveSession}
                            onCancelRename={cancelSessionRename}
                            onDelete={onDeleteSession}
                            onFork={onForkSession}
                            onRenameSubmit={(sessionId, value) => {
                              void handleRenameSession(sessionId, value);
                            }}
                            onRestore={onRestoreSession}
                            onSelectThread={onSelectThread}
                            onStartRename={startSessionRename}
                            renaming={renamingSessionId === session.id}
                            selectedThreadId={selectedThreadId}
                            session={session}
                            sessionThread={sessionThreadById.get(session.id)}
                            t={t}
                            visibleSessions={groupVisibility.visible}
                          />
                        ))}
                        {groupVisibility.collapsible ? (
                          <WorkspaceSidebarSessionGroupToggle
                            expanded={groupVisibility.expanded}
                            hiddenCount={groupVisibility.hiddenCount}
                            onToggle={() => toggleSessionGroup(group.key)}
                            t={t}
                          />
                        ) : null}
                        {group.sessions.length === 0 ? (
                          <div className="rounded-card border border-dashed border-border px-3 py-2 text-sm leading-6 text-muted-foreground">
                            {t("sidebar.emptySessions.default")}
                          </div>
                        ) : null}
                      </div>
                    ) : null}
                  </div>
                );
              })
            : emptyState}
        </WorkspaceSidebarSessionBrowserToolbar>
      </div>
      {/* 拖拽重排的无障碍播报：只播报结果，不占用视觉空间。 */}
      <div
        role="status"
        aria-live="polite"
        className="sr-only"
        data-testid="sidebar-session-order-announcement"
      >
        {orderAnnouncement}
      </div>
    </SidebarSection>
  ) : null;
}
