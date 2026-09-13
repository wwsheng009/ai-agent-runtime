// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { ChevronDownIcon, FolderIcon, FolderPlusIcon, LoaderCircleIcon, MessageSquarePlusIcon, PencilIcon, TrashIcon, TriangleAlertIcon } from "lucide-react";
import { describeThreadSession } from "@/components/workspace/workspace-sidebar-shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { type Dispatch, type SetStateAction } from "react";
import { type TFunction } from "i18next";

import { type RuntimeWorkspaceDirectory } from "@/lib/runtime-api";

import { buildSidebarIconLabels, buildThreadSessionDetails } from "./labels";
import { SidebarSection } from "./section-shell";
import { InlineRenameInput, SidebarSessionItem } from "./session-item";
import { buildSessionDescriptorThread } from "./session-descriptor";
import { getSessionStatusIcon } from "./state-icon-utils";
import {
  type SidebarDirectoryGroup,
  type SidebarSectionId,
  type SidebarSectionState,
  type SidebarThread,
} from "./types";

type WorkspaceSidebarDirectoriesSectionProps = {
  cancelDirectoryRename: () => void;
  commitDirectoryRename: (nextName: string) => Promise<void>;
  creatingSessionKey: string | null;
  handleCreateSessionInDirectory: (group: SidebarDirectoryGroup) => Promise<void>;
  handleRenameSession: (sessionId: string, title: string) => Promise<void>;
  mergedDirectoryGroups: SidebarDirectoryGroup[];
  onSelectThread: (threadId: string) => void;
  openSections: SidebarSectionState;
  openSessionDirectories: Record<string, boolean>;
  renamingDirectoryId: string | null;
  renamingSessionId: string | null;
  selectedThreadId: string;
  sessionThreadById: Map<string, SidebarThread>;
  setDirectoryAddOpen: Dispatch<SetStateAction<boolean>>;
  setDirectoryDeleteTarget: Dispatch<
    SetStateAction<{
      id: string;
      label: string;
      fullPath: string;
      sessionCount: number;
    } | null>
  >;
  setRenamingSessionId: Dispatch<SetStateAction<string | null>>;
  setSidebarActionError: Dispatch<SetStateAction<string | null>>;
  showDirectoriesSection: boolean;
  sidebarActionError: string | null;
  startDirectoryRename: (group: SidebarDirectoryGroup) => void;
  startSessionRename: (sessionId: string) => void;
  t: TFunction<"workspace">;
  toggleSection: (section: SidebarSectionId) => void;
  toggleSessionDirectory: (directoryKey: string) => void;
  workspaceDirectories: RuntimeWorkspaceDirectory[];
  workspaceDirectoriesError: string | null;
  workspaceDirectoriesLoading: boolean;
  workspaceDirectoriesRefreshing?: boolean;
};

