import {
  CheckIcon,
  ChevronDownIcon,
  Clock3Icon,
  CompassIcon,
  FolderIcon,
  FolderPlusIcon,
  HistoryIcon,
  LoaderCircleIcon,
  MessageSquarePlusIcon,
  MessagesSquareIcon,
  PencilIcon,
  SearchIcon,
  Settings2Icon,
  SparklesIcon,
  TrashIcon,
  TriangleAlertIcon,
  UserIcon,
  XIcon,
  type LucideIcon,
} from "lucide-react";
import {
  lazy,
  Suspense,
  useDeferredValue,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Link } from "react-router-dom";

import {
  describeThreadSession,
  mergeDirectoryGroups,
  summarizeSidebarSessions,
  type MergedDirectoryGroup,
  type ThreadSessionDescriptor,
} from "@/components/workspace/workspace-sidebar-shared";
import { WorkspaceDirectoryAddDialog } from "@/components/workspace/workspace-directory-add-dialog";
import { WorkspaceDirectoryDeleteDialog } from "@/components/workspace/workspace-directory-delete-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { type Thread } from "@/data/mock";
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";
import { type RuntimeSessionsSummary } from "@/hooks/workspace/use-runtime-sessions-data";
import {
  type RuntimeSessionRecord,
  type RuntimeSessionUserSummary,
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
  type RuntimeWorkspaceDirectory,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

const RuntimeTeamsDialog = lazy(() =>
  import("@/components/workspace/runtime-teams-dialog").then((module) => ({
    default: module.RuntimeTeamsDialog,
  })),
);

type WorkspaceDirectoryCreateRequest = {
  path: string;
  directoryId?: string;
  label: string;
};

type WorkspaceSidebarProps = {
  density: "comfortable" | "compact";
  mobileOpen?: boolean;
  onCloseMobile?: () => void;
  onOpenSettings?: () => void;
  runtimeTeams: RuntimeTeamRecord[];
  runtimeTeamsError: string | null;
  runtimeTeamsLoading: boolean;
  runtimeTeamsRefreshing?: boolean;
  runtimeTeamSummaries: RuntimeTeamSummaryEntry[];
  runtimeSessionsError: string | null;
  runtimeSessions: RuntimeSessionRecord[];
  runtimeSessionsLoading: boolean;
  runtimeSessionsRefreshing?: boolean;
  runtimeSessionsSummary: RuntimeSessionsSummary;
  runtimeSessionDefaultUserId?: string;
  runtimeSessionUsers: RuntimeSessionUserSummary[];
  runtimeSessionUsersError: string | null;
  runtimeSessionUsersLoading: boolean;
  selectedRuntimeSessionUserId: string;
  onRefreshRuntimeTeams?: () => void;
  onSelectRuntimeSessionUser: (userId: string) => void;
  workspaceDirectories: RuntimeWorkspaceDirectory[];
  workspaceDirectoriesError: string | null;
  workspaceDirectoriesLoading: boolean;
  workspaceDirectoriesRefreshing?: boolean;
  onAddWorkspaceDirectory: (path: string, name?: string) => Promise<unknown>;
  onRenameWorkspaceDirectory: (id: string, name: string) => Promise<void>;
  onRemoveWorkspaceDirectory: (id: string) => Promise<void>;
  onCreateSessionInDirectory: (
    directory: WorkspaceDirectoryCreateRequest,
  ) => Promise<void>;
  onRenameRuntimeSession: (sessionId: string, title: string) => Promise<void>;
  threads: Thread[];
  selectedThreadId: string;
  onSelectThread: (threadId: string) => void;
};

type SidebarSectionId = "directories" | "chats" | "sessions" | "runtime";

type SidebarSectionState = Record<SidebarSectionId, boolean>;

type SidebarStateIconSpec = {
  icon: LucideIcon;
  label: string;
  toneClassName: string;
};

type WorkspaceSidebarIconLabels = {
  threadReview: string;
  threadDraft: string;
  threadActive: string;
  sessionError: string;
  sessionRestored: string;
  sessionAttached: string;
  sessionPending: string;
};

function getThreadWorkflowIcon(
  thread: Thread,
  labels: Pick<
    WorkspaceSidebarIconLabels,
    "threadReview" | "threadDraft" | "threadActive"
  >,
): SidebarStateIconSpec {
  if (thread.status === "review") {
    return {
      icon: SearchIcon,
      label: labels.threadReview,
      toneClassName:
        "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] text-[var(--accent-primary)]",
    };
  }

  if (thread.status === "draft") {
    return {
      icon: Clock3Icon,
      label: labels.threadDraft,
      toneClassName:
        "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--muted-foreground)]",
    };
  }

  return {
    icon: SparklesIcon,
    label: labels.threadActive,
    toneClassName:
      "border-[var(--accent-secondary-border)] bg-[var(--accent-secondary-soft)] text-[var(--accent-secondary)]",
  };
}

function getSessionStatusIcon(
  label: ThreadSessionDescriptor["label"],
  labels: Pick<
    WorkspaceSidebarIconLabels,
    "sessionError" | "sessionRestored" | "sessionAttached" | "sessionPending"
  >,
): SidebarStateIconSpec {
  if (label === "error") {
    return {
      icon: TriangleAlertIcon,
      label: labels.sessionError,
      toneClassName: "border-[#f59e7d]/24 bg-[#f59e7d]/10 text-[#f59e7d]",
    };
  }

  if (label === "restored") {
    return {
      icon: HistoryIcon,
      label: labels.sessionRestored,
      toneClassName: "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]",
    };
  }

  if (label === "attached") {
    return {
      icon: CheckIcon,
      label: labels.sessionAttached,
      toneClassName: "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]",
    };
  }

  return {
    icon: LoaderCircleIcon,
    label: labels.sessionPending,
    toneClassName:
      "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--muted-foreground)]",
  };
}

function SidebarStateIcon({ spec }: { spec: SidebarStateIconSpec }) {
  const Icon = spec.icon;

  return (
    <span
      title={spec.label}
      aria-label={spec.label}
      className={cn(
        "inline-flex size-[1.375rem] items-center justify-center rounded-[0.65rem] border",
        spec.toneClassName,
      )}
    >
      <Icon size={11} />
    </span>
  );
}

type SidebarSectionProps = {
  children: ReactNode;
  count?: ReactNode;
  icon: LucideIcon;
  iconClassName: string;
  id: SidebarSectionId;
  isOpen: boolean;
  /** Optional header action (e.g. "add directory") outside the toggle button. */
  action?: ReactNode;
  onToggle: (id: SidebarSectionId) => void;
  title: string;
};

function SidebarSection({
  children,
  count,
  icon: Icon,
  iconClassName,
  id,
  isOpen,
  action,
  onToggle,
  title,
}: SidebarSectionProps) {
  return (
    <section>
      <div className="mb-2 flex w-full items-center justify-between gap-3 rounded-[0.7rem] px-1.5 py-1 transition hover:bg-[var(--surface-softer)]">
        <button
          type="button"
          onClick={() => onToggle(id)}
          aria-expanded={isOpen}
          className="flex min-w-0 flex-1 items-center justify-between gap-3 text-left"
        >
          <span className="inline-flex items-center gap-2 text-base uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
            <Icon size={14} className={iconClassName} />
            {title}
          </span>
          <span className="inline-flex items-center gap-2">
            {count}
            <ChevronDownIcon
              size={14}
              className={cn(
                "text-[var(--muted-foreground)] transition-transform duration-200",
                isOpen ? "rotate-0" : "-rotate-90",
              )}
            />
          </span>
        </button>
        {action}
      </div>

      {isOpen ? children : null}
    </section>
  );
}

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
  threads,
  selectedThreadId,
  onSelectThread,
}: WorkspaceSidebarProps) {
  const { t } = useTranslation("workspace");
  const isCompact = density === "compact";
  const [query, setQuery] = useState("");
  const [runtimeTeamsDialogOpen, setRuntimeTeamsDialogOpen] = useState(false);
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
  const mergedDirectoryGroups = useMemo(
    () => mergeDirectoryGroups(workspaceDirectories, runtimeSessions),
    [runtimeSessions, workspaceDirectories],
  );
  // Per-user session browser: skip registered directories without sessions
  // so the friendly empty state survives directory-only registrations.
  const sessionDirectoryGroups = useMemo(
    () => mergedDirectoryGroups.filter((group) => group.sessions.length > 0),
    [mergedDirectoryGroups],
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
  const sessionRailSummary = useMemo(() => summarizeSidebarSessions(threads), [threads]);
  const showSessionsSection =
    sessionThreads.length > 0 ||
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
  const liveTeamCount = runtimeTeams.filter(
    (team) => (team.status || "").trim().toLowerCase() === "active",
  ).length;
  const hasRuntimeStats =
    runtimeSessionsSummary.totalCount > 0 ||
    runtimeSessionsSummary.recoverableCount > 0 ||
    sessionRailSummary.pendingCount > 0 ||
    runtimeSessionsLoading ||
    Boolean(runtimeSessionsRefreshing);
  const sidebarLabels: WorkspaceSidebarIconLabels = {
    threadReview: t("sidebar.threadStatuses.review"),
    threadDraft: t("sidebar.threadStatuses.draft"),
    threadActive: t("sidebar.threadStatuses.active"),
    sessionError: t("sidebar.sessionStatuses.error"),
    sessionRestored: t("sidebar.sessionStatuses.restored"),
    sessionAttached: t("sidebar.sessionStatuses.attached"),
    sessionPending: t("sidebar.sessionStatuses.pending"),
  };
  const threadSessionDetails = {
    pending: t("sidebar.emptyChats.default"),
    error: t("sidebar.sessionDetails.error"),
    restored: t("sidebar.sessionDetails.restored"),
    attached: t("sidebar.sessionDetails.attached"),
  };

  useEffect(() => {
    if (!deferredQuery) {
      return;
    }

    let cancelled = false;
    queueMicrotask(() => {
      if (cancelled) {
        return;
      }

      setOpenSections((current) =>
        current.chats && current.sessions
          ? current
          : { ...current, chats: true, sessions: true },
      );
    });

    return () => {
      cancelled = true;
    };
  }, [deferredQuery]);

  useEffect(() => {
    if (!mobileOpen || !onCloseMobile) {
      return;
    }

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onCloseMobile();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [mobileOpen, onCloseMobile]);

  useEffect(() => {
    setOpenSessionDirectories((current) => {
      const knownKeys = new Set(mergedDirectoryGroups.map((group) => group.key));
      const next: Record<string, boolean> = {};
      let changed = false;

      for (const [key, value] of Object.entries(current)) {
        if (knownKeys.has(key)) {
          next[key] = value;
        } else {
          changed = true;
        }
      }

      mergedDirectoryGroups.forEach((group, index) => {
        if (typeof next[group.key] === "boolean") {
          return;
        }
        const containsSelectedThread = group.sessions.some((session) => {
          const thread = sessionThreadById.get(session.id);
          return thread?.id === selectedThreadId || thread?.sessionId === selectedThreadId;
        });
        next[group.key] = containsSelectedThread || index === 0;
        changed = true;
      });

      return changed ? next : current;
    });
  }, [mergedDirectoryGroups, selectedThreadId, sessionThreadById]);

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
          "fixed inset-y-0 left-0 z-50 flex h-full w-[min(20rem,calc(100vw-3rem))] min-h-0 flex-col overflow-hidden border-r border-[var(--border)] [background:var(--workspace-sidebar-bg)] shadow-[0_18px_48px_rgba(0,0,0,0.35)] transition-[transform,visibility] duration-200 xl:visible xl:static xl:z-auto xl:w-auto xl:translate-x-0 xl:shadow-none",
          mobileOpen
            ? "visible translate-x-0"
            : "invisible -translate-x-full",
        )}
      >
      <div
        className={cn(
          "border-b border-[var(--border)]",
          isCompact ? "px-2.5 py-2.5" : "px-3 py-3",
        )}
      >
        <div className="flex items-center justify-between gap-3">
          <Link to="/" className="flex items-center gap-3" onClick={onCloseMobile}>
            <span className="grid size-8 place-items-center rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-soft)] text-xs font-semibold text-[var(--accent-primary)]">
              AR
            </span>
            <div>
              <div className="app-text-10 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
                {t("sidebar.workspaceLabel")}
              </div>
              <div className="mt-0.5 text-sm font-semibold">{t("sidebar.appName")}</div>
            </div>
          </Link>
          <div className="flex items-center gap-1">
            <Button
              variant="ghost"
              size="icon"
              className="xl:hidden"
              onClick={onCloseMobile}
              aria-label={t("sidebar.closeNavigation")}
              title={t("sidebar.closeNavigation")}
            >
              <XIcon size={16} />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              onClick={onRefreshRuntimeTeams}
              disabled={!onRefreshRuntimeTeams}
              aria-label={t("sidebar.refreshRuntimeTeams")}
            >
              <SparklesIcon size={16} />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              onClick={onOpenSettings}
              aria-label={t("sidebar.openSettings")}
            >
              <Settings2Icon size={16} />
            </Button>
          </div>
        </div>

        <button
          type="button"
          onClick={() => onSelectThread(NEW_THREAD_ID)}
          className={cn(
            "mt-3 flex w-full items-center justify-center gap-2 rounded-[0.85rem] border px-3 text-base font-medium transition",
            isCompact ? "py-2" : "py-2.5",
            selectedThreadId === NEW_THREAD_ID
              ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] text-[var(--foreground)]"
              : "border-[var(--border)] bg-[var(--surface-softer)] text-[var(--foreground)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
          )}
        >
          <MessageSquarePlusIcon
            size={16}
            className="text-[var(--accent-primary)]"
          />
          {t("sidebar.startNewChat")}
        </button>

        {showSearch ? (
          <div
            className={cn(
              "mt-2.5 rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3",
              isCompact ? "py-2" : "py-2.5",
            )}
          >
            <div className="flex items-center gap-2.5 text-sm text-[var(--muted-foreground)]">
              <SearchIcon size={15} />
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={t("sidebar.searchPlaceholder")}
                aria-label={t("sidebar.searchPlaceholder")}
                className="w-full bg-transparent outline-none"
              />
            </div>
          </div>
        ) : null}
      </div>

      <div
        className={cn(
          "min-h-0 flex-1 overflow-y-auto",
          isCompact ? "p-2.5" : "p-3",
        )}
      >
        <div className={cn(isCompact ? "space-y-3.5" : "space-y-4")}>
          {showChatsSection ? (
          <SidebarSection
              id="chats"
              icon={MessagesSquareIcon}
              iconClassName="text-[var(--accent-primary)]"
              title={t("sidebar.sections.chats")}
              count={<Badge>{chatThreads.length}</Badge>}
              isOpen={openSections.chats}
              onToggle={toggleSection}
            >
                <div className="space-y-1">
                  {chatThreads.length > 0 ? (
                    chatThreads.map((thread) => {
                      const isActive = thread.id === selectedThreadId;
                      const sessionDescriptor = describeThreadSession(
                        thread,
                        threadSessionDetails,
                      );
                      const itemStatusIcons = [
                        getThreadWorkflowIcon(thread, sidebarLabels),
                        getSessionStatusIcon(sessionDescriptor.label, sidebarLabels),
                      ];
                      const title = [
                    thread.title,
                    ...itemStatusIcons.map((item) => item.label),
                  ].join(" · ");

                  return (
                    <button
                      key={thread.id}
                      type="button"
                      title={title}
                      onClick={() => onSelectThread(thread.id)}
                      className={cn(
                        "flex w-full items-center gap-2.5 rounded-[0.8rem] border px-2.5 py-2 text-left transition",
                        isActive
                          ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)]"
                          : "border-[var(--border)] bg-[var(--surface-softer)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
                      )}
                    >
                      <div className="min-w-0 flex-1 truncate text-base font-medium text-[var(--foreground)]">
                        {thread.title}
                      </div>
                      <div className="flex shrink-0 items-center gap-1.5">
                        {itemStatusIcons.map((item) => (
                          <SidebarStateIcon
                            key={`${thread.id}-${item.label}`}
                            spec={item}
                          />
                        ))}
                      </div>
                    </button>
                  );
                })
              ) : (
                <div className="rounded-[0.8rem] border border-dashed border-[var(--border)] px-3 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
                  {deferredQuery
                    ? t("sidebar.emptyChats.search")
                    : t("sidebar.emptyChats.default")}
                </div>
              )}
            </div>
            </SidebarSection>
          ) : null}

          {showDirectoriesSection ? (
            <SidebarSection
              id="directories"
              icon={FolderIcon}
              iconClassName="text-[var(--accent-primary)]"
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
                  <div className="inline-flex items-center gap-1.5 rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-1 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                    <LoaderCircleIcon size={12} className="animate-spin" />
                    {t("sidebar.runtimeStats.syncing")}
                  </div>
                ) : null}
                {workspaceDirectoriesError ? (
                  <div className="rounded-[0.75rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-2.5 py-2 text-xs leading-5 text-[var(--muted-foreground)]">
                    {workspaceDirectoriesError}
                  </div>
                ) : null}
                {sidebarActionError ? (
                  <div className="rounded-[0.75rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-2.5 py-2 text-xs leading-5 text-[var(--muted-foreground)]">
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
                                ? "border border-[var(--border)] bg-[var(--surface-softer)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]"
                                : "hover:bg-[var(--surface-softer)]",
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
                                    ? "text-[var(--accent-primary)]"
                                    : "text-[var(--muted-foreground)]",
                                )}
                              />
                              <span
                                className={cn(
                                  "min-w-0 flex-1 truncate text-xs font-medium",
                                  group.registered
                                    ? "text-[var(--foreground)]"
                                    : "text-[var(--muted-foreground)]",
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
                                  className="shrink-0 text-[#f59e7d]"
                                >
                                  <TriangleAlertIcon size={12} />
                                </span>
                              ) : null}
                              <span className="shrink-0 app-text-10 text-[var(--muted-foreground)]">
                                {group.sessions.length}
                              </span>
                              <ChevronDownIcon
                                size={13}
                                className={cn(
                                  "shrink-0 text-[var(--muted-foreground)] transition-transform duration-200",
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
                                  className="rounded-[0.5rem] p-1 text-[var(--muted-foreground)] transition hover:bg-[var(--surface-soft)] hover:text-[var(--foreground)] disabled:opacity-50"
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
                                  className="rounded-[0.5rem] p-1 text-[var(--muted-foreground)] transition hover:bg-[var(--surface-soft)] hover:text-[var(--foreground)]"
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
                                  className="rounded-[0.5rem] p-1 text-[var(--muted-foreground)] transition hover:bg-[var(--surface-soft)] hover:text-[#f59e7d]"
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
                            <div className="ml-3 space-y-1 border-l border-[var(--border)] pl-2">
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
                                <div className="rounded-[0.8rem] border border-dashed border-[var(--border)] px-3 py-2 text-sm leading-6 text-[var(--muted-foreground)]">
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
                  <div className="rounded-[0.8rem] border border-dashed border-[var(--border)] px-3 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
                    {t("sidebar.directories.empty")}
                  </div>
                )}
              </div>
            </SidebarSection>
          ) : null}

          {showSessionsSection ? (
            <SidebarSection
              id="sessions"
              icon={HistoryIcon}
              iconClassName="text-[var(--accent-secondary)]"
              title={t("sidebar.sections.sessions")}
              count={<Badge>{sessionThreads.length}</Badge>}
              isOpen={openSections.sessions}
              onToggle={toggleSection}
            >
              <div className="space-y-2">
                {runtimeSessionUsersLoading && sessionUserMenuItems.length === 0 ? (
                  <div className="inline-flex items-center gap-1.5 rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-1 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                    <LoaderCircleIcon size={12} className="animate-spin" />
                    {t("sidebar.sessionUsersLoading")}
                  </div>
                ) : null}
                {runtimeSessionUsersError ? (
                  <div className="rounded-[0.75rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-2.5 py-2 text-xs leading-5 text-[var(--muted-foreground)]">
                    {runtimeSessionUsersError}
                  </div>
                ) : null}
                <div className="space-y-1.5">
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
                              "flex w-full items-center gap-2 rounded-[0.8rem] border px-2.5 py-2 text-left transition",
                              isSelectedUser
                                ? "border-[var(--accent-secondary-border)] bg-[var(--accent-secondary-soft)]"
                                : "border-[var(--border)] bg-[var(--surface-softer)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
                            )}
                          >
                            <UserIcon
                              size={14}
                              className="shrink-0 text-[var(--accent-secondary)]"
                            />
                            <span className="min-w-0 flex-1 truncate text-sm font-semibold text-[var(--foreground)]">
                              {user.displayName}
                            </span>
                            {user.isDefaultUser ? (
                              <span className="shrink-0 rounded-[0.55rem] border border-[var(--border)] bg-[var(--surface-soft)] px-1.5 py-0.5 app-text-10 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                                {t("sidebar.sessionUserDefault")}
                              </span>
                            ) : null}
                            <Badge>{user.sessionCount}</Badge>
                            <ChevronDownIcon
                              size={14}
                              className={cn(
                                "shrink-0 text-[var(--muted-foreground)] transition-transform duration-200",
                                isSelectedUser ? "rotate-0" : "-rotate-90",
                              )}
                            />
                          </button>

                          {isSelectedUser ? (
                            <div className="ml-3 space-y-1 border-l border-[var(--border)] pl-2">
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
                                        className="flex w-full items-center gap-2 rounded-[0.72rem] px-2 py-1.5 text-left text-[var(--muted-foreground)] transition hover:bg-[var(--surface-softer)] hover:text-[var(--foreground)]"
                                      >
                                        <FolderIcon
                                          size={13}
                                          className="shrink-0 text-[var(--accent-primary)]"
                                        />
                                        <span className="min-w-0 flex-1 truncate text-xs font-medium">
                                          {group.fullPath
                                            ? group.label
                                            : t("sidebar.sessionDirectoryUnscoped")}
                                        </span>
                                        <span className="shrink-0 app-text-10 text-[var(--muted-foreground)]">
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
                                            const sessionStatusIcon = getSessionStatusIcon(
                                              describeThreadSession(
                                                thread ?? {
                                                  id: session.id,
                                                  title,
                                                  summary:
                                                    session.metadata?.summary ?? "",
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
                                            );

                                            return (
                                              <SidebarSessionItem
                                                key={`recoverable-${session.id}`}
                                                isActive={isActive}
                                                onCancelRename={() =>
                                                  setRenamingSessionId(null)
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
                                                session={session}
                                                statusIcon={sessionStatusIcon}
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
                                <div className="rounded-[0.8rem] border border-dashed border-[var(--border)] px-3 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
                                  {deferredQuery
                                    ? t("sidebar.emptySessions.search")
                                    : t("sidebar.emptySessions.default")}
                                </div>
                              )}
                            </div>
                          ) : null}
                        </div>
                      );
                    })
                  ) : !runtimeSessionUsersLoading ? (
                    <div className="rounded-[0.8rem] border border-dashed border-[var(--border)] px-3 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
                      {deferredQuery
                        ? t("sidebar.emptySessions.search")
                        : t("sidebar.emptySessions.default")}
                    </div>
                  ) : null}
                </div>
              </div>
            </SidebarSection>
          ) : null}

            <SidebarSection
              id="runtime"
              icon={CompassIcon}
              iconClassName="text-[var(--accent-secondary)]"
              title={t("sidebar.sections.runtime")}
              count={
                liveTeamCount > 0 ? (
                  <Badge>{t("sidebar.active", { count: liveTeamCount })}</Badge>
                ) : runtimeSessionsSummary.totalCount > 0 ? (
                  <Badge>
                    {t("sidebar.runtimeStats.sessions", {
                      count: runtimeSessionsSummary.totalCount,
                    })}
                  </Badge>
                ) : undefined
              }
              isOpen={openSections.runtime}
              onToggle={toggleSection}
            >
              <section className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
                {hasRuntimeStats ? (
                  <div className="flex flex-wrap gap-1.5 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                    {runtimeSessionsSummary.totalCount > 0 ? (
                      <span className="rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                        {t("sidebar.runtimeStats.sessions", {
                          count: runtimeSessionsSummary.totalCount,
                        })}
                      </span>
                    ) : null}
                    {runtimeSessionsSummary.recoverableCount > 0 ? (
                      <span className="rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                        {t("sidebar.runtimeStats.recoverable", {
                          count: runtimeSessionsSummary.recoverableCount,
                        })}
                      </span>
                    ) : null}
                    {sessionRailSummary.pendingCount > 0 ? (
                      <span className="rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                        {t("sidebar.runtimeStats.pending", {
                          count: sessionRailSummary.pendingCount,
                        })}
                      </span>
                    ) : null}
                  {runtimeSessionsLoading || runtimeSessionsRefreshing ? (
                    <span className="inline-flex items-center gap-1.5 rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                      <LoaderCircleIcon size={12} className="animate-spin" />
                      {t("sidebar.runtimeStats.syncing")}
                    </span>
                  ) : null}
                  </div>
                ) : null}

              {runtimeSessionsError ? (
                <div className="mt-3 rounded-[0.8rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
                  {runtimeSessionsError}
                </div>
              ) : null}

              {runtimeTeams.length > 0 ? (
                <div className="mt-3 space-y-1.5">
                  {runtimeTeams.slice(0, 4).map((team) => (
                    <div
                      key={team.id}
                      className="flex items-center justify-between rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-2.5 py-2"
                    >
                      <div className="truncate app-text-13 text-[var(--foreground)]">
                        {team.id}
                      </div>
                      <span className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                        {team.status || t("sidebar.unknown")}
                      </span>
                    </div>
                  ))}
                </div>
              ) : null}

              <Button
                variant="secondary"
                size="sm"
                className="mt-3 w-full"
                onClick={() => setRuntimeTeamsDialogOpen(true)}
              >
                {t("sidebar.openRuntimeTeamDetails")}
              </Button>
              <Link
                to="/runtime/config"
                onClick={onCloseMobile}
                className={cn(
                  buttonVariants({ variant: "secondary", size: "sm" }),
                  "mt-2 w-full",
                )}
              >
                {t("sidebar.backendConfigPage")}
              </Link>
            </section>
          </SidebarSection>
        </div>
      </div>
      {runtimeTeamsDialogOpen ? (
        <Suspense fallback={<RuntimeTeamsDialogFallback />}>
          <RuntimeTeamsDialog
            error={runtimeTeamsError}
            isLoading={runtimeTeamsLoading}
            isRefreshing={runtimeTeamsRefreshing}
            onClose={() => setRuntimeTeamsDialogOpen(false)}
            onRefresh={onRefreshRuntimeTeams}
            open={runtimeTeamsDialogOpen}
            summaries={runtimeTeamSummaries}
            teams={runtimeTeams}
          />
        </Suspense>
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
      </aside>
    </>
  );
}

