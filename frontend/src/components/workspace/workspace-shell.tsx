// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useEffect, useRef, useState, type CSSProperties } from "react";
import { Code2Icon, FileSearchIcon, ListTodoIcon, ShieldCheckIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { type WorkspacePanelSurfaceId } from "@/components/workspace/panel-registry";
import { type SettingsSectionId } from "@/components/workspace/settings";
import {
  getThreadStatusLabel,
  getThreadTopbarSubtitle,
  getThreadTransportLabel,
} from "@/components/workspace/workspace-shell-shared";
import { useAppSettings } from "@/core/settings";
import { useRightRailWidth } from "@/hooks/workspace/use-right-rail-width";
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";
import { RAIL_GRID_MIN_VIEWPORT_PX } from "@/lib/layout/rail-width";
import {
  WORKSPACE_SIDEBAR_COLLAPSED_WIDTH,
  WORKSPACE_SIDEBAR_EXPANDED_WIDTH,
} from "@/lib/layout/sidebar-width";
import { cn } from "@/lib/utils";

import { WorkspaceMainSection } from "./workspace-shell/main-section";
import { WorkspaceOverlaysSection } from "./workspace-shell/overlays-section";
import { WorkspaceRightRailSection } from "./workspace-shell/right-rail-section";
import { WorkspaceSidebarSection } from "./workspace-shell/sidebar-section";
import {
  type WorkspaceShellProps,
  type WorkspaceViewMode,
} from "./workspace-shell/types";

/** P0-2：视口宽度兜底读取（无 window 时按 xl 断点处理，避免 NaN 宽度）。 */
function readViewportWidth() {
  if (typeof window === "undefined") {
    return RAIL_GRID_MIN_VIEWPORT_PX;
  }

  return Math.round(window.innerWidth);
}

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
  runtimeSessionUsers,
  workspaceDirectories,
  workspaceDirectoriesError,
  workspaceDirectoriesLoading,
  workspaceDirectoriesRefreshing,
  onAddWorkspaceDirectory,
  onRenameWorkspaceDirectory,
  onRemoveWorkspaceDirectory,
  onRefreshSession,
  onRetryConnection,
  streamStalled,
  onCreateSessionInDirectory,
  onMoveRuntimeSession,
  onRenameRuntimeSession,
  onArchiveRuntimeSession,
  onRestoreRuntimeSession,
  onForkRuntimeSession,
  onDeleteRuntimeSession,
  onStopRuntimeSession,
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
  runtimeModels = null,
  onDraftChange,
  onModelChange,
  onProviderChange,
  onReasoningEffortChange,
  onSelectArtifact,
  onSelectThread,
  onRefreshRuntimeTeams,
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
  branchError = null,
  branchPendingMessageId = null,
  onBranchFromMessage,
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
  sessionRefreshing,
  earlierLoader,
  trajectoryStore = null,
  trajectoryEarlier,
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
  /** 根容器：复用既有 ResizeObserver 监听它，拿到视口宽度的变化时机。 */
  const shellRef = useRef<HTMLDivElement | null>(null);
  const [artifactDialogOpen, setArtifactDialogOpen] = useState(false);
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  /** 桌面（xl+）左栏收起：整列只剩图标；移动抽屉不受影响（见 workspace-sidebar 的 xl 门控）。 */
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [settingsDialogOpen, setSettingsDialogOpen] = useState(false);
  const [viewMode, setViewMode] = useState<WorkspaceViewMode>("chat");
  /** P0-2：视口宽度（右栏 auto 宽度与 `<xl` 降级判定共用）。 */
  const [shellViewportWidth, setShellViewportWidth] = useState(readViewportWidth);
  const [settingsSection, setSettingsSection] =
    useState<SettingsSectionId>("appearance");
  const [rightRailManualOpen, setRightRailManualOpen] = useState(
    Boolean(settings.workspace.autoOpenArtifacts),
  );
  // 右侧栏是「条目 / 计划 / 还原 / 会话用量」合并后的单一可折叠面板。
  const rightRailOpen = !isNewThread && rightRailManualOpen;
  const rightRailVisible = rightRailOpen;
  // P0-2：右栏宽度状态源（模式 + 视口重算 + 提交持久化）；宽度算术只在 lib/layout/rail-width.ts。
  const [railSurface, setRailSurface] = useState<WorkspacePanelSurfaceId | null>(
    null,
  );
  const railWidth = useRightRailWidth({
    surface: railSurface,
    viewportWidth: shellViewportWidth,
  });

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
    viaSource: (source) => t("topbar.subtitle.viaSource", { source }),
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

    // P0-2：同一个 observer 兼听根容器，视口尺寸变化时重算右栏 auto 宽度（不新增监听）。
    const shellNode = shellRef.current;

    const observer = new ResizeObserver(() => {
      const nextHeight = Math.ceil(node.getBoundingClientRect().height);
      setComposerOverlayHeight((currentHeight) =>
        currentHeight === nextHeight ? currentHeight : nextHeight,
      );
      const nextViewportWidth = readViewportWidth();
      setShellViewportWidth((currentWidth) =>
        currentWidth === nextViewportWidth ? currentWidth : nextViewportWidth,
      );
    });

    observer.observe(node);
    if (shellNode) {
      observer.observe(shellNode);
    }

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
    // P0-2 机械搬迁：保留原「autoOpenArtifacts 变化即同步刷新右侧栏开合」语义。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setRightRailManualOpen(settings.workspace.autoOpenArtifacts);
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

  // 网格第一列（左栏）与第三列（右栏）都读 CSS 变量（动态类名会破坏 Tailwind 静态扫描，
  // 运行时只改变量）；右栏关闭时不注入 `--right-rail-width` → 保持两列网格、不占位（回归红线 ①）。
  const shellGridStyle = {
    "--workspace-sidebar-width": sidebarCollapsed
      ? WORKSPACE_SIDEBAR_COLLAPSED_WIDTH
      : WORKSPACE_SIDEBAR_EXPANDED_WIDTH,
    ...(rightRailVisible
      ? { "--right-rail-width": `${railWidth.widthPx}px` }
      : {}),
  } as CSSProperties;

  return (
    <div
      className="h-screen overflow-hidden [background:var(--workspace-shell-bg)] text-foreground"
      ref={shellRef}
    >
      <div
        className={cn(
          "grid h-full min-h-0 grid-cols-1 gap-0",
          rightRailVisible
            ? "xl:grid-cols-[var(--workspace-sidebar-width,16rem)_minmax(0,1fr)_var(--right-rail-width,18rem)]"
            : "xl:grid-cols-[var(--workspace-sidebar-width,16rem)_minmax(0,1fr)]",
        )}
        style={shellGridStyle}
      >
        <WorkspaceSidebarSection
          collapsed={sidebarCollapsed}
          density={settings.workspace.density}
          mobileOpen={mobileSidebarOpen}
          onCollapsedChange={setSidebarCollapsed}
          onAddWorkspaceDirectory={onAddWorkspaceDirectory}
          onCreateSessionInDirectory={onCreateSessionInDirectory}
          onRefreshRuntimeTeams={onRefreshRuntimeTeams}
          onArchiveRuntimeSession={onArchiveRuntimeSession}
          onDeleteRuntimeSession={onDeleteRuntimeSession}
          onForkRuntimeSession={onForkRuntimeSession}
          onRemoveWorkspaceDirectory={onRemoveWorkspaceDirectory}
          onMoveRuntimeSession={onMoveRuntimeSession}
          onRenameRuntimeSession={onRenameRuntimeSession}
          onRestoreRuntimeSession={onRestoreRuntimeSession}
          onStopRuntimeSession={onStopRuntimeSession}
          onRenameWorkspaceDirectory={onRenameWorkspaceDirectory}
          onSelectThread={onSelectThread}
          openSettings={openSettings}
          runtimeSessionUsers={runtimeSessionUsers}
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
          backtrackError={backtrackError}
          backtrackNavigationActive={backtrackNavigationActive}
          backtrackNotice={backtrackNotice}
          backtrackPendingMessageId={backtrackPendingMessageId}
          backtrackSelectedMessageId={backtrackSelectedMessageId}
          canBacktrack={canBacktrack}
          branchError={branchError}
          branchPendingMessageId={branchPendingMessageId}
          composerAttachments={composerAttachments}
          composerOverlayHeight={composerOverlayHeight}
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
          runtimeModels={runtimeModels}
          onAnswerPendingQuestion={onAnswerPendingQuestion}
          onPlanDecision={onPlanDecision}
          onPlanNotesChange={onPlanNotesChange}
          onBacktrackToMessage={onBacktrackToMessage}
          onBranchFromMessage={onBranchFromMessage}
          onDraftChange={onDraftChange}
          onModelChange={onModelChange}
          onProviderChange={onProviderChange}
          onReasoningEffortChange={onReasoningEffortChange}
          onRenameRuntimeSession={onRenameRuntimeSession}
          onResolvePendingApproval={onResolvePendingApproval}
          onRefreshSession={onRefreshSession}
          onRetryConnection={onRetryConnection}
          streamStalled={streamStalled}
          onSelectBacktrackNavigationMessage={onSelectBacktrackNavigationMessage}
          onStopResponding={onStopResponding}
          onSubmit={onSubmit}
          onToggleRightRail={() => setRightRailManualOpen((current) => !current)}
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
          earlierLoader={earlierLoader}
          selectedModel={selectedModel}
          selectedProvider={selectedProvider}
          selectedReasoningEffort={selectedReasoningEffort}
          selectedThread={selectedThread}
          rightRailOpen={rightRailOpen}
          sessionRefreshing={sessionRefreshing}
          setMobileSidebarOpen={setMobileSidebarOpen}
          setViewMode={setViewMode}
          t={t}
          threadStatusLabel={threadStatusLabel}
          threadSubtitle={threadSubtitle}
          trajectoryStore={trajectoryStore}
          trajectoryEarlier={trajectoryEarlier}
          transportLabel={transportLabel}
          viewMode={viewMode}
        />
        <WorkspaceRightRailSection
          handleOpenArtifact={handleOpenArtifact}
          isNewThread={isNewThread}
          isResponding={isResponding}
          onActiveSurfaceChange={setRailSurface}
          onCloseRightRail={() => setRightRailManualOpen(false)}
          railWidth={railWidth}
          rightRailOpen={rightRailOpen}
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
