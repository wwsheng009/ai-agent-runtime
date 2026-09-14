// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { ArchiveIcon, ChevronDownIcon, FolderIcon, HistoryIcon, LoaderCircleIcon, UserIcon } from "lucide-react";
import { describeThreadSession } from "@/components/workspace/workspace-sidebar-shared";
import { Badge } from "@/components/ui/badge";
import { cn, formatRelativeTimestamp } from "@/lib/utils";
import { type Dispatch, type SetStateAction } from "react";
import { type TFunction } from "i18next";

import type { SessionStatsStatus } from "@/hooks/workspace/use-session-stats";
import type { RuntimeSessionStats } from "@/types/runtime";

import { buildSidebarIconLabels, buildThreadSessionDetails } from "./labels";
import { SidebarSection } from "./section-shell";
import { WorkspaceSidebarSessionStatsSummary } from "./session-stats-summary";
import { SidebarSessionItem } from "./session-item";
import {
  getRuntimeSessionActivityIcon,
  getSessionStatusIcon,
} from "./state-icon-utils";
import {
  resolveSidebarSessionRowState,
  resolveSidebarSessionRowStatus,
  type SidebarSessionActivity,
} from "./session-row-status";
import {
  type SidebarDirectoryGroup,
  type SidebarSectionId,
  type SidebarSectionState,
  type SidebarThread,
} from "./types";

type WorkspaceSidebarSessionUserMenuItem = {
  displayName: string;
  isDefaultUser: boolean;
  sessionCount: number;
  userId: string;
};

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
  deferredQuery,
  handleRenameSession,
  hiddenArchivedCount,
  onArchiveSession,
  onDeleteSession,
  onForkSession,
  onRefreshSessionStats,
  onRestoreSession,
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
  const sidebarLabels = buildSidebarIconLabels(t);
  const threadSessionDetails = buildThreadSessionDetails(t);

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
                      <div className="ml-3 space-y-1 border-l border-border pl-2">
                        {sessionDirectoryGroups.length > 0 ? (
                          sessionDirectoryGroups.map((group) => {
                            const isDirectoryOpen =
                              openSessionDirectories[group.key] ?? false;

                            return (
                              <div key={group.key} className="space-y-1">
                                <button
                                  type="button"
                                  title={group.fullPath || group.label}
                                  onClick={() => toggleSessionDirectory(group.key)}
                                  className="flex w-full items-center gap-2 rounded-[0.72rem] px-2 py-1.5 text-left text-muted-foreground transition hover:bg-surface-softer hover:text-foreground"
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

                                {isDirectoryOpen ? (
                                  <div className="ml-4 space-y-1">
                                    {group.sessions.map((session) => {
                                      const thread =
                                        sessionThreadById.get(session.id);
                                      const title =
                                        thread?.title ||
                                        session.metadata?.title?.trim() ||
                                        session.id;
                                      const isActive =
                                        thread?.id === selectedThreadId ||
                                        thread?.sessionId === selectedThreadId;
                                      const rowState =
                                        resolveSidebarSessionRowState(session);
                                      const rowStatus =
                                        resolveSidebarSessionRowStatus(
                                          session,
                                          sessionActivity?.[session.id],
                                        );
                                      const sessionStatusIcon =
                                        rowStatus.kind === "idle"
                                          ? getSessionStatusIcon(
                                              describeThreadSession(
                                                thread ?? {
                                                  id: session.id,
                                                  title,
                                                  summary:
                                                    session.metadata?.summary ??
                                                    "",
                                                  updatedAt:
                                                    session.updatedAt ||
                                                    session.createdAt ||
                                                    "",
                                                  status: "active",
                                                  sessionId: session.id,
                                                  tags: ["runtime-session"],
                                                  prompts: [],
                                                  messages: [],
                                                  artifacts: [],
                                                },
                                                threadSessionDetails,
                                              ).label,
                                              sidebarLabels,
                                            )
                                          : getRuntimeSessionActivityIcon(
                                              rowStatus,
                                              sidebarLabels,
                                            );
                                      const timestamp =
                                        session.updatedAt ||
                                        session.createdAt ||
                                        "";
                                      const createdAt =
                                        session.createdAt || timestamp;
                                      const itemTime =
                                        timestamp &&
                                        !Number.isNaN(Date.parse(timestamp))
                                          ? {
                                              relative:
                                                formatRelativeTimestamp(
                                                  timestamp,
                                                ),
                                              title: t(
                                                "sidebar.session.createdAt",
                                                {
                                                  time: Number.isNaN(
                                                    Date.parse(createdAt),
                                                  )
                                                    ? createdAt
                                                    : new Date(
                                                        createdAt,
                                                      ).toLocaleString(),
                                                },
                                              ),
                                            }
                                          : undefined;

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
                                          isActive={isActive}
                                          onArchive={onArchiveSession}
                                          onCancelRename={() =>
                                            setRenamingSessionId(null)
                                          }
                                          onDelete={onDeleteSession}
                                          onFork={
                                            onForkSession
                                              ? (sessionId) =>
                                                  onForkSession(sessionId, title)
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
                                              thread?.id ?? session.id,
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
                                          rowState={rowState}
                                          session={session}
                                          statusIcon={sessionStatusIcon}
                                          time={itemTime}
                                          title={title}
                                        />
                                      );
                                    })}
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
      </SidebarSection>
    ) : null
  );
}
