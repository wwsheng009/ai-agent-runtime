// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { ArchiveIcon, ChevronDownIcon, FolderIcon, HistoryIcon, LoaderCircleIcon, UserIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { type Dispatch, type SetStateAction } from "react";
import { type TFunction } from "i18next";

import type { SessionStatsStatus } from "@/hooks/workspace/use-session-stats";
import type { RuntimeSessionStats } from "@/types/runtime";
import { type SessionGroupingMode } from "@/lib/workspace/session-grouping";
import { type SessionOrderMode } from "@/lib/workspace/session-order";

import { resolveSidebarSessionRowViewModel } from "./session-row-view-model";
import { WorkspaceSidebarSessionGroupingControl } from "./session-grouping-control";
import { WorkspaceSidebarSessionOrderControl } from "./session-order-control";
import { WorkspaceSidebarSessionGroupToggle } from "./session-group-toggle";
import { useSessionGroupVisibility } from "./use-session-group-visibility";
import { SidebarSection } from "./section-shell";
import { WorkspaceSidebarSessionStatsSummary } from "./session-stats-summary";
import {
  type WorkspaceSidebarSessionUserMenuItem,
} from "./session-user-menu";
import { SidebarSessionItem } from "./session-item";
import { useSidebarSessionDrag } from "./use-session-drag-reorder";
import {
  type SidebarSessionActivity,
} from "./session-row-status";
import {
  type SidebarDirectoryGroup,
  type SidebarSectionId,
  type SidebarSectionState,
  type SidebarThread,
} from "./types";

type WorkspaceSidebarSessionsSectionProps = {
  deferredQuery: string;
  handleRenameSession: (sessionId: string, title: string) => Promise<void>;
  hiddenArchivedCount: number;
  onArchiveSession?: (sessionId: string) => void;
  onDeleteSession?: (sessionId: string) => void;
  onForkSession?: (sessionId: string, sourceTitle: string) => void;
  onRefreshSessionStats: () => void;
  onRestoreSession?: (sessionId: string) => void;
  onToggleArchivedSessions: () => void;
  onSelectRuntimeSessionUser: (userId: string) => void;
  onSelectThread: (threadId: string) => void;
  openSections: SidebarSectionState;
  openSessionDirectories: Record<string, boolean>;
  renamingSessionId: string | null;
  runtimeSessionUsersError: string | null;
  runtimeSessionUsersLoading: boolean;
  selectedRuntimeSessionUserId: string;
  selectedThreadId: string;
  sessionDirectoryGroups: SidebarDirectoryGroup[];
  sessionGroupingMode: SessionGroupingMode;
  /** 跨组移动失败的就地提示（失败即回滚，提示挂在会话段而不是目录段）。 */
  sessionMoveError: string | null;
  sessionOrderMode: SessionOrderMode;
  onSelectSessionGroupingMode: (mode: SessionGroupingMode) => void;
  onSelectSessionOrderMode: (mode: SessionOrderMode) => void;
  onReorderSessions: (accountKey: string, order: readonly string[]) => void;
  /** 跨组移动：目标目录是否可归属（未登记 / 宿主缺位的目录不可作落点）。 */
  canMoveSessionToGroup: (groupKey: string) => boolean;
  /** 跨组移动意图（乐观归属 + Host 写回由接线层执行）。 */
  onMoveSessionToGroup: (sessionId: string, groupKey: string) => void;
  sessionStats: RuntimeSessionStats | null;
  sessionStatsError: unknown;
  sessionStatsStatus: SessionStatsStatus;
  sessionStatsUnavailable: boolean;
  sessionThreadById: Map<string, SidebarThread>;
  sessionThreads: SidebarThread[];
  sessionUserMenuItems: WorkspaceSidebarSessionUserMenuItem[];
  setRenamingSessionId: Dispatch<SetStateAction<string | null>>;
  sessionActivity?: Record<string, SidebarSessionActivity>;
  showArchivedSessions: boolean;
  showSessionsSection: boolean;
  startSessionRename: (sessionId: string) => void;
  t: TFunction<"workspace">;
  toggleSection: (section: SidebarSectionId) => void;
  toggleSessionDirectory: (directoryKey: string) => void;
};

