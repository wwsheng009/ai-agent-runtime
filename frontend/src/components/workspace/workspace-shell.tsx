// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useEffect, useRef, useState, type CSSProperties } from "react";
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
import {
  WORKSPACE_SIDEBAR_COLLAPSED_WIDTH,
  WORKSPACE_SIDEBAR_WIDTH_DEFAULT_PX,
  clampWorkspaceSidebarWidth,
  resolveWorkspaceSidebarWidthMax,
} from "@/lib/layout/sidebar-width";
import { cn } from "@/lib/utils";

import { WorkspaceMainSection } from "./workspace-shell/main-section";
import { buildNewThreadSuggestions } from "./workspace-shell/new-thread-suggestions";
import { WorkspaceOverlaysSection } from "./workspace-shell/overlays-section";
import { WorkspaceRightRailSection } from "./workspace-shell/right-rail-section";
import { SidebarResizeHandle } from "./workspace-shell/sidebar-resize-handle";
import { WorkspaceSidebarSection } from "./workspace-shell/sidebar-section";
import { useShellViewport } from "./workspace-shell/use-shell-viewport";
import {
  type WorkspaceShellProps,
  type WorkspaceViewMode,
} from "./workspace-shell/types";

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
  selectedWorkspaceDirectory,
  selectedWorkspaceDirectoryId,
  onSelectWorkspaceDirectory,
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
  pendingPermissionMode = "",
  pendingProfileRef = "",
  onPendingPermissionModeChange,
  onPendingPermissionModeConfirmChange,
  onPendingProfileChange,
  imageSubmitFeedback = null,
  isResponding,
  modelOptions,
  phase,
  reasoningEffortDefault,
  reasoningEffortError,
  reasoningEffortOptions,
  runtimeModels = null,
  runtimeModelsError,
  runtimeModelsLoading,
  runtimeSkills = null,
  runtimeSkillsError,
  runtimeSkillsLoading,
  onDraftChange,
  onModelChange,
  onProviderChange,
  onReasoningEffortChange,
  onSelectArtifact,
  onSelectThread,
  onRefreshRuntimeTeams,
  onRefreshRuntimeSessions,
  onResetRuntimeClientIdentity,
  onStopResponding,
  onSubmit,
  pendingInteraction = null,
  parkedTurn = null,
  onResolvePendingApproval,
  onAnswerPendingQuestion,
  planActionPending,
  planNotesDraft,
  plan = null,
  planStatusLabel,
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
  selectedModel,
  selectedProvider,
  selectedReasoningEffort,
  sessionRefreshing,
  earlierLoader,
  trajectoryStore = null,
  trajectoryEarlier,
}: WorkspaceShellProps) {
  const { settings, updateSection } = useAppSettings();
  const { t } = useTranslation("workspace");
  const isNewThread = selectedThread.id === NEW_THREAD_ID;
  const isCompact = settings.workspace.density === "compact";
  const newThreadSuggestions = buildNewThreadSuggestions(t);
  const {
    composerOverlayHeight,
    composerOverlayRef,
    shellRef,
    shellViewportWidth,
  } = useShellViewport(isNewThread);
  const shellGridRef = useRef<HTMLDivElement | null>(null);
  const [artifactDialogOpen, setArtifactDialogOpen] = useState(false);
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  /** 桌面（xl+）左栏收起：整列只剩图标；移动抽屉不受影响（见 workspace-sidebar 的 xl 门控）。 */
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  /** 拖拽提交后先保留本地几何，避免设置持久化异步落盘时把 CSS 变量闪回默认值。 */
  const [sidebarWidthOverride, setSidebarWidthOverride] = useState<
    number | null
  >(null);
  const [settingsDialogOpen, setSettingsDialogOpen] = useState(false);
  const [viewMode, setViewMode] = useState<WorkspaceViewMode>("chat");
  const [settingsSection, setSettingsSection] =
    useState<SettingsSectionId>("appearance");
  const [rightRailManualOpen, setRightRailManualOpen] = useState(
    Boolean(settings.workspace.autoOpenArtifacts),
  );
  // 右侧栏是「条目 / 计划 / 还原 / 会话用量」合并后的单一可折叠面板。
  const rightRailOpen = rightRailManualOpen;
  const rightRailVisible = rightRailOpen;
  // P0-2：右栏宽度状态源（模式 + 视口重算 + 提交持久化）；宽度算术只在 lib/layout/rail-width.ts。
  const [railSurface, setRailSurface] =
    useState<WorkspacePanelSurfaceId | null>(null);
  const railWidth = useRightRailWidth({
    surface: railSurface,
    viewportWidth: shellViewportWidth,
  });
  const reservedRightRailWidth =
    rightRailVisible && !railWidth.overlay ? railWidth.widthPx : 0;
  const sidebarWidthMaxPx = resolveWorkspaceSidebarWidthMax(
    shellViewportWidth,
    reservedRightRailWidth,
  );
  const sidebarWidthPx = clampWorkspaceSidebarWidth(
    sidebarWidthOverride ?? settings.workspace.sidebarWidthPx,
    shellViewportWidth,
    reservedRightRailWidth,
  );

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
  const threadSubtitle = getThreadTopbarSubtitle(
    selectedThread,
    transportLabel,
    {
      needsRestoreWithSession: (sessionId) =>
        t("topbar.subtitle.needsRestoreWithSession", { sessionId }),
      needsRestore: t("topbar.subtitle.needsRestore"),
      viaSource: (source) => t("topbar.subtitle.viaSource", { source }),
      session: (sessionId) => t("topbar.subtitle.session", { sessionId }),
    },
  );

  const liveTeamCount = runtimeTeams.filter(
    (team) => (team.status || "").trim().toLowerCase() === "active",
  ).length;

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
      : `${sidebarWidthPx}px`,
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
          "relative grid h-full min-h-0 grid-cols-1 gap-0",
          rightRailVisible
            ? "xl:grid-cols-[var(--workspace-sidebar-width,16rem)_minmax(0,1fr)_var(--right-rail-width,18rem)]"
            : "xl:grid-cols-[var(--workspace-sidebar-width,16rem)_minmax(0,1fr)]",
        )}
        ref={shellGridRef}
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
          onRefreshRuntimeSessions={onRefreshRuntimeSessions}
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
        {!sidebarCollapsed ? (
          <SidebarResizeHandle
            containerRef={shellGridRef}
            label={t("panels.shell.leftSidebar.resizeHandle")}
            maxWidthPx={sidebarWidthMaxPx}
            onCommit={(widthPx) => {
              setSidebarWidthOverride(widthPx);
              updateSection("workspace", { sidebarWidthPx: widthPx });
            }}
            onReset={() => {
              setSidebarWidthOverride(WORKSPACE_SIDEBAR_WIDTH_DEFAULT_PX);
              updateSection("workspace", {
                sidebarWidthPx: WORKSPACE_SIDEBAR_WIDTH_DEFAULT_PX,
              });
            }}
            reservedRightRailWidth={reservedRightRailWidth}
            value={sidebarWidthPx}
            viewportWidth={shellViewportWidth}
          />
        ) : null}
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
          pendingPermissionMode={pendingPermissionMode}
          pendingProfileRef={pendingProfileRef}
          onPendingPermissionModeChange={onPendingPermissionModeChange}
          onPendingPermissionModeConfirmChange={
            onPendingPermissionModeConfirmChange
          }
          onPendingProfileChange={onPendingProfileChange}
          handleOpenArtifact={handleOpenArtifact}
          isCompact={isCompact}
          isNewThread={isNewThread}
          isResponding={isResponding}
          imageSubmitFeedback={imageSubmitFeedback}
          liveTeamCount={liveTeamCount}
          messageListStyle={messageListStyle}
          modelOptions={modelOptions}
          newThreadSuggestions={newThreadSuggestions}
          runtimeModels={runtimeModels}
          runtimeSkills={runtimeSkills}
          runtimeSkillsError={runtimeSkillsError}
          runtimeSkillsLoading={runtimeSkillsLoading}
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
          onSelectBacktrackNavigationMessage={
            onSelectBacktrackNavigationMessage
          }
          onStopResponding={onStopResponding}
          onSubmit={onSubmit}
          onToggleRightRail={() =>
            setRightRailManualOpen((current) => !current)
          }
          openSettings={openSettings}
          pendingInteraction={pendingInteraction}
          parkedTurn={parkedTurn}
          plan={plan}
          planActionPending={planActionPending}
          planNotesDraft={planNotesDraft}
          planStatusLabel={planStatusLabel}
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
          workspaceDirectories={workspaceDirectories}
          workspaceDirectoriesError={workspaceDirectoriesError}
          workspaceDirectoriesLoading={workspaceDirectoriesLoading}
          selectedWorkspaceDirectoryId={selectedWorkspaceDirectoryId ?? null}
          onSelectWorkspaceDirectory={onSelectWorkspaceDirectory}
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
          isResponding={isResponding}
          onActiveSurfaceChange={setRailSurface}
          onCloseRightRail={() => setRightRailManualOpen(false)}
          railWidth={railWidth}
          rightRailOpen={rightRailOpen}
          selectedArtifactId={selectedArtifactId}
          selectedThread={selectedThread}
          t={t}
          workspacePath={
            isNewThread
              ? selectedWorkspaceDirectory?.path
              : runtimeClient.workspacePath
          }
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