function RuntimeTeamsDialogFallback() {
  return (
    <div className="fixed inset-0 z-[120] flex items-center justify-center bg-[var(--dialog-backdrop)] px-3 py-4 backdrop-blur-sm">
      <div className="rounded-[0.9rem] border border-[var(--border)] [background:var(--dialog-bg)] px-3.5 py-2.5 text-sm text-[var(--muted-foreground)] shadow-[0_12px_36px_rgba(0,0,0,0.22)]">
        Loading runtime teams panel...
      </div>
    </div>
  );
}

function buildSessionDescriptorThread(
  session: RuntimeSessionRecord,
  title: string,
): Thread {
  return {
    id: session.id,
    title,
    summary: session.metadata?.summary ?? "",
    updatedAt: session.updatedAt || session.createdAt || "",
    status: "active",
    sessionId: session.id,
    tags: ["runtime-session"],
    prompts: [],
    messages: [],
    artifacts: [],
  };
}

type InlineRenameInputProps = {
  ariaLabel: string;
  initial: string;
  onCancel: () => void;
  onSubmit: (value: string) => void;
  placeholder: string;
};

function InlineRenameInput({
  ariaLabel,
  initial,
  onCancel,
  onSubmit,
  placeholder,
}: InlineRenameInputProps) {
  const [value, setValue] = useState(initial);
  const settledRef = useRef(false);

  return (
    <input
      autoFocus
      value={value}
      aria-label={ariaLabel}
      onChange={(event) => setValue(event.target.value)}
      onClick={(event) => event.stopPropagation()}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          const trimmed = value.trim();
          if (!trimmed) {
            settledRef.current = true;
            onCancel();
            return;
          }
          settledRef.current = true;
          onSubmit(trimmed);
        } else if (event.key === "Escape") {
          event.preventDefault();
          settledRef.current = true;
          onCancel();
        }
      }}
      onBlur={() => {
        if (!settledRef.current) {
          onCancel();
        }
      }}
      placeholder={placeholder}
      spellCheck={false}
      className="w-full min-w-0 rounded-[0.55rem] border border-[var(--accent-primary-border)] bg-[var(--surface-solid)] px-2 py-1 text-sm text-[var(--foreground)] outline-none"
    />
  );
}