export function WorkspaceSidebarSessionsSection({
  canMoveSessionToGroup,
  deferredQuery,
  handleRenameSession,
  hiddenArchivedCount,
  onArchiveSession,
  onDeleteSession,
  onForkSession,
  onMoveSessionToGroup,
  onRefreshSessionStats,
  onReorderSessions,
  onRestoreSession,
  onSelectSessionGroupingMode,
  onSelectSessionOrderMode,
  onToggleArchivedSessions,
  onSelectRuntimeSessionUser,
  onSelectThread,
  openSections,
  openSessionDirectories,
  renamingSessionId,
  runtimeSessionUsersError,
  runtimeSessionUsersLoading,
  selectedRuntimeSessionUserId,
  selectedThreadId,
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
  setRenamingSessionId,
  sessionActivity,
  showArchivedSessions,
  showSessionsSection,
  startSessionRename,
  t,
  toggleSection,
  toggleSessionDirectory,
}: WorkspaceSidebarSessionsSectionProps) {
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
  // 跨组移动只在组边界可见时可表达：平铺视图与不可归属的目标目录都不接收落点。
  const { announcement: orderAnnouncement, dragPropsFor, groupDropPropsFor } =
    useSidebarSessionDrag({
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

  return (
    showSessionsSection ? (
      <SidebarSection
        id="sessions"
        icon={HistoryIcon}
        iconClassName="text-accent-secondary"
        title={t("sidebar.sections.sessions")}
        count={<Badge>{sessionThreads.length}</Badge>}
        isOpen={openSections.sessions}
        onToggle={toggleSection}
      >
        <div className="space-y-2">
          {runtimeSessionUsersLoading && sessionUserMenuItems.length === 0 ? (
            <div className="inline-flex items-center gap-1.5 rounded-control border border-border bg-surface-soft px-2 py-1 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
              <LoaderCircleIcon size={12} className="animate-spin" />
              {t("sidebar.sessionUsersLoading")}
            </div>
          ) : null}
          {runtimeSessionUsersError ? (
            <div className="rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-2.5 py-2 text-xs leading-5 text-muted-foreground">
              {runtimeSessionUsersError}
            </div>
          ) : null}
          <div className="space-y-1.5">
            <WorkspaceSidebarSessionStatsSummary
              error={sessionStatsError}
              onRefresh={onRefreshSessionStats}
              stats={sessionStats}
              status={sessionStatsStatus}
              t={t}
              unavailable={sessionStatsUnavailable}
            />
            {sessionDirectoryGroups.length > 0 ? (
              <WorkspaceSidebarSessionOrderControl
                mode={sessionOrderMode}
                onSelect={onSelectSessionOrderMode}
                t={t}
              />
            ) : null}
            {sessionDirectoryGroups.length > 0 ? (
              <WorkspaceSidebarSessionGroupingControl
                mode={sessionGroupingMode}
                onSelect={onSelectSessionGroupingMode}
                t={t}
              />
            ) : null}
            {sessionMoveError ? (
              <div className="rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-2.5 py-2 text-xs leading-5 text-muted-foreground">
                {sessionMoveError}
              </div>
            ) : null}
            {showArchivedSessions || hiddenArchivedCount > 0 ? (
              <button
                type="button"
                onClick={onToggleArchivedSessions}
                className={cn(
                  "flex w-full items-center justify-center gap-1.5 rounded-control border px-2 py-1 app-text-10 uppercase tracking-[0.14em] transition",
                  showArchivedSessions
                    ? "border-accent-secondary-border bg-accent-secondary-soft text-accent-secondary"
                    : "border-dashed border-border bg-surface-softer text-muted-foreground hover:text-foreground",
                )}
              >
                <ArchiveIcon size={12} />
                {showArchivedSessions
                  ? t("sidebar.session.hideArchived")
                  : t("sidebar.session.showArchived", {
                      count: hiddenArchivedCount,
                    })}
              </button>
            ) : null}
            {sessionUserMenuItems.length > 0 ? (
              sessionUserMenuItems.map((user) => {
                const isSelectedUser =
                  user.userId === selectedRuntimeSessionUserId.trim();

                return (
                  <div key={user.userId} className="space-y-1">
                    <button
                      type="button"
                      title={user.userId}
                      onClick={() => onSelectRuntimeSessionUser(user.userId)}
                      className={cn(
                        "flex w-full items-center gap-2 rounded-card border px-2.5 py-2 text-left transition",
                        isSelectedUser
                          ? "border-accent-secondary-border bg-accent-secondary-soft"
                          : "border-border bg-surface-softer hover:border-border-strong hover:bg-surface-soft",
                      )}
                    >
                      <UserIcon
                        size={14}
                        className="shrink-0 text-accent-secondary"
                      />
                      <span className="min-w-0 flex-1 truncate text-sm font-semibold text-foreground">
                        {user.displayName}
                      </span>
                      {user.isDefaultUser ? (
                        <span className="shrink-0 rounded-[0.55rem] border border-border bg-surface-soft px-1.5 py-0.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
                          {t("sidebar.sessionUserDefault")}
                        </span>
                      ) : null}
                      <Badge>{user.sessionCount}</Badge>
                      <ChevronDownIcon
                        size={14}
                        className={cn(
                          "shrink-0 text-muted-foreground transition-transform duration-200",
                          isSelectedUser ? "rotate-0" : "-rotate-90",
                        )}
                      />
                    </button>

                    {isSelectedUser ? (
                      <div
                        className={cn(
                          "ml-3 space-y-1",
                          sessionGroupingMode === "directory" &&
                            "border-l border-border pl-2",
                        )}
                      >
                        {sessionDirectoryGroups.length > 0 ? (
                          sessionDirectoryGroups.map((group) => {
                            const isDirectoryOpen =
                              openSessionDirectories[group.key] ?? false;
                            const pinnedSessionId =
                              group.sessions.find(
                                (session) =>
                                  (sessionThreadById.get(session.id)?.id ??
                                    session.id) === selectedThreadId,
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
                              <div key={group.key} className="space-y-1">
                                {sessionGroupingMode === "directory" ? (
                                  <button
                                    type="button"
                                    title={group.fullPath || group.label}
                                    onClick={() =>
                                      toggleSessionDirectory(group.key)
                                    }
                                    {...groupDragHandlers}
                                    data-testid="sidebar-session-group-drop"
                                    data-drop-active={dropActive}
                                    className={cn(
                                      "flex w-full items-center gap-2 rounded-[0.72rem] border px-2 py-1.5 text-left transition",
                                      dropActive
                                        ? "border-accent-primary-border bg-accent-primary-soft text-foreground"
                                        : "border-transparent text-muted-foreground hover:bg-surface-softer hover:text-foreground",
                                    )}
                                  >
                                    <FolderIcon
                                      size={13}
                                      className="shrink-0 text-accent-primary"
                                    />
                                    <span className="min-w-0 flex-1 truncate text-xs font-medium">
                                      {group.fullPath
                                        ? group.label
                                        : t("sidebar.sessionDirectoryUnscoped")}
                                    </span>
                                    <span className="shrink-0 app-text-10 text-muted-foreground">
                                      {group.sessions.length}
                                    </span>
                                    <ChevronDownIcon
                                      size={13}
                                      className={cn(
                                        "shrink-0 transition-transform duration-200",
                                        isDirectoryOpen
                                          ? "rotate-0"
                                          : "-rotate-90",
                                      )}
                                    />
                                  </button>
                                ) : null}

                                {sessionGroupingMode === "flat" ||
                                isDirectoryOpen ? (
                                  <div
                                    className={cn(
                                      "space-y-1",
                                      sessionGroupingMode === "directory" &&
                                        "ml-4",
                                    )}
                                  >
                                    {groupVisibility.visible.map((session) => {
                                      const row = resolveSidebarSessionRowViewModel(
                                        {
                                          activity:
                                            sessionActivity?.[session.id],
                                          selectedThreadId,
                                          session,
                                          sessionThread: sessionThreadById.get(
                                            session.id,
                                          ),
                                          t,
                                        },
                                      );
                                      return (
                                        <SidebarSessionItem
                                          key={`recoverable-${session.id}`}
                                          actionLabels={{
                                            archivedBadge: t(
                                              "sidebar.session.archivedBadge",
                                            ),
                                            archive: t(
                                              "sidebar.session.archive",
                                            ),
                                            delete: t(
                                              "sidebar.session.delete",
                                            ),
                                            fork: t(
                                              "sidebar.session.fork",
                                            ),
                                            menu: t("sidebar.session.menu"),
                                            restore: t(
                                              "sidebar.session.restore",
                                            ),
                                          }}
                                          {...dragPropsFor(
                                            group.key,
                                            session.id,
                                          )}
                                          isActive={row.isActive}
                                          onArchive={onArchiveSession}
                                          onCancelRename={() =>
                                            setRenamingSessionId(null)
                                          }
                                          onDelete={onDeleteSession}
                                          onFork={
                                            onForkSession
                                              ? (sessionId) =>
                                                  onForkSession(
                                                    sessionId,
                                                    row.title,
                                                  )
                                              : undefined
                                          }
                                          onRenameSubmit={
                                            (sessionId, value) =>
                                              void handleRenameSession(
                                                sessionId,
                                                value,
                                              )
                                          }
                                          onSelect={() =>
                                            onSelectThread(
                                              row.thread?.id ?? session.id,
                                            )
                                          }
                                          onStartRename={
                                            startSessionRename
                                          }
                                          onRestore={onRestoreSession}
                                          renameLabels={{
                                            placeholder: t(
                                              "sidebar.session.renamePlaceholder",
                                            ),
                                            rename: t(
                                              "sidebar.session.rename",
                                            ),
                                          }}
                                          renaming={
                                            renamingSessionId ===
                                            session.id
                                          }
                                          rowState={row.rowState}
                                          session={session}
                                          statusIcon={row.statusIcon}
                                          time={row.itemTime}
                                          title={row.title}
                                        />
                                      );
                                    })}
                                    {groupVisibility.collapsible ? (
                                      <WorkspaceSidebarSessionGroupToggle
                                        expanded={groupVisibility.expanded}
                                        hiddenCount={
                                          groupVisibility.hiddenCount
                                        }
                                        onToggle={() =>
                                          toggleSessionGroup(group.key)
                                        }
                                        t={t}
                                      />
                                    ) : null}
                                  </div>
                                ) : null}
                              </div>
                            );
                          })
                        ) : (
                          <div className="rounded-card border border-dashed border-border px-3 py-3 text-sm leading-6 text-muted-foreground">
                            {deferredQuery
                              ? t("sidebar.emptySessions.search")
                              : hiddenArchivedCount > 0
                                ? t("sidebar.emptySessions.allArchived")
                                : t("sidebar.emptySessions.default")}
                          </div>
                        )}
                      </div>
                    ) : null}
                  </div>
                );
              })
            ) : !runtimeSessionUsersLoading ? (
              <div className="rounded-card border border-dashed border-border px-3 py-3 text-sm leading-6 text-muted-foreground">
                {deferredQuery
                  ? t("sidebar.emptySessions.search")
                  : hiddenArchivedCount > 0
                    ? t("sidebar.emptySessions.allArchived")
                    : t("sidebar.emptySessions.default")}
              </div>
            ) : null}
          </div>
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
    ) : null
  );
}
