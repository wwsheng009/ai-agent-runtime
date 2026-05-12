import {
  CheckIcon,
  ChevronDownIcon,
  Clock3Icon,
  CompassIcon,
  FolderIcon,
  HistoryIcon,
  LoaderCircleIcon,
  MessageSquarePlusIcon,
  MessagesSquareIcon,
  SearchIcon,
  Settings2Icon,
  SparklesIcon,
  TriangleAlertIcon,
  UserIcon,
  type LucideIcon,
} from "lucide-react";
import {
  lazy,
  Suspense,
  useDeferredValue,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { Link } from "react-router-dom";

import {
  describeThreadSession,
  groupRuntimeSessionsByDirectory,
  summarizeSidebarSessions,
  type ThreadSessionDescriptor,
} from "@/components/workspace/workspace-sidebar-shared";
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
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

const RuntimeTeamsDialog = lazy(() =>
  import("@/components/workspace/runtime-teams-dialog").then((module) => ({
    default: module.RuntimeTeamsDialog,
  })),
);

type WorkspaceSidebarProps = {
  density: "comfortable" | "compact";
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
  threads: Thread[];
  selectedThreadId: string;
  onSelectThread: (threadId: string) => void;
};

type SidebarSectionId = "chats" | "sessions" | "runtime";

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
  onToggle,
  title,
}: SidebarSectionProps) {
  return (
    <section>
      <button
        type="button"
        onClick={() => onToggle(id)}
        aria-expanded={isOpen}
        className="mb-2 flex w-full items-center justify-between gap-3 rounded-[0.7rem] px-1.5 py-1 text-left transition hover:bg-[var(--surface-softer)]"
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

      {isOpen ? children : null}
    </section>
  );
}

export function WorkspaceSidebar({
  density,
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
  threads,
  selectedThreadId,
  onSelectThread,
}: WorkspaceSidebarProps) {
  const { t } = useTranslation("workspace");
  const isCompact = density === "compact";
  const [query, setQuery] = useState("");
  const [runtimeTeamsDialogOpen, setRuntimeTeamsDialogOpen] = useState(false);
  const [openSections, setOpenSections] = useState<SidebarSectionState>({
    chats: true,
    sessions: true,
    runtime: true,
  });
  const [openSessionDirectories, setOpenSessionDirectories] = useState<
    Record<string, boolean>
  >({});
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
    if (selectedUserId && !seen.has(selectedUserId)) {
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
  const sessionDirectoryGroups = useMemo(
    () => groupRuntimeSessionsByDirectory(runtimeSessions),
    [runtimeSessions],
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
  const liveTeamCount = runtimeTeams.filter(
    (team) => (team.status || "").trim().toLowerCase() === "active",
  ).length;
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
    setOpenSessionDirectories((current) => {
      const knownKeys = new Set(sessionDirectoryGroups.map((group) => group.key));
      const next: Record<string, boolean> = {};
      let changed = false;

      for (const [key, value] of Object.entries(current)) {
        if (knownKeys.has(key)) {
          next[key] = value;
        } else {
          changed = true;
        }
      }

      sessionDirectoryGroups.forEach((group, index) => {
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
  }, [selectedThreadId, sessionDirectoryGroups, sessionThreadById]);

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

  return (
    <aside className="hidden h-full min-h-0 flex-col overflow-hidden border-r border-[var(--border)] [background:var(--workspace-sidebar-bg)] xl:flex">
      <div
        className={cn(
          "border-b border-[var(--border)]",
          isCompact ? "px-2.5 py-2.5" : "px-3 py-3",
        )}
      >
        <div className="flex items-center justify-between gap-3">
          <Link to="/" className="flex items-center gap-3">
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
              className="w-full bg-transparent outline-none"
            />
          </div>
        </div>
      </div>

      <div
        className={cn(
          "min-h-0 flex-1 overflow-y-auto",
          isCompact ? "p-2.5" : "p-3",
        )}
      >
        <div className={cn(isCompact ? "space-y-3.5" : "space-y-4")}>
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
                                              <button
                                                key={`recoverable-${session.id}`}
                                                type="button"
                                                title={`${title} · ${sessionStatusIcon.label}`}
                                                onClick={() => onSelectThread(thread?.id ?? session.id)}
                                                className={cn(
                                                  "flex w-full items-center gap-2 rounded-[0.72rem] border px-2 py-1.5 text-left transition",
                                                  isActive
                                                    ? "border-[var(--accent-secondary-border)] bg-[var(--accent-secondary-soft)]"
                                                    : "border-[var(--border)] bg-[var(--surface-softer)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
                                                )}
                                              >
                                                <div className="min-w-0 flex-1 truncate text-sm font-medium text-[var(--foreground)]">
                                                  {title}
                                                </div>
                                                <SidebarStateIcon spec={sessionStatusIcon} />
                                              </button>
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
              count={<Badge>{t("sidebar.active", { count: liveTeamCount })}</Badge>}
              isOpen={openSections.runtime}
              onToggle={toggleSection}
            >
              <section className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
                <div className="flex flex-wrap gap-1.5 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                  <span className="rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                  {t("sidebar.runtimeStats.sessions", {
                    count: runtimeSessionsSummary.totalCount,
                  })}
                  </span>
                  <span className="rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                  {t("sidebar.runtimeStats.recoverable", {
                    count: runtimeSessionsSummary.recoverableCount,
                  })}
                  </span>
                  <span className="rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                  {t("sidebar.runtimeStats.pending", {
                    count: sessionRailSummary.pendingCount,
                  })}
                  </span>
                  {runtimeSessionsLoading || runtimeSessionsRefreshing ? (
                    <span className="inline-flex items-center gap-1.5 rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                      <LoaderCircleIcon size={12} className="animate-spin" />
                      {t("sidebar.runtimeStats.syncing")}
                    </span>
                  ) : null}
                </div>

              {runtimeSessionsError ? (
                <div className="mt-3 rounded-[0.8rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
                  {runtimeSessionsError}
                </div>
              ) : null}

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
                {!runtimeTeamsLoading && runtimeTeams.length === 0 ? (
                  <div className="rounded-[0.8rem] border border-dashed border-[var(--border)] px-3 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
                    {t("sidebar.runtimeTeamsUnavailable")}
                  </div>
                ) : null}
              </div>

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
    </aside>
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