export function WorkspaceSidebarDirectoriesSection({
  cancelDirectoryRename,
  commitDirectoryRename,
  creatingSessionKey,
  handleCreateSessionInDirectory,
  handleRenameSession,
  mergedDirectoryGroups,
  onSelectThread,
  openSections,
  openSessionDirectories,
  renamingDirectoryId,
  renamingSessionId,
  selectedThreadId,
  sessionThreadById,
  setDirectoryAddOpen,
  setDirectoryDeleteTarget,
  setRenamingSessionId,
  setSidebarActionError,
  showDirectoriesSection,
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
  const sidebarLabels = buildSidebarIconLabels(t);
  const threadSessionDetails = buildThreadSessionDetails(t);

  return (
    showDirectoriesSection ? (
      <SidebarSection
        id="directories"
        icon={FolderIcon}
        iconClassName="text-accent-primary"
        title={t("sidebar.sections.directories")}
        count={<Badge>{workspaceDirectories.length}</Badge>}
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
          {mergedDirectoryGroups.length > 0 ? (
            <div className="space-y-1">
              {mergedDirectoryGroups.map((group) => {
                const isDirectoryOpen =
                  openSessionDirectories[group.key] ?? false;
                const isCreating = creatingSessionKey === group.key;
                const isRenamingDirectory =
                  Boolean(group.directoryId) &&
                  renamingDirectoryId === group.directoryId;
                const displayLabel = group.fullPath
                  ? group.label
                  : t("sidebar.sessionDirectoryUnscoped");

                return (
                  <div key={group.key} className="space-y-1">
                    <div
                      className={cn(
                        "group/directory-row flex w-full items-center gap-1 rounded-[0.72rem] px-1.5 py-1 transition",
                        group.registered
                          ? "border border-border bg-surface-softer hover:border-border-strong hover:bg-surface-soft"
                          : "hover:bg-surface-softer",
                      )}
                    >
                      <button
                        type="button"
                        title={group.fullPath || displayLabel}
                        onClick={() => toggleSessionDirectory(group.key)}
                        className="flex min-w-0 flex-1 items-center gap-2 py-0.5 text-left"
                      >
                        <FolderIcon
                          size={13}
                          className={cn(
                            "shrink-0",
                            group.registered
                              ? "text-accent-primary"
                              : "text-muted-foreground",
                          )}
                        />
                        <span
                          className={cn(
                            "min-w-0 flex-1 truncate text-xs font-medium",
                            group.registered
                              ? "text-foreground"
                              : "text-muted-foreground",
                          )}
                        >
                          {displayLabel}
                        </span>
                        {group.registered && group.exists === false ? (
                          <span
                            title={t("sidebar.directories.existsWarning")}
                            aria-label={t(
                              "sidebar.directories.existsWarning",
                            )}
                            className="shrink-0 text-accent-orange"
                          >
                            <TriangleAlertIcon size={12} />
                          </span>
                        ) : null}
                        <span className="shrink-0 app-text-10 text-muted-foreground">
                          {group.sessions.length}
                        </span>
                        <ChevronDownIcon
                          size={13}
                          className={cn(
                            "shrink-0 text-muted-foreground transition-transform duration-200",
                            isDirectoryOpen ? "rotate-0" : "-rotate-90",
                          )}
                        />
                      </button>
                      {group.registered ? (
                        <div className="flex shrink-0 items-center gap-0.5 opacity-0 transition group-hover/directory-row:opacity-100 focus-within:opacity-100">
                          <button
                            type="button"
                            title={t("sidebar.directories.newChat")}
                            aria-label={t("sidebar.directories.newChat")}
                            disabled={isCreating}
                            onClick={() =>
                              void handleCreateSessionInDirectory(group)
                            }
                            className="rounded-chip p-1 text-muted-foreground transition hover:bg-surface-soft hover:text-foreground disabled:opacity-50"
                          >
                            {isCreating ? (
                              <LoaderCircleIcon
                                size={12}
                                className="animate-spin"
                              />
                            ) : (
                              <MessageSquarePlusIcon size={12} />
                            )}
                          </button>
                          <button
                            type="button"
                            title={t("sidebar.directories.rename")}
                            aria-label={t("sidebar.directories.rename")}
                            onClick={() => startDirectoryRename(group)}
                            className="rounded-chip p-1 text-muted-foreground transition hover:bg-surface-soft hover:text-foreground"
                          >
                            <PencilIcon size={12} />
                          </button>
                          <button
                            type="button"
                            title={t("sidebar.directories.deleteTitle")}
                            aria-label={t("sidebar.directories.deleteTitle")}
                            onClick={() =>
                              setDirectoryDeleteTarget({
                                id: group.directoryId ?? "",
                                label: displayLabel,
                                fullPath: group.fullPath,
                                sessionCount: group.sessions.length,
                              })
                            }
                            className="rounded-chip p-1 text-muted-foreground transition hover:bg-surface-soft hover:text-accent-orange"
                          >
                            <TrashIcon size={12} />
                          </button>
                        </div>
                      ) : null}
                    </div>
                    {isRenamingDirectory ? (
                      <div className="px-1.5">
                        <InlineRenameInput
                          ariaLabel={t("sidebar.directories.rename")}
                          initial={group.label}
                          placeholder={t(
                            "sidebar.session.renamePlaceholder",
                          )}
                          onCancel={cancelDirectoryRename}
                          onSubmit={(value) =>
                            void commitDirectoryRename(value)
                          }
                        />
                      </div>
                    ) : null}
                    {isDirectoryOpen ? (
                      <div className="ml-3 space-y-1 border-l border-border pl-2">
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
                          const sessionStatusIcon = getSessionStatusIcon(
                            describeThreadSession(
                              thread ??
                                buildSessionDescriptorThread(
                                  session,
                                  title,
                                ),
                              threadSessionDetails,
                            ).label,
                            sidebarLabels,
                          );

                          return (
                            <SidebarSessionItem
                              key={`directory-${group.key}-${session.id}`}
                              isActive={isActive}
                              onCancelRename={() =>
                                setRenamingSessionId(null)
                              }
                              onRenameSubmit={(sessionId, value) =>
                                void handleRenameSession(sessionId, value)
                              }
                              onSelect={() =>
                                onSelectThread(thread?.id ?? session.id)
                              }
                              onStartRename={startSessionRename}
                              renameLabels={{
                                placeholder: t(
                                  "sidebar.session.renamePlaceholder",
                                ),
                                rename: t("sidebar.session.rename"),
                              }}
                              renaming={renamingSessionId === session.id}
                              session={session}
                              statusIcon={sessionStatusIcon}
                              title={title}
                            />
                          );
                        })}
                        {group.sessions.length === 0 ? (
                          <div className="rounded-card border border-dashed border-border px-3 py-2 text-sm leading-6 text-muted-foreground">
                            {t("sidebar.emptySessions.default")}
                          </div>
                        ) : null}
                      </div>
                    ) : null}
                  </div>
                );
              })}
            </div>
          ) : (
            <div className="rounded-card border border-dashed border-border px-3 py-3 text-sm leading-6 text-muted-foreground">
              {t("sidebar.directories.empty")}
            </div>
          )}
        </div>
      </SidebarSection>
    ) : null
  );
}