type SidebarSessionItemProps = {
  isActive: boolean;
  onCancelRename: () => void;
  onRenameSubmit: (sessionId: string, title: string) => void;
  onSelect: () => void;
  onStartRename: (sessionId: string, currentTitle: string) => void;
  renameLabels: {
    placeholder: string;
    rename: string;
  };
  renaming: boolean;
  session: RuntimeSessionRecord;
  statusIcon: SidebarStateIconSpec;
  title: string;
};

function SidebarSessionItem({
  isActive,
  onCancelRename,
  onRenameSubmit,
  onSelect,
  onStartRename,
  renameLabels,
  renaming,
  session,
  statusIcon,
  title,
}: SidebarSessionItemProps) {
  if (renaming) {
    return (
      <div
        className={cn(
          "flex w-full items-center gap-2 rounded-[0.72rem] border px-2 py-1 text-left transition",
          isActive
            ? "border-[var(--accent-secondary-border)] bg-[var(--accent-secondary-soft)]"
            : "border-[var(--border)] bg-[var(--surface-softer)]",
        )}
      >
        <InlineRenameInput
          ariaLabel={renameLabels.rename}
          initial={title}
          placeholder={renameLabels.placeholder}
          onCancel={onCancelRename}
          onSubmit={(value) => onRenameSubmit(session.id, value)}
        />
      </div>
    );
  }

  return (
    <div className="group/session relative">
      <button
        type="button"
        title={`${title} · ${statusIcon.label}`}
        onClick={onSelect}
        className={cn(
          "flex w-full items-center gap-2 rounded-[0.72rem] border py-1.5 pl-2 pr-7 text-left transition",
          isActive
            ? "border-[var(--accent-secondary-border)] bg-[var(--accent-secondary-soft)]"
            : "border-[var(--border)] bg-[var(--surface-softer)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
        )}
      >
        <div className="min-w-0 flex-1 truncate text-sm font-medium text-[var(--foreground)]">
          {title}
        </div>
        <SidebarStateIcon spec={statusIcon} />
      </button>
      <button
        type="button"
        aria-label={renameLabels.rename}
        title={renameLabels.rename}
        onClick={(event) => {
          event.stopPropagation();
          onStartRename(session.id, title);
        }}
        className="absolute right-1 top-1/2 -translate-y-1/2 rounded-[0.5rem] p-1 text-[var(--muted-foreground)] opacity-0 transition hover:bg-[var(--surface-soft)] hover:text-[var(--foreground)] focus-visible:opacity-100 group-hover/session:opacity-100"
      >
        <PencilIcon size={12} />
      </button>
    </div>
  );
}
