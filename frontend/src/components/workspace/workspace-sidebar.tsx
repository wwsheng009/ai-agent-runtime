import {
  CheckIcon,
  ChevronDownIcon,
  Clock3Icon,
  CompassIcon,
  HistoryIcon,
  LoaderCircleIcon,
  MessageSquarePlusIcon,
  MessagesSquareIcon,
  SearchIcon,
  Settings2Icon,
  SparklesIcon,
  TriangleAlertIcon,
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
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

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
  runtimeSessionsLoading: boolean;
  runtimeSessionsRefreshing?: boolean;
  runtimeSessionsSummary: RuntimeSessionsSummary;
  onRefreshRuntimeTeams?: () => void;
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

function getThreadWorkflowIcon(thread: Thread): SidebarStateIconSpec {
  if (thread.status === "review") {
    return {
      icon: SearchIcon,
      label: "等待复核",
      toneClassName:
        "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] text-[var(--accent-primary)]",
    };
  }

  if (thread.status === "draft") {
    return {
      icon: Clock3Icon,
      label: "草稿线程",
      toneClassName:
        "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--muted-foreground)]",
    };
  }

  return {
    icon: SparklesIcon,
    label: "活跃线程",
    toneClassName:
      "border-[var(--accent-secondary-border)] bg-[var(--accent-secondary-soft)] text-[var(--accent-secondary)]",
  };
}

function getSessionStatusIcon(
  label: ThreadSessionDescriptor["label"],
): SidebarStateIconSpec {
  if (label === "error") {
    return {
      icon: TriangleAlertIcon,
      label: "会话同步异常",
      toneClassName: "border-[#f59e7d]/24 bg-[#f59e7d]/10 text-[#f59e7d]",
    };
  }

  if (label === "restored") {
    return {
      icon: HistoryIcon,
      label: "已恢复会话",
      toneClassName: "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]",
    };
  }

  if (label === "attached") {
    return {
      icon: CheckIcon,
      label: "已附着运行时会话",
      toneClassName: "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]",
    };
  }

  return {
    icon: LoaderCircleIcon,
    label: "尚未附着会话",
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
  runtimeSessionsLoading,
  runtimeSessionsRefreshing,
  runtimeSessionsSummary,
  onRefreshRuntimeTeams,
  threads,
  selectedThreadId,
  onSelectThread,
}: WorkspaceSidebarProps) {
  const isCompact = density === "compact";
  const [query, setQuery] = useState("");
  const [runtimeTeamsDialogOpen, setRuntimeTeamsDialogOpen] = useState(false);
  const [openSections, setOpenSections] = useState<SidebarSectionState>({
    chats: true,
    sessions: true,
    runtime: true,
  });
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
  const sessionRailSummary = useMemo(() => summarizeSidebarSessions(threads), [threads]);
  const showSessionsSection =
    sessionThreads.length > 0 ||
    runtimeSessionsLoading ||
    runtimeSessionsRefreshing ||
    Boolean(runtimeSessionsError) ||
    Boolean(deferredQuery);
  const liveTeamCount = runtimeTeams.filter(
    (team) => (team.status || "").trim().toLowerCase() === "active",
  ).length;

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

  function toggleSection(section: SidebarSectionId) {
    setOpenSections((current) => ({
      ...current,
      [section]: !current[section],
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
                Workspace
              </div>
              <div className="mt-0.5 text-sm font-semibold">AI Agent Runtime</div>
            </div>
          </Link>
          <div className="flex items-center gap-1">
            <Button
              variant="ghost"
              size="icon"
              onClick={onRefreshRuntimeTeams}
              disabled={!onRefreshRuntimeTeams}
              aria-label="Refresh runtime teams"
            >
              <SparklesIcon size={16} />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              onClick={onOpenSettings}
              aria-label="Open settings"
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
          Start new chat
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
              placeholder="Search threads"
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
            title="Local chats"
            count={<Badge>{chatThreads.length}</Badge>}
            isOpen={openSections.chats}
            onToggle={toggleSection}
          >
            <div className="space-y-1">
              {chatThreads.length > 0 ? (
                chatThreads.map((thread) => {
                  const isActive = thread.id === selectedThreadId;
                  const sessionDescriptor = describeThreadSession(thread);
                  const itemStatusIcons = [
                    getThreadWorkflowIcon(thread),
                    getSessionStatusIcon(sessionDescriptor.label),
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
                    ? "No local chats match the current search."
                    : "Local-only chats will appear here before runtime session attachment."}
                </div>
              )}
            </div>
          </SidebarSection>

          {showSessionsSection ? (
            <SidebarSection
              id="sessions"
              icon={HistoryIcon}
              iconClassName="text-[var(--accent-secondary)]"
              title="Sessions"
              count={<Badge>{sessionThreads.length}</Badge>}
              isOpen={openSections.sessions}
              onToggle={toggleSection}
            >
              <div className="space-y-1">
                {sessionThreads.length > 0 ? (
                  sessionThreads.map((thread) => {
                    const isActive = thread.id === selectedThreadId;
                    const sessionDescriptor = describeThreadSession(thread);
                    const sessionStatusIcon = getSessionStatusIcon(
                      sessionDescriptor.label,
                    );

                    return (
                      <button
                        key={`recoverable-${thread.id}`}
                        type="button"
                        title={`${thread.title} · ${sessionStatusIcon.label}`}
                        onClick={() => onSelectThread(thread.id)}
                        className={cn(
                          "flex w-full items-center gap-2.5 rounded-[0.8rem] border px-2.5 py-2 text-left transition",
                          isActive
                            ? "border-[var(--accent-secondary-border)] bg-[var(--accent-secondary-soft)]"
                            : "border-[var(--border)] bg-[var(--surface-softer)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
                        )}
                      >
                        <div className="min-w-0 flex-1 truncate text-base font-semibold text-[var(--foreground)]">
                          {thread.title}
                        </div>
                        <SidebarStateIcon spec={sessionStatusIcon} />
                      </button>
                    );
                  })
                ) : (
                  <div className="rounded-[0.8rem] border border-dashed border-[var(--border)] px-3 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
                    {deferredQuery
                      ? "No sessions match the current search."
                      : "Recoverable runtime sessions will appear here after loading."}
                  </div>
                )}
              </div>
            </SidebarSection>
          ) : null}

          <SidebarSection
            id="runtime"
            icon={CompassIcon}
            iconClassName="text-[var(--accent-secondary)]"
            title="Runtime overview"
            count={<Badge>{liveTeamCount} active</Badge>}
            isOpen={openSections.runtime}
            onToggle={toggleSection}
          >
            <section className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
              <div className="flex flex-wrap gap-1.5 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                <span className="rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                  {runtimeSessionsSummary.totalCount} sessions
                </span>
                <span className="rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                  {runtimeSessionsSummary.recoverableCount} recoverable
                </span>
                <span className="rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                  {sessionRailSummary.pendingCount} pending
                </span>
                {runtimeSessionsLoading || runtimeSessionsRefreshing ? (
                  <span className="inline-flex items-center gap-1.5 rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5">
                    <LoaderCircleIcon size={12} className="animate-spin" />
                    syncing
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
                      {team.status || "unknown"}
                    </span>
                  </div>
                ))}
                {!runtimeTeamsLoading && runtimeTeams.length === 0 ? (
                  <div className="rounded-[0.8rem] border border-dashed border-[var(--border)] px-3 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
                    No runtime teams available.
                  </div>
                ) : null}
              </div>

              <Button
                variant="secondary"
                size="sm"
                className="mt-3 w-full"
                onClick={() => setRuntimeTeamsDialogOpen(true)}
              >
                Open runtime team details
              </Button>
              <Link
                to="/runtime/config"
                className={cn(
                  buttonVariants({ variant: "secondary", size: "sm" }),
                  "mt-2 w-full",
                )}
              >
                后端配置页
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
        正在加载 runtime teams 面板…
      </div>
    </div>
  );
}
