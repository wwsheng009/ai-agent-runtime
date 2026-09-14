// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useEffect, useRef, useState, type CSSProperties } from "react";
import { Code2Icon, FileSearchIcon, ListTodoIcon, ShieldCheckIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { type SettingsSectionId } from "@/components/workspace/settings";
import {
  getThreadStatusLabel,
  getThreadTopbarSubtitle,
  getThreadTransportLabel,
} from "@/components/workspace/workspace-shell-shared";
import { useAppSettings } from "@/core/settings";
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";
import { cn } from "@/lib/utils";

import { WorkspaceMainSection } from "./workspace-shell/main-section";
import { WorkspaceOverlaysSection } from "./workspace-shell/overlays-section";
import { WorkspaceRightRailSection } from "./workspace-shell/right-rail-section";
import { WorkspaceSidebarSection } from "./workspace-shell/sidebar-section";
import { type WorkspaceShellProps } from "./workspace-shell/types";

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
  workspaceDirectories,
  workspaceDirectoriesError,
  workspaceDirectoriesLoading,
  workspaceDirectoriesRefreshing,
  onAddWorkspaceDirectory,
  onRenameWorkspaceDirectory,
  onRemoveWorkspaceDirectory,
  onRetryConnection,
  onCreateSessionInDirectory,
  onRenameRuntimeSession,
  onArchiveRuntimeSession,
  onRestoreRuntimeSession,
  onForkRuntimeSession,
  onDeleteRuntimeSession,
  sessionActivity,
  runtimeClient,
  selectedRuntimeSessionUserId,
  selectedThread,
  selectedArtifact,
  selectedArtifactId,
  composerAttachments,
  connectionStatus = null,
  draft,
  isResponding,
  modelOptions,
  phase,
  reasoningEffortDefault,
  reasoningEffortError,
  reasoningEffortOptions,
  onDraftChange,
  onModelChange,
  onProviderChange,
  onReasoningEffortChange,
  onSelectArtifact,
  onSelectThread,
  onRefreshRuntimeTeams,
  onSelectRuntimeSessionUser,
  onResetRuntimeClientIdentity,
  onStopResponding,
  onSubmit,
  pendingInteraction = null,
  onResolvePendingApproval,
  onAnswerPendingQuestion,
  planActionPending,
  planNotesDraft,
  onPlanNotesChange,
  onPlanDecision,
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
  selectedReasoningEffort,
  trajectoryStore = null,
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
  const [viewMode, setViewMode] = useState<"chat" | "trajectory">("chat");
  const [settingsSection, setSettingsSection] =
    useState<SettingsSectionId>("appearance");
  const [artifactRailManualOpen, setArtifactRailManualOpen] = useState(
    Boolean(settings.workspace.autoOpenArtifacts),
  );
  const artifactRailOpen = !isNewThread && artifactRailManualOpen;
  // 右侧栏只要「会话用量」或 artifact 面板其一可见就占位，避免空列留白。
  const rightRailVisible =
    !isNewThread &&
    (artifactRailOpen || Boolean(selectedThread.sessionId?.trim()));

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
    // P0-2 机械搬迁：保留原「autoOpenArtifacts 变化即同步刷新 artifact 栏开合」语义。
    // eslint-disable-next-line react-hooks/set-state-in-effect
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
    <div className="h-screen overflow-hidden [background:var(--workspace-shell-bg)] text-foreground">
      <div
        className={cn(
          "grid h-full min-h-0 grid-cols-1 gap-0",
          rightRailVisible
            ? "xl:grid-cols-[16rem_minmax(0,1fr)_18rem]"
            : "xl:grid-cols-[16rem_minmax(0,1fr)]",
        )}
      >
        <WorkspaceSidebarSection
          density={settings.workspace.density}
          mobileOpen={mobileSidebarOpen}
          onAddWorkspaceDirectory={onAddWorkspaceDirectory}
          onCreateSessionInDirectory={onCreateSessionInDirectory}
          onRefreshRuntimeTeams={onRefreshRuntimeTeams}
          onArchiveRuntimeSession={onArchiveRuntimeSession}
          onDeleteRuntimeSession={onDeleteRuntimeSession}
          onForkRuntimeSession={onForkRuntimeSession}
          onRemoveWorkspaceDirectory={onRemoveWorkspaceDirectory}
          onRenameRuntimeSession={onRenameRuntimeSession}
          onRestoreRuntimeSession={onRestoreRuntimeSession}
          onRenameWorkspaceDirectory={onRenameWorkspaceDirectory}
          onSelectRuntimeSessionUser={onSelectRuntimeSessionUser}
          onSelectThread={onSelectThread}
          openSettings={openSettings}
          runtimeSessionDefaultUserId={runtimeSessionDefaultUserId}
          runtimeSessionUsers={runtimeSessionUsers}
          runtimeSessionUsersError={runtimeSessionUsersError}
          runtimeSessionUsersLoading={runtimeSessionUsersLoading}
          runtimeSessions={runtimeSessions}
          runtimeSessionsError={runtimeSessionsError}
          runtimeSessionsLoading={runtimeSessionsLoading}
          runtimeSessionsRefreshing={runtimeSessionsRefreshing}
          runtimeSessionsSummary={runtimeSessionsSummary}
          runtimeTeamSummaries={runtimeTeamSummaries}
          runtimeTeams={runtimeTeams}
          runtimeTeamsError={runtimeTeamsError}
          runtimeTeamsLoading={runtimeTeamsLoading}
          runtimeTeamsRefreshing={runtimeTeamsRefreshing}
          selectedRuntimeSessionUserId={selectedRuntimeSessionUserId}
          selectedThread={selectedThread}
          sessionActivity={sessionActivity}
          setMobileSidebarOpen={setMobileSidebarOpen}
          threads={threads}
          workspaceDirectories={workspaceDirectories}
          workspaceDirectoriesError={workspaceDirectoriesError}
          workspaceDirectoriesLoading={workspaceDirectoriesLoading}
          workspaceDirectoriesRefreshing={workspaceDirectoriesRefreshing}
        />
        <WorkspaceMainSection
          artifactRailOpen={artifactRailOpen}
          backtrackError={backtrackError}
          backtrackNavigationActive={backtrackNavigationActive}
          backtrackNotice={backtrackNotice}
          backtrackPendingMessageId={backtrackPendingMessageId}
          backtrackSelectedMessageId={backtrackSelectedMessageId}
          canBacktrack={canBacktrack}
          composerAttachments={composerAttachments}
          composerOverlayRef={composerOverlayRef}
          connectionStatus={connectionStatus}
          density={settings.workspace.density}
          draft={draft}
          handleOpenArtifact={handleOpenArtifact}
          isCompact={isCompact}
          isNewThread={isNewThread}
          isResponding={isResponding}
          liveTeamCount={liveTeamCount}
          messageListStyle={messageListStyle}
          modelOptions={modelOptions}
          newThreadSuggestions={newThreadSuggestions}
          onAnswerPendingQuestion={onAnswerPendingQuestion}
          onPlanDecision={onPlanDecision}
          onPlanNotesChange={onPlanNotesChange}
          onBacktrackToMessage={onBacktrackToMessage}
          onDraftChange={onDraftChange}
          onModelChange={onModelChange}
          onProviderChange={onProviderChange}
          onReasoningEffortChange={onReasoningEffortChange}
          onRenameRuntimeSession={onRenameRuntimeSession}
          onResolvePendingApproval={onResolvePendingApproval}
          onRetryConnection={onRetryConnection}
          onSelectBacktrackNavigationMessage={onSelectBacktrackNavigationMessage}
          onStopResponding={onStopResponding}
          onSubmit={onSubmit}
          openSettings={openSettings}
          pendingInteraction={pendingInteraction}
          planActionPending={planActionPending}
          planNotesDraft={planNotesDraft}
          phase={phase}
          providerOptions={providerOptions}
          reasoningEffortDefault={reasoningEffortDefault}
          reasoningEffortError={reasoningEffortError}
          reasoningEffortOptions={reasoningEffortOptions}
          runtimeModelsError={runtimeModelsError}
          runtimeModelsLoading={runtimeModelsLoading}
          selectedModel={selectedModel}
          selectedProvider={selectedProvider}
          selectedReasoningEffort={selectedReasoningEffort}
          selectedThread={selectedThread}
          setArtifactRailManualOpen={setArtifactRailManualOpen}
          setMobileSidebarOpen={setMobileSidebarOpen}
          setViewMode={setViewMode}
          t={t}
          threadStatusLabel={threadStatusLabel}
          threadSubtitle={threadSubtitle}
          trajectoryStore={trajectoryStore}
          transportLabel={transportLabel}
          viewMode={viewMode}
        />
        <WorkspaceRightRailSection
          artifactRailOpen={artifactRailOpen}
          handleOpenArtifact={handleOpenArtifact}
          isNewThread={isNewThread}
          isResponding={isResponding}
          selectedArtifactId={selectedArtifactId}
          selectedThread={selectedThread}
          t={t}
        />
      </div>
      <WorkspaceOverlaysSection
        artifactDialogOpen={artifactDialogOpen}
        backtrackDialog={backtrackDialog}
        modelOptions={modelOptions}
        onBacktrackEditPromptChange={onBacktrackEditPromptChange}
        onBacktrackModeChange={onBacktrackModeChange}
        onBacktrackPrefillChange={onBacktrackPrefillChange}
        onCloseBacktrackDialog={onCloseBacktrackDialog}
        onConfirmBacktrack={onConfirmBacktrack}
        onModelChange={onModelChange}
        onProviderChange={onProviderChange}
        onResetRuntimeClientIdentity={onResetRuntimeClientIdentity}
        providerOptions={providerOptions}
        runtimeClient={runtimeClient}
        runtimeModelsError={runtimeModelsError}
        runtimeModelsLoading={runtimeModelsLoading}
        runtimeSessionsSummary={runtimeSessionsSummary}
        runtimeTeams={runtimeTeams}
        selectedArtifact={selectedArtifact}
        selectedModel={selectedModel}
        selectedProvider={selectedProvider}
        setArtifactDialogOpen={setArtifactDialogOpen}
        setSettingsDialogOpen={setSettingsDialogOpen}
        settingsDialogOpen={settingsDialogOpen}
        settingsSection={settingsSection}
        t={t}
      />
    </div>
  );
}
