import {
  lazy,
  Suspense,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import {
  ArrowUpRightIcon,
  BotIcon,
  Code2Icon,
  FileSearchIcon,
  ListTodoIcon,
  ShieldCheckIcon,
} from "lucide-react";

import { MessageBacktrackDialog } from "@/components/workspace/message-backtrack-dialog";
import { MessageComposer } from "@/components/workspace/message-composer";
import { MessageList } from "@/components/workspace/message-list";
import { type SettingsSectionId } from "@/components/workspace/settings";
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";
import {
  getThreadStatusLabel,
  getThreadTopbarSubtitle,
  getThreadTransportLabel,
} from "@/components/workspace/workspace-shell-shared";
import { WorkspaceShellTopbar } from "@/components/workspace/workspace-shell-topbar";
import { WorkspaceSidebar } from "@/components/workspace/workspace-sidebar";
import { useAppSettings } from "@/core/settings";
import { type Artifact, type Thread } from "@/data/mock";
import { type RuntimeSessionsSummary } from "@/hooks/workspace/use-runtime-sessions-data";
import type { SessionBacktrackDialogState } from "@/hooks/workspace/use-session-backtrack";
import { type RuntimeClientIdentity } from "@/lib/runtime-client";
import {
  type RuntimeSessionBacktrackMode,
  type RuntimeSessionRecord,
  type RuntimeSessionUserSummary,
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

const SettingsDialog = lazy(() =>
  import("@/components/workspace/settings/settings-dialog").then((module) => ({
    default: module.SettingsDialog,
  })),
);
const ArtifactDetailDialog = lazy(() =>
  import("@/components/workspace/artifact-detail-dialog").then((module) => ({
    default: module.ArtifactDetailDialog,
  })),
);
const ArtifactPanel = lazy(() =>
  import("@/components/workspace/artifact-panel").then((module) => ({
    default: module.ArtifactPanel,
  })),
);

type WorkspaceShellProps = {
  threads: Thread[];
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
  runtimeClient: RuntimeClientIdentity;
  selectedRuntimeSessionUserId: string;
  selectedThread: Thread;
  selectedArtifact: Artifact | null;
  selectedArtifactId: string | null;
  draft: string;
  isResponding: boolean;
  modelOptions: string[];
  onDraftChange: (value: string) => void;
  onModelChange: (value: string) => void;
  onProviderChange: (value: string) => void;
  onSelectArtifact: (artifactId: string) => void;
  onSelectThread: (threadId: string) => void;
  onRefreshRuntimeTeams?: () => void;
  onSelectRuntimeSessionUser: (userId: string) => void;
  onResetRuntimeClientIdentity: () => void;
  onStopResponding: () => void;
  onSubmit: () => void;
  onBacktrackToMessage?: (
    messageId: string,
    mode?: "conversation" | "both",
    options?: { editPrompt?: string },
  ) => void;
  backtrackDialog?: SessionBacktrackDialogState;
  backtrackError?: string | null;
  backtrackNotice?: string | null;
  backtrackPendingMessageId?: string | null;
  backtrackNavigationActive?: boolean;
  backtrackSelectedMessageId?: string | null;
  canBacktrack?: boolean;
  onCloseBacktrackDialog?: () => void;
  onConfirmBacktrack?: () => void;
  onBacktrackEditPromptChange?: (value: string) => void;
  onBacktrackModeChange?: (mode: RuntimeSessionBacktrackMode) => void;
  onBacktrackPrefillChange?: (prefill: boolean) => void;
  onSelectBacktrackNavigationMessage?: (messageId: string) => void;
  providerOptions: string[];
  runtimeModelsError: string | null;
  runtimeModelsLoading: boolean;
  selectedModel: string;
  selectedProvider: string;
};

export function WorkspaceShell({
  threads,
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
  runtimeClient,
  selectedRuntimeSessionUserId,
  selectedThread,
  selectedArtifact,
  selectedArtifactId,
  draft,
  isResponding,
  modelOptions,
  onDraftChange,
  onModelChange,
  onProviderChange,
  onSelectArtifact,
  onSelectThread,
  onRefreshRuntimeTeams,
  onSelectRuntimeSessionUser,
  onResetRuntimeClientIdentity,
  onStopResponding,
  onSubmit,
  onBacktrackToMessage,
  backtrackDialog,
  backtrackError = null,
  backtrackNotice = null,
  backtrackPendingMessageId = null,
  backtrackNavigationActive = false,
  backtrackSelectedMessageId = null,
  canBacktrack = false,
  onCloseBacktrackDialog,
  onConfirmBacktrack,
  onBacktrackEditPromptChange,
  onBacktrackModeChange,
  onBacktrackPrefillChange,
  onSelectBacktrackNavigationMessage,
  providerOptions,
  runtimeModelsError,
  runtimeModelsLoading,
  selectedModel,
  selectedProvider,
}: WorkspaceShellProps) {
  const { settings } = useAppSettings();
  const { t } = useTranslation("workspace");
  const isNewThread = selectedThread.id === NEW_THREAD_ID;
  const isCompact = settings.workspace.density === "compact";
  const newThreadSuggestions = [
    {
      key: "analyze",
      icon: FileSearchIcon,
      title: t("shell.suggestions.analyze.title"),
      description: t("shell.suggestions.analyze.description"),
      prompt: t("shell.suggestions.analyze.prompt"),
    },
    {
      key: "implement",
      icon: Code2Icon,
      title: t("shell.suggestions.implement.title"),
      description: t("shell.suggestions.implement.description"),
      prompt: t("shell.suggestions.implement.prompt"),
    },
    {
      key: "review",
      icon: ShieldCheckIcon,
      title: t("shell.suggestions.review.title"),
      description: t("shell.suggestions.review.description"),
      prompt: t("shell.suggestions.review.prompt"),
    },
    {
      key: "plan",
      icon: ListTodoIcon,
      title: t("shell.suggestions.plan.title"),
      description: t("shell.suggestions.plan.description"),
      prompt: t("shell.suggestions.plan.prompt"),
    },
  ];
  const composerOverlayRef = useRef<HTMLDivElement | null>(null);
  const [artifactDialogOpen, setArtifactDialogOpen] = useState(false);
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  const [settingsDialogOpen, setSettingsDialogOpen] = useState(false);
  const [settingsSection, setSettingsSection] =
    useState<SettingsSectionId>("appearance");
  const [artifactRailManualOpen, setArtifactRailManualOpen] = useState(
    Boolean(settings.workspace.autoOpenArtifacts),
  );
  const artifactRailOpen = !isNewThread && artifactRailManualOpen;

  const transportLabel = getThreadTransportLabel(selectedThread, {
    live: t("topbar.threadTransport.live"),
    error: t("topbar.threadTransport.error"),
    seeded: t("topbar.threadTransport.seeded"),
  });
  const threadStatusLabel = getThreadStatusLabel(selectedThread, {
    sessionAttached: t("topbar.threadStatus.sessionAttached"),
    previewThread: t("topbar.threadStatus.previewThread"),
    newThread: t("topbar.threadStatus.newThread"),
  });
  const threadSubtitle = getThreadTopbarSubtitle(selectedThread, transportLabel, {
    needsRestoreWithSession: (sessionId) =>
      t("topbar.subtitle.needsRestoreWithSession", { sessionId }),
    needsRestore: t("topbar.subtitle.needsRestore"),
    viaSource: (transportLabelValue, source) =>
      t("topbar.subtitle.viaSource", {
        transportLabel: transportLabelValue,
        source,
      }),
    session: (sessionId) => t("topbar.subtitle.session", { sessionId }),
  });

  const liveTeamCount = runtimeTeams.filter(
    (team) => (team.status || "").trim().toLowerCase() === "active",
  ).length;
  const [composerOverlayHeight, setComposerOverlayHeight] = useState(220);

  useEffect(() => {
    if (isNewThread) {
      return;
    }

    const node = composerOverlayRef.current;
    if (!node || typeof ResizeObserver === "undefined") {
      return;
    }

    const observer = new ResizeObserver(() => {
      const nextHeight = Math.ceil(node.getBoundingClientRect().height);
      setComposerOverlayHeight((currentHeight) =>
        currentHeight === nextHeight ? currentHeight : nextHeight,
      );
    });

    observer.observe(node);

    return () => {
      observer.disconnect();
    };
  }, [isNewThread]);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key === ",") {
        event.preventDefault();
        setSettingsSection("appearance");
        setSettingsDialogOpen(true);
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, []);

  useEffect(() => {
    setArtifactRailManualOpen(settings.workspace.autoOpenArtifacts);
  }, [settings.workspace.autoOpenArtifacts]);

  const messageListStyle: CSSProperties | undefined = isNewThread
    ? undefined
    : {
        paddingBottom: `calc(${composerOverlayHeight}px + 0.75rem)`,
        scrollPaddingBottom: `calc(${composerOverlayHeight}px + 0.75rem)`,
      };

  function openSettings(section: SettingsSectionId = "appearance") {
    setSettingsSection(section);
    setSettingsDialogOpen(true);
  }

  function handleOpenArtifact(artifactId: string) {
    onSelectArtifact(artifactId);
    setArtifactDialogOpen(true);
  }

  return (
    <div className="h-screen overflow-hidden [background:var(--workspace-shell-bg)] text-[var(--foreground)]">
      <div
        className={cn(
          "grid h-full min-h-0 grid-cols-1 gap-0",
          artifactRailOpen
            ? "xl:grid-cols-[16rem_minmax(0,1fr)_18rem]"
            : "xl:grid-cols-[16rem_minmax(0,1fr)]",
        )}
      >
        <WorkspaceSidebar
          density={settings.workspace.density}
          mobileOpen={mobileSidebarOpen}
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
          runtimeSessionDefaultUserId={runtimeSessionDefaultUserId}
          runtimeSessionUsers={runtimeSessionUsers}
          runtimeSessionUsersError={runtimeSessionUsersError}
          runtimeSessionUsersLoading={runtimeSessionUsersLoading}
          selectedRuntimeSessionUserId={selectedRuntimeSessionUserId}
          onSelectRuntimeSessionUser={onSelectRuntimeSessionUser}
          onRefreshRuntimeTeams={onRefreshRuntimeTeams}
          threads={threads}
          selectedThreadId={selectedThread.id}
          onSelectThread={(threadId) => {
            setMobileSidebarOpen(false);
            onSelectThread(threadId);
          }}
        />

        <section
          id="workspace-preview"
          className="relative flex h-full min-h-0 flex-col overflow-hidden [background:var(--workspace-main-bg)]"
        >
          <WorkspaceShellTopbar
            artifactRailOpen={artifactRailOpen}
            density={settings.workspace.density}
            isNewThread={isNewThread}
            liveTeamCount={liveTeamCount}
            onOpenSidebar={() => setMobileSidebarOpen(true)}
            onOpenSettings={() => openSettings("appearance")}
            onToggleArtifactRail={() => setArtifactRailManualOpen((current) => !current)}
            selectedThread={selectedThread}
            threadSubtitle={threadSubtitle}
            threadStatusLabel={threadStatusLabel}
            transportLabel={transportLabel}
          />

          <div
            className={cn(
              "flex min-h-0 flex-1 justify-center overflow-hidden",
              isNewThread
                ? "items-center px-3 pb-4 pt-14 sm:px-4"
                : isCompact
                  ? "pt-[2.95rem]"
                  : "pt-[3.2rem]",
            )}
          >
            <div
              className={cn(
                "relative flex h-full min-h-0 w-full flex-col",
                isNewThread ? "max-w-[48rem]" : null,
              )}
            >
              {!isNewThread ? (
                <div className="min-h-0 flex-1 overflow-hidden">
                  <MessageList
                    artifacts={selectedThread.artifacts}
                    backtrackError={backtrackError}
                    backtrackNotice={backtrackNotice}
                    backtrackPendingMessageId={backtrackPendingMessageId}
                    backtrackNavigationActive={backtrackNavigationActive}
                    backtrackSelectedMessageId={backtrackSelectedMessageId}
                    canBacktrack={canBacktrack}
                    className={cn(
                      "h-full px-3 sm:px-4 lg:px-5",
                      isCompact ? "pt-3" : "pt-4",
                    )}
                    contentClassName={cn(
                      "max-w-[50rem]",
                      isCompact ? "gap-4" : "gap-6",
                    )}
                    isResponding={isResponding}
                    messages={selectedThread.messages}
                    onBacktrackToMessage={onBacktrackToMessage}
                    onSelectBacktrackNavigationMessage={onSelectBacktrackNavigationMessage}
                    onSelectArtifact={handleOpenArtifact}
                    style={messageListStyle}
                  />
                </div>
              ) : (
                <div className="mx-auto flex w-full max-w-[46rem] flex-1 flex-col justify-center pb-4">
                  <div className="text-center">
                    <div className="mx-auto grid size-11 place-items-center rounded-[1rem] border border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] text-[var(--accent-primary)] shadow-[0_8px_24px_var(--accent-primary-shadow)]">
                      <BotIcon size={20} />
                    </div>
                    <h1 className="mt-3 text-[1.45rem] font-semibold tracking-[-0.03em] text-[var(--foreground)] sm:text-[1.7rem]">
                      {t("shell.newChatTitle")}
                    </h1>
                  </div>
                  <div className="mt-5 grid grid-cols-2 gap-2 sm:gap-3">
                    {newThreadSuggestions.map((suggestion) => {
                      const SuggestionIcon = suggestion.icon;

                      return (
                        <button
                          key={suggestion.key}
                          type="button"
                          onClick={() => onDraftChange(suggestion.prompt)}
                          className="group flex min-h-[5.5rem] items-start gap-3 rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-3 text-left transition hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)] sm:px-3.5"
                        >
                          <span className="mt-0.5 grid size-8 shrink-0 place-items-center rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--accent-secondary)]">
                            <SuggestionIcon size={15} />
                          </span>
                          <span className="min-w-0 flex-1">
                            <span className="flex items-center justify-between gap-2 text-sm font-semibold text-[var(--foreground)]">
                              {suggestion.title}
                              <ArrowUpRightIcon
                                size={13}
                                className="shrink-0 text-[var(--muted-foreground)] transition group-hover:-translate-y-0.5 group-hover:translate-x-0.5 group-hover:text-[var(--foreground)]"
                              />
                            </span>
                            <span className="mt-1 block text-xs leading-5 text-[var(--muted-foreground)]">
                              {suggestion.description}
                            </span>
                          </span>
                        </button>
                      );
                    })}
                  </div>
                </div>
              )}

              {!isNewThread ? (
                <div
                  aria-hidden="true"
                  className="pointer-events-none absolute inset-x-0 bottom-0 z-20 px-3 sm:px-4 lg:px-5"
                >
                  <div className="mx-auto h-16 w-full max-w-[54rem] [background:var(--workspace-fade-overlay)] blur-lg" />
                </div>
              ) : null}

              <div
                ref={composerOverlayRef}
                className={cn(
                  "pointer-events-none z-30 px-3 sm:px-4 lg:px-5",
                  isNewThread
                    ? "relative inset-auto mx-auto w-full max-w-[50rem] pb-0"
                    : "absolute inset-x-0 bottom-0 pb-3",
                )}
              >
                <div className="pointer-events-auto mx-auto w-full max-w-[50rem]">
                  <MessageComposer
                    density={settings.workspace.density}
                    draft={draft}
                    hasSession={Boolean(selectedThread.sessionId)}
                    isNewThread={isNewThread}
                    isResponding={isResponding}
                    modelOptions={modelOptions}
                    selectedArtifactCount={selectedThread.artifacts.length}
                    onModelChange={onModelChange}
                    onProviderChange={onProviderChange}
                    providerOptions={providerOptions}
                    runtimeModelsError={runtimeModelsError}
                    runtimeModelsLoading={runtimeModelsLoading}
                    selectedModel={selectedModel}
                    selectedProvider={selectedProvider}
                    transport={selectedThread.transport}
                    onDraftChange={onDraftChange}
                    onStop={onStopResponding}
                    onSubmit={onSubmit}
                  />
                </div>
              </div>
            </div>
          </div>
        </section>

        {artifactRailOpen && !isNewThread ? (
          <Suspense fallback={<ArtifactPanelFallback message={t("shell.loadingArtifactPanel")} />}>
            <ArtifactPanel
              artifacts={selectedThread.artifacts}
              lastRuntimeEventType={selectedThread.lastRuntimeEventType}
              runtimeEventCount={selectedThread.runtimeEventCount}
              selectedArtifactId={selectedArtifactId}
              sessionId={selectedThread.sessionId}
              onOpenArtifact={handleOpenArtifact}
            />
          </Suspense>
        ) : null}
      </div>
      {artifactDialogOpen && selectedArtifact ? (
        <Suspense fallback={<ArtifactDialogFallback message={t("shell.loadingArtifactDetails")} />}>
          <ArtifactDetailDialog
            artifact={selectedArtifact}
            onClose={() => setArtifactDialogOpen(false)}
            open={artifactDialogOpen}
          />
        </Suspense>
      ) : null}
      {settingsDialogOpen ? (
        <Suspense fallback={<SettingsDialogFallback message={t("shell.loadingSettingsPanel")} />}>
          <SettingsDialog
            defaultSection={settingsSection}
            modelOptions={modelOptions}
            onClose={() => setSettingsDialogOpen(false)}
            onModelChange={onModelChange}
            onProviderChange={onProviderChange}
            open={settingsDialogOpen}
            providerOptions={providerOptions}
            runtimeModelsError={runtimeModelsError}
            runtimeModelsLoading={runtimeModelsLoading}
            runtimeSessionsSummary={runtimeSessionsSummary}
            runtimeClient={runtimeClient}
            runtimeTeams={runtimeTeams}
            onResetRuntimeClientIdentity={onResetRuntimeClientIdentity}
            selectedModel={selectedModel}
            selectedProvider={selectedProvider}
          />
        </Suspense>
      ) : null}
      {backtrackDialog?.open &&
      onCloseBacktrackDialog &&
      onConfirmBacktrack &&
      onBacktrackEditPromptChange &&
      onBacktrackModeChange &&
      onBacktrackPrefillChange ? (
        <MessageBacktrackDialog
          onApply={onConfirmBacktrack}
          onClose={onCloseBacktrackDialog}
          onEditPromptChange={onBacktrackEditPromptChange}
          onModeChange={onBacktrackModeChange}
          onPrefillChange={onBacktrackPrefillChange}
          state={backtrackDialog}
        />
      ) : null}
    </div>
  );
}

function SettingsDialogFallback({ message }: { message: string }) {
  return (
    <div className="fixed inset-0 z-[120] flex items-center justify-center bg-[var(--dialog-backdrop)] px-3 py-4 backdrop-blur-sm">
      <div className="rounded-[0.9rem] border border-[var(--border)] [background:var(--dialog-bg)] px-3.5 py-2.5 text-sm text-[var(--muted-foreground)] shadow-[0_12px_36px_rgba(0,0,0,0.22)]">
        {message}
      </div>
    </div>
  );
}

function ArtifactPanelFallback({ message }: { message: string }) {
  return (
    <aside className="hidden h-full min-h-0 flex-col overflow-hidden border-l border-white/8 [background:var(--workspace-sidebar-bg)] xl:flex">
      <div className="flex h-full items-center justify-center px-4 text-sm text-[var(--muted-foreground)]">
        {message}
      </div>
    </aside>
  );
}

function ArtifactDialogFallback({ message }: { message: string }) {
  return (
    <div className="fixed inset-0 z-[130] flex items-center justify-center bg-[var(--dialog-backdrop)] px-3 py-4 backdrop-blur-sm">
      <div className="rounded-[0.9rem] border border-[var(--border)] [background:var(--dialog-bg)] px-3.5 py-2.5 text-sm text-[var(--muted-foreground)] shadow-[0_12px_36px_rgba(0,0,0,0.22)]">
        {message}
      </div>
    </div>
  );
}
